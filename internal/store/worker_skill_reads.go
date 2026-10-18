package store

import (
	"context"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/skills"
)

// Keep activation facts with the child that actually read the skill. Earlier
// attempts supply exact reread references, never another child's instructions.
func (s *SQLiteStore) ListWorkerSkillReadCalls(ctx context.Context, ref domain.AgentAttemptRef) ([]domain.SpecialistToolCall, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `WITH reads AS (
		SELECT c.agent_attempt_id,c.call_id,ROW_NUMBER() OVER (PARTITION BY CASE
			WHEN json_extract(c.payload_json,'$.installation_id') IS NOT NULL THEN
				json_array('installed',json_extract(c.payload_json,'$.installation_id'),json_extract(c.payload_json,'$.package_id'),json_extract(c.payload_json,'$.component_id'),json_extract(c.payload_json,'$.revision'))
			ELSE json_array('bundled',json_extract(c.payload_json,'$.name')) END
		ORDER BY a.turn_number DESC,c.round DESC,c.position DESC,c.rowid DESC) AS rank
		FROM specialist_tool_calls c JOIN agent_attempts a ON a.id=c.agent_attempt_id
		JOIN specialist_tool_bindings b ON b.agent_attempt_id=a.id AND b.mode='worker'
		WHERE a.run_id=? AND a.agent_id=? AND c.name='skill_read'
		AND c.status='completed' AND c.completed_at IS NOT NULL
		AND c.started_at IS NOT NULL AND c.charge_id<>'' AND c.authority_json<>''
		AND COALESCE(json_extract(c.payload_json,'$.resource'),'')=''
		AND COALESCE(json_extract(c.payload_json,'$.catalog'),0)=0
	) `+specialistToolCallSelect+` JOIN reads ON reads.agent_attempt_id=c.agent_attempt_id AND reads.call_id=c.call_id
		WHERE reads.rank=1 ORDER BY c.payload_json LIMIT ?`, ref.RunID, ref.AgentID, skills.MaxSelectionItems+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var calls []domain.SpecialistToolCall
	for rows.Next() {
		call, err := scanSpecialistToolCall(rows)
		if err != nil {
			return nil, err
		}
		calls = append(calls, call)
	}
	return calls, rows.Err()
}
