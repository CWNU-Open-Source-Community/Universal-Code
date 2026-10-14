package store

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

func TestV192PreservesLegacyScopeAndSealsWebSelectionLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web-access-v191.db")
	historical := openHistoricalTestDatabase(t, path, 191)
	mission, run, err := newMigrationFixtureRunService(t, historical).Create(t.Context(), application.CreateRunRequest{Goal: "legacy public search and exact fetch", Profile: "review", NetworkMode: "allowlist", AllowedTargets: []string{"docs.example.org"}, Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	var beforeMission, beforeMode string
	if err := historical.db.QueryRowContext(t.Context(), `SELECT scope_json FROM missions WHERE id=?`, mission.ID).Scan(&beforeMission); err != nil {
		t.Fatal(err)
	}
	if err := historical.db.QueryRowContext(t.Context(), `SELECT scope_json FROM run_mode_snapshots WHERE run_id=?`, run.ID).Scan(&beforeMode); err != nil {
		t.Fatal(err)
	}
	if err := historical.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	var afterMission, afterMode string
	if err := state.db.QueryRowContext(t.Context(), `SELECT scope_json FROM missions WHERE id=?`, mission.ID).Scan(&afterMission); err != nil {
		t.Fatal(err)
	}
	if err := state.db.QueryRowContext(t.Context(), `SELECT scope_json FROM run_mode_snapshots WHERE run_id=?`, run.ID).Scan(&afterMode); err != nil {
		t.Fatal(err)
	}
	mode, err := state.GetRunMode(t.Context(), run.ID)
	if err != nil || beforeMission != afterMission || beforeMode != afterMode || mode.Scope.WebAccessMode != "" || mode.Scope.EffectiveWebAccessMode() != domain.WebAccessRules {
		t.Fatalf("historical scope reinterpreted: %+v err=%v", mode, err)
	}
	selected, err := application.NewRunWebAccessService(state).Change(t.Context(), application.ChangeRunWebAccessRequest{Version: application.RunWebAccessControlProtocolVersion, RunID: run.ID, ExpectedModeRevision: 1, WebAccessMode: "rules", WebAllowedTargets: []string{"docs.example.org"}, OperationKey: "migration-web-access-0001"})
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`UPDATE run_web_access_operations SET requested_by='other' WHERE snapshot_id=?`, `DELETE FROM run_web_access_operations WHERE snapshot_id=?`} {
		if _, err := state.db.ExecContext(t.Context(), query, selected.Mode.ID); err == nil {
			t.Fatalf("mutable web operation: %s", query)
		}
	}
	for _, target := range []string{"public_https", "*.example.org", "localhost", "127.0.0.1", "metadata.google.internal"} {
		scope := selected.Mode.Scope
		scope.WebAllowedTargets = []string{target}
		raw, _ := json.Marshal(scope)
		_, err := state.db.ExecContext(t.Context(), `INSERT INTO run_mode_snapshots (id,run_id,mission_id,revision,protocol_version,surface,phase,profile,scope_json,policy_version,requested_by,reason,created_at) SELECT 'forged-web-access',run_id,mission_id,revision+1,protocol_version,surface,phase,profile,?,policy_version,requested_by,reason,created_at FROM run_mode_snapshots WHERE id=?`, string(raw), selected.Mode.ID)
		if err == nil || !strings.Contains(err.Error(), "web access scope") {
			t.Fatalf("SQL accepted unsafe rule %s or missed scope validator: %v", target, err)
		}
	}
	assertNoForeignKeyViolations(t, state.db)
}
