package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"cyberagent-workbench/internal/gitadvanced"
)

// CaptureLinkedWorktreeBinding reads an exact linked worktree through the same
// hardened Git inspection used for native worktrees. A redirected .git file
// is accepted only when the common repository registers this exact root.
func CaptureLinkedWorktreeBinding(ctx context.Context, root string) (gitadvanced.RepositoryBinding, error) {
	var empty gitadvanced.RepositoryBinding
	canonical, err := canonicalExistingRepositoryRoot(root)
	if err != nil {
		return empty, err
	}
	dotGit := filepath.Join(canonical, ".git")
	if info, err := os.Lstat(dotGit); err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return empty, errors.New("linked worktree requires a bounded regular .git file")
	}
	executor, err := NewAdvancedExecutor("", false)
	if err != nil {
		return empty, err
	}
	gitDir, err := executor.requiredPathOutput(ctx, canonical, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return empty, err
	}
	gitDir, err = canonicalGitMetadataPath(gitDir)
	if err != nil {
		return empty, err
	}
	common, err := executor.requiredPathOutput(ctx, canonical, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return empty, err
	}
	common, err = canonicalGitMetadataPath(common)
	if err != nil {
		return empty, err
	}
	registration, err := filepath.Rel(filepath.Join(common, "worktrees"), gitDir)
	if err != nil || registration == "." || registration == ".." || strings.ContainsAny(registration, "/\\") {
		return empty, errors.New("linked worktree metadata is outside its common repository registration")
	}
	backref := filepath.Join(gitDir, "gitdir")
	if info, err := os.Lstat(backref); err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return empty, errors.New("linked worktree root registration is unavailable")
	}
	raw, err := os.ReadFile(backref)
	if err != nil {
		return empty, err
	}
	registered, err := canonicalGitMetadataPath(strings.TrimSpace(string(raw)))
	if err != nil || !sameFilesystemPath(registered, dotGit) {
		return empty, errors.New("linked worktree registration belongs to a different root")
	}
	return executor.CaptureAdvancedBinding(ctx, canonical)
}
