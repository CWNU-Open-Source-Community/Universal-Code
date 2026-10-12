package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unicode/utf8"

	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/workspaceidentity"
)

const (
	DefaultMaxReadBytes   = 128 * 1024
	DefaultMaxListEntries = 200
)

type WorkspaceFS struct {
	Root           string
	MaxReadBytes   int
	MaxListEntries int
	PathPolicy     workspaceidentity.PathPolicy
}

func NewWorkspaceFS(root string) WorkspaceFS {
	return WorkspaceFS{
		Root:           root,
		MaxReadBytes:   DefaultMaxReadBytes,
		MaxListEntries: DefaultMaxListEntries,
	}
}

// ResolveFileForRead validates a workspace-relative file path without reading it.
func (fs WorkspaceFS) ResolveFileForRead(requested string) (string, error) {
	fs = fs.withFallback("")
	return fs.resolveExistingFile(requested)
}

type ReadFileTool struct {
	FS WorkspaceFS
}

func NewReadFileTool(root string) ReadFileTool {
	return ReadFileTool{FS: NewWorkspaceFS(root)}
}

func (ReadFileTool) Name() string {
	return "read_file"
}

func (t ReadFileTool) Run(ctx context.Context, call Call) (Result, error) {
	if ctx == nil {
		return Result{Stderr: "tool context is required", ExitCode: 1, MIME: "text/plain; charset=utf-8"}, errors.New("tool context is required")
	}
	if err := ctx.Err(); err != nil {
		return interruptedResult(err), err
	}
	fs := t.FS.withFallback(call.WorkingDir)
	path, err := fs.resolveExistingFile(call.Args["path"])
	if err != nil {
		return Result{Stderr: err.Error(), ExitCode: 1, MIME: "text/plain; charset=utf-8"}, err
	}
	limit := fs.MaxReadBytes
	if value := strings.TrimSpace(call.Args["max_bytes"]); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 || parsed > fs.MaxReadBytes {
			message := fmt.Sprintf("max_bytes must be between 1 and %d", fs.MaxReadBytes)
			return Result{Stderr: message, ExitCode: 1, MIME: "text/plain; charset=utf-8"}, errors.New(message)
		}
		limit = parsed
	}
	text, truncated, err := readTextLimited(ctx, path, limit)
	if err != nil {
		return Result{Stderr: err.Error(), ExitCode: 1, MIME: "text/plain; charset=utf-8"}, err
	}
	return Result{Stdout: text, ExitCode: 0, MIME: "text/plain; charset=utf-8", Truncated: truncated}, nil
}

type ListWorkspaceTool struct {
	FS WorkspaceFS
}

func NewListWorkspaceTool(root string) ListWorkspaceTool {
	return ListWorkspaceTool{FS: NewWorkspaceFS(root)}
}

func (ListWorkspaceTool) Name() string {
	return "list_workspace"
}

func (t ListWorkspaceTool) Run(ctx context.Context, call Call) (Result, error) {
	if ctx == nil {
		return Result{Stderr: "tool context is required", ExitCode: 1, MIME: "text/plain; charset=utf-8"}, errors.New("tool context is required")
	}
	if err := ctx.Err(); err != nil {
		return interruptedResult(err), err
	}
	fs := t.FS.withFallback(call.WorkingDir)
	path := strings.TrimSpace(call.Args["path"])
	if path == "" {
		path = "."
	}
	depth := 2
	if value := strings.TrimSpace(call.Args["max_depth"]); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			return Result{Stderr: "max_depth must be a non-negative integer", ExitCode: 1, MIME: "text/plain; charset=utf-8"}, errors.New("max_depth must be a non-negative integer")
		}
		depth = parsed
	}
	out, truncated, err := fs.list(ctx, path, depth)
	if err != nil {
		return Result{Stderr: err.Error(), ExitCode: 1, MIME: "text/plain; charset=utf-8"}, err
	}
	return Result{Stdout: out, ExitCode: 0, MIME: "text/plain; charset=utf-8", Truncated: truncated}, nil
}

func (fs WorkspaceFS) withFallback(root string) WorkspaceFS {
	if strings.TrimSpace(fs.Root) == "" {
		fs.Root = root
	}
	if fs.MaxReadBytes <= 0 {
		fs.MaxReadBytes = DefaultMaxReadBytes
	}
	if fs.MaxListEntries <= 0 {
		fs.MaxListEntries = DefaultMaxListEntries
	}
	return fs
}

func (fs WorkspaceFS) resolveExistingFile(requested string) (string, error) {
	path, err := fs.resolveExisting(requested)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", requested)
	}
	return path, nil
}

// ResolveForWrite returns a workspace-scoped file path without following
// symlinks, junctions, reparse-point redirects, or case aliases. New files
// require an existing real parent directory.
func (fs WorkspaceFS) ResolveForWrite(requested string) (string, error) {
	return fs.resolveForWrite(requested, false)
}

// ResolveForCreate is a read-only create-target check. Missing parent suffixes
// are allowed; every existing component retains the ordinary write checks.
// It never creates directories or authorizes a write.
func (fs WorkspaceFS) ResolveForCreate(requested string) (string, error) {
	return fs.resolveForWrite(requested, true)
}

func (fs WorkspaceFS) resolveForWrite(requested string, allowMissingParents bool) (string, error) {
	fs = fs.withFallback("")
	if strings.TrimSpace(fs.Root) == "" {
		return "", errors.New("workspace root is required")
	}
	requested = strings.TrimSpace(requested)
	if requested == "" || requested == "." {
		return "", errors.New("file path is required")
	}
	if filepath.IsAbs(requested) {
		return "", errors.New("path must be relative to the workspace")
	}

	root, err := filepath.Abs(fs.Root)
	if err != nil {
		return "", err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("workspace root must be a real directory")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil || !sameWorkspacePath(root, resolvedRoot) {
		return "", errors.New("workspace root cannot be redirected")
	}
	root = filepath.Clean(resolvedRoot)
	candidate, err := filepath.Abs(filepath.Join(root, requested))
	if err != nil {
		return "", err
	}
	if !withinRoot(root, candidate) {
		return "", fmt.Errorf("path escapes workspace: %s", requested)
	}

	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return "", err
	}
	current := root
	components := strings.Split(relative, string(os.PathSeparator))
	for index, component := range components {
		entries, readErr := os.ReadDir(current)
		if readErr != nil {
			return "", fmt.Errorf("parent directory must already exist: %w", readErr)
		}
		exact, alias := false, false
		for _, entry := range entries {
			if entry.Name() == component {
				exact = true
				break
			}
			if strings.EqualFold(entry.Name(), component) {
				alias = true
			}
		}
		last := index == len(components)-1
		if !exact {
			if alias {
				return "", errors.New("workspace path casing does not match the stored entry")
			}
			if last || allowMissingParents {
				return candidate, nil
			}
			return "", errors.New("parent directory must already exist")
		}
		current = filepath.Join(current, component)
		info, infoErr := os.Lstat(current)
		if infoErr != nil {
			return "", infoErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("workspace writes do not follow symbolic links or reparse points")
		}
		resolved, resolveErr := filepath.EvalSymlinks(current)
		if resolveErr != nil || !withinRoot(root, resolved) || !sameWorkspacePath(current, resolved) {
			return "", errors.New("workspace writes do not follow redirected paths")
		}
		if last {
			if info.IsDir() {
				return "", fmt.Errorf("%s is a directory", requested)
			}
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("%s is not a regular file", requested)
			}
		} else if !info.IsDir() {
			return "", errors.New("parent path is not a directory")
		}
	}
	return current, nil
}

func sameWorkspacePath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func (fs WorkspaceFS) resolveExistingDir(requested string) (string, error) {
	path, err := fs.resolveExisting(requested)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", requested)
	}
	return path, nil
}

func (fs WorkspaceFS) resolveExisting(requested string) (string, error) {
	if strings.TrimSpace(fs.Root) == "" {
		return "", errors.New("workspace root is required")
	}
	requested = strings.TrimSpace(requested)
	if requested == "" {
		requested = "."
	}
	if filepath.IsAbs(requested) {
		return "", errors.New("path must be relative to the workspace")
	}
	root, err := filepath.Abs(fs.Root)
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(root, requested)
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	candidate, err = filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	if !withinRoot(root, candidate) {
		return "", fmt.Errorf("path escapes workspace: %s", requested)
	}
	if len(fs.PathPolicy.ExcludePaths) != 0 {
		relative, err := filepath.Rel(root, candidate)
		if err != nil {
			return "", err
		}
		if err := fs.PathPolicy.Check(filepath.ToSlash(relative)); err != nil {
			return "", err
		}
	}
	return candidate, nil
}

func (fs WorkspaceFS) list(ctx context.Context, requested string, maxDepth int) (string, bool, error) {
	base, err := fs.resolveExistingDir(requested)
	if err != nil {
		return "", false, err
	}
	policyBase := requested
	if len(fs.PathPolicy.ExcludePaths) != 0 {
		root, err := filepath.Abs(fs.Root)
		if err != nil {
			return "", false, err
		}
		root, err = filepath.EvalSymlinks(root)
		if err != nil {
			return "", false, err
		}
		policyBase, err = filepath.Rel(root, base)
		if err != nil {
			return "", false, err
		}
	}
	var lines []string
	truncated := false
	limit := fs.MaxListEntries
	err = filepath.WalkDir(base, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if path == base {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		if len(fs.PathPolicy.ExcludePaths) != 0 {
			projectPath := filepath.ToSlash(filepath.Join(policyBase, rel))
			if err := fs.PathPolicy.Check(projectPath); err != nil {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		depth := pathDepth(rel)
		if depth > maxDepth {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if entry.IsDir() {
			name += string(os.PathSeparator)
		}
		lines = append(lines, strings.Repeat("  ", max(0, depth-1))+name)
		if len(lines) >= limit {
			lines = append(lines, fmt.Sprintf("... truncated at %d entries", limit))
			truncated = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", false, err
	}
	if len(lines) == 0 {
		return "(empty)", false, nil
	}
	return strings.Join(lines, "\n"), truncated, nil
}

func readTextLimited(ctx context.Context, path string, maxBytes int) (string, bool, error) {
	if maxBytes <= 0 {
		return "", false, errors.New("max bytes must be positive")
	}
	if maxBytes > int(^uint(0)>>1)-utf8.UTFMax {
		return "", false, errors.New("max bytes exceeds the platform integer limit")
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	file, err := openRegularFile(path)
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	limit := maxBytes + utf8.UTFMax
	data := make([]byte, 0, limit)
	buffer := make([]byte, min(32*1024, limit))
	for len(data) < limit {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		readLimit := min(len(buffer), limit-len(data))
		count, readErr := file.Read(buffer[:readLimit])
		if count > 0 {
			data = append(data, buffer[:count]...)
		}
		if readErr != nil {
			if errors.Is(readErr, os.ErrClosed) {
				return "", false, readErr
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			return "", false, readErr
		}
		if count == 0 {
			break
		}
	}
	truncated := len(data) > maxBytes
	if truncated {
		data, err = validUTF8PrefixAtLimit(data, maxBytes)
		if err != nil {
			return "", false, err
		}
	}
	if !utf8.Valid(data) {
		return "", false, errors.New("file is not valid UTF-8 text")
	}
	text := string(data)
	if truncated {
		text += fmt.Sprintf("\n[truncated at %d bytes]\n", maxBytes)
	}
	return redact.String(text), truncated, nil
}

func validUTF8PrefixAtLimit(data []byte, limit int) ([]byte, error) {
	if len(data) <= limit {
		if !utf8.Valid(data) {
			return nil, errors.New("file is not valid UTF-8 text")
		}
		return data, nil
	}
	prefix := data[:limit]
	if utf8.Valid(prefix) {
		return prefix, nil
	}
	start := len(prefix) - 1
	for start > 0 && !utf8.RuneStart(prefix[start]) {
		start--
	}
	if !utf8.Valid(prefix[:start]) {
		return nil, errors.New("file is not valid UTF-8 text")
	}
	current, size := utf8.DecodeRune(data[start:])
	if current == utf8.RuneError && size == 1 {
		return nil, errors.New("file is not valid UTF-8 text")
	}
	if start+size <= limit {
		return nil, errors.New("file is not valid UTF-8 text")
	}
	return prefix[:start], nil
}

func withinRoot(root string, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func pathDepth(rel string) int {
	if rel == "." || rel == "" {
		return 0
	}
	return 1 + strings.Count(rel, string(os.PathSeparator))
}
