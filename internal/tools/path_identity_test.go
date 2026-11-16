package tools

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/rootfs"
)

func TestWorkspaceFSPreservesWhitespaceWithoutRedirecting(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := NewWorkspaceFS(root)
	if _, err := workspace.ResolveFileForRead("file.txt "); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("nonexistent spaced path: %v", err)
	}
	dir, err := rootfs.ExactAbsolutePath(filepath.Join(root, " src "))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	const requested = " src /file.txt "
	target, err := workspace.ResolveForCreate(requested)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("spaced"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, resolve := range []func(string) (string, error){workspace.ResolveFileForRead, workspace.ResolveForWrite} {
		resolved, err := resolve(requested)
		if err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(resolved)
		if err != nil || string(content) != "spaced" {
			t.Fatalf("read %q = %q, %v", resolved, content, err)
		}
	}
	listed, _, err := workspace.list(t.Context(), ".", 3)
	if err != nil || !strings.Contains(listed, " src "+string(os.PathSeparator)+"\n  file.txt \n") {
		t.Fatalf("listing did not visit the exact child path: %q, %v", listed, err)
	}
}
