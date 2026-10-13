package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sort"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

// FileAttachmentInputDirectory is private runtime storage, never a Workspace.
// The application checks it against both source and execution roots before any
// materialization. Merely uploading a file does not write this directory.
func (s *SQLiteStore) FileAttachmentInputDirectory() string {
	return filepath.Join(s.home, "attachment-inputs")
}

// ListSupervisorFileAttachmentInputs observes only sent evidence. A queued
// message is eligible only while this exact checkpoint owns its prepared
// delivery or its successor retained it as an unexecuted input. Imported-but-unsent
// files and later queued messages are excluded.
// The flat inventory also survives Session compaction and same-Thread successors.
func (s *SQLiteStore) ListSupervisorFileAttachmentInputs(ctx context.Context, cp domain.SupervisorCheckpoint) (domain.FileAttachmentInputSet, error) {
	set := domain.FileAttachmentInputSet{Version: "file_attachment_inputs.v1", RunID: cp.RunID, Files: []domain.FileAttachmentInput{}}
	tx, finish, err := s.beginThreadRequestObservation(ctx)
	if err != nil {
		return set, err
	}
	defer finish()
	err = tx.QueryRowContext(ctx, `SELECT t.id,r.session_id,t.workspace_id FROM thread_runs tr
	 JOIN threads t ON t.id=tr.thread_id JOIN runs r ON r.id=tr.run_id AND r.session_id=tr.session_id
	 WHERE tr.run_id=?`, cp.RunID).Scan(&set.ThreadID, &set.SessionID, &set.WorkspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return set, nil
	}
	if err != nil {
		return set, err
	}
	var total int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM thread_message_attachments b
	 JOIN operator_steering_messages m ON m.id=b.message_id JOIN thread_runs tr ON tr.run_id=m.run_id
	 WHERE tr.thread_id=?`, set.ThreadID).Scan(&total); err != nil || total == 0 {
		return set, err
	}
	current, err := scanSupervisorCheckpoint(tx.QueryRowContext(ctx, supervisorCheckpointSelect, cp.RunID))
	if err != nil {
		return set, err
	}
	if cp.Validate() != nil || cp.Phase != domain.SupervisorTurnStarted ||
		current.Phase != cp.Phase || current.AttemptID != cp.AttemptID || current.NextTurn != cp.NextTurn ||
		current.PendingInput != cp.PendingInput || current.PendingAttachmentCount != cp.PendingAttachmentCount ||
		current.LeaseID != cp.LeaseID || current.LeaseGeneration != cp.LeaseGeneration || cp.LeaseID == "" {
		return set, apperror.New(apperror.CodeConflict, "Attachment input checkpoint changed")
	}
	lease, err := scanRunExecutionLease(tx.QueryRowContext(ctx, runExecutionLeaseSelect, cp.RunID))
	if err != nil {
		return set, err
	}
	if lease.LeaseID != cp.LeaseID || lease.Generation != cp.LeaseGeneration || !lease.ActiveAt(time.Now().UTC()) {
		return set, apperror.New(apperror.CodeConflict, "Attachment input execution lease changed")
	}
	currentMessageID := ""
	err = tx.QueryRowContext(ctx, `SELECT message_id FROM operator_steering_deliveries
	 WHERE run_id=? AND attempt_id=? AND turn=? AND status='prepared'`, cp.RunID, cp.AttemptID, cp.NextTurn).Scan(&currentMessageID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return set, err
	}
	rows, err := tx.QueryContext(ctx, `WITH `+retainedThreadInputsCTE+`
	 SELECT m.id,m.attachment_count,b.ordinal,f.id,f.workspace_id,f.name,f.mime_type,f.sha256,f.byte_size
	 FROM thread_runs caller JOIN threads t ON t.id=caller.thread_id AND t.active_run_id=caller.run_id
	 JOIN thread_runs prior ON prior.thread_id=t.id AND prior.ordinal<=caller.ordinal
	 JOIN operator_steering_messages m ON m.run_id=prior.run_id AND m.session_id=prior.session_id
	 LEFT JOIN thread_message_attachments b ON b.message_id=m.id
	 LEFT JOIN workspace_file_attachments f ON f.id=b.attachment_id AND f.workspace_id=t.workspace_id
	 WHERE caller.run_id=? AND m.attachment_count>0 AND (m.id=? OR
	 EXISTS(SELECT 1 FROM retained_thread_inputs retained WHERE retained.message_id=m.id) OR
	 (m.status='committed' AND EXISTS(SELECT 1 FROM operator_steering_deliveries d WHERE d.message_id=m.id AND d.status='committed')
	 AND EXISTS(SELECT 1 FROM session_messages sm WHERE sm.id=m.session_message_id AND sm.session_id=m.session_id AND sm.role='user')))
	 ORDER BY (m.id=?) DESC,prior.ordinal DESC,m.sequence DESC,b.ordinal`, cp.RunID, cp.RunID, currentMessageID, currentMessageID)
	if err != nil {
		return set, err
	}
	counts, expected, seen := map[string]int{}, map[string]int{}, map[string]bool{}
	for rows.Next() {
		var messageID string
		var count, ordinal int
		var file domain.WorkspaceFileAttachment
		if err := rows.Scan(&messageID, &count, &ordinal, &file.ID, &file.WorkspaceID, &file.Name, &file.MIMEType, &file.SHA256, &file.ByteSize); err != nil {
			rows.Close()
			return set, err
		}
		if ordinal != counts[messageID] || file.WorkspaceID != set.WorkspaceID {
			rows.Close()
			return set, apperror.New(apperror.CodeConflict, "Attachment input source binding is incomplete")
		}
		counts[messageID]++
		expected[messageID] = count
		if !seen[file.ID] {
			if len(set.Files) < domain.MaxRunFileAttachmentInputs {
				set.Files = append(set.Files, domain.NewFileAttachmentInput(file))
			} else {
				set.OmittedCount++
			}
			seen[file.ID] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return set, err
	}
	for id, count := range expected {
		if count != counts[id] {
			return set, apperror.New(apperror.CodeConflict, "Attachment input message binding is incomplete")
		}
	}
	if currentMessageID != "" && counts[currentMessageID] != cp.PendingAttachmentCount {
		return set, apperror.New(apperror.CodeConflict, "Prepared attachment input count changed")
	}
	// Stable order keeps repeated execution/restart of the same selected files
	// on the same immutable cache directory. Selection above prioritizes the
	// current input, then recent sent files; old bytes and bindings remain saved.
	sort.Slice(set.Files, func(i, j int) bool { return set.Files[i].ID < set.Files[j].ID })
	return set, nil
}
