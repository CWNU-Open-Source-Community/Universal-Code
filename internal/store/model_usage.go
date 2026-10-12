package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
)

// GetModelUsage uses the immutable model terminal receipts, which retain the
// full normalized Usage even when relational checkpoints only store totals.
func (s *SQLiteStore) GetModelUsage(ctx context.Context, runID string) (domain.ModelUsage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT json_extract(payload_json,'$.usage') FROM run_events WHERE run_id=? AND
		((source IN ('model_gateway','specialist_model_gateway') AND type IN ('model.completed','model.failed'))
		OR (source='readonly_fanout' AND type IN ('readonly_fanout.shard_completed','readonly_fanout.shard_failed','readonly_fanout.shard_cancelled')))
		AND COALESCE(json_extract(payload_json,'$.dispatch'),'')<>'not_sent'
		AND json_type(payload_json,'$.usage')='object' ORDER BY sequence`, runID)
	if err != nil {
		return domain.ModelUsage{}, err
	}
	defer rows.Close()
	var result domain.ModelUsage
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return domain.ModelUsage{}, err
		}
		var u llm.Usage
		if err := json.Unmarshal([]byte(raw), &u); err != nil {
			return domain.ModelUsage{}, err
		}
		input, output, total, err := supervisorUsage(u)
		if err != nil {
			return domain.ModelUsage{}, err
		}
		for _, counter := range []struct {
			target *int64
			value  int64
		}{
			{&result.InputTokens, input}, {&result.OutputTokens, output}, {&result.TotalTokens, total},
			{&result.CacheReadTokens, int64(u.CacheReadTokens)}, {&result.CacheCreationTokens, int64(u.CacheCreationTokens)},
			{&result.CacheCreation5mTokens, int64(u.CacheCreation5mTokens)}, {&result.CacheCreation1hTokens, int64(u.CacheCreation1hTokens)},
		} {
			*counter.target, err = supervisorAddCounter(*counter.target, counter.value, "model usage")
			if err != nil {
				return domain.ModelUsage{}, err
			}
		}
		result.Calls++
		if u.CacheUsageReported {
			result.CacheUsageReportedCalls++
		}
	}
	return result, rows.Err()
}

func sameModelCacheUsage(a, b *llm.Usage) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.CacheReadTokens == b.CacheReadTokens && a.CacheCreationTokens == b.CacheCreationTokens &&
		a.CacheCreation5mTokens == b.CacheCreation5mTokens && a.CacheCreation1hTokens == b.CacheCreation1hTokens &&
		a.CacheCreationBreakdownKnown == b.CacheCreationBreakdownKnown && a.CacheUsageReported == b.CacheUsageReported
}

func requireFanoutCacheUsageReplayTx(ctx context.Context, tx *sql.Tx, runID, executionID string, ordinal, attempt int, usage *llm.Usage) error {
	var encoded sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT json_extract(payload_json,'$.usage') FROM run_events
		WHERE run_id=? AND source='readonly_fanout' AND subject_id=? AND json_extract(payload_json,'$.attempt')=?
		AND type IN ('readonly_fanout.shard_completed','readonly_fanout.shard_failed','readonly_fanout.shard_cancelled')`,
		runID, readOnlyFanoutShardSubject(executionID, ordinal), attempt).Scan(&encoded); err != nil {
		return err
	}
	var prior *llm.Usage
	if encoded.Valid {
		prior = &llm.Usage{}
		if err := json.Unmarshal([]byte(encoded.String), prior); err != nil {
			return err
		}
	} else if usage != nil {
		// Earlier fan-out events retained only relational input/output/total.
		// Their absence of cache details remains compatible with plain usage.
		prior = &llm.Usage{}
	}
	if !sameModelCacheUsage(prior, usage) {
		return apperror.New(apperror.CodeConflict, "read-only fan-out cache usage replay differs from its durable record")
	}
	return nil
}
