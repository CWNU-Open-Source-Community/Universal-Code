package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
)

func TestReadReviewFileKeepsIndependentBoundAndPathChecks(t *testing.T) {
	root := t.TempDir()
	text := strings.Repeat("review line\n", 10000)
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	data, large, err := ReadReviewFile(root, "large.txt", 2*1024*1024)
	if err != nil || large || string(data) != text {
		t.Fatalf("bounded read: bytes=%d large=%v err=%v", len(data), large, err)
	}
	preview, err := Explore(root, "workspace", "large.txt")
	if err != nil || !preview.Truncated {
		t.Fatalf("ordinary explorer bound changed: %v", err)
	}
	data, large, err = ReadReviewFile(root, "large.txt", 64*1024)
	if err != nil || !large || len(data) != 0 {
		t.Fatal("oversized files must not return partial raw content")
	}
	for _, path := range []string{"../outside", ".", "a/../large.txt", "C:/outside"} {
		if _, _, err := ReadReviewFile(root, path, 1024); err == nil {
			t.Fatal("accepted", path)
		}
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, _, err := ReadReviewFile(root, "linked/secret.txt", 1024); err == nil {
		t.Fatal("followed parent symlink")
	}
}

func TestReadReviewFileRejectsReplacementAfterValidation(t *testing.T) {
	for _, mutation := range []string{"replace", "rewrite"} {
		t.Run(mutation, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "sample.txt")
			if err := os.WriteFile(path, []byte("first\n"), 0600); err != nil {
				t.Fatal(err)
			}
			_, expected, err := resolveExplorerTarget(root, "sample.txt")
			if err != nil {
				t.Fatal(err)
			}
			if mutation == "replace" {
				if err := os.Rename(path, filepath.Join(root, "original.txt")); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, []byte("replacement content\n"), 0600); err != nil {
				t.Fatal(err)
			}
			data, large, err := readReviewTarget(root, "sample.txt", 1024, expected)
			if apperror.CodeOf(err) != apperror.CodeConflict || large || len(data) != 0 {
				t.Fatalf("returned changed file: bytes=%d large=%v err=%v", len(data), large, err)
			}
		})
	}
}
