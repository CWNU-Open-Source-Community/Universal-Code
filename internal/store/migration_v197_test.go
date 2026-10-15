package store

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/plugins"
	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/testfixtures/legacyskill"
	"cyberagent-workbench/internal/toolgateway"
)

func TestSchemaV197PreservesV196LegacyAndPluginSelections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v196-selections.db")
	st := openHistoricalTestDatabase(t, path, 196)
	installation, _, result := fixturePackageInstallation(t, "v196-historical", "1.0.0", "historical-install-operation", time.Now().UTC().Add(-time.Minute))
	if err := legacyskill.Insert(t.Context(), st.db, installation); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CompletePackageInstallation(t.Context(), result); err != nil {
		t.Fatal(err)
	}
	body := []byte("# Retained Plugin guidance\nRead the selected evidence.\n")
	manifest := skills.BindManifestContent(skills.Manifest{Protocol: skills.ProtocolVersion, Name: "v196-plugin", Version: "1.0.0",
		Description: "Migration fixture", Profiles: []domain.Profile{domain.ProfileReview},
		ToolDependencies: []toolgateway.ToolName{}}, body)
	raw, err := skills.BuildUnsignedPackage(manifest, body)
	if err != nil {
		t.Fatal(err)
	}
	builtins, _ := skills.BuiltinRegistry()
	objects, _ := skills.NewLocalPackageObjectStore(t.TempDir())
	imported, err := application.NewSkillPackageRegistryService(st, objects, builtins).Import(t.Context(), application.ImportSkillPackageRequest{
		Raw: raw, Surface: domain.ExecutionSurfaceCode, OperationKey: "v196-plugin-import", InstalledBy: "operator", ConfirmUntrusted: true})
	if err != nil || imported.Installation == nil {
		t.Fatalf("import old Plugin selection fixture: %+v %v", imported, err)
	}
	value := *imported.Installation
	service, _ := plugins.NewService(st)
	for _, action := range []plugins.ReviewAction{plugins.ReviewApprove, plugins.ReviewEnable} {
		value, err = service.Review(t.Context(), value.ID, plugins.ReviewRequest{Action: action,
			ExpectedPackageFingerprint: value.PackageFingerprint, ExpectedGeneration: value.Generation,
			ReviewedBy: "operator", ConfirmUntrusted: true, Capabilities: []plugins.Capability{plugins.CapabilitySkills}})
		if err != nil {
			t.Fatal(err)
		}
	}
	var selections []skills.ExternalSelection
	var requests []application.SelectExternalSkillsRequest
	for _, ref := range []string{"v196-historical@1.0.0", "v196-plugin@1.0.0"} {
		_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{
			Goal: "Preserve released Skill selections", Profile: "review", Surface: "code", Phase: "deliver",
			Budget: domain.Budget{MaxTurns: 4, MaxTokens: 4096}})
		if err != nil {
			t.Fatal(err)
		}
		request := application.SelectExternalSkillsRequest{RunID: run.ID, PackageRefs: []string{ref}, TokenBudget: 4096,
			OperationKey: "v196-selection-" + ref, RequestedBy: "operator", ConfirmUntrustedContext: true}
		selected, err := application.NewExternalSkillSelectionService(st).Select(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		selections, requests = append(selections, selected.Selection), append(requests, request)
	}
	ledger, err := st.loadAppliedMigrations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	queries := map[string]string{}
	before := map[string]string{}
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
		queries[table] = "SELECT json_group_array(value) FROM (SELECT json_array(" + strings.Join(columns, ",") + ") value FROM " + table + " ORDER BY rowid)"
		var encoded string
		if err := st.db.QueryRowContext(t.Context(), queries[table]).Scan(&encoded); err != nil {
			t.Fatal(err)
		}
		before[table] = encoded
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	afterLedger, err := st.loadAppliedMigrations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for version, entry := range ledger {
		if afterLedger[version] != entry {
			t.Fatalf("released migration %d changed", version)
		}
	}
	for table, query := range queries {
		var after string
		if err := st.db.QueryRowContext(t.Context(), query).Scan(&after); err != nil || after != before[table] {
			t.Fatalf("%s rows/rowids changed: %v", table, err)
		}
	}
	for i, original := range selections {
		got, found, err := st.GetExternalSkillSelectionByRun(t.Context(), original.RunID)
		if err != nil || !found || !reflect.DeepEqual(got, original) {
			t.Fatalf("historical selection changed: %+v %v", got, err)
		}
		replay, err := application.NewExternalSkillSelectionService(st).Select(t.Context(), requests[i])
		if err != nil || !replay.Replayed || !reflect.DeepEqual(replay.Selection, original) {
			t.Fatalf("historical replay changed: %+v %v", replay, err)
		}
		projection, found, err := st.GetExternalSkillProjectionByRun(t.Context(), original.RunID)
		if err != nil || !found || projection.Items[0].Native || projection.Items[0].SelectionRef != "" {
			t.Fatalf("historical public projection changed: %+v %v", projection, err)
		}
	}
	rows, err := st.db.QueryContext(t.Context(), "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		t.Fatalf("migration left broken foreign keys: %v", rows.Err())
	}
}

func TestNativeSkillSelectionSQLPinsOriginalComponent(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "native-guard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	directory := filepath.Join(t.TempDir(), "证据-review")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte("---\nname: 证据-review\ndescription: Inspect selected evidence\n---\nRead the exact source.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	builtins, _ := skills.BuiltinRegistry()
	objects, _ := skills.NewLocalPackageObjectStore(t.TempDir())
	catalog := application.NewSkillCatalogService(st, application.NewSkillPackageRegistryService(st, objects, builtins))
	imported, err := catalog.ImportFromDirectory(t.Context(), application.ImportSkillFromDirectoryRequest{
		Directory: directory, Surface: domain.ExecutionSurfaceCode, OperationKey: "native-sql-import", InstalledBy: "operator", ConfirmUntrusted: true})
	if err != nil || imported.Portable == nil {
		t.Fatalf("native fixture import: %+v %v", imported, err)
	}
	value := *imported.Portable
	source, err := plugins.SkillSelectionSourceForComponent(value, value.Snapshot.Skills[0].Instructions.Component.ComponentID)
	if err != nil {
		t.Fatal(err)
	}
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{
		Goal: "Native selection SQL boundary", Profile: "code", Surface: "code", Phase: "deliver", Budget: domain.Budget{MaxTurns: 4, MaxTokens: 4096}})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := application.NewExternalSkillSelectionService(st).Select(t.Context(), application.SelectExternalSkillsRequest{
		RunID: run.ID, PackageRefs: []string{skills.ExternalSelectionItemRef(source.Item)}, TokenBudget: 4096,
		OperationKey: "native-sql-selection", RequestedBy: "operator", ConfirmUntrustedContext: true})
	if err != nil {
		t.Fatal(err)
	}
	projection, found, err := st.GetExternalSkillProjectionByRun(t.Context(), run.ID)
	if err != nil || !found || !projection.Items[0].Native || projection.Items[0].Name != "证据-review" || projection.Items[0].Version != "" {
		t.Fatalf("native identity was not projected: %+v %v", projection, err)
	}
	columns := []string{"selection_id", "ordinal", "installation_id", "installation_fingerprint", "install_result_fingerprint", "name", "version", "surface", "content_sha256", "content_bytes", "token_upper_bound", "archive_sha256", "archive_bytes", "package_fingerprint", "object_key", "trust_class", "tool_dependency_count", "specialist_eligible", "plugin_binding_json", "legacy_installation_id", "plugin_installation_id"}
	for _, test := range []struct {
		name, column, expression, want string
	}{
		{"duplicate", "ordinal", "2", "UNIQUE constraint failed"},
		{"name", "name", "'different'", "native Skill selection component binding is invalid"},
		{"version", "version", "'1.0.0'", "native Skill selection component binding is invalid"},
		{"component", "plugin_binding_json", "json_set(plugin_binding_json, '$.component_id', 'skill:missing')", "native Skill selection component binding is invalid"},
		{"generation", "plugin_binding_json", "json_set(plugin_binding_json, '$.generation', 99)", "native Skill selection component binding is invalid"},
		{"native-kind", "plugin_binding_json", "json_remove(plugin_binding_json, '$.native')", "Plugin binding is invalid"},
		{"content", "content_sha256", "printf('%064d', 0)", "native Skill selection component binding is invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			expressions := append([]string(nil), columns...)
			expressions[1] = "2"
			for i, column := range columns {
				if column == test.column {
					expressions[i] = test.expression
				}
			}
			query := "INSERT INTO run_external_skill_selection_items (" + strings.Join(columns, ",") + ") SELECT " + strings.Join(expressions, ",") + " FROM run_external_skill_selection_items WHERE selection_id = ? AND ordinal = 1"
			if _, err := st.db.ExecContext(t.Context(), query, selected.Selection.ID); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("native SQL boundary did not reject %s at the intended guard: %v", test.name, err)
			}
		})
	}
	got, found, err := st.GetExternalSkillSelectionByRun(t.Context(), run.ID)
	if err != nil || !found || !reflect.DeepEqual(got, selected.Selection) {
		t.Fatalf("rejected insert altered the saved selection: %+v %v", got, err)
	}
}
