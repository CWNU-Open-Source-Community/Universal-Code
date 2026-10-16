package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/executionauth"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/mcpoauth"
	"cyberagent-workbench/internal/redact"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
)

type CredentialReader interface {
	Get(context.Context, string) (string, bool, error)
}

type TransportFactory func(context.Context, ServerDescriptor, string) (clientTransport, error)

type ManagerOptions struct {
	InputBroker      *InputBroker
	HTTPClient       *http.Client
	TransportFactory TransportFactory
	Now              func() time.Time
	NativeSources    NativeSourceResolver
	WorkspaceRoot    WorkspaceRootProvider
	// ComputerUseExecutable is supplied only by a host that implements the
	// reserved Computer Use stdio entrypoint. Empty disables the built-in service.
	ComputerUseExecutable string
}

type Manager struct {
	inputs                *InputBroker
	store                 ClientStore
	credentials           CredentialReader
	factory               TransportFactory
	httpClient            *http.Client
	nativeSources         NativeSourceResolver
	workspaceRoot         WorkspaceRootProvider
	computerUseExecutable string
	now                   func() time.Time
	locksMu               sync.Mutex
	locks                 map[string]*sync.Mutex
	sessionsMu            sync.Mutex
	sessions              map[catalogSessionKey]*catalogSession
	closed                bool
}

// ReconcileStartup deterministically fences discovery whose durable lease has
// expired. A live lease may belong to another process sharing the same store,
// so it must not be mistaken for work interrupted by this process starting.
// Interrupted refreshes retain the last catalog and enabled state; unavailable
// health keeps calls fenced until discovery succeeds again.
func (m *Manager) ReconcileStartup(ctx context.Context) (int, error) {
	if m == nil || m.store == nil {
		return 0, errors.New("MCP Client manager is required")
	}
	records, err := m.store.ListRecoverableMCPClientServers(ctx, MaxClientServers)
	if err != nil {
		return 0, err
	}
	reconciled := 0
	for _, record := range records {
		lock := m.lock(record.Descriptor.ID)
		lock.Lock()
		current, loadErr := m.store.GetMCPClientServer(ctx, record.Descriptor.ID)
		if loadErr != nil {
			lock.Unlock()
			return reconciled, loadErr
		}
		if current.Health != HealthConnecting || current.DiscoveryLeaseExpiresAt == nil ||
			current.DiscoveryLeaseExpiresAt.After(m.now().UTC()) {
			lock.Unlock()
			continue
		}
		before := current.Generation
		current.Health = HealthUnavailable
		current.HealthMessage = "discovery interrupted by process restart"
		current.DiscoveryLeaseID = ""
		current.DiscoveryLeaseExpiresAt = nil
		current.Generation++
		current.UpdatedAt = m.now().UTC()
		_, updateErr := m.store.UpdateMCPClientServer(ctx, current, before)
		lock.Unlock()
		if updateErr != nil {
			return reconciled, updateErr
		}
		reconciled++
	}
	return reconciled, nil
}

func NewClientManager(store ClientStore, credentials CredentialReader,
	options ManagerOptions,
) (*Manager, error) {
	if store == nil {
		return nil, errors.New("MCP client manager requires a durable store")
	}
	if options.ComputerUseExecutable != "" && !filepath.IsAbs(options.ComputerUseExecutable) {
		return nil, errors.New("Computer Use host executable must be absolute")
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	factory := options.TransportFactory
	if factory == nil {
		factory = func(_ context.Context, descriptor ServerDescriptor, bearer string) (clientTransport, error) {
			if descriptor.Transport == TransportStdio {
				return newStdioClientTransport(descriptor)
			}
			return newRemoteClientTransport(descriptor, bearer, options.HTTPClient)
		}
	}
	return &Manager{inputs: options.InputBroker, store: store, credentials: credentials, factory: factory, httpClient: options.HTTPClient, nativeSources: options.NativeSources, workspaceRoot: options.WorkspaceRoot, computerUseExecutable: options.ComputerUseExecutable, now: now,
		locks: make(map[string]*sync.Mutex), sessions: make(map[catalogSessionKey]*catalogSession)}, nil
}

func (m *Manager) Stage(ctx context.Context, descriptor ServerDescriptor) (ServerRecord, bool, error) {
	if descriptor.Source.Kind == "builtin" || strings.HasPrefix(descriptor.ID, computerUseServerPrefix) {
		return ServerRecord{}, false, apperror.New(apperror.CodePolicyDenied, "built-in MCP registrations are owned by the host")
	}
	if err := descriptor.Validate(); err != nil {
		return ServerRecord{}, false, apperror.Wrap(apperror.CodeInvalidArgument,
			"MCP server descriptor is invalid", err)
	}
	if descriptor.NativeSource != nil {
		if m.nativeSources == nil {
			return ServerRecord{}, false, errors.New("native MCP source resolver is unavailable")
		}
		if err := m.nativeSources.Check(ctx, *descriptor.NativeSource, descriptor.RunID); err != nil {
			return ServerRecord{}, false, err
		}
	}
	now := m.now().UTC()
	record := ServerRecord{ProtocolVersion: ServerRecordProtocolVersion,
		Descriptor: descriptor, DescriptorFingerprint: descriptor.Fingerprint(),
		State: TrustStaged, Health: HealthUnknown, Generation: 1,
		CreatedAt: now, UpdatedAt: now}
	if err := record.Validate(); err != nil {
		return ServerRecord{}, false, err
	}
	return m.store.CreateMCPClientServer(ctx, record)
}

func (m *Manager) Review(ctx context.Context, serverID string,
	request ReviewRequest,
) (ServerRecord, error) {
	lock := m.lock(serverID)
	lock.Lock()
	defer lock.Unlock()
	record, err := m.store.GetMCPClientServer(ctx, strings.TrimSpace(serverID))
	if err != nil {
		return ServerRecord{}, err
	}
	if request.ExpectedDescriptorFingerprint != record.DescriptorFingerprint ||
		!validClientText(request.ReviewedBy, 256, false) {
		return ServerRecord{}, apperror.New(apperror.CodeConflict,
			"MCP review does not match the current descriptor")
	}
	now := m.now().UTC()
	switch request.Action {
	case ReviewApproveDiscovery:
		if record.State == TrustRevoked || record.State == TrustEnabled {
			return ServerRecord{}, apperror.New(apperror.CodeFailedPrecondition,
				"MCP server cannot enter discovery review from its current state")
		}
		record.State = TrustDiscoveryApproved
		record.ApprovedCapabilityFingerprint = ""
		record.Capabilities = CapabilitySnapshot{}
		record.Health = HealthUnknown
		record.HealthMessage = ""
	case ReviewEnableCapabilities:
		if record.State != TrustCapabilitiesPending || record.Capabilities.Fingerprint == "" ||
			request.ExpectedCapabilityFingerprint != record.Capabilities.Fingerprint {
			return ServerRecord{}, apperror.New(apperror.CodeConflict,
				"MCP capability review does not match the discovered snapshot")
		}
		record.State = TrustEnabled
		record.ApprovedCapabilityFingerprint = record.Capabilities.Fingerprint
		record.Health = HealthHealthy
		record.HealthMessage = ""
	case ReviewDisable:
		if record.State == TrustRevoked {
			return ServerRecord{}, apperror.New(apperror.CodeFailedPrecondition,
				"revoked MCP server cannot be disabled")
		}
		record.State = TrustDisabled
		record.Health = HealthUnknown
		record.HealthMessage = "disabled by operator"
	case ReviewRevoke:
		record.State = TrustRevoked
		record.Health = HealthUnknown
		record.HealthMessage = "revoked by operator"
		record.ApprovedCapabilityFingerprint = ""
	default:
		return ServerRecord{}, apperror.New(apperror.CodeInvalidArgument,
			"MCP review action is invalid")
	}
	record.ReviewedBy = strings.TrimSpace(request.ReviewedBy)
	record.ReviewedAt = &now
	record.UpdatedAt = now
	record.Generation++
	if err := record.Validate(); err != nil {
		return ServerRecord{}, err
	}
	updated, err := m.store.UpdateMCPClientServer(ctx, record, record.Generation-1)
	if err == nil && (record.State == TrustDisabled || record.State == TrustRevoked) {
		m.closeSessions(func(key catalogSessionKey) bool { return key.server == record.Descriptor.ID })
	}
	return updated, err
}

func (m *Manager) Refresh(ctx context.Context, serverID string) (ServerRecord, error) {
	lock := m.lock(serverID)
	lock.Lock()
	locked := true
	defer func() {
		if locked {
			lock.Unlock()
		}
	}()
	record, err := m.store.GetMCPClientServer(ctx, strings.TrimSpace(serverID))
	if err != nil {
		return ServerRecord{}, err
	}
	if record.Descriptor.NativeSource != nil {
		// Native discovery has actual send guards and the existing durable
		// generation/lease CAS; operator disable must remain live in flight.
		lock.Unlock()
		locked = false
	}
	return m.refreshLocked(ctx, record)
}

func (m *Manager) refreshLocked(ctx context.Context, record ServerRecord) (ServerRecord, error) {
	if record.State != TrustDiscoveryApproved && record.State != TrustCapabilitiesPending &&
		record.State != TrustEnabled && record.State != TrustQuarantined {
		return ServerRecord{}, apperror.New(apperror.CodeFailedPrecondition,
			"MCP server discovery is not operator-approved")
	}
	if IsComputerUseServer(record.Descriptor) {
		return m.refreshComputerUse(ctx, record)
	}
	previousGeneration := record.Generation
	startedAt := m.now().UTC()
	record.Health = HealthConnecting
	record.HealthMessage = ""
	record.DiscoveryLeaseID = idgen.New("mcp-discovery")
	leaseExpiresAt := startedAt.Add(time.Duration(record.Descriptor.CallTimeoutMillis)*time.Millisecond +
		15*time.Second)
	record.DiscoveryLeaseExpiresAt = &leaseExpiresAt
	record.UpdatedAt = startedAt
	record.Generation++
	record, err := m.store.UpdateMCPClientServer(ctx, record, previousGeneration)
	if err != nil {
		return ServerRecord{}, err
	}
	callCtx, cancel := context.WithTimeout(ctx,
		time.Duration(record.Descriptor.CallTimeoutMillis)*time.Millisecond)
	if record.Descriptor.NativeSource != nil {
		var capabilities CapabilitySnapshot
		capabilities, err = m.discoverNativeSource(callCtx, record)
		if err == nil {
			record.Capabilities = capabilities
		}
	} else {
		var client *Client
		client, err = m.connect(callCtx, record.Descriptor)
		if err == nil {
			defer client.Close()
			var roots *clientWorkspaceRoots
			roots, err = m.prepareWorkspaceRoots(callCtx, record, record.Descriptor.RunID, func(context.Context) error { return callCtx.Err() })
			if err == nil {
				client.setWorkspaceRoots(roots)
				var capabilities CapabilitySnapshot
				capabilities, err = client.Discover(callCtx, m.now())
				if err == nil {
					record.Capabilities = capabilities
				}
			}
		}
	}
	cancel()
	previousGeneration = record.Generation
	record.Generation++
	record.UpdatedAt = m.now().UTC()
	record.DiscoveryLeaseID = ""
	record.DiscoveryLeaseExpiresAt = nil
	if err != nil {
		record.Health = HealthUnavailable
		record.HealthMessage = boundedClientMessage(err.Error())
		updated, updateErr := m.store.UpdateMCPClientServer(context.WithoutCancel(ctx), record,
			previousGeneration)
		if updateErr != nil {
			return ServerRecord{}, errors.Join(err, updateErr)
		}
		return updated, apperror.Wrap(apperror.CodeUnavailable,
			"MCP server discovery failed", errors.New(boundedClientMessage(err.Error())))
	}
	if record.ApprovedCapabilityFingerprint == "" {
		record.State = TrustCapabilitiesPending
		record.Health = HealthHealthy
		record.HealthMessage = "capability review required"
	} else {
		record.ApprovedCapabilityFingerprint = record.Capabilities.Fingerprint
		record.State = TrustEnabled
		record.Health = HealthHealthy
		record.HealthMessage = ""
	}
	return m.store.UpdateMCPClientServer(ctx, record, previousGeneration)
}

func (m *Manager) connect(ctx context.Context, descriptor ServerDescriptor) (*Client, error) {
	if len(descriptor.EnvCredentialRefs) != 0 {
		environment, err := m.readManualEnvironment(ctx, descriptor)
		if err != nil {
			return nil, err
		}
		launch, err := resolveManualLaunch(descriptor, []string{legacyClientProtocolVersion, "2024-11-05", "2025-03-26", "2025-11-25", preferredClientProtocolVersion}, environment)
		if err != nil {
			return nil, err
		}
		recheck := func(ctx context.Context) error { return m.recheckManualEnvironment(ctx, descriptor, launch) }
		transport, _, err := newLaunchClient(launch, m.httpClient, recheck, func(ctx context.Context, _ *jsonrpc.Request, _ int64) error { return recheck(ctx) })
		if err != nil {
			return nil, err
		}
		secrets := make([]string, 0, len(environment))
		for _, value := range environment {
			secrets = append(secrets, value)
		}
		return newClient(transport, descriptor, secrets...), nil
	}
	bearer := ""
	if descriptor.CredentialRef != "" {
		if m.credentials == nil {
			return nil, errors.New("MCP credential manager is unavailable")
		}
		value, found, err := m.credentials.Get(ctx, descriptor.CredentialRef)
		if err != nil {
			return nil, err
		}
		if !found || value == "" {
			return nil, errors.New("configured MCP credential is unavailable")
		}
		bearer = value
		if mcpoauth.IsEnvelope(value) {
			owned, ok := m.credentials.(credential.Store)
			if !ok {
				return nil, mcpoauth.ErrOAuth
			}
			bearer, err = mcpoauth.ResolveRevision(ctx, owned, mcpoauth.Binding{Ref: descriptor.CredentialRef, Fingerprint: descriptor.Fingerprint(), Target: descriptor.Target}, m.httpClient, mcpoauth.RevisionIdentity(value))
			if err != nil {
				return nil, err
			}
		}
	}
	transport, err := m.factory(ctx, descriptor, bearer)
	if err != nil {
		return nil, err
	}
	return newClient(transport, descriptor, bearer), nil
}

type ScopedServerCapability struct {
	Resources             []RemoteResource         `json:"resources,omitempty"`
	ResourceTemplates     []RemoteResourceTemplate `json:"resource_templates,omitempty"`
	Prompts               []RemotePrompt           `json:"prompts,omitempty"`
	ServerID              string                   `json:"server_id"`
	Name                  string                   `json:"name"`
	CapabilityFingerprint string                   `json:"capability_fingerprint"`
	Tools                 []RemoteTool             `json:"tools"`
	// Host-only review identity; never accepted from a model tool payload.
	DescriptorFingerprint  string `json:"-"`
	RegistrationGeneration int64  `json:"-"`
}

type ScopedCapabilities struct {
	ProtocolVersion string                   `json:"protocol_version"`
	Generation      string                   `json:"generation"`
	Servers         []ScopedServerCapability `json:"servers"`
}

func (m *Manager) Capabilities(ctx context.Context, runID, workspaceID string) (
	ScopedCapabilities, error,
) {
	if !validClientIdentity(runID) || !validClientIdentity(workspaceID) {
		return ScopedCapabilities{}, errors.New("MCP capability scope is invalid")
	}
	if err := m.EnsureComputerUse(ctx, workspaceID); err != nil {
		return ScopedCapabilities{}, err
	}
	records, err := m.store.ListMCPClientServers(ctx, runID, workspaceID, MaxClientServers)
	if err != nil {
		return ScopedCapabilities{}, err
	}
	result := ScopedCapabilities{ProtocolVersion: ClientProtocolVersion}
	for _, record := range records {
		if IsComputerUseServer(record.Descriptor) && m.computerUseExecutable == "" {
			continue
		}
		if record.State != TrustEnabled || record.Health != HealthHealthy ||
			record.ApprovedCapabilityFingerprint != record.Capabilities.Fingerprint ||
			!scopeMatches(record.Descriptor, runID, workspaceID) {
			continue
		}
		if source := record.Descriptor.NativeSource; source != nil {
			if m.nativeSources == nil {
				continue
			}
			if err := m.nativeSources.Check(ctx, *source, runID); err != nil {
				code := apperror.CodeOf(err)
				if code != apperror.CodePolicyDenied && code != apperror.CodeNotFound {
					return ScopedCapabilities{}, err
				}
				continue
			}
		}
		result.Servers = append(result.Servers, ScopedServerCapability{
			ServerID: record.Descriptor.ID, Name: record.Descriptor.Name,
			CapabilityFingerprint: record.Capabilities.Fingerprint,
			DescriptorFingerprint: record.DescriptorFingerprint, RegistrationGeneration: record.Generation,
			Resources: slices.Clone(record.Capabilities.Resources), ResourceTemplates: slices.Clone(record.Capabilities.ResourceTemplates), Prompts: slices.Clone(record.Capabilities.Prompts),
			Tools: slices.Clone(record.Capabilities.Tools)})
	}
	sort.Slice(result.Servers, func(i, j int) bool { return result.Servers[i].ServerID < result.Servers[j].ServerID })
	raw, _ := json.Marshal(result.Servers)
	digest := sha256.Sum256(append([]byte(ClientProtocolVersion+"\x00"+runID+"\x00"+workspaceID+"\x00"), raw...))
	result.Generation = hex.EncodeToString(digest[:])
	return result, nil
}

type InvokeRequest struct {
	catalogCheck          func(context.Context) error
	Method                string
	RunID                 string
	WorkspaceID           string
	ServerID              string
	ToolName              string
	CapabilityFingerprint string
	Arguments             json.RawMessage
	// Host-only runtime binding. This is never decoded from a tool payload.
	OperationID string                          `json:"-"`
	Subject     executionauth.SubjectRef        `json:"-"`
	Authorizer  *executionauth.PolicyAuthorizer `json:"-"`
}

func (m *Manager) Invoke(ctx context.Context, request InvokeRequest) (result ClientCallResult,
	err error,
) {
	started := m.now().UTC()
	audit := CallAudit{ProtocolVersion: CallAuditProtocolVersion, ID: idgen.New("mcp-call"),
		RunID: request.RunID, WorkspaceID: request.WorkspaceID, ServerID: request.ServerID,
		ToolName: request.ToolName, CapabilityFingerprint: request.CapabilityFingerprint,
		ArgumentsSHA256: digestBytes(request.Arguments), Status: "failed", StartedAt: started}
	defer func() {
		audit.CompletedAt = m.now().UTC()
		audit.ResultBytes = result.Bytes
		audit.Truncated = result.Truncated
		if err == nil {
			audit.Status = "completed"
			if result.IsError {
				audit.Status = "failed"
				audit.ErrorCode = "remote_tool_error"
			}
		} else if errors.Is(err, context.Canceled) {
			audit.Status, audit.ErrorCode = "cancelled", "cancelled"
		} else if errors.Is(err, context.DeadlineExceeded) {
			audit.Status, audit.ErrorCode = "timed_out", "deadline_exceeded"
		} else {
			audit.ErrorCode = string(apperror.CodeOf(apperror.Normalize(err)))
		}
		if receipt, found := InvocationReceipt(err); found {
			audit.ErrorCode = string(receipt.State)
		}
		if audit.Validate() == nil {
			_ = m.store.RecordMCPClientCall(context.WithoutCancel(ctx), audit)
		}
	}()
	if !validClientIdentity(request.RunID) || !validClientIdentity(request.WorkspaceID) ||
		!validClientIdentity(request.ServerID) || !validRemoteName(request.ToolName) ||
		!validClientDigest(request.CapabilityFingerprint) || len(request.Arguments) == 0 ||
		len(request.Arguments) > MaxClientArgumentsBytes || !json.Valid(request.Arguments) {
		return ClientCallResult{}, apperror.New(apperror.CodeInvalidArgument,
			"MCP tool invocation is invalid")
	}
	lock := m.lock(request.ServerID)
	lock.Lock()
	locked := true
	defer func() {
		if locked {
			lock.Unlock()
		}
	}()
	record, err := m.store.GetMCPClientServer(ctx, request.ServerID)
	if err != nil {
		return ClientCallResult{}, err
	}
	if record.State != TrustEnabled || record.Health != HealthHealthy ||
		!scopeMatches(record.Descriptor, request.RunID, request.WorkspaceID) {
		return ClientCallResult{}, apperror.New(apperror.CodePolicyDenied,
			"MCP server is not enabled for the exact Run/Workspace capability scope")
	}
	if record.Capabilities.Fingerprint != request.CapabilityFingerprint ||
		record.ApprovedCapabilityFingerprint != request.CapabilityFingerprint {
		return ClientCallResult{}, staleCatalogError()
	}
	if _, err := ValidateInvocation(record.Capabilities, request.Method, request.ToolName, request.Arguments); err != nil {
		return ClientCallResult{}, apperror.Wrap(apperror.CodeInvalidArgument, "MCP operation does not match the approved catalog", err)
	}
	if request.Method != "" && request.Authorizer == nil {
		return ClientCallResult{}, apperror.New(apperror.CodePolicyDenied, "MCP content access requires common host authority")
	}
	if request.Authorizer != nil {
		// Do not hold the review lock during transport. Operator disable/revoke
		// must be observable by the actual connect/send guards while in flight.
		lock.Unlock()
		locked = false
		return m.invokeResolved(ctx, request, record)
	}
	if record.Descriptor.NativeSource != nil || IsComputerUseServer(record.Descriptor) {
		return ClientCallResult{}, apperror.New(apperror.CodePolicyDenied, "native MCP invocation requires common host authority")
	}
	callCtx, cancel := context.WithTimeout(ctx,
		time.Duration(record.Descriptor.CallTimeoutMillis)*time.Millisecond)
	defer cancel()
	client, err := m.connect(callCtx, record.Descriptor)
	if err != nil {
		return ClientCallResult{}, m.markInvocationUnavailable(ctx, record,
			"connection failed during invocation", err)
	}
	defer client.Close()
	current, err := client.Discover(callCtx, m.now())
	if err != nil {
		return ClientCallResult{}, m.markInvocationUnavailable(ctx, record,
			"capability verification failed during invocation", err)
	}
	latest, err := m.verifyInvocationSnapshot(callCtx, request, record, current)
	if err != nil {
		return ClientCallResult{}, err
	}
	result, err = client.CallTool(callCtx, request.ToolName, request.Arguments,
		record.Descriptor.MaxResultBytes)
	if err != nil {
		return ClientCallResult{}, m.markInvocationUnavailable(ctx, latest,
			"tool call failed during invocation", err)
	}
	return result, nil
}

func (m *Manager) verifyInvocationSnapshot(ctx context.Context, request InvokeRequest, record ServerRecord, current CapabilitySnapshot) (ServerRecord, error) {
	if current.Fingerprint != record.ApprovedCapabilityFingerprint {
		previous := record.Generation
		record.Capabilities = current
		record.ApprovedCapabilityFingerprint = current.Fingerprint
		record.Health = HealthHealthy
		record.HealthMessage = ""
		record.Generation++
		record.UpdatedAt = m.now().UTC()
		_, updateErr := m.store.UpdateMCPClientServer(context.WithoutCancel(ctx), record, previous)
		return ServerRecord{}, errors.Join(staleCatalogError(), updateErr)
	}
	latest, err := m.store.GetMCPClientServer(ctx, request.ServerID)
	if err != nil {
		return ServerRecord{}, err
	}
	if latest.Generation != record.Generation || latest.State != TrustEnabled ||
		latest.Health != HealthHealthy ||
		latest.ApprovedCapabilityFingerprint != request.CapabilityFingerprint ||
		latest.Capabilities.Fingerprint != request.CapabilityFingerprint ||
		!scopeMatches(latest.Descriptor, request.RunID, request.WorkspaceID) {
		return ServerRecord{}, apperror.New(apperror.CodePolicyDenied,
			"MCP server authorization changed during capability verification")
	}
	return latest, nil
}

func (m *Manager) markInvocationUnavailable(ctx context.Context, record ServerRecord,
	message string, cause error,
) error {
	if cause == nil || errors.Is(cause, context.Canceled) {
		return cause
	}
	previous := record.Generation
	record.Health = HealthUnavailable
	record.HealthMessage = message
	record.DiscoveryLeaseID = ""
	record.DiscoveryLeaseExpiresAt = nil
	record.Generation++
	record.UpdatedAt = m.now().UTC()
	updateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if _, err := m.store.UpdateMCPClientServer(updateCtx, record, previous); err != nil {
		return errors.Join(cause, catalogStoreError(err))
	}
	return cause
}

func validateArgumentsAgainstSchema(schemaRaw, arguments json.RawMessage) error {
	compiler := jsonschema.NewCompiler()
	compiler.Draft = jsonschema.Draft2020
	compiler.LoadURL = func(string) (io.ReadCloser, error) {
		return nil, errors.New("external MCP schema resources are forbidden")
	}
	if err := compiler.AddResource("mcp-input.json", bytes.NewReader(schemaRaw)); err != nil {
		return fmt.Errorf("load schema: %w", err)
	}
	schema, err := compiler.Compile("mcp-input.json")
	if err != nil {
		return fmt.Errorf("compile schema: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := schema.Validate(value); err != nil {
		return err
	}
	return nil
}

func scopeMatches(descriptor ServerDescriptor, runID, workspaceID string) bool {
	if descriptor.WorkspaceID != workspaceID {
		return false
	}
	return descriptor.Scope == ScopeWorkspace || descriptor.RunID == runID
}

func findRemoteTool(values []RemoteTool, name string) (RemoteTool, bool) {
	for _, value := range values {
		if value.Name == name {
			return value, true
		}
	}
	return RemoteTool{}, false
}

func (m *Manager) lock(id string) *sync.Mutex {
	m.locksMu.Lock()
	defer m.locksMu.Unlock()
	value := m.locks[id]
	if value == nil {
		value = &sync.Mutex{}
		m.locks[id] = value
	}
	return value
}

func digestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func boundedClientMessage(value string) string {
	value = strings.TrimSpace(redact.String(strings.ToValidUTF8(value, "?")))
	if len([]byte(value)) > 2048 {
		value = truncateClientUTF8(value, 2048)
	}
	if value == "" {
		return "MCP operation failed"
	}
	return value
}
