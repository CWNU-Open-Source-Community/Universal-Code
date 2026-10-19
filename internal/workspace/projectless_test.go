package workspace

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/store"
)

func TestProjectlessDirectoriesPersistAndStayOutOfProjects(t *testing.T) {
	database := filepath.Join(t.TempDir(), "state.db")
	state, err := store.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	manager := ProjectlessManager{Store: state}
	initial, err := manager.Directory(t.Context())
	if err != nil || !filepath.IsAbs(initial.DirectoryPath) || filepath.Base(initial.DirectoryPath) != "Universal-Code" {
		t.Fatalf("default=%+v err=%v", initial, err)
	}
	root := t.TempDir()
	if _, err := manager.SetDirectory(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	first, replayed, err := manager.Allocate(t.Context(), "draft-one")
	if err != nil || replayed || filepath.Dir(first.RootPath) != root {
		t.Fatalf("allocation=%+v replayed=%v err=%v", first, replayed, err)
	}
	if err := os.WriteFile(filepath.Join(first.RootPath, "draft.txt"), []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	second, _, err := manager.Allocate(t.Context(), "draft-two")
	if err != nil || first.ID == second.ID || first.RootPath == second.RootPath {
		t.Fatalf("independent allocation=%+v err=%v", second, err)
	}
	projects, err := state.ListWorkspaces(t.Context())
	if err != nil || len(projects) != 0 {
		t.Fatalf("internal directory exposed as project: %+v err=%v", projects, err)
	}
	page, err := state.ListWorkspacesPage(t.Context(), 0, 10)
	if err != nil || len(page) != 0 {
		t.Fatalf("internal directory exposed in project page: %+v err=%v", page, err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = store.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	manager.Store = state
	if _, err := manager.SetDirectory(t.Context(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	retried, replayed, err := manager.Allocate(t.Context(), "draft-one")
	if err != nil || !replayed || retried != first {
		t.Fatalf("durable retry=%+v original=%+v replayed=%v err=%v", retried, first, replayed, err)
	}
	if data, err := os.ReadFile(filepath.Join(retried.RootPath, "draft.txt")); err != nil || string(data) != "retained" {
		t.Fatalf("draft attachment lost: %q %v", data, err)
	}
}

func TestProjectlessConcurrentAllocationUsesOneDirectory(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	manager := ProjectlessManager{Store: state}
	root := t.TempDir()
	if _, err := manager.SetDirectory(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	results := make(chan session.WorkspaceRecord, 8)
	errors := make(chan error, 8)
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			record, _, err := manager.Allocate(t.Context(), "same-draft")
			results <- record
			errors <- err
		}()
	}
	workers.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var identity string
	for result := range results {
		if identity != "" && identity != result.ID {
			t.Fatalf("multiple workspace identities: %s and %s", identity, result.ID)
		}
		identity = result.ID
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("allocated %d directories err=%v", len(entries), err)
	}
}

func TestProjectlessFailedCreationRetriesOriginalDirectory(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	manager := ProjectlessManager{Store: state}
	root := filepath.Join(t.TempDir(), "original")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetDirectory(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root, []byte("blocking file"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.Allocate(t.Context(), "retry-draft"); err == nil {
		t.Fatal("directory failure was hidden")
	}
	if _, err := manager.SetDirectory(t.Context(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	result, replayed, err := manager.Allocate(t.Context(), "retry-draft")
	if err != nil || !replayed || filepath.Dir(result.RootPath) != root {
		t.Fatalf("failed attempt changed destination: %+v replayed=%v err=%v", result, replayed, err)
	}
}
