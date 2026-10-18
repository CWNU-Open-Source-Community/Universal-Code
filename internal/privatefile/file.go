// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

// Package privatefile provides the private atomic writes and process locks used
// by the host's encrypted credential files.
package privatefile

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

// Write syncs a private temporary file before replacing path. rename may be nil
// to use os.Rename; callers may inject it to test an interrupted commit.
func Write(path string, data []byte, rename func(string, string) error) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if rename == nil {
		rename = os.Rename
	}
	return rename(f.Name(), path)
}

// WithLock serializes cooperating processes. The parent directory must exist.
func WithLock(ctx context.Context, path string, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	for {
		acquired, err := tryLock(f)
		if err != nil {
			return err
		}
		if acquired {
			defer unlock(f)
			if err := ctx.Err(); err != nil {
				return err
			}
			return fn()
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
