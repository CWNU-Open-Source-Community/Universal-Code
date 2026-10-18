package store

import (
	"context"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runmutation"
)

// ListWorkflowCandidates is paged by a stable checkpoint cursor, not an offset:
// dormant waiting records cannot hide runnable executions behind the first page.
func (s *SQLiteStore) ListWorkflowCandidates(ctx context.Context, afterTime time.Time, afterID string, now time.Time, limit int) ([]domain.WorkflowExecution, error) {
	if now.IsZero() || limit < 1 || limit > 128 {
		return nil, apperror.New(apperror.CodeInvalidArgument, "workflow candidate time or page bound is invalid")
	}
	rows, err := s.db.QueryContext(ctx, workflowExecutionSelect+` WHERE status IN ('running','waiting','waiting_approval') AND (claim_owner='' OR claim_until<=?) AND (updated_at>? OR (updated_at=? AND id>?)) ORDER BY updated_at,id LIMIT ?`, ts(now), ts(afterTime), ts(afterTime), afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []domain.WorkflowExecution{}
	for rows.Next() {
		execution, err := scanWorkflowExecution(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, execution)
	}
	return items, rows.Err()
}

// WorkflowWakeFingerprint observes dependencies in one read snapshot. Lease
// renewal timestamps are deliberately excluded: only busy/available can unblock
// a wait. Reading or polling a workflow never changes this fingerprint.
func (s *SQLiteStore) WorkflowWakeFingerprint(ctx context.Context, runID string, now time.Time) (string, error) {
	var state string
	err := s.db.QueryRowContext(ctx, `SELECT json_array(r.status,r.updated_at,
		(SELECT group_concat(value,'|') FROM (SELECT n.id||':'||n.version AS value FROM agent_nodes n WHERE n.run_id=r.id ORDER BY n.id)),
		(SELECT group_concat(value,'|') FROM (SELECT p.id||':'||p.version AS value FROM tool_approvals p WHERE p.run_id=r.id ORDER BY p.id)),
		(SELECT group_concat(value,'|') FROM (SELECT b.id||':'||b.status AS value FROM batch_delivery_plans b WHERE b.run_id=r.id ORDER BY b.id)),
		(SELECT group_concat(value,'|') FROM (SELECT w.plan_id||':'||w.ordinal||':'||w.generation||':'||w.status||':'||(w.lease_expires_at>?1) AS value FROM batch_delivery_workspaces w JOIN batch_delivery_plans b ON b.id=w.plan_id WHERE b.run_id=r.id ORDER BY w.plan_id,w.ordinal)),
		(SELECT group_concat(value,'|') FROM (SELECT q.id||':'||q.status||':'||q.next_index AS value FROM batch_delivery_merge_queues q JOIN batch_delivery_plans b ON b.id=q.plan_id WHERE b.run_id=r.id ORDER BY q.id)),
		EXISTS(SELECT 1 FROM run_execution_leases l WHERE l.run_id=r.id AND l.status='active' AND l.expires_at>?1)) FROM runs r WHERE r.id=?2`, ts(now), runID).Scan(&state)
	if err != nil {
		return "", err
	}
	return runmutation.Fingerprint(domain.WorkflowVersion, "wake", state), nil
}
