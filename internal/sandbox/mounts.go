package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"cyberagent-workbench/internal/rootfs"
)

type MountBinding struct {
	Fingerprint      string
	MountCount       int
	RegularFileCount int
	DirectoryCount   int
}

// ResolveMountSources opens every source through os.Root. It follows only
// links that stay beneath the already trusted workspace root and rejects
// devices, pipes, sockets, and other non-file objects.
func ResolveMountSources(ctx context.Context, rootPath string, manifest Manifest) (MountBinding, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, err := NormalizeManifest(manifest)
	if err != nil {
		return MountBinding{}, err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return MountBinding{}, fmt.Errorf("open sandbox workspace root: %w", err)
	}
	defer root.Close()

	parts := make([]string, 0, 1+len(normalized.Mounts)*7)
	parts = append(parts, "sandbox_mount_binding.v1")
	binding := MountBinding{MountCount: len(normalized.Mounts)}
	for index, mount := range normalized.Mounts {
		if err := ctx.Err(); err != nil {
			return MountBinding{}, err
		}
		name := filepath.FromSlash(mount.Source)
		before, err := root.Stat(name)
		if err != nil {
			return MountBinding{}, fmt.Errorf("stat sandbox mount source %d: %w", index+1, err)
		}
		if !before.Mode().IsRegular() && !before.IsDir() {
			return MountBinding{}, fmt.Errorf("sandbox mount source %d is not a regular file or directory", index+1)
		}
		file, err := rootfs.OpenReadOnly(root, name)
		if err != nil {
			return MountBinding{}, fmt.Errorf("open sandbox mount source %d: %w", index+1, err)
		}
		info, statErr := file.Stat()
		closeErr := file.Close()
		if statErr != nil {
			return MountBinding{}, fmt.Errorf("stat sandbox mount source %d: %w", index+1, statErr)
		}
		if closeErr != nil {
			return MountBinding{}, fmt.Errorf("close sandbox mount source %d: %w", index+1, closeErr)
		}
		kind := ""
		switch {
		case info.Mode().IsRegular():
			kind = "regular"
			binding.RegularFileCount++
		case info.IsDir():
			kind = "directory"
			binding.DirectoryCount++
		default:
			return MountBinding{}, fmt.Errorf("sandbox mount source %d is not a regular file or directory", index+1)
		}
		parts = append(parts, mount.Source, mount.Target, string(mount.Access), kind,
			strconv.FormatInt(info.Size(), 10), info.ModTime().UTC().Format(time.RFC3339Nano))
	}
	binding.Fingerprint = fingerprint(parts...)
	return binding, nil
}
