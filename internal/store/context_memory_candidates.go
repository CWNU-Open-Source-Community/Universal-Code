package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/standardcodedelivery"
)

const memoryCandidateSelect = `SELECT payload_json FROM context_memory_candidates`

// The queue is derived on operator inspection rather than in Run finalization:
// Thread replies and Deliver receipts can finish while their Run stays active.
// This transaction scans at most 200 unseen Notes, retains at most 50 pending
// entries per Workspace, and never modifies formal memories or execution state.
func (s *SQLiteStore) ListContextMemoryCandidates(ctx context.Context, filter contextmgr.MemoryCandidateFilter, at time.Time) ([]contextmgr.MemoryCandidate, error) {
	if err := filter.Validate(); err != nil {
		return nil, apperror.Wrap(apperror.CodeInvalidArgument, err.Error(), err)
	}
	at = memoryCandidateTime(at)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireMemoryCandidateWorkspaceTx(ctx, tx, filter.WorkspaceID); err != nil {
		return nil, err
	}
	pending, err := listMemoryCandidatesTx(ctx, tx, filter.WorkspaceID, false, contextmgr.MaxMemoryCandidates)
	if err != nil {
		return nil, err
	}
	for _, value := range pending {
		if _, err := refreshMemoryCandidateTx(ctx, tx, value, at); err != nil {
			return nil, err
		}
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM context_memory_candidates WHERE workspace_id=? AND status='pending'`, filter.WorkspaceID).Scan(&count); err != nil {
		return nil, err
	}
	if count < contextmgr.MaxMemoryCandidates {
		rows, err := tx.QueryContext(ctx, `SELECT note.id FROM notes note
			JOIN runs run ON run.id=note.run_id JOIN missions mission ON mission.id=run.mission_id
			WHERE mission.workspace_id=? AND run.status IN ('completed','running','paused')
			AND note.status='active' AND note.category IN ('decision','summary')
			AND note.visibility IN ('run','root')
			AND length(CAST(note.title AS BLOB)) BETWEEN 1 AND 256
			AND length(CAST(note.content AS BLOB)) BETWEEN 1 AND 16384
			AND (EXISTS(SELECT 1 FROM note_sources source WHERE source.note_id=note.id AND source.run_id=note.run_id)
			 OR EXISTS(SELECT 1 FROM note_evidence evidence WHERE evidence.note_id=note.id AND evidence.run_id=note.run_id))
			AND NOT EXISTS(SELECT 1 FROM context_memory_candidates candidate WHERE candidate.workspace_id=? AND candidate.note_id=note.id)
			ORDER BY note.updated_at DESC,note.id LIMIT ?`, filter.WorkspaceID, filter.WorkspaceID, contextmgr.MaxMemoryCandidateSourceScan)
		if err != nil {
			return nil, err
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return nil, err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		for _, id := range ids {
			if count >= contextmgr.MaxMemoryCandidates {
				break
			}
			value, err := prepareMemoryCandidateTx(ctx, tx, filter.WorkspaceID, id, nil, at)
			if apperror.CodeOf(err) == apperror.CodeFailedPrecondition {
				continue
			}
			if err != nil {
				return nil, err
			}
			payload, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO context_memory_candidates
				(id,workspace_id,note_id,status,fingerprint,version,payload_json,created_at,updated_at)
				VALUES(?,?,?,?,?,?,?,?,?)`, value.ID, value.WorkspaceID, value.Source.NoteID, value.Status, value.Fingerprint, value.Version, string(payload), ts(value.CreatedAt), ts(value.UpdatedAt)); err != nil {
				return nil, err
			}
			count++
		}
	}
	limit := filter.Limit
	if limit == 0 {
		limit = contextmgr.MaxMemoryCandidates
	}
	items, err := listMemoryCandidatesTx(ctx, tx, filter.WorkspaceID, filter.IncludeResolved, limit)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *SQLiteStore) GetContextMemoryCandidate(ctx context.Context, id, workspaceID string, at time.Time) (contextmgr.MemoryCandidate, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return contextmgr.MemoryCandidate{}, err
	}
	defer func() { _ = tx.Rollback() }()
	value, err := getMemoryCandidateTx(ctx, tx, id, workspaceID)
	if err != nil {
		return contextmgr.MemoryCandidate{}, err
	}
	value, err = refreshMemoryCandidateTx(ctx, tx, value, memoryCandidateTime(at))
	if err != nil {
		return contextmgr.MemoryCandidate{}, err
	}
	if err := tx.Commit(); err != nil {
		return contextmgr.MemoryCandidate{}, err
	}
	return value, nil
}

func (s *SQLiteStore) ReviewContextMemoryCandidate(ctx context.Context, id string, request contextmgr.ReviewMemoryCandidateRequest, status contextmgr.MemoryCandidateStatus, at time.Time) (contextmgr.MemoryCandidateReviewResult, error) {
	if err := request.Validate(); err != nil {
		return contextmgr.MemoryCandidateReviewResult{}, apperror.Wrap(apperror.CodeInvalidArgument, err.Error(), err)
	}
	if status != contextmgr.MemoryCandidateConfirmed && status != contextmgr.MemoryCandidateRejected {
		return contextmgr.MemoryCandidateReviewResult{}, apperror.New(apperror.CodeInvalidArgument, "memory candidate review action is invalid")
	}
	if status == contextmgr.MemoryCandidateConfirmed && !request.Confirm {
		return contextmgr.MemoryCandidateReviewResult{}, apperror.New(apperror.CodeInvalidArgument, "memory candidate confirmation must be explicit")
	}
	at = memoryCandidateTime(at)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return contextmgr.MemoryCandidateReviewResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireMemoryCandidateWorkspaceTx(ctx, tx, request.WorkspaceID); err != nil {
		return contextmgr.MemoryCandidateReviewResult{}, err
	}
	value, err := getMemoryCandidateTx(ctx, tx, id, request.WorkspaceID)
	if err != nil {
		return contextmgr.MemoryCandidateReviewResult{}, err
	}
	if value.Fingerprint != request.ExpectedFingerprint || value.Source.NoteVersion != request.ExpectedSourceVersion {
		return contextmgr.MemoryCandidateReviewResult{}, apperror.New(apperror.CodeConflict, "memory candidate reviewed content or source version changed")
	}
	if value.Status == status && value.ReviewedBy == strings.TrimSpace(request.RequestedBy) && value.Version == request.ExpectedVersion+1 {
		if err := tx.Commit(); err != nil {
			return contextmgr.MemoryCandidateReviewResult{}, err
		}
		return contextmgr.MemoryCandidateReviewResult{Candidate: value, MemoryID: value.MemoryID, Replayed: true}, nil
	}
	if value.Status != contextmgr.MemoryCandidatePending || value.Version != request.ExpectedVersion {
		return contextmgr.MemoryCandidateReviewResult{}, apperror.New(apperror.CodeConflict, "memory candidate is already resolved or changed concurrently")
	}
	if status == contextmgr.MemoryCandidateConfirmed {
		refreshed, err := refreshMemoryCandidateTx(ctx, tx, value, at)
		if err != nil {
			return contextmgr.MemoryCandidateReviewResult{}, err
		}
		if refreshed.Status != contextmgr.MemoryCandidatePending {
			if err := tx.Commit(); err != nil {
				return contextmgr.MemoryCandidateReviewResult{}, err
			}
			return contextmgr.MemoryCandidateReviewResult{}, apperror.New(apperror.CodeFailedPrecondition, "memory candidate source changed or is no longer eligible; review a current source")
		}
		memory, err := value.Memory(strings.TrimSpace(request.RequestedBy), at)
		if err != nil {
			return contextmgr.MemoryCandidateReviewResult{}, err
		}
		if err := insertConfirmedCandidateMemoryTx(ctx, tx, memory); err != nil {
			return contextmgr.MemoryCandidateReviewResult{}, err
		}
		value = value.Resolve(status, strings.TrimSpace(request.RequestedBy), memory.ID, at)
	} else {
		value = value.Resolve(status, strings.TrimSpace(request.RequestedBy), "", at)
	}
	if err := resolveMemoryCandidateTx(ctx, tx, value, request.ExpectedVersion); err != nil {
		return contextmgr.MemoryCandidateReviewResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return contextmgr.MemoryCandidateReviewResult{}, err
	}
	return contextmgr.MemoryCandidateReviewResult{Candidate: value, MemoryID: value.MemoryID}, nil
}

func getMemoryCandidateTx(ctx context.Context, tx *sql.Tx, id, workspaceID string) (contextmgr.MemoryCandidate, error) {
	return scanMemoryCandidate(tx.QueryRowContext(ctx, memoryCandidateSelect+` WHERE id=? AND workspace_id=?`, strings.TrimSpace(id), workspaceID))
}

func listMemoryCandidatesTx(ctx context.Context, tx *sql.Tx, workspaceID string, includeResolved bool, limit int) ([]contextmgr.MemoryCandidate, error) {
	query := memoryCandidateSelect + ` WHERE workspace_id=?`
	if !includeResolved {
		query += ` AND status='pending'`
	}
	query += ` ORDER BY CASE status WHEN 'pending' THEN 0 ELSE 1 END,created_at DESC,id LIMIT ?`
	rows, err := tx.QueryContext(ctx, query, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]contextmgr.MemoryCandidate, 0)
	for rows.Next() {
		value, err := scanMemoryCandidate(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func scanMemoryCandidate(row scanner) (contextmgr.MemoryCandidate, error) {
	var payload string
	if err := row.Scan(&payload); err != nil {
		return contextmgr.MemoryCandidate{}, err
	}
	var value contextmgr.MemoryCandidate
	if err := json.Unmarshal([]byte(payload), &value); err != nil {
		return contextmgr.MemoryCandidate{}, err
	}
	return value, value.Validate()
}

func refreshMemoryCandidateTx(ctx context.Context, tx *sql.Tx, value contextmgr.MemoryCandidate, at time.Time) (contextmgr.MemoryCandidate, error) {
	if value.Status != contextmgr.MemoryCandidatePending {
		return value, nil
	}
	current, err := prepareMemoryCandidateTx(ctx, tx, value.WorkspaceID, value.Source.NoteID, &value.Source, value.CreatedAt)
	if err != nil && apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		return contextmgr.MemoryCandidate{}, err
	}
	if err == nil && current.Fingerprint == value.Fingerprint {
		return value, nil
	}
	stale := value.Resolve(contextmgr.MemoryCandidateStale, "", "", at)
	if err := resolveMemoryCandidateTx(ctx, tx, stale, value.Version); err != nil {
		return contextmgr.MemoryCandidate{}, err
	}
	return stale, nil
}

func resolveMemoryCandidateTx(ctx context.Context, tx *sql.Tx, value contextmgr.MemoryCandidate, expectedVersion int64) error {
	if err := value.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE context_memory_candidates SET status=?,version=?,payload_json=?,updated_at=? WHERE id=? AND workspace_id=? AND status='pending' AND version=? AND fingerprint=?`, value.Status, value.Version, string(payload), ts(value.UpdatedAt), value.ID, value.WorkspaceID, expectedVersion, value.Fingerprint)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return apperror.New(apperror.CodeConflict, "memory candidate changed concurrently")
	}
	return nil
}

func prepareMemoryCandidateTx(ctx context.Context, tx *sql.Tx, workspaceID, noteID string, expected *contextmgr.MemoryCandidateSource, at time.Time) (contextmgr.MemoryCandidate, error) {
	invalid := func() (contextmgr.MemoryCandidate, error) {
		return contextmgr.MemoryCandidate{}, apperror.New(apperror.CodeFailedPrecondition, "memory candidate source is missing, changed or ineligible")
	}
	note, err := getNoteTx(ctx, tx, noteID)
	if errors.Is(err, sql.ErrNoRows) {
		return invalid()
	}
	if err != nil {
		return contextmgr.MemoryCandidate{}, err
	}
	if note.Status != domain.NoteActive || (note.Category != domain.NoteDecision && note.Category != domain.NoteSummary) || note.Visibility == domain.NoteVisibilityOwner || len(note.SourceRefs)+len(note.EvidenceIDs) == 0 {
		return invalid()
	}
	run, mission, err := getCoordinatorRunTx(ctx, tx, note.RunID)
	if errors.Is(err, sql.ErrNoRows) {
		return invalid()
	}
	if err != nil {
		return contextmgr.MemoryCandidate{}, err
	}
	if run.Validate() != nil || mission.Validate() != nil || mission.WorkspaceID != workspaceID || run.Status == domain.RunFailed || run.Status == domain.RunCancelled {
		return invalid()
	}
	if expected != nil && (expected.RunID != run.ID || expected.NoteVersion != note.Version) {
		return invalid()
	}
	kind, ref, fingerprint, found, err := memoryCandidateCompletionTx(ctx, tx, run, note, expected)
	if err != nil {
		return contextmgr.MemoryCandidate{}, err
	}
	if !found {
		return invalid()
	}
	value, err := contextmgr.NewMemoryCandidate(workspaceID, note.Title, note.Content, contextmgr.MemoryCandidateSource{
		RunID: run.ID, NoteID: note.ID, NoteVersion: note.Version, Category: string(note.Category), References: note.SourceRefs, EvidenceIDs: note.EvidenceIDs, UpdatedAt: note.UpdatedAt,
		CompletionKind: kind, CompletionRef: ref, CompletionFingerprint: fingerprint}, at)
	if err != nil {
		return invalid()
	}
	return value, nil
}

func memoryCandidateCompletionTx(ctx context.Context, tx *sql.Tx, run domain.Run, note domain.Note, expected *contextmgr.MemoryCandidateSource) (kind, ref, fingerprint string, found bool, err error) {
	wanted := ""
	if expected != nil {
		wanted = expected.CompletionKind
	}
	if wanted == "" || wanted == "delivery_checkpoint" {
		query := `SELECT checkpoint.id FROM delivery_checkpoints checkpoint JOIN delivery_checkpoint_operations operation ON operation.checkpoint_id=checkpoint.id WHERE checkpoint.run_id=? AND checkpoint.handoff_note_id=?`
		args := []any{run.ID, note.ID}
		if expected != nil {
			query += ` AND checkpoint.id=?`
			args = append(args, strings.TrimPrefix(expected.CompletionRef, "delivery_checkpoint:"))
		}
		query += ` LIMIT 1`
		var id string
		if err := tx.QueryRowContext(ctx, query, args...).Scan(&id); err == nil {
			checkpoint, err := getDeliveryCheckpoint(ctx, tx, id)
			if err != nil {
				return "", "", "", false, err
			}
			item, err := getWorkItemTx(ctx, tx, checkpoint.WorkItemID)
			if err != nil {
				return "", "", "", false, err
			}
			mode, err := getCurrentRunModeSnapshot(ctx, tx, run.ID)
			if err != nil {
				return "", "", "", false, err
			}
			if !domain.DeliveryCheckpointReady(checkpoint, item, mode) ||
				checkpoint.HandoffDigest != domain.DeliveryHandoffDigest(note.Title, note.Content) {
				return "", "", "", false, nil
			}
			return "delivery_checkpoint", "delivery_checkpoint:" + id, memoryCandidateDigest(checkpoint), true, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return "", "", "", false, err
		}
	}
	if wanted == "" || wanted == "delivery_receipt" {
		query := standardCodeDeliverySelect + ` WHERE run_id=? AND receipt_status='passed' AND julianday(created_at)>=julianday(?)`
		args := []any{run.ID, ts(note.UpdatedAt)}
		if expected != nil {
			query += ` AND id=?`
			args = append(args, strings.TrimPrefix(expected.CompletionRef, "delivery_receipt:"))
		}
		query += ` ORDER BY event_sequence DESC LIMIT 1`
		report, err := scanStandardCodeDelivery(tx.QueryRowContext(ctx, query, args...))
		if err == nil && report.Binding.RunID == run.ID && report.ReceiptStatus == standardcodedelivery.StatusPassed && !report.CreatedAt.Before(note.UpdatedAt) {
			return "delivery_receipt", "delivery_receipt:" + report.ID, report.ReceiptSHA256, true, nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", "", "", false, err
		}
	}
	if wanted == "" || wanted == "thread_reply" {
		query := `SELECT event.event_id,event.payload_json,event.created_at FROM run_events event
			WHERE event.run_id=? AND event.type=? AND event.source='run_supervisor'
			AND json_valid(event.payload_json) AND json_extract(event.payload_json,'$.requested_lifecycle_action')='finish'
			AND json_extract(event.payload_json,'$.lifecycle_action') IN ('continue','finish')
			AND COALESCE(json_extract(event.payload_json,'$.tool_round_boundary'),0)=0
			AND julianday(event.created_at)>=julianday(?)
			AND EXISTS(SELECT 1 FROM session_messages message WHERE message.id=json_extract(event.payload_json,'$.assistant_message_id') AND message.session_id=? AND message.role='assistant')`
		args := []any{run.ID, events.AgentTurnCompletedEvent, ts(note.UpdatedAt), run.SessionID}
		if expected != nil {
			query += ` AND event.event_id=?`
			args = append(args, strings.TrimPrefix(expected.CompletionRef, "run_event:"))
		}
		query += ` ORDER BY event.sequence DESC LIMIT 1`
		var id, payload, created string
		if err := tx.QueryRowContext(ctx, query, args...).Scan(&id, &payload, &created); err == nil {
			if !parseTS(created).Before(note.UpdatedAt) {
				return "thread_reply", "run_event:" + id, memoryCandidateDigest([]string{id, payload, created}), true, nil
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return "", "", "", false, err
		}
	}
	if (wanted == "" || wanted == "completed_run") && run.Status == domain.RunCompleted && run.FinishedAt != nil && !run.FinishedAt.Before(note.UpdatedAt) {
		return "completed_run", "run:" + run.ID, memoryCandidateDigest([]string{run.ID, run.MissionID, string(run.Status), ts(run.UpdatedAt), ts(*run.FinishedAt)}), true, nil
	}
	return "", "", "", false, nil
}

func requireMemoryCandidateWorkspaceTx(ctx context.Context, tx *sql.Tx, id string) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspaces WHERE id=? AND length(trim(root_path))>0)`, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return apperror.New(apperror.CodeNotFound, "memory candidate Workspace was not found")
	}
	return nil
}

func insertConfirmedCandidateMemoryTx(ctx context.Context, tx *sql.Tx, memory contextmgr.Memory) error {
	if err := memory.ValidateAt(memory.CreatedAt); err != nil {
		return err
	}
	references, err := json.Marshal(memory.References)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO context_memories
		(id,protocol_version,scope,scope_id,title,content,content_sha256,status,source_kind,source_ref,references_json,retention_until,redacted,created_by,updated_by,version,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, memory.ID, memory.ProtocolVersion, memory.Scope, memory.ScopeID, memory.Title, memory.Content, memory.ContentSHA256, memory.Status, memory.SourceKind, memory.SourceRef, string(references), nullableTS(memory.RetentionUntil), boolInt(memory.Redacted), memory.CreatedBy, memory.UpdatedBy, memory.Version, ts(memory.CreatedAt), ts(memory.UpdatedAt))
	return err
}

func memoryCandidateDigest(value any) string {
	payload, _ := json.Marshal(value)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
func memoryCandidateTime(at time.Time) time.Time {
	if at.IsZero() {
		return time.Now().UTC()
	}
	return at.UTC()
}
