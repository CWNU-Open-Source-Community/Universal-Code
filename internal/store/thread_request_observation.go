package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/runmutation"
)

// The store's normal DSN uses BEGIN IMMEDIATE, and sqlite3 ignores TxOptions
// ReadOnly. Use a dedicated connection and explicit deferred transaction here:
// this observation must also work under SQLite query_only and never acquire a
// writer reservation. Discard a connection if rollback cannot restore it.
func (s *SQLiteStore) beginThreadRequestObservation(ctx context.Context) (*sql.Conn, func(), error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	if _, err = conn.ExecContext(ctx, "BEGIN DEFERRED"); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return conn, func() {
		if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = conn.Close()
	}, nil
}

func validateThreadObservationKey(identity, key, requester string) error {
	normalized, err := domain.NormalizeAgentOperationKey(key)
	if !domain.ValidAgentID(identity) || !domain.ValidAgentID(requester) || err != nil || normalized != key {
		return apperror.New(apperror.CodeInvalidArgument, "Thread request observation identity is invalid")
	}
	return nil
}

// InspectThreadCreationRequest never calls Create/loadResult: their admission
// checks and original-created-state assertions are inappropriate for observation
// of a request whose Run may have executed, changed mode, or gained successors.
func (s *SQLiteStore) InspectThreadCreationRequest(ctx context.Context, workspaceID, key, requester string) (domain.ThreadRequestObservation, error) {
	value := domain.ThreadRequestObservation{Kind: "creation", State: "not_received", WorkspaceID: workspaceID}
	if err := validateThreadObservationKey(workspaceID, key, requester); err != nil {
		return value, err
	}
	tx, finish, err := s.beginThreadRequestObservation(ctx)
	if err != nil {
		return value, err
	}
	defer finish()
	operation, found, err := getRunCreationOperation(ctx, tx, runmutation.RunCreationOperationDigest(key))
	if err != nil || !found {
		return value, err
	}
	if operation.WorkspaceID != workspaceID || operation.RequestedBy != requester {
		return value, apperror.New(apperror.CodeConflict, "Thread creation request belongs to a different workspace or requester")
	}
	var threadID string
	err = tx.QueryRowContext(ctx, `SELECT thread.id FROM threads thread
		JOIN thread_runs binding ON binding.thread_id=thread.id
		JOIN runs run ON run.id=binding.run_id
		JOIN sessions session ON session.id=binding.session_id
		JOIN missions mission ON mission.id=run.mission_id
		WHERE binding.run_id=? AND binding.session_id=? AND run.session_id=binding.session_id
		AND binding.ordinal=1 AND mission.id=?
		AND mission.workspace_id=? AND session.workspace_id=mission.workspace_id`, operation.RunID, operation.SessionID,
		operation.MissionID, workspaceID).Scan(&threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return value, apperror.New(apperror.CodeConflict, "Thread creation request binding is inconsistent")
	}
	if err != nil {
		return value, err
	}
	value.State, value.Settled = "completed", true
	value.ThreadID, value.RunID, value.SessionID = threadID, operation.RunID, operation.SessionID
	value.RequestFingerprint = operation.RequestFingerprint
	return value, nil
}

// InspectThreadTurnRequest observes the intent and its original message in one
// read transaction. It must not reserve/adopt an intent, enqueue, repair a
// failure, acquire a lease, or resume work. Even a dead process's reserved intent
// remains received/unsettled until an explicit operator action handles it.
func (s *SQLiteStore) InspectThreadTurnRequest(ctx context.Context, threadID, key, requester string) (domain.ThreadRequestObservation, error) {
	value := domain.ThreadRequestObservation{Kind: "turn", State: "not_received", ThreadID: threadID}
	if err := validateThreadObservationKey(threadID, key, requester); err != nil {
		return value, err
	}
	tx, finish, err := s.beginThreadRequestObservation(ctx)
	if err != nil {
		return value, err
	}
	defer finish()
	thread, err := scanThread(tx.QueryRowContext(ctx, threadSelect+` WHERE id=?`, threadID))
	if err != nil {
		return value, err
	}
	value.WorkspaceID = thread.WorkspaceID
	keyDigest := runmutation.Fingerprint("thread_message_intent_operation.v1", threadID, key)
	var intent domain.ThreadMessageIntent
	var storedThreadID, filesJSON, imagesJSON, attachmentsJSON string
	err = tx.QueryRowContext(ctx, `SELECT thread_id,operation_key_digest,request_fingerprint,
		COALESCE(run_id,''),COALESCE(message_id,''),rejected,files_json,images_json,attachments_json FROM thread_message_intents
		WHERE operation_key_digest=?`, keyDigest).Scan(&storedThreadID, &intent.OperationKeyDigest,
		&intent.RequestFingerprint, &intent.RunID, &intent.MessageID, &intent.Rejected, &filesJSON, &imagesJSON, &attachmentsJSON)
	legacy := errors.Is(err, sql.ErrNoRows)
	if err != nil && !legacy {
		return value, err
	}
	if legacy {
		// Older plain-text requests predate intents. Read their exact Run-scoped
		// operation without the mutating adoption used by Reserve.
		rows, err := tx.QueryContext(ctx, `SELECT run_id FROM thread_runs WHERE thread_id=? ORDER BY ordinal DESC`, threadID)
		if err != nil {
			return value, err
		}
		var runIDs []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
			runIDs = append(runIDs, id)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return value, err
		}
		for _, runID := range runIDs {
			var messageID string
			err := tx.QueryRowContext(ctx, `SELECT message_id FROM operator_steering_operations WHERE operation_key_digest=?`, runmutation.Fingerprint("operator_steering_operation.v1", runID, key)).Scan(&messageID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return value, err
			}
			if err == nil {
				intent.RunID, intent.MessageID = runID, messageID
				break
			}
		}
		if intent.MessageID == "" {
			return value, nil
		}
	} else {
		if storedThreadID != threadID || !validStoreDigest(intent.RequestFingerprint) {
			return value, apperror.New(apperror.CodeConflict, "Thread request intent binding is inconsistent")
		}
		value.RequestFingerprint = intent.RequestFingerprint
	}
	value.State = "received"
	if intent.Rejected {
		value.State, value.Settled = "rejected", true
		return value, nil
	}
	if intent.MessageID == "" {
		return value, nil
	}
	message, err := getOperatorSteeringMessageRow(tx.QueryRowContext(ctx, operatorSteeringSelect+` WHERE id=?`, intent.MessageID))
	if err != nil {
		return value, err
	}
	var exact int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM thread_runs WHERE thread_id=? AND run_id=? AND session_id=?`, threadID, message.RunID, message.SessionID).Scan(&exact)
	if err != nil {
		return value, err
	}
	if exact != 1 || message.RunID != intent.RunID || message.RequestedBy != requester {
		return value, apperror.New(apperror.CodeConflict, "Thread request message binding is inconsistent")
	}
	if err = tx.QueryRowContext(ctx, `SELECT workspace_id FROM sessions WHERE id=?`, message.SessionID).Scan(&value.WorkspaceID); err != nil {
		return value, err
	}
	if !legacy {
		var files []domain.WorkspaceFileReference
		var images []domain.ImageReference
		var attachments []domain.FileAttachmentReference
		if err = json.Unmarshal([]byte(attachmentsJSON), &attachments); err != nil {
			return value, err
		}
		if err = validateObservedThreadAttachments(ctx, tx, value.WorkspaceID, message, attachments); err != nil {
			return value, err
		}
		if err = json.Unmarshal([]byte(filesJSON), &files); err != nil {
			return value, err
		}
		if err = json.Unmarshal([]byte(imagesJSON), &images); err != nil {
			return value, err
		}
		if err = validateObservedThreadImages(ctx, tx, value.WorkspaceID, message, images); err != nil {
			return value, err
		}
		_, fingerprint, _, err := threadMessageIntentIdentity(domain.ThreadMessageIntentRequest{ThreadID: threadID, Content: message.OriginalContent, Files: files, Images: images, Attachments: attachments, RequestedBy: requester, OperationKey: key})
		if err != nil {
			return value, err
		}
		if fingerprint != intent.RequestFingerprint {
			return value, apperror.New(apperror.CodeConflict, "Thread request content binding is inconsistent")
		}
	}
	value.RunID, value.SessionID, value.MessageID, value.MessageStatus = message.RunID, message.SessionID, message.ID, message.Status
	if message.Status == domain.OperatorSteeringCancelled {
		value.State, value.Settled = "cancelled", true
		return value, nil
	}
	failure, failed, err := getThreadTurnFailure(ctx, tx, message.RunID, message.ID)
	if err != nil {
		return value, err
	}
	if failed {
		if failure.ThreadID != threadID {
			return value, apperror.New(apperror.CodeConflict, "Thread failure belongs to another Thread")
		}
		value.State, value.Settled, value.Failure = "failed", true, &failure
		return value, nil
	}
	if message.Status != domain.OperatorSteeringCommitted {
		return value, nil
	}
	// A committed message alone is not success: M failures also commit input.
	// Completion requires the exact successful turn event and original message.
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM operator_steering_deliveries delivery
		JOIN session_messages original ON original.id=? AND original.session_id=?
		JOIN run_events event ON event.run_id=delivery.run_id AND event.subject_id=delivery.attempt_id
		WHERE delivery.message_id=? AND delivery.status='committed' AND delivery.run_id=?
		AND original.role='user' AND original.source_kind='operator_message' AND original.instruction_authorized=1
		AND original.content=? AND original.content_sha256=? AND event.type=?
		AND json_extract(event.payload_json,'$.attempt_id')=delivery.attempt_id
		AND json_extract(event.payload_json,'$.turn')=delivery.turn
		AND json_extract(event.payload_json,'$.user_message_id')=original.id
		AND json_extract(event.payload_json,'$.requested_lifecycle_action') IN ('continue','finish','wait')
		AND json_extract(event.payload_json,'$.lifecycle_action') IN ('continue','finish','wait')`,
		message.SessionMessageID, message.SessionID, message.ID, message.RunID, message.Content,
		message.ContentSHA256, events.AgentTurnCompletedEvent).Scan(&exact)
	if err != nil {
		return value, err
	}
	if exact > 1 {
		return value, apperror.New(apperror.CodeConflict, "Thread request has ambiguous completion records")
	}
	if exact == 1 {
		value.State, value.Settled = "completed", true
	}
	return value, nil
}

func validateObservedThreadImages(ctx context.Context, tx *sql.Conn, workspaceID string, message domain.OperatorSteeringMessage, images []domain.ImageReference) error {
	invalid := func() error {
		return apperror.New(apperror.CodeConflict, "Thread request image binding is inconsistent")
	}
	if domain.ValidateThreadMessageImages(images) != nil || message.ImageCount != len(images) {
		return invalid()
	}
	rows, err := tx.QueryContext(ctx, `SELECT binding.ordinal,image.id,image.sha256 FROM thread_message_images binding
		JOIN workspace_image_attachments image ON image.id=binding.image_id AND image.workspace_id=?
		WHERE binding.message_id=? ORDER BY binding.ordinal`, workspaceID, message.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	index := 0
	for rows.Next() {
		var ordinal int
		var ref domain.ImageReference
		if err := rows.Scan(&ordinal, &ref.ID, &ref.SHA256); err != nil {
			return err
		}
		if index >= len(images) || ordinal != index || ref != images[index] {
			return invalid()
		}
		index++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if index != len(images) {
		return invalid()
	}
	return nil
}
