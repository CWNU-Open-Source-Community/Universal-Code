// Package gitadvanced defines the closed, versioned contract for advanced
// repository operations. It intentionally contains no process execution: the
// repository package owns the fixed Git command templates and application owns
// Run authority, approval, checkpoints, leases, and persistence.
package gitadvanced

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	ProtocolVersion           = "git-advanced.v1"
	CapabilityProtocolVersion = "git-advanced-capability.v1"
	PreviewProtocolVersion    = "git-advanced-preview.v1"
	ReceiptProtocolVersion    = "git-advanced-receipt.v1"
	SequenceProtocolVersion   = "git-advanced-sequence.v1"
	WorktreeProtocolVersion   = "git-managed-worktree.v1"
	ReviewDiffProtocolVersion = "git-review-diff-evidence.v1"

	ApprovalToolName    = "git.advanced"
	ApprovalActionClass = "git_advanced_write"

	MaxHunks              = 200
	MaxPaths              = 200
	MaxReviewChangedFiles = 3000
	MaxCommits            = 128
	MaxMessageRunes       = 4096
	MaxPreviewPatchBytes  = 1024 * 1024
	MaxSpecJSONBytes      = 64 * 1024
)

// Operation is deliberately closed. There is no raw Git argv escape hatch.
type Operation string

const (
	HunkStage   Operation = "hunk_stage"
	HunkUnstage Operation = "hunk_unstage"
	HunkRevert  Operation = "hunk_revert"

	StashCreate Operation = "stash_create"
	StashApply  Operation = "stash_apply"
	StashPop    Operation = "stash_pop"
	StashDrop   Operation = "stash_drop"

	RebaseStart    Operation = "rebase_start"
	RebaseContinue Operation = "rebase_continue"
	RebaseSkip     Operation = "rebase_skip"
	RebaseAbort    Operation = "rebase_abort"

	CherryPickStart    Operation = "cherry_pick_start"
	CherryPickContinue Operation = "cherry_pick_continue"
	CherryPickSkip     Operation = "cherry_pick_skip"
	CherryPickAbort    Operation = "cherry_pick_abort"

	BisectStart Operation = "bisect_start"
	BisectGood  Operation = "bisect_good"
	BisectBad   Operation = "bisect_bad"
	BisectSkip  Operation = "bisect_skip"
	BisectRun   Operation = "bisect_run"
	BisectReset Operation = "bisect_reset"

	WorktreeCreate Operation = "worktree_create"
	WorktreeLock   Operation = "worktree_lock"
	WorktreeUnlock Operation = "worktree_unlock"
	WorktreeRemove Operation = "worktree_remove"
	WorktreePrune  Operation = "worktree_prune"
)

var operations = []Operation{
	HunkStage, HunkUnstage, HunkRevert,
	StashCreate, StashApply, StashPop, StashDrop,
	RebaseStart, RebaseContinue, RebaseSkip, RebaseAbort,
	CherryPickStart, CherryPickContinue, CherryPickSkip, CherryPickAbort,
	BisectStart, BisectGood, BisectBad, BisectSkip, BisectRun, BisectReset,
	WorktreeCreate, WorktreeLock, WorktreeUnlock, WorktreeRemove, WorktreePrune,
}

func Operations() []Operation { return append([]Operation{}, operations...) }

func (o Operation) Valid() bool {
	for _, candidate := range operations {
		if o == candidate {
			return true
		}
	}
	return false
}

func (o Operation) Destructive() bool {
	switch o {
	case HunkRevert, StashApply, StashPop, StashDrop,
		RebaseStart, RebaseContinue, RebaseSkip, RebaseAbort,
		CherryPickStart, CherryPickContinue, CherryPickSkip, CherryPickAbort,
		BisectStart, BisectGood, BisectBad, BisectSkip, BisectRun, BisectReset,
		WorktreeRemove, WorktreePrune:
		return true
	default:
		return false
	}
}

func (o Operation) RequiresCheckpoint() bool {
	return o.Valid()
}

type RecipeName string

const (
	RecipeGoTest  RecipeName = "go_test"
	RecipeNPMTest RecipeName = "npm_test"
)

func (r RecipeName) Valid() bool { return r == RecipeGoTest || r == RecipeNPMTest }

// BisectRecipe selects a Go-owned command template. Arguments and shell text
// are intentionally absent. One process belongs to one Run and one commit.
type BisectRecipe struct {
	Name           RecipeName `json:"name"`
	MaxSteps       int        `json:"max_steps"`
	TimeoutSeconds int        `json:"timeout_seconds"`
}

// Spec contains the bounded union of all operation inputs. Validation rejects
// fields which do not belong to the selected operation.
type Spec struct {
	ProtocolVersion string    `json:"protocol_version"`
	Operation       Operation `json:"operation"`

	Paths   []string `json:"paths,omitempty"`
	HunkIDs []string `json:"hunk_ids,omitempty"`

	Message          string `json:"message,omitempty"`
	IncludeUntracked bool   `json:"include_untracked,omitempty"`
	KeepIndex        bool   `json:"keep_index,omitempty"`
	RestoreIndex     bool   `json:"restore_index,omitempty"`
	StashOID         string `json:"stash_oid,omitempty"`

	SequenceID  string   `json:"sequence_id,omitempty"`
	UpstreamOID string   `json:"upstream_oid,omitempty"`
	OntoOID     string   `json:"onto_oid,omitempty"`
	Commits     []string `json:"commits,omitempty"`

	GoodCommit      string        `json:"good_commit,omitempty"`
	BadCommit       string        `json:"bad_commit,omitempty"`
	ExpectedCurrent string        `json:"expected_current,omitempty"`
	Recipe          *BisectRecipe `json:"recipe,omitempty"`

	WorktreeID   string `json:"worktree_id,omitempty"`
	WorktreeName string `json:"worktree_name,omitempty"`
	Branch       string `json:"branch,omitempty"`
	Commit       string `json:"commit,omitempty"`
	LockReason   string `json:"lock_reason,omitempty"`
}

func (s Spec) Validate() error {
	if s.ProtocolVersion != ProtocolVersion || !s.Operation.Valid() {
		return fmt.Errorf("unsupported Git advanced protocol or operation")
	}
	if len(s.Paths) > MaxPaths || len(s.HunkIDs) > MaxHunks || len(s.Commits) > MaxCommits {
		return errors.New("Git advanced request exceeds its item bound")
	}
	for _, values := range [][]string{s.Paths, s.HunkIDs, s.Commits} {
		seen := make(map[string]struct{}, len(values))
		for _, value := range values {
			if _, duplicate := seen[value]; duplicate {
				return errors.New("Git advanced request contains duplicate identities")
			}
			seen[value] = struct{}{}
		}
	}
	for _, path := range s.Paths {
		if !validRelativePath(path) {
			return fmt.Errorf("Git advanced path %q is not normalized", path)
		}
	}
	for _, id := range s.HunkIDs {
		if !ValidDigest(id) {
			return errors.New("Git hunk identity must be a lowercase SHA-256 digest")
		}
	}
	for _, oid := range append(append([]string{}, s.Commits...), s.UpstreamOID, s.OntoOID,
		s.GoodCommit, s.BadCommit, s.ExpectedCurrent, s.Commit, s.StashOID) {
		if oid != "" && !ValidObjectID(oid) {
			return errors.New("Git object identity must be an exact lowercase object id")
		}
	}
	if !boundedText(s.Message, MaxMessageRunes) || !boundedText(s.LockReason, 1024) {
		return errors.New("Git advanced message is not bounded UTF-8")
	}
	if s.Recipe != nil && (!s.Recipe.Name.Valid() || s.Recipe.MaxSteps < 1 ||
		s.Recipe.MaxSteps > 128 || s.Recipe.TimeoutSeconds < 1 || s.Recipe.TimeoutSeconds > 900) {
		return errors.New("Git bisect recipe bounds are invalid")
	}
	if err := validateOperationFields(s); err != nil {
		return err
	}
	value, err := json.Marshal(s)
	if err != nil || len(value) > MaxSpecJSONBytes {
		return errors.New("Git advanced request exceeds its encoded bound")
	}
	return nil
}

func validateOperationFields(s Spec) error {
	if unexpectedSpecFields(s) {
		return errors.New("Git advanced request contains fields outside the selected operation")
	}
	switch s.Operation {
	case HunkStage, HunkUnstage, HunkRevert:
		// An empty selection is valid for discovery/review. Execution rejects it.
	case StashCreate:
		if strings.TrimSpace(s.Message) == "" {
			return errors.New("stash create requires an audit message")
		}
	case StashApply, StashPop, StashDrop:
		if s.StashOID == "" {
			return errors.New("stash operation requires an exact stash object id")
		}
	case RebaseStart:
		if s.UpstreamOID == "" || s.OntoOID == "" {
			return errors.New("rebase start requires exact upstream and onto commits")
		}
	case RebaseContinue, RebaseSkip, RebaseAbort,
		CherryPickContinue, CherryPickSkip, CherryPickAbort:
		if !validIdentity(s.SequenceID) {
			return errors.New("sequence continuation requires a durable sequence id")
		}
	case CherryPickStart:
		if len(s.Commits) == 0 {
			return errors.New("cherry-pick start requires exact commit identities")
		}
	case BisectStart:
		if s.GoodCommit == "" || s.BadCommit == "" || s.GoodCommit == s.BadCommit {
			return errors.New("bisect start requires distinct exact good and bad commits")
		}
	case BisectGood, BisectBad, BisectSkip:
		if s.ExpectedCurrent == "" || !validIdentity(s.SequenceID) {
			return errors.New("bisect mark requires sequence and exact current commit")
		}
	case BisectRun:
		if s.ExpectedCurrent == "" || !validIdentity(s.SequenceID) || s.Recipe == nil {
			return errors.New("bisect run requires sequence, exact current commit, and recipe")
		}
	case BisectReset:
		if !validIdentity(s.SequenceID) {
			return errors.New("bisect reset requires a durable sequence id")
		}
	case WorktreeCreate:
		if !validWorktreeName(s.WorktreeName) || !validBranch(s.Branch) || s.Commit == "" {
			return errors.New("worktree create requires a safe name, branch, and exact commit")
		}
	case WorktreeLock, WorktreeUnlock, WorktreeRemove:
		if !validIdentity(s.WorktreeID) || !validWorktreeName(s.WorktreeName) {
			return errors.New("worktree operation requires durable id and safe name")
		}
	case WorktreePrune:
		// No caller-controlled path is accepted.
	}
	return nil
}

type specField uint32

const (
	specPaths specField = 1 << iota
	specHunkIDs
	specMessage
	specIncludeUntracked
	specKeepIndex
	specRestoreIndex
	specStashOID
	specSequenceID
	specUpstreamOID
	specOntoOID
	specCommits
	specGoodCommit
	specBadCommit
	specExpectedCurrent
	specRecipe
	specWorktreeID
	specWorktreeName
	specBranch
	specCommit
	specLockReason
)

func unexpectedSpecFields(s Spec) bool {
	var present specField
	if len(s.Paths) != 0 {
		present |= specPaths
	}
	if len(s.HunkIDs) != 0 {
		present |= specHunkIDs
	}
	if s.Message != "" {
		present |= specMessage
	}
	if s.IncludeUntracked {
		present |= specIncludeUntracked
	}
	if s.KeepIndex {
		present |= specKeepIndex
	}
	if s.RestoreIndex {
		present |= specRestoreIndex
	}
	if s.StashOID != "" {
		present |= specStashOID
	}
	if s.SequenceID != "" {
		present |= specSequenceID
	}
	if s.UpstreamOID != "" {
		present |= specUpstreamOID
	}
	if s.OntoOID != "" {
		present |= specOntoOID
	}
	if len(s.Commits) != 0 {
		present |= specCommits
	}
	if s.GoodCommit != "" {
		present |= specGoodCommit
	}
	if s.BadCommit != "" {
		present |= specBadCommit
	}
	if s.ExpectedCurrent != "" {
		present |= specExpectedCurrent
	}
	if s.Recipe != nil {
		present |= specRecipe
	}
	if s.WorktreeID != "" {
		present |= specWorktreeID
	}
	if s.WorktreeName != "" {
		present |= specWorktreeName
	}
	if s.Branch != "" {
		present |= specBranch
	}
	if s.Commit != "" {
		present |= specCommit
	}
	if s.LockReason != "" {
		present |= specLockReason
	}

	var allowed specField
	switch s.Operation {
	case HunkStage, HunkUnstage, HunkRevert:
		allowed = specPaths | specHunkIDs
	case StashCreate:
		allowed = specMessage | specIncludeUntracked | specKeepIndex
	case StashApply, StashPop:
		allowed = specStashOID | specRestoreIndex
	case StashDrop:
		allowed = specStashOID
	case RebaseStart:
		allowed = specUpstreamOID | specOntoOID
	case RebaseContinue, RebaseSkip, RebaseAbort,
		CherryPickContinue, CherryPickSkip, CherryPickAbort,
		BisectReset:
		allowed = specSequenceID
	case CherryPickStart:
		allowed = specCommits
	case BisectStart:
		allowed = specGoodCommit | specBadCommit
	case BisectGood, BisectBad, BisectSkip:
		allowed = specSequenceID | specExpectedCurrent
	case BisectRun:
		allowed = specSequenceID | specExpectedCurrent | specRecipe
	case WorktreeCreate:
		allowed = specWorktreeName | specBranch | specCommit
	case WorktreeLock:
		allowed = specWorktreeID | specWorktreeName | specLockReason
	case WorktreeUnlock, WorktreeRemove:
		allowed = specWorktreeID | specWorktreeName
	case WorktreePrune:
		allowed = 0
	default:
		return true
	}
	return present&^allowed != 0
}

type CapabilitySnapshot struct {
	ProtocolVersion   string      `json:"protocol_version"`
	Enabled           bool        `json:"enabled"`
	Generation        string      `json:"generation"`
	ManagedRootSHA256 string      `json:"managed_root_sha256"`
	Operations        []Operation `json:"operations"`
	MaxHunks          int         `json:"max_hunks"`
	MaxPaths          int         `json:"max_paths"`
	MaxCommits        int         `json:"max_commits"`
	CapturedAt        time.Time   `json:"captured_at"`
}

func (c CapabilitySnapshot) Validate() error {
	if c.ProtocolVersion != CapabilityProtocolVersion || !ValidDigest(c.Generation) ||
		!ValidDigest(c.ManagedRootSHA256) || c.MaxHunks != MaxHunks ||
		c.MaxPaths != MaxPaths || c.MaxCommits != MaxCommits || c.CapturedAt.IsZero() {
		return errors.New("Git advanced capability snapshot is invalid")
	}
	if !c.Enabled {
		if len(c.Operations) != 0 {
			return errors.New("disabled Git advanced capability cannot advertise operations")
		}
		return nil
	}
	if len(c.Operations) != len(operations) {
		return errors.New("enabled Git advanced capability must advertise the complete operation set")
	}
	for index, operation := range c.Operations {
		if operation != operations[index] {
			return errors.New("Git advanced capability operation set is not canonical")
		}
	}
	return nil
}

type RepositoryBinding struct {
	ProtocolVersion  string    `json:"protocol_version"`
	RepositorySHA256 string    `json:"repository_sha256"`
	CommonDirSHA256  string    `json:"common_dir_sha256"`
	Head             string    `json:"head"`
	Branch           string    `json:"branch"`
	IndexSHA256      string    `json:"index_sha256"`
	WorktreeSHA256   string    `json:"worktree_sha256"`
	StatusSHA256     string    `json:"status_sha256"`
	StashSHA256      string    `json:"stash_sha256"`
	SequenceSHA256   string    `json:"sequence_sha256"`
	UpstreamRef      string    `json:"upstream_ref,omitempty"`
	UpstreamOID      string    `json:"upstream_oid,omitempty"`
	Detached         bool      `json:"detached"`
	ObjectFormat     string    `json:"object_format"`
	CapturedAt       time.Time `json:"captured_at"`
}

func (b RepositoryBinding) Fingerprint() string {
	return Fingerprint("repository-binding", b.RepositorySHA256, b.CommonDirSHA256,
		b.Head, b.Branch, b.IndexSHA256, b.WorktreeSHA256, b.StatusSHA256,
		b.StashSHA256, b.SequenceSHA256, b.UpstreamRef, b.UpstreamOID,
		fmt.Sprintf("%t", b.Detached), b.ObjectFormat)
}

func (b RepositoryBinding) SameState(other RepositoryBinding) bool {
	return b.RepositorySHA256 == other.RepositorySHA256 &&
		b.CommonDirSHA256 == other.CommonDirSHA256 && b.Head == other.Head &&
		b.Branch == other.Branch && b.IndexSHA256 == other.IndexSHA256 &&
		b.WorktreeSHA256 == other.WorktreeSHA256 && b.StatusSHA256 == other.StatusSHA256 &&
		b.StashSHA256 == other.StashSHA256 && b.SequenceSHA256 == other.SequenceSHA256 &&
		b.UpstreamRef == other.UpstreamRef && b.UpstreamOID == other.UpstreamOID &&
		b.Detached == other.Detached && b.ObjectFormat == other.ObjectFormat
}

type Hunk struct {
	ID             string `json:"id"`
	Path           string `json:"path"`
	OldStart       int    `json:"old_start"`
	OldLines       int    `json:"old_lines"`
	NewStart       int    `json:"new_start"`
	NewLines       int    `json:"new_lines"`
	BaseBlob       string `json:"base_blob,omitempty"`
	IndexBlob      string `json:"index_blob,omitempty"`
	WorktreeSHA256 string `json:"worktree_sha256,omitempty"`
	ContextSHA256  string `json:"context_sha256"`
	PatchSHA256    string `json:"patch_sha256"`
	Patch          string `json:"patch"`
	Destructive    bool   `json:"destructive"`
}

// ReviewDiffEvidence is a read-only, exact merge-base view for review
// providers. It reuses the repository identity and stable hunk model from the
// advanced Git runtime without granting any mutation authority.
type ReviewDiffEvidence struct {
	ProtocolVersion string            `json:"protocol_version"`
	Binding         RepositoryBinding `json:"binding"`
	BaseSHA         string            `json:"base_sha"`
	HeadSHA         string            `json:"head_sha"`
	MergeBaseSHA    string            `json:"merge_base_sha"`
	DiffSHA256      string            `json:"diff_sha256"`
	CallChainSHA256 string            `json:"call_chain_sha256"`
	DiffBytes       int64             `json:"diff_bytes"`
	DiffStat        string            `json:"diff_stat"`
	ChangedFiles    []string          `json:"changed_files"`
	Hunks           []Hunk            `json:"hunks"`
	Conflict        ConflictState     `json:"conflict"`
	Complete        bool              `json:"complete"`
	Omissions       []string          `json:"omissions"`
	CapturedAt      time.Time         `json:"captured_at"`
}

func (e ReviewDiffEvidence) Validate() error {
	if e.ProtocolVersion != ReviewDiffProtocolVersion ||
		!validRepositoryBinding(e.Binding) || !ValidObjectID(e.BaseSHA) ||
		!ValidObjectID(e.HeadSHA) || !ValidObjectID(e.MergeBaseSHA) ||
		!ValidDigest(e.DiffSHA256) || !ValidDigest(e.CallChainSHA256) ||
		e.DiffBytes < 0 || e.DiffBytes > 16*1024*1024 ||
		len(e.ChangedFiles) > MaxReviewChangedFiles || len(e.Hunks) > MaxHunks ||
		len(e.Conflict.Files) > MaxPaths || e.CapturedAt.IsZero() ||
		len(e.Omissions) > 32 || (e.Complete && len(e.Omissions) != 0) {
		return errors.New("Git review diff evidence is invalid")
	}
	for _, path := range e.ChangedFiles {
		if !validRelativePath(path) {
			return errors.New("Git review diff evidence contains an invalid path")
		}
	}
	for _, hunk := range e.Hunks {
		if !ValidDigest(hunk.ID) || !validRelativePath(hunk.Path) ||
			!ValidDigest(hunk.ContextSHA256) || !ValidDigest(hunk.PatchSHA256) ||
			len(hunk.Patch) > MaxPreviewPatchBytes {
			return errors.New("Git review diff evidence contains an invalid hunk")
		}
	}
	return nil
}

type FileImpact struct {
	Path         string `json:"path"`
	BeforeSHA256 string `json:"before_sha256,omitempty"`
	AfterSHA256  string `json:"after_sha256,omitempty"`
	Change       string `json:"change"`
	Destructive  bool   `json:"destructive"`
}

// StashEntry is a selector-free observation of one exact stash commit. Parent
// identities make the base/index/untracked structure explicit while avoiding
// unstable stash@{n} selectors in public contracts.
type StashEntry struct {
	OID             string       `json:"oid"`
	BaseCommit      string       `json:"base_commit"`
	IndexCommit     string       `json:"index_commit"`
	UntrackedCommit string       `json:"untracked_commit,omitempty"`
	Subject         string       `json:"subject"`
	Files           []FileImpact `json:"files"`
}

type ConflictFile struct {
	Path      string `json:"path"`
	BaseOID   string `json:"base_oid,omitempty"`
	OursOID   string `json:"ours_oid,omitempty"`
	TheirsOID string `json:"theirs_oid,omitempty"`
}

type ConflictState struct {
	Active      bool           `json:"active"`
	Kind        string         `json:"kind,omitempty"`
	Files       []ConflictFile `json:"files"`
	CanContinue bool           `json:"can_continue"`
	CanSkip     bool           `json:"can_skip"`
	CanAbort    bool           `json:"can_abort"`
}

type RecoveryPlan struct {
	Required          bool     `json:"required"`
	CheckpointID      string   `json:"checkpoint_id,omitempty"`
	CheckpointLevel   string   `json:"checkpoint_level,omitempty"`
	RestoreAction     string   `json:"restore_action,omitempty"`
	IncompleteReasons []string `json:"incomplete_reasons"`
}

type Preview struct {
	ProtocolVersion      string             `json:"protocol_version"`
	ID                   string             `json:"id"`
	Operation            Operation          `json:"operation"`
	Spec                 Spec               `json:"spec"`
	Binding              RepositoryBinding  `json:"binding"`
	Capability           CapabilitySnapshot `json:"capability"`
	Hunks                []Hunk             `json:"hunks"`
	Files                []FileImpact       `json:"files"`
	Conflict             ConflictState      `json:"conflict"`
	Recovery             RecoveryPlan       `json:"recovery"`
	Target               string             `json:"target,omitempty"`
	Summary              string             `json:"summary"`
	BlockedReasons       []string           `json:"blocked_reasons"`
	ApprovalFingerprint  string             `json:"approval_fingerprint"`
	PermissionSnapshotID string             `json:"permission_snapshot_id,omitempty"`
	PermissionRevision   int64              `json:"permission_revision,omitempty"`
	LeaseID              string             `json:"-"`
	LeaseGeneration      int64              `json:"lease_generation,omitempty"`
	CreatedAt            time.Time          `json:"created_at"`
}

func (p Preview) Executable() bool { return len(p.BlockedReasons) == 0 }

type ReceiptStatus string

const (
	ReceiptSucceeded  ReceiptStatus = "succeeded"
	ReceiptConflicted ReceiptStatus = "conflicted"
	ReceiptFailed     ReceiptStatus = "failed"
)

func (s ReceiptStatus) Valid() bool {
	return s == ReceiptSucceeded || s == ReceiptConflicted || s == ReceiptFailed
}

type Receipt struct {
	ProtocolVersion  string                   `json:"protocol_version"`
	ID               string                   `json:"id"`
	PreviewID        string                   `json:"preview_id"`
	Operation        Operation                `json:"operation"`
	Status           ReceiptStatus            `json:"status"`
	PreBinding       RepositoryBinding        `json:"pre_binding"`
	PostBinding      RepositoryBinding        `json:"post_binding"`
	Conflict         ConflictState            `json:"conflict"`
	CheckpointID     string                   `json:"checkpoint_id,omitempty"`
	TargetOID        string                   `json:"target_oid,omitempty"`
	SequenceID       string                   `json:"sequence_id,omitempty"`
	WorktreeID       string                   `json:"worktree_id,omitempty"`
	ObservedBytes    int                      `json:"observed_bytes"`
	ErrorCode        FailureCode              `json:"error_code,omitempty"`
	ErrorSummary     string                   `json:"error_summary,omitempty"`
	StartedAt        time.Time                `json:"started_at"`
	CompletedAt      time.Time                `json:"completed_at"`
	BisectExecutions []BisectExecutionReceipt `json:"bisect_executions,omitempty"`
}

func (r Receipt) Validate() error {
	if r.ProtocolVersion != ReceiptProtocolVersion || !validIdentity(r.ID) ||
		!validIdentity(r.PreviewID) || !r.Operation.Valid() || !r.Status.Valid() ||
		!validRepositoryBinding(r.PreBinding) || r.ObservedBytes < 0 ||
		!boundedText(r.ErrorSummary, 4096) || r.StartedAt.IsZero() ||
		r.CompletedAt.IsZero() || r.CompletedAt.Before(r.StartedAt) ||
		len(r.Conflict.Files) > MaxPaths {
		return errors.New("Git advanced receipt is invalid")
	}
	for _, value := range []string{r.CheckpointID, r.SequenceID, r.WorktreeID} {
		if value != "" && !validIdentity(value) {
			return errors.New("Git advanced receipt contains an invalid durable identity")
		}
	}
	if r.TargetOID != "" && !ValidObjectID(r.TargetOID) {
		return errors.New("Git advanced receipt target is not an exact object id")
	}
	if len(r.BisectExecutions) > MaxCommits || len(r.BisectExecutions) > 0 && r.Operation != BisectRun {
		return errors.New("Git advanced receipt contains invalid bisect execution evidence")
	}
	for index, execution := range r.BisectExecutions {
		if execution.Ordinal != index {
			return errors.New("Git advanced bisect execution order is invalid")
		}
		if err := execution.Validate(); err != nil {
			return err
		}
	}
	switch r.Status {
	case ReceiptSucceeded:
		if r.ErrorCode != "" || !validRepositoryBinding(r.PostBinding) {
			return errors.New("successful Git advanced receipt has invalid terminal evidence")
		}
	case ReceiptConflicted:
		if r.ErrorCode != FailureConflict || !r.Conflict.Active ||
			!validRepositoryBinding(r.PostBinding) {
			return errors.New("conflicted Git advanced receipt has invalid terminal evidence")
		}
	case ReceiptFailed:
		if !r.ErrorCode.Valid() || r.ErrorSummary == "" {
			return errors.New("failed Git advanced receipt has no typed failure")
		}
	}
	return nil
}

func validRepositoryBinding(value RepositoryBinding) bool {
	if value.ProtocolVersion != ProtocolVersion ||
		!ValidDigest(value.RepositorySHA256) || !ValidDigest(value.CommonDirSHA256) ||
		!ValidDigest(value.IndexSHA256) || !ValidDigest(value.WorktreeSHA256) ||
		!ValidDigest(value.StatusSHA256) || !ValidDigest(value.StashSHA256) ||
		!ValidDigest(value.SequenceSHA256) || value.CapturedAt.IsZero() ||
		(value.ObjectFormat != "sha1" && value.ObjectFormat != "sha256") ||
		(value.Head != "unborn" && !ValidObjectID(value.Head)) ||
		(value.UpstreamOID != "" && !ValidObjectID(value.UpstreamOID)) ||
		!boundedText(value.Branch, 255) || !boundedText(value.UpstreamRef, 1024) {
		return false
	}
	return true
}

type SequenceKind string

const (
	SequenceRebase     SequenceKind = "rebase"
	SequenceCherryPick SequenceKind = "cherry_pick"
	SequenceBisect     SequenceKind = "bisect"
)

type SequenceStatus string

const (
	SequenceActive     SequenceStatus = "active"
	SequenceConflicted SequenceStatus = "conflicted"
	SequenceCompleted  SequenceStatus = "completed"
	SequenceAborted    SequenceStatus = "aborted"
	SequenceFailed     SequenceStatus = "failed"
)

func (s SequenceStatus) Terminal() bool {
	return s == SequenceCompleted || s == SequenceAborted || s == SequenceFailed
}

type Sequence struct {
	ID                 string         `json:"id"`
	ProtocolVersion    string         `json:"protocol_version"`
	RunID              string         `json:"run_id"`
	WorkspaceID        string         `json:"workspace_id"`
	Kind               SequenceKind   `json:"kind"`
	Status             SequenceStatus `json:"status"`
	RepositorySHA256   string         `json:"repository_sha256"`
	OriginalHead       string         `json:"original_head"`
	OriginalBranch     string         `json:"original_branch"`
	TargetJSON         string         `json:"-"`
	SequencerSHA256    string         `json:"sequencer_sha256"`
	CurrentHead        string         `json:"current_head"`
	ConflictJSON       string         `json:"-"`
	Generation         int64          `json:"generation"`
	StartedOperationID string         `json:"started_operation_id"`
	LastOperationID    string         `json:"last_operation_id"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
	CompletedAt        *time.Time     `json:"completed_at,omitempty"`
}

// SequenceIDForPreview derives the durable sequencer identity before Git is
// invoked, so startup reconciliation can recover the same identity after a
// crash between process completion and state persistence.
func SequenceIDForPreview(previewID string, kind SequenceKind) string {
	return "gseq-" + Fingerprint("sequence", previewID, string(kind))[:32]
}

type ManagedWorktree struct {
	ID                 string     `json:"id"`
	ProtocolVersion    string     `json:"protocol_version"`
	RunID              string     `json:"run_id"`
	WorkspaceID        string     `json:"workspace_id"`
	RepositorySHA256   string     `json:"repository_sha256"`
	CommonDirSHA256    string     `json:"common_dir_sha256"`
	Name               string     `json:"name"`
	Path               string     `json:"-"`
	PathSHA256         string     `json:"path_sha256"`
	Branch             string     `json:"branch"`
	Head               string     `json:"head"`
	Locked             bool       `json:"locked"`
	LockReason         string     `json:"lock_reason,omitempty"`
	Present            bool       `json:"present"`
	Generation         int64      `json:"generation"`
	CreatedOperationID string     `json:"created_operation_id"`
	LastOperationID    string     `json:"last_operation_id"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	RemovedAt          *time.Time `json:"removed_at,omitempty"`
}

type OperationStatus string

const (
	OperationProposed   OperationStatus = "proposed"
	OperationRunning    OperationStatus = "running"
	OperationSucceeded  OperationStatus = "succeeded"
	OperationConflicted OperationStatus = "conflicted"
	OperationFailed     OperationStatus = "failed"
)

func (s OperationStatus) Valid() bool {
	switch s {
	case OperationProposed, OperationRunning, OperationSucceeded,
		OperationConflicted, OperationFailed:
		return true
	default:
		return false
	}
}

func (s OperationStatus) Terminal() bool {
	return s == OperationSucceeded || s == OperationConflicted || s == OperationFailed
}

type OperationRecord struct {
	ID                   string          `json:"id"`
	ProtocolVersion      string          `json:"protocol_version"`
	OperationKeySHA256   string          `json:"operation_key_sha256"`
	RequestFingerprint   string          `json:"request_fingerprint"`
	PreviewID            string          `json:"preview_id"`
	ApprovalFingerprint  string          `json:"approval_fingerprint"`
	ApprovalID           string          `json:"approval_id,omitempty"`
	RunID                string          `json:"run_id"`
	SessionID            string          `json:"session_id"`
	WorkspaceID          string          `json:"workspace_id"`
	Operation            Operation       `json:"operation"`
	SpecJSON             string          `json:"-"`
	PreviewJSON          string          `json:"-"`
	RepositorySHA256     string          `json:"repository_sha256"`
	CommonDirSHA256      string          `json:"common_dir_sha256"`
	PermissionSnapshotID string          `json:"permission_snapshot_id"`
	PermissionRevision   int64           `json:"permission_revision"`
	CapabilityGeneration string          `json:"capability_generation"`
	LeaseID              string          `json:"-"`
	LeaseGeneration      int64           `json:"lease_generation"`
	Status               OperationStatus `json:"status"`
	ReceiptJSON          string          `json:"-"`
	ErrorCode            FailureCode     `json:"error_code,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
	StartedAt            *time.Time      `json:"started_at,omitempty"`
	CompletedAt          *time.Time      `json:"completed_at,omitempty"`
}

type OperationListFilter struct {
	RunID            string          `json:"run_id,omitempty"`
	WorkspaceID      string          `json:"workspace_id,omitempty"`
	RepositorySHA256 string          `json:"repository_sha256,omitempty"`
	Status           OperationStatus `json:"status,omitempty"`
	Limit            int             `json:"limit,omitempty"`
}

type FailureCode string

const (
	FailureCapabilityDisabled FailureCode = "capability_disabled"
	FailureApprovalRequired   FailureCode = "approval_required"
	FailureStalePreview       FailureCode = "stale_preview"
	FailureRepositoryDrift    FailureCode = "repository_drift"
	FailureRemoteDrift        FailureCode = "remote_drift"
	FailurePermissionDrift    FailureCode = "permission_drift"
	FailureLeaseDrift         FailureCode = "lease_drift"
	FailureBranchProtected    FailureCode = "branch_protected"
	FailureConflict           FailureCode = "conflict"
	FailureUnsafeRepository   FailureCode = "unsafe_repository"
	FailureOutsideManagedRoot FailureCode = "outside_managed_root"
	FailureUnknownWorktree    FailureCode = "unknown_worktree"
	FailureDirtyWorktree      FailureCode = "dirty_worktree"
	FailureBudgetExceeded     FailureCode = "budget_exceeded"
	FailureTimeout            FailureCode = "timeout"
	FailureCancelled          FailureCode = "cancelled"
	FailureInterrupted        FailureCode = "interrupted"
	FailureGit                FailureCode = "git_failed"
)

func (c FailureCode) Valid() bool {
	switch c {
	case FailureCapabilityDisabled, FailureApprovalRequired, FailureStalePreview,
		FailureRepositoryDrift, FailureRemoteDrift, FailurePermissionDrift,
		FailureLeaseDrift, FailureBranchProtected, FailureConflict,
		FailureUnsafeRepository, FailureOutsideManagedRoot, FailureUnknownWorktree,
		FailureDirtyWorktree, FailureBudgetExceeded, FailureTimeout,
		FailureCancelled, FailureInterrupted, FailureGit:
		return true
	default:
		return false
	}
}

type Error struct {
	Code    FailureCode
	Message string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return string(e.Code) + ": " + e.Message
}

func Fingerprint(parts ...string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(ProtocolVersion))
	for _, part := range parts {
		value := []byte(part)
		_, _ = fmt.Fprintf(hash, "\x00%d:", len(value))
		_, _ = hash.Write(value)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func ValidDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func ValidObjectID(value string) bool {
	if (len(value) != 40 && len(value) != 64) || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded)*2 == len(value)
}

func validRelativePath(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len([]rune(value)) > 4096 ||
		!utf8.ValidString(value) || strings.ContainsRune(value, 0) ||
		strings.HasPrefix(value, "-") || strings.HasPrefix(value, "/") ||
		strings.ContainsAny(value, "\\:") || strings.Contains(value, "//") ||
		value == "." || value == ".." || strings.HasPrefix(value, "../") ||
		strings.Contains(value, "/../") || strings.HasSuffix(value, "/..") ||
		strings.Contains(value, "/./") {
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

func validIdentity(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) ||
		len([]rune(value)) > 256 || strings.ContainsRune(value, 0) {
		return false
	}
	for _, current := range value {
		if current < 0x20 || current == 0x7f {
			return false
		}
	}
	return true
}

func validWorktreeName(value string) bool {
	if value == "" || len(value) > 80 || strings.HasPrefix(value, "-") {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func validBranch(value string) bool {
	if value == "" || value == "@" || len(value) > 255 || !utf8.ValidString(value) {
		return false
	}
	if strings.HasPrefix(value, "-") || strings.HasPrefix(value, "/") ||
		strings.HasSuffix(value, "/") || strings.HasSuffix(value, ".") ||
		strings.ContainsAny(value, " \\~^:?*[\"") || strings.Contains(value, "..") ||
		strings.Contains(value, "//") || strings.Contains(value, "@{") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || strings.HasPrefix(component, ".") ||
			strings.HasSuffix(strings.ToLower(component), ".lock") {
			return false
		}
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func boundedText(value string, max int) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, 0) &&
		len([]rune(value)) <= max
}
