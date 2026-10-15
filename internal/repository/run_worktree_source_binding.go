package repository

import (
	"context"
	"errors"

	"cyberagent-workbench/internal/gitadvanced"
)

// CaptureSourceRepositoryBinding observes identity even while a native Git
// sequence has detached HEAD. It grants no worktree mutation authority.
func (e *RunWorktreeExecutor) CaptureSourceRepositoryBinding(ctx context.Context, root string) (gitadvanced.RepositoryBinding, error) {
	if !e.Available() {
		return gitadvanced.RepositoryBinding{}, errors.New("Drydock source inspection is unavailable")
	}
	return e.advanced.CaptureAdvancedBinding(ctx, root)
}
