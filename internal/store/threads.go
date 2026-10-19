package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/threadtranscript"
)

const threadSelect = `SELECT id, protocol_version, workspace_id, mission_id, title,
	status, COALESCE(active_run_id, ''), COALESCE(last_run_id, ''), version,
	created_at, updated_at, archived_at, deleted_at FROM threads`

// insertInitialThreadTx is intentionally tolerant of pre-v129 partial schemas:
// migration compatibility tests construct old schema prefixes and exercise Run
// creation before upgrading. A fully-migrated product database always has the
// Thread tables and therefore always commits the projection atomically.
func insertInitialThreadTx(ctx context.Context, tx *sql.Tx, mission domain.Mission,
	run domain.Run,
) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'threads'`).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return nil
	}
	threadID := domain.InitialThreadID(run.ID)
	title := redact.String(mission.Goal)
	if _, err := tx.ExecContext(ctx, `INSERT INTO threads
		(id, protocol_version, workspace_id, mission_id, title, status,
		 active_run_id, last_run_id, version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, 0, ?, ?)`, threadID,
		domain.ThreadProtocolVersion, mission.WorkspaceID, mission.ID, title,
		domain.ThreadActive, ts(run.CreatedAt), ts(run.UpdatedAt)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO thread_runs
		(thread_id, run_id, session_id, ordinal, predecessor_run_id, created_at)
		VALUES (?, ?, ?, 1, NULL, ?)`, threadID, run.ID, run.SessionID,
		ts(run.CreatedAt)); err != nil {
		return err
	}
	// Old migration-prefix fixtures intentionally create Runs before v139. A
	// fully migrated database always has this table and records a conservative,
	// non-authorizing Thread preference in the same creation transaction.
	var permissionTableExists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'thread_execution_permission_snapshots'`).
		Scan(&permissionTableExists); err != nil {
		return err
	}
	if permissionTableExists != 0 {
		threadRecord, err := scanThread(tx.QueryRowContext(ctx,
			threadSelect+` WHERE id = ?`, threadID))
		if err != nil {
			return err
		}
		preference, err := domain.NewInitialThreadExecutionPermissionSnapshot(
			idgen.New("thread-exec-permission"), threadRecord, "run_creation", run.CreatedAt)
		if err != nil {
			return err
		}
		if err := insertInitialThreadExecutionPermissionSnapshotTx(
			ctx, tx, preference, threadRecord); err != nil {
			return err
		}
	}
	payload, _ := json.Marshal(map[string]any{"run_id": run.ID, "backfilled": false})
	_, err := tx.ExecContext(ctx, `INSERT INTO thread_events
		(thread_id, run_id, type, source, payload_json, created_at)
		VALUES (?, ?, 'thread.created', 'run_creation', ?, ?)`, threadID, run.ID,
		string(payload), ts(run.CreatedAt))
	return err
}

func (s *SQLiteStore) GetThread(ctx context.Context, id string) (domain.Thread, error) {
	item, err := scanThread(s.db.QueryRowContext(ctx, threadSelect+` WHERE id = ?`, strings.TrimSpace(id)))
	return s.withProjectAssignment(ctx, item, err)
}

func (s *SQLiteStore) GetThreadByRun(ctx context.Context, runID string) (domain.Thread, error) {
	item, err := scanThread(s.db.QueryRowContext(ctx, threadSelect+`
		WHERE id = (SELECT thread_id FROM thread_runs WHERE run_id = ?)`,
		strings.TrimSpace(runID)))
	return s.withProjectAssignment(ctx, item, err)
}

func (s *SQLiteStore) GetThreadBySession(ctx context.Context, sessionID string) (domain.Thread, error) {
	item, err := scanThread(s.db.QueryRowContext(ctx, threadSelect+`
		WHERE id = (SELECT thread_id FROM thread_runs WHERE session_id = ?)`,
		strings.TrimSpace(sessionID)))
	return s.withProjectAssignment(ctx, item, err)
}

func (s *SQLiteStore) ListThreadsByCreationPage(ctx context.Context, filter domain.ThreadFilter,
	beforeCreatedAt time.Time, beforeID string,
) ([]domain.Thread, error) {
	if err := validateStoreCreationPage(beforeCreatedAt, beforeID, filter.Limit); err != nil {
		return nil, err
	}
	titleQuery, err := domain.NormalizeThreadTitleQuery(filter.TitleQuery)
	if err != nil {
		return nil, err
	}
	query := threadSelect + ` WHERE 1=1`
	args := make([]any, 0, 6)
	if filter.Status != "" {
		if !domain.ValidThreadStatus(filter.Status) {
			return nil, fmt.Errorf("invalid thread status %q", filter.Status)
		}
		query += ` AND status = ?`
		args = append(args, filter.Status)
	} else if !filter.IncludeDeleted {
		query += ` AND status <> ?`
		args = append(args, domain.ThreadDeleted)
	}
	if titleQuery != "" {
		query += ` AND instr(lower(title), ?) > 0`
		args = append(args, titleQuery)
	}
	if !beforeCreatedAt.IsZero() {
		query += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		anchor := ts(beforeCreatedAt)
		args = append(args, anchor, anchor, beforeID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, filter.Limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.Thread, 0, filter.Limit)
	for rows.Next() {
		item, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range items {
		if err = readProjectAssignment(ctx, s.db, &items[i]); err != nil {
			return nil, err
		}
	}
	return items, nil
}

// GetThreadExecutionFacts reads a whole bounded list page in one SQLite
// snapshot. Lease and request identities stay inside the control plane.
func (s *SQLiteStore) GetThreadExecutionFacts(ctx context.Context,
	threadIDs []string,
) (map[string]domain.ThreadExecutionFacts, error) {
	if len(threadIDs) > 1000 {
		return nil, errors.New("Thread execution facts are limited to 1000 Threads")
	}
	facts := make(map[string]domain.ThreadExecutionFacts, len(threadIDs))
	if len(threadIDs) == 0 {
		return facts, nil
	}
	args := make([]any, len(threadIDs))
	placeholders := make([]string, len(threadIDs))
	for index, id := range threadIDs {
		if !domain.ValidAgentID(id) {
			return nil, errors.New("Thread execution facts identity is invalid")
		}
		args[index], placeholders[index] = id, "?"
	}
	rows, err := s.db.QueryContext(ctx, `SELECT thread.id, run.id, run.status,
		EXISTS (SELECT 1 FROM tool_approvals approval WHERE approval.run_id = run.id AND approval.status = 'pending'),
		(EXISTS (SELECT 1 FROM run_execution_leases lease
			WHERE lease.run_id = run.id AND lease.status = 'active')
		 OR EXISTS (SELECT 1 FROM run_supervisor_checkpoints checkpoint
			WHERE checkpoint.run_id = run.id AND run.status NOT IN ('completed', 'failed', 'cancelled')
			 AND (checkpoint.phase = 'turn_failed'
			 OR (checkpoint.phase = 'turn_started' AND NOT EXISTS
				(SELECT 1 FROM tool_approvals approval WHERE approval.run_id = run.id AND approval.status = 'pending'))))
		 OR EXISTS (SELECT 1 FROM run_execution_handoff_operations operation
			WHERE operation.run_id = run.id AND NOT EXISTS
				(SELECT 1 FROM run_execution_handoff_results result WHERE result.operation_id = operation.id))
		 OR EXISTS (SELECT 1 FROM thread_message_intents intent
			WHERE intent.thread_id = thread.id AND intent.message_id IS NULL AND intent.rejected = 0))
		FROM threads thread JOIN runs run
		 ON run.id = COALESCE(NULLIF(thread.active_run_id, ''), thread.last_run_id)
		WHERE thread.id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var fact domain.ThreadExecutionFacts
		if err := rows.Scan(&id, &fact.RunID, &fact.RunStatus, &fact.PendingApproval, &fact.Unsettled); err != nil {
			return nil, err
		}
		if !domain.ValidRunStatus(fact.RunStatus) {
			return nil, errors.New("Thread execution facts Run status is invalid")
		}
		facts[id] = fact
	}
	return facts, rows.Err()
}

func (s *SQLiteStore) ListThreadRuns(ctx context.Context, threadID string) ([]domain.ThreadRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT thread_id, run_id, session_id, ordinal,
		COALESCE(predecessor_run_id, ''), created_at FROM thread_runs
		WHERE thread_id = ? ORDER BY ordinal`, strings.TrimSpace(threadID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ThreadRun
	for rows.Next() {
		var item domain.ThreadRun
		var created string
		if err := rows.Scan(&item.ThreadID, &item.RunID, &item.SessionID, &item.Ordinal,
			&item.PredecessorRunID, &created); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTS(created)
		if err := item.Validate(); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListThreadMessagesPage(ctx context.Context, threadID string,
	includeCompacted bool, offset, limit int,
) ([]domain.ThreadMessage, error) {
	if err := validateStoreListOffset(offset); err != nil {
		return nil, err
	}
	all := limit == 0 && offset == 0
	if !all && (limit <= 0 || limit > 1000) {
		return nil, errors.New("thread message limit must be between 1 and 1000")
	}
	query := `SELECT identity, thread_id, run_id, session_id, role, content,
		provenance_version, source_kind, source_ref, content_sha256,
		instruction_authorized, message_status, token_estimate, compacted, created_at
		FROM (
			SELECT binding.ordinal AS run_ordinal, message.created_at AS sort_time,
				'message-' || message.id AS identity, binding.thread_id,
				binding.run_id, message.session_id, message.role, message.content,
				message.provenance_version, message.source_kind,
				message.source_ref, message.content_sha256,
				message.instruction_authorized, 'committed' AS message_status,
				message.token_estimate, message.compacted, message.created_at
			FROM thread_runs binding JOIN session_messages message
				ON message.session_id = binding.session_id
			WHERE binding.thread_id = ?`
	args := []any{strings.TrimSpace(threadID)}
	if !includeCompacted {
		query += ` AND message.compacted = 0`
	}
	query += ` UNION ALL
			SELECT binding.ordinal AS run_ordinal, steering.created_at AS sort_time,
				steering.id AS identity, binding.thread_id, binding.run_id,
				steering.session_id, 'user' AS role, steering.content,
				? AS provenance_version, ? AS source_kind, '' AS source_ref,
				steering.content_sha256, 1 AS instruction_authorized,
				steering.status AS message_status,
				(length(CAST(steering.content AS BLOB)) + 3) / 4 AS token_estimate,
				0 AS compacted, steering.created_at
			FROM thread_runs binding JOIN operator_steering_messages steering
				ON steering.run_id = binding.run_id AND steering.session_id = binding.session_id
			WHERE binding.thread_id = ? AND steering.status IN ('pending', 'cancelled')
		) ORDER BY run_ordinal, sort_time, identity`
	args = append(args, session.ContextProvenanceVersion, session.SourceOperatorMessage,
		strings.TrimSpace(threadID))
	if !all {
		query += ` LIMIT ? OFFSET ?`
		args = append(args, limit, offset)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ThreadMessage
	for rows.Next() {
		var item domain.ThreadMessage
		var compacted, instructionAuthorized int
		var created string
		if err := rows.Scan(&item.ID, &item.ThreadID, &item.RunID, &item.SessionID,
			&item.Role, &item.Content, &item.ProvenanceVersion, &item.SourceKind,
			&item.SourceRef, &item.ContentSHA256, &instructionAuthorized, &item.Status,
			&item.TokenEstimate, &compacted, &created); err != nil {
			return nil, err
		}
		item.Compacted = compacted != 0
		item.InstructionAuthorized = instructionAuthorized != 0
		item.CreatedAt = parseTS(created)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListThreadEvents(ctx context.Context, threadID string,
	limit int,
) ([]domain.ThreadEvent, error) {
	if limit < 0 || limit > 1000 {
		return nil, errors.New("thread event limit must be between 0 and 1000")
	}
	query := `SELECT id, thread_id, COALESCE(run_id, ''),
		type, source, payload_json, created_at FROM thread_events
		WHERE thread_id = ? ORDER BY id`
	args := []any{strings.TrimSpace(threadID)}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ThreadEvent
	for rows.Next() {
		var item domain.ThreadEvent
		var created string
		if err := rows.Scan(&item.ID, &item.ThreadID, &item.RunID, &item.Type,
			&item.Source, &item.PayloadJSON, &created); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTS(created)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) listThreadRunAuditEvents(ctx context.Context,
	threadID string,
) ([]domain.ThreadRunAuditEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT event.event_id, event.version,
		event.run_id, event.mission_id, event.sequence, event.type, event.source,
		COALESCE(event.subject_id, ''), event.payload_json, event.created_at
		FROM thread_runs binding JOIN run_events event ON event.run_id = binding.run_id
		WHERE binding.thread_id = ? ORDER BY binding.ordinal, event.sequence`,
		strings.TrimSpace(threadID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ThreadRunAuditEvent
	for rows.Next() {
		var item domain.ThreadRunAuditEvent
		var created string
		if err := rows.Scan(&item.EventID, &item.Version, &item.RunID, &item.MissionID,
			&item.Sequence, &item.Type, &item.Source, &item.SubjectID,
			&item.PayloadJSON, &created); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTS(created)
		out = append(out, item)
	}
	return out, rows.Err()
}

// ListThreadTranscriptSourceBefore reads the append-only Thread ordering key in
// reverse so the primary UI can open at the newest activity and page older
// history without offsets that shift when a Run appends events or gains a
// successor. Sequence zero is the immutable Run boundary.
func (s *SQLiteStore) ListThreadTranscriptSourceBefore(ctx context.Context, threadID string,
	beforeOrdinal, beforeSequence int64, limit int,
) ([]threadtranscript.Source, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil, errors.New("thread transcript requires a Thread id")
	}
	if limit <= 0 || limit > threadtranscript.MaxSourceRecords {
		return nil, fmt.Errorf("thread transcript source limit must be between 1 and %d",
			threadtranscript.MaxSourceRecords)
	}
	if beforeOrdinal < 0 || beforeSequence < 0 || (beforeOrdinal == 0 && beforeSequence != 0) {
		return nil, errors.New("thread transcript source cursor is invalid")
	}
	// A committed Session message is recorded after its tools. Keep the user's
	// original queue event as the narrative anchor only when both durable records
	// are exactly bound. This lookup is independent of the requested page, so a
	// queue event and its later Session record cannot produce two user bubbles.
	query := `WITH bound_operator_messages AS (
		SELECT queued.event_id AS queued_event_id, recorded.event_id AS session_event_id
		FROM thread_runs binding
		JOIN operator_steering_messages steering ON steering.run_id = binding.run_id
			AND steering.session_id = binding.session_id AND steering.status = 'committed'
		JOIN session_messages message ON message.id = steering.session_message_id
			AND message.session_id = steering.session_id AND message.role = 'user'
			AND message.source_kind = 'operator_message' AND message.instruction_authorized = 1
			AND message.content_sha256 = steering.content_sha256 AND message.content = steering.content
		JOIN run_events queued ON queued.run_id = steering.run_id
			AND queued.type = 'operator.steering_queued' AND queued.subject_id = steering.id
		JOIN run_events recorded ON recorded.run_id = steering.run_id
			AND recorded.type = 'session.message_created' AND recorded.subject_id = CAST(message.id AS TEXT)
			AND recorded.sequence > queued.sequence
			AND json_extract(recorded.payload_json, '$.message_id') = message.id
			AND json_extract(recorded.payload_json, '$.session_id') = message.session_id
			AND json_extract(recorded.payload_json, '$.role') = 'user'
			AND json_extract(recorded.payload_json, '$.source_kind') = 'operator_message'
			AND json_extract(recorded.payload_json, '$.instruction_authorized') = 1
			AND json_extract(recorded.payload_json, '$.content_sha256') = message.content_sha256
			AND json_extract(recorded.payload_json, '$.content') = message.content
		WHERE binding.thread_id = ?
	), transcript AS (
		SELECT binding.thread_id, binding.ordinal, binding.run_id, binding.session_id,
			COALESCE(binding.predecessor_run_id, '') AS predecessor_run_id,
			COALESCE(predecessor.status, '') AS predecessor_run_status,
			current.status AS run_status, 0 AS sequence, '' AS event_id,
			'' AS event_version, current.mission_id, '' AS event_type,
			'' AS event_source, '' AS subject_id, '' AS payload_json,
			'' AS operator_content, '' AS operator_status, 0 AS operator_message_bound,0 AS operator_image_count,0 AS operator_attachment_count, '' AS operator_delivery_mode,
			'' AS promoted_to_message_id, '' AS promoted_from_message_id, binding.created_at
		FROM thread_runs binding
		JOIN runs current ON current.id = binding.run_id
		LEFT JOIN runs predecessor ON predecessor.id = binding.predecessor_run_id
		UNION ALL
		SELECT binding.thread_id, binding.ordinal, binding.run_id, binding.session_id,
			COALESCE(binding.predecessor_run_id, ''),
			COALESCE(predecessor.status, ''), current.status, event.sequence,
			event.event_id, event.version, event.mission_id, event.type,
			event.source, COALESCE(event.subject_id, ''), event.payload_json,
			CASE WHEN EXISTS (SELECT 1 FROM bound_operator_messages bound WHERE bound.queued_event_id = event.event_id)
				THEN COALESCE(steering.content, '')
			WHEN EXISTS (SELECT 1 FROM run_events boundary
				JOIN session_messages original ON original.id=json_extract(boundary.payload_json,'$.user_message_id')
				WHERE boundary.run_id=event.run_id AND boundary.type='agent.turn_completed' AND boundary.source='run_supervisor'
				AND json_extract(boundary.payload_json,'$.tool_round_boundary')=1
				AND json_extract(boundary.payload_json,'$.operator_message_id')=steering.id
				AND original.session_id=steering.session_id AND original.content_sha256=steering.content_sha256
				AND original.content=steering.content AND original.role='user' AND original.source_kind='operator_message')
				THEN '' ELSE COALESCE(steering.content, '') END,
			COALESCE(steering.status, ''),
			EXISTS (SELECT 1 FROM bound_operator_messages bound
				WHERE bound.queued_event_id = event.event_id OR bound.session_event_id = event.event_id),
			COALESCE(steering.image_count,0),COALESCE(steering.attachment_count,0),
			COALESCE(steering.delivery_mode, ''),
			COALESCE(promoted_to.replacement_message_id, ''), COALESCE(promoted_from.message_id, ''),
			event.created_at
		FROM thread_runs binding
		JOIN runs current ON current.id = binding.run_id
		LEFT JOIN runs predecessor ON predecessor.id = binding.predecessor_run_id
		JOIN run_events event ON event.run_id = binding.run_id
		LEFT JOIN operator_steering_messages steering ON steering.id = event.subject_id
		LEFT JOIN operator_steering_promotions promoted_to ON promoted_to.message_id=steering.id AND promoted_to.run_id=binding.run_id
		LEFT JOIN operator_steering_promotions promoted_from ON promoted_from.replacement_message_id=steering.id AND promoted_from.run_id=binding.run_id
	)
	SELECT ordinal, run_id, session_id, predecessor_run_id,
		predecessor_run_status, run_status, sequence, event_id, event_version,
		mission_id, event_type, event_source, subject_id, payload_json, created_at
		, operator_content, operator_status, operator_message_bound,operator_image_count,operator_attachment_count, operator_delivery_mode
		, promoted_to_message_id, promoted_from_message_id
	FROM transcript WHERE thread_id = ?`
	args := []any{threadID, threadID}
	if beforeOrdinal > 0 {
		query += ` AND (ordinal < ? OR (ordinal = ? AND sequence < ?))`
		args = append(args, beforeOrdinal, beforeOrdinal, beforeSequence)
	}
	query += ` ORDER BY ordinal DESC, sequence DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]threadtranscript.Source, 0, limit)
	for rows.Next() {
		var item threadtranscript.Source
		var eventID, eventVersion, missionID, eventType, eventSource, subjectID string
		var payloadJSON, created string
		if err := rows.Scan(&item.Ordinal, &item.RunID, &item.SessionID,
			&item.PredecessorRunID, &item.PredecessorRunStatus, &item.RunStatus,
			&item.Sequence, &eventID, &eventVersion, &missionID, &eventType,
			&eventSource, &subjectID, &payloadJSON, &created, &item.OperatorContent,
			&item.OperatorStatus, &item.OperatorMessageBound, &item.OperatorImageCount, &item.OperatorAttachmentCount,
			&item.OperatorDeliveryMode, &item.PromotedToMessageID, &item.PromotedFromMessageID); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTS(created)
		if item.Sequence > 0 {
			item.Event = &events.Event{EventID: eventID, Version: eventVersion,
				RunID: item.RunID, MissionID: missionID, Sequence: item.Sequence,
				Type: eventType, Source: eventSource, SubjectID: subjectID,
				PayloadJSON: payloadJSON, CreatedAt: item.CreatedAt}
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// EnsureThreadSuccessor returns the current live Run when one already exists,
// otherwise it creates exactly one successor Run and a fresh Session inside the
// same immediate SQLite transaction. The successor carries the predecessor's
// latest exact network preference, while createRunGraphTx still resets process,
// lease, capability, and execution-permission authority for the new Run.
func (s *SQLiteStore) EnsureThreadSuccessor(ctx context.Context, threadID,
	expectedLastRunID string, mission domain.Mission, candidate domain.Run,
	mode domain.RunModeSnapshot, linkedSession session.Session, initialEvents []events.Event,
) (domain.Thread, domain.Run, bool, error) {
	return s.ensureThreadSuccessor(ctx, threadID, expectedLastRunID, mission, candidate, mode, linkedSession, initialEvents, nil, nil, nil)
}

func (s *SQLiteStore) EnsureThreadSuccessorForMessage(ctx context.Context, request domain.ThreadMessageIntentRequest,
	expectedLastRunID string, mission domain.Mission, candidate domain.Run,
	mode domain.RunModeSnapshot, linkedSession session.Session, initialEvents []events.Event,
) (domain.Thread, domain.Run, bool, error) {
	return s.ensureThreadSuccessor(ctx, request.ThreadID, expectedLastRunID, mission, candidate, mode, linkedSession, initialEvents, &request, nil, nil)
}

func (s *SQLiteStore) ensureThreadSuccessor(ctx context.Context, threadID,
	expectedLastRunID string, mission domain.Mission, candidate domain.Run,
	mode domain.RunModeSnapshot, linkedSession session.Session, initialEvents []events.Event,
	messageIntent *domain.ThreadMessageIntentRequest,
	files *domain.ThreadFileContinuation,
	plan *threadPlanSuccessorBinding,
) (domain.Thread, domain.Run, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.Thread{}, domain.Run{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if plan != nil {
		if err := prepareThreadPlanSuccessorTx(ctx, tx, threadID, expectedLastRunID, mode, files, *plan); err != nil {
			return domain.Thread{}, domain.Run{}, false, err
		}
		if saved, found, err := readThreadPlanSuccessorTx(ctx, tx, threadID, expectedLastRunID, *plan); err != nil || found {
			if err != nil {
				return domain.Thread{}, domain.Run{}, false, err
			}
			thread, err := scanThread(tx.QueryRowContext(ctx, threadSelect+` WHERE id=?`, threadID))
			return thread, saved, false, err
		}
	}
	threadRecord, err := scanThread(tx.QueryRowContext(ctx, threadSelect+` WHERE id = ?`,
		strings.TrimSpace(threadID)))
	if err != nil {
		return domain.Thread{}, domain.Run{}, false, err
	}
	if messageIntent != nil {
		key, fingerprint, _, err := threadMessageIntentIdentity(*messageIntent)
		if err != nil {
			return domain.Thread{}, domain.Run{}, false, err
		}
		intent, found, err := getThreadMessageIntentTx(ctx, tx, key)
		if err != nil {
			return domain.Thread{}, domain.Run{}, false, err
		}
		if !found || intent.RequestFingerprint != fingerprint || intent.Rejected {
			return domain.Thread{}, domain.Run{}, false, apperror.New(apperror.CodeConflict, "Thread message preparation intent changed")
		}
		if intent.MessageID != "" {
			accepted, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id, status, config_json, budget_json, started_at, finished_at, created_at, updated_at FROM runs WHERE id = ?`, intent.RunID))
			if err != nil {
				return domain.Thread{}, domain.Run{}, false, err
			}
			return threadRecord, accepted, false, tx.Commit()
		}
	}
	if threadRecord.Status != domain.ThreadActive {
		return domain.Thread{}, domain.Run{}, false, apperror.New(
			apperror.CodeFailedPrecondition, "Thread is not active")
	}
	if threadRecord.ActiveRunID != "" {
		active, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id,
			status, config_json, budget_json, started_at, finished_at, created_at, updated_at
			FROM runs WHERE id = ?`, threadRecord.ActiveRunID))
		if err != nil {
			return domain.Thread{}, domain.Run{}, false, err
		}
		if active.Terminal() {
			return domain.Thread{}, domain.Run{}, false, errors.New(
				"Thread active Run projection points at a terminal Run")
		}
		if err := tx.Commit(); err != nil {
			return domain.Thread{}, domain.Run{}, false, err
		}
		return threadRecord, active, false, nil
	}
	if threadRecord.LastRunID != strings.TrimSpace(expectedLastRunID) {
		return domain.Thread{}, domain.Run{}, false, apperror.New(
			apperror.CodeConflict, "Thread last Run changed during continuation")
	}
	predecessor, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id,
		status, config_json, budget_json, started_at, finished_at, created_at, updated_at
		FROM runs WHERE id = ?`, threadRecord.LastRunID))
	if err != nil {
		return domain.Thread{}, domain.Run{}, false, err
	}
	if !predecessor.Terminal() {
		return domain.Thread{}, domain.Run{}, false, apperror.New(
			apperror.CodeFailedPrecondition, "Thread has no terminal Run to continue")
	}
	predecessorMode, err := getCurrentRunModeSnapshot(ctx, tx, predecessor.ID)
	if err != nil {
		return domain.Thread{}, domain.Run{}, false, err
	}
	expectedScope, legacyNetworkReset := successorStoredRunScope(predecessorMode.Scope)
	if candidate.MissionID != threadRecord.MissionID || mission.ID != threadRecord.MissionID ||
		mission.WorkspaceID != threadRecord.WorkspaceID ||
		!sameRunModeScope(mission.Scope, expectedScope) ||
		!sameRunModeScope(mode.Scope, expectedScope) ||
		mode.Surface != predecessorMode.Surface || (plan == nil && mode.Phase != predecessorMode.Phase) {
		return domain.Thread{}, domain.Run{}, false, apperror.New(
			apperror.CodeConflict, "Thread successor did not preserve its predecessor mode preference")
	}
	if files != nil {
		if err := validateThreadFilePreparationTx(ctx, tx, *files); err != nil {
			return domain.Thread{}, domain.Run{}, false, err
		}
	} else {
		var needsFiles bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_file_drydock_bindings WHERE run_id = ?)`, predecessor.ID).Scan(&needsFiles); err != nil {
			return domain.Thread{}, domain.Run{}, false, err
		}
		if needsFiles {
			return domain.Thread{}, domain.Run{}, false, apperror.New(apperror.CodeFailedPrecondition, "Thread successor requires its current isolated workspace preparation")
		}
	}
	if err := createRunGraphTx(ctx, tx, mission, candidate, mode, linkedSession, true,
		false, initialEvents); err != nil {
		return domain.Thread{}, domain.Run{}, false, err
	}
	var ordinal int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal), 0) + 1
		FROM thread_runs WHERE thread_id = ?`, threadRecord.ID).Scan(&ordinal); err != nil {
		return domain.Thread{}, domain.Run{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO thread_runs
		(thread_id, run_id, session_id, ordinal, predecessor_run_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, threadRecord.ID, candidate.ID, linkedSession.ID,
		ordinal, predecessor.ID, ts(candidate.CreatedAt)); err != nil {
		return domain.Thread{}, domain.Run{}, false, err
	}
	materializedPermission, permissionMaterialized, err :=
		materializeThreadExecutionPermissionForSuccessorTx(ctx, tx, threadRecord, candidate)
	if err != nil {
		return domain.Thread{}, domain.Run{}, false, err
	}
	threadPreference, err := getCurrentThreadExecutionPermissionSnapshot(
		ctx, tx, threadRecord.ID)
	if err != nil {
		return domain.Thread{}, domain.Run{}, false, err
	}
	materializedBrowserCDP, browserCDPMaterialized, err :=
		materializeThreadBrowserCDPPermissionForSuccessorTx(ctx, tx, threadRecord,
			predecessor, candidate, threadPreference)
	if err != nil {
		return domain.Thread{}, domain.Run{}, false, err
	}
	if err := carryThreadEpochInstructionsTx(ctx, tx, threadRecord.ID, predecessor, candidate); err != nil {
		return domain.Thread{}, domain.Run{}, false, err
	}
	var executionSettings map[string]any
	if files != nil {
		if err := commitThreadFileContinuationTx(ctx, tx, predecessor, candidate, mode, *files); err != nil {
			return domain.Thread{}, domain.Run{}, false, err
		}
	} else {
		executionSettings, err = materializeThreadExecutionSettingsTx(ctx, tx, threadRecord, predecessor, candidate, mode)
		if err != nil {
			return domain.Thread{}, domain.Run{}, false, err
		}
	}
	if plan == nil {
		if err := continueThreadPlanTx(ctx, tx, predecessor, candidate, mode, candidate.CreatedAt); err != nil {
			return domain.Thread{}, domain.Run{}, false, err
		}
	}
	networkInherited := mode.Scope.NetworkMode == "allowlist" &&
		len(mode.Scope.AllowedTargets) > 0
	payloadFields := map[string]any{
		"predecessor_run_id": predecessor.ID, "successor_run_id": candidate.ID,
		"authority_inherited":                 networkInherited,
		"network_authority_inherited":         networkInherited,
		"network_authority_source_revision":   predecessorMode.Revision,
		"network_allowed_target_count":        len(mode.Scope.AllowedTargets),
		"legacy_network_authority_reset":      legacyNetworkReset,
		"runtime_authority_inherited":         false,
		"execution_permission_materialized":   permissionMaterialized,
		"execution_permission_mode":           materializedPermission.Mode,
		"execution_permission_snapshot_id":    materializedPermission.ID,
		"browser_cdp_permission_materialized": browserCDPMaterialized,
		"browser_cdp_permission_mode":         materializedBrowserCDP.Mode,
		"browser_cdp_permission_snapshot_id":  materializedBrowserCDP.ID,
	}
	for key, value := range executionSettings {
		payloadFields[key] = value
	}
	if plan != nil {
		payloadFields["plan_control_operation_digest"] = plan.keyDigest
		payloadFields["plan_control_requested_by"] = plan.requestedBy
		payloadFields["plan_control_phase"] = mode.Phase
		payloadFields["plan_selection_inherited"] = false
	}
	if messageIntent != nil {
		key, fingerprint, _, err := threadMessageIntentIdentity(*messageIntent)
		if err != nil {
			return domain.Thread{}, domain.Run{}, false, err
		}
		payloadFields["message_operation_key_digest"] = key
		payloadFields["message_request_fingerprint"] = fingerprint
	}
	payload, _ := json.Marshal(payloadFields)
	if _, err := tx.ExecContext(ctx, `INSERT INTO thread_events
		(thread_id, run_id, type, source, payload_json, created_at)
		VALUES (?, ?, 'thread.run_successor_created', 'thread_continuation', ?, ?)`,
		threadRecord.ID, candidate.ID, string(payload), ts(candidate.CreatedAt)); err != nil {
		return domain.Thread{}, domain.Run{}, false, err
	}
	threadRecord, err = scanThread(tx.QueryRowContext(ctx, threadSelect+` WHERE id = ?`,
		threadRecord.ID))
	if err != nil {
		return domain.Thread{}, domain.Run{}, false, err
	}
	if threadRecord.ActiveRunID != candidate.ID || threadRecord.LastRunID != candidate.ID {
		return domain.Thread{}, domain.Run{}, false, errors.New(
			"Thread successor projection did not bind the candidate Run")
	}
	if err := tx.Commit(); err != nil {
		return domain.Thread{}, domain.Run{}, false, err
	}
	return threadRecord, candidate, true, nil
}

func successorStoredRunScope(scope domain.Scope) (domain.Scope, bool) {
	if _, err := canonicalStoredRunNetworkTargets(scope); err == nil {
		return domain.CloneScope(scope), false
	}
	reset := domain.CloneScope(scope)
	reset.NetworkMode = "disabled"
	reset.AllowedTargets = nil
	return reset, true
}

func (s *SQLiteStore) TransitionThread(ctx context.Context, id string,
	action domain.ThreadLifecycleAction, expectedVersion int64, requestedBy string,
	at time.Time,
) (domain.Thread, error) {
	return s.transitionThread(ctx, id, action, expectedVersion, requestedBy, "", at)
}

func (s *SQLiteStore) TransitionThreadWithOperationKey(ctx context.Context, id string,
	action domain.ThreadLifecycleAction, expectedVersion int64, requestedBy, operationKey string,
	at time.Time,
) (domain.Thread, error) {
	operationKey, err := domain.NormalizeAgentOperationKey(operationKey)
	if err != nil {
		return domain.Thread{}, apperror.New(apperror.CodeInvalidArgument,
			"Thread lifecycle idempotency key is invalid")
	}
	return s.transitionThread(ctx, id, action, expectedVersion, requestedBy, operationKey, at)
}

func (s *SQLiteStore) transitionThread(ctx context.Context, id string,
	action domain.ThreadLifecycleAction, expectedVersion int64, requestedBy, operationKey string,
	at time.Time,
) (domain.Thread, error) {
	if expectedVersion <= 0 {
		return domain.Thread{}, apperror.New(apperror.CodeInvalidArgument,
			"Thread lifecycle expected version must be positive")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	} else {
		at = at.UTC()
	}
	requestedBy = strings.TrimSpace(requestedBy)
	if requestedBy == "" {
		requestedBy = "thread_service"
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.Thread{}, err
	}
	defer func() { _ = tx.Rollback() }()
	keyDigest, requestFingerprint := "", ""
	if operationKey != "" {
		keyDigest = runmutation.Fingerprint("thread_lifecycle_operation.v1", operationKey)
		requestFingerprint = runmutation.Fingerprint("thread_lifecycle_request.v1",
			strings.TrimSpace(id), string(action), strconv.FormatInt(expectedVersion, 10),
			requestedBy)
		var storedFingerprint, storedThreadID, storedAction, resultJSON string
		err := tx.QueryRowContext(ctx, `SELECT request_fingerprint, thread_id, action,
			result_json FROM thread_lifecycle_operations WHERE key_digest = ?`, keyDigest).
			Scan(&storedFingerprint, &storedThreadID, &storedAction, &resultJSON)
		if err == nil {
			if storedFingerprint != requestFingerprint || storedThreadID != strings.TrimSpace(id) ||
				storedAction != string(action) {
				return domain.Thread{}, apperror.New(apperror.CodeConflict,
					"Thread lifecycle idempotency key was reused for another request")
			}
			var replay domain.Thread
			if err := json.Unmarshal([]byte(resultJSON), &replay); err != nil {
				return domain.Thread{}, err
			}
			if err := replay.Validate(); err != nil {
				return domain.Thread{}, err
			}
			if err := tx.Commit(); err != nil {
				return domain.Thread{}, err
			}
			return replay, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return domain.Thread{}, err
		}
	}
	current, err := scanThread(tx.QueryRowContext(ctx, threadSelect+` WHERE id = ?`,
		strings.TrimSpace(id)))
	if err != nil {
		return domain.Thread{}, err
	}
	if expectedVersion > 0 && current.Version != expectedVersion {
		return domain.Thread{}, apperror.New(apperror.CodeConflict,
			"Thread lifecycle version changed")
	}
	commitResult := func(value domain.Thread) (domain.Thread, error) {
		if keyDigest != "" {
			resultJSON, marshalErr := json.Marshal(value)
			if marshalErr != nil {
				return domain.Thread{}, marshalErr
			}
			if _, insertErr := tx.ExecContext(ctx, `INSERT INTO thread_lifecycle_operations
				(key_digest, request_fingerprint, thread_id, action, result_json, created_at)
				VALUES (?, ?, ?, ?, ?, ?)`, keyDigest, requestFingerprint, value.ID,
				string(action), string(resultJSON), ts(at)); insertErr != nil {
				return domain.Thread{}, insertErr
			}
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return domain.Thread{}, commitErr
		}
		return value, nil
	}
	var target domain.ThreadStatus
	var archivedAt, deletedAt any
	switch action {
	case domain.ThreadArchive:
		if current.Status == domain.ThreadArchived {
			return commitResult(current)
		}
		if current.Status != domain.ThreadActive {
			return domain.Thread{}, apperror.New(apperror.CodeFailedPrecondition,
				"only an active Thread can be archived")
		}
		target, archivedAt = domain.ThreadArchived, ts(at)
	case domain.ThreadRestore:
		if current.Status == domain.ThreadActive {
			return commitResult(current)
		}
		if current.Status != domain.ThreadArchived {
			return domain.Thread{}, apperror.New(apperror.CodeFailedPrecondition,
				"only an archived Thread can be restored")
		}
		target = domain.ThreadActive
	case domain.ThreadDelete:
		if current.Status == domain.ThreadDeleted {
			return commitResult(current)
		}
		target, deletedAt = domain.ThreadDeleted, ts(at)
		if current.ArchivedAt != nil {
			archivedAt = ts(*current.ArchivedAt)
		} else {
			archivedAt = ts(at)
		}
	default:
		return domain.Thread{}, apperror.New(apperror.CodeInvalidArgument,
			"unsupported Thread lifecycle action")
	}
	lifecycleRunID := current.ActiveRunID
	if current.ActiveRunID != "" && action != domain.ThreadRestore {
		active, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id,
			status, config_json, budget_json, started_at, finished_at, created_at, updated_at
			FROM runs WHERE id = ?`, current.ActiveRunID))
		if err != nil {
			return domain.Thread{}, err
		}
		if active.Terminal() {
			return domain.Thread{}, apperror.New(apperror.CodeConflict,
				"Thread active Run projection is terminal")
		}
		if err := requireThreadLifecycleRunQuiescentTx(ctx, tx, active.ID, at); err != nil {
			return domain.Thread{}, err
		}
		targetRunStatus := domain.RunStatus("")
		reason := "Thread archived by explicit operator request"
		if action == domain.ThreadDelete {
			targetRunStatus = domain.RunCancelled
			reason = "Thread deleted by explicit operator request"
		} else {
			switch active.Status {
			case domain.RunCreated, domain.RunPreparing:
				targetRunStatus = domain.RunCancelled
			case domain.RunRunning:
				targetRunStatus = domain.RunPaused
			case domain.RunWaitingApproval:
				// Preserve the approval gate itself. Turning a real pending approval
				// into a generic paused Run would strand its decision record and let
				// ordinary resume semantics bypass the dedicated approval flow.
			case domain.RunPaused:
				// A restored Thread keeps its paused Run until the user explicitly
				// continues it. Archiving an already paused Run is a no-op.
			default:
				return domain.Thread{}, apperror.New(apperror.CodeFailedPrecondition,
					"Thread active Run cannot be safely quiesced")
			}
		}
		if targetRunStatus != "" {
			if err := transitionThreadLifecycleRunTx(ctx, tx, &active,
				targetRunStatus, reason, at); err != nil {
				return domain.Thread{}, err
			}
			if err := syncControlledRunRootTx(ctx, tx, active); err != nil {
				return domain.Thread{}, err
			}
		}
		if targetRunStatus == domain.RunCancelled {
			// The v129 terminal projection trigger clears active_run_id and bumps
			// the Thread version in this transaction. Reload before applying the
			// requested Thread transition so optimistic versioning remains exact.
			current, err = scanThread(tx.QueryRowContext(ctx,
				threadSelect+` WHERE id = ?`, current.ID))
			if err != nil {
				return domain.Thread{}, err
			}
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE threads SET status = ?, version = version + 1,
		updated_at = ?, archived_at = ?, deleted_at = ? WHERE id = ? AND version = ?`,
		target, ts(at), archivedAt, deletedAt, current.ID, current.Version)
	if err != nil {
		return domain.Thread{}, err
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		if err != nil {
			return domain.Thread{}, err
		}
		return domain.Thread{}, apperror.New(apperror.CodeConflict,
			"Thread lifecycle changed concurrently")
	}
	if target == domain.ThreadActive {
		// Restoring a Thread reactivates only its current nonterminal Run
		// Session. Historical terminal Run Sessions retain their archived state.
		if current.ActiveRunID != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE sessions SET status = ?, updated_at = ?
				WHERE id = (SELECT session_id FROM thread_runs
					WHERE thread_id = ? AND run_id = ?)`, session.StatusActive,
				ts(at), current.ID, current.ActiveRunID); err != nil {
				return domain.Thread{}, err
			}
		}
	} else if _, err := tx.ExecContext(ctx, `UPDATE sessions SET status = ?, updated_at = ?
		WHERE id IN (SELECT session_id FROM thread_runs WHERE thread_id = ?)`,
		session.StatusArchived, ts(at), current.ID); err != nil {
		return domain.Thread{}, err
	}
	payload, _ := json.Marshal(map[string]any{"status": target, "requested_by": requestedBy})
	if _, err := tx.ExecContext(ctx, `INSERT INTO thread_events
		(thread_id, run_id, type, source, payload_json, created_at)
		VALUES (?, ?, ?, 'thread_lifecycle', ?, ?)`, current.ID,
		nullableString(lifecycleRunID), "thread."+string(action), string(payload),
		ts(at)); err != nil {
		return domain.Thread{}, err
	}
	updated, err := scanThread(tx.QueryRowContext(ctx, threadSelect+` WHERE id = ?`, current.ID))
	if err != nil {
		return domain.Thread{}, err
	}
	return commitResult(updated)
}

// requireThreadLifecycleRunQuiescentTx closes every durable execution surface
// before a Thread lifecycle transition changes its Run or Session projection.
// An execution lease is necessary but not sufficient evidence of activity:
// persistent command-runtime Jobs and user terminal Sessions can outlive a
// Supervisor turn and must independently settle first.
func requireThreadLifecycleRunQuiescentTx(ctx context.Context, tx *sql.Tx,
	runID string, at time.Time,
) error {
	if err := requireNoActiveRunControlLeaseTx(ctx, tx, runID, at); err != nil {
		return err
	}
	if err := requireQuiescentRunPauseTx(ctx, tx, runID); err != nil {
		return err
	}
	var activeCommandJobs int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM command_runtime_jobs
		WHERE run_id = ? AND state IN ('prepared', 'running', 'stopping')`, runID).
		Scan(&activeCommandJobs); err != nil {
		return err
	}
	if activeCommandJobs != 0 {
		return apperror.New(apperror.CodeFailedPrecondition,
			"Thread lifecycle requires command runtime Jobs to stop")
	}
	var activeTerminalSessions int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM terminal_sessions
		WHERE run_id = ? AND state IN ('starting', 'running')`, runID).
		Scan(&activeTerminalSessions); err != nil {
		return err
	}
	if activeTerminalSessions != 0 {
		return apperror.New(apperror.CodeFailedPrecondition,
			"Thread lifecycle requires terminal Sessions to stop")
	}
	return nil
}

func transitionThreadLifecycleRunTx(ctx context.Context, tx *sql.Tx,
	run *domain.Run, target domain.RunStatus, reason string, at time.Time,
) error {
	if run == nil {
		return apperror.New(apperror.CodeFailedPrecondition,
			"Thread lifecycle Run is required")
	}
	expected := run.Status
	if err := run.Transition(target, at); err != nil {
		return apperror.Wrap(apperror.CodeFailedPrecondition, err.Error(), err)
	}
	configJSON, err := marshalRedactedJSON(run.Config)
	if err != nil {
		return err
	}
	budgetJSON, err := marshalRedactedJSON(run.Budget)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE runs SET status = ?, config_json = ?,
		budget_json = ?, started_at = ?, finished_at = ?, updated_at = ?
		WHERE id = ? AND status = ?`, run.Status, configJSON, budgetJSON,
		nullableTS(run.StartedAt), nullableTS(run.FinishedAt), ts(run.UpdatedAt),
		run.ID, expected)
	if err != nil {
		return err
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		if err != nil {
			return err
		}
		return apperror.New(apperror.CodeConflict,
			"Run changed before Thread lifecycle transition")
	}
	if target == domain.RunCancelled {
		if _, err := cancelOperatorSteeringTx(ctx, tx, *run,
			"thread_lifecycle", reason, at); err != nil {
			return err
		}
	}
	statusEvent, err := events.New(run.ID, run.MissionID,
		events.RunStatusChangedEvent, "thread_lifecycle", run.ID, map[string]any{
			"from": expected, "to": target,
			"reason": redact.String(strings.TrimSpace(reason)),
		})
	if err != nil {
		return err
	}
	statusEvent.CreatedAt = at.UTC()
	if _, err := insertRunEventTx(ctx, tx, statusEvent); err != nil {
		return err
	}
	return abortSpecialistDelegationApplicationsTx(ctx, tx, *run, at)
}

func (s *SQLiteStore) ExportThread(ctx context.Context, threadID string) (domain.ThreadExport, error) {
	threadRecord, err := s.GetThread(ctx, threadID)
	if err != nil {
		return domain.ThreadExport{}, err
	}
	mission, err := s.GetMission(ctx, threadRecord.MissionID)
	if err != nil {
		return domain.ThreadExport{}, err
	}
	bindings, err := s.ListThreadRuns(ctx, threadRecord.ID)
	if err != nil {
		return domain.ThreadExport{}, err
	}
	missions := []domain.Mission{}
	seenMissions := map[string]bool{}
	runs := make([]domain.Run, 0, len(bindings))
	sessions := make([]domain.ThreadSession, 0, len(bindings))
	for _, binding := range bindings {
		run, err := s.GetRun(ctx, binding.RunID)
		if err != nil {
			return domain.ThreadExport{}, err
		}
		runs = append(runs, run)
		if !seenMissions[run.MissionID] {
			historical, err := s.GetMission(ctx, run.MissionID)
			if err != nil {
				return domain.ThreadExport{}, err
			}
			missions = append(missions, historical)
			seenMissions[run.MissionID] = true
		}
		linkedSession, err := s.GetSession(ctx, binding.SessionID)
		if err != nil {
			return domain.ThreadExport{}, err
		}
		sessions = append(sessions, domain.ThreadSession{ID: linkedSession.ID,
			WorkspaceID: linkedSession.WorkspaceID, Title: linkedSession.Title,
			Route: linkedSession.Route, Status: linkedSession.Status,
			CreatedAt: linkedSession.CreatedAt, UpdatedAt: linkedSession.UpdatedAt})
	}
	messages, err := s.ListThreadMessagesPage(ctx, threadRecord.ID, true, 0, 0)
	if err != nil {
		return domain.ThreadExport{}, err
	}
	events, err := s.ListThreadEvents(ctx, threadRecord.ID, 0)
	if err != nil {
		return domain.ThreadExport{}, err
	}
	auditEvents, err := s.listThreadRunAuditEvents(ctx, threadRecord.ID)
	if err != nil {
		return domain.ThreadExport{}, err
	}
	return domain.ThreadExport{ProtocolVersion: domain.ThreadExportProtocolVersion,
		ExportedAt: time.Now().UTC(), Thread: threadRecord, Mission: mission, Missions: missions,
		Bindings: bindings, Runs: runs, Sessions: sessions, Messages: messages, Events: events,
		AuditEvents: auditEvents}, nil
}

func scanThread(row scanner) (domain.Thread, error) {
	var item domain.Thread
	var status, created, updated string
	var archived, deleted sql.NullString
	if err := row.Scan(&item.ID, &item.ProtocolVersion, &item.WorkspaceID,
		&item.MissionID, &item.Title, &status, &item.ActiveRunID, &item.LastRunID,
		&item.Version, &created, &updated, &archived, &deleted); err != nil {
		return domain.Thread{}, err
	}
	item.ProjectWorkspaceID = item.WorkspaceID
	item.ProjectAssignmentVersion = 1
	item.Status = domain.ThreadStatus(status)
	item.CreatedAt = parseTS(created)
	item.UpdatedAt = parseTS(updated)
	item.ArchivedAt = parseNullableTS(archived)
	item.DeletedAt = parseNullableTS(deleted)
	return item, item.Validate()
}
