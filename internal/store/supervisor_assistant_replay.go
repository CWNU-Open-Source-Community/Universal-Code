package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/providerhistory"
	"cyberagent-workbench/internal/session"
)

func hasSupervisorAssistantReplayTx(ctx context.Context, tx *sql.Tx) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='run_supervisor_assistant_replay'`).Scan(&count)
	return count == 1, err
}

func insertSupervisorAssistantReplayTx(ctx context.Context, tx *sql.Tx, checkpoint domain.SupervisorCheckpoint,
	attempt llm.ModelAttempt, replay *llm.ProviderReplay,
) error {
	if !replay.RequiresPrivateAssistantHistory() {
		return nil
	}
	raw, err := encodeSupervisorProviderReplay(replay, attempt, nil)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO run_supervisor_assistant_replay
		(run_id,turn,attempt_id,model_attempt,tool_round,provider,model,replay_blob,replay_sha256,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, checkpoint.RunID, checkpoint.NextTurn, checkpoint.AttemptID,
		attempt.Number, attempt.ToolRound, attempt.Provider, attempt.Model, raw, providerReplaySHA(raw), ts(time.Now().UTC()))
	return err
}

// Old migration fixtures do not gain a new protocol by lacking its ledger.
// Existing providers keep their original terminal replay semantics.
func requireSupervisorAssistantCandidateMatchTx(ctx context.Context, tx *sql.Tx, checkpoint domain.SupervisorCheckpoint,
	attempt llm.ModelAttempt, replay *llm.ProviderReplay,
) error {
	exists, err := hasSupervisorAssistantReplayTx(ctx, tx)
	if err != nil || !exists && !replay.RequiresPrivateAssistantHistory() {
		return err
	}
	if !exists {
		return apperror.New(apperror.CodeFailedPrecondition, "ordinary provider replay requires the current schema")
	}
	var stored []byte
	var digest string
	err = tx.QueryRowContext(ctx, `SELECT replay_blob,replay_sha256 FROM run_supervisor_assistant_replay
		WHERE run_id=? AND turn=? AND attempt_id=? AND model_attempt=?`, checkpoint.RunID,
		checkpoint.NextTurn, checkpoint.AttemptID, attempt.Number).Scan(&stored, &digest)
	if errors.Is(err, sql.ErrNoRows) && !replay.RequiresPrivateAssistantHistory() {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return apperror.New(apperror.CodeConflict, "model terminal replay introduced ordinary private state")
	}
	if err != nil {
		return err
	}
	expected, err := encodeSupervisorProviderReplay(replay, attempt, nil)
	if err != nil || digest != providerReplaySHA(stored) || !bytes.Equal(stored, expected) {
		return apperror.New(apperror.CodeConflict, "model terminal replay changed ordinary private state")
	}
	return nil
}

func latestSupervisorPrimaryModelTx(ctx context.Context, tx *sql.Tx, checkpoint domain.SupervisorCheckpoint) (int, string, error) {
	var number int
	var eventType string
	err := tx.QueryRowContext(ctx, `SELECT json_extract(payload_json,'$.model_attempt'),type FROM run_events
		WHERE run_id=? AND source='model_gateway' AND json_extract(payload_json,'$.attempt_id')=?
		AND json_extract(payload_json,'$.turn')=? AND COALESCE(json_extract(payload_json,'$.purpose'),'')=''
		AND type IN ('model.started','model.completed','model.failed') ORDER BY sequence DESC LIMIT 1`,
		checkpoint.RunID, checkpoint.AttemptID, checkpoint.NextTurn).Scan(&number, &eventType)
	return number, eventType, err
}

// Bind the original native response before Go projects its public message.
// Parsing, recovery and verified-delivery projections remain Go-owned; the
// accepted action is sealed separately in the immutable completion event.
func prepareSupervisorAssistantBindingTx(ctx context.Context, tx *sql.Tx, checkpoint domain.SupervisorCheckpoint,
	native llm.ChatResponse,
) (int, string, error) {
	exists, err := hasSupervisorAssistantReplayTx(ctx, tx)
	if err != nil || !exists && !native.Replay.RequiresPrivateAssistantHistory() {
		return 0, "", err
	}
	if !exists {
		return 0, "", apperror.New(apperror.CodeFailedPrecondition, "ordinary provider replay requires the current schema")
	}
	if !native.Replay.RequiresPrivateAssistantHistory() {
		var candidates int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_supervisor_assistant_replay WHERE run_id=? AND turn=? AND attempt_id=?`,
			checkpoint.RunID, checkpoint.NextTurn, checkpoint.AttemptID).Scan(&candidates); err != nil {
			return 0, "", err
		}
		if candidates == 0 {
			return 0, "", nil
		}
	}
	number, eventType, err := latestSupervisorPrimaryModelTx(ctx, tx, checkpoint)
	if err != nil || eventType != events.ModelCompletedEvent {
		return 0, "", apperror.New(apperror.CodeFailedPrecondition, "ordinary replay has no latest successful model source")
	}
	var raw []byte
	var digest, provider, model string
	err = tx.QueryRowContext(ctx, `SELECT replay_blob,replay_sha256,provider,model FROM run_supervisor_assistant_replay
		WHERE run_id=? AND turn=? AND attempt_id=? AND model_attempt=?`, checkpoint.RunID, checkpoint.NextTurn,
		checkpoint.AttemptID, number).Scan(&raw, &digest, &provider, &model)
	if errors.Is(err, sql.ErrNoRows) && !native.Replay.RequiresPrivateAssistantHistory() {
		return 0, "", nil
	}
	if err != nil || !native.Replay.RequiresPrivateAssistantHistory() || digest != providerReplaySHA(raw) ||
		provider != native.Provider || model != native.Model || native.Text != native.Replay.AssistantText() || len(native.ToolCalls) != 0 {
		return 0, "", apperror.New(apperror.CodeConflict, "ordinary replay does not match the completing native response")
	}
	expected, err := encodeSupervisorProviderReplay(native.Replay, llm.ModelAttempt{Provider: provider, Model: model}, nil)
	if err != nil || !bytes.Equal(raw, expected) {
		return 0, "", apperror.New(apperror.CodeConflict, "ordinary replay source changed before turn completion")
	}
	return number, digest, nil
}

func insertSupervisorAssistantBindingTx(ctx context.Context, tx *sql.Tx, checkpoint domain.SupervisorCheckpoint,
	message session.Message, number int, digest string,
) error {
	if number == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO run_supervisor_assistant_replay_bindings
		(session_message_id,run_id,turn,attempt_id,model_attempt,projected_content_sha256,replay_sha256,created_at)
		VALUES (?,?,?,?,?,?,?,?)`, message.ID, checkpoint.RunID, checkpoint.NextTurn, checkpoint.AttemptID,
		number, message.Provenance.ContentSHA256, digest, ts(time.Now().UTC()))
	return err
}

func requireSupervisorAssistantCompletionReplayTx(ctx context.Context, tx *sql.Tx, checkpoint domain.SupervisorCheckpoint,
	native llm.ChatResponse, action domain.RootAction,
) error {
	exists, err := hasSupervisorAssistantReplayTx(ctx, tx)
	if err != nil || !exists && !native.Replay.RequiresPrivateAssistantHistory() {
		return err
	}
	if !exists {
		return apperror.New(apperror.CodeFailedPrecondition, "ordinary provider replay requires the current schema")
	}
	var number int
	var contentDigest, replayDigest, projected, actionDigest string
	err = tx.QueryRowContext(ctx, `SELECT b.model_attempt,b.projected_content_sha256,b.replay_sha256,m.content,
		json_extract(e.payload_json,'$.accepted_action_sha256')
		FROM run_supervisor_assistant_replay_bindings b JOIN session_messages m ON m.id=b.session_message_id
		JOIN run_events e ON e.run_id=b.run_id AND e.type=? AND e.source='run_supervisor' AND e.subject_id=b.attempt_id
		AND json_extract(e.payload_json,'$.turn')=b.turn AND json_extract(e.payload_json,'$.attempt_id')=b.attempt_id
		AND json_extract(e.payload_json,'$.assistant_message_id')=b.session_message_id
		WHERE b.run_id=? AND b.turn=? AND b.attempt_id=?`, events.AgentTurnCompletedEvent, checkpoint.RunID, checkpoint.NextTurn, checkpoint.AttemptID).
		Scan(&number, &contentDigest, &replayDigest, &projected, &actionDigest)
	if errors.Is(err, sql.ErrNoRows) && !native.Replay.RequiresPrivateAssistantHistory() {
		return nil
	}
	if err != nil || !native.Replay.RequiresPrivateAssistantHistory() || native.Text != native.Replay.AssistantText() ||
		contentDigest != session.ContentSHA256(projected) || len(native.ToolCalls) != 0 || actionDigest != supervisorAcceptedActionSHA(action) {
		return apperror.New(apperror.CodeConflict, "turn completion replay changed ordinary private history")
	}
	raw, err := encodeSupervisorProviderReplay(native.Replay, llm.ModelAttempt{Provider: native.Provider, Model: native.Model}, nil)
	if err != nil || replayDigest != providerReplaySHA(raw) {
		return apperror.New(apperror.CodeConflict, "turn completion replay changed its native source")
	}
	return requireSupervisorAssistantCandidateMatchTx(ctx, tx, checkpoint,
		llm.ModelAttempt{Number: number, Provider: native.Provider, Model: native.Model}, native.Replay)
}

func supervisorAcceptedActionSHA(action domain.RootAction) string {
	// RootAction contains only scalar strings. Seal the already accepted public
	// action rather than reparse native text or include private reasoning.
	raw, _ := json.Marshal(sanitizeRootAction(action))
	return providerReplaySHA(raw)
}

// Selected message IDs are re-read under the current lease. Historical replay
// binds immutable successes in this session; it never restores historical
// execution authority, resumes a tool, or enters a public history DTO.
func (s *SQLiteStore) LoadSupervisorAssistantHistory(ctx context.Context, checkpoint domain.SupervisorCheckpoint,
	messageIDs []int64,
) (map[int64]providerhistory.Assistant, error) {
	if checkpoint.Validate() != nil || len(messageIDs) > 256 {
		return nil, apperror.New(apperror.CodeInvalidArgument, "private assistant history selection is invalid or unbounded")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	run, _, err := requireActiveSupervisorAttemptTx(ctx, tx, checkpoint)
	if err != nil {
		return nil, err
	}
	result := make(map[int64]providerhistory.Assistant, len(messageIDs))
	privateBytes := 0
	for _, id := range messageIDs {
		if id <= 0 {
			return nil, apperror.New(apperror.CodeInvalidArgument, "private assistant history requires stored source identities")
		}
		if _, duplicate := result[id]; duplicate {
			return nil, apperror.New(apperror.CodeInvalidArgument, "private assistant history repeats a source")
		}
		message, err := scanSessionMessage(tx.QueryRowContext(ctx, `SELECT id,session_id,role,content,provenance_version,source_kind,source_ref,
			content_sha256,instruction_authorized,token_estimate,compacted,created_at FROM session_messages WHERE id=?`, id))
		if err != nil || message.SessionID != run.SessionID || message.Role != "assistant" || message.Compacted ||
			message.Provenance.SourceKind != session.SourceModelResponse || message.Provenance.InstructionAuthorized {
			return nil, apperror.New(apperror.CodeFailedPrecondition, "private assistant history lost its accepted session source")
		}
		var source domain.SupervisorCheckpoint
		var number, lastRound int
		var provider, model, digest, boundDigest, projectedDigest string
		var raw []byte
		err = tx.QueryRowContext(ctx, `SELECT p.run_id,p.turn,p.attempt_id,p.model_attempt,p.tool_round,p.provider,p.model,
			p.replay_blob,p.replay_sha256,b.replay_sha256,b.projected_content_sha256
			FROM run_supervisor_assistant_replay_bindings b JOIN run_supervisor_assistant_replay p
			ON p.run_id=b.run_id AND p.turn=b.turn AND p.attempt_id=b.attempt_id AND p.model_attempt=b.model_attempt
			JOIN runs r ON r.id=p.run_id WHERE b.session_message_id=? AND r.session_id=?`, id, run.SessionID).
			Scan(&source.RunID, &source.NextTurn, &source.AttemptID, &number, &lastRound, &provider, &model, &raw, &digest, &boundDigest, &projectedDigest)
		if err != nil || digest != providerReplaySHA(raw) || digest != boundDigest || projectedDigest != message.Provenance.ContentSHA256 {
			return nil, apperror.New(apperror.CodeFailedPrecondition, "assistant history has no intact native replay; old or imported history is unsupported")
		}
		latest, eventType, err := latestSupervisorPrimaryModelTx(ctx, tx, source)
		if err != nil || latest != number || eventType != events.ModelCompletedEvent {
			return nil, apperror.New(apperror.CodeFailedPrecondition, "private assistant history model source was superseded")
		}
		if err := requireSupervisorHistoryModelSourceTx(ctx, tx, source, number, lastRound, provider, model, 0); err != nil {
			return nil, err
		}
		var completed int
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_events WHERE run_id=? AND type=? AND source='run_supervisor'
			AND subject_id=? AND json_extract(payload_json,'$.turn')=? AND json_extract(payload_json,'$.attempt_id')=?
			AND json_extract(payload_json,'$.assistant_message_id')=? AND json_extract(payload_json,'$.provider')=?
			AND json_extract(payload_json,'$.model')=? AND json_type(payload_json,'$.accepted_action_sha256')='text'
			AND length(json_extract(payload_json,'$.accepted_action_sha256'))=64
			AND json_extract(payload_json,'$.accepted_action_sha256') NOT GLOB '*[^0-9a-f]*'`, source.RunID, events.AgentTurnCompletedEvent, source.AttemptID,
			source.NextTurn, source.AttemptID, id, provider, model).Scan(&completed)
		if err != nil {
			return nil, err
		}
		if completed != 1 {
			return nil, apperror.New(apperror.CodeFailedPrecondition, "private assistant history lost its completed turn binding")
		}
		replay, err := llm.DecodeProviderReplay(raw)
		if err != nil || !replay.RequiresPrivateAssistantHistory() || replay.ValidateSource(provider, model) != nil || replay.ValidateToolCalls(nil) != nil {
			return nil, apperror.New(apperror.CodeFailedPrecondition, "private ordinary assistant replay is invalid")
		}
		privateBytes += len(raw)
		rounds, err := supervisorHistoryToolRoundsTx(ctx, tx, source)
		if err != nil || len(rounds) != lastRound {
			return nil, apperror.New(apperror.CodeFailedPrecondition, "private assistant history lost its complete native tool segment")
		}
		history := providerhistory.Assistant{Replay: replay, Rounds: rounds, RoundReplay: make(map[int]*llm.ProviderReplay), AttemptID: source.AttemptID}
		previousAttempt := 0
		for index, round := range rounds {
			if round.ProjectHook() {
				if round.Round != index+1 || !round.Complete() || round.ModelAttempt < previousAttempt || round.ModelAttempt >= number {
					return nil, apperror.New(apperror.CodeFailedPrecondition, "project hook history ordering is invalid")
				}
				previousAttempt = round.ModelAttempt
				continue
			}
			if round.Round != index+1 || !round.Complete() || round.ModelAttempt <= previousAttempt || round.ModelAttempt >= number {
				return nil, apperror.New(apperror.CodeFailedPrecondition, "private assistant history tool ordering is invalid")
			}
			previousAttempt = round.ModelAttempt
			var toolRaw []byte
			var toolDigest string
			err := tx.QueryRowContext(ctx, `SELECT replay_blob,replay_sha256 FROM run_supervisor_provider_replay
				WHERE run_id=? AND turn=? AND attempt_id=? AND round=? AND model_attempt=? AND provider=? AND model=?`,
				source.RunID, source.NextTurn, source.AttemptID, round.Round, round.ModelAttempt, provider, model).Scan(&toolRaw, &toolDigest)
			if err != nil || toolDigest != providerReplaySHA(toolRaw) {
				return nil, apperror.New(apperror.CodeFailedPrecondition, "private assistant history lost its native tool replay")
			}
			if err := requireSupervisorHistoryModelSourceTx(ctx, tx, source, round.ModelAttempt, round.Round-1, provider, model, len(round.Calls)); err != nil {
				return nil, err
			}
			calls, err := supervisorProviderReplayCallsTx(ctx, tx, source, round.Round, round.ModelAttempt)
			if err != nil {
				return nil, err
			}
			toolReplay, err := llm.DecodeProviderReplay(toolRaw)
			if err != nil || !toolReplay.RequiresPrivateAssistantHistory() || toolReplay.ValidateSource(provider, model) != nil || toolReplay.ValidateToolCalls(calls) != nil {
				return nil, apperror.New(apperror.CodeFailedPrecondition, "private assistant history tool replay is invalid")
			}
			history.RoundReplay[round.Round] = toolReplay
			privateBytes += len(toolRaw)
		}
		if privateBytes > llm.MaxProviderReplayBytes {
			return nil, apperror.New(apperror.CodeResourceExhausted, "private assistant history exceeds its native replay bound")
		}
		result[id] = history
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func requireSupervisorHistoryModelSourceTx(ctx context.Context, tx *sql.Tx, checkpoint domain.SupervisorCheckpoint,
	number, toolRound int, provider, model string, callCount int,
) error {
	var matched int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_events WHERE run_id=? AND type='model.completed' AND source='model_gateway'
		AND subject_id=? AND json_extract(payload_json,'$.turn')=? AND json_extract(payload_json,'$.attempt_id')=?
		AND json_extract(payload_json,'$.model_attempt')=? AND json_extract(payload_json,'$.tool_round')=?
		AND json_extract(payload_json,'$.provider')=? AND json_extract(payload_json,'$.model')=?
		AND json_extract(payload_json,'$.tool_call_count')=? AND json_extract(payload_json,'$.outcome')='success'
		AND COALESCE(json_extract(payload_json,'$.purpose'),'')=''`, checkpoint.RunID, supervisorModelSubject(checkpoint, number),
		checkpoint.NextTurn, checkpoint.AttemptID, number, toolRound, provider, model, callCount).Scan(&matched)
	if err != nil {
		return err
	}
	if matched != 1 {
		return apperror.New(apperror.CodeFailedPrecondition, "private assistant history lost its successful primary model source")
	}
	return nil
}

func supervisorHistoryToolRoundsTx(ctx context.Context, tx *sql.Tx, checkpoint domain.SupervisorCheckpoint) ([]domain.SupervisorToolRound, error) {
	rows, err := tx.QueryContext(ctx, `SELECT r.run_id,r.turn,r.attempt_id,r.round,r.model_attempt,r.created_at,r.completed_at,
		c.run_id,c.turn,c.attempt_id,c.round,c.position,c.model_attempt,c.call_id,
		c.stream_response_id,c.stream_item_id,c.stream_call_id,c.tool_name,c.payload_json,c.authority_json,
		c.status,c.result_json,c.error_code,c.created_at,c.completed_at
		FROM run_supervisor_tool_rounds r JOIN run_supervisor_tool_calls c
		ON c.run_id=r.run_id AND c.turn=r.turn AND c.attempt_id=r.attempt_id AND c.round=r.round
		WHERE r.run_id=? AND r.turn=? AND r.attempt_id=? ORDER BY r.round,c.position`,
		checkpoint.RunID, checkpoint.NextTurn, checkpoint.AttemptID)
	if err != nil {
		return nil, err
	}
	rounds, err := scanSupervisorToolRounds(rows, domain.MaxSupervisorExecutionRounds)
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if err := enrichProjectHookRounds(ctx, tx, rounds); err != nil {
		return nil, err
	}
	return rounds, nil
}
