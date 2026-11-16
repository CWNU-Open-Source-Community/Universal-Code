package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"cyberagent-workbench/internal/gitadvanced"
	"cyberagent-workbench/internal/rootfs"
)

type advancedWorktreeEntry struct {
	path     string
	head     string
	branch   string
	detached bool
	locked   bool
	reason   string
	prunable bool
}

// AdvancedManagedWorktreeObservation is a read-only, exact observation used
// by startup reconciliation. Path remains application-internal and is never
// projected by the HTTP or Desktop contracts.
type AdvancedManagedWorktreeObservation struct {
	Path       string
	Found      bool
	Present    bool
	Prunable   bool
	Detached   bool
	Locked     bool
	LockReason string
	Head       string
	Branch     string
	Clean      bool
	Binding    gitadvanced.RepositoryBinding
}

func (e *AdvancedExecutor) InspectManagedWorktree(ctx context.Context, root string,
	binding gitadvanced.RepositoryBinding, name string,
) (AdvancedManagedWorktreeObservation, error) {
	destination, err := e.managedWorktreeDestination(binding, name, false)
	if err != nil {
		return AdvancedManagedWorktreeObservation{}, err
	}
	observation := AdvancedManagedWorktreeObservation{Path: destination}
	entries, err := e.listGitWorktrees(ctx, root)
	if err != nil {
		return observation, err
	}
	var matched *advancedWorktreeEntry
	for index := range entries {
		if sameFilesystemPath(entries[index].path, destination) {
			matched = &entries[index]
			break
		}
	}
	if matched == nil {
		if _, statErr := os.Lstat(destination); statErr == nil ||
			!errors.Is(statErr, os.ErrNotExist) {
			return observation, &gitadvanced.Error{Code: gitadvanced.FailureUnknownWorktree,
				Message: "managed destination exists without exact Git worktree metadata"}
		}
		return observation, nil
	}
	observation.Found, observation.Prunable = true, matched.prunable
	observation.Detached, observation.Locked = matched.detached, matched.locked
	observation.LockReason, observation.Head = matched.reason, matched.head
	observation.Branch = strings.TrimPrefix(matched.branch, "refs/heads/")
	if matched.prunable {
		return observation, nil
	}
	destination, err = e.managedWorktreeDestination(binding, name, true)
	if err != nil {
		return observation, err
	}
	targetBinding, err := e.CaptureAdvancedBinding(ctx, destination)
	if err != nil {
		return observation, err
	}
	if targetBinding.CommonDirSHA256 != binding.CommonDirSHA256 {
		return observation, &gitadvanced.Error{Code: gitadvanced.FailureUnknownWorktree,
			Message: "managed worktree common repository identity changed"}
	}
	clean, err := e.worktreeRemovalClean(ctx, destination)
	if err != nil {
		return observation, err
	}
	observation.Path, observation.Present = destination, true
	observation.Binding, observation.Clean = targetBinding, clean
	return observation, nil
}

func (e *AdvancedExecutor) previewManagedWorktree(ctx context.Context, root string,
	preview *gitadvanced.Preview,
) error {
	entries, err := e.listGitWorktrees(ctx, root)
	if err != nil {
		return err
	}
	listFingerprint := fingerprintWorktreeEntries(entries)
	spec := preview.Spec
	switch spec.Operation {
	case gitadvanced.WorktreeCreate:
		if err := e.requireCommit(ctx, root, spec.Commit); err != nil {
			return err
		}
		destination, err := e.managedWorktreeDestination(preview.Binding,
			spec.WorktreeName, false)
		if err != nil {
			return err
		}
		if _, statErr := os.Lstat(destination); statErr == nil || !errors.Is(statErr, os.ErrNotExist) {
			preview.BlockedReasons = append(preview.BlockedReasons,
				"managed worktree destination already exists or is inaccessible")
		}
		for _, entry := range entries {
			if sameFilesystemPath(entry.path, destination) || entry.branch == "refs/heads/"+spec.Branch {
				preview.BlockedReasons = append(preview.BlockedReasons,
					"managed worktree path or branch is already registered by Git")
			}
		}
		if exists, err := e.localBranchExists(ctx, root, spec.Branch); err != nil {
			return err
		} else if exists {
			preview.BlockedReasons = append(preview.BlockedReasons,
				"worktree create never reuses an existing local branch")
		}
		preview.Target = gitadvanced.Fingerprint("worktree-create-target", destination,
			spec.Branch, spec.Commit, listFingerprint)
		preview.Files = []gitadvanced.FileImpact{{Path: "managed-worktree/" + spec.WorktreeName,
			AfterSHA256: gitadvanced.Fingerprint("worktree-content", spec.Commit),
			Change:      "create managed worktree", Destructive: false}}
		preview.Summary = "create branch-backed worktree below the product managed root"
	case gitadvanced.WorktreeLock, gitadvanced.WorktreeUnlock, gitadvanced.WorktreeRemove:
		destination, target, err := e.requireManagedWorktree(ctx, root, preview.Binding,
			spec.WorktreeName, entries)
		if err != nil {
			return err
		}
		targetBinding, err := e.CaptureAdvancedBinding(ctx, destination)
		if err != nil {
			return err
		}
		if targetBinding.CommonDirSHA256 != preview.Binding.CommonDirSHA256 {
			return &gitadvanced.Error{Code: gitadvanced.FailureUnknownWorktree,
				Message: "managed worktree common repository identity changed"}
		}
		preview.Target = gitadvanced.Fingerprint("worktree-target", destination,
			targetBinding.Fingerprint(), fmt.Sprintf("%t", target.locked), target.reason,
			listFingerprint)
		preview.Files = []gitadvanced.FileImpact{{Path: "managed-worktree/" + spec.WorktreeName,
			BeforeSHA256: targetBinding.WorktreeSHA256,
			AfterSHA256:  targetBinding.WorktreeSHA256,
			Change:       string(spec.Operation), Destructive: spec.Operation == gitadvanced.WorktreeRemove}}
		preview.Summary = fmt.Sprintf("%s exact managed worktree at %s",
			spec.Operation, shortOID(targetBinding.Head))
		switch spec.Operation {
		case gitadvanced.WorktreeLock:
			if target.locked {
				preview.BlockedReasons = append(preview.BlockedReasons, "managed worktree is already locked")
			}
		case gitadvanced.WorktreeUnlock:
			if !target.locked {
				preview.BlockedReasons = append(preview.BlockedReasons, "managed worktree is not locked")
			}
		case gitadvanced.WorktreeRemove:
			if target.locked {
				preview.BlockedReasons = append(preview.BlockedReasons,
					"locked managed worktree must be explicitly unlocked before removal")
			}
			clean, cleanErr := e.worktreeRemovalClean(ctx, destination)
			if cleanErr != nil {
				return cleanErr
			}
			if !clean {
				preview.BlockedReasons = append(preview.BlockedReasons,
					"managed worktree has tracked, untracked, or ignored changes and cannot be removed")
			}
		}
	case gitadvanced.WorktreePrune:
		candidates := make([]advancedWorktreeEntry, 0)
		repositoryManagedRoot := filepath.Join(e.managedRoot,
			preview.Binding.CommonDirSHA256[:16])
		for _, entry := range entries {
			if !entry.prunable {
				continue
			}
			if !pathInsideRoot(repositoryManagedRoot, entry.path) {
				preview.BlockedReasons = append(preview.BlockedReasons,
					"Git reports a stale external or cross-repository worktree; product prune refuses to touch it")
				continue
			}
			candidates = append(candidates, entry)
		}
		preview.Target = gitadvanced.Fingerprint("worktree-prune-target", listFingerprint)
		for _, entry := range candidates {
			preview.Files = append(preview.Files, gitadvanced.FileImpact{
				Path: "managed-worktree-stale/" + gitadvanced.Fingerprint("path", entry.path)[:16],
				BeforeSHA256: gitadvanced.Fingerprint("administrative-entry", entry.path,
					entry.head, entry.branch), Change: "prune missing worktree metadata",
				Destructive: true})
		}
		preview.Summary = fmt.Sprintf("prune metadata for %d missing managed worktree(s)",
			len(candidates))
		if len(candidates) == 0 {
			preview.BlockedReasons = append(preview.BlockedReasons,
				"no safe managed worktree metadata is eligible for prune")
		}
	}
	return nil
}

// PrunableManagedWorktreePathSHA256 returns only path identities for stale Git
// administrative entries inside this repository's derived product root. The
// application layer must cross-check every identity against its durable
// registry before approval and again before execution.
func (e *AdvancedExecutor) PrunableManagedWorktreePathSHA256(ctx context.Context,
	root string, binding gitadvanced.RepositoryBinding,
) ([]string, error) {
	if len(binding.CommonDirSHA256) < 16 {
		return nil, errors.New("managed worktree repository binding is invalid")
	}
	entries, err := e.listGitWorktrees(ctx, root)
	if err != nil {
		return nil, err
	}
	repositoryManagedRoot := filepath.Join(e.managedRoot, binding.CommonDirSHA256[:16])
	values := make([]string, 0)
	for _, entry := range entries {
		if !entry.prunable {
			continue
		}
		if !pathInsideRoot(repositoryManagedRoot, entry.path) {
			return nil, &gitadvanced.Error{Code: gitadvanced.FailureOutsideManagedRoot,
				Message: "stale Git worktree is outside the exact repository managed root"}
		}
		values = append(values,
			gitadvanced.Fingerprint("managed-worktree-path", filepath.Clean(entry.path)))
	}
	sort.Strings(values)
	return values, nil
}

func (e *AdvancedExecutor) executeManagedWorktree(ctx context.Context, root string,
	preview gitadvanced.Preview, receipt *gitadvanced.Receipt,
) (string, string, int, error) {
	spec := preview.Spec
	destination, err := e.managedWorktreeDestination(preview.Binding,
		spec.WorktreeName, spec.Operation != gitadvanced.WorktreeCreate &&
			spec.Operation != gitadvanced.WorktreePrune)
	if err != nil && spec.Operation != gitadvanced.WorktreePrune {
		return "", "", 0, err
	}
	switch spec.Operation {
	case gitadvanced.WorktreeCreate:
		if err := checkAdvancedDispatch(ctx); err != nil {
			return "", "", 0, err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return "", "", 0, err
		}
		if err := validateManagedWorktreeParent(e.managedRoot,
			filepath.Dir(destination), false); err != nil {
			return "", "", 0, err
		}
		receipt.WorktreeID = "gwt-" + gitadvanced.Fingerprint("worktree", preview.ID)[:32]
		return e.git(ctx, root, nil, "worktree", "add", "--no-track", "-b",
			spec.Branch, destination, spec.Commit)
	case gitadvanced.WorktreeLock:
		receipt.WorktreeID = spec.WorktreeID
		args := []string{"worktree", "lock"}
		if spec.LockReason != "" {
			args = append(args, "--reason", spec.LockReason)
		}
		args = append(args, destination)
		return e.git(ctx, root, nil, args...)
	case gitadvanced.WorktreeUnlock:
		receipt.WorktreeID = spec.WorktreeID
		return e.git(ctx, root, nil, "worktree", "unlock", destination)
	case gitadvanced.WorktreeRemove:
		receipt.WorktreeID = spec.WorktreeID
		clean, cleanErr := e.worktreeRemovalClean(ctx, destination)
		if cleanErr != nil {
			return "", "", 0, cleanErr
		}
		if !clean {
			return "", "", 0, &gitadvanced.Error{Code: gitadvanced.FailureDirtyWorktree,
				Message: "managed worktree changed after preview and contains tracked, untracked, or ignored content"}
		}
		// Deliberately no --force. Git performs another tracked/untracked check;
		// the immediately preceding product check additionally includes ignored files.
		return e.git(ctx, root, nil, "worktree", "remove", destination)
	case gitadvanced.WorktreePrune:
		// Review rejected any external stale entry. Re-review immediately before
		// this call binds the exact list; Git only removes administrative data for
		// already-missing directories and never recursively deletes a worktree.
		return e.git(ctx, root, nil, "worktree", "prune", "--verbose", "--expire=now")
	default:
		return "", "", 0, errors.New("unsupported managed worktree operation")
	}
}

func (e *AdvancedExecutor) managedWorktreeDestination(
	binding gitadvanced.RepositoryBinding, name string, mustExist bool,
) (string, error) {
	if !e.Available() || len(binding.CommonDirSHA256) < 16 {
		return "", errors.New("managed worktree capability is unavailable")
	}
	// Spec validation restricts name to a single ASCII path segment.
	destination := filepath.Join(e.managedRoot, binding.CommonDirSHA256[:16], name)
	abs, err := filepath.Abs(destination)
	if err != nil || !pathInsideRoot(e.managedRoot, abs) {
		return "", &gitadvanced.Error{Code: gitadvanced.FailureOutsideManagedRoot,
			Message: "managed worktree destination escaped its product root"}
	}
	if err := validateManagedWorktreeParent(e.managedRoot, filepath.Dir(abs),
		!mustExist); err != nil {
		return "", err
	}
	if mustExist {
		info, statErr := os.Lstat(abs)
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", &gitadvanced.Error{Code: gitadvanced.FailureUnknownWorktree,
				Message: "managed worktree is missing or is not a real directory"}
		}
		resolved, resolveErr := filepath.EvalSymlinks(abs)
		if resolveErr != nil || !sameFilesystemPath(abs, resolved) ||
			!pathInsideRoot(e.managedRoot, resolved) {
			return "", &gitadvanced.Error{Code: gitadvanced.FailureOutsideManagedRoot,
				Message: "managed worktree traverses a link or reparse point"}
		}
	}
	return filepath.Clean(abs), nil
}

func validateManagedWorktreeParent(root, parent string, allowMissing bool) error {
	info, err := os.Lstat(parent)
	if errors.Is(err, os.ErrNotExist) && allowMissing {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return &gitadvanced.Error{Code: gitadvanced.FailureOutsideManagedRoot,
			Message: "managed worktree parent must be a real directory below the product root"}
	}
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || !sameFilesystemPath(parent, resolved) ||
		!pathInsideRoot(root, resolved) {
		return &gitadvanced.Error{Code: gitadvanced.FailureOutsideManagedRoot,
			Message: "managed worktree parent traverses a link or reparse point"}
	}
	return nil
}

func (e *AdvancedExecutor) listGitWorktrees(ctx context.Context,
	root string,
) ([]advancedWorktreeEntry, error) {
	stdout, stderr, code, err := e.git(ctx, root, nil, "worktree", "list", "--porcelain", "-z")
	if err != nil || code != 0 {
		return nil, fmt.Errorf("list Git worktrees: %w: %s", err, strings.TrimSpace(stderr))
	}
	var out []advancedWorktreeEntry
	current := advancedWorktreeEntry{}
	flush := func() error {
		if current.path == "" {
			return nil
		}
		if !filepath.IsAbs(current.path) || strings.ContainsRune(current.path, 0) {
			return errors.New("Git worktree list returned an invalid path")
		}
		current.path = filepath.Clean(rootfs.OrdinaryPath(current.path))
		if current.head != "" && !gitadvanced.ValidObjectID(current.head) {
			return errors.New("Git worktree list returned an invalid commit")
		}
		out = append(out, current)
		current = advancedWorktreeEntry{}
		return nil
	}
	for _, field := range strings.Split(stdout, "\x00") {
		if field == "" {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		switch {
		case strings.HasPrefix(field, "worktree "):
			if current.path != "" {
				if err := flush(); err != nil {
					return nil, err
				}
			}
			current.path = strings.TrimPrefix(field, "worktree ")
		case strings.HasPrefix(field, "HEAD "):
			current.head = strings.TrimPrefix(field, "HEAD ")
		case strings.HasPrefix(field, "branch "):
			current.branch = strings.TrimPrefix(field, "branch ")
		case field == "detached":
			current.detached = true
		case field == "locked":
			current.locked = true
		case strings.HasPrefix(field, "locked "):
			current.locked, current.reason = true, strings.TrimPrefix(field, "locked ")
		case field == "prunable" || strings.HasPrefix(field, "prunable "):
			current.prunable = true
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(out) > 1000 {
		return nil, errors.New("Git worktree list exceeds safe bound")
	}
	return out, nil
}

func (e *AdvancedExecutor) requireManagedWorktree(ctx context.Context, root string,
	binding gitadvanced.RepositoryBinding, name string, entries []advancedWorktreeEntry,
) (string, advancedWorktreeEntry, error) {
	destination, err := e.managedWorktreeDestination(binding, name, true)
	if err != nil {
		return "", advancedWorktreeEntry{}, err
	}
	for _, entry := range entries {
		if sameFilesystemPath(entry.path, destination) {
			if entry.prunable {
				return "", advancedWorktreeEntry{}, &gitadvanced.Error{
					Code:    gitadvanced.FailureUnknownWorktree,
					Message: "managed worktree is recorded as prunable"}
			}
			return destination, entry, nil
		}
	}
	return "", advancedWorktreeEntry{}, &gitadvanced.Error{
		Code:    gitadvanced.FailureUnknownWorktree,
		Message: "path is not an exact registered Git worktree"}
}

func (e *AdvancedExecutor) localBranchExists(ctx context.Context, root,
	branch string,
) (bool, error) {
	_, stderr, code, err := e.git(ctx, root, nil, "show-ref", "--verify", "--quiet",
		"refs/heads/"+branch)
	if err != nil {
		return false, err
	}
	if code == 0 {
		return true, nil
	}
	if code == 1 || (code == 128 && strings.Contains(stderr, "not a valid ref")) {
		return false, nil
	}
	return false, fmt.Errorf("inspect local worktree branch: %s", strings.TrimSpace(stderr))
}

func fingerprintWorktreeEntries(entries []advancedWorktreeEntry) string {
	values := make([]string, 0, len(entries))
	for _, entry := range entries {
		values = append(values, strings.Join([]string{entry.path, entry.head, entry.branch,
			fmt.Sprintf("%t", entry.detached), fmt.Sprintf("%t", entry.locked),
			entry.reason, fmt.Sprintf("%t", entry.prunable)}, "\x00"))
	}
	sort.Strings(values)
	return gitadvanced.Fingerprint("worktree-list", strings.Join(values, "\x01"))
}
