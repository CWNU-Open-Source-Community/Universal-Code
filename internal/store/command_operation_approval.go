package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolgateway"
)

func validateCommandApprovalSourceTx(ctx context.Context, tx *sql.Tx, p approval.Proposal) error {
	binding, bound, err := runBindingForSessionTx(ctx, tx, p.SessionID)
	if err != nil {
		return err
	}
	if !bound {
		return errors.New("command approval requires a bound Run session")
	}
	c, started, err := getSupervisorApprovalCallTx(ctx, tx, binding.RunID, p.ProposalID)
	if err != nil {
		return err
	}
	a, err := commandruntimeadapter.DecodeAuthority(json.RawMessage(c.AuthorityJSON))
	if err != nil {
		return err
	}
	input, canonical, err := toolgateway.CommandToolInput(toolgateway.ToolName(c.ToolName), json.RawMessage(c.PayloadJSON), a)
	if err != nil {
		return err
	}
	if started || c.Status != domain.SupervisorToolPending || !toolgateway.IsCommandExecutionTool(toolgateway.ToolName(c.ToolName)) ||
		c.AgentAttribution == domain.AgentAttributionLegacyUnknown || c.AgentID == "" || c.AgentAttemptID == "" || c.AgentAttemptID != c.AttemptID ||
		a.ProtocolVersion != commandruntimeadapter.OperationAuthorityVersion || a.RunID != c.RunID || a.ScopeFingerprint == "" ||
		len(a.CommandFingerprints) != len(input.Commands) || ((input.JobID == "") != (a.JobFingerprint == "")) ||
		string(canonical) != c.PayloadJSON || p.ToolName != string(toolgateway.CommandRuntimeTool) || p.ActionClass != "command_process" || p.Mode != "per_call" ||
		p.Status != approval.StatusPending || p.RequestedBy != "run_supervisor" || p.RequestFingerprint != commandruntimeadapter.OperationApprovalFingerprint(c) {
		return errors.New("command approval source is not the exact pinned undispatched call")
	}
	var sessionID, workspaceID, runStatus, actorRun, actorAttempt, actorStatus, actorRole string
	err = tx.QueryRowContext(ctx, `SELECT r.session_id,COALESCE(m.workspace_id,''),r.status,a.run_id,a.active_attempt_id,a.status,a.role
		FROM runs r JOIN missions m ON m.id=r.mission_id JOIN agent_nodes a ON a.id=? WHERE r.id=?`, c.AgentID, c.RunID).
		Scan(&sessionID, &workspaceID, &runStatus, &actorRun, &actorAttempt, &actorStatus, &actorRole)
	if err != nil {
		return err
	}
	if sessionID != p.SessionID || workspaceID != p.WorkspaceID || actorRun != c.RunID || actorAttempt != c.AgentAttemptID || actorRole != string(domain.AgentRoleRoot) ||
		(actorStatus != string(domain.AgentRunning) && actorStatus != string(domain.AgentWaiting)) || (domain.Run{Status: domain.RunStatus(runStatus)}).Terminal() {
		return errors.New("command approval Run or actor identity changed")
	}
	permission, err := getCurrentRunExecutionPermissionSnapshot(ctx, tx, c.RunID)
	if err != nil {
		return err
	}
	if permission.Validate() != nil || permission.ID != a.PermissionSnapshotID || permission.Revision != a.PermissionRevision || permission.Mode != a.PermissionMode {
		return errors.New("command approval permission changed")
	}
	return nil
}
