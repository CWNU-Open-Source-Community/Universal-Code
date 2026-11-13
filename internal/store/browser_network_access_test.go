package store

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

func TestBrowserNetworkSuccessorUsesLatestCommittedPreference(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "browser-successor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, preparedBeforeToggle := createStructuredToolTestRun(t, t.Context(), st, "next browser choice")
	if _, err = application.NewRunService(st).Cancel(t.Context(), preparedBeforeToggle.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = application.ChangeBrowserNetworkAccess(t.Context(), st, nil, domain.ChangeBrowserNetworkAccessRequest{Version: domain.BrowserNetworkAccessVersion, RunID: preparedBeforeToggle.ID, ExpectedRevision: 1, BrowserNetworkAccess: domain.BrowserNetworkInternet, OperationKey: "browser-next-atomic-0001", RequestedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	// historySuccessor builds its candidate from this stale pre-toggle config.
	// The store must select the latest preference in its commit transaction.
	next := historySuccessor(t, st, preparedBeforeToggle)
	if next.Config.BrowserNetworkAccess != domain.BrowserNetworkInternet {
		t.Fatalf("stale successor config=%+v", next.Config)
	}
	historical, err := st.GetRun(t.Context(), preparedBeforeToggle.ID)
	if err != nil || historical.Config.BrowserNetworkAccess != domain.BrowserNetworkOff {
		t.Fatalf("history=%+v %v", historical.Config, err)
	}
	for _, statement := range []string{`UPDATE browser_network_access_operations SET requested_by='other'`, `DELETE FROM browser_network_access_operations`} {
		if _, err = st.db.Exec(statement); err == nil {
			t.Fatal("browser selection receipt was mutable")
		}
	}
}

func TestSchemaV230PreservesLegacyBrowserChoiceAndDefaultsNewRunsOff(t *testing.T) {
	const migrationVersion = 230
	path := filepath.Join(t.TempDir(), "legacy-browser.db")
	st := openHistoricalTestDatabase(t, path, migrationVersion-1)
	_, run := createStructuredToolTestRun(t, t.Context(), st, "legacy scoped browser")
	legacy := run.Config
	legacy.BrowserNetworkAccess, legacy.BrowserNetworkRevision = "", 0
	legacy.BrowserOrigins = []string{"https://example.org:443"}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE runs SET config_json=? WHERE id=?`, string(raw), run.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.applyMigration(t.Context(), migrationPlan()[migrationVersion-1]); err != nil {
		t.Fatal(err)
	}
	if version, err := st.SchemaVersion(t.Context()); err != nil || version != migrationVersion {
		t.Fatalf("migration version=%d want=%d err=%v", version, migrationVersion, err)
	}
	assertLegacyChoice := func() {
		t.Helper()
		var preserved string
		if err := st.db.QueryRow(`SELECT config_json FROM runs WHERE id=?`, run.ID).Scan(&preserved); err != nil || preserved != string(raw) {
			t.Fatalf("migration edited historical config: %s %v", preserved, err)
		}
		selection, err := st.GetBrowserNetworkAccess(t.Context(), run.ID, "")
		if err != nil || selection.BrowserNetworkAccess != domain.BrowserNetworkRestricted || !reflect.DeepEqual(selection.BrowserOrigins, legacy.BrowserOrigins) {
			t.Fatalf("legacy selection=%+v %v", selection, err)
		}
	}
	assertLegacyChoice()
	_, fresh, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{Goal: "new default", Profile: "review"})
	if err != nil || fresh.Config.BrowserNetworkAccess != domain.BrowserNetworkOff {
		t.Fatalf("new default=%+v %v", fresh.Config, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if version, err := st.SchemaVersion(t.Context()); err != nil || version != LatestSchemaVersion {
		t.Fatalf("reopened version=%d want=%d err=%v", version, LatestSchemaVersion, err)
	}
	assertLegacyChoice()
	storedFresh, err := st.GetRun(t.Context(), fresh.ID)
	if err != nil || storedFresh.Config.BrowserNetworkAccess != domain.BrowserNetworkOff {
		t.Fatalf("new default changed after upgrade: %+v %v", storedFresh.Config, err)
	}
}

func TestBrowserNetworkProjectPreferenceSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remember-browser.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	workspace := WorkspaceRecord{ID: "browser-project", Name: "Browser Project", RootPath: t.TempDir()}
	if err = st.SaveWorkspace(t.Context(), workspace); err != nil {
		t.Fatal(err)
	}
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{Goal: "remember internet", WorkspaceID: workspace.ID, Profile: "review"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = application.ChangeBrowserNetworkAccess(t.Context(), st, nil, domain.ChangeBrowserNetworkAccessRequest{Version: domain.BrowserNetworkAccessVersion, RunID: run.ID, ExpectedRevision: 1, BrowserNetworkAccess: domain.BrowserNetworkInternet, RememberForProject: true, OperationKey: "browser-remember-restart-0001", RequestedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, next, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{Goal: "next project chat", WorkspaceID: workspace.ID, Profile: "review"})
	if err != nil || next.Config.BrowserNetworkAccess != domain.BrowserNetworkInternet {
		t.Fatalf("remembered preference lost: %+v %v", next.Config, err)
	}
}

func TestBrowserNetworkSuccessorMaterializesLegacySelection(t *testing.T) {
	for _, test := range []struct {
		name       string
		permission domain.RunExecutionPermissionMode
		origins    []string
		want       domain.BrowserNetworkAccess
	}{
		{"full", domain.RunExecutionPermissionFull, nil, domain.BrowserNetworkInternet},
		{"full_with_origins", domain.RunExecutionPermissionFull, []string{"https://example.org:443"}, domain.BrowserNetworkInternet},
		{"restricted", domain.RunExecutionPermissionAsk, []string{"https://example.org:443"}, domain.BrowserNetworkRestricted},
		{"off", domain.RunExecutionPermissionAsk, nil, domain.BrowserNetworkOff},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			st, err := Open(filepath.Join(t.TempDir(), "legacy-successor.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			_, run, err := application.NewRunService(st).Create(ctx, application.CreateRunRequest{Goal: "continue legacy browser choice", Profile: "review"})
			if err != nil {
				t.Fatal(err)
			}
			thread, err := st.GetThreadByRun(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			permissions := application.NewThreadExecutionPermissionService(st, domain.ExecutionPermissionRuntimeCapabilities{
				OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
				RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority(),
			})
			selectThreadPermissionForFullCDPTest(t, ctx, permissions, thread.ID, test.permission, "legacy-browser-initial-permission")
			legacy := run.Config
			legacy.BrowserNetworkAccess, legacy.BrowserNetworkRevision, legacy.BrowserOrigins = "", 0, test.origins
			raw, err := json.Marshal(legacy)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.db.ExecContext(ctx, `UPDATE runs SET config_json=? WHERE id=?`, string(raw), run.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := application.NewRunService(st).Cancel(ctx, run.ID); err != nil {
				t.Fatal(err)
			}
			selectThreadPermissionForFullCDPTest(t, ctx, permissions, thread.ID, domain.RunExecutionPermissionAsk, "legacy-browser-next-permission")
			continued, err := application.NewThreadService(st).Submit(ctx, application.SubmitThreadMessageRequest{
				Version: domain.ThreadMessageProtocolVersion, ThreadID: thread.ID, Content: "continue", OperationKey: "legacy-browser-successor", RequestedBy: "operator",
			})
			if err != nil {
				t.Fatal(err)
			}
			if continued.Run.Config.BrowserNetworkAccess != test.want || continued.Run.Config.BrowserNetworkRevision != 1 {
				t.Fatalf("successor browser selection was not materialized: %+v", continued.Run.Config)
			}
			if test.want == domain.BrowserNetworkInternet && len(continued.Run.Config.BrowserOrigins) != 0 {
				t.Fatal("internet successor retained inactive legacy origins")
			}
			selectThreadPermissionForFullCDPTest(t, ctx, permissions, thread.ID, domain.RunExecutionPermissionFull, "legacy-browser-later-permission")
			selected, err := st.GetBrowserNetworkAccess(ctx, continued.Run.ID, "")
			if err != nil || selected.BrowserNetworkAccess != test.want {
				t.Fatalf("permission change altered browser selection: %+v %v", selected, err)
			}
			historical, err := st.GetRun(ctx, run.ID)
			if err != nil || historical.Config.BrowserNetworkAccess != "" || historical.Config.BrowserNetworkRevision != 0 {
				t.Fatalf("predecessor was rewritten: %+v %v", historical.Config, err)
			}
		})
	}
}
