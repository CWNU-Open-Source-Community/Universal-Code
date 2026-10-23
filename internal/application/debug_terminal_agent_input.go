package application

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/executionauth"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
	terminalruntime "cyberagent-workbench/internal/terminal"
	"cyberagent-workbench/internal/tools"
)

const (
	DebugTerminalAgentInputProtocolVersion = "debug_terminal_agent_input.v1"
	DebugTerminalAgentInputPolicyVersion   = "debug_terminal_agent_input_policy.v1"
	MaxDebugTerminalAgentInputBindings     = 64
	MaxDebugTerminalOperationsPerBinding   = 256
)

type DebugTerminalAgentInputStore interface {
	GetRun(context.Context, string) (domain.Run, error)
	GetMission(context.Context, string) (domain.Mission, error)
	GetWorkspaceByID(context.Context, string) (session.WorkspaceRecord, error)
	GetRunMode(context.Context, string) (domain.RunModeSnapshot, error)
	GetRunExecutionProfile(
		context.Context,
		string,
	) (domain.RunExecutionProfileSnapshot, error)
	GetRunExecutionInteraction(
		context.Context,
		string,
	) (domain.RunExecutionInteractionSnapshot, error)
	GetRunExecutionPermission(
		context.Context,
		string,
	) (domain.RunExecutionPermissionSnapshot, error)
	RecordDebugTerminalAgentInputAudit(
		context.Context,
		terminalruntime.AgentInputAuditRecord,
	) error
}

type DebugTerminalAgentInputBridge interface {
	Issue(context.Context, terminalruntime.IssueAgentInputRequest) (
		terminalruntime.IssuedAgentInput,
		error,
	)
	Write(context.Context, terminalruntime.AgentWriteRequest) (
		terminalruntime.AgentWriteResult,
		error,
	)
	Read(context.Context, terminalruntime.AgentReadRequest) (
		terminalruntime.AgentReadResult,
		error,
	)
	Revoke(string, string, bool) (
		executionauth.TerminalInputLease,
		error,
	)
}

type DebugTerminalAgentInputController interface {
	Grant(context.Context, GrantDebugTerminalAgentInputRequest) (
		DebugTerminalAgentInputBinding,
		error,
	)
	Write(context.Context, WriteDebugTerminalAgentInputRequest) (
		DebugTerminalAgentInputWriteResult,
		error,
	)
	Read(context.Context, ReadDebugTerminalAgentOutputRequest) (
		DebugTerminalAgentOutputResult,
		error,
	)
	Active(context.Context, string) (
		DebugTerminalAgentInputBinding,
		bool,
		error,
	)
	Revoke(context.Context, RevokeDebugTerminalAgentInputRequest) error
	Reconcile(context.Context) int
	Shutdown(context.Context) int
}

type GrantDebugTerminalAgentInputRequest struct {
	ProtocolVersion           string
	RunID                     string
	TerminalSessionID         string
	RequestedBy               string
	TTL                       time.Duration
	ConfirmFullAccess         bool
	ConfirmAgentTerminalInput bool
}

type WriteDebugTerminalAgentInputRequest struct {
	ProtocolVersion string
	BindingID       string
	OperationKey    string
	Data            []byte
	// Interrupt selects a fixed Ctrl+C frame and requires Data to be empty.
	Interrupt bool
}

type ReadDebugTerminalAgentOutputRequest struct {
	ProtocolVersion string
	BindingID       string
	Cursor          uint64
	MaxBytes        int
}

type RevokeDebugTerminalAgentInputRequest struct {
	ProtocolVersion   string
	BindingID         string
	RequestedBy       string
	OperatorConfirmed bool
}

type DebugTerminalAgentInputBinding struct {
	ID                       string
	ProtocolVersion          string
	PolicyVersion            string
	RunID                    string
	MissionID                string
	SessionID                string
	WorkspaceID              string
	TerminalSessionID        string
	WorkspaceRootSHA256      string
	ModeSnapshotID           string
	ModeRevision             int64
	InteractionSnapshotID    string
	InteractionRevision      int64
	ExecutionProfileRevision int64
	PermissionSnapshotID     string
	PermissionRevision       int64
	PermissionMode           domain.RunExecutionPermissionMode
	RequestedBy              string
	IssuedAt                 time.Time
	ExpiresAt                time.Time
	Revoked                  bool
	ProcessLocal             bool
	AgentInputDefault        bool
	TokenPersisted           bool
	TokenExposed             bool
	RawInputPersisted        bool
	AutomaticRetryAllowed    bool
}

type DebugTerminalAgentInputWriteResult struct {
	ProtocolVersion       string
	BindingID             string
	TerminalSessionID     string
	OperationDigest       string
	DataSHA256            string
	DataBytes             int
	BytesWritten          int
	OutputCursor          uint64
	Replayed              bool
	ProcessLocal          bool
	TokenExposed          bool
	RawInputPersisted     bool
	AutomaticRetryAllowed bool
}

type DebugTerminalAgentOutputResult struct {
	ProtocolVersion   string
	BindingID         string
	TerminalSessionID string
	Backend           string
	BaseCursor        uint64
	NextCursor        uint64
	Data              []byte
	Dropped           bool
	State             terminalruntime.SessionState
}

type debugTerminalAgentBindings struct {
	run         domain.Run
	mission     domain.Mission
	workspace   session.WorkspaceRecord
	mode        domain.RunModeSnapshot
	profile     domain.RunExecutionProfileSnapshot
	interaction domain.RunExecutionInteractionSnapshot
	permission  domain.RunExecutionPermissionSnapshot
}

type debugTerminalOperationState string

const (
	debugTerminalOperationPending   debugTerminalOperationState = "pending"
	debugTerminalOperationCompleted debugTerminalOperationState = "completed"
	debugTerminalOperationUncertain debugTerminalOperationState = "uncertain"
)

type debugTerminalOperation struct {
	keyDigest  string
	dataDigest string
	state      debugTerminalOperationState
	result     DebugTerminalAgentInputWriteResult
}

type debugTerminalBindingEntry struct {
	binding              DebugTerminalAgentInputBinding
	token                string
	scope                executionauth.TerminalInputScope
	outputFloor          uint64
	permissionGeneration uint64
	runtimeEpoch         string
	runFence             uint64
	operations           map[string]debugTerminalOperation
}

type DebugTerminalAgentInputService struct {
	store        DebugTerminalAgentInputStore
	bridge       DebugTerminalAgentInputBridge
	checker      policy.Checker
	capabilities domain.ExecutionPermissionRuntimeCapabilities
	enabled      bool
	now          func() time.Time
	mu           sync.Mutex
	bindings     map[string]*debugTerminalBindingEntry
}

func NewDebugTerminalAgentInputService(
	stateStore DebugTerminalAgentInputStore,
	bridge DebugTerminalAgentInputBridge,
	checker policy.Checker,
	capabilities domain.ExecutionPermissionRuntimeCapabilities,
	enabled bool,
) (*DebugTerminalAgentInputService, error) {
	if stateStore == nil || bridge == nil || checker == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"debug terminal Agent-input dependencies are required")
	}
	if err := capabilities.Validate(); err != nil {
		return nil, apperror.Wrap(apperror.CodeFailedPrecondition,
			"debug terminal Agent-input runtime capabilities are invalid", err)
	}
	return &DebugTerminalAgentInputService{
		store: stateStore, bridge: bridge, checker: checker,
		capabilities: capabilities, enabled: enabled, now: time.Now,
		bindings: make(map[string]*debugTerminalBindingEntry),
	}, nil
}

func (s *DebugTerminalAgentInputService) Grant(
	ctx context.Context,
	request GrantDebugTerminalAgentInputRequest,
) (DebugTerminalAgentInputBinding, error) {
	normalized, err := normalizeDebugTerminalGrant(request)
	if err != nil {
		return DebugTerminalAgentInputBinding{}, apperror.Wrap(
			apperror.CodeInvalidArgument,
			"debug terminal Agent-input grant is invalid", err)
	}
	if err := s.requireAvailable(ctx); err != nil {
		return DebugTerminalAgentInputBinding{}, err
	}
	// Expired process-local bindings must not consume the bounded registry and
	// block a later explicit operator grant.
	s.Reconcile(ctx)
	bindings, err := s.loadAuthorizedBindings(ctx, normalized.RunID)
	if err != nil {
		return DebugTerminalAgentInputBinding{}, err
	}
	snapshotID, generation, epoch, fence, live := bindAgentCodeRuntime(s.capabilities, bindings.permission)
	if !live {
		return DebugTerminalAgentInputBinding{}, apperror.New(apperror.CodePolicyDenied, "terminal input requires active Full permission")
	}
	s.mu.Lock()
	if len(s.bindings) >= MaxDebugTerminalAgentInputBindings {
		s.mu.Unlock()
		return DebugTerminalAgentInputBinding{}, apperror.New(
			apperror.CodeResourceExhausted,
			"debug terminal Agent-input binding limit reached")
	}
	for _, entry := range s.bindings {
		if entry != nil && entry.binding.RunID == bindings.run.ID &&
			!entry.binding.Revoked &&
			s.now().UTC().Before(entry.binding.ExpiresAt) {
			s.mu.Unlock()
			return DebugTerminalAgentInputBinding{}, apperror.New(
				apperror.CodeConflict,
				"Run already has an active debug terminal Agent-input binding")
		}
	}
	s.mu.Unlock()
	issued, err := s.bridge.Issue(ctx, terminalruntime.IssueAgentInputRequest{
		SessionID:         normalized.TerminalSessionID,
		RequestedBy:       normalized.RequestedBy,
		OperatorConfirmed: true, TTL: normalized.TTL,
	})
	if err != nil {
		return DebugTerminalAgentInputBinding{}, apperror.Wrap(
			apperror.CodePolicyDenied,
			"debug terminal Agent-input lease was denied", err)
	}
	workspaceRootSHA256, err := terminalruntime.WorkspaceRootSHA256(
		filepath.Clean(bindings.workspace.RootPath))
	if err != nil {
		_, _ = s.bridge.Revoke(issued.Lease.ID, normalized.RequestedBy, true)
		return DebugTerminalAgentInputBinding{}, apperror.Wrap(
			apperror.CodeFailedPrecondition,
			"debug terminal Workspace root binding is invalid", err)
	}
	expectedScope := debugTerminalInputScope(bindings,
		normalized.TerminalSessionID)
	if issued.ProtocolVersion != terminalruntime.AgentInputBridgeProtocolVersion ||
		issued.Token == "" || issued.Lease.Scope != expectedScope ||
		issued.Lease.RequestedBy != normalized.RequestedBy ||
		issued.Lease.Revoked ||
		issued.WorkspaceRootSHA256 != workspaceRootSHA256 {
		_, _ = s.bridge.Revoke(issued.Lease.ID, normalized.RequestedBy, true)
		return DebugTerminalAgentInputBinding{}, apperror.New(
			apperror.CodeConflict,
			"debug terminal Agent-input lease binding is inconsistent")
	}
	current, err := s.loadAuthorizedBindings(ctx, normalized.RunID)
	if err != nil || !sameDebugTerminalBindings(bindings, current) ||
		!agentCodeRuntimeCurrent(s.capabilities, current.permission, snapshotID, generation, epoch, fence) {
		_, _ = s.bridge.Revoke(issued.Lease.ID, normalized.RequestedBy, true)
		if err != nil {
			return DebugTerminalAgentInputBinding{}, err
		}
		return DebugTerminalAgentInputBinding{}, apperror.New(
			apperror.CodeConflict,
			"debug terminal Agent-input binding changed during grant")
	}
	binding := projectDebugTerminalBinding(bindings, issued)
	if err := s.store.RecordDebugTerminalAgentInputAudit(ctx,
		debugTerminalAuditRecord(binding,
			terminalruntime.AgentInputAuditGranted, "", "", 0, 0,
			binding.IssuedAt)); err != nil {
		_, _ = s.bridge.Revoke(binding.ID, binding.RequestedBy, true)
		return DebugTerminalAgentInputBinding{}, apperror.Normalize(err)
	}
	// The audit store may block across a permission change. Do not publish a
	// lease from a revoked activation after that durable write completes.
	current, err = s.loadAuthorizedBindings(ctx, normalized.RunID)
	if err != nil || !sameDebugTerminalBindings(bindings, current) ||
		!agentCodeRuntimeCurrent(s.capabilities, current.permission, snapshotID, generation, epoch, fence) {
		_, _ = s.bridge.Revoke(binding.ID, binding.RequestedBy, true)
		revoked := binding
		revoked.Revoked = true
		_ = s.store.RecordDebugTerminalAgentInputAudit(ctx, debugTerminalAuditRecord(revoked,
			terminalruntime.AgentInputAuditRevoked, "", "", 0, 0, s.now().UTC()))
		return DebugTerminalAgentInputBinding{}, apperror.New(apperror.CodePolicyDenied, "terminal input authorization changed during grant")
	}
	s.mu.Lock()
	duplicateRun := false
	for _, entry := range s.bindings {
		if entry != nil && entry.binding.RunID == binding.RunID &&
			!entry.binding.Revoked &&
			s.now().UTC().Before(entry.binding.ExpiresAt) {
			duplicateRun = true
			break
		}
	}
	if len(s.bindings) >= MaxDebugTerminalAgentInputBindings ||
		s.bindings[binding.ID] != nil || duplicateRun {
		s.mu.Unlock()
		_, _ = s.bridge.Revoke(binding.ID, binding.RequestedBy, true)
		revoked := binding
		revoked.Revoked = true
		_ = s.store.RecordDebugTerminalAgentInputAudit(ctx,
			debugTerminalAuditRecord(revoked,
				terminalruntime.AgentInputAuditRevoked, "", "", 0, 0,
				s.now().UTC()))
		code := apperror.CodeResourceExhausted
		message := "debug terminal Agent-input binding limit reached"
		if duplicateRun {
			code = apperror.CodeConflict
			message = "Run already has an active debug terminal Agent-input binding"
		}
		return DebugTerminalAgentInputBinding{}, apperror.New(code, message)
	}
	// Do not discard a pending line for a failed duplicate grant. Establish the
	// actual publication fence only after the binding has passed admission.
	outputFence, err := s.bridge.Read(ctx, terminalruntime.AgentReadRequest{
		Token: issued.Token, Scope: issued.Lease.Scope,
		Cursor: math.MaxUint64, MaxBytes: 1, Fence: true,
	})
	if err != nil || outputFence.ProtocolVersion != terminalruntime.AgentInputBridgeProtocolVersion ||
		outputFence.LeaseID != binding.ID || outputFence.SessionID != binding.TerminalSessionID ||
		outputFence.Page.SessionID != binding.TerminalSessionID {
		s.mu.Unlock()
		_, _ = s.bridge.Revoke(binding.ID, binding.RequestedBy, true)
		_ = s.store.RecordDebugTerminalAgentInputAudit(ctx, debugTerminalAuditRecord(binding,
			terminalruntime.AgentInputAuditRevoked, "", "", 0, 0, s.now().UTC()))
		if err == nil {
			err = errors.New("debug terminal output fence returned an inconsistent binding")
		}
		return DebugTerminalAgentInputBinding{}, apperror.Wrap(apperror.CodeFailedPrecondition,
			"debug terminal output fence could not be established", err)
	}
	s.bindings[binding.ID] = &debugTerminalBindingEntry{
		binding: binding, token: issued.Token, scope: issued.Lease.Scope,
		outputFloor:          outputFence.Page.NextCursor,
		permissionGeneration: generation, runtimeEpoch: epoch, runFence: fence,
		operations: make(map[string]debugTerminalOperation),
	}
	s.mu.Unlock()
	return binding, nil
}

func (s *DebugTerminalAgentInputService) Write(
	ctx context.Context,
	request WriteDebugTerminalAgentInputRequest,
) (DebugTerminalAgentInputWriteResult, error) {
	normalized, command, err := normalizeDebugTerminalWrite(request)
	if err != nil {
		return DebugTerminalAgentInputWriteResult{}, apperror.Wrap(
			apperror.CodeInvalidArgument,
			"debug terminal Agent-input write is invalid", err)
	}
	if err := s.requireAvailable(ctx); err != nil {
		return DebugTerminalAgentInputWriteResult{}, err
	}
	entry, err := s.bindingEntry(ctx, normalized.BindingID)
	if err != nil {
		return DebugTerminalAgentInputWriteResult{}, err
	}
	if err := s.revalidateBinding(ctx, entry); err != nil {
		return DebugTerminalAgentInputWriteResult{}, err
	}
	policyCall := tools.Call{
		Name: "shell", Args: map[string]string{"command": command},
	}
	if normalized.Interrupt {
		policyCall = tools.Call{
			Name: "debug_terminal", Args: map[string]string{"action": "interrupt"},
		}
	}
	policyDecision := s.checker.CheckToolCall(policyCall)
	if !policyDecision.Allowed || policyDecision.NeedsApproval {
		return DebugTerminalAgentInputWriteResult{}, apperror.New(
			apperror.CodePolicyDenied, policyDecision.Reason)
	}
	operationDigest := runmutation.Fingerprint(
		"debug_terminal_agent_input_operation.v1",
		entry.binding.ID, normalized.OperationKey)
	dataDigest := terminalruntime.AgentInputDataDigest(normalized.Data)
	s.mu.Lock()
	live := s.bindings[entry.binding.ID]
	if live == nil || live.binding != entry.binding {
		s.mu.Unlock()
		return DebugTerminalAgentInputWriteResult{}, apperror.New(
			apperror.CodeConflict,
			"debug terminal Agent-input binding changed concurrently")
	}
	if existing, found := live.operations[operationDigest]; found {
		s.mu.Unlock()
		if existing.keyDigest != operationDigest ||
			existing.dataDigest != dataDigest {
			return DebugTerminalAgentInputWriteResult{}, apperror.New(
				apperror.CodeConflict,
				"debug terminal operation key was reused for different input")
		}
		switch existing.state {
		case debugTerminalOperationCompleted:
			result := existing.result
			result.Replayed = true
			return result, nil
		default:
			return DebugTerminalAgentInputWriteResult{}, apperror.New(
				apperror.CodeFailedPrecondition,
				"debug terminal input may already have been written; automatic retry is disabled")
		}
	}
	if len(live.operations) >= MaxDebugTerminalOperationsPerBinding {
		s.mu.Unlock()
		return DebugTerminalAgentInputWriteResult{}, apperror.New(
			apperror.CodeResourceExhausted,
			"debug terminal operation limit reached for this binding")
	}
	live.operations[operationDigest] = debugTerminalOperation{
		keyDigest: operationDigest, dataDigest: dataDigest,
		state: debugTerminalOperationPending,
	}
	s.mu.Unlock()
	tail, tailErr := s.bridge.Read(ctx, terminalruntime.AgentReadRequest{
		Token: entry.token, Scope: entry.scope,
		Cursor: math.MaxUint64, MaxBytes: 1, Fence: true,
	})
	if tailErr != nil {
		s.removePendingOperation(entry.binding.ID, operationDigest)
		return DebugTerminalAgentInputWriteResult{}, apperror.Wrap(
			apperror.CodeFailedPrecondition,
			"debug terminal output cursor could not be fenced before input", tailErr)
	}
	if tail.ProtocolVersion != terminalruntime.AgentInputBridgeProtocolVersion ||
		tail.LeaseID != entry.binding.ID ||
		tail.SessionID != entry.binding.TerminalSessionID ||
		tail.Page.SessionID != entry.binding.TerminalSessionID {
		s.removePendingOperation(entry.binding.ID, operationDigest)
		return DebugTerminalAgentInputWriteResult{}, apperror.New(
			apperror.CodeConflict,
			"debug terminal output cursor returned an inconsistent binding")
	}

	preparedAt := s.now().UTC()
	prepared := debugTerminalAuditRecord(entry.binding,
		terminalruntime.AgentInputAuditPrepared, operationDigest,
		dataDigest, len(normalized.Data), 0, preparedAt)
	if err := s.store.RecordDebugTerminalAgentInputAudit(ctx, prepared); err != nil {
		s.removePendingOperation(entry.binding.ID, operationDigest)
		return DebugTerminalAgentInputWriteResult{}, apperror.Normalize(err)
	}
	// Recheck after the durable prepare and immediately before the native
	// sink, which independently authorizes the exact bytes using PolicyAuthorizer.
	if err := s.revalidateBinding(ctx, entry); err != nil {
		s.markOperationUncertain(entry.binding.ID, operationDigest)
		return DebugTerminalAgentInputWriteResult{}, err
	}
	written, writeErr := s.bridge.Write(ctx, terminalruntime.AgentWriteRequest{
		Token: entry.token, Scope: entry.scope, Data: normalized.Data,
	})
	if writeErr != nil {
		s.markOperationUncertain(entry.binding.ID, operationDigest)
		return DebugTerminalAgentInputWriteResult{}, apperror.Wrap(
			apperror.CodeFailedPrecondition,
			"debug terminal input may have been partially written; automatic retry is disabled",
			writeErr)
	}
	if written.ProtocolVersion != terminalruntime.AgentInputBridgeProtocolVersion ||
		written.LeaseID != entry.binding.ID ||
		written.SessionID != entry.binding.TerminalSessionID ||
		written.BytesWritten != len(normalized.Data) {
		s.markOperationUncertain(entry.binding.ID, operationDigest)
		return DebugTerminalAgentInputWriteResult{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"debug terminal write returned an inconsistent result; automatic retry is disabled")
	}
	result := DebugTerminalAgentInputWriteResult{
		ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
		BindingID:       entry.binding.ID, TerminalSessionID: written.SessionID,
		OperationDigest: operationDigest, DataSHA256: dataDigest,
		DataBytes: len(normalized.Data), BytesWritten: written.BytesWritten,
		OutputCursor: tail.Page.NextCursor,
		ProcessLocal: true, AutomaticRetryAllowed: false,
	}
	completed := debugTerminalAuditRecord(entry.binding,
		terminalruntime.AgentInputAuditCompleted, operationDigest,
		dataDigest, len(normalized.Data), written.BytesWritten, s.now().UTC())
	if err := s.store.RecordDebugTerminalAgentInputAudit(ctx, completed); err != nil {
		s.markOperationUncertain(entry.binding.ID, operationDigest)
		return DebugTerminalAgentInputWriteResult{}, apperror.Wrap(
			apperror.CodeFailedPrecondition,
			"debug terminal write completed but its audit receipt is uncertain; automatic retry is disabled",
			err)
	}
	s.mu.Lock()
	if live := s.bindings[entry.binding.ID]; live != nil {
		live.operations[operationDigest] = debugTerminalOperation{
			keyDigest: operationDigest, dataDigest: dataDigest,
			state: debugTerminalOperationCompleted, result: result,
		}
	}
	s.mu.Unlock()
	return result, nil
}

// Active returns the single process-local Agent-input binding for a Run. It
// never exposes the bearer token and revalidates every durable permission
// snapshot before making the binding available to a model-facing adapter.
func (s *DebugTerminalAgentInputService) Active(
	ctx context.Context,
	runID string,
) (DebugTerminalAgentInputBinding, bool, error) {
	if err := s.requireAvailable(ctx); err != nil {
		return DebugTerminalAgentInputBinding{}, false, err
	}
	runID = strings.TrimSpace(runID)
	if !domain.ValidAgentID(runID) {
		return DebugTerminalAgentInputBinding{}, false, apperror.New(
			apperror.CodeInvalidArgument,
			"debug terminal Agent-input Run id is invalid")
	}
	s.mu.Lock()
	var found *debugTerminalBindingEntry
	for _, entry := range s.bindings {
		if entry != nil && entry.binding.RunID == runID &&
			!entry.binding.Revoked {
			if found != nil {
				s.mu.Unlock()
				return DebugTerminalAgentInputBinding{}, false, apperror.New(
					apperror.CodeConflict,
					"Run has multiple debug terminal Agent-input bindings")
			}
			found = entry
		}
	}
	s.mu.Unlock()
	if found == nil {
		return DebugTerminalAgentInputBinding{}, false, nil
	}
	entry, err := s.bindingEntry(ctx, found.binding.ID)
	if err != nil {
		if apperror.CodeOf(err) == apperror.CodeFailedPrecondition {
			return DebugTerminalAgentInputBinding{}, false, nil
		}
		return DebugTerminalAgentInputBinding{}, false, err
	}
	if err := s.revalidateBinding(ctx, entry); err != nil {
		if apperror.CodeOf(err) == apperror.CodePolicyDenied ||
			apperror.CodeOf(err) == apperror.CodeFailedPrecondition {
			return DebugTerminalAgentInputBinding{}, false, nil
		}
		return DebugTerminalAgentInputBinding{}, false, err
	}
	return entry.binding, true, nil
}

// Read returns a bounded cursor page without consuming renderer output. The
// lease and all durable Run bindings are checked again for every page.
func (s *DebugTerminalAgentInputService) Read(
	ctx context.Context,
	request ReadDebugTerminalAgentOutputRequest,
) (DebugTerminalAgentOutputResult, error) {
	if err := s.requireAvailable(ctx); err != nil {
		return DebugTerminalAgentOutputResult{}, err
	}
	request.BindingID = strings.TrimSpace(request.BindingID)
	if request.ProtocolVersion != DebugTerminalAgentInputProtocolVersion ||
		!domain.ValidAgentID(request.BindingID) || request.MaxBytes < 1 ||
		request.MaxBytes > terminalruntime.MaxTerminalOutputReadBytes {
		return DebugTerminalAgentOutputResult{}, apperror.New(
			apperror.CodeInvalidArgument,
			"debug terminal Agent-output read is invalid")
	}
	entry, err := s.bindingEntry(ctx, request.BindingID)
	if err != nil {
		return DebugTerminalAgentOutputResult{}, err
	}
	if err := s.revalidateBinding(ctx, entry); err != nil {
		return DebugTerminalAgentOutputResult{}, err
	}
	cursor := request.Cursor
	if cursor == 0 || cursor < entry.outputFloor {
		cursor = entry.outputFloor
	}
	read, err := s.bridge.Read(ctx, terminalruntime.AgentReadRequest{
		Token: entry.token, Scope: entry.scope, Cursor: cursor,
		MaxBytes: request.MaxBytes,
	})
	if err != nil {
		return DebugTerminalAgentOutputResult{}, apperror.Wrap(
			apperror.CodeFailedPrecondition,
			"debug terminal Agent-output read was denied", err)
	}
	if read.ProtocolVersion != terminalruntime.AgentInputBridgeProtocolVersion ||
		read.LeaseID != entry.binding.ID ||
		read.SessionID != entry.binding.TerminalSessionID ||
		read.Page.SessionID != entry.binding.TerminalSessionID {
		return DebugTerminalAgentOutputResult{}, apperror.New(
			apperror.CodeConflict,
			"debug terminal Agent-output read returned an inconsistent binding")
	}
	return DebugTerminalAgentOutputResult{
		ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
		BindingID:       entry.binding.ID, TerminalSessionID: read.SessionID,
		Backend: read.Backend, BaseCursor: read.Page.BaseCursor,
		NextCursor: read.Page.NextCursor,
		Data:       append([]byte(nil), read.Page.Data...),
		Dropped:    read.Page.Dropped, State: read.Page.State,
	}, nil
}

func (s *DebugTerminalAgentInputService) Revoke(
	ctx context.Context,
	request RevokeDebugTerminalAgentInputRequest,
) error {
	if err := s.requireAvailable(ctx); err != nil {
		return err
	}
	request.BindingID = strings.TrimSpace(request.BindingID)
	request.RequestedBy = strings.TrimSpace(redact.String(request.RequestedBy))
	if request.ProtocolVersion != DebugTerminalAgentInputProtocolVersion ||
		!domain.ValidAgentID(request.BindingID) ||
		!validDebugTerminalOperator(request.RequestedBy) ||
		!request.OperatorConfirmed {
		return apperror.New(apperror.CodeInvalidArgument,
			"debug terminal Agent-input revoke requires an exact operator confirmation")
	}
	entry, err := s.bindingEntry(ctx, request.BindingID)
	if err != nil {
		return err
	}
	if !s.removeBinding(entry) {
		return apperror.New(apperror.CodeConflict,
			"debug terminal Agent-input binding changed concurrently")
	}
	_, revokeErr := s.bridge.Revoke(entry.binding.ID, request.RequestedBy, true)
	binding := entry.binding
	binding.Revoked = true
	auditErr := s.store.RecordDebugTerminalAgentInputAudit(ctx,
		debugTerminalAuditRecord(binding,
			terminalruntime.AgentInputAuditRevoked, "", "", 0, 0,
			s.now().UTC()))
	if revokeErr != nil {
		return apperror.Wrap(apperror.CodeFailedPrecondition,
			"debug terminal Agent-input lease revoke failed", revokeErr)
	}
	if auditErr != nil {
		return apperror.Normalize(auditErr)
	}
	return nil
}

func (s *DebugTerminalAgentInputService) Reconcile(ctx context.Context) int {
	if s == nil || ctx == nil || ctx.Err() != nil {
		return 0
	}
	s.mu.Lock()
	entries := make([]*debugTerminalBindingEntry, 0, len(s.bindings))
	for _, entry := range s.bindings {
		entries = append(entries, entry)
	}
	s.mu.Unlock()
	revoked := 0
	for _, entry := range entries {
		current, err := s.loadAuthorizedBindings(
			ctx, entry.binding.RunID)
		if err == nil && s.bindingRuntimeCurrent(entry, current) &&
			sameBindingAndDurableState(entry.binding, current) &&
			s.now().UTC().Before(entry.binding.ExpiresAt) &&
			s.bridgeBindingLive(ctx, entry) {
			continue
		}
		if ctx.Err() != nil {
			return revoked
		}
		if s.invalidateBindingWithAudit(ctx, entry) {
			revoked++
		}
	}
	return revoked
}

func (s *DebugTerminalAgentInputService) Shutdown(ctx context.Context) int {
	if s == nil || ctx == nil {
		return 0
	}
	s.mu.Lock()
	entries := make([]*debugTerminalBindingEntry, 0, len(s.bindings))
	for _, entry := range s.bindings {
		entries = append(entries, entry)
	}
	s.mu.Unlock()
	revoked := 0
	for _, entry := range entries {
		if s.invalidateBindingWithAudit(ctx, entry) {
			revoked++
		}
	}
	return revoked
}

func (s *DebugTerminalAgentInputService) requireAvailable(
	ctx context.Context,
) error {
	if s == nil || s.store == nil || s.bridge == nil || s.checker == nil ||
		s.now == nil || s.bindings == nil || !s.enabled {
		return apperror.New(apperror.CodeFailedPrecondition,
			"debug terminal Agent input is disabled for this process")
	}
	if ctx == nil {
		return apperror.New(apperror.CodeInvalidArgument,
			"debug terminal Agent-input context is required")
	}
	if ctx.Err() != nil {
		return apperror.Normalize(ctx.Err())
	}
	if err := s.capabilities.Validate(); err != nil ||
		!s.capabilities.Allows(domain.RunExecutionPermissionFull) {
		return apperror.New(apperror.CodePolicyDenied,
			"debug terminal Agent input requires the current Full process gate")
	}
	return nil
}

func (s *DebugTerminalAgentInputService) loadAuthorizedBindings(
	ctx context.Context,
	runID string,
) (debugTerminalAgentBindings, error) {
	var empty debugTerminalAgentBindings
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return empty, apperror.Normalize(err)
	}
	if run.Terminal() {
		return empty, apperror.New(
			apperror.CodeFailedPrecondition,
			"debug terminal Agent input cannot bind a terminal Run")
	}
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return empty, apperror.Normalize(err)
	}
	workspace, err := s.store.GetWorkspaceByID(ctx, mission.WorkspaceID)
	if err != nil {
		return empty, apperror.Normalize(err)
	}
	mode, err := s.store.GetRunMode(ctx, run.ID)
	if err != nil {
		return empty, apperror.Normalize(err)
	}
	profile, err := s.store.GetRunExecutionProfile(ctx, run.ID)
	if err != nil {
		return empty, apperror.Normalize(err)
	}
	interaction, err := s.store.GetRunExecutionInteraction(ctx, run.ID)
	if err != nil {
		return empty, apperror.Normalize(err)
	}
	permission, err := s.store.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		return empty, apperror.Normalize(err)
	}
	if run.MissionID != mission.ID || run.SessionID == "" ||
		mission.WorkspaceID != workspace.ID ||
		mode.RunID != run.ID || mode.MissionID != mission.ID ||
		mode.Surface != domain.ExecutionSurfaceCode ||
		mode.Phase != domain.ExecutionPhaseDeliver ||
		profile.RunID != run.ID || profile.MissionID != mission.ID ||
		profile.Profile != domain.RunExecutionProfileLocal ||
		interaction.RunID != run.ID ||
		interaction.MissionID != mission.ID ||
		interaction.Mode != domain.RunExecutionInteractionDebug ||
		interaction.Surface != domain.ExecutionSurfaceCode ||
		interaction.ExecutionProfile != domain.RunExecutionProfileLocal ||
		interaction.ExecutionProfileRevision != profile.Revision ||
		interaction.WorkspaceTrust != domain.WorkspaceTrustTrusted ||
		interaction.AgentInputDefault ||
		permission.RunID != run.ID ||
		permission.MissionID != mission.ID ||
		permission.Mode != domain.RunExecutionPermissionFull || permission.Validate() != nil {
		return empty, apperror.New(
			apperror.CodeConflict,
			"debug terminal Agent-input durable binding is stale")
	}
	if _, live := s.capabilities.FullAccessGeneration(permission); !live {
		return empty, apperror.New(apperror.CodePolicyDenied, "terminal input requires active Full permission")
	}
	return debugTerminalAgentBindings{
		run: run, mission: mission, workspace: workspace, mode: mode,
		profile: profile, interaction: interaction, permission: permission,
	}, nil
}

func (s *DebugTerminalAgentInputService) revalidateBinding(
	ctx context.Context,
	entry *debugTerminalBindingEntry,
) error {
	current, err := s.loadAuthorizedBindings(ctx, entry.binding.RunID)
	if err == nil && sameBindingAndDurableState(entry.binding, current) &&
		s.bindingRuntimeCurrent(entry, current) &&
		s.bridgeBindingLive(ctx, entry) {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	s.invalidateBindingWithAudit(ctx, entry)
	if err != nil && apperror.CodeOf(err) != apperror.CodeConflict {
		return err
	}
	return apperror.New(apperror.CodePolicyDenied,
		"debug terminal Agent-input binding is stale or no longer authorized")
}

func (s *DebugTerminalAgentInputService) bridgeBindingLive(
	ctx context.Context,
	entry *debugTerminalBindingEntry,
) bool {
	if entry == nil || s.bridge == nil {
		return false
	}
	read, err := s.bridge.Read(ctx, terminalruntime.AgentReadRequest{
		Token: entry.token, Scope: entry.scope,
		Cursor: math.MaxUint64, MaxBytes: 1,
	})
	return err == nil &&
		read.ProtocolVersion == terminalruntime.AgentInputBridgeProtocolVersion &&
		read.LeaseID == entry.binding.ID &&
		read.SessionID == entry.binding.TerminalSessionID &&
		read.Page.SessionID == entry.binding.TerminalSessionID
}

func normalizeDebugTerminalGrant(
	request GrantDebugTerminalAgentInputRequest,
) (GrantDebugTerminalAgentInputRequest, error) {
	request.RunID = strings.TrimSpace(request.RunID)
	request.TerminalSessionID = strings.TrimSpace(request.TerminalSessionID)
	request.RequestedBy = strings.TrimSpace(redact.String(request.RequestedBy))
	if request.ProtocolVersion != DebugTerminalAgentInputProtocolVersion ||
		!domain.ValidAgentID(request.RunID) ||
		!domain.ValidAgentID(request.TerminalSessionID) ||
		!validDebugTerminalOperator(request.RequestedBy) ||
		!request.ConfirmFullAccess ||
		!request.ConfirmAgentTerminalInput {
		return GrantDebugTerminalAgentInputRequest{}, errors.New(
			"an operator must explicitly confirm Full activation and Agent terminal input")
	}
	if request.TTL == 0 {
		request.TTL = executionauth.DefaultTerminalInputLeaseTTL
	}
	if request.TTL < executionauth.MinTerminalInputLeaseTTL ||
		request.TTL > executionauth.MaxTerminalInputLeaseTTL {
		return GrantDebugTerminalAgentInputRequest{}, fmt.Errorf(
			"debug terminal Agent-input TTL must be between %s and %s",
			executionauth.MinTerminalInputLeaseTTL,
			executionauth.MaxTerminalInputLeaseTTL)
	}
	return request, nil
}

func normalizeDebugTerminalWrite(
	request WriteDebugTerminalAgentInputRequest,
) (WriteDebugTerminalAgentInputRequest, string, error) {
	request.BindingID = strings.TrimSpace(request.BindingID)
	if request.ProtocolVersion != DebugTerminalAgentInputProtocolVersion ||
		!domain.ValidAgentID(request.BindingID) {
		return WriteDebugTerminalAgentInputRequest{}, "", errors.New(
			"debug terminal Agent-input protocol or binding is invalid")
	}
	operationKey, err := domain.NormalizeAgentOperationKey(request.OperationKey)
	if err != nil {
		return WriteDebugTerminalAgentInputRequest{}, "", err
	}
	for _, value := range operationKey {
		if unicode.IsControl(value) || unicode.IsSpace(value) {
			return WriteDebugTerminalAgentInputRequest{}, "", errors.New(
				"debug terminal operation key cannot contain whitespace or control characters")
		}
	}
	request.OperationKey = operationKey
	if request.Interrupt {
		if len(request.Data) != 0 {
			return WriteDebugTerminalAgentInputRequest{}, "", errors.New(
				"debug terminal interrupt cannot include input data")
		}
		request.Data = []byte{0x03}
		return request, "", nil
	}
	if len(request.Data) == 0 ||
		len(request.Data) > terminalruntime.MaxTerminalInputBytes ||
		!utf8.Valid(request.Data) {
		return WriteDebugTerminalAgentInputRequest{}, "", errors.New(
			"debug terminal input must be bounded UTF-8")
	}
	request.Data = append([]byte(nil), request.Data...)
	command := request.Data
	switch {
	case strings.HasSuffix(string(command), "\r\n"):
		command = command[:len(command)-2]
	case command[len(command)-1] == '\r' || command[len(command)-1] == '\n':
		command = command[:len(command)-1]
	default:
		return WriteDebugTerminalAgentInputRequest{}, "", errors.New(
			"debug terminal Agent input must contain one complete command frame")
	}
	if len(command) == 0 || strings.TrimSpace(string(command)) == "" ||
		strings.ContainsAny(string(command), "\r\n") {
		return WriteDebugTerminalAgentInputRequest{}, "", errors.New(
			"debug terminal Agent input must contain exactly one command line")
	}
	for _, value := range string(command) {
		if unicode.IsControl(value) && value != '\t' {
			return WriteDebugTerminalAgentInputRequest{}, "", errors.New(
				"debug terminal Agent input contains an unsupported control character")
		}
	}
	return request, string(command), nil
}

func validDebugTerminalOperator(value string) bool {
	value = strings.TrimSpace(value)
	if !domain.ValidAgentID(value) || strings.ContainsRune(value, 0) {
		return false
	}
	switch strings.ToLower(value) {
	case "agent", "llm", "model", "repository", "repo", "skill",
		"supervisor", "run_supervisor":
		return false
	default:
		return true
	}
}

func debugTerminalInputScope(bindings debugTerminalAgentBindings,
	terminalSessionID string,
) executionauth.TerminalInputScope {
	return executionauth.TerminalInputScope{
		WorkspaceID: bindings.workspace.ID, RunID: bindings.run.ID,
		TerminalSessionID:        terminalSessionID,
		InteractionSnapshotID:    bindings.interaction.ID,
		InteractionRevision:      bindings.interaction.Revision,
		ExecutionProfileRevision: bindings.profile.Revision,
		PermissionSnapshotID:     bindings.permission.ID,
		PermissionRevision:       bindings.permission.Revision,
		PermissionMode:           bindings.permission.Mode,
		Mode:                     bindings.interaction.Mode,
	}
}

func projectDebugTerminalBinding(bindings debugTerminalAgentBindings,
	issued terminalruntime.IssuedAgentInput,
) DebugTerminalAgentInputBinding {
	return DebugTerminalAgentInputBinding{
		ID:              issued.Lease.ID,
		ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
		PolicyVersion:   DebugTerminalAgentInputPolicyVersion,
		RunID:           bindings.run.ID, MissionID: bindings.mission.ID,
		SessionID: bindings.run.SessionID, WorkspaceID: bindings.workspace.ID,
		TerminalSessionID:        issued.Lease.Scope.TerminalSessionID,
		WorkspaceRootSHA256:      issued.WorkspaceRootSHA256,
		ModeSnapshotID:           bindings.mode.ID,
		ModeRevision:             bindings.mode.Revision,
		InteractionSnapshotID:    issued.Lease.Scope.InteractionSnapshotID,
		InteractionRevision:      issued.Lease.Scope.InteractionRevision,
		ExecutionProfileRevision: issued.Lease.Scope.ExecutionProfileRevision,
		PermissionSnapshotID:     issued.Lease.Scope.PermissionSnapshotID,
		PermissionRevision:       issued.Lease.Scope.PermissionRevision,
		PermissionMode:           issued.Lease.Scope.PermissionMode,
		RequestedBy:              issued.Lease.RequestedBy,
		IssuedAt:                 issued.Lease.IssuedAt, ExpiresAt: issued.Lease.ExpiresAt,
		ProcessLocal: true, AgentInputDefault: false,
		TokenPersisted: false, TokenExposed: false, RawInputPersisted: false,
		AutomaticRetryAllowed: false,
	}
}

func sameDebugTerminalBindings(left debugTerminalAgentBindings,
	right debugTerminalAgentBindings,
) bool {
	return left.run.ID == right.run.ID &&
		left.run.MissionID == right.run.MissionID &&
		left.run.SessionID == right.run.SessionID &&
		left.workspace.ID == right.workspace.ID &&
		filepath.Clean(left.workspace.RootPath) == filepath.Clean(right.workspace.RootPath) &&
		left.mode.ID == right.mode.ID &&
		left.mode.Revision == right.mode.Revision &&
		left.profile.ID == right.profile.ID &&
		left.profile.Revision == right.profile.Revision &&
		left.interaction.ID == right.interaction.ID &&
		left.interaction.Revision == right.interaction.Revision &&
		left.permission.ID == right.permission.ID &&
		left.permission.Revision == right.permission.Revision
}

func sameBindingAndDurableState(binding DebugTerminalAgentInputBinding,
	current debugTerminalAgentBindings,
) bool {
	workspaceRootSHA256, err := terminalruntime.WorkspaceRootSHA256(
		filepath.Clean(current.workspace.RootPath))
	if err != nil {
		return false
	}
	return binding.RunID == current.run.ID &&
		binding.MissionID == current.mission.ID &&
		binding.SessionID == current.run.SessionID &&
		binding.WorkspaceID == current.workspace.ID &&
		binding.WorkspaceRootSHA256 == workspaceRootSHA256 &&
		binding.ModeSnapshotID == current.mode.ID &&
		binding.ModeRevision == current.mode.Revision &&
		binding.InteractionSnapshotID == current.interaction.ID &&
		binding.InteractionRevision == current.interaction.Revision &&
		binding.ExecutionProfileRevision == current.profile.Revision &&
		binding.PermissionSnapshotID == current.permission.ID &&
		binding.PermissionRevision == current.permission.Revision &&
		binding.PermissionMode == current.permission.Mode
}

func debugTerminalAuditRecord(binding DebugTerminalAgentInputBinding,
	kind terminalruntime.AgentInputAuditKind,
	operationDigest string,
	dataDigest string,
	dataBytes int,
	bytesWritten int,
	at time.Time,
) terminalruntime.AgentInputAuditRecord {
	recordDigest := runmutation.Fingerprint(
		"debug_terminal_agent_input_audit_record.v1",
		binding.ID, string(kind), operationDigest)
	return terminalruntime.AgentInputAuditRecord{
		ID:              "debug-terminal-audit-" + recordDigest[:24],
		ProtocolVersion: terminalruntime.AgentInputAuditProtocolVersion,
		Kind:            kind, RunID: binding.RunID, MissionID: binding.MissionID,
		SessionID: binding.SessionID, WorkspaceID: binding.WorkspaceID,
		TerminalSessionID:        binding.TerminalSessionID,
		BindingID:                binding.ID,
		InteractionSnapshotID:    binding.InteractionSnapshotID,
		InteractionRevision:      binding.InteractionRevision,
		ExecutionProfileRevision: binding.ExecutionProfileRevision,
		PermissionSnapshotID:     binding.PermissionSnapshotID,
		PermissionRevision:       binding.PermissionRevision,
		PermissionMode:           binding.PermissionMode,
		RequestedBy:              binding.RequestedBy,
		OperationDigest:          operationDigest, DataSHA256: dataDigest,
		DataBytes: dataBytes, BytesWritten: bytesWritten,
		ProcessLocal: true, TokenPersisted: false, TokenExposed: false,
		RawInputPersisted: false, AutomaticRetryAllowed: false,
		CreatedAt: at,
	}
}

func (s *DebugTerminalAgentInputService) bindingEntry(
	ctx context.Context,
	bindingID string,
) (*debugTerminalBindingEntry, error) {
	s.mu.Lock()
	entry := s.bindings[bindingID]
	if entry == nil || entry.binding.Revoked {
		s.mu.Unlock()
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"debug terminal Agent-input binding is unavailable or expired")
	}
	expired := !s.now().UTC().Before(entry.binding.ExpiresAt)
	s.mu.Unlock()
	if expired {
		s.invalidateBindingWithAudit(ctx, entry)
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"debug terminal Agent-input binding is unavailable or expired")
	}
	return entry, nil
}

func (s *DebugTerminalAgentInputService) invalidateBinding(
	entry *debugTerminalBindingEntry,
) bool {
	if entry == nil {
		return false
	}
	if !s.removeBinding(entry) {
		return false
	}
	_, _ = s.bridge.Revoke(entry.binding.ID, entry.binding.RequestedBy, true)
	return true
}

func (s *DebugTerminalAgentInputService) removeBinding(
	entry *debugTerminalBindingEntry,
) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bindings[entry.binding.ID] != entry {
		return false
	}
	delete(s.bindings, entry.binding.ID)
	return true
}

func (s *DebugTerminalAgentInputService) invalidateBindingWithAudit(
	ctx context.Context,
	entry *debugTerminalBindingEntry,
) bool {
	if entry == nil {
		return false
	}
	if !s.invalidateBinding(entry) {
		return false
	}
	binding := entry.binding
	binding.Revoked = true
	_ = s.store.RecordDebugTerminalAgentInputAudit(ctx,
		debugTerminalAuditRecord(binding,
			terminalruntime.AgentInputAuditRevoked, "", "", 0, 0,
			s.now().UTC()))
	return true
}

func (s *DebugTerminalAgentInputService) removePendingOperation(
	bindingID string,
	operationDigest string,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.bindings[bindingID]; entry != nil {
		if operation := entry.operations[operationDigest]; operation.state == debugTerminalOperationPending {
			delete(entry.operations, operationDigest)
		}
	}
}

func (s *DebugTerminalAgentInputService) markOperationUncertain(
	bindingID string,
	operationDigest string,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.bindings[bindingID]; entry != nil {
		operation := entry.operations[operationDigest]
		operation.state = debugTerminalOperationUncertain
		entry.operations[operationDigest] = operation
	}
}

func (s *DebugTerminalAgentInputService) bindingRuntimeCurrent(entry *debugTerminalBindingEntry, current debugTerminalAgentBindings) bool {
	return entry != nil && agentCodeRuntimeCurrent(s.capabilities, current.permission,
		entry.binding.PermissionSnapshotID, entry.permissionGeneration, entry.runtimeEpoch, entry.runFence)
}
