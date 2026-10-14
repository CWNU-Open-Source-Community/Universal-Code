//go:build unix

package application

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestEmbeddedAnalyzerRejectsFIFOWithoutWriter(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("pipe", filepath.Join(root, "pipe-link")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pipe", "pipe-link"} {
		done := make(chan error, 1)
		go func() { _, err := readEmbeddedAnalyzerWorkspaceFile(root, name); done <- err }()
		select {
		case err := <-done:
			if err == nil {
				t.Fatalf("accepted analyzer input %s", name)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("analyzer input %s blocked", name)
		}
	}
}
