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
	SpecialistDelegationVersion        = "specialist_delegation.v1"
	AgentSkillSpecialistDelegation     = "specialist_delegation_propose"
	MaxSpecialistDelegationAssignments = MaxAgentChildren
	MaxSpecialistDelegationJSONBytes   = 32 * 1024
	MaxSpecialistDelegationGoalRunes   = MaxSpecialistInstructionRunes
)

func DelegableAgentSkill(skill string) bool {
	return strings.TrimSpace(skill) != AgentSkillSpecialistDelegation
}

type SpecialistDelegationStatus string

const SpecialistDelegationProposed SpecialistDelegationStatus = "proposed"

type SpecialistDelegationAssignment struct {
	AllowedReadPaths []string `json:"allowed_read_paths,omitempty"`
	ToolCallLimit    int64    `json:"tool_call_limit,omitempty"`
	CostLimitMicros  int64    `json:"cost_limit_micros,omitempty"`
	Ordinal          int      `json:"-"`
	Title            string   `json:"title"`
	Goal             string   `json:"goal"`
	Skills           []string `json:"skills"`
	TurnLimit        int64    `json:"turn_limit"`
	TokenLimit       int64    `json:"token_limit"`
}

type SpecialistDelegationSpec struct {
	Version     string                           `json:"version"`
	Assignments []SpecialistDelegationAssignment `json:"assignments"`
}

type SpecialistDelegationProposal struct {
	ID          string
	RunID       string
	RootAgentID string
	SessionID   string
	WorkspaceID string
	Status      SpecialistDelegationStatus
	Spec        SpecialistDelegationSpec
	RequestedBy string
	Version     int64
	CreatedAt   time.Time
}

type SpecialistDelegationOperation struct {
	KeyDigest          string
	RequestFingerprint string
	InvocationID       string
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

func DecodeSpecialistDelegationSpec(raw []byte) (SpecialistDelegationSpec, error) {
	if len(raw) == 0 || len(raw) > MaxSpecialistDelegationJSONBytes || !utf8.Valid(raw) {
		return SpecialistDelegationSpec{}, fmt.Errorf(
			"specialist delegation payload must be valid UTF-8 JSON within %d bytes",
			MaxSpecialistDelegationJSONBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var spec SpecialistDelegationSpec
	if err := decoder.Decode(&spec); err != nil {
		return SpecialistDelegationSpec{}, errors.New("specialist delegation payload does not match specialist_delegation.v1")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return SpecialistDelegationSpec{}, errors.New("specialist delegation payload contains trailing data")
	}
	return NormalizeSpecialistDelegationSpec(spec)
}

func NormalizeSpecialistDelegationSpec(spec SpecialistDelegationSpec) (SpecialistDelegationSpec, error) {
	spec.Version = strings.TrimSpace(spec.Version)
	if spec.Version != SpecialistDelegationVersion {
		return SpecialistDelegationSpec{}, fmt.Errorf("unsupported specialist delegation version %q", spec.Version)
	}
	if len(spec.Assignments) == 0 || len(spec.Assignments) > MaxSpecialistDelegationAssignments {
		return SpecialistDelegationSpec{}, fmt.Errorf("specialist delegation requires between 1 and %d assignments",
			MaxSpecialistDelegationAssignments)
	}
	normalized := make([]SpecialistDelegationAssignment, len(spec.Assignments))
	seen := make(map[string]struct{}, len(spec.Assignments))
	for index, assignment := range spec.Assignments {
		assignment.Ordinal = index + 1
		if err := ValidateSpecialistReadLimits(assignment.AllowedReadPaths, assignment.ToolCallLimit, assignment.CostLimitMicros); err != nil {
			return SpecialistDelegationSpec{}, err
		}
		assignment.Title = strings.TrimSpace(assignment.Title)
		assignment.Goal = strings.TrimSpace(assignment.Goal)
		if !utf8.ValidString(assignment.Title) || assignment.Title == "" || strings.ContainsRune(assignment.Title, 0) ||
			utf8.RuneCountInString(assignment.Title) > MaxAgentSessionTitleRunes {
			return SpecialistDelegationSpec{}, fmt.Errorf(
				"specialist delegation assignment %d title must contain between 1 and %d characters",
				index+1, MaxAgentSessionTitleRunes)
		}
		if !utf8.ValidString(assignment.Goal) || assignment.Goal == "" || strings.ContainsRune(assignment.Goal, 0) ||
			utf8.RuneCountInString(assignment.Goal) > MaxSpecialistDelegationGoalRunes {
			return SpecialistDelegationSpec{}, fmt.Errorf(
				"specialist delegation assignment %d goal must contain between 1 and %d characters",
				index+1, MaxSpecialistDelegationGoalRunes)
		}
		skills, err := NormalizeAgentSkills(assignment.Skills)
		if err != nil || len(skills) == 0 {
			return SpecialistDelegationSpec{}, fmt.Errorf(
				"specialist delegation assignment %d skills are invalid", index+1)
		}
		assignment.Skills = skills
		if assignment.TurnLimit <= 0 || assignment.TokenLimit <= 0 ||
			assignment.TokenLimit > MaxAgentTokenReservation {
			return SpecialistDelegationSpec{}, fmt.Errorf(
				"specialist delegation assignment %d budget must be positive and bounded", index+1)
		}
		identity := assignment.Title + "\x00" + assignment.Goal
		if _, duplicate := seen[identity]; duplicate {
			return SpecialistDelegationSpec{}, errors.New("specialist delegation assignments must have distinct goals")
		}
		seen[identity] = struct{}{}
		normalized[index] = assignment
	}
	spec.Assignments = normalized
	return spec, nil
}

func (s SpecialistDelegationSpec) Validate() error {
	normalized, err := NormalizeSpecialistDelegationSpec(s)
	if err != nil {
		return err
	}
	if normalized.Version != s.Version || !slices.EqualFunc(normalized.Assignments, s.Assignments,
		func(left, right SpecialistDelegationAssignment) bool {
			return left.Ordinal == right.Ordinal && left.Title == right.Title && left.Goal == right.Goal &&
				slices.Equal(left.Skills, right.Skills) && left.TurnLimit == right.TurnLimit &&
				left.TokenLimit == right.TokenLimit && slices.Equal(left.AllowedReadPaths, right.AllowedReadPaths) && left.ToolCallLimit == right.ToolCallLimit && left.CostLimitMicros == right.CostLimitMicros
		}) {
		return errors.New("specialist delegation specification must be normalized")
	}
	return nil
}

func (p SpecialistDelegationProposal) Validate() error {
	for _, value := range []string{p.ID, p.RunID, p.RootAgentID, p.SessionID} {
		if !validAgentIdentity(value, false) {
			return errors.New("specialist delegation proposal identities are required and normalized")
		}
	}
	if !validAgentIdentity(p.WorkspaceID, true) || !validAgentIdentity(p.RequestedBy, false) {
		return errors.New("specialist delegation proposal scope is invalid")
	}
	if p.Status != SpecialistDelegationProposed {
		return fmt.Errorf("invalid specialist delegation proposal status %q", p.Status)
	}
	if p.Version != 1 || p.CreatedAt.IsZero() {
		return errors.New("specialist delegation proposal version and creation time are required")
	}
	return p.Spec.Validate()
}

func (o SpecialistDelegationOperation) Validate() error {
	if err := o.validatePersistableFields(); err != nil {
		return err
	}
	if !validAgentIdentity(o.LeaseID, false) || o.LeaseGeneration <= 0 {
		return errors.New("specialist delegation operation requires an active lease")
	}
	return nil
}

func (o SpecialistDelegationOperation) ValidatePersisted() error {
	if o.LeaseID != "" || o.LeaseGeneration != 0 {
		return errors.New("persisted specialist delegation operation contains lease identity")
	}
	return o.validatePersistableFields()
}

func (o SpecialistDelegationOperation) validatePersistableFields() error {
	for _, value := range []string{o.InvocationID, o.ProposalID, o.RunID, o.SessionID,
		o.RootAgentID, o.RequestedBy} {
		if !validAgentIdentity(value, false) {
			return errors.New("specialist delegation operation identities are required and normalized")
		}
	}
	if !validAgentIdentity(o.WorkspaceID, true) || !validLowerHexDigest(o.KeyDigest) ||
		!validLowerHexDigest(o.RequestFingerprint) {
		return errors.New("specialist delegation operation scope or digest is invalid")
	}
	if o.CreatedAt.IsZero() {
		return errors.New("specialist delegation operation creation time is required")
	}
	return nil
}

func validLowerHexDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, current := range value {
		if (current < '0' || current > '9') && (current < 'a' || current > 'f') {
			return false
		}
	}
	return true
}
