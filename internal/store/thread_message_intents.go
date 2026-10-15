package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
)

func threadMessageIntentIdentity(request domain.ThreadMessageIntentRequest) (string, string, string, error) {
	if !domain.ValidAgentID(request.ThreadID) || !domain.ValidAgentID(request.RequestedBy) {
		return "", "", "", apperror.New(apperror.CodeInvalidArgument, "Thread message intent identity is invalid")
	}
	key, err := domain.NormalizeAgentOperationKey(request.OperationKey)
	if err != nil || key != request.OperationKey {
		return "", "", "", apperror.New(apperror.CodeInvalidArgument, "Thread message intent key is invalid")
	}
	content, err := domain.NormalizeThreadMessageContent(redact.String(request.Content), request.Images, request.Attachments)
	if err != nil {
		return "", "", "", apperror.New(apperror.CodeInvalidArgument, "Thread message intent content is invalid")
	}
	if err := domain.ValidateThreadMessageFiles(request.Files); err != nil {
		return "", "", "", apperror.New(apperror.CodeInvalidArgument, err.Error())
	}
	if err := domain.ValidateThreadMessageImages(request.Images); err != nil {
		return "", "", "", apperror.New(apperror.CodeInvalidArgument, err.Error())
	}
	if err := domain.ValidateThreadMCPReferences(request.MCPReferences); err != nil {
		return "", "", "", apperror.New(apperror.CodeInvalidArgument, err.Error())
	}
	files := request.Files
	if files == nil {
		files = []domain.WorkspaceFileReference{}
	}
	raw, err := json.Marshal(files)
	if err != nil {
		return "", "", "", err
	}
	fingerprint := runmutation.Fingerprint("thread_message_intent_request.v1", request.ThreadID,
		domain.OperatorSteeringContentSHA256(content), request.RequestedBy, string(raw))
	if len(request.Images) > 0 {
		imagesJSON, err := json.Marshal(request.Images)
		if err != nil {
			return "", "", "", err
		}
		fingerprint = runmutation.Fingerprint("thread_message_image_intent_request.v1", fingerprint, string(imagesJSON))
	}
	if len(request.Attachments) > 0 {
		raw, err := json.Marshal(request.Attachments)
		if err != nil {
			return "", "", "", err
		}
		fingerprint = runmutation.Fingerprint("workspace_file_upload.v1", fingerprint, string(raw))
	}
	if len(request.MCPReferences) > 0 {
		refs, err := json.Marshal(request.MCPReferences)
		if err != nil {
			return "", "", "", err
		}
		fingerprint = runmutation.Fingerprint("thread_mcp_references.v1", fingerprint, string(refs))
	}
	return runmutation.Fingerprint("thread_message_intent_operation.v1", request.ThreadID, key), fingerprint, string(raw), nil
}

func getThreadMessageIntentTx(ctx context.Context, tx *sql.Tx, key string) (domain.ThreadMessageIntent, bool, error) {
	var intent domain.ThreadMessageIntent
	err := tx.QueryRowContext(ctx, `SELECT operation_key_digest, request_fingerprint,
		COALESCE(run_id, ''), COALESCE(message_id, ''), rejected FROM thread_message_intents
		WHERE operation_key_digest = ?`, key).Scan(&intent.OperationKeyDigest, &intent.RequestFingerprint, &intent.RunID, &intent.MessageID, &intent.Rejected)
	if errors.Is(err, sql.ErrNoRows) {
		return intent, false, nil
	}
	return intent, err == nil, err
}

// ReserveThreadMessageIntent fixes the original Thread operation key before
// preparation changes a Run. Legacy steering operations are adopted rather than
// replayed into a newer successor or treated as file-bearing submissions.
func (s *SQLiteStore) ReserveThreadMessageIntent(ctx context.Context, request domain.ThreadMessageIntentRequest) (domain.ThreadMessageIntent, error) {
	key, fingerprint, filesJSON, err := threadMessageIntentIdentity(request)
	if err != nil {
		return domain.ThreadMessageIntent{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.ThreadMessageIntent{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE threads SET updated_at = updated_at WHERE id = ?`, request.ThreadID); err != nil {
		return domain.ThreadMessageIntent{}, err
	}
	intent, found, err := getThreadMessageIntentTx(ctx, tx, key)
	if err != nil {
		return intent, err
	}
	if found {
		if intent.RequestFingerprint != fingerprint {
			return intent, apperror.New(apperror.CodeConflict, "Thread message operation key was already used for different content or file references")
		}
		return intent, tx.Commit()
	}
	threadRecord, err := scanThread(tx.QueryRowContext(ctx, threadSelect+` WHERE id = ?`, request.ThreadID))
	if err != nil {
		return intent, err
	}
	if err := validateThreadAttachmentsTx(ctx, tx, threadRecord.WorkspaceID, request.Attachments); err != nil {
		return intent, err
	}
	if err := validateThreadImagesTx(ctx, tx, threadRecord.WorkspaceID, request.Images); err != nil {
		return intent, err
	}
	intent.OperationKeyDigest, intent.RequestFingerprint = key, fingerprint
	rows, err := tx.QueryContext(ctx, `SELECT run_id FROM thread_runs WHERE thread_id = ? ORDER BY ordinal DESC`, request.ThreadID)
	if err != nil {
		return intent, err
	}
	var runIDs []string
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			rows.Close()
			return intent, err
		}
		runIDs = append(runIDs, runID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return intent, err
	}
	for _, runID := range runIDs {
		_, messageID, found, err := getOperatorSteeringOperationTx(ctx, tx,
			runmutation.Fingerprint("operator_steering_operation.v1", runID, request.OperationKey))
		if err != nil {
			return intent, err
		}
		if !found {
			continue
		}
		message, err := getOperatorSteeringMessageTx(ctx, tx, messageID)
		if err != nil {
			return intent, err
		}
		content, _ := domain.NormalizeOperatorSteeringContent(redact.String(request.Content))
		if len(request.Files) != 0 || len(request.Images) != 0 || len(request.Attachments) != 0 || len(request.MCPReferences) != 0 || message.RequestedBy != request.RequestedBy || message.OriginalContentSHA256 != domain.OperatorSteeringContentSHA256(content) {
			return intent, apperror.New(apperror.CodeConflict, "Thread message key already belongs to a different legacy message intent")
		}
		intent.RunID, intent.MessageID = runID, messageID
		break
	}
	if intent.MessageID == "" && !threadRecord.CanAcceptMessages() {
		return intent, apperror.New(apperror.CodeFailedPrecondition, "Thread is not active")
	}
	attachments := request.Attachments
	if attachments == nil {
		attachments = []domain.FileAttachmentReference{}
	}
	attachmentsJSON, err := json.Marshal(attachments)
	if err != nil {
		return intent, err
	}
	images := request.Images
	if images == nil {
		images = []domain.ImageReference{}
	}
	imagesJSON, err := json.Marshal(images)
	if err != nil {
		return intent, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO thread_message_intents
		(operation_key_digest, thread_id, request_fingerprint, files_json, images_json, attachments_json, run_id, message_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?)`, key, request.ThreadID, fingerprint, filesJSON, string(imagesJSON), string(attachmentsJSON), intent.RunID, intent.MessageID, ts(time.Now().UTC())); err != nil {
		return intent, err
	}
	if err := reserveThreadMCPReferencesTx(ctx, tx, key, request.MCPReferences, request.RequestedBy); err != nil {
		return intent, err
	}
	return intent, tx.Commit()
}

// CommitThreadMessage exposes all validated evidence and the steering message
// together. SQLite's write transaction serializes this busy check with Run
// lease acquisition and other submitters, including other store connections.
func (s *SQLiteStore) CommitThreadMessage(ctx context.Context, request domain.ThreadMessageIntentRequest, runID string,
	prepared []session.PreparedEvidenceAttachment,
) (domain.OperatorSteeringEnqueueResult, error) {
	return s.commitThreadMessage(ctx, request, runID, prepared, "", "")
}

func (s *SQLiteStore) CommitThreadPlanMessage(ctx context.Context, request domain.ThreadMessageIntentRequest, runID, proposalID, selectionKey string) (domain.OperatorSteeringEnqueueResult, error) {
	if !domain.ValidAgentID(proposalID) || selectionKey == "" || len(request.Files) != 0 || len(request.Images) != 0 || len(request.Attachments) != 0 || len(request.MCPReferences) != 0 {
		return domain.OperatorSteeringEnqueueResult{}, apperror.New(apperror.CodeInvalidArgument, "Thread Plan message binding is invalid")
	}
	return s.commitThreadMessage(ctx, request, runID, nil, proposalID, selectionKey)
}

func (s *SQLiteStore) commitThreadMessage(ctx context.Context, request domain.ThreadMessageIntentRequest, runID string,
	prepared []session.PreparedEvidenceAttachment, proposalID, selectionKey string,
) (domain.OperatorSteeringEnqueueResult, error) {
	key, fingerprint, _, err := threadMessageIntentIdentity(request)
	if err != nil {
		return domain.OperatorSteeringEnqueueResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.OperatorSteeringEnqueueResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE threads SET updated_at = updated_at WHERE id = ?`, request.ThreadID); err != nil {
		return domain.OperatorSteeringEnqueueResult{}, err
	}
	intent, found, err := getThreadMessageIntentTx(ctx, tx, key)
	if err != nil {
		return domain.OperatorSteeringEnqueueResult{}, err
	}
	if !found || intent.RequestFingerprint != fingerprint {
		return domain.OperatorSteeringEnqueueResult{}, apperror.New(apperror.CodeConflict, "Thread message intent binding changed")
	}
	if intent.Rejected {
		return domain.OperatorSteeringEnqueueResult{}, apperror.New(apperror.CodeFailedPrecondition, "This Thread message was rejected before enqueue; retry the draft with a new operation key")
	}
	if intent.MessageID != "" {
		message, err := getOperatorSteeringMessageTx(ctx, tx, intent.MessageID)
		if err != nil {
			return domain.OperatorSteeringEnqueueResult{}, err
		}
		return domain.OperatorSteeringEnqueueResult{Message: message, Replayed: true}, tx.Commit()
	}
	threadRecord, err := scanThread(tx.QueryRowContext(ctx, threadSelect+` WHERE id = ?`, request.ThreadID))
	if err != nil {
		return domain.OperatorSteeringEnqueueResult{}, err
	}
	if !threadRecord.CanAcceptMessages() || threadRecord.ActiveRunID != runID {
		return domain.OperatorSteeringEnqueueResult{}, apperror.New(apperror.CodeConflict, "Thread active Run changed")
	}
	if proposalID != "" {
		if err := requireThreadPlanMessageTx(ctx, tx, request, runID, proposalID, selectionKey); err != nil {
			return domain.OperatorSteeringEnqueueResult{}, err
		}
	}
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id, status, config_json, budget_json, started_at, finished_at, created_at, updated_at FROM runs WHERE id = ?`, runID))
	if err != nil {
		return domain.OperatorSteeringEnqueueResult{}, err
	}
	if err := validateThreadAttachmentsTx(ctx, tx, threadRecord.WorkspaceID, request.Attachments); err != nil {
		return domain.OperatorSteeringEnqueueResult{}, err
	}
	if len(prepared) != len(request.Files) {
		return domain.OperatorSteeringEnqueueResult{}, apperror.New(apperror.CodeInvalidArgument, "Thread file reference preparation is incomplete")
	}
	if err := validateThreadImagesTx(ctx, tx, threadRecord.WorkspaceID, request.Images); err != nil {
		return domain.OperatorSteeringEnqueueResult{}, err
	}
	if len(request.Files) > 0 || len(request.MCPReferences) > 0 {
		busy, err := operatorSteeringBusyTx(ctx, tx, runID, time.Now().UTC())
		if err != nil {
			return domain.OperatorSteeringEnqueueResult{}, err
		}
		if busy || (run.Status != domain.RunRunning && run.Status != domain.RunPaused) {
			return domain.OperatorSteeringEnqueueResult{}, apperror.New(apperror.CodeFailedPrecondition, "Thread project file references require an idle task without queued messages or approval")
		}
		for index, file := range request.Files {
			item := prepared[index]
			attachment := item.Attachment
			if attachment.RunID != run.ID || attachment.SessionID != run.SessionID || attachment.WorkspaceID != threadRecord.WorkspaceID ||
				attachment.SourceKind != file.SourceKind || attachment.SourceRef != file.Path || attachment.ContentSHA256 != file.ExpectedSHA256 || attachment.AttachedBy != request.RequestedBy ||
				attachment.OperationKeyDigest != runmutation.EvidenceAttachmentOperationDigest(run.ID, domain.ThreadMessageFileOperationKey(key, index)) ||
				attachment.RequestFingerprint != runmutation.EvidenceAttachmentRequestFingerprint(run.ID, threadRecord.WorkspaceID, file.SourceKind, file.Path, file.ExpectedSHA256, request.RequestedBy) {
				return domain.OperatorSteeringEnqueueResult{}, apperror.New(apperror.CodeConflict, "Thread file reference snapshot binding changed")
			}
			if _, _, _, err := attachEvidenceTx(ctx, tx, attachment, item.Message); err != nil {
				return domain.OperatorSteeringEnqueueResult{}, err
			}
		}
	}
	queued, _, err := enqueueOperatorSteeringTx(ctx, tx, domain.EnqueueOperatorSteeringRequest{RunID: run.ID, SessionID: run.SessionID,
		Content: request.Content, OperationKey: request.OperationKey, RequestedBy: request.RequestedBy, Images: request.Images, Attachments: request.Attachments}, false)
	if err != nil {
		return domain.OperatorSteeringEnqueueResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE thread_message_intents SET run_id = ?, message_id = ? WHERE operation_key_digest = ?`, run.ID, queued.Message.ID, key); err != nil {
		return domain.OperatorSteeringEnqueueResult{}, err
	}
	return queued, tx.Commit()
}

// RejectThreadMessageIntent makes a negative acknowledgement durable. It wins
// only against the same still-unsubmitted intent, so another process can never
// enqueue after the API has truthfully reported message_queued:false.
func (s *SQLiteStore) RejectThreadMessageIntent(ctx context.Context, request domain.ThreadMessageIntentRequest) (bool, error) {
	key, fingerprint, _, err := threadMessageIntentIdentity(request)
	if err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE threads SET updated_at = updated_at WHERE id = ?`, request.ThreadID); err != nil {
		return false, err
	}
	intent, found, err := getThreadMessageIntentTx(ctx, tx, key)
	if err != nil {
		return false, err
	}
	if !found || intent.RequestFingerprint != fingerprint || intent.MessageID != "" {
		return false, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE thread_message_intents SET rejected = 1 WHERE operation_key_digest = ?`, key); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
