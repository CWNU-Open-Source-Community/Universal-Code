package store

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/testfixtures/legacyskill"
)

func TestSchemaV184PreservesRealV183SelectionRowsAndReceipts(t *testing.T) {
	// Includes the v131 command-history repair; released v131 compatibility is tested separately.
	if got := migrationPlanDigest(migrationPlan()[:183]); got != "b575b30a0eb52022a21ddd59ccded6928b57c1c4f580e172edc5bf86ee21f5c4" {
		t.Fatalf("published v1-v183 prefix changed: %s", got)
	}
	path := filepath.Join(t.TempDir(), "actual-v183.db")
	st := openHistoricalTestDatabase(t, path, 183)
	selected, request := seedHistoricalSelectionV183(t, st)
	before, err := st.loadAppliedMigrations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	queries := map[string]string{}
	rowsBefore := map[string]string{}
	for _, table := range []string{"run_external_skill_selections", "run_external_skill_selection_items", "run_external_skill_selection_operations", "events"} {
		rows, err := st.db.QueryContext(t.Context(), "PRAGMA table_info("+table+")")
		if err != nil {
			t.Fatal(err)
		}
		columns := []string{"rowid"}
		for rows.Next() {
			var ordinal, required, primary int
			var name, kind string
			var def any
			if err := rows.Scan(&ordinal, &name, &kind, &required, &def, &primary); err != nil {
				t.Fatal(err)
			}
			columns = append(columns, name)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		query := "SELECT COALESCE(json_group_array(value), '[]') FROM (SELECT json_array(" + strings.Join(columns, ",") + ") AS value FROM " + table + " ORDER BY rowid)"
		queries[table] = query
		var value string
		if err := st.db.QueryRowContext(t.Context(), query).Scan(&value); err != nil {
			t.Fatal(err)
		}
		rowsBefore[table] = value
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	after, err := st.loadAppliedMigrations(t.Context())
	if err != nil || len(after) != LatestSchemaVersion {
		t.Fatalf("v184 migration ledger: %v", err)
	}
	for version, entry := range before {
		if after[version] != entry {
			t.Fatalf("historical migration %d changed", version)
		}
	}
	for table, query := range queries {
		var value string
		if err := st.db.QueryRowContext(t.Context(), query).Scan(&value); err != nil || value != rowsBefore[table] {
			t.Fatalf("%s rows/rowids/receipts changed: %v", table, err)
		}
	}
	got, found, err := st.GetExternalSkillSelectionByRun(t.Context(), selected.RunID)
	if err != nil || !found || !reflect.DeepEqual(got, selected) {
		t.Fatalf("historical selection meaning changed: %+v %v", got, err)
	}
	replay, err := application.NewExternalSkillSelectionService(st).Select(t.Context(), request)
	if err != nil || !replay.Replayed || !reflect.DeepEqual(replay.Selection, selected) {
		t.Fatalf("v1 replay changed: %+v %v", replay, err)
	}
	var legacyID string
	var pluginID any
	var binding string
	if err := st.db.QueryRowContext(t.Context(), `SELECT legacy_installation_id, plugin_installation_id, plugin_binding_json FROM run_external_skill_selection_items WHERE selection_id=?`, selected.ID).Scan(&legacyID, &pluginID, &binding); err != nil || legacyID != selected.Items[0].InstallationID || pluginID != nil || binding != "{}" {
		t.Fatalf("migration fabricated a Plugin selection: %s %v %s %v", legacyID, pluginID, binding, err)
	}
	assertTableCount(t, st, "plugin_installations", 0)
	rows, err := st.db.QueryContext(t.Context(), "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("v184 left a broken foreign key")
	}
}

// The historical writer exists only in this migration fixture. It writes the
// published v183 schema, not a downgraded current database or fake Plugin rows.
func seedHistoricalSelectionV183(t *testing.T, st *SQLiteStore) (skills.ExternalSelection, application.SelectExternalSkillsRequest) {
	t.Helper()
	installation, _, result := fixturePackageInstallation(t, "historical-v183", "1.0.0", "v183-install-operation", time.Now().UTC().Add(-time.Minute))
	if err := legacyskill.Insert(t.Context(), st.db, installation); err != nil {
		t.Fatal(err)
	}
	installed, _, err := st.CompletePackageInstallation(t.Context(), result)
	if err != nil {
		t.Fatal(err)
	}
	mission, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{Goal: "Preserve historical selection", Profile: "review", Budget: domain.Budget{MaxTurns: 4, MaxTokens: 4096}})
	if err != nil {
		t.Fatal(err)
	}
	mode, err := st.GetRunMode(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	request := application.SelectExternalSkillsRequest{RunID: run.ID, PackageRefs: []string{"historical-v183@1.0.0"}, SpecialistRef: "historical-v183@1.0.0", TokenBudget: 1024, OperationKey: "v183-selection-operation", RequestedBy: "operator", ConfirmUntrustedContext: true}
	selected, err := skills.ResolveExternalSelection(skills.ResolveExternalSelectionRequest{SelectionID: "v183-selection", RunID: run.ID, MissionID: mission.ID, ModeSnapshotID: mode.ID, ModeRevision: mode.Revision, Surface: mode.Surface, Phase: mode.Phase, Profile: mode.Profile, Packages: []skills.InstalledPackage{installed}, SpecialistRef: request.SpecialistRef, TokenBudget: request.TokenBudget, RequestedBy: request.RequestedBy, Confirmed: true, CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	operation := skills.ExternalSelectionOperation{KeyDigest: runmutation.Fingerprint("external_skill_selection_operation.v1", run.ID, request.OperationKey), RequestFingerprint: skills.ExternalSelectionRequestFingerprint(selected), SelectionID: selected.ID, RunID: run.ID, RequestedBy: request.RequestedBy, CreatedAt: selected.CreatedAt}
	tx, err := st.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), `INSERT INTO run_external_skill_selections VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, selected.ID, selected.RunID, selected.MissionID, selected.ModeSnapshotID, selected.ModeRevision, selected.ProtocolVersion, selected.Surface, selected.Profile, selected.TokenBudget, selected.TokenUpperBound, selected.ItemCount, selected.Fingerprint, selected.RequestedBy, 1, 1, 0, ts(selected.CreatedAt)); err != nil {
		t.Fatal(err)
	}
	for _, item := range selected.Items {
		if _, err := tx.ExecContext(t.Context(), `INSERT INTO run_external_skill_selection_items VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, item.SelectionID, item.Ordinal, item.InstallationID, item.InstallationFingerprint, item.InstallResultFingerprint, item.Name, item.Version, item.Surface, item.ContentSHA256, item.ContentBytes, item.TokenUpperBound, item.ArchiveSHA256, item.ArchiveBytes, item.PackageFingerprint, item.ObjectKey, item.TrustClass, item.ToolDependencyCount, boolInt(item.SpecialistEligible)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(t.Context(), `INSERT INTO run_external_skill_selection_operations VALUES (?, ?, ?, ?, ?, ?)`, operation.KeyDigest, operation.RequestFingerprint, operation.SelectionID, operation.RunID, operation.RequestedBy, ts(operation.CreatedAt)); err != nil {
		t.Fatal(err)
	}
	event, err := events.New(run.ID, mission.ID, events.ExternalSkillSelectionCreatedEvent, "external_skills", selected.ID, map[string]any{"protocol": selected.ProtocolVersion, "surface": selected.Surface, "profile": selected.Profile, "item_count": selected.ItemCount, "token_budget": selected.TokenBudget, "token_upper_bound": selected.TokenUpperBound, "operator_confirmed": true, "context_delivery": true, "tool_capability_grant": false})
	if err != nil {
		t.Fatal(err)
	}
	event.CreatedAt = selected.CreatedAt
	if _, err := insertRunEventTx(t.Context(), tx, event); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if selected.ProtocolVersion != skills.ExternalSelectionProtocolVersion || selected.Items[0].Plugin != nil {
		t.Fatal("fixture is not an actual v1 selection")
	}
	return selected, request
}
