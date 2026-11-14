package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"

	"cyberagent-workbench/internal/gitadvanced"
)

// CaptureReviewDiffEvidence renders the exact committed merge-base diff used
// by a GitHub pull request. It is read-only and retains #117 repository,
// conflict, hunk, and dirty-worktree bindings so later remote or local drift
// invalidates review mappings.
func (e *AdvancedExecutor) CaptureReviewDiffEvidence(ctx context.Context, root,
	baseSHA, headSHA string,
) (gitadvanced.ReviewDiffEvidence, error) {
	if e == nil || !e.Available() || !gitadvanced.ValidObjectID(baseSHA) ||
		!gitadvanced.ValidObjectID(headSHA) {
		return gitadvanced.ReviewDiffEvidence{}, errors.New("Git review diff request is invalid")
	}
	binding, err := e.CaptureAdvancedBinding(ctx, root)
	if err != nil {
		return gitadvanced.ReviewDiffEvidence{}, err
	}
	if binding.Head != headSHA {
		return gitadvanced.ReviewDiffEvidence{}, &gitadvanced.Error{
			Code:    gitadvanced.FailureStalePreview,
			Message: "local HEAD does not match the GitHub pull request head"}
	}
	mergeBase, stderr, code, err := e.git(ctx, root, nil, "merge-base", baseSHA, headSHA)
	mergeBase = strings.TrimSpace(mergeBase)
	if err != nil || code != 0 || !gitadvanced.ValidObjectID(mergeBase) {
		return gitadvanced.ReviewDiffEvidence{}, errors.New("resolve GitHub review merge-base: " +
			strings.TrimSpace(stderr))
	}
	rawDiff, stderr, code, err := e.git(ctx, root, nil, "diff", "--full-index", "--binary",
		"--no-ext-diff", "--find-renames", mergeBase+"..."+headSHA, "--")
	if err != nil || code != 0 {
		return gitadvanced.ReviewDiffEvidence{}, errors.New("render GitHub review diff: " +
			strings.TrimSpace(stderr))
	}
	callChain, stderr, code, err := e.git(ctx, root, nil, "diff", "--full-index",
		"--function-context", "--no-ext-diff", "--find-renames",
		mergeBase+"..."+headSHA, "--")
	if err != nil || code != 0 {
		return gitadvanced.ReviewDiffEvidence{}, errors.New("render GitHub review call-chain diff: " +
			strings.TrimSpace(stderr))
	}
	nameStatus, stderr, code, err := e.git(ctx, root, nil, "diff", "--name-status", "-z",
		"--find-renames", mergeBase+"..."+headSHA, "--")
	if err != nil || code != 0 {
		return gitadvanced.ReviewDiffEvidence{}, errors.New("render GitHub review path diff: " +
			strings.TrimSpace(stderr))
	}
	changedFiles, err := parseBatchChangedFiles([]byte(nameStatus), gitadvanced.MaxReviewChangedFiles)
	if err != nil {
		return gitadvanced.ReviewDiffEvidence{}, err
	}
	stat, stderr, code, err := e.git(ctx, root, nil, "diff", "--shortstat",
		mergeBase+"..."+headSHA, "--")
	if err != nil || code != 0 {
		return gitadvanced.ReviewDiffEvidence{}, errors.New("render GitHub review diffstat: " +
			strings.TrimSpace(stderr))
	}
	conflict, err := e.captureConflictState(ctx, root)
	if err != nil {
		return gitadvanced.ReviewDiffEvidence{}, err
	}
	diffSum := sha256.Sum256([]byte(rawDiff))
	callSum := sha256.Sum256([]byte(callChain))
	evidence := gitadvanced.ReviewDiffEvidence{
		ProtocolVersion: gitadvanced.ReviewDiffProtocolVersion,
		Binding:         binding, BaseSHA: strings.ToLower(baseSHA), HeadSHA: strings.ToLower(headSHA),
		MergeBaseSHA:    strings.ToLower(mergeBase),
		DiffSHA256:      hex.EncodeToString(diffSum[:]),
		CallChainSHA256: hex.EncodeToString(callSum[:]), DiffBytes: int64(len(rawDiff)),
		DiffStat: boundedOutput(strings.TrimSpace(stat)), ChangedFiles: changedFiles,
		Hunks: []gitadvanced.Hunk{}, Conflict: conflict, Complete: true,
		Omissions: []string{}, CapturedAt: e.now().UTC()}
	if len(rawDiff) > gitadvanced.MaxPreviewPatchBytes || unsupportedGitDiff(rawDiff) {
		evidence.Complete = false
		evidence.Omissions = append(evidence.Omissions,
			"stable textual hunks omitted for binary or oversized review diff")
	} else {
		hunkDiff, hunkStderr, hunkCode, hunkErr := e.git(ctx, root, nil, "diff",
			"--no-ext-diff", "--no-renames", "--full-index", "--unified=3",
			"--src-prefix=a/", "--dst-prefix=b/", mergeBase+"..."+headSHA, "--")
		if hunkErr != nil || hunkCode != 0 {
			return gitadvanced.ReviewDiffEvidence{}, errors.New(
				"render stable GitHub review hunks: " + strings.TrimSpace(hunkStderr))
		}
		parsed, parseErr := parseAdvancedUnifiedDiff(hunkDiff)
		if parseErr != nil {
			evidence.Complete = false
			evidence.Omissions = append(evidence.Omissions,
				"stable textual hunks could not be parsed")
		} else {
			evidence.Hunks, err = e.reviewHunks(ctx, root, mergeBase, headSHA, parsed)
			if err != nil {
				return gitadvanced.ReviewDiffEvidence{}, err
			}
		}
	}
	sort.Strings(evidence.ChangedFiles)
	sort.Strings(evidence.Omissions)
	if err := evidence.Validate(); err != nil {
		return gitadvanced.ReviewDiffEvidence{}, err
	}
	return evidence, nil
}

func (e *AdvancedExecutor) reviewHunks(ctx context.Context, root, mergeBase, headSHA string,
	parsed []parsedAdvancedHunk,
) ([]gitadvanced.Hunk, error) {
	if len(parsed) > gitadvanced.MaxHunks {
		return nil, errors.New("GitHub review hunks exceed the stable hunk bound")
	}
	result := make([]gitadvanced.Hunk, 0, len(parsed))
	for _, item := range parsed {
		baseMode, baseBlob, _, err := e.readGitTreeFile(ctx, root, mergeBase, item.path)
		if err != nil {
			return nil, err
		}
		headMode, headBlob, _, err := e.readGitTreeFile(ctx, root, headSHA, item.path)
		if err != nil {
			return nil, err
		}
		for _, mode := range []string{baseMode, headMode} {
			if mode == "120000" || mode == "160000" {
				return nil, errors.New("symlink and submodule review hunks are not supported")
			}
			if mode != "" && mode != "100644" && mode != "100755" {
				return nil, errors.New("GitHub review hunk has an unsupported file mode")
			}
		}
		identity, err := e.captureAdvancedFileIdentity(ctx, root, item.path)
		if err != nil {
			return nil, err
		}
		contextLines := make([]string, 0)
		for _, line := range strings.Split(item.body, "\n") {
			if strings.HasPrefix(line, " ") {
				contextLines = append(contextLines, line)
			}
		}
		patch := item.header + item.body
		patchSum := sha256.Sum256([]byte(patch))
		contextDigest := gitadvanced.Fingerprint("review-hunk-context", item.path,
			strings.Join(contextLines, "\n"))
		id := gitadvanced.Fingerprint("github-review-hunk", mergeBase, headSHA,
			item.path, baseBlob, headBlob, identity.worktreeSHA256,
			contextDigest, hex.EncodeToString(patchSum[:]))
		result = append(result, gitadvanced.Hunk{ID: id, Path: item.path,
			OldStart: item.oldStart, OldLines: item.oldLines,
			NewStart: item.newStart, NewLines: item.newLines,
			BaseBlob: baseBlob, IndexBlob: headBlob,
			WorktreeSHA256: identity.worktreeSHA256,
			ContextSHA256:  contextDigest, PatchSHA256: hex.EncodeToString(patchSum[:]),
			Patch: patch})
	}
	return result, nil
}
