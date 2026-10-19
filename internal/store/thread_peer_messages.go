package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
)

const peerMessageSelect = `SELECT peer.id,peer.source_thread_id,source.title,peer.target_thread_id,target.title,
 message.content,peer.reply_to,
	 CASE WHEN peer.delivered_at IS NOT NULL THEN 'delivered' WHEN peer.error<>'' THEN 'failed' WHEN message.status='cancelled' THEN 'cancelled'
	 ELSE 'queued' END,
 peer.created_at,peer.delivered_at,peer.error,peer.steering_message_id,message.run_id,peer.depth
 FROM thread_peer_messages peer JOIN threads source ON source.id=peer.source_thread_id
 JOIN threads target ON target.id=peer.target_thread_id
 JOIN operator_steering_messages message ON message.id=peer.steering_message_id `

func scanPeerMessage(row operatorSteeringRow) (domain.ThreadPeerMessage, error) {
	var message domain.ThreadPeerMessage
	var created string
	var delivered sql.NullString
	err := row.Scan(&message.ID, &message.SourceThreadID, &message.SourceTitle, &message.TargetThreadID, &message.TargetTitle,
		&message.Content, &message.ReplyTo, &message.Status, &created, &delivered, &message.Error, &message.SteeringMessageID, &message.RunID, &message.Depth)
	if err != nil {
		return message, err
	}
	message.CreatedAt = parseTS(created)
	if delivered.Valid {
		at := parseTS(delivered.String)
		message.DeliveredAt = &at
	}
	return message, nil
}

func peerOperation(sourceID, key string) string {
	return runmutation.Fingerprint("thread_peer_operation.v1", sourceID, key)
}

func peerFingerprint(request domain.SendThreadPeerMessageRequest) string {
	return runmutation.Fingerprint("thread_peer_request.v1", request.SourceThreadID, request.TargetThreadID,
		redact.String(request.Content), request.ReplyTo, request.RequestedBy)
}

func (s *SQLiteStore) FindThreadPeerMessage(ctx context.Context, request domain.SendThreadPeerMessageRequest) (domain.ThreadPeerMessage, bool, error) {
	var fingerprint, id string
	err := s.db.QueryRowContext(ctx, `SELECT request_fingerprint,id FROM thread_peer_messages WHERE operation_key_digest=?`, peerOperation(request.SourceThreadID, request.OperationKey)).Scan(&fingerprint, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ThreadPeerMessage{}, false, nil
	}
	if err != nil {
		return domain.ThreadPeerMessage{}, false, err
	}
	if fingerprint != peerFingerprint(request) {
		return domain.ThreadPeerMessage{}, false, apperror.New(apperror.CodeConflict, "Peer message operation key was used for different content or recipient")
	}
	message, err := s.GetThreadPeerMessage(ctx, id)
	return message, true, err
}

func (s *SQLiteStore) GetThreadPeerMessage(ctx context.Context, id string) (domain.ThreadPeerMessage, error) {
	return scanPeerMessage(s.db.QueryRowContext(ctx, peerMessageSelect+` WHERE peer.id=?`, id))
}

func (s *SQLiteStore) ListThreadPeerMessages(ctx context.Context, threadID string, limit int) ([]domain.ThreadPeerMessage, error) {
	if limit < 1 || limit > 200 {
		return nil, apperror.New(apperror.CodeInvalidArgument, "Peer message limit must be between 1 and 200")
	}
	rows, err := s.db.QueryContext(ctx, peerMessageSelect+` WHERE peer.source_thread_id=? OR peer.target_thread_id=? ORDER BY peer.created_at DESC,peer.id DESC LIMIT ?`, threadID, threadID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.ThreadPeerMessage, 0)
	for rows.Next() {
		message, err := scanPeerMessage(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, message)
	}
	return items, rows.Err()
}

func (s *SQLiteStore) ListThreadPeerTargets(ctx context.Context, sourceID string) (domain.ThreadPeerTargets, error) {
	source, err := s.GetThread(ctx, sourceID)
	if err != nil {
		return domain.ThreadPeerTargets{}, err
	}
	if !source.CanAcceptMessages() {
		return domain.ThreadPeerTargets{}, apperror.New(apperror.CodeFailedPrecondition, "Source chat is not active")
	}
	result := domain.ThreadPeerTargets{Version: domain.ThreadPeerTargetsVersion, SourceThreadID: sourceID, WorkspaceID: source.WorkspaceID, Items: []domain.ThreadPeerTarget{}}
	rows, err := s.db.QueryContext(ctx, `SELECT id,title,workspace_id,status FROM threads WHERE workspace_id=? AND id<>? AND status='active' ORDER BY updated_at DESC,id LIMIT 101`, source.WorkspaceID, sourceID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var target domain.ThreadPeerTarget
		if err := rows.Scan(&target.ID, &target.Title, &target.WorkspaceID, &target.Status); err != nil {
			return result, err
		}
		result.Items = append(result.Items, target)
	}
	if len(result.Items) > 100 {
		result.HasMore = true
		result.Items = result.Items[:100]
	}
	return result, rows.Err()
}

// EnqueueThreadPeerMessage binds the non-authorizing peer record and existing
// steering queue in one transaction; neither can become model-visible alone.
func (s *SQLiteStore) EnqueueThreadPeerMessage(ctx context.Context, request domain.SendThreadPeerMessageRequest, runID string) (domain.ThreadPeerMessageReceipt, error) {
	if message, found, err := s.FindThreadPeerMessage(ctx, request); err != nil || found {
		return domain.ThreadPeerMessageReceipt{Version: domain.ThreadPeerMessageVersion, Message: message, Replayed: found}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ThreadPeerMessageReceipt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE threads SET updated_at=updated_at WHERE id IN (?,?)`, request.SourceThreadID, request.TargetThreadID); err != nil {
		return domain.ThreadPeerMessageReceipt{}, err
	}
	var existingID, existingFingerprint string
	if err := tx.QueryRowContext(ctx, `SELECT id,request_fingerprint FROM thread_peer_messages WHERE operation_key_digest=?`, peerOperation(request.SourceThreadID, request.OperationKey)).Scan(&existingID, &existingFingerprint); err == nil {
		if existingFingerprint != peerFingerprint(request) {
			return domain.ThreadPeerMessageReceipt{}, apperror.New(apperror.CodeConflict, "Peer message operation key was used for different intent")
		}
		message, err := scanPeerMessage(tx.QueryRowContext(ctx, peerMessageSelect+` WHERE peer.id=?`, existingID))
		return domain.ThreadPeerMessageReceipt{Version: domain.ThreadPeerMessageVersion, Message: message, Replayed: true}, err
	} else if !errors.Is(err, sql.ErrNoRows) {
		return domain.ThreadPeerMessageReceipt{}, err
	}
	var sourceRun, targetSession string
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(source.active_run_id,''),source.last_run_id),run.session_id
	 FROM threads source JOIN threads target ON source.workspace_id=target.workspace_id
	 JOIN runs run ON run.id=target.active_run_id
	 WHERE source.id=? AND target.id=? AND source.id<>target.id AND source.status='active' AND target.status='active'
	 AND run.id=?`, request.SourceThreadID, request.TargetThreadID, runID).Scan(&sourceRun, &targetSession)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ThreadPeerMessageReceipt{}, apperror.New(apperror.CodeFailedPrecondition, "Peer chats must be distinct active chats in the same workspace")
	}
	if err != nil {
		return domain.ThreadPeerMessageReceipt{}, err
	}
	depth := 0
	if request.ReplyTo != "" {
		var parentDepth int
		err = tx.QueryRowContext(ctx, `SELECT depth FROM thread_peer_messages WHERE id=? AND source_thread_id=? AND target_thread_id=?`, request.ReplyTo, request.TargetThreadID, request.SourceThreadID).Scan(&parentDepth)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ThreadPeerMessageReceipt{}, apperror.New(apperror.CodeInvalidArgument, "Reply must refer to a message received from this recipient")
		}
		if err != nil {
			return domain.ThreadPeerMessageReceipt{}, err
		}
		depth = parentDepth + 1
		if depth > domain.MaxThreadPeerReplyDepth {
			return domain.ThreadPeerMessageReceipt{}, apperror.New(apperror.CodeResourceExhausted, "Peer reply chain reached its eight-reply limit")
		}
	}
	if sourceRun != request.SourceRunID || !domain.ValidAgentID(request.SourceAttemptID) {
		return domain.ThreadPeerMessageReceipt{}, apperror.New(apperror.CodeConflict, "Model peer source no longer owns the source chat")
	}
	if err := requireRunExecutionLeaseTx(ctx, tx, request.SourceRunID, request.SourceLeaseID, request.SourceLeaseGeneration); err != nil {
		return domain.ThreadPeerMessageReceipt{}, err
	}
	var cp domain.SupervisorCheckpoint
	cp, err = scanSupervisorCheckpoint(tx.QueryRowContext(ctx, supervisorCheckpointSelect, sourceRun))
	if err != nil {
		return domain.ThreadPeerMessageReceipt{}, err
	}
	if cp.AttemptID != request.SourceAttemptID || cp.LeaseID != request.SourceLeaseID || cp.LeaseGeneration != request.SourceLeaseGeneration || cp.Phase != domain.SupervisorTurnStarted {
		return domain.ThreadPeerMessageReceipt{}, apperror.New(apperror.CodeConflict, "Model peer source attempt changed")
	}
	var sourceSession string
	if err := tx.QueryRowContext(ctx, `SELECT session_id FROM runs WHERE id=?`, sourceRun).Scan(&sourceSession); err != nil {
		return domain.ThreadPeerMessageReceipt{}, err
	}
	input, err := supervisorInputMessageTx(ctx, tx, sourceRun, sourceSession, cp.AttemptID, cp.PendingInput)
	if err != nil {
		return domain.ThreadPeerMessageReceipt{}, err
	}
	if input.Provenance.SourceKind == session.SourcePeerMessage {
		var currentPeerID string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM thread_peer_messages WHERE 'thread:'||source_thread_id||'/peer-message:'||id=?`, input.Provenance.SourceRef).Scan(&currentPeerID); err != nil {
			return domain.ThreadPeerMessageReceipt{}, err
		}
		if request.ReplyTo != currentPeerID {
			return domain.ThreadPeerMessageReceipt{}, apperror.New(apperror.CodePolicyDenied, "A peer-triggered turn may send only a reply to its current peer message")
		}
	}
	id := idgen.New("peer-message")
	queued, _, err := enqueueSteeringTx(ctx, tx, domain.EnqueueOperatorSteeringRequest{RunID: runID, SessionID: targetSession, Content: request.Content, OperationKey: "peer-" + peerOperation(request.SourceThreadID, request.OperationKey), RequestedBy: "peer:" + request.SourceThreadID}, false, "peer")
	if err != nil {
		return domain.ThreadPeerMessageReceipt{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO thread_peer_messages(id,source_thread_id,target_thread_id,steering_message_id,source_run_id,operation_key_digest,request_fingerprint,reply_to,depth,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, request.SourceThreadID, request.TargetThreadID, queued.Message.ID, sourceRun, peerOperation(request.SourceThreadID, request.OperationKey), peerFingerprint(request), request.ReplyTo, depth, ts(queued.Message.CreatedAt))
	if err != nil {
		return domain.ThreadPeerMessageReceipt{}, err
	}
	// Both events sort within their own Thread's existing durable Run journal.
	for _, binding := range []struct{ runID, kind string }{{sourceRun, "thread.peer_message.sent"}, {runID, "thread.peer_message.received"}} {
		run, err := scanRun(tx.QueryRowContext(ctx, `SELECT id,mission_id,session_id,status,config_json,budget_json,started_at,finished_at,created_at,updated_at FROM runs WHERE id=?`, binding.runID))
		if err != nil {
			return domain.ThreadPeerMessageReceipt{}, err
		}
		if err := appendSupervisorEventTx(ctx, tx, run, binding.kind, "peer", id, map[string]any{"peer_message_id": id, "steering_message_id": queued.Message.ID}); err != nil {
			return domain.ThreadPeerMessageReceipt{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.ThreadPeerMessageReceipt{}, err
	}
	message, err := s.GetThreadPeerMessage(ctx, id)
	return domain.ThreadPeerMessageReceipt{Version: domain.ThreadPeerMessageVersion, Message: message}, err
}

func (s *SQLiteStore) ListPendingThreadPeerMessages(ctx context.Context) ([]domain.ThreadPeerMessage, error) {
	rows, err := s.db.QueryContext(ctx, peerMessageSelect+` WHERE message.status='pending' AND peer.error=''
	 AND peer.id=(SELECT candidate.id FROM thread_peer_messages candidate JOIN operator_steering_messages input ON input.id=candidate.steering_message_id
	 WHERE candidate.target_thread_id=peer.target_thread_id AND candidate.error='' AND input.status='pending' ORDER BY candidate.created_at,candidate.id LIMIT 1)
	 ORDER BY peer.created_at,peer.id LIMIT 64`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []domain.ThreadPeerMessage{}
	for rows.Next() {
		message, err := scanPeerMessage(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, message)
	}
	return items, rows.Err()
}

func (s *SQLiteStore) FailThreadPeerMessage(ctx context.Context, id, reason string) error {
	message, err := s.GetThreadPeerMessage(ctx, id)
	if err != nil {
		return err
	}
	if message.DeliveredAt != nil {
		return nil
	}
	input, err := s.GetOperatorSteering(ctx, message.SteeringMessageID)
	if err != nil {
		return err
	}
	if input.Status == domain.OperatorSteeringPending {
		_, err = s.cancelOperatorSteering(ctx, domain.CancelOperatorSteeringRequest{MessageID: input.ID, OperationKey: "peer-failure-" + id, RequestedBy: "peer_delivery", Reason: "Peer delivery could not start"}, true)
		if err != nil {
			if apperror.CodeOf(apperror.Normalize(err)) == apperror.CodeFailedPrecondition {
				return nil
			}
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, `UPDATE thread_peer_messages SET error=? WHERE id=? AND error='' AND delivered_at IS NULL
	 AND EXISTS(SELECT 1 FROM operator_steering_messages input WHERE input.id=thread_peer_messages.steering_message_id AND input.status='cancelled')`, redact.String(reason), id)
	return err
}

func peerSourceRef(sourceID, id string) string { return "thread:" + sourceID + "/peer-message:" + id }

func isThreadPeerSteering(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (bool, error) {
	if exists, err := storeTableExists(ctx, q, "thread_peer_messages"); err != nil || !exists {
		return false, err
	}
	var count int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM thread_peer_messages WHERE steering_message_id=?`, id).Scan(&count)
	return count == 1, err
}

func supervisorInputMessageTx(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, runID, sessionID, attemptID, input string) (session.Message, error) {
	if exists, err := storeTableExists(ctx, q, "thread_peer_messages"); err != nil {
		return session.Message{}, err
	} else if !exists {
		return session.NewMessage(sessionID, "user", input), nil
	}
	if tx, ok := q.(*sql.Tx); ok {
		cp, err := scanSupervisorCheckpoint(tx.QueryRowContext(ctx, supervisorCheckpointSelect, runID))
		if err == nil && cp.AttemptID == attemptID {
			continuation, found, err := approvalContinuationForCheckpointTx(ctx, tx, cp)
			if err != nil {
				return session.Message{}, err
			}
			if found {
				return scanSessionMessage(tx.QueryRowContext(ctx, `SELECT id,session_id,role,content,provenance_version,source_kind,source_ref,content_sha256,instruction_authorized,token_estimate,compacted,created_at FROM session_messages WHERE id=?`, continuation.UserMessageID))
			}
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return session.Message{}, err
		}
	}
	var sourceID, id string
	err := q.QueryRowContext(ctx, `SELECT peer.source_thread_id,peer.id FROM thread_peer_messages peer JOIN operator_steering_deliveries delivery ON delivery.message_id=peer.steering_message_id WHERE delivery.run_id=? AND delivery.attempt_id=? AND delivery.status='prepared'`, runID, attemptID).Scan(&sourceID, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return session.NewMessage(sessionID, "user", input), nil
	}
	if err != nil {
		return session.Message{}, err
	}
	return session.NewEvidenceMessage(sessionID, session.SourcePeerMessage, peerSourceRef(sourceID, id), input), nil
}

func (s *SQLiteStore) PreparedSupervisorInputMessage(ctx context.Context, checkpoint domain.SupervisorCheckpoint) (session.Message, error) {
	run, err := s.GetRun(ctx, checkpoint.RunID)
	if err != nil {
		return session.Message{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return session.Message{}, err
	}
	defer func() { _ = tx.Rollback() }()
	return supervisorInputMessageTx(ctx, tx, run.ID, run.SessionID, checkpoint.AttemptID, checkpoint.PendingInput)
}

// Exact lookup also serves old transcript pages without a truncated history scan.
func (s *SQLiteStore) ThreadPeerMessageForSteering(ctx context.Context, id string) (domain.ThreadPeerMessage, bool, error) {
	if exists, err := storeTableExists(ctx, s.db, "thread_peer_messages"); err != nil || !exists {
		return domain.ThreadPeerMessage{}, false, err
	}
	message, err := scanPeerMessage(s.db.QueryRowContext(ctx, peerMessageSelect+` WHERE peer.steering_message_id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return message, false, nil
	}
	return message, err == nil, err
}

func (s *SQLiteStore) PendingPeerQueueOwnsInput(ctx context.Context, runID, messageID string) (bool, error) {
	if exists, err := storeTableExists(ctx, s.db, "thread_peer_messages"); err != nil || !exists {
		return false, err
	}
	var pending, peers, own int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COUNT(peer.id),COALESCE(SUM(message.id=? AND peer.id IS NOT NULL),0)
	 FROM operator_steering_messages message LEFT JOIN thread_peer_messages peer ON peer.steering_message_id=message.id
	 WHERE message.run_id=? AND message.status='pending'`, messageID, runID).Scan(&pending, &peers, &own)
	return pending == peers && own == 1, err
}

func validQueuedInputProvenanceTx(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, messageID string, message session.Message) bool {
	if exists, err := storeTableExists(ctx, q, "thread_peer_messages"); err != nil {
		return false
	} else if !exists {
		return message.Role == "user" && message.Provenance.SourceKind == session.SourceOperatorMessage && message.Provenance.InstructionAuthorized
	}
	var sourceID, id string
	err := q.QueryRowContext(ctx, `SELECT source_thread_id,id FROM thread_peer_messages WHERE steering_message_id=?`, messageID).Scan(&sourceID, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return message.Role == "user" && message.Provenance.SourceKind == session.SourceOperatorMessage && message.Provenance.InstructionAuthorized
	}
	return err == nil && message.Role == "tool" && message.Provenance.SourceKind == session.SourcePeerMessage && !message.Provenance.InstructionAuthorized && message.Provenance.SourceRef == peerSourceRef(sourceID, id)
}

func markPeerInputDeliveredTx(ctx context.Context, tx *sql.Tx, checkpoint domain.SupervisorCheckpoint) error {
	if exists, err := storeTableExists(ctx, tx, "thread_peer_messages"); err != nil || !exists {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE thread_peer_messages SET delivered_at=? WHERE delivered_at IS NULL AND steering_message_id IN (SELECT message_id FROM operator_steering_deliveries WHERE run_id=? AND attempt_id=? AND status='prepared')`, ts(time.Now().UTC()), checkpoint.RunID, checkpoint.AttemptID)
	return err
}
