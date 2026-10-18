package application

import (
	"context"
	"encoding/json"
	"errors"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolgateway"
)

type workerHistoryToolStore interface {
	HistoryRecallToolStore
	workerToolSourceStore
	GetSpecialistToolBinding(context.Context, domain.AgentAttemptRef) (domain.SpecialistToolBinding, error)
	GetRunExecutionPermission(context.Context, string) (domain.RunExecutionPermissionSnapshot, error)
	SearchWorkerHistory(context.Context, domain.AgentAttemptRef, domain.HistorySearchRequest) (domain.HistorySearchResult, error)
	ReadWorkerHistory(context.Context, domain.AgentAttemptRef, domain.HistoryReadRequest) (domain.HistoryReadResult, error)
}

func (e *HistoryRecallToolExecutor) WithExecutionPermissionCapabilities(caps domain.ExecutionPermissionRuntimeCapabilities) *HistoryRecallToolExecutor {
	if e != nil {
		e.executionCapabilities = caps
	}
	return e
}

func (s *AgentRunner) workerHistoryToolAuthority(ctx context.Context, ref domain.AgentAttemptRef) (json.RawMessage, error) {
	st, ok := s.store.(workerHistoryToolStore)
	if !ok {
		return nil, errors.New("worker history storage is unavailable")
	}
	child, err := st.GetAgentNode(ctx, ref.AgentID)
	if err != nil {
		return nil, err
	}
	binding, err := st.GetSpecialistToolBinding(ctx, ref)
	if err != nil {
		return nil, err
	}
	profile, err := domain.WorkerProfileJSON(child.Worker)
	if err != nil || child.Worker == nil || child.RunID != ref.RunID || child.ActiveAttemptID != ref.AttemptID ||
		child.Role != domain.AgentRoleSpecialist || child.Status != domain.AgentRunning || binding.Mode != "worker" ||
		binding.SessionID != child.SessionID || binding.WorkerProfileJSON != profile {
		return nil, apperror.New(apperror.CodePolicyDenied, "worker history actor or delegation changed")
	}
	permission, err := st.GetRunExecutionPermission(ctx, ref.RunID)
	if err != nil {
		return nil, err
	}
	id, generation, epoch, fence, live := bindAgentCodeRuntime(s.executionCapabilities, permission)
	if !live {
		return nil, apperror.New(apperror.CodePolicyDenied, "worker history permission is no longer current")
	}
	return json.Marshal(toolgateway.WorkerHistoryAuthority{Version: "worker-history.v1", RunID: ref.RunID,
		AgentID: ref.AgentID, AttemptID: ref.AttemptID, ParentAgentID: child.ParentID,
		SessionID: child.SessionID, WorkspaceID: binding.WorkspaceID, WorkerProfileJSON: profile,
		BriefFingerprint: binding.BriefFingerprint, PermissionMode: permission.Mode,
		PermissionRevision: permission.Revision, PermissionSnapshotID: id, PermissionGeneration: generation,
		PermissionRuntimeEpoch: epoch, RunAuthorizationFence: fence})
}

func (e *HistoryRecallToolExecutor) validateWorkerHistoryScope(ctx context.Context, call toolgateway.ToolCall) (workerHistoryToolStore, domain.AgentAttemptRef, error) {
	ref := domain.AgentAttemptRef{RunID: call.RunID, AgentID: call.AgentID, AttemptID: call.AgentAttemptID}
	st, ok := e.store.(workerHistoryToolStore)
	if !ok || !toolgateway.IsHistoryRecallTool(call.Name) || call.RequestedBy != "specialist_runner" ||
		call.Role != domain.AgentRoleSpecialist || call.SpecialistToolCallID == "" || call.InvocationID == "" {
		return nil, ref, apperror.New(apperror.CodeFailedPrecondition, "worker history requires its charged durable call")
	}
	c, err := st.GetSpecialistToolCall(ctx, ref, call.SpecialistToolCallID)
	if err != nil {
		return nil, ref, err
	}
	a, err := toolgateway.DecodeWorkerHistoryAuthority(json.RawMessage(c.AuthorityJSON))
	if err != nil {
		return nil, ref, err
	}
	child, err := st.GetAgentNode(ctx, ref.AgentID)
	if err != nil {
		return nil, ref, err
	}
	parent, err := st.GetAgentNode(ctx, child.ParentID)
	if err != nil {
		return nil, ref, err
	}
	b, err := st.GetSpecialistToolBinding(ctx, ref)
	if err != nil {
		return nil, ref, err
	}
	permission, err := st.GetRunExecutionPermission(ctx, call.RunID)
	if err != nil {
		return nil, ref, err
	}
	profile, err := domain.WorkerProfileJSON(child.Worker)
	if err != nil || a.RunID != ref.RunID || a.AgentID != ref.AgentID || a.AttemptID != ref.AttemptID ||
		a.ParentAgentID != child.ParentID || call.RootAgentID != child.ParentID || a.SessionID != child.SessionID ||
		a.SessionID != call.SessionID || a.WorkspaceID != call.WorkspaceID || a.WorkspaceID != b.WorkspaceID ||
		a.WorkerProfileJSON != profile || a.WorkerProfileJSON != b.WorkerProfileJSON || a.BriefFingerprint != b.BriefFingerprint ||
		parent.RunID != ref.RunID || parent.Role != domain.AgentRoleRoot || parent.ParentID != "" || parent.Terminal() ||
		child.Worker == nil || !child.Worker.AllowsTool(string(call.Name)) ||
		a.PermissionMode != permission.Mode || a.PermissionRevision != permission.Revision ||
		call.PermissionMode != a.PermissionMode || call.PermissionRevision != a.PermissionRevision ||
		call.PermissionSnapshotID != a.PermissionSnapshotID || call.PermissionGeneration != a.PermissionGeneration ||
		call.PermissionRuntimeEpoch != a.PermissionRuntimeEpoch || call.RunAuthorizationFence != a.RunAuthorizationFence ||
		!agentCodeRuntimeCurrent(e.executionCapabilities, permission, a.PermissionSnapshotID, a.PermissionGeneration, a.PermissionRuntimeEpoch, a.RunAuthorizationFence) {
		return nil, ref, apperror.New(apperror.CodePolicyDenied, "worker history delegation or current permission changed")
	}
	_, err = st.ValidateSpecialistToolSource(ctx, domain.SpecialistToolSource{Ref: ref,
		CallID: call.SpecialistToolCallID, SessionID: call.SessionID, WorkspaceID: call.WorkspaceID,
		Name: string(call.Name), PayloadJSON: string(call.Payload), AuthorityJSON: c.AuthorityJSON,
		ChargeID: call.InvocationID, LeaseID: call.LeaseID, LeaseGeneration: call.LeaseGeneration, RequireStarted: true})
	return st, ref, err
}

func (e *HistoryRecallToolExecutor) executeWorkerHistoryRecall(ctx context.Context, call toolgateway.ToolCall,
	payload json.RawMessage,
) (json.RawMessage, bool, error) {
	st, ref, err := e.validateWorkerHistoryScope(ctx, call)
	if err != nil {
		return nil, false, err
	}
	var value any
	var more bool
	if call.Name == toolgateway.HistorySearchTool {
		var request domain.HistorySearchRequest
		if err := json.Unmarshal(payload, &request); err != nil {
			return nil, false, err
		}
		result, err := st.SearchWorkerHistory(ctx, ref, request)
		if err != nil {
			return nil, false, err
		}
		value, more = result, result.HasMore
	} else {
		var request domain.HistoryReadRequest
		if err := json.Unmarshal(payload, &request); err != nil {
			return nil, false, err
		}
		result, err := st.ReadWorkerHistory(ctx, ref, request)
		if err != nil {
			return nil, false, err
		}
		value, more = result, result.HasMore
	}
	if _, _, err := e.validateWorkerHistoryScope(ctx, call); err != nil {
		return nil, false, err
	}
	raw, err := json.Marshal(value)
	return raw, more, err
}
