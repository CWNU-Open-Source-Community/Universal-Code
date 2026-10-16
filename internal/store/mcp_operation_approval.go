package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/toolgateway"
)

func validateMCPApprovalSourceTx(ctx context.Context, tx *sql.Tx, p approval.Proposal) error {
	return validateMCPApprovalSourceStateTx(ctx, tx, p, false)
}

func validateMCPApprovalSourceStateTx(ctx context.Context, tx *sql.Tx, p approval.Proposal, allowStarted bool) error {
	binding, bound, err := runBindingForSessionTx(ctx, tx, p.SessionID)
	if err != nil {
		return err
	}
	if !bound {
		return errors.New("MCP approval requires a bound Run session")
	}
	c, started, err := getSupervisorApprovalCallTx(ctx, tx, binding.RunID, p.ProposalID)
	if err != nil {
		return err
	}
	a, err := mcp.DecodeSupervisorCallAuthority(json.RawMessage(c.AuthorityJSON))
	if err != nil {
		return err
	}
	payload, canonical, err := toolgateway.NormalizeMCPToolPayload(json.RawMessage(c.PayloadJSON))
	if err != nil {
		return err
	}
	if (started && !allowStarted) || c.Status != domain.SupervisorToolPending || c.ToolName != mcp.OperationApprovalTool ||
		c.AgentAttribution == domain.AgentAttributionLegacyUnknown || c.AgentID == "" || c.AgentAttemptID != c.AttemptID ||
		a.Version != mcp.SupervisorOperationAuthorityVersion || a.RunID != c.RunID || a.ServerID != payload.ServerID ||
		string(canonical) != c.PayloadJSON || p.WorkspaceID != a.WorkspaceID || p.ToolName != mcp.OperationApprovalTool ||
		p.ActionClass != "mcp_server_and_tool" || p.Mode != "per_call" || p.Status != approval.StatusPending ||
		p.RequestedBy != "run_supervisor" || p.RequestFingerprint != mcp.OperationApprovalFingerprint(c) {
		return errors.New("MCP approval source is not the exact pending undispatched call")
	}
	var missionID, sessionID, workspaceID, runStatus, actorRun, actorAttempt, actorStatus string
	err = tx.QueryRowContext(ctx, `SELECT r.mission_id,r.session_id,COALESCE(m.workspace_id,''),r.status,
		a.run_id,a.active_attempt_id,a.status FROM runs r JOIN missions m ON m.id=r.mission_id
		JOIN agent_nodes a ON a.id=? WHERE r.id=?`, c.AgentID, c.RunID).
		Scan(&missionID, &sessionID, &workspaceID, &runStatus, &actorRun, &actorAttempt, &actorStatus)
	if err != nil {
		return err
	}
	if missionID != a.MissionID || sessionID != p.SessionID || workspaceID != a.WorkspaceID ||
		actorRun != c.RunID || actorAttempt != c.AgentAttemptID ||
		(actorStatus != string(domain.AgentRunning) && actorStatus != string(domain.AgentWaiting)) ||
		(domain.Run{Status: domain.RunStatus(runStatus)}).Terminal() {
		return errors.New("MCP approval Run or actor identity changed")
	}
	permission, err := getCurrentRunExecutionPermissionSnapshot(ctx, tx, c.RunID)
	if err != nil {
		return err
	}
	if permission.Validate() != nil || permission.ID != a.PermissionSnapshotID ||
		permission.Revision != a.PermissionRevision || permission.Mode != a.PermissionMode {
		return errors.New("MCP approval permission changed")
	}
	server, err := getMCPClientServer(ctx, tx, a.ServerID)
	if err != nil {
		return err
	}
	if !a.MatchesServer(server) || payload.CapabilityFingerprint != server.ApprovedCapabilityFingerprint {
		return errors.New("MCP approval reviewed registration changed")
	}
	if _, err := mcp.ValidateInvocation(server.Capabilities, payload.Method, payload.ToolName, payload.Arguments); err != nil {
		return errors.New("MCP approval operation is absent from the reviewed registration")
	}
	return nil
}

// Only negative results may settle an undispatched intent. This exception does
// not manufacture an execution start or authorize a successful operation.
func validatePendingOperationPreflightResultTx(ctx context.Context, tx *sql.Tx, call domain.SupervisorToolCall, result domain.SupervisorToolResult) (bool, error) {
	prefix, expired, fingerprint := "mcp", "mcp_authority_expired", mcp.OperationApprovalFingerprint
	switch call.ToolName {
	case mcp.OperationApprovalTool:
	case string(toolgateway.CommandRuntimeTool):
		prefix, expired, fingerprint = "command", "command_authority_expired", commandruntimeadapter.OperationApprovalFingerprint
	default:
		return false, nil
	}
	if !((result.Status == domain.SupervisorToolDenied && result.ErrorCode == "policy_denied") ||
		(result.Status == domain.SupervisorToolFailed && result.ErrorCode == expired)) {
		return false, nil
	}
	exact, started, err := getSupervisorApprovalCallTx(ctx, tx, call.RunID, call.CallID)
	if err != nil {
		return false, err
	}
	if started || exact.Status != domain.SupervisorToolPending {
		return false, nil
	}
	var envelope struct {
		Version  string            `json:"version"`
		Tool     string            `json:"tool"`
		Status   string            `json:"status"`
		Code     string            `json:"code"`
		Metadata map[string]string `json:"metadata"`
	}
	return json.Unmarshal([]byte(result.ResultJSON), &envelope) == nil && envelope.Version == "supervisor_tool_result.v1" &&
		envelope.Tool == exact.ToolName && envelope.Status == string(result.Status) && envelope.Code == result.ErrorCode &&
		envelope.Metadata[prefix+"_preflight"] == "not_dispatched" &&
		envelope.Metadata[prefix+"_source_fingerprint"] == fingerprint(exact), nil
}
