package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/gitmutation"
	"cyberagent-workbench/internal/rootfs"
	"cyberagent-workbench/internal/runmutation"
)

const (
	MutationProtocolVersion = gitmutation.ProtocolVersion
	MaxMutationPaths        = 200
	MaxMutationMessageRunes = 4096
	MaxMutationBranchBytes  = 255
	MaxGitOutputBytes       = 64 * 1024
	MaxGitDuration          = 2 * time.Minute
)

// MutationOperation and its closed typed set live in the gitmutation package
// so store and application can share them without import cycles.
type MutationOperation = gitmutation.Operation

const (
	MutationStage        = gitmutation.Stage
	MutationUnstage      = gitmutation.Unstage
	MutationCommit       = gitmutation.Commit
	MutationCreateBranch = gitmutation.CreateBranch
	MutationSwitchBranch = gitmutation.SwitchBranch
)

// MutationSpec is the structured request. It contains no shell text and no
// free-form git argv.
type MutationSpec struct {
	ProtocolVersion string            `json:"protocol_version"`
	Operation       MutationOperation `json:"operation"`
	Paths           []string          `json:"paths,omitempty"`
	AllChanges      bool              `json:"all_changes,omitempty"`
	Message         string            `json:"message,omitempty"`
	Branch          string            `json:"branch,omitempty"`
}

// MutationBinding pins the exact repository state the operator reviewed.
// Any drift invalidates execution until a fresh review happens.
type MutationBinding struct {
	ProtocolVersion   string
	Root              string
	Head              string
	Branch            string
	IndexSHA256       string
	StatusFingerprint string
	CapturedAt        time.Time
}

// MutationReview is the pre-execution evidence: the bound state, the file
// list, and the bounded diff stat for the exact staged content.
type MutationReview struct {
	Binding      MutationBinding
	Changes      []Change
	StagedDiff   string
	DiffStat     string
	TargetBranch string
	TargetCommit string
}

// MutationReceipt is the post-execution verified evidence.
type MutationReceipt struct {
	ProtocolVersion    string
	Operation          MutationOperation
	PreHead            string
	PostHead           string
	Branch             string
	CommitID           string
	Conflicted         bool
	Clean              bool
	StderrPrefix       string
	ObservedBytes      int
	StartedAt          time.Time
	CompletedAt        time.Time
	BindingFingerprint string
}

// MutationExecutor runs typed operations through the real git binary with a
// hardened environment. The closed argv templates make arbitrary git
// invocation impossible.
type MutationExecutor struct {
	gitPath        string
	maxDuration    time.Duration
	commandContext func(context.Context, string, ...string) *exec.Cmd
}

func NewMutationExecutor() (*MutationExecutor, error) {
	path, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("git binary is unavailable: %w", err)
	}
	return &MutationExecutor{
		gitPath: path, maxDuration: MaxGitDuration, commandContext: repositoryCommandContext,
	}, nil
}

func (e *MutationExecutor) Available() bool { return e != nil && e.gitPath != "" }

// CaptureBinding snapshots HEAD, branch, index bytes, and the sorted porcelain
// status into an immutable fingerprint.
func (e *MutationExecutor) CaptureBinding(ctx context.Context, root string) (MutationBinding, error) {
	root, err := normalizeRepositoryRoot(root)
	if err != nil {
		return MutationBinding{}, err
	}
	// Share the complete content/metadata binding, including linked-worktree
	// Git paths. Porcelain alone does not change when a tracked file is edited
	// again between review and execution.
	advanced := &AdvancedExecutor{gitPath: e.gitPath, commandContext: e.commandContext,
		maxDuration: e.maxDuration, now: func() time.Time { return time.Now().UTC() }}
	bound, err := advanced.CaptureAdvancedBinding(ctx, root)
	if err != nil {
		return MutationBinding{}, err
	}
	return MutationBinding{
		ProtocolVersion: MutationProtocolVersion, Root: root,
		Head: bound.Head, Branch: bound.Branch,
		IndexSHA256:       bound.IndexSHA256,
		StatusFingerprint: bound.Fingerprint(),
		CapturedAt:        time.Now().UTC(),
	}, nil
}

// Review produces the pre-execution evidence for the exact spec.
func (e *MutationExecutor) Review(ctx context.Context, root string, spec MutationSpec) (MutationReview, error) {
	if err := e.validateSpec(spec); err != nil {
		return MutationReview{}, err
	}
	binding, err := e.CaptureBinding(ctx, root)
	if err != nil {
		return MutationReview{}, err
	}
	review := MutationReview{Binding: binding, TargetBranch: spec.Branch}
	state, err := e.InspectThreadGitState(ctx, root, "git-mutation")
	if err != nil {
		return MutationReview{}, err
	}
	review.Changes = state.Changes
	if len(spec.Paths) > 0 && (spec.Operation == MutationCommit || spec.Operation == MutationStage || spec.Operation == MutationUnstage) {
		var selected SelectedGitReview
		if spec.Operation == MutationCommit {
			selected, err = e.ReviewSelected(ctx, root, spec.Paths)
		} else {
			selected, err = e.ReviewIndexChange(ctx, root, spec.Paths, spec.Operation == MutationUnstage)
		}
		if err != nil {
			return MutationReview{}, err
		}
		review.StagedDiff = selected.Diff
		after, err := e.CaptureBinding(ctx, root)
		if err != nil {
			return MutationReview{}, err
		}
		if !bindingCurrent(binding, after) {
			return MutationReview{}, apperror.New(apperror.CodeConflict, "repository changed during review")
		}
		return review, nil
	}
	if spec.Operation == MutationCommit || spec.Operation == MutationStage {
		staged, err := e.gitOutput(ctx, root, "diff", "--cached", "--stat")
		if err == nil {
			review.DiffStat = staged
		}
		stagedDiff, err := e.gitOutput(ctx, root, "diff", "--cached", "--unified=3", "--")
		if err == nil {
			review.StagedDiff = stagedDiff
		}
	}
	return review, nil
}

// Execute verifies the binding is still current, runs the exact operation,
// and readbacks the repository state as evidence. A drifted binding fails
// closed and demands a fresh review.
func (e *MutationExecutor) Execute(ctx context.Context, root string, spec MutationSpec,
	binding MutationBinding,
) (MutationReceipt, error) {
	if err := e.validateSpec(spec); err != nil {
		return MutationReceipt{}, err
	}
	current, err := e.CaptureBinding(ctx, root)
	if err != nil {
		return MutationReceipt{}, err
	}
	if !bindingCurrent(binding, current) {
		return MutationReceipt{}, apperror.New(apperror.CodeFailedPrecondition,
			"repository state drifted after review; re-review is required")
	}
	started := time.Now().UTC()
	stdout, stderr, exitCode, err := e.runGit(ctx, root, spec)
	completed := time.Now().UTC()
	receipt := MutationReceipt{
		ProtocolVersion: MutationProtocolVersion, Operation: spec.Operation,
		PreHead: current.Head, Branch: current.Branch,
		StderrPrefix: boundedOutput(stderr), ObservedBytes: len(stderr) + len(stdout),
		StartedAt: started, CompletedAt: completed,
		BindingFingerprint: runmutation.Fingerprint("repository_mutation_binding.v1",
			root, current.Head, current.IndexSHA256, current.StatusFingerprint),
	}
	if exitCode != 0 {
		return receipt, apperror.New(apperror.CodeFailedPrecondition,
			"git mutation failed: "+strings.TrimSpace(boundedOutput(stderr)))
	}
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return receipt, apperror.New(apperror.CodeDeadlineExceeded,
			"git mutation exceeded its duration bound")
	}
	if err != nil && ctx.Err() == nil {
		return receipt, apperror.Wrap(apperror.CodeFailedPrecondition,
			"git mutation failed: "+strings.TrimSpace(boundedOutput(stderr)), err)
	}
	if ctx.Err() != nil {
		return receipt, ctx.Err()
	}
	post, err := e.CaptureBinding(ctx, root)
	if err != nil {
		return receipt, err
	}
	receipt.PostHead = post.Head
	receipt.Clean = len(strings.TrimSpace(mustStatus(e, ctx, root))) == 0
	receipt.Conflicted = hasConflicts(e, ctx, root)
	if spec.Operation == MutationCommit {
		receipt.CommitID = post.Head
		if post.Head == current.Head {
			return receipt, apperror.New(apperror.CodeFailedPrecondition,
				"commit produced no new commit")
		}
	}
	return receipt, nil
}

func (e *MutationExecutor) runGit(ctx context.Context, root string, spec MutationSpec) (string, string, int, error) {
	if err := rootfs.CheckExternalDirectory(root); err != nil {
		return "", "", 0, err
	}
	args, err := e.argvFor(root, spec)
	if err != nil {
		return "", "", 0, err
	}
	duration := e.maxDuration
	if duration <= 0 {
		duration = MaxGitDuration
	}
	commandCtx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	commandContext := e.commandContext
	if commandContext == nil {
		commandContext = repositoryCommandContext
	}
	command := commandContext(commandCtx, e.gitPath, args...)
	command.Dir = root
	command.Env = hardenedGitEnvironment()
	var stdout, stderr boundedBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := checkThreadGitDispatch(commandCtx); err != nil {
		return "", "", 0, err
	}
	err = command.Run()
	if commandErr := commandCtx.Err(); commandErr != nil {
		return stdout.String(), stderr.String(), 0, commandErr
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

func (e *MutationExecutor) argvFor(root string, spec MutationSpec) ([]string, error) {
	base := []string{"-C", root, "--no-optional-locks", "-c", "core.autocrlf=false", "--literal-pathspecs"}
	paths, err := normalizeMutationPaths(spec.Paths)
	if err != nil {
		return nil, err
	}
	switch spec.Operation {
	case MutationStage:
		if spec.AllChanges {
			return append(base, "add", "--all"), nil
		}
		if len(paths) == 0 {
			return nil, errors.New("stage requires at least one path or all_changes")
		}
		return append(append(base, "add", "--"), paths...), nil
	case MutationUnstage:
		if spec.AllChanges {
			return append(base, "reset", "--quiet", "HEAD", "--"), nil
		}
		if len(paths) == 0 {
			return nil, errors.New("unstage requires at least one path or all_changes")
		}
		return append(append(base, "reset", "--quiet", "HEAD", "--"), paths...), nil
	case MutationCommit:
		if strings.TrimSpace(spec.Message) == "" || utf8.RuneCountInString(spec.Message) > MaxMutationMessageRunes ||
			!utf8.ValidString(spec.Message) || strings.ContainsRune(spec.Message, 0) {
			return nil, errors.New("commit message must be bounded valid UTF-8")
		}
		args := append(base, "commit", "--quiet", "-m", spec.Message)
		if len(paths) > 0 {
			args = append(append(args, "--"), paths...)
		}
		return args, nil
	case MutationCreateBranch:
		if err := validateBranchName(spec.Branch); err != nil {
			return nil, err
		}
		return append(base, "branch", "--", spec.Branch), nil
	case MutationSwitchBranch:
		if err := validateBranchName(spec.Branch); err != nil {
			return nil, err
		}
		return append(base, "switch", "--quiet", "--", spec.Branch), nil
	default:
		return nil, errors.New("unsupported mutation operation")
	}
}

func (e *MutationExecutor) validateSpec(spec MutationSpec) error {
	if spec.ProtocolVersion != MutationProtocolVersion {
		return fmt.Errorf("unsupported mutation protocol %q", spec.ProtocolVersion)
	}
	if !spec.Operation.Valid() {
		return fmt.Errorf("unsupported mutation operation %q", spec.Operation)
	}
	if spec.AllChanges && len(spec.Paths) > 0 {
		return errors.New("all_changes cannot be combined with explicit paths")
	}
	if spec.Operation == MutationCommit {
		if strings.TrimSpace(spec.Message) == "" {
			return errors.New("commit requires a message")
		}
	} else if spec.Operation == MutationCreateBranch || spec.Operation == MutationSwitchBranch {
		if err := validateBranchName(spec.Branch); err != nil {
			return err
		}
	}
	_, err := normalizeMutationPaths(spec.Paths)
	return err
}

func normalizeMutationPaths(paths []string) ([]string, error) {
	if len(paths) > MaxMutationPaths {
		return nil, fmt.Errorf("mutation paths exceed %d entries", MaxMutationPaths)
	}
	normalized := make([]string, 0, len(paths))
	for _, value := range paths {
		if value == "" || len(value) > MaxPathRunes {
			return nil, errors.New("mutation path is invalid")
		}
		if strings.HasPrefix(value, "-") {
			return nil, fmt.Errorf("mutation path %q escapes the typed contract", value)
		}
		if err := checkNativeGitPath(value); err != nil {
			return nil, apperror.New(apperror.CodeInvalidArgument, err.Error())
		}
		normalized = append(normalized, value)
	}
	return normalized, nil
}

func validateBranchName(branch string) error {
	if branch == "" || len(branch) > MaxMutationBranchBytes || !utf8.ValidString(branch) ||
		strings.ContainsRune(branch, 0) || strings.ContainsAny(branch, " \\~^:?*[\"\\") ||
		strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") ||
		strings.HasSuffix(branch, ".") || strings.Contains(branch, "..") || strings.Contains(branch, "//") ||
		strings.Contains(branch, "@{") {
		return fmt.Errorf("branch name %q is invalid", branch)
	}
	return nil
}

func bindingCurrent(left, right MutationBinding) bool {
	return left.Head == right.Head && left.IndexSHA256 == right.IndexSHA256 &&
		left.StatusFingerprint == right.StatusFingerprint
}

func (e *MutationExecutor) gitOutput(ctx context.Context, root string, args ...string) (string, error) {
	if err := rootfs.CheckExternalDirectory(root); err != nil {
		return "", err
	}
	full := append([]string{"-C", root, "--no-optional-locks", "--literal-pathspecs"}, args...)
	command := repositoryCommandContext(ctx, e.gitPath, full...)
	command.Env = hardenedGitEnvironment()
	command.Dir = root
	var stdout, stderr boundedBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err,
			strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func mustStatus(e *MutationExecutor, ctx context.Context, root string) string {
	value, err := e.gitOutput(ctx, root, "status", "--porcelain=v1")
	if err != nil {
		return ""
	}
	return value
}

func hasConflicts(e *MutationExecutor, ctx context.Context, root string) bool {
	value, err := e.gitOutput(ctx, root, "status", "--porcelain=v1")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(value, "\n") {
		if strings.HasPrefix(line, "UU ") || strings.HasPrefix(line, "AA ") ||
			strings.HasPrefix(line, "DD ") || strings.HasPrefix(line, "AU ") || strings.HasPrefix(line, "UA ") {
			return true
		}
	}
	return false
}

func normalizeRepositoryRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" || strings.ContainsRune(root, 0) {
		return "", errors.New("repository root is invalid")
	}
	root, err := rootfs.ExactAbsolutePath(root)
	if err != nil {
		return "", err
	}
	if err := rootfs.CheckExternalDirectory(root); err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return "", errors.New("repository root does not contain Git metadata")
	}
	return root, nil
}

// hardenedGitEnvironment runs git with system/global config ignored, hooks,
// pager, interactive sequence editor, external diff, credential helpers, and
// LFS filters disabled, and never inherits the agent process environment. The
// ordinary editor is the no-op `true` command so a reviewed rebase continuation
// can reuse Git's existing commit message without invoking a user editor.
func hardenedGitEnvironment() []string {
	environment := []string{
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=false",
		"GIT_EDITOR=true",
		"GIT_SEQUENCE_EDITOR=false",
		"GIT_PAGER=cat",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_LFS_SKIP_SMUDGE=1",
		"GIT_CONFIG_COUNT=8",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0=" + os.DevNull,
		"GIT_CONFIG_KEY_1=core.fsmonitor",
		"GIT_CONFIG_VALUE_1=false",
		"GIT_CONFIG_KEY_2=credential.helper",
		"GIT_CONFIG_VALUE_2=",
		"GIT_CONFIG_KEY_3=diff.external",
		"GIT_CONFIG_VALUE_3=",
		"GIT_CONFIG_KEY_4=core.autocrlf",
		"GIT_CONFIG_VALUE_4=false",
		"GIT_CONFIG_KEY_5=core.pager",
		"GIT_CONFIG_VALUE_5=cat",
		"GIT_CONFIG_KEY_6=core.attributesFile",
		"GIT_CONFIG_VALUE_6=" + os.DevNull,
		"GIT_CONFIG_KEY_7=commit.gpgSign",
		"GIT_CONFIG_VALUE_7=false",
		"LC_ALL=C",
		"LANG=C",
		"SystemRoot=" + os.Getenv("SystemRoot"),
	}
	// Git must expand repository-local include.path=~/... consistently with
	// the read-only identity preview. This is only the home path; global and
	// system config stay disabled and executable local drivers are rejected.
	home := os.Getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home != "" {
		environment = append(environment, "HOME="+home)
	}
	return environment
}

// validateHardenedGitRepository rejects repository-local executable Git
// drivers before a Go-owned checkout, merge, reset, add, or commit. Remote
// repository content can select drivers through .gitattributes; allowing a
// local clean/smudge/process, diff textconv/command, or merge driver would turn
// an otherwise typed Git mutation into implicit host code execution.
func validateHardenedGitRepository(ctx context.Context, gitPath, root string) error {
	if ctx == nil || ctx.Err() != nil {
		return errors.New("hardened Git configuration context is unavailable")
	}
	commandCtx, cancel := context.WithTimeout(ctx, MaxGitDuration)
	defer cancel()
	command := repositoryCommandContext(commandCtx, gitPath, "-C", root,
		"--no-optional-locks", "config", "--includes", "--null", "--get-regexp",
		`^(filter|diff|merge)\..*\.(clean|smudge|process|required|command|textconv|driver)$`)
	command.Dir, command.Env = root, hardenedGitEnvironment()
	var stdout, stderr boundedBuffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if err == nil {
		if stdout.buf.Len() != 0 {
			return errors.New("repository-local executable Git drivers are not allowed")
		}
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && commandCtx.Err() == nil {
		return nil // git config uses exit 1 when no key matches.
	}
	if commandCtx.Err() != nil {
		return commandCtx.Err()
	}
	return fmt.Errorf("inspect repository-local Git drivers: %w: %s", err,
		strings.TrimSpace(stderr.String()))
}

type boundedBuffer struct{ buf bytes.Buffer }

func (b *boundedBuffer) Write(value []byte) (int, error) {
	if b.buf.Len() >= MaxGitOutputBytes {
		return len(value), nil
	}
	remaining := MaxGitOutputBytes - b.buf.Len()
	if len(value) > remaining {
		value = value[:remaining]
	}
	return b.buf.Write(value)
}

func (b *boundedBuffer) String() string { return b.buf.String() }

func boundedOutput(value string) string {
	if len(value) > 4096 {
		value = value[:4096]
	}
	return value
}

var _ = sort.Strings
