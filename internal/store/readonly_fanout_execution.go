package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
)

const readOnlyFanoutExecutionSelect = `SELECT id, plan_id, run_id, workspace_id,
	status, parallelism, max_output_tokens_per_shard, snapshot_digest, requested_by,
	stop_code, lease_id, lease_generation, version, started_at, updated_at, finished_at
	FROM readonly_fanout_executions`

const readOnlyFanoutExecutionShardSelect = `SELECT execution_id, plan_id, ordinal,
	status, input_digest, attempt_count, current_attempt, provider, model, input_tokens,
	output_tokens, total_tokens, elapsed_millis, report_json, report_digest, finding_count,
	error_code, error_reason, version, created_at, updated_at, started_at, finished_at
	FROM readonly_fanout_execution_shards`

const readOnlyFanoutExecutionSummarySelect = `SELECT id, plan_id, run_id, workspace_id,
	status, parallelism, max_output_tokens_per_shard, requested_by, stop_code, version,
	started_at, updated_at, finished_at FROM readonly_fanout_executions`

const readOnlyFanoutExecutionShardSummarySelect = `SELECT execution_id, plan_id,
	ordinal, status, attempt_count, current_attempt, provider, model, input_tokens,
	output_tokens, total_tokens, elapsed_millis, finding_count, error_code, version,
	created_at, updated_at, started_at, finished_at FROM readonly_fanout_execution_shards`

type readOnlyFanoutExecutionRecord struct {
	Execution       domain.ReadOnlyFanoutExecution
	LeaseID         string
	LeaseGeneration int64
}

type readOnlyFanoutModelCall struct {
	ExecutionID         string
	PlanID              string
	RunID               string
	ShardOrdinal        int
	AttemptNumber       int
	LeaseID             string
	LeaseGeneration     int64
	Provider            string
	Model               string
	Status              string
	Outcome             string
	InputFingerprint    string
	ResponseDigest      string
	ReservedInputTokens int64
	ReservedOutput      int64
	ReservedTotal       int64
	ReservedMillis      int64
	UsageRecorded       bool
	InputTokens         int64
	OutputTokens        int64
	TotalTokens         int64
	ElapsedRecorded     bool
	ElapsedMillis       int64
	ErrorCode           string
	ErrorReason         string
	Version             int64
	StartedAt           time.Time
	FinishedAt          *time.Time
}

const readOnlyFanoutModelCallSelect = `SELECT execution_id, plan_id, run_id,
	shard_ordinal, attempt_number, lease_id, lease_generation, provider, model, status,
	outcome, input_fingerprint, response_digest, reserved_input_tokens,
	reserved_output_tokens, reserved_total_tokens, reserved_millis, usage_recorded,
	input_tokens, output_tokens, total_tokens, elapsed_recorded, elapsed_millis,
	error_code, error_reason, version, started_at, finished_at
	FROM readonly_fanout_model_calls`

func (s *SQLiteStore) GetReadOnlyFanoutExecution(ctx context.Context,
	id string,
) (domain.ReadOnlyFanoutExecution, error) {
	id = strings.TrimSpace(id)
	if !domain.ValidAgentID(id) {
		return domain.ReadOnlyFanoutExecution{}, apperror.New(
			apperror.CodeInvalidArgument, "read-only fan-out execution id is invalid")
	}
	record, err := getReadOnlyFanoutExecutionRecord(ctx, s.db, id)
	if err != nil {
		return domain.ReadOnlyFanoutExecution{}, err
	}
	return record.Execution, nil
}

func (s *SQLiteStore) ListReadOnlyFanoutExecutions(ctx context.Context,
	planID string, limit int,
) ([]domain.ReadOnlyFanoutExecution, error) {
	planID = strings.TrimSpace(planID)
	if !domain.ValidAgentID(planID) || limit <= 0 || limit > 100 {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"read-only fan-out execution list requires a valid plan and limit")
	}
	rows, err := s.db.QueryContext(ctx, readOnlyFanoutExecutionSelect+
		` WHERE plan_id = ? ORDER BY started_at DESC, id DESC LIMIT ?`, planID, limit)
	if err != nil {
		return nil, err
	}
	records := make([]readOnlyFanoutExecutionRecord, 0)
	for rows.Next() {
		record, err := scanReadOnlyFanoutExecutionRecord(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	result := make([]domain.ReadOnlyFanoutExecution, len(records))
	for index := range records {
		if err := loadReadOnlyFanoutExecutionShards(ctx, s.db, &records[index]); err != nil {
			return nil, err
		}
		result[index] = records[index].Execution
	}
	return result, nil
}

func (s *SQLiteStore) GetLatestReadOnlyFanoutExecutionSummary(ctx context.Context,
	planID string,
) (domain.ReadOnlyFanoutExecutionSummary, bool, error) {
	planID = strings.TrimSpace(planID)
	if !domain.ValidAgentID(planID) {
		return domain.ReadOnlyFanoutExecutionSummary{}, false, apperror.New(
			apperror.CodeInvalidArgument, "read-only fan-out plan id is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.ReadOnlyFanoutExecutionSummary{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	summary, err := scanReadOnlyFanoutExecutionSummary(tx.QueryRowContext(ctx,
		readOnlyFanoutExecutionSummarySelect+
			` WHERE plan_id = ? ORDER BY started_at DESC, id DESC LIMIT 1`, planID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ReadOnlyFanoutExecutionSummary{}, false, nil
	}
	if err != nil {
		return domain.ReadOnlyFanoutExecutionSummary{}, false, err
	}
	rows, err := tx.QueryContext(ctx, readOnlyFanoutExecutionShardSummarySelect+
		` WHERE execution_id = ? ORDER BY ordinal`, summary.ID)
	if err != nil {
		return domain.ReadOnlyFanoutExecutionSummary{}, false, err
	}
	for rows.Next() {
		shard, err := scanReadOnlyFanoutExecutionShardSummary(rows)
		if err != nil {
			_ = rows.Close()
			return domain.ReadOnlyFanoutExecutionSummary{}, false, err
		}
		summary.Shards = append(summary.Shards, shard)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return domain.ReadOnlyFanoutExecutionSummary{}, false, err
	}
	if err := rows.Close(); err != nil {
		return domain.ReadOnlyFanoutExecutionSummary{}, false, err
	}
	if err := summary.Validate(); err != nil {
		return domain.ReadOnlyFanoutExecutionSummary{}, false, apperror.Wrap(
			apperror.CodeConflict, "stored read-only fan-out execution summary is invalid", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.ReadOnlyFanoutExecutionSummary{}, false, err
	}
	return summary, true, nil
}

func (s *SQLiteStore) GetReadOnlyFanoutExecutionOperation(ctx context.Context,
	keyDigest string,
) (domain.ReadOnlyFanoutExecutionOperation, bool, error) {
	keyDigest = strings.TrimSpace(keyDigest)
	if !validStoreDigest(keyDigest) {
		return domain.ReadOnlyFanoutExecutionOperation{}, false, apperror.New(
			apperror.CodeInvalidArgument,
			"read-only fan-out execution operation digest is invalid")
	}
	return getReadOnlyFanoutExecutionOperation(ctx, s.db, keyDigest)
}

func (s *SQLiteStore) CreateReadOnlyFanoutExecution(ctx context.Context,
	lease domain.RunExecutionLease, execution domain.ReadOnlyFanoutExecution,
	operation domain.ReadOnlyFanoutExecutionOperation, decision policy.Decision,
) (domain.ReadOnlyFanoutExecution, bool, error) {
	execution = normalizeReadOnlyFanoutExecution(execution)
	operation = normalizeReadOnlyFanoutExecutionOperation(operation)
	decision = normalizeReadOnlyFanoutDecision(decision)
	if err := validateReadOnlyFanoutExecutionCreate(lease, execution, operation,
		decision); err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := acquireReadOnlyFanoutWriteLockTx(ctx, tx, execution.RunID); err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	if err := requireRunExecutionLeaseTx(ctx, tx, lease.RunID, lease.LeaseID,
		lease.Generation); err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	if existing, found, err := getReadOnlyFanoutExecutionOperation(ctx, tx,
		operation.KeyDigest); err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	} else if found {
		if err := validateReadOnlyFanoutExecutionReplay(existing, operation); err != nil {
			return domain.ReadOnlyFanoutExecution{}, false, err
		}
		stored, err := getReadOnlyFanoutExecutionRecord(ctx, tx, existing.ExecutionID)
		if err != nil {
			return domain.ReadOnlyFanoutExecution{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.ReadOnlyFanoutExecution{}, false, err
		}
		return stored.Execution, true, nil
	}
	plan, err := getReadOnlyFanoutPlan(ctx, tx, execution.PlanID)
	if err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	run, _, err := requireReadOnlyFanoutPlanBindingTx(ctx, tx, plan)
	if err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	if execution.RunID != plan.RunID || execution.WorkspaceID != plan.WorkspaceID ||
		execution.Parallelism != plan.EffectiveParallelism ||
		execution.SnapshotDigest != plan.SnapshotDigest ||
		execution.RequestedBy != plan.RequestedBy || execution.StartedAt.Before(plan.CreatedAt) {
		return domain.ReadOnlyFanoutExecution{}, false, apperror.New(
			apperror.CodeFailedPrecondition,
			"read-only fan-out execution does not match its immutable plan")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO readonly_fanout_executions
		(id, plan_id, run_id, workspace_id, status, parallelism,
		max_output_tokens_per_shard, snapshot_digest, requested_by, stop_code,
		lease_id, lease_generation, version, started_at, updated_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`, execution.ID,
		execution.PlanID, execution.RunID, execution.WorkspaceID, execution.Status,
		execution.Parallelism, execution.MaxOutputTokensPerShard,
		execution.SnapshotDigest, execution.RequestedBy, execution.StopCode,
		lease.LeaseID, lease.Generation, execution.Version, ts(execution.StartedAt),
		ts(execution.UpdatedAt)); err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	for _, shard := range execution.Shards {
		if _, err := tx.ExecContext(ctx, `INSERT INTO readonly_fanout_execution_shards
			(execution_id, plan_id, ordinal, status, input_digest, attempt_count,
			current_attempt, provider, model, input_tokens, output_tokens, total_tokens,
			elapsed_millis, report_json, report_digest, finding_count, error_code,
			error_reason, version, created_at, updated_at, started_at, finished_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL)`,
			shard.ExecutionID, shard.PlanID, shard.Ordinal, shard.Status,
			shard.InputDigest, shard.AttemptCount, shard.CurrentAttempt, shard.Provider,
			shard.Model, shard.InputTokens, shard.OutputTokens, shard.TotalTokens,
			shard.ElapsedMillis, shard.ReportJSON, shard.ReportDigest,
			shard.FindingCount, shard.ErrorCode, shard.ErrorReason, shard.Version,
			ts(shard.CreatedAt), ts(shard.UpdatedAt)); err != nil {
			return domain.ReadOnlyFanoutExecution{}, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO readonly_fanout_execution_operations
		(operation_key_digest, request_fingerprint, execution_id, plan_id, run_id,
		requested_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, operation.KeyDigest,
		operation.RequestFingerprint, operation.ExecutionID, operation.PlanID,
		operation.RunID, operation.RequestedBy, ts(operation.CreatedAt)); err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.PolicyDecisionEvent,
		"readonly_fanout", execution.ID, map[string]any{
			"context": "readonly_fanout_execution", "allowed": true,
			"needs_approval": false, "risk": decision.Risk, "reason": decision.Reason,
			"capability": "workspace_readonly", "execution_authorized": true,
		}); err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run,
		events.ReadOnlyFanoutExecutionStartedEvent, "readonly_fanout", execution.ID,
		map[string]any{
			"execution_id": execution.ID, "plan_id": execution.PlanID,
			"parallelism":                 execution.Parallelism,
			"max_output_tokens_per_shard": execution.MaxOutputTokensPerShard,
			"snapshot_digest":             execution.SnapshotDigest,
			"shell":                       false, "file_write": false, "process": false,
			"network": false, "external_tools": false, "child_spawn": false,
		}); err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	return execution, false, nil
}

func (s *SQLiteStore) RecoverReadOnlyFanoutExecution(ctx context.Context,
	lease domain.RunExecutionLease, executionID string,
) (domain.ReadOnlyFanoutExecution, bool, error) {
	executionID = strings.TrimSpace(executionID)
	if err := lease.Validate(); err != nil || lease.Status != domain.RunExecutionLeaseActive ||
		!domain.ValidAgentID(executionID) {
		return domain.ReadOnlyFanoutExecution{}, false, apperror.New(
			apperror.CodeInvalidArgument,
			"read-only fan-out recovery lease or execution is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := acquireReadOnlyFanoutWriteLockTx(ctx, tx, lease.RunID); err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	if err := requireRunExecutionLeaseTx(ctx, tx, lease.RunID, lease.LeaseID,
		lease.Generation); err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	record, err := getReadOnlyFanoutExecutionRecord(ctx, tx, executionID)
	if err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	if record.Execution.RunID != lease.RunID {
		return domain.ReadOnlyFanoutExecution{}, false, apperror.New(
			apperror.CodeInvalidArgument,
			"read-only fan-out execution belongs to a different Run")
	}
	if record.Execution.Status.Terminal() {
		if err := tx.Commit(); err != nil {
			return domain.ReadOnlyFanoutExecution{}, false, err
		}
		return record.Execution, false, nil
	}
	if record.LeaseID == lease.LeaseID && record.LeaseGeneration == lease.Generation {
		if err := tx.Commit(); err != nil {
			return domain.ReadOnlyFanoutExecution{}, false, err
		}
		return record.Execution, false, nil
	}
	if lease.Generation <= record.LeaseGeneration {
		return domain.ReadOnlyFanoutExecution{}, false, apperror.New(
			apperror.CodeConflict,
			"read-only fan-out recovery requires a newer Run lease generation")
	}
	now := time.Now().UTC()
	callResult, err := tx.ExecContext(ctx, `UPDATE readonly_fanout_model_calls
		SET status = 'abandoned', outcome = 'lease_recovered',
			error_code = 'lease_recovered',
			error_reason = 'execution lease changed before the model outcome was committed',
			version = version + 1, finished_at = ?
		WHERE execution_id = ? AND status = 'started'`, ts(now), executionID)
	if err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	abandoned, err := callResult.RowsAffected()
	if err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	shardResult, err := tx.ExecContext(ctx, `UPDATE readonly_fanout_execution_shards
		SET status = 'pending', current_attempt = 0, provider = '', model = '',
			input_tokens = 0, output_tokens = 0, total_tokens = 0, elapsed_millis = 0,
			report_json = '', report_digest = '', finding_count = 0, error_code = '',
			error_reason = '', version = version + 1, updated_at = ?, started_at = NULL,
			finished_at = NULL WHERE execution_id = ? AND status = 'running'`,
		ts(now), executionID)
	if err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	reset, err := shardResult.RowsAffected()
	if err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	if abandoned != reset {
		return domain.ReadOnlyFanoutExecution{}, false, apperror.New(
			apperror.CodeConflict,
			"read-only fan-out active shard and model-call ledgers disagree")
	}
	executionResult, err := tx.ExecContext(ctx, `UPDATE readonly_fanout_executions
		SET lease_id = ?, lease_generation = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND status = 'running' AND lease_generation = ?`, lease.LeaseID,
		lease.Generation, ts(now), executionID, record.LeaseGeneration)
	if err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	if changed, err := executionResult.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return domain.ReadOnlyFanoutExecution{}, false, err
		}
		return domain.ReadOnlyFanoutExecution{}, false, apperror.New(
			apperror.CodeConflict, "read-only fan-out recovery lost its race")
	}
	run, _, err := getCoordinatorRunTx(ctx, tx, lease.RunID)
	if err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run,
		events.ReadOnlyFanoutExecutionRecoveredEvent, "readonly_fanout", executionID,
		map[string]any{
			"execution_id": executionID, "lease_generation": lease.Generation,
			"abandoned_calls": abandoned, "reset_shards": reset,
		}); err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	updated, err := getReadOnlyFanoutExecutionRecord(ctx, tx, executionID)
	if err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	return updated.Execution, true, nil
}

func (s *SQLiteStore) StartReadOnlyFanoutExecutionShard(ctx context.Context,
	lease domain.RunExecutionLease, executionID string, ordinal int,
	provider string, model string, inputFingerprint string,
	reservedInputTokens int64, reservedOutputTokens int64, reservedMillis int64,
) (domain.ReadOnlyFanoutExecutionShard, error) {
	executionID = strings.TrimSpace(executionID)
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	inputFingerprint = strings.TrimSpace(inputFingerprint)
	if err := lease.Validate(); err != nil || lease.Status != domain.RunExecutionLeaseActive ||
		!domain.ValidAgentID(executionID) || ordinal <= 0 ||
		ordinal > domain.MaxReadOnlyFanoutParallelism || !domain.ValidAgentID(provider) ||
		!domain.ValidAgentID(model) || !validStoreDigest(inputFingerprint) ||
		reservedInputTokens < 0 ||
		reservedOutputTokens < domain.MinReadOnlyFanoutMaxOutputTokens ||
		reservedOutputTokens > domain.MaxReadOnlyFanoutMaxOutputTokens ||
		reservedInputTokens > int64(^uint64(0)>>1)-reservedOutputTokens ||
		reservedMillis < 0 {
		return domain.ReadOnlyFanoutExecutionShard{}, apperror.New(
			apperror.CodeInvalidArgument,
			"read-only fan-out shard start request is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	defer func() { _ = tx.Rollback() }()
	record, err := requireReadOnlyFanoutExecutionLeaseTx(ctx, tx, lease, executionID)
	if err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	shard, err := getReadOnlyFanoutExecutionShard(ctx, tx, executionID, ordinal)
	if err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	if shard.Status != domain.ReadOnlyFanoutExecutionShardPending {
		return domain.ReadOnlyFanoutExecutionShard{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"read-only fan-out shard is not pending")
	}
	if shard.AttemptCount >= domain.MaxReadOnlyFanoutAttemptsPerShard {
		return domain.ReadOnlyFanoutExecutionShard{}, apperror.New(
			apperror.CodeResourceExhausted,
			"read-only fan-out shard recovery limit is exhausted")
	}
	attempt := shard.AttemptCount + 1
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE readonly_fanout_execution_shards
		SET status = 'running', attempt_count = ?, current_attempt = ?,
			version = version + 1, updated_at = ?, started_at = ?
		WHERE execution_id = ? AND ordinal = ? AND status = 'pending'
			AND version = ?`, attempt, attempt, ts(now), ts(now), executionID, ordinal,
		shard.Version)
	if err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return domain.ReadOnlyFanoutExecutionShard{}, err
		}
		return domain.ReadOnlyFanoutExecutionShard{}, apperror.New(
			apperror.CodeConflict, "read-only fan-out shard start lost its race")
	}
	reservedTotal := reservedInputTokens + reservedOutputTokens
	if _, err := tx.ExecContext(ctx, `INSERT INTO readonly_fanout_model_calls
		(execution_id, plan_id, run_id, shard_ordinal, attempt_number, lease_id,
		lease_generation, provider, model, status, input_fingerprint,
		reserved_input_tokens, reserved_output_tokens, reserved_total_tokens,
		reserved_millis, version, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'started', ?, ?, ?, ?, ?, 1, ?)`,
		executionID, record.Execution.PlanID, record.Execution.RunID, ordinal, attempt,
		lease.LeaseID, lease.Generation, provider, model, inputFingerprint,
		reservedInputTokens, reservedOutputTokens, reservedTotal, reservedMillis,
		ts(now)); err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	run, _, err := getCoordinatorRunTx(ctx, tx, record.Execution.RunID)
	if err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.ReadOnlyFanoutShardStartedEvent,
		"readonly_fanout", readOnlyFanoutShardSubject(executionID, ordinal), map[string]any{
			"execution_id": executionID, "plan_id": record.Execution.PlanID,
			"shard": ordinal, "attempt": attempt, "provider": provider, "model": model,
			"input_digest": shard.InputDigest, "reserved_tokens": reservedTotal,
			"reserved_millis": reservedMillis, "tools": false,
		}); err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	updated, err := getReadOnlyFanoutExecutionShard(ctx, tx, executionID, ordinal)
	if err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	return updated, nil
}

func (s *SQLiteStore) CompleteReadOnlyFanoutExecutionShard(ctx context.Context,
	lease domain.RunExecutionLease, executionID string, ordinal int, attempt int,
	provider string, model string, usage llm.Usage, elapsed time.Duration,
	report domain.ReadOnlyFanoutReport,
) (domain.ReadOnlyFanoutExecutionShard, error) {
	executionID = strings.TrimSpace(executionID)
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	inputTokens, outputTokens, totalTokens, err := supervisorUsage(usage)
	if err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	elapsedMillis, err := supervisorElapsedMillis(elapsed)
	if err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	if err := validateReadOnlyFanoutTerminalIdentity(lease, executionID, ordinal,
		attempt, provider, model); err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	defer func() { _ = tx.Rollback() }()
	record, shard, call, run, err := prepareReadOnlyFanoutShardTerminalTx(ctx, tx,
		lease, executionID, ordinal, attempt, provider, model)
	if err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	allowedPaths, err := allowedReadOnlyFanoutPathsTx(ctx, tx, record.Execution.PlanID,
		ordinal)
	if err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	reportJSON, err := domain.EncodeReadOnlyFanoutReport(report, allowedPaths)
	if err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, apperror.Wrap(
			apperror.CodeInvalidArgument, "read-only fan-out report is invalid", err)
	}
	reportDigest, err := domain.ReadOnlyFanoutReportDigest(reportJSON)
	if err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	normalizedReport, err := domain.DecodeReadOnlyFanoutReport(reportJSON, allowedPaths)
	if err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	if shard.Status == domain.ReadOnlyFanoutExecutionShardCompleted {
		if shard.Provider != provider || shard.Model != model ||
			shard.InputTokens != inputTokens || shard.OutputTokens != outputTokens ||
			shard.TotalTokens != totalTokens || shard.ElapsedMillis != elapsedMillis ||
			shard.ReportDigest != reportDigest || shard.ReportJSON != reportJSON {
			return domain.ReadOnlyFanoutExecutionShard{}, apperror.New(
				apperror.CodeConflict,
				"read-only fan-out completion replay differs from stored state")
		}
		if err := requireFanoutCacheUsageReplayTx(ctx, tx, lease.RunID, executionID, ordinal, attempt, &usage); err != nil {
			return domain.ReadOnlyFanoutExecutionShard{}, err
		}
		if err := tx.Commit(); err != nil {
			return domain.ReadOnlyFanoutExecutionShard{}, err
		}
		return shard, nil
	}
	if totalTokens > call.ReservedTotal || outputTokens > call.ReservedOutput {
		return domain.ReadOnlyFanoutExecutionShard{}, apperror.New(
			apperror.CodeResourceExhausted,
			"read-only fan-out provider usage exceeded its reserved token envelope")
	}
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE readonly_fanout_model_calls
		SET status = 'completed', outcome = 'success', response_digest = ?,
			usage_recorded = 1, input_tokens = ?, output_tokens = ?, total_tokens = ?,
			elapsed_recorded = 1, elapsed_millis = ?, version = version + 1,
			finished_at = ? WHERE execution_id = ? AND shard_ordinal = ?
			AND attempt_number = ? AND status = 'started'`, reportDigest, inputTokens,
		outputTokens, totalTokens, elapsedMillis, ts(now), executionID, ordinal, attempt)
	if err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return domain.ReadOnlyFanoutExecutionShard{}, err
		}
		return domain.ReadOnlyFanoutExecutionShard{}, apperror.New(
			apperror.CodeConflict, "read-only fan-out model completion lost its race")
	}
	result, err = tx.ExecContext(ctx, `UPDATE readonly_fanout_execution_shards
		SET status = 'completed', provider = ?, model = ?, input_tokens = ?,
			output_tokens = ?, total_tokens = ?, elapsed_millis = ?, report_json = ?,
			report_digest = ?, finding_count = ?, version = version + 1,
			updated_at = ?, finished_at = ?
		WHERE execution_id = ? AND ordinal = ? AND status = 'running'
			AND current_attempt = ? AND version = ?`, provider, model, inputTokens,
		outputTokens, totalTokens, elapsedMillis, reportJSON, reportDigest,
		len(normalizedReport.Findings), ts(now), ts(now), executionID, ordinal, attempt,
		shard.Version)
	if err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return domain.ReadOnlyFanoutExecutionShard{}, err
		}
		return domain.ReadOnlyFanoutExecutionShard{}, apperror.New(
			apperror.CodeConflict, "read-only fan-out shard completion lost its race")
	}
	for index, finding := range normalizedReport.Findings {
		fingerprint, err := domain.ReadOnlyFanoutFindingFingerprint(executionID,
			ordinal, finding)
		if err != nil {
			return domain.ReadOnlyFanoutExecutionShard{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO readonly_fanout_findings
			(execution_id, shard_ordinal, ordinal, fingerprint, severity, category,
			title, detail, relative_path, line_start, line_end, confidence, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, executionID, ordinal,
			index+1, fingerprint, finding.Severity, finding.Category, finding.Title,
			finding.Detail, finding.Path, finding.LineStart, finding.LineEnd,
			finding.Confidence, ts(now)); err != nil {
			return domain.ReadOnlyFanoutExecutionShard{}, err
		}
	}
	if err := appendSupervisorEventTx(ctx, tx, run,
		events.ReadOnlyFanoutShardCompletedEvent, "readonly_fanout",
		readOnlyFanoutShardSubject(executionID, ordinal), map[string]any{
			"execution_id": executionID, "plan_id": record.Execution.PlanID,
			"shard": ordinal, "attempt": attempt, "provider": provider, "model": model,
			"input_tokens": inputTokens, "output_tokens": outputTokens,
			"usage":        usage,
			"total_tokens": totalTokens, "elapsed_millis": elapsedMillis,
			"report_digest": reportDigest, "finding_count": len(normalizedReport.Findings),
		}); err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	updated, err := getReadOnlyFanoutExecutionShard(ctx, tx, executionID, ordinal)
	if err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	return updated, nil
}

func (s *SQLiteStore) FailReadOnlyFanoutExecutionShard(ctx context.Context,
	lease domain.RunExecutionLease, executionID string, ordinal int, attempt int,
	provider string, model string, usage *llm.Usage, elapsed time.Duration,
	status domain.ReadOnlyFanoutExecutionShardStatus, errorCode string,
	errorReason string,
) (domain.ReadOnlyFanoutExecutionShard, error) {
	executionID = strings.TrimSpace(executionID)
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	errorCode, errorReason = normalizeReadOnlyFanoutError(errorCode, errorReason)
	if status != domain.ReadOnlyFanoutExecutionShardFailed &&
		status != domain.ReadOnlyFanoutExecutionShardCancelled {
		return domain.ReadOnlyFanoutExecutionShard{}, apperror.New(
			apperror.CodeInvalidArgument,
			"read-only fan-out terminal status must be failed or cancelled")
	}
	if err := validateReadOnlyFanoutTerminalIdentity(lease, executionID, ordinal,
		attempt, provider, model); err != nil || errorCode == "" || errorReason == "" {
		return domain.ReadOnlyFanoutExecutionShard{}, apperror.New(
			apperror.CodeInvalidArgument,
			"read-only fan-out failure request is invalid")
	}
	inputTokens, outputTokens, totalTokens := int64(0), int64(0), int64(0)
	usageRecorded := 0
	if usage != nil {
		var err error
		inputTokens, outputTokens, totalTokens, err = supervisorUsage(*usage)
		if err != nil {
			return domain.ReadOnlyFanoutExecutionShard{}, err
		}
		usageRecorded = 1
	}
	elapsedMillis, err := supervisorElapsedMillis(elapsed)
	if err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	defer func() { _ = tx.Rollback() }()
	record, shard, call, run, err := prepareReadOnlyFanoutShardTerminalTx(ctx, tx,
		lease, executionID, ordinal, attempt, provider, model)
	if err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	if shard.Status.Terminal() {
		if shard.Status != status || shard.Provider != provider || shard.Model != model ||
			shard.InputTokens != inputTokens || shard.OutputTokens != outputTokens ||
			shard.TotalTokens != totalTokens || shard.ElapsedMillis != elapsedMillis ||
			shard.ErrorCode != errorCode || shard.ErrorReason != errorReason {
			return domain.ReadOnlyFanoutExecutionShard{}, apperror.New(
				apperror.CodeConflict,
				"read-only fan-out failure replay differs from stored state")
		}
		if err := requireFanoutCacheUsageReplayTx(ctx, tx, lease.RunID, executionID, ordinal, attempt, usage); err != nil {
			return domain.ReadOnlyFanoutExecutionShard{}, err
		}
		if err := tx.Commit(); err != nil {
			return domain.ReadOnlyFanoutExecutionShard{}, err
		}
		return shard, nil
	}
	now := time.Now().UTC()
	callStatus := string(status)
	outcome := errorCode
	result, err := tx.ExecContext(ctx, `UPDATE readonly_fanout_model_calls
		SET status = ?, outcome = ?, usage_recorded = ?, input_tokens = ?,
			output_tokens = ?, total_tokens = ?, elapsed_recorded = 1,
			elapsed_millis = ?, error_code = ?, error_reason = ?, version = version + 1,
			finished_at = ? WHERE execution_id = ? AND shard_ordinal = ?
			AND attempt_number = ? AND status = 'started'`, callStatus, outcome,
		usageRecorded, inputTokens, outputTokens, totalTokens, elapsedMillis,
		errorCode, errorReason, ts(now), executionID, ordinal, attempt)
	if err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return domain.ReadOnlyFanoutExecutionShard{}, err
		}
		return domain.ReadOnlyFanoutExecutionShard{}, apperror.New(
			apperror.CodeConflict, "read-only fan-out model failure lost its race")
	}
	result, err = tx.ExecContext(ctx, `UPDATE readonly_fanout_execution_shards
		SET status = ?, provider = ?, model = ?, input_tokens = ?, output_tokens = ?,
			total_tokens = ?, elapsed_millis = ?, error_code = ?, error_reason = ?,
			version = version + 1, updated_at = ?, finished_at = ?
		WHERE execution_id = ? AND ordinal = ? AND status = 'running'
			AND current_attempt = ? AND version = ?`, status, provider, model,
		inputTokens, outputTokens, totalTokens, elapsedMillis, errorCode, errorReason,
		ts(now), ts(now), executionID, ordinal, attempt, shard.Version)
	if err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return domain.ReadOnlyFanoutExecutionShard{}, err
		}
		return domain.ReadOnlyFanoutExecutionShard{}, apperror.New(
			apperror.CodeConflict, "read-only fan-out shard failure lost its race")
	}
	eventType := events.ReadOnlyFanoutShardFailedEvent
	if status == domain.ReadOnlyFanoutExecutionShardCancelled {
		eventType = events.ReadOnlyFanoutShardCancelledEvent
	}
	if err := appendSupervisorEventTx(ctx, tx, run, eventType, "readonly_fanout",
		readOnlyFanoutShardSubject(executionID, ordinal), map[string]any{
			"execution_id": executionID, "plan_id": record.Execution.PlanID,
			"shard": ordinal, "attempt": attempt, "provider": provider, "model": model,
			"total_tokens": totalTokens, "elapsed_millis": elapsedMillis,
			"error_code": errorCode,
			"usage":      usage,
		}); err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	updated, err := getReadOnlyFanoutExecutionShard(ctx, tx, executionID, ordinal)
	if err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	if call.Status != "started" {
		return domain.ReadOnlyFanoutExecutionShard{}, apperror.New(
			apperror.CodeConflict, "read-only fan-out model call was already terminal")
	}
	if err := tx.Commit(); err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	return updated, nil
}

func (s *SQLiteStore) CancelReadOnlyFanoutExecutionRemainder(ctx context.Context,
	lease domain.RunExecutionLease, executionID string, errorCode string,
	errorReason string,
) (domain.ReadOnlyFanoutExecution, error) {
	executionID = strings.TrimSpace(executionID)
	errorCode, errorReason = normalizeReadOnlyFanoutError(errorCode, errorReason)
	if err := lease.Validate(); err != nil || lease.Status != domain.RunExecutionLeaseActive ||
		!domain.ValidAgentID(executionID) || errorCode == "" || errorReason == "" {
		return domain.ReadOnlyFanoutExecution{}, apperror.New(
			apperror.CodeInvalidArgument,
			"read-only fan-out cancellation request is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.ReadOnlyFanoutExecution{}, err
	}
	defer func() { _ = tx.Rollback() }()
	record, err := requireReadOnlyFanoutExecutionLeaseTx(ctx, tx, lease, executionID)
	if err != nil {
		return domain.ReadOnlyFanoutExecution{}, err
	}
	if record.Execution.Status.Terminal() {
		if err := tx.Commit(); err != nil {
			return domain.ReadOnlyFanoutExecution{}, err
		}
		return record.Execution, nil
	}
	run, _, err := getCoordinatorRunTx(ctx, tx, record.Execution.RunID)
	if err != nil {
		return domain.ReadOnlyFanoutExecution{}, err
	}
	now := time.Now().UTC()
	for _, shard := range record.Execution.Shards {
		switch shard.Status {
		case domain.ReadOnlyFanoutExecutionShardPending:
			result, err := tx.ExecContext(ctx, `UPDATE readonly_fanout_execution_shards
				SET status = 'cancelled', error_code = ?, error_reason = ?,
					version = version + 1, updated_at = ?, finished_at = ?
				WHERE execution_id = ? AND ordinal = ? AND status = 'pending'
					AND version = ?`, errorCode, errorReason, ts(now), ts(now),
				executionID, shard.Ordinal, shard.Version)
			if err != nil {
				return domain.ReadOnlyFanoutExecution{}, err
			}
			if changed, err := result.RowsAffected(); err != nil || changed != 1 {
				if err != nil {
					return domain.ReadOnlyFanoutExecution{}, err
				}
				return domain.ReadOnlyFanoutExecution{}, apperror.New(
					apperror.CodeConflict, "read-only fan-out pending cancellation lost its race")
			}
			if err := appendReadOnlyFanoutCancellationEvent(ctx, tx, run, record.Execution,
				shard.Ordinal, 0, "", "", errorCode); err != nil {
				return domain.ReadOnlyFanoutExecution{}, err
			}
		case domain.ReadOnlyFanoutExecutionShardRunning:
			call, err := getReadOnlyFanoutModelCall(ctx, tx, executionID, shard.Ordinal,
				shard.CurrentAttempt)
			if err != nil {
				return domain.ReadOnlyFanoutExecution{}, err
			}
			if call.Status != "started" {
				return domain.ReadOnlyFanoutExecution{}, apperror.New(
					apperror.CodeConflict,
					"read-only fan-out running shard has no active model call")
			}
			callResult, err := tx.ExecContext(ctx, `UPDATE readonly_fanout_model_calls
				SET status = 'cancelled', outcome = 'cancelled', error_code = ?,
					error_reason = ?, version = version + 1, finished_at = ?
				WHERE execution_id = ? AND shard_ordinal = ? AND attempt_number = ?
					AND status = 'started'`, errorCode, errorReason, ts(now), executionID,
				shard.Ordinal, shard.CurrentAttempt)
			if err != nil {
				return domain.ReadOnlyFanoutExecution{}, err
			}
			if changed, err := callResult.RowsAffected(); err != nil || changed != 1 {
				if err != nil {
					return domain.ReadOnlyFanoutExecution{}, err
				}
				return domain.ReadOnlyFanoutExecution{}, apperror.New(
					apperror.CodeConflict,
					"read-only fan-out active call cancellation lost its race")
			}
			shardResult, err := tx.ExecContext(ctx, `UPDATE readonly_fanout_execution_shards
				SET status = 'cancelled', provider = ?, model = ?, error_code = ?,
					error_reason = ?, version = version + 1, updated_at = ?, finished_at = ?
				WHERE execution_id = ? AND ordinal = ? AND status = 'running'
					AND current_attempt = ? AND version = ?`, call.Provider, call.Model,
				errorCode, errorReason, ts(now), ts(now), executionID, shard.Ordinal,
				shard.CurrentAttempt, shard.Version)
			if err != nil {
				return domain.ReadOnlyFanoutExecution{}, err
			}
			if changed, err := shardResult.RowsAffected(); err != nil || changed != 1 {
				if err != nil {
					return domain.ReadOnlyFanoutExecution{}, err
				}
				return domain.ReadOnlyFanoutExecution{}, apperror.New(
					apperror.CodeConflict,
					"read-only fan-out active shard cancellation lost its race")
			}
			if err := appendReadOnlyFanoutCancellationEvent(ctx, tx, run, record.Execution,
				shard.Ordinal, shard.CurrentAttempt, call.Provider, call.Model,
				errorCode); err != nil {
				return domain.ReadOnlyFanoutExecution{}, err
			}
		}
	}
	updated, err := getReadOnlyFanoutExecutionRecord(ctx, tx, executionID)
	if err != nil {
		return domain.ReadOnlyFanoutExecution{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ReadOnlyFanoutExecution{}, err
	}
	return updated.Execution, nil
}

func (s *SQLiteStore) FinalizeReadOnlyFanoutExecution(ctx context.Context,
	lease domain.RunExecutionLease, executionID string,
	status domain.ReadOnlyFanoutExecutionStatus, stopCode string,
) (domain.ReadOnlyFanoutExecution, bool, error) {
	executionID = strings.TrimSpace(executionID)
	stopCode = normalizeReadOnlyFanoutCode(stopCode)
	if err := lease.Validate(); err != nil || lease.Status != domain.RunExecutionLeaseActive ||
		!domain.ValidAgentID(executionID) || !status.Terminal() ||
		(status == domain.ReadOnlyFanoutExecutionCompleted && stopCode != "") ||
		(status != domain.ReadOnlyFanoutExecutionCompleted && stopCode == "") {
		return domain.ReadOnlyFanoutExecution{}, false, apperror.New(
			apperror.CodeInvalidArgument,
			"read-only fan-out finalization request is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	record, err := requireReadOnlyFanoutExecutionLeaseTx(ctx, tx, lease, executionID)
	if err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	if record.Execution.Status.Terminal() {
		if record.Execution.Status != status || record.Execution.StopCode != stopCode {
			return domain.ReadOnlyFanoutExecution{}, false, apperror.New(
				apperror.CodeConflict,
				"read-only fan-out finalization replay differs from stored state")
		}
		if err := tx.Commit(); err != nil {
			return domain.ReadOnlyFanoutExecution{}, false, err
		}
		return record.Execution, true, nil
	}
	allCompleted := true
	allTerminal := true
	for _, shard := range record.Execution.Shards {
		allCompleted = allCompleted &&
			shard.Status == domain.ReadOnlyFanoutExecutionShardCompleted
		allTerminal = allTerminal && shard.Status.Terminal()
	}
	if !allTerminal || (status == domain.ReadOnlyFanoutExecutionCompleted && !allCompleted) ||
		(status != domain.ReadOnlyFanoutExecutionCompleted && allCompleted) {
		return domain.ReadOnlyFanoutExecution{}, false, apperror.New(
			apperror.CodeFailedPrecondition,
			"read-only fan-out execution cannot finalize before every shard is terminal")
	}
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE readonly_fanout_executions
		SET status = ?, stop_code = ?, version = version + 1, updated_at = ?,
			finished_at = ? WHERE id = ? AND status = 'running' AND version = ?`,
		status, stopCode, ts(now), ts(now), executionID, record.Execution.Version)
	if err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return domain.ReadOnlyFanoutExecution{}, false, err
		}
		return domain.ReadOnlyFanoutExecution{}, false, apperror.New(
			apperror.CodeConflict, "read-only fan-out finalization lost its race")
	}
	run, _, err := getCoordinatorRunTx(ctx, tx, record.Execution.RunID)
	if err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	eventType := events.ReadOnlyFanoutExecutionFailedEvent
	switch status {
	case domain.ReadOnlyFanoutExecutionCompleted:
		eventType = events.ReadOnlyFanoutExecutionCompletedEvent
	case domain.ReadOnlyFanoutExecutionCancelled:
		eventType = events.ReadOnlyFanoutExecutionCancelledEvent
	}
	if err := appendSupervisorEventTx(ctx, tx, run, eventType, "readonly_fanout",
		executionID, map[string]any{
			"execution_id": executionID, "plan_id": record.Execution.PlanID,
			"status": status, "stop_code": stopCode,
			"parallelism": record.Execution.Parallelism,
		}); err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	updated, err := getReadOnlyFanoutExecutionRecord(ctx, tx, executionID)
	if err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ReadOnlyFanoutExecution{}, false, err
	}
	return updated.Execution, false, nil
}

func requireReadOnlyFanoutExecutionLeaseTx(ctx context.Context, tx *sql.Tx,
	lease domain.RunExecutionLease, executionID string,
) (readOnlyFanoutExecutionRecord, error) {
	if err := requireRunExecutionLeaseTx(ctx, tx, lease.RunID, lease.LeaseID,
		lease.Generation); err != nil {
		return readOnlyFanoutExecutionRecord{}, err
	}
	record, err := getReadOnlyFanoutExecutionRecord(ctx, tx, executionID)
	if err != nil {
		return readOnlyFanoutExecutionRecord{}, err
	}
	if record.Execution.RunID != lease.RunID || record.LeaseID != lease.LeaseID ||
		record.LeaseGeneration != lease.Generation {
		return readOnlyFanoutExecutionRecord{}, apperror.New(
			apperror.CodeConflict,
			"read-only fan-out execution is fenced by another Run lease")
	}
	return record, nil
}

func prepareReadOnlyFanoutShardTerminalTx(ctx context.Context, tx *sql.Tx,
	lease domain.RunExecutionLease, executionID string, ordinal int, attempt int,
	provider string, model string,
) (readOnlyFanoutExecutionRecord, domain.ReadOnlyFanoutExecutionShard,
	readOnlyFanoutModelCall, domain.Run, error,
) {
	record, err := requireReadOnlyFanoutExecutionLeaseTx(ctx, tx, lease, executionID)
	if err != nil {
		return readOnlyFanoutExecutionRecord{}, domain.ReadOnlyFanoutExecutionShard{},
			readOnlyFanoutModelCall{}, domain.Run{}, err
	}
	if record.Execution.Status != domain.ReadOnlyFanoutExecutionRunning {
		return readOnlyFanoutExecutionRecord{}, domain.ReadOnlyFanoutExecutionShard{},
			readOnlyFanoutModelCall{}, domain.Run{}, apperror.New(
				apperror.CodeFailedPrecondition,
				"read-only fan-out execution is not running")
	}
	shard, err := getReadOnlyFanoutExecutionShard(ctx, tx, executionID, ordinal)
	if err != nil {
		return readOnlyFanoutExecutionRecord{}, domain.ReadOnlyFanoutExecutionShard{},
			readOnlyFanoutModelCall{}, domain.Run{}, err
	}
	if shard.CurrentAttempt != attempt || shard.AttemptCount != attempt ||
		(shard.Status != domain.ReadOnlyFanoutExecutionShardRunning && !shard.Status.Terminal()) {
		return readOnlyFanoutExecutionRecord{}, domain.ReadOnlyFanoutExecutionShard{},
			readOnlyFanoutModelCall{}, domain.Run{}, apperror.New(
				apperror.CodeConflict,
				"read-only fan-out shard attempt is no longer current")
	}
	call, err := getReadOnlyFanoutModelCall(ctx, tx, executionID, ordinal, attempt)
	if err != nil {
		return readOnlyFanoutExecutionRecord{}, domain.ReadOnlyFanoutExecutionShard{},
			readOnlyFanoutModelCall{}, domain.Run{}, err
	}
	if call.PlanID != record.Execution.PlanID || call.RunID != record.Execution.RunID ||
		call.LeaseID != lease.LeaseID || call.LeaseGeneration != lease.Generation ||
		call.Provider != provider || call.Model != model {
		return readOnlyFanoutExecutionRecord{}, domain.ReadOnlyFanoutExecutionShard{},
			readOnlyFanoutModelCall{}, domain.Run{}, apperror.New(
				apperror.CodeConflict,
				"read-only fan-out model-call identity does not match its shard")
	}
	run, _, err := getCoordinatorRunTx(ctx, tx, record.Execution.RunID)
	if err != nil {
		return readOnlyFanoutExecutionRecord{}, domain.ReadOnlyFanoutExecutionShard{},
			readOnlyFanoutModelCall{}, domain.Run{}, err
	}
	return record, shard, call, run, nil
}

func validateReadOnlyFanoutTerminalIdentity(lease domain.RunExecutionLease,
	executionID string, ordinal int, attempt int, provider string, model string,
) error {
	if err := lease.Validate(); err != nil || lease.Status != domain.RunExecutionLeaseActive ||
		!domain.ValidAgentID(executionID) || ordinal <= 0 ||
		ordinal > domain.MaxReadOnlyFanoutParallelism || attempt <= 0 ||
		attempt > domain.MaxReadOnlyFanoutAttemptsPerShard ||
		!domain.ValidAgentID(provider) || !domain.ValidAgentID(model) {
		return apperror.New(apperror.CodeInvalidArgument,
			"read-only fan-out terminal model-call identity is invalid")
	}
	return nil
}

func appendReadOnlyFanoutCancellationEvent(ctx context.Context, tx *sql.Tx,
	run domain.Run, execution domain.ReadOnlyFanoutExecution, ordinal int,
	attempt int, provider string, model string, errorCode string,
) error {
	return appendSupervisorEventTx(ctx, tx, run, events.ReadOnlyFanoutShardCancelledEvent,
		"readonly_fanout", readOnlyFanoutShardSubject(execution.ID, ordinal),
		map[string]any{
			"execution_id": execution.ID, "plan_id": execution.PlanID,
			"shard": ordinal, "attempt": attempt, "provider": provider,
			"model": model, "total_tokens": 0, "elapsed_millis": 0,
			"error_code": errorCode,
		})
}

func getReadOnlyFanoutExecutionRecord(ctx context.Context,
	queryer readOnlyFanoutQueryer, id string,
) (readOnlyFanoutExecutionRecord, error) {
	record, err := scanReadOnlyFanoutExecutionRecord(queryer.QueryRowContext(ctx,
		readOnlyFanoutExecutionSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return readOnlyFanoutExecutionRecord{}, apperror.New(
			apperror.CodeNotFound, "read-only fan-out execution was not found")
	}
	if err != nil {
		return readOnlyFanoutExecutionRecord{}, err
	}
	if err := loadReadOnlyFanoutExecutionShards(ctx, queryer, &record); err != nil {
		return readOnlyFanoutExecutionRecord{}, err
	}
	return record, nil
}

func scanReadOnlyFanoutExecutionRecord(row scanner) (readOnlyFanoutExecutionRecord, error) {
	var record readOnlyFanoutExecutionRecord
	var status string
	var started, updated string
	var finished sql.NullString
	if err := row.Scan(&record.Execution.ID, &record.Execution.PlanID,
		&record.Execution.RunID, &record.Execution.WorkspaceID, &status,
		&record.Execution.Parallelism, &record.Execution.MaxOutputTokensPerShard,
		&record.Execution.SnapshotDigest, &record.Execution.RequestedBy,
		&record.Execution.StopCode, &record.LeaseID, &record.LeaseGeneration,
		&record.Execution.Version, &started, &updated, &finished); err != nil {
		return readOnlyFanoutExecutionRecord{}, err
	}
	record.Execution.Status = domain.ReadOnlyFanoutExecutionStatus(status)
	record.Execution.StartedAt = parseTS(started)
	record.Execution.UpdatedAt = parseTS(updated)
	if finished.Valid {
		value := parseTS(finished.String)
		record.Execution.FinishedAt = &value
	}
	return record, nil
}

func scanReadOnlyFanoutExecutionSummary(row scanner) (domain.ReadOnlyFanoutExecutionSummary, error) {
	var value domain.ReadOnlyFanoutExecutionSummary
	var status string
	var started, updated string
	var finished sql.NullString
	if err := row.Scan(&value.ID, &value.PlanID, &value.RunID, &value.WorkspaceID,
		&status, &value.Parallelism, &value.MaxOutputTokensPerShard,
		&value.RequestedBy, &value.StopCode, &value.Version, &started, &updated,
		&finished); err != nil {
		return domain.ReadOnlyFanoutExecutionSummary{}, err
	}
	value.Status = domain.ReadOnlyFanoutExecutionStatus(status)
	value.StartedAt = parseTS(started)
	value.UpdatedAt = parseTS(updated)
	if finished.Valid {
		parsed := parseTS(finished.String)
		value.FinishedAt = &parsed
	}
	value.Shards = []domain.ReadOnlyFanoutExecutionShardSummary{}
	return value, nil
}

func scanReadOnlyFanoutExecutionShardSummary(row scanner) (
	domain.ReadOnlyFanoutExecutionShardSummary, error,
) {
	var value domain.ReadOnlyFanoutExecutionShardSummary
	var status string
	var created, updated string
	var started, finished sql.NullString
	if err := row.Scan(&value.ExecutionID, &value.PlanID, &value.Ordinal, &status,
		&value.AttemptCount, &value.CurrentAttempt, &value.Provider, &value.Model,
		&value.InputTokens, &value.OutputTokens, &value.TotalTokens,
		&value.ElapsedMillis, &value.FindingCount, &value.ErrorCode, &value.Version,
		&created, &updated, &started, &finished); err != nil {
		return domain.ReadOnlyFanoutExecutionShardSummary{}, err
	}
	value.Status = domain.ReadOnlyFanoutExecutionShardStatus(status)
	value.CreatedAt = parseTS(created)
	value.UpdatedAt = parseTS(updated)
	if started.Valid {
		parsed := parseTS(started.String)
		value.StartedAt = &parsed
	}
	if finished.Valid {
		parsed := parseTS(finished.String)
		value.FinishedAt = &parsed
	}
	return value, nil
}

func loadReadOnlyFanoutExecutionShards(ctx context.Context,
	queryer readOnlyFanoutQueryer, record *readOnlyFanoutExecutionRecord,
) error {
	rows, err := queryer.QueryContext(ctx, readOnlyFanoutExecutionShardSelect+
		` WHERE execution_id = ? ORDER BY ordinal`, record.Execution.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	shards := make([]domain.ReadOnlyFanoutExecutionShard, 0,
		record.Execution.Parallelism)
	for rows.Next() {
		shard, err := scanReadOnlyFanoutExecutionShard(rows)
		if err != nil {
			return err
		}
		shards = append(shards, shard)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	record.Execution.Shards = shards
	if err := record.Execution.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeConflict,
			"stored read-only fan-out execution is invalid", err)
	}
	return nil
}

func getReadOnlyFanoutExecutionShard(ctx context.Context,
	queryer readOnlyFanoutQueryer, executionID string, ordinal int,
) (domain.ReadOnlyFanoutExecutionShard, error) {
	shard, err := scanReadOnlyFanoutExecutionShard(queryer.QueryRowContext(ctx,
		readOnlyFanoutExecutionShardSelect+` WHERE execution_id = ? AND ordinal = ?`,
		executionID, ordinal))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ReadOnlyFanoutExecutionShard{}, apperror.New(
			apperror.CodeNotFound, "read-only fan-out execution shard was not found")
	}
	return shard, err
}

func scanReadOnlyFanoutExecutionShard(row scanner) (
	domain.ReadOnlyFanoutExecutionShard, error,
) {
	var shard domain.ReadOnlyFanoutExecutionShard
	var status string
	var created, updated string
	var started, finished sql.NullString
	if err := row.Scan(&shard.ExecutionID, &shard.PlanID, &shard.Ordinal, &status,
		&shard.InputDigest, &shard.AttemptCount, &shard.CurrentAttempt,
		&shard.Provider, &shard.Model, &shard.InputTokens, &shard.OutputTokens,
		&shard.TotalTokens, &shard.ElapsedMillis, &shard.ReportJSON,
		&shard.ReportDigest, &shard.FindingCount, &shard.ErrorCode,
		&shard.ErrorReason, &shard.Version, &created, &updated, &started,
		&finished); err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, err
	}
	shard.Status = domain.ReadOnlyFanoutExecutionShardStatus(status)
	shard.CreatedAt = parseTS(created)
	shard.UpdatedAt = parseTS(updated)
	if started.Valid {
		value := parseTS(started.String)
		shard.StartedAt = &value
	}
	if finished.Valid {
		value := parseTS(finished.String)
		shard.FinishedAt = &value
	}
	if err := shard.Validate(); err != nil {
		return domain.ReadOnlyFanoutExecutionShard{}, apperror.Wrap(
			apperror.CodeConflict,
			"stored read-only fan-out execution shard is invalid", err)
	}
	return shard, nil
}

func getReadOnlyFanoutModelCall(ctx context.Context, queryer readOnlyFanoutQueryer,
	executionID string, ordinal int, attempt int,
) (readOnlyFanoutModelCall, error) {
	call, err := scanReadOnlyFanoutModelCall(queryer.QueryRowContext(ctx,
		readOnlyFanoutModelCallSelect+` WHERE execution_id = ? AND shard_ordinal = ?
		AND attempt_number = ?`, executionID, ordinal, attempt))
	if errors.Is(err, sql.ErrNoRows) {
		return readOnlyFanoutModelCall{}, apperror.New(
			apperror.CodeNotFound, "read-only fan-out model call was not found")
	}
	return call, err
}

func scanReadOnlyFanoutModelCall(row scanner) (readOnlyFanoutModelCall, error) {
	var call readOnlyFanoutModelCall
	var usageRecorded, elapsedRecorded int
	var started string
	var finished sql.NullString
	if err := row.Scan(&call.ExecutionID, &call.PlanID, &call.RunID,
		&call.ShardOrdinal, &call.AttemptNumber, &call.LeaseID,
		&call.LeaseGeneration, &call.Provider, &call.Model, &call.Status,
		&call.Outcome, &call.InputFingerprint, &call.ResponseDigest,
		&call.ReservedInputTokens, &call.ReservedOutput, &call.ReservedTotal,
		&call.ReservedMillis, &usageRecorded, &call.InputTokens,
		&call.OutputTokens, &call.TotalTokens, &elapsedRecorded,
		&call.ElapsedMillis, &call.ErrorCode, &call.ErrorReason, &call.Version,
		&started, &finished); err != nil {
		return readOnlyFanoutModelCall{}, err
	}
	call.UsageRecorded = usageRecorded == 1
	call.ElapsedRecorded = elapsedRecorded == 1
	call.StartedAt = parseTS(started)
	if finished.Valid {
		value := parseTS(finished.String)
		call.FinishedAt = &value
	}
	return call, nil
}

func allowedReadOnlyFanoutPathsTx(ctx context.Context, queryer readOnlyFanoutQueryer,
	planID string, shardOrdinal int,
) (map[string]struct{}, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT relative_path
		FROM readonly_fanout_files WHERE plan_id = ? AND shard_ordinal = ?
		ORDER BY ordinal`, planID, shardOrdinal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	paths := map[string]struct{}{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		paths[value] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, apperror.New(apperror.CodeConflict,
			"read-only fan-out shard has no immutable manifest files")
	}
	return paths, nil
}

func getReadOnlyFanoutExecutionOperation(ctx context.Context,
	queryer readOnlyFanoutQueryer, keyDigest string,
) (domain.ReadOnlyFanoutExecutionOperation, bool, error) {
	var operation domain.ReadOnlyFanoutExecutionOperation
	var created string
	err := queryer.QueryRowContext(ctx, `SELECT operation_key_digest,
		request_fingerprint, execution_id, plan_id, run_id, requested_by, created_at
		FROM readonly_fanout_execution_operations WHERE operation_key_digest = ?`,
		keyDigest).Scan(&operation.KeyDigest, &operation.RequestFingerprint,
		&operation.ExecutionID, &operation.PlanID, &operation.RunID,
		&operation.RequestedBy, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ReadOnlyFanoutExecutionOperation{}, false, nil
	}
	if err != nil {
		return domain.ReadOnlyFanoutExecutionOperation{}, false, err
	}
	operation.CreatedAt = parseTS(created)
	if err := operation.Validate(); err != nil {
		return domain.ReadOnlyFanoutExecutionOperation{}, false,
			apperror.Wrap(apperror.CodeConflict,
				"stored read-only fan-out execution operation is invalid", err)
	}
	return operation, true, nil
}

func normalizeReadOnlyFanoutExecution(execution domain.ReadOnlyFanoutExecution,
) domain.ReadOnlyFanoutExecution {
	execution.ID = strings.TrimSpace(execution.ID)
	execution.PlanID = strings.TrimSpace(execution.PlanID)
	execution.RunID = strings.TrimSpace(execution.RunID)
	execution.WorkspaceID = strings.TrimSpace(execution.WorkspaceID)
	execution.Status = domain.ReadOnlyFanoutExecutionStatus(strings.TrimSpace(
		string(execution.Status)))
	execution.SnapshotDigest = strings.TrimSpace(execution.SnapshotDigest)
	execution.RequestedBy = strings.TrimSpace(redact.String(execution.RequestedBy))
	execution.StopCode = normalizeReadOnlyFanoutCode(execution.StopCode)
	execution.StartedAt = execution.StartedAt.UTC()
	execution.UpdatedAt = execution.UpdatedAt.UTC()
	if execution.FinishedAt != nil {
		value := execution.FinishedAt.UTC()
		execution.FinishedAt = &value
	}
	execution.Shards = append([]domain.ReadOnlyFanoutExecutionShard(nil),
		execution.Shards...)
	for index := range execution.Shards {
		shard := &execution.Shards[index]
		shard.ExecutionID = strings.TrimSpace(shard.ExecutionID)
		shard.PlanID = strings.TrimSpace(shard.PlanID)
		shard.Status = domain.ReadOnlyFanoutExecutionShardStatus(strings.TrimSpace(
			string(shard.Status)))
		shard.InputDigest = strings.TrimSpace(shard.InputDigest)
		shard.Provider = strings.TrimSpace(shard.Provider)
		shard.Model = strings.TrimSpace(shard.Model)
		shard.ReportJSON = strings.TrimSpace(redact.String(shard.ReportJSON))
		shard.ReportDigest = strings.TrimSpace(shard.ReportDigest)
		shard.ErrorCode, shard.ErrorReason = normalizeReadOnlyFanoutError(
			shard.ErrorCode, shard.ErrorReason)
		shard.CreatedAt = shard.CreatedAt.UTC()
		shard.UpdatedAt = shard.UpdatedAt.UTC()
		if shard.StartedAt != nil {
			value := shard.StartedAt.UTC()
			shard.StartedAt = &value
		}
		if shard.FinishedAt != nil {
			value := shard.FinishedAt.UTC()
			shard.FinishedAt = &value
		}
	}
	return execution
}

func normalizeReadOnlyFanoutExecutionOperation(
	operation domain.ReadOnlyFanoutExecutionOperation,
) domain.ReadOnlyFanoutExecutionOperation {
	operation.KeyDigest = strings.TrimSpace(operation.KeyDigest)
	operation.RequestFingerprint = strings.TrimSpace(operation.RequestFingerprint)
	operation.ExecutionID = strings.TrimSpace(operation.ExecutionID)
	operation.PlanID = strings.TrimSpace(operation.PlanID)
	operation.RunID = strings.TrimSpace(operation.RunID)
	operation.RequestedBy = strings.TrimSpace(redact.String(operation.RequestedBy))
	operation.CreatedAt = operation.CreatedAt.UTC()
	return operation
}

func normalizeReadOnlyFanoutDecision(decision policy.Decision) policy.Decision {
	decision.Risk = strings.TrimSpace(redact.String(decision.Risk))
	decision.Reason = strings.TrimSpace(redact.String(decision.Reason))
	if runes := []rune(decision.Risk); len(runes) > 256 {
		decision.Risk = string(runes[:256])
	}
	if runes := []rune(decision.Reason); len(runes) > 2048 {
		decision.Reason = string(runes[:2048])
	}
	return decision
}

func validateReadOnlyFanoutExecutionCreate(lease domain.RunExecutionLease,
	execution domain.ReadOnlyFanoutExecution,
	operation domain.ReadOnlyFanoutExecutionOperation, decision policy.Decision,
) error {
	if err := lease.Validate(); err != nil || lease.Status != domain.RunExecutionLeaseActive {
		return apperror.New(apperror.CodeInvalidArgument,
			"active Run execution lease is required")
	}
	if err := execution.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"read-only fan-out execution is invalid", err)
	}
	if execution.Status != domain.ReadOnlyFanoutExecutionRunning ||
		execution.Version != 1 || execution.FinishedAt != nil || execution.StopCode != "" {
		return apperror.New(apperror.CodeInvalidArgument,
			"new read-only fan-out execution must be running at version one")
	}
	for _, shard := range execution.Shards {
		if shard.Status != domain.ReadOnlyFanoutExecutionShardPending ||
			shard.Version != 1 || !shard.CreatedAt.Equal(execution.StartedAt) ||
			!shard.UpdatedAt.Equal(execution.StartedAt) {
			return apperror.New(apperror.CodeInvalidArgument,
				"new read-only fan-out execution shards must be pending")
		}
	}
	if err := operation.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"read-only fan-out execution operation is invalid", err)
	}
	if lease.RunID != execution.RunID || operation.ExecutionID != execution.ID ||
		operation.PlanID != execution.PlanID || operation.RunID != execution.RunID ||
		operation.RequestedBy != execution.RequestedBy ||
		!operation.CreatedAt.Equal(execution.StartedAt) {
		return apperror.New(apperror.CodeInvalidArgument,
			"read-only fan-out execution operation does not match its execution")
	}
	expectedFingerprint := runmutation.Fingerprint(
		"readonly_fanout_execution_request.v1", execution.PlanID,
		execution.RunID, execution.RequestedBy,
		fmt.Sprint(execution.MaxOutputTokensPerShard))
	if operation.RequestFingerprint != expectedFingerprint {
		return apperror.New(apperror.CodeInvalidArgument,
			"read-only fan-out execution request fingerprint is invalid")
	}
	if !decision.Allowed || decision.NeedsApproval || decision.Reason == "" {
		return apperror.New(apperror.CodePolicyDenied,
			"read-only fan-out execution is not authorized by Policy")
	}
	return nil
}

func validateReadOnlyFanoutExecutionReplay(existing,
	request domain.ReadOnlyFanoutExecutionOperation,
) error {
	if existing.KeyDigest != request.KeyDigest ||
		existing.RequestFingerprint != request.RequestFingerprint ||
		existing.PlanID != request.PlanID ||
		existing.RunID != request.RunID || existing.RequestedBy != request.RequestedBy {
		return apperror.New(apperror.CodeConflict,
			"read-only fan-out execution operation key was used for different intent")
	}
	return nil
}

func normalizeReadOnlyFanoutCode(value string) string {
	value = strings.TrimSpace(redact.String(strings.ReplaceAll(value, "\x00", "")))
	if runes := []rune(value); len(runes) > 256 {
		value = string(runes[:256])
	}
	return value
}

func normalizeReadOnlyFanoutError(code string, reason string) (string, string) {
	code = normalizeReadOnlyFanoutCode(code)
	reason = strings.TrimSpace(redact.String(strings.ReplaceAll(reason, "\x00", "")))
	if runes := []rune(reason); len(runes) > domain.MaxReadOnlyFanoutFindingDetailRunes {
		reason = string(runes[:domain.MaxReadOnlyFanoutFindingDetailRunes])
	}
	return code, reason
}

func readOnlyFanoutShardSubject(executionID string, ordinal int) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("readonly_fanout_shard.v1\x00%s\x00%d",
		executionID, ordinal)))
	return "fanout-shard-" + hex.EncodeToString(digest[:8])
}
