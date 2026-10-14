package workspacecheckpoint

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCapturePreservesTrackedUntrackedBinaryIndexAndLineEndings(t *testing.T) {
	root := newCheckpointRepository(t)
	mustCheckpointWrite(t, filepath.Join(root, "tracked.txt"), []byte("one\r\ntwo\r\n"))
	runCheckpointGit(t, root, "add", "tracked.txt")
	runCheckpointGit(t, root, "commit", "-m", "baseline")
	mustCheckpointWrite(t, filepath.Join(root, "tracked.txt"), []byte("changed\r\n"))
	runCheckpointGit(t, root, "add", "tracked.txt")
	mustCheckpointWrite(t, filepath.Join(root, "binary.dat"), []byte{0, 1, 2, 3})
	mustCheckpointWrite(t, filepath.Join(root, ".env"), []byte("TOKEN=secret-value\n"))
	mustCheckpointWrite(t, filepath.Join(root, ".gitignore"), []byte("dist/\n"))
	mustCheckpointWrite(t, filepath.Join(root, "dist", "generated.txt"), []byte("generated"))

	now := time.Date(2026, 8, 19, 1, 2, 3, 0, time.UTC)
	snapshot, err := Capture(context.Background(), validCaptureRequest(root, now))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Checkpoint.ProtocolVersion != ProtocolVersion ||
		snapshot.Checkpoint.BaseCommit == "" || snapshot.Checkpoint.BaseCommit == "unborn" ||
		snapshot.Checkpoint.IndexBlobSHA256 == "" || snapshot.Checkpoint.EntryCount != 5 ||
		snapshot.Checkpoint.RecoveryLevel != RecoveryPartial {
		t.Fatalf("unexpected checkpoint: %+v", snapshot.Checkpoint)
	}
	entries := checkpointEntriesByPath(snapshot.Entries)
	tracked := entries["tracked.txt"]
	if !tracked.Tracked || !tracked.Staged || !tracked.Recoverable ||
		tracked.StoragePolicy != StorageStored || tracked.LineEndings != "crlf" ||
		tracked.BlobSHA256 == "" {
		t.Fatalf("unexpected tracked entry: %+v", tracked)
	}
	binary := entries["binary.dat"]
	if binary.Tracked || !binary.Binary || !binary.Recoverable || binary.BlobSHA256 == "" {
		t.Fatalf("unexpected binary entry: %+v", binary)
	}
	sensitive := entries[".env"]
	if sensitive.StoragePolicy != StorageExcludedSensitive || sensitive.Recoverable ||
		sensitive.BlobSHA256 != "" {
		t.Fatalf("unexpected sensitive entry: %+v", sensitive)
	}
	ignored := entries["dist"]
	if ignored.State != StateIgnored || ignored.StoragePolicy != StorageExcludedIgnored ||
		ignored.Recoverable {
		t.Fatalf("unexpected ignored entry: %+v", ignored)
	}
	if _, exists := entries["dist/generated.txt"]; exists {
		t.Fatal("ignored directory contents must not be expanded into the manifest")
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	tampered := snapshot
	tampered.Entries = append([]Entry{}, snapshot.Entries...)
	for index := range tampered.Entries {
		if tampered.Entries[index].BlobSHA256 != "" {
			tampered.Entries[index].Size++
			break
		}
	}
	if err := tampered.Validate(); err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("entry/blob size mismatch was accepted: %v", err)
	}
}

func TestCaptureManifestIsStableAndDeletedTrackedFilesRemainRecoverable(t *testing.T) {
	root := newCheckpointRepository(t)
	mustCheckpointWrite(t, filepath.Join(root, "gone.txt"), []byte("before\n"))
	runCheckpointGit(t, root, "add", "gone.txt")
	runCheckpointGit(t, root, "commit", "-m", "baseline")
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 19, 2, 0, 0, 0, time.UTC)
	first, err := Capture(t.Context(), validCaptureRequest(root, now))
	if err != nil {
		t.Fatal(err)
	}
	request := validCaptureRequest(root, now.Add(time.Minute))
	request.ID = "checkpoint-2"
	second, err := Capture(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Checkpoint.ManifestSHA256 != second.Checkpoint.ManifestSHA256 ||
		first.Checkpoint.IndexSHA256 != second.Checkpoint.IndexSHA256 {
		t.Fatalf("stable state produced different manifests: %s / %s",
			first.Checkpoint.ManifestSHA256, second.Checkpoint.ManifestSHA256)
	}
	entry := checkpointEntriesByPath(first.Entries)["gone.txt"]
	if entry.State != StateMissing || entry.StoragePolicy != StorageMissing ||
		!entry.Recoverable || entry.WorktreeSHA256 != "missing" {
		t.Fatalf("unexpected deleted entry: %+v", entry)
	}
}

func TestCaptureExcludesLargeAndGeneratedUntrackedContent(t *testing.T) {
	root := newCheckpointRepository(t)
	mustCheckpointWrite(t, filepath.Join(root, "baseline.txt"), []byte("base\n"))
	runCheckpointGit(t, root, "add", "baseline.txt")
	runCheckpointGit(t, root, "commit", "-m", "baseline")
	mustCheckpointWrite(t, filepath.Join(root, "large.bin"),
		bytes.Repeat([]byte{0x7f}, MaxStoredFileBytes+1))
	mustCheckpointWrite(t, filepath.Join(root, "build", "result.txt"), []byte("output"))
	mustCheckpointWrite(t, filepath.Join(root, ".traverse-preparation", "npm-cache", "package.tgz"), []byte("dependency cache"))

	snapshot, err := Capture(t.Context(), validCaptureRequest(root, time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	entries := checkpointEntriesByPath(snapshot.Entries)
	if entry := entries["large.bin"]; entry.StoragePolicy != StorageExcludedLarge ||
		entry.Recoverable || entry.WorktreeSHA256 == "" || entry.WorktreeSHA256 == "missing" {
		t.Fatalf("unexpected large entry: %+v", entry)
	}
	if entry := entries["build/result.txt"]; entry.State != StateGenerated ||
		entry.StoragePolicy != StorageExcludedGenerated || entry.Recoverable {
		t.Fatalf("unexpected generated entry: %+v", entry)
	}
	if entry := entries[".traverse-preparation/npm-cache/package.tgz"]; entry.State != StateGenerated ||
		entry.StoragePolicy != StorageExcludedGenerated || entry.Recoverable {
		t.Fatalf("dependency cache entered recovery content: %+v", entry)
	}
}

func TestCaptureKeepsAMissingGitIndexAsAnExactState(t *testing.T) {
	root := newCheckpointRepository(t)
	snapshot, err := Capture(t.Context(), validCaptureRequest(root, time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Checkpoint.BaseCommit != "unborn" ||
		snapshot.Checkpoint.IndexBlobSHA256 != "" ||
		snapshot.Checkpoint.RecoveryLevel != RecoveryComplete {
		t.Fatalf("missing Git index was not represented exactly: %+v", snapshot.Checkpoint)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureKeepsUnmergedIndexExactAndMarksProjectionPartial(t *testing.T) {
	root := newCheckpointRepository(t)
	mustCheckpointWrite(t, filepath.Join(root, "conflict.txt"), []byte("base\n"))
	runCheckpointGit(t, root, "add", "conflict.txt")
	runCheckpointGit(t, root, "commit", "-m", "conflict base")
	baseBranch := runCheckpointGit(t, root, "branch", "--show-current")
	runCheckpointGit(t, root, "switch", "-q", "-c", "conflict-other")
	mustCheckpointWrite(t, filepath.Join(root, "conflict.txt"), []byte("other\n"))
	runCheckpointGit(t, root, "add", "conflict.txt")
	runCheckpointGit(t, root, "commit", "-m", "other side")
	runCheckpointGit(t, root, "switch", "-q", baseBranch)
	mustCheckpointWrite(t, filepath.Join(root, "conflict.txt"), []byte("ours\n"))
	runCheckpointGit(t, root, "add", "conflict.txt")
	runCheckpointGit(t, root, "commit", "-m", "our side")
	command := exec.Command("git", "-C", root, "merge", "--no-edit", "conflict-other")
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("fixture merge unexpectedly succeeded: %s", output)
	}

	snapshot, err := Capture(t.Context(), validCaptureRequest(root, time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Checkpoint.RecoveryLevel != RecoveryPartial ||
		snapshot.Checkpoint.IndexBlobSHA256 == "" ||
		!strings.Contains(strings.Join(snapshot.Checkpoint.IncompleteReasons, "\n"),
			"unmerged stages") {
		t.Fatalf("unmerged index recovery evidence is incomplete: %+v", snapshot.Checkpoint)
	}
	entry := checkpointEntriesByPath(snapshot.Entries)["conflict.txt"]
	if !entry.Tracked || entry.IndexOID == "" || entry.BlobSHA256 == "" {
		t.Fatalf("unmerged path lost its bounded ours projection: %+v", entry)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureRejectsLinkedWorkspaceRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating a directory symlink may require elevation on Windows")
	}
	root := t.TempDir()
	linked := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(root, linked); err != nil {
		t.Fatal(err)
	}
	_, err := Capture(t.Context(), validCaptureRequest(linked, time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("expected linked root rejection, got %v", err)
	}
}

func TestCaptureMarksWindowsPathCaseDriftUnavailable(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path casing behavior")
	}
	root := newCheckpointRepository(t)
	mustCheckpointWrite(t, filepath.Join(root, "tracked.txt"), []byte("case\n"))
	runCheckpointGit(t, root, "add", "tracked.txt")
	runCheckpointGit(t, root, "commit", "-m", "case baseline")
	original := filepath.Join(root, "tracked.txt")
	intermediate := filepath.Join(root, "case-transition.tmp")
	drifted := filepath.Join(root, "TRACKED.txt")
	if err := os.Rename(original, intermediate); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(intermediate, drifted); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Capture(t.Context(), validCaptureRequest(root, time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Checkpoint.RecoveryLevel != RecoveryUnavailable ||
		!strings.Contains(strings.Join(snapshot.Checkpoint.IncompleteReasons, "\n"),
			"Workspace path casing differs from the Git index") {
		t.Fatalf("case drift was not explicit: %+v", snapshot.Checkpoint)
	}
}

func validCaptureRequest(root string, at time.Time) CaptureRequest {
	return CaptureRequest{ID: "checkpoint-1", RunID: "run-1", MissionID: "mission-1",
		SessionID: "session-1", WorkspaceID: "workspace-1", WorkspaceRoot: root,
		AttemptID: "attempt-1", CapabilityGeneration: strings.Repeat("a", 64),
		Trigger: TriggerFileTool, Phase: PhaseBefore, TriggerReceiptID: "receipt-1",
		CreatedAt: at}
}

func checkpointEntriesByPath(values []Entry) map[string]Entry {
	result := make(map[string]Entry, len(values))
	for _, value := range values {
		result[value.Path] = value
	}
	return result
}

func newCheckpointRepository(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	runCheckpointGit(t, root, "init", "-q")
	runCheckpointGit(t, root, "config", "user.email", "checkpoint@example.invalid")
	runCheckpointGit(t, root, "config", "user.name", "Checkpoint Test")
	return root
}

func runCheckpointGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func mustCheckpointWrite(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}
