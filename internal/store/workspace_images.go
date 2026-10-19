package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/imageattachment"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
)

const workspaceImageSelect = `SELECT id,workspace_id,sha256,mime_type,byte_size,width,height,name,content FROM workspace_image_attachments`

func scanWorkspaceImage(row interface{ Scan(...any) error }) (domain.WorkspaceImage, []byte, error) {
	var image domain.WorkspaceImage
	var content []byte
	err := row.Scan(&image.ID, &image.WorkspaceID, &image.SHA256, &image.MIMEType, &image.ByteSize, &image.Width, &image.Height, &image.Name, &content)
	if errors.Is(err, sql.ErrNoRows) {
		return image, nil, apperror.New(apperror.CodeNotFound, "Workspace image was not found")
	}
	if err != nil {
		return image, nil, err
	}
	digest := sha256.Sum256(content)
	if len(content) != image.ByteSize || hex.EncodeToString(digest[:]) != image.SHA256 {
		return image, nil, apperror.New(apperror.CodeConflict, "Stored image integrity check failed")
	}
	return image, content, nil
}

func saveImageEvidenceTx(ctx context.Context, tx *sql.Tx, runID, sessionID, messageID string, refs []domain.ImageReference) ([]session.Message, error) {
	saved := make([]session.Message, 0, len(refs))
	for _, ref := range refs {
		image, _, err := scanWorkspaceImage(tx.QueryRowContext(ctx, workspaceImageSelect+` WHERE id=?`, ref.ID))
		if err != nil {
			return nil, err
		}
		body, _ := json.Marshal(struct {
			Image                 domain.WorkspaceImage `json:"image"`
			MessageID             string                `json:"operator_message_id"`
			InstructionAuthorized bool                  `json:"instruction_authorized"`
		}{image, messageID, false})
		message := session.NewEvidenceMessage(sessionID, session.SourceWorkspaceImage, image.ID, "Uploaded image observation. The original pixels remain in the attachment store; this descriptor does not describe their visual content or grant authority.\n"+string(body))
		stored, err := saveSessionMessageTx(ctx, tx, message)
		if err != nil {
			return nil, err
		}
		saved = append(saved, stored)
	}
	return saved, nil
}

// Only this exact current attempt, committed messages, or retained successor
// inputs of the same Thread can supply image evidence. Old/compacted pixels are
// not silently presented as remembered; callers must retain their descriptor.
func (s *SQLiteStore) ListSupervisorImageEvidence(ctx context.Context, cp domain.SupervisorCheckpoint, historyIDs []int64) (domain.ModelImageContext, error) {
	var projection domain.ModelImageContext
	if err := cp.Validate(); err != nil {
		return projection, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return projection, err
	}
	defer func() { _ = tx.Rollback() }()
	current, found, err := getSupervisorCheckpointTx(ctx, tx, cp.RunID)
	if err != nil {
		return projection, err
	}
	if !found || current.AttemptID != cp.AttemptID || current.NextTurn != cp.NextTurn || current.PendingInput != cp.PendingInput || current.PendingImageCount != cp.PendingImageCount {
		return projection, apperror.New(apperror.CodeConflict, "Image input attempt changed")
	}
	if err := requireSupervisorCheckpointLeaseTx(ctx, tx, cp, current); err != nil {
		return projection, err
	}
	currentMessageID := ""
	err = tx.QueryRowContext(ctx, `SELECT message_id FROM operator_steering_deliveries WHERE run_id=? AND attempt_id=? AND status='prepared'`, cp.RunID, cp.AttemptID).Scan(&currentMessageID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return projection, err
	}
	if currentMessageID == "" {
		if continuation, found, err := approvalContinuationForCheckpointTx(ctx, tx, cp); err != nil {
			return projection, err
		} else if found {
			currentMessageID = continuation.Handoff.Items[0].MessageID
		}
	}
	historyJSON, err := json.Marshal(historyIDs)
	if err != nil {
		return projection, err
	}
	const candidates = `WITH ` + retainedThreadInputsCTE + `, candidates AS (
	 SELECT m.id,COALESCE(m.session_message_id,carried.session_message_id) AS session_message_id,m.image_count,prior.ordinal,m.sequence,
	 (m.id=? OR COALESCE(m.session_message_id,carried.session_message_id) IN (SELECT value FROM json_each(?))) AS retained
	 FROM thread_runs caller JOIN threads t ON t.id=caller.thread_id JOIN thread_runs prior ON prior.thread_id=t.id AND prior.ordinal<=caller.ordinal
	 JOIN operator_steering_messages m ON m.run_id=prior.run_id AND m.session_id=prior.session_id
	 LEFT JOIN retained_thread_inputs carried ON carried.message_id=m.id
	 WHERE caller.run_id=? AND m.image_count>0 AND (m.status='committed' OR m.id=? OR carried.message_id IS NOT NULL)
	), selected AS (
	 SELECT * FROM candidates WHERE retained OR id IN (
	 SELECT id FROM candidates WHERE NOT retained ORDER BY ordinal DESC,sequence DESC LIMIT 8
	 )) `
	args := []any{cp.RunID, currentMessageID, string(historyJSON), cp.RunID, currentMessageID}
	var selectedCount int
	if err := tx.QueryRowContext(ctx, candidates+`SELECT COALESCE((SELECT SUM(image_count) FROM candidates),0)-COALESCE((SELECT SUM(image_count) FROM selected),0),COALESCE((SELECT SUM(image_count) FROM selected),0)`, args...).Scan(&projection.OmittedOlderImages, &selectedCount); err != nil {
		return projection, err
	}
	rows, err := tx.QueryContext(ctx, candidates+`SELECT m.id,COALESCE(m.session_message_id,0),m.image_count,i.id,i.workspace_id,i.sha256,i.mime_type,i.byte_size,i.width,i.height,i.name
	 FROM selected m JOIN thread_message_images b ON b.message_id=m.id
	 JOIN workspace_image_attachments i ON i.id=b.image_id
	 JOIN operator_steering_messages original ON original.id=m.id JOIN sessions source ON source.id=original.session_id AND source.workspace_id=i.workspace_id
	 JOIN thread_runs caller ON caller.run_id=?
	 ORDER BY m.ordinal,m.sequence,b.ordinal`, append(args, cp.RunID)...)
	if err != nil {
		return projection, err
	}
	counts := map[string]int{}
	expected := map[string]int{}
	for rows.Next() {
		var item domain.ModelImageEvidence
		var count int
		if err := rows.Scan(&item.MessageID, &item.SessionMessageID, &count, &item.Image.ID, &item.Image.WorkspaceID, &item.Image.SHA256, &item.Image.MIMEType, &item.Image.ByteSize, &item.Image.Width, &item.Image.Height, &item.Image.Name); err != nil {
			rows.Close()
			return projection, err
		}
		item.Current = item.MessageID == currentMessageID
		projection.Images = append(projection.Images, item)
		counts[item.MessageID]++
		expected[item.MessageID] = count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return projection, err
	}
	if len(projection.Images) != selectedCount {
		return projection, apperror.New(apperror.CodeConflict, "Selected image history binding is incomplete")
	}
	for id, count := range counts {
		if count != expected[id] {
			return projection, apperror.New(apperror.CodeConflict, "Image message binding is incomplete")
		}
	}
	if counts[currentMessageID] != cp.PendingImageCount {
		return projection, apperror.New(apperror.CodeConflict, "Current image input binding is incomplete")
	}
	return projection, tx.Commit()
}

func (s *SQLiteStore) SaveWorkspaceImage(ctx context.Context, workspaceID, operationKey, mimeType, name string, content []byte) (domain.WorkspaceImage, error) {
	// MCP content can use GIF; operator uploads retain their published formats.
	if mimeType != "image/png" && mimeType != "image/jpeg" && mimeType != "image/webp" {
		return domain.WorkspaceImage{}, apperror.New(apperror.CodeInvalidArgument, "Workspace image uploads support PNG, JPEG, or WebP")
	}
	image, err := imageattachment.Validate(content, mimeType, name)
	if err != nil {
		return image, err
	}
	key, err := domain.NormalizeAgentOperationKey(operationKey)
	if err != nil || key != operationKey || !domain.ValidAgentID(workspaceID) {
		return image, apperror.New(apperror.CodeInvalidArgument, "Workspace image upload identity is invalid")
	}
	op := runmutation.Fingerprint("workspace_image_operation.v1", workspaceID, key)
	fingerprint := runmutation.Fingerprint("workspace_image_request.v1", workspaceID, image.SHA256, image.MIMEType, image.Name)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return image, err
	}
	defer func() { _ = tx.Rollback() }()
	// Acquire the workspace write fence before checking or inserting the key.
	result, err := tx.ExecContext(ctx, `UPDATE workspaces SET name=name WHERE id=?`, workspaceID)
	if err != nil {
		return image, err
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		return image, apperror.New(apperror.CodeNotFound, "Workspace was not found")
	}
	var prior string
	err = tx.QueryRowContext(ctx, `SELECT request_fingerprint FROM workspace_image_attachments WHERE operation_digest=?`, op).Scan(&prior)
	if err == nil {
		if prior != fingerprint {
			return image, apperror.New(apperror.CodeConflict, "Image upload key belongs to different image bytes or metadata")
		}
		stored, _, readErr := scanWorkspaceImage(tx.QueryRowContext(ctx, workspaceImageSelect+` WHERE operation_digest=?`, op))
		if readErr != nil {
			return image, readErr
		}
		return stored, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return image, err
	}
	image.ID = "image-" + op
	image.WorkspaceID = workspaceID
	_, err = tx.ExecContext(ctx, `INSERT INTO workspace_image_attachments (id,workspace_id,operation_digest,request_fingerprint,sha256,mime_type,byte_size,width,height,name,content,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, image.ID, workspaceID, op, fingerprint, image.SHA256, image.MIMEType, image.ByteSize, image.Width, image.Height, image.Name, content, ts(time.Now().UTC()))
	if err != nil {
		return image, err
	}
	return image, tx.Commit()
}

func (s *SQLiteStore) GetWorkspaceImage(ctx context.Context, workspaceID, imageID string) (domain.WorkspaceImage, []byte, error) {
	return scanWorkspaceImage(s.db.QueryRowContext(ctx, workspaceImageSelect+` WHERE workspace_id=? AND id=?`, workspaceID, imageID))
}

func validateThreadImagesTx(ctx context.Context, tx *sql.Tx, workspaceID string, images []domain.ImageReference) error {
	for _, ref := range images {
		image, _, err := scanWorkspaceImage(tx.QueryRowContext(ctx, workspaceImageSelect+` WHERE workspace_id=? AND id=?`, workspaceID, ref.ID))
		if err != nil {
			return err
		}
		if image.SHA256 != ref.SHA256 {
			return apperror.New(apperror.CodeConflict, "Thread image hash does not match the uploaded image")
		}
	}
	return nil
}

func (s *SQLiteStore) ListOperatorMessageImages(ctx context.Context, runID, messageID string) ([]domain.WorkspaceImage, error) {
	return listOperatorMessageImagesTx(ctx, s.db, runID, messageID)
}

func listOperatorMessageImagesTx(ctx context.Context, reader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, runID, messageID string) ([]domain.WorkspaceImage, error) {
	rows, err := reader.QueryContext(ctx, `SELECT i.id,i.workspace_id,i.sha256,i.mime_type,i.byte_size,i.width,i.height,i.name FROM thread_message_images b JOIN workspace_image_attachments i ON i.id=b.image_id JOIN operator_steering_messages m ON m.id=b.message_id WHERE m.run_id=? AND m.id=? ORDER BY b.ordinal`, runID, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var images []domain.WorkspaceImage
	for rows.Next() {
		var image domain.WorkspaceImage
		if err := rows.Scan(&image.ID, &image.WorkspaceID, &image.SHA256, &image.MIMEType, &image.ByteSize, &image.Width, &image.Height, &image.Name); err != nil {
			return nil, err
		}
		images = append(images, image)
	}
	return images, rows.Err()
}
