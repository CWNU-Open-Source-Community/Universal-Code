package mcp

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/toolcontract"
)

const RuntimeAdapterID = "mcp-sdk"

// Host adapter revision, not a new persisted or wire protocol identifier.
const RuntimeAdapterRevision = "1"

// InvocationError preserves dispatch evidence through the existing error and
// audit paths. A received error is not success; an unknown outcome is not retryable.
type InvocationError struct {
	Receipt toolcontract.Receipt
	cause   error
}

func (e *InvocationError) Error() string {
	if e.Receipt.State != toolcontract.ReceiptNotDispatched {
		return fmt.Sprintf("MCP call was dispatched (%s, operation %s); inspect existing evidence and do not automatically repeat the action", e.Receipt.State, e.Receipt.OperationID)
	}
	return fmt.Sprintf("MCP tools/call operation %s was not dispatched; connection or discovery may have occurred", e.Receipt.OperationID)
}
func (e *InvocationError) Unwrap() error { return e.cause }

func NewInvocationError(receipt toolcontract.Receipt, cause error) error {
	if cause == nil {
		return nil
	}
	if receipt.Validate() != nil {
		return errors.New("MCP failure receipt is invalid")
	}
	return &InvocationError{Receipt: receipt, cause: cause}
}
func InvocationReceipt(err error) (toolcontract.Receipt, bool) {
	var failure *InvocationError
	if errors.As(err, &failure) && failure.Receipt.Validate() == nil {
		return failure.Receipt, true
	}
	return toolcontract.Receipt{}, false
}

type runtimeAuthorityError struct{ cause error }

func (e *runtimeAuthorityError) Error() string {
	return "MCP host operation authority is no longer current"
}
func (e *runtimeAuthorityError) Unwrap() error { return e.cause }
func runtimeAuthorityFailure(err error) error {
	if err == nil {
		return nil
	}
	return &runtimeAuthorityError{cause: err}
}

func (m *Manager) runtimeInvocationFailure(ctx context.Context, record ServerRecord, message string, cause error) error {
	var stopped *runtimeAuthorityError
	if errors.As(cause, &stopped) {
		// A Run revocation/lease loss is not a server outage. In particular it
		// must not disable a workspace server shared by another current Run.
		return cause
	}
	return m.markInvocationUnavailable(ctx, record, message, cause)
}

// Legacy descriptors keep their own codec and exact historical protocol. They
// are not relabeled Agent Plugins or expanded using portable placeholder rules.
func (m *Manager) resolveLegacyRuntime(ctx context.Context, record ServerRecord, key []byte) (toolcontract.ResolvedLaunch, string, error) {
	d := record.Descriptor
	if d.Validate() != nil {
		return toolcontract.ResolvedLaunch{}, "", errors.New("invalid persisted MCP descriptor")
	}
	environment, err := m.readManualEnvironment(ctx, d)
	if err != nil {
		return toolcontract.ResolvedLaunch{}, "", err
	}
	launch, err := resolveManualLaunch(d, []string{record.Capabilities.ProtocolVersion}, environment)
	if err != nil {
		return launch, "", err
	}
	launch.InstanceID = record.DescriptorFingerprint + "-" + fmt.Sprint(record.Generation)
	bearer := ""
	if d.Transport == TransportStreamableHTTP {
		if d.CredentialRef != "" {
			if m.credentials == nil {
				return launch, "", errors.New("MCP credential reader unavailable")
			}
			value, found, err := m.credentials.Get(ctx, d.CredentialRef)
			if err != nil || !found || value == "" {
				return launch, "", errors.New("MCP credential unavailable")
			}
			bearer = value
			launch.HTTP.Credential = &toolcontract.CredentialRef{ID: d.CredentialRef, Revision: runtimeCredentialRevision(key, value)}
		}
	}
	return launch, bearer, launch.Validate()
}

func runtimeCredentialRevision(key []byte, value string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("mcp.runtime.credential\x00" + value))
	return hex.EncodeToString(mac.Sum(nil))
}

func runtimeOperation(id string, kind toolcontract.OperationKind, launch toolcontract.ResolvedLaunch, input string) toolcontract.Operation {
	target := toolcontract.Target{Kind: "endpoint"}
	if launch.HTTP != nil {
		target.Locator = launch.HTTP.Endpoint
	}
	effects := []toolcontract.Effect{toolcontract.EffectPublicNetwork, toolcontract.EffectUnknown}
	if launch.Stdio != nil {
		target = toolcontract.Target{Kind: "process", Locator: launch.Stdio.Command}
		effects = []toolcontract.Effect{toolcontract.EffectProcess, toolcontract.EffectUnknown}
	}
	return toolcontract.Operation{ID: id, Kind: kind, ToolID: "mcp_tool_call", Component: launch.Component,
		AdapterID: RuntimeAdapterID, AdapterRevision: RuntimeAdapterRevision, InputFingerprint: input, Targets: []toolcontract.Target{target}, Effects: effects}
}

func (m *Manager) invokeResolved(ctx context.Context, request InvokeRequest, record ServerRecord) (result ClientCallResult, err error) {
	receipt := toolcontract.Receipt{OperationID: request.OperationID, State: toolcontract.ReceiptNotDispatched}
	defer func() {
		if err != nil && receipt.Validate() == nil {
			if receipt.ErrorCode == "" {
				receipt.ErrorCode = string(apperror.CodeOf(apperror.Normalize(err)))
			}
			err = &InvocationError{Receipt: receipt, cause: err}
		}
	}()
	if request.Subject.Validate() != nil || request.Subject.RunID != request.RunID || !validClientIdentity(request.OperationID) {
		return result, apperror.New(apperror.CodeInvalidArgument, "MCP invocation requires a host operation and subject")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(record.Descriptor.CallTimeoutMillis)*time.Millisecond)
	defer cancel()
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		return result, err
	}
	var launch toolcontract.ResolvedLaunch
	var bearer string
	var secrets []string
	var recheckSource func(context.Context) error
	if source := record.Descriptor.NativeSource; source != nil {
		if m.nativeSources == nil {
			return result, errors.New("native MCP source resolver is unavailable")
		}
		var closeSource func()
		launch, secrets, recheckSource, closeSource, err = m.nativeSources.Resolve(ctx, *source, request.RunID)
		if closeSource != nil {
			defer closeSource()
		}
		if err != nil {
			return result, err
		}
		if recheckSource == nil || closeSource == nil || launch.Component != source.Component ||
			launch.Transport != toolcontract.Transport(record.Descriptor.Transport) || !slices.Contains(launch.ProtocolVersions, record.Capabilities.ProtocolVersion) {
			return result, errors.New("native MCP source does not match the reviewed registration")
		}
		launch.ProtocolVersions = []string{record.Capabilities.ProtocolVersion}
	} else {
		launch, bearer, err = m.resolveLegacyRuntime(ctx, record, key)
		if err != nil {
			return result, err
		}
		if bearer != "" {
			secrets = []string{bearer}
		}
		if launch.Stdio != nil {
			for name := range record.Descriptor.EnvCredentialRefs {
				secrets = append(secrets, launch.Stdio.Env[manualEnvironmentKey(name)])
			}
		}
	}
	credential := func(ctx context.Context, ref toolcontract.CredentialRef) (string, error) {
		if m.credentials == nil || launch.HTTP == nil || launch.HTTP.Credential == nil || ref != *launch.HTTP.Credential {
			return "", runtimeAuthorityFailure(errors.New("MCP credential binding changed"))
		}
		value, found, err := m.credentials.Get(ctx, ref.ID)
		if err != nil || !found || runtimeCredentialRevision(key, value) != ref.Revision {
			return "", runtimeAuthorityFailure(errors.New("MCP credential revision changed"))
		}
		return value, nil
	}
	recheckRecord := func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := m.store.GetMCPClientServer(ctx, record.Descriptor.ID)
		if err != nil {
			return err
		}
		if current.Generation != record.Generation || current.DescriptorFingerprint != record.DescriptorFingerprint ||
			current.State != TrustEnabled || current.Health != HealthHealthy || current.ApprovedCapabilityFingerprint != request.CapabilityFingerprint ||
			current.Capabilities.Fingerprint != request.CapabilityFingerprint || !scopeMatches(current.Descriptor, request.RunID, request.WorkspaceID) {
			return apperror.New(apperror.CodePolicyDenied, "MCP server review or scope changed before dispatch")
		}
		if recheckSource != nil {
			if err := recheckSource(ctx); err != nil {
				return err
			}
		}
		if err := m.recheckManualEnvironment(ctx, record.Descriptor, launch); err != nil {
			return err
		}
		if launch.HTTP != nil && launch.HTTP.Credential != nil {
			_, credentialErr := credential(ctx, *launch.HTTP.Credential)
			return credentialErr
		}
		return nil
	}
	// One host decision per connect/discovery/call; later discovery sends recheck
	// that exact decision without consuming another grant or trusting annotations.
	authorize := func(ctx context.Context, operation toolcontract.Operation) (toolcontract.DispatchGuard, func(context.Context) error, error) {
		if err := recheckRecord(ctx); err != nil {
			return nil, nil, runtimeAuthorityFailure(err)
		}
		decision, err := request.Authorizer.Authorize(ctx, request.Subject, operation, "")
		if err != nil {
			return nil, nil, runtimeAuthorityFailure(err)
		}
		if decision.Validate() != nil || decision.Outcome != "allow" {
			return nil, nil, runtimeAuthorityFailure(apperror.New(apperror.CodePolicyDenied, "MCP operation requires current host authorization"))
		}
		recheck := func(ctx context.Context) error {
			if err := recheckRecord(ctx); err != nil {
				return runtimeAuthorityFailure(err)
			}
			return runtimeAuthorityFailure(request.Authorizer.Recheck(ctx, request.Subject, operation, "", decision.AuthorizationRef))
		}
		guard := func(ctx context.Context, actual string) error {
			if err := recheckRecord(ctx); err != nil {
				return runtimeAuthorityFailure(err)
			}
			return runtimeAuthorityFailure(decision.BeforeDispatch(ctx, actual))
		}
		return guard, recheck, nil
	}
	launchFingerprint, err := toolcontract.FingerprintLaunch(key, launch)
	if err != nil {
		return result, err
	}
	connect := runtimeOperation("mcp-connect-"+digestBytes([]byte(request.OperationID)), toolcontract.OperationConnect, launch, launchFingerprint)
	connectGuard, _, err := authorize(ctx, connect)
	if err != nil {
		return result, err
	}
	discovery := toolcontract.DiscoveryScope{OperationID: "mcp-discovery-" + digestBytes([]byte(request.OperationID)), ConnectionFingerprint: launchFingerprint,
		Profile: launch.ProtocolVersions[0], Methods: []string{"initialize", "notifications/initialized", "tools/list", "resources/list", "resources/templates/list", "prompts/list"},
		MaxRequests: 64, MaxBytes: 1024 * 1024, ExpiresAt: time.Now().Add(time.Duration(record.Descriptor.CallTimeoutMillis) * time.Millisecond)}
	if discovery.Profile == preferredClientProtocolVersion {
		discovery.Methods = append(discovery.Methods, "server/discover")
	}
	var toolGuard toolcontract.DispatchGuard
	guards := toolcontract.ExecutionGuards{
		BeforeConnect: connectGuard,
		BeginDiscovery: func(ctx context.Context, scope toolcontract.DiscoveryScope) (toolcontract.DiscoverySendGuard, error) {
			fingerprint, err := toolcontract.FingerprintDiscovery(scope)
			if err != nil {
				return nil, err
			}
			expected, _ := toolcontract.FingerprintDiscovery(discovery)
			if fingerprint != expected {
				return nil, errors.New("MCP discovery budget changed")
			}
			operation := runtimeOperation(scope.OperationID, toolcontract.OperationDiscovery, launch, fingerprint)
			guard, recheck, err := authorize(ctx, operation)
			if err != nil {
				return nil, err
			}
			actual, _ := toolcontract.FingerprintOperation(operation)
			if err := guard(ctx, actual); err != nil {
				return nil, err
			}
			return toolcontract.NewDiscoverySendGuard(scope, recheck)
		},
		BeforeToolCall: func(ctx context.Context, actual string) error {
			if toolGuard == nil {
				return errors.New("MCP tool decision unavailable")
			}
			return toolGuard(ctx, actual)
		},
	}
	client, err := NewResolvedClient(launch, key, connect, guards, credential, m.httpClient)
	if err != nil {
		return result, err
	}
	defer client.Close()
	client.secrets = slices.Clone(secrets)
	client.descriptor.DeclaredCapabilities = slices.Clone(record.Descriptor.DeclaredCapabilities)
	current, err := client.DiscoverWithScope(ctx, discovery)
	if err != nil {
		return result, m.runtimeInvocationFailure(ctx, record, "runtime discovery failed", err)
	}
	latest, err := m.verifyInvocationSnapshot(ctx, request, record, current)
	if err != nil {
		return result, err
	}
	params, err := ValidateInvocation(record.Capabilities, request.Method, request.ToolName, request.Arguments)
	if err != nil {
		return result, err
	}
	var input string
	if request.Method == "" {
		input, err = FingerprintCallInput(key, launch, request.ToolName, request.Arguments)
	} else {
		input, err = FingerprintContentInput(key, launch, request.Method, params)
	}
	if err != nil {
		return result, err
	}
	operation := runtimeOperation(request.OperationID, toolcontract.OperationToolCall, launch, input)
	operation.CapabilityFingerprint = client.NativeCapabilityFingerprint()
	var recheckCall func(context.Context) error
	toolGuard, recheckCall, err = authorize(ctx, operation)
	if err != nil {
		return result, err
	}
	var raw json.RawMessage
	isError := false
	if request.Method == "" {
		typed, body, callReceipt, callErr := client.CallToolWithReceipt(ctx, operation, request.ToolName, request.Arguments)
		raw, receipt, err = body, callReceipt, callErr
		if typed != nil {
			isError = typed.IsError
		}
	} else {
		raw, receipt, err = client.InvokeContentWithReceipt(ctx, operation, request.Method, params)
	}
	if err != nil {
		return result, m.runtimeInvocationFailure(ctx, latest, "runtime tool call failed", err)
	}
	if err = recheckCall(ctx); err != nil {
		receipt.ErrorCode = "authority_changed"
		return result, err
	}
	raw, err = client.sanitizeJSON(raw)
	if err == nil {
		raw, err = redact.SanitizeSensitiveJSON(raw)
	}
	if err != nil {
		return result, err
	}
	content, truncated := BoundResultJSON(raw, record.Descriptor.MaxResultBytes)
	return ClientCallResult{Content: content, IsError: isError, Bytes: len(content), Truncated: truncated}, nil
}
