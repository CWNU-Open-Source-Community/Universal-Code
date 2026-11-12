package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolrun"
)

func createControlledBrowserDefaultRun(t *testing.T, st *SQLiteStore) domain.Run {
	t.Helper()
	workspace := WorkspaceRecord{ID: "workspace-browser-default", Name: "Browser default", RootPath: t.TempDir()}
	if err := st.SaveWorkspace(t.Context(), workspace); err != nil {
		t.Fatal(err)
	}
	created, err := application.NewControlledRunCreationService(st).Create(t.Context(), application.ControlledRunCreationRequest{
		Version: domain.RunCreationProtocolVersion, Goal: "browse with ordinary chat permissions", WorkspaceID: workspace.ID,
		Profile: "code", Surface: "code", Phase: "deliver", OperationKey: "controlled-browser-default", RequestedBy: "http_control",
	})
	if err != nil {
		t.Fatal(err)
	}
	return created.Run
}

func TestDesktopBrowserDefaultsRepairOnlyProvenDefaultOff(t *testing.T) {
	for _, kind := range []string{"default", "running_default", "missing_legacy", "active_lease", "pending_approval", "explicit_run_off", "project_off", "project_restricted", "origins", "unknown_direct", "terminal", "terminal_explicit_off"} {
		t.Run(kind, func(t *testing.T) {
			st, err := Open(filepath.Join(t.TempDir(), "default-browser.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			run := createControlledBrowserDefaultRun(t, st)
			if kind == "unknown_direct" {
				_, run, err = application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{
					Goal: "explicit initial off from a direct embedder", Profile: "code", WorkspaceID: "workspace-browser-default", Interactive: true, BrowserNetworkAccess: domain.BrowserNetworkOff,
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			// Seed the released rev1 default, which did not record its source.
			if _, err := st.db.Exec(`UPDATE runs SET config_json=json_set(config_json,'$.browser_network_access','off','$.browser_network_revision',1) WHERE id=?`, run.ID); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "running_default":
				_, err = application.NewRunService(st).Start(t.Context(), run.ID)
			case "active_lease":
				_, err = st.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "other-desktop-window", TTL: time.Minute})
			case "pending_approval":
				_, err = st.SaveToolRun(t.Context(), toolrun.ToolRun{ID: "pending-desktop-tool", SessionID: run.SessionID, WorkspaceID: "workspace-browser-default", ToolName: toolrun.ShellTool, Command: "echo approval remains pending", Status: toolrun.StatusProposed})
				if err == nil {
					record, readErr := st.GetApprovalByProposal(t.Context(), "pending-desktop-tool")
					if readErr != nil || record.RunID != run.ID || record.Status != "pending" {
						t.Fatalf("missing real pending Run approval: %+v %v", record, readErr)
					}
				}
			case "missing_legacy":
				if _, err := st.db.Exec(`UPDATE runs SET config_json=json_remove(config_json,'$.browser_network_access','$.browser_network_revision') WHERE id=?`, run.ID); err != nil {
					t.Fatal(err)
				}
			case "explicit_run_off":
				_, err = application.ChangeBrowserNetworkAccess(t.Context(), st, nil, domain.ChangeBrowserNetworkAccessRequest{Version: domain.BrowserNetworkAccessVersion, RunID: run.ID, ExpectedRevision: 1, BrowserNetworkAccess: domain.BrowserNetworkOff, OperationKey: "explicit-browser-off", RequestedBy: "operator"})
			case "project_off", "project_restricted":
				mode := domain.BrowserNetworkOff
				var origins []string
				if kind == "project_restricted" {
					mode = domain.BrowserNetworkRestricted
					origins = []string{"https://example.org"}
				}
				_, err = application.ChangeBrowserNetworkAccess(t.Context(), st, nil, domain.ChangeBrowserNetworkAccessRequest{Version: domain.BrowserNetworkAccessVersion, WorkspaceID: "workspace-browser-default", ExpectedRevision: 0, BrowserNetworkAccess: mode, BrowserOrigins: origins, OperationKey: "remember-browser-limit", RequestedBy: "operator"})
			case "origins":
				_, err = st.db.Exec(`UPDATE runs SET config_json=json_set(config_json,'$.browser_origins',json('["https://example.org:443"]')) WHERE id=?`, run.ID)
			case "terminal":
				_, err = application.NewRunService(st).Cancel(t.Context(), run.ID)
			case "terminal_explicit_off":
				if _, err = application.NewRunService(st).Cancel(t.Context(), run.ID); err == nil {
					_, err = application.ChangeBrowserNetworkAccess(t.Context(), st, nil, domain.ChangeBrowserNetworkAccessRequest{Version: domain.BrowserNetworkAccessVersion, RunID: run.ID, ExpectedRevision: 1, BrowserNetworkAccess: domain.BrowserNetworkOff, OperationKey: "terminal-next-browser-off", RequestedBy: "operator"})
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			var before string
			permissionBefore, err := st.GetRunExecutionPermission(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.db.QueryRow(`SELECT config_json FROM runs WHERE id=?`, run.ID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if err := st.EnableDesktopBrowserDefaults(t.Context()); err != nil {
				t.Fatal(err)
			}
			if kind == "pending_approval" {
				pending, err := st.DesktopBrowserDefaultNeedsSuccessor(t.Context(), run.ID)
				if err != nil || pending {
					t.Fatalf("browser default would supersede a pending approval: %t %v", pending, err)
				}
			}
			updated, err := st.GetRun(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			permissionAfter, err := st.GetRunExecutionPermission(t.Context(), run.ID)
			if err != nil || permissionBefore != permissionAfter {
				t.Fatalf("browser default changed execution permission: before=%+v after=%+v err=%v", permissionBefore, permissionAfter, err)
			}
			if kind == "default" || kind == "running_default" || kind == "missing_legacy" {
				if updated.Config.BrowserNetworkAccess != domain.BrowserNetworkInternet || updated.Config.BrowserNetworkRevision != 2 {
					t.Fatalf("default repair=%+v", updated.Config)
				}
				var old domain.RunConfig
				if err := json.Unmarshal([]byte(before), &old); err != nil {
					t.Fatal(err)
				}
				if old.EffectiveBrowserNetworkRevision() >= updated.Config.BrowserNetworkRevision {
					t.Fatal("browser policy revision did not fence pre-upgrade authority")
				}
				if err := st.EnableDesktopBrowserDefaults(t.Context()); err != nil {
					t.Fatal(err)
				}
				again, err := st.GetRun(t.Context(), run.ID)
				if err != nil || again.Config.BrowserNetworkRevision != 2 {
					t.Fatalf("repeated startup changed policy revision: %+v %v", again.Config, err)
				}
			} else {
				var after string
				if err := st.db.QueryRow(`SELECT config_json FROM runs WHERE id=?`, run.ID).Scan(&after); err != nil || after != before {
					t.Fatalf("desktop startup rewrote explicit/unknown/historical config: %s %v", after, err)
				}
			}
			if kind == "terminal" || kind == "terminal_explicit_off" || kind == "explicit_run_off" || kind == "unknown_direct" {
				if kind != "terminal" && kind != "terminal_explicit_off" {
					if _, err := application.NewRunService(st).Cancel(t.Context(), run.ID); err != nil {
						t.Fatal(err)
					}
				}
				thread, err := st.GetThreadByRun(t.Context(), run.ID)
				if err != nil {
					t.Fatal(err)
				}
				continued, err := application.NewThreadService(st).Submit(t.Context(), application.SubmitThreadMessageRequest{Version: domain.ThreadMessageProtocolVersion, ThreadID: thread.ID, Content: "continue browsing", OperationKey: "continue-browser-default", RequestedBy: "operator"})
				if err != nil {
					t.Fatal(err)
				}
				next := continued.Run
				want := domain.BrowserNetworkOff
				if kind == "terminal" {
					want = domain.BrowserNetworkInternet
				}
				if next.Config.BrowserNetworkAccess != want || next.Config.BrowserNetworkRevision != 1 {
					t.Fatalf("successor=%+v want=%s", next.Config, want)
				}
			}
		})
	}
}

func TestBrowserDefaultsRequireHostOptIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host-default.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	run := createControlledBrowserDefaultRun(t, st)
	if run.Config.BrowserNetworkAccess != domain.BrowserNetworkOff {
		t.Fatalf("ordinary store changed its default: %+v", run.Config)
	}
	if err := st.EnableDesktopBrowserDefaults(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, next, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{
		Goal: "ordinary Web or CLI reopens a desktop database", Profile: "code", WorkspaceID: "workspace-browser-default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.Config.BrowserNetworkAccess != domain.BrowserNetworkOff {
		t.Fatalf("desktop host default persisted as a project preference: %+v", next.Config)
	}
	prior, err := st.GetRun(t.Context(), run.ID)
	if err != nil || prior.Config.BrowserNetworkAccess != domain.BrowserNetworkInternet || prior.Config.BrowserNetworkRevision != 2 {
		t.Fatalf("plain reopen changed stored desktop Run policy: %+v %v", prior.Config, err)
	}
}

func TestDesktopBrowserDefaultsAndExplicitProjectRestrictions(t *testing.T) {
	for _, mode := range []domain.BrowserNetworkAccess{"", domain.BrowserNetworkOff, domain.BrowserNetworkRestricted} {
		t.Run(string(mode), func(t *testing.T) {
			st, err := Open(filepath.Join(t.TempDir(), "browser-preference.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := st.EnableDesktopBrowserDefaults(t.Context()); err != nil {
				t.Fatal(err)
			}
			run := createControlledBrowserDefaultRun(t, st)
			if run.Config.BrowserNetworkAccess != domain.BrowserNetworkInternet {
				t.Fatalf("ordinary creation=%+v", run.Config)
			}
			if mode != "" {
				var origins []string
				if mode == domain.BrowserNetworkRestricted {
					origins = []string{"https://example.org"}
				}
				if _, err := application.ChangeBrowserNetworkAccess(t.Context(), st, nil, domain.ChangeBrowserNetworkAccessRequest{Version: domain.BrowserNetworkAccessVersion, WorkspaceID: "workspace-browser-default", ExpectedRevision: 0, BrowserNetworkAccess: mode, BrowserOrigins: origins, OperationKey: "explicit-project-browser", RequestedBy: "operator"}); err != nil {
					t.Fatal(err)
				}
			}
			_, next, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{Goal: "new chat honors advanced browser choice", WorkspaceID: "workspace-browser-default", Profile: "code"})
			if err != nil {
				t.Fatal(err)
			}
			want := mode
			if want == "" {
				want = domain.BrowserNetworkInternet
			}
			if next.Config.BrowserNetworkAccess != want {
				t.Fatalf("new chat=%+v want=%s", next.Config, want)
			}
		})
	}
}
