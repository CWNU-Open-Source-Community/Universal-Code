package store

import (
	"context"
	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"database/sql"
	"errors"
)

func (s *SQLiteStore) SpecialistCostLimit(ctx context.Context, runID, attemptID string) (int64, error) {
	var limit int64
	err := s.db.QueryRowContext(ctx, `SELECT n.cost_limit_micros FROM agent_nodes n JOIN agent_attempts a ON a.agent_id=n.id WHERE a.id=? AND a.run_id=?`, attemptID, runID).Scan(&limit)
	return limit, err
}

func specialistReservationAgentTx(ctx context.Context, tx *sql.Tx, r domain.MonetaryReserveRequest) (string, int64, error) {
	if r.Scope != domain.MonetaryScopeSpecialist {
		return "", 0, nil
	}
	var agentID string
	var limit int64
	err := tx.QueryRowContext(ctx, `SELECT n.id,n.cost_limit_micros FROM run_events e JOIN agent_attempts a ON a.id=json_extract(e.payload_json,'$.agent_attempt_id') JOIN agent_nodes n ON n.id=a.agent_id WHERE e.run_id=? AND a.run_id=e.run_id AND e.source='specialist_model_gateway' AND e.type='model.started' AND json_extract(e.payload_json,'$.monetary_attempt_number')=? AND json_extract(e.payload_json,'$.provider')=? AND json_extract(e.payload_json,'$.model')=?`, r.RunID, r.AttemptNumber, r.Provider, r.Model).Scan(&agentID, &limit)
	if errors.Is(err, sql.ErrNoRows) {
		if r.AttemptNumber >= 1<<61 {
			return "", 0, apperror.New(apperror.CodeFailedPrecondition, "child cost reservation requires its durable model start")
		}
		return "", 0, nil
	}
	return agentID, limit, err
}
