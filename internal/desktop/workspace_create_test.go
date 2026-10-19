package desktop

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type namedTestWorkspaceRegistrar struct {
	testWorkspaceDirectoryRegistrar
	calls int
	name  string
}

func (r *namedTestWorkspaceRegistrar) RegisterNamedWorkspaceDirectory(ctx context.Context, path, name string) (WorkspaceImportSummary, error) {
	r.calls++
	r.selectedPath = path
	r.name = name
	return WorkspaceImportSummary{ID: "ws-selected-project", Name: name, CreatedAt: time.Now().UTC()}, nil
}
func TestDesktopWorkspaceSelectDoesNotRegisterAndCreateRequiresNativeHandle(t *testing.T) {
	root := t.TempDir()
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	registrar := &namedTestWorkspaceRegistrar{}
	bridge := newWorkspaceImportBridge(t, testWorkspaceDirectoryPicker{path: root}, registrar)
	selected, err := bridge.SelectWorkspaceDirectory()
	if err != nil || selected.Selection == nil || registrar.calls != 0 {
		t.Fatal(selected, err, registrar.calls)
	}
	if selected.Selection.DirectoryPath != canonicalRoot {
		t.Fatalf("selected directory %q, want canonical directory %q", selected.Selection.DirectoryPath, canonicalRoot)
	}
	if _, err = bridge.CreateWorkspace(CreateWorkspaceRequest{Handle: root, Name: "attempt"}); err == nil || registrar.calls != 0 {
		t.Fatal("renderer path accepted", err)
	}
	request := CreateWorkspaceRequest{Handle: selected.Selection.Handle, Name: "selected / name"}
	first, err := bridge.CreateWorkspace(request)
	if err != nil {
		t.Fatal(err)
	}
	again, err := bridge.CreateWorkspace(request)
	if err != nil || first != again || registrar.calls != 1 || registrar.selectedPath != selected.Selection.DirectoryPath {
		t.Fatal(first, again, err, registrar.calls)
	}
	request.Name = "another"
	if _, err = bridge.CreateWorkspace(request); err == nil {
		t.Fatal("selection replay changed name")
	}
	bridge.workspaceDirectoryPicker = testWorkspaceDirectoryPicker{}
	cancelled, err := bridge.SelectWorkspaceDirectory()
	if err != nil || cancelled.Status != "cancelled" || registrar.calls != 1 {
		t.Fatal(cancelled, err)
	}
}
func TestDesktopWorkspaceCreateRejectsReplacedDirectory(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "selected")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	registrar := &namedTestWorkspaceRegistrar{}
	bridge := newWorkspaceImportBridge(t, testWorkspaceDirectoryPicker{path: root}, registrar)
	selected, err := bridge.SelectWorkspaceDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(root, filepath.Join(parent, "original")); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = bridge.CreateWorkspace(CreateWorkspaceRequest{Handle: selected.Selection.Handle, Name: "changed"}); err == nil || registrar.calls != 0 {
		t.Fatal("replaced directory registered", err)
	}
}

func TestDesktopWorkspaceSelectionRegistersOnlyOnConfirmedProductionCreate(t *testing.T) {
	home := t.TempDir()
	plane, err := OpenControlPlane(ControlPlaneConfig{DatabasePath: filepath.Join(home, "state.db"), HomePath: home, ReadToken: desktopControlPlaneTestToken, AppVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	root := t.TempDir()
	bridge := newWorkspaceImportBridge(t, testWorkspaceDirectoryPicker{path: root}, plane)
	before, err := plane.stateStore.ListWorkspaces(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	selected, err := bridge.SelectWorkspaceDirectory()
	if err != nil {
		t.Fatal(err)
	}
	pending, err := plane.stateStore.ListWorkspaces(t.Context())
	if err != nil || len(pending) != len(before) {
		t.Fatal("selection registered project", pending, err)
	}
	created, err := bridge.CreateWorkspace(CreateWorkspaceRequest{Handle: selected.Selection.Handle, Name: "Custom project"})
	if err != nil {
		t.Fatal(err)
	}
	record, err := plane.stateStore.GetWorkspaceByID(t.Context(), created.ID)
	if err != nil || record.Name != "Custom project" || record.RootPath != selected.Selection.DirectoryPath {
		t.Fatal(record, err)
	}
}
