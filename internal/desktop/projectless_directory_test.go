package desktop

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/workspace"
)

func TestChooseProjectlessDirectoryUsesNativeSelectionOnly(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	plane := &ControlPlane{stateStore: state}
	root := t.TempDir()
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	bridge := newWorkspaceImportBridge(t, testWorkspaceDirectoryPicker{path: root}, plane)
	bridge.bootstrap.ProjectlessWorkspacesEnabled = true
	method, _ := reflect.TypeFor[*DesktopBridge]().MethodByName("ChooseProjectlessDirectory")
	if method.Type.NumIn() != 1 {
		t.Fatal("renderer can pass an arbitrary host path")
	}
	result, err := bridge.ChooseProjectlessDirectory()
	if err != nil || result.Status != "selected" || result.Settings == nil || result.Settings.DirectoryPath != canonicalRoot {
		t.Fatalf("selection=%+v err=%v", result, err)
	}
	stored, err := (workspace.ProjectlessManager{Store: state}).Directory(t.Context())
	if err != nil || stored.DirectoryPath != canonicalRoot {
		t.Fatalf("setting did not persist: %+v %v", stored, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("selection created content: %d %v", len(entries), err)
	}
	bridge.workspaceDirectoryPicker = testWorkspaceDirectoryPicker{}
	cancelled, err := bridge.ChooseProjectlessDirectory()
	if err != nil || cancelled.Status != "cancelled" || cancelled.Settings != nil {
		t.Fatalf("cancelled=%+v err=%v", cancelled, err)
	}
	stored, err = (workspace.ProjectlessManager{Store: state}).Directory(t.Context())
	if err != nil || stored.DirectoryPath != canonicalRoot {
		t.Fatalf("cancellation changed saved root: %+v %v", stored, err)
	}
}
