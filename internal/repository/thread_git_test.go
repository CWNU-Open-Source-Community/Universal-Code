package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func threadGitFixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git unavailable")
	}
	root := t.TempDir()
	threadGitTestCommand(t, root, "init", "-q")
	threadGitTestCommand(t, root, "config", "user.name", "Thread Git test")
	threadGitTestCommand(t, root, "config", "user.email", "thread@example.test")
	threadGitTestCommand(t, root, "config", "core.autocrlf", "false")
	for _, path := range []string{"chosen.txt", "other.txt"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte("original\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	threadGitTestCommand(t, root, "add", ".")
	threadGitTestCommand(t, root, "commit", "-qm", "initial")
	return root
}

func threadGitTestCommand(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, data)
	}
	return strings.TrimSpace(string(data))
}

func TestThreadGitSelectedCommitPreservesUnrelatedStagingAndExactBytes(t *testing.T) {
	root := threadGitFixture(t)
	ctx := context.Background()
	const selected = " [chosen].txt"
	executor, _ := NewMutationExecutor()
	if err := os.WriteFile(filepath.Join(root, "other.txt"), []byte("unrelated user staging\n"), 0600); err != nil {
		t.Fatal(err)
	}
	threadGitTestCommand(t, root, "add", "other.txt")
	if err := os.WriteFile(filepath.Join(root, selected), []byte("approved content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, " c.txt"), []byte("literal sibling\n"), 0600); err != nil {
		t.Fatal(err)
	}
	threadGitTestCommand(t, root, "--literal-pathspecs", "add", "--", " c.txt")
	threadGitTestCommand(t, root, "commit", "--quiet", "--only", "-m", "literal sibling", "--", " c.txt")
	review, err := executor.ReviewSelected(ctx, root, []string{selected})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(review.Diff, "+approved content") {
		t.Fatalf("wrong review: %s", review.Diff)
	}
	prepared, err := executor.PrepareSelectedCommit(ctx, root, review, "selected change", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	if err = prepared.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	if got := threadGitTestCommand(t, root, "show", "HEAD:other.txt"); got != "original" {
		t.Fatalf("unrelated staging committed: %q", got)
	}
	if got := threadGitTestCommand(t, root, "show", ":other.txt"); got != "unrelated user staging" {
		t.Fatalf("unrelated staging lost: %q", got)
	}
	if got := threadGitTestCommand(t, root, "show", "HEAD:"+selected); got != "approved content" {
		t.Fatalf("selected content wrong: %q", got)
	}
	if got := threadGitTestCommand(t, root, "show", "HEAD:chosen.txt"); got != "original" {
		t.Fatalf("neighboring content changed: %q", got)
	}
	if got := threadGitTestCommand(t, root, "show", "HEAD: c.txt"); got != "literal sibling" {
		t.Fatalf("a path matching the glob changed: %q", got)
	}
	if got := threadGitTestCommand(t, root, "diff", "--name-only"); got != "" {
		t.Fatalf("selected index is inconsistent: %s", got)
	}
	if ok, err := executor.ObserveSelectedCommit(ctx, root, *prepared); err != nil || !ok {
		t.Fatalf("exact observation=%v %v", ok, err)
	}
	wrong := *prepared
	wrong.Marker = strings.Repeat("b", 64)
	if ok, _ := executor.ObserveSelectedCommit(ctx, root, wrong); ok {
		t.Fatal("wrong operation marker accepted")
	}
}

func TestThreadGitTrackedContentDriftAndLinkedWorktree(t *testing.T) {
	root := threadGitFixture(t)
	linked := filepath.Join(t.TempDir(), "linked")
	threadGitTestCommand(t, root, "worktree", "add", "-b", "linked", linked)
	executor, _ := NewMutationExecutor()
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(linked, "chosen.txt"), []byte("first\n"), 0600); err != nil {
		t.Fatal(err)
	}
	review, err := executor.ReviewSelected(ctx, linked, []string{"chosen.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linked, "chosen.txt"), []byte("changed again\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = executor.PrepareSelectedCommit(ctx, linked, review, "must reject", strings.Repeat("c", 64)); err == nil {
		t.Fatal("same porcelain tracked-content drift accepted")
	}
	if _, err = os.Stat(filepath.Join(root, ".git", "worktrees", "linked", "index.lock")); !os.IsNotExist(err) {
		t.Fatalf("failed prepare left index lock: %v", err)
	}
	fresh, err := executor.ReviewSelected(ctx, linked, []string{"chosen.txt"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := executor.PrepareSelectedCommit(ctx, linked, fresh, "linked commit", strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err = p.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	if threadGitTestCommand(t, root, "show", "HEAD:chosen.txt") != "original" {
		t.Fatal("linked worktree wrote source branch")
	}
}

func TestThreadGitExactPushUsesReviewedOIDAndRemoteCAS(t *testing.T) {
	root := threadGitFixture(t)
	bare := filepath.Join(t.TempDir(), "remote.git")
	threadGitTestCommand(t, root, "init", "--bare", "-q", bare)
	remote, _ := NewRemoteExecutor(nil)
	remote.AllowLocalRemotesForTest()
	spec := RemoteSpec{ProtocolVersion: RemoteProtocolVersion, Operation: RemotePushBranch, RemoteURL: "file:///" + filepath.ToSlash(bare), Branch: "reviewed", NetworkTTLMillis: 30000, CommitOID: threadGitTestCommand(t, root, "rev-parse", "HEAD"), ExpectedRemoteOID: "missing"}
	ctx := context.Background()
	receipt, err := remote.ExecuteGit(ctx, root, spec, RemoteBinding{LocalHead: spec.CommitOID}, "key")
	if err != nil {
		t.Fatal(err)
	}
	if receipt.CommitID != spec.CommitOID {
		t.Fatalf("push receipt=%#v", receipt)
	}
	if _, err = remote.ExecuteGit(ctx, root, spec, RemoteBinding{}, "key2"); err == nil {
		t.Fatal("remote drift accepted")
	}
	if err = os.WriteFile(filepath.Join(root, "chosen.txt"), []byte("next\n"), 0600); err != nil {
		t.Fatal(err)
	}
	threadGitTestCommand(t, root, "add", "chosen.txt")
	threadGitTestCommand(t, root, "commit", "-qm", "next")
	spec.ExpectedRemoteOID = spec.CommitOID
	spec.CommitOID = threadGitTestCommand(t, root, "rev-parse", "HEAD")
	if _, err = remote.ExecuteGit(ctx, root, spec, RemoteBinding{}, "key3"); err != nil {
		t.Fatal(err)
	}
	observed, err := remote.ReadRemoteOID(ctx, root, spec)
	if err != nil || observed != spec.CommitOID {
		t.Fatalf("readback=%s %v", observed, err)
	}
	spec.ExpectedRemoteOID = spec.CommitOID
	spec.CommitOID = threadGitTestCommand(t, root, "rev-parse", "HEAD~1")
	if _, err = remote.ExecuteGit(ctx, root, spec, RemoteBinding{}, "key4"); err == nil {
		t.Fatal("non-fast-forward accepted")
	}
}

func TestThreadGitCommitIndexPublicationFailureStaysUnknown(t *testing.T) {
	root := threadGitFixture(t)
	executor, _ := NewMutationExecutor()
	ctx := t.Context()
	if err := os.WriteFile(filepath.Join(root, "chosen.txt"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	review, err := executor.ReviewSelected(ctx, root, []string{"chosen.txt"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := executor.PrepareSelectedCommit(ctx, root, review, "index failure", strings.Repeat("e", 64))
	if err != nil {
		t.Fatal(err)
	}
	p.renameIndex = func(string, string) error { return errors.New("fixture publication failed") }
	if err = p.Publish(ctx); err == nil {
		t.Fatal("expected publication failure")
	}
	p.Close()
	if head := threadGitTestCommand(t, root, "rev-parse", "HEAD"); head != p.CommitOID {
		t.Fatal("fixture must cross the commit-ref boundary")
	}
	if ok, err := executor.ObserveSelectedCommit(ctx, root, *p); err != nil || ok {
		t.Fatalf("partial commit/index publication reported complete: %v %v", ok, err)
	}
	if _, err = os.Stat(p.lockPath); err != nil {
		t.Fatalf("uncertain closed lock evidence was discarded: %v", err)
	}
}

func TestThreadGitIndexPreviewsMatchTheirActualSourceAndDestination(t *testing.T) {
	root := threadGitFixture(t)
	executor, _ := NewMutationExecutor()
	ctx := t.Context()
	if err := os.WriteFile(filepath.Join(root, "chosen.txt"), []byte("staged A\n"), 0600); err != nil {
		t.Fatal(err)
	}
	threadGitTestCommand(t, root, "add", "chosen.txt")
	if err := os.WriteFile(filepath.Join(root, "chosen.txt"), []byte("worktree B\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stage, err := executor.ReviewIndexChange(ctx, root, []string{"chosen.txt"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stage.Diff, "-staged A") || !strings.Contains(stage.Diff, "+worktree B") {
		t.Fatalf("stage wrong diff: %s", stage.Diff)
	}
	unstage, err := executor.ReviewIndexChange(ctx, root, []string{"chosen.txt"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(unstage.Diff, "-staged A") || !strings.Contains(unstage.Diff, "+original") || strings.Contains(unstage.Diff, "worktree B") {
		t.Fatalf("unstage wrong diff: %s", unstage.Diff)
	}
	p, err := executor.PrepareSelectedIndex(ctx, root, unstage, true)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err = p.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	if got := threadGitTestCommand(t, root, "show", ":chosen.txt"); got != "original" {
		t.Fatalf("wrong index outcome: %s", got)
	}
	data, _ := os.ReadFile(filepath.Join(root, "chosen.txt"))
	if string(data) != "worktree B\n" {
		t.Fatal("unstage changed working file")
	}
}

func TestThreadGitBranchSwitchUsesReviewedTargetInLinkedWorktree(t *testing.T) {
	root := threadGitFixture(t)
	executor, _ := NewMutationExecutor()
	ctx := t.Context()
	threadGitTestCommand(t, root, "branch", "target")
	linked := filepath.Join(t.TempDir(), "linked")
	threadGitTestCommand(t, root, "worktree", "add", "-b", "linked", linked)
	bound, err := executor.AdvancedBinding(ctx, linked)
	if err != nil {
		t.Fatal(err)
	}
	target, err := executor.ReadBranchTarget(ctx, linked, "target")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := executor.ExecuteThreadBranch(ctx, linked, MutationSpec{ProtocolVersion: MutationProtocolVersion, Operation: MutationSwitchBranch, Branch: "target"}, bound, target)
	if err != nil || receipt.Branch != "target" {
		t.Fatalf("switch=%#v err=%v", receipt, err)
	}
}

func TestThreadGitUnstageAddedFileAfterWorkingFileRemoved(t *testing.T) {
	for _, unborn := range []bool{false, true} {
		t.Run(fmt.Sprint("unborn=", unborn), func(t *testing.T) {
			root := threadGitFixture(t)
			if unborn {
				threadGitTestCommand(t, root, "checkout", "--orphan", "unborn")
				threadGitTestCommand(t, root, "read-tree", "--empty")
			}
			path := filepath.Join(root, "added.txt")
			if err := os.WriteFile(path, []byte("staged then removed\n"), 0600); err != nil {
				t.Fatal(err)
			}
			threadGitTestCommand(t, root, "add", "added.txt")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			executor, _ := NewMutationExecutor()
			review, err := executor.ReviewIndexChange(t.Context(), root, []string{"added.txt"}, true)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(review.Diff, "-staged then removed") {
				t.Fatalf("wrong removal preview: %s", review.Diff)
			}
			p, err := executor.PrepareSelectedIndex(t.Context(), root, review, true)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			if err = p.Publish(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := threadGitTestCommand(t, root, "ls-files", "--stage", "--", "added.txt"); got != "" {
				t.Fatalf("index entry remains: %s", got)
			}
			if _, err = os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("unstage recreated working file: %v", err)
			}
		})
	}
}
