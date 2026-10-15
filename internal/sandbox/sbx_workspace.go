package sandbox

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

const sbxMaximumTreeEntries = 250_000

// A direct SBX mount exposes host inodes, not copied workspace contents.
// Reject every existing multi-link file rather than trying to infer where its
// other names live. This is a pre-dispatch check of the granted workspace;
// it does not make concurrently modified host files immutable.
func sbxValidateWorkspace(ctx context.Context, root string) error {
	return sbxValidateWorkspaceForSource(ctx, root, false)
}

func sbxValidateWorkspaceForSource(ctx context.Context, root string, sourceRepository bool) error {
	if ctx == nil {
		return ErrSBXBoundary
	}
	if _, err := sbxCanonical(root, true); err != nil {
		return errors.Join(ErrSBXBoundary, err)
	}
	git, err := os.Lstat(filepath.Join(root, ".git"))
	if err != nil || (!git.Mode().IsRegular() && !(sourceRepository && git.IsDir())) {
		return errors.Join(ErrSBXBoundary, err)
	}
	count := 0
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return errors.Join(ErrSBXBoundary, err)
		}
		if walkErr != nil {
			return errors.Join(ErrSBXBoundary, walkErr)
		}
		count++
		if count > sbxMaximumTreeEntries {
			return ErrSBXBoundary
		}
		info, err := entry.Info() // Lstat semantics: do not follow a directory link.
		if err != nil || info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return errors.Join(ErrSBXBoundary, err)
		}
		if err := sbxValidateWorkspaceEntry(path, info); err != nil {
			return errors.Join(ErrSBXBoundary, err)
		}
		return nil
	})
}
