package store

import (
	"context"
	"database/sql"
	"errors"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/session"
)

func ensureProjectlessWorkspaceUnusedTx(ctx context.Context, tx *sql.Tx, workspaceID string) error {
	// Historical migration fixtures can exercise controlled creation before 216.
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='projectless_workspace_allocations')`).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return nil
	}
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM projectless_workspace_allocations allocation
	 JOIN run_creation_operations creation ON creation.workspace_id=allocation.workspace_id
	 WHERE allocation.workspace_id=?)`, workspaceID).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return apperror.New(apperror.CodeConflict, "this local chat directory already belongs to a chat; recover that creation or start a new chat")
	}
	return nil
}

func (s *SQLiteStore) ReserveProjectlessWorkspace(ctx context.Context, key string, candidate session.WorkspaceRecord) (session.WorkspaceRecord, bool, error) {
	if len(key) != 64 || candidate.ID == "" || candidate.RootPath == "" || candidate.CreatedAt.IsZero() {
		return session.WorkspaceRecord{}, false, errors.New("complete chat directory reservation is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return session.WorkspaceRecord{}, false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO projectless_workspace_allocations (operation_key_digest, workspace_id, root_path, created_at) VALUES (?, ?, ?, ?) ON CONFLICT(operation_key_digest) DO NOTHING`, key, candidate.ID, candidate.RootPath, ts(candidate.CreatedAt))
	if err != nil {
		return session.WorkspaceRecord{}, false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return session.WorkspaceRecord{}, false, err
	}
	var record session.WorkspaceRecord
	var created string
	err = tx.QueryRowContext(ctx, `SELECT workspace_id, root_path, created_at FROM projectless_workspace_allocations WHERE operation_key_digest=?`, key).Scan(&record.ID, &record.RootPath, &created)
	if err != nil {
		return session.WorkspaceRecord{}, false, err
	}
	record.Name = "chat-" + key[:32]
	record.CreatedAt = parseTS(created)
	err = tx.Commit()
	return record, n == 0, err
}

func (s *SQLiteStore) CompleteProjectlessWorkspace(ctx context.Context, key string, record session.WorkspaceRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id, root string
	if err = tx.QueryRowContext(ctx, `SELECT workspace_id, root_path FROM projectless_workspace_allocations WHERE operation_key_digest=?`, key).Scan(&id, &root); err != nil {
		return err
	}
	if id != record.ID || root != record.RootPath {
		return errors.New("chat directory reservation changed")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workspaces(id,name,root_path,created_at) VALUES(?,?,?,?) ON CONFLICT(id) DO NOTHING`, record.ID, record.Name, record.RootPath, ts(record.CreatedAt))
	if err != nil {
		return err
	}
	stored, err := scanWorkspace(tx.QueryRowContext(ctx, `SELECT id,name,root_path,created_at FROM workspaces WHERE id=?`, record.ID))
	if err != nil {
		return err
	}
	if stored != record {
		return errors.New("chat directory registration conflicts with its reservation")
	}
	return tx.Commit()
}

// Used by project assignment validation without exposing these private roots.
func (s *SQLiteStore) IsProjectlessWorkspace(ctx context.Context, id string) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM projectless_workspace_allocations WHERE workspace_id=?)`, id).Scan(&exists)
	return exists != 0, err
}
