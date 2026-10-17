package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

// GetRunAgentUsage rebuilds aggregate model usage from durable projections and
// ledgers. Projection disagreement is treated as corruption, not silently
// hidden by choosing one source.
func (s *SQLiteStore) GetRunAgentUsage(ctx context.Context,
	runID string,
) (domain.RunAgentUsage, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return domain.RunAgentUsage{},
			apperror.New(apperror.CodeInvalidArgument, "run Agent usage requires a Run id")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.RunAgentUsage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	usage, err := getRunAgentUsageTx(ctx, tx, runID)
	if err != nil {
		return domain.RunAgentUsage{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RunAgentUsage{}, err
	}
	return usage, nil
}

func getRunAgentUsageTx(ctx context.Context, tx *sql.Tx,
	runID string,
) (domain.RunAgentUsage, error) {
	var runCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE id = ?`, runID).
		Scan(&runCount); err != nil {
		return domain.RunAgentUsage{}, err
	}
	if runCount != 1 {
		return domain.RunAgentUsage{}, apperror.New(apperror.CodeNotFound, "Run was not found")
	}

	var rootCount int
	var rootTokens int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(tokens_used), 0)
		FROM agent_nodes WHERE run_id = ? AND role = 'root'`, runID).
		Scan(&rootCount, &rootTokens); err != nil {
		return domain.RunAgentUsage{}, err
	}
	if rootCount != 1 {
		return domain.RunAgentUsage{}, apperror.New(apperror.CodeConflict,
			"Run Agent usage requires exactly one root projection")
	}

	var checkpointTokens, rootExecutionMillis int64
	err := tx.QueryRowContext(ctx, `SELECT total_tokens, execution_millis
		FROM run_supervisor_checkpoints WHERE run_id = ?`, runID).
		Scan(&checkpointTokens, &rootExecutionMillis)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if rootTokens != 0 {
			return domain.RunAgentUsage{}, apperror.New(apperror.CodeConflict,
				"root Agent token projection exists without a Supervisor checkpoint")
		}
		checkpointTokens = 0
		rootExecutionMillis = 0
	case err != nil:
		return domain.RunAgentUsage{}, err
	case checkpointTokens != rootTokens:
		return domain.RunAgentUsage{}, apperror.New(apperror.CodeConflict,
			"root Agent token projection disagrees with the Supervisor checkpoint")
	}

	var specialistTokens int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(tokens_used), 0)
		FROM agent_nodes WHERE run_id = ? AND role = 'specialist'`, runID).
		Scan(&specialistTokens); err != nil {
		return domain.RunAgentUsage{}, err
	}
	var attemptTokens int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(total_tokens), 0)
		FROM agent_attempts WHERE run_id = ?`, runID).Scan(&attemptTokens); err != nil {
		return domain.RunAgentUsage{}, err
	}
	if specialistTokens != attemptTokens {
		return domain.RunAgentUsage{}, apperror.New(apperror.CodeConflict,
			"Specialist token projection disagrees with the attempt ledger")
	}

	var specialistExecutionMillis int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(elapsed_millis), 0)
		FROM specialist_model_calls WHERE run_id = ?`, runID).
		Scan(&specialistExecutionMillis); err != nil {
		return domain.RunAgentUsage{}, err
	}
	var readOnlyTokens, readOnlyExecutionMillis int64
	if err := tx.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN usage_recorded = 1 THEN total_tokens
			ELSE reserved_total_tokens END), 0),
		COALESCE(SUM(CASE WHEN elapsed_recorded = 1 THEN elapsed_millis
			ELSE reserved_millis END), 0)
		FROM readonly_fanout_model_calls WHERE run_id = ?`, runID).
		Scan(&readOnlyTokens, &readOnlyExecutionMillis); err != nil {
		return domain.RunAgentUsage{}, err
	}
	var contextTokens, contextMillis int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN usage_json<>'null' THEN json_extract(usage_json,'$.total_tokens') WHEN started_at IS NOT NULL THEN reserved_tokens ELSE 0 END),0),COALESCE(SUM(elapsed_millis),0) FROM thread_context_compactions WHERE run_id=?`, runID).Scan(&contextTokens, &contextMillis); err != nil {
		return domain.RunAgentUsage{}, err
	}
	if rootTokens > math.MaxInt64-specialistTokens ||
		rootTokens+specialistTokens > math.MaxInt64-readOnlyTokens ||
		rootTokens+specialistTokens+readOnlyTokens > math.MaxInt64-contextTokens ||
		rootExecutionMillis > math.MaxInt64-specialistExecutionMillis ||
		rootExecutionMillis+specialistExecutionMillis >
			math.MaxInt64-readOnlyExecutionMillis || rootExecutionMillis+specialistExecutionMillis+readOnlyExecutionMillis > math.MaxInt64-contextMillis {
		return domain.RunAgentUsage{}, apperror.New(apperror.CodeResourceExhausted,
			"Run Agent usage total exceeds the supported range")
	}
	usage := domain.RunAgentUsage{
		RunID:      runID,
		RootTokens: rootTokens, SpecialistTokens: specialistTokens,
		ReadOnlyFanoutTokens:    readOnlyTokens,
		TotalTokens:             rootTokens + specialistTokens + readOnlyTokens + contextTokens,
		ContextCompactionTokens: contextTokens, ContextCompactionMillis: contextMillis,
		RootExecutionMillis:       rootExecutionMillis,
		SpecialistExecutionMillis: specialistExecutionMillis,
		ReadOnlyFanoutMillis:      readOnlyExecutionMillis,
		TotalExecutionMillis: rootExecutionMillis + specialistExecutionMillis +
			readOnlyExecutionMillis + contextMillis,
	}
	if err := usage.Validate(); err != nil {
		return domain.RunAgentUsage{}, apperror.Wrap(apperror.CodeConflict,
			"Run Agent usage is invalid", err)
	}
	return usage, nil
}
