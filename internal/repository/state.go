package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/redact"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

const (
	ProtocolVersion    = "repository_state.v1"
	MaxChangeItems     = 200
	MaxStatusEntries   = 10_000
	MaxMetadataEntries = 50_000
	MaxPathRunes       = 512
	MaxReferenceRunes  = 255
)

type Change struct {
	Path     string `json:"path"`
	Staging  string `json:"staging"`
	Worktree string `json:"worktree"`
}

type State struct {
	ProtocolVersion string `json:"protocol_version"`
	WorkspaceID     string `json:"workspace_id"`
	Kind            string `json:"kind"`
	Available       bool   `json:"available"`
	Clean           bool   `json:"clean"`
	Detached        bool   `json:"detached"`
	Branch          string `json:"branch"`
	Head            string `json:"head"`
	// FullHead is used by Go-owned immutable bindings. Public views retain the
	// deliberately shortened display hash in Head.
	FullHead             string   `json:"-"`
	Changes              []Change `json:"changes"`
	StagedCount          int      `json:"staged_count"`
	WorktreeCount        int      `json:"worktree_count"`
	UntrackedCount       int      `json:"untracked_count"`
	ConflictedCount      int      `json:"conflicted_count"`
	RedactionCount       int      `json:"redaction_count"`
	Truncated            bool     `json:"truncated"`
	ReadOnly             bool     `json:"read_only"`
	RootPathExposed      bool     `json:"root_path_exposed"`
	ContentIncluded      bool     `json:"content_included"`
	RemoteConfigIncluded bool     `json:"remote_config_included"`
	ProcessStarted       bool     `json:"process_started"`
	NetworkUsed          bool     `json:"network_used"`
	HooksExecuted        bool     `json:"hooks_executed"`
}

func Inspect(ctx context.Context, root string, workspaceID string) (State, error) {
	return inspectChanges(ctx, root, workspaceID, MaxChangeItems)
}

func inspectChanges(ctx context.Context, root string, workspaceID string, changeLimit int) (State, error) {
	base := State{ProtocolVersion: ProtocolVersion, WorkspaceID: workspaceID,
		Kind: "none", Changes: []Change{}, ReadOnly: true}
	if err := validateIdentity(workspaceID); err != nil {
		return State{}, err
	}
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	_, repo, available, err := openExactRepository(ctx, root)
	if err != nil {
		return State{}, err
	}
	if !available {
		return base, nil
	}
	worktree, err := repo.Worktree()
	if err != nil {
		return State{}, apperror.New(apperror.CodeFailedPrecondition,
			"repository worktree is unavailable")
	}
	status, err := worktree.StatusWithOptions(git.StatusOptions{Strategy: git.Empty})
	if err != nil {
		return State{}, apperror.New(apperror.CodeFailedPrecondition,
			"repository status could not be inspected")
	}
	if err := ctx.Err(); err != nil {
		return State{}, err
	}

	base.Kind = "git"
	base.Available = true
	base.Clean = status.IsClean()
	if head, headErr := repo.Head(); headErr == nil {
		base.FullHead = head.Hash().String()
		base.Head = shortHash(base.FullHead)
		if head.Name().IsBranch() {
			base.Branch, base.Truncated, base.RedactionCount = safeReference(
				head.Name().Short(), base.Truncated, base.RedactionCount)
		} else {
			base.Detached = true
		}
	} else if !errors.Is(headErr, plumbing.ErrReferenceNotFound) {
		return State{}, apperror.New(apperror.CodeFailedPrecondition,
			"repository HEAD could not be inspected")
	}

	paths := make([]string, 0, len(status))
	for path := range status {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	if len(paths) > MaxStatusEntries {
		paths = paths[:MaxStatusEntries]
		base.Truncated = true
	}
	for _, path := range paths {
		entry := status[path]
		if entry == nil || (entry.Staging == git.Unmodified && entry.Worktree == git.Unmodified) {
			continue
		}
		staging, ok := statusName(entry.Staging)
		if !ok {
			base.Truncated = true
			continue
		}
		worktreeState, ok := statusName(entry.Worktree)
		if !ok {
			base.Truncated = true
			continue
		}
		if entry.Staging != git.Unmodified && entry.Staging != git.Untracked {
			base.StagedCount++
		}
		if entry.Worktree == git.Untracked {
			base.UntrackedCount++
		} else if entry.Worktree != git.Unmodified {
			base.WorktreeCount++
		}
		if entry.Staging == git.UpdatedButUnmerged || entry.Worktree == git.UpdatedButUnmerged {
			base.ConflictedCount++
		}
		canonical, redactions, ok := safePath(path)
		base.RedactionCount += redactions
		if !ok {
			base.Truncated = true
			continue
		}
		if len(base.Changes) >= changeLimit {
			base.Truncated = true
			continue
		}
		base.Changes = append(base.Changes, Change{Path: canonical,
			Staging: staging, Worktree: worktreeState})
	}
	return base, nil
}

func openExactRepository(ctx context.Context, root string) (string, *git.Repository, bool, error) {
	resolvedRoot, err := resolveRoot(root)
	if err != nil {
		return "", nil, false, err
	}
	dotGit := filepath.Join(resolvedRoot, ".git")
	info, err := os.Lstat(dotGit)
	if os.IsNotExist(err) {
		return resolvedRoot, nil, false, nil
	}
	if err != nil {
		return "", nil, false, apperror.New(apperror.CodeFailedPrecondition,
			"repository metadata could not be inspected")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", nil, false, apperror.New(apperror.CodeFailedPrecondition,
			"linked or redirected Git metadata is not supported")
	}
	if err := rejectRedirectedMetadata(ctx, dotGit); err != nil {
		return "", nil, false, err
	}
	repo, err := git.PlainOpenWithOptions(resolvedRoot, &git.PlainOpenOptions{
		DetectDotGit: false,
	})
	if errors.Is(err, git.ErrRepositoryNotExists) {
		return resolvedRoot, nil, false, nil
	}
	if err != nil {
		return "", nil, false, apperror.New(apperror.CodeFailedPrecondition,
			"repository metadata could not be opened")
	}
	return resolvedRoot, repo, true, nil
}

func rejectRedirectedMetadata(ctx context.Context, dotGit string) error {
	entries := 0
	err := filepath.WalkDir(dotGit, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry == nil {
			return apperror.New(apperror.CodeFailedPrecondition,
				"repository metadata could not be inspected")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		entries++
		if entries > MaxMetadataEntries {
			return apperror.New(apperror.CodeResourceExhausted,
				"repository metadata exceeds the inspection limit")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return apperror.New(apperror.CodeFailedPrecondition,
				"linked or redirected Git metadata is not supported")
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func resolveRoot(root string) (string, error) {
	if root == "" || root != strings.TrimSpace(root) || strings.ContainsRune(root, 0) {
		return "", apperror.New(apperror.CodeFailedPrecondition,
			"workspace root is unavailable")
	}
	resolved, err := filepath.Abs(root)
	if err != nil {
		return "", apperror.New(apperror.CodeFailedPrecondition,
			"workspace root could not be resolved")
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", apperror.New(apperror.CodeFailedPrecondition,
			"workspace root could not be resolved")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", apperror.New(apperror.CodeFailedPrecondition,
			"workspace root is unavailable")
	}
	return resolved, nil
}

func validateIdentity(value string) error {
	if value == "" || value != strings.TrimSpace(value) || strings.ContainsRune(value, 0) ||
		!utf8.ValidString(value) || utf8.RuneCountInString(value) > 255 {
		return apperror.New(apperror.CodeInvalidArgument, "workspace identity is invalid")
	}
	for _, current := range value {
		if unicode.IsControl(current) {
			return apperror.New(apperror.CodeInvalidArgument, "workspace identity is invalid")
		}
	}
	return nil
}

func safePath(value string) (string, int, bool) {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > MaxPathRunes ||
		filepath.IsAbs(value) || strings.ContainsAny(value, `\:`) || strings.ContainsRune(value, 0) {
		return "", 0, false
	}
	for _, current := range value {
		if unicode.IsControl(current) {
			return "", 0, false
		}
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if cleaned != value || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", 0, false
	}
	projected := redact.Text(cleaned)
	redactions := 0
	for _, finding := range projected.Findings {
		redactions += finding.Count
	}
	if projected.Text != cleaned {
		return "", redactions, false
	}
	return cleaned, redactions, true
}

// PublicPath returns a normalized repository-relative path only when it is
// safe for a public projection. Absolute, secret-shaped, control-bearing, and
// traversal paths are omitted rather than rewritten into a different identity.
func PublicPath(value string) (string, bool) {
	canonical, _, ok := safePath(value)
	return canonical, ok
}

func safeReference(value string, truncated bool, redactionCount int) (string, bool, int) {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > MaxReferenceRunes ||
		strings.ContainsRune(value, 0) {
		return "", true, redactionCount
	}
	for _, current := range value {
		if unicode.IsControl(current) {
			return "", true, redactionCount
		}
	}
	projected := redact.Text(value)
	for _, finding := range projected.Findings {
		redactionCount += finding.Count
	}
	if projected.Text != value {
		return "", true, redactionCount
	}
	return value, truncated, redactionCount
}

func shortHash(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	return value
}

func statusName(value git.StatusCode) (string, bool) {
	switch value {
	case git.Unmodified:
		return "unmodified", true
	case git.Untracked:
		return "untracked", true
	case git.Modified:
		return "modified", true
	case git.Added:
		return "added", true
	case git.Deleted:
		return "deleted", true
	case git.Renamed:
		return "renamed", true
	case git.Copied:
		return "copied", true
	case git.UpdatedButUnmerged:
		return "conflicted", true
	default:
		return "", false
	}
}
