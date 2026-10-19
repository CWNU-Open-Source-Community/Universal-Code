package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
)

type branchAnchor struct {
	runID              string
	ordinal, sequence  int64
	eventType, payload string
}

func readBranchAnchor(ctx context.Context, q historyQueryer, request domain.ThreadBranchRequest) (branchAnchor, error) {
	var anchor branchAnchor
	err := q.QueryRowContext(ctx, `SELECT e.run_id,b.ordinal,e.sequence,e.type,e.payload_json
		FROM run_events e JOIN thread_runs b ON b.run_id=e.run_id JOIN threads t ON t.id=b.thread_id
		WHERE t.id=? AND t.status!='deleted' AND e.event_id=?`, request.ThreadID, request.SourceEventID).
		Scan(&anchor.runID, &anchor.ordinal, &anchor.sequence, &anchor.eventType, &anchor.payload)
	if errors.Is(err, sql.ErrNoRows) {
		return anchor, apperror.New(apperror.CodeNotFound, "The selected reply is unavailable in this Thread")
	}
	if err != nil {
		return anchor, err
	}
	var payload struct {
		Role, Content, Text string
		SourceKind          string `json:"source_kind"`
	}
	if json.Unmarshal([]byte(anchor.payload), &payload) != nil {
		return anchor, apperror.New(apperror.CodeFailedPrecondition, "The selected reply has invalid stored content")
	}
	if (anchor.eventType == events.SessionMessageEvent && payload.Role == "assistant" && payload.SourceKind == session.SourceModelResponse && strings.TrimSpace(payload.Content) != "") ||
		(anchor.eventType == events.ModelPublicCommentaryEvent && strings.TrimSpace(payload.Text) != "") {
		return anchor, nil
	}
	return anchor, apperror.New(apperror.CodeInvalidArgument, "Branching requires a durable assistant reply")
}

func (s *SQLiteStore) GetThreadBranchSource(ctx context.Context, request domain.ThreadBranchRequest) (domain.ThreadBranchSource, error) {
	a, err := readBranchAnchor(ctx, s.db, request)
	return domain.ThreadBranchSource{RunID: a.runID}, err
}

func branchOperationIdentity(r domain.ThreadBranchRequest) (string, string) {
	return runmutation.Fingerprint("thread_branch_operation", r.ThreadID, r.OperationKey),
		runmutation.Fingerprint(domain.ThreadBranchProtocolVersion, r.ThreadID, r.SourceEventID, r.RequestedBy)
}

func readBranchOperation(ctx context.Context, q historyQueryer, r domain.ThreadBranchRequest) (domain.ThreadBranchRecord, bool, error) {
	key, fingerprint := branchOperationIdentity(r)
	var raw string
	err := q.QueryRowContext(ctx, `SELECT payload_json FROM thread_events WHERE thread_id=?
		AND type='thread.branched' AND source='thread_branch'
		AND json_extract(payload_json,'$.operation_key_digest')=?`, r.ThreadID, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ThreadBranchRecord{}, false, nil
	}
	if err != nil {
		return domain.ThreadBranchRecord{}, false, err
	}
	var stored struct {
		Record      domain.ThreadBranchRecord `json:"record"`
		Fingerprint string                    `json:"request_fingerprint"`
	}
	if json.Unmarshal([]byte(raw), &stored) != nil || stored.Record.ThreadID == "" || stored.Fingerprint != fingerprint {
		return domain.ThreadBranchRecord{}, false, apperror.New(apperror.CodeConflict, "This branch key was already used for a different reply")
	}
	stored.Record.Replayed = true
	return stored.Record, true, nil
}

func (s *SQLiteStore) GetThreadBranchOperation(ctx context.Context, r domain.ThreadBranchRequest) (domain.ThreadBranchRecord, bool, error) {
	return readBranchOperation(ctx, s.db, r)
}

// The existing write transaction serializes replay lookup, creation and the
// immutable branch receipt, including projectless conversations.
func (s *SQLiteStore) CreateThreadBranch(ctx context.Context, request domain.ThreadBranchRequest,
	mission domain.Mission, run domain.Run, mode domain.RunModeSnapshot, sess session.Session, initial []events.Event,
) (domain.ThreadBranchRecord, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.ThreadBranchRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if stored, found, err := readBranchOperation(ctx, tx, request); err != nil {
		return domain.ThreadBranchRecord{}, err
	} else if found {
		return stored, tx.Commit()
	}
	anchor, err := readBranchAnchor(ctx, tx, request)
	if err != nil {
		return domain.ThreadBranchRecord{}, err
	}
	var workspace string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(m.workspace_id,'') FROM runs r JOIN missions m ON m.id=r.mission_id WHERE r.id=?`, anchor.runID).Scan(&workspace); err != nil {
		return domain.ThreadBranchRecord{}, err
	}
	if mission.WorkspaceID != workspace || sess.WorkspaceID != workspace || run.Config.ContinuityContextFingerprint != "" || len(run.Config.ContinuityContext) > 0 {
		return domain.ThreadBranchRecord{}, apperror.New(apperror.CodeInvalidArgument, "Branch creation must retain its Workspace and use fresh context")
	}
	if err := createMissionRunTx(ctx, tx, mission, run, mode, sess, true, initial); err != nil {
		return domain.ThreadBranchRecord{}, err
	}
	if err := copyBranchMessagesTx(ctx, tx, request, anchor, run); err != nil {
		return domain.ThreadBranchRecord{}, err
	}
	record := domain.ThreadBranchRecord{ThreadID: domain.InitialThreadID(run.ID), RunID: run.ID, SessionID: sess.ID, SourceEventID: request.SourceEventID}
	key, fingerprint := branchOperationIdentity(request)
	payload, _ := json.Marshal(map[string]any{"version": domain.ThreadBranchProtocolVersion, "operation_key_digest": key,
		"request_fingerprint": fingerprint, "source_event_id": request.SourceEventID, "record": record})
	if _, err := tx.ExecContext(ctx, `INSERT INTO thread_events(thread_id,run_id,type,source,payload_json,created_at)
		VALUES(?,?,'thread.branched','thread_branch',?,?)`, request.ThreadID, anchor.runID, string(payload), ts(time.Now().UTC())); err != nil {
		return domain.ThreadBranchRecord{}, err
	}
	return record, tx.Commit()
}

type branchMessage struct {
	message    session.Message
	steeringID string
	eventID    string
	attemptID  string
}

func copyBranchMessagesTx(ctx context.Context, tx *sql.Tx, request domain.ThreadBranchRequest, anchor branchAnchor, run domain.Run) error {
	// Order committed operator input at its queue event, as the transcript does.
	// A live commentary may precede Session commit; include only its delivered
	// input, never unrelated queued messages waiting for a subsequent turn.
	rows, err := tx.QueryContext(ctx, `SELECT m.id,m.session_id,m.role,m.content,m.provenance_version,m.source_kind,m.source_ref,
		m.content_sha256,m.instruction_authorized,m.token_estimate,m.compacted,m.created_at,COALESCE(o.id,''),b.ordinal,
		COALESCE(queued.sequence,e.sequence) AS position,e.event_id,''
		FROM thread_runs b JOIN session_messages m ON m.session_id=b.session_id
		JOIN run_events e ON e.run_id=b.run_id AND e.type='session.message_created' AND e.subject_id=CAST(m.id AS TEXT)
		LEFT JOIN operator_steering_messages o ON o.session_message_id=m.id AND o.run_id=b.run_id
		LEFT JOIN run_events queued ON queued.run_id=b.run_id AND queued.type='operator.steering_queued' AND queued.subject_id=o.id
		WHERE b.thread_id=? AND m.role!='system'
		AND NOT EXISTS(SELECT 1 FROM operator_message_attachment_evidence attachment WHERE attachment.session_message_id=m.id)
		AND (b.ordinal<? OR (b.ordinal=? AND e.sequence<=?))
		UNION ALL
		SELECT 0,b.session_id,'assistant',json_extract(e.payload_json,'$.text'),'context_provenance.v1','model_response','',
		'',0,0,0,e.created_at,'',b.ordinal,e.sequence,e.event_id,COALESCE(json_extract(e.payload_json,'$.attempt_id'),'')
		FROM thread_runs b JOIN run_events e ON e.run_id=b.run_id
		WHERE b.thread_id=? AND e.type='model.public_commentary' AND json_type(e.payload_json,'$.context_message_id') IS NULL
		AND (b.ordinal<? OR (b.ordinal=? AND e.sequence<=?))
		ORDER BY 14,15`, request.ThreadID, anchor.ordinal, anchor.ordinal, anchor.sequence, request.ThreadID, anchor.ordinal, anchor.ordinal, anchor.sequence)
	if err != nil {
		return err
	}
	var messages []branchMessage
	for rows.Next() {
		var item branchMessage
		var authorized, compacted int
		var created string
		var ordinal, sequence int64
		if err := rows.Scan(&item.message.ID, &item.message.SessionID, &item.message.Role, &item.message.Content, &item.message.Provenance.Version,
			&item.message.Provenance.SourceKind, &item.message.Provenance.SourceRef, &item.message.Provenance.ContentSHA256, &authorized,
			&item.message.TokenEstimate, &compacted, &created, &item.steeringID, &ordinal, &sequence, &item.eventID, &item.attemptID); err != nil {
			rows.Close()
			return err
		}
		item.message.Provenance.InstructionAuthorized = authorized != 0
		item.message.CreatedAt = parseTS(created)
		messages = append(messages, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if anchor.eventType == events.ModelPublicCommentaryEvent {
		var payload struct {
			AttemptID string `json:"attempt_id"`
		}
		_ = json.Unmarshal([]byte(anchor.payload), &payload)
		var steeringID, content string
		err := tx.QueryRowContext(ctx, `SELECT o.id,o.content FROM operator_steering_deliveries d JOIN operator_steering_messages o ON o.id=d.message_id
			WHERE d.run_id=? AND d.attempt_id=? AND d.status IN ('prepared','committed')
			AND NOT EXISTS(SELECT 1 FROM run_events e WHERE e.run_id=o.run_id AND e.type='session.message_created'
			AND e.subject_id=CAST(o.session_message_id AS TEXT) AND e.sequence<=?)`, anchor.runID, payload.AttemptID, anchor.sequence).Scan(&steeringID, &content)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if errors.Is(err, sql.ErrNoRows) && payload.AttemptID != "" {
			// Non-queued Supervisor inputs are committed with the completed
			// turn. Its immutable attempt binding also reconstructs the input
			// when branching from an earlier public update after that commit.
			err = tx.QueryRowContext(ctx, `SELECT '',m.content FROM run_events completed JOIN session_messages m
				ON m.id=json_extract(completed.payload_json,'$.user_message_id') JOIN runs r ON r.id=completed.run_id AND r.session_id=m.session_id
				WHERE completed.run_id=? AND completed.type='agent.turn_completed' AND completed.source='run_supervisor'
				AND completed.subject_id=? AND m.role='user' AND m.source_kind='operator_message'
				AND NOT EXISTS(SELECT 1 FROM run_events recorded WHERE recorded.run_id=completed.run_id AND recorded.type='session.message_created'
				AND recorded.subject_id=CAST(m.id AS TEXT) AND recorded.sequence<=?)`, anchor.runID, payload.AttemptID, anchor.sequence).Scan(&steeringID, &content)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if err == nil {
			// The current input belongs before this attempt's commentary. Prior
			// committed messages retain their original order.
			at := len(messages)
			for index, item := range messages {
				if item.attemptID == payload.AttemptID {
					at = index
					break
				}
			}
			item := branchMessage{message: session.NewMessage(run.SessionID, "user", content), steeringID: steeringID}
			messages = append(messages, branchMessage{})
			copy(messages[at+1:], messages[at:])
			messages[at] = item
		}
	}
	var lastAssistant string
	for _, item := range messages {
		originalID := item.message.ID
		if strings.TrimSpace(item.message.Content) == "" && item.steeringID == "" {
			continue
		}
		historicalAssistant := item.message.Role == "assistant" || (item.message.Role == "tool" && strings.HasPrefix(item.message.Provenance.SourceRef, "branch-assistant:"))
		if historicalAssistant {
			if item.message.Content == lastAssistant {
				continue
			}
			lastAssistant = item.message.Content
		} else if item.message.Role == "user" {
			lastAssistant = ""
		}
		item.message.ID = 0
		item.message.SessionID = run.SessionID
		item.message.Compacted = false
		item.message.CreatedAt = time.Now().UTC()
		if historicalAssistant {
			// Historical model output remains evidence. Providers requiring
			// native private replay can continue without inventing that replay
			// or copying hidden reasoning into the new conversation.
			item.message = session.NewEvidenceMessage(run.SessionID, session.SourceToolResult, "branch-assistant:"+item.eventID, item.message.Content)
		}
		// Migrated unclassified evidence stays untrusted when re-sealed.
		if item.message.Provenance.Version == session.LegacyContextProvenanceVersion {
			item.message = session.NewEvidenceMessage(run.SessionID, session.SourceToolResult, "branch-message:"+strconv.FormatInt(originalID, 10), item.message.Content)
		}
		attachmentInput, err := prepareBranchAttachmentsTx(ctx, tx, item.steeringID, item.message, run)
		if err != nil {
			return err
		}
		saved, err := saveSessionMessageTx(ctx, tx, item.message)
		if err != nil {
			return err
		}
		if historicalAssistant {
			event, err := events.New(run.ID, run.MissionID, events.ModelPublicCommentaryEvent, "thread_branch", idgen.New("branch-reply"), map[string]any{
				"text": saved.Content, "context_message_id": saved.ID, "source_event_id": item.eventID, "instruction_authorized": false,
			})
			if err != nil {
				return err
			}
			if _, err := insertRunEventTx(ctx, tx, event); err != nil {
				return err
			}
		}
		if attachmentInput.ID != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE operator_steering_messages SET status='committed',session_message_id=?,committed_at=? WHERE id=?`, saved.ID, ts(time.Now().UTC()), attachmentInput.ID); err != nil {
				return err
			}
			if err := commitOperatorMessageAttachmentEvidenceTx(ctx, tx, attachmentInput); err != nil {
				return err
			}
		}
	}
	return nil
}

func prepareBranchAttachmentsTx(ctx context.Context, tx *sql.Tx, originalID string, message session.Message, run domain.Run) (domain.OperatorSteeringMessage, error) {
	empty := domain.OperatorSteeringMessage{}
	if originalID == "" {
		return empty, nil
	}
	var images, files int
	if err := tx.QueryRowContext(ctx, `SELECT image_count,attachment_count FROM operator_steering_messages WHERE id=?`, originalID).Scan(&images, &files); err != nil {
		return empty, err
	}
	if images == 0 && files == 0 {
		return empty, nil
	}
	id := idgen.New("steering")
	now := ts(time.Now().UTC())
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM operator_steering_messages WHERE run_id=?`, run.ID).Scan(&sequence); err != nil {
		return empty, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO operator_steering_messages(id,run_id,session_id,sequence,status,content,content_sha256,requested_by,created_at,image_count,attachment_count)
		VALUES(?,?,?,?,'pending',?,?, 'thread_branch',?,?,?)`, id, run.ID, run.SessionID, sequence, message.Content, message.Provenance.ContentSHA256, now, images, files); err != nil {
		return empty, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO thread_message_images(message_id,ordinal,image_id) SELECT ?,ordinal,image_id FROM thread_message_images WHERE message_id=?`, id, originalID); err != nil {
		return empty, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO thread_message_attachments(message_id,ordinal,attachment_id) SELECT ?,ordinal,attachment_id FROM thread_message_attachments WHERE message_id=?`, id, originalID); err != nil {
		return empty, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.OperatorSteeringQueuedEvent, "thread_branch", id, map[string]any{"message_id": id, "sequence": sequence, "image_count": images, "attachment_count": files}); err != nil {
		return empty, err
	}
	return domain.OperatorSteeringMessage{ID: id, RunID: run.ID, SessionID: run.SessionID, ImageCount: images, AttachmentCount: files}, nil
}
