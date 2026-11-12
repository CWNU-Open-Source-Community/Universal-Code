package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceFSRejectsSurroundingWhitespaceWithoutRedirecting(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	fs := NewWorkspaceFS(root)
	for _, path := range []string{" file.txt", "file.txt ", "\tfile.txt"} {
		for name, resolve := range map[string]func(string) (string, error){
			"read": fs.ResolveFileForRead, "write": fs.ResolveForWrite, "create": fs.ResolveForCreate,
		} {
			if got, err := resolve(path); err == nil {
				t.Errorf("%s(%q) redirected to %q instead of rejecting it", name, path, got)
			}
		}
	}
}
