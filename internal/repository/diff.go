package repository

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/workspace"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const (
	DiffProtocolVersion       = "repository_diff.v1"
	MaxDiffItems              = 50
	MaxDiffFileBytes          = 4 * 1024 * 1024
	MaxDiffPatchBytes         = 256 * 1024
	MaxDiffTotalBytes         = 2 * 1024 * 1024
	DiffContentText           = "text"
	DiffContentBinary         = "binary_or_unsupported"
	DiffContentTooLarge       = "size_limited"
	DiffContentLinked         = "linked"
	DiffContentMissing        = "unavailable"
	DiffContentComputeLimited = "computation_limited"
)

type DiffItem struct {
	Path               string `json:"path"`
	Staging            string `json:"staging"`
	Worktree           string `json:"worktree"`
	ContentState       string `json:"content_state"`
	Patch              string `json:"patch"`
	PatchBytes         int    `json:"patch_bytes"`
	AddedLines         int    `json:"added_lines"`
	DeletedLines       int    `json:"deleted_lines"`
	Redacted           bool   `json:"redacted"`
	Truncated          bool   `json:"truncated"`
	StatisticsComplete bool   `json:"statistics_complete"`
}

type Diff struct {
	ProtocolVersion       string     `json:"protocol_version"`
	WorkspaceID           string     `json:"workspace_id"`
	Kind                  string     `json:"kind"`
	Available             bool       `json:"available"`
	BaseHead              string     `json:"base_head"`
	NextAfter             string     `json:"next_after"`
	Items                 []DiffItem `json:"items"`
	ReturnedCount         int        `json:"returned_count"`
	OmittedCount          int        `json:"omitted_count"`
	RedactionCount        int        `json:"redaction_count"`
	TotalPatchBytes       int        `json:"total_patch_bytes"`
	Truncated             bool       `json:"truncated"`
	ReadOnly              bool       `json:"read_only"`
	InstructionAuthorized bool       `json:"instruction_authorized"`
	MutationSupported     bool       `json:"mutation_supported"`
	AuthorityGranted      bool       `json:"authority_granted"`
	RootPathExposed       bool       `json:"root_path_exposed"`
	RawContentIncluded    bool       `json:"raw_content_included"`
	PatchContentIncluded  bool       `json:"patch_content_included"`
	RemoteConfigIncluded  bool       `json:"remote_config_included"`
	ProcessStarted        bool       `json:"process_started"`
	NetworkUsed           bool       `json:"network_used"`
	HooksExecuted         bool       `json:"hooks_executed"`
}

type DiffOptions struct{ After string }

func InspectDiff(ctx context.Context, root string, workspaceID string, options ...DiffOptions) (Diff, error) {
	after := ""
	if len(options) > 0 {
		after = options[0].After
	}
	if after != "" {
		canonical, _, ok := safePath(after)
		if !ok || canonical != after {
			return Diff{}, apperror.New(apperror.CodeInvalidArgument, "invalid diff page path")
		}
	}
	base := Diff{ProtocolVersion: DiffProtocolVersion, WorkspaceID: workspaceID,
		Kind: "none", Items: []DiffItem{}, ReadOnly: true}
	state, err := inspectChanges(ctx, root, workspaceID, MaxStatusEntries)
	if err != nil {
		return Diff{}, err
	}
	base.Kind = state.Kind
	base.Available = state.Available
	base.BaseHead = state.Head
	base.RedactionCount = state.RedactionCount
	base.Truncated = state.Truncated
	base.PatchContentIncluded = state.Available
	if !state.Available {
		return base, nil
	}
	resolvedRoot, err := resolveRoot(root)
	if err != nil {
		return Diff{}, err
	}
	repo, err := git.PlainOpenWithOptions(resolvedRoot,
		&git.PlainOpenOptions{DetectDotGit: false})
	if err != nil {
		return Diff{}, apperror.New(apperror.CodeFailedPrecondition,
			"repository diff metadata could not be opened")
	}
	headTree, err := repositoryHeadTree(repo)
	if err != nil {
		return Diff{}, err
	}

	candidates := state.Changes
	for len(candidates) > 0 && candidates[0].Path <= after {
		candidates = candidates[1:]
	}
	// Work is bounded per page, independently of preview bytes. The next page
	// starts after the last returned path, so budget exhaustion loses no files.
	pageContext, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for index, change := range candidates {
		if err := ctx.Err(); err != nil {
			return Diff{}, err
		}
		if len(base.Items) >= MaxDiffItems || (len(base.Items) > 0 &&
			(pageContext.Err() != nil || base.TotalPatchBytes+MaxDiffPatchBytes > MaxDiffTotalBytes)) {
			base.OmittedCount = len(candidates) - index
			base.NextAfter = base.Items[len(base.Items)-1].Path
			break
		}
		item, redactions, err := buildDiffItem(pageContext, headTree, resolvedRoot,
			workspaceID, change)
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			item = DiffItem{Path: change.Path, Staging: change.Staging, Worktree: change.Worktree,
				ContentState: DiffContentComputeLimited}
			err = nil
		}
		if err != nil {
			return Diff{}, err
		}
		if item.ContentState == DiffContentComputeLimited && pageContext.Err() != nil && len(base.Items) > 0 {
			base.OmittedCount = len(candidates) - index
			base.NextAfter = base.Items[len(base.Items)-1].Path
			break
		}
		base.RedactionCount += redactions
		base.TotalPatchBytes += item.PatchBytes
		base.Truncated = base.Truncated || item.Truncated
		base.Items = append(base.Items, item)
	}
	base.ReturnedCount = len(base.Items)
	return base, nil
}

func repositoryHeadTree(repo *git.Repository) (*object.Tree, error) {
	head, err := repo.Head()
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"repository diff HEAD could not be inspected")
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"repository diff base commit could not be inspected")
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"repository diff base tree could not be inspected")
	}
	return tree, nil
}

func buildDiffItem(ctx context.Context, tree *object.Tree, root string,
	workspaceID string, change Change,
) (DiffItem, int, error) {
	item := DiffItem{Path: change.Path, Staging: change.Staging,
		Worktree: change.Worktree, ContentState: DiffContentText}
	oldText, oldState, oldRedactions, oldRedacted, err := readHeadText(ctx, tree, change.Path)
	if err != nil {
		return DiffItem{}, 0, err
	}
	newText, newState, newRedactions, newRedacted := readWorkspaceText(root,
		workspaceID, change.Path)
	item.Redacted = oldRedacted || newRedacted
	redactions := oldRedactions + newRedactions
	item.ContentState = combinedContentState(oldState, newState)
	if item.ContentState != DiffContentText {
		return item, redactions, nil
	}
	edits, complete := lineEdits(ctx, oldText, newText)
	if !complete {
		item.ContentState = DiffContentComputeLimited
		return item, redactions, nil
	}
	item.StatisticsComplete = !item.Redacted
	item.Patch, item.AddedLines, item.DeletedLines, item.Truncated = renderLinePatch(change.Path, edits, MaxDiffPatchBytes)
	if item.Redacted && oldText == newText {
		item.Patch = fmt.Sprintf("--- a/%s\n+++ b/%s\n@@ -1,1 +1,1 @@\n-[sensitive change omitted]\n+[sensitive change omitted]\n",
			change.Path, change.Path)
	}
	item.PatchBytes = len([]byte(item.Patch))
	return item, redactions, nil
}

func readHeadText(ctx context.Context, tree *object.Tree,
	path string,
) (string, string, int, bool, error) {
	if tree == nil {
		return "", "missing", 0, false, nil
	}
	if err := ctx.Err(); err != nil {
		return "", "", 0, false, err
	}
	file, err := tree.File(path)
	if errors.Is(err, object.ErrFileNotFound) {
		return "", "missing", 0, false, nil
	}
	if err != nil {
		return "", "", 0, false, apperror.New(apperror.CodeFailedPrecondition,
			"repository diff base file could not be inspected")
	}
	if !file.Mode.IsRegular() {
		return "", DiffContentLinked, 0, false, nil
	}
	if file.Size > MaxDiffFileBytes {
		return "", DiffContentTooLarge, 0, false, nil
	}
	reader, err := file.Reader()
	if err != nil {
		return "", "", 0, false, apperror.New(apperror.CodeFailedPrecondition,
			"repository diff base file could not be opened")
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, MaxDiffFileBytes+1))
	if err != nil {
		return "", "", 0, false, apperror.New(apperror.CodeFailedPrecondition,
			"repository diff base file could not be read")
	}
	if len(data) > MaxDiffFileBytes {
		return "", DiffContentTooLarge, 0, false, nil
	}
	if !safeDiffText(data) {
		return "", DiffContentBinary, 0, false, nil
	}
	projection := redact.Text(string(data))
	return projection.Text, "present", diffFindingCount(projection.Findings),
		projection.Text != string(data), nil
}

func readWorkspaceText(root string, workspaceID string,
	path string,
) (string, string, int, bool) {
	data, tooLarge, err := workspace.ReadReviewFile(root, path, MaxDiffFileBytes)
	if err != nil {
		switch apperror.CodeOf(apperror.Normalize(err)) {
		case apperror.CodeNotFound:
			return "", "missing", 0, false
		case apperror.CodePolicyDenied:
			return "", DiffContentLinked, 0, false
		default:
			return "", DiffContentBinary, 0, false
		}
	}
	if tooLarge {
		return "", DiffContentTooLarge, 0, false
	}
	if !safeDiffText(data) {
		return "", DiffContentBinary, 0, false
	}
	projection := redact.Text(string(data))
	return projection.Text, "present", diffFindingCount(projection.Findings), projection.Text != string(data)
}

func combinedContentState(oldState string, newState string) string {
	for _, state := range []string{oldState, newState} {
		if state == DiffContentLinked {
			return DiffContentLinked
		}
	}
	for _, state := range []string{oldState, newState} {
		if state == DiffContentTooLarge {
			return DiffContentTooLarge
		}
	}
	for _, state := range []string{oldState, newState} {
		if state == DiffContentBinary {
			return DiffContentBinary
		}
	}
	if oldState == "missing" && newState == "missing" {
		return DiffContentMissing
	}
	return DiffContentText
}

func safeDiffText(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	for _, current := range string(data) {
		if current == 0 || (unicode.IsControl(current) && current != '\n' &&
			current != '\r' && current != '\t') {
			return false
		}
	}
	return true
}

func boundPatch(value string, limit int) (string, bool) {
	if len([]byte(value)) <= limit {
		return value, false
	}
	const marker = "@@ diff truncated by repository_diff.v1 @@\n"
	if limit <= len(marker) {
		return marker[:limit], true
	}
	data := []byte(value)
	data = data[:limit-len(marker)]
	for !utf8.Valid(data) {
		data = data[:len(data)-1]
	}
	if last := strings.LastIndexByte(string(data), '\n'); last >= 0 {
		data = data[:last+1]
	}
	return string(data) + marker, true
}

func diffFindingCount(values []redact.Finding) int {
	total := 0
	for _, value := range values {
		total += value.Count
	}
	return total
}
