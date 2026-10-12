package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"
)

const (
	MaxBatchValidationDuration = 10 * time.Minute
	maxBatchGitMetadataBytes   = 2 * 1024 * 1024
)

// BatchDeliveryInspection is computed from the full merge-base diff. It
// intentionally carries digests and bounded metadata rather than raw source.
type BatchDeliveryInspection struct {
	BaseCommit      string
	HeadCommit      string
	Branch          string
	DiffSHA256      string
	CallChainSHA256 string
	DiffBytes       int64
	DiffStat        string
	ChangedFiles    []string
	Clean           bool
}

// InspectBatchDelivery verifies one exact branch-backed worktree and hashes
// the complete merge-base diff and function-context view. Dirty worktrees,
// unrelated histories, detached heads, empty deliveries, and oversized
// changes fail closed before a receipt can be issued.
func InspectBatchDelivery(ctx context.Context, root, expectedBranch, baseCommit string,
	maxChangedFiles int, maxDiffBytes int64,
) (BatchDeliveryInspection, error) {
	if ctx == nil || ctx.Err() != nil {
		return BatchDeliveryInspection{}, errors.New("batch delivery inspection context is unavailable")
	}
	if err := ValidateBranchName(expectedBranch); err != nil || !validWorktreeCommit(baseCommit) {
		return BatchDeliveryInspection{}, errors.New("batch delivery Git identity is invalid")
	}
	if maxChangedFiles <= 0 || maxChangedFiles > domain.MaxBatchChangedFiles ||
		maxDiffBytes <= 0 || maxDiffBytes > domain.MaxBatchDiffBytes {
		return BatchDeliveryInspection{}, errors.New("batch delivery inspection limits are invalid")
	}
	root, err := canonicalExistingRepositoryRoot(root)
	if err != nil {
		return BatchDeliveryInspection{}, err
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return BatchDeliveryInspection{}, fmt.Errorf("git binary is unavailable: %w", err)
	}
	status, err := batchGitOutput(ctx, gitPath, root, "status", "--porcelain=v1", "-z",
		"--untracked-files=all")
	if err != nil {
		return BatchDeliveryInspection{}, err
	}
	if len(status) != 0 {
		return BatchDeliveryInspection{}, errors.New("batch delivery worktree is dirty; commit or discard every change before delivery")
	}
	branch, err := batchGitOutput(ctx, gitPath, root, "branch", "--show-current")
	if err != nil || strings.TrimSpace(string(branch)) != expectedBranch {
		return BatchDeliveryInspection{}, errors.New("batch delivery worktree branch drifted")
	}
	head, err := batchGitOutput(ctx, gitPath, root, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return BatchDeliveryInspection{}, err
	}
	headCommit := strings.TrimSpace(string(head))
	if !validWorktreeCommit(headCommit) || headCommit == baseCommit {
		return BatchDeliveryInspection{}, errors.New("batch delivery requires a non-empty committed change")
	}
	mergeBase, err := batchGitOutput(ctx, gitPath, root, "merge-base", baseCommit, headCommit)
	if err != nil || strings.TrimSpace(string(mergeBase)) != baseCommit {
		return BatchDeliveryInspection{}, errors.New("batch delivery head is not based on the assigned base commit")
	}
	nameStatus, err := batchGitOutput(ctx, gitPath, root, "diff", "--name-status", "-z",
		"--find-renames", baseCommit+"..."+headCommit, "--")
	if err != nil {
		return BatchDeliveryInspection{}, err
	}
	changedFiles, err := parseBatchChangedFiles(nameStatus, maxChangedFiles)
	if err != nil {
		return BatchDeliveryInspection{}, err
	}
	if len(changedFiles) == 0 {
		return BatchDeliveryInspection{}, errors.New("batch delivery merge-base diff is empty")
	}
	diffHash, diffBytes, err := batchGitDigest(ctx, gitPath, root, maxDiffBytes,
		"diff", "--full-index", "--binary", "--no-ext-diff", baseCommit+"..."+headCommit, "--")
	if err != nil {
		return BatchDeliveryInspection{}, err
	}
	callHash, _, err := batchGitDigest(ctx, gitPath, root, maxDiffBytes,
		"diff", "--full-index", "--function-context", "--no-ext-diff",
		baseCommit+"..."+headCommit, "--")
	if err != nil {
		return BatchDeliveryInspection{}, err
	}
	stat, err := batchGitOutput(ctx, gitPath, root, "diff", "--shortstat",
		baseCommit+"..."+headCommit, "--")
	if err != nil || strings.TrimSpace(string(stat)) == "" {
		return BatchDeliveryInspection{}, errors.New("batch delivery diffstat is unavailable")
	}
	return BatchDeliveryInspection{BaseCommit: baseCommit, HeadCommit: headCommit,
		Branch: expectedBranch, DiffSHA256: diffHash, CallChainSHA256: callHash,
		DiffBytes: diffBytes, DiffStat: strings.TrimSpace(string(stat)),
		ChangedFiles: changedFiles, Clean: true}, nil
}

func parseBatchChangedFiles(raw []byte, limit int) ([]string, error) {
	parts := bytes.Split(raw, []byte{0})
	unique := make(map[string]struct{}, limit)
	for index := 0; index < len(parts); {
		if len(parts[index]) == 0 {
			index++
			continue
		}
		status := string(parts[index])
		index++
		pathCount := 1
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			pathCount = 2
		}
		for pathIndex := 0; pathIndex < pathCount; pathIndex++ {
			if index >= len(parts) || len(parts[index]) == 0 {
				return nil, errors.New("batch delivery changed-file stream is malformed")
			}
			value := filepath.ToSlash(string(parts[index]))
			index++
			if strings.ContainsRune(value, 0) || filepath.IsAbs(value) || value == ".." ||
				strings.HasPrefix(value, "../") {
				return nil, errors.New("batch delivery changed path escapes the repository")
			}
			unique[value] = struct{}{}
			if len(unique) > limit {
				return nil, fmt.Errorf("batch delivery changed files exceed %d", limit)
			}
		}
	}
	out := make([]string, 0, len(unique))
	for value := range unique {
		out = append(out, value)
	}
	sort.Strings(out)
	return out, nil
}

type batchDigestWriter struct {
	hash  hash.Hash
	bytes int64
	limit int64
}

func (w *batchDigestWriter) Write(value []byte) (int, error) {
	if w.bytes+int64(len(value)) > w.limit {
		return 0, fmt.Errorf("batch delivery diff exceeds %d bytes", w.limit)
	}
	w.bytes += int64(len(value))
	return w.hash.Write(value)
}

func batchGitDigest(ctx context.Context, gitPath, root string, limit int64,
	args ...string,
) (string, int64, error) {
	commandCtx, cancel := context.WithTimeout(ctx, MaxGitDuration)
	defer cancel()
	command := exec.CommandContext(commandCtx, gitPath,
		append([]string{"-C", root, "--no-optional-locks"}, args...)...)
	command.Dir, command.Env = root, hardenedGitEnvironment()
	writer := &batchDigestWriter{hash: sha256.New(), limit: limit}
	var stderr boundedBuffer
	command.Stdout, command.Stderr = writer, &stderr
	if err := command.Run(); err != nil {
		if commandCtx.Err() != nil {
			return "", writer.bytes, commandCtx.Err()
		}
		return "", writer.bytes, fmt.Errorf("git %s: %w: %s", args[0], err,
			strings.TrimSpace(stderr.String()))
	}
	return hex.EncodeToString(writer.hash.Sum(nil)), writer.bytes, nil
}

func batchGitOutput(ctx context.Context, gitPath, root string, args ...string) ([]byte, error) {
	return batchGitOutputLimit(ctx, gitPath, root, maxBatchGitMetadataBytes, args...)
}

// CurrentFullHead returns a full object id from an exact repository root.
func CurrentFullHead(ctx context.Context, root string) (string, error) {
	root, err := canonicalExistingRepositoryRoot(root)
	if err != nil {
		return "", err
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", err
	}
	raw, err := batchGitOutput(ctx, gitPath, root, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", err
	}
	head := strings.TrimSpace(string(raw))
	if !validWorktreeCommit(head) {
		return "", errors.New("repository HEAD is not a full object id")
	}
	return head, nil
}

// CurrentBranch returns the exact checked-out branch and rejects detached
// HEADs. It is used only for durable internal bindings, not public display.
func CurrentBranch(ctx context.Context, root string) (string, error) {
	root, err := canonicalExistingRepositoryRoot(root)
	if err != nil {
		return "", err
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", err
	}
	raw, err := batchGitOutput(ctx, gitPath, root, "branch", "--show-current")
	if err != nil {
		return "", err
	}
	branch := strings.TrimSpace(string(raw))
	if ValidateBranchName(branch) != nil {
		return "", errors.New("repository requires a valid checked-out branch")
	}
	return branch, nil
}

// IsAncestor verifies a replay base relationship using the real object graph.
func IsAncestor(ctx context.Context, root, ancestor, descendant string) (bool, error) {
	if !validWorktreeCommit(ancestor) || !validWorktreeCommit(descendant) {
		return false, errors.New("Git ancestor identities are invalid")
	}
	root, err := canonicalExistingRepositoryRoot(root)
	if err != nil {
		return false, err
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return false, err
	}
	commandCtx, cancel := context.WithTimeout(ctx, MaxGitDuration)
	defer cancel()
	command := exec.CommandContext(commandCtx, gitPath, "-C", root,
		"--no-optional-locks", "merge-base", "--is-ancestor", ancestor, descendant)
	command.Dir, command.Env = root, hardenedGitEnvironment()
	var stderr boundedBuffer
	command.Stderr = &stderr
	err = command.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	if commandCtx.Err() != nil {
		return false, commandCtx.Err()
	}
	return false, fmt.Errorf("inspect Git ancestry: %w: %s", err,
		strings.TrimSpace(stderr.String()))
}

// VerifyBatchWorktree verifies the exact branch and current head without
// accepting a symlinked or overlapping destination.
func VerifyBatchWorktree(ctx context.Context, root, branch, head string) error {
	if err := ValidateBranchName(branch); err != nil || !validWorktreeCommit(head) {
		return errors.New("batch worktree verification identity is invalid")
	}
	canonical, err := canonicalExistingRepositoryRoot(root)
	if err != nil {
		return err
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return err
	}
	return verifyCreatedWorktree(ctx, gitPath, canonical, branch, head)
}

// VerifyBatchWorktreeBinding proves that a persisted worktree still names the
// same common Git repository, exact branch/head, and (when requested) clean
// state as the source checkout. It is used immediately before durable state
// transitions so path or repository replacement cannot satisfy only the
// branch/OID portion of the contract.
func VerifyBatchWorktreeBinding(ctx context.Context, sourceRoot, root, branch,
	head string, requireClean bool,
) error {
	source, err := canonicalExistingRepositoryRoot(sourceRoot)
	if err != nil {
		return err
	}
	worktree, err := canonicalExistingRepositoryRoot(root)
	if err != nil || pathsOverlap(source, worktree) {
		return errors.New("batch worktree binding root is invalid")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return err
	}
	if err := verifyCreatedWorktree(ctx, gitPath, worktree, branch, head); err != nil {
		return err
	}
	if err := verifyWorktreeCommonDirectory(ctx, gitPath, source, worktree); err != nil {
		return err
	}
	if requireClean {
		status, err := batchGitOutput(ctx, gitPath, worktree, "status", "--porcelain=v1",
			"-z", "--untracked-files=all")
		if err != nil || len(status) != 0 {
			return errors.New("batch worktree is not clean")
		}
	}
	return nil
}

// RemoveWorktreeKeepBranch removes an exact clean registered worktree but
// retains its branch and commits as reviewable recovery evidence.
func RemoveWorktreeKeepBranch(ctx context.Context, sourceRoot, destinationRoot,
	branch, expectedHead string,
) error {
	if ctx == nil || ctx.Err() != nil {
		return errors.New("batch worktree cleanup context is unavailable")
	}
	if err := ValidateBranchName(branch); err != nil || !validWorktreeCommit(expectedHead) {
		return errors.New("batch worktree cleanup identity is invalid")
	}
	source, err := canonicalExistingRepositoryRoot(sourceRoot)
	if err != nil {
		return err
	}
	destination, err := canonicalExistingRepositoryRoot(destinationRoot)
	if err != nil || pathsOverlap(source, destination) {
		return errors.New("batch worktree cleanup destination is invalid")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return err
	}
	if err := verifyCreatedWorktree(ctx, gitPath, destination, branch, expectedHead); err != nil {
		return err
	}
	if err := verifyWorktreeCommonDirectory(ctx, gitPath, source, destination); err != nil {
		return err
	}
	status, err := batchGitOutput(ctx, gitPath, destination, "status", "--porcelain=v1", "-z",
		"--untracked-files=all")
	if err != nil {
		return err
	}
	if len(status) != 0 {
		return errors.New("dirty batch worktree is preserved for manual recovery")
	}
	commandCtx, cancel := context.WithTimeout(ctx, MaxGitDuration)
	defer cancel()
	command := exec.CommandContext(commandCtx, gitPath, "-C", source,
		"--no-optional-locks", "worktree", "remove", destination)
	command.Dir, command.Env = source, hardenedGitEnvironment()
	var stderr boundedBuffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("remove batch worktree: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// BatchValidationResult is an independently executed, output-digested check.
type BatchValidationResult struct {
	RequirementID  string
	Kind           domain.BatchDeliveryValidationKind
	Scope          string
	ExitCode       int
	OutputSHA256   string
	DurationMillis int64
	CompletedAt    time.Time
}

func RunBatchValidation(ctx context.Context, root, baseCommit string,
	requirement domain.BatchDeliveryValidationRequirement, starter runner.OnceStarter,
) (BatchValidationResult, error) {
	result := BatchValidationResult{RequirementID: requirement.ID, Kind: requirement.Kind,
		Scope: requirement.Scope}
	if ctx == nil || ctx.Err() != nil || !validWorktreeCommit(baseCommit) {
		return result, errors.New("batch validation context or base is invalid")
	}
	root, err := canonicalExistingRepositoryRoot(root)
	if err != nil {
		return result, err
	}
	var executable string
	var args []string
	workingRoot := root
	environment := batchValidationEnvironment(filepath.Join(os.TempDir(),
		"cyberagent-batch-validation-cache"))
	switch requirement.Kind {
	case domain.BatchValidationGitDiffCheck:
		executable, err = exec.LookPath("git")
		args = []string{"-C", root, "--no-optional-locks", "diff", "--check", baseCommit + "...HEAD", "--"}
		environment = hardenedGitEnvironment()
	case domain.BatchValidationGoTest:
		executable, err = exec.LookPath("go")
		pattern := "./..."
		if requirement.Scope != "." {
			pattern = "./" + strings.TrimSuffix(requirement.Scope, "/") + "/..."
		}
		// A receipt must describe an execution performed for this exact state,
		// not a prior success replayed from Go's test cache.
		args = []string{"test", "-count=1", pattern}
	case domain.BatchValidationNPMTest:
		executable, args, err = NPMTestLauncher(root)
		args = append(args, "test", "--", "--runInBand")
	default:
		return result, errors.New("unsupported batch validation requirement")
	}
	if err != nil {
		return result, fmt.Errorf("batch validation executable is unavailable: %w", err)
	}
	executable, err = canonicalBatchValidationExecutable(root, executable)
	if err != nil {
		return result, err
	}
	workingRoot, err = canonicalBatchValidationWorkingRoot(root, requirement.Scope)
	if err != nil {
		return result, err
	}
	cacheRoot := filepath.Join(os.TempDir(), "cyberagent-batch-validation-cache")
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		return result, errors.New("batch validation cache is unavailable")
	}
	if requirement.Kind != domain.BatchValidationGitDiffCheck {
		environment = batchValidationEnvironment(cacheRoot)
	}
	commandCtx, cancel := context.WithTimeout(ctx, MaxBatchValidationDuration)
	defer cancel()
	if starter == nil || !starter.Available() {
		return result, errors.New("batch validation process-tree boundary is unavailable")
	}
	started, runErr := starter.Start(commandCtx, runner.OnceStartSpec{
		RequestFingerprint: batchValidationFingerprint(root, baseCommit, requirement),
		ExecutablePath:     executable, Argv: args, WorkingDirectory: workingRoot,
		Environment: environment,
	})
	// A denied dispatch has no process completion evidence. Preserve the
	// authority error instead of replacing it with a missing tree receipt.
	if runErr != nil && started.StartedAt.IsZero() {
		return result, runErr
	}
	result.ExitCode = started.ExitCode
	result.CompletedAt = started.CompletedAt
	if result.CompletedAt.IsZero() {
		result.CompletedAt = time.Now().UTC()
	}
	if !started.StartedAt.IsZero() {
		result.DurationMillis = result.CompletedAt.Sub(started.StartedAt).Milliseconds()
	}
	result.OutputSHA256 = batchValidationOutputDigest(started.Stdout, started.Stderr)
	if !started.StdinClosed || !started.TreeReaped {
		return result, errors.New("batch validation process-tree completion evidence is invalid")
	}
	if runErr != nil {
		if commandCtx.Err() != nil {
			return result, fmt.Errorf("batch validation %s did not complete before its authority expired: %w",
				requirement.ID, commandCtx.Err())
		}
		return result, fmt.Errorf("batch validation %s process failed (output sha256 %s): %w",
			requirement.ID, result.OutputSHA256, runErr)
	}
	if result.ExitCode != 0 {
		return result, fmt.Errorf("batch validation %s failed with exit code %d (output sha256 %s)",
			requirement.ID, result.ExitCode, result.OutputSHA256)
	}
	return result, nil
}

func canonicalBatchValidationExecutable(root, executable string) (string, error) {
	resolved, err := filepath.EvalSymlinks(strings.TrimSpace(executable))
	if err != nil {
		return "", fmt.Errorf("resolve batch validation executable: %w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("batch validation executable is not a real regular file")
	}
	relative, err := filepath.Rel(root, resolved)
	if err == nil && (relative == "." || (relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)))) {
		return "", errors.New("batch validation cannot execute a workspace file")
	}
	return filepath.Clean(resolved), nil
}

func canonicalBatchValidationWorkingRoot(root, scope string) (string, error) {
	requested := root
	if scope != "." {
		requested = filepath.Join(root, filepath.FromSlash(scope))
	}
	requested, err := filepath.Abs(requested)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(requested)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("batch validation scope is not a real directory")
	}
	resolved, err := filepath.EvalSymlinks(requested)
	if err != nil || !sameFilesystemPath(requested, resolved) {
		return "", errors.New("batch validation scope identity changed")
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("batch validation scope escapes the integration worktree")
	}
	return filepath.Clean(resolved), nil
}

// NPMTestLauncher resolves native Node plus the installed npm CLI entrypoint.
// It never executes npm.cmd or a shell and rejects a launcher inside the workspace.
// Batch validation and project action preparation share this exact resolver.
func NPMTestLauncher(root string) (string, []string, error) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		return "", nil, err
	}
	npmPath, err := exec.LookPath("npm")
	if err != nil {
		return "", nil, err
	}
	resolvedNPM, resolveErr := filepath.EvalSymlinks(npmPath)
	candidates := make([]string, 0, 4)
	if resolveErr == nil && strings.EqualFold(filepath.Base(resolvedNPM), "npm-cli.js") {
		candidates = append(candidates, resolvedNPM)
	}
	for _, directory := range []string{filepath.Dir(npmPath), filepath.Dir(nodePath)} {
		candidates = append(candidates,
			filepath.Join(directory, "node_modules", "npm", "bin", "npm-cli.js"))
	}
	for _, candidate := range candidates {
		resolved, candidateErr := filepath.EvalSymlinks(candidate)
		if candidateErr != nil {
			continue
		}
		info, candidateErr := os.Lstat(resolved)
		if candidateErr != nil || !info.Mode().IsRegular() ||
			info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		relative, candidateErr := filepath.Rel(root, resolved)
		if candidateErr == nil && (relative == "." || (relative != ".." &&
			!strings.HasPrefix(relative, ".."+string(filepath.Separator)))) {
			continue
		}
		return nodePath, []string{filepath.Clean(resolved)}, nil
	}
	return "", nil, errors.New("npm CLI entrypoint is unavailable")
}

func batchValidationFingerprint(root, baseCommit string,
	requirement domain.BatchDeliveryValidationRequirement,
) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		"batch-validation-process.v1", root, baseCommit, requirement.ID,
		string(requirement.Kind), requirement.Scope,
	}, "\x00")))
	return hex.EncodeToString(digest[:])
}

func batchValidationOutputDigest(stdout, stderr runner.OnceOutputCapture) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf(
		"batch-validation-output.v2\x00%d\x00%d\x00%s\x00%s\x00%d\x00%d\x00%s\x00%s",
		stdout.ObservedBytes, stdout.CapturedBytes, stdout.CapturedPrefixSHA256,
		stdout.ObservedSHA256, stderr.ObservedBytes, stderr.CapturedBytes,
		stderr.CapturedPrefixSHA256, stderr.ObservedSHA256)))
	return hex.EncodeToString(digest[:])
}

func batchValidationEnvironment(cacheRoot string) []string {
	temp := os.TempDir()
	return []string{
		"PATH=" + os.Getenv("PATH"), "SystemRoot=" + os.Getenv("SystemRoot"),
		"TEMP=" + temp, "TMP=" + temp, "HOME=" + temp,
		"LOCALAPPDATA=" + temp, "GOCACHE=" + filepath.Join(cacheRoot, "go-build"),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never",
		"GOPROXY=off", "GOSUMDB=off", "GONOSUMDB=*",
		"npm_config_offline=true", "npm_config_audit=false", "npm_config_fund=false",
		"npm_config_cache=" + filepath.Join(cacheRoot, "npm"),
		"NO_COLOR=1", "CI=1",
	}
}

// MergeBatchDeliveryStep applies one reviewed child head to an isolated
// integration worktree. A failed merge is aborted and hard-reset only inside
// that exact integration worktree, leaving source and child worktrees intact.
func MergeBatchDeliveryStep(ctx context.Context, integrationRoot, expectedBranch,
	expectedPreHead, childHead string, ordinal int,
) (string, bool, error) {
	if ctx == nil || ctx.Err() != nil || ordinal < 1 || ordinal > domain.MaxBatchDeliveryTasks ||
		!validWorktreeCommit(expectedPreHead) || !validWorktreeCommit(childHead) ||
		ValidateBranchName(expectedBranch) != nil {
		return "", false, errors.New("batch merge step identity is invalid")
	}
	root, err := canonicalExistingRepositoryRoot(integrationRoot)
	if err != nil {
		return "", false, err
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", false, err
	}
	if err := verifyCreatedWorktree(ctx, gitPath, root, expectedBranch, expectedPreHead); err != nil {
		return "", false, err
	}
	status, err := batchGitOutput(ctx, gitPath, root, "status", "--porcelain=v1", "-z")
	if err != nil || len(status) != 0 {
		return "", false, errors.New("batch integration worktree is not clean")
	}
	message := fmt.Sprintf("merge batch delivery task %d", ordinal)
	mergeErr := runBatchGitMutation(ctx, gitPath, root, "merge", "--no-ff", "--no-commit", childHead)
	if mergeErr != nil {
		_ = runBatchGitMutation(context.WithoutCancel(ctx), gitPath, root, "merge", "--abort")
		_ = runBatchGitMutation(context.WithoutCancel(ctx), gitPath, root, "reset", "--hard", expectedPreHead)
		return "", true, fmt.Errorf("batch merge conflict: %w", mergeErr)
	}
	if err := runBatchGitMutation(ctx, gitPath, root, "diff", "--check", "--cached"); err != nil {
		_ = runBatchGitMutation(context.WithoutCancel(ctx), gitPath, root, "merge", "--abort")
		_ = runBatchGitMutation(context.WithoutCancel(ctx), gitPath, root, "reset", "--hard", expectedPreHead)
		return "", false, fmt.Errorf("batch merge diff check failed: %w", err)
	}
	if err := runBatchGitMutation(ctx, gitPath, root, "-c", "user.name=CyberAgent Reviewer",
		"-c", "user.email=reviewer@cyberagent.invalid", "commit", "--no-gpg-sign", "-m", message); err != nil {
		_ = runBatchGitMutation(context.WithoutCancel(ctx), gitPath, root, "merge", "--abort")
		_ = runBatchGitMutation(context.WithoutCancel(ctx), gitPath, root, "reset", "--hard", expectedPreHead)
		return "", false, fmt.Errorf("batch merge commit failed: %w", err)
	}
	post, err := CurrentFullHead(ctx, root)
	if err != nil || post == expectedPreHead {
		return "", false, errors.New("batch merge step did not advance integration HEAD")
	}
	return post, false, nil
}

// VerifyBatchMergeCommit accepts recovery only for the exact deterministic
// merge result produced by MergeBatchDeliveryStep. An arbitrary clean
// descendant containing the child head is not sufficient evidence.
func VerifyBatchMergeCommit(ctx context.Context, integrationRoot, expectedBranch,
	expectedPreHead, childHead, actualHead string, ordinal int,
) error {
	if ctx == nil || ctx.Err() != nil || ordinal < 1 ||
		ordinal > domain.MaxBatchDeliveryTasks || ValidateBranchName(expectedBranch) != nil ||
		!validWorktreeCommit(expectedPreHead) || !validWorktreeCommit(childHead) ||
		!validWorktreeCommit(actualHead) {
		return errors.New("batch recovered merge identity is invalid")
	}
	root, err := canonicalExistingRepositoryRoot(integrationRoot)
	if err != nil {
		return err
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return err
	}
	if err := verifyCreatedWorktree(ctx, gitPath, root, expectedBranch, actualHead); err != nil {
		return err
	}
	status, err := batchGitOutput(ctx, gitPath, root, "status", "--porcelain=v1", "-z",
		"--untracked-files=all")
	if err != nil || len(status) != 0 {
		return errors.New("recovered batch merge worktree is not clean")
	}
	metadata, err := batchGitOutput(ctx, gitPath, root, "show", "-s",
		"--format=%P%x00%T%x00%an%x00%ae%x00%cn%x00%ce%x00%B", actualHead, "--")
	if err != nil {
		return err
	}
	parts := bytes.SplitN(metadata, []byte{0}, 7)
	if len(parts) != 7 {
		return errors.New("recovered batch merge metadata is malformed")
	}
	parents := strings.Fields(strings.TrimSpace(string(parts[0])))
	message := fmt.Sprintf("merge batch delivery task %d", ordinal)
	if len(parents) != 2 || parents[0] != expectedPreHead || parents[1] != childHead ||
		strings.TrimSpace(string(parts[2])) != "CyberAgent Reviewer" ||
		strings.TrimSpace(string(parts[3])) != "reviewer@cyberagent.invalid" ||
		strings.TrimSpace(string(parts[4])) != "CyberAgent Reviewer" ||
		strings.TrimSpace(string(parts[5])) != "reviewer@cyberagent.invalid" ||
		strings.TrimSpace(string(parts[6])) != message {
		return errors.New("recovered batch merge commit identity changed")
	}
	if err := validateHardenedGitRepository(ctx, gitPath, root); err != nil {
		return err
	}
	expectedTreeOutput, err := batchGitOutput(ctx, gitPath, root, "merge-tree", "--write-tree",
		expectedPreHead, childHead)
	if err != nil {
		return fmt.Errorf("reconstruct recovered batch merge tree: %w", err)
	}
	expectedTree := strings.TrimSpace(strings.SplitN(string(expectedTreeOutput), "\n", 2)[0])
	if !validWorktreeCommit(expectedTree) || strings.TrimSpace(string(parts[1])) != expectedTree {
		return errors.New("recovered batch merge tree changed")
	}
	return nil
}

// RollbackBatchIntegration restores only an exact isolated integration
// worktree after post-merge validation or source-drift failure.
func RollbackBatchIntegration(ctx context.Context, integrationRoot, expectedBranch,
	currentHead, targetHead string,
) error {
	if ctx == nil || ctx.Err() != nil || ValidateBranchName(expectedBranch) != nil ||
		!validWorktreeCommit(currentHead) || !validWorktreeCommit(targetHead) {
		return errors.New("batch integration rollback identity is invalid")
	}
	root, err := canonicalExistingRepositoryRoot(integrationRoot)
	if err != nil {
		return err
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return err
	}
	if err := verifyCreatedWorktree(ctx, gitPath, root, expectedBranch, currentHead); err != nil {
		return err
	}
	status, err := batchGitOutput(ctx, gitPath, root, "status", "--porcelain=v1", "-z",
		"--untracked-files=all")
	if err != nil || len(status) != 0 {
		return errors.New("dirty batch integration is preserved instead of rolled back")
	}
	if err := runBatchGitMutation(ctx, gitPath, root, "reset", "--hard", targetHead); err != nil {
		return err
	}
	return verifyCreatedWorktree(ctx, gitPath, root, expectedBranch, targetHead)
}

func runBatchGitMutation(ctx context.Context, gitPath, root string, args ...string) error {
	if err := validateHardenedGitRepository(ctx, gitPath, root); err != nil {
		return err
	}
	commandCtx, cancel := context.WithTimeout(ctx, MaxGitDuration)
	defer cancel()
	command := exec.CommandContext(commandCtx, gitPath,
		append([]string{"-C", root, "--no-optional-locks"}, args...)...)
	command.Dir, command.Env = root, hardenedGitEnvironment()
	var stdout, stderr boundedBuffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		if commandCtx.Err() != nil {
			return commandCtx.Err()
		}
		return fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
