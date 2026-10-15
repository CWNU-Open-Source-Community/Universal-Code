package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"cyberagent-workbench/internal/gitadvanced"
)

type advancedStash struct {
	oid      string
	selector string
	subject  string
	parents  string
}

func (e *AdvancedExecutor) populateAdvancedPreview(ctx context.Context, root string,
	preview *gitadvanced.Preview,
) error {
	spec := preview.Spec
	switch spec.Operation {
	case gitadvanced.HunkStage, gitadvanced.HunkUnstage, gitadvanced.HunkRevert:
		hunks, files, err := e.previewHunks(ctx, root, spec)
		if err != nil {
			return err
		}
		preview.Hunks, preview.Files = hunks, files
		preview.Target = fmt.Sprintf("%d exact content-addressed hunk(s)", len(hunks))
		preview.Summary = fmt.Sprintf("%s will update %d file(s)", spec.Operation, len(files))
		if len(hunks) == 0 {
			preview.BlockedReasons = append(preview.BlockedReasons,
				"no matching textual hunks are available")
		}
	case gitadvanced.StashCreate:
		files, err := e.changedFileImpacts(ctx, root, spec.IncludeUntracked)
		if err != nil {
			return err
		}
		preview.Files = files
		preview.Target = "new refs/stash entry"
		preview.Summary = fmt.Sprintf("stash %d changed file(s); ignored files are always excluded", len(files))
		if len(files) == 0 {
			preview.BlockedReasons = append(preview.BlockedReasons, "repository has no selected changes to stash")
		}
	case gitadvanced.StashApply, gitadvanced.StashPop, gitadvanced.StashDrop:
		stash, err := e.requireStash(ctx, root, spec.StashOID)
		if err != nil {
			return err
		}
		preview.Target = stash.oid
		preview.Files, err = e.stashFileImpacts(ctx, root, stash)
		if err != nil {
			return err
		}
		preview.Summary = fmt.Sprintf("%s exact stash %s affecting %d file(s)",
			spec.Operation, shortOID(stash.oid), len(preview.Files))
		if spec.Operation != gitadvanced.StashDrop {
			reason, collisionErr := e.ignoredCandidateCollision(ctx, root,
				fileImpactPaths(preview.Files))
			if collisionErr != nil {
				return collisionErr
			}
			if reason != "" {
				preview.BlockedReasons = append(preview.BlockedReasons, reason)
			}
		}
	case gitadvanced.RebaseStart:
		if err := e.requireCommit(ctx, root, spec.UpstreamOID); err != nil {
			return err
		}
		if err := e.requireCommit(ctx, root, spec.OntoOID); err != nil {
			return err
		}
		preview.Target = spec.OntoOID
		preview.Summary = "rebase local-only branch commits onto exact target"
		if preview.Binding.Head == "unborn" {
			preview.BlockedReasons = append(preview.BlockedReasons, "unborn HEAD cannot be rebased")
		}
		if clean, err := e.worktreeClean(ctx, root); err != nil {
			return err
		} else if !clean {
			preview.BlockedReasons = append(preview.BlockedReasons, "rebase start requires a clean index and worktree")
		}
		_, _, code, err := e.git(ctx, root, nil, "merge-base", "--is-ancestor",
			spec.UpstreamOID, preview.Binding.Head)
		if err != nil || code != 0 {
			preview.BlockedReasons = append(preview.BlockedReasons,
				"rebase upstream must be an ancestor of the reviewed HEAD")
		}
		paths, pathErr := e.rebaseCandidatePaths(ctx, root, preview.Binding.Head,
			spec.UpstreamOID, spec.OntoOID)
		if pathErr != nil {
			return pathErr
		}
		preview.Files = advancedSequenceFileImpacts(paths, "rebase may check out path")
		if reason, collisionErr := e.ignoredCandidateCollision(ctx, root, paths); collisionErr != nil {
			return collisionErr
		} else if reason != "" {
			preview.BlockedReasons = append(preview.BlockedReasons, reason)
		}
	case gitadvanced.CherryPickStart:
		for _, oid := range spec.Commits {
			if err := e.requireSingleParentCommit(ctx, root, oid); err != nil {
				return err
			}
		}
		preview.Target = strings.Join(spec.Commits, ",")
		preview.Summary = fmt.Sprintf("cherry-pick %d exact non-merge commit(s)", len(spec.Commits))
		if clean, err := e.worktreeClean(ctx, root); err != nil {
			return err
		} else if !clean {
			preview.BlockedReasons = append(preview.BlockedReasons,
				"cherry-pick start requires a clean index and worktree")
		}
		paths, pathErr := e.cherryPickCandidatePaths(ctx, root, spec.Commits)
		if pathErr != nil {
			return pathErr
		}
		preview.Files = advancedSequenceFileImpacts(paths, "cherry-pick may update path")
		if reason, collisionErr := e.ignoredCandidateCollision(ctx, root, paths); collisionErr != nil {
			return collisionErr
		} else if reason != "" {
			preview.BlockedReasons = append(preview.BlockedReasons, reason)
		}
	case gitadvanced.RebaseContinue, gitadvanced.RebaseSkip, gitadvanced.RebaseAbort:
		if err := e.previewSequenceControl(ctx, root, preview, gitadvanced.SequenceRebase); err != nil {
			return err
		}
		return e.blockIgnoredHistoryCollision(ctx, root, preview)
	case gitadvanced.CherryPickContinue, gitadvanced.CherryPickSkip, gitadvanced.CherryPickAbort:
		if err := e.previewSequenceControl(ctx, root, preview, gitadvanced.SequenceCherryPick); err != nil {
			return err
		}
		return e.blockIgnoredHistoryCollision(ctx, root, preview)
	case gitadvanced.BisectStart:
		if err := e.requireCommit(ctx, root, spec.GoodCommit); err != nil {
			return err
		}
		if err := e.requireCommit(ctx, root, spec.BadCommit); err != nil {
			return err
		}
		preview.Target = spec.GoodCommit + ".." + spec.BadCommit
		preview.Summary = "start bounded bisect at exact good and bad commits"
		if clean, err := e.worktreeClean(ctx, root); err != nil {
			return err
		} else if !clean {
			preview.BlockedReasons = append(preview.BlockedReasons,
				"bisect start requires a clean index and worktree")
		}
		paths, pathErr := e.bisectCandidatePaths(ctx, root, preview.Binding.Head,
			spec.GoodCommit, spec.BadCommit)
		if pathErr != nil {
			return pathErr
		}
		preview.Files = advancedSequenceFileImpacts(paths, "bisect may check out path")
		if reason, collisionErr := e.ignoredCandidateCollision(ctx, root, paths); collisionErr != nil {
			return collisionErr
		} else if reason != "" {
			preview.BlockedReasons = append(preview.BlockedReasons, reason)
		}
	case gitadvanced.BisectGood, gitadvanced.BisectBad, gitadvanced.BisectSkip,
		gitadvanced.BisectRun, gitadvanced.BisectReset:
		kind, active, err := e.sequenceKind(ctx, root)
		if err != nil {
			return err
		}
		if !active || kind != gitadvanced.SequenceBisect {
			preview.BlockedReasons = append(preview.BlockedReasons, "no matching bisect sequence is active")
		}
		preview.Target = preview.Binding.Head
		preview.Summary = string(spec.Operation) + " for the active bounded bisect"
		if spec.ExpectedCurrent != "" && preview.Binding.Head != spec.ExpectedCurrent {
			preview.BlockedReasons = append(preview.BlockedReasons,
				"bisect current commit drifted from the reviewed recipe step")
		}
		if err := e.blockIgnoredHistoryCollision(ctx, root, preview); err != nil {
			return err
		}
	case gitadvanced.WorktreeCreate, gitadvanced.WorktreeLock,
		gitadvanced.WorktreeUnlock, gitadvanced.WorktreeRemove, gitadvanced.WorktreePrune:
		return e.previewManagedWorktree(ctx, root, preview)
	default:
		return errors.New("unsupported Git advanced operation")
	}
	return nil
}

func (e *AdvancedExecutor) executeAdvancedOperation(ctx context.Context, root string,
	preview gitadvanced.Preview, receipt *gitadvanced.Receipt,
) (string, string, int, error) {
	spec := preview.Spec
	if err := e.revalidateIgnoredMutationFence(ctx, root, preview); err != nil {
		return "", "", 0, err
	}
	switch spec.Operation {
	case gitadvanced.HunkStage, gitadvanced.HunkUnstage, gitadvanced.HunkRevert:
		return e.executeHunks(ctx, root, preview)
	case gitadvanced.StashCreate:
		before, err := e.listStashes(ctx, root)
		if err != nil {
			return "", "", 0, err
		}
		args := []string{"stash", "push", "--quiet", "--message", spec.Message}
		if spec.IncludeUntracked {
			args = append(args, "--include-untracked")
		}
		if spec.KeepIndex {
			args = append(args, "--keep-index")
		}
		args = append(args, "--")
		stdout, stderr, code, runErr := e.git(ctx, root, nil, args...)
		if runErr == nil && code == 0 {
			after, listErr := e.listStashes(context.WithoutCancel(ctx), root)
			if listErr != nil || len(after) == 0 || (len(before) > 0 && after[0].oid == before[0].oid) {
				return stdout, stderr, 1, errors.New("stash create did not produce a new exact stash object")
			}
			receipt.TargetOID = after[0].oid
		}
		return stdout, stderr, code, runErr
	case gitadvanced.StashApply:
		args := []string{"stash", "apply", "--quiet"}
		if spec.RestoreIndex {
			args = append(args, "--index")
		}
		args = append(args, spec.StashOID)
		receipt.TargetOID = spec.StashOID
		return e.git(ctx, root, nil, args...)
	case gitadvanced.StashPop:
		args := []string{"stash", "apply", "--quiet"}
		if spec.RestoreIndex {
			args = append(args, "--index")
		}
		args = append(args, spec.StashOID)
		stdout, stderr, code, runErr := e.git(ctx, root, nil, args...)
		receipt.TargetOID = spec.StashOID
		if runErr != nil || code != 0 {
			return stdout, stderr, code, runErr // stash is intentionally retained.
		}
		stash, resolveErr := e.requireStash(context.WithoutCancel(ctx), root, spec.StashOID)
		if resolveErr != nil {
			return stdout, stderr, 1, errors.New("stash apply succeeded but exact stash could not be retained for safe drop")
		}
		dropOut, dropErr, dropCode, dropRunErr := e.git(context.WithoutCancel(ctx), root,
			nil, "stash", "drop", "--quiet", stash.selector)
		return stdout + dropOut, stderr + dropErr, dropCode, dropRunErr
	case gitadvanced.StashDrop:
		stash, err := e.requireStash(ctx, root, spec.StashOID)
		if err != nil {
			return "", "", 0, err
		}
		receipt.TargetOID = stash.oid
		return e.git(ctx, root, nil, "stash", "drop", "--quiet", stash.selector)
	case gitadvanced.RebaseStart:
		receipt.SequenceID = derivedSequenceID(preview.ID, gitadvanced.SequenceRebase)
		return e.git(ctx, root, nil, "rebase", "--onto", spec.OntoOID,
			spec.UpstreamOID)
	case gitadvanced.RebaseContinue:
		receipt.SequenceID = spec.SequenceID
		return e.git(ctx, root, nil, "rebase", "--continue")
	case gitadvanced.RebaseSkip:
		receipt.SequenceID = spec.SequenceID
		return e.git(ctx, root, nil, "rebase", "--skip")
	case gitadvanced.RebaseAbort:
		receipt.SequenceID = spec.SequenceID
		return e.git(ctx, root, nil, "rebase", "--abort")
	case gitadvanced.CherryPickStart:
		receipt.SequenceID = derivedSequenceID(preview.ID, gitadvanced.SequenceCherryPick)
		return e.git(ctx, root, nil, append([]string{"cherry-pick"}, spec.Commits...)...)
	case gitadvanced.CherryPickContinue:
		receipt.SequenceID = spec.SequenceID
		return e.git(ctx, root, nil, "cherry-pick", "--continue")
	case gitadvanced.CherryPickSkip:
		receipt.SequenceID = spec.SequenceID
		return e.git(ctx, root, nil, "cherry-pick", "--skip")
	case gitadvanced.CherryPickAbort:
		receipt.SequenceID = spec.SequenceID
		return e.git(ctx, root, nil, "cherry-pick", "--abort")
	case gitadvanced.BisectStart:
		receipt.SequenceID = derivedSequenceID(preview.ID, gitadvanced.SequenceBisect)
		return e.git(ctx, root, nil, "bisect", "start", spec.BadCommit, spec.GoodCommit)
	case gitadvanced.BisectGood, gitadvanced.BisectBad, gitadvanced.BisectSkip:
		receipt.SequenceID = spec.SequenceID
		mark := strings.TrimPrefix(string(spec.Operation), "bisect_")
		return e.git(ctx, root, nil, "bisect", mark)
	case gitadvanced.BisectRun:
		receipt.SequenceID = spec.SequenceID
		return e.executeBisectRecipe(ctx, root, spec, receipt)
	case gitadvanced.BisectReset:
		receipt.SequenceID = spec.SequenceID
		return e.git(ctx, root, nil, "bisect", "reset")
	case gitadvanced.WorktreeCreate, gitadvanced.WorktreeLock,
		gitadvanced.WorktreeUnlock, gitadvanced.WorktreeRemove, gitadvanced.WorktreePrune:
		return e.executeManagedWorktree(ctx, root, preview, receipt)
	default:
		return "", "", 0, errors.New("unsupported Git advanced operation")
	}
}

func (e *AdvancedExecutor) changedFileImpacts(ctx context.Context, root string,
	includeUntracked bool,
) ([]gitadvanced.FileImpact, error) {
	paths := make(map[string]map[string]struct{})
	for _, item := range []struct {
		state string
		args  []string
	}{
		{"worktree", []string{"diff", "--name-only", "-z", "--no-renames", "--"}},
		{"index", []string{"diff", "--cached", "--name-only", "-z", "--no-renames", "--"}},
	} {
		stdout, stderr, code, err := e.git(ctx, root, nil, item.args...)
		if err != nil || code != 0 {
			return nil, fmt.Errorf("inspect changed files: %w: %s", err, strings.TrimSpace(stderr))
		}
		for _, path := range strings.Split(stdout, "\x00") {
			if path != "" {
				if paths[path] == nil {
					paths[path] = make(map[string]struct{})
				}
				paths[path][item.state] = struct{}{}
			}
		}
	}
	if includeUntracked {
		stdout, stderr, code, err := e.git(ctx, root, nil, "ls-files", "-z", "--others", "--exclude-standard")
		if err != nil || code != 0 {
			return nil, fmt.Errorf("inspect untracked files: %w: %s", err, strings.TrimSpace(stderr))
		}
		for _, path := range strings.Split(stdout, "\x00") {
			if path != "" {
				paths[path] = map[string]struct{}{"untracked": {}}
			}
		}
	}
	if len(paths) > gitadvanced.MaxPaths {
		return nil, errors.New("stash preview exceeds changed-file bound")
	}
	out := make([]gitadvanced.FileImpact, 0, len(paths))
	for path, states := range paths {
		if !safeGitRelativePath(path) {
			return nil, errors.New("stash preview contains an unsafe path")
		}
		identity, err := e.captureAdvancedFileIdentity(ctx, root, path)
		if err != nil {
			return nil, err
		}
		change := orderedStashStates(states)
		out = append(out, gitadvanced.FileImpact{Path: path,
			BeforeSHA256: identity.baseSHA256, AfterSHA256: identity.worktreeSHA256,
			Change: change, Destructive: true})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// ListAdvancedStashes returns bounded selector-free stash metadata and the
// exact tracked/index/untracked file roles used by preview and Desktop.
func (e *AdvancedExecutor) ListAdvancedStashes(ctx context.Context, root string,
	limit int,
) ([]gitadvanced.StashEntry, error) {
	if !e.Available() || limit < 1 || limit > 100 {
		return nil, errors.New("Git stash observation limit is invalid")
	}
	stashes, err := e.listStashes(ctx, root)
	if err != nil {
		return nil, err
	}
	if len(stashes) > limit {
		stashes = stashes[:limit]
	}
	out := make([]gitadvanced.StashEntry, 0, len(stashes))
	for _, stash := range stashes {
		parents := strings.Fields(stash.parents)
		if len(parents) < 2 || len(parents) > 3 {
			return nil, errors.New("Git stash has an unsupported parent structure")
		}
		for _, oid := range parents {
			if !gitadvanced.ValidObjectID(oid) {
				return nil, errors.New("Git stash parent identity is invalid")
			}
		}
		files, err := e.stashFileImpacts(ctx, root, stash)
		if err != nil {
			return nil, err
		}
		entry := gitadvanced.StashEntry{OID: stash.oid, BaseCommit: parents[0],
			IndexCommit: parents[1], Subject: stash.subject, Files: files}
		if len(parents) == 3 {
			entry.UntrackedCommit = parents[2]
		}
		out = append(out, entry)
	}
	return out, nil
}

func (e *AdvancedExecutor) listStashes(ctx context.Context, root string) ([]advancedStash, error) {
	stdout, stderr, code, err := e.git(ctx, root, nil, "stash", "list",
		"--format=%H%x00%gd%x00%gs%x00%P%x00")
	if err != nil || code != 0 {
		return nil, fmt.Errorf("list Git stashes: %w: %s", err, strings.TrimSpace(stderr))
	}
	parts := strings.Split(stdout, "\x00")
	var out []advancedStash
	for index := 0; index+3 < len(parts); index += 4 {
		oid, selector, subject, parents := strings.TrimSpace(parts[index]),
			strings.TrimSpace(parts[index+1]), parts[index+2], strings.TrimSpace(parts[index+3])
		if oid == "" && selector == "" {
			continue
		}
		if !gitadvanced.ValidObjectID(oid) || !safeStashSelector(selector) {
			return nil, errors.New("Git stash stack returned an unsafe identity")
		}
		out = append(out, advancedStash{oid: oid, selector: selector,
			subject: subject, parents: parents})
		if len(out) > 1000 {
			return nil, errors.New("Git stash stack exceeds safe list bound")
		}
	}
	return out, nil
}

func (e *AdvancedExecutor) requireStash(ctx context.Context, root,
	oid string,
) (advancedStash, error) {
	if !gitadvanced.ValidObjectID(oid) {
		return advancedStash{}, errors.New("stash requires an exact object id")
	}
	stashes, err := e.listStashes(ctx, root)
	if err != nil {
		return advancedStash{}, err
	}
	for _, stash := range stashes {
		if stash.oid == oid {
			return stash, nil
		}
	}
	return advancedStash{}, &gitadvanced.Error{Code: gitadvanced.FailureRepositoryDrift,
		Message: "exact stash object is no longer present in refs/stash"}
}

func safeStashSelector(value string) bool {
	if !strings.HasPrefix(value, "stash@{") || !strings.HasSuffix(value, "}") {
		return false
	}
	index := strings.TrimSuffix(strings.TrimPrefix(value, "stash@{"), "}")
	if index == "" {
		return false
	}
	for _, r := range index {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (e *AdvancedExecutor) stashFileImpacts(ctx context.Context, root string,
	stash advancedStash,
) ([]gitadvanced.FileImpact, error) {
	stdout, stderr, code, err := e.git(ctx, root, nil, "stash", "show",
		"--name-only", "-z", "--include-untracked", stash.oid)
	if err != nil || code != 0 {
		return nil, fmt.Errorf("show exact Git stash: %w: %s", err, strings.TrimSpace(stderr))
	}
	paths := strings.Split(stdout, "\x00")
	if len(paths) > gitadvanced.MaxPaths+1 {
		return nil, errors.New("stash preview exceeds changed-file bound")
	}
	parents := strings.Fields(stash.parents)
	if len(parents) < 2 || len(parents) > 3 {
		return nil, errors.New("Git stash has an unsupported parent structure")
	}
	states := make(map[string]map[string]struct{})
	if err := e.addStashDiffPaths(ctx, root, parents[0], parents[1], "index", states); err != nil {
		return nil, err
	}
	if err := e.addStashDiffPaths(ctx, root, parents[1], stash.oid, "worktree", states); err != nil {
		return nil, err
	}
	if len(parents) == 3 {
		untracked, listErr := e.gitTreePaths(ctx, root, parents[2])
		if listErr != nil {
			return nil, listErr
		}
		for _, path := range untracked {
			states[path] = map[string]struct{}{"untracked": {}}
		}
	}
	var out []gitadvanced.FileImpact
	for _, path := range paths {
		if path == "" {
			continue
		}
		if !safeGitRelativePath(path) {
			return nil, errors.New("stash contains an unsafe path")
		}
		pathStates := states[path]
		before, hashErr := e.treePathSHA256(ctx, root, parents[0], path)
		if hashErr != nil {
			return nil, hashErr
		}
		afterTree := stash.oid
		if _, untracked := pathStates["untracked"]; untracked {
			afterTree = parents[2]
		} else if _, worktree := pathStates["worktree"]; !worktree {
			afterTree = parents[1]
		}
		after, hashErr := e.treePathSHA256(ctx, root, afterTree, path)
		if hashErr != nil {
			return nil, hashErr
		}
		out = append(out, gitadvanced.FileImpact{Path: path,
			BeforeSHA256: before, AfterSHA256: after,
			Change: orderedStashStates(pathStates), Destructive: true})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func orderedStashStates(states map[string]struct{}) string {
	ordered := make([]string, 0, 3)
	for _, state := range []string{"index", "worktree", "untracked"} {
		if _, ok := states[state]; ok {
			ordered = append(ordered, state)
		}
	}
	return strings.Join(ordered, "+")
}

func (e *AdvancedExecutor) addStashDiffPaths(ctx context.Context, root, before,
	after, state string, target map[string]map[string]struct{},
) error {
	stdout, stderr, code, err := e.git(ctx, root, nil, "diff", "--name-only", "-z",
		"--no-renames", before, after, "--")
	if err != nil || code != 0 {
		return fmt.Errorf("inspect stash %s paths: %w: %s", state, err,
			strings.TrimSpace(stderr))
	}
	for _, path := range strings.Split(stdout, "\x00") {
		if path == "" {
			continue
		}
		if !safeGitRelativePath(path) {
			return errors.New("stash contains an unsafe path")
		}
		if target[path] == nil {
			target[path] = make(map[string]struct{})
		}
		target[path][state] = struct{}{}
	}
	return nil
}

func (e *AdvancedExecutor) gitTreePaths(ctx context.Context, root,
	tree string,
) ([]string, error) {
	stdout, stderr, code, err := e.git(ctx, root, nil, "ls-tree", "-r", "--name-only",
		"-z", tree, "--")
	if err != nil || code != 0 {
		return nil, fmt.Errorf("inspect stash untracked tree: %w: %s", err,
			strings.TrimSpace(stderr))
	}
	paths := strings.Split(stdout, "\x00")
	if len(paths) > gitadvanced.MaxPaths+1 {
		return nil, errors.New("stash untracked tree exceeds changed-file bound")
	}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		if !safeGitRelativePath(path) {
			return nil, errors.New("stash contains an unsafe path")
		}
		out = append(out, path)
	}
	return out, nil
}

func (e *AdvancedExecutor) treePathSHA256(ctx context.Context, root, tree,
	path string,
) (string, error) {
	stdout, stderr, code, err := e.git(ctx, root, nil, "ls-tree", "-z", tree, "--", path)
	if err != nil || code != 0 {
		return "", fmt.Errorf("inspect stash tree path: %w: %s", err,
			strings.TrimSpace(stderr))
	}
	record := strings.TrimSuffix(stdout, "\x00")
	if record == "" {
		return "", nil
	}
	metadata, observedPath, ok := strings.Cut(record, "\t")
	fields := strings.Fields(metadata)
	if !ok || observedPath != path || len(fields) != 3 || fields[1] != "blob" ||
		(fields[0] != "100644" && fields[0] != "100755") ||
		!gitadvanced.ValidObjectID(fields[2]) {
		return "", &gitadvanced.Error{Code: gitadvanced.FailureUnsafeRepository,
			Message: "stash contains a symlink, submodule, or unsupported tree entry"}
	}
	content, stderr, code, err := e.git(ctx, root, nil, "cat-file", "blob", fields[2])
	if err != nil || code != 0 {
		return "", fmt.Errorf("read stash blob: %w: %s", err, strings.TrimSpace(stderr))
	}
	return gitadvanced.Fingerprint("stash-content", content), nil
}

func (e *AdvancedExecutor) requireCommit(ctx context.Context, root,
	oid string,
) error {
	if !gitadvanced.ValidObjectID(oid) {
		return errors.New("commit identity is not exact")
	}
	stdout, stderr, code, err := e.git(ctx, root, nil, "cat-file", "-t", oid)
	if err != nil || code != 0 || strings.TrimSpace(stdout) != "commit" {
		return fmt.Errorf("exact object is not an available commit: %w: %s", err,
			strings.TrimSpace(stderr))
	}
	return nil
}

func (e *AdvancedExecutor) requireSingleParentCommit(ctx context.Context,
	root, oid string,
) error {
	if err := e.requireCommit(ctx, root, oid); err != nil {
		return err
	}
	stdout, stderr, code, err := e.git(ctx, root, nil, "rev-list", "--parents", "-n", "1", oid)
	if err != nil || code != 0 {
		return fmt.Errorf("inspect cherry-pick commit: %w: %s", err, strings.TrimSpace(stderr))
	}
	if fields := strings.Fields(stdout); len(fields) > 2 {
		return errors.New("merge commits require a mainline choice and are not supported")
	}
	return nil
}

func (e *AdvancedExecutor) worktreeClean(ctx context.Context, root string) (bool, error) {
	stdout, stderr, code, err := e.git(ctx, root, nil, "status", "--porcelain=v1", "-z")
	if err != nil || code != 0 {
		return false, fmt.Errorf("inspect clean worktree: %w: %s", err, strings.TrimSpace(stderr))
	}
	return stdout == "", nil
}

func (e *AdvancedExecutor) worktreeRemovalClean(ctx context.Context, root string) (bool, error) {
	stdout, stderr, code, err := e.git(ctx, root, nil, "status", "--porcelain=v1", "-z",
		"--untracked-files=all", "--ignored=matching")
	if err != nil || code != 0 {
		return false, fmt.Errorf("inspect removable worktree: %w: %s", err,
			strings.TrimSpace(stderr))
	}
	return stdout == "", nil
}

func fileImpactPaths(values []gitadvanced.FileImpact) []string {
	paths := make([]string, 0, len(values))
	for _, value := range values {
		paths = append(paths, value.Path)
	}
	return paths
}

func advancedSequenceFileImpacts(paths []string, change string) []gitadvanced.FileImpact {
	values := make([]gitadvanced.FileImpact, 0, len(paths))
	for _, path := range paths {
		values = append(values, gitadvanced.FileImpact{Path: path, Change: change, Destructive: true})
	}
	return values
}

func (e *AdvancedExecutor) rebaseCandidatePaths(ctx context.Context, root, head,
	upstream, onto string,
) ([]string, error) {
	paths, err := e.advancedChangedPaths(ctx, root, "log", "--format=", "--name-only", "-z",
		"--no-renames", upstream+".."+head, "--")
	if err != nil {
		return nil, err
	}
	checkout, err := e.advancedChangedPaths(ctx, root, "diff", "--name-only", "-z",
		"--no-renames", head, onto, "--")
	return mergeAdvancedPaths(paths, checkout), err
}

func (e *AdvancedExecutor) cherryPickCandidatePaths(ctx context.Context, root string,
	commits []string,
) ([]string, error) {
	paths := []string{}
	for _, commit := range commits {
		changed, err := e.advancedChangedPaths(ctx, root, "show", "--format=", "--name-only",
			"-z", "--no-renames", commit, "--")
		if err != nil {
			return nil, err
		}
		paths = mergeAdvancedPaths(paths, changed)
		if len(paths) > gitadvanced.MaxPaths {
			return nil, errors.New("cherry-pick impact exceeds the path bound")
		}
	}
	return paths, nil
}

func (e *AdvancedExecutor) bisectCandidatePaths(ctx context.Context, root, head,
	good, bad string,
) ([]string, error) {
	paths, err := e.advancedChangedPaths(ctx, root, "log", "--format=", "--name-only", "-z",
		"--no-renames", good+".."+bad, "--")
	if err != nil {
		return nil, err
	}
	checkout, err := e.advancedChangedPaths(ctx, root, "diff", "--name-only", "-z",
		"--no-renames", head, bad, "--")
	return mergeAdvancedPaths(paths, checkout), err
}

func (e *AdvancedExecutor) advancedChangedPaths(ctx context.Context, root string,
	args ...string,
) ([]string, error) {
	stdout, stderr, code, err := e.git(ctx, root, nil, args...)
	if err != nil || code != 0 {
		return nil, fmt.Errorf("inspect Git sequence impact: %w: %s", err,
			strings.TrimSpace(stderr))
	}
	paths := []string{}
	seen := make(map[string]struct{})
	for _, path := range strings.Split(stdout, "\x00") {
		if path == "" {
			continue
		}
		if !safeGitRelativePath(path) {
			return nil, errors.New("Git sequence impact contains an unsafe path")
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
		if len(paths) > gitadvanced.MaxPaths {
			return nil, errors.New("Git sequence impact exceeds the path bound")
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func mergeAdvancedPaths(left, right []string) []string {
	seen := make(map[string]struct{}, len(left)+len(right))
	for _, values := range [][]string{left, right} {
		for _, value := range values {
			seen[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func (e *AdvancedExecutor) ignoredWorktreePaths(ctx context.Context,
	root string,
) ([]string, error) {
	stdout, stderr, code, err := e.git(ctx, root, nil, "ls-files", "-z", "--others",
		"--ignored", "--exclude-standard", "--directory", "--no-empty-directory")
	if err != nil || code != 0 {
		return nil, fmt.Errorf("inspect ignored worktree paths: %w: %s", err,
			strings.TrimSpace(stderr))
	}
	paths := []string{}
	seen := make(map[string]struct{})
	for _, path := range strings.Split(stdout, "\x00") {
		path = strings.TrimSuffix(path, "/")
		if path == "" {
			continue
		}
		if !safeGitRelativePath(path) {
			return nil, errors.New("ignored worktree state contains an unsafe path")
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
		if len(paths) > gitadvanced.MaxPaths {
			return nil, errors.New("ignored worktree state exceeds the path bound")
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func (e *AdvancedExecutor) ignoredCandidateCollision(ctx context.Context, root string,
	candidates []string,
) (string, error) {
	ignored, err := e.ignoredWorktreePaths(ctx, root)
	if err != nil {
		return "", err
	}
	for _, ignoredPath := range ignored {
		for _, candidate := range candidates {
			if advancedPathsOverlap(ignoredPath, candidate) {
				return "operation may overwrite ignored worktree path " + ignoredPath, nil
			}
		}
	}
	return "", nil
}

func (e *AdvancedExecutor) blockIgnoredHistoryCollision(ctx context.Context, root string,
	preview *gitadvanced.Preview,
) error {
	reason, err := e.ignoredHistoryCollision(ctx, root)
	if err != nil {
		return err
	}
	if reason != "" {
		preview.BlockedReasons = append(preview.BlockedReasons, reason)
	}
	return nil
}

func (e *AdvancedExecutor) ignoredHistoryCollision(ctx context.Context,
	root string,
) (string, error) {
	ignored, err := e.ignoredWorktreePaths(ctx, root)
	if err != nil || len(ignored) == 0 {
		return "", err
	}
	pathspecs := make(map[string]struct{})
	for _, path := range ignored {
		parts := strings.Split(path, "/")
		for index := 1; index <= len(parts); index++ {
			pathspecs[strings.Join(parts[:index], "/")] = struct{}{}
			if len(pathspecs) > gitadvanced.MaxPaths {
				return "ignored worktree state exceeds the sequence collision-analysis bound", nil
			}
		}
	}
	args := []string{"log", "-1", "--format=%H", "--all", "--"}
	for path := range pathspecs {
		args = append(args, path)
	}
	stdout, stderr, code, runErr := e.git(ctx, root, nil, args...)
	if runErr != nil || code != 0 {
		return "", fmt.Errorf("inspect ignored sequence collisions: %w: %s", runErr,
			strings.TrimSpace(stderr))
	}
	if strings.TrimSpace(stdout) != "" {
		return "active sequence may overwrite ignored worktree content recorded in repository history", nil
	}
	return "", nil
}

func (e *AdvancedExecutor) revalidateIgnoredMutationFence(ctx context.Context, root string,
	preview gitadvanced.Preview,
) error {
	var reason string
	var err error
	switch preview.Operation {
	case gitadvanced.StashApply, gitadvanced.StashPop,
		gitadvanced.RebaseStart, gitadvanced.CherryPickStart, gitadvanced.BisectStart:
		reason, err = e.ignoredCandidateCollision(ctx, root, fileImpactPaths(preview.Files))
	case gitadvanced.RebaseContinue, gitadvanced.RebaseSkip, gitadvanced.RebaseAbort,
		gitadvanced.CherryPickContinue, gitadvanced.CherryPickSkip, gitadvanced.CherryPickAbort,
		gitadvanced.BisectGood, gitadvanced.BisectBad, gitadvanced.BisectSkip,
		gitadvanced.BisectRun, gitadvanced.BisectReset:
		reason, err = e.ignoredHistoryCollision(ctx, root)
	}
	if err != nil {
		return err
	}
	if reason != "" {
		return &gitadvanced.Error{Code: gitadvanced.FailureDirtyWorktree,
			Message: reason + "; create a new preview after preserving or removing that content"}
	}
	return nil
}

func advancedPathsOverlap(left, right string) bool {
	if runtime.GOOS == "windows" {
		left, right = strings.ToLower(left), strings.ToLower(right)
	}
	return left == right || strings.HasPrefix(left, right+"/") ||
		strings.HasPrefix(right, left+"/")
}

func (e *AdvancedExecutor) previewSequenceControl(ctx context.Context, root string,
	preview *gitadvanced.Preview, expected gitadvanced.SequenceKind,
) error {
	kind, active, err := e.sequenceKind(ctx, root)
	if err != nil {
		return err
	}
	if !active || kind != expected {
		preview.BlockedReasons = append(preview.BlockedReasons,
			"no matching durable Git sequence is active")
	}
	preview.Target = preview.Spec.SequenceID
	preview.Summary = string(preview.Spec.Operation) + " for exact durable sequence"
	return nil
}

func (e *AdvancedExecutor) sequenceKind(ctx context.Context, root string) (
	gitadvanced.SequenceKind, bool, error,
) {
	gitDir, err := e.requiredPathOutput(ctx, root, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return "", false, err
	}
	checks := []struct {
		kind  gitadvanced.SequenceKind
		paths []string
	}{
		{gitadvanced.SequenceRebase, []string{"rebase-merge", "rebase-apply"}},
		{gitadvanced.SequenceCherryPick, []string{"CHERRY_PICK_HEAD", "sequencer"}},
		{gitadvanced.SequenceBisect, []string{"BISECT_START"}},
	}
	var found gitadvanced.SequenceKind
	for _, check := range checks {
		active := false
		for _, name := range check.paths {
			if _, statErr := os.Lstat(filepath.Join(gitDir, name)); statErr == nil {
				active = true
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return "", false, statErr
			}
		}
		if active {
			if found != "" && found != check.kind {
				return "", false, errors.New("multiple Git sequencers are active")
			}
			found = check.kind
		}
	}
	return found, found != "", nil
}

func (e *AdvancedExecutor) captureConflictState(ctx context.Context,
	root string,
) (gitadvanced.ConflictState, error) {
	stdout, stderr, code, err := e.git(ctx, root, nil, "ls-files", "-u", "-z")
	if err != nil || code != 0 {
		return gitadvanced.ConflictState{}, fmt.Errorf("inspect Git conflicts: %w: %s", err,
			strings.TrimSpace(stderr))
	}
	byPath := make(map[string]gitadvanced.ConflictFile)
	for _, record := range strings.Split(stdout, "\x00") {
		if record == "" {
			continue
		}
		metadata, path, ok := strings.Cut(record, "\t")
		fields := strings.Fields(metadata)
		if !ok || len(fields) != 3 || !safeGitRelativePath(path) ||
			!gitadvanced.ValidObjectID(fields[1]) {
			return gitadvanced.ConflictState{}, errors.New("Git conflict index entry is invalid")
		}
		value := byPath[path]
		value.Path = path
		switch fields[2] {
		case "1":
			value.BaseOID = fields[1]
		case "2":
			value.OursOID = fields[1]
		case "3":
			value.TheirsOID = fields[1]
		default:
			return gitadvanced.ConflictState{}, errors.New("Git conflict stage is invalid")
		}
		byPath[path] = value
		if len(byPath) > gitadvanced.MaxPaths {
			return gitadvanced.ConflictState{}, errors.New("Git conflict set exceeds safe bound")
		}
	}
	files := make([]gitadvanced.ConflictFile, 0, len(byPath))
	for _, file := range byPath {
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	kind, sequence, sequenceErr := e.sequenceKind(ctx, root)
	if sequenceErr != nil {
		return gitadvanced.ConflictState{}, sequenceErr
	}
	return gitadvanced.ConflictState{Active: len(files) != 0,
		Kind: string(kind), Files: files, CanContinue: sequence,
		CanSkip:  sequence && kind != gitadvanced.SequenceBisect,
		CanAbort: sequence}, nil
}

func derivedSequenceID(previewID string, kind gitadvanced.SequenceKind) string {
	return gitadvanced.SequenceIDForPreview(previewID, kind)
}

func shortOID(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	return value
}

func (e *AdvancedExecutor) executeBisectRecipe(ctx context.Context, root string,
	spec gitadvanced.Spec, receipt *gitadvanced.Receipt,
) (string, string, int, error) {
	if spec.Recipe == nil {
		return "", "", 0, errors.New("bisect recipe is required")
	}
	if e.recipeExecutor == nil {
		return "", "", 0, errors.New("bisect recipe Run-selected execution backend is unavailable")
	}
	var allOut, allErr strings.Builder
	for step := 0; step < spec.Recipe.MaxSteps; step++ {
		binding, captureErr := e.CaptureAdvancedBinding(ctx, root)
		if captureErr != nil {
			return allOut.String(), allErr.String(), 0, captureErr
		}
		if step == 0 && binding.Head != spec.ExpectedCurrent {
			return allOut.String(), allErr.String(), 0,
				errors.New("bisect recipe current commit drifted")
		}
		testCtx, cancel := context.WithTimeout(ctx,
			time.Duration(spec.Recipe.TimeoutSeconds)*time.Second)
		if err := checkAdvancedDispatch(testCtx); err != nil {
			cancel()
			return allOut.String(), allErr.String(), 0, err
		}
		started, execution, runErr := e.recipeExecutor.ExecuteBisectRecipe(testCtx, BisectRecipeStep{
			RequestFingerprint: gitadvanced.Fingerprint("bisect-recipe-step", spec.SequenceID,
				binding.Head, string(spec.Recipe.Name), fmt.Sprint(step)),
			Root: root, SequenceID: spec.SequenceID, Commit: binding.Head, Ordinal: step,
			RecipeName: spec.Recipe.Name, TimeoutSeconds: spec.Recipe.TimeoutSeconds,
		})
		timedOut := errors.Is(testCtx.Err(), context.DeadlineExceeded)
		cancel()
		execution.Commit, execution.Ordinal = binding.Head, step
		if execution.Execution.JobID != "" {
			if err := execution.Validate(); err != nil {
				return allOut.String(), allErr.String(), 0, err
			}
			receipt.BisectExecutions = append(receipt.BisectExecutions, execution)
		}
		maxInt := int(^uint(0) >> 1)
		if started.Stdout.ObservedBytes < 0 || started.Stderr.ObservedBytes < 0 ||
			started.Stdout.ObservedBytes > maxInt-started.Stderr.ObservedBytes {
			return allOut.String(), allErr.String(), 0,
				errors.New("bisect recipe output evidence is invalid")
		}
		observed := started.Stdout.ObservedBytes + started.Stderr.ObservedBytes
		if receipt.ObservedBytes > maxInt-observed {
			return allOut.String(), allErr.String(), 0,
				errors.New("bisect recipe output evidence exceeds its numeric bound")
		}
		receipt.ObservedBytes += observed
		if !started.StdinClosed || !started.TreeReaped || started.StartedAt.IsZero() ||
			started.CompletedAt.IsZero() || started.CompletedAt.Before(started.StartedAt) {
			return allOut.String(), allErr.String(), 0,
				errors.New("bisect recipe process-tree completion evidence is invalid")
		}
		if timedOut || started.TimedOut {
			return allOut.String(), allErr.String(), 0,
				&gitadvanced.Error{Code: gitadvanced.FailureTimeout,
					Message: "bisect recipe step exceeded its timeout"}
		}
		if started.Cancelled {
			return allOut.String(), allErr.String(), 0,
				&gitadvanced.Error{Code: gitadvanced.FailureCancelled,
					Message: "bisect recipe step was cancelled"}
		}
		if runErr != nil {
			return allOut.String(), allErr.String(), 0, runErr
		}
		if execution.Execution.JobID == "" || !execution.Execution.TreeReaped ||
			!execution.Execution.StdinClosed {
			return allOut.String(), allErr.String(), 0,
				errors.New("bisect recipe backend completion evidence is unavailable")
		}
		if started.ExitCode < 0 || started.ExitCode > 127 {
			return allOut.String(), allErr.String(), 0,
				errors.New("bisect recipe terminated without a classifiable exit code")
		}
		postTest, captureErr := e.CaptureAdvancedBinding(ctx, root)
		if captureErr != nil || postTest.Head != binding.Head {
			return allOut.String(), allErr.String(), 0,
				errors.New("bisect recipe current commit changed while testing; result was not marked")
		}
		if reason, collisionErr := e.ignoredHistoryCollision(ctx, root); collisionErr != nil {
			return allOut.String(), allErr.String(), 0, collisionErr
		} else if reason != "" {
			return allOut.String(), allErr.String(), 0,
				&gitadvanced.Error{Code: gitadvanced.FailureDirtyWorktree,
					Message: reason + "; the bisect recipe result was not marked"}
		}
		mark := "good"
		if started.ExitCode != 0 {
			if started.ExitCode == 125 {
				mark = "skip"
			} else {
				mark = "bad"
			}
		}
		markOut, markErr, code, gitErr := e.git(ctx, root, nil, "bisect", mark)
		allOut.WriteString(markOut)
		allErr.WriteString(markErr)
		if gitErr != nil || code != 0 {
			return allOut.String(), allErr.String(), code, gitErr
		}
		if found := bisectFirstBadCommit(markOut); found != "" {
			// Git may leave HEAD on the last good candidate. Its bad ref, not
			// the checkout, identifies the regression proven by this step.
			bad, _, code, err := e.git(ctx, root, nil, "rev-parse", "--verify", "refs/bisect/bad")
			bad = strings.TrimSpace(bad)
			if err != nil || code != 0 || bad != found {
				return allOut.String(), allErr.String(), 0,
					errors.New("bisect first bad commit could not be confirmed")
			}
			receipt.TargetOID = bad
			return allOut.String(), allErr.String(), 0, nil
		}
	}
	return allOut.String(), allErr.String(), 0,
		&gitadvanced.Error{Code: gitadvanced.FailureBudgetExceeded,
			Message: "bisect recipe exhausted its reviewed step budget"}
}

func bisectFirstBadCommit(output string) string {
	line, _, _ := strings.Cut(output, "\n")
	commit, summary, ok := strings.Cut(strings.TrimSuffix(line, "\r"), " ")
	if !ok || !gitadvanced.ValidObjectID(commit) {
		return ""
	}
	// Git 2.55 quotes the bad term. Require the completion header so that
	// commit subjects and bodies printed by Git cannot end the recipe early.
	if summary != "is the first bad commit" && summary != "is the first 'bad' commit" {
		return ""
	}
	return commit
}
