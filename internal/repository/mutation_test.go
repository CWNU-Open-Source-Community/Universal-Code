package repository

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
)

func fixtureGit(t *testing.T, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output))
}

func newMutationRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}
	root := t.TempDir()
	fixtureGit(t, "init", "--quiet", root)
	fixtureGit(t, "-C", root, "config", "user.email", "test@example.com")
	fixtureGit(t, "-C", root, "config", "user.name", "mutation-test")
	fixtureGit(t, "-C", root, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", root, "add", "base.txt")
	fixtureGit(t, "-C", root, "commit", "--quiet", "-m", "baseline")
	return root
}

func newExecutor(t *testing.T) *MutationExecutor {
	t.Helper()
	executor, err := NewMutationExecutor()
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func TestRepositoryMetadataStatErrors(t *testing.T) {
	for _, cause := range []error{fs.ErrNotExist, fs.ErrPermission, syscall.EIO} {
		t.Run(cause.Error(), func(t *testing.T) {
			statErr := &fs.PathError{Op: "stat", Path: ".git", Err: cause}
			got := repositoryMetadataError(statErr)
			if cause == fs.ErrNotExist {
				if apperror.CodeOf(got) != apperror.CodeFailedPrecondition || !errors.Is(got, cause) {
					t.Fatalf("missing Git metadata: got %v", got)
				}
			} else if got != statErr {
				t.Fatalf("got %v, want original stat error %v", got, statErr)
			}
		})
	}
}

func TestRepositoryRootsPreserveUnreadableMetadata(t *testing.T) {
	root := t.TempDir()
	metadata := filepath.Join(root, ".git")
	if runtime.GOOS == "windows" {
		if output, err := exec.Command("cmd", "/c", "mklink", "/J", metadata, metadata).CombinedOutput(); err != nil {
			t.Fatalf("create metadata junction: %v: %s", err, output)
		}
	} else if err := os.Symlink(".git", metadata); err != nil {
		t.Fatal(err)
	}
	_, err := os.Stat(metadata)
	var statErr *fs.PathError
	if !errors.As(err, &statErr) || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected an unresolvable link at .git, got %v", err)
	}
	for name, normalize := range map[string]func(string) (string, error){
		"mutation": normalizeRepositoryRoot,
		"worktree": canonicalExistingRepositoryRoot,
	} {
		if _, err := normalize(root); !errors.Is(err, statErr.Err) || apperror.CodeOf(err) == apperror.CodeFailedPrecondition {
			t.Errorf("%s: got %v, want the underlying stat error %v", name, err, statErr.Err)
		}
	}
}

func TestMutationStageUnstageCommitLifecycle(t *testing.T) {
	ctx := context.Background()
	root := newMutationRepo(t)
	executor := newExecutor(t)
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stage := MutationSpec{ProtocolVersion: MutationProtocolVersion, Operation: MutationStage, Paths: []string{"new.txt"}}
	stageBinding, err := executor.CaptureBinding(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(ctx, root, stage, stageBinding); err != nil {
		t.Fatalf("stage: %v", err)
	}
	status, err := executor.gitOutput(ctx, root, "status", "--porcelain=v1")
	if err != nil || !strings.HasPrefix(status, "A  new.txt") {
		t.Fatalf("staged status wrong: %q err=%v", status, err)
	}
	unstage := MutationSpec{ProtocolVersion: MutationProtocolVersion, Operation: MutationUnstage, Paths: []string{"new.txt"}}
	unstageBinding, _ := executor.CaptureBinding(ctx, root)
	if _, err := executor.Execute(ctx, root, unstage, unstageBinding); err != nil {
		t.Fatalf("unstage: %v", err)
	}
	if status, _ := executor.gitOutput(ctx, root, "status", "--porcelain=v1"); !strings.HasPrefix(status, "?? new.txt") {
		t.Fatalf("unstage did not restore untracked: %q", status)
	}
	stageAgain, _ := executor.CaptureBinding(ctx, root)
	if _, err := executor.Execute(ctx, root, stage, stageAgain); err != nil {
		t.Fatal(err)
	}
	commitSpec := MutationSpec{ProtocolVersion: MutationProtocolVersion, Operation: MutationCommit,
		Paths: []string{"new.txt"}, Message: "add new"}
	commitBinding, _ := executor.CaptureBinding(ctx, root)
	receipt, err := executor.Execute(ctx, root, commitSpec, commitBinding)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if receipt.CommitID == "" || receipt.CommitID == receipt.PreHead || !receipt.Clean || receipt.Conflicted {
		t.Fatalf("commit receipt invalid: %#v", receipt)
	}
}

func TestMutationStaleReviewRejected(t *testing.T) {
	ctx := context.Background()
	root := newMutationRepo(t)
	executor := newExecutor(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := MutationSpec{ProtocolVersion: MutationProtocolVersion, Operation: MutationStage, Paths: []string{"a.txt"}}
	binding, err := executor.CaptureBinding(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	// Drift after review.
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("drifted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = executor.Execute(ctx, root, spec, binding)
	if err == nil || apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("stale review executed: %v", err)
	}
}

func TestMutationSwitchBranchConflictRejected(t *testing.T) {
	ctx := context.Background()
	root := newMutationRepo(t)
	executor := newExecutor(t)
	fixtureGit(t, "-C", root, "switch", "--quiet", "-c", "feature")
	if err := os.WriteFile(filepath.Join(root, "conflict.txt"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", root, "add", "conflict.txt")
	fixtureGit(t, "-C", root, "commit", "--quiet", "-m", "feature change")
	fixtureGit(t, "-C", root, "switch", "--quiet", "-")
	if err := os.WriteFile(filepath.Join(root, "conflict.txt"), []byte("master\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := MutationSpec{ProtocolVersion: MutationProtocolVersion, Operation: MutationSwitchBranch, Branch: "feature"}
	binding, _ := executor.CaptureBinding(ctx, root)
	_, err := executor.Execute(ctx, root, spec, binding)
	if err == nil {
		t.Fatal("conflicting switch succeeded")
	}
}

func TestMutationDisablesHooksAndConfig(t *testing.T) {
	ctx := context.Background()
	root := newMutationRepo(t)
	executor := newExecutor(t)
	hook := filepath.Join(root, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho HOOK-RAN > "+filepath.Join(root, "hook-marker.txt")+"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stageSpec := MutationSpec{ProtocolVersion: MutationProtocolVersion, Operation: MutationStage, Paths: []string{"b.txt"}}
	stageBinding, _ := executor.CaptureBinding(ctx, root)
	if _, err := executor.Execute(ctx, root, stageSpec, stageBinding); err != nil {
		t.Fatal(err)
	}
	commitSpec := MutationSpec{ProtocolVersion: MutationProtocolVersion, Operation: MutationCommit,
		Paths: []string{"b.txt"}, Message: "no hooks"}
	commitBinding, _ := executor.CaptureBinding(ctx, root)
	if _, err := executor.Execute(ctx, root, commitSpec, commitBinding); err != nil {
		t.Fatalf("commit with hostile hook: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "hook-marker.txt")); err == nil {
		t.Fatal("repository hook executed during mutation")
	}
}

func TestMutationRejectsEscapesAndUnknownOperations(t *testing.T) {
	newMutationRepo(t)
	executor := newExecutor(t)
	badPaths := [][]string{{"../escape"}, {"-weird"}, {"a\\b"}}
	for _, paths := range badPaths {
		spec := MutationSpec{ProtocolVersion: MutationProtocolVersion, Operation: MutationStage, Paths: paths}
		if err := executor.validateSpec(spec); err == nil {
			t.Fatalf("escape paths accepted: %v", paths)
		}
	}
	badBranches := []string{"", "-x", "bad name", "a..b", "head@{1}"}
	for _, branch := range badBranches {
		spec := MutationSpec{ProtocolVersion: MutationProtocolVersion, Operation: MutationCreateBranch, Branch: branch}
		if err := executor.validateSpec(spec); err == nil {
			t.Fatalf("bad branch accepted: %q", branch)
		}
	}
	spec := MutationSpec{ProtocolVersion: MutationProtocolVersion, Operation: "reset"}
	if err := executor.validateSpec(spec); err == nil {
		t.Fatal("unknown operation accepted")
	}
}

func TestMutationUnicodeAndSpacePaths(t *testing.T) {
	ctx := context.Background()
	root := newMutationRepo(t)
	executor := newExecutor(t)
	name := filepath.Join("dir with space", "üñí.txt")
	if err := os.MkdirAll(filepath.Join(root, "dir with space"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte("unicode\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	slash := strings.ReplaceAll(name, "\\", "/")
	spec := MutationSpec{ProtocolVersion: MutationProtocolVersion, Operation: MutationStage, Paths: []string{slash}}
	binding, _ := executor.CaptureBinding(ctx, root)
	if _, err := executor.Execute(ctx, root, spec, binding); err != nil {
		t.Fatalf("unicode stage: %v", err)
	}
}

func TestMutationCreateAndSwitchBranch(t *testing.T) {
	ctx := context.Background()
	root := newMutationRepo(t)
	executor := newExecutor(t)
	create := MutationSpec{ProtocolVersion: MutationProtocolVersion, Operation: MutationCreateBranch, Branch: "work"}
	createBinding, _ := executor.CaptureBinding(ctx, root)
	if _, err := executor.Execute(ctx, root, create, createBinding); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	if _, err := executor.gitOutput(ctx, root, "rev-parse", "--verify", "refs/heads/work"); err != nil {
		t.Fatalf("branch not created: %v", err)
	}
	switchSpec := MutationSpec{ProtocolVersion: MutationProtocolVersion, Operation: MutationSwitchBranch, Branch: "work"}
	switchBinding, _ := executor.CaptureBinding(ctx, root)
	receipt, err := executor.Execute(ctx, root, switchSpec, switchBinding)
	if err != nil || receipt.Branch == "" {
		t.Fatalf("switch branch: %v", err)
	}
	if current, _ := executor.gitOutput(ctx, root, "branch", "--show-current"); strings.TrimSpace(current) != "work" {
		t.Fatalf("branch did not switch: %q", current)
	}
}

func TestMutationRunGitEnforcesDurationBound(t *testing.T) {
	executor := blockingMutationExecutor(50 * time.Millisecond)
	started := time.Now()
	_, _, _, err := executor.runGit(t.Context(), t.TempDir(), MutationSpec{
		ProtocolVersion: MutationProtocolVersion, Operation: MutationStage,
		Paths: []string{"file.txt"},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("git mutation timeout was not enforced: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("git mutation exceeded the injected duration bound: %s", elapsed)
	}
}

func TestMutationRunGitPreservesCallerCancellation(t *testing.T) {
	executor := blockingMutationExecutor(time.Minute)
	ctx, cancel := context.WithCancel(t.Context())
	timer := time.AfterFunc(50*time.Millisecond, cancel)
	defer timer.Stop()
	_, _, _, err := executor.runGit(ctx, t.TempDir(), MutationSpec{
		ProtocolVersion: MutationProtocolVersion, Operation: MutationStage,
		Paths: []string{"file.txt"},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation was not preserved: %v", err)
	}
}

func TestMutationExecuteReportsDurationDeadline(t *testing.T) {
	root := newMutationRepo(t)
	executor := newExecutor(t)
	executor.maxDuration = 2 * time.Second
	// Repository binding now also uses the real Git executable. Only the
	// mutation is stalled; its exact preflight reads must remain real.
	executor.commandContext = func(ctx context.Context, path string, args ...string) *exec.Cmd {
		for _, arg := range args {
			if arg == "add" {
				return blockingMutationCommand(ctx, path, args...)
			}
		}
		return repositoryCommandContext(ctx, path, args...)
	}
	if err := os.WriteFile(filepath.Join(root, "slow.txt"), []byte("slow\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := MutationSpec{ProtocolVersion: MutationProtocolVersion,
		Operation: MutationStage, Paths: []string{"slow.txt"}}
	binding, err := executor.CaptureBinding(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.Execute(t.Context(), root, spec, binding)
	if apperror.CodeOf(err) != apperror.CodeDeadlineExceeded {
		t.Fatalf("duration bound did not surface as deadline exceeded: %v", err)
	}
}

func blockingMutationExecutor(duration time.Duration) *MutationExecutor {
	return &MutationExecutor{
		gitPath: os.Args[0], maxDuration: duration,
		commandContext: blockingMutationCommand,
	}
}

func blockingMutationCommand(ctx context.Context, _ string, _ ...string) *exec.Cmd {
	return exec.CommandContext(ctx, os.Args[0],
		"-test.run=^TestMutationGitHelperProcess$", "--", "mutation-git-helper")
}

func TestMutationGitHelperProcess(t *testing.T) {
	helper := false
	for _, argument := range os.Args {
		if argument == "mutation-git-helper" {
			helper = true
			break
		}
	}
	if !helper {
		return
	}
	time.Sleep(10 * time.Second)
}
