package workspacecheckpoint

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"cyberagent-workbench/internal/rootfs"
)

func TestRestorePreviewsAndReplaysTextBinaryRenameDeleteUntrackedAndDirtyIndex(t *testing.T) {
	root := newCheckpointRepository(t)
	mustCheckpointWrite(t, filepath.Join(root, "text.txt"), []byte("alpha\r\nbeta\r\n"))
	mustCheckpointWrite(t, filepath.Join(root, "old.bin"), []byte{0, 1, 2, 3})
	mustCheckpointWrite(t, filepath.Join(root, "removed.txt"), []byte("keep me\n"))
	runCheckpointGit(t, root, "add", ".")
	runCheckpointGit(t, root, "commit", "-m", "baseline")
	// The starting point intentionally has a dirty index. Restoring must replay
	// the raw index bytes instead of manufacturing an approximation with Git.
	mustCheckpointWrite(t, filepath.Join(root, "text.txt"), []byte("staged\r\nvalue\r\n"))
	runCheckpointGit(t, root, "add", "text.txt")
	before := captureRestoreFixture(t, root, "checkpoint-before", "receipt-before",
		time.Date(2026, 8, 19, 3, 0, 0, 0, time.UTC))

	mustCheckpointWrite(t, filepath.Join(root, "text.txt"), []byte("changed\nvalue\n"))
	if err := os.Rename(filepath.Join(root, "old.bin"), filepath.Join(root, "new.bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "removed.txt")); err != nil {
		t.Fatal(err)
	}
	mustCheckpointWrite(t, filepath.Join(root, "untracked.dat"), []byte{9, 8, 0, 7})
	runCheckpointGit(t, root, "add", "-A")
	after := captureRestoreFixture(t, root, "checkpoint-after", "receipt-after",
		time.Date(2026, 8, 19, 3, 1, 0, 0, time.UTC))

	preview, err := PreviewRestore(after, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Conflicts) != 0 || !preview.IndexChanged {
		t.Fatalf("unexpected preview: %+v", preview)
	}
	assertRestoreChange(t, preview.Changes, ChangeRename, "old.bin", "new.bin")
	assertRestoreChange(t, preview.Changes, ChangeModify, "text.txt", "")
	assertRestoreChange(t, preview.Changes, ChangeCreate, "removed.txt", "")
	assertRestoreChange(t, preview.Changes, ChangeDelete, "untracked.dat", "")

	undo, err := ApplyRestore(t.Context(), root, after, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if !undo.IndexRestored || len(undo.AppliedPaths) != 3 || len(undo.DeletedPaths) != 2 {
		t.Fatalf("unexpected undo result: %+v", undo)
	}
	observedBefore := captureRestoreFixture(t, root, "checkpoint-observed-before",
		"receipt-observed-before", time.Date(2026, 8, 19, 3, 2, 0, 0, time.UTC))
	assertRestoreState(t, observedBefore, before)

	redo, err := ApplyRestore(t.Context(), root, before, after, observedBefore)
	if err != nil {
		t.Fatal(err)
	}
	if !redo.IndexRestored {
		t.Fatalf("redo did not restore index: %+v", redo)
	}
	observedAfter := captureRestoreFixture(t, root, "checkpoint-observed-after",
		"receipt-observed-after", time.Date(2026, 8, 19, 3, 3, 0, 0, time.UTC))
	assertRestoreState(t, observedAfter, after)

	// Terminal replay is a no-op and remains safe after an event-persistence
	// interruption.
	replay, err := ApplyRestore(t.Context(), root, before, after, observedAfter)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.AppliedPaths) != 0 || len(replay.DeletedPaths) != 0 {
		t.Fatalf("completed restore replay wrote files: %+v", replay)
	}
}

func TestRestoreRecreatesAndDeletesExactWhitespacePaths(t *testing.T) {
	root := newCheckpointRepository(t)
	const scope = " src "
	const relative = scope + "/nested /notes.txt "
	const neighboring = " src/nested/notes.txt"
	const content = "exact original\n"
	native := func(name string) string {
		t.Helper()
		full, err := rootfs.ExactAbsolutePath(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		return full
	}
	dir := native(scope)
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	mustCheckpointWrite(t, native(neighboring), []byte("neighbor\n"))
	mustCheckpointWrite(t, native(relative), []byte(content))
	mustCheckpointWrite(t, native(scope+"/.env "), []byte("fixture value\n"))
	now := time.Now().UTC()
	before := captureRestoreFixture(t, root, "exact-before", "exact-receipt-before", now)
	entries := checkpointEntriesByPath(before.Entries)
	if entry := entries[relative]; entry.StoragePolicy != StorageStored || !entry.Recoverable {
		t.Fatalf("exact file was not captured: %+v", entry)
	}
	if entry := entries[scope+"/.env "]; entry.StoragePolicy != StorageExcludedSensitive {
		t.Fatalf("padded sensitive file was captured: %+v", entry)
	}
	if err := os.RemoveAll(native(scope + "/nested ")); err != nil {
		t.Fatal(err)
	}
	after := captureRestoreFixture(t, root, "exact-after", "exact-receipt-after", now.Add(time.Minute))

	undo, err := ApplyRestore(t.Context(), root, after, before, after)
	if err != nil || !slices.Equal(undo.AppliedPaths, []string{relative}) {
		t.Fatalf("undo did not recreate the exact path: %+v, %v", undo, err)
	}
	if data, err := os.ReadFile(native(relative)); err != nil || string(data) != content {
		t.Fatalf("restored file = %q, %v", data, err)
	}
	restored := captureRestoreFixture(t, root, "exact-restored", "exact-receipt-restored", now.Add(2*time.Minute))
	redo, err := ApplyRestore(t.Context(), root, before, after, restored)
	if err != nil || !slices.Equal(redo.DeletedPaths, []string{relative}) {
		t.Fatalf("redo did not delete the exact path: %+v, %v", redo, err)
	}
	if _, err := os.Stat(native(relative)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("exact path still present after redo: %v", err)
	}
	if data, err := os.ReadFile(native(neighboring)); err != nil || string(data) != "neighbor\n" {
		t.Fatalf("neighbor changed = %q, %v", data, err)
	}
}

func TestRestoreFailsClosedOnExternalWorktreeAndIndexChanges(t *testing.T) {
	root := newCheckpointRepository(t)
	mustCheckpointWrite(t, filepath.Join(root, "file.txt"), []byte("one\n"))
	runCheckpointGit(t, root, "add", ".")
	runCheckpointGit(t, root, "commit", "-m", "baseline")
	before := captureRestoreFixture(t, root, "before", "receipt-before", time.Now().UTC())
	mustCheckpointWrite(t, filepath.Join(root, "file.txt"), []byte("two\n"))
	after := captureRestoreFixture(t, root, "after", "receipt-after",
		time.Now().UTC().Add(time.Second))

	mustCheckpointWrite(t, filepath.Join(root, "file.txt"), []byte("external\n"))
	runCheckpointGit(t, root, "add", "file.txt")
	observed := captureRestoreFixture(t, root, "observed", "receipt-observed",
		time.Now().UTC().Add(2*time.Second))
	preview, err := PreviewRestore(after, before, observed)
	if err != nil {
		t.Fatal(err)
	}
	if !hasRestoreConflict(preview.Conflicts, ConflictExternalChange) ||
		!hasRestoreConflict(preview.Conflicts, ConflictIndexDrift) {
		t.Fatalf("external changes were not reported: %+v", preview.Conflicts)
	}
	_, err = ApplyRestore(t.Context(), root, after, before, observed)
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected conflict error, got %v", err)
	}
	data, readErr := os.ReadFile(filepath.Join(root, "file.txt"))
	if readErr != nil || string(data) != "external\n" {
		t.Fatalf("conflicting content was changed: %q err=%v", data, readErr)
	}
}

func TestRestoreRejectsUnrecoverableTargetContent(t *testing.T) {
	root := newCheckpointRepository(t)
	mustCheckpointWrite(t, filepath.Join(root, "base.txt"), []byte("base\n"))
	runCheckpointGit(t, root, "add", ".")
	runCheckpointGit(t, root, "commit", "-m", "baseline")
	mustCheckpointWrite(t, filepath.Join(root, ".env"), []byte("TOKEN=first\n"))
	target := captureRestoreFixture(t, root, "target", "receipt-target", time.Now().UTC())
	mustCheckpointWrite(t, filepath.Join(root, ".env"), []byte("TOKEN=second\n"))
	expected := captureRestoreFixture(t, root, "expected", "receipt-expected",
		time.Now().UTC().Add(time.Second))
	preview, err := PreviewRestore(expected, target, expected)
	if err != nil {
		t.Fatal(err)
	}
	if !hasRestoreConflict(preview.Conflicts, ConflictUnrecoverable) {
		t.Fatalf("sensitive target was not rejected: %+v", preview)
	}
}

func TestRestoreUsesGitIndexLockAndRestoresAMissingIndex(t *testing.T) {
	root := newCheckpointRepository(t)
	mustCheckpointWrite(t, filepath.Join(root, "draft.txt"), []byte("draft\n"))
	targetRequest := validCaptureRequest(root, time.Now().UTC())
	targetRequest.ID = "checkpoint-index-missing"
	target, err := Capture(t.Context(), targetRequest)
	if err != nil {
		t.Fatal(err)
	}
	if target.Checkpoint.IndexBlobSHA256 != "" {
		t.Fatalf("target index unexpectedly exists: %+v", target.Checkpoint)
	}
	runCheckpointGit(t, root, "add", "draft.txt")
	expectedRequest := validCaptureRequest(root, time.Now().UTC().Add(time.Second))
	expectedRequest.ID = "checkpoint-index-present"
	expected, err := Capture(t.Context(), expectedRequest)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := PreviewRestore(expected, target, expected)
	if err != nil || len(preview.Conflicts) != 0 || !preview.IndexChanged {
		t.Fatalf("missing-index preview=%+v err=%v", preview, err)
	}

	indexPath := filepath.Join(root, ".git", "index")
	lockPath := indexPath + ".lock"
	if err := os.WriteFile(lockPath, []byte("external lock"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyRestore(t.Context(), root, expected, target, expected); err == nil {
		t.Fatal("restore ignored an existing Git index lock")
	}
	if _, err := os.Stat(indexPath); err != nil {
		t.Fatalf("locked restore changed the Git index: %v", err)
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}

	if _, err := ApplyRestore(t.Context(), root, expected, target, expected); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(indexPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing target index was not restored: %v", err)
	}
	observedRequest := validCaptureRequest(root, time.Now().UTC().Add(2*time.Second))
	observedRequest.ID = "checkpoint-index-observed"
	observed, err := Capture(t.Context(), observedRequest)
	if err != nil || observed.Checkpoint.IndexSHA256 != target.Checkpoint.IndexSHA256 ||
		observed.Checkpoint.ManifestSHA256 != target.Checkpoint.ManifestSHA256 {
		t.Fatalf("restored missing index=%+v err=%v", observed.Checkpoint, err)
	}
}

func captureRestoreFixture(t *testing.T, root, id, receipt string, at time.Time) Snapshot {
	t.Helper()
	request := validCaptureRequest(root, at)
	request.ID = id
	request.TriggerReceiptID = receipt
	snapshot, err := Capture(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func assertRestoreChange(t *testing.T, changes []Change, kind ChangeKind, currentPath,
	previousPath string,
) {
	t.Helper()
	for _, change := range changes {
		if change.Kind == kind && change.Path == currentPath &&
			change.PreviousPath == previousPath {
			return
		}
	}
	t.Fatalf("missing %s change %s <- %s in %+v", kind, currentPath, previousPath, changes)
}

func hasRestoreConflict(conflicts []Conflict, kind ConflictKind) bool {
	for _, conflict := range conflicts {
		if conflict.Kind == kind {
			return true
		}
	}
	return false
}

func assertRestoreState(t *testing.T, observed, expected Snapshot) {
	t.Helper()
	if observed.Checkpoint.ManifestSHA256 != expected.Checkpoint.ManifestSHA256 ||
		observed.Checkpoint.IndexSHA256 != expected.Checkpoint.IndexSHA256 ||
		observed.Checkpoint.BaseCommit != expected.Checkpoint.BaseCommit ||
		observed.Checkpoint.Branch != expected.Checkpoint.Branch {
		t.Fatalf("restored state mismatch:\nobserved=%+v\nexpected=%+v",
			observed.Checkpoint, expected.Checkpoint)
	}
}
