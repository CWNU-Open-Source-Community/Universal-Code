package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	BatchDeliveryProtocolVersion           = "batch-delivery.v1"
	BatchDeliveryToolProfileVersion        = "batch-delivery-tools.v1"
	BatchDeliveryMailboxVersion            = "batch-delivery-mailbox.v1"
	BatchDeliveryReceiptVersion            = "batch-delivery-receipt.v1"
	BatchDeliveryReviewVersion             = "batch-delivery-review.v1"
	BatchDeliveryMergeQueueVersion         = "batch-delivery-merge-queue.v1"
	MaxBatchDeliverySpecJSONBytes          = 64 * 1024
	MaxBatchDeliveryTasks                  = MaxAgentChildren
	MaxBatchOwnershipHints                 = 32
	MaxBatchValidationRequirements         = 16
	MaxBatchChangedFiles                   = 512
	MaxBatchDiffBytes                int64 = 16 * 1024 * 1024
	MaxBatchMailboxSummaryRunes            = 4_096
	MaxBatchMailboxEvidenceRefs            = 32
	MaxBatchDeliveryLimitations            = 32
	MaxBatchDeliveryLimitationRunes        = 1_024
	MaxBatchDeliveryTestReceipts           = 32
	MaxBatchDeliveryCallChainEntries       = 256
	MaxBatchDeliveryLeaseMillis      int64 = 24 * 60 * 60 * 1000
	MaxBatchDeliveryGenerations            = 8
)

type BatchDeliveryOwnershipKind string

const (
	BatchDeliveryOwnershipFile      BatchDeliveryOwnershipKind = "file"
	BatchDeliveryOwnershipDirectory BatchDeliveryOwnershipKind = "directory"
)

type BatchDeliveryOwnershipHint struct {
	Path string                     `json:"path"`
	Kind BatchDeliveryOwnershipKind `json:"kind"`
}

type BatchDeliveryValidationKind string

const (
	BatchValidationGitDiffCheck BatchDeliveryValidationKind = "git_diff_check"
	BatchValidationGoTest       BatchDeliveryValidationKind = "go_test"
	BatchValidationNPMTest      BatchDeliveryValidationKind = "npm_test"
)

type BatchDeliveryValidationRequirement struct {
	ID    string                      `json:"id"`
	Kind  BatchDeliveryValidationKind `json:"kind"`
	Scope string                      `json:"scope"`
}

type BatchDeliveryBudget struct {
	TurnLimit     int64 `json:"turn_limit"`
	TokenLimit    int64 `json:"token_limit"`
	TimeoutMillis int64 `json:"timeout_millis"`
}

type BatchDeliveryTaskSpec struct {
	Ordinal            int                                  `json:"ordinal"`
	OwnershipHints     []BatchDeliveryOwnershipHint         `json:"ownership_hints"`
	DependencyOrdinals []int                                `json:"dependency_ordinals"`
	Budget             BatchDeliveryBudget                  `json:"budget"`
	Validations        []BatchDeliveryValidationRequirement `json:"validations"`
	ExpectedArtifacts  []ChildTaskExpectedArtifact          `json:"expected_artifacts"`
}

type BatchDeliveryContract struct {
	RequireClean             bool  `json:"require_clean"`
	RequireIndependentReview bool  `json:"require_independent_review"`
	RequireAllValidations    bool  `json:"require_all_validations"`
	MaxChangedFiles          int   `json:"max_changed_files"`
	MaxDiffBytes             int64 `json:"max_diff_bytes"`
}

type BatchDeliverySpec struct {
	Version  string                  `json:"version"`
	Tasks    []BatchDeliveryTaskSpec `json:"tasks"`
	Contract BatchDeliveryContract   `json:"contract"`
}

func DecodeBatchDeliverySpec(raw []byte) (BatchDeliverySpec, error) {
	if len(raw) == 0 || len(raw) > MaxBatchDeliverySpecJSONBytes || !utf8.Valid(raw) {
		return BatchDeliverySpec{}, fmt.Errorf("batch delivery payload must be UTF-8 JSON within %d bytes",
			MaxBatchDeliverySpecJSONBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var spec BatchDeliverySpec
	if err := decoder.Decode(&spec); err != nil {
		return BatchDeliverySpec{}, errors.New("batch delivery payload does not match batch-delivery.v1")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return BatchDeliverySpec{}, errors.New("batch delivery payload contains trailing data")
	}
	return NormalizeBatchDeliverySpec(spec)
}

func NormalizeBatchDeliverySpec(spec BatchDeliverySpec) (BatchDeliverySpec, error) {
	spec.Version = strings.TrimSpace(spec.Version)
	if spec.Version != BatchDeliveryProtocolVersion {
		return BatchDeliverySpec{}, fmt.Errorf("unsupported batch delivery version %q", spec.Version)
	}
	if len(spec.Tasks) < 1 || len(spec.Tasks) > MaxBatchDeliveryTasks {
		return BatchDeliverySpec{}, fmt.Errorf("batch delivery requires between 1 and %d tasks",
			MaxBatchDeliveryTasks)
	}
	if !spec.Contract.RequireClean || !spec.Contract.RequireIndependentReview ||
		!spec.Contract.RequireAllValidations {
		return BatchDeliverySpec{}, errors.New("batch delivery cannot weaken clean, review, or validation gates")
	}
	if spec.Contract.MaxChangedFiles <= 0 ||
		spec.Contract.MaxChangedFiles > MaxBatchChangedFiles ||
		spec.Contract.MaxDiffBytes <= 0 || spec.Contract.MaxDiffBytes > MaxBatchDiffBytes {
		return BatchDeliverySpec{}, errors.New("batch delivery contract limits are invalid")
	}
	seenOrdinals := make(map[int]struct{}, len(spec.Tasks))
	for index := range spec.Tasks {
		task := &spec.Tasks[index]
		if task.Ordinal < 1 || task.Ordinal > len(spec.Tasks) {
			return BatchDeliverySpec{}, fmt.Errorf("batch delivery task %d ordinal is invalid", index+1)
		}
		if _, exists := seenOrdinals[task.Ordinal]; exists {
			return BatchDeliverySpec{}, errors.New("batch delivery task ordinals must be unique")
		}
		seenOrdinals[task.Ordinal] = struct{}{}
		if task.Budget.TurnLimit <= 0 || task.Budget.TokenLimit <= 0 ||
			task.Budget.TokenLimit > MaxAgentTokenReservation ||
			task.Budget.TimeoutMillis <= 0 || task.Budget.TimeoutMillis > MaxChildTaskTimeoutMillis {
			return BatchDeliverySpec{}, fmt.Errorf("batch delivery task %d budget is invalid", task.Ordinal)
		}
		if len(task.OwnershipHints) == 0 || len(task.OwnershipHints) > MaxBatchOwnershipHints {
			return BatchDeliverySpec{}, fmt.Errorf("batch delivery task %d ownership is missing or too large", task.Ordinal)
		}
		hints := make([]BatchDeliveryOwnershipHint, 0, len(task.OwnershipHints))
		for _, hint := range task.OwnershipHints {
			normalized, err := normalizeBatchOwnershipHint(hint)
			if err != nil {
				return BatchDeliverySpec{}, fmt.Errorf("batch delivery task %d ownership: %w", task.Ordinal, err)
			}
			if slices.Contains(hints, normalized) {
				return BatchDeliverySpec{}, fmt.Errorf("batch delivery task %d repeats an ownership hint", task.Ordinal)
			}
			hints = append(hints, normalized)
		}
		sort.Slice(hints, func(left, right int) bool {
			if hints[left].Path == hints[right].Path {
				return hints[left].Kind < hints[right].Kind
			}
			return hints[left].Path < hints[right].Path
		})
		task.OwnershipHints = hints
		if len(task.Validations) == 0 || len(task.Validations) > MaxBatchValidationRequirements {
			return BatchDeliverySpec{}, fmt.Errorf("batch delivery task %d validations are missing or too large", task.Ordinal)
		}
		validationIDs := make(map[string]struct{}, len(task.Validations))
		hasDiffCheck := false
		for validationIndex := range task.Validations {
			validation, err := normalizeBatchValidation(task.Validations[validationIndex])
			if err != nil {
				return BatchDeliverySpec{}, fmt.Errorf("batch delivery task %d validation: %w", task.Ordinal, err)
			}
			if _, duplicate := validationIDs[validation.ID]; duplicate {
				return BatchDeliverySpec{}, fmt.Errorf("batch delivery task %d validation ids must be unique", task.Ordinal)
			}
			validationIDs[validation.ID] = struct{}{}
			hasDiffCheck = hasDiffCheck || validation.Kind == BatchValidationGitDiffCheck
			task.Validations[validationIndex] = validation
		}
		if !hasDiffCheck {
			return BatchDeliverySpec{}, fmt.Errorf("batch delivery task %d requires git_diff_check", task.Ordinal)
		}
		if len(task.DependencyOrdinals) > len(spec.Tasks)-1 {
			return BatchDeliverySpec{}, fmt.Errorf("batch delivery task %d dependencies are invalid", task.Ordinal)
		}
		deps := make([]int, 0, len(task.DependencyOrdinals))
		for _, dependency := range task.DependencyOrdinals {
			if dependency < 1 || dependency > len(spec.Tasks) || dependency == task.Ordinal ||
				slices.Contains(deps, dependency) {
				return BatchDeliverySpec{}, fmt.Errorf("batch delivery task %d dependency is invalid", task.Ordinal)
			}
			deps = append(deps, dependency)
		}
		slices.Sort(deps)
		task.DependencyOrdinals = deps
		if len(task.ExpectedArtifacts) > MaxChildTaskExpectedArtifacts {
			return BatchDeliverySpec{}, fmt.Errorf("batch delivery task %d expected artifacts exceed %d",
				task.Ordinal, MaxChildTaskExpectedArtifacts)
		}
		for artifactIndex := range task.ExpectedArtifacts {
			artifact := task.ExpectedArtifacts[artifactIndex]
			artifact.PathHint = strings.TrimSpace(artifact.PathHint)
			artifact.Kind = strings.TrimSpace(artifact.Kind)
			if !validChildTaskInputRef(artifact.PathHint) || artifact.Kind == "" ||
				len([]byte(artifact.Kind)) > 64 || strings.ContainsRune(artifact.Kind, 0) {
				return BatchDeliverySpec{}, fmt.Errorf("batch delivery task %d expected artifact is invalid", task.Ordinal)
			}
			task.ExpectedArtifacts[artifactIndex] = artifact
		}
	}
	sort.Slice(spec.Tasks, func(left, right int) bool { return spec.Tasks[left].Ordinal < spec.Tasks[right].Ordinal })
	if err := validateBatchDependencyAcyclic(spec.Tasks); err != nil {
		return BatchDeliverySpec{}, err
	}
	if err := validateBatchOwnershipSeparation(spec.Tasks); err != nil {
		return BatchDeliverySpec{}, err
	}
	return spec, nil
}

func (s BatchDeliverySpec) Validate() error {
	normalized, err := NormalizeBatchDeliverySpec(s)
	if err != nil {
		return err
	}
	want, _ := json.Marshal(normalized)
	got, _ := json.Marshal(s)
	if !bytes.Equal(want, got) {
		return errors.New("batch delivery specification must be normalized")
	}
	return nil
}

func normalizeBatchOwnershipHint(hint BatchDeliveryOwnershipHint) (BatchDeliveryOwnershipHint, error) {
	hint.Path = strings.TrimSpace(strings.ReplaceAll(hint.Path, "\\", "/"))
	hint.Path = strings.TrimSuffix(hint.Path, "/")
	hint.Kind = BatchDeliveryOwnershipKind(strings.TrimSpace(string(hint.Kind)))
	if hint.Kind != BatchDeliveryOwnershipFile && hint.Kind != BatchDeliveryOwnershipDirectory {
		return BatchDeliveryOwnershipHint{}, errors.New("ownership kind must be file or directory")
	}
	if !validBatchRelativePath(hint.Path) {
		return BatchDeliveryOwnershipHint{}, errors.New("ownership path must be a normalized repository-relative path")
	}
	return hint, nil
}

func normalizeBatchValidation(value BatchDeliveryValidationRequirement) (BatchDeliveryValidationRequirement, error) {
	value.ID = strings.ToLower(strings.TrimSpace(value.ID))
	value.Kind = BatchDeliveryValidationKind(strings.TrimSpace(string(value.Kind)))
	value.Scope = strings.TrimSpace(strings.ReplaceAll(value.Scope, "\\", "/"))
	value.Scope = strings.TrimSuffix(value.Scope, "/")
	if !validBatchIdentifier(value.ID) {
		return BatchDeliveryValidationRequirement{}, errors.New("validation id is invalid")
	}
	switch value.Kind {
	case BatchValidationGitDiffCheck:
		if value.Scope != "." && value.Scope != "" {
			return BatchDeliveryValidationRequirement{}, errors.New("git_diff_check scope must be repository root")
		}
		value.Scope = "."
	case BatchValidationGoTest, BatchValidationNPMTest:
		if value.Scope == "" {
			value.Scope = "."
		}
		if value.Scope != "." && !validBatchRelativePath(value.Scope) {
			return BatchDeliveryValidationRequirement{}, errors.New("validation scope is invalid")
		}
	default:
		return BatchDeliveryValidationRequirement{}, fmt.Errorf("unsupported validation kind %q", value.Kind)
	}
	return value, nil
}

func validBatchIdentifier(value string) bool {
	if value == "" || len(value) > 96 || !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' && char != '_' && char != '.' {
			return false
		}
	}
	return true
}

func validBatchRelativePath(value string) bool {
	if value == "" || value == "." || strings.ContainsRune(value, 0) ||
		strings.HasPrefix(value, "/") || strings.Contains(value, "//") ||
		len([]byte(value)) > MaxReadOnlyFanoutPathBytes || !utf8.ValidString(value) {
		return false
	}
	cleaned := path.Clean(value)
	return cleaned == value && cleaned != ".." && !strings.HasPrefix(cleaned, "../") &&
		!strings.Contains(cleaned, ":")
}

func validateBatchDependencyAcyclic(tasks []BatchDeliveryTaskSpec) error {
	state := make([]int, len(tasks)+1)
	var visit func(int) error
	visit = func(ordinal int) error {
		if state[ordinal] == 1 {
			return errors.New("batch delivery dependencies contain a cycle")
		}
		if state[ordinal] == 2 {
			return nil
		}
		state[ordinal] = 1
		for _, dependency := range tasks[ordinal-1].DependencyOrdinals {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		state[ordinal] = 2
		return nil
	}
	for ordinal := 1; ordinal <= len(tasks); ordinal++ {
		if err := visit(ordinal); err != nil {
			return err
		}
	}
	return nil
}

func validateBatchOwnershipSeparation(tasks []BatchDeliveryTaskSpec) error {
	for left := range tasks {
		for right := left + 1; right < len(tasks); right++ {
			for _, leftHint := range tasks[left].OwnershipHints {
				for _, rightHint := range tasks[right].OwnershipHints {
					if BatchOwnershipHintsOverlap(leftHint, rightHint) {
						return fmt.Errorf("batch delivery ownership overlaps between tasks %d and %d at %q and %q",
							tasks[left].Ordinal, tasks[right].Ordinal, leftHint.Path, rightHint.Path)
					}
				}
			}
		}
	}
	return nil
}

func BatchOwnershipHintsOverlap(left, right BatchDeliveryOwnershipHint) bool {
	if left.Path == right.Path {
		return true
	}
	if left.Kind == BatchDeliveryOwnershipDirectory && strings.HasPrefix(right.Path, left.Path+"/") {
		return true
	}
	return right.Kind == BatchDeliveryOwnershipDirectory && strings.HasPrefix(left.Path, right.Path+"/")
}

func BatchOwnershipAllows(hints []BatchDeliveryOwnershipHint, changedPath string) bool {
	changedPath = strings.TrimSpace(strings.ReplaceAll(changedPath, "\\", "/"))
	if !validBatchRelativePath(changedPath) {
		return false
	}
	for _, hint := range hints {
		if hint.Path == changedPath ||
			(hint.Kind == BatchDeliveryOwnershipDirectory && strings.HasPrefix(changedPath, hint.Path+"/")) {
			return true
		}
	}
	return false
}

// BatchDeliveryToolProfile is the closed capability ceiling for a mutating
// child. Authority is meaningful only together with the persisted worktree,
// owner token digest, lease generation, and exact Agent identity.
type BatchDeliveryToolProfile struct {
	Version         string `json:"version"`
	WorkspaceList   bool   `json:"workspace_list"`
	WorkspaceRead   bool   `json:"workspace_read"`
	WorkspaceSearch bool   `json:"workspace_search"`
	WorkspaceChange bool   `json:"workspace_change"`
	WorkspaceApply  bool   `json:"workspace_apply"`
	GitStatus       bool   `json:"git_status"`
	GitDiff         bool   `json:"git_diff"`
	GitCommit       bool   `json:"git_commit"`
	WorkspaceDelete bool   `json:"workspace_delete"`
	Shell           bool   `json:"shell"`
	Process         bool   `json:"process"`
	Network         bool   `json:"network"`
	Credentials     bool   `json:"credentials"`
	DebugTerminal   bool   `json:"debug_terminal"`
	Approvals       bool   `json:"approvals"`
	SpawnChildren   bool   `json:"spawn_children"`
}

func DefaultBatchDeliveryToolProfile() BatchDeliveryToolProfile {
	return BatchDeliveryToolProfile{Version: BatchDeliveryToolProfileVersion,
		WorkspaceList: true, WorkspaceRead: true, WorkspaceSearch: true,
		WorkspaceChange: true, WorkspaceApply: true, GitStatus: true,
		GitDiff: true, GitCommit: true}
}

func (p BatchDeliveryToolProfile) Validate() error {
	if p != DefaultBatchDeliveryToolProfile() {
		return errors.New("batch delivery tool profile must match the closed child capability ceiling")
	}
	return nil
}

func (p BatchDeliveryToolProfile) Fingerprint() string {
	encoded, _ := json.Marshal(p)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

type BatchDeliveryStatus string

const (
	BatchDeliveryPreparing BatchDeliveryStatus = "preparing"
	BatchDeliveryActive    BatchDeliveryStatus = "active"
	BatchDeliveryReviewing BatchDeliveryStatus = "reviewing"
	BatchDeliveryMerging   BatchDeliveryStatus = "merging"
	BatchDeliveryCompleted BatchDeliveryStatus = "completed"
	BatchDeliveryBlocked   BatchDeliveryStatus = "blocked"
	BatchDeliveryAborted   BatchDeliveryStatus = "aborted"
)

func (s BatchDeliveryStatus) Terminal() bool {
	return s == BatchDeliveryCompleted || s == BatchDeliveryAborted
}

func ValidBatchDeliveryStatus(status BatchDeliveryStatus) bool {
	switch status {
	case BatchDeliveryPreparing, BatchDeliveryActive, BatchDeliveryReviewing,
		BatchDeliveryMerging, BatchDeliveryCompleted, BatchDeliveryBlocked,
		BatchDeliveryAborted:
		return true
	default:
		return false
	}
}

type BatchDeliveryWorkspaceStatus string

const (
	BatchWorkspacePreparing        BatchDeliveryWorkspaceStatus = "preparing"
	BatchWorkspaceDispatched       BatchDeliveryWorkspaceStatus = "dispatched"
	BatchWorkspaceAcknowledged     BatchDeliveryWorkspaceStatus = "acknowledged"
	BatchWorkspaceWorking          BatchDeliveryWorkspaceStatus = "working"
	BatchWorkspaceQuestion         BatchDeliveryWorkspaceStatus = "question"
	BatchWorkspaceReadyForReview   BatchDeliveryWorkspaceStatus = "ready_for_review"
	BatchWorkspaceChangesRequested BatchDeliveryWorkspaceStatus = "changes_requested"
	BatchWorkspaceAccepted         BatchDeliveryWorkspaceStatus = "accepted"
	BatchWorkspaceMerged           BatchDeliveryWorkspaceStatus = "merged"
	BatchWorkspaceCancelled        BatchDeliveryWorkspaceStatus = "cancelled"
	BatchWorkspaceFailed           BatchDeliveryWorkspaceStatus = "failed"
	BatchWorkspaceOrphaned         BatchDeliveryWorkspaceStatus = "orphaned"
)

func (s BatchDeliveryWorkspaceStatus) Terminal() bool {
	return s == BatchWorkspaceMerged || s == BatchWorkspaceCancelled ||
		s == BatchWorkspaceFailed || s == BatchWorkspaceOrphaned
}

func ValidBatchDeliveryWorkspaceStatus(status BatchDeliveryWorkspaceStatus) bool {
	switch status {
	case BatchWorkspacePreparing, BatchWorkspaceDispatched, BatchWorkspaceAcknowledged,
		BatchWorkspaceWorking, BatchWorkspaceQuestion, BatchWorkspaceReadyForReview,
		BatchWorkspaceChangesRequested, BatchWorkspaceAccepted, BatchWorkspaceMerged,
		BatchWorkspaceCancelled, BatchWorkspaceFailed, BatchWorkspaceOrphaned:
		return true
	default:
		return false
	}
}

type BatchDeliveryPlan struct {
	ID                 string
	RunID              string
	ProposalID         string
	RootAgentID        string
	WorkspaceID        string
	Status             BatchDeliveryStatus
	Spec               BatchDeliverySpec
	BaseCommit         string
	SourceBranch       string
	OperationDigest    string
	RequestFingerprint string
	CreatedBy          string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type BatchDeliveryWorkspace struct {
	PlanID                 string
	Ordinal                int
	AgentID                string
	Generation             int64
	Status                 BatchDeliveryWorkspaceStatus
	Branch                 string
	WorktreeRoot           string
	BaseCommit             string
	HeadCommit             string
	OwnerTokenDigest       string
	ToolProfile            BatchDeliveryToolProfile
	ToolProfileFingerprint string
	LeaseExpiresAt         time.Time
	LastHeartbeatAt        time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

type BatchDeliveryMailboxKind string

const (
	BatchMailboxDispatch         BatchDeliveryMailboxKind = "dispatch"
	BatchMailboxAck              BatchDeliveryMailboxKind = "ack"
	BatchMailboxProgress         BatchDeliveryMailboxKind = "progress"
	BatchMailboxQuestion         BatchDeliveryMailboxKind = "question"
	BatchMailboxEvidence         BatchDeliveryMailboxKind = "evidence"
	BatchMailboxReadyForReview   BatchDeliveryMailboxKind = "ready_for_review"
	BatchMailboxChangesRequested BatchDeliveryMailboxKind = "changes_requested"
	BatchMailboxAccepted         BatchDeliveryMailboxKind = "accepted"
	BatchMailboxAborted          BatchDeliveryMailboxKind = "aborted"
)

func ValidBatchDeliveryMailboxKind(kind BatchDeliveryMailboxKind) bool {
	switch kind {
	case BatchMailboxDispatch, BatchMailboxAck, BatchMailboxProgress, BatchMailboxQuestion,
		BatchMailboxEvidence, BatchMailboxReadyForReview, BatchMailboxChangesRequested,
		BatchMailboxAccepted, BatchMailboxAborted:
		return true
	default:
		return false
	}
}

type BatchDeliveryMailboxMessage struct {
	ID                 string
	PlanID             string
	Ordinal            int
	Generation         int64
	Sequence           int64
	Kind               BatchDeliveryMailboxKind
	Actor              string
	Summary            string
	EvidenceRefs       []string
	OperationDigest    string
	RequestFingerprint string
	CreatedAt          time.Time
}

type BatchDeliveryTestReceipt struct {
	RequirementID  string                           `json:"requirement_id"`
	Kind           BatchDeliveryValidationKind      `json:"kind"`
	Scope          string                           `json:"scope"`
	ExitCode       int                              `json:"exit_code"`
	OutputSHA256   string                           `json:"output_sha256"`
	DurationMillis int64                            `json:"duration_millis"`
	CompletedAt    time.Time                        `json:"completed_at"`
	Execution      *BatchValidationExecutionReceipt `json:"execution,omitempty"`
}

// Execution describes the actual native backend. Missing evidence in historical
// receipts remains unknown and cannot be promoted to a sandbox receipt.
type BatchValidationExecutionReceipt struct {
	JobID              string    `json:"job_id,omitempty"`
	Backend            string    `json:"backend"`
	BackendIdentity    string    `json:"backend_identity"`
	BackendGeneration  string    `json:"backend_generation"`
	IsolationGrade     string    `json:"isolation_grade"`
	NetworkPolicy      string    `json:"network_policy"`
	CredentialPolicy   string    `json:"credential_policy"`
	Readiness          string    `json:"readiness"`
	ReadinessCheckedAt time.Time `json:"readiness_checked_at"`
	WorkspaceSHA256    string    `json:"workspace_sha256"`
	ImageDigest        string    `json:"image_digest,omitempty"`
	TemplateReference  string    `json:"template_reference,omitempty"`
	TreeReaped         bool      `json:"tree_reaped"`
	StdinClosed        bool      `json:"stdin_closed"`
}

type BatchDeliveryReceipt struct {
	ID                 string
	PlanID             string
	Ordinal            int
	Generation         int64
	ProtocolVersion    string
	BaseCommit         string
	HeadCommit         string
	DiffSHA256         string
	CallChainSHA256    string
	DiffBytes          int64
	DiffStat           string
	ChangedFiles       []string
	TestReceipts       []BatchDeliveryTestReceipt
	EvidenceRefs       []string
	Limitations        []string
	OperationDigest    string
	RequestFingerprint string
	CreatedAt          time.Time
}

type BatchDeliveryReviewVerdict string

const (
	BatchReviewAccepted         BatchDeliveryReviewVerdict = "accepted"
	BatchReviewChangesRequested BatchDeliveryReviewVerdict = "changes_requested"
)

type BatchDeliveryReview struct {
	ID                 string
	PlanID             string
	Ordinal            int
	Generation         int64
	ProtocolVersion    string
	ReceiptID          string
	Reviewer           string
	Verdict            BatchDeliveryReviewVerdict
	Summary            string
	BaseCommit         string
	HeadCommit         string
	DiffSHA256         string
	CallChainSHA256    string
	FullDiffReviewed   bool
	CallChainReviewed  bool
	TestsReviewed      bool
	OperationDigest    string
	RequestFingerprint string
	CreatedAt          time.Time
}

type BatchDeliveryMergeQueueStatus string

const (
	BatchMergeQueuePrepared  BatchDeliveryMergeQueueStatus = "prepared"
	BatchMergeQueueRunning   BatchDeliveryMergeQueueStatus = "running"
	BatchMergeQueueBlocked   BatchDeliveryMergeQueueStatus = "blocked"
	BatchMergeQueueCompleted BatchDeliveryMergeQueueStatus = "completed"
	BatchMergeQueueAborted   BatchDeliveryMergeQueueStatus = "aborted"
)

type BatchDeliveryMergeQueue struct {
	ID                 string
	PlanID             string
	ProtocolVersion    string
	Status             BatchDeliveryMergeQueueStatus
	BaseCommit         string
	LatestBaseCommit   string
	IntegrationBranch  string
	IntegrationRoot    string
	IntegrationHead    string
	OrderedOrdinals    []int
	NextIndex          int
	FailureCode        string
	FailureSummary     string
	OperationDigest    string
	RequestFingerprint string
	CreatedBy          string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type BatchDeliveryMergeStep struct {
	QueueID        string
	StepIndex      int
	Ordinal        int
	InputHead      string
	PreMergeHead   string
	PostMergeHead  string
	Status         BatchDeliveryMergeQueueStatus
	ValidationJSON string
	FailureCode    string
	CreatedAt      time.Time
	CompletedAt    *time.Time
}
