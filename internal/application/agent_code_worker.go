package application

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspace"
)

type agentCodeWorkerStore interface {
	GetSpecialistToolBinding(context.Context, domain.AgentAttemptRef) (domain.SpecialistToolBinding, error)
	GetSpecialistToolCall(context.Context, domain.AgentAttemptRef, string) (domain.SpecialistToolCall, error)
	ValidateSpecialistToolSource(context.Context, domain.SpecialistToolSource) (domain.SpecialistToolCall, error)
	SetSpecialistFileEditApproval(context.Context, domain.AgentAttemptRef, string, string) (domain.SpecialistToolCall, error)
}

func (e *AgentCodeToolExecutor) BindSpecialistAgentCodeAuthority(ctx context.Context, ref domain.AgentAttemptRef, name toolgateway.ToolName, rawAuthority, payload json.RawMessage) (json.RawMessage, error) {
	_, ok := e.store.(agentCodeWorkerStore)
	if !ok {
		return nil, errors.New("worker file authority store is unavailable")
	}
	a, err := toolgateway.DecodeAgentCodeCallAuthority(rawAuthority)
	if err != nil {
		return nil, err
	}
	child, err := e.store.GetAgentNode(ctx, ref.AgentID)
	if err != nil {
		return nil, err
	}
	if a.Worker == nil {
		run, err := e.store.GetRun(ctx, ref.RunID)
		if err != nil {
			return nil, err
		}
		if a.SessionID != run.SessionID || a.RootAgentID != child.ParentID || a.Role != domain.AgentRoleRoot {
			return nil, errors.New("worker file authority does not descend from its parent")
		}
		a.Worker = &toolgateway.AgentCodeWorkerAuthority{AgentID: ref.AgentID, AttemptID: ref.AttemptID, SessionID: child.SessionID}
		a.Role = domain.AgentRoleSpecialist
		a.SessionID = child.SessionID
	}
	if _, err = e.validateWorkerAgentCodeAuthority(ctx, ref, a, name); err != nil {
		return nil, err
	}
	_, err = toolgateway.NormalizeAgentCodePayload(name, payload)
	if err != nil {
		return nil, err
	}
	encoded, err := toolgateway.EncodeAgentCodeCallAuthority(a)
	if err != nil {
		return nil, err
	}
	// The runner pins this host result to the exact provider call before review.
	return encoded, nil
}

func (s *FileEditApplyService) checkWorkerFileEditSource(ctx context.Context, binding fileEditApplyBinding, request ApplyFileEditRequest) error {
	if request.WorkerSource == nil {
		if binding.edit.SessionID != binding.run.SessionID {
			return errors.New("worker FileEdit apply requires its recorded child source")
		}
		return nil
	}
	source := *request.WorkerSource
	st, ok := s.store.(interface {
		ValidateSpecialistToolSource(context.Context, domain.SpecialistToolSource) (domain.SpecialistToolCall, error)
	})
	if !ok {
		return errors.New("worker FileEdit source validator is unavailable")
	}
	c, err := st.ValidateSpecialistToolSource(ctx, source)
	if err != nil {
		return err
	}
	var payload struct {
		EditID string `json:"edit_id"`
	}
	if json.Unmarshal([]byte(c.PayloadJSON), &payload) != nil || payload.EditID != binding.edit.ID || source.SessionID != binding.edit.SessionID || source.Ref.AgentID != request.AppliedBy || source.LeaseID != request.LeaseID || source.LeaseGeneration != request.LeaseGeneration || (c.Name != string(toolgateway.WorkspaceApplyTool) && c.Name != string(toolgateway.WorkspaceDeleteTool)) {
		return errors.New("worker FileEdit intent differs from its prepared apply")
	}
	a, err := toolgateway.DecodeAgentCodeCallAuthority(json.RawMessage(c.AuthorityJSON))
	if err != nil {
		return err
	}
	if a.Worker == nil || a.Worker.AgentID != request.AppliedBy || a.Worker.AttemptID != source.Ref.AttemptID || a.Worker.SessionID != binding.edit.SessionID {
		return errors.New("worker FileEdit actor differs from its file authority")
	}
	permissionReader, ok := s.store.(interface {
		GetRunExecutionPermission(context.Context, string) (domain.RunExecutionPermissionSnapshot, error)
	})
	if !ok {
		return errors.New("worker FileEdit permission reader is unavailable")
	}
	permission, err := permissionReader.GetRunExecutionPermission(ctx, binding.run.ID)
	if err != nil {
		return err
	}
	if a.PermissionRevision != permission.Revision || a.PermissionMode != permission.Mode || !agentCodeRuntimeCurrent(s.executionCapabilities, permission, a.PermissionSnapshotID, a.PermissionGeneration, a.PermissionRuntimeEpoch, a.RunAuthorizationFence) {
		return errors.New("worker FileEdit permission changed")
	}
	return nil
}

func (e *AgentCodeToolExecutor) validateWorkerAgentCodeAuthority(ctx context.Context, ref domain.AgentAttemptRef, a toolgateway.AgentCodeCallAuthority, name toolgateway.ToolName) (RunFileWorkspace, error) {
	var files RunFileWorkspace
	st, ok := e.store.(agentCodeWorkerStore)
	if !ok {
		return files, errors.New("worker file authority store is unavailable")
	}
	if err := a.Validate(); err != nil {
		return files, err
	}
	child, err := e.store.GetAgentNode(ctx, ref.AgentID)
	if err != nil {
		return files, err
	}
	root, err := e.store.GetAgentNode(ctx, a.RootAgentID)
	if err != nil {
		return files, err
	}
	run, err := e.store.GetRun(ctx, ref.RunID)
	if err != nil {
		return files, err
	}
	mission, err := e.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return files, err
	}
	linked, err := e.store.GetSession(ctx, child.SessionID)
	if err != nil {
		return files, err
	}
	b, err := st.GetSpecialistToolBinding(ctx, ref)
	if err != nil {
		return files, err
	}
	profile, err := domain.WorkerProfileJSON(child.Worker)
	if err != nil {
		return files, err
	}
	if a.Worker == nil || a.Worker.AgentID != ref.AgentID || a.Worker.AttemptID != ref.AttemptID || a.Worker.SessionID != child.SessionID || a.SessionID != child.SessionID || a.RunID != run.ID || a.MissionID != mission.ID || a.WorkspaceID != mission.WorkspaceID || child.RunID != run.ID || child.ParentID != root.ID || child.Role != domain.AgentRoleSpecialist || child.Status != domain.AgentRunning || child.ActiveAttemptID != ref.AttemptID || root.RunID != run.ID || root.Role != domain.AgentRoleRoot || root.ParentID != "" || root.Terminal() || run.Status != domain.RunRunning || linked.Status != session.StatusActive || linked.WorkspaceID != mission.WorkspaceID || b.Mode != "worker" || b.BatchPlanID != "" || b.SessionID != child.SessionID || b.WorkspaceID != mission.WorkspaceID || b.WorkerProfileJSON != profile || !child.Worker.AllowsTool(string(name)) {
		return files, apperror.New(apperror.CodePolicyDenied, "worker file actor or delegated authority changed")
	}
	files, err = ResolveRunFileWorkspace(ctx, e.store, run, mission, e.drydocks)
	if err != nil {
		return files, err
	}
	fingerprint, err := workspace.AgentCodeRootFingerprint(files.Workspace.RootPath)
	if err != nil {
		return files, err
	}
	mode, err := e.store.GetRunMode(ctx, run.ID)
	if err != nil {
		return files, err
	}
	permission, err := e.store.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		return files, err
	}
	if a.RootFingerprint != fingerprint || b.RootFingerprint != fingerprint || a.ModeRevision != mode.Revision || a.Surface != mode.Surface || a.Phase != mode.Phase || a.Profile != mode.Profile || a.PermissionRevision != permission.Revision || a.PermissionMode != permission.Mode || !agentCodeRuntimeCurrent(e.executionCapabilities, permission, a.PermissionSnapshotID, a.PermissionGeneration, a.PermissionRuntimeEpoch, a.RunAuthorizationFence) {
		return files, apperror.New(apperror.CodeConflict, "worker file permission or execution workspace changed")
	}
	catalogue := toolgateway.AgentCodeCapabilities(toolgateway.AgentCodeCapabilityContext{RunID: run.ID, MissionID: mission.ID, RootAgentID: root.ID, WorkspaceID: mission.WorkspaceID, RootFingerprint: fingerprint, Surface: mode.Surface, Phase: mode.Phase, Role: domain.AgentRoleRoot, Profile: mode.Profile, PermissionMode: permission.Mode, PermissionSnapshotID: a.PermissionSnapshotID, PermissionGeneration: a.PermissionGeneration, PermissionRuntimeEpoch: a.PermissionRuntimeEpoch, RunAuthorizationFence: a.RunAuthorizationFence, ModeRevision: mode.Revision, PermissionRevision: permission.Revision})
	if catalogue.Generation != a.CapabilityGeneration {
		return files, apperror.New(apperror.CodeConflict, "worker parent file catalogue changed")
	}
	for _, tool := range catalogue.Tools {
		if tool.Name == name && tool.Available {
			return files, nil
		}
	}
	return files, apperror.New(apperror.CodePolicyDenied, "worker file tool is unavailable in the parent catalogue")
}

func (e *AgentCodeToolExecutor) workerAgentCodeCall(ctx context.Context, scope toolgateway.AgentCodeExecutionScope, name toolgateway.ToolName, started bool) (domain.SpecialistToolCall, error) {
	st, ok := e.store.(agentCodeWorkerStore)
	if !ok {
		return domain.SpecialistToolCall{}, errors.New("worker file source reader is unavailable")
	}
	ref := domain.AgentAttemptRef{RunID: scope.RunID, AgentID: scope.AgentID, AttemptID: scope.AgentAttemptID}
	c, err := st.GetSpecialistToolCall(ctx, ref, scope.SpecialistToolCallID)
	if err != nil {
		return c, err
	}
	a, err := toolgateway.DecodeAgentCodeCallAuthority(json.RawMessage(c.AuthorityJSON))
	if err != nil {
		return c, err
	}
	files, err := e.validateWorkerAgentCodeAuthority(ctx, ref, a, name)
	if err != nil {
		return c, err
	}
	if c.Name != string(name) || scope.Role != domain.AgentRoleSpecialist || scope.RootAgentID != a.RootAgentID || scope.MissionID != a.MissionID || scope.SessionID != a.SessionID || scope.ControlWorkspaceID() != a.WorkspaceID || scope.WorkspaceID != files.Workspace.ID || scope.WorkspaceRoot != files.Workspace.RootPath || scope.RootFingerprint != a.RootFingerprint || scope.Surface != a.Surface || scope.Phase != a.Phase || scope.Profile != a.Profile || scope.ModeRevision != a.ModeRevision || scope.PermissionMode != a.PermissionMode || scope.PermissionRevision != a.PermissionRevision || scope.PermissionSnapshotID != a.PermissionSnapshotID || scope.PermissionGeneration != a.PermissionGeneration || scope.PermissionRuntimeEpoch != a.PermissionRuntimeEpoch || scope.RunAuthorizationFence != a.RunAuthorizationFence || scope.CapabilityGeneration != a.CapabilityGeneration || (started && c.ChargeID != scope.InvocationID) {
		return c, apperror.New(apperror.CodeConflict, "worker file scope differs from its pinned call")
	}
	return st.ValidateSpecialistToolSource(ctx, domain.SpecialistToolSource{Ref: ref, CallID: c.CallID, SessionID: scope.SessionID, WorkspaceID: scope.ControlWorkspaceID(), Name: c.Name, PayloadJSON: c.PayloadJSON, AuthorityJSON: c.AuthorityJSON, ChargeID: c.ChargeID, LeaseID: scope.LeaseID, LeaseGeneration: scope.LeaseGeneration, RequireStarted: started})
}

func (e *AgentCodeToolExecutor) workerFileApplySource(ctx context.Context, scope toolgateway.AgentCodeExecutionScope, name toolgateway.ToolName) (*domain.SpecialistToolSource, error) {
	if scope.RequestedBy != "specialist_runner" {
		return nil, nil
	}
	c, err := e.workerAgentCodeCall(ctx, scope, name, true)
	if err != nil {
		return nil, err
	}
	return &domain.SpecialistToolSource{Ref: c.Ref, CallID: c.CallID, SessionID: scope.SessionID, WorkspaceID: scope.ControlWorkspaceID(), Name: c.Name, PayloadJSON: c.PayloadJSON, AuthorityJSON: c.AuthorityJSON, ChargeID: c.ChargeID, LeaseID: scope.LeaseID, LeaseGeneration: scope.LeaseGeneration, RequireStarted: true}, nil
}

func (e *AgentCodeToolExecutor) PreflightSpecialistFileEditApproval(ctx context.Context, ref domain.AgentAttemptRef, callID string) (bool, *domain.SupervisorToolResult, error) {
	st, ok := e.store.(agentCodeWorkerStore)
	if !ok {
		return false, nil, errors.New("worker file approval store is unavailable")
	}
	c, err := st.GetSpecialistToolCall(ctx, ref, callID)
	if err != nil {
		return false, nil, err
	}
	if c.Name != string(toolgateway.WorkspaceApplyTool) && c.Name != string(toolgateway.WorkspaceDeleteTool) {
		return false, nil, nil
	}
	a, err := toolgateway.DecodeAgentCodeCallAuthority(json.RawMessage(c.AuthorityJSON))
	if err != nil {
		return false, nil, err
	}
	files, err := e.validateWorkerAgentCodeAuthority(ctx, ref, a, toolgateway.ToolName(c.Name))
	if err != nil {
		return false, nil, err
	}
	var input struct {
		EditID string `json:"edit_id"`
		Action string `json:"action"`
	}
	if json.Unmarshal([]byte(c.PayloadJSON), &input) != nil {
		return false, nil, errors.New("worker file apply lacks its edit")
	}
	if c.Name == string(toolgateway.WorkspaceDeleteTool) && input.Action == "propose" {
		return false, nil, nil
	}
	if input.EditID == "" {
		return false, nil, errors.New("worker file apply lacks its edit")
	}
	edit, err := e.store.GetFileEdit(ctx, input.EditID)
	if err != nil {
		return false, nil, err
	}
	if edit.SessionID != a.SessionID || edit.WorkspaceID != files.Workspace.ID {
		return false, nil, errors.New("worker cannot review another Agent's file proposal")
	}
	record, err := e.store.GetApprovalByProposal(ctx, edit.ID)
	if err != nil {
		return false, nil, err
	}
	if record.RunID != ref.RunID || record.SessionID != a.SessionID || record.WorkspaceID != edit.WorkspaceID || record.RequestFingerprint != fileedit.ApprovalFingerprint(edit.SessionID, edit.WorkspaceID, edit) {
		return false, nil, errors.New("worker file review differs from its exact proposal")
	}
	if record.Mode == "automatic" {
		return false, nil, nil
	}
	if _, err = st.SetSpecialistFileEditApproval(ctx, ref, callID, record.ID); err != nil {
		return false, nil, err
	}
	if record.Status == approval.StatusPending {
		return true, nil, nil
	}
	if record.Status == approval.StatusDenied {
		raw, _ := json.Marshal(map[string]any{"status": "denied", "error_code": "policy_denied", "message": "The file proposal was denied.", "edit_id": edit.ID})
		return false, &domain.SupervisorToolResult{CallID: c.CallID, Status: domain.SupervisorToolDenied, ResultJSON: string(raw), ErrorCode: "policy_denied", CompletedAt: time.Now().UTC()}, nil
	}
	return false, nil, nil
}
