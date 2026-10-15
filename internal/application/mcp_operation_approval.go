package application

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/executionauth"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/toolcontract"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/tools"
)

type mcpApprovalStore interface {
	GetSupervisorApprovalCall(context.Context, string, string) (domain.SupervisorToolCall, bool, error)
	GetRun(context.Context, string) (domain.Run, error)
	GetMission(context.Context, string) (domain.Mission, error)
	GetAgentNode(context.Context, string) (domain.AgentNode, error)
	GetRunExecutionPermission(context.Context, string) (domain.RunExecutionPermissionSnapshot, error)
	GetMCPClientServer(context.Context, string) (mcp.ServerRecord, error)
	GetApprovalByProposal(context.Context, string) (approval.Record, error)
	EnsureApproval(context.Context, approval.Proposal) (approval.Record, error)
	DecideApproval(context.Context, approval.DecisionRequest) (approval.DecisionResult, error)
}

type mcpApprovalSource struct {
	call       domain.SupervisorToolCall
	authority  mcp.SupervisorCallAuthority
	payload    toolgateway.MCPToolCallPayload
	permission domain.RunExecutionPermissionSnapshot
	server     mcp.ServerRecord
	run        domain.Run
	started    bool
}

func mcpApprovalUnavailable(message string) error {
	return apperror.New(apperror.CodeFailedPrecondition, message)
}

// A capability digest alone cannot identify the process/configuration the user
// reviewed. Read the exact immutable intent and its independently pinned host
// registration on every proposal, review and dispatch.
func readMCPApprovalSource(ctx context.Context, st mcpApprovalStore, runID, callID string) (mcpApprovalSource, error) {
	var source mcpApprovalSource
	var err error
	source.call, source.started, err = st.GetSupervisorApprovalCall(ctx, runID, callID)
	if err != nil {
		return source, err
	}
	c := source.call
	source.authority, err = mcp.DecodeSupervisorCallAuthority(json.RawMessage(c.AuthorityJSON))
	if err != nil {
		return source, err
	}
	a := source.authority
	var canonical json.RawMessage
	source.payload, canonical, err = toolgateway.NormalizeMCPToolPayload(json.RawMessage(c.PayloadJSON))
	if err != nil {
		return source, err
	}
	if c.ToolName != mcp.OperationApprovalTool || c.RunID != runID || c.CallID != callID ||
		c.Status != domain.SupervisorToolPending || c.AgentAttribution == domain.AgentAttributionLegacyUnknown ||
		c.AgentID == "" || c.AgentAttemptID == "" || c.AgentAttemptID != c.AttemptID ||
		a.Version != mcp.SupervisorOperationAuthorityVersion || a.RunID != c.RunID ||
		a.ServerID != source.payload.ServerID || c.PayloadJSON != string(canonical) {
		return source, mcpApprovalUnavailable("MCP approval requires the exact pending host-bound call")
	}
	source.run, err = st.GetRun(ctx, runID)
	if err != nil {
		return source, err
	}
	mission, err := st.GetMission(ctx, source.run.MissionID)
	if err != nil {
		return source, err
	}
	actor, err := st.GetAgentNode(ctx, c.AgentID)
	if err != nil {
		return source, err
	}
	source.permission, err = st.GetRunExecutionPermission(ctx, runID)
	if err != nil {
		return source, err
	}
	if source.run.Terminal() || source.run.MissionID != a.MissionID || mission.WorkspaceID != a.WorkspaceID ||
		actor.RunID != runID || actor.ActiveAttemptID != c.AgentAttemptID ||
		(actor.Status != domain.AgentRunning && actor.Status != domain.AgentWaiting) ||
		source.permission.ID != a.PermissionSnapshotID || source.permission.Revision != a.PermissionRevision ||
		source.permission.Mode != a.PermissionMode || source.permission.Validate() != nil {
		return source, mcpApprovalUnavailable("MCP approval actor, attempt or permission changed")
	}
	source.server, err = st.GetMCPClientServer(ctx, a.ServerID)
	if err != nil {
		return source, err
	}
	if !a.MatchesServer(source.server) || source.server.ApprovedCapabilityFingerprint != source.payload.CapabilityFingerprint {
		return source, mcpApprovalUnavailable("MCP approved registration or capability changed")
	}
	if _, err := mcp.ValidateInvocation(source.server.Capabilities, source.payload.Method, source.payload.ToolName, source.payload.Arguments); err != nil {
		return source, mcpApprovalUnavailable("MCP operation no longer matches the reviewed capability snapshot")
	}
	if ref := source.server.Descriptor.NativeSource; ref != nil {
		native, ok := st.(nativeMCPSourceStore)
		if !ok {
			return source, mcpApprovalUnavailable("native MCP source reader unavailable")
		}
		if err := (&NativeMCPSourceResolver{store: native}).Check(ctx, *ref, runID); err != nil {
			return source, err
		}
	}
	return source, ctx.Err()
}

func mcpApprovalMatches(record approval.Record, source mcpApprovalSource) bool {
	return record.Validate() == nil && record.RunID == source.call.RunID &&
		record.ProposalID == source.call.CallID && record.ToolName == mcp.OperationApprovalTool &&
		record.SessionID == source.run.SessionID && record.WorkspaceID == source.authority.WorkspaceID &&
		record.ActionClass == "mcp_server_and_tool" && record.Mode == "per_call" && record.GrantID == "" &&
		record.RequestFingerprint == mcp.OperationApprovalFingerprint(source.call)
}

// Shared by the approval controller and its read-only preview. This never
// creates a process grant; runtime activation is still checked at execution.
func RecheckMCPApproval(ctx context.Context, base ApprovalControlStore, record approval.Record) error {
	st, ok := base.(mcpApprovalStore)
	if !ok {
		return mcpApprovalUnavailable("MCP approval source reader unavailable")
	}
	source, err := readMCPApprovalSource(ctx, st, record.RunID, record.ProposalID)
	if err != nil {
		return err
	}
	if source.started || !mcpApprovalMatches(record, source) {
		return mcpApprovalUnavailable("MCP approval source changed or was already dispatched")
	}
	return nil
}

func (s *AgentRunner) preflightMCPApproval(ctx context.Context, call domain.SupervisorToolCall) (bool, *domain.SupervisorToolResult, error) {
	st, ok := s.store.(mcpApprovalStore)
	if !ok {
		return false, nil, mcpApprovalUnavailable("MCP approval storage unavailable")
	}
	stored, started, err := st.GetSupervisorApprovalCall(ctx, call.RunID, call.CallID)
	if err != nil {
		return false, nil, err
	}
	if mcp.OperationApprovalFingerprint(stored) != mcp.OperationApprovalFingerprint(call) {
		return false, nil, mcpApprovalUnavailable("MCP stored intent changed")
	}
	if started {
		return false, nil, nil
	} // Existing outcome_unknown path, never resend.
	stop := func(code, message string, status domain.SupervisorToolCallStatus) (bool, *domain.SupervisorToolResult, error) {
		result := mcpPreflightResult(call, code, message, status)
		return false, &result, nil
	}
	source, err := readMCPApprovalSource(ctx, st, call.RunID, call.CallID)
	if err != nil {
		if ctx.Err() != nil {
			return false, nil, ctx.Err()
		}
		return stop("mcp_authority_expired", "The exact MCP source or execution authority changed. This action was not dispatched; reassess before proposing a new action.", domain.SupervisorToolFailed)
	}
	generation, live := s.executionCapabilities.FullAccessGeneration(source.permission)
	if !live || generation != source.authority.PermissionGeneration ||
		!mcpRuntimeAuthorityCurrent(s.executionCapabilities, call.RunID, source.authority.RunAuthorizationFence, source.authority.PermissionRuntimeEpoch) {
		return stop("mcp_authority_expired", "MCP runtime activation or revocation fence is no longer current. This action was not dispatched.", domain.SupervisorToolFailed)
	}
	p := source.payload
	policy := s.checker.CheckToolCall(tools.Call{Name: mcp.OperationApprovalTool, Args: map[string]string{
		"server_id": p.ServerID, "tool_name": p.ToolName, "capability_fingerprint": p.CapabilityFingerprint, "arguments": string(p.Arguments)}})
	if !policy.Allowed {
		return stop("policy_denied", "Current host policy denied this exact MCP call. It was not dispatched.", domain.SupervisorToolDenied)
	}
	record, err := st.GetApprovalByProposal(ctx, call.CallID)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, nil, err
	}
	if exists && !mcpApprovalMatches(record, source) {
		return false, nil, mcpApprovalUnavailable("MCP approval decision identity changed")
	}
	var proof *approval.Record
	if exists {
		proof = &record
	}
	ensure := func() error {
		now := time.Now().UTC()
		record, err = st.EnsureApproval(ctx, approval.Proposal{
			IdempotencyKey: approval.ProposalIdempotencyKey(mcp.OperationApprovalTool, call.CallID), ProposalID: call.CallID,
			SessionID: source.run.SessionID, WorkspaceID: source.authority.WorkspaceID,
			ToolName: mcp.OperationApprovalTool, ActionClass: "mcp_server_and_tool", Mode: "per_call", Status: approval.StatusPending,
			RequestFingerprint: mcp.OperationApprovalFingerprint(call), RequestedBy: "run_supervisor",
			DecisionReason: "Approve this exact MCP server startup/discovery and tool call; its external effects are unverified.",
			CreatedAt:      now, UpdatedAt: now})
		if err == nil {
			proof = &record
		}
		return err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return false, nil, err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(mcp.OperationApprovalFingerprint(call)))
	component := toolcontract.ComponentRef{PackageID: mcp.ClientProtocolVersion, ComponentID: p.ServerID}
	if source.server.Descriptor.NativeSource != nil {
		component = source.server.Descriptor.NativeSource.Component
	}
	operation := toolcontract.Operation{ID: call.CallID, Kind: toolcontract.OperationToolCall, ToolID: p.ToolName,
		AdapterID: mcp.RuntimeAdapterID, AdapterRevision: mcp.RuntimeAdapterRevision, Component: component,
		InputFingerprint: hex.EncodeToString(mac.Sum(nil)), CapabilityFingerprint: p.CapabilityFingerprint,
		Targets: []toolcontract.Target{{Kind: "endpoint", Locator: p.ServerID}}, Effects: []toolcontract.Effect{toolcontract.EffectUnknown}}
	waiting, allowed, err := decidePendingOperation(ctx, source.permission,
		executionauth.SubjectRef{RunID: call.RunID, ActorID: call.AgentID}, operation,
		mcp.OperationApprovalFingerprint(call), &proof, ensure, policy.NeedsApproval, false)
	if err != nil || waiting {
		return waiting, nil, err
	}
	if !allowed {
		return stop("policy_denied", "The operator denied this exact MCP call. It was not dispatched.", domain.SupervisorToolDenied)
	}
	return false, nil, nil
}

func mcpPreflightResult(call domain.SupervisorToolCall, code, message string, status domain.SupervisorToolCallStatus) domain.SupervisorToolResult {
	raw, _ := marshalSupervisorToolResultEnvelope(supervisorToolResultEnvelope{Version: supervisorToolResultVersion, Tool: call.ToolName,
		Status: string(status), Code: code, Message: message,
		Metadata: map[string]string{"mcp_preflight": "not_dispatched", "mcp_source_fingerprint": mcp.OperationApprovalFingerprint(call)}})
	return domain.SupervisorToolResult{CallID: call.CallID, Status: status, ErrorCode: code, ResultJSON: string(raw), CompletedAt: time.Now().UTC()}
}
