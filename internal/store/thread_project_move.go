package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
)

func projectMoveIdentity(r domain.ThreadProjectMoveRequest) (string, string) {
	return runmutation.Fingerprint(domain.ThreadProjectMoveVersion, r.ThreadID, r.OperationKey), runmutation.Fingerprint(domain.ThreadProjectMoveVersion, r.ThreadID, r.ProjectWorkspaceID, strconv.FormatInt(r.ExpectedVersion, 10), r.RequestedBy)
}
func readProjectAssignment(ctx context.Context, q historyQueryer, t *domain.Thread) error {
	// Read sidebar metadata with project membership to avoid a second per-row query.
	err := q.QueryRowContext(ctx, `SELECT p.project_workspace_id,p.version,t.pinned FROM thread_project_assignments p JOIN threads t ON t.id=p.thread_id WHERE p.thread_id=?`, t.ID).Scan(&t.ProjectWorkspaceID, &t.ProjectAssignmentVersion, &t.Pinned)
	if err != nil && strings.Contains(err.Error(), "no such column: t.pinned") {
		// Historical migration-prefix fixtures predate pinned conversations.
		err = q.QueryRowContext(ctx, `SELECT project_workspace_id,version FROM thread_project_assignments WHERE thread_id=?`, t.ID).Scan(&t.ProjectWorkspaceID, &t.ProjectAssignmentVersion)
	}
	// Historical migration-prefix fixtures predate project membership.
	if err != nil && strings.Contains(err.Error(), "no such table: thread_project_assignments") {
		t.ProjectWorkspaceID = t.WorkspaceID
		t.ProjectAssignmentVersion = 1
		return nil
	}
	return err
}
func (s *SQLiteStore) withProjectAssignment(ctx context.Context, t domain.Thread, err error) (domain.Thread, error) {
	if err == nil {
		err = readProjectAssignment(ctx, s.db, &t)
	}
	return t, err
}
func readProjectMove(ctx context.Context, q historyQueryer, r domain.ThreadProjectMoveRequest) (bool, error) {
	key, fingerprint := projectMoveIdentity(r)
	var stored string
	err := q.QueryRowContext(ctx, `SELECT fingerprint FROM thread_project_moves WHERE operation_key=?`, key).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if stored != fingerprint {
		return false, apperror.New(apperror.CodeConflict, "Project move key was already used for different intent")
	}
	return true, nil
}
func (s *SQLiteStore) GetThreadProjectMove(ctx context.Context, r domain.ThreadProjectMoveRequest) (domain.ThreadProjectMoveResult, bool, error) {
	found, err := readProjectMove(ctx, s.db, r)
	if err != nil || !found {
		return domain.ThreadProjectMoveResult{}, found, err
	}
	t, err := s.GetThread(ctx, r.ThreadID)
	return domain.ThreadProjectMoveResult{Thread: t, Replayed: true}, true, err
}

// MoveThreadProject commits a fresh execution graph and its source binding together.
// Historical Runs, Sessions, files, approvals and checkpoints are never reassigned.
func (s *SQLiteStore) MoveThreadProject(ctx context.Context, r domain.ThreadProjectMoveRequest, expectedRun string, expectedSequence int64, mission domain.Mission, run domain.Run, mode domain.RunModeSnapshot, sess session.Session, initial []events.Event) (domain.ThreadProjectMoveResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ThreadProjectMoveResult{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE threads SET updated_at=updated_at WHERE id=?`, r.ThreadID); err != nil {
		return domain.ThreadProjectMoveResult{}, err
	}
	replay, err := readProjectMove(ctx, tx, r)
	if err != nil {
		return domain.ThreadProjectMoveResult{}, err
	}
	t, err := scanThread(tx.QueryRowContext(ctx, threadSelect+` WHERE id=?`, r.ThreadID))
	if err != nil {
		return domain.ThreadProjectMoveResult{}, err
	}
	if err = readProjectAssignment(ctx, tx, &t); err != nil {
		return domain.ThreadProjectMoveResult{}, err
	}
	if replay {
		return domain.ThreadProjectMoveResult{Thread: t, Replayed: true}, tx.Commit()
	}
	if t.ProjectAssignmentVersion != r.ExpectedVersion || t.LastRunID != expectedRun {
		return domain.ThreadProjectMoveResult{}, apperror.New(apperror.CodeConflict, "Chat changed before project move; reload and retry")
	}
	if t.Status != domain.ThreadActive {
		return domain.ThreadProjectMoveResult{}, apperror.New(apperror.CodeFailedPrecondition, "Only an active chat can move projects")
	}
	old, err := scanRun(tx.QueryRowContext(ctx, `SELECT id,mission_id,session_id,status,config_json,budget_json,started_at,finished_at,created_at,updated_at FROM runs WHERE id=?`, t.LastRunID))
	if err != nil {
		return domain.ThreadProjectMoveResult{}, err
	}
	var currentSequence int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM run_events WHERE run_id=?`, old.ID).Scan(&currentSequence); err != nil {
		return domain.ThreadProjectMoveResult{}, err
	}
	if currentSequence != expectedSequence {
		return domain.ThreadProjectMoveResult{}, apperror.New(apperror.CodeConflict, "Chat history changed while preparing the project move; reload and retry")
	}
	now := time.Now().UTC()
	if err = requireThreadProjectMoveIdleTx(ctx, tx, t, old, now); err != nil {
		return domain.ThreadProjectMoveResult{}, err
	}
	switchWorkspace := r.ProjectWorkspaceID != "" && r.ProjectWorkspaceID != t.WorkspaceID
	if switchWorkspace {
		if mission.WorkspaceID != r.ProjectWorkspaceID || run.MissionID != mission.ID || run.ID == "" {
			return domain.ThreadProjectMoveResult{}, apperror.New(apperror.CodeConflict, "Project move target execution is invalid")
		}
		if !old.Terminal() {
			if err = transitionThreadLifecycleRunTx(ctx, tx, &old, domain.RunCancelled, "chat moved to another project", now); err != nil {
				return domain.ThreadProjectMoveResult{}, err
			}
		}
		if err = createRunGraphTx(ctx, tx, mission, run, mode, sess, true, true, initial); err != nil {
			return domain.ThreadProjectMoveResult{}, err
		}
	}
	key, fingerprint := projectMoveIdentity(r)
	var successor any
	if switchWorkspace {
		successor = run.ID
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO thread_project_moves VALUES(?,?,?,?,?,?,?,?)`, key, fingerprint, t.ID, old.ID, successor, r.ProjectWorkspaceID, t.ProjectAssignmentVersion+1, ts(now)); err != nil {
		return domain.ThreadProjectMoveResult{}, err
	}
	if switchWorkspace {
		if _, err = tx.ExecContext(ctx, `UPDATE threads SET workspace_id=?,mission_id=? WHERE id=?`, mission.WorkspaceID, mission.ID, t.ID); err != nil {
			return domain.ThreadProjectMoveResult{}, err
		}
		var ordinal int64
		if err = tx.QueryRowContext(ctx, `SELECT MAX(ordinal)+1 FROM thread_runs WHERE thread_id=?`, t.ID).Scan(&ordinal); err != nil {
			return domain.ThreadProjectMoveResult{}, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO thread_runs(thread_id,run_id,session_id,ordinal,predecessor_run_id,created_at) VALUES(?,?,?,?,?,?)`, t.ID, run.ID, sess.ID, ordinal, old.ID, ts(run.CreatedAt)); err != nil {
			return domain.ThreadProjectMoveResult{}, err
		}
		preference, err := getCurrentThreadExecutionPermissionSnapshot(ctx, tx, t.ID)
		if err != nil {
			return domain.ThreadProjectMoveResult{}, err
		}
		next, err := preference.Next(idgen.New("thread-exec-permission"), domain.RunExecutionPermissionAsk, false, r.RequestedBy, "project move resets execution authorization", now)
		if err != nil {
			return domain.ThreadProjectMoveResult{}, err
		}
		next.MissionID = mission.ID
		if err = insertThreadExecutionPermissionSnapshotTx(ctx, tx, next); err != nil {
			return domain.ThreadProjectMoveResult{}, err
		}
		payload, _ := json.Marshal(map[string]any{"predecessor_run_id": old.ID, "successor_run_id": run.ID, "workspace_changed": true, "authority_inherited": false})
		if _, err = tx.ExecContext(ctx, `INSERT INTO thread_events(thread_id,run_id,type,source,payload_json,created_at) VALUES(?,?,'thread.run_successor_created','thread_continuation',?,?)`, t.ID, run.ID, string(payload), ts(now)); err != nil {
			return domain.ThreadProjectMoveResult{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE thread_project_assignments SET project_workspace_id=?,version=version+1 WHERE thread_id=?`, r.ProjectWorkspaceID, t.ID); err != nil {
		return domain.ThreadProjectMoveResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE threads SET version=version+1,updated_at=? WHERE id=?`, ts(now), t.ID); err != nil {
		return domain.ThreadProjectMoveResult{}, err
	}
	t, err = scanThread(tx.QueryRowContext(ctx, threadSelect+` WHERE id=?`, t.ID))
	if err != nil {
		return domain.ThreadProjectMoveResult{}, err
	}
	if err = readProjectAssignment(ctx, tx, &t); err != nil {
		return domain.ThreadProjectMoveResult{}, err
	}
	return domain.ThreadProjectMoveResult{Thread: t}, tx.Commit()
}
func requireThreadProjectMoveIdleTx(ctx context.Context, tx *sql.Tx, t domain.Thread, run domain.Run, now time.Time) error {
	if run.Status == domain.RunWaitingApproval {
		return apperror.New(apperror.CodeFailedPrecondition, "Wait for the chat approval before moving projects")
	}
	if err := requireThreadLifecycleRunQuiescentTx(ctx, tx, run.ID, now); err != nil {
		return err
	}
	var busy bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM thread_message_intents WHERE thread_id=? AND message_id IS NULL AND rejected=0)
 OR EXISTS(SELECT 1 FROM operator_steering_messages m JOIN thread_runs b ON b.run_id=m.run_id WHERE b.thread_id=? AND m.status='pending')
 OR EXISTS(SELECT 1 FROM tool_approvals WHERE run_id=? AND status='pending')
 OR EXISTS(SELECT 1 FROM workspace_checkpoint_transactions WHERE run_id=? AND status IN ('prepared','applying'))
 OR EXISTS(SELECT 1 FROM run_execution_handoff_operations operation WHERE operation.run_id=? AND NOT EXISTS(SELECT 1 FROM run_execution_handoff_results result WHERE result.operation_id=operation.id))`, t.ID, t.ID, run.ID, run.ID, run.ID).Scan(&busy)
	if err != nil {
		return err
	}
	if busy {
		return apperror.New(apperror.CodeFailedPrecondition, "Chat has pending input, approval, or file work; finish it before moving projects")
	}
	return nil
}
