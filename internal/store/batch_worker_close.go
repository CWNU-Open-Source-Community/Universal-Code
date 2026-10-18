package store

import (
	"context"
	"database/sql"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
)

// CloseTerminalBatchWorkers releases nodes and reservations only after a durable
// cancellation or completed integration. Active cancelled attempts are retained
// as interrupted; integration never invents another model completion.
func (s *SQLiteStore) CloseTerminalBatchWorkers(ctx context.Context, planID string) ([]domain.AgentNode, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	plan, found, err := getBatchDeliveryPlanTx(ctx, tx, planID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, apperror.New(apperror.CodeNotFound, "isolated delivery was not found")
	}
	if !plan.Status.Terminal() {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "close requires cancelled or integrated isolated delivery")
	}
	integrated := plan.Status == domain.BatchDeliveryCompleted
	reason := "isolated delivery cancelled; worker closed"
	if integrated {
		reason = "isolated delivery integrated; worker closed"
		var completed int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM batch_delivery_merge_queues WHERE plan_id=? AND status='completed'`, planID).Scan(&completed); err != nil {
			return nil, err
		}
		if completed != 1 {
			return nil, apperror.New(apperror.CodeFailedPrecondition, "close requires completed isolated integration")
		}
	}
	nodes := []domain.AgentNode{}
	changed := false
	now := time.Now().UTC()
	run, _, err := getCoordinatorRunTx(ctx, tx, plan.RunID)
	if err != nil {
		return nil, err
	}
	for _, task := range plan.Spec.Tasks {
		workspace, found, err := getBatchDeliveryWorkspaceTx(ctx, tx, planID, task.Ordinal)
		if err != nil {
			return nil, err
		}
		if !found || (integrated && workspace.Status != domain.BatchWorkspaceMerged) {
			return nil, apperror.New(apperror.CodeConflict, "terminal Batch worker binding changed")
		}
		node, err := scanAgentNode(tx.QueryRowContext(ctx, agentNodeSelect+` WHERE id=?`, workspace.AgentID))
		if err != nil {
			return nil, err
		}
		if node.RunID != plan.RunID || node.ParentID != plan.RootAgentID || node.Role != domain.AgentRoleSpecialist {
			return nil, apperror.New(apperror.CodeConflict, "terminal Batch worker parent changed")
		}
		if !node.Terminal() {
			if integrated && (node.ActiveAttemptID != "" || (node.Status != domain.AgentReady && node.Status != domain.AgentWaiting)) {
				return nil, apperror.New(apperror.CodeConflict, "integrated worker is still executing")
			}
			if node.ActiveAttemptID != "" {
				if _, err := interruptAgentAttemptTx(ctx, tx, run, node, "batch_delivery_cancelled", reason, now); err != nil {
					return nil, err
				}
			}
			result, err := tx.ExecContext(ctx, `UPDATE agent_nodes SET status='cancelled',active_attempt_id='',status_reason=?,finished_at=?,updated_at=?,version=version+1 WHERE id=? AND version=? AND active_attempt_id=?`, reason, ts(now), ts(now), node.ID, node.Version, node.ActiveAttemptID)
			if err != nil {
				return nil, err
			}
			if err := requireSingleAgentAttemptUpdate(result, "terminal Batch worker changed before close"); err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE sessions SET status='archived',updated_at=? WHERE id=?`, ts(now), node.SessionID); err != nil {
				return nil, err
			}
			node.Status, node.StatusReason, node.UpdatedAt, node.FinishedAt = domain.AgentCancelled, reason, now, &now
			node.ActiveAttemptID = ""
			node.Version++
			if err := appendSupervisorEventTx(ctx, tx, run, events.AgentStatusChangedEvent, "batch_delivery", node.ID, map[string]any{"to": node.Status, "reason": node.StatusReason, "plan_id": plan.ID, "version": node.Version}); err != nil {
				return nil, err
			}
			changed = true
		}
		nodes = append(nodes, node)
	}
	if changed {
		if _, err := createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
			return nil, err
		}
	}
	return nodes, tx.Commit()
}
