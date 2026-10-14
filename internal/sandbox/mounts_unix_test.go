//go:build unix

package sandbox

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestResolveMountSourcesRejectsFIFOWithoutWriter(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("pipe", filepath.Join(root, "pipe-link")); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"pipe", "pipe-link"} {
		done := make(chan error, 1)
		go func() { _, err := ResolveMountSources(t.Context(), root, mountResolutionManifest(source)); done <- err }()
		select {
		case err := <-done:
			if err == nil {
				t.Fatalf("accepted %s", source)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("mount source %s blocked", source)
		}
	}
}
