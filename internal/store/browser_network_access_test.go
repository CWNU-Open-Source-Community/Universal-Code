package store

import (
	"encoding/json"
	"path/filepath"
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

func TestSchemaV223PreservesLegacyBrowserChoiceAndDefaultsNewRunsOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-browser.db")
	st := openHistoricalTestDatabase(t, path, 222)
	_, run := createStructuredToolTestRun(t, t.Context(), st, "legacy scoped browser")
	legacy := run.Config
	legacy.BrowserNetworkAccess, legacy.BrowserNetworkRevision = "", 0
	legacy.BrowserOrigins = []string{"https://example.org:443"}
	raw, _ := json.Marshal(legacy)
	if _, err := st.db.Exec(`UPDATE runs SET config_json=? WHERE id=?`, string(raw), run.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.applyMigration(t.Context(), migrationPlan()[222]); err != nil {
		t.Fatal(err)
	}
	var preserved string
	if err := st.db.QueryRow(`SELECT config_json FROM runs WHERE id=?`, run.ID).Scan(&preserved); err != nil || preserved != string(raw) {
		t.Fatalf("migration edited historical config: %s %v", preserved, err)
	}
	selection, err := st.GetBrowserNetworkAccess(t.Context(), run.ID, "")
	if err != nil || selection.BrowserNetworkAccess != domain.BrowserNetworkRestricted {
		t.Fatalf("legacy selection=%+v %v", selection, err)
	}
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
	if version, err := st.SchemaVersion(t.Context()); err != nil || version != 223 {
		t.Fatalf("version=%d %v", version, err)
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
