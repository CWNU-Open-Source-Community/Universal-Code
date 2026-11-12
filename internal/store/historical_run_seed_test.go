package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/session"
)

// Historical tests cannot use the retired public five-mode writer. This private
// fixture writes v1 tuples under the actual old schema and immutable triggers.
// Current databases always use the unmodified public RunService/SQLiteStore.
func newMigrationFixtureRunService(t testing.TB, state *SQLiteStore) *application.RunService {
	t.Helper()
	version, err := state.SchemaVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if version >= 230 {
		return application.NewRunService(state)
	}
	if version >= 178 {
		return application.NewRunService(historicalBrowserPreferenceStore{state})
	}
	return application.NewRunService(legacyRunSeedStore{state})
}

type legacyRunSeedStore struct{ *SQLiteStore }

// The historical schema cannot contain a browser network preference. Keep
// fixture setup independent of tables introduced after its pinned version.
type historicalBrowserPreferenceStore struct{ *SQLiteStore }

func (s historicalBrowserPreferenceStore) GetBrowserNetworkPreference(_ context.Context, workspaceID string) (domain.BrowserNetworkPreference, error) {
	return domain.BrowserNetworkPreference{WorkspaceID: workspaceID, BrowserNetworkAccess: domain.BrowserNetworkOff}, nil
}
func (s legacyRunSeedStore) GetBrowserNetworkPreference(_ context.Context, workspaceID string) (domain.BrowserNetworkPreference, error) {
	return domain.BrowserNetworkPreference{WorkspaceID: workspaceID, BrowserNetworkAccess: domain.BrowserNetworkOff}, nil
}

func (s legacyRunSeedStore) CreateMissionRun(ctx context.Context, mission domain.Mission, run domain.Run,
	mode domain.RunModeSnapshot, linkedSession session.Session, createSession bool, initialEvents []events.Event,
) error {
	version, err := s.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if version < 91 || version >= 178 {
		return fmt.Errorf("historical Run fixture rejects schema %d", version)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := seedLegacyRunGraphTx(ctx, tx, mission, run, mode, linkedSession, createSession, true, initialEvents); err != nil {
		return err
	}
	if err := seedLegacyInitialThreadTx(ctx, tx, mission, run); err != nil {
		return err
	}
	return tx.Commit()
}

// Frozen from 6a703079's graph and Thread persistence, with only initial
// permission tuples replaced by v1 conservative records. Never relax SQL
// constraints or route current production writes through this test-only code.
func seedLegacyRunGraphTx(ctx context.Context, tx *sql.Tx, mission domain.Mission, run domain.Run,
	mode domain.RunModeSnapshot, linkedSession session.Session, createSession bool,
	createMission bool, initialEvents []events.Event,
) error {
	mission.Goal = redact.String(mission.Goal)
	linkedSession.Title = redact.String(linkedSession.Title)
	if err := mission.Validate(); err != nil {
		return err
	}
	if err := run.Validate(); err != nil {
		return err
	}
	if err := linkedSession.Validate(); err != nil {
		return err
	}
	if run.Status != domain.RunCreated {
		return errors.New("new run must start in created status")
	}
	if run.MissionID != mission.ID || run.SessionID != linkedSession.ID {
		return errors.New("mission, run, and session identities do not match")
	}
	if mission.WorkspaceID != linkedSession.WorkspaceID {
		return errors.New("mission and session workspaces do not match")
	}
	if len(initialEvents) == 0 {
		return errors.New("initial run events are required")
	}
	for _, event := range initialEvents {
		if event.RunID != run.ID || event.MissionID != mission.ID {
			return errors.New("mission, run, and event identities do not match")
		}
		if err := event.Validate(); err != nil {
			return err
		}
	}
	scopeJSON, err := marshalRedactedJSON(mission.Scope)
	if err != nil {
		return err
	}
	configJSON, err := marshalRedactedJSON(run.Config)
	if err != nil {
		return err
	}
	budgetJSON, err := marshalRedactedJSON(run.Budget)
	if err != nil {
		return err
	}
	if createSession {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sessions
			(id, workspace_id, title, route, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, linkedSession.ID, linkedSession.WorkspaceID, linkedSession.Title,
			linkedSession.Route, linkedSession.Status, ts(linkedSession.CreatedAt), ts(linkedSession.UpdatedAt)); err != nil {
			return err
		}
	} else {
		result, err := tx.ExecContext(ctx, `UPDATE sessions SET workspace_id = ?, route = ?, updated_at = ?
			WHERE id = ? AND status = ?`, linkedSession.WorkspaceID, linkedSession.Route,
			ts(linkedSession.UpdatedAt), linkedSession.ID, session.StatusActive)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows != 1 {
			return errors.New("active run session was not found")
		}
	}
	if createMission {
		if _, err := tx.ExecContext(ctx, `INSERT INTO missions
			(id, goal, profile, workspace_id, scope_json, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, mission.ID, mission.Goal, mission.Profile, mission.WorkspaceID,
			scopeJSON, ts(mission.CreatedAt), ts(mission.UpdatedAt)); err != nil {
			return err
		}
	} else {
		stored, err := scanMission(tx.QueryRowContext(ctx, `SELECT id, goal, profile, workspace_id,
			scope_json, created_at, updated_at FROM missions WHERE id = ?`, mission.ID))
		if err != nil {
			return err
		}
		if stored.ID != mission.ID || stored.Profile != mission.Profile ||
			stored.WorkspaceID != mission.WorkspaceID {
			return errors.New("successor Run Mission binding changed")
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs
		(id, mission_id, session_id, status, config_json, budget_json, started_at, finished_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, run.ID, run.MissionID, run.SessionID, run.Status,
		configJSON, budgetJSON, nullableTS(run.StartedAt), nullableTS(run.FinishedAt), ts(run.CreatedAt), ts(run.UpdatedAt)); err != nil {
		return err
	}
	if err := insertInitialRunInstructionSnapshotTx(ctx, tx, run, mode.RequestedBy); err != nil {
		return err
	}
	if mission.WorkspaceID != "" {
		snapshot, err := contextmgr.SealContinuitySnapshot(contextmgr.ContinuitySnapshot{
			SourceRunID: run.ID, SourceSessionID: run.SessionID,
			WorkspaceID: mission.WorkspaceID, RecentMessages: []contextmgr.ContinuityMessage{},
			Memories:                       []contextmgr.ContinuityMemoryReference{},
			ProjectConfigFingerprint:       run.Config.ProjectConfigFingerprint,
			ProjectInstructionsFingerprint: run.Config.ProjectInstructionsFingerprint,
			InheritedContext:               []string{}, CreatedAt: run.CreatedAt,
		})
		if err != nil {
			return fmt.Errorf("seal initial continuity snapshot: %w", err)
		}
		node, err := contextmgr.NewContinuityNode(idgen.New("continuity"),
			contextmgr.ContinuityNodeRoot, run.SessionID, run.ID, mission.WorkspaceID,
			"", "", "Run created", "Initial immutable Run context", "run_service",
			snapshot, run.CreatedAt)
		if err != nil {
			return fmt.Errorf("prepare initial continuity node: %w", err)
		}
		if err := insertSessionContinuityNodeTx(ctx, tx, node); err != nil {
			return err
		}
	}
	if err := insertInitialRunModeSnapshotTx(ctx, tx, mode, run, mission); err != nil {
		return err
	}
	executionProfile, err := domain.NewInitialRunExecutionProfileSnapshot(
		idgen.New("run-exec-profile"), run, mission, mode.RequestedBy,
		"initial preview execution profile", run.CreatedAt)
	if err != nil {
		return err
	}
	if err := insertInitialRunExecutionProfileSnapshotTx(ctx, tx, executionProfile, run, mission); err != nil {
		return err
	}
	executionInteraction, err := domain.NewInitialRunExecutionInteractionSnapshot(
		idgen.New("run-exec-interaction"), run, mission, mode, executionProfile,
		mode.RequestedBy, run.CreatedAt)
	if err != nil {
		return err
	}
	if err := insertInitialRunExecutionInteractionSnapshotTx(ctx, tx,
		executionInteraction, run, mission, mode, executionProfile); err != nil {
		return err
	}
	executionPermission, err := domain.NewInitialRunExecutionPermissionSnapshot(
		idgen.New("run-exec-permission"), run, mission, mode.RequestedBy, run.CreatedAt)
	if err != nil {
		return err
	}
	executionPermission, err = executionPermission.Next(executionPermission.ID,
		domain.RunExecutionPermissionConservative, false, mode.RequestedBy,
		"historical initial conservative permission", run.CreatedAt)
	if err != nil {
		return err
	}
	executionPermission.Revision = 1
	if err := insertRunExecutionPermissionSnapshotTx(ctx, tx, executionPermission); err != nil {
		return err
	}
	browserCDPPermission, err := domain.NewInitialRunBrowserCDPPermissionSnapshot(
		idgen.New("run-browser-cdp-permission"), run, mission, mode.RequestedBy,
		run.CreatedAt)
	if err != nil {
		return err
	}
	if err := insertInitialRunBrowserCDPPermissionSnapshotTx(ctx, tx,
		browserCDPPermission, run, mission); err != nil {
		return err
	}
	for _, event := range initialEvents {
		if _, err := insertRunEventTx(ctx, tx, event); err != nil {
			return err
		}
	}
	if err := appendInitialRunModeEventTx(ctx, tx, mode); err != nil {
		return err
	}
	if _, _, _, err := syncRootAgentTx(ctx, tx, run, mission, rootAgentProjection{
		Status: domain.AgentReady,
	}, run.CreatedAt); err != nil {
		return err
	}
	if _, err := createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
		return err
	}
	return nil
}

func seedLegacyInitialThreadTx(ctx context.Context, tx *sql.Tx, mission domain.Mission,
	run domain.Run,
) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'threads'`).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return nil
	}
	threadID := domain.InitialThreadID(run.ID)
	title := redact.String(mission.Goal)
	if _, err := tx.ExecContext(ctx, `INSERT INTO threads
		(id, protocol_version, workspace_id, mission_id, title, status,
		 active_run_id, last_run_id, version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, 0, ?, ?)`, threadID,
		domain.ThreadProtocolVersion, mission.WorkspaceID, mission.ID, title,
		domain.ThreadActive, ts(run.CreatedAt), ts(run.UpdatedAt)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO thread_runs
		(thread_id, run_id, session_id, ordinal, predecessor_run_id, created_at)
		VALUES (?, ?, ?, 1, NULL, ?)`, threadID, run.ID, run.SessionID,
		ts(run.CreatedAt)); err != nil {
		return err
	}
	// Old migration-prefix fixtures intentionally create Runs before v139. A
	// fully migrated database always has this table and records a conservative,
	// non-authorizing Thread preference in the same creation transaction.
	var permissionTableExists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'thread_execution_permission_snapshots'`).
		Scan(&permissionTableExists); err != nil {
		return err
	}
	if permissionTableExists != 0 {
		threadRecord, err := scanThread(tx.QueryRowContext(ctx,
			threadSelect+` WHERE id = ?`, threadID))
		if err != nil {
			return err
		}
		preference, err := domain.NewInitialThreadExecutionPermissionSnapshot(
			idgen.New("thread-exec-permission"), threadRecord, "run_creation", run.CreatedAt)
		if err != nil {
			return err
		}
		preference, err = preference.Next(preference.ID, domain.RunExecutionPermissionConservative,
			false, "run_creation", "historical initial conservative preference", run.CreatedAt)
		if err != nil {
			return err
		}
		preference.Revision = 1
		if err := insertThreadExecutionPermissionSnapshotTx(ctx, tx, preference); err != nil {
			return err
		}
	}
	payload, _ := json.Marshal(map[string]any{"run_id": run.ID, "backfilled": false})
	_, err := tx.ExecContext(ctx, `INSERT INTO thread_events
		(thread_id, run_id, type, source, payload_json, created_at)
		VALUES (?, ?, 'thread.created', 'run_creation', ?, ?)`, threadID, run.ID,
		string(payload), ts(run.CreatedAt))
	return err
}
