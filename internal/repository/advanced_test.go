package repository

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/gitadvanced"
	"cyberagent-workbench/internal/runner"
)

func newAdvancedExecutor(t *testing.T) *AdvancedExecutor {
	t.Helper()
	executor, err := NewAdvancedExecutor(filepath.Join(t.TempDir(), "managed"), true)
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func advancedSpec(operation gitadvanced.Operation) gitadvanced.Spec {
	return gitadvanced.Spec{ProtocolVersion: gitadvanced.ProtocolVersion,
		Operation: operation}
}

func TestAdvancedHunkIdentityStagesSelectionAndRejectsDrift(t *testing.T) {
	ctx := context.Background()
	root := newMutationRepo(t)
	content := strings.Join([]string{"zero", "one", "two", "three", "four", "five",
		"six", "seven", "eight", "nine", "ten", "eleven", "twelve"}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", root, "add", "base.txt")
	fixtureGit(t, "-C", root, "commit", "--quiet", "-m", "expand fixture")
	changed := strings.Replace(content, "one\n", "ONE\n", 1)
	changed = strings.Replace(changed, "eleven\n", "ELEVEN\n", 1)
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	executor := newAdvancedExecutor(t)
	discovery, err := executor.ReviewAdvanced(ctx, root, advancedSpec(gitadvanced.HunkStage))
	if err != nil {
		t.Fatal(err)
	}
	if len(discovery.Hunks) != 2 || discovery.Hunks[0].ID == discovery.Hunks[1].ID {
		t.Fatalf("hunk discovery is not content-addressed: %#v", discovery.Hunks)
	}
	selected := advancedSpec(gitadvanced.HunkStage)
	selected.HunkIDs = []string{discovery.Hunks[0].ID}
	preview, err := executor.ReviewAdvanced(ctx, root, selected)
	if err != nil || len(preview.Hunks) != 1 {
		t.Fatalf("selected hunk preview: %v %#v", err, preview.Hunks)
	}
	receipt, err := executor.ExecuteAdvanced(ctx, root, preview)
	if err != nil || receipt.Status != gitadvanced.ReceiptSucceeded {
		t.Fatalf("stage selected hunk: %v %#v", err, receipt)
	}
	indexContent, err := exec.Command("git", "-C", root, "show", ":base.txt").Output()
	if err != nil || len(preview.Files) != 1 ||
		preview.Files[0].AfterSHA256 != digestBytes(indexContent) {
		t.Fatalf("selected stage projected the wrong exact index hash: %v %#v", err,
			preview.Files)
	}
	staged := fixtureGit(t, "-C", root, "diff", "--cached")
	if !strings.Contains(staged, "ONE") || strings.Contains(staged, "ELEVEN") {
		t.Fatalf("stage changed more than selected hunk: %s", staged)
	}

	unstageDiscovery, err := executor.ReviewAdvanced(ctx, root,
		advancedSpec(gitadvanced.HunkUnstage))
	if err != nil || len(unstageDiscovery.Hunks) != 1 {
		t.Fatalf("unstage discovery: %v %#v", err, unstageDiscovery.Hunks)
	}
	unstageSpec := advancedSpec(gitadvanced.HunkUnstage)
	unstageSpec.HunkIDs = []string{unstageDiscovery.Hunks[0].ID}
	unstagePreview, err := executor.ReviewAdvanced(ctx, root, unstageSpec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte(changed+"drift\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = executor.ExecuteAdvanced(ctx, root, unstagePreview)
	var advancedErr *gitadvanced.Error
	if !errors.As(err, &advancedErr) || advancedErr.Code != gitadvanced.FailureStalePreview {
		t.Fatalf("drifted hunk preview executed: %v", err)
	}
}

func TestAdvancedHunkRevertRestoresOnlySelectedContent(t *testing.T) {
	root := newMutationRepo(t)
	original := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk\n"
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", root, "add", "base.txt")
	fixtureGit(t, "-C", root, "commit", "--quiet", "-m", "revert fixture")
	changed := strings.Replace(original, "b\n", "B\n", 1)
	changed = strings.Replace(changed, "j\n", "J\n", 1)
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	executor := newAdvancedExecutor(t)
	discovery, err := executor.ReviewAdvanced(t.Context(), root,
		advancedSpec(gitadvanced.HunkRevert))
	if err != nil || len(discovery.Hunks) != 2 {
		t.Fatalf("revert discovery: %v %#v", err, discovery.Hunks)
	}
	spec := advancedSpec(gitadvanced.HunkRevert)
	spec.HunkIDs = []string{discovery.Hunks[0].ID}
	preview, err := executor.ReviewAdvanced(t.Context(), root, spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.ExecuteAdvanced(t.Context(), root, preview); err != nil {
		t.Fatal(err)
	}
	value, err := os.ReadFile(filepath.Join(root, "base.txt"))
	if err != nil || strings.Contains(string(value), "B\n") || !strings.Contains(string(value), "J\n") {
		t.Fatalf("selected revert produced wrong content: %v %q", err, value)
	}
	if len(preview.Files) != 1 || preview.Files[0].AfterSHA256 != digestBytes(value) {
		t.Fatalf("selected revert projected the wrong exact worktree hash: %#v", preview.Files)
	}
}

func TestAdvancedHunkProjectionPreservesMissingFinalNewline(t *testing.T) {
	source := []byte("one\ntwo")
	hunks := []parsedAdvancedHunk{{oldStart: 2, newStart: 2,
		body: "@@ -2 +2 @@\n-two\n\\ No newline at end of file\n+TWO\n\\ No newline at end of file\n"}}
	projected, err := applyAdvancedHunkProjection(source, hunks, false)
	if err != nil || string(projected) != "one\nTWO" {
		t.Fatalf("no-newline projection: %v %q", err, projected)
	}
	restored, err := applyAdvancedHunkProjection(projected, hunks, true)
	if err != nil || string(restored) != string(source) {
		t.Fatalf("reverse no-newline projection: %v %q", err, restored)
	}
}

func TestAdvancedHunkPathsAreAlwaysLiteralPathspecs(t *testing.T) {
	root := newMutationRepo(t)
	for _, name := range []string{"[a].txt", "a.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("original\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fixtureGit(t, "-C", root, "add", "--", "[a].txt", "a.txt")
	fixtureGit(t, "-C", root, "commit", "--quiet", "-m", "pathspec fixture")
	for _, name := range []string{"[a].txt", "a.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("changed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	spec := advancedSpec(gitadvanced.HunkStage)
	spec.Paths = []string{"[a].txt"}
	preview, err := newAdvancedExecutor(t).ReviewAdvanced(t.Context(), root, spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Hunks) != 1 || preview.Hunks[0].Path != "[a].txt" {
		t.Fatalf("Git interpreted a caller path as pathspec syntax: %#v", preview.Hunks)
	}
}

func TestAdvancedHunkRejectsLinkedParentDirectory(t *testing.T) {
	root := newMutationRepo(t)
	directory := filepath.Join(root, "linked")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "base.txt"), []byte("tracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", root, "add", "linked/base.txt")
	fixtureGit(t, "-C", root, "commit", "--quiet", "-m", "linked parent fixture")
	if err := os.Remove(filepath.Join(directory, "base.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "base.txt"), []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		command := exec.Command("cmd", "/c", "mklink", "/J", directory, outside)
		if output, err := command.CombinedOutput(); err != nil {
			t.Skipf("junction unavailable: %v: %s", err, output)
		}
	} else if err := os.Symlink(outside, directory); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	executor := newAdvancedExecutor(t)
	if _, err := executor.captureAdvancedFileIdentity(t.Context(), root,
		"linked/base.txt"); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "link") {
		t.Fatalf("linked hunk parent was accepted: %v", err)
	}
}

func TestAdvancedStashConflictRetainsExactStash(t *testing.T) {
	root := newMutationRepo(t)
	executor := newAdvancedExecutor(t)
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("stash side\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	create := advancedSpec(gitadvanced.StashCreate)
	create.Message = "issue-117 exact stash"
	createPreview, err := executor.ReviewAdvanced(t.Context(), root, create)
	if err != nil {
		t.Fatal(err)
	}
	created, err := executor.ExecuteAdvanced(t.Context(), root, createPreview)
	if err != nil || !gitadvanced.ValidObjectID(created.TargetOID) {
		t.Fatalf("create stash: %v %#v", err, created)
	}
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("other side\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", root, "add", "base.txt")
	fixtureGit(t, "-C", root, "commit", "--quiet", "-m", "conflicting side")
	pop := advancedSpec(gitadvanced.StashPop)
	pop.StashOID = created.TargetOID
	popPreview, err := executor.ReviewAdvanced(t.Context(), root, pop)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := executor.ExecuteAdvanced(t.Context(), root, popPreview)
	if err != nil || receipt.Status != gitadvanced.ReceiptConflicted || !receipt.Conflict.Active {
		t.Fatalf("stash conflict was not durable: %v %#v", err, receipt)
	}
	if _, err := executor.requireStash(t.Context(), root, created.TargetOID); err != nil {
		t.Fatalf("conflicted pop discarded the original stash: %v", err)
	}
}

func TestAdvancedStashListShowsExactIndexWorktreeAndUntrackedState(t *testing.T) {
	root := newMutationRepo(t)
	executor := newAdvancedExecutor(t)
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("worktree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "staged.txt"), []byte("index\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", root, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := advancedSpec(gitadvanced.StashCreate)
	spec.Message = "typed stash inventory"
	spec.IncludeUntracked = true
	preview, err := executor.ReviewAdvanced(t.Context(), root, spec)
	if err != nil || len(preview.Files) != 3 {
		t.Fatalf("stash preview: %v %#v", err, preview.Files)
	}
	changes := make(map[string]string)
	for _, file := range preview.Files {
		changes[file.Path] = file.Change
	}
	if changes["base.txt"] != "worktree" || changes["staged.txt"] != "index" ||
		changes["untracked.txt"] != "untracked" {
		t.Fatalf("stash preview lost state roles: %#v", changes)
	}
	receipt, err := executor.ExecuteAdvanced(t.Context(), root, preview)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := executor.ListAdvancedStashes(t.Context(), root, 10)
	if err != nil || len(entries) != 1 || entries[0].OID != receipt.TargetOID ||
		entries[0].BaseCommit == "" || entries[0].IndexCommit == "" ||
		entries[0].UntrackedCommit == "" || len(entries[0].Files) != 3 {
		t.Fatalf("stash observation: %v %#v", err, entries)
	}
	changes = make(map[string]string)
	for _, file := range entries[0].Files {
		changes[file.Path] = file.Change
		if file.AfterSHA256 == "" {
			t.Fatalf("stash file lacks exact content hash: %#v", file)
		}
	}
	if changes["base.txt"] != "worktree" || changes["staged.txt"] != "index" ||
		changes["untracked.txt"] != "untracked" {
		t.Fatalf("stash observation lost state roles: %#v", changes)
	}
}

func TestAdvancedStashApplyRejectsIgnoredWorktreeCollision(t *testing.T) {
	root := newMutationRepo(t)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", root, "add", ".gitignore")
	fixtureGit(t, "-C", root, "commit", "--quiet", "-m", "ignore collision fixture")
	if err := os.WriteFile(filepath.Join(root, "ignored.txt"), []byte("stashed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", root, "add", "--force", "ignored.txt")
	fixtureGit(t, "-C", root, "stash", "push", "--quiet", "--message", "ignored collision")
	stashOID := fixtureGit(t, "-C", root, "rev-parse", "refs/stash")
	precious := []byte("precious ignored content\n")
	if err := os.WriteFile(filepath.Join(root, "ignored.txt"), precious, 0o600); err != nil {
		t.Fatal(err)
	}

	spec := advancedSpec(gitadvanced.StashApply)
	spec.StashOID = stashOID
	preview, err := newAdvancedExecutor(t).ReviewAdvanced(t.Context(), root, spec)
	if err != nil || preview.Executable() ||
		!strings.Contains(strings.Join(preview.BlockedReasons, " "), "ignored worktree path ignored.txt") {
		t.Fatalf("ignored stash collision was not blocked: %v %#v", err, preview.BlockedReasons)
	}
	content, err := os.ReadFile(filepath.Join(root, "ignored.txt"))
	if err != nil || string(content) != string(precious) {
		t.Fatalf("stash preview changed ignored content: %v %q", err, content)
	}
}

func TestAdvancedStashRejectsSymlinkAndSubmoduleEntries(t *testing.T) {
	root := newMutationRepo(t)
	head := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	fixtureGit(t, "-C", root, "update-index", "--add", "--cacheinfo",
		"160000,"+head+",nested-module")
	spec := advancedSpec(gitadvanced.StashCreate)
	spec.Message = "unsafe tree entry"
	if _, err := newAdvancedExecutor(t).ReviewAdvanced(t.Context(), root, spec); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "submodule") {
		t.Fatalf("submodule stash entry was not rejected: %v", err)
	}
	fixtureGit(t, "-C", root, "reset", "--quiet", "HEAD", "--", "nested-module")
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.Symlink("base.txt", filepath.Join(root, "linked.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	fixtureGit(t, "-C", root, "add", "linked.txt")
	if _, err := newAdvancedExecutor(t).ReviewAdvanced(t.Context(), root, spec); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "symlink") {
		t.Fatalf("symlink stash entry was not rejected: %v", err)
	}
}

func TestAdvancedRebaseConflictCanAbortToOriginalHead(t *testing.T) {
	root := newMutationRepo(t)
	fixtureGit(t, "-C", root, "config", "core.autocrlf", "false")
	base := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	baseBranch := fixtureGit(t, "-C", root, "branch", "--show-current")
	fixtureGit(t, "-C", root, "switch", "--quiet", "-c", "feature-local")
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", root, "add", "base.txt")
	fixtureGit(t, "-C", root, "commit", "--quiet", "-m", "feature")
	featureHead := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	fixtureGit(t, "-C", root, "switch", "--quiet", baseBranch)
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("main side\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", root, "add", "base.txt")
	fixtureGit(t, "-C", root, "commit", "--quiet", "-m", "main side")
	onto := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	fixtureGit(t, "-C", root, "switch", "--quiet", "feature-local")

	executor := newAdvancedExecutor(t)
	start := advancedSpec(gitadvanced.RebaseStart)
	start.UpstreamOID, start.OntoOID = base, onto
	preview, err := executor.ReviewAdvanced(t.Context(), root, start)
	if err != nil || !preview.Executable() {
		t.Fatalf("rebase preview: %v %#v status=%q", err, preview.BlockedReasons,
			fixtureGit(t, "-C", root, "status", "--porcelain=v1"))
	}
	receipt, err := executor.ExecuteAdvanced(t.Context(), root, preview)
	if err != nil || receipt.Status != gitadvanced.ReceiptConflicted || receipt.SequenceID == "" {
		t.Fatalf("rebase did not pause in conflict: %v %#v", err, receipt)
	}
	abort := advancedSpec(gitadvanced.RebaseAbort)
	abort.SequenceID = receipt.SequenceID
	abortPreview, err := executor.ReviewAdvanced(t.Context(), root, abort)
	if err != nil || !abortPreview.Executable() {
		t.Fatalf("rebase abort preview: %v %#v", err, abortPreview.BlockedReasons)
	}
	aborted, err := executor.ExecuteAdvanced(t.Context(), root, abortPreview)
	if err != nil || aborted.Status != gitadvanced.ReceiptSucceeded {
		t.Fatalf("rebase abort: %v %#v", err, aborted)
	}
	if head := fixtureGit(t, "-C", root, "rev-parse", "HEAD"); head != featureHead {
		t.Fatalf("rebase abort did not restore original head: got %s want %s", head, featureHead)
	}
}

func TestAdvancedRebaseDisablesConfiguredUpdateRefs(t *testing.T) {
	root := newMutationRepo(t)
	fixtureGit(t, "-C", root, "config", "core.autocrlf", "false")
	base := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	baseBranch := fixtureGit(t, "-C", root, "branch", "--show-current")
	fixtureGit(t, "-C", root, "switch", "--quiet", "-c", "update-refs-feature")
	if err := os.WriteFile(filepath.Join(root, "feature.txt"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", root, "add", "feature.txt")
	fixtureGit(t, "-C", root, "commit", "--quiet", "-m", "feature")
	featureHead := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	fixtureGit(t, "-C", root, "branch", "shared-pointer", featureHead)
	fixtureGit(t, "-C", root, "switch", "--quiet", baseBranch)
	if err := os.WriteFile(filepath.Join(root, "onto.txt"), []byte("onto\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", root, "add", "onto.txt")
	fixtureGit(t, "-C", root, "commit", "--quiet", "-m", "onto")
	onto := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	fixtureGit(t, "-C", root, "switch", "--quiet", "update-refs-feature")
	fixtureGit(t, "-C", root, "config", "rebase.updateRefs", "true")

	executor := newAdvancedExecutor(t)
	spec := advancedSpec(gitadvanced.RebaseStart)
	spec.UpstreamOID, spec.OntoOID = base, onto
	preview, err := executor.ReviewAdvanced(t.Context(), root, spec)
	if err != nil || !preview.Executable() {
		t.Fatalf("update-refs rebase preview: %v %#v", err, preview.BlockedReasons)
	}
	receipt, err := executor.ExecuteAdvanced(t.Context(), root, preview)
	if err != nil || receipt.Status != gitadvanced.ReceiptSucceeded {
		t.Fatalf("update-refs rebase: %v %#v", err, receipt)
	}
	if pointer := fixtureGit(t, "-C", root, "rev-parse", "shared-pointer"); pointer != featureHead {
		t.Fatalf("repository rebase.updateRefs changed another branch: got %s want %s",
			pointer, featureHead)
	}
}

func TestAdvancedRebaseConflictCanContinueOrSkip(t *testing.T) {
	for _, control := range []gitadvanced.Operation{
		gitadvanced.RebaseContinue, gitadvanced.RebaseSkip,
	} {
		t.Run(string(control), func(t *testing.T) {
			root := newMutationRepo(t)
			fixtureGit(t, "-C", root, "config", "core.autocrlf", "false")
			base := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
			baseBranch := fixtureGit(t, "-C", root, "branch", "--show-current")
			fixtureGit(t, "-C", root, "switch", "--quiet", "-c", "feature-control")
			if err := os.WriteFile(filepath.Join(root, "base.txt"),
				[]byte("feature control\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			fixtureGit(t, "-C", root, "commit", "-qam", "feature control")
			fixtureGit(t, "-C", root, "switch", "--quiet", baseBranch)
			if err := os.WriteFile(filepath.Join(root, "base.txt"),
				[]byte("onto control\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			fixtureGit(t, "-C", root, "commit", "-qam", "onto control")
			onto := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
			fixtureGit(t, "-C", root, "switch", "--quiet", "feature-control")

			executor := newAdvancedExecutor(t)
			start := advancedSpec(gitadvanced.RebaseStart)
			start.UpstreamOID, start.OntoOID = base, onto
			preview, err := executor.ReviewAdvanced(t.Context(), root, start)
			if err != nil || !preview.Executable() {
				t.Fatalf("rebase control preview: %v %#v", err, preview.BlockedReasons)
			}
			started, err := executor.ExecuteAdvanced(t.Context(), root, preview)
			if err != nil || started.Status != gitadvanced.ReceiptConflicted {
				t.Fatalf("rebase control did not conflict: %v %#v", err, started)
			}
			if control == gitadvanced.RebaseContinue {
				if err := os.WriteFile(filepath.Join(root, "base.txt"),
					[]byte("resolved rebase control\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				fixtureGit(t, "-C", root, "add", "base.txt")
			}
			spec := advancedSpec(control)
			spec.SequenceID = started.SequenceID
			controlPreview, err := executor.ReviewAdvanced(t.Context(), root, spec)
			if err != nil || !controlPreview.Executable() {
				t.Fatalf("rebase %s preview: %v %#v", control, err,
					controlPreview.BlockedReasons)
			}
			completed, err := executor.ExecuteAdvanced(t.Context(), root, controlPreview)
			if err != nil || completed.Status != gitadvanced.ReceiptSucceeded {
				t.Fatalf("rebase %s: %v %#v", control, err, completed)
			}
			observation, err := executor.InspectAdvancedSequence(t.Context(), root)
			if err != nil || observation.Active {
				t.Fatalf("rebase %s left an active sequencer: %v %#v", control, err,
					observation)
			}
		})
	}
}

func TestAdvancedCherryPickConflictCanAbortToOriginalHead(t *testing.T) {
	root := newMutationRepo(t)
	baseBranch := fixtureGit(t, "-C", root, "branch", "--show-current")
	fixtureGit(t, "-C", root, "switch", "--quiet", "-c", "picked-source")
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("picked side\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", root, "add", "base.txt")
	fixtureGit(t, "-C", root, "commit", "--quiet", "-m", "picked source")
	picked := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	fixtureGit(t, "-C", root, "switch", "--quiet", baseBranch)
	fixtureGit(t, "-C", root, "switch", "--quiet", "-c", "cherry-destination")
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("destination side\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", root, "add", "base.txt")
	fixtureGit(t, "-C", root, "commit", "--quiet", "-m", "destination side")
	original := fixtureGit(t, "-C", root, "rev-parse", "HEAD")

	executor := newAdvancedExecutor(t)
	start := advancedSpec(gitadvanced.CherryPickStart)
	start.Commits = []string{picked}
	preview, err := executor.ReviewAdvanced(t.Context(), root, start)
	if err != nil || !preview.Executable() {
		t.Fatalf("cherry-pick preview: %v %#v", err, preview.BlockedReasons)
	}
	receipt, err := executor.ExecuteAdvanced(t.Context(), root, preview)
	if err != nil || receipt.Status != gitadvanced.ReceiptConflicted ||
		receipt.SequenceID == "" || !receipt.Conflict.Active {
		t.Fatalf("cherry-pick did not pause in conflict: %v %#v", err, receipt)
	}
	abort := advancedSpec(gitadvanced.CherryPickAbort)
	abort.SequenceID = receipt.SequenceID
	abortPreview, err := executor.ReviewAdvanced(t.Context(), root, abort)
	if err != nil || !abortPreview.Executable() {
		t.Fatalf("cherry-pick abort preview: %v %#v", err, abortPreview.BlockedReasons)
	}
	if _, err := executor.ExecuteAdvanced(t.Context(), root, abortPreview); err != nil {
		t.Fatal(err)
	}
	if head := fixtureGit(t, "-C", root, "rev-parse", "HEAD"); head != original {
		t.Fatalf("cherry-pick abort did not restore head: got %s want %s", head, original)
	}
}

func TestAdvancedCherryPickConflictCanContinueOrSkip(t *testing.T) {
	for _, control := range []gitadvanced.Operation{
		gitadvanced.CherryPickContinue, gitadvanced.CherryPickSkip,
	} {
		t.Run(string(control), func(t *testing.T) {
			root := newMutationRepo(t)
			fixtureGit(t, "-C", root, "config", "core.autocrlf", "false")
			baseBranch := fixtureGit(t, "-C", root, "branch", "--show-current")
			fixtureGit(t, "-C", root, "switch", "--quiet", "-c", "pick-control-source")
			if err := os.WriteFile(filepath.Join(root, "base.txt"),
				[]byte("pick control source\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			fixtureGit(t, "-C", root, "commit", "-qam", "pick control source")
			picked := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
			fixtureGit(t, "-C", root, "switch", "--quiet", baseBranch)
			fixtureGit(t, "-C", root, "switch", "--quiet", "-c", "pick-control-target")
			if err := os.WriteFile(filepath.Join(root, "base.txt"),
				[]byte("pick control target\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			fixtureGit(t, "-C", root, "commit", "-qam", "pick control target")

			executor := newAdvancedExecutor(t)
			start := advancedSpec(gitadvanced.CherryPickStart)
			start.Commits = []string{picked}
			preview, err := executor.ReviewAdvanced(t.Context(), root, start)
			if err != nil || !preview.Executable() {
				t.Fatalf("cherry-pick control preview: %v %#v", err,
					preview.BlockedReasons)
			}
			started, err := executor.ExecuteAdvanced(t.Context(), root, preview)
			if err != nil || started.Status != gitadvanced.ReceiptConflicted {
				t.Fatalf("cherry-pick control did not conflict: %v %#v", err, started)
			}
			if control == gitadvanced.CherryPickContinue {
				if err := os.WriteFile(filepath.Join(root, "base.txt"),
					[]byte("resolved cherry-pick control\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				fixtureGit(t, "-C", root, "add", "base.txt")
			}
			spec := advancedSpec(control)
			spec.SequenceID = started.SequenceID
			controlPreview, err := executor.ReviewAdvanced(t.Context(), root, spec)
			if err != nil || !controlPreview.Executable() {
				t.Fatalf("cherry-pick %s preview: %v %#v", control, err,
					controlPreview.BlockedReasons)
			}
			completed, err := executor.ExecuteAdvanced(t.Context(), root, controlPreview)
			if err != nil || completed.Status != gitadvanced.ReceiptSucceeded {
				t.Fatalf("cherry-pick %s: %v %#v", control, err, completed)
			}
			observation, err := executor.InspectAdvancedSequence(t.Context(), root)
			if err != nil || observation.Active {
				t.Fatalf("cherry-pick %s left an active sequencer: %v %#v", control,
					err, observation)
			}
		})
	}
}

func TestAdvancedCherryPickDisablesRepositorySigningAgent(t *testing.T) {
	root := newMutationRepo(t)
	fixtureGit(t, "-C", root, "checkout", "--quiet", "-b", "signing-target")
	targetBranch := fixtureGit(t, "-C", root, "branch", "--show-current")
	fixtureGit(t, "-C", root, "checkout", "--quiet", "-b", "signing-source")
	if err := os.WriteFile(filepath.Join(root, "picked.txt"), []byte("picked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", root, "add", "picked.txt")
	fixtureGit(t, "-C", root, "commit", "--quiet", "-m", "picked without fixture signing")
	picked := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	fixtureGit(t, "-C", root, "checkout", "--quiet", targetBranch)
	fixtureGit(t, "-C", root, "config", "commit.gpgSign", "true")
	fixtureGit(t, "-C", root, "config", "gpg.program", "definitely-not-a-real-signing-program")

	executor := newAdvancedExecutor(t)
	spec := advancedSpec(gitadvanced.CherryPickStart)
	spec.Commits = []string{picked}
	preview, err := executor.ReviewAdvanced(t.Context(), root, spec)
	if err != nil || !preview.Executable() {
		t.Fatalf("signed cherry-pick preview: %v %#v", err, preview.BlockedReasons)
	}
	receipt, err := executor.ExecuteAdvanced(t.Context(), root, preview)
	if err != nil || receipt.Status != gitadvanced.ReceiptSucceeded {
		t.Fatalf("repository signing configuration escaped the hardened Git environment: %v %#v",
			err, receipt)
	}
}

func TestAdvancedCherryPickRejectsIgnoredWorktreeCollision(t *testing.T) {
	root := newMutationRepo(t)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", root, "add", ".gitignore")
	fixtureGit(t, "-C", root, "commit", "--quiet", "-m", "ignore collision fixture")
	base := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	fixtureGit(t, "-C", root, "switch", "--quiet", "-c", "ignored-collision-source")
	if err := os.WriteFile(filepath.Join(root, "ignored.txt"), []byte("picked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", root, "add", "--force", "ignored.txt")
	fixtureGit(t, "-C", root, "commit", "--quiet", "-m", "add ignored path")
	picked := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	fixtureGit(t, "-C", root, "switch", "--quiet", "-c", "ignored-collision-target", base)
	precious := []byte("precious ignored content\n")
	if err := os.WriteFile(filepath.Join(root, "ignored.txt"), precious, 0o600); err != nil {
		t.Fatal(err)
	}

	spec := advancedSpec(gitadvanced.CherryPickStart)
	spec.Commits = []string{picked}
	preview, err := newAdvancedExecutor(t).ReviewAdvanced(t.Context(), root, spec)
	if err != nil || preview.Executable() ||
		!strings.Contains(strings.Join(preview.BlockedReasons, " "), "ignored worktree path ignored.txt") {
		t.Fatalf("ignored cherry-pick collision was not blocked: %v %#v", err, preview.BlockedReasons)
	}
	content, err := os.ReadFile(filepath.Join(root, "ignored.txt"))
	if err != nil || string(content) != string(precious) {
		t.Fatalf("cherry-pick preview changed ignored content: %v %q", err, content)
	}
}

func TestAdvancedHistoryRewriteRejectsDetachedHead(t *testing.T) {
	root := newMutationRepo(t)
	base := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", root, "add", "base.txt")
	fixtureGit(t, "-C", root, "commit", "--quiet", "-m", "second")
	fixtureGit(t, "-C", root, "checkout", "--quiet", "--detach", "HEAD")
	spec := advancedSpec(gitadvanced.RebaseStart)
	spec.UpstreamOID, spec.OntoOID = base, base
	preview, err := newAdvancedExecutor(t).ReviewAdvanced(t.Context(), root, spec)
	if err != nil || preview.Executable() || !preview.Binding.Detached ||
		!strings.Contains(strings.Join(preview.BlockedReasons, " "), "attached local branch") {
		t.Fatalf("detached rewrite was not blocked: err=%v preview=%#v", err, preview)
	}
}

func TestAdvancedCherryPickRejectsProtectedBranchHistory(t *testing.T) {
	root := newMutationRepo(t)
	base := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	protected := fixtureGit(t, "-C", root, "branch", "--show-current")
	fixtureGit(t, "-C", root, "checkout", "-q", "-b", "candidate")
	if err := os.WriteFile(filepath.Join(root, "candidate.txt"),
		[]byte("candidate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", root, "add", "candidate.txt")
	fixtureGit(t, "-C", root, "commit", "--quiet", "-m", "candidate")
	candidate := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	fixtureGit(t, "-C", root, "checkout", "-q", protected)
	if head := fixtureGit(t, "-C", root, "rev-parse", "HEAD"); head != base {
		t.Fatalf("protected branch head changed: %s", head)
	}
	executor := newAdvancedExecutor(t)
	spec := advancedSpec(gitadvanced.CherryPickStart)
	spec.Commits = []string{candidate}
	preview, err := executor.ReviewAdvanced(t.Context(), root, spec)
	if err != nil || preview.Executable() ||
		!strings.Contains(strings.Join(preview.BlockedReasons, " "), "protected branch") {
		t.Fatalf("protected branch cherry-pick was not blocked: %v %#v",
			err, preview.BlockedReasons)
	}
}

func TestAdvancedExecutionRejectsExactUpstreamDrift(t *testing.T) {
	root := newMutationRepo(t)
	branch := fixtureGit(t, "-C", root, "branch", "--show-current")
	head := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	tree := fixtureGit(t, "-C", root, "rev-parse", "HEAD^{tree}")
	alternate := fixtureGit(t, "-C", root, "commit-tree", tree, "-p", head,
		"-m", "alternate upstream identity")
	fixtureGit(t, "-C", root, "branch", "observed-upstream", head)
	fixtureGit(t, "-C", root, "config", "branch."+branch+".remote", ".")
	fixtureGit(t, "-C", root, "config", "branch."+branch+".merge",
		"refs/heads/observed-upstream")
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("stash payload\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	executor := newAdvancedExecutor(t)
	spec := advancedSpec(gitadvanced.StashCreate)
	spec.Message = "remote drift fixture"
	preview, err := executor.ReviewAdvanced(t.Context(), root, spec)
	if err != nil || preview.Binding.UpstreamOID != head {
		t.Fatalf("upstream preview err=%v binding=%#v", err, preview.Binding)
	}
	fixtureGit(t, "-C", root, "update-ref", "refs/heads/observed-upstream", alternate, head)
	_, err = executor.ExecuteAdvanced(t.Context(), root, preview)
	var advancedErr *gitadvanced.Error
	if !errors.As(err, &advancedErr) || advancedErr.Code != gitadvanced.FailureRemoteDrift {
		t.Fatalf("upstream drift failure=%v", err)
	}
}

func TestAdvancedCancelledExecutionReturnsTypedTerminalReceiptWithoutMutation(t *testing.T) {
	root := newMutationRepo(t)
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("cancelled payload\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	executor := newAdvancedExecutor(t)
	spec := advancedSpec(gitadvanced.StashCreate)
	spec.Message = "cancelled exact stash"
	preview, err := executor.ReviewAdvanced(t.Context(), root, spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	receipt, err := executor.ExecuteAdvanced(ctx, root, preview)
	var advancedErr *gitadvanced.Error
	if !errors.As(err, &advancedErr) || advancedErr.Code != gitadvanced.FailureCancelled ||
		receipt.Status != gitadvanced.ReceiptFailed || receipt.ErrorCode != gitadvanced.FailureCancelled {
		t.Fatalf("cancelled execution err=%v receipt=%#v", err, receipt)
	}
	if stash := strings.TrimSpace(fixtureGit(t, "-C", root, "stash", "list")); stash != "" {
		t.Fatalf("cancelled operation mutated stash state: %s", stash)
	}
}

func TestAdvancedExecutionTerminalizesPreflightInspectionFailure(t *testing.T) {
	root := newMutationRepo(t)
	executor := newAdvancedExecutor(t)
	spec := advancedSpec(gitadvanced.StashCreate)
	spec.Message = "terminal preflight receipt"
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := executor.ReviewAdvanced(t.Context(), root, spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, ".git"), filepath.Join(root, ".git-hidden")); err != nil {
		t.Fatal(err)
	}
	receipt, err := executor.ExecuteAdvanced(t.Context(), root, preview)
	if err == nil || receipt.Status != gitadvanced.ReceiptFailed ||
		receipt.ErrorCode != gitadvanced.FailureGit || receipt.ErrorSummary == "" ||
		receipt.StartedAt.IsZero() || receipt.CompletedAt.IsZero() {
		t.Fatalf("preflight failure was not a terminal typed receipt: err=%v %#v", err, receipt)
	}
}

func TestAdvancedBisectResetRestoresOriginalReference(t *testing.T) {
	root := newMutationRepo(t)
	good := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("bad\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", root, "add", "base.txt")
	fixtureGit(t, "-C", root, "commit", "--quiet", "-m", "bad")
	bad := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	executor := newAdvancedExecutor(t)
	start := advancedSpec(gitadvanced.BisectStart)
	start.GoodCommit, start.BadCommit = good, bad
	preview, err := executor.ReviewAdvanced(t.Context(), root, start)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := executor.ExecuteAdvanced(t.Context(), root, preview)
	if err != nil || receipt.SequenceID == "" {
		t.Fatalf("bisect start: %v %#v", err, receipt)
	}
	reset := advancedSpec(gitadvanced.BisectReset)
	reset.SequenceID = receipt.SequenceID
	resetPreview, err := executor.ReviewAdvanced(t.Context(), root, reset)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.ExecuteAdvanced(t.Context(), root, resetPreview); err != nil {
		t.Fatal(err)
	}
	if head := fixtureGit(t, "-C", root, "rev-parse", "HEAD"); head != bad {
		t.Fatalf("bisect reset did not restore original head: %s", head)
	}
}

func TestAdvancedBisectRecipeReportsReviewedStepBudget(t *testing.T) {
	root := newMutationRepo(t)
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.invalid/bisectfixture\n\ngo 1.22\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "probe_test.go"), []byte(
		"package bisectfixture\n\nimport \"testing\"\nfunc TestProbe(t *testing.T) {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", root, "add", "go.mod", "probe_test.go")
	fixtureGit(t, "-C", root, "commit", "--quiet", "-m", "known good tests")
	good := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	for index := 0; index < 6; index++ {
		if err := os.WriteFile(filepath.Join(root, "base.txt"),
			[]byte(strings.Repeat("candidate\n", index+1)), 0o600); err != nil {
			t.Fatal(err)
		}
		fixtureGit(t, "-C", root, "add", "base.txt")
		fixtureGit(t, "-C", root, "commit", "--quiet", "-m", "candidate")
	}
	bad := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	executor := newAdvancedExecutor(t)
	start := advancedSpec(gitadvanced.BisectStart)
	start.GoodCommit, start.BadCommit = good, bad
	startPreview, err := executor.ReviewAdvanced(t.Context(), root, start)
	if err != nil {
		t.Fatal(err)
	}
	started, err := executor.ExecuteAdvanced(t.Context(), root, startPreview)
	if err != nil {
		t.Fatal(err)
	}
	current := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	run := advancedSpec(gitadvanced.BisectRun)
	run.SequenceID, run.ExpectedCurrent = started.SequenceID, current
	run.Recipe = &gitadvanced.BisectRecipe{Name: gitadvanced.RecipeGoTest,
		MaxSteps: 1, TimeoutSeconds: 30}
	runPreview, err := executor.ReviewAdvanced(t.Context(), root, run)
	if err != nil || !runPreview.Executable() {
		t.Fatalf("bisect run preview: %v %#v", err, runPreview.BlockedReasons)
	}
	unbound, unavailableErr := executor.ExecuteAdvanced(t.Context(), root, runPreview)
	if unavailableErr == nil || unbound.Status != gitadvanced.ReceiptFailed ||
		!strings.Contains(unbound.ErrorSummary, "Run-selected execution backend") ||
		fixtureGit(t, "-C", root, "rev-parse", "HEAD") != current {
		t.Fatalf("unbound bisect executed without a selected runtime: %+v %v", unbound, unavailableErr)
	}
	bound := executor.WithBisectRecipeExecutor(bisectFixtureExecutor{})
	if executor.recipeExecutor != nil {
		t.Fatal("per-operation binding changed the shared executor")
	}
	executor = bound
	receipt, err := executor.ExecuteAdvanced(t.Context(), root, runPreview)
	var advancedErr *gitadvanced.Error
	if !errors.As(err, &advancedErr) || advancedErr.Code != gitadvanced.FailureBudgetExceeded ||
		receipt.ErrorCode != gitadvanced.FailureBudgetExceeded {
		t.Fatalf("bisect budget failure err=%v receipt=%#v", err, receipt)
	}
	if len(receipt.BisectExecutions) != 1 || receipt.BisectExecutions[0].Commit != current ||
		receipt.BisectExecutions[0].Ordinal != 0 || receipt.BisectExecutions[0].Execution.JobID == "" || receipt.Validate() != nil {
		t.Fatalf("tested revision/backend receipt was lost: %+v", receipt)
	}
	fixtureGit(t, "-C", root, "bisect", "reset")
}

type bisectFixtureExecutor struct {
	starter runner.OnceStarter
}

func (s bisectFixtureExecutor) ExecuteBisectRecipe(ctx context.Context, step BisectRecipeStep) (runner.OnceStartResult, gitadvanced.BisectExecutionReceipt, error) {
	now := time.Now().UTC()
	result := runner.OnceStartResult{StartedAt: now, CompletedAt: now, StdinClosed: true, TreeReaped: true}
	var err error
	if s.starter != nil {
		result, err = s.starter.Start(ctx, runner.OnceStartSpec{WorkingDirectory: step.Root,
			RequestFingerprint: step.RequestFingerprint})
	}
	return result, gitadvanced.BisectExecutionReceipt{Commit: step.Commit, Ordinal: step.Ordinal,
		Execution: domain.BatchValidationExecutionReceipt{JobID: "bisect-fixture-job", Backend: "test-sandbox",
			BackendIdentity: "bisect-fixture.v1", BackendGeneration: "fixture-generation",
			IsolationGrade: "workspace_sandbox", NetworkPolicy: "denied", CredentialPolicy: "none",
			Readiness: "ready", ReadinessCheckedAt: now, WorkspaceSHA256: gitadvanced.Fingerprint(step.Root),
			TreeReaped: result.TreeReaped, StdinClosed: result.StdinClosed}}, err
}

type incompleteBisectProcessTreeStarter struct{ calls int }

func (s *incompleteBisectProcessTreeStarter) Name() string    { return "incomplete_bisect_tree" }
func (s *incompleteBisectProcessTreeStarter) Available() bool { return true }
func (s *incompleteBisectProcessTreeStarter) Start(_ context.Context,
	_ runner.OnceStartSpec,
) (runner.OnceStartResult, error) {
	s.calls++
	now := time.Now().UTC()
	return runner.OnceStartResult{StartedAt: now, CompletedAt: now,
		StdinClosed: true, TreeReaped: false}, nil
}

type reportedBisectTimeoutStarter struct{ calls int }

func (s *reportedBisectTimeoutStarter) Name() string    { return "reported_bisect_timeout" }
func (s *reportedBisectTimeoutStarter) Available() bool { return true }
func (s *reportedBisectTimeoutStarter) Start(_ context.Context,
	_ runner.OnceStartSpec,
) (runner.OnceStartResult, error) {
	s.calls++
	now := time.Now().UTC()
	return runner.OnceStartResult{StartedAt: now, CompletedAt: now,
		StdinClosed: true, TreeReaped: true, TimedOut: true,
		Stderr: runner.OnceOutputCapture{ObservedBytes: 19,
			CapturedPrefix: "SECRET_RECIPE_TOKEN"}}, nil
}

func TestAdvancedBisectRecipeRequiresWholeProcessTreeCompletion(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain is unavailable")
	}
	root := newMutationRepo(t)
	good := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("candidate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", root, "add", "base.txt")
	fixtureGit(t, "-C", root, "commit", "--quiet", "-m", "candidate")
	bad := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	executor := newAdvancedExecutor(t)
	start := advancedSpec(gitadvanced.BisectStart)
	start.GoodCommit, start.BadCommit = good, bad
	preview, err := executor.ReviewAdvanced(t.Context(), root, start)
	if err != nil {
		t.Fatal(err)
	}
	started, err := executor.ExecuteAdvanced(t.Context(), root, preview)
	if err != nil {
		t.Fatal(err)
	}
	current := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	recipe := advancedSpec(gitadvanced.BisectRun)
	recipe.SequenceID, recipe.ExpectedCurrent = started.SequenceID, current
	recipe.Recipe = &gitadvanced.BisectRecipe{Name: gitadvanced.RecipeGoTest,
		MaxSteps: 1, TimeoutSeconds: 30}
	preview, err = executor.ReviewAdvanced(t.Context(), root, recipe)
	if err != nil || !preview.Executable() {
		t.Fatalf("bisect recipe preview: %v %#v", err, preview.BlockedReasons)
	}
	boundary := &incompleteBisectProcessTreeStarter{}
	executor = executor.WithBisectRecipeExecutor(bisectFixtureExecutor{starter: boundary})
	receipt, err := executor.ExecuteAdvanced(t.Context(), root, preview)
	if err == nil || boundary.calls != 1 || receipt.Status != gitadvanced.ReceiptFailed ||
		!strings.Contains(receipt.ErrorSummary, "process-tree completion") {
		t.Fatalf("incomplete process tree was accepted: calls=%d err=%v receipt=%#v",
			boundary.calls, err, receipt)
	}
	fixtureGit(t, "-C", root, "bisect", "reset")
}

func TestAdvancedBisectRecipeHonorsReportedTimeoutAndDiscardsRawOutput(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain is unavailable")
	}
	root := newMutationRepo(t)
	good := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("candidate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", root, "add", "base.txt")
	fixtureGit(t, "-C", root, "commit", "--quiet", "-m", "candidate")
	bad := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	executor := newAdvancedExecutor(t)
	start := advancedSpec(gitadvanced.BisectStart)
	start.GoodCommit, start.BadCommit = good, bad
	preview, err := executor.ReviewAdvanced(t.Context(), root, start)
	if err != nil {
		t.Fatal(err)
	}
	started, err := executor.ExecuteAdvanced(t.Context(), root, preview)
	if err != nil {
		t.Fatal(err)
	}
	recipe := advancedSpec(gitadvanced.BisectRun)
	recipe.SequenceID = started.SequenceID
	recipe.ExpectedCurrent = fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	recipe.Recipe = &gitadvanced.BisectRecipe{Name: gitadvanced.RecipeGoTest,
		MaxSteps: 1, TimeoutSeconds: 30}
	preview, err = executor.ReviewAdvanced(t.Context(), root, recipe)
	if err != nil || !preview.Executable() {
		t.Fatalf("bisect recipe preview: %v %#v", err, preview.BlockedReasons)
	}
	boundary := &reportedBisectTimeoutStarter{}
	executor = executor.WithBisectRecipeExecutor(bisectFixtureExecutor{starter: boundary})
	receipt, err := executor.ExecuteAdvanced(t.Context(), root, preview)
	var advancedErr *gitadvanced.Error
	if !errors.As(err, &advancedErr) || advancedErr.Code != gitadvanced.FailureTimeout ||
		boundary.calls != 1 || receipt.ErrorCode != gitadvanced.FailureTimeout ||
		receipt.ObservedBytes != 19 || strings.Contains(receipt.ErrorSummary, "SECRET") {
		t.Fatalf("reported timeout evidence was mishandled: calls=%d err=%v receipt=%#v",
			boundary.calls, err, receipt)
	}
	fixtureGit(t, "-C", root, "bisect", "reset")
}

func TestAdvancedManagedWorktreeLifecycleAndDirtyRemovalFence(t *testing.T) {
	root := newMutationRepo(t)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", root, "add", ".gitignore")
	fixtureGit(t, "-C", root, "commit", "--quiet", "-m", "ignored removal fixture")
	head := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	executor := newAdvancedExecutor(t)
	create := advancedSpec(gitadvanced.WorktreeCreate)
	create.WorktreeName, create.Branch, create.Commit = "issue-117", "isolated-117", head
	createPreview, err := executor.ReviewAdvanced(t.Context(), root, create)
	if err != nil || !createPreview.Executable() {
		t.Fatalf("worktree create preview: %v %#v", err, createPreview.BlockedReasons)
	}
	created, err := executor.ExecuteAdvanced(t.Context(), root, createPreview)
	if err != nil || created.WorktreeID == "" {
		t.Fatalf("worktree create: %v %#v", err, created)
	}
	destination, err := executor.managedWorktreeDestination(createPreview.Binding,
		create.WorktreeName, true)
	if err != nil {
		t.Fatal(err)
	}
	lock := advancedSpec(gitadvanced.WorktreeLock)
	lock.WorktreeID, lock.WorktreeName, lock.LockReason = created.WorktreeID,
		create.WorktreeName, "verification in progress"
	lockPreview, err := executor.ReviewAdvanced(t.Context(), root, lock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.ExecuteAdvanced(t.Context(), root, lockPreview); err != nil {
		t.Fatal(err)
	}
	unlock := advancedSpec(gitadvanced.WorktreeUnlock)
	unlock.WorktreeID, unlock.WorktreeName = created.WorktreeID, create.WorktreeName
	unlockPreview, err := executor.ReviewAdvanced(t.Context(), root, unlock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.ExecuteAdvanced(t.Context(), root, unlockPreview); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "unknown.txt"), []byte("do not delete\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	remove := advancedSpec(gitadvanced.WorktreeRemove)
	remove.WorktreeID, remove.WorktreeName = created.WorktreeID, create.WorktreeName
	removePreview, err := executor.ReviewAdvanced(t.Context(), root, remove)
	if err != nil || removePreview.Executable() ||
		!strings.Contains(strings.Join(removePreview.BlockedReasons, " "), "changes") {
		t.Fatalf("dirty worktree removal was not fenced: %v %#v", err, removePreview.BlockedReasons)
	}
	if _, err := os.Stat(filepath.Join(destination, "unknown.txt")); err != nil {
		t.Fatalf("dirty worktree content disappeared: %v", err)
	}
	if err := os.Remove(filepath.Join(destination, "unknown.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "ignored.txt"),
		[]byte("Git would delete this without --force\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	removePreview, err = executor.ReviewAdvanced(t.Context(), root, remove)
	if err != nil || removePreview.Executable() ||
		!strings.Contains(strings.Join(removePreview.BlockedReasons, " "), "ignored") {
		t.Fatalf("ignored worktree content was not fenced: %v %#v",
			err, removePreview.BlockedReasons)
	}
	if _, err := os.Stat(filepath.Join(destination, "ignored.txt")); err != nil {
		t.Fatalf("ignored worktree content disappeared: %v", err)
	}
	if err := os.Remove(filepath.Join(destination, "ignored.txt")); err != nil {
		t.Fatal(err)
	}
	removePreview, err = executor.ReviewAdvanced(t.Context(), root, remove)
	if err != nil || !removePreview.Executable() {
		t.Fatalf("clean worktree removal preview: %v %#v", err, removePreview.BlockedReasons)
	}
	if _, err := executor.ExecuteAdvanced(t.Context(), root, removePreview); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clean managed worktree was not removed: %v", err)
	}
}

func TestAdvancedRepositoryBindingUsesPlatformCaseSemantics(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "CaseRepo")
	fixtureGit(t, "init", "--quiet", root)
	fixtureGit(t, "-C", root, "config", "user.email", "test@example.com")
	fixtureGit(t, "-C", root, "config", "user.name", "case-test")
	fixtureGit(t, "-C", root, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", root, "add", "base.txt")
	fixtureGit(t, "-C", root, "commit", "--quiet", "-m", "baseline")

	executor := newAdvancedExecutor(t)
	binding, err := executor.CaptureAdvancedBinding(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	caseAlias := filepath.Join(base, "caserepo")
	aliasBinding, aliasErr := executor.CaptureAdvancedBinding(t.Context(), caseAlias)
	if runtime.GOOS == "windows" {
		if aliasErr != nil || !binding.SameState(aliasBinding) {
			t.Fatalf("Windows case alias changed repository identity: err=%v primary=%#v alias=%#v",
				aliasErr, binding, aliasBinding)
		}
		if !sameFilesystemPath(root, caseAlias) || !pathInsideRoot(base, caseAlias) {
			t.Fatal("Windows path comparison did not preserve case-insensitive filesystem identity")
		}
		return
	}
	if aliasErr == nil || sameFilesystemPath(root, caseAlias) {
		t.Fatalf("case-sensitive filesystem accepted a missing case alias: err=%v", aliasErr)
	}
}

func TestAdvancedRepositoryBindingRejectsConfiguredExternalWorktree(t *testing.T) {
	root := newMutationRepo(t)
	outside := t.TempDir()
	fixtureGit(t, "-C", root, "config", "core.worktree", outside)
	_, err := newAdvancedExecutor(t).CaptureAdvancedBinding(t.Context(), root)
	var advancedErr *gitadvanced.Error
	if !errors.As(err, &advancedErr) || advancedErr.Code != gitadvanced.FailureUnsafeRepository {
		t.Fatalf("configured external worktree was not rejected: %v", err)
	}
}

func TestAdvancedCapabilityDefaultsClosedAndManagedRootRejectsLinks(t *testing.T) {
	disabled, err := NewAdvancedExecutor("", false)
	if err != nil || disabled.Available() || disabled.Capability().Enabled {
		t.Fatalf("disabled capability opened unexpectedly: %v %#v", err, disabled.Capability())
	}
	if _, err := disabled.ReviewAdvanced(t.Context(), newMutationRepo(t),
		advancedSpec(gitadvanced.StashCreate)); err == nil {
		t.Fatal("disabled capability reviewed a mutation")
	}
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if runtime.GOOS == "windows" {
		command := exec.Command("cmd", "/c", "mklink", "/J", link, target)
		if output, createErr := command.CombinedOutput(); createErr != nil {
			t.Skipf("junction unavailable: %v: %s", createErr, output)
		}
		if _, err := NewAdvancedExecutor(link, true); err == nil {
			t.Fatal("managed worktree root accepted a junction")
		}
		return
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := NewAdvancedExecutor(link, true); err == nil {
		t.Fatal("managed worktree root accepted a symlink")
	}
}

func TestAdvancedWorktreeCreateRejectsManagedRepositoryParentLinks(t *testing.T) {
	root := newMutationRepo(t)
	executor := newAdvancedExecutor(t)
	binding, err := executor.CaptureAdvancedBinding(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	parent := filepath.Join(executor.ManagedRoot(), binding.CommonDirSHA256[:16])
	if runtime.GOOS == "windows" {
		command := exec.Command("cmd", "/c", "mklink", "/J", parent, outside)
		if output, createErr := command.CombinedOutput(); createErr != nil {
			t.Skipf("junction unavailable: %v: %s", createErr, output)
		}
	} else if err := os.Symlink(outside, parent); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	spec := advancedSpec(gitadvanced.WorktreeCreate)
	spec.WorktreeName, spec.Branch, spec.Commit = "escaped", "codex/escaped", binding.Head
	if _, err := executor.ReviewAdvanced(t.Context(), root, spec); err == nil {
		t.Fatal("worktree create accepted a linked repository parent below the managed root")
	}
	if _, err := os.Stat(filepath.Join(outside, spec.WorktreeName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree create escaped before approval: %v", err)
	}
}
