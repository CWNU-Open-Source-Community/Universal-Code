package codeintel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/workspace"
)

const (
	maxDirtyBindingFileBytes  = 16 * 1024 * 1024
	maxDirtyBindingIndexBytes = 64 * 1024 * 1024
	maxDirtyBindingTotalBytes = 64 * 1024 * 1024
)

type workspaceBinding struct {
	WorkspaceID         string
	Root                string
	RootFingerprint     string
	RepositoryAvailable bool
	Commit              string
	Branch              string
	Dirty               bool
	DirtyDigest         string
}

type documentBinding struct {
	Path    string
	Target  string
	URI     string
	Text    string
	SHA256  string
	Version int
}

func captureWorkspaceBinding(ctx context.Context, root, workspaceID string) (workspaceBinding, error) {
	if ctx == nil {
		return workspaceBinding{}, errors.New("LSP workspace binding requires a context")
	}
	rootFingerprint, err := workspace.AgentCodeRootFingerprint(root)
	if err != nil {
		return workspaceBinding{}, err
	}
	canonical, err := filepath.Abs(root)
	if err != nil {
		return workspaceBinding{}, apperror.New(apperror.CodeFailedPrecondition,
			"LSP workspace root could not be resolved")
	}
	canonical = filepath.Clean(canonical)
	binding := workspaceBinding{WorkspaceID: workspaceID, Root: canonical,
		RootFingerprint: rootFingerprint}
	if metadata, err := os.Lstat(filepath.Join(canonical, ".git")); err == nil && metadata.Mode().IsRegular() {
		linked, err := repository.CaptureLinkedWorktreeBinding(ctx, canonical)
		if err != nil {
			return workspaceBinding{}, err
		}
		binding.RepositoryAvailable = true
		binding.Commit, binding.Branch = linked.Head, linked.Branch
		if binding.Commit == "unborn" {
			binding.Commit = ""
		}
		emptyStatus := sha256.Sum256(nil)
		binding.Dirty = linked.StatusSHA256 != hex.EncodeToString(emptyStatus[:])
		binding.DirtyDigest = digestStrings(ProtocolVersion, workspaceID, rootFingerprint,
			linked.CommonDirSHA256, linked.IndexSHA256, linked.WorktreeSHA256, linked.StatusSHA256)
		return binding, nil
	}
	state, inspectErr := repository.Inspect(ctx, canonical, workspaceID)
	if inspectErr != nil {
		return workspaceBinding{}, inspectErr
	}
	if state.Available {
		binding.RepositoryAvailable = true
		binding.Commit = state.FullHead
		binding.Branch = state.Branch
		binding.Dirty = !state.Clean
		binding.DirtyDigest, err = captureDirtyBinding(ctx, canonical, state)
		if err != nil {
			return workspaceBinding{}, err
		}
	} else {
		binding.DirtyDigest = digestStrings(ProtocolVersion, workspaceID, rootFingerprint,
			"repository_unavailable")
	}
	return binding, nil
}

type dirtyContentBinding struct {
	Path     string `json:"path"`
	Staging  string `json:"staging"`
	Worktree string `json:"worktree"`
	State    string `json:"state"`
	SHA256   string `json:"sha256,omitempty"`
	Bytes    int64  `json:"bytes,omitempty"`
}

func captureDirtyBinding(ctx context.Context, root string,
	state repository.State,
) (string, error) {
	if state.Truncated || state.RedactionCount != 0 {
		return "", apperror.New(apperror.CodeResourceExhausted,
			"Git dirty state is too large or sensitive to bind semantic evidence safely")
	}
	indexState := "not_required"
	indexDigest := ""
	indexBytes := int64(0)
	var err error
	if state.StagedCount > 0 || state.ConflictedCount > 0 {
		indexState, indexDigest, indexBytes, err = hashBindingFile(ctx,
			filepath.Join(root, ".git", "index"), maxDirtyBindingIndexBytes)
		if err != nil {
			return "", err
		}
	}
	totalBytes := indexBytes
	content := make([]dirtyContentBinding, 0, len(state.Changes))
	for _, change := range state.Changes {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		target, _, err := workspace.AgentCodeResolveWritePath(root, change.Path, true)
		if err != nil {
			return "", apperror.Wrap(apperror.CodeFailedPrecondition,
				"Git dirty path cannot be bound safely", err)
		}
		remaining := int64(maxDirtyBindingTotalBytes) - totalBytes
		if remaining <= 0 {
			return "", apperror.New(apperror.CodeResourceExhausted,
				"Git dirty content exceeds the semantic evidence binding limit")
		}
		limit := int64(maxDirtyBindingFileBytes)
		if remaining < limit {
			limit = remaining
		}
		fileState, digest, size, err := hashBindingFile(ctx, target, limit)
		if err != nil {
			return "", err
		}
		totalBytes += size
		content = append(content, dirtyContentBinding{Path: change.Path,
			Staging: change.Staging, Worktree: change.Worktree,
			State: fileState, SHA256: digest, Bytes: size})
	}
	raw, _ := json.Marshal(struct {
		Protocol        string                `json:"protocol"`
		Available       bool                  `json:"available"`
		Commit          string                `json:"commit"`
		Branch          string                `json:"branch"`
		Detached        bool                  `json:"detached"`
		Content         []dirtyContentBinding `json:"content"`
		IndexState      string                `json:"index_state"`
		IndexSHA256     string                `json:"index_sha256,omitempty"`
		IndexBytes      int64                 `json:"index_bytes,omitempty"`
		StagedCount     int                   `json:"staged_count"`
		WorktreeCount   int                   `json:"worktree_count"`
		UntrackedCount  int                   `json:"untracked_count"`
		ConflictedCount int                   `json:"conflicted_count"`
	}{Protocol: ProtocolVersion, Available: true, Commit: state.FullHead,
		Branch: state.Branch, Detached: state.Detached, Content: content,
		IndexState: indexState, IndexSHA256: indexDigest, IndexBytes: indexBytes,
		StagedCount: state.StagedCount, WorktreeCount: state.WorktreeCount,
		UntrackedCount: state.UntrackedCount, ConflictedCount: state.ConflictedCount})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func hashBindingFile(ctx context.Context, target string, maximum int64) (
	string, string, int64, error,
) {
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return "missing", "", 0, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", "", 0, apperror.New(apperror.CodeFailedPrecondition,
			"Git dirty binding target is unavailable or redirected")
	}
	if maximum < 0 || info.Size() < 0 || info.Size() > maximum {
		return "", "", 0, apperror.New(apperror.CodeResourceExhausted,
			"Git dirty file exceeds the semantic evidence binding limit")
	}
	file, err := os.Open(target)
	if err != nil {
		return "", "", 0, apperror.New(apperror.CodeFailedPrecondition,
			"Git dirty binding target could not be opened")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return "", "", 0, apperror.New(apperror.CodeConflict,
			"Git dirty binding target changed while it was opened")
	}
	hash := sha256.New()
	buffer := make([]byte, 64*1024)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return "", "", 0, err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			written += int64(count)
			if written > maximum {
				return "", "", 0, apperror.New(apperror.CodeResourceExhausted,
					"Git dirty file exceeds the semantic evidence binding limit")
			}
			_, _ = hash.Write(buffer[:count])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", "", 0, apperror.New(apperror.CodeFailedPrecondition,
				"Git dirty binding target could not be read")
		}
	}
	completed, err := file.Stat()
	if err != nil || !os.SameFile(opened, completed) || written != opened.Size() ||
		opened.Size() != completed.Size() || !opened.ModTime().Equal(completed.ModTime()) {
		return "", "", 0, apperror.New(apperror.CodeConflict,
			"Git dirty binding target changed while it was hashed")
	}
	return "regular", hex.EncodeToString(hash.Sum(nil)), written, nil
}

func captureDocumentBinding(root, requested string, version int) (documentBinding, error) {
	target, _, err := workspace.AgentCodeResolveWritePath(root, requested, false)
	if err != nil {
		return documentBinding{}, err
	}
	text, digest, err := workspace.AgentCodeReadMutationSource(root, requested)
	if err != nil {
		return documentBinding{}, err
	}
	uri, err := fileURI(target)
	if err != nil {
		return documentBinding{}, err
	}
	return documentBinding{Path: requested, Target: target, URI: uri, Text: text,
		SHA256: digest, Version: version}, nil
}

func fileURI(target string) (string, error) {
	absolute, err := filepath.Abs(target)
	if err != nil {
		return "", apperror.New(apperror.CodeFailedPrecondition,
			"LSP document path could not be resolved")
	}
	value := filepath.ToSlash(filepath.Clean(absolute))
	if runtime.GOOS == "windows" && !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return (&url.URL{Scheme: "file", Path: value}).String(), nil
}

func workspaceURI(root string) (string, error) { return fileURI(root) }

// workspaceRelativeURI treats every server URI as untrusted input. It accepts
// only a real file already reachable through the Agent Code path policy and
// returns the canonical Workspace-relative spelling used in semantic evidence.
func workspaceRelativeURI(root, rawURI string) (string, string, error) {
	if rawURI == "" || len(rawURI) > 8192 || !strings.HasPrefix(strings.ToLower(rawURI), "file:") {
		return "", "", apperror.New(apperror.CodePolicyDenied,
			"language server returned a non-file or oversized URI")
	}
	parsed, err := url.Parse(rawURI)
	if err != nil || !strings.EqualFold(parsed.Scheme, "file") || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost")) {
		return "", "", apperror.New(apperror.CodePolicyDenied,
			"language server returned an invalid file URI")
	}
	decoded, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil || strings.ContainsRune(decoded, 0) {
		return "", "", apperror.New(apperror.CodePolicyDenied,
			"language server returned an invalid encoded file URI")
	}
	if runtime.GOOS == "windows" && len(decoded) >= 3 && decoded[0] == '/' &&
		decoded[2] == ':' {
		decoded = decoded[1:]
	}
	target := filepath.Clean(filepath.FromSlash(decoded))
	if !filepath.IsAbs(target) {
		return "", "", apperror.New(apperror.CodePolicyDenied,
			"language server returned a relative file URI")
	}
	canonicalRoot, err := filepath.Abs(root)
	if err != nil {
		return "", "", apperror.New(apperror.CodeFailedPrecondition,
			"LSP workspace root could not be resolved")
	}
	relative, err := filepath.Rel(filepath.Clean(canonicalRoot), target)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", "", apperror.New(apperror.CodePolicyDenied,
			"language server returned a URI outside the Workspace")
	}
	relative = filepath.ToSlash(relative)
	resolved, _, err := workspace.AgentCodeResolveWritePath(root, relative, false)
	if err != nil {
		return "", "", err
	}
	if !samePlatformPath(resolved, target) {
		return "", "", apperror.New(apperror.CodePolicyDenied,
			"language server returned a redirected or case-confused URI")
	}
	canonicalURI, err := fileURI(resolved)
	if err != nil {
		return "", "", err
	}
	return relative, canonicalURI, nil
}

func samePlatformPath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func newProvenance(workspace workspaceBinding, document *documentBinding,
	snapshot CapabilitySnapshot, queryFingerprint string,
) Provenance {
	value := Provenance{ProtocolVersion: ProtocolVersion,
		WorkspaceID: workspace.WorkspaceID, RootFingerprint: workspace.RootFingerprint,
		RepositoryAvailable: workspace.RepositoryAvailable, Commit: workspace.Commit,
		Branch: workspace.Branch, Dirty: workspace.Dirty, DirtyDigest: workspace.DirtyDigest,
		ServerID: snapshot.ServerID, ServerGeneration: snapshot.Generation,
		CapabilityFingerprint: snapshot.CapabilityFingerprint,
		QueryFingerprint:      queryFingerprint}
	if document != nil {
		value.DocumentURI = document.URI
		value.DocumentPath = document.Path
		value.DocumentSHA256 = document.SHA256
		value.DocumentVersion = document.Version
	}
	return value
}
