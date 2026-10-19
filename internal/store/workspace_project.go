package store

import (
	"context"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/session"
)

func (s *SQLiteStore) RenameWorkspace(ctx context.Context, id, name string) (session.WorkspaceRecord, error) {
	name, err := domain.NormalizeWorkspaceName(name)
	if err != nil {
		return session.WorkspaceRecord{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return session.WorkspaceRecord{}, err
	}
	defer tx.Rollback()
	var record session.WorkspaceRecord
	record, err = scanWorkspace(tx.QueryRowContext(ctx, `SELECT id,name,root_path,created_at FROM workspaces WHERE id=? AND NOT EXISTS(SELECT 1 FROM drydock_workspaces WHERE workspace_id=workspaces.id) AND NOT EXISTS(SELECT 1 FROM projectless_workspace_allocations WHERE workspace_id=workspaces.id)`, id))
	if err != nil {
		return record, err
	}
	var taken bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspaces WHERE name=? AND id<>?)`, name, id).Scan(&taken); err != nil {
		return record, err
	}
	if taken {
		return record, apperror.New(apperror.CodeConflict, "A project with this name already exists")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workspaces SET name=? WHERE id=?`, name, id); err != nil {
		return record, err
	}
	record.Name = name
	return record, tx.Commit()
}
func (s *SQLiteStore) WorkspaceThreadCounts(ctx context.Context, id string) (int64, int64, error) {
	var total, active int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN
 r.status='waiting_approval' OR EXISTS(SELECT 1 FROM tool_approvals a WHERE a.run_id=r.id AND a.status='pending')
 OR EXISTS(SELECT 1 FROM run_execution_leases l WHERE l.run_id=r.id AND l.status='active' AND julianday(l.expires_at)>julianday('now'))
 OR EXISTS(SELECT 1 FROM thread_message_intents i WHERE i.thread_id=t.id AND i.message_id IS NULL AND i.rejected=0)
 OR EXISTS(SELECT 1 FROM operator_steering_messages m WHERE m.run_id=r.id AND m.status='pending')
 OR EXISTS(SELECT 1 FROM command_runtime_jobs j WHERE j.run_id=r.id AND j.state IN ('prepared','running','stopping'))
 OR EXISTS(SELECT 1 FROM terminal_sessions s WHERE s.run_id=r.id AND s.state IN ('starting','running'))
 THEN 1 ELSE 0 END),0) FROM threads t JOIN thread_project_assignments p ON p.thread_id=t.id JOIN runs r ON r.id=t.last_run_id WHERE p.project_workspace_id=? AND t.status='active'`, id).Scan(&total, &active)
	return total, active, err
}
