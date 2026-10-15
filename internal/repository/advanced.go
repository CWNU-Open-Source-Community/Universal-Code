package repository

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/gitadvanced"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolcontract"
)

const (
	MaxAdvancedGitDuration   = 5 * time.Minute
	MaxAdvancedTrackedBytes  = 16 * 1024 * 1024
	MaxAdvancedSequenceBytes = 1024 * 1024
)

// AdvancedExecutor owns the fixed Git command templates for git-advanced.v1.
// It never accepts raw argv or a caller-selected worktree destination.
type AdvancedExecutor struct {
	gitPath        string
	managedRoot    string
	capability     gitadvanced.CapabilitySnapshot
	maxDuration    time.Duration
	commandContext func(context.Context, string, ...string) *exec.Cmd
	recipeExecutor BisectRecipeExecutor
	now            func() time.Time
}

type AdvancedSequenceObservation struct {
	Kind     gitadvanced.SequenceKind      `json:"kind,omitempty"`
	Active   bool                          `json:"active"`
	Binding  gitadvanced.RepositoryBinding `json:"binding"`
	Conflict gitadvanced.ConflictState     `json:"conflict"`
}

func NewAdvancedExecutor(managedRoot string, enabled bool) (*AdvancedExecutor, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("git binary is unavailable: %w", err)
	}
	root, err := canonicalManagedWorktreeRoot(managedRoot, enabled)
	if err != nil {
		return nil, err
	}
	generation, err := randomCapabilityGeneration()
	if err != nil {
		return nil, err
	}
	operations := []gitadvanced.Operation(nil)
	if enabled {
		operations = gitadvanced.Operations()
	}
	capability := gitadvanced.CapabilitySnapshot{
		ProtocolVersion: gitadvanced.CapabilityProtocolVersion,
		Enabled:         enabled, Generation: generation,
		ManagedRootSHA256: gitadvanced.Fingerprint("managed-worktree-root", root),
		Operations:        operations, MaxHunks: gitadvanced.MaxHunks,
		MaxPaths: gitadvanced.MaxPaths, MaxCommits: gitadvanced.MaxCommits,
		CapturedAt: time.Now().UTC(),
	}
	if err := capability.Validate(); err != nil {
		return nil, err
	}
	return &AdvancedExecutor{gitPath: gitPath, managedRoot: root,
		capability: capability, maxDuration: MaxAdvancedGitDuration,
		commandContext: repositoryCommandContext,
		now:            func() time.Time { return time.Now().UTC() }}, nil
}

// BisectRecipeStep is issued by the native Git loop for the current commit.
// The application resolves its fixed command through the Run-selected backend.
type BisectRecipeStep struct {
	Root, SequenceID, Commit, RequestFingerprint string
	Ordinal                                      int
	RecipeName                                   gitadvanced.RecipeName
	TimeoutSeconds                               int
}

type BisectRecipeExecutor interface {
	ExecuteBisectRecipe(context.Context, BisectRecipeStep) (runner.OnceStartResult, gitadvanced.BisectExecutionReceipt, error)
}

// WithBisectRecipeExecutor scopes execution authority to one operation without
// changing the shared Git executor. An unbound executor cannot run project code.
func (e *AdvancedExecutor) WithBisectRecipeExecutor(executor BisectRecipeExecutor) *AdvancedExecutor {
	if e == nil {
		return nil
	}
	bound := *e
	bound.recipeExecutor = executor
	return &bound
}

func (e *AdvancedExecutor) Available() bool {
	return e != nil && e.gitPath != "" && e.capability.Enabled
}

func (e *AdvancedExecutor) Capability() gitadvanced.CapabilitySnapshot {
	if e == nil {
		return gitadvanced.CapabilitySnapshot{}
	}
	value := e.capability
	value.Operations = append([]gitadvanced.Operation{}, value.Operations...)
	value.CapturedAt = e.now().UTC()
	return value
}

func (e *AdvancedExecutor) ManagedRoot() string {
	if e == nil {
		return ""
	}
	return e.managedRoot
}

func (e *AdvancedExecutor) InspectAdvancedSequence(ctx context.Context,
	root string,
) (AdvancedSequenceObservation, error) {
	binding, err := e.CaptureAdvancedBinding(ctx, root)
	if err != nil {
		return AdvancedSequenceObservation{}, err
	}
	kind, active, err := e.sequenceKind(ctx, root)
	if err != nil {
		return AdvancedSequenceObservation{}, err
	}
	conflict, err := e.captureConflictState(ctx, root)
	if err != nil {
		return AdvancedSequenceObservation{}, err
	}
	return AdvancedSequenceObservation{Kind: kind, Active: active,
		Binding: binding, Conflict: conflict}, nil
}

func (e *AdvancedExecutor) ManagedWorktreePath(binding gitadvanced.RepositoryBinding,
	name string,
) (string, error) {
	return e.managedWorktreeDestination(binding, name, true)
}

// PlannedManagedWorktreePath returns the exact product-derived destination
// before materialization. It accepts no caller-selected directory and is used
// by higher-level lifecycle ledgers to persist a write-ahead ownership record.
func (e *AdvancedExecutor) PlannedManagedWorktreePath(binding gitadvanced.RepositoryBinding,
	name string,
) (string, error) {
	return e.managedWorktreeDestination(binding, name, false)
}

// CaptureAdvancedBinding binds repository identity, HEAD, index, worktree,
// stash, sequencer, and current upstream. It handles linked worktrees by
// resolving --git-path instead of assuming <root>/.git/index.
func (e *AdvancedExecutor) CaptureAdvancedBinding(ctx context.Context,
	root string,
) (gitadvanced.RepositoryBinding, error) {
	if e == nil || e.gitPath == "" {
		return gitadvanced.RepositoryBinding{}, errors.New("Git advanced executor is unavailable")
	}
	canonical, err := canonicalExistingRepositoryRoot(root)
	if err != nil {
		return gitadvanced.RepositoryBinding{}, err
	}
	if err := validateHardenedGitRepository(ctx, e.gitPath, canonical); err != nil {
		return gitadvanced.RepositoryBinding{}, &gitadvanced.Error{
			Code: gitadvanced.FailureUnsafeRepository, Message: err.Error()}
	}
	topLevel, err := e.requiredPathOutput(ctx, canonical, "rev-parse", "--show-toplevel")
	if err != nil {
		return gitadvanced.RepositoryBinding{}, &gitadvanced.Error{
			Code: gitadvanced.FailureUnsafeRepository, Message: "Git worktree root is unavailable"}
	}
	topLevel, err = canonicalExistingRepositoryRoot(topLevel)
	if err != nil || !sameFilesystemPath(topLevel, canonical) {
		return gitadvanced.RepositoryBinding{}, &gitadvanced.Error{
			Code:    gitadvanced.FailureUnsafeRepository,
			Message: "Git configuration redirects the worktree outside the registered Workspace"}
	}
	bare, _, bareCode, bareErr := e.git(ctx, canonical, nil, "rev-parse", "--is-bare-repository")
	if bareErr != nil || bareCode != 0 || strings.TrimSpace(bare) != "false" {
		return gitadvanced.RepositoryBinding{}, &gitadvanced.Error{
			Code:    gitadvanced.FailureUnsafeRepository,
			Message: "Git advanced operations require the exact non-bare Workspace worktree"}
	}
	common, err := e.requiredPathOutput(ctx, canonical, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return gitadvanced.RepositoryBinding{}, err
	}
	common, err = canonicalGitMetadataPath(common)
	if err != nil {
		return gitadvanced.RepositoryBinding{}, err
	}
	gitDir, err := e.requiredPathOutput(ctx, canonical, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return gitadvanced.RepositoryBinding{}, err
	}
	gitDir, err = canonicalGitMetadataPath(gitDir)
	if err != nil {
		return gitadvanced.RepositoryBinding{}, err
	}
	head := "unborn"
	if value, _, exit, outputErr := e.git(ctx, canonical, nil,
		"rev-parse", "--verify", "HEAD"); outputErr == nil && exit == 0 {
		head = strings.TrimSpace(value)
	}
	branch, _, _, _ := e.git(ctx, canonical, nil, "branch", "--show-current")
	branch = strings.TrimSpace(branch)
	objectFormat, _, exit, err := e.git(ctx, canonical, nil, "rev-parse", "--show-object-format")
	if err != nil || exit != 0 {
		return gitadvanced.RepositoryBinding{}, errors.New("inspect Git object format")
	}
	objectFormat = strings.TrimSpace(objectFormat)
	if objectFormat != "sha1" && objectFormat != "sha256" {
		return gitadvanced.RepositoryBinding{}, errors.New("unsupported Git object format")
	}
	status, stderr, exit, err := e.git(ctx, canonical, nil,
		"status", "--porcelain=v2", "-z", "--untracked-files=all")
	if err != nil || exit != 0 {
		return gitadvanced.RepositoryBinding{}, fmt.Errorf("inspect Git status: %w: %s", err,
			strings.TrimSpace(stderr))
	}
	worktreeDigest, err := e.captureWorktreeDigest(ctx, canonical, status)
	if err != nil {
		return gitadvanced.RepositoryBinding{}, err
	}
	// Git status may refresh only the index stat cache. Digest the index after
	// that read so a second inspection observes the same post-inspection state.
	indexPath, err := e.requiredPathOutput(ctx, canonical, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return gitadvanced.RepositoryBinding{}, err
	}
	indexDigest, err := digestOptionalRegularFile(indexPath, MaxAdvancedTrackedBytes)
	if err != nil {
		return gitadvanced.RepositoryBinding{}, err
	}
	stash, _, _, _ := e.git(ctx, canonical, nil, "stash", "list", "--format=%H%x00%gd%x00%P")
	sequenceDigest, err := digestGitSequenceState(gitDir)
	if err != nil {
		return gitadvanced.RepositoryBinding{}, err
	}
	upstreamRef, upstreamOID := "", ""
	if branch != "" {
		if value, _, code, outputErr := e.git(ctx, canonical, nil,
			"rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); outputErr == nil && code == 0 {
			upstreamRef = strings.TrimSpace(value)
			if validObservedRef(upstreamRef) {
				if value, _, code, outputErr = e.git(ctx, canonical, nil,
					"rev-parse", "--verify", upstreamRef); outputErr == nil && code == 0 {
					upstreamOID = strings.TrimSpace(value)
				}
			} else {
				upstreamRef = ""
			}
		}
	}
	statusSum := sha256.Sum256([]byte(status))
	stashSum := sha256.Sum256([]byte(stash))
	return gitadvanced.RepositoryBinding{
		ProtocolVersion:  gitadvanced.ProtocolVersion,
		RepositorySHA256: gitadvanced.Fingerprint("repository-root", canonical),
		CommonDirSHA256:  gitadvanced.Fingerprint("git-common-dir", common),
		Head:             head, Branch: branch, IndexSHA256: indexDigest,
		WorktreeSHA256: worktreeDigest, StatusSHA256: hex.EncodeToString(statusSum[:]),
		StashSHA256: hex.EncodeToString(stashSum[:]), SequenceSHA256: sequenceDigest,
		UpstreamRef: upstreamRef, UpstreamOID: upstreamOID,
		Detached: branch == "" && head != "unborn", ObjectFormat: objectFormat,
		CapturedAt: e.now().UTC(),
	}, nil
}

// ReviewAdvanced computes immutable evidence. Blocked previews are returned so
// Desktop and CLI can explain policy, state, or recovery limitations without
// executing Git.
func (e *AdvancedExecutor) ReviewAdvanced(ctx context.Context, root string,
	spec gitadvanced.Spec,
) (gitadvanced.Preview, error) {
	if e == nil || !e.Available() {
		return gitadvanced.Preview{}, &gitadvanced.Error{Code: gitadvanced.FailureCapabilityDisabled,
			Message: "Git advanced capability is disabled at process startup"}
	}
	if err := spec.Validate(); err != nil {
		return gitadvanced.Preview{}, apperror.Wrap(apperror.CodeInvalidArgument,
			"Git advanced request is invalid", err)
	}
	binding, err := e.CaptureAdvancedBinding(ctx, root)
	if err != nil {
		return gitadvanced.Preview{}, err
	}
	preview := gitadvanced.Preview{ProtocolVersion: gitadvanced.PreviewProtocolVersion,
		Operation: spec.Operation, Spec: spec, Binding: binding, Capability: e.Capability(),
		Hunks: []gitadvanced.Hunk{}, Files: []gitadvanced.FileImpact{},
		Conflict: gitadvanced.ConflictState{Files: []gitadvanced.ConflictFile{}},
		Recovery: gitadvanced.RecoveryPlan{Required: spec.Operation.RequiresCheckpoint(),
			RestoreAction: "workspace_checkpoint_rewind", IncompleteReasons: []string{}},
		BlockedReasons: []string{}, CreatedAt: e.now().UTC()}
	if err := e.populateAdvancedPreview(ctx, root, &preview); err != nil {
		return gitadvanced.Preview{}, err
	}
	preview.Conflict, err = e.captureConflictState(ctx, root)
	if err != nil {
		return gitadvanced.Preview{}, err
	}
	preview.BlockedReasons = append(preview.BlockedReasons,
		advancedPolicyBlocks(spec, binding, preview.Conflict)...)
	preview.BlockedReasons = deduplicateStrings(preview.BlockedReasons)
	specJSON, _ := json.Marshal(spec)
	hunkIDs := make([]string, 0, len(preview.Hunks))
	for _, hunk := range preview.Hunks {
		hunkIDs = append(hunkIDs, hunk.ID)
	}
	sort.Strings(hunkIDs)
	fileJSON, _ := json.Marshal(preview.Files)
	conflictJSON, _ := json.Marshal(preview.Conflict)
	previewDigest := gitadvanced.Fingerprint("preview", string(specJSON),
		binding.Fingerprint(), preview.Capability.Generation,
		strings.Join(hunkIDs, ","), preview.Target, string(fileJSON), string(conflictJSON),
		strings.Join(preview.BlockedReasons, "\x00"))
	preview.ID = "gap-" + previewDigest[:32]
	preview.ApprovalFingerprint = gitadvanced.Fingerprint("approval", preview.ID,
		string(spec.Operation), binding.Fingerprint(), preview.Capability.Generation)
	return preview, nil
}

// ExecuteAdvanced re-renders the preview and rejects every observable drift
// before invoking one closed command template.
func (e *AdvancedExecutor) ExecuteAdvanced(ctx context.Context, root string,
	preview gitadvanced.Preview, guards ...toolcontract.DispatchGuard,
) (gitadvanced.Receipt, error) {
	// Freeze slice and pointer fields before inspection or the first callback.
	// Callers cannot change the operation after its host authorization is bound.
	frozen, cloneErr := json.Marshal(preview)
	if cloneErr == nil {
		var owned gitadvanced.Preview
		cloneErr = json.Unmarshal(frozen, &owned)
		preview = owned
	}
	started := time.Now().UTC()
	if e != nil && e.now != nil {
		started = e.now().UTC()
	}
	receipt := gitadvanced.Receipt{ProtocolVersion: gitadvanced.ReceiptProtocolVersion,
		ID:        "gar-" + gitadvanced.Fingerprint("receipt", preview.ID)[:32],
		PreviewID: preview.ID, Operation: preview.Operation, Status: gitadvanced.ReceiptFailed,
		PreBinding: preview.Binding, Conflict: gitadvanced.ConflictState{Files: []gitadvanced.ConflictFile{}},
		StartedAt: started}
	if cloneErr != nil {
		return receipt, e.advancedFailure(&receipt, gitadvanced.FailureStalePreview,
			"Git operation inputs could not be frozen")
	}
	if !e.Available() || preview.Capability.Generation != e.capability.Generation {
		return receipt, e.advancedFailure(&receipt, gitadvanced.FailureCapabilityDisabled,
			"Git advanced capability generation changed")
	}
	current, err := e.ReviewAdvanced(ctx, root, preview.Spec)
	if err != nil {
		if ctx.Err() != nil {
			return receipt, e.advancedFailure(&receipt, gitadvanced.FailureCancelled,
				"Git operation was cancelled before mutation; repository state was not replayed")
		}
		var typed *gitadvanced.Error
		if errors.As(err, &typed) && typed.Code != "" {
			return receipt, e.advancedFailure(&receipt, typed.Code,
				"Git preflight inspection failed: "+typed.Message)
		}
		return receipt, e.advancedFailure(&receipt, gitadvanced.FailureGit,
			"Git preflight inspection failed: "+err.Error())
	}
	if preview.Binding.RepositorySHA256 != current.Binding.RepositorySHA256 ||
		preview.Binding.CommonDirSHA256 != current.Binding.CommonDirSHA256 {
		return receipt, e.advancedFailure(&receipt, gitadvanced.FailureRepositoryDrift,
			"repository identity changed after review")
	}
	if preview.Binding.UpstreamRef != current.Binding.UpstreamRef ||
		preview.Binding.UpstreamOID != current.Binding.UpstreamOID {
		return receipt, e.advancedFailure(&receipt, gitadvanced.FailureRemoteDrift,
			"upstream identity changed after review")
	}
	if preview.ProtocolVersion != gitadvanced.PreviewProtocolVersion ||
		preview.Operation != preview.Spec.Operation || preview.ID != current.ID ||
		!preview.Binding.SameState(current.Binding) {
		return receipt, e.advancedFailure(&receipt, gitadvanced.FailureStalePreview,
			"repository or preview evidence drifted; a new review is required")
	}
	if !current.Executable() {
		return receipt, e.advancedFailure(&receipt, gitadvanced.FailureBranchProtected,
			strings.Join(current.BlockedReasons, "; "))
	}
	if (preview.Operation == gitadvanced.HunkStage ||
		preview.Operation == gitadvanced.HunkUnstage ||
		preview.Operation == gitadvanced.HunkRevert) && len(preview.Spec.HunkIDs) == 0 {
		return receipt, e.advancedFailure(&receipt, gitadvanced.FailureStalePreview,
			"hunk execution requires explicit identities selected from a discovery preview")
	}
	operationCtx, err := advancedDispatchContext(ctx, preview, guards)
	if err != nil {
		return receipt, e.advancedFailure(&receipt, gitadvanced.FailureStalePreview, err.Error())
	}
	stdout, stderr, exitCode, operationErr := e.executeAdvancedOperation(operationCtx, root,
		preview, &receipt)
	if observed := len(stdout) + len(stderr); receipt.ObservedBytes < observed {
		receipt.ObservedBytes = observed
	}
	if operationErr != nil {
		captureSummary := ""
		if post, postErr := e.CaptureAdvancedBinding(context.WithoutCancel(ctx), root); postErr == nil {
			receipt.PostBinding = post
			if conflict, conflictErr := e.captureConflictState(context.WithoutCancel(ctx), root); conflictErr == nil {
				receipt.Conflict = conflict
			} else {
				captureSummary = "; post-operation conflict capture failed: " + conflictErr.Error()
			}
		} else {
			captureSummary = "; post-operation repository capture failed: " + postErr.Error()
		}
		if ctx.Err() != nil {
			return receipt, e.advancedFailure(&receipt, gitadvanced.FailureCancelled,
				"Git operation was cancelled; persisted repository state must be inspected before recovery"+
					captureSummary)
		}
		var typed *gitadvanced.Error
		if errors.As(operationErr, &typed) && typed.Code != "" {
			return receipt, e.advancedFailure(&receipt, typed.Code,
				boundedOutput(typed.Message+captureSummary))
		}
		return receipt, e.advancedFailure(&receipt, gitadvanced.FailureGit,
			boundedOutput(operationErr.Error()+captureSummary))
	}
	post, captureErr := e.CaptureAdvancedBinding(context.WithoutCancel(ctx), root)
	if captureErr != nil {
		return receipt, e.advancedFailure(&receipt, gitadvanced.FailureGit,
			"Git mutation completed but post-state capture failed: "+captureErr.Error())
	}
	receipt.PostBinding = post
	receipt.Conflict, captureErr = e.captureConflictState(context.WithoutCancel(ctx), root)
	if captureErr != nil {
		return receipt, e.advancedFailure(&receipt, gitadvanced.FailureGit,
			"Git mutation completed but conflict-state capture failed: "+captureErr.Error())
	}
	if exitCode != 0 {
		if receipt.Conflict.Active && operationCanConflict(preview.Operation) {
			receipt.Status, receipt.ErrorCode = gitadvanced.ReceiptConflicted,
				gitadvanced.FailureConflict
			receipt.ErrorSummary = "Git operation paused with conflicts; continue, skip, or abort remains available"
			receipt.CompletedAt = e.now().UTC()
			return receipt, nil
		}
		return receipt, e.advancedFailure(&receipt, gitadvanced.FailureGit,
			"Git operation failed: "+strings.TrimSpace(boundedOutput(stderr)))
	}
	receipt.Status = gitadvanced.ReceiptSucceeded
	receipt.CompletedAt = e.now().UTC()
	return receipt, nil
}

func (e *AdvancedExecutor) advancedFailure(receipt *gitadvanced.Receipt,
	code gitadvanced.FailureCode,
	message string,
) error {
	receipt.Status, receipt.ErrorCode, receipt.ErrorSummary = gitadvanced.ReceiptFailed,
		code, boundedOutput(message)
	receipt.CompletedAt = time.Now().UTC()
	if e != nil && e.now != nil {
		receipt.CompletedAt = e.now().UTC()
	}
	return &gitadvanced.Error{Code: code, Message: message}
}

func (e *AdvancedExecutor) git(ctx context.Context, root string, stdin []byte,
	args ...string,
) (string, string, int, error) {
	duration := e.maxDuration
	if duration <= 0 {
		duration = MaxAdvancedGitDuration
	}
	commandCtx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	commandContext := e.commandContext
	if commandContext == nil {
		commandContext = repositoryCommandContext
	}
	base := []string{"-C", root, "--no-optional-locks", "-c", "core.autocrlf=false",
		"-c", "submodule.recurse=false", "-c", "rebase.autoStash=false",
		"-c", "rebase.updateRefs=false", "-c", "rerere.enabled=false",
		"-c", "rerere.autoupdate=false", "--literal-pathspecs"}
	command := commandContext(commandCtx, e.gitPath, append(base, args...)...)
	command.Dir, command.Env = root, hardenedGitEnvironment()
	if stdin != nil {
		command.Stdin = bytes.NewReader(stdin)
	}
	stdout := advancedBoundedBuffer{max: MaxAdvancedTrackedBytes + 1}
	stderr := advancedBoundedBuffer{max: MaxGitOutputBytes}
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := checkAdvancedDispatch(commandCtx); err != nil {
		return "", "", 0, err
	}
	if err := checkThreadGitDispatch(commandCtx); err != nil {
		return "", "", 0, err
	}
	err := command.Run()
	if commandCtx.Err() != nil {
		return stdout.String(), stderr.String(), 0, commandCtx.Err()
	}
	if stdout.truncated || stderr.truncated {
		return stdout.String(), stderr.String(), 0,
			errors.New("Git output exceeded its protocol bound")
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return stdout.String(), stderr.String(), exitErr.ExitCode(), nil
		}
		return stdout.String(), stderr.String(), 0, err
	}
	return stdout.String(), stderr.String(), 0, nil
}

type advancedBoundedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (b *advancedBoundedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := b.max - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return original, nil
	}
	if len(value) > remaining {
		value = value[:remaining]
		b.truncated = true
	}
	_, _ = b.buf.Write(value)
	return original, nil
}

func (b *advancedBoundedBuffer) String() string { return b.buf.String() }

func (e *AdvancedExecutor) requiredPathOutput(ctx context.Context, root string,
	args ...string,
) (string, error) {
	stdout, stderr, code, err := e.git(ctx, root, nil, args...)
	if err != nil || code != 0 {
		return "", fmt.Errorf("Git metadata lookup failed: %w: %s", err,
			strings.TrimSpace(stderr))
	}
	value := strings.TrimSpace(stdout)
	if value == "" || strings.ContainsRune(value, 0) {
		return "", errors.New("Git metadata lookup returned an invalid path")
	}
	return value, nil
}

func (e *AdvancedExecutor) captureWorktreeDigest(ctx context.Context, root,
	status string,
) (string, error) {
	unstaged, stderr, code, err := e.git(ctx, root, nil, "diff", "--no-ext-diff",
		"--no-renames", "--binary", "--full-index", "--")
	if err != nil || code != 0 {
		return "", fmt.Errorf("capture worktree diff: %w: %s", err, strings.TrimSpace(stderr))
	}
	staged, stderr, code, err := e.git(ctx, root, nil, "diff", "--cached",
		"--no-ext-diff", "--no-renames", "--binary", "--full-index", "--")
	if err != nil || code != 0 {
		return "", fmt.Errorf("capture index diff: %w: %s", err, strings.TrimSpace(stderr))
	}
	untracked, stderr, code, err := e.git(ctx, root, nil, "ls-files", "-z",
		"--others", "--exclude-standard")
	if err != nil || code != 0 {
		return "", fmt.Errorf("capture untracked files: %w: %s", err, strings.TrimSpace(stderr))
	}
	hash := sha256.New()
	for _, value := range []string{status, unstaged, staged} {
		_, _ = fmt.Fprintf(hash, "%d:", len(value))
		_, _ = hash.Write([]byte(value))
	}
	total, count := 0, 0
	for _, path := range strings.Split(untracked, "\x00") {
		if path == "" {
			continue
		}
		if !safeGitRelativePath(path) || count >= MaxChangeItems {
			return "", errors.New("untracked worktree state exceeds safe capture bounds")
		}
		data, readErr := readBoundedWorktreeFile(root, path,
			MaxAdvancedTrackedBytes-total)
		if readErr != nil {
			return "", readErr
		}
		total += len(data)
		_, _ = hash.Write([]byte(path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
		count++
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func canonicalManagedWorktreeRoot(root string, enabled bool) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		if enabled {
			return "", errors.New("enabled Git advanced capability requires a managed worktree root")
		}
		return "disabled", nil
	}
	abs, err := filepath.Abs(root)
	if err != nil || strings.ContainsRune(root, 0) {
		return "", errors.New("managed worktree root is invalid")
	}
	if !enabled {
		return filepath.Clean(abs), nil
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", fmt.Errorf("create managed worktree root: %w", err)
	}
	info, err := os.Lstat(abs)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("managed worktree root must be a real directory")
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil || !sameFilesystemPath(abs, resolved) {
		return "", errors.New("managed worktree root must not traverse a link or reparse point")
	}
	return filepath.Clean(resolved), nil
}

func canonicalGitMetadataPath(value string) (string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(value))
	if err != nil || strings.ContainsRune(value, 0) {
		return "", errors.New("Git metadata path is invalid")
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve Git metadata path: %w", err)
	}
	return filepath.Clean(resolved), nil
}

func randomCapabilityGeneration() (string, error) {
	value := make([]byte, sha256.Size)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("create Git capability generation: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func digestOptionalRegularFile(path string, max int) (string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		sum := sha256.Sum256(nil)
		return hex.EncodeToString(sum[:]), nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() > int64(max) {
		return "", errors.New("Git index is not a bounded regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func digestGitSequenceState(gitDir string) (string, error) {
	hash := sha256.New()
	total := 0
	for _, name := range []string{"CHERRY_PICK_HEAD", "REVERT_HEAD", "MERGE_HEAD",
		"BISECT_START", "BISECT_LOG", "BISECT_NAMES", "sequencer", "rebase-merge", "rebase-apply"} {
		path := filepath.Join(gitDir, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("Git sequencer metadata is unsafe")
		}
		if info.Mode().IsRegular() {
			data, readErr := os.ReadFile(path)
			if readErr != nil || total+len(data) > MaxAdvancedSequenceBytes {
				return "", errors.New("Git sequencer metadata exceeds safe bounds")
			}
			total += len(data)
			_, _ = hash.Write([]byte(name + "\x00"))
			_, _ = hash.Write(data)
			continue
		}
		if !info.IsDir() {
			return "", errors.New("Git sequencer metadata has an unsafe type")
		}
		var paths []string
		walkErr := filepath.WalkDir(path, func(candidate string, entry fs.DirEntry,
			walkErr error,
		) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("Git sequencer metadata contains a link")
			}
			if entry.IsDir() {
				return nil
			}
			if !entry.Type().IsRegular() {
				return errors.New("Git sequencer metadata contains an unsafe file")
			}
			paths = append(paths, candidate)
			if len(paths) > 256 {
				return errors.New("Git sequencer metadata exceeds file bound")
			}
			return nil
		})
		if walkErr != nil {
			return "", walkErr
		}
		sort.Strings(paths)
		for _, candidate := range paths {
			data, readErr := os.ReadFile(candidate)
			if readErr != nil || total+len(data) > MaxAdvancedSequenceBytes {
				return "", errors.New("Git sequencer metadata exceeds safe bounds")
			}
			relative, _ := filepath.Rel(gitDir, candidate)
			total += len(data)
			_, _ = hash.Write([]byte(filepath.ToSlash(relative) + "\x00"))
			_, _ = hash.Write(data)
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func readBoundedWorktreeFile(root, relative string, remaining int) ([]byte, error) {
	if remaining < 0 || !safeGitRelativePath(relative) {
		return nil, errors.New("worktree content exceeds safe capture bounds")
	}
	path := filepath.Join(root, filepath.FromSlash(relative))
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() > int64(remaining) {
		return nil, errors.New("untracked worktree entry is not a bounded regular file")
	}
	if err := validateAdvancedPathParent(root, filepath.Dir(path)); err != nil {
		return nil, errors.New("untracked worktree entry traverses a link or reparse point")
	}
	return os.ReadFile(path)
}

func validateAdvancedPathParent(root, parent string) error {
	root, parent = filepath.Clean(root), filepath.Clean(parent)
	relative, err := filepath.Rel(root, parent)
	if err != nil || relative == ".." || strings.HasPrefix(relative,
		".."+string(filepath.Separator)) {
		return errors.New("path parent is outside the repository root")
	}
	if relative == "." {
		return nil
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("path parent contains a link or reparse point")
		}
		resolved, resolveErr := filepath.EvalSymlinks(current)
		if resolveErr != nil || !sameFilesystemPath(current, resolved) ||
			!pathInsideRoot(root, resolved) {
			return errors.New("path parent contains a redirected component")
		}
	}
	return nil
}

func safeGitRelativePath(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || filepath.IsAbs(value) ||
		strings.ContainsRune(value, 0) || strings.ContainsAny(value, "\\:") ||
		value != filepath.ToSlash(filepath.Clean(value)) || value == "." || value == ".." ||
		strings.HasPrefix(value, "../") {
		return false
	}
	for _, current := range value {
		if current < 0x20 || current == 0x7f {
			return false
		}
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component != strings.TrimSpace(component) ||
			strings.EqualFold(component, ".git") {
			return false
		}
	}
	return true
}

func pathInsideRoot(root, value string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(value))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func validObservedRef(value string) bool {
	return value != "" && len(value) <= 1024 && !strings.ContainsRune(value, 0) &&
		!strings.HasPrefix(value, "-") && !strings.ContainsAny(value, " \t\r\n")
}

func deduplicateStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func advancedPolicyBlocks(spec gitadvanced.Spec, binding gitadvanced.RepositoryBinding,
	conflict gitadvanced.ConflictState,
) []string {
	var blocked []string
	if conflict.Active && !isSequenceContinuation(spec.Operation) {
		blocked = append(blocked, "repository already has unresolved conflicts")
	}
	if binding.Head == "unborn" && sequenceStartOperation(spec.Operation) {
		blocked = append(blocked, "Git sequence start requires an existing exact HEAD commit")
	}
	if conflict.CanAbort && sequenceStartOperation(spec.Operation) {
		blocked = append(blocked, "another Git sequence is already active")
	}
	if branchHistoryOperation(spec.Operation) {
		if binding.Detached || binding.Branch == "" {
			blocked = append(blocked, "branch history mutation requires an attached local branch")
		}
		if protectedBranch(binding.Branch) {
			blocked = append(blocked, "protected branch history cannot be changed")
		}
		if spec.Operation == gitadvanced.RebaseStart && binding.UpstreamRef != "" {
			blocked = append(blocked, "branches with a configured upstream are shared and cannot be rewritten")
		}
	}
	if strings.TrimSpace(binding.UpstreamRef) != "" && binding.UpstreamOID == "" {
		blocked = append(blocked, "upstream identity could not be resolved")
	}
	return blocked
}

func sequenceStartOperation(operation gitadvanced.Operation) bool {
	return operation == gitadvanced.RebaseStart ||
		operation == gitadvanced.CherryPickStart || operation == gitadvanced.BisectStart
}

func protectedBranch(branch string) bool {
	switch strings.ToLower(strings.TrimSpace(branch)) {
	case "main", "master", "trunk", "production", "prod", "release":
		return true
	default:
		return strings.HasPrefix(strings.ToLower(branch), "release/")
	}
}

func branchHistoryOperation(operation gitadvanced.Operation) bool {
	switch operation {
	case gitadvanced.RebaseStart, gitadvanced.CherryPickStart:
		return true
	default:
		return false
	}
}

func isSequenceContinuation(operation gitadvanced.Operation) bool {
	switch operation {
	case gitadvanced.RebaseContinue, gitadvanced.RebaseSkip, gitadvanced.RebaseAbort,
		gitadvanced.CherryPickContinue, gitadvanced.CherryPickSkip,
		gitadvanced.CherryPickAbort, gitadvanced.BisectGood, gitadvanced.BisectBad,
		gitadvanced.BisectSkip, gitadvanced.BisectRun, gitadvanced.BisectReset:
		return true
	default:
		return false
	}
}

func operationCanConflict(operation gitadvanced.Operation) bool {
	switch operation {
	case gitadvanced.StashApply, gitadvanced.StashPop,
		gitadvanced.RebaseStart, gitadvanced.RebaseContinue,
		gitadvanced.CherryPickStart, gitadvanced.CherryPickContinue:
		return true
	default:
		return false
	}
}
