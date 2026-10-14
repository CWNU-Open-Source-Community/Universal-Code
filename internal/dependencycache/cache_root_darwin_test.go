//go:build darwin

package dependencycache

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewCacheAcceptsDarwinSystemDirectoryAliases(t *testing.T) {
	for _, systemDirectory := range []string{"/var/tmp", "/tmp"} {
		t.Run(systemDirectory, func(t *testing.T) {
			home, err := os.MkdirTemp(systemDirectory, "traverse-cache-alias-*")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(home) })
			requested := filepath.Join(home, "dependency-cache")
			broker, err := New(requested)
			if err != nil {
				t.Fatalf("system-alias application home rejected: %v", err)
			}
			canonical, err := filepath.EvalSymlinks(requested)
			if err != nil || broker.root != canonical || canonical == requested {
				t.Fatalf("cache not bound to its canonical system directory: root=%q canonical=%q %v", broker.root, canonical, err)
			}
			linked := filepath.Join(home, "linked-cache")
			if err := os.Symlink(requested, linked); err != nil {
				t.Fatal(err)
			}
			if _, err := New(linked); err == nil {
				t.Fatal("system-alias exception accepted a linked cache subdirectory")
			}
		})
	}
}
