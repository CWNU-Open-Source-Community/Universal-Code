package application

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/domain"
)

func TestMCPWorkspaceRootProviderUsesExactRunWorkspace(t *testing.T) {
	f := newDrydockApplicationFixture(t, "MCP roots")
	factoryCalls := 0
	provider := NewMCPWorkspaceRootProvider(f.state, func(context.Context, string) (*RunWorktreeService, error) {
		factoryCalls++
		return f.service, nil
	})
	selected, err := provider(t.Context(), "", f.workspace.ID)
	if err != nil || selected.Path != f.sourceRoot || len(selected.Fingerprint) != 64 {
		t.Fatalf("operator discovery lost selected Workspace: %+v %v", selected, err)
	}
	ordinary, err := provider(t.Context(), f.run.ID, f.workspace.ID)
	if err != nil || ordinary != selected || factoryCalls != 0 {
		t.Fatalf("ordinary Run needs only its registered root: %+v %v", ordinary, err)
	}
	owned := mustCreateDrydock(t, f)
	actual, err := provider(t.Context(), f.run.ID, f.workspace.ID)
	if err != nil || actual.Path != owned.Path || actual.Fingerprint == selected.Fingerprint || factoryCalls != 1 {
		t.Fatalf("owned Run did not use its inspected Drydock: %+v %v", actual, err)
	}
	_, other, err := NewRunService(f.state).Create(t.Context(), CreateRunRequest{Goal: "same source, different Run", Profile: "code", WorkspaceID: f.workspace.ID})
	if err != nil {
		t.Fatal(err)
	}
	if actual, err := provider(t.Context(), other.ID, f.workspace.ID); err != nil || actual != selected {
		t.Fatalf("one Run borrowed another Run's Drydock: %+v %v", actual, err)
	}
	if _, err := provider(t.Context(), f.run.ID, "foreign-workspace"); err == nil {
		t.Fatal("foreign Workspace was accepted")
	}
	if _, err := NewMCPWorkspaceRootProvider(f.state, nil)(t.Context(), f.run.ID, f.workspace.ID); err == nil {
		t.Fatal("owned Run fell back to source without its Drydock service")
	}
	gitFile := filepath.Join(owned.Path, ".git")
	hidden := filepath.Join(owned.Path, ".git-hidden-by-roots-test")
	if err := os.Rename(gitFile, hidden); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(hidden, gitFile) })
	if _, err := provider(t.Context(), f.run.ID, f.workspace.ID); err == nil {
		t.Fatal("drifted Drydock fell back to source")
	}
}

func TestMCPWorkspaceRootProviderConfiguredMissingDrydockFailsClosed(t *testing.T) {
	f := newDrydockApplicationFixture(t, "missing MCP roots target")
	presets, err := NewStandardCodePresetService(f.state, f.service, standardCodeThreadTestRuntime())
	if err != nil {
		t.Fatal(err)
	}
	request := ConfigureStandardCodeRequest{Version: domain.StandardCodePresetProtocolVersion, RunID: f.run.ID,
		Action: "configure", BackendIntent: "local", OperationKey: "mcp-roots-preset-0001", RequestedBy: "operator"}
	preview, err := presets.Configure(t.Context(), request)
	if err != nil || !preview.TrustRequired {
		t.Fatal("workspace trust preview missing", err)
	}
	request.ConfirmWorkspaceTrust, request.ExpectedTrustDigest = true, preview.TrustDigest
	if result, err := presets.Configure(t.Context(), request); err != nil || result.Status != StandardCodeResultConfigured {
		t.Fatal("workspace configuration failed", err)
	}
	provider := NewMCPWorkspaceRootProvider(runFileMissingDrydockStore{f.state}, func(context.Context, string) (*RunWorktreeService, error) {
		return f.service, nil
	})
	if _, err := provider(t.Context(), f.run.ID, f.workspace.ID); err == nil {
		t.Fatal("configured Run with no owned directory exposed its source Workspace")
	}
}
