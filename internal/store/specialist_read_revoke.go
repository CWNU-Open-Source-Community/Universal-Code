package store

import (
	"context"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/session"
)

func (s *SQLiteStore) RevokeSpecialistReadTools(ctx context.Context, runID, agentID string) (domain.AgentNode, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.AgentNode{}, err
	}
	defer tx.Rollback()
	node, err := scanAgentNode(tx.QueryRowContext(ctx, agentNodeSelect+` WHERE id=?`, agentID))
	if err != nil {
		return domain.AgentNode{}, err
	}
	if node.RunID != runID || node.Role != domain.AgentRoleSpecialist {
		return domain.AgentNode{}, apperror.New(apperror.CodeInvalidArgument, "read revocation requires a child in this Run")
	}
	if len(node.AllowedReadPaths) > 0 || node.Worker != nil {
		run, _, err := getCoordinatorRunTx(ctx, tx, runID)
		if err != nil {
			return domain.AgentNode{}, err
		}
		now := time.Now().UTC()
		// An undispatched approval wait has no live runner to observe revocation.
		// End that attempt here so its child capacity and reservations are released.
		var waiting bool
		if node.Worker != nil && node.Status == domain.AgentRunning {
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM specialist_tool_calls WHERE agent_attempt_id=? AND status='pending' AND started_at IS NULL AND approval_id<>'')`, node.ActiveAttemptID).Scan(&waiting); err != nil {
				return domain.AgentNode{}, err
			}
		}
		if waiting {
			const reason = "worker tools revoked during approval wait"
			if _, err = interruptAgentAttemptTx(ctx, tx, run, node, "worker_tools_revoked", reason, now); err != nil {
				return domain.AgentNode{}, err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE agent_nodes SET status='cancelled',active_attempt_id='',status_reason=?,finished_at=?,version=version+1,updated_at=? WHERE id=?`, reason, ts(now), ts(now), node.ID); err != nil {
				return domain.AgentNode{}, err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE sessions SET status=?,updated_at=? WHERE id=?`, session.StatusArchived, ts(now), node.SessionID); err != nil {
				return domain.AgentNode{}, err
			}
			updated, err := scanAgentNode(tx.QueryRowContext(ctx, agentNodeSelect+` WHERE id=?`, node.ID))
			if err != nil {
				return domain.AgentNode{}, err
			}
			if err = appendAgentAttemptStatusEventTx(ctx, tx, run, node, updated, node.ActiveAttemptID); err != nil {
				return domain.AgentNode{}, err
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE agent_nodes SET allowed_read_paths_json='[]',worker_profile_json='null',version=version+1,updated_at=? WHERE id=?`, ts(now), agentID); err != nil {
			return domain.AgentNode{}, err
		}
		if err = appendSupervisorEventTx(ctx, tx, run, "agent.read_tools.revoked", "operator", agentID, map[string]any{"agent_id": agentID}); err != nil {
			return domain.AgentNode{}, err
		}
		if _, err = createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
			return domain.AgentNode{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return domain.AgentNode{}, err
	}
	return s.GetAgentNode(ctx, agentID)
}
