package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	ChildTaskProposalVersion      = "child_task_proposal.v1"
	MaxChildTaskProposalJSONBytes = 32 * 1024
	MaxChildTaskTasks             = 6
	MaxChildTaskInputRefs         = 16
	MaxChildTaskExpectedArtifacts = 8
	MaxChildTaskGoalRunes         = MaxSpecialistInstructionRunes
	MaxChildTaskTimeoutMillis     = 30 * 60 * 1000
)

// ChildTaskSurface is the Go-resolved execution surface for one proposed
// child task. Models may only hint; the control plane decides.
type ChildTaskSurface string

const (
	ChildTaskSurfaceCore           ChildTaskSurface = "core"
	ChildTaskSurfaceReadOnlyFanout ChildTaskSurface = "readonly_fanout"
)

func ValidChildTaskSurface(surface ChildTaskSurface) bool {
	return surface == ChildTaskSurfaceCore || surface == ChildTaskSurfaceReadOnlyFanout
}

// ChildTaskSurfaceHint is the model's requested surface. Auto leaves the
// decision to the Go control plane.
type ChildTaskSurfaceHint string

const (
	ChildTaskSurfaceHintAuto           ChildTaskSurfaceHint = "auto"
	ChildTaskSurfaceHintCore           ChildTaskSurfaceHint = "core"
	ChildTaskSurfaceHintReadOnlyFanout ChildTaskSurfaceHint = "readonly_fanout"
)

func ValidChildTaskSurfaceHint(hint ChildTaskSurfaceHint) bool {
	switch hint {
	case ChildTaskSurfaceHintAuto, ChildTaskSurfaceHintCore, ChildTaskSurfaceHintReadOnlyFanout:
		return true
	default:
		return false
	}
}

// ChildTaskExpectedArtifact declares one artifact the child claims it will
// produce. It is model-supplied intent, never Go truth.
type ChildTaskExpectedArtifact struct {
	PathHint string `json:"path_hint"`
	Kind     string `json:"kind"`
}

// ChildTask is one bounded child proposal. All limits are upper bounds the
// Go control plane validates against the Run aggregate before admission.
type ChildTask struct {
	Worker             *WorkerProfile              `json:"worker,omitempty"`
	AllowedReadPaths   []string                    `json:"allowed_read_paths,omitempty"`
	ToolCallLimit      int64                       `json:"tool_call_limit,omitempty"`
	CostLimitMicros    int64                       `json:"cost_limit_micros,omitempty"`
	Ordinal            int                         `json:"-"`
	Title              string                      `json:"title"`
	Goal               string                      `json:"goal"`
	Skills             []string                    `json:"skills"`
	InputRefs          []string                    `json:"input_refs"`
	DependencyOrdinals []int                       `json:"dependency_ordinals"`
	SurfaceHint        ChildTaskSurfaceHint        `json:"surface_hint"`
	TurnLimit          int64                       `json:"turn_limit"`
	TokenLimit         int64                       `json:"token_limit"`
	TimeoutMillis      int64                       `json:"timeout_millis"`
	ExpectedArtifacts  []ChildTaskExpectedArtifact `json:"expected_artifacts"`
}

// ChildTaskProposalSpec is the strict, versioned wire shape the model may
// propose. Unknown fields are rejected.
type ChildTaskProposalSpec struct {
	Version string      `json:"version"`
	Tasks   []ChildTask `json:"tasks"`
}

// ChildTaskProposal is the persisted, Go-validated proposal.
type ChildTaskProposal struct {
	ID          string
	RunID       string
	RootAgentID string
	SessionID   string
	WorkspaceID string
	Status      string
	Spec        ChildTaskProposalSpec
	Surface     ChildTaskSurface
	FanoutTier  ReadOnlyFanoutTier
	RequestedBy string
	Version     int64
	CreatedAt   time.Time
}

// ChildTaskAssignment is the per-task durable admission record. The Go
// control plane fills Surface, FanoutTier, DependencyEdgeID, and the
// admitted identities; the model's proposal never sets them.
type ChildTaskAssignment struct {
	ProposalID       string
	Ordinal          int
	Surface          ChildTaskSurface
	FanoutTier       ReadOnlyFanoutTier
	Status           string
	DependencyEdgeID string
	TurnLimit        int64
	TokenLimit       int64
	TimeoutMillis    int64
	AdmittedAgentID  string
	FanoutPlanID     string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func DecodeChildTaskProposalSpec(raw []byte) (ChildTaskProposalSpec, error) {
	if len(raw) == 0 || len(raw) > MaxChildTaskProposalJSONBytes || !utf8.Valid(raw) {
		return ChildTaskProposalSpec{}, fmt.Errorf(
			"child task proposal payload must be valid UTF-8 JSON within %d bytes",
			MaxChildTaskProposalJSONBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var spec ChildTaskProposalSpec
	if err := decoder.Decode(&spec); err != nil {
		return ChildTaskProposalSpec{}, errors.New("child task proposal payload does not match child_task_proposal.v1")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ChildTaskProposalSpec{}, errors.New("child task proposal payload contains trailing data")
	}
	return NormalizeChildTaskProposalSpec(spec)
}

func NormalizeChildTaskProposalSpec(spec ChildTaskProposalSpec) (ChildTaskProposalSpec, error) {
	spec.Version = strings.TrimSpace(spec.Version)
	if spec.Version != ChildTaskProposalVersion {
		return ChildTaskProposalSpec{}, fmt.Errorf("unsupported child task proposal version %q", spec.Version)
	}
	if len(spec.Tasks) == 0 || len(spec.Tasks) > MaxChildTaskTasks {
		return ChildTaskProposalSpec{}, fmt.Errorf("child task proposal requires between 1 and %d tasks",
			MaxChildTaskTasks)
	}
	normalized := make([]ChildTask, len(spec.Tasks))
	seen := make(map[string]struct{}, len(spec.Tasks))
	for index, task := range spec.Tasks {
		task.Ordinal = index + 1
		task.Title = strings.TrimSpace(task.Title)
		task.Goal = strings.TrimSpace(task.Goal)
		if !utf8.ValidString(task.Title) || task.Title == "" || strings.ContainsRune(task.Title, 0) ||
			utf8.RuneCountInString(task.Title) > MaxAgentSessionTitleRunes {
			return ChildTaskProposalSpec{}, fmt.Errorf(
				"child task %d title must contain between 1 and %d characters",
				index+1, MaxAgentSessionTitleRunes)
		}
		if !utf8.ValidString(task.Goal) || task.Goal == "" || strings.ContainsRune(task.Goal, 0) ||
			utf8.RuneCountInString(task.Goal) > MaxChildTaskGoalRunes {
			return ChildTaskProposalSpec{}, fmt.Errorf(
				"child task %d goal must contain between 1 and %d characters",
				index+1, MaxChildTaskGoalRunes)
		}
		skills, err := NormalizeAgentSkills(task.Skills)
		if err != nil || len(skills) == 0 {
			return ChildTaskProposalSpec{}, fmt.Errorf("child task %d skills are invalid", index+1)
		}
		task.Skills = skills
		worker, err := NormalizeWorkerProfile(task.Worker)
		if err != nil {
			return ChildTaskProposalSpec{}, err
		}
		task.Worker = worker
		if len(task.InputRefs) > MaxChildTaskInputRefs {
			return ChildTaskProposalSpec{}, fmt.Errorf("child task %d input references exceed %d",
				index+1, MaxChildTaskInputRefs)
		}
		refs := make([]string, 0, len(task.InputRefs))
		for _, ref := range task.InputRefs {
			ref = strings.TrimSpace(ref)
			if !validChildTaskInputRef(ref) {
				return ChildTaskProposalSpec{}, fmt.Errorf("child task %d input reference is invalid",
					index+1)
			}
			refs = append(refs, ref)
		}
		task.InputRefs = refs
		if task.SurfaceHint == "" {
			task.SurfaceHint = ChildTaskSurfaceHintAuto
		}
		if !ValidChildTaskSurfaceHint(task.SurfaceHint) {
			return ChildTaskProposalSpec{}, fmt.Errorf("child task %d surface hint is invalid", index+1)
		}
		if ValidateSpecialistReadLimits(task.AllowedReadPaths, task.ToolCallLimit, task.CostLimitMicros) != nil || task.TurnLimit <= 0 || task.TokenLimit <= 0 || task.TokenLimit > MaxAgentTokenReservation ||
			task.TimeoutMillis <= 0 || task.TimeoutMillis > MaxChildTaskTimeoutMillis {
			return ChildTaskProposalSpec{}, fmt.Errorf("child task %d budget must be positive and bounded",
				index+1)
		}
		if len(task.DependencyOrdinals) > len(spec.Tasks) {
			return ChildTaskProposalSpec{}, fmt.Errorf("child task %d dependencies exceed the task count",
				index+1)
		}
		deps := make([]int, 0, len(task.DependencyOrdinals))
		for _, ordinal := range task.DependencyOrdinals {
			if ordinal == task.Ordinal {
				return ChildTaskProposalSpec{}, errors.New("child task cannot depend on itself")
			}
			if ordinal < 1 || ordinal > len(spec.Tasks) || slices.Contains(deps, ordinal) {
				return ChildTaskProposalSpec{}, fmt.Errorf("child task %d dependency ordinal is invalid",
					index+1)
			}
			deps = append(deps, ordinal)
		}
		slices.Sort(deps)
		task.DependencyOrdinals = deps
		if len(task.ExpectedArtifacts) > MaxChildTaskExpectedArtifacts {
			return ChildTaskProposalSpec{}, fmt.Errorf("child task %d expected artifacts exceed %d",
				index+1, MaxChildTaskExpectedArtifacts)
		}
		for artifactIndex, artifact := range task.ExpectedArtifacts {
			artifact.PathHint = strings.TrimSpace(artifact.PathHint)
			artifact.Kind = strings.TrimSpace(artifact.Kind)
			if !validChildTaskInputRef(artifact.PathHint) || artifact.Kind == "" ||
				len([]byte(artifact.Kind)) > 64 || strings.ContainsRune(artifact.Kind, 0) {
				return ChildTaskProposalSpec{}, fmt.Errorf(
					"child task %d expected artifact %d is invalid", index+1, artifactIndex+1)
			}
			task.ExpectedArtifacts[artifactIndex] = artifact
		}
		identity := task.Title + "\x00" + task.Goal
		if _, duplicate := seen[identity]; duplicate {
			return ChildTaskProposalSpec{}, errors.New("child tasks must have distinct goals")
		}
		seen[identity] = struct{}{}
		normalized[index] = task
	}
	spec.Tasks = normalized
	if err := validateChildTaskDependencyAcyclic(spec.Tasks); err != nil {
		return ChildTaskProposalSpec{}, err
	}
	return spec, nil
}

// validateChildTaskDependencyAcyclic rejects dependency cycles across tasks
// before the proposal is persisted.
func validateChildTaskDependencyAcyclic(tasks []ChildTask) error {
	state := make([]int, len(tasks)+1) // 0 unvisited, 1 visiting, 2 done
	var visit func(ordinal int) error
	visit = func(ordinal int) error {
		if state[ordinal] == 1 {
			return errors.New("child task dependencies contain a cycle")
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

func validChildTaskInputRef(ref string) bool {
	if ref == "" || strings.Contains(ref, "\\") || strings.ContainsRune(ref, 0) ||
		!utf8.ValidString(ref) || len([]byte(ref)) > MaxReadOnlyFanoutPathBytes {
		return false
	}
	if ref == ".." || strings.HasPrefix(ref, "../") || strings.HasPrefix(ref, "/") {
		return false
	}
	return true
}

// ResolveChildTaskSurface decides the execution surface for the whole
// proposal. Read-only task sets fan out unless core is explicitly requested.
// Write-capable, approval-capable, or explicitly core tasks stay on the core
// surface (at most two). Conflicting surface hints are rejected.
func ResolveChildTaskSurface(spec ChildTaskProposalSpec) (ChildTaskSurface, ReadOnlyFanoutTier, error) {
	normalized, err := NormalizeChildTaskProposalSpec(spec)
	if err != nil {
		return "", "", err
	}
	coreHint := false
	fanoutHint := false
	requiresCore := false
	readonlyEligible := true
	for _, task := range normalized.Tasks {
		if task.Worker != nil || len(task.AllowedReadPaths) > 0 || task.ToolCallLimit > 0 || task.CostLimitMicros > 0 {
			if task.SurfaceHint == ChildTaskSurfaceHintReadOnlyFanout {
				return "", "", errors.New("execution workers and independent read and cost limits require the core child surface")
			}
			requiresCore = true
		}
		switch task.SurfaceHint {
		case ChildTaskSurfaceHintCore:
			coreHint = true
		case ChildTaskSurfaceHintReadOnlyFanout:
			fanoutHint = true
		}
		for _, skill := range task.Skills {
			if skillWritesOrApproves(skill) {
				requiresCore = true
			}
		}
		if taskSkillSetExceedsReadOnly(task.Skills) {
			readonlyEligible = false
		}
	}
	if coreHint && fanoutHint {
		return "", "", errors.New("child task proposal cannot mix core and read-only fan-out hints")
	}
	if requiresCore || coreHint {
		if fanoutHint {
			return "", "", errors.New("child task proposal hints read-only fan-out but contains write-capable tasks")
		}
		if len(normalized.Tasks) > MaxAgentChildren {
			return "", "", fmt.Errorf("core child tasks cannot exceed %d", MaxAgentChildren)
		}
		return ChildTaskSurfaceCore, "", nil
	}
	// Read-only fan-out tasks are independent parallel shards: durable
	// inter-task dependencies are a core-surface contract only.
	for _, task := range normalized.Tasks {
		if len(task.DependencyOrdinals) > 0 {
			return "", "", errors.New("read-only fan-out tasks cannot declare inter-task dependencies")
		}
	}
	if !readonlyEligible {
		return "", "", errors.New("child task skill set is not eligible for read-only fan-out")
	}
	tier := ReadOnlyFanoutAuto
	switch len(normalized.Tasks) {
	case 1:
		tier = ReadOnlyFanoutOne
	case 2:
		tier = ReadOnlyFanoutTwo
	case 3, 4:
		tier = ReadOnlyFanoutFour
	default:
		tier = ReadOnlyFanoutSix
	}
	return ChildTaskSurfaceReadOnlyFanout, tier, nil
}

// readonlyChildTaskSkills is the closed skill set a read-only fan-out child
// may carry. Anything else (writes, Shell, proposals) forces the core
// surface or rejects the task.
func readonlyChildTaskSkills() map[string]struct{} {
	return map[string]struct{}{
		"model.chat": {}, "list_workspace": {}, "read_file": {},
	}
}

func skillWritesOrApproves(skill string) bool {
	_, readonly := readonlyChildTaskSkills()[skill]
	return !readonly
}

func taskSkillSetExceedsReadOnly(skills []string) bool {
	allowed := readonlyChildTaskSkills()
	for _, skill := range skills {
		if _, ok := allowed[skill]; !ok {
			return true
		}
	}
	return false
}

const (
	ChildTaskProposalProposed   = "proposed"
	ChildTaskProposalApproved   = "approved"
	ChildTaskProposalDenied     = "denied"
	ChildTaskAssignmentProposed = "proposed"
	ChildTaskAssignmentAdmitted = "admitted"
)

func ValidChildTaskProposalStatus(status string) bool {
	return status == ChildTaskProposalProposed || status == ChildTaskProposalApproved ||
		status == ChildTaskProposalDenied
}

// ChildTaskOperation is the proposal idempotency record. Like the delegation
// operation, the lease identity is validated in flight and never persisted.
type ChildTaskOperation struct {
	KeyDigest          string
	RequestFingerprint string
	ProposalID         string
	RunID              string
	SessionID          string
	WorkspaceID        string
	RootAgentID        string
	LeaseID            string
	LeaseGeneration    int64
	RequestedBy        string
	CreatedAt          time.Time
}

func (o ChildTaskOperation) Validate() error {
	for _, value := range []string{o.ProposalID, o.RunID, o.SessionID, o.RootAgentID, o.RequestedBy} {
		if !validAgentIdentity(value, false) {
			return errors.New("child task operation identities are required and normalized")
		}
	}
	if !validAgentIdentity(o.WorkspaceID, true) || !validLowerHexDigest(o.KeyDigest) ||
		!validLowerHexDigest(o.RequestFingerprint) {
		return errors.New("child task operation scope or digest is invalid")
	}
	if !validAgentIdentity(o.LeaseID, false) || o.LeaseGeneration <= 0 {
		return errors.New("child task operation requires an active lease")
	}
	if o.CreatedAt.IsZero() {
		return errors.New("child task operation creation time is required")
	}
	return nil
}

// ChildTaskReview is the operator decision over one proposal.
type ChildTaskReview struct {
	ProposalID string
	Action     string
	Reviewer   string
	FanoutTier ReadOnlyFanoutTier
	ReviewedAt time.Time
}

func (r ChildTaskReview) Normalize() (ChildTaskReview, error) {
	r.ProposalID = strings.TrimSpace(r.ProposalID)
	r.Action = strings.TrimSpace(r.Action)
	r.Reviewer = strings.TrimSpace(r.Reviewer)
	if !validAgentIdentity(r.ProposalID, false) || !validAgentIdentity(r.Reviewer, false) {
		return ChildTaskReview{}, errors.New("child task review identities are required")
	}
	if r.Action != "approve" && r.Action != "deny" {
		return ChildTaskReview{}, errors.New("child task review action must be approve or deny")
	}
	if r.FanoutTier == "" || r.FanoutTier == ReadOnlyFanoutAuto {
		return ChildTaskReview{}, errors.New("child task review requires a concrete fan-out tier")
	}
	if _, err := ParseReadOnlyFanoutTier(string(r.FanoutTier)); err != nil ||
		r.FanoutTier == ReadOnlyFanoutAuto {
		return ChildTaskReview{}, errors.New("child task review fan-out tier must be 1, 2, 4, or 6")
	}
	if r.ReviewedAt.IsZero() {
		r.ReviewedAt = time.Now().UTC()
	} else {
		r.ReviewedAt = r.ReviewedAt.UTC()
	}
	return r, nil
}

func (p ChildTaskProposal) Validate() error {
	for _, value := range []string{p.ID, p.RunID, p.RootAgentID, p.SessionID} {
		if !validAgentIdentity(value, false) {
			return errors.New("child task proposal identities are required and normalized")
		}
	}
	if !validAgentIdentity(p.WorkspaceID, true) || !validAgentIdentity(p.RequestedBy, false) {
		return errors.New("child task proposal scope is invalid")
	}
	if !ValidChildTaskProposalStatus(p.Status) {
		return errors.New("child task proposal status is invalid")
	}
	if !ValidChildTaskSurface(p.Surface) {
		return errors.New("child task proposal surface is invalid")
	}
	if p.FanoutTier != "" {
		if _, err := ParseReadOnlyFanoutTier(string(p.FanoutTier)); err != nil ||
			p.FanoutTier == ReadOnlyFanoutAuto {
			return errors.New("child task proposal fan-out tier is invalid")
		}
	}
	if p.Surface == ChildTaskSurfaceCore && p.FanoutTier != "" {
		return errors.New("core child task proposals cannot carry a fan-out tier")
	}
	if p.Surface == ChildTaskSurfaceReadOnlyFanout && p.FanoutTier == "" {
		return errors.New("read-only fan-out child task proposals require a fan-out tier")
	}
	if p.Version != 1 || p.CreatedAt.IsZero() {
		return errors.New("child task proposal version and creation time are required")
	}
	return p.Spec.Validate()
}

// SpecJSONFingerprint binds the dedup identity to the canonical spec JSON.
func (s ChildTaskProposalSpec) SpecJSONFingerprint() string {
	encoded, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func (s ChildTaskProposalSpec) Validate() error {
	normalized, err := NormalizeChildTaskProposalSpec(s)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	original, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if !bytes.Equal(encoded, original) {
		return errors.New("child task proposal specification must be normalized")
	}
	return nil
}
