package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	RunExecutionProfileProtocolVersion = "run_execution_profile.v1"
	RunExecutionProfilePolicyVersion   = "execution_profile_policy.v1"
	MaxRunExecutionProfileReasonRunes  = 1024
)

type RunExecutionProfile string

const (
	RunExecutionProfilePreview RunExecutionProfile = "preview"
	RunExecutionProfileDocker  RunExecutionProfile = "docker"
	RunExecutionProfileSBX     RunExecutionProfile = "sbx"
	RunExecutionProfileLocal   RunExecutionProfile = "local"
)

type ExecutionBackend string

const (
	ExecutionBackendNoop   ExecutionBackend = "noop"
	ExecutionBackendDocker ExecutionBackend = "docker"
	ExecutionBackendSBX    ExecutionBackend = "sbx"
	ExecutionBackendLocal  ExecutionBackend = "local"
)

type ExecutionApprovalPolicy string

const (
	ExecutionApprovalNone   ExecutionApprovalPolicy = "none"
	ExecutionApprovalAlways ExecutionApprovalPolicy = "always"
)

type ExecutionFilesystemScope string

const (
	ExecutionFilesystemNone      ExecutionFilesystemScope = "none"
	ExecutionFilesystemWorkspace ExecutionFilesystemScope = "workspace"
)

type ExecutionNetworkScope string

const ExecutionNetworkDisabled ExecutionNetworkScope = "disabled"

type ExecutionRiskTier string

const (
	ExecutionRiskMinimal  ExecutionRiskTier = "minimal"
	ExecutionRiskElevated ExecutionRiskTier = "elevated"
	ExecutionRiskHigh     ExecutionRiskTier = "high"
)

type ExecutionRequiredGate string

const (
	ExecutionGateNone                  ExecutionRequiredGate = "none"
	ExecutionGateDockerProductionStart ExecutionRequiredGate = "docker_production_start_gate"
	ExecutionGateLocalOSSandbox        ExecutionRequiredGate = "local_os_sandbox_gate"
	ExecutionGateSBXMicroVM            ExecutionRequiredGate = "sbx_microvm_gate"
)

type runExecutionProfileDefinition struct {
	Backend         ExecutionBackend
	ApprovalPolicy  ExecutionApprovalPolicy
	FilesystemScope ExecutionFilesystemScope
	NetworkScope    ExecutionNetworkScope
	RiskTier        ExecutionRiskTier
	RequiredGate    ExecutionRequiredGate
}

var runExecutionProfileDefinitions = map[RunExecutionProfile]runExecutionProfileDefinition{
	RunExecutionProfilePreview: {
		Backend: ExecutionBackendNoop, ApprovalPolicy: ExecutionApprovalNone,
		FilesystemScope: ExecutionFilesystemNone, NetworkScope: ExecutionNetworkDisabled,
		RiskTier: ExecutionRiskMinimal, RequiredGate: ExecutionGateNone,
	},
	RunExecutionProfileDocker: {
		Backend: ExecutionBackendDocker, ApprovalPolicy: ExecutionApprovalAlways,
		FilesystemScope: ExecutionFilesystemWorkspace, NetworkScope: ExecutionNetworkDisabled,
		RiskTier: ExecutionRiskElevated, RequiredGate: ExecutionGateDockerProductionStart,
	},
	RunExecutionProfileLocal: {
		Backend: ExecutionBackendLocal, ApprovalPolicy: ExecutionApprovalAlways,
		FilesystemScope: ExecutionFilesystemWorkspace, NetworkScope: ExecutionNetworkDisabled,
		RiskTier: ExecutionRiskHigh, RequiredGate: ExecutionGateLocalOSSandbox,
	},
	RunExecutionProfileSBX: {
		Backend: ExecutionBackendSBX, ApprovalPolicy: ExecutionApprovalAlways,
		FilesystemScope: ExecutionFilesystemWorkspace, NetworkScope: ExecutionNetworkDisabled,
		RiskTier: ExecutionRiskElevated, RequiredGate: ExecutionGateSBXMicroVM,
	},
}

func ParseRunExecutionProfile(value string) (RunExecutionProfile, error) {
	profile := RunExecutionProfile(strings.ToLower(strings.TrimSpace(value)))
	if _, ok := runExecutionProfileDefinitions[profile]; !ok {
		return "", fmt.Errorf("unsupported Run execution profile %q", value)
	}
	return profile, nil
}

func (p RunExecutionProfile) Valid() bool {
	parsed, err := ParseRunExecutionProfile(string(p))
	return err == nil && parsed == p
}

// RunExecutionProfileSnapshot records operator intent only. Process and
// capability authority remain false until a backend-specific gate is added
// and independently audited.
type RunExecutionProfileSnapshot struct {
	ID                  string
	RunID               string
	MissionID           string
	Revision            int64
	ProtocolVersion     string
	Profile             RunExecutionProfile
	Backend             ExecutionBackend
	ApprovalPolicy      ExecutionApprovalPolicy
	FilesystemScope     ExecutionFilesystemScope
	NetworkScope        ExecutionNetworkScope
	RiskTier            ExecutionRiskTier
	RequiredGate        ExecutionRequiredGate
	PolicyVersion       string
	ProcessEnabled      bool
	ExecutionAuthorized bool
	CapabilityGrant     bool
	RequestedBy         string
	Reason              string
	CreatedAt           time.Time
}

func NewInitialRunExecutionProfileSnapshot(id string, run Run, mission Mission,
	requestedBy string, reason string, at time.Time,
) (RunExecutionProfileSnapshot, error) {
	snapshot := newRunExecutionProfileSnapshot(id, run.ID, mission.ID, 1,
		RunExecutionProfilePreview, requestedBy, reason, at)
	if snapshot.Reason == "" {
		snapshot.Reason = "initial preview execution profile"
	}
	if run.MissionID != mission.ID {
		return RunExecutionProfileSnapshot{}, errors.New(
			"Run execution profile Run and Mission identities do not match")
	}
	if err := snapshot.Validate(); err != nil {
		return RunExecutionProfileSnapshot{}, err
	}
	return snapshot, nil
}

func newRunExecutionProfileSnapshot(id string, runID string, missionID string,
	revision int64, profile RunExecutionProfile, requestedBy string, reason string,
	at time.Time,
) RunExecutionProfileSnapshot {
	definition := runExecutionProfileDefinitions[profile]
	return RunExecutionProfileSnapshot{
		ID: strings.TrimSpace(id), RunID: runID, MissionID: missionID, Revision: revision,
		ProtocolVersion: RunExecutionProfileProtocolVersion, Profile: profile,
		Backend: definition.Backend, ApprovalPolicy: definition.ApprovalPolicy,
		FilesystemScope: definition.FilesystemScope, NetworkScope: definition.NetworkScope,
		RiskTier: definition.RiskTier, RequiredGate: definition.RequiredGate,
		PolicyVersion: RunExecutionProfilePolicyVersion,
		RequestedBy:   strings.TrimSpace(requestedBy), Reason: strings.TrimSpace(reason),
		CreatedAt: at.UTC(),
	}
}

func (s RunExecutionProfileSnapshot) Validate() error {
	for label, value := range map[string]string{
		"snapshot id": s.ID, "Run id": s.RunID, "Mission id": s.MissionID,
		"requester": s.RequestedBy, "policy version": s.PolicyVersion,
	} {
		if !ValidAgentID(value) || strings.ContainsRune(value, 0) {
			return fmt.Errorf("Run execution profile %s must be normalized and bounded UTF-8", label)
		}
	}
	if s.Revision <= 0 {
		return errors.New("Run execution profile revision must be positive")
	}
	if s.ProtocolVersion != RunExecutionProfileProtocolVersion {
		return fmt.Errorf("unsupported Run execution profile protocol %q", s.ProtocolVersion)
	}
	definition, ok := runExecutionProfileDefinitions[s.Profile]
	if !ok {
		return fmt.Errorf("invalid Run execution profile %q", s.Profile)
	}
	if s.Backend != definition.Backend || s.ApprovalPolicy != definition.ApprovalPolicy ||
		s.FilesystemScope != definition.FilesystemScope || s.NetworkScope != definition.NetworkScope ||
		s.RiskTier != definition.RiskTier || s.RequiredGate != definition.RequiredGate {
		return errors.New("Run execution profile controls do not match the selected profile")
	}
	if s.PolicyVersion != RunExecutionProfilePolicyVersion {
		return fmt.Errorf("unsupported Run execution profile policy %q", s.PolicyVersion)
	}
	if s.ProcessEnabled || s.ExecutionAuthorized || s.CapabilityGrant {
		return errors.New("Run execution profile selection cannot grant process or capability authority")
	}
	if !utf8.ValidString(s.Reason) || strings.TrimSpace(s.Reason) != s.Reason ||
		s.Reason == "" || utf8.RuneCountInString(s.Reason) > MaxRunExecutionProfileReasonRunes ||
		strings.ContainsRune(s.Reason, 0) {
		return fmt.Errorf("Run execution profile reason must contain between 1 and %d normalized UTF-8 characters",
			MaxRunExecutionProfileReasonRunes)
	}
	if s.CreatedAt.IsZero() {
		return errors.New("Run execution profile creation time is required")
	}
	return nil
}

func (s RunExecutionProfileSnapshot) Next(id string, profile RunExecutionProfile,
	requestedBy string, reason string, at time.Time,
) (RunExecutionProfileSnapshot, error) {
	if err := s.Validate(); err != nil {
		return RunExecutionProfileSnapshot{}, err
	}
	if !profile.Valid() {
		return RunExecutionProfileSnapshot{}, fmt.Errorf("invalid Run execution profile %q", profile)
	}
	if profile == s.Profile && !(s.IsDefaultPreview() && profile == RunExecutionProfilePreview) {
		return RunExecutionProfileSnapshot{}, errors.New(
			"Run execution profile transition must change profile")
	}
	next := newRunExecutionProfileSnapshot(id, s.RunID, s.MissionID, s.Revision+1,
		profile, requestedBy, reason, at)
	if err := next.Validate(); err != nil {
		return RunExecutionProfileSnapshot{}, err
	}
	if next.CreatedAt.Before(s.CreatedAt) {
		return RunExecutionProfileSnapshot{}, errors.New(
			"Run execution profile transition time cannot move backwards")
	}
	return next, nil
}

// IsDefaultPreview identifies the initial compatibility snapshot, before an
// operator has selected an execution environment. An explicit Preview choice
// receives revision 2 even when the initial profile already says Preview.
func (s RunExecutionProfileSnapshot) IsDefaultPreview() bool {
	return s.Profile == RunExecutionProfilePreview && s.Revision == 1
}

func CanChangeRunExecutionProfile(status RunStatus) bool {
	return status == RunCreated || status == RunPaused
}

type RunExecutionProfileOperation struct {
	KeyDigest          string
	RequestFingerprint string
	SnapshotID         string
	RunID              string
	RequestedBy        string
	CreatedAt          time.Time
}

func (o RunExecutionProfileOperation) Validate() error {
	if !validLowerHexDigest(o.KeyDigest) || !validLowerHexDigest(o.RequestFingerprint) {
		return errors.New("Run execution profile operation digests must be lowercase SHA-256")
	}
	for label, value := range map[string]string{
		"snapshot id": o.SnapshotID, "Run id": o.RunID, "requester": o.RequestedBy,
	} {
		if !ValidAgentID(value) || strings.ContainsRune(value, 0) {
			return fmt.Errorf("Run execution profile operation %s must be normalized and bounded UTF-8", label)
		}
	}
	if o.CreatedAt.IsZero() {
		return errors.New("Run execution profile operation creation time is required")
	}
	return nil
}
