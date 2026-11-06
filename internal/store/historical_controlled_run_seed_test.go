package store

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/session"
)

// Historical controlled-creation fixtures retain the operation's atomic graph,
// model pin, and original SQL guards. Only the permission seed uses the frozen
// old graph writer. Current-schema tests must use SQLiteStore directly.
type legacyControlledRunSeedStore struct{ *SQLiteStore }

func (s legacyControlledRunSeedStore) GetBrowserNetworkPreference(ctx context.Context, workspaceID string) (domain.BrowserNetworkPreference, error) {
	return (historicalBrowserPreferenceStore{s.SQLiteStore}).GetBrowserNetworkPreference(ctx, workspaceID)
}

func (s legacyControlledRunSeedStore) CreateMissionRunWithOperation(ctx context.Context,
	mission domain.Mission, run domain.Run, mode domain.RunModeSnapshot,
	linkedSession session.Session, initialEvents []events.Event,
	operation domain.RunCreationOperation, pin domain.InitialThreadModelRoutePin,
) (domain.RunCreationOperation, bool, error) {
	version, err := s.SchemaVersion(ctx)
	if err != nil {
		return domain.RunCreationOperation{}, false, err
	}
	if version < 143 || version >= 178 {
		return domain.RunCreationOperation{}, false, fmt.Errorf("historical controlled Run fixture rejects schema %d", version)
	}
	if err := validateControlledRunCreation(mission, run, mode, linkedSession,
		initialEvents, operation, pin); err != nil {
		return domain.RunCreationOperation{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.RunCreationOperation{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if existing, found, err := getRunCreationOperation(ctx, tx, operation.KeyDigest); err != nil {
		return domain.RunCreationOperation{}, false, err
	} else if found {
		if err := validateRunCreationReplay(existing, operation); err != nil {
			return domain.RunCreationOperation{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.RunCreationOperation{}, false, err
		}
		return existing, true, nil
	}
	if err := seedLegacyRunGraphTx(ctx, tx, mission, run, mode, linkedSession,
		true, true, initialEvents); err != nil {
		return domain.RunCreationOperation{}, false, err
	}
	if err := seedLegacyInitialThreadTx(ctx, tx, mission, run); err != nil {
		return domain.RunCreationOperation{}, false, err
	}
	if err := insertInitialThreadModelRoutePreferenceTx(ctx, tx, run, pin,
		operation.RequestedBy); err != nil {
		return domain.RunCreationOperation{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_creation_operations
		(operation_key_digest, request_fingerprint, protocol_version, mission_id,
		run_id, session_id, workspace_id, requested_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, operation.KeyDigest,
		operation.RequestFingerprint, operation.ProtocolVersion, operation.MissionID,
		operation.RunID, operation.SessionID, operation.WorkspaceID,
		operation.RequestedBy, ts(operation.CreatedAt)); err != nil {
		return domain.RunCreationOperation{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RunCreationOperation{}, false, err
	}
	return operation, false, nil
}

func historicalControlledSeedRequest(t *testing.T, state *SQLiteStore) application.ControlledRunCreationRequest {
	t.Helper()
	workspace := WorkspaceRecord{ID: "historical-controlled-boundary", Name: "fixture boundary",
		RootPath: t.TempDir(), CreatedAt: time.Now().UTC()}
	if err := state.SaveWorkspace(t.Context(), workspace); err != nil {
		t.Fatal(err)
	}
	return application.ControlledRunCreationRequest{
		Version: domain.RunCreationProtocolVersion, Goal: "atomic historical controlled graph",
		WorkspaceID: workspace.ID, OperationKey: "historical-controlled-seed-boundary", RequestedBy: "http_control",
	}
}

func TestHistoricalControlledRunSeedRejectsCurrentSchema(t *testing.T) {
	for _, version := range []int{178, 0} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			state := openRunSeedBoundaryStore(t, version)
			request := historicalControlledSeedRequest(t, state)
			before := runSeedBoundaryRows(t, state)
			_, err := application.NewControlledRunCreationService(legacyControlledRunSeedStore{state}).Create(t.Context(), request)
			if err == nil || !strings.Contains(err.Error(), "historical controlled Run fixture rejects schema") {
				t.Fatalf("historical controlled writer did not reject current schema: %v", err)
			}
			if after := runSeedBoundaryRows(t, state); !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected fixture left partial rows: before=%v after=%v", before, after)
			}
			var currentWriter application.ControlledRunCreationStore = state
			if version == 178 {
				currentWriter = historicalBrowserPreferenceStore{state}
			}
			created, err := application.NewControlledRunCreationService(currentWriter).Create(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			assertRunSeedPermission(t, state, created.Run, false)
		})
	}
}

func TestHistoricalControlledRunSeedRollsBackFailedOperation(t *testing.T) {
	state := openRunSeedBoundaryStore(t, 177)
	request := historicalControlledSeedRequest(t, state)
	if _, err := state.db.ExecContext(t.Context(), `CREATE TRIGGER force_historical_operation_failure
		BEFORE INSERT ON run_creation_operations
		BEGIN SELECT RAISE(ABORT, 'forced final historical operation failure'); END`); err != nil {
		t.Fatal(err)
	}
	before := runSeedBoundaryRows(t, state)
	service := application.NewControlledRunCreationService(legacyControlledRunSeedStore{state})
	if _, err := service.Create(t.Context(), request); err == nil || !strings.Contains(err.Error(), "forced final historical operation failure") {
		t.Fatalf("did not exercise the final operation insert: %v", err)
	}
	if after := runSeedBoundaryRows(t, state); !reflect.DeepEqual(before, after) {
		t.Fatalf("failed operation retained partial graph: before=%v after=%v", before, after)
	}
	if _, err := state.db.ExecContext(t.Context(), `DROP TRIGGER force_historical_operation_failure`); err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	assertRunSeedPermission(t, state, created.Run, true)
	replayed, err := service.Create(t.Context(), request)
	if err != nil || !replayed.Replayed || replayed.Run.ID != created.Run.ID {
		t.Fatalf("historical controlled replay=%#v err=%v", replayed, err)
	}
}
