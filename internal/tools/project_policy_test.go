package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/workspaceidentity"
)

func TestLegacyFileToolsRespectProjectExclusions(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"visible.txt", "secrets/hidden.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("file contents"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fs := NewWorkspaceFS(root)
	fs.PathPolicy = workspaceidentity.PathPolicy{ExcludePaths: []string{"secrets/**"}}
	if _, err := (ReadFileTool{FS: fs}).Run(t.Context(), Call{Args: map[string]string{"path": "secrets/hidden.txt"}}); err == nil {
		t.Fatal("excluded read accepted")
	}
	listed, err := (ListWorkspaceTool{FS: fs}).Run(t.Context(), Call{Args: map[string]string{"path": "."}})
	if err != nil || strings.Contains(listed.Stdout, "secrets") || !strings.Contains(listed.Stdout, "visible.txt") {
		t.Fatalf("filtered legacy listing = %+v, %v", listed, err)
	}
}
