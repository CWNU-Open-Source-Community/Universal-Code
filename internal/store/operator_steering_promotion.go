package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
)

const promotionSelect = `SELECT id,message_id,replacement_message_id,run_id,session_id,
	expected_revision,content_sha256,target_attempt_id,execution_id,cancellation_id,requested_by,
	created_at,operation_key_digest,request_fingerprint FROM operator_steering_promotions`

const promotionRejectionSelect = `SELECT id,message_id,run_id,session_id,expected_revision,content_sha256,
	target_attempt_id,execution_id,requested_by,created_at,operation_key_digest,request_fingerprint FROM operator_steering_promotion_rejections`

func promotionKey(sessionID, messageID, key string) string {
	return runmutation.Fingerprint("operator_steering_promotion_operation.v1", sessionID, messageID, key)
}

// The application must first fence the in-process execution owner. This store
// transaction fences revision, active Run/attempt, claim and tool dispatch.
func (s *SQLiteStore) PromoteOperatorSteering(ctx context.Context, request domain.PromoteOperatorSteeringRequest) (domain.PromoteOperatorSteeringResult, error) {
	result, err := s.promoteOrRejectOperatorSteering(ctx, request, false)
	// A definite admission refusal is sealed after the attempted transaction
	// rolled back. A competing success wins if it committed before this fence.
	if err != nil && ctx.Err() == nil && (apperror.CodeOf(err) == apperror.CodeFailedPrecondition || apperror.CodeOf(err) == apperror.CodeConflict) {
		return s.promoteOrRejectOperatorSteering(ctx, request, true)
	}
	return result, err
}
func (s *SQLiteStore) RejectOperatorSteeringPromotion(ctx context.Context, request domain.PromoteOperatorSteeringRequest) (domain.PromoteOperatorSteeringResult, error) {
	return s.promoteOrRejectOperatorSteering(ctx, request, true)
}
func (s *SQLiteStore) promoteOrRejectOperatorSteering(ctx context.Context, request domain.PromoteOperatorSteeringRequest, reject bool) (domain.PromoteOperatorSteeringResult, error) {
	r, err := request.Normalize()
	if err != nil {
		return domain.PromoteOperatorSteeringResult{}, apperror.Wrap(apperror.CodeInvalidArgument, err.Error(), err)
	}
	if redact.String(r.RequestedBy) != r.RequestedBy {
		return domain.PromoteOperatorSteeringResult{}, apperror.New(apperror.CodeInvalidArgument, "promotion requester is invalid")
	}
	key := promotionKey(r.SessionID, r.MessageID, r.OperationKey)
	fingerprint := runmutation.Fingerprint("operator_steering_promotion_request.v1", r.SessionID, r.MessageID,
		fmt.Sprint(r.ExpectedRevision), r.ExpectedContentSHA256, r.ExpectedAttemptID, r.ExpectedExecutionID, r.RequestedBy)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.PromoteOperatorSteeringResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var runID string
	if err := tx.QueryRowContext(ctx, `SELECT run_id FROM operator_steering_messages WHERE id=? AND session_id=?`, r.MessageID, r.SessionID).Scan(&runID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = apperror.New(apperror.CodeNotFound, "queued message was not found")
		}
		return domain.PromoteOperatorSteeringResult{}, err
	}
	if err := lockRunForOperatorSteeringControlTx(ctx, tx, runID); err != nil {
		return domain.PromoteOperatorSteeringResult{}, err
	}
	if receipt, found, err := scanPromotion(tx.QueryRowContext(ctx, promotionSelect+` WHERE operation_key_digest=?`, key)); err != nil {
		return domain.PromoteOperatorSteeringResult{}, err
	} else if found {
		if receipt.RequestFingerprint != fingerprint || receipt.RequestedBy != r.RequestedBy {
			return domain.PromoteOperatorSteeringResult{}, apperror.New(apperror.CodeConflict, "promotion key was used for different intent")
		}
		return domain.PromoteOperatorSteeringResult{Receipt: receipt, Replayed: true}, tx.Commit()
	}
	if receipt, found, err := scanPromotionRejection(tx.QueryRowContext(ctx, promotionRejectionSelect+` WHERE operation_key_digest=?`, key)); err != nil {
		return domain.PromoteOperatorSteeringResult{}, err
	} else if found {
		if receipt.RequestFingerprint != fingerprint || receipt.RequestedBy != r.RequestedBy {
			return domain.PromoteOperatorSteeringResult{}, apperror.New(apperror.CodeConflict, "promotion key was used for different intent")
		}
		return domain.PromoteOperatorSteeringResult{Rejection: &receipt, Replayed: true}, tx.Commit()
	}
	if reject {
		receipt := domain.OperatorSteeringPromotionRejection{ID: idgen.New("steer-promotion-rejected"), MessageID: r.MessageID, RunID: runID, SessionID: r.SessionID,
			ExpectedRevision: r.ExpectedRevision, ContentSHA256: r.ExpectedContentSHA256, TargetAttemptID: r.ExpectedAttemptID, ExecutionID: r.ExpectedExecutionID,
			RequestedBy: r.RequestedBy, CreatedAt: time.Now().UTC(), OperationKeyDigest: key, RequestFingerprint: fingerprint}
		if err := receipt.Validate(); err != nil {
			return domain.PromoteOperatorSteeringResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO operator_steering_promotion_rejections
			(id,message_id,run_id,session_id,expected_revision,content_sha256,target_attempt_id,execution_id,requested_by,created_at,operation_key_digest,request_fingerprint)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, receipt.ID, receipt.MessageID, receipt.RunID, receipt.SessionID, receipt.ExpectedRevision, receipt.ContentSHA256, receipt.TargetAttemptID,
			receipt.ExecutionID, receipt.RequestedBy, ts(receipt.CreatedAt), receipt.OperationKeyDigest, receipt.RequestFingerprint); err != nil {
			return domain.PromoteOperatorSteeringResult{}, err
		}
		return domain.PromoteOperatorSteeringResult{Rejection: &receipt}, tx.Commit()
	}
	message, err := getOperatorSteeringMessageTx(ctx, tx, r.MessageID)
	if err != nil {
		return domain.PromoteOperatorSteeringResult{}, err
	}
	peer, err := isThreadPeerSteering(ctx, tx, message.ID)
	if err != nil {
		return domain.PromoteOperatorSteeringResult{}, err
	}
	if peer {
		return domain.PromoteOperatorSteeringResult{}, apperror.New(apperror.CodeFailedPrecondition, "Peer messages cannot become operator corrections")
	}
	if message.SessionID != r.SessionID || message.RunID != runID || message.Revision != r.ExpectedRevision || message.ContentSHA256 != r.ExpectedContentSHA256 {
		return domain.PromoteOperatorSteeringResult{}, apperror.New(apperror.CodeConflict, "queued message changed before promotion")
	}
	if message.Status != domain.OperatorSteeringPending || message.Prepared || message.DeliveryMode != domain.OperatorSteeringNextTurn {
		return domain.PromoteOperatorSteeringResult{}, apperror.New(apperror.CodeFailedPrecondition, "only unprepared next-turn messages can be guided")
	}
	if message.ImageCount != 0 || message.AttachmentCount != 0 {
		return domain.PromoteOperatorSteeringResult{}, apperror.New(apperror.CodeFailedPrecondition, "当前任务引导只支持文字，原消息和附件仍保留在队列中。")
	}
	var eligible int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM threads thread
		JOIN thread_runs binding ON binding.thread_id=thread.id AND binding.run_id=thread.active_run_id
		JOIN runs run ON run.id=binding.run_id JOIN sessions session ON session.id=run.session_id
		JOIN run_supervisor_checkpoints checkpoint ON checkpoint.run_id=run.id
		WHERE thread.active_run_id=? AND thread.status='active' AND binding.session_id=? AND run.session_id=?
		AND session.status='active' AND run.status='running' AND checkpoint.phase='turn_started'
		AND checkpoint.attempt_id=?`, runID, r.SessionID, r.SessionID, r.ExpectedAttemptID).Scan(&eligible); err != nil {
		return domain.PromoteOperatorSteeringResult{}, err
	}
	if eligible != 1 {
		return domain.PromoteOperatorSteeringResult{}, apperror.New(apperror.CodeFailedPrecondition, "当前执行已变化，原消息仍保留在队列中。")
	}
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT id,mission_id,session_id,status,config_json,budget_json,started_at,finished_at,created_at,updated_at FROM runs WHERE id=?`, runID))
	if err != nil {
		return domain.PromoteOperatorSteeringResult{}, err
	}
	now := time.Now().UTC()
	reason := "已转为当前任务纠正"
	cancellationID := idgen.New("steer-cancel")
	if _, err := tx.ExecContext(ctx, `INSERT INTO operator_steering_cancellations
		(id,message_id,run_id,kind,requested_by,reason,reason_sha256,created_at) VALUES(?,?,?,'operator',?,?,?,?)`,
		cancellationID, message.ID, runID, r.RequestedBy, reason, domain.OperatorSteeringContentSHA256(reason), ts(now)); err != nil {
		return domain.PromoteOperatorSteeringResult{}, err
	}
	cancelKey := runmutation.Fingerprint("operator_steering_cancellation_operation.v1", runID,
		runmutation.Fingerprint("operator_steering_promotion_cancel.v1", key))
	cancelFingerprint := runmutation.Fingerprint("operator_steering_cancellation_request.v1", runID,
		message.ID, r.RequestedBy, domain.OperatorSteeringContentSHA256(reason))
	if _, err := tx.ExecContext(ctx, `INSERT INTO operator_steering_cancellation_operations
		(operation_key_digest,request_fingerprint,cancellation_id,message_id,run_id,requested_by,created_at)
		VALUES(?,?,?,?,?,?,?)`, cancelKey, cancelFingerprint, cancellationID, message.ID, runID, r.RequestedBy, ts(now)); err != nil {
		return domain.PromoteOperatorSteeringResult{}, err
	}
	changed, err := tx.ExecContext(ctx, `UPDATE operator_steering_messages SET status='cancelled',cancelled_at=?
		WHERE id=? AND status='pending' AND revision=? AND delivery_mode='next_turn'
		AND NOT EXISTS(SELECT 1 FROM operator_steering_deliveries d WHERE d.message_id=operator_steering_messages.id AND d.status='prepared')
		AND NOT EXISTS(SELECT 1 FROM operator_steering_midturn_claims c WHERE c.message_id=operator_steering_messages.id)`, ts(now), message.ID, r.ExpectedRevision)
	if err != nil {
		return domain.PromoteOperatorSteeringResult{}, err
	}
	if count, err := changed.RowsAffected(); err != nil || count != 1 {
		return domain.PromoteOperatorSteeringResult{}, apperror.New(apperror.CodeConflict, "queued message was claimed before promotion")
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.OperatorSteeringCancelledEvent, "operator", cancellationID,
		map[string]any{"message_id": message.ID, "sequence": message.Sequence, "kind": "operator", "status": "cancelled", "reason": reason}); err != nil {
		return domain.PromoteOperatorSteeringResult{}, err
	}
	// Release the old slot before enqueue so a full queue can convert in place.
	// Any later error rolls back the cancellation and the replacement together.
	replacement, _, err := enqueueOperatorSteeringTx(ctx, tx, domain.EnqueueOperatorSteeringRequest{
		RunID: runID, SessionID: r.SessionID, Content: message.Content, RequestedBy: r.RequestedBy,
		DeliveryMode: domain.OperatorSteeringCurrentTurn,
		OperationKey: runmutation.Fingerprint("operator_steering_promotion_enqueue.v1", key)}, false)
	if err != nil {
		return domain.PromoteOperatorSteeringResult{}, err
	}
	if replacement.Replayed || replacement.Message.TargetAttemptID != r.ExpectedAttemptID {
		return domain.PromoteOperatorSteeringResult{}, apperror.New(apperror.CodeConflict, "promotion replacement binding differs")
	}
	receipt := domain.OperatorSteeringPromotionReceipt{ID: idgen.New("steer-promotion"), MessageID: message.ID,
		ReplacementMessageID: replacement.Message.ID, RunID: runID, SessionID: r.SessionID, ExpectedRevision: r.ExpectedRevision,
		ContentSHA256: message.ContentSHA256, TargetAttemptID: r.ExpectedAttemptID, ExecutionID: r.ExpectedExecutionID,
		CancellationID: cancellationID, RequestedBy: r.RequestedBy, CreatedAt: now, OperationKeyDigest: key, RequestFingerprint: fingerprint}
	if err := receipt.Validate(); err != nil {
		return domain.PromoteOperatorSteeringResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO operator_steering_promotions
		(id,message_id,replacement_message_id,run_id,session_id,expected_revision,content_sha256,target_attempt_id,
		execution_id,cancellation_id,requested_by,created_at,operation_key_digest,request_fingerprint)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, receipt.ID, receipt.MessageID, receipt.ReplacementMessageID, receipt.RunID, receipt.SessionID,
		receipt.ExpectedRevision, receipt.ContentSHA256, receipt.TargetAttemptID, receipt.ExecutionID, receipt.CancellationID, receipt.RequestedBy,
		ts(receipt.CreatedAt), receipt.OperationKeyDigest, receipt.RequestFingerprint); err != nil {
		return domain.PromoteOperatorSteeringResult{}, err
	}
	return domain.PromoteOperatorSteeringResult{Receipt: receipt}, tx.Commit()
}

func scanPromotion(row operatorSteeringRow) (domain.OperatorSteeringPromotionReceipt, bool, error) {
	var r domain.OperatorSteeringPromotionReceipt
	var created string
	err := row.Scan(&r.ID, &r.MessageID, &r.ReplacementMessageID, &r.RunID, &r.SessionID, &r.ExpectedRevision,
		&r.ContentSHA256, &r.TargetAttemptID, &r.ExecutionID, &r.CancellationID, &r.RequestedBy, &created, &r.OperationKeyDigest, &r.RequestFingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	r.CreatedAt = parseTS(created)
	return r, true, r.Validate()
}

func scanPromotionRejection(row operatorSteeringRow) (domain.OperatorSteeringPromotionRejection, bool, error) {
	var r domain.OperatorSteeringPromotionRejection
	var created string
	err := row.Scan(&r.ID, &r.MessageID, &r.RunID, &r.SessionID, &r.ExpectedRevision, &r.ContentSHA256, &r.TargetAttemptID, &r.ExecutionID, &r.RequestedBy, &created, &r.OperationKeyDigest, &r.RequestFingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	r.CreatedAt = parseTS(created)
	if err := r.Validate(); err != nil {
		return r, false, err
	}
	return r, true, nil
}

func (s *SQLiteStore) InspectOperatorSteeringPromotion(ctx context.Context, sessionID, messageID, key, actor string) (domain.OperatorSteeringPromotionInspection, error) {
	for _, value := range []string{sessionID, messageID, actor} {
		if !domain.ValidAgentID(value) {
			return domain.OperatorSteeringPromotionInspection{}, apperror.New(apperror.CodeInvalidArgument, "promotion observation binding is invalid")
		}
	}
	normalizedKey, err := domain.NormalizeAgentOperationKey(key)
	if err != nil {
		return domain.OperatorSteeringPromotionInspection{}, apperror.Wrap(apperror.CodeInvalidArgument, err.Error(), err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.OperatorSteeringPromotionInspection{}, err
	}
	defer func() { _ = tx.Rollback() }()
	r, found, err := scanPromotion(tx.QueryRowContext(ctx, promotionSelect+` WHERE operation_key_digest=?`, promotionKey(sessionID, messageID, normalizedKey)))
	if err != nil {
		return domain.OperatorSteeringPromotionInspection{}, err
	}
	if found {
		if r.SessionID != sessionID || r.MessageID != messageID || r.RequestedBy != actor {
			return domain.OperatorSteeringPromotionInspection{}, apperror.New(apperror.CodeConflict, "promotion observation actor or source differs")
		}
		return domain.OperatorSteeringPromotionInspection{State: domain.OperatorSteeringRevisionSealed, Receipt: &r}, tx.Commit()
	}
	if rejected, found, err := scanPromotionRejection(tx.QueryRowContext(ctx, promotionRejectionSelect+` WHERE operation_key_digest=?`, promotionKey(sessionID, messageID, normalizedKey))); err != nil {
		return domain.OperatorSteeringPromotionInspection{}, err
	} else if found {
		if rejected.SessionID != sessionID || rejected.MessageID != messageID || rejected.RequestedBy != actor {
			return domain.OperatorSteeringPromotionInspection{}, apperror.New(apperror.CodeConflict, "promotion rejection belongs to another actor or source")
		}
		return domain.OperatorSteeringPromotionInspection{State: "rejected", Rejection: &rejected}, tx.Commit()
	}
	message, err := getOperatorSteeringObservationMessage(ctx, tx, sessionID, messageID)
	if err != nil {
		return domain.OperatorSteeringPromotionInspection{}, err
	}
	return domain.OperatorSteeringPromotionInspection{State: domain.OperatorSteeringRevisionAbsent, Message: &message}, tx.Commit()
}
