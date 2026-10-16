package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
)

// This is a native host seam, not part of the plugin Operation contract. Input
// pins are prepared before the immutable Supervisor call enters its ledger.
type commandRuntimeAuthorityBinder interface {
	BindCommandToolAuthority(context.Context, toolgateway.ToolName, json.RawMessage, json.RawMessage) (json.RawMessage, error)
}

func (s *AgentRunner) bindCommandRuntimeCalls(ctx context.Context, calls []llm.ToolCall) ([]llm.ToolCall, error) {
	for i := range calls {
		if !toolgateway.IsCommandExecutionTool(toolgateway.ToolName(calls[i].Name)) {
			continue
		}
		a, err := commandruntimeadapter.DecodeAuthority(calls[i].Authority)
		if err != nil {
			return nil, err
		}
		if a.ProtocolVersion != commandruntimeadapter.OperationAuthorityVersion {
			continue
		}
		host, ok := s.commandRuntime.(commandRuntimeAuthorityBinder)
		if !ok {
			return nil, errors.New("command runtime input pinning is unavailable")
		}
		calls[i].Authority, err = host.BindCommandToolAuthority(ctx, toolgateway.ToolName(calls[i].Name), calls[i].Authority, calls[i].Arguments)
		if err != nil {
			return nil, err
		}
	}
	return calls, nil
}

func (s *CommandRuntimeService) SetCommandRuntimePolicy(checker policy.Checker) {
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	s.checker = checker
}

func (s *CommandRuntimeService) commandRuntimePolicy() policy.Checker {
	s.policyMu.RLock()
	defer s.policyMu.RUnlock()
	return s.checker
}
func (m *CommandRuntimeMultiplexer) SetCommandRuntimePolicy(checker policy.Checker) {
	for _, service := range m.adapters {
		service.SetCommandRuntimePolicy(checker)
	}
}

func (m *CommandRuntimeMultiplexer) BindCommandRuntimeAuthority(ctx context.Context, raw, payload json.RawMessage) (json.RawMessage, error) {
	return m.BindCommandToolAuthority(ctx, toolgateway.CommandRuntimeTool, raw, payload)
}
func (m *CommandRuntimeMultiplexer) BindCommandToolAuthority(ctx context.Context, name toolgateway.ToolName, raw, payload json.RawMessage) (json.RawMessage, error) {
	a, err := commandruntimeadapter.DecodeAuthority(raw)
	if err != nil {
		return nil, err
	}
	for _, host := range m.adapters {
		if host.adapter.SameBackend(a.Adapter) {
			return host.BindCommandToolAuthority(ctx, name, raw, payload)
		}
	}
	return nil, apperror.New(apperror.CodeConflict, "command runtime adapter changed")
}

func (s *CommandRuntimeService) BindCommandRuntimeAuthority(ctx context.Context, raw, payload json.RawMessage) (json.RawMessage, error) {
	return s.BindCommandToolAuthority(ctx, toolgateway.CommandRuntimeTool, raw, payload)
}
func (s *CommandRuntimeService) BindCommandToolAuthority(ctx context.Context, name toolgateway.ToolName, raw, payload json.RawMessage) (json.RawMessage, error) {
	a, err := commandruntimeadapter.DecodeAuthority(raw)
	if err != nil || a.ProtocolVersion != commandruntimeadapter.OperationAuthorityVersion {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "command runtime operation authority is required")
	}
	original := a
	if name == toolgateway.RunTestsTool {
		i, _, err := toolgateway.NormalizeRunTestsPayload(payload)
		if err != nil {
			return nil, err
		}
		prepared, err := s.prepareProjectTest(ctx, a, i)
		if err != nil {
			return nil, err
		}
		a.ProjectTestCommand, err = json.Marshal(prepared)
		if err != nil {
			return nil, err
		}
		if len(original.ProjectTestCommand) != 0 && !bytes.Equal(original.ProjectTestCommand, a.ProjectTestCommand) {
			return nil, errors.New("project test preset changed")
		}
	}
	input, canonical, err := toolgateway.CommandToolInput(name, payload, a)
	if err != nil || !bytes.Equal(canonical, payload) {
		return nil, errors.New("command runtime input is not canonical")
	}
	_, bindings, err := s.commandRuntimeAuthorityBindingsForTest(ctx, a, name == toolgateway.RunTestsTool)
	if err != nil {
		return nil, err
	}
	prepared := a
	prepared.ScopeFingerprint = commandRuntimeBindingFingerprint(bindings, s.adapter)
	prepared.CommandFingerprints = nil
	prepared.JobFingerprint = ""
	for _, spec := range input.Commands {
		resolved, err := s.normalizeCommandRuntimeSpec(spec, bindings.rootPath)
		if err != nil {
			return nil, err
		}
		resolved, err = s.bindOriginalAttachmentInputs(ctx, bindings, resolved)
		if err != nil {
			return nil, err
		}
		prepared.CommandFingerprints = append(prepared.CommandFingerprints, runner.CommandRuntimeSpecFingerprint(resolved))
	}
	if input.JobID != "" {
		job, err := s.authorizeJob(ctx, input.JobID, bindings)
		if err != nil {
			return nil, err
		}
		if input.Action == toolgateway.CommandRuntimeActionWriteStdin {
			if _, err = s.authorizeActiveJob(ctx, input.JobID, bindings); err != nil {
				return nil, err
			}
		}
		prepared.JobFingerprint = commandRuntimeJobFingerprint(job)
	}
	// No proposal, execution start or process is created by preparing these pins.
	encoded, err := commandruntimeadapter.EncodeAuthority(prepared)
	if err != nil {
		return nil, err
	}
	if a.ScopeFingerprint != "" && !bytes.Equal(raw, encoded) {
		return nil, apperror.New(apperror.CodeConflict, "command runtime reviewed inputs or host scope changed")
	}
	return encoded, nil
}

func (s *CommandRuntimeService) commandRuntimeAuthorityBindings(ctx context.Context, a commandruntimeadapter.Authority) (toolgateway.CommandRuntimeContext, commandRuntimeBindings, error) {
	return s.commandRuntimeAuthorityBindingsForTest(ctx, a, false)
}
func (s *CommandRuntimeService) commandRuntimeAuthorityBindingsForTest(ctx context.Context, a commandruntimeadapter.Authority, projectTest bool) (toolgateway.CommandRuntimeContext, commandRuntimeBindings, error) {
	var scope toolgateway.CommandRuntimeContext
	var bindings commandRuntimeBindings
	if !s.commandRuntimeAdapterCurrent() || !s.adapter.SameBackend(a.Adapter) {
		return scope, bindings, errors.New("command runtime adapter changed")
	}
	bindings, err := s.loadCommandRuntimeBindings(ctx, a.RunID)
	if err != nil {
		return scope, bindings, err
	}
	if !bindings.rootFound {
		return scope, bindings, errors.New("command runtime root is unavailable")
	}
	if !commandRuntimeAuthorityCurrent(s.capabilities, a, bindings.permission) {
		return scope, bindings, errors.New("command runtime permission or revocation binding changed")
	}
	if !bindings.leaseFound {
		return scope, bindings, errors.New("command runtime lease is unavailable")
	}
	run, mission, root, lease := bindings.run, bindings.mission, bindings.root, bindings.lease
	scope = toolgateway.CommandRuntimeContext{ProjectTest: projectTest, RunID: run.ID, MissionID: mission.ID, SessionID: run.SessionID,
		WorkspaceID: mission.WorkspaceID, RootAgentID: root.ID, AgentID: root.ID, AgentAttemptID: root.ActiveAttemptID,
		PermissionSnapshotID: a.PermissionSnapshotID, PermissionMode: a.PermissionMode, PermissionRevision: a.PermissionRevision,
		PermissionGeneration: a.PermissionGeneration, PermissionRuntimeEpoch: a.PermissionRuntimeEpoch, RunAuthorizationFence: a.RunAuthorizationFence,
		LeaseID: lease.LeaseID, LeaseGeneration: lease.Generation, RequestedBy: "run_supervisor", Adapter: s.adapter}
	bindings, err = s.validateAuthorizedBindings(ctx, scope, false, bindings)
	return scope, bindings, err
}

func commandRuntimeBindingFingerprint(b commandRuntimeBindings, adapter commandruntimeadapter.Identity) string {
	// Lease ownership is reacquired for the same waiting turn; snapshot, actor,
	// adapter, runtime fence and original input pins cannot be refreshed that way.
	raw, _ := json.Marshal(struct {
		Run, Mission, Session, Workspace, Root, RootSHA, Mode, Profile, Permission string
		ModeRevision, ProfileRevision, PermissionRevision                          int64
		Adapter                                                                    commandruntimeadapter.Identity
	}{b.run.ID, b.mission.ID, b.run.SessionID, b.workspace.ID, b.root.ID, b.rootSHA256,
		b.mode.ID, b.profile.ID, b.permission.ID, b.mode.Revision, b.profile.Revision, b.permission.Revision, adapter})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func commandRuntimeJobFingerprint(job runner.CommandRuntimeJob) string {
	raw, _ := json.Marshal(struct {
		ID, Request, Spec, Owner string
		Generation               int64
	}{
		job.ID, job.RequestFingerprint, job.SpecFingerprint, job.OwnerID, job.OwnerGeneration})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func commandRuntimeMutates(input toolgateway.CommandRuntimeInput) bool {
	return input.Action == toolgateway.CommandRuntimeActionRun || input.Action == toolgateway.CommandRuntimeActionStart || input.Action == toolgateway.CommandRuntimeActionWriteStdin
}

func commandSourceMatchesScope(source commandApprovalSource, scope toolgateway.CommandRuntimeContext, bindings commandRuntimeBindings, adapter commandruntimeadapter.Identity) bool {
	a := source.authority
	return source.started && (source.call.ToolName == string(toolgateway.RunTestsTool)) == scope.ProjectTest && source.call.AgentID == scope.AgentID && source.call.AgentAttemptID == scope.AgentAttemptID &&
		source.run.SessionID == scope.SessionID && source.workspaceID == scope.WorkspaceID &&
		a.Adapter.SameBackend(adapter) && a.ScopeFingerprint == commandRuntimeBindingFingerprint(bindings, adapter) &&
		a.PermissionSnapshotID == scope.PermissionSnapshotID && a.PermissionMode == bindings.permission.Mode &&
		a.PermissionGeneration == scope.PermissionGeneration && a.PermissionRuntimeEpoch == scope.PermissionRuntimeEpoch && a.RunAuthorizationFence == scope.RunAuthorizationFence &&
		runmutation.SupervisorToolExecutionKey(source.call.RunID, source.call.Turn, source.call.ToolName, source.call.PayloadJSON, source.call.CallID) == scope.OperationKey
}

func (s *CommandRuntimeService) checkPreparedCommandCall(ctx context.Context, scope toolgateway.CommandRuntimeContext, bindings commandRuntimeBindings, input toolgateway.CommandRuntimeInput) error {
	if !bindings.permission.Mode.IsApprovalMode() || scope.RequestedBy != "run_supervisor" {
		return nil
	}
	st, ok := s.store.(commandApprovalStore)
	if !ok || scope.SupervisorToolCallID == "" {
		return errors.New("command runtime prepared call is required")
	}
	source, err := readCommandApprovalSource(ctx, st, scope.RunID, scope.SupervisorToolCallID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(input)
	if err != nil || !bytes.Equal(raw, mustCommandInputJSON(source.input)) || !commandSourceMatchesScope(source, scope, bindings, s.adapter) {
		return errors.New("command runtime prepared call differs from dispatch")
	}
	_, err = s.BindCommandToolAuthority(ctx, toolgateway.ToolName(source.call.ToolName), json.RawMessage(source.call.AuthorityJSON), json.RawMessage(source.call.PayloadJSON))
	if err == nil && !commandRuntimeMutates(input) {
		return s.checkCommandLifecycleReview(ctx, st, source)
	}
	return err
}

func mustCommandInputJSON(i toolgateway.CommandRuntimeInput) []byte {
	raw, _ := json.Marshal(i)
	return raw
}
