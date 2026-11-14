package repository

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"cyberagent-workbench/internal/rootfs"
)

// ValidateBranchName exposes the same closed branch-name contract used by
// typed Git mutations so Workspace forks cannot smuggle arbitrary argv.
func ValidateBranchName(branch string) error {
	return validateBranchName(strings.TrimSpace(branch))
}

// NormalizeWorktreeDestination returns one absolute, symlink-resolved
// destination identity whether the final directory exists yet or not. Callers
// can persist it before materialization and later use the same identity for
// crash recovery.
func NormalizeWorktreeDestination(sourceRoot, destinationRoot string) (string, error) {
	source, err := canonicalExistingRepositoryRoot(sourceRoot)
	if err != nil {
		return "", err
	}
	destination, err := canonicalWorktreeRoot(destinationRoot)
	if err != nil {
		return "", err
	}
	if pathsOverlap(source, destination) {
		return "", errors.New("git worktree destination overlaps the source repository")
	}
	return destination, nil
}

// CreateWorktree creates one new branch-backed Git worktree at an exact full
// commit. The destination must not exist and must be outside the source root.
func CreateWorktree(ctx context.Context, sourceRoot, destinationRoot, branch,
	commit string,
) (string, error) {
	if ctx == nil || ctx.Err() != nil {
		return "", errors.New("git worktree context is unavailable")
	}
	branch, commit = strings.TrimSpace(branch), strings.TrimSpace(commit)
	if err := ValidateBranchName(branch); err != nil || !validWorktreeCommit(commit) {
		return "", errors.New("git worktree branch or commit is invalid")
	}
	source, err := canonicalExistingRepositoryRoot(sourceRoot)
	if err != nil {
		return "", err
	}
	destination, err := canonicalAbsentWorktreeRoot(destinationRoot)
	if err != nil {
		return "", err
	}
	if pathsOverlap(source, destination) {
		return "", errors.New("git worktree destination overlaps the source repository")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("git binary is unavailable: %w", err)
	}
	if err := validateHardenedGitRepository(ctx, gitPath, source); err != nil {
		return "", err
	}
	commandCtx, cancel := context.WithTimeout(ctx, MaxGitDuration)
	defer cancel()
	command := exec.CommandContext(commandCtx, gitPath, "-C", source,
		"--no-optional-locks", "worktree", "add", "--no-track", "-b", branch,
		destination, commit)
	command.Env = hardenedGitEnvironment()
	command.Dir = source
	var stdout, stderr boundedBuffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("create Git worktree: %w: %s", err,
			strings.TrimSpace(stderr.String()))
	}
	if err := verifyCreatedWorktree(commandCtx, gitPath, destination, branch, commit); err != nil {
		cleanupErr := RemoveCreatedWorktree(context.WithoutCancel(ctx), source,
			destination, branch)
		return "", errors.Join(err, cleanupErr)
	}
	if err := verifyWorktreeCommonDirectory(commandCtx, gitPath, source, destination); err != nil {
		return "", err
	}
	return destination, nil
}

// RestoreExistingWorktree rematerializes a missing worktree from an exact
// durable branch/head binding. Unlike CreateWorktree it never creates or
// rewrites the branch, so restart recovery cannot discard reviewed commits.
func RestoreExistingWorktree(ctx context.Context, sourceRoot, destinationRoot,
	branch, expectedHead string,
) (string, error) {
	if ctx == nil || ctx.Err() != nil {
		return "", errors.New("git worktree restore context is unavailable")
	}
	branch, expectedHead = strings.TrimSpace(branch), strings.TrimSpace(expectedHead)
	if err := ValidateBranchName(branch); err != nil || !validWorktreeCommit(expectedHead) {
		return "", errors.New("git worktree restore identity is invalid")
	}
	source, err := canonicalExistingRepositoryRoot(sourceRoot)
	if err != nil {
		return "", err
	}
	destination, err := canonicalAbsentWorktreeRoot(destinationRoot)
	if err != nil {
		return "", err
	}
	if pathsOverlap(source, destination) {
		return "", errors.New("git worktree restore destination overlaps the source repository")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("git binary is unavailable: %w", err)
	}
	if err := validateHardenedGitRepository(ctx, gitPath, source); err != nil {
		return "", err
	}
	commandCtx, cancel := context.WithTimeout(ctx, MaxGitDuration)
	defer cancel()
	ref := "refs/heads/" + branch
	show := exec.CommandContext(commandCtx, gitPath, "-C", source,
		"--no-optional-locks", "show-ref", "--verify", "--hash", ref)
	show.Env, show.Dir = hardenedGitEnvironment(), source
	var stdout, stderr boundedBuffer
	show.Stdout, show.Stderr = &stdout, &stderr
	if err := show.Run(); err != nil {
		return "", fmt.Errorf("restore Git worktree branch binding changed: %w: %s",
			err, strings.TrimSpace(stderr.String()))
	}
	if observed := strings.TrimSpace(stdout.String()); observed != expectedHead {
		return "", fmt.Errorf("restore Git worktree branch binding changed: got %q, want %q",
			observed, expectedHead)
	}
	add := exec.CommandContext(commandCtx, gitPath, "-C", source,
		"--no-optional-locks", "worktree", "add", destination, branch)
	add.Env, add.Dir = hardenedGitEnvironment(), source
	var addStdout, addStderr boundedBuffer
	add.Stdout, add.Stderr = &addStdout, &addStderr
	if err := add.Run(); err != nil {
		return "", fmt.Errorf("restore Git worktree: %w: %s", err,
			strings.TrimSpace(addStderr.String()))
	}
	if err := verifyCreatedWorktree(commandCtx, gitPath, destination, branch,
		expectedHead); err != nil {
		remove := exec.CommandContext(context.WithoutCancel(ctx), gitPath, "-C", source,
			"--no-optional-locks", "worktree", "remove", "--force", destination)
		remove.Env, remove.Dir = hardenedGitEnvironment(), source
		_ = remove.Run()
		return "", err
	}
	if err := verifyWorktreeCommonDirectory(commandCtx, gitPath, source, destination); err != nil {
		return "", err
	}
	return destination, nil
}

// BranchFullHead reads an exact local branch ref without resolving arbitrary
// revision syntax supplied by a caller.
func BranchFullHead(ctx context.Context, sourceRoot, branch string) (string, error) {
	if ctx == nil || ctx.Err() != nil || ValidateBranchName(branch) != nil {
		return "", errors.New("git branch inspection identity is invalid")
	}
	source, err := canonicalExistingRepositoryRoot(sourceRoot)
	if err != nil {
		return "", err
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", err
	}
	commandCtx, cancel := context.WithTimeout(ctx, MaxGitDuration)
	defer cancel()
	command := exec.CommandContext(commandCtx, gitPath, "-C", source,
		"--no-optional-locks", "show-ref", "--verify", "--hash",
		"refs/heads/"+strings.TrimSpace(branch))
	command.Env, command.Dir = hardenedGitEnvironment(), source
	var stdout, stderr boundedBuffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("inspect Git branch: %w: %s", err,
			strings.TrimSpace(stderr.String()))
	}
	head := strings.TrimSpace(stdout.String())
	if !validWorktreeCommit(head) {
		return "", errors.New("Git branch did not resolve to a full commit")
	}
	return head, nil
}

// RemoveCreatedWorktree removes only an exact registered worktree and its
// newly-created branch. It never falls back to recursive filesystem deletion.
func RemoveCreatedWorktree(ctx context.Context, sourceRoot, destinationRoot,
	branch string,
) error {
	if ctx == nil {
		return errors.New("git worktree cleanup context is unavailable")
	}
	branch = strings.TrimSpace(branch)
	if err := ValidateBranchName(branch); err != nil {
		return err
	}
	source, err := canonicalExistingRepositoryRoot(sourceRoot)
	if err != nil {
		return err
	}
	destination, err := canonicalExistingRepositoryRoot(destinationRoot)
	if err != nil || pathsOverlap(source, destination) {
		return errors.New("git worktree cleanup destination is invalid")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return err
	}
	if err := verifyWorktreeCommonDirectory(ctx, gitPath, source, destination); err != nil {
		return err
	}
	if current, branchErr := CurrentBranch(ctx, destination); branchErr != nil || current != branch {
		return errors.New("git worktree cleanup branch identity changed")
	}
	commandCtx, cancel := context.WithTimeout(ctx, MaxGitDuration)
	defer cancel()
	remove := exec.CommandContext(commandCtx, gitPath, "-C", source,
		"--no-optional-locks", "worktree", "remove", "--force", destination)
	remove.Env, remove.Dir = hardenedGitEnvironment(), source
	var removeOutput boundedBuffer
	remove.Stderr = &removeOutput
	if err := remove.Run(); err != nil {
		return fmt.Errorf("remove Git worktree: %w: %s", err,
			strings.TrimSpace(removeOutput.String()))
	}
	deleteBranch := exec.CommandContext(commandCtx, gitPath, "-C", source,
		"--no-optional-locks", "branch", "-D", "--", branch)
	deleteBranch.Env, deleteBranch.Dir = hardenedGitEnvironment(), source
	var branchOutput boundedBuffer
	deleteBranch.Stderr = &branchOutput
	if err := deleteBranch.Run(); err != nil {
		return fmt.Errorf("remove Git worktree branch: %w: %s", err,
			strings.TrimSpace(branchOutput.String()))
	}
	return nil
}

// CleanupInterruptedWorktree removes only the exact branch-backed worktree
// recorded before a fork was materialized. Every observable identity is
// checked first; unexpected filesystem, branch, or commit drift fails closed.
func CleanupInterruptedWorktree(ctx context.Context, sourceRoot, destinationRoot,
	branch, expectedCommit string,
) error {
	if ctx == nil || ctx.Err() != nil {
		return errors.New("git worktree recovery context is unavailable")
	}
	branch, expectedCommit = strings.TrimSpace(branch), strings.TrimSpace(expectedCommit)
	if err := ValidateBranchName(branch); err != nil || !validWorktreeCommit(expectedCommit) {
		return errors.New("git worktree recovery identity is invalid")
	}
	source, err := canonicalExistingRepositoryRoot(sourceRoot)
	if err != nil {
		return err
	}
	requested, err := rootfs.ExactAbsolutePath(destinationRoot)
	if err != nil || strings.ContainsRune(destinationRoot, 0) {
		return errors.New("git worktree recovery destination is invalid")
	}
	destination, err := canonicalWorktreeRoot(destinationRoot)
	if err != nil {
		return err
	}
	if !sameFilesystemPath(requested, destination) || pathsOverlap(source, destination) {
		return errors.New("git worktree recovery destination identity changed")
	}
	info, statErr := os.Lstat(destination)
	if statErr == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("git worktree recovery destination is not a real directory")
		}
		gitPath, lookupErr := exec.LookPath("git")
		if lookupErr != nil {
			return fmt.Errorf("git binary is unavailable: %w", lookupErr)
		}
		verifyCtx, cancel := context.WithTimeout(ctx, MaxGitDuration)
		verifyErr := verifyCreatedWorktree(verifyCtx, gitPath, destination, branch,
			expectedCommit)
		if verifyErr == nil {
			verifyErr = verifyWorktreeCommonDirectory(verifyCtx, gitPath, source, destination)
		}
		cancel()
		if verifyErr != nil {
			return verifyErr
		}
		return RemoveCreatedWorktree(ctx, source, destination, branch)
	}
	if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect Git worktree recovery destination: %w", statErr)
	}
	return removeInterruptedWorktreeBranch(ctx, source, branch, expectedCommit)
}

func canonicalExistingRepositoryRoot(root string) (string, error) {
	abs, err := rootfs.ExactAbsolutePath(root)
	if err != nil || root == "" || strings.ContainsRune(root, 0) {
		return "", errors.New("repository root is invalid")
	}
	info, err := os.Lstat(abs)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("repository root is not a real directory")
	}
	resolved, err := rootfs.EvalExactSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	if !sameFilesystemPath(abs, resolved) {
		return "", errors.New("repository root identity changed through a link or reparse point")
	}
	if err := rootfs.CheckExternalDirectory(resolved); err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(resolved, ".git")); err != nil {
		return "", errors.New("repository root does not contain Git metadata")
	}
	return filepath.Clean(resolved), nil
}

func canonicalAbsentWorktreeRoot(root string) (string, error) {
	if root == "" || strings.ContainsRune(root, 0) {
		return "", errors.New("git worktree destination is invalid")
	}
	abs, err := rootfs.ExactAbsolutePath(root)
	if err != nil {
		return "", err
	}
	if err := rootfs.CheckExternalDirectory(abs); err != nil {
		return "", err
	}
	if _, err := os.Lstat(abs); err == nil || !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("git worktree destination already exists or is inaccessible")
	}
	parent, err := rootfs.EvalExactSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", fmt.Errorf("resolve Git worktree parent: %w", err)
	}
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return "", errors.New("git worktree parent is not a directory")
	}
	destination := filepath.Join(parent, filepath.Base(abs))
	return destination, rootfs.CheckExternalDirectory(destination)
}

func canonicalWorktreeRoot(root string) (string, error) {
	if root == "" || strings.ContainsRune(root, 0) {
		return "", errors.New("git worktree destination is invalid")
	}
	abs, err := rootfs.ExactAbsolutePath(root)
	if err != nil {
		return "", err
	}
	if err := rootfs.CheckExternalDirectory(abs); err != nil {
		return "", err
	}
	info, err := os.Lstat(abs)
	if err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("git worktree destination is not a real directory")
		}
		resolved, resolveErr := rootfs.EvalExactSymlinks(abs)
		if resolveErr != nil {
			return "", fmt.Errorf("resolve Git worktree destination: %w", resolveErr)
		}
		if !sameFilesystemPath(abs, resolved) {
			return "", errors.New("git worktree destination identity changed")
		}
		return filepath.Clean(resolved), rootfs.CheckExternalDirectory(resolved)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect Git worktree destination: %w", err)
	}
	parent, err := rootfs.EvalExactSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", fmt.Errorf("resolve Git worktree parent: %w", err)
	}
	info, err = os.Stat(parent)
	if err != nil || !info.IsDir() {
		return "", errors.New("git worktree parent is not a directory")
	}
	destination := filepath.Join(filepath.Clean(parent), filepath.Base(abs))
	return destination, rootfs.CheckExternalDirectory(destination)
}

func sameFilesystemPath(left, right string) bool {
	relative, err := filepath.Rel(rootfs.OrdinaryPath(left), rootfs.OrdinaryPath(right))
	return err == nil && relative == "."
}

func removeInterruptedWorktreeBranch(ctx context.Context, source, branch,
	expectedCommit string,
) error {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return fmt.Errorf("git binary is unavailable: %w", err)
	}
	commandCtx, cancel := context.WithTimeout(ctx, MaxGitDuration)
	defer cancel()
	ref := "refs/heads/" + branch
	show := exec.CommandContext(commandCtx, gitPath, "-C", source,
		"--no-optional-locks", "show-ref", "--verify", "--hash", ref)
	show.Env, show.Dir = hardenedGitEnvironment(), source
	var stdout, stderr boundedBuffer
	show.Stdout, show.Stderr = &stdout, &stderr
	if err := show.Run(); err != nil {
		var exitErr *exec.ExitError
		missingRef := strings.Contains(stderr.String(), "not a valid ref") ||
			strings.Contains(stderr.String(), "not a valid reference")
		if errors.As(err, &exitErr) && (exitErr.ExitCode() == 1 ||
			(exitErr.ExitCode() == 128 && missingRef)) && strings.TrimSpace(stdout.String()) == "" {
			return nil
		}
		return fmt.Errorf("inspect interrupted Git worktree branch: %w: %s", err,
			strings.TrimSpace(stderr.String()))
	}
	if observed := strings.TrimSpace(stdout.String()); observed != expectedCommit {
		return fmt.Errorf("interrupted Git worktree branch drifted: got %q, want %q",
			observed, expectedCommit)
	}
	remove := exec.CommandContext(commandCtx, gitPath, "-C", source,
		"--no-optional-locks", "branch", "-D", "--", branch)
	remove.Env, remove.Dir = hardenedGitEnvironment(), source
	var removeOutput boundedBuffer
	remove.Stderr = &removeOutput
	if err := remove.Run(); err != nil {
		return fmt.Errorf("remove interrupted Git worktree branch: %w: %s", err,
			strings.TrimSpace(removeOutput.String()))
	}
	return nil
}

func pathsOverlap(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	for _, pair := range [][2]string{{left, right}, {right, left}} {
		relative, err := filepath.Rel(pair[0], pair[1])
		if err == nil && relative != ".." && !strings.HasPrefix(relative,
			".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func validWorktreeCommit(value string) bool {
	if (len(value) != 40 && len(value) != 64) || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded)*2 == len(value)
}

func verifyCreatedWorktree(ctx context.Context, gitPath, root, branch, commit string) error {
	canonical, err := canonicalExistingRepositoryRoot(root)
	if err != nil || !sameFilesystemPath(canonical, root) {
		return errors.New("Git worktree root identity changed")
	}
	root = canonical
	for _, check := range []struct {
		args []string
		want string
	}{
		{args: []string{"rev-parse", "--verify", "HEAD"}, want: commit},
		{args: []string{"branch", "--show-current"}, want: branch},
	} {
		command := exec.CommandContext(ctx, gitPath, append([]string{"-C", root,
			"--no-optional-locks"}, check.args...)...)
		command.Env, command.Dir = hardenedGitEnvironment(), root
		var stdout, stderr boundedBuffer
		command.Stdout, command.Stderr = &stdout, &stderr
		if err := command.Run(); err != nil {
			return fmt.Errorf("verify Git worktree %s: %w: %s", check.args[0], err,
				strings.TrimSpace(stderr.String()))
		}
		if got := strings.TrimSpace(stdout.String()); got != check.want {
			return fmt.Errorf("verify Git worktree %s: got %q, want %q",
				check.args[0], got, check.want)
		}
	}
	return nil
}

func verifyWorktreeCommonDirectory(ctx context.Context, gitPath, source,
	destination string,
) error {
	read := func(root string) (string, error) {
		canonical, err := canonicalExistingRepositoryRoot(root)
		if err != nil {
			return "", err
		}
		command := exec.CommandContext(ctx, gitPath, "-C", canonical,
			"--no-optional-locks", "rev-parse", "--path-format=absolute", "--git-common-dir")
		command.Env, command.Dir = hardenedGitEnvironment(), canonical
		stdout := advancedBoundedBuffer{max: MaxGitOutputBytes}
		var stderr boundedBuffer
		command.Stdout, command.Stderr = &stdout, &stderr
		if err := command.Run(); err != nil {
			return "", fmt.Errorf("inspect Git common directory: %w: %s", err,
				strings.TrimSpace(stderr.String()))
		}
		if stdout.truncated {
			return "", errors.New("Git common directory exceeded its output bound")
		}
		return canonicalGitMetadataPath(trimGitLineEnding(stdout.String()))
	}
	sourceCommon, err := read(source)
	if err != nil {
		return err
	}
	destinationCommon, err := read(destination)
	if err != nil {
		return err
	}
	if !sameFilesystemPath(sourceCommon, destinationCommon) {
		return errors.New("Git worktree common repository identity changed")
	}
	return nil
}
