package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	RunModeProtocolVersion = "run_mode.v1"
	RunModePolicyVersion   = "mode_policy.v1"
	MaxRunModeReasonRunes  = 1024
)

type ExecutionSurface string

const (
	ExecutionSurfaceCode  ExecutionSurface = "code"
	ExecutionSurfaceCyber ExecutionSurface = "cyber"
)

func ParseExecutionSurface(value string) (ExecutionSurface, error) {
	surface := ExecutionSurface(strings.ToLower(strings.TrimSpace(value)))
	switch surface {
	case ExecutionSurfaceCode, ExecutionSurfaceCyber:
		return surface, nil
	default:
		return "", fmt.Errorf("unsupported execution surface %q", value)
	}
}

func (s ExecutionSurface) Valid() bool {
	parsed, err := ParseExecutionSurface(string(s))
	return err == nil && parsed == s
}

type ExecutionPhase string

const (
	ExecutionPhasePlan    ExecutionPhase = "plan"
	ExecutionPhaseDeliver ExecutionPhase = "deliver"
)

func ParseExecutionPhase(value string) (ExecutionPhase, error) {
	phase := ExecutionPhase(strings.ToLower(strings.TrimSpace(value)))
	switch phase {
	case ExecutionPhasePlan, ExecutionPhaseDeliver:
		return phase, nil
	default:
		return "", fmt.Errorf("unsupported execution phase %q", value)
	}
}

func (p ExecutionPhase) Valid() bool {
	parsed, err := ParseExecutionPhase(string(p))
	return err == nil && parsed == p
}

// RunModeSnapshot is an append-only policy snapshot for one Run. Surface,
// Profile, Workspace scope, and PolicyVersion are fixed. Phase can move through
// an explicit operator transition while the Run is quiescent. Command network
// scope grows through the audited exact-host transition; independent webpage
// access changes through a revision-bound operator selection.
type RunModeSnapshot struct {
	ID              string
	RunID           string
	MissionID       string
	Revision        int64
	ProtocolVersion string
	Surface         ExecutionSurface
	Phase           ExecutionPhase
	Profile         Profile
	Scope           Scope
	PolicyVersion   string
	RequestedBy     string
	Reason          string
	CreatedAt       time.Time
}

func NewInitialRunModeSnapshot(id string, run Run, mission Mission, surface ExecutionSurface,
	phase ExecutionPhase, requestedBy string, reason string, at time.Time,
) (RunModeSnapshot, error) {
	if surface == "" {
		surface = ExecutionSurfaceCode
	}
	if phase == "" {
		phase = ExecutionPhaseDeliver
	}
	snapshot := RunModeSnapshot{
		ID: strings.TrimSpace(id), RunID: run.ID, MissionID: mission.ID, Revision: 1,
		ProtocolVersion: RunModeProtocolVersion, Surface: surface, Phase: phase,
		Profile: mission.Profile, Scope: CloneScope(mission.Scope),
		PolicyVersion: RunModePolicyVersion, RequestedBy: strings.TrimSpace(requestedBy),
		Reason: strings.TrimSpace(reason), CreatedAt: at.UTC(),
	}
	if snapshot.Reason == "" {
		snapshot.Reason = "initial Run mode"
	}
	if run.MissionID != mission.ID {
		return RunModeSnapshot{}, errors.New("run mode Run and Mission identities do not match")
	}
	if err := snapshot.Validate(); err != nil {
		return RunModeSnapshot{}, err
	}
	return snapshot, nil
}

func (s RunModeSnapshot) Validate() error {
	for label, value := range map[string]string{
		"snapshot id": s.ID, "Run id": s.RunID, "Mission id": s.MissionID,
		"requester": s.RequestedBy, "policy version": s.PolicyVersion,
	} {
		if !ValidAgentID(value) || strings.ContainsRune(value, 0) {
			return fmt.Errorf("run mode %s must be normalized and bounded UTF-8", label)
		}
	}
	if s.Revision <= 0 {
		return errors.New("run mode revision must be positive")
	}
	if s.ProtocolVersion != RunModeProtocolVersion {
		return fmt.Errorf("unsupported run mode protocol %q", s.ProtocolVersion)
	}
	if !s.Surface.Valid() {
		return fmt.Errorf("invalid run mode surface %q", s.Surface)
	}
	if !s.Phase.Valid() {
		return fmt.Errorf("invalid run mode phase %q", s.Phase)
	}
	if _, err := ParseProfile(string(s.Profile)); err != nil {
		return err
	}
	if s.PolicyVersion != RunModePolicyVersion {
		return fmt.Errorf("unsupported run mode policy %q", s.PolicyVersion)
	}
	if !utf8.ValidString(s.Reason) || strings.TrimSpace(s.Reason) != s.Reason ||
		s.Reason == "" || utf8.RuneCountInString(s.Reason) > MaxRunModeReasonRunes ||
		strings.ContainsRune(s.Reason, 0) {
		return fmt.Errorf("run mode reason must contain between 1 and %d normalized UTF-8 characters",
			MaxRunModeReasonRunes)
	}
	if s.CreatedAt.IsZero() {
		return errors.New("run mode creation time is required")
	}
	return validateRunModeScope(s.Scope)
}

func (s RunModeSnapshot) Next(id string, phase ExecutionPhase, requestedBy string,
	reason string, at time.Time,
) (RunModeSnapshot, error) {
	if err := s.Validate(); err != nil {
		return RunModeSnapshot{}, err
	}
	if !phase.Valid() {
		return RunModeSnapshot{}, fmt.Errorf("invalid run mode phase %q", phase)
	}
	if phase == s.Phase {
		return RunModeSnapshot{}, errors.New("run mode transition must change phase")
	}
	next := s
	next.ID = strings.TrimSpace(id)
	next.Revision++
	next.Phase = phase
	next.RequestedBy = strings.TrimSpace(requestedBy)
	next.Reason = strings.TrimSpace(reason)
	next.CreatedAt = at.UTC()
	next.Scope = CloneScope(s.Scope)
	if err := next.Validate(); err != nil {
		return RunModeSnapshot{}, err
	}
	if next.CreatedAt.Before(s.CreatedAt) {
		return RunModeSnapshot{}, errors.New("run mode transition time cannot move backwards")
	}
	return next, nil
}

func (s RunModeSnapshot) SamePolicy(other RunModeSnapshot) bool {
	if !s.SameStaticPolicy(other) || !s.Scope.SameWebAccess(other.Scope) ||
		s.Scope.NetworkMode != other.Scope.NetworkMode ||
		len(s.Scope.AllowedTargets) != len(other.Scope.AllowedTargets) {
		return false
	}
	for index := range s.Scope.AllowedTargets {
		if s.Scope.AllowedTargets[index] != other.Scope.AllowedTargets[index] {
			return false
		}
	}
	return true
}

// SameStaticPolicy compares every immutable Run mode boundary while leaving
// the explicitly mutable phase and exact-host network authority aside.
func (s RunModeSnapshot) SameStaticPolicy(other RunModeSnapshot) bool {
	return s.RunID == other.RunID && s.MissionID == other.MissionID &&
		s.ProtocolVersion == other.ProtocolVersion && s.Surface == other.Surface &&
		s.Profile == other.Profile && s.PolicyVersion == other.PolicyVersion &&
		s.Scope.WorkspaceID == other.Scope.WorkspaceID
}

func CanChangeRunPhase(status RunStatus) bool {
	return status == RunCreated || status == RunPaused
}

type RunModeOperation struct {
	KeyDigest          string
	RequestFingerprint string
	SnapshotID         string
	RunID              string
	RequestedBy        string
	CreatedAt          time.Time
}

func (o RunModeOperation) Validate() error {
	if !validLowerHexDigest(o.KeyDigest) || !validLowerHexDigest(o.RequestFingerprint) {
		return errors.New("run mode operation key and request fingerprint must be lowercase SHA-256 digests")
	}
	for label, value := range map[string]string{
		"snapshot id": o.SnapshotID, "Run id": o.RunID, "requester": o.RequestedBy,
	} {
		if !ValidAgentID(value) || strings.ContainsRune(value, 0) {
			return fmt.Errorf("run mode operation %s must be normalized and bounded UTF-8", label)
		}
	}
	if o.CreatedAt.IsZero() {
		return errors.New("run mode operation creation time is required")
	}
	return nil
}

func CloneScope(scope Scope) Scope {
	cloned := scope
	cloned.AllowedTargets = append([]string(nil), scope.AllowedTargets...)
	cloned.WebAllowedTargets = append([]string(nil), scope.WebAllowedTargets...)
	return cloned
}

func validateRunModeScope(scope Scope) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	if !utf8.ValidString(scope.WorkspaceID) || strings.TrimSpace(scope.WorkspaceID) != scope.WorkspaceID ||
		utf8.RuneCountInString(scope.WorkspaceID) > MaxAgentIdentityRunes ||
		strings.ContainsRune(scope.WorkspaceID, 0) {
		return errors.New("run mode scope workspace must be normalized and bounded UTF-8")
	}
	if len(scope.AllowedTargets) > 256 {
		return errors.New("run mode scope target allowlist exceeds 256 entries")
	}
	for _, target := range scope.AllowedTargets {
		if !utf8.ValidString(target) || target == "" || strings.TrimSpace(target) != target ||
			utf8.RuneCountInString(target) > 512 || strings.ContainsRune(target, 0) {
			return errors.New("run mode scope targets must be normalized and bounded UTF-8")
		}
	}
	return nil
}
