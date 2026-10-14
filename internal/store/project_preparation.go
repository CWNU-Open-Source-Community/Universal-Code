package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

const preparationSelect = `SELECT id,run_id,key_digest,request_fingerprint,target,plan_fingerprint,backend,requested_by,request_json,owner_id,permission_revision,state,completed_packages,total_packages,cache_hits,downloaded_bytes,job_id,script_job_id,error,cancel_requested,revision,created_at,updated_at FROM project_preparations`

func scanPreparation(row interface{ Scan(...any) error }) (domain.ProjectPreparationOperation, error) {
	var o domain.ProjectPreparationOperation
	var created, updated string
	err := row.Scan(&o.ID, &o.RunID, &o.KeyDigest, &o.RequestFingerprint, &o.Target, &o.PlanFingerprint, &o.Backend, &o.RequestedBy, &o.RequestJSON, &o.OwnerID, &o.PermissionRevision, &o.State, &o.CompletedPackages, &o.TotalPackages, &o.CacheHits, &o.DownloadedBytes, &o.JobID, &o.ScriptJobID, &o.Error, &o.CancelRequested, &o.Revision, &created, &updated)
	if err != nil {
		return o, err
	}
	if o.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return o, err
	}
	if o.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
		return o, err
	}
	return o, o.Validate()
}

func (s *SQLiteStore) GetProjectPreparation(ctx context.Context, runID, id string) (domain.ProjectPreparationOperation, error) {
	o, err := scanPreparation(s.db.QueryRowContext(ctx, preparationSelect+` WHERE run_id=? AND id=?`, runID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return o, apperror.New(apperror.CodeNotFound, "project preparation was not found")
	}
	return o, err
}

func (s *SQLiteStore) LatestProjectPreparation(ctx context.Context, runID, target string) (domain.ProjectPreparationOperation, bool, error) {
	o, err := scanPreparation(s.db.QueryRowContext(ctx, preparationSelect+` WHERE run_id=? AND target=? ORDER BY created_at DESC,id DESC LIMIT 1`, runID, target))
	if errors.Is(err, sql.ErrNoRows) {
		return o, false, nil
	}
	return o, err == nil, err
}

// A Run admits at most one active preparation, regardless of project target.
func (s *SQLiteStore) ActiveProjectPreparation(ctx context.Context, runID string) (domain.ProjectPreparationOperation, bool, error) {
	o, err := scanPreparation(s.db.QueryRowContext(ctx, preparationSelect+` WHERE run_id=? AND state IN ('pending','downloading','installing')`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return o, false, nil
	}
	return o, err == nil, err
}

// Recovery only retires the exact abandoned revision observed by the caller.
// A concurrent progress/cancellation update or newly acquired lease keeps its
// current record, and new admission still uses the existing Run interlocks.
func (s *SQLiteStore) InterruptStaleProjectPreparation(ctx context.Context, expected domain.ProjectPreparationOperation) (domain.ProjectPreparationOperation, error) {
	if err := expected.Validate(); err != nil {
		return expected, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return expected, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockRunControlTx(ctx, tx, expected.RunID); err != nil {
		return expected, err
	}
	current, err := scanPreparation(tx.QueryRowContext(ctx, preparationSelect+` WHERE run_id=? AND id=?`, expected.RunID, expected.ID))
	if err != nil {
		return current, err
	}
	now := time.Now().UTC()
	if current.Terminal() || current.Revision != expected.Revision || current.OwnerID != expected.OwnerID || now.Sub(current.UpdatedAt) < 30*time.Second {
		return current, tx.Commit()
	}
	if err := requireNoOpenWorkspaceRestoreTx(ctx, tx, current.RunID); err != nil {
		return current, err
	}
	lease, found, err := getRunExecutionLeaseTx(ctx, tx, current.RunID)
	if err != nil {
		return current, err
	}
	if found && lease.ActiveAt(now) {
		return current, tx.Commit()
	}
	current.State = "interrupted"
	current.Error = "上次准备已中断；重新准备将复用已校验的下载"
	current.Revision++
	current.UpdatedAt = now
	result, err := tx.ExecContext(ctx, `UPDATE project_preparations SET state=?,error=?,revision=?,updated_at=? WHERE run_id=? AND id=? AND revision=? AND owner_id=?`, current.State, current.Error, current.Revision, ts(current.UpdatedAt), current.RunID, current.ID, expected.Revision, expected.OwnerID)
	if err != nil {
		return current, err
	}
	if rows, err := result.RowsAffected(); err != nil {
		return current, err
	} else if rows != 1 {
		return current, apperror.New(apperror.CodeConflict, "project preparation changed before recovery")
	}
	return current, tx.Commit()
}

func (s *SQLiteStore) BeginProjectPreparation(ctx context.Context, o domain.ProjectPreparationOperation) (domain.ProjectPreparationOperation, bool, error) {
	if err := o.Validate(); err != nil {
		return o, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return o, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = lockRunControlTx(ctx, tx, o.RunID); err != nil {
		return o, false, err
	}
	current, err := scanPreparation(tx.QueryRowContext(ctx, preparationSelect+` WHERE key_digest=?`, o.KeyDigest))
	if err == nil {
		if current.RequestFingerprint != o.RequestFingerprint {
			return current, false, apperror.New(apperror.CodeConflict, "preparation request key belongs to a different intent")
		}
		return current, true, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return o, false, err
	}
	if err := requireNoActiveRunControlLeaseTx(ctx, tx, o.RunID, o.CreatedAt); err != nil {
		return o, false, err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM project_preparations WHERE run_id=? AND state IN ('pending','downloading','installing')`, o.RunID).Scan(&active); err != nil {
		return o, false, err
	}
	if active != 0 {
		return o, false, apperror.New(apperror.CodeConflict, "a project preparation is already active for this task")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO project_preparations (id,run_id,key_digest,request_fingerprint,target,plan_fingerprint,backend,requested_by,request_json,owner_id,permission_revision,state,completed_packages,total_packages,cache_hits,downloaded_bytes,job_id,script_job_id,error,cancel_requested,revision,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, o.ID, o.RunID, o.KeyDigest, o.RequestFingerprint, o.Target, o.PlanFingerprint, o.Backend, o.RequestedBy, o.RequestJSON, o.OwnerID, o.PermissionRevision, o.State, o.CompletedPackages, o.TotalPackages, o.CacheHits, o.DownloadedBytes, o.JobID, o.ScriptJobID, o.Error, o.CancelRequested, o.Revision, ts(o.CreatedAt), ts(o.UpdatedAt))
	if err != nil {
		return o, false, err
	}
	return o, false, tx.Commit()
}

// Updates are compare-and-swap; cancellation and worker progress can never
// overwrite each other or change the original source authorization.
func (s *SQLiteStore) UpdateProjectPreparation(ctx context.Context, o domain.ProjectPreparationOperation, expectedRevision int64) (domain.ProjectPreparationOperation, error) {
	if err := o.Validate(); err != nil {
		return o, err
	}
	if o.Revision != expectedRevision+1 {
		return o, apperror.New(apperror.CodeInvalidArgument, "preparation revision must advance once")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE project_preparations SET state=?,completed_packages=?,cache_hits=?,downloaded_bytes=?,job_id=?,script_job_id=?,error=?,cancel_requested=?,revision=?,updated_at=? WHERE run_id=? AND id=? AND revision=? AND owner_id=?`, o.State, o.CompletedPackages, o.CacheHits, o.DownloadedBytes, o.JobID, o.ScriptJobID, o.Error, o.CancelRequested, o.Revision, ts(o.UpdatedAt), o.RunID, o.ID, expectedRevision, o.OwnerID)
	if err != nil {
		return o, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return o, err
	}
	if n != 1 {
		return o, apperror.New(apperror.CodeConflict, "project preparation changed; read its current state")
	}
	return o, nil
}
