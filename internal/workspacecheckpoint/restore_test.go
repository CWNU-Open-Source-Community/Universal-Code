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

func TestRestoreStopsBeforeWritingWhenRecoveryPreflightIsIncomplete(t *testing.T) {
	root := newCheckpointRepository(t)
	file := filepath.Join(root, "notes.txt")
	mustCheckpointWrite(t, file, []byte("original\n"))
	runCheckpointGit(t, root, "add", "notes.txt")
	now := time.Now().UTC()
	target := captureRestoreFixture(t, root, "recovery-target", "receipt-target", now)
	mustCheckpointWrite(t, file, []byte("current\n"))
	runCheckpointGit(t, root, "add", "notes.txt")
	current := captureRestoreFixture(t, root, "recovery-current", "receipt-current", now.Add(time.Minute))
	if target.Checkpoint.IndexSHA256 == current.Checkpoint.IndexSHA256 {
		t.Fatal("expected different index states")
	}

	for _, scenario := range []struct {
		name   string
		change func(*Snapshot, *Snapshot)
		reason string
	}{
		{"truncated manifest", func(_ *Snapshot, observed *Snapshot) {
			observed.Checkpoint.RecoveryLevel = RecoveryUnavailable
			observed.Checkpoint.IncompleteReasons = []string{incompleteManifestReason}
		}, incompleteManifestReason},
		{"target index missing", func(target, _ *Snapshot) {
			target.Checkpoint.RecoveryLevel = RecoveryPartial
			index := target.Checkpoint.IndexBlobSHA256
			target.Checkpoint.IndexBlobSHA256 = ""
			target.Blobs = slices.DeleteFunc(slices.Clone(target.Blobs), func(blob Blob) bool {
				if blob.SHA256 != index {
					return false
				}
				target.Checkpoint.StoredBytes -= int64(len(blob.Content))
				return true
			})
		}, "target Git index blob is unavailable"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			want, live := target, current
			scenario.change(&want, &live)
			result, err := ApplyRestore(t.Context(), root, current, want, live)
			var conflict *ConflictError
			if !errors.As(err, &conflict) || !slices.ContainsFunc(conflict.Conflicts, func(c Conflict) bool {
				return c.Kind == ConflictUnrecoverable && c.Reason == scenario.reason
			}) || len(result.AppliedPaths) != 0 || len(result.DeletedPaths) != 0 || result.IndexRestored {
				t.Fatalf("restore should reject before writing: %+v, %v", result, err)
			}
			if content, err := os.ReadFile(file); err != nil || string(content) != "current\n" {
				t.Fatalf("restore changed the live file: %q, %v", content, err)
			}
		})
	}
}

func TestRestoreUsesGitIndexLockAndRestoresAMissingIndex(t *testing.T) {
	root := newCheckpointRepository(t)
	draft := filepath.Join(root, "draft.txt")
	mustCheckpointWrite(t, draft, []byte("draft\n"))
	targetRequest := validCaptureRequest(root, time.Now().UTC())
	targetRequest.ID = "checkpoint-index-missing"
	target, err := Capture(t.Context(), targetRequest)
	if err != nil {
		t.Fatal(err)
	}
	if target.Checkpoint.IndexBlobSHA256 != "" {
		t.Fatalf("target index unexpectedly exists: %+v", target.Checkpoint)
	}
	mustCheckpointWrite(t, draft, []byte("edited draft\n"))
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
	if content, err := os.ReadFile(draft); err != nil || string(content) != "edited draft\n" {
		t.Fatalf("locked restore changed the worktree: %q, %v", content, err)
	}
	if content, err := os.ReadFile(lockPath); err != nil || string(content) != "external lock" {
		t.Fatalf("restore changed another operation's lock: %q, %v", content, err)
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

func TestRestoreDistinguishesMissingAndEmptyGitIndexes(t *testing.T) {
	root := newCheckpointRepository(t)
	now := time.Now().UTC()
	missing := captureRestoreFixture(t, root, "missing-index", "receipt-missing", now)
	// Git refuses to inventory a zero-byte index. Use a sealed snapshot with
	// the empty blob to verify how restore handles that stored representation.
	empty := missing
	empty.Checkpoint.ID = "empty-index"
	empty.Checkpoint.IndexBlobSHA256 = missing.Checkpoint.IndexSHA256
	empty.Blobs = []Blob{{SHA256: empty.Checkpoint.IndexBlobSHA256, Content: []byte{}, CreatedAt: now}}
	index := filepath.Join(root, ".git", "index")
	if err := os.WriteFile(index, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := ApplyRestore(t.Context(), root, empty, missing, empty)
	if err != nil || !result.IndexRestored {
		t.Fatalf("restore missing index: %+v, %v", result, err)
	}
	if _, err := os.Stat(index); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("index still present: %v", err)
	}
	result, err = ApplyRestore(t.Context(), root, missing, empty, missing)
	if err != nil || !result.IndexRestored {
		t.Fatalf("restore empty index: %+v, %v", result, err)
	}
	if content, err := os.ReadFile(index); err != nil || len(content) != 0 {
		t.Fatalf("restored empty index = %q, %v", content, err)
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
