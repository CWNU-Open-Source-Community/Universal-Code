package store

import (
	"context"

	"cyberagent-workbench/internal/domain"
)

// ListProjectVerificationCalls reads only one actor's command evidence. Native
// Job ownership and current execution permission are checked by the service.
func (s *SQLiteStore) ListProjectVerificationCalls(ctx context.Context, runID, agentID string) ([]domain.ProjectVerificationCall, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.call_id FROM run_supervisor_tool_calls c
	 JOIN run_supervisor_tool_call_agents a ON a.run_id=c.run_id AND a.turn=c.turn AND a.attempt_id=c.attempt_id AND a.call_id=c.call_id
	 WHERE c.run_id=? AND a.agent_id=? AND c.tool_name IN ('run_tests','command_runtime') ORDER BY c.created_at,c.turn,c.round,c.position`, runID, agentID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := make([]domain.ProjectVerificationCall, 0, len(ids))
	for _, id := range ids {
		call, _, err := s.GetSupervisorApprovalCall(ctx, runID, id)
		if err != nil {
			return nil, err
		}
		result = append(result, domain.ProjectVerificationCall{Supervisor: &call})
	}
	rows, err = s.db.QueryContext(ctx, specialistToolCallSelect+` WHERE a.run_id=? AND a.agent_id=? AND c.name IN ('run_tests','command_runtime') ORDER BY c.created_at,c.round,c.position`, runID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		call, err := scanSpecialistToolCall(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, domain.ProjectVerificationCall{Specialist: &call})
	}
	return result, rows.Err()
}
