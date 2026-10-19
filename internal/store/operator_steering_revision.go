package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
)

const operatorSteeringRevisionSelect = `SELECT id,message_id,run_id,session_id,
	from_revision,to_revision,old_content_sha256,new_content_sha256,requested_by,
	created_at,operation_key_digest,request_fingerprint FROM operator_steering_revisions`

func (s *SQLiteStore) ReviseOperatorSteering(ctx context.Context,
	request domain.ReviseOperatorSteeringRequest,
) (domain.ReviseOperatorSteeringResult, error) {
	requestContentDigest := domain.OperatorSteeringContentSHA256(request.Content)
	normalized, err := request.Normalize()
	if err != nil {
		return domain.ReviseOperatorSteeringResult{}, apperror.Wrap(apperror.CodeInvalidArgument, err.Error(), err)
	}
	if redact.String(normalized.RequestedBy) != normalized.RequestedBy {
		return domain.ReviseOperatorSteeringResult{}, apperror.New(apperror.CodeInvalidArgument,
			"operator steering revision requester cannot contain sensitive material")
	}
	normalized.Content = redact.String(normalized.Content)
	if strings.TrimSpace(normalized.Content) == "" {
		normalized.Content = ""
	} else {
		normalized.Content, err = domain.NormalizeOperatorSteeringContent(normalized.Content)
		if err != nil {
			return domain.ReviseOperatorSteeringResult{}, apperror.Wrap(apperror.CodeInvalidArgument, err.Error(), err)
		}
	}
	newDigest := domain.OperatorSteeringContentSHA256(normalized.Content)
	keyDigest := runmutation.Fingerprint("operator_steering_revision_operation.v1",
		normalized.SessionID, normalized.MessageID, normalized.OperationKey)
	fingerprint := runmutation.Fingerprint("operator_steering_revision_request.v1",
		normalized.SessionID, normalized.MessageID, fmt.Sprint(normalized.ExpectedRevision),
		newDigest, normalized.RequestedBy)

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.ReviseOperatorSteeringResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var runID string
	if err := tx.QueryRowContext(ctx, `SELECT run_id FROM operator_steering_messages
		WHERE id=? AND session_id=?`, normalized.MessageID, normalized.SessionID).Scan(&runID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ReviseOperatorSteeringResult{}, apperror.New(apperror.CodeNotFound,
				"operator steering message was not found")
		}
		return domain.ReviseOperatorSteeringResult{}, err
	}
	if err := lockRunForOperatorSteeringControlTx(ctx, tx, runID); err != nil {
		return domain.ReviseOperatorSteeringResult{}, err
	}
	if receipt, found, err := getOperatorSteeringRevisionByKeyTx(ctx, tx, keyDigest); err != nil {
		return domain.ReviseOperatorSteeringResult{}, err
	} else if found {
		if receipt.RequestFingerprint != fingerprint || receipt.MessageID != normalized.MessageID ||
			receipt.SessionID != normalized.SessionID || receipt.RequestedBy != normalized.RequestedBy {
			return domain.ReviseOperatorSteeringResult{}, apperror.New(apperror.CodeConflict,
				"operator steering revision operation key was already used for different intent")
		}
		message, err := getOperatorSteeringMessageTx(ctx, tx, receipt.MessageID)
		if err != nil {
			return domain.ReviseOperatorSteeringResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return domain.ReviseOperatorSteeringResult{}, err
		}
		return domain.ReviseOperatorSteeringResult{Message: message, Receipt: receipt, Replayed: true}, nil
	}
	message, err := getOperatorSteeringMessageTx(ctx, tx, normalized.MessageID)
	if err != nil {
		return domain.ReviseOperatorSteeringResult{}, err
	}
	peer, err := isThreadPeerSteering(ctx, tx, message.ID)
	if err != nil {
		return domain.ReviseOperatorSteeringResult{}, err
	}
	if peer {
		return domain.ReviseOperatorSteeringResult{}, apperror.New(apperror.CodeFailedPrecondition, "Peer message content cannot be revised by the recipient")
	}
	if message.SessionID != normalized.SessionID || message.RunID != runID {
		return domain.ReviseOperatorSteeringResult{}, apperror.New(apperror.CodeConflict,
			"operator steering revision binding changed")
	}
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id, status,
		config_json, budget_json, started_at, finished_at, created_at, updated_at
		FROM runs WHERE id=? AND session_id=?`, runID, normalized.SessionID))
	if err != nil {
		return domain.ReviseOperatorSteeringResult{}, err
	}
	if run.Status != domain.RunRunning && run.Status != domain.RunPaused {
		return domain.ReviseOperatorSteeringResult{}, apperror.New(apperror.CodeFailedPrecondition,
			fmt.Sprintf("run %s cannot revise operator steering while %s", runID, run.Status))
	}
	if message.Status != domain.OperatorSteeringPending || message.Prepared {
		return domain.ReviseOperatorSteeringResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"only unprepared pending operator steering can be revised")
	}
	if message.Revision != normalized.ExpectedRevision {
		return domain.ReviseOperatorSteeringResult{}, apperror.New(apperror.CodeConflict,
			"operator steering revision changed")
	}
	if normalized.Content == "" && message.ImageCount+message.AttachmentCount == 0 {
		return domain.ReviseOperatorSteeringResult{}, apperror.New(apperror.CodeInvalidArgument,
			"operator steering content cannot be empty without an attachment")
	}
	if newDigest == message.ContentSHA256 {
		proof := &domain.OperatorSteeringRevisionUnchangedError{
			RunID: message.RunID, SessionID: message.SessionID, MessageID: message.ID,
			ExpectedRevision:     normalized.ExpectedRevision,
			OperationKeySHA256:   domain.OperatorSteeringContentSHA256(normalized.OperationKey),
			RequestContentSHA256: requestContentDigest, NormalizedContentSHA256: newDigest,
			CurrentContentSHA256: message.ContentSHA256,
		}
		return domain.ReviseOperatorSteeringResult{}, apperror.Wrap(apperror.CodeInvalidArgument,
			proof.Error(), proof)
	}
	var pendingBytes int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(CAST(content AS BLOB))),0)
		FROM operator_steering_messages WHERE run_id=? AND status='pending'`, runID).Scan(&pendingBytes); err != nil {
		return domain.ReviseOperatorSteeringResult{}, err
	}
	if pendingBytes-len([]byte(message.Content))+len([]byte(normalized.Content)) > domain.MaxPendingOperatorSteeringBytes {
		return domain.ReviseOperatorSteeringResult{}, apperror.New(apperror.CodeResourceExhausted,
			"operator steering queue reached its pending byte limit")
	}
	now := time.Now().UTC()
	receipt := domain.OperatorSteeringRevisionReceipt{
		ID: idgen.New("steer-revision"), MessageID: message.ID, RunID: runID,
		SessionID: normalized.SessionID, FromRevision: message.Revision,
		ToRevision: message.Revision + 1, OldContentSHA256: message.ContentSHA256,
		NewContentSHA256: newDigest, RequestedBy: normalized.RequestedBy, CreatedAt: now,
		OperationKeyDigest: keyDigest, RequestFingerprint: fingerprint,
	}
	if err := receipt.Validate(); err != nil {
		return domain.ReviseOperatorSteeringResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO operator_steering_revisions
		(id,message_id,run_id,session_id,from_revision,to_revision,old_content_sha256,
		 new_content,new_content_sha256,requested_by,created_at,operation_key_digest,request_fingerprint)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, receipt.ID, receipt.MessageID, receipt.RunID,
		receipt.SessionID, receipt.FromRevision, receipt.ToRevision, receipt.OldContentSHA256,
		normalized.Content, receipt.NewContentSHA256, receipt.RequestedBy, ts(receipt.CreatedAt),
		receipt.OperationKeyDigest, receipt.RequestFingerprint); err != nil {
		return domain.ReviseOperatorSteeringResult{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE operator_steering_messages
		SET content=?,content_sha256=?,revision=?,edited_at=?
		WHERE id=? AND session_id=? AND status='pending' AND revision=?
		AND NOT EXISTS(SELECT 1 FROM operator_steering_deliveries delivery
			WHERE delivery.message_id=operator_steering_messages.id AND delivery.status='prepared')
		AND NOT EXISTS(SELECT 1 FROM operator_steering_midturn_claims claim
			WHERE claim.message_id=operator_steering_messages.id)`,
		normalized.Content, newDigest, receipt.ToRevision, ts(now), message.ID,
		normalized.SessionID, receipt.FromRevision)
	if err != nil {
		return domain.ReviseOperatorSteeringResult{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		if err != nil {
			return domain.ReviseOperatorSteeringResult{}, err
		}
		return domain.ReviseOperatorSteeringResult{}, apperror.New(apperror.CodeConflict,
			"operator steering changed before revision")
	}
	message.Content, message.ContentSHA256 = normalized.Content, newDigest
	message.Revision, message.EditedAt = receipt.ToRevision, &now
	if err := appendSupervisorEventTx(ctx, tx, run,
		"operator.steering_revised", "operator", receipt.ID, map[string]any{
			"message_id": message.ID, "sequence": message.Sequence,
			"from_revision": receipt.FromRevision, "to_revision": receipt.ToRevision,
			"old_content_sha256": receipt.OldContentSHA256,
			"new_content_sha256": receipt.NewContentSHA256,
		}); err != nil {
		return domain.ReviseOperatorSteeringResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ReviseOperatorSteeringResult{}, err
	}
	return domain.ReviseOperatorSteeringResult{Message: message, Receipt: receipt}, nil
}

func (s *SQLiteStore) InspectOperatorSteeringRevision(ctx context.Context, sessionID,
	messageID, operationKey, requestedBy string,
) (domain.OperatorSteeringRevisionInspection, error) {
	sessionID, messageID, requestedBy = strings.TrimSpace(sessionID), strings.TrimSpace(messageID), strings.TrimSpace(requestedBy)
	for _, value := range []string{sessionID, messageID, requestedBy} {
		if !domain.ValidAgentID(value) {
			return domain.OperatorSteeringRevisionInspection{}, apperror.New(apperror.CodeInvalidArgument,
				"operator steering revision observation identity is invalid")
		}
	}
	key, err := domain.NormalizeAgentOperationKey(operationKey)
	if err != nil {
		return domain.OperatorSteeringRevisionInspection{}, apperror.New(apperror.CodeInvalidArgument,
			"operator steering revision observation key is invalid")
	}
	keyDigest := runmutation.Fingerprint("operator_steering_revision_operation.v1", sessionID, messageID, key)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.OperatorSteeringRevisionInspection{}, err
	}
	defer func() { _ = tx.Rollback() }()
	receipt, found, err := getOperatorSteeringRevisionRow(tx.QueryRowContext(ctx,
		operatorSteeringRevisionSelect+` WHERE operation_key_digest=?`, keyDigest))
	if err != nil {
		return domain.OperatorSteeringRevisionInspection{}, err
	}
	if found {
		if receipt.SessionID != sessionID || receipt.MessageID != messageID || receipt.RequestedBy != requestedBy ||
			receipt.OperationKeyDigest != keyDigest {
			return domain.OperatorSteeringRevisionInspection{}, apperror.New(apperror.CodeConflict,
				"operator steering revision receipt belongs to different intent")
		}
		if err := tx.Commit(); err != nil {
			return domain.OperatorSteeringRevisionInspection{}, err
		}
		return domain.OperatorSteeringRevisionInspection{State: domain.OperatorSteeringRevisionSealed,
			Receipt: &receipt}, nil
	}
	message, err := getOperatorSteeringObservationMessage(ctx, tx, sessionID, messageID)
	if err != nil {
		return domain.OperatorSteeringRevisionInspection{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.OperatorSteeringRevisionInspection{}, err
	}
	return domain.OperatorSteeringRevisionInspection{State: domain.OperatorSteeringRevisionAbsent,
		Message: &message}, nil
}

func (s *SQLiteStore) InspectOperatorSteeringCancellation(ctx context.Context, sessionID,
	messageID, operationKey, requestedBy string,
) (domain.OperatorSteeringCancellationInspection, error) {
	sessionID, messageID, requestedBy = strings.TrimSpace(sessionID), strings.TrimSpace(messageID), strings.TrimSpace(requestedBy)
	for _, value := range []string{sessionID, messageID, requestedBy} {
		if !domain.ValidAgentID(value) {
			return domain.OperatorSteeringCancellationInspection{}, apperror.New(apperror.CodeInvalidArgument,
				"operator steering cancellation observation identity is invalid")
		}
	}
	key, err := domain.NormalizeAgentOperationKey(operationKey)
	if err != nil {
		return domain.OperatorSteeringCancellationInspection{}, apperror.New(apperror.CodeInvalidArgument,
			"operator steering cancellation observation key is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.OperatorSteeringCancellationInspection{}, err
	}
	defer func() { _ = tx.Rollback() }()
	receipt, found, err := getOperatorSteeringCancellationReceiptRow(tx.QueryRowContext(ctx, `SELECT
		cancellation.id,cancellation.message_id,cancellation.run_id,message.session_id,
		cancellation.kind,cancellation.requested_by,cancellation.reason_sha256,cancellation.created_at,
		operation.operation_key_digest,operation.request_fingerprint
		FROM operator_steering_cancellation_operations operation
		JOIN operator_steering_cancellations cancellation ON cancellation.id=operation.cancellation_id
		JOIN operator_steering_messages message ON message.id=cancellation.message_id
		WHERE operation.message_id=? AND cancellation.message_id=? AND message.session_id=?`,
		messageID, messageID, sessionID))
	if err != nil {
		return domain.OperatorSteeringCancellationInspection{}, err
	}
	if found {
		keyDigest := runmutation.Fingerprint("operator_steering_cancellation_operation.v1",
			receipt.RunID, key)
		expectedFingerprint := runmutation.Fingerprint("operator_steering_cancellation_request.v1",
			receipt.RunID, receipt.MessageID, requestedBy, receipt.ReasonSHA256)
		if receipt.OperationKeyDigest == keyDigest &&
			(receipt.SessionID != sessionID || receipt.MessageID != messageID ||
				receipt.RequestedBy != requestedBy || receipt.RequestFingerprint != expectedFingerprint) {
			return domain.OperatorSteeringCancellationInspection{}, apperror.New(apperror.CodeConflict,
				"operator steering cancellation receipt belongs to different intent")
		}
		if receipt.OperationKeyDigest == keyDigest {
			if err := tx.Commit(); err != nil {
				return domain.OperatorSteeringCancellationInspection{}, err
			}
			return domain.OperatorSteeringCancellationInspection{State: domain.OperatorSteeringCancellationSealed,
				Receipt: &receipt}, nil
		}
	}
	message, err := getOperatorSteeringObservationMessage(ctx, tx, sessionID, messageID)
	if err != nil {
		return domain.OperatorSteeringCancellationInspection{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.OperatorSteeringCancellationInspection{}, err
	}
	return domain.OperatorSteeringCancellationInspection{State: domain.OperatorSteeringCancellationAbsent,
		Message: &message}, nil
}

func getOperatorSteeringObservationMessage(ctx context.Context, reader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, sessionID, messageID string) (domain.OperatorSteeringObservationMessage, error) {
	var message domain.OperatorSteeringObservationMessage
	err := reader.QueryRowContext(ctx, `SELECT id,run_id,session_id,revision,status
		FROM operator_steering_messages WHERE id=? AND session_id=?`, messageID, sessionID).
		Scan(&message.ID, &message.RunID, &message.SessionID, &message.Revision, &message.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return message, apperror.New(apperror.CodeNotFound, "operator steering message was not found")
	}
	if err != nil {
		return message, err
	}
	if err := message.Validate(); err != nil {
		return message, err
	}
	return message, nil
}

func getOperatorSteeringCancellationReceiptRow(row operatorSteeringRow) (
	domain.OperatorSteeringCancellationReceipt, bool, error,
) {
	var receipt domain.OperatorSteeringCancellationReceipt
	var createdAt string
	err := row.Scan(&receipt.CancellationID, &receipt.MessageID, &receipt.RunID,
		&receipt.SessionID, &receipt.Kind, &receipt.RequestedBy, &receipt.ReasonSHA256,
		&createdAt, &receipt.OperationKeyDigest, &receipt.RequestFingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return receipt, false, nil
	}
	if err != nil {
		return receipt, false, err
	}
	receipt.CreatedAt = parseTS(createdAt)
	if err := receipt.Validate(); err != nil {
		return receipt, false, err
	}
	return receipt, true, nil
}

func getOperatorSteeringRevisionByKeyTx(ctx context.Context, tx *sql.Tx, key string) (domain.OperatorSteeringRevisionReceipt, bool, error) {
	return getOperatorSteeringRevisionRow(tx.QueryRowContext(ctx,
		operatorSteeringRevisionSelect+` WHERE operation_key_digest=?`, key))
}

func getOperatorSteeringRevisionRow(row operatorSteeringRow) (domain.OperatorSteeringRevisionReceipt, bool, error) {
	var receipt domain.OperatorSteeringRevisionReceipt
	var createdAt string
	err := row.Scan(&receipt.ID, &receipt.MessageID, &receipt.RunID, &receipt.SessionID,
		&receipt.FromRevision, &receipt.ToRevision, &receipt.OldContentSHA256,
		&receipt.NewContentSHA256, &receipt.RequestedBy, &createdAt,
		&receipt.OperationKeyDigest, &receipt.RequestFingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return receipt, false, nil
	}
	if err != nil {
		return receipt, false, err
	}
	receipt.CreatedAt = parseTS(createdAt)
	if err := receipt.Validate(); err != nil {
		return receipt, false, err
	}
	return receipt, true, nil
}

func (s *SQLiteStore) ListThreadQueuedMessages(ctx context.Context, threadID string) (domain.ThreadQueuedMessagesSnapshot, error) {
	threadID = strings.TrimSpace(threadID)
	snapshot := domain.ThreadQueuedMessagesSnapshot{ProtocolVersion: domain.ThreadQueuedMessagesProtocolVersion,
		ThreadID: threadID, Messages: []domain.QueuedOperatorSteeringMessage{}}
	if !domain.ValidAgentID(threadID) {
		return snapshot, apperror.New(apperror.CodeInvalidArgument, "Thread id is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return snapshot, err
	}
	defer func() { _ = tx.Rollback() }()
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(t.active_run_id,''),COALESCE(r.session_id,''),COALESCE(r.status,'')
		FROM threads t LEFT JOIN runs r ON r.id=t.active_run_id WHERE t.id=?`, threadID).
		Scan(&snapshot.RunID, &snapshot.SessionID, &snapshot.RunStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, apperror.New(apperror.CodeNotFound, "Thread was not found")
	}
	if err != nil || snapshot.RunID == "" {
		if err != nil {
			return snapshot, err
		}
		return snapshot, tx.Commit()
	}
	if snapshot.RunStatus == domain.RunRunning {
		err := tx.QueryRowContext(ctx, `SELECT checkpoint.attempt_id FROM run_supervisor_checkpoints checkpoint
			WHERE checkpoint.run_id=? AND checkpoint.phase='turn_started' AND EXISTS
			(SELECT 1 FROM run_execution_leases lease WHERE lease.run_id=checkpoint.run_id
			AND lease.status='active' AND julianday(lease.expires_at)>julianday(?))`, snapshot.RunID, ts(time.Now().UTC())).Scan(&snapshot.CurrentAttemptID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return snapshot, err
		}
	}
	rows, err := tx.QueryContext(ctx, operatorSteeringSelect+` WHERE message.run_id=?
		AND message.status='pending' ORDER BY message.sequence`, snapshot.RunID)
	if err != nil {
		return snapshot, err
	}
	for rows.Next() {
		message, err := getOperatorSteeringMessageRow(rows)
		if err != nil {
			rows.Close()
			return snapshot, err
		}
		snapshot.Messages = append(snapshot.Messages, domain.QueuedOperatorSteeringMessage{Message: message,
			Images: []domain.WorkspaceImage{}, Attachments: []domain.WorkspaceFileAttachment{}})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return snapshot, err
	}
	rows.Close()
	for i := range snapshot.Messages {
		entry := &snapshot.Messages[i]
		entry.Images, err = listOperatorMessageImagesTx(ctx, tx, entry.Message.RunID, entry.Message.ID)
		if err == nil {
			entry.Attachments, err = listOperatorMessageAttachmentsTx(ctx, tx, entry.Message.RunID, entry.Message.ID)
		}
		if err != nil {
			return snapshot, err
		}
		if len(entry.Images) != entry.Message.ImageCount || len(entry.Attachments) != entry.Message.AttachmentCount {
			return snapshot, apperror.New(apperror.CodeConflict, "queued message attachment binding is incomplete")
		}
	}
	return snapshot, tx.Commit()
}
