package sandbox

import (
	"cyberagent-workbench/internal/projectprep"
	"os"
	"path/filepath"
	"testing"
)

func TestPreparedGoCacheSelectsProjectAncestorAndRejectsEscape(t *testing.T) {
	root := t.TempDir()
	cache := filepath.FromSlash(projectprep.GoModuleCacheDirectory("project"))
	if err := os.MkdirAll(filepath.Join(root, cache), 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := PreparedGoModuleCache(root, "project/pkg/nested"); err != nil || got != filepath.ToSlash(cache) {
		t.Fatalf("cache=%q err=%v", got, err)
	}
	if got, err := PreparedGoModuleCache(root, "another-project"); err != nil || got != "" {
		t.Fatalf("cache crossed projects: %q %v", got, err)
	}
	if _, err := PreparedGoModuleCache(root, "../outside"); err == nil {
		t.Fatal("cache traversal admitted")
	}
}
