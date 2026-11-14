package uievidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"cyberagent-workbench/internal/rootfs"
	"cyberagent-workbench/internal/workspacecheckpoint"
	"cyberagent-workbench/internal/workspaceidentity"
)

const maxGitStatusBytes = 8 * 1024 * 1024

// BindSource turns an already validated workspace checkpoint into the source
// identity persisted by ui-evidence.v1. Git status bytes and host paths are
// hashed but never retained in the returned record.
func BindSource(ctx context.Context, workspaceRoot string,
	snapshot workspacecheckpoint.Snapshot,
) (SourceBinding, error) {
	if ctx == nil {
		return SourceBinding{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return SourceBinding{}, err
	}
	if err := snapshot.Validate(); err != nil {
		return SourceBinding{}, err
	}
	if workspaceRoot == "" {
		return SourceBinding{}, errors.New("UI evidence Workspace root is invalid")
	}
	root, err := rootfs.EvalExactSymlinks(workspaceRoot)
	if err != nil {
		return SourceBinding{}, errors.New("UI evidence Workspace root is unavailable")
	}
	rootFingerprint, err := workspaceRootFingerprint(root)
	if err != nil || rootFingerprint != snapshot.Checkpoint.RootFingerprint {
		return SourceBinding{}, errors.New("UI evidence Workspace identity changed after checkpoint capture")
	}

	binding := SourceBinding{RepositoryKind: "git",
		Commit: snapshot.Checkpoint.BaseCommit, Branch: snapshot.Checkpoint.Branch,
		RootFingerprint: snapshot.Checkpoint.RootFingerprint,
		IndexSHA256:     snapshot.Checkpoint.IndexSHA256}
	binding.ManifestSHA256, err = sourceManifestSHA256(snapshot.Entries)
	if err != nil {
		return SourceBinding{}, err
	}
	status := []byte(nil)
	if snapshot.Checkpoint.BaseCommit == "non-git" {
		binding.RepositoryKind = "non_git"
	} else {
		status, err = readGitStatus(ctx, root)
		if err != nil {
			return SourceBinding{}, err
		}
		binding.Dirty = len(status) != 0
	}
	payload := struct {
		RepositoryKind string `json:"repository_kind"`
		Commit         string `json:"commit"`
		Branch         string `json:"branch"`
		IndexSHA256    string `json:"index_sha256"`
		ManifestSHA256 string `json:"manifest_sha256"`
		StatusSHA256   string `json:"status_sha256"`
	}{RepositoryKind: binding.RepositoryKind, Commit: binding.Commit,
		Branch: binding.Branch, IndexSHA256: binding.IndexSHA256,
		ManifestSHA256: binding.ManifestSHA256, StatusSHA256: bytesSHA256(status)}
	raw, err := json.Marshal(payload)
	if err != nil {
		return SourceBinding{}, err
	}
	binding.DirtyDigest = bytesSHA256(raw)
	return binding, binding.Validate()
}

// sourceManifestSHA256 binds every tracked and non-ignored untracked entry,
// including its worktree content digest, while deliberately excluding ignored
// build/cache directories. The full checkpoint still records those directories
// as excluded metadata, but their creation during a reviewed build must not be
// confused with source drift.
func sourceManifestSHA256(entries []workspacecheckpoint.Entry) (string, error) {
	sourceEntries := make([]workspacecheckpoint.Entry, 0, len(entries))
	for _, entry := range entries {
		if entry.State != workspacecheckpoint.StateIgnored {
			sourceEntries = append(sourceEntries, entry)
		}
	}
	raw, err := json.Marshal(sourceEntries)
	if err != nil {
		return "", err
	}
	return bytesSHA256(raw), nil
}

func readGitStatus(ctx context.Context, root string) ([]byte, error) {
	if err := rootfs.CheckExternalDirectory(root); err != nil {
		return nil, err
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, errors.New("UI evidence source binding requires Git")
	}
	command := exec.CommandContext(ctx, git, "-C", root, "--no-optional-locks",
		"-c", "core.hooksPath=", "-c", "core.fsmonitor=false", "status",
		"--porcelain=v1", "-z", "--untracked-files=all")
	command.Dir = root
	command.Env = gitStatusEnvironment()
	var stdout, stderr boundedBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("capture UI evidence Git status: %w", err)
	}
	if stdout.exceeded || stderr.exceeded {
		return nil, errors.New("UI evidence Git status exceeds its bound")
	}
	return append([]byte(nil), stdout.Bytes()...), nil
}

type boundedBuffer struct {
	bytes.Buffer
	exceeded bool
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	remaining := maxGitStatusBytes - b.Len()
	if remaining <= 0 {
		b.exceeded = true
		return len(value), nil
	}
	if len(value) > remaining {
		_, _ = b.Buffer.Write(value[:remaining])
		b.exceeded = true
		return len(value), nil
	}
	return b.Buffer.Write(value)
}

func gitStatusEnvironment() []string {
	names := []string{"PATH", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT", "TMP", "TEMP"}
	values := make([]string, 0, len(names)+8)
	for _, name := range names {
		if value, ok := os.LookupEnv(name); ok {
			values = append(values, name+"="+value)
		}
	}
	return append(values, "LANG=C", "LC_ALL=C", "GIT_TERMINAL_PROMPT=0",
		"GCM_INTERACTIVE=never", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_PAGER=cat")
}

func workspaceRootFingerprint(root string) (string, error) {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("UI evidence Workspace root is not a direct directory")
	}
	return workspaceidentity.Fingerprint(filepath.Clean(root))
}

func bytesSHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
