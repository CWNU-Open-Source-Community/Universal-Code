package store

import (
	"context"
	"cyberagent-workbench/internal/domain"
)

// SpecialistToolApprovalState observes an intentional wait without requiring
// its expired execution lease. Only ResumeSpecialistToolAttempt can dispatch it.
func (s *SQLiteStore) SpecialistToolApprovalState(ctx context.Context, ref domain.AgentAttemptRef) (waiting, pending bool, err error) {
	if err = ref.Validate(); err != nil {
		return
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*)>0,COALESCE(SUM(CASE WHEN p.status='pending' THEN 1 ELSE 0 END),0)>0 FROM specialist_tool_calls c JOIN agent_attempts a ON a.id=c.agent_attempt_id JOIN agent_nodes n ON n.id=a.agent_id JOIN tool_approvals p ON p.id=c.approval_id WHERE a.id=? AND a.run_id=? AND a.agent_id=? AND a.status='running' AND n.active_attempt_id=a.id AND n.status='running' AND c.status='pending' AND c.started_at IS NULL`, ref.AttemptID, ref.RunID, ref.AgentID).Scan(&waiting, &pending)
	return
}
