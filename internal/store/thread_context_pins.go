package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
)

func (s *SQLiteStore) InspectThreadContextPins(ctx context.Context, threadID string) (domain.ThreadContextPins, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.ThreadContextPins{}, err
	}
	defer tx.Rollback()
	thread, err := scanThread(tx.QueryRowContext(ctx, threadSelect+` WHERE id=?`, threadID))
	if err != nil {
		return domain.ThreadContextPins{}, err
	}
	state, err := readThreadContextPinsTx(ctx, tx, thread.ID)
	if err != nil {
		return state, err
	}
	if err := threadContextPinsIdleTx(ctx, tx, thread); err != nil {
		switch apperror.CodeOf(err) {
		case apperror.CodeConflict, apperror.CodeFailedPrecondition:
			state.Busy = true
		default:
			return state, err
		}
	}
	return state, tx.Commit()
}

func (s *SQLiteStore) ThreadContextPinsForRun(ctx context.Context, runID string) (domain.ThreadContextPins, error) {
	thread, err := s.GetThreadByRun(ctx, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ThreadContextPins{Version: domain.ThreadContextPinsVersion, Pins: []domain.ThreadContextPin{}, Proposals: []domain.ContextPinProposal{}}, nil
	}
	if err != nil {
		return domain.ThreadContextPins{}, err
	}
	return s.InspectThreadContextPins(ctx, thread.ID)
}

func readThreadContextPinsTx(ctx context.Context, tx *sql.Tx, threadID string) (domain.ThreadContextPins, error) {
	state := domain.ThreadContextPins{Version: domain.ThreadContextPinsVersion, ThreadID: threadID, Pins: []domain.ThreadContextPin{}, Proposals: []domain.ContextPinProposal{}, EffectiveFrom: "next_request"}
	err := tx.QueryRowContext(ctx, `SELECT revision FROM thread_context_pin_sets WHERE thread_id=?`, threadID).Scan(&state.Revision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return state, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT payload_json FROM thread_context_pins WHERE thread_id=? AND status='active' ORDER BY id`, threadID)
	if err != nil {
		return state, err
	}
	for rows.Next() {
		var raw string
		var pin domain.ThreadContextPin
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return state, err
		}
		if err := json.Unmarshal([]byte(raw), &pin); err != nil {
			rows.Close()
			return state, err
		}
		if pin.ThreadID != threadID || pin.Status != "active" || pin.Version < 1 || pin.ContentSHA256 != session.ContentSHA256(pin.Text) {
			rows.Close()
			return state, apperror.New(apperror.CodeFailedPrecondition, "stored context pin integrity is invalid")
		}
		state.Pins = append(state.Pins, pin)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return state, err
	}
	rows.Close()
	rows, err = tx.QueryContext(ctx, `SELECT payload_json FROM thread_context_pin_proposals WHERE thread_id=? ORDER BY CASE status WHEN 'pending' THEN 0 ELSE 1 END,created_at DESC,id DESC LIMIT 64`, threadID)
	if err != nil {
		return state, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var proposal domain.ContextPinProposal
		if err := rows.Scan(&raw); err != nil {
			return state, err
		}
		if err := json.Unmarshal([]byte(raw), &proposal); err != nil {
			return state, err
		}
		state.Proposals = append(state.Proposals, proposal)
	}
	return state, rows.Err()
}

// Pin mutations and execution admission both cross SQLite's write lock. An
// active execution lease prevents a new rule from changing an in-flight turn.
func threadContextPinsIdleTx(ctx context.Context, tx *sql.Tx, thread domain.Thread) error {
	if thread.Status != domain.ThreadActive {
		return apperror.New(apperror.CodeFailedPrecondition, "context pins require an active Thread")
	}
	runID := thread.ActiveRunID
	if runID == "" {
		runID = thread.LastRunID
	}
	if runID == "" {
		return nil
	}
	if err := requireNoActiveRunControlLeaseTx(ctx, tx, runID, time.Now().UTC()); err != nil {
		return err
	}
	if err := requireQuiescentRunPauseTx(ctx, tx, runID); err != nil {
		return err
	}
	if err := requireThreadToolEffectsSettledTx(ctx, tx, runID); err != nil {
		return err
	}
	if err := requireApprovalEffectsSettledTx(ctx, tx, runID); err != nil {
		return err
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM operator_steering_messages WHERE run_id=? AND status='pending')+(SELECT COUNT(*) FROM command_runtime_jobs WHERE run_id=? AND state IN ('prepared','running','stopping'))`, runID, runID).Scan(&pending); err != nil {
		return err
	}
	if pending != 0 {
		return apperror.New(apperror.CodeConflict, "context pins require all earlier user inputs and tool work to settle")
	}
	return nil
}

func beginThreadContextPinMutation(ctx context.Context, s *SQLiteStore, threadID string) (*sql.Tx, domain.Thread, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, domain.Thread{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE threads SET version=version WHERE id=?`, threadID); err != nil {
		tx.Rollback()
		return nil, domain.Thread{}, err
	}
	thread, err := scanThread(tx.QueryRowContext(ctx, threadSelect+` WHERE id=?`, threadID))
	if err != nil {
		tx.Rollback()
		return nil, thread, err
	}
	return tx, thread, nil
}

func contextPinTextForStorage(text string) error {
	if err := domain.ValidateContextPinText(text); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument, err.Error(), err)
	}
	if text != redact.String(text) {
		return apperror.New(apperror.CodeInvalidArgument, "remove protected secrets before fixing this text as a context requirement")
	}
	return nil
}

func requireContextPinSourceTx(ctx context.Context, tx *sql.Tx, threadID string, messageID int64, text string) error {
	if messageID == 0 {
		return nil
	}
	var content, kind, digest, role string
	var authorized bool
	err := tx.QueryRowContext(ctx, `SELECT message.content,message.role,message.source_kind,message.content_sha256,message.instruction_authorized FROM session_messages message JOIN thread_runs lineage ON lineage.session_id=message.session_id WHERE lineage.thread_id=? AND message.id=?`, threadID, messageID).Scan(&content, &role, &kind, &digest, &authorized)
	if errors.Is(err, sql.ErrNoRows) {
		return apperror.New(apperror.CodeFailedPrecondition, "context pin source is outside this Thread")
	}
	if err != nil {
		return err
	}
	if role != "user" || kind != session.SourceOperatorMessage || !authorized || digest != session.ContentSHA256(content) || !strings.Contains(content, text) {
		return apperror.New(apperror.CodeFailedPrecondition, "context pin source must be an exact span of a verified user message")
	}
	return nil
}

func bumpThreadContextPinRevisionTx(ctx context.Context, tx *sql.Tx, threadID string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO thread_context_pin_sets(thread_id,revision) VALUES(?,1) ON CONFLICT(thread_id) DO UPDATE SET revision=revision+1`, threadID)
	return err
}

func saveThreadContextPinTx(ctx context.Context, tx *sql.Tx, pin domain.ThreadContextPin) error {
	encoded, err := json.Marshal(pin)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO thread_context_pins(id,thread_id,version,status,payload_json) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET version=excluded.version,status=excluded.status,payload_json=excluded.payload_json`, pin.ID, pin.ThreadID, pin.Version, pin.Status, string(encoded))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO thread_context_pin_revisions(pin_id,version,payload_json) VALUES(?,?,?)`, pin.ID, pin.Version, string(encoded))
	return err
}

func validateThreadContextPinCapacity(pins []domain.ThreadContextPin) error {
	runes := 0
	for _, pin := range pins {
		if pin.Status == "active" {
			runes += utf8.RuneCountInString(pin.Text)
		}
	}
	if len(pins) > domain.MaxThreadContextPins || runes > domain.MaxThreadContextPinTotalRunes {
		return apperror.New(apperror.CodeResourceExhausted, "fixed task requirements exceed the pin budget; shorten or remove a requirement before adding more")
	}
	return nil
}

func contextPinOperationReplayTx(ctx context.Context, tx interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, threadID, key, fingerprint string) (domain.ThreadContextPinResult, bool, error) {
	var stored, raw string
	err := tx.QueryRowContext(ctx, `SELECT request_fingerprint,result_json FROM thread_context_pin_operations WHERE thread_id=? AND operation_key_digest=?`, threadID, key).Scan(&stored, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ThreadContextPinResult{}, false, nil
	}
	if err != nil {
		return domain.ThreadContextPinResult{}, false, err
	}
	if stored != fingerprint {
		return domain.ThreadContextPinResult{}, false, apperror.New(apperror.CodeConflict, "context pin operation key was used for different intent")
	}
	var result domain.ThreadContextPinResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return result, false, err
	}
	result.Replayed = true
	return result, true, nil
}

func (s *SQLiteStore) InspectThreadContextPinControlOperation(ctx context.Context, request domain.ThreadContextPinControl) (domain.ThreadContextPinResult, bool, error) {
	if err := domain.ValidateThreadContextPinOperator(request.Version, request.ThreadID, request.OperationKey, request.RequestedBy, request.ExpectedRevision); err != nil {
		return domain.ThreadContextPinResult{}, false, apperror.Wrap(apperror.CodeInvalidArgument, err.Error(), err)
	}
	return contextPinOperationReplayTx(ctx, s.db, request.ThreadID, runmutation.Fingerprint("thread-context-pin-control", request.ThreadID, request.OperationKey), runmutation.Fingerprint(domain.ThreadContextPinsVersion, domain.ContextPinRequestJSON(request), request.RequestedBy))
}

func (s *SQLiteStore) InspectThreadContextPinResolveOperation(ctx context.Context, request domain.ResolveContextPinProposal) (domain.ThreadContextPinResult, bool, error) {
	if err := domain.ValidateThreadContextPinOperator(request.Version, request.ThreadID, request.OperationKey, request.RequestedBy, request.ExpectedRevision); err != nil {
		return domain.ThreadContextPinResult{}, false, apperror.Wrap(apperror.CodeInvalidArgument, err.Error(), err)
	}
	return contextPinOperationReplayTx(ctx, s.db, request.ThreadID, runmutation.Fingerprint("thread-context-pin-resolve", request.ThreadID, request.OperationKey), runmutation.Fingerprint(domain.ThreadContextPinsVersion, request.ProposalID, domain.ContextPinRequestJSON(request), request.RequestedBy))
}

func finishContextPinOperationTx(ctx context.Context, tx *sql.Tx, threadID, key, fingerprint, reply string) (domain.ThreadContextPinResult, error) {
	if err := bumpThreadContextPinRevisionTx(ctx, tx, threadID); err != nil {
		return domain.ThreadContextPinResult{}, err
	}
	state, err := readThreadContextPinsTx(ctx, tx, threadID)
	if err != nil {
		return domain.ThreadContextPinResult{}, err
	}
	result := domain.ThreadContextPinResult{State: state, ReplyText: reply}
	if reply != "" {
		result.ReplyOperationKey = "context-pin-reply-" + key[:32]
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO thread_context_pin_operations(thread_id,operation_key_digest,request_fingerprint,result_json) VALUES(?,?,?,?)`, threadID, key, fingerprint, string(encoded))
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func (s *SQLiteStore) ControlThreadContextPin(ctx context.Context, request domain.ThreadContextPinControl) (domain.ThreadContextPinResult, error) {
	if err := domain.ValidateThreadContextPinOperator(request.Version, request.ThreadID, request.OperationKey, request.RequestedBy, request.ExpectedRevision); err != nil {
		return domain.ThreadContextPinResult{}, apperror.Wrap(apperror.CodeInvalidArgument, err.Error(), err)
	}
	if request.Action != "add" && request.Action != "edit" && request.Action != "remove" {
		return domain.ThreadContextPinResult{}, apperror.New(apperror.CodeInvalidArgument, "context pin action must be add, edit or remove")
	}
	if request.Action != "remove" {
		if err := contextPinTextForStorage(request.Text); err != nil {
			return domain.ThreadContextPinResult{}, err
		}
	}
	key := runmutation.Fingerprint("thread-context-pin-control", request.ThreadID, request.OperationKey)
	fingerprint := runmutation.Fingerprint(domain.ThreadContextPinsVersion, domain.ContextPinRequestJSON(request), request.RequestedBy)
	tx, thread, err := beginThreadContextPinMutation(ctx, s, request.ThreadID)
	if err != nil {
		return domain.ThreadContextPinResult{}, err
	}
	defer tx.Rollback()
	if replay, found, err := contextPinOperationReplayTx(ctx, tx, thread.ID, key, fingerprint); err != nil || found {
		return replay, err
	}
	if err := threadContextPinsIdleTx(ctx, tx, thread); err != nil {
		return domain.ThreadContextPinResult{}, apperror.Wrap(apperror.CodeConflict, "context pins are busy; wait for current execution and pending tools to finish", err)
	}
	state, err := readThreadContextPinsTx(ctx, tx, thread.ID)
	if err != nil {
		return domain.ThreadContextPinResult{}, err
	}
	if state.Revision != request.ExpectedRevision {
		return domain.ThreadContextPinResult{}, apperror.New(apperror.CodeConflict, "context pins changed; reload before editing")
	}
	now := time.Now().UTC()
	pin := domain.ThreadContextPin{}
	if request.Action == "add" {
		if request.PinID != "" || request.ExpectedPinVersion != 0 {
			return domain.ThreadContextPinResult{}, apperror.New(apperror.CodeInvalidArgument, "new context pin must not select an existing pin")
		}
		pin = domain.ThreadContextPin{ID: idgen.New("context-pin"), ThreadID: thread.ID, Version: 1, Status: "active", CreatedBy: request.RequestedBy, CreatedAt: now}
	} else {
		for _, candidate := range state.Pins {
			if candidate.ID == request.PinID {
				pin = candidate
				break
			}
		}
		if pin.ID == "" || pin.Version != request.ExpectedPinVersion {
			return domain.ThreadContextPinResult{}, apperror.New(apperror.CodeConflict, "context pin was edited or removed; reload before changing it")
		}
		pin.Version++
	}
	pin.UpdatedAt = now
	if request.Action == "remove" {
		pin.Status = "removed"
	} else {
		if err := requireContextPinSourceTx(ctx, tx, thread.ID, request.SourceMessageID, request.Text); err != nil {
			return domain.ThreadContextPinResult{}, err
		}
		pin.Text = request.Text
		pin.ContentSHA256 = session.ContentSHA256(pin.Text)
		pin.SourceMessageID = request.SourceMessageID
		pin.SourceKind = "operator_input"
		pin.ProposalID = ""
		if pin.SourceMessageID != 0 {
			pin.SourceKind = "operator_message_span"
		}
	}
	active := []domain.ThreadContextPin{}
	for _, old := range state.Pins {
		if old.ID != pin.ID {
			active = append(active, old)
		}
	}
	if pin.Status == "active" {
		active = append(active, pin)
	}
	if err := validateThreadContextPinCapacity(active); err != nil {
		return domain.ThreadContextPinResult{}, err
	}
	if err := saveThreadContextPinTx(ctx, tx, pin); err != nil {
		return domain.ThreadContextPinResult{}, err
	}
	return finishContextPinOperationTx(ctx, tx, thread.ID, key, fingerprint, "")
}

func (s *SQLiteStore) ResolveThreadContextPinProposal(ctx context.Context, request domain.ResolveContextPinProposal) (domain.ThreadContextPinResult, error) {
	if err := domain.ValidateThreadContextPinOperator(request.Version, request.ThreadID, request.OperationKey, request.RequestedBy, request.ExpectedRevision); err != nil {
		return domain.ThreadContextPinResult{}, apperror.Wrap(apperror.CodeInvalidArgument, err.Error(), err)
	}
	if request.Choice != "pin_task" && request.Choice != "discussion_only" && request.Choice != "custom" && request.Choice != "dismiss" {
		return domain.ThreadContextPinResult{}, apperror.New(apperror.CodeInvalidArgument, "context pin choice is invalid")
	}
	if request.Choice != "custom" && (request.Text != "" || request.PinCustom) {
		return domain.ThreadContextPinResult{}, apperror.New(apperror.CodeInvalidArgument, "custom text and pin_custom require the custom choice")
	}
	if request.Choice == "custom" {
		if err := contextPinTextForStorage(request.Text); err != nil {
			return domain.ThreadContextPinResult{}, err
		}
	}
	key := runmutation.Fingerprint("thread-context-pin-resolve", request.ThreadID, request.OperationKey)
	fingerprint := runmutation.Fingerprint(domain.ThreadContextPinsVersion, request.ProposalID, domain.ContextPinRequestJSON(request), request.RequestedBy)
	tx, thread, err := beginThreadContextPinMutation(ctx, s, request.ThreadID)
	if err != nil {
		return domain.ThreadContextPinResult{}, err
	}
	defer tx.Rollback()
	if replay, found, err := contextPinOperationReplayTx(ctx, tx, thread.ID, key, fingerprint); err != nil || found {
		return replay, err
	}
	if err := threadContextPinsIdleTx(ctx, tx, thread); err != nil {
		return domain.ThreadContextPinResult{}, apperror.Wrap(apperror.CodeConflict, "context pins are busy; wait for the current execution to finish", err)
	}
	state, err := readThreadContextPinsTx(ctx, tx, thread.ID)
	if err != nil {
		return domain.ThreadContextPinResult{}, err
	}
	if state.Revision != request.ExpectedRevision {
		return domain.ThreadContextPinResult{}, apperror.New(apperror.CodeConflict, "context pin proposal changed; reload before confirming")
	}
	var proposal domain.ContextPinProposal
	for _, candidate := range state.Proposals {
		if candidate.ID == request.ProposalID {
			proposal = candidate
			break
		}
	}
	if proposal.ID == "" || proposal.Status != "pending" || proposal.Version != request.ExpectedProposalVersion {
		return domain.ThreadContextPinResult{}, apperror.New(apperror.CodeConflict, "context pin proposal is no longer pending at that version")
	}
	if err := requireContextPinSourceTx(ctx, tx, thread.ID, proposal.SourceMessageID, proposal.Text); err != nil {
		return domain.ThreadContextPinResult{}, err
	}
	if proposal.ContentSHA256 != session.ContentSHA256(proposal.Text) {
		return domain.ThreadContextPinResult{}, apperror.New(apperror.CodeFailedPrecondition, "context pin proposal digest is invalid")
	}
	now := time.Now().UTC()
	reply := ""
	text := ""
	sourceID := int64(0)
	kind := "operator_input"
	switch request.Choice {
	case "pin_task":
		text = proposal.Text
		sourceID = proposal.SourceMessageID
		kind = "confirmed_model_proposal"
		reply = "固定到本任务：\n" + text
	case "discussion_only":
		reply = "仅用于当前讨论，不固定：\n" + proposal.Text
	case "custom":
		reply = request.Text
		if request.PinCustom {
			text = request.Text
		}
	}
	if text != "" {
		if err := contextPinTextForStorage(text); err != nil {
			return domain.ThreadContextPinResult{}, err
		}
		pin := domain.ThreadContextPin{ID: idgen.New("context-pin"), ThreadID: thread.ID, Text: text, Version: 1, Status: "active", ContentSHA256: session.ContentSHA256(text), SourceKind: kind, SourceMessageID: sourceID, ProposalID: proposal.ID, CreatedBy: request.RequestedBy, CreatedAt: now, UpdatedAt: now}
		if err := validateThreadContextPinCapacity(append(state.Pins, pin)); err != nil {
			return domain.ThreadContextPinResult{}, err
		}
		if err := saveThreadContextPinTx(ctx, tx, pin); err != nil {
			return domain.ThreadContextPinResult{}, err
		}
		proposal.PinID = pin.ID
	}
	proposal.Status = "resolved"
	if request.Choice == "dismiss" {
		proposal.Status = "dismissed"
	}
	proposal.Version++
	proposal.Choice = request.Choice
	proposal.ResolvedAt = &now
	proposal.ReplyText = reply
	if reply != "" {
		proposal.ReplyOperationKey = "context-pin-reply-" + key[:32]
	}
	encoded, err := json.Marshal(proposal)
	if err != nil {
		return domain.ThreadContextPinResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE thread_context_pin_proposals SET version=?,status=?,payload_json=? WHERE id=? AND thread_id=? AND status='pending'`, proposal.Version, proposal.Status, string(encoded), proposal.ID, thread.ID); err != nil {
		return domain.ThreadContextPinResult{}, err
	}
	return finishContextPinOperationTx(ctx, tx, thread.ID, key, fingerprint, reply)
}

func (s *SQLiteStore) CreateThreadContextPinProposal(ctx context.Context, scope domain.ContextPinProposalScope, spec domain.ContextPinProposalSpec) (domain.ContextPinProposal, bool, error) {
	if err := spec.Validate(); err != nil {
		return domain.ContextPinProposal{}, false, apperror.Wrap(apperror.CodeInvalidArgument, err.Error(), err)
	}
	if err := contextPinTextForStorage(spec.Text); err != nil {
		return domain.ContextPinProposal{}, false, err
	}
	if scope.RequestedBy != "run_supervisor" || scope.RunID == "" || scope.RootAgentID == "" || scope.SessionID == "" || scope.OperationKey == "" || scope.InvocationID == "" || scope.LeaseID == "" || scope.LeaseGeneration <= 0 {
		return domain.ContextPinProposal{}, false, apperror.New(apperror.CodePolicyDenied, "context pin proposals require the current fenced root agent")
	}
	thread, err := s.GetThreadByRun(ctx, scope.RunID)
	if err != nil {
		return domain.ContextPinProposal{}, false, err
	}
	tx, current, err := beginThreadContextPinMutation(ctx, s, thread.ID)
	if err != nil {
		return domain.ContextPinProposal{}, false, err
	}
	defer tx.Rollback()
	if current.ActiveRunID != scope.RunID || current.Status != domain.ThreadActive {
		return domain.ContextPinProposal{}, false, apperror.New(apperror.CodeConflict, "context pin proposal does not belong to the current Thread execution")
	}
	if err := requireRunExecutionLeaseTx(ctx, tx, scope.RunID, scope.LeaseID, scope.LeaseGeneration); err != nil {
		return domain.ContextPinProposal{}, false, err
	}
	var binding int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs run JOIN agent_nodes root ON root.run_id=run.id AND root.id=? JOIN run_tool_calls invocation ON invocation.run_id=run.id AND invocation.id=? WHERE run.id=? AND run.session_id=? AND root.parent_id IS NULL AND root.active_attempt_id<>'' AND invocation.session_id=run.session_id AND invocation.workspace_id=? AND invocation.tool_name='context_pin_propose' AND invocation.action_class='agent_proposal'`, scope.RootAgentID, scope.InvocationID, scope.RunID, scope.SessionID, scope.WorkspaceID).Scan(&binding); err != nil {
		return domain.ContextPinProposal{}, false, err
	}
	if binding != 1 {
		return domain.ContextPinProposal{}, false, apperror.New(apperror.CodeFailedPrecondition, "context pin proposal is not backed by a root tool invocation")
	}
	key := runmutation.Fingerprint("context_pin_propose", scope.RunID, scope.OperationKey)
	fingerprint := runmutation.Fingerprint(domain.ContextPinProposalVersion, domain.ContextPinRequestJSON(spec), scope.RootAgentID, scope.SessionID)
	var previous, raw string
	err = tx.QueryRowContext(ctx, `SELECT request_fingerprint,payload_json FROM thread_context_pin_proposals WHERE operation_key_digest=?`, key).Scan(&previous, &raw)
	if err == nil {
		if previous != fingerprint {
			return domain.ContextPinProposal{}, false, apperror.New(apperror.CodeConflict, "context pin proposal operation was reused for different text")
		}
		var existing domain.ContextPinProposal
		if err := json.Unmarshal([]byte(raw), &existing); err != nil {
			return existing, false, err
		}
		return existing, true, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.ContextPinProposal{}, false, err
	}
	if err := requireContextPinSourceTx(ctx, tx, thread.ID, spec.SourceMessageID, spec.Text); err != nil {
		return domain.ContextPinProposal{}, false, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM thread_context_pin_proposals WHERE thread_id=? AND status='pending'`, thread.ID).Scan(&count); err != nil {
		return domain.ContextPinProposal{}, false, err
	}
	if count >= domain.MaxPendingContextPinProposals {
		return domain.ContextPinProposal{}, false, apperror.New(apperror.CodeResourceExhausted, "too many context pin proposals await user confirmation")
	}
	now := time.Now().UTC()
	proposal := domain.ContextPinProposal{ID: idgen.New("pin-proposal"), ThreadID: thread.ID, RunID: scope.RunID, Version: 1, Status: "pending", Text: spec.Text, Question: spec.Question, SourceMessageID: spec.SourceMessageID, ContentSHA256: session.ContentSHA256(spec.Text), CreatedAt: now}
	encoded, err := json.Marshal(proposal)
	if err != nil {
		return proposal, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO thread_context_pin_proposals(id,thread_id,run_id,version,status,operation_key_digest,request_fingerprint,invocation_id,payload_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, proposal.ID, proposal.ThreadID, proposal.RunID, proposal.Version, proposal.Status, key, fingerprint, scope.InvocationID, string(encoded), ts(now))
	if err != nil {
		return proposal, false, err
	}
	if err := bumpThreadContextPinRevisionTx(ctx, tx, thread.ID); err != nil {
		return proposal, false, err
	}
	return proposal, false, tx.Commit()
}
