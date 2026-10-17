package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
)

func encodeSupervisorProviderReplay(replay *llm.ProviderReplay, attempt llm.ModelAttempt, calls []llm.ToolCall) ([]byte, error) {
	if replay == nil {
		return nil, nil
	}
	if err := replay.ValidateSource(attempt.Provider, attempt.Model); err != nil {
		return nil, apperror.New(apperror.CodeInvalidArgument, "provider replay source does not match its model attempt")
	}
	if err := replay.ValidateToolCalls(calls); err != nil {
		return nil, apperror.New(apperror.CodeInvalidArgument, "provider replay does not match its durable tool calls")
	}
	raw, err := replay.EncodeForStore()
	if err != nil || len(raw) == 0 || len(raw) > 8*1024*1024 {
		return nil, apperror.New(apperror.CodeInvalidArgument, "invalid private provider replay")
	}
	return raw, nil
}

func providerReplaySHA(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// A claimed semantic recovery starts one new transport group while the global
// model attempt keeps increasing. Older migration fixtures retain their strict
// original transport count; absence of the ledger never grants a fresh group.
func supervisorContextRecoverySourceTx(ctx context.Context, tx *sql.Tx, checkpoint domain.SupervisorCheckpoint, protocolRepair, toolRound int) (int, error) {
	source, _, err := supervisorContextRecoveryTx(ctx, tx, checkpoint, protocolRepair, toolRound)
	return source, err
}

func supervisorContextRecoveryTx(ctx context.Context, tx *sql.Tx, checkpoint domain.SupervisorCheckpoint, protocolRepair, toolRound int) (int, int, error) {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='run_supervisor_context_recoveries'`).Scan(&exists); err != nil {
		return 0, 0, err
	}
	if exists == 0 {
		return 0, 0, nil
	}
	var source, originalInput int
	err := tx.QueryRowContext(ctx, `SELECT model_attempt,original_input_tokens FROM run_supervisor_context_recoveries WHERE run_id=? AND turn=? AND attempt_id=? AND tool_round=? AND protocol_repair=?`,
		checkpoint.RunID, checkpoint.NextTurn, checkpoint.AttemptID, toolRound, protocolRepair).Scan(&source, &originalInput)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil
	}
	return source, originalInput, err
}

// A claim reserves one recovery group, but never proves that its request was
// reduced. Check the actual Go-computed request estimate in the same transaction
// as its start event, including after a restart or execution lease takeover.
func requireSupervisorContextRecoveryReductionTx(ctx context.Context, tx *sql.Tx, checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt) error {
	if attempt.Purpose != "" {
		return nil
	}
	source, originalInput, err := supervisorContextRecoveryTx(ctx, tx, checkpoint, attempt.ProtocolRepair, attempt.ToolRound)
	if err != nil {
		return err
	}
	if source != 0 && (attempt.Number <= source || originalInput <= 0 || attempt.InputEstimate <= 0 || attempt.InputEstimate >= originalInput) {
		return apperror.New(apperror.CodeResourceExhausted, "context recovery requires a smaller model request before it can start")
	}
	return requireSupervisorContextRecoveryAvailableTx(ctx, tx, checkpoint, source, attempt.ProtocolRepair, attempt.ToolRound)
}

func requireSupervisorContextRecoveryAvailableTx(ctx context.Context, tx *sql.Tx, checkpoint domain.SupervisorCheckpoint, source, protocolRepair, toolRound int) error {
	var exhausted int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_events WHERE run_id=? AND type=? AND source='model_gateway'
		AND json_extract(payload_json,'$.attempt_id')=? AND json_extract(payload_json,'$.model_attempt')>?
		AND json_extract(payload_json,'$.tool_round')=? AND json_extract(payload_json,'$.protocol_repair')=?
		AND COALESCE(json_extract(payload_json,'$.purpose'),'')=''
		AND json_extract(payload_json,'$.failure_reason')='context_limit'`, checkpoint.RunID, events.ModelFailedEvent,
		checkpoint.AttemptID, source, toolRound, protocolRepair).Scan(&exhausted); err != nil {
		return err
	}
	if exhausted != 0 {
		if source == 0 {
			return apperror.New(apperror.CodeResourceExhausted, "context limit failure requires a prepared recovery before another model request")
		}
		return apperror.New(apperror.CodeResourceExhausted, "context recovery already reached the provider limit again")
	}
	return nil
}

// CheckSupervisorContextRecoveryInput avoids reserving money or a live call for
// a recovery request that cannot start. RecordSupervisorModelStarted repeats
// this check atomically; passing the preflight alone never authorizes a call.
func (s *SQLiteStore) CheckSupervisorContextRecoveryInput(ctx context.Context, checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt) error {
	if err := checkpoint.Validate(); err != nil {
		return err
	}
	if err := attempt.ValidateStarted(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument, "invalid context recovery model input", err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, current, err := requireActiveSupervisorAttemptTx(ctx, tx, checkpoint)
	if err != nil {
		return err
	}
	if err := requireSupervisorContextRecoveryReductionTx(ctx, tx, current, attempt); err != nil {
		return err
	}
	return tx.Commit()
}

// Read the durable reduction ceiling for planning only. This does not claim a
// recovery, reserve money or authorize dispatch; both existing gates still run.
func (s *SQLiteStore) SupervisorContextRecoveryInputLimit(ctx context.Context, checkpoint domain.SupervisorCheckpoint, protocolRepair, toolRound int) (int, bool, error) {
	if err := checkpoint.Validate(); err != nil {
		return 0, false, err
	}
	if protocolRepair < 0 || protocolRepair > 1 || toolRound < 0 || toolRound > domain.MaxSupervisorExecutionRounds {
		return 0, false, apperror.New(apperror.CodeInvalidArgument, "invalid context recovery planning phase")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback() }()
	_, current, err := requireActiveSupervisorAttemptTx(ctx, tx, checkpoint)
	if err != nil {
		return 0, false, err
	}
	source, original, err := supervisorContextRecoveryTx(ctx, tx, current, protocolRepair, toolRound)
	if err != nil {
		return 0, false, err
	}
	if err := requireSupervisorContextRecoveryAvailableTx(ctx, tx, current, source, protocolRepair, toolRound); err != nil {
		return 0, false, err
	}
	if source == 0 {
		return 0, false, tx.Commit()
	}
	if original <= 0 {
		return 0, false, apperror.New(apperror.CodeResourceExhausted, "context recovery has no durable input capacity")
	}
	return original - 1, true, tx.Commit()
}

func insertSupervisorProviderReplayTx(ctx context.Context, tx *sql.Tx, checkpoint domain.SupervisorCheckpoint,
	attempt llm.ModelAttempt, replay *llm.ProviderReplay, calls []llm.ToolCall,
) error {
	if replay == nil {
		return nil
	}
	raw, err := encodeSupervisorProviderReplay(replay, attempt, calls)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO run_supervisor_provider_replay
		(run_id,turn,attempt_id,round,model_attempt,provider,model,replay_blob,replay_sha256,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, checkpoint.RunID, checkpoint.NextTurn, checkpoint.AttemptID,
		attempt.ToolRound+1, attempt.Number, attempt.Provider, attempt.Model, raw, providerReplaySHA(raw), ts(time.Now().UTC()))
	return err
}

func requireSupervisorProviderReplayMatchTx(ctx context.Context, tx *sql.Tx, checkpoint domain.SupervisorCheckpoint,
	attempt llm.ModelAttempt, replay *llm.ProviderReplay, calls []llm.ToolCall,
) error {
	if len(calls) == 0 {
		if err := requireSupervisorAssistantCandidateMatchTx(ctx, tx, checkpoint, attempt, replay); err != nil {
			return err
		}
		if replay.RequiresPrivateAssistantHistory() {
			return nil
		}
	}
	expected, err := encodeSupervisorProviderReplay(replay, attempt, calls)
	if err != nil {
		return err
	}
	var stored []byte
	var digest string
	err = tx.QueryRowContext(ctx, `SELECT replay_blob,replay_sha256 FROM run_supervisor_provider_replay
		WHERE run_id=? AND turn=? AND attempt_id=? AND model_attempt=?`, checkpoint.RunID,
		checkpoint.NextTurn, checkpoint.AttemptID, attempt.Number).Scan(&stored, &digest)
	if errors.Is(err, sql.ErrNoRows) && replay == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return apperror.New(apperror.CodeConflict, "model terminal replay introduced new private provider state")
	}
	if err != nil {
		return err
	}
	if digest != providerReplaySHA(stored) || !bytes.Equal(stored, expected) {
		return apperror.New(apperror.CodeConflict, "model terminal replay changed private provider state")
	}
	return nil
}

// LoadSupervisorProviderReplay exposes only the active turn's private model
// state. The ordinary tool/history DTOs deliberately have no replay field.
func (s *SQLiteStore) LoadSupervisorProviderReplay(ctx context.Context, checkpoint domain.SupervisorCheckpoint) (map[int]*llm.ProviderReplay, error) {
	if err := checkpoint.Validate(); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, _, err = requireActiveSupervisorAttemptTx(ctx, tx, checkpoint); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT round,model_attempt,provider,model,replay_blob,replay_sha256
		FROM run_supervisor_provider_replay WHERE run_id=? AND turn=? AND attempt_id=? ORDER BY round`,
		checkpoint.RunID, checkpoint.NextTurn, checkpoint.AttemptID)
	if err != nil {
		return nil, err
	}
	type savedReplay struct {
		round, attempt          int
		provider, model, digest string
		raw                     []byte
	}
	var saved []savedReplay
	for rows.Next() {
		var entry savedReplay
		if err = rows.Scan(&entry.round, &entry.attempt, &entry.provider, &entry.model, &entry.raw, &entry.digest); err != nil {
			_ = rows.Close()
			return nil, err
		}
		saved = append(saved, entry)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	result := make(map[int]*llm.ProviderReplay, len(saved))
	for _, entry := range saved {
		if entry.digest != providerReplaySHA(entry.raw) {
			return nil, apperror.New(apperror.CodeFailedPrecondition, "private provider replay integrity check failed")
		}
		var matched int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_supervisor_tool_rounds r JOIN run_events e ON e.run_id=r.run_id
			WHERE r.run_id=? AND r.turn=? AND r.attempt_id=? AND r.round=? AND r.model_attempt=?
			AND e.type='model.completed' AND e.source='model_gateway' AND e.subject_id=?
			AND json_extract(e.payload_json,'$.provider')=? AND json_extract(e.payload_json,'$.model')=?
			AND json_extract(e.payload_json,'$.outcome')='success'`, checkpoint.RunID, checkpoint.NextTurn,
			checkpoint.AttemptID, entry.round, entry.attempt, supervisorModelSubject(checkpoint, entry.attempt), entry.provider, entry.model).Scan(&matched); err != nil {
			return nil, err
		}
		if matched != 1 {
			return nil, apperror.New(apperror.CodeFailedPrecondition, "private provider replay lost its successful model source")
		}
		calls, err := supervisorProviderReplayCallsTx(ctx, tx, checkpoint, entry.round, entry.attempt)
		if err != nil {
			return nil, err
		}
		replay, err := llm.DecodeProviderReplay(entry.raw)
		if err != nil || replay == nil || replay.ValidateSource(entry.provider, entry.model) != nil || replay.ValidateToolCalls(calls) != nil {
			return nil, apperror.New(apperror.CodeFailedPrecondition, "private provider replay does not match its tool round")
		}
		result[entry.round] = replay
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func supervisorProviderReplayCallsTx(ctx context.Context, tx *sql.Tx, checkpoint domain.SupervisorCheckpoint, round, attempt int) ([]llm.ToolCall, error) {
	rows, err := tx.QueryContext(ctx, `SELECT call_id,tool_name,payload_json,model_attempt FROM run_supervisor_tool_calls
		WHERE run_id=? AND turn=? AND attempt_id=? AND round=? ORDER BY position`, checkpoint.RunID,
		checkpoint.NextTurn, checkpoint.AttemptID, round)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var calls []llm.ToolCall
	for rows.Next() {
		var call llm.ToolCall
		var payload string
		var modelAttempt int
		if err := rows.Scan(&call.ID, &call.Name, &payload, &modelAttempt); err != nil {
			return nil, err
		}
		if modelAttempt != attempt {
			return nil, apperror.New(apperror.CodeFailedPrecondition, "private provider replay tool attempt changed")
		}
		call.Arguments = json.RawMessage(payload)
		calls = append(calls, call)
	}
	return calls, rows.Err()
}

// ClaimSupervisorContextRecovery is a durable one-shot allowance, not a new
// model retry budget. A claim cannot authorize work under an old owner/turn.
func (s *SQLiteStore) ClaimSupervisorContextRecovery(ctx context.Context, checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt) (bool, error) {
	if err := checkpoint.Validate(); err != nil {
		return false, err
	}
	attempt = sanitizeModelAttempt(attempt)
	if attempt.ValidateFailed() != nil || attempt.Purpose != "" || attempt.Outcome != llm.OutcomePermanent ||
		attempt.FailureReason != llm.ProviderFailureReason("context_limit") || attempt.RetryPlanned ||
		(attempt.SupervisorAttemptID != "" && attempt.SupervisorAttemptID != checkpoint.AttemptID) {
		return false, apperror.New(apperror.CodeInvalidArgument, "context recovery requires a nonretryable context limit failure")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	_, current, err := requireActiveSupervisorAttemptTx(ctx, tx, checkpoint)
	if err != nil {
		return false, err
	}
	if (attempt.ProtocolRepair == 0 && current.RepairPhase != domain.ProtocolRepairNone) ||
		(attempt.ProtocolRepair == 1 && current.RepairPhase != domain.ProtocolRepairPending) {
		return false, apperror.New(apperror.CodeConflict, "context recovery protocol phase changed")
	}
	var round int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(round),0) FROM run_supervisor_tool_rounds
		WHERE run_id=? AND turn=? AND attempt_id=?`, checkpoint.RunID, checkpoint.NextTurn, checkpoint.AttemptID).Scan(&round); err != nil {
		return false, err
	}
	if round != attempt.ToolRound {
		return false, apperror.New(apperror.CodeConflict, "context recovery tool phase changed")
	}
	subject := supervisorModelSubject(checkpoint, attempt.Number)
	if err = requireSupervisorModelStartedMatchTx(ctx, tx, checkpoint.RunID, subject, attempt); err != nil {
		return false, err
	}
	var startedJSON string
	if err = tx.QueryRowContext(ctx, `SELECT payload_json FROM run_events WHERE run_id=? AND type=? AND source='model_gateway' AND subject_id=?`,
		checkpoint.RunID, events.ModelStartedEvent, subject).Scan(&startedJSON); err != nil {
		return false, err
	}
	started, err := parseSupervisorModelStartedPayload(startedJSON)
	if err != nil || started.LeaseID != current.LeaseID || started.LeaseGeneration != current.LeaseGeneration {
		return false, apperror.New(apperror.CodeConflict, "context recovery model source belongs to another execution lease")
	}
	if started.InputEstimate <= 0 {
		return false, apperror.New(apperror.CodeFailedPrecondition, "context recovery model source has no bounded request estimate")
	}
	var superseded int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_events WHERE run_id=? AND type=? AND source='model_gateway'
		AND json_extract(payload_json,'$.attempt_id')=? AND json_extract(payload_json,'$.model_attempt')>?
		AND json_extract(payload_json,'$.tool_round')=? AND json_extract(payload_json,'$.protocol_repair')=?
		AND COALESCE(json_extract(payload_json,'$.purpose'),'')=''`, checkpoint.RunID, events.ModelStartedEvent,
		checkpoint.AttemptID, attempt.Number, attempt.ToolRound, attempt.ProtocolRepair).Scan(&superseded); err != nil {
		return false, err
	}
	if superseded != 0 {
		return false, apperror.New(apperror.CodeConflict, "context recovery model source was superseded")
	}
	var failureJSON string
	if err = tx.QueryRowContext(ctx, `SELECT payload_json FROM run_events WHERE run_id=? AND type=? AND source='model_gateway' AND subject_id=?`,
		checkpoint.RunID, events.ModelFailedEvent, subject).Scan(&failureJSON); err != nil {
		return false, apperror.New(apperror.CodeFailedPrecondition, "context recovery has no durable failed model source")
	}
	var failure struct {
		Turn           int                       `json:"turn"`
		AttemptID      string                    `json:"attempt_id"`
		Number         int                       `json:"model_attempt"`
		ToolRound      int                       `json:"tool_round"`
		ProtocolRepair int                       `json:"protocol_repair"`
		Provider       string                    `json:"provider"`
		Model          string                    `json:"model"`
		Outcome        llm.Outcome               `json:"outcome"`
		Reason         llm.ProviderFailureReason `json:"failure_reason"`
		RetryPlanned   bool                      `json:"retry_planned"`
	}
	if json.Unmarshal([]byte(failureJSON), &failure) != nil || failure.Turn != checkpoint.NextTurn ||
		failure.AttemptID != checkpoint.AttemptID || failure.Number != attempt.Number ||
		failure.ToolRound != attempt.ToolRound || failure.ProtocolRepair != attempt.ProtocolRepair ||
		failure.Provider != attempt.Provider || failure.Model != attempt.Model || failure.Outcome != llm.OutcomePermanent ||
		failure.Reason != llm.ProviderFailureReason("context_limit") || failure.RetryPlanned {
		return false, apperror.New(apperror.CodeConflict, "context recovery failed model source does not match")
	}
	inserted, err := tx.ExecContext(ctx, `INSERT INTO run_supervisor_context_recoveries
		(run_id,turn,attempt_id,tool_round,protocol_repair,model_attempt,original_input_tokens,provider,model,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT(run_id,turn,attempt_id,tool_round,protocol_repair) DO NOTHING`,
		checkpoint.RunID, checkpoint.NextTurn, checkpoint.AttemptID, attempt.ToolRound, attempt.ProtocolRepair,
		attempt.Number, started.InputEstimate, attempt.Provider, attempt.Model, ts(time.Now().UTC()))
	if err != nil {
		return false, err
	}
	count, err := inserted.RowsAffected()
	if err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return count == 1, nil
}
