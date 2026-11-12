package app

import (
	"os"
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/rootfs"
)

func TestReadEditContentPreservesWhitespace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.txt")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if content, err := readEditContent(path + " "); !os.IsNotExist(err) {
		t.Fatalf("missing spaced path read %q, error %v", content, err)
	}
	exact, err := rootfs.ExactAbsolutePath(path + " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exact, []byte("spaced"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(exact) })
	if content, err := readEditContent(path + " "); err != nil || content != "spaced" {
		t.Fatalf("spaced path read %q, error %v", content, err)
	}
}
