package workspacecheckpoint

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestCaptureExcludesTrackedFilesThroughLinkedParents(t *testing.T) {
	for _, inside := range []bool{false, true} {
		name := "outside workspace"
		if inside {
			name = "inside workspace"
		}
		t.Run(name, func(t *testing.T) {
			root := newCheckpointRepository(t)
			const path = "docs/nested/memo.txt"
			mustCheckpointWrite(t, filepath.Join(root, filepath.FromSlash(path)), []byte("tracked original"))
			runCheckpointGit(t, root, "add", path)
			outside := t.TempDir()
			if inside {
				outside = filepath.Join(root, "linked-target")
				mustCheckpointWrite(t, filepath.Join(root, ".gitignore"), []byte("linked-target/\n"))
			}
			content := []byte("linked content must not become a checkpoint blob")
			mustCheckpointWrite(t, filepath.Join(outside, "nested", "memo.txt"), content)
			docs := filepath.Join(root, "docs")
			if err := os.Rename(docs, filepath.Join(t.TempDir(), "original-docs")); err != nil {
				t.Fatal(err)
			}
			makeCheckpointDirectoryLink(t, docs, outside)
			snapshot, err := Capture(t.Context(), validCaptureRequest(root, time.Now().UTC()))
			if err != nil {
				t.Fatal(err)
			}
			entry := checkpointEntriesByPath(snapshot.Entries)[path]
			if entry.StoragePolicy != StorageExcludedLink || entry.Recoverable || entry.BlobSHA256 != "" ||
				entry.WorktreeSHA256 != "missing" || snapshot.Checkpoint.RecoveryLevel == RecoveryComplete {
				t.Fatalf("linked parent was captured: %+v", entry)
			}
			for _, blob := range snapshot.Blobs {
				if bytes.Equal(blob.Content, content) {
					t.Fatal("linked content persisted in snapshot")
				}
			}
		})
	}
}

func TestCaptureRootedReadRejectsParentReplacementAfterInspection(t *testing.T) {
	rootPath := t.TempDir()
	const path = "docs/memo.txt"
	mustCheckpointWrite(t, filepath.Join(rootPath, filepath.FromSlash(path)), []byte("original"))
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, linked, err := capturePathInfo(root, path); err != nil || linked {
		t.Fatalf("ordinary file inspection failed: linked=%v err=%v", linked, err)
	}
	outside := t.TempDir()
	mustCheckpointWrite(t, filepath.Join(outside, "memo.txt"), []byte("outside content"))
	docs := filepath.Join(rootPath, "docs")
	if err := os.Rename(docs, filepath.Join(t.TempDir(), "original-docs")); err != nil {
		t.Fatal(err)
	}
	makeCheckpointDirectoryLink(t, docs, outside)
	if digest, content, err := hashCaptureFile(t.Context(), root, path, true); err == nil || digest != "" || content != nil {
		t.Fatalf("read escaped after inspection: digest=%q content=%q err=%v", digest, content, err)
	}
}

func TestCaptureRejectsJunctionWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	linked := filepath.Join(t.TempDir(), "workspace-link")
	makeCheckpointDirectoryLink(t, linked, root)
	if _, err := Capture(t.Context(), validCaptureRequest(linked, time.Now().UTC())); err == nil {
		t.Fatal("linked workspace root accepted")
	}
}

func makeCheckpointDirectoryLink(t *testing.T, name, target string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", name, target).CombinedOutput()
		if err != nil {
			t.Fatalf("create fixture junction: %v: %s", err, output)
		}
		return
	}
	relative, err := filepath.Rel(filepath.Dir(name), target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(relative, name); err != nil {
		t.Fatal(err)
	}
}
