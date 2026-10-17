package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/toolgateway"
)

func workerMCPWorkspace(ctx context.Context, st any, ref domain.AgentAttemptRef, drydocks *RunWorktreeService) (*mcp.InvocationWorkspace, error) {
	worker, err := readWorkerExecutionContext(ctx, st, ref, drydocks)
	if err != nil {
		return nil, err
	}
	fingerprint := worker.Binding.RootFingerprint
	return &mcp.InvocationWorkspace{ScopeID: workerMCPScopeID(ref), Resolve: func(ctx context.Context) (mcp.WorkspaceRoot, error) {
		current, err := readWorkerExecutionContext(ctx, st, ref, drydocks)
		if err != nil {
			return mcp.WorkspaceRoot{}, err
		}
		if current.Binding.RootFingerprint != fingerprint {
			return mcp.WorkspaceRoot{}, mcpApprovalUnavailable("MCP worker workspace generation changed")
		}
		return mcp.WorkspaceRoot{Path: current.Files.Workspace.RootPath, Fingerprint: fingerprint}, nil
	}}, nil
}

func workerMCPScopeID(ref domain.AgentAttemptRef) string {
	return "worker-" + runmutation.Fingerprint("mcp_worker_scope.v1", ref.RunID, ref.AgentID, ref.AttemptID)
}

// Shared approval code consumes an intent projection, while the child source
// and its fingerprint retain their own ledger and actual actor identity.
func workerMCPIntent(c domain.SpecialistToolCall) domain.SupervisorToolCall {
	return domain.SupervisorToolCall{RunID: c.Ref.RunID, AgentID: c.Ref.AgentID, AttemptID: c.Ref.AttemptID, AgentAttemptID: c.Ref.AttemptID,
		AgentAttribution: domain.AgentAttributionRecorded, CallID: domain.SpecialistToolApprovalID(c.Ref, c.CallID), Turn: c.Round,
		ToolName: c.Name, PayloadJSON: c.PayloadJSON, AuthorityJSON: c.AuthorityJSON, Status: c.Status}
}

func readMCPApprovalIntent(ctx context.Context, st mcpApprovalStore, runID, callID string) (domain.SupervisorToolCall, bool, *domain.SpecialistToolCall, error) {
	if strings.HasPrefix(callID, "specialist-tool-") {
		workers, ok := st.(workerToolSourceStore)
		if !ok {
			return domain.SupervisorToolCall{}, false, nil, mcpApprovalUnavailable("worker MCP intent storage unavailable")
		}
		c, started, err := workers.GetSpecialistApprovalCall(ctx, runID, callID)
		return workerMCPIntent(c), started, &c, err
	}
	c, started, err := st.GetSupervisorApprovalCall(ctx, runID, callID)
	return c, started, nil, err
}

func (s mcpApprovalSource) fingerprint() string {
	if s.worker != nil {
		return domain.SpecialistToolApprovalFingerprint(*s.worker)
	}
	return mcp.OperationApprovalFingerprint(s.call)
}

func (s mcpApprovalSource) requester() string {
	if s.worker != nil {
		return "specialist_runner"
	}
	return "run_supervisor"
}

func (s *AgentRunner) preflightWorkerMCPApproval(ctx context.Context, c domain.SpecialistToolCall) (bool, *domain.SupervisorToolResult, error) {
	waiting, result, err := s.preflightMCPApproval(ctx, workerMCPIntent(c))
	if result != nil {
		result.CallID = c.CallID
	}
	return waiting, result, err
}

func (e *MCPClientToolExecutor) workerMCPDispatchApproval(ctx context.Context, scope toolgateway.MCPExecutionScope, payload toolgateway.MCPToolCallPayload) (*approval.Record, error) {
	workers, ok := e.store.(workerToolSourceStore)
	st, approvalStore := e.store.(mcpApprovalStore)
	if !ok || !approvalStore {
		return nil, mcpApprovalUnavailable("worker MCP durable source unavailable")
	}
	ref := domain.AgentAttemptRef{RunID: scope.RunID, AgentID: scope.AgentID, AttemptID: scope.AgentAttemptID}
	worker, err := readWorkerExecutionContext(ctx, e.store, ref, e.drydocks)
	if err != nil {
		return nil, err
	}
	if !worker.Child.Worker.AllowsTool(string(toolgateway.MCPToolCallTool)) {
		return nil, mcpApprovalUnavailable("worker MCP capability was revoked")
	}
	source, err := readMCPApprovalSource(ctx, st, scope.RunID, domain.SpecialistToolApprovalID(ref, scope.SpecialistToolCallID))
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(payload)
	_, canonical, err := toolgateway.NormalizeMCPToolPayload(raw)
	if err != nil {
		return nil, err
	}
	a := source.authority
	if a.MissionID != scope.MissionID || a.WorkspaceID != scope.WorkspaceID || a.PermissionMode != scope.PermissionMode || a.PermissionSnapshotID != scope.PermissionSnapshotID || a.PermissionRevision != scope.PermissionRevision ||
		a.PermissionGeneration != scope.PermissionGeneration || a.PermissionRuntimeEpoch != scope.PermissionRuntimeEpoch || a.RunAuthorizationFence != scope.RunAuthorizationFence || source.actorSessionID != scope.SessionID {
		return nil, mcpApprovalUnavailable("worker MCP authority changed")
	}
	_, err = workers.ValidateSpecialistToolSource(ctx, domain.SpecialistToolSource{Ref: ref, CallID: scope.SpecialistToolCallID,
		SessionID: scope.SessionID, WorkspaceID: scope.WorkspaceID, Name: string(toolgateway.MCPToolCallTool), PayloadJSON: string(canonical), AuthorityJSON: source.call.AuthorityJSON,
		ChargeID: scope.InvocationID, LeaseID: scope.LeaseID, LeaseGeneration: scope.LeaseGeneration, RequireStarted: true})
	if err != nil {
		return nil, err
	}
	record, err := st.GetApprovalByProposal(ctx, source.call.CallID)
	if errors.Is(err, sql.ErrNoRows) && scope.PolicyDecision.Approval != toolgateway.ApprovalPerCall {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !mcpApprovalMatches(record, source) || record.GrantID != "" {
		return nil, mcpApprovalUnavailable("worker MCP exact approval changed")
	}
	return &record, nil
}
