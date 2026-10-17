package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
)

const threadContextOperationSelect = "SELECT id,operation_id,thread_id,run_id,session_id,source_sha256,input_fingerprint,input_messages_omitted,status,provider,model,response_text,completion_sequence,usage_json,result_json,error_code,error_message,created_at,started_at FROM thread_context_compactions"

func scanThreadContextOperation(row scanner) (domain.ThreadContextCompactionOperation, error) {
	var o domain.ThreadContextCompactionOperation
	var usage, result, created string
	var started sql.NullString
	err := row.Scan(&o.ID, &o.OperationID, &o.ThreadID, &o.RunID, &o.SessionID, &o.SourceSHA256, &o.InputFingerprint, &o.InputMessagesOmitted, &o.Status, &o.Provider, &o.Model, &o.ResponseText, &o.CompletionSequence, &usage, &result, &o.ErrorCode, &o.ErrorMessage, &created, &started)
	if err != nil {
		return o, err
	}
	o.Usage = json.RawMessage(usage)
	if err = json.Unmarshal([]byte(result), &o.Result); err != nil {
		return o, err
	}
	o.CreatedAt = parseTS(created)
	if started.Valid {
		value := parseTS(started.String)
		o.StartedAt = &value
	}
	return o, nil
}

func readThreadContextSnapshot(ctx context.Context, q supervisorCompactionReader, threadID, runID string) (domain.ThreadContextCompactionSnapshot, error) {
	var snap domain.ThreadContextCompactionSnapshot
	var err error
	snap.Thread, err = scanThread(q.QueryRowContext(ctx, threadSelect+" WHERE id=?", threadID))
	if err != nil {
		return snap, apperror.Normalize(err)
	}
	if snap.Thread.Status != domain.ThreadActive || snap.Thread.LastRunID != runID || (snap.Thread.ActiveRunID != "" && snap.Thread.ActiveRunID != runID) {
		return snap, apperror.New(apperror.CodeConflict, "thread_context_source_changed: current Thread Run changed")
	}
	snap.Run, err = scanRun(q.QueryRowContext(ctx, "SELECT id,mission_id,session_id,status,config_json,budget_json,started_at,finished_at,created_at,updated_at FROM runs WHERE id=?", runID))
	if err != nil {
		return snap, err
	}
	snap.Mission, err = scanMission(q.QueryRowContext(ctx, "SELECT id,goal,profile,workspace_id,scope_json,created_at,updated_at FROM missions WHERE id=?", snap.Thread.MissionID))
	if err != nil {
		return snap, err
	}
	var bindings int
	if err = q.QueryRowContext(ctx, "SELECT COUNT(*) FROM thread_runs tr JOIN sessions sess ON sess.id=tr.session_id WHERE tr.thread_id=? AND tr.run_id=? AND tr.session_id=? AND sess.workspace_id=?", threadID, runID, snap.Run.SessionID, snap.Mission.WorkspaceID).Scan(&bindings); err != nil {
		return snap, err
	}
	if bindings != 1 || snap.Run.MissionID != snap.Thread.MissionID || snap.Thread.WorkspaceID != snap.Mission.WorkspaceID {
		return snap, apperror.New(apperror.CodeConflict, "thread_context_source_changed: Run/Session binding changed")
	}
	messages, err := listSupervisorContextMessages(ctx, q, snap.Run.SessionID)
	if err != nil {
		return snap, err
	}
	snap.Messages = (supervisorCompactionSnapshot{Messages: messages}).contextMessages()
	snap.Summary, snap.HasSummary, err = latestContextSummary(ctx, q, snap.Run.SessionID)
	if err != nil {
		return snap, err
	}
	if err = q.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0) FROM run_events WHERE run_id=? AND type IN (?,?,?,?,?)", runID, events.OperatorSteeringQueuedEvent, events.OperatorSteeringPreparedEvent, events.OperatorSteeringCommittedEvent, events.OperatorSteeringSupersededEvent, events.OperatorSteeringCancelledEvent).Scan(&snap.OperatorSequence); err != nil {
		return snap, err
	}
	if err = q.QueryRowContext(ctx, "SELECT COALESCE(MAX(id),0) FROM thread_events WHERE thread_id=?", threadID).Scan(&snap.ThreadEventSequence); err != nil {
		return snap, err
	}
	encoded, err := json.Marshal(snap)
	if err != nil {
		return snap, err
	}
	snap.SourceSHA256 = runmutation.Fingerprint("thread_context_source.v1", string(encoded))
	return snap, nil
}

// This boundary concerns model/history ownership. Background terminals alone do
// not block reading old context, unlike a destructive Thread lifecycle action.
func requireThreadContextIdleTx(ctx context.Context, tx *sql.Tx, runID string) error {
	if err := requireNoActiveRunControlLeaseTx(ctx, tx, runID, time.Now().UTC()); err != nil {
		return apperror.Wrap(apperror.CodeConflict, "thread_context_busy: wait for the current task to finish", err)
	}
	if err := requireQuiescentRunPauseTx(ctx, tx, runID); err != nil {
		return apperror.Wrap(apperror.CodeConflict, "thread_context_busy: wait for the current task to finish", err)
	}
	var pending int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM operator_steering_messages WHERE run_id=? AND status='pending'", runID).Scan(&pending); err != nil {
		return err
	}
	if pending != 0 {
		return apperror.New(apperror.CodeConflict, "thread_context_busy: queued input must settle first")
	}
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM run_events start WHERE start.run_id=? AND start.source IN ('model_gateway','specialist_model_gateway') AND start.type='model.started' AND NOT EXISTS(SELECT 1 FROM run_events terminal WHERE terminal.run_id=start.run_id AND terminal.source=start.source AND terminal.subject_id=start.subject_id AND terminal.type IN ('model.completed','model.failed'))", runID).Scan(&pending); err != nil {
		return err
	}
	if pending != 0 {
		return apperror.New(apperror.CodeConflict, "thread_context_busy: an unresolved model call requires reconciliation")
	}
	return nil
}

func (s *SQLiteStore) BeginThreadContextCompaction(ctx context.Context, request domain.ThreadContextCompactionRequest) (domain.ThreadContextCompactionOperation, domain.ThreadContextCompactionSnapshot, error) {
	var empty domain.ThreadContextCompactionSnapshot
	request, err := request.Normalize()
	if err != nil {
		return domain.ThreadContextCompactionOperation{}, empty, err
	}
	key := runmutation.Fingerprint("thread_context_operation.v1", request.OperationKey)
	fingerprint := runmutation.Fingerprint("thread_context_request.v1", request.ThreadID, request.ExpectedRunID, request.Focus, request.RequestedBy)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ThreadContextCompactionOperation{}, empty, err
	}
	defer tx.Rollback()
	o, err := scanThreadContextOperation(tx.QueryRowContext(ctx, threadContextOperationSelect+" WHERE thread_id=? AND key_digest=?", request.ThreadID, key))
	if err == nil {
		var prior string
		if err = tx.QueryRowContext(ctx, "SELECT request_fingerprint FROM thread_context_compactions WHERE id=?", o.ID).Scan(&prior); err != nil {
			return o, empty, err
		}
		if prior != fingerprint {
			return o, empty, apperror.New(apperror.CodeConflict, "Thread context operation key was used for different input")
		}
		o.Replayed = true
		switch o.Status {
		case "completed", "noop":
			o.Result.Replayed = true
			return o, empty, tx.Commit()
		case "failed":
			return o, empty, apperror.New(o.ErrorCode, o.ErrorMessage)
		case "generated":
			snap, e := readThreadContextSnapshot(ctx, tx, o.ThreadID, o.RunID)
			if e != nil {
				return o, empty, e
			}
			if snap.SourceSHA256 != o.SourceSHA256 {
				cause := apperror.New(apperror.CodeConflict, "thread_context_source_changed: generated source changed")
				if _, err = tx.ExecContext(ctx, "UPDATE thread_context_compactions SET status='failed',error_code=?,error_message=? WHERE id=?", apperror.CodeConflict, cause.Error(), o.ID); err != nil {
					return o, empty, err
				}
				if err = tx.Commit(); err != nil {
					return o, empty, err
				}
				return o, empty, cause
			}
			return o, snap, tx.Commit()
		default:
			return o, empty, apperror.New(apperror.CodeConflict, "thread_context_operation_pending: this operation may have an unresolved model charge; it will not be sent again")
		}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return o, empty, err
	}
	snap, err := readThreadContextSnapshot(ctx, tx, request.ThreadID, request.ExpectedRunID)
	if err != nil {
		return o, empty, err
	}
	if err = requireThreadContextIdleTx(ctx, tx, snap.Run.ID); err != nil {
		return o, empty, err
	}
	var active int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM thread_context_compactions WHERE thread_id=? AND status IN ('prepared','started','generated')", request.ThreadID).Scan(&active); err != nil {
		return o, empty, err
	}
	if active != 0 {
		return o, empty, apperror.New(apperror.CodeConflict, "thread_context_busy: another context operation is pending")
	}
	o = domain.ThreadContextCompactionOperation{OperationID: idgen.New("context-compact"), ThreadID: request.ThreadID, RunID: snap.Run.ID, SessionID: snap.Run.SessionID, SourceSHA256: snap.SourceSHA256, Status: "prepared", CreatedAt: time.Now().UTC()}
	inserted, err := tx.ExecContext(ctx, "INSERT INTO thread_context_compactions(operation_id,thread_id,run_id,session_id,key_digest,request_fingerprint,source_sha256,status,created_at) VALUES(?,?,?,?,?,?,?,'prepared',?)", o.OperationID, o.ThreadID, o.RunID, o.SessionID, key, fingerprint, o.SourceSHA256, ts(o.CreatedAt))
	if err != nil {
		return o, empty, err
	}
	o.ID, err = inserted.LastInsertId()
	if err != nil {
		return o, empty, err
	}
	return o, snap, tx.Commit()
}

func (s *SQLiteStore) PrepareThreadContextCompactionCall(ctx context.Context, id int64, provider, model string, tokens int, inputFingerprint string, inputMessagesOmitted int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	o, err := scanThreadContextOperation(tx.QueryRowContext(ctx, threadContextOperationSelect+" WHERE id=?", id))
	if err != nil {
		return err
	}
	snap, err := readThreadContextSnapshot(ctx, tx, o.ThreadID, o.RunID)
	if err != nil {
		return err
	}
	if o.Status != "prepared" || snap.SourceSHA256 != o.SourceSHA256 {
		return apperror.New(apperror.CodeConflict, "thread_context_source_changed: source changed before preparing summary")
	}
	if err = requireThreadContextIdleTx(ctx, tx, o.RunID); err != nil {
		return err
	}
	usage, err := getRunAgentUsageTx(ctx, tx, o.RunID)
	if err != nil {
		return err
	}
	if len(inputFingerprint) != 64 || inputMessagesOmitted < 0 || inputMessagesOmitted > len(snap.Messages) {
		return apperror.New(apperror.CodeInvalidArgument, "context summary input coverage is invalid")
	}
	if tokens <= 0 || (snap.Run.Budget.MaxTokens > 0 && int64(tokens) > snap.Run.Budget.MaxTokens-usage.TotalTokens) {
		return apperror.New(apperror.CodeResourceExhausted, "insufficient token budget for context summary")
	}
	_, err = tx.ExecContext(ctx, "UPDATE thread_context_compactions SET provider=?,model=?,reserved_tokens=?,input_fingerprint=?,input_messages_omitted=? WHERE id=? AND status='prepared'", provider, model, tokens, inputFingerprint, inputMessagesOmitted, id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) StartThreadContextCompactionCall(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	o, err := scanThreadContextOperation(tx.QueryRowContext(ctx, threadContextOperationSelect+" WHERE id=?", id))
	if err != nil {
		return err
	}
	snap, err := readThreadContextSnapshot(ctx, tx, o.ThreadID, o.RunID)
	if err != nil {
		return err
	}
	if o.Status != "prepared" || o.Provider == "" || o.Model == "" || snap.SourceSHA256 != o.SourceSHA256 {
		return apperror.New(apperror.CodeConflict, "thread_context_source_changed: source changed before dispatch")
	}
	if err = requireThreadContextIdleTx(ctx, tx, o.RunID); err != nil {
		return err
	}
	if snap.Run.Budget.MaxCostUSD > 0 {
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM run_monetary_reservations WHERE run_id=? AND scope=? AND attempt_number=? AND provider=? AND model=? AND status='reserved'", o.RunID, domain.MonetaryScopeContextCompaction, o.ID, o.Provider, o.Model).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return apperror.New(apperror.CodeFailedPrecondition, "context summary requires its own monetary reservation")
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE thread_context_compactions SET status='started',started_at=? WHERE id=? AND status='prepared'", ts(time.Now().UTC()), id); err != nil {
		return err
	}
	if err = appendSupervisorEventTx(ctx, tx, snap.Run, "context_compaction.model_started", "thread_context_compaction", o.OperationID, map[string]any{"operation_id": o.OperationID, "source_sha256": o.SourceSHA256, "input_fingerprint": o.InputFingerprint, "input_messages_omitted": o.InputMessagesOmitted, "provider": o.Provider, "model": o.Model, "monetary_attempt_number": o.ID}); err != nil {
		return err
	}
	return tx.Commit()
}

// Received accounting is persisted independently of the caller's cancellation
// and source CAS. A rejected summary can still be billable.
func (s *SQLiteStore) CompleteThreadContextCompactionCall(ctx context.Context, id int64, text string, usage *llm.Usage, elapsed time.Duration, callErr error) (domain.ThreadContextCompactionOperation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ThreadContextCompactionOperation{}, err
	}
	defer tx.Rollback()
	o, err := scanThreadContextOperation(tx.QueryRowContext(ctx, threadContextOperationSelect+" WHERE id=?", id))
	if err != nil {
		return o, err
	}
	if o.Status != "started" {
		return o, apperror.New(apperror.CodeConflict, "context summary call is not exclusively started")
	}
	if usage != nil {
		if err = usage.Validate(); err != nil {
			return o, err
		}
	}
	state, errorCode, errorMessage := "generated", apperror.Code(""), ""
	if callErr != nil || text == "" {
		state = "failed"
		if callErr == nil {
			callErr = errors.New("summary response was empty")
		}
		normalized := apperror.Normalize(callErr)
		errorCode, errorMessage = apperror.CodeOf(normalized), redact.String(normalized.Error())
	}
	raw, _ := json.Marshal(usage)
	run, err := getRunControlRunTx(ctx, tx, o.RunID)
	if err != nil {
		return o, err
	}
	eventType := "context_compaction.model_completed"
	if state == "failed" {
		eventType = "context_compaction.model_failed"
	}
	event, err := events.New(run.ID, run.MissionID, eventType, "thread_context_compaction", o.OperationID, map[string]any{"operation_id": o.OperationID, "source_sha256": o.SourceSHA256, "input_fingerprint": o.InputFingerprint, "input_messages_omitted": o.InputMessagesOmitted, "provider": o.Provider, "model": o.Model, "usage": usage, "usage_unknown": usage == nil, "elapsed_millis": max(0, elapsed.Milliseconds()), "dispatch": map[bool]string{true: "not_sent", false: "sent"}[errors.Is(callErr, llm.ErrPreparedRequestChanged)]})
	if err != nil {
		return o, err
	}
	recorded, err := insertRunEventTx(ctx, tx, event)
	if err != nil {
		return o, err
	}
	_, err = tx.ExecContext(ctx, "UPDATE thread_context_compactions SET status=?,response_text=?,usage_json=?,elapsed_millis=?,completion_sequence=?,terminal_at=?,error_code=?,error_message=?,reserved_tokens=CASE WHEN ? THEN 0 ELSE reserved_tokens END WHERE id=? AND status='started'", state, redact.String(text), string(raw), max(0, elapsed.Milliseconds()), recorded.Sequence, ts(time.Now().UTC()), errorCode, errorMessage, errors.Is(callErr, llm.ErrPreparedRequestChanged), id)
	if err != nil {
		return o, err
	}
	o, err = scanThreadContextOperation(tx.QueryRowContext(ctx, threadContextOperationSelect+" WHERE id=?", id))
	if err != nil {
		return o, err
	}
	return o, tx.Commit()
}

func (s *SQLiteStore) FailThreadContextCompaction(ctx context.Context, id int64, cause error) error {
	normalized := apperror.Normalize(cause)
	_, err := s.db.ExecContext(ctx, "UPDATE thread_context_compactions SET status='failed',error_code=?,error_message=? WHERE id=? AND status IN ('prepared','generated')", apperror.CodeOf(normalized), redact.String(normalized.Error()), id)
	return err
}

func (s *SQLiteStore) CommitThreadContextCompaction(ctx context.Context, id int64, result contextmgr.Result) (domain.ThreadContextCompactionResult, error) {
	var view domain.ThreadContextCompactionResult
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return view, err
	}
	defer tx.Rollback()
	o, err := scanThreadContextOperation(tx.QueryRowContext(ctx, threadContextOperationSelect+" WHERE id=?", id))
	if err != nil {
		return view, err
	}
	snap, err := readThreadContextSnapshot(ctx, tx, o.ThreadID, o.RunID)
	if err != nil {
		return view, err
	}
	if snap.SourceSHA256 != o.SourceSHA256 {
		return view, apperror.New(apperror.CodeConflict, "thread_context_source_changed: history or configuration changed while summarizing")
	}
	if err = requireThreadContextIdleTx(ctx, tx, o.RunID); err != nil {
		return view, err
	}
	state := "noop"
	if result.Compacted {
		if o.Status != "generated" || !result.Generated || result.RemovedMessages < 1 || result.RemovedMessages > len(snap.Messages) {
			return view, apperror.New(apperror.CodeConflict, "context summary has no completed operation receipt")
		}
		var envelope struct {
			Generated *contextmgr.GeneratedSummary `json:"generated"`
		}
		if err = json.Unmarshal([]byte(result.Summary.Content), &envelope); err != nil {
			return view, err
		}
		if envelope.Generated == nil {
			return view, errors.New("context summary is missing generated content")
		}
		receipt := envelope.Generated.Receipt
		if envelope.Generated.Text != o.ResponseText || envelope.Generated.InputFingerprint != o.InputFingerprint || envelope.Generated.InputMessagesOmitted != o.InputMessagesOmitted || receipt.RunID != o.RunID || receipt.AttemptID != o.OperationID || receipt.ModelAttempt != 1 || receipt.CompletionSequence != o.CompletionSequence || receipt.SourceSHA256 != o.SourceSHA256 || receipt.Provider != o.Provider || receipt.Model != o.Model {
			return view, apperror.New(apperror.CodeConflict, "context summary receipt differs from its durable operation")
		}
		if result.Summary.TaskID != o.SessionID || result.Summary.WorkspaceID != snap.Mission.WorkspaceID || result.Summary.PreviousSummaryID != snap.Summary.ID {
			return view, errors.New("context summary source identity changed")
		}
		result.Summary, err = saveContextSummaryTx(ctx, tx, result.Summary)
		if err != nil {
			return view, err
		}
		if err = markSupervisorContextMessagesCompacted(ctx, tx, o.SessionID, snap.Messages, result.RemovedMessages); err != nil {
			return view, err
		}
		state = "completed"
	} else if o.Status != "prepared" {
		return view, apperror.New(apperror.CodeConflict, "context operation cannot be completed as noop")
	}
	view = domain.ThreadContextCompactionResult{Version: domain.ThreadContextCompactionVersion, ThreadID: o.ThreadID, RunID: o.RunID, SessionID: o.SessionID, Status: state, SummaryID: result.Summary.ID, RemovedMessages: result.RemovedMessages, PreservedMessages: len(snap.Messages) - result.RemovedMessages, SourceSHA256: o.SourceSHA256, InputFingerprint: o.InputFingerprint, InputMessagesOmitted: o.InputMessagesOmitted, Generated: result.Generated, Usage: o.Usage}
	encoded, err := json.Marshal(view)
	if err != nil {
		return view, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE thread_context_compactions SET status=?,result_json=? WHERE id=?", state, string(encoded), id); err != nil {
		return view, err
	}
	if err = appendSupervisorEventTx(ctx, tx, snap.Run, "session.context_compacted", "thread_context_compaction", o.OperationID, view); err != nil {
		return view, err
	}
	return view, tx.Commit()
}

func contextCompactionMonetaryEvidenceTx(ctx context.Context, tx *sql.Tx, runID string, number int64, provider, model string) (monetaryModelEvidence, error) {
	var evidence monetaryModelEvidence
	o, err := scanThreadContextOperation(tx.QueryRowContext(ctx, threadContextOperationSelect+" WHERE run_id=? AND id=?", runID, number))
	if err != nil {
		return evidence, err
	}
	if o.Provider != provider || o.Model != model {
		return evidence, apperror.New(apperror.CodeConflict, "context compaction monetary model identity changed")
	}
	evidence.Sent = o.StartedAt != nil
	if o.CompletionSequence > 0 {
		var dispatch string
		if err = tx.QueryRowContext(ctx, "SELECT COALESCE(json_extract(payload_json,'$.dispatch'),'sent') FROM run_events WHERE run_id=? AND sequence=? AND source='thread_context_compaction' AND subject_id=?", o.RunID, o.CompletionSequence, o.OperationID).Scan(&dispatch); err != nil {
			return evidence, err
		}
		if dispatch == "not_sent" {
			evidence.Sent = false
			evidence.NotSent = true
		}
		evidence.EventType = events.ModelCompletedEvent
		if o.Status == "failed" {
			evidence.EventType = events.ModelFailedEvent
		}
		encoded, _ := json.Marshal(map[string]any{"usage": o.Usage, "usage_unknown": len(o.Usage) == 0 || string(o.Usage) == "null", "tool_call_count": 0})
		evidence.PayloadJSON = string(encoded)
	}
	return evidence, nil
}

// Terminal Runs may spend only through a real, not-yet-dispatched context
// operation. This does not reopen Run execution or restore any lease.
func requireContextCompactionReservationTx(ctx context.Context, tx *sql.Tx, r domain.MonetaryReserveRequest) error {
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM thread_context_compactions WHERE id=? AND run_id=? AND provider=? AND model=? AND status='prepared' AND started_at IS NULL", r.AttemptNumber, r.RunID, r.Provider, r.Model).Scan(&exists); err != nil {
		return err
	}
	if exists != 1 {
		return apperror.New(apperror.CodeFailedPrecondition, "context cost reservation requires an unsent context operation")
	}
	return nil
}
