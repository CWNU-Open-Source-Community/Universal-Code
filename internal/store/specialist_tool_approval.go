package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/toolgateway"
)

func validateSpecialistApprovalSourceTx(ctx context.Context, tx *sql.Tx, p approval.Proposal) error {
	b, bound, err := specialistApprovalRunBindingTx(ctx, tx, p.SessionID)
	if err != nil {
		return err
	}
	if !bound {
		return errors.New("child approval requires its admitted child Session")
	}
	c, started, err := getSpecialistApprovalCallTx(ctx, tx, b.RunID, p.ProposalID)
	if err != nil {
		return err
	}
	if started || c.Status != domain.SupervisorToolPending || c.AuthorityJSON == "" || p.WorkspaceID != b.WorkspaceID || p.Mode != "per_call" || p.Status != approval.StatusPending || p.RequestedBy != "specialist_runner" || p.RequestFingerprint != domain.SpecialistToolApprovalFingerprint(c) {
		return errors.New("child approval source differs from its pinned undispatched call")
	}
	if err := validateSpecialistApprovalPermissionTx(ctx, tx, b.RunID, c.AuthorityJSON); err != nil {
		return err
	}
	binding, err := validateSpecialistWorkerBindingTx(ctx, tx, c.Ref, c.Name)
	if err != nil {
		return err
	}
	if binding.SessionID != p.SessionID || binding.WorkspaceID != p.WorkspaceID {
		return errors.New("child approval Session or workspace differs")
	}
	var live int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM agent_nodes n JOIN agent_attempts a ON a.id=n.active_attempt_id JOIN runs r ON r.id=n.run_id WHERE n.id=? AND n.run_id=? AND n.session_id=? AND n.role='specialist' AND n.status='running' AND a.id=? AND a.status='running' AND r.status='running'`, c.Ref.AgentID, c.Ref.RunID, p.SessionID, c.Ref.AttemptID).Scan(&live)
	if err != nil {
		return err
	}
	if live != 1 {
		return errors.New("child approval actor or Run is no longer active")
	}
	switch p.ToolName {
	case string(toolgateway.CommandRuntimeTool):
		if !toolgateway.IsCommandExecutionTool(toolgateway.ToolName(c.Name)) || p.ActionClass != "command_process" {
			return errors.New("child command approval class differs")
		}
	case mcp.OperationApprovalTool:
		if c.Name != mcp.OperationApprovalTool || p.ActionClass != "mcp_server_and_tool" {
			return errors.New("child MCP approval class differs")
		}
	case toolgateway.AgentBrowserApprovalTool:
		canonical, err := toolgateway.NormalizeAgentBrowserPayload(toolgateway.ToolName(c.Name), json.RawMessage(c.PayloadJSON))
		if err != nil {
			return err
		}
		var payload toolgateway.AgentBrowserPayload
		if json.Unmarshal(canonical, &payload) != nil || payload.Sensitive == nil || p.ActionClass != "browser_"+payload.Sensitive.Effect {
			return errors.New("child browser approval effect differs")
		}
	default:
		return errors.New("this child tool has no operation approval adapter")
	}
	return nil
}

func validateSpecialistApprovalPermissionTx(ctx context.Context, tx *sql.Tx, runID, authorityJSON string) error {
	var pin struct {
		ID       string                            `json:"permission_snapshot_id"`
		Revision int64                             `json:"permission_revision"`
		Mode     domain.RunExecutionPermissionMode `json:"permission_mode"`
	}
	if json.Unmarshal([]byte(authorityJSON), &pin) != nil || pin.ID == "" || pin.Revision <= 0 {
		return errors.New("child approval lacks its pinned permission")
	}
	permission, err := getCurrentRunExecutionPermissionSnapshot(ctx, tx, runID)
	if err != nil {
		return err
	}
	if permission.Validate() != nil || permission.ID != pin.ID || permission.Revision != pin.Revision || permission.Mode != pin.Mode {
		return errors.New("child approval permission changed")
	}
	return nil
}
