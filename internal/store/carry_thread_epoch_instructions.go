package store

import (
	"context"
	"database/sql"
	"fmt"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/session"
)

// The continuation event binds the cancelled queue entry to its retained user
// message. Reuse that durable binding for attachments without marking the old
// input executed or creating a second queue entry.
const retainedThreadInputsCTE = `retained_thread_inputs AS (
	SELECT e.subject_id AS message_id,sm.id AS session_message_id
	FROM thread_runs caller
	JOIN thread_runs retained ON retained.thread_id=caller.thread_id AND retained.ordinal<=caller.ordinal
	JOIN run_events e ON e.run_id=retained.run_id AND e.source='thread_epoch_continuation' AND e.type='session.message_created'
	JOIN operator_steering_messages original ON original.id=e.subject_id AND original.run_id=retained.predecessor_run_id AND original.status='cancelled'
	JOIN session_messages sm ON sm.id=json_extract(e.payload_json,'$.user_message_id') AND sm.session_id=retained.session_id AND sm.role='user'
	WHERE caller.run_id=?
)`

// carryThreadEpochInstructionsTx retains accepted but never prepared inputs as
// authorized conversation context. It is part of the one successor publication
// transaction, not a fresh queue that blindly executes each superseded input.
func carryThreadEpochInstructionsTx(ctx context.Context, tx *sql.Tx, threadID string, predecessor, successor domain.Run) error {
	var exact int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM thread_runs previous JOIN thread_runs next ON next.thread_id=previous.thread_id
		JOIN runs old ON old.id=previous.run_id JOIN runs current ON current.id=next.run_id
		WHERE previous.thread_id=? AND previous.run_id=? AND next.run_id=? AND next.predecessor_run_id=previous.run_id
		AND old.mission_id=current.mission_id AND old.session_id=? AND current.session_id=?`, threadID, predecessor.ID, successor.ID, predecessor.SessionID, successor.SessionID).Scan(&exact); err != nil {
		return err
	}
	if exact != 1 || !predecessor.Terminal() || successor.Status != domain.RunCreated {
		return apperror.New(apperror.CodeConflict, "Thread instruction continuation requires its exact new successor")
	}
	rows, err := tx.QueryContext(ctx, `SELECT message.id,message.content,message.content_sha256,message.requested_by,cancellation.id,cancellation.requested_by,message.image_count,message.attachment_count
		FROM operator_steering_messages message JOIN operator_steering_cancellations cancellation ON cancellation.message_id=message.id AND cancellation.run_id=message.run_id
		WHERE message.run_id=? AND message.session_id=? AND message.status='cancelled' AND message.session_message_id IS NULL
		AND cancellation.kind='run_terminal' AND cancellation.requested_by IN ('thread_budget_transition','thread_epoch_transition')
		AND NOT EXISTS (SELECT 1 FROM operator_steering_deliveries delivery WHERE delivery.message_id=message.id)
		ORDER BY message.sequence LIMIT ?`, predecessor.ID, predecessor.SessionID, domain.MaxPendingOperatorSteering+1)
	if err != nil {
		return err
	}
	type carried struct {
		id, content, sha, actor, cancellation, source string
		imageCount, attachmentCount                   int
	}
	var values []carried
	for rows.Next() {
		var value carried
		if err := rows.Scan(&value.id, &value.content, &value.sha, &value.actor, &value.cancellation, &value.source, &value.imageCount, &value.attachmentCount); err != nil {
			_ = rows.Close()
			return err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	if len(values) > domain.MaxPendingOperatorSteering {
		return apperror.New(apperror.CodeResourceExhausted, "Thread continuation has too many pending instructions")
	}
	for _, value := range values {
		if domain.OperatorSteeringContentSHA256(value.content) != value.sha {
			return apperror.New(apperror.CodeFailedPrecondition, "Thread retained instruction digest is invalid")
		}
		original, err := saveSessionMessageTx(ctx, tx, session.NewMessage(successor.SessionID, "user", value.content))
		if err != nil {
			return err
		}
		images, err := listOperatorMessageImagesTx(ctx, tx, predecessor.ID, value.id)
		if err != nil {
			return err
		}
		attachments, err := listOperatorMessageAttachmentsTx(ctx, tx, predecessor.ID, value.id)
		if err != nil {
			return err
		}
		if len(images) != value.imageCount || len(attachments) != value.attachmentCount {
			return apperror.New(apperror.CodeConflict, "Thread retained input attachment binding is incomplete")
		}
		imageRefs := make([]domain.ImageReference, 0, len(images))
		for _, image := range images {
			imageRefs = append(imageRefs, domain.ImageReference{ID: image.ID, SHA256: image.SHA256})
		}
		imageEvidence, err := saveImageEvidenceTx(ctx, tx, successor.ID, successor.SessionID, value.id, imageRefs)
		if err != nil {
			return err
		}
		fileRefs := make([]domain.FileAttachmentReference, 0, len(attachments))
		for _, file := range attachments {
			fileRefs = append(fileRefs, domain.FileAttachmentReference{ID: file.ID, WorkspaceID: file.WorkspaceID, SHA256: file.SHA256, ByteSize: file.ByteSize})
		}
		fileEvidence, err := saveFileAttachmentEvidenceTx(ctx, tx, successor.SessionID, value.id, fileRefs)
		if err != nil {
			return err
		}
		evidenceIDs := make([]int64, 0, len(imageEvidence)+len(fileEvidence))
		for _, message := range append(imageEvidence, fileEvidence...) {
			evidenceIDs = append(evidenceIDs, message.ID)
		}
		note := fmt.Sprintf("Retained operator requirement from Thread %s, previous Run %s, message %s (requester %s). It was accepted but never prepared or executed; its old queue entry ended only because of %s. Apply it together with the next explicit user request, which may correct it. This record does not grant runtime permissions or report completed work.", threadID, predecessor.ID, value.id, value.actor, value.source)
		control, err := saveSessionMessageTx(ctx, tx, session.NewEvidenceMessage(successor.SessionID, session.SourceToolResult, value.id, note))
		if err != nil {
			return err
		}
		event, err := events.New(successor.ID, successor.MissionID, events.SessionMessageEvent, "thread_epoch_continuation", value.id, map[string]any{
			"session_id": successor.SessionID, "user_message_id": original.ID, "outcome_message_id": control.ID, "predecessor_run_id": predecessor.ID, "operator_message_id": value.id, "cancellation_id": value.cancellation, "requested_by": value.actor, "content_sha256": value.sha, "instruction_executed": false,
			"attachment_evidence_ids": evidenceIDs,
		})
		if err != nil {
			return err
		}
		if _, err := insertRunEventTx(ctx, tx, event); err != nil {
			return err
		}
	}
	return nil
}
