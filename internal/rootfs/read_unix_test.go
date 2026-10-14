//go:build unix

package rootfs

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestOpenReadOnlyDoesNotBlockAfterFIFOReplacement(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "input")
	if err := os.WriteFile(path, []byte("ordinary input"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if info, err := root.Stat("input"); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("pre-open stat: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		file, err := OpenReadOnly(root, "input")
		if err == nil {
			defer file.Close()
			info, statErr := file.Stat()
			if statErr != nil {
				err = statErr
			} else if info.Mode().IsRegular() {
				err = syscall.EINVAL
			}
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("FIFO open blocked before handle validation")
	}
}
