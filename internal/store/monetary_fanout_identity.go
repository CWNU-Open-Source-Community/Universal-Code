package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
)

func fanoutMonetaryEvidenceTx(ctx context.Context, tx *sql.Tx, runID string, number int64,
	provider, model string,
) (monetaryModelEvidence, error) {
	// Started/abandoned calls and legacy keys without an exact terminal
	// receipt retain their exposure. Absence of a receipt is not free usage.
	result := monetaryModelEvidence{Sent: true}
	rows, err := tx.QueryContext(ctx, `SELECT c.execution_id,c.shard_ordinal,c.attempt_number,
		c.error_code,e.type,e.payload_json FROM readonly_fanout_model_calls c
		JOIN run_events e ON e.run_id=c.run_id AND e.source='readonly_fanout'
		AND json_extract(e.payload_json,'$.execution_id')=c.execution_id
		AND json_extract(e.payload_json,'$.shard')=c.shard_ordinal
		AND json_extract(e.payload_json,'$.attempt')=c.attempt_number
		AND json_extract(e.payload_json,'$.provider')=c.provider
		AND json_extract(e.payload_json,'$.model')=c.model
		WHERE c.run_id=? AND c.provider=? AND c.model=? AND
		((c.status='completed' AND e.type=?) OR (c.status='failed' AND e.type=?) OR
		(c.status='cancelled' AND e.type=?))`, runID, provider, model,
		events.ReadOnlyFanoutShardCompletedEvent, events.ReadOnlyFanoutShardFailedEvent,
		events.ReadOnlyFanoutShardCancelledEvent)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	var matchedCode string
	for rows.Next() {
		var executionID, code, eventType, payload string
		var ordinal, attempt int
		if err := rows.Scan(&executionID, &ordinal, &attempt, &code, &eventType, &payload); err != nil {
			return result, err
		}
		if int64(llm.ReadOnlyFanoutMonetaryAttemptNumber(executionID, ordinal, attempt)) != number {
			continue
		}
		if result.EventType != "" {
			// An ambiguous legacy-sized hash cannot transfer another call's usage.
			return monetaryModelEvidence{Sent: true}, nil
		}
		result.EventType, result.PayloadJSON, matchedCode = eventType, payload, code
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if matchedCode == "request_not_dispatched" {
		var payload struct {
			Usage *llm.Usage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(result.PayloadJSON), &payload); err != nil {
			return result, err
		}
		if result.EventType != events.ReadOnlyFanoutShardFailedEvent || payload.Usage == nil || *payload.Usage != (llm.Usage{}) {
			return result, apperror.New(apperror.CodeFailedPrecondition, "invalid not-dispatched fan-out monetary evidence")
		}
		result.Sent, result.NotSent = false, true
	}
	return result, nil
}
