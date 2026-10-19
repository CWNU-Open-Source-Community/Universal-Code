package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

const workspaceCheckpointColumns = `id, protocol_version, run_id, mission_id,
	session_id, workspace_id, attempt_id, capability_generation, trigger_kind, phase,
	trigger_receipt_id, requested_by, title, parent_checkpoint_id, root_fingerprint, root_path_sha256,
	base_commit, branch, index_sha256, index_blob_sha256, manifest_sha256,
	recovery_level, incomplete_reasons_json, entry_count, stored_bytes, created_at`

const workspaceCheckpointTransactionColumns = `id, protocol_version,
	operation_key_digest, request_fingerprint, run_id, workspace_id, kind,
	trigger_receipt_id, before_checkpoint_id, after_checkpoint_id,
	expected_current_checkpoint_id, target_checkpoint_id, fork_workspace_root,
	fork_branch, status, recovery_level,
	error_code, conflict_json, created_at, updated_at, completed_at`

type workspaceCheckpointQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// CreateWorkspaceCheckpoint commits blobs, manifest metadata, entries, and the
// final seal in one SQLite transaction. A repeated exact ID is a read-only
// replay; an ID reused for different content fails closed.
func (s *SQLiteStore) CreateWorkspaceCheckpoint(ctx context.Context,
	snapshot workspacecheckpoint.Snapshot,
) (workspacecheckpoint.Checkpoint, bool, error) {
	if err := snapshot.Validate(); err != nil {
		return workspacecheckpoint.Checkpoint{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workspacecheckpoint.Checkpoint{}, false, err
	}
	defer tx.Rollback()
	valueResult, replayed, err := createWorkspaceCheckpointTx(ctx, tx, snapshot)
	if err != nil {
		return workspacecheckpoint.Checkpoint{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return workspacecheckpoint.Checkpoint{}, false, err
	}
	return valueResult, replayed, nil
}

func createWorkspaceCheckpointTx(ctx context.Context, tx *sql.Tx, snapshot workspacecheckpoint.Snapshot) (workspacecheckpoint.Checkpoint, bool, error) {
	var err error
	if existing, getErr := getWorkspaceCheckpoint(ctx, tx, snapshot.Checkpoint.ID); getErr == nil {
		if !sameWorkspaceCheckpoint(existing, snapshot.Checkpoint) {
			return workspacecheckpoint.Checkpoint{}, false, apperror.New(
				apperror.CodeConflict, "workspace checkpoint ID was reused for different content")
		}
		return existing, true, nil
	} else if !errors.Is(getErr, sql.ErrNoRows) {
		return workspacecheckpoint.Checkpoint{}, false, getErr
	}
	for _, blob := range snapshot.Blobs {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO workspace_checkpoint_blobs
			(sha256, size_bytes, content, reference_count, created_at)
			VALUES (?, ?, ?, 0, ?)`, blob.SHA256, len(blob.Content), blob.Content,
			ts(blob.CreatedAt)); err != nil {
			return workspacecheckpoint.Checkpoint{}, false, err
		}
		var size int
		var content []byte
		if err := tx.QueryRowContext(ctx, `SELECT size_bytes, content
			FROM workspace_checkpoint_blobs WHERE sha256 = ?`, blob.SHA256).
			Scan(&size, &content); err != nil {
			return workspacecheckpoint.Checkpoint{}, false, err
		}
		if size != len(blob.Content) || !bytes.Equal(content, blob.Content) {
			return workspacecheckpoint.Checkpoint{}, false, apperror.New(
				apperror.CodeConflict, "workspace checkpoint blob digest collision")
		}
	}
	reasonsJSON, err := json.Marshal(snapshot.Checkpoint.IncompleteReasons)
	if err != nil {
		return workspacecheckpoint.Checkpoint{}, false, err
	}
	c := snapshot.Checkpoint
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_checkpoints
		(id, protocol_version, run_id, mission_id, session_id, workspace_id, attempt_id,
		 capability_generation, trigger_kind, phase, trigger_receipt_id,
		 requested_by, title, parent_checkpoint_id, root_fingerprint, root_path_sha256, base_commit, branch,
		 index_sha256, index_blob_sha256, manifest_sha256, recovery_level,
		 incomplete_reasons_json, entry_count, stored_bytes, sealed, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`,
		c.ID, c.ProtocolVersion, c.RunID, c.MissionID, c.SessionID, c.WorkspaceID,
		c.AttemptID, c.CapabilityGeneration, c.Trigger, c.Phase, c.TriggerReceiptID,
		c.RequestedBy, c.Title, c.ParentCheckpointID, c.RootFingerprint, c.RootPathSHA256, c.BaseCommit,
		c.Branch, c.IndexSHA256, c.IndexBlobSHA256, c.ManifestSHA256,
		c.RecoveryLevel, string(reasonsJSON), c.EntryCount, c.StoredBytes,
		ts(c.CreatedAt)); err != nil {
		return workspacecheckpoint.Checkpoint{}, false, normalizeWorkspaceCheckpointStoreError(err)
	}
	for _, entry := range snapshot.Entries {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_checkpoint_entries
			(checkpoint_id, path, kind, worktree_state, storage_policy, mode,
			 size_bytes, worktree_sha256, blob_sha256, index_oid, index_mode,
			 tracked, staged, binary, line_endings, recoverable, reason)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ID, entry.Path, entry.Kind, entry.State, entry.StoragePolicy, entry.Mode,
			entry.Size, entry.WorktreeSHA256, entry.BlobSHA256, entry.IndexOID,
			entry.IndexMode, boolInt(entry.Tracked), boolInt(entry.Staged),
			boolInt(entry.Binary), entry.LineEndings, boolInt(entry.Recoverable),
			entry.Reason); err != nil {
			return workspacecheckpoint.Checkpoint{}, false,
				normalizeWorkspaceCheckpointStoreError(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspace_checkpoints SET sealed = 1
		WHERE id = ? AND sealed = 0`, c.ID); err != nil {
		return workspacecheckpoint.Checkpoint{}, false, normalizeWorkspaceCheckpointStoreError(err)
	}
	event, err := events.New(c.RunID, c.MissionID, events.WorkspaceCheckpointCreatedEvent,
		"workspace_checkpoint", c.ID, map[string]any{
			"checkpoint_id": c.ID, "workspace_id": c.WorkspaceID,
			"trigger": c.Trigger, "phase": c.Phase,
			"trigger_receipt_id":   c.TriggerReceiptID,
			"requested_by":         c.RequestedBy,
			"title":                c.Title,
			"parent_checkpoint_id": c.ParentCheckpointID,
			"recovery_level":       c.RecoveryLevel, "entry_count": c.EntryCount,
			"stored_bytes": c.StoredBytes,
		})
	if err != nil {
		return workspacecheckpoint.Checkpoint{}, false, err
	}
	event.CreatedAt = c.CreatedAt
	if _, err := insertRunEventTx(ctx, tx, event); err != nil {
		return workspacecheckpoint.Checkpoint{}, false, err
	}
	return c, false, nil
}

func (s *SQLiteStore) GetWorkspaceCheckpoint(ctx context.Context,
	id string,
) (workspacecheckpoint.Checkpoint, error) {
	value, err := getWorkspaceCheckpoint(ctx, s.db, strings.TrimSpace(id))
	if errors.Is(err, sql.ErrNoRows) {
		return workspacecheckpoint.Checkpoint{}, apperror.New(
			apperror.CodeNotFound, "workspace checkpoint not found")
	}
	return value, err
}

func (s *SQLiteStore) ListWorkspaceCheckpoints(ctx context.Context, runID string,
	limit int,
) ([]workspacecheckpoint.Checkpoint, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" || limit < 1 || limit > 2_000 {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"workspace checkpoint list request is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+workspaceCheckpointColumns+`
		FROM workspace_checkpoints WHERE run_id = ? AND sealed = 1
		ORDER BY created_at DESC, id DESC LIMIT ?`, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]workspacecheckpoint.Checkpoint, 0, limit)
	for rows.Next() {
		value, err := scanWorkspaceCheckpoint(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *SQLiteStore) ListWorkspaceCheckpointsByWorkspace(ctx context.Context,
	runID, workspaceID string, limit int,
) ([]workspacecheckpoint.Checkpoint, error) {
	runID, workspaceID = strings.TrimSpace(runID), strings.TrimSpace(workspaceID)
	if runID == "" || workspaceID == "" || limit < 1 || limit > 2_000 {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"workspace checkpoint scoped list request is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+workspaceCheckpointColumns+`
		FROM workspace_checkpoints WHERE run_id = ? AND workspace_id = ? AND sealed = 1
		ORDER BY created_at DESC, id DESC LIMIT ?`, runID, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]workspacecheckpoint.Checkpoint, 0, limit)
	for rows.Next() {
		value, scanErr := scanWorkspaceCheckpoint(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *SQLiteStore) GetWorkspaceCheckpointSnapshot(ctx context.Context,
	id string,
) (workspacecheckpoint.Snapshot, error) {
	checkpoint, err := s.GetWorkspaceCheckpoint(ctx, id)
	if err != nil {
		return workspacecheckpoint.Snapshot{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT path, kind, worktree_state,
		storage_policy, mode, size_bytes, worktree_sha256, blob_sha256, index_oid,
		index_mode, tracked, staged, binary, line_endings, recoverable, reason
		FROM workspace_checkpoint_entries WHERE checkpoint_id = ? ORDER BY path`, checkpoint.ID)
	if err != nil {
		return workspacecheckpoint.Snapshot{}, err
	}
	entries := make([]workspacecheckpoint.Entry, 0, checkpoint.EntryCount)
	blobIDs := make(map[string]struct{})
	for rows.Next() {
		var entry workspacecheckpoint.Entry
		var tracked, staged, binary, recoverable int
		if err := rows.Scan(&entry.Path, &entry.Kind, &entry.State, &entry.StoragePolicy,
			&entry.Mode, &entry.Size, &entry.WorktreeSHA256, &entry.BlobSHA256,
			&entry.IndexOID, &entry.IndexMode, &tracked, &staged, &binary,
			&entry.LineEndings, &recoverable, &entry.Reason); err != nil {
			rows.Close()
			return workspacecheckpoint.Snapshot{}, err
		}
		entry.Tracked = tracked != 0
		entry.Staged = staged != 0
		entry.Binary = binary != 0
		entry.Recoverable = recoverable != 0
		if entry.BlobSHA256 != "" {
			blobIDs[entry.BlobSHA256] = struct{}{}
		}
		entries = append(entries, entry)
	}
	if err := rows.Close(); err != nil {
		return workspacecheckpoint.Snapshot{}, err
	}
	if checkpoint.IndexBlobSHA256 != "" {
		blobIDs[checkpoint.IndexBlobSHA256] = struct{}{}
	}
	ids := make([]string, 0, len(blobIDs))
	for id := range blobIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	blobs := make([]workspacecheckpoint.Blob, 0, len(ids))
	for _, digest := range ids {
		var content []byte
		var created string
		if err := s.db.QueryRowContext(ctx, `SELECT content, created_at
			FROM workspace_checkpoint_blobs WHERE sha256 = ?`, digest).
			Scan(&content, &created); err != nil {
			return workspacecheckpoint.Snapshot{}, err
		}
		blobs = append(blobs, workspacecheckpoint.Blob{SHA256: digest,
			Content: append([]byte{}, content...), CreatedAt: parseTS(created)})
	}
	snapshot := workspacecheckpoint.Snapshot{Checkpoint: checkpoint,
		Entries: entries, Blobs: blobs}
	if err := snapshot.Validate(); err != nil {
		return workspacecheckpoint.Snapshot{}, fmt.Errorf(
			"persisted workspace checkpoint failed validation: %w", err)
	}
	manifestJSON, err := json.Marshal(entries)
	if err != nil {
		return workspacecheckpoint.Snapshot{}, err
	}
	manifestSHA := sha256.Sum256(manifestJSON)
	if hex.EncodeToString(manifestSHA[:]) != checkpoint.ManifestSHA256 {
		return workspacecheckpoint.Snapshot{}, errors.New(
			"persisted workspace checkpoint manifest digest mismatch")
	}
	return snapshot, nil
}

func (s *SQLiteStore) GarbageCollectWorkspaceCheckpointBlobs(ctx context.Context,
	limit int,
) (int, error) {
	if limit < 1 || limit > 10_000 {
		return 0, apperror.New(apperror.CodeInvalidArgument,
			"workspace checkpoint GC limit is invalid")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM workspace_checkpoint_blobs
		WHERE sha256 IN (SELECT sha256 FROM workspace_checkpoint_blobs
			WHERE reference_count = 0 ORDER BY created_at, sha256 LIMIT ?)`, limit)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	return int(count), err
}

func (s *SQLiteStore) CreateWorkspaceCheckpointTransaction(ctx context.Context,
	value workspacecheckpoint.Transaction,
) (workspacecheckpoint.Transaction, bool, error) {
	if value.ConflictJSON == "" {
		value.ConflictJSON = "[]"
	}
	if err := value.Validate(); err != nil {
		return workspacecheckpoint.Transaction{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workspacecheckpoint.Transaction{}, false, err
	}
	defer tx.Rollback()
	if isWorkspaceRestoreTransaction(value.Kind) {
		// Replay before checking current authority: a completed operation stays
		// replayable after its Run resumes. A fresh restore reserves the paused
		// Run in the same write transaction that checks execution ownership.
		existing, lookupErr := scanWorkspaceCheckpointTransaction(tx.QueryRowContext(ctx,
			`SELECT `+workspaceCheckpointTransactionColumns+`
			FROM workspace_checkpoint_transactions WHERE operation_key_digest = ?`, value.OperationKeyDigest))
		if lookupErr == nil {
			sameIntent := sameWorkspaceCheckpointTransactionIntent(existing, value)
			if workspacecheckpoint.IsThreadUndo(existing) && workspacecheckpoint.IsThreadUndo(value) {
				sameIntent = existing.RunID == value.RunID && existing.WorkspaceID == value.WorkspaceID && existing.TriggerReceiptID == value.TriggerReceiptID && existing.RequestFingerprint == value.RequestFingerprint
			}
			if !sameIntent {
				return workspacecheckpoint.Transaction{}, false, apperror.New(
					apperror.CodeConflict, "workspace checkpoint operation key was reused")
			}
			if err := tx.Commit(); err != nil {
				return workspacecheckpoint.Transaction{}, false, err
			}
			return existing, true, nil
		}
		if !errors.Is(lookupErr, sql.ErrNoRows) {
			return workspacecheckpoint.Transaction{}, false, lookupErr
		}
		if workspacecheckpoint.IsThreadUndo(value) {
			if err := requireThreadChangeUndoJournalTx(ctx, tx, value); err != nil {
				return workspacecheckpoint.Transaction{}, false, err
			}
		} else {
			var pausedAndActive bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM runs run JOIN sessions session_record ON session_record.id = run.session_id
			WHERE run.id = ? AND run.status = 'paused' AND session_record.status = 'active'
				AND session_record.workspace_id = ?)`, value.RunID, value.WorkspaceID).Scan(&pausedAndActive); err != nil {
				return workspacecheckpoint.Transaction{}, false, err
			}
			if !pausedAndActive {
				if hasBindings, err := hasRunFileDrydockBindings(ctx, tx); err != nil {
					return workspacecheckpoint.Transaction{}, false, err
				} else if hasBindings {
					if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
					SELECT 1 FROM runs run JOIN sessions linked ON linked.id=run.session_id
					JOIN run_file_drydock_bindings owner ON owner.run_id=run.id AND owner.session_id=linked.id
					JOIN drydock_workspaces d ON d.id=owner.drydock_id
					LEFT JOIN threads thread ON thread.id=owner.thread_id
					WHERE run.id=? AND run.status='paused' AND linked.status='active'
					AND linked.workspace_id=owner.source_workspace_id AND owner.workspace_id=?
					AND d.state<>'cleaned' AND (owner.thread_id='' OR thread.last_run_id=run.id))`,
						value.RunID, value.WorkspaceID).Scan(&pausedAndActive); err != nil {
						return workspacecheckpoint.Transaction{}, false, err
					}
					if pausedAndActive {
						var busy bool
						if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
						SELECT 1 FROM run_file_drydock_bindings current
						WHERE current.run_id=? AND current.workspace_id=? AND (
						EXISTS(SELECT 1 FROM run_file_drydock_bindings other JOIN run_execution_leases lease ON lease.run_id=other.run_id
						 WHERE other.drydock_id=current.drydock_id AND lease.status='active' AND julianday(lease.expires_at)>julianday(?))
						OR EXISTS(SELECT 1 FROM workspace_checkpoint_transactions pending
						 WHERE pending.workspace_id=current.workspace_id AND pending.status IN ('prepared','applying'))
						OR EXISTS(SELECT 1 FROM drydock_cleanup_operations cleanup
						 WHERE cleanup.drydock_id=current.drydock_id AND cleanup.status='prepared')))`,
							value.RunID, value.WorkspaceID, ts(time.Now().UTC())).Scan(&busy); err != nil {
							return workspacecheckpoint.Transaction{}, false, err
						}
						if busy {
							return workspacecheckpoint.Transaction{}, false, apperror.New(apperror.CodeConflict,
								"Thread working directory has an unfinished execution, mutation, or cleanup")
						}
					}
				}
			}
			if !pausedAndActive {
				return workspacecheckpoint.Transaction{}, false, apperror.New(apperror.CodeFailedPrecondition,
					"workspace restore preparation requires a paused Run and active Session")
			}
			if err := requireNoActiveRunControlLeaseTx(ctx, tx, value.RunID, time.Now().UTC()); err != nil {
				return workspacecheckpoint.Transaction{}, false, err
			}
		}
	}
	err = insertWorkspaceCheckpointTransactionTx(ctx, tx, value)
	if err == nil {
		missionID, lookupErr := workspaceCheckpointMissionID(ctx, tx, value.RunID)
		if lookupErr != nil {
			return workspacecheckpoint.Transaction{}, false, lookupErr
		}
		event, eventErr := events.New(value.RunID, missionID,
			events.WorkspaceCheckpointTransactionPreparedEvent, "workspace_checkpoint",
			value.ID, workspaceCheckpointTransactionEventPayload(value))
		if eventErr != nil {
			return workspacecheckpoint.Transaction{}, false, eventErr
		}
		event.CreatedAt = value.CreatedAt
		if _, eventErr = insertRunEventTx(ctx, tx, event); eventErr != nil {
			return workspacecheckpoint.Transaction{}, false, eventErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return workspacecheckpoint.Transaction{}, false, commitErr
		}
		return value, false, nil
	}
	_ = tx.Rollback()
	existing, found, getErr := s.GetWorkspaceCheckpointTransactionByOperation(ctx,
		value.OperationKeyDigest)
	if getErr != nil {
		return workspacecheckpoint.Transaction{}, false,
			normalizeWorkspaceCheckpointStoreError(err)
	}
	if !found {
		return workspacecheckpoint.Transaction{}, false,
			normalizeWorkspaceCheckpointStoreError(err)
	}
	if !sameWorkspaceCheckpointTransactionIntent(existing, value) {
		return workspacecheckpoint.Transaction{}, false, apperror.New(
			apperror.CodeConflict, "workspace checkpoint operation key was reused")
	}
	return existing, true, nil
}

func isWorkspaceRestoreTransaction(kind workspacecheckpoint.TransactionKind) bool {
	return kind == workspacecheckpoint.TransactionRewind ||
		kind == workspacecheckpoint.TransactionUndo || kind == workspacecheckpoint.TransactionRedo ||
		kind == workspacecheckpoint.TransactionFork
}

// A restore's durable prepared/applying state owns the Run until filesystem
// recovery finishes. Resume and lease acquisition check this in their own write
// transactions, so neither can enter between restore preparation and file IO.
func requireNoOpenWorkspaceRestoreTx(ctx context.Context, tx *sql.Tx, runID string) error {
	// Historical stores use the mission workspace before physical owner bindings exist.
	workspaceQuery := `SELECT mission.workspace_id FROM runs run
	 JOIN missions mission ON mission.id=run.mission_id WHERE run.id=?`
	if hasBindings, err := hasRunFileDrydockBindings(ctx, tx); err != nil {
		return err
	} else if hasBindings {
		workspaceQuery = `SELECT COALESCE(owner.workspace_id,mission.workspace_id)
	 FROM runs run JOIN missions mission ON mission.id=run.mission_id
	 LEFT JOIN run_file_drydock_bindings owner ON owner.run_id=run.id WHERE run.id=?`
	}
	var pending bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM workspace_checkpoint_transactions pending
		WHERE (pending.run_id = ? OR (pending.trigger_receipt_id LIKE 'thread-reply-undo:%'
		 AND pending.workspace_id=(`+workspaceQuery+`)))
			AND kind IN ('rewind', 'undo', 'redo', 'fork') AND status IN ('prepared', 'applying'))`,
		runID, runID).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return apperror.New(apperror.CodeConflict,
			"Run execution is reserved by an unfinished workspace restore")
	}
	return nil
}

func (s *SQLiteStore) UpdateWorkspaceCheckpointTransaction(ctx context.Context,
	value workspacecheckpoint.Transaction,
) (workspacecheckpoint.Transaction, bool, error) {
	if value.ConflictJSON == "" {
		value.ConflictJSON = "[]"
	}
	if err := value.Validate(); err != nil {
		return workspacecheckpoint.Transaction{}, false, err
	}
	current, found, err := s.GetWorkspaceCheckpointTransaction(ctx, value.ID)
	if err != nil || !found {
		return workspacecheckpoint.Transaction{}, false, err
	}
	if !sameWorkspaceCheckpointTransactionIntent(current, value) {
		return workspacecheckpoint.Transaction{}, false, apperror.New(
			apperror.CodeConflict, "workspace checkpoint transaction binding changed")
	}
	if sameWorkspaceCheckpointTransaction(current, value) {
		return current, true, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workspacecheckpoint.Transaction{}, false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE workspace_checkpoint_transactions
		SET after_checkpoint_id = ?, status = ?, recovery_level = ?, error_code = ?,
			conflict_json = ?, updated_at = ?, completed_at = ?
		WHERE id = ? AND status = ? AND updated_at = ?`, value.AfterCheckpointID,
		value.Status, value.RecoveryLevel, value.ErrorCode, value.ConflictJSON,
		ts(value.UpdatedAt), nullableWorkspaceCheckpointTime(value.CompletedAt), value.ID,
		current.Status, ts(current.UpdatedAt))
	if err != nil {
		return workspacecheckpoint.Transaction{}, false,
			normalizeWorkspaceCheckpointStoreError(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return workspacecheckpoint.Transaction{}, false, err
	}
	if count != 1 {
		return workspacecheckpoint.Transaction{}, false, apperror.New(
			apperror.CodeConflict, "workspace checkpoint transaction changed concurrently")
	}
	if value.Status.Terminal() {
		missionID, lookupErr := workspaceCheckpointMissionID(ctx, tx, value.RunID)
		if lookupErr != nil {
			return workspacecheckpoint.Transaction{}, false, lookupErr
		}
		eventType := events.WorkspaceCheckpointTransactionCompletedEvent
		if value.Status != workspacecheckpoint.TransactionCompleted {
			eventType = events.WorkspaceCheckpointTransactionFailedEvent
		}
		event, eventErr := events.New(value.RunID, missionID, eventType,
			"workspace_checkpoint", value.ID,
			workspaceCheckpointTransactionEventPayload(value))
		if eventErr != nil {
			return workspacecheckpoint.Transaction{}, false, eventErr
		}
		event.CreatedAt = value.UpdatedAt
		if _, eventErr = insertRunEventTx(ctx, tx, event); eventErr != nil {
			return workspacecheckpoint.Transaction{}, false, eventErr
		}
	}
	if err := tx.Commit(); err != nil {
		return workspacecheckpoint.Transaction{}, false, err
	}
	return value, false, nil
}

func (s *SQLiteStore) GetWorkspaceCheckpointTransaction(ctx context.Context,
	id string,
) (workspacecheckpoint.Transaction, bool, error) {
	value, err := scanWorkspaceCheckpointTransaction(s.db.QueryRowContext(ctx,
		`SELECT `+workspaceCheckpointTransactionColumns+`
		FROM workspace_checkpoint_transactions WHERE id = ?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return workspacecheckpoint.Transaction{}, false, nil
	}
	return value, err == nil, err
}

func (s *SQLiteStore) GetWorkspaceCheckpointTransactionByOperation(ctx context.Context,
	digest string,
) (workspacecheckpoint.Transaction, bool, error) {
	value, err := scanWorkspaceCheckpointTransaction(s.db.QueryRowContext(ctx,
		`SELECT `+workspaceCheckpointTransactionColumns+`
		FROM workspace_checkpoint_transactions WHERE operation_key_digest = ?`,
		strings.TrimSpace(digest)))
	if errors.Is(err, sql.ErrNoRows) {
		return workspacecheckpoint.Transaction{}, false, nil
	}
	return value, err == nil, err
}

func (s *SQLiteStore) ListOpenWorkspaceCheckpointTransactions(ctx context.Context,
	limit int,
) ([]workspacecheckpoint.Transaction, error) {
	if limit < 1 || limit > 2_000 {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"workspace checkpoint transaction list limit is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+workspaceCheckpointTransactionColumns+`
		FROM workspace_checkpoint_transactions WHERE status IN ('prepared', 'applying')
		ORDER BY updated_at, id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]workspacecheckpoint.Transaction, 0, limit)
	for rows.Next() {
		value, err := scanWorkspaceCheckpointTransaction(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// ListWorkspaceCheckpointTransactionsPendingCursor finds the narrow crash
// window where a non-Fork transaction became terminal but its Run cursor still
// points at the transaction's before checkpoint. Fork intentionally never moves
// the source Run cursor and is excluded.
func (s *SQLiteStore) ListWorkspaceCheckpointTransactionsPendingCursor(ctx context.Context,
	limit int,
) ([]workspacecheckpoint.Transaction, error) {
	if limit < 1 || limit > 2_000 {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"workspace checkpoint pending-cursor list limit is invalid")
	}
	ownedCursor := ""
	var hasDrydocks bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM sqlite_master
		WHERE type='table' AND name='drydock_workspaces')`).Scan(&hasDrydocks); err != nil {
		return nil, err
	}
	if hasDrydocks {
		ownedCursor = ` OR EXISTS (SELECT 1 FROM drydock_workspaces d
			JOIN runs r ON r.id = d.run_id JOIN missions m ON m.id = r.mission_id
			WHERE d.run_id = transaction_record.run_id AND d.mission_id = m.id
			AND d.session_id = r.session_id AND d.source_workspace_id = m.workspace_id
			AND d.workspace_id = transaction_record.workspace_id
			AND d.last_checkpoint_id = transaction_record.before_checkpoint_id
			AND transaction_record.kind = 'file_tool')`
	}
	if hasBindings, err := hasRunFileDrydockBindings(ctx, s.db); err != nil {
		return nil, err
	} else if hasBindings {
		ownedCursor = ` OR EXISTS (SELECT 1 FROM run_file_drydock_bindings owner
 JOIN drydock_workspaces d ON d.id=owner.drydock_id
 LEFT JOIN threads thread ON thread.id=owner.thread_id
 WHERE owner.run_id=transaction_record.run_id AND owner.workspace_id=transaction_record.workspace_id
 AND (owner.thread_id='' OR thread.last_run_id=owner.run_id)
 AND d.last_checkpoint_id=transaction_record.before_checkpoint_id AND transaction_record.kind='file_tool')`
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+workspaceCheckpointTransactionColumns+`
		FROM workspace_checkpoint_transactions WHERE id IN (
			SELECT transaction_record.id
			FROM workspace_checkpoint_transactions transaction_record
			WHERE transaction_record.status IN ('completed', 'failed', 'interrupted')
				AND transaction_record.kind != 'fork'
				AND transaction_record.trigger_receipt_id NOT LIKE 'thread-reply-undo:%'
				AND transaction_record.after_checkpoint_id != ''
				AND (EXISTS (SELECT 1 FROM workspace_checkpoint_run_state state
					WHERE state.run_id = transaction_record.run_id
					AND state.workspace_id = transaction_record.workspace_id
					AND state.current_checkpoint_id = transaction_record.before_checkpoint_id
					AND state.last_transaction_id = '')`+ownedCursor+`))
		ORDER BY completed_at, id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]workspacecheckpoint.Transaction, 0, limit)
	for rows.Next() {
		value, scanErr := scanWorkspaceCheckpointTransaction(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *SQLiteStore) ListWorkspaceCheckpointTransactions(ctx context.Context,
	runID string, limit int,
) ([]workspacecheckpoint.Transaction, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" || limit < 1 || limit > 2_000 {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"workspace checkpoint transaction list request is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+workspaceCheckpointTransactionColumns+`
		FROM workspace_checkpoint_transactions WHERE run_id = ?
		ORDER BY created_at DESC, id DESC LIMIT ?`, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]workspacecheckpoint.Transaction, 0, limit)
	for rows.Next() {
		value, err := scanWorkspaceCheckpointTransaction(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *SQLiteStore) ListWorkspaceCheckpointTransactionsByWorkspace(
	ctx context.Context, runID, workspaceID string, limit int,
) ([]workspacecheckpoint.Transaction, error) {
	runID, workspaceID = strings.TrimSpace(runID), strings.TrimSpace(workspaceID)
	if runID == "" || workspaceID == "" || limit < 1 || limit > 2_000 {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"workspace checkpoint scoped transaction list request is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+workspaceCheckpointTransactionColumns+`
		FROM workspace_checkpoint_transactions WHERE run_id = ? AND workspace_id = ?
		ORDER BY created_at DESC, id DESC LIMIT ?`, runID, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]workspacecheckpoint.Transaction, 0, limit)
	for rows.Next() {
		value, scanErr := scanWorkspaceCheckpointTransaction(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *SQLiteStore) WorkspaceCheckpointStorageUsage(ctx context.Context) (
	workspacecheckpoint.StorageUsage, error,
) {
	var value workspacecheckpoint.StorageUsage
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(size_bytes), 0)
		FROM workspace_checkpoint_blobs`).Scan(&value.BlobCount, &value.BlobBytes); err != nil {
		return workspacecheckpoint.StorageUsage{}, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_checkpoints
		WHERE sealed = 1`).Scan(&value.CheckpointCount); err != nil {
		return workspacecheckpoint.StorageUsage{}, err
	}
	return value, value.Validate()
}

func (s *SQLiteStore) GetWorkspaceCheckpointRunState(ctx context.Context,
	runID string,
) (workspacecheckpoint.RunState, bool, error) {
	var value workspacecheckpoint.RunState
	var updated string
	err := s.db.QueryRowContext(ctx, `SELECT run_id, workspace_id,
		current_checkpoint_id, last_transaction_id, updated_at
		FROM workspace_checkpoint_run_state WHERE run_id = ?`, strings.TrimSpace(runID)).
		Scan(&value.RunID, &value.WorkspaceID, &value.CurrentCheckpointID,
			&value.LastTransactionID, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return workspacecheckpoint.RunState{}, false, nil
	}
	if err != nil {
		return workspacecheckpoint.RunState{}, false, err
	}
	value.UpdatedAt = parseTS(updated)
	if err := value.Validate(); err != nil {
		return workspacecheckpoint.RunState{}, false, err
	}
	return value, true, nil
}

// AdvanceWorkspaceCheckpointRunState is the durable compare-and-swap cursor.
// expectedCheckpointID must be empty only for the first checkpoint of a Run.
func (s *SQLiteStore) AdvanceWorkspaceCheckpointRunState(ctx context.Context,
	value workspacecheckpoint.RunState, expectedCheckpointID string,
) (workspacecheckpoint.RunState, bool, error) {
	if err := value.Validate(); err != nil {
		return workspacecheckpoint.RunState{}, false, err
	}
	if expectedCheckpointID != "" &&
		(expectedCheckpointID != strings.TrimSpace(expectedCheckpointID) ||
			len([]rune(expectedCheckpointID)) > 256 || strings.ContainsRune(expectedCheckpointID, 0)) {
		return workspacecheckpoint.RunState{}, false, apperror.New(
			apperror.CodeInvalidArgument, "workspace checkpoint cursor expectation is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workspacecheckpoint.RunState{}, false, err
	}
	defer tx.Rollback()
	var current workspacecheckpoint.RunState
	var updated string
	err = tx.QueryRowContext(ctx, `SELECT run_id, workspace_id, current_checkpoint_id,
		last_transaction_id, updated_at FROM workspace_checkpoint_run_state WHERE run_id = ?`,
		value.RunID).Scan(&current.RunID, &current.WorkspaceID, &current.CurrentCheckpointID,
		&current.LastTransactionID, &updated)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if expectedCheckpointID != "" {
			return workspacecheckpoint.RunState{}, false, apperror.New(
				apperror.CodeConflict, "workspace checkpoint cursor is absent")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_checkpoint_run_state
			(run_id, workspace_id, current_checkpoint_id, last_transaction_id, updated_at)
			VALUES (?, ?, ?, ?, ?)`, value.RunID, value.WorkspaceID,
			value.CurrentCheckpointID, value.LastTransactionID, ts(value.UpdatedAt)); err != nil {
			return workspacecheckpoint.RunState{}, false,
				normalizeWorkspaceCheckpointStoreError(err)
		}
	case err != nil:
		return workspacecheckpoint.RunState{}, false, err
	default:
		current.UpdatedAt = parseTS(updated)
		if err := current.Validate(); err != nil {
			return workspacecheckpoint.RunState{}, false, err
		}
		if current.WorkspaceID != value.WorkspaceID ||
			current.CurrentCheckpointID != expectedCheckpointID {
			return workspacecheckpoint.RunState{}, false, apperror.New(
				apperror.CodeConflict, "workspace checkpoint cursor changed concurrently")
		}
		if current.CurrentCheckpointID == value.CurrentCheckpointID &&
			current.LastTransactionID == value.LastTransactionID {
			return current, true, nil
		}
		result, updateErr := tx.ExecContext(ctx, `UPDATE workspace_checkpoint_run_state
			SET current_checkpoint_id = ?, last_transaction_id = ?, updated_at = ?
			WHERE run_id = ? AND workspace_id = ? AND current_checkpoint_id = ?
				AND updated_at = ?`, value.CurrentCheckpointID, value.LastTransactionID,
			ts(value.UpdatedAt), value.RunID, value.WorkspaceID, expectedCheckpointID,
			ts(current.UpdatedAt))
		if updateErr != nil {
			return workspacecheckpoint.RunState{}, false,
				normalizeWorkspaceCheckpointStoreError(updateErr)
		}
		count, updateErr := result.RowsAffected()
		if updateErr != nil || count != 1 {
			if updateErr != nil {
				return workspacecheckpoint.RunState{}, false, updateErr
			}
			return workspacecheckpoint.RunState{}, false, apperror.New(
				apperror.CodeConflict, "workspace checkpoint cursor changed concurrently")
		}
	}
	if err := tx.Commit(); err != nil {
		return workspacecheckpoint.RunState{}, false, err
	}
	return value, false, nil
}

func (s *SQLiteStore) GetWorkspaceCheckpointInvocationAttempt(ctx context.Context,
	runID, invocationID string,
) (string, bool, error) {
	runID, invocationID = strings.TrimSpace(runID), strings.TrimSpace(invocationID)
	if runID == "" || invocationID == "" {
		return "", false, apperror.New(apperror.CodeInvalidArgument,
			"workspace checkpoint invocation binding is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT attempt_id
		FROM run_supervisor_tool_calls WHERE run_id = ? AND call_id = ? LIMIT 2`,
		runID, invocationID)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	values := make([]string, 0, 2)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return "", false, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return "", false, err
	}
	if len(values) == 0 {
		return "", false, nil
	}
	if len(values) != 1 {
		return "", false, apperror.New(apperror.CodeConflict,
			"workspace checkpoint invocation has ambiguous attempts")
	}
	return values[0], true, nil
}

func workspaceCheckpointMissionID(ctx context.Context, queryer workspaceCheckpointQueryer,
	runID string,
) (string, error) {
	var missionID string
	if err := queryer.QueryRowContext(ctx, `SELECT mission_id FROM runs WHERE id = ?`,
		runID).Scan(&missionID); err != nil {
		return "", err
	}
	return missionID, nil
}

func workspaceCheckpointTransactionEventPayload(value workspacecheckpoint.Transaction) map[string]any {
	var conflicts []workspacecheckpoint.Conflict
	_ = json.Unmarshal([]byte(value.ConflictJSON), &conflicts)
	return map[string]any{"transaction_id": value.ID, "kind": value.Kind,
		"workspace_id": value.WorkspaceID, "trigger_receipt_id": value.TriggerReceiptID,
		"before_checkpoint_id":           value.BeforeCheckpointID,
		"after_checkpoint_id":            value.AfterCheckpointID,
		"expected_current_checkpoint_id": value.ExpectedCurrentCheckpointID,
		"target_checkpoint_id":           value.TargetCheckpointID, "status": value.Status,
		"recovery_level": value.RecoveryLevel, "error_code": value.ErrorCode,
		"conflict_count": len(conflicts)}
}

func getWorkspaceCheckpoint(ctx context.Context, queryer workspaceCheckpointQueryer,
	id string,
) (workspacecheckpoint.Checkpoint, error) {
	return scanWorkspaceCheckpoint(queryer.QueryRowContext(ctx,
		`SELECT `+workspaceCheckpointColumns+`
		FROM workspace_checkpoints WHERE id = ? AND sealed = 1`, id))
}

type workspaceCheckpointScanner interface {
	Scan(...any) error
}

func scanWorkspaceCheckpoint(scanner workspaceCheckpointScanner) (
	workspacecheckpoint.Checkpoint, error,
) {
	var value workspacecheckpoint.Checkpoint
	var reasonsJSON, created string
	if err := scanner.Scan(&value.ID, &value.ProtocolVersion, &value.RunID,
		&value.MissionID, &value.SessionID, &value.WorkspaceID, &value.AttemptID,
		&value.CapabilityGeneration, &value.Trigger, &value.Phase,
		&value.TriggerReceiptID, &value.RequestedBy, &value.Title,
		&value.ParentCheckpointID, &value.RootFingerprint,
		&value.RootPathSHA256, &value.BaseCommit, &value.Branch, &value.IndexSHA256,
		&value.IndexBlobSHA256, &value.ManifestSHA256, &value.RecoveryLevel,
		&reasonsJSON, &value.EntryCount, &value.StoredBytes, &created); err != nil {
		return workspacecheckpoint.Checkpoint{}, err
	}
	if err := json.Unmarshal([]byte(reasonsJSON), &value.IncompleteReasons); err != nil {
		return workspacecheckpoint.Checkpoint{}, err
	}
	value.CreatedAt = parseTS(created)
	return value, value.Validate()
}

func scanWorkspaceCheckpointTransaction(scanner workspaceCheckpointScanner) (
	workspacecheckpoint.Transaction, error,
) {
	var value workspacecheckpoint.Transaction
	var created, updated string
	var completed sql.NullString
	if err := scanner.Scan(&value.ID, &value.ProtocolVersion, &value.OperationKeyDigest,
		&value.RequestFingerprint, &value.RunID, &value.WorkspaceID, &value.Kind,
		&value.TriggerReceiptID, &value.BeforeCheckpointID, &value.AfterCheckpointID,
		&value.ExpectedCurrentCheckpointID, &value.TargetCheckpointID,
		&value.ForkWorkspaceRoot, &value.ForkBranch, &value.Status,
		&value.RecoveryLevel, &value.ErrorCode, &value.ConflictJSON, &created, &updated,
		&completed); err != nil {
		return workspacecheckpoint.Transaction{}, err
	}
	value.CreatedAt = parseTS(created)
	value.UpdatedAt = parseTS(updated)
	if completed.Valid {
		at := parseTS(completed.String)
		value.CompletedAt = &at
	}
	return value, value.Validate()
}

func sameWorkspaceCheckpoint(left, right workspacecheckpoint.Checkpoint) bool {
	return left.ID == right.ID && left.ProtocolVersion == right.ProtocolVersion &&
		left.RunID == right.RunID && left.MissionID == right.MissionID &&
		left.SessionID == right.SessionID && left.WorkspaceID == right.WorkspaceID &&
		left.AttemptID == right.AttemptID &&
		left.CapabilityGeneration == right.CapabilityGeneration &&
		left.Trigger == right.Trigger && left.Phase == right.Phase &&
		left.TriggerReceiptID == right.TriggerReceiptID &&
		left.RequestedBy == right.RequestedBy && left.Title == right.Title &&
		left.ParentCheckpointID == right.ParentCheckpointID &&
		left.RootFingerprint == right.RootFingerprint &&
		left.RootPathSHA256 == right.RootPathSHA256 &&
		left.BaseCommit == right.BaseCommit && left.Branch == right.Branch &&
		left.IndexSHA256 == right.IndexSHA256 &&
		left.IndexBlobSHA256 == right.IndexBlobSHA256 &&
		left.ManifestSHA256 == right.ManifestSHA256 &&
		left.RecoveryLevel == right.RecoveryLevel && left.EntryCount == right.EntryCount &&
		left.StoredBytes == right.StoredBytes &&
		strings.Join(left.IncompleteReasons, "\x00") == strings.Join(right.IncompleteReasons, "\x00")
}

func sameWorkspaceCheckpointTransactionIntent(left,
	right workspacecheckpoint.Transaction,
) bool {
	return left.ProtocolVersion == right.ProtocolVersion &&
		left.OperationKeyDigest == right.OperationKeyDigest &&
		left.RequestFingerprint == right.RequestFingerprint && left.RunID == right.RunID &&
		left.WorkspaceID == right.WorkspaceID && left.Kind == right.Kind &&
		left.TriggerReceiptID == right.TriggerReceiptID &&
		left.BeforeCheckpointID == right.BeforeCheckpointID &&
		left.ExpectedCurrentCheckpointID == right.ExpectedCurrentCheckpointID &&
		left.TargetCheckpointID == right.TargetCheckpointID &&
		left.ForkWorkspaceRoot == right.ForkWorkspaceRoot && left.ForkBranch == right.ForkBranch
}

func sameWorkspaceCheckpointTransaction(left, right workspacecheckpoint.Transaction) bool {
	if !sameWorkspaceCheckpointTransactionIntent(left, right) {
		return false
	}
	if left.AfterCheckpointID != right.AfterCheckpointID || left.Status != right.Status ||
		left.RecoveryLevel != right.RecoveryLevel || left.ErrorCode != right.ErrorCode ||
		left.ConflictJSON != right.ConflictJSON || !left.UpdatedAt.Equal(right.UpdatedAt) {
		return false
	}
	if left.CompletedAt == nil || right.CompletedAt == nil {
		return left.CompletedAt == nil && right.CompletedAt == nil
	}
	return left.CompletedAt.Equal(*right.CompletedAt)
}

func nullableWorkspaceCheckpointTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return ts(value.UTC())
}

func normalizeWorkspaceCheckpointStoreError(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "workspace checkpoint blob store quota exceeded") {
		return apperror.Wrap(apperror.CodeResourceExhausted,
			"workspace checkpoint content store quota is exhausted", err)
	}
	if strings.Contains(message, "workspace checkpoint metadata quota exceeded") ||
		strings.Contains(message, "workspace checkpoint transaction quota exceeded") {
		return apperror.Wrap(apperror.CodeResourceExhausted,
			"workspace checkpoint metadata store quota is exhausted", err)
	}
	if strings.Contains(message, "unique") || strings.Contains(message, "constraint") ||
		strings.Contains(message, "workspace checkpoint") {
		return apperror.Wrap(apperror.CodeConflict,
			"workspace checkpoint persistence conflict", err)
	}
	return err
}

func insertWorkspaceCheckpointTransactionTx(ctx context.Context, tx *sql.Tx, value workspacecheckpoint.Transaction) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO workspace_checkpoint_transactions
		(id, protocol_version, operation_key_digest, request_fingerprint, run_id,
		 workspace_id, kind, trigger_receipt_id, before_checkpoint_id,
		 after_checkpoint_id, expected_current_checkpoint_id, target_checkpoint_id,
		 fork_workspace_root, fork_branch, status, recovery_level, error_code,
		 conflict_json, created_at, updated_at, completed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		value.ID, value.ProtocolVersion, value.OperationKeyDigest, value.RequestFingerprint,
		value.RunID, value.WorkspaceID, value.Kind, value.TriggerReceiptID,
		value.BeforeCheckpointID, value.AfterCheckpointID,
		value.ExpectedCurrentCheckpointID, value.TargetCheckpointID,
		value.ForkWorkspaceRoot, value.ForkBranch, value.Status, value.RecoveryLevel,
		value.ErrorCode, value.ConflictJSON, ts(value.CreatedAt),
		ts(value.UpdatedAt), nullableWorkspaceCheckpointTime(value.CompletedAt))
	return err
}
