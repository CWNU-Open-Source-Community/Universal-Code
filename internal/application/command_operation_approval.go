package application

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/executionauth"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolcontract"
	"cyberagent-workbench/internal/toolgateway"
)

type commandApprovalStore interface {
	GetSupervisorApprovalCall(context.Context, string, string) (domain.SupervisorToolCall, bool, error)
	GetRun(context.Context, string) (domain.Run, error)
	GetMission(context.Context, string) (domain.Mission, error)
	GetAgentNode(context.Context, string) (domain.AgentNode, error)
	GetRunExecutionPermission(context.Context, string) (domain.RunExecutionPermissionSnapshot, error)
	GetApprovalByProposal(context.Context, string) (approval.Record, error)
	EnsureApproval(context.Context, approval.Proposal) (approval.Record, error)
}

type commandApprovalSource struct {
	call        domain.SupervisorToolCall
	authority   commandruntimeadapter.Authority
	input       toolgateway.CommandRuntimeInput
	permission  domain.RunExecutionPermissionSnapshot
	run         domain.Run
	workspaceID string
	started     bool
}

func readCommandApprovalSource(ctx context.Context, st commandApprovalStore, runID, callID string) (commandApprovalSource, error) {
	return readCommandSource(ctx, st, runID, callID, false)
}

// Only the native dispatch recheck may inspect a completed start receipt. Its
// caller must additionally prove the exact still-owned, running durable Job.
// Review/preflight/recovery keep using the pending-only reader above.
func readCommandSource(ctx context.Context, st commandApprovalStore, runID, callID string, allowCompletedStart bool) (commandApprovalSource, error) {
	var s commandApprovalSource
	var err error
	s.call, s.started, err = st.GetSupervisorApprovalCall(ctx, runID, callID)
	if err != nil {
		return s, err
	}
	c := s.call
	s.authority, err = commandruntimeadapter.DecodeAuthority(json.RawMessage(c.AuthorityJSON))
	if err != nil {
		return s, err
	}
	a := s.authority
	var canonical json.RawMessage
	s.input, canonical, err = toolgateway.CommandToolInput(toolgateway.ToolName(c.ToolName), json.RawMessage(c.PayloadJSON), a)
	if err != nil {
		return s, err
	}
	completedStart := allowCompletedStart && s.started && c.Status == domain.SupervisorToolCompleted &&
		c.CompletedAt != nil && c.ErrorCode == "" && s.input.Action == toolgateway.CommandRuntimeActionStart && len(s.input.Commands) == 1
	if !toolgateway.IsCommandExecutionTool(toolgateway.ToolName(c.ToolName)) || c.RunID != runID || c.CallID != callID ||
		(c.Status != domain.SupervisorToolPending && !completedStart) || c.AgentAttribution == domain.AgentAttributionLegacyUnknown ||
		c.AgentID == "" || c.AgentAttemptID == "" || c.AgentAttemptID != c.AttemptID ||
		a.ProtocolVersion != commandruntimeadapter.OperationAuthorityVersion || a.RunID != runID ||
		a.ScopeFingerprint == "" || c.PayloadJSON != string(canonical) ||
		len(a.CommandFingerprints) != len(s.input.Commands) || ((s.input.JobID == "") != (a.JobFingerprint == "")) {
		return s, pendingToolApprovalUnavailable("command approval requires the exact pinned pending call")
	}
	s.run, err = st.GetRun(ctx, runID)
	if err != nil {
		return s, err
	}
	mission, err := st.GetMission(ctx, s.run.MissionID)
	if err != nil {
		return s, err
	}
	s.workspaceID = mission.WorkspaceID
	actor, err := st.GetAgentNode(ctx, c.AgentID)
	if err != nil {
		return s, err
	}
	s.permission, err = st.GetRunExecutionPermission(ctx, runID)
	if err != nil {
		return s, err
	}
	if s.run.Terminal() || actor.RunID != runID || actor.Role != domain.AgentRoleRoot ||
		(!completedStart && (actor.ActiveAttemptID != c.AgentAttemptID || (actor.Status != domain.AgentRunning && actor.Status != domain.AgentWaiting))) ||
		s.permission.Validate() != nil || s.permission.ID != a.PermissionSnapshotID || s.permission.Revision != a.PermissionRevision || s.permission.Mode != a.PermissionMode {
		return s, pendingToolApprovalUnavailable("command approval actor, attempt or permission changed")
	}
	return s, ctx.Err()
}

func commandApprovalMatches(record approval.Record, source commandApprovalSource) bool {
	return record.Validate() == nil && record.RunID == source.call.RunID && record.ProposalID == source.call.CallID &&
		record.ToolName == string(toolgateway.CommandRuntimeTool) && record.SessionID == source.run.SessionID &&
		record.WorkspaceID == source.workspaceID && record.ActionClass == "command_process" && record.Mode == "per_call" &&
		record.RequestFingerprint == commandruntimeadapter.OperationApprovalFingerprint(source.call)
}

// Operator review binds the exact pending call. Filesystem inputs, adapter,
// any bounded grant, process epoch/fence and policy are rechecked at dispatch.
func RecheckCommandApproval(ctx context.Context, base ApprovalControlStore, record approval.Record) error {
	st, ok := base.(commandApprovalStore)
	if !ok {
		return pendingToolApprovalUnavailable("command approval source reader unavailable")
	}
	s, err := readCommandApprovalSource(ctx, st, record.RunID, record.ProposalID)
	if err != nil {
		return err
	}
	if s.started || !commandApprovalMatches(record, s) {
		return pendingToolApprovalUnavailable("command approval source changed or was already dispatched")
	}
	return nil
}

func (s *AgentRunner) preflightCommandApproval(ctx context.Context, call domain.SupervisorToolCall) (bool, *domain.SupervisorToolResult, error) {
	a, err := commandruntimeadapter.DecodeAuthority(json.RawMessage(call.AuthorityJSON))
	if err != nil {
		return false, nil, err
	}
	if a.ProtocolVersion != commandruntimeadapter.OperationAuthorityVersion {
		return false, nil, nil
	}
	st, ok := s.store.(commandApprovalStore)
	if !ok {
		return false, nil, pendingToolApprovalUnavailable("command approval storage unavailable")
	}
	stored, started, err := st.GetSupervisorApprovalCall(ctx, call.RunID, call.CallID)
	if err != nil {
		return false, nil, err
	}
	if commandruntimeadapter.OperationApprovalFingerprint(stored) != commandruntimeadapter.OperationApprovalFingerprint(call) {
		return false, nil, pendingToolApprovalUnavailable("command stored intent changed")
	}
	if started {
		return false, nil, nil
	} // existing outcome_unknown path, never resend
	stop := func(code, message string, status domain.SupervisorToolCallStatus) (bool, *domain.SupervisorToolResult, error) {
		result := commandPreflightResult(call, code, message, status)
		return false, &result, nil
	}
	source, err := readCommandApprovalSource(ctx, st, call.RunID, call.CallID)
	if err == nil {
		host, ok := s.commandRuntime.(commandRuntimeAuthorityBinder)
		if !ok {
			err = errors.New("command runtime authority reader unavailable")
		} else {
			_, err = host.BindCommandToolAuthority(ctx, toolgateway.ToolName(call.ToolName), json.RawMessage(call.AuthorityJSON), json.RawMessage(call.PayloadJSON))
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return false, nil, ctx.Err()
		}
		return stop("command_authority_expired", "The pinned command inputs or execution authority changed. No command was dispatched; reassess before proposing a new action.", domain.SupervisorToolFailed)
	}
	policy := toolgateway.CommandRuntimePolicyDecision(s.checker, source.input)
	if !policy.Allowed {
		return stop("policy_denied", "Current host policy denied this exact command action. It was not dispatched.", domain.SupervisorToolDenied)
	}
	value, err := st.GetApprovalByProposal(ctx, call.CallID)
	var record *approval.Record
	if err == nil {
		if !commandApprovalMatches(value, source) {
			return false, nil, pendingToolApprovalUnavailable("command approval decision identity changed")
		}
		record = &value
	} else if !errors.Is(err, sql.ErrNoRows) {
		return false, nil, err
	}
	if !commandRuntimeMutates(source.input) && !policy.NeedsApproval && record == nil {
		return false, nil, nil
	}
	ensure := func() error {
		now := time.Now().UTC()
		value, err = st.EnsureApproval(ctx, approval.Proposal{IdempotencyKey: approval.ProposalIdempotencyKey(string(toolgateway.CommandRuntimeTool), call.CallID),
			ProposalID: call.CallID, SessionID: source.run.SessionID, WorkspaceID: source.workspaceID,
			ToolName: string(toolgateway.CommandRuntimeTool), ActionClass: "command_process", Mode: "per_call", Status: approval.StatusPending,
			RequestFingerprint: commandruntimeadapter.OperationApprovalFingerprint(call), RequestedBy: "run_supervisor",
			DecisionReason: "Approve only these pinned commands or stdin bytes for the current Run-owned process. Host effects are not isolated by a working directory or network declaration.", CreatedAt: now, UpdatedAt: now})
		if err == nil {
			record = &value
		}
		return err
	}
	if record != nil {
		if err := checkCommandGrant(ctx, st, *record); err != nil {
			return stop("policy_denied", "The bounded command authorization is no longer valid. No process was dispatched.", domain.SupervisorToolDenied)
		}
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		return false, nil, err
	}
	network := runner.CommandRuntimeNetworkDisabled
	for _, command := range source.input.Commands {
		if command.Network == runner.CommandRuntimeNetworkHost {
			network = runner.CommandRuntimeNetworkHost
		}
	}
	operation, err := commandOperation(key, call.CallID, a.Adapter, call.PayloadJSON, network,
		[]toolcontract.Target{{Kind: "process", Locator: call.CallID}})
	if err != nil {
		return false, nil, err
	}
	waiting, allowed, err := decidePendingOperation(ctx, source.permission, executionauth.SubjectRef{RunID: call.RunID, ActorID: call.AgentID},
		operation, commandruntimeadapter.OperationApprovalFingerprint(call), &record, ensure, policy.NeedsApproval || source.input.ReviewScope != nil,
		a.Adapter.Kind == commandruntimeadapter.KindSandboxedWorkspace)
	if err != nil || waiting {
		return waiting, nil, err
	}
	if !allowed {
		return stop("policy_denied", "The operator denied this exact command action. It was not dispatched.", domain.SupervisorToolDenied)
	}
	return false, nil, nil
}

// Reading or containing an already-owned Job grants no new execution. Preserve
// that native path, while enforcing current denial and any exact review that
// was already recorded; a later policy relaxation cannot erase a pending review.
func (s *CommandRuntimeService) checkCommandLifecycleReview(ctx context.Context, st commandApprovalStore, source commandApprovalSource) error {
	decision := toolgateway.CommandRuntimePolicyDecision(s.commandRuntimePolicy(), source.input)
	if !decision.Allowed {
		return errors.New("current host policy denied the command lifecycle action")
	}
	value, err := st.GetApprovalByProposal(ctx, source.call.CallID)
	if errors.Is(err, sql.ErrNoRows) && !decision.NeedsApproval {
		return nil
	}
	if err != nil {
		return errors.Join(err, errors.New("command lifecycle action requires exact review"))
	}
	if !commandApprovalMatches(value, source) {
		return errors.New("command lifecycle review identity changed")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	operation, err := commandOperation(key, source.call.CallID, source.authority.Adapter, source.call.PayloadJSON, runner.CommandRuntimeNetworkDisabled,
		[]toolcontract.Target{{Kind: "process", Locator: source.call.CallID}})
	if err != nil {
		return err
	}
	proof := &value
	waiting, allowed, err := decidePendingOperation(ctx, source.permission, executionauth.SubjectRef{RunID: source.call.RunID, ActorID: source.call.AgentID},
		operation, commandruntimeadapter.OperationApprovalFingerprint(source.call), &proof,
		func() error { return errors.New("dispatch cannot create a new lifecycle review") }, decision.NeedsApproval, false)
	if err != nil || waiting || !allowed {
		return errors.Join(err, errors.New("command lifecycle review did not authorize dispatch"))
	}
	return ctx.Err()
}

func commandPreflightResult(call domain.SupervisorToolCall, code, message string, status domain.SupervisorToolCallStatus) domain.SupervisorToolResult {
	metadata := map[string]string{"command_preflight": "not_dispatched",
		"command_source_fingerprint": commandruntimeadapter.OperationApprovalFingerprint(call)}
	if code == "outcome_unknown" {
		metadata["command_preflight"] = "outcome_unknown"
		metadata["automatic_retry"] = "forbidden"
	}
	raw, _ := marshalSupervisorToolResultEnvelope(supervisorToolResultEnvelope{Version: supervisorToolResultVersion, Tool: call.ToolName,
		Status: string(status), Code: code, Message: message, Metadata: metadata})
	return domain.SupervisorToolResult{CallID: call.CallID, Status: status, ErrorCode: code, ResultJSON: string(raw), CompletedAt: time.Now().UTC()}
}

func commandCallOutcomeUnknown(call domain.SupervisorToolCall) bool {
	a, err := commandruntimeadapter.DecodeAuthority(json.RawMessage(call.AuthorityJSON))
	if err != nil || a.ProtocolVersion != commandruntimeadapter.OperationAuthorityVersion {
		return false
	}
	input, _, err := toolgateway.CommandToolInput(toolgateway.ToolName(call.ToolName), json.RawMessage(call.PayloadJSON), a)
	return err == nil && commandRuntimeMutates(input)
}
