package application_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/store"
)

func TestRunPinsProjectInstructionsUntilConfirmedRefresh(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	filename := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(filename, []byte("run focused tests\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	workspace := store.WorkspaceRecord{ID: "workspace-instruction-refresh",
		Name: "instruction-refresh", RootPath: root, CreatedAt: time.Now().UTC()}
	if err := state.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	pinned, err := projectconfig.DiscoverInstructions(ctx, root, ".")
	if err != nil {
		t.Fatal(err)
	}
	_, run, err := application.NewRunService(state).Create(ctx, application.CreateRunRequest{
		Goal: "test instruction pinning", Profile: "code", WorkspaceID: workspace.ID,
		Budget: domain.DefaultBudget(), ProjectInstructions: &pinned,
		RequestedBy: "cli_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	initial, found, err := state.GetLatestRunInstructionSnapshot(ctx, run.ID)
	if err != nil || !found || initial.Revision != 1 ||
		initial.Snapshot.Fingerprint != pinned.Fingerprint {
		t.Fatalf("initial=%#v found=%t err=%v", initial, found, err)
	}
	if err := os.WriteFile(filename, []byte("run all tests\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	live, err := projectconfig.DiscoverInstructions(ctx, root, ".")
	if err != nil {
		t.Fatal(err)
	}
	if live.Fingerprint == pinned.Fingerprint {
		t.Fatal("changed instruction did not change the live fingerprint")
	}
	unchanged, err := state.GetRun(ctx, run.ID)
	if err != nil || unchanged.Config.ProjectInstructionsFingerprint != pinned.Fingerprint {
		t.Fatalf("live file silently changed the Run: %#v err=%v", unchanged.Config, err)
	}
	diff := projectconfig.DiffInstructionSnapshots(pinned, live)
	refreshed, changed, err := state.ConfirmRunInstructionSnapshot(ctx, run.ID,
		pinned.Fingerprint, live, diff, "desktop_operator", time.Now().UTC())
	if err != nil || !changed || refreshed.Revision != 2 {
		t.Fatalf("refreshed=%#v changed=%t err=%v", refreshed, changed, err)
	}
	updated, err := state.GetRun(ctx, run.ID)
	if err != nil || updated.Config.ProjectInstructionsFingerprint != live.Fingerprint {
		t.Fatalf("confirmed refresh was not pinned: %#v err=%v", updated.Config, err)
	}
	if _, _, err := state.ConfirmRunInstructionSnapshot(ctx, run.ID,
		pinned.Fingerprint, live, diff, "desktop_operator", time.Now().UTC()); err == nil {
		t.Fatal("stale refresh confirmation was accepted")
	}
	history, err := state.ListRunInstructionSnapshots(ctx, run.ID, 10)
	if err != nil || len(history) != 2 || history[0].Revision != 2 || history[1].Revision != 1 {
		t.Fatalf("history=%#v err=%v", history, err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.override.md"), []byte("run override checks\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := application.NewProjectInstructionService(state)
	preview, err := service.Inspect(ctx, run.ID, ".")
	if err != nil || !preview.Stale || len(preview.Live.Sources) != 1 ||
		preview.Live.Sources[0].Content != "run override checks\n" ||
		preview.Pinned.Snapshot.Fingerprint != live.Fingerprint ||
		len(preview.Diff.Added) != 1 || len(preview.Diff.Removed) != 1 {
		t.Fatalf("override did not produce a source replacement preview: %+v err=%v", preview, err)
	}
	unconfirmed, err := service.Refresh(ctx, run.ID, ".", live.Fingerprint,
		preview.Live.Fingerprint, "desktop_operator", false)
	if err != nil || unconfirmed.RefreshConfirmed || unconfirmed.Pinned.Revision != 2 ||
		unconfirmed.Pinned.Snapshot.Sources[0].Content != "run all tests\n" {
		t.Fatalf("override bypassed confirmed refresh: %+v err=%v", unconfirmed, err)
	}
	confirmed, err := service.Refresh(ctx, run.ID, ".", live.Fingerprint,
		preview.Live.Fingerprint, "desktop_operator", true)
	if err != nil || !confirmed.RefreshConfirmed || confirmed.Pinned.Revision != 3 ||
		confirmed.Pinned.Snapshot.Fingerprint != preview.Live.Fingerprint ||
		confirmed.Pinned.Snapshot.Sources[0].Content != "run override checks\n" {
		t.Fatalf("confirmed override was not pinned: %+v err=%v", confirmed, err)
	}
}
