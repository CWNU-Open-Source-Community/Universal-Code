// Package standardcode defines the backend-neutral Standard Code command,
// readiness, checkpoint, and Artifact result contracts. Backend selection is
// product state and deliberately is not part of the Supervisor command schema.
package standardcode

import (
	"errors"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runworktree"
	"cyberagent-workbench/internal/sandbox"
)

const (
	CommandProtocolVersion   = "standard-code-command.v1"
	ReadinessProtocolVersion = "standard-code-backend-readiness.v1"
	ResultProtocolVersion    = "standard-code-command-result.v1"

	BackendDocker = "docker"
	BackendLocal  = "local"

	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
	StatusTimedOut  = "timed_out"

	NetworkDisabled = "disabled"
	CredentialsNone = "none"

	ReadinessReady       = "ready"
	ReadinessDisabled    = "disabled"
	ReadinessUnavailable = "unavailable"

	MaxPurposeRunes = 1_200
	MaxArtifacts    = sandbox.MaxDockerOutputFiles + 2
)

// Command is the schema visible above a sandbox backend. It has no backend,
// host path, image, endpoint, mount, environment, credential, or Docker field.
type Command struct {
	ProtocolVersion  string   `json:"protocol_version"`
	Toolchain        string   `json:"toolchain"`
	Arguments        []string `json:"arguments"`
	WorkingDirectory string   `json:"working_directory"`
	TimeoutSeconds   int      `json:"timeout_seconds"`
	Purpose          string   `json:"purpose"`
}

func (command Command) Validate() error {
	if command.ProtocolVersion != CommandProtocolVersion ||
		!validToolchain(command.Toolchain) ||
		!validRelativeDirectory(command.WorkingDirectory) ||
		command.TimeoutSeconds < 1 || command.TimeoutSeconds > sandbox.MaxTimeoutSeconds ||
		len(command.Arguments) > sandbox.MaxCommandArguments ||
		command.Purpose == "" || command.Purpose != strings.TrimSpace(command.Purpose) ||
		!utf8.ValidString(command.Purpose) || strings.ContainsRune(command.Purpose, 0) ||
		utf8.RuneCountInString(command.Purpose) > MaxPurposeRunes {
		return errors.New("Standard Code command is invalid")
	}
	total := 0
	for _, argument := range command.Arguments {
		if !utf8.ValidString(argument) || strings.ContainsRune(argument, 0) ||
			len([]byte(argument)) > sandbox.MaxCommandArgumentBytes {
			return errors.New("Standard Code command argument is invalid")
		}
		for _, current := range argument {
			if unicode.IsControl(current) {
				return errors.New("Standard Code command argument contains a control character")
			}
		}
		total += len(argument)
	}
	if total > sandbox.MaxCommandBytes {
		return errors.New("Standard Code command exceeds its byte bound")
	}
	return nil
}

// Fingerprint binds the complete backend-neutral command, including its
// operator-visible purpose, without exposing that text to a backend runner.
func (command Command) Fingerprint() (string, error) {
	if err := command.Validate(); err != nil {
		return "", err
	}
	parts := []string{CommandProtocolVersion, command.Toolchain,
		command.WorkingDirectory, strconv.Itoa(command.TimeoutSeconds),
		command.Purpose, strconv.Itoa(len(command.Arguments))}
	parts = append(parts, command.Arguments...)
	return runmutation.Fingerprint(parts...), nil
}

func validToolchain(value string) bool {
	switch value {
	case sandbox.DockerStandardCodeToolchainGo,
		sandbox.DockerStandardCodeToolchainGofmt,
		sandbox.DockerStandardCodeToolchainNode,
		sandbox.DockerStandardCodeToolchainPython,
		sandbox.DockerStandardCodeToolchainRust:
		return true
	default:
		return false
	}
}

func validRelativeDirectory(value string) bool {
	if value == "." {
		return true
	}
	return value != "" && value == strings.TrimSpace(value) && utf8.ValidString(value) &&
		!strings.ContainsRune(value, 0) && !strings.Contains(value, `\`) &&
		!strings.HasPrefix(value, "/") && value != ".." &&
		!strings.HasPrefix(value, "../") && path.Clean(value) == value
}

// ExecutionContext is supplied by the Go control plane after loading current
// Run, Drydock, permission, profile, and process-local capability facts.
type ExecutionContext struct {
	RunID                string
	MissionID            string
	SessionID            string
	WorkspaceID          string
	DrydockID            string
	DrydockWorkspaceID   string
	DrydockGeneration    int64
	CheckpointID         string
	DrydockBindingSHA256 string
	ProfileSnapshotID    string
	ProfileRevision      int64
	PermissionSnapshotID string
	PermissionRevision   int64
	CapabilityGeneration string
}

func (scope ExecutionContext) Validate() error {
	for _, value := range []string{scope.RunID, scope.MissionID, scope.SessionID,
		scope.WorkspaceID, scope.DrydockID, scope.DrydockWorkspaceID,
		scope.CheckpointID, scope.ProfileSnapshotID, scope.PermissionSnapshotID} {
		if !domain.ValidAgentID(value) || strings.ContainsRune(value, 0) {
			return errors.New("Standard Code execution context identity is invalid")
		}
	}
	if scope.DrydockGeneration < 1 || scope.ProfileRevision < 1 ||
		scope.PermissionRevision < 1 || !runworktree.ValidDigest(scope.DrydockBindingSHA256) ||
		!runworktree.ValidDigest(scope.CapabilityGeneration) {
		return errors.New("Standard Code execution context revision is invalid")
	}
	return nil
}

func CompileDockerManifest(scope ExecutionContext, command Command) (sandbox.Manifest, error) {
	return CompileDockerManifestWithStdin(scope, command,
		sandbox.DockerStandardCodeStdinClosed)
}

// CompileDockerManifestWithStdin is reserved for the Run-owned Command Runtime
// adapter. The caller supplies only the backend-neutral closed/pipe policy;
// Docker attachment flags and the input transport remain Go-owned.
func CompileDockerManifestWithStdin(scope ExecutionContext, command Command,
	stdinPolicy string,
) (sandbox.Manifest, error) {
	if err := scope.Validate(); err != nil {
		return sandbox.Manifest{}, err
	}
	if err := command.Validate(); err != nil {
		return sandbox.Manifest{}, err
	}
	commandSHA256, err := command.Fingerprint()
	if err != nil {
		return sandbox.Manifest{}, err
	}
	return sandbox.DockerStandardCodeManifest(sandbox.DockerStandardCodeRunnerBinding{
		RunID: scope.RunID, MissionID: scope.MissionID, SessionID: scope.SessionID,
		WorkspaceID: scope.WorkspaceID, DrydockID: scope.DrydockID,
		DrydockWorkspaceID: scope.DrydockWorkspaceID,
		DrydockGeneration:  scope.DrydockGeneration, CheckpointID: scope.CheckpointID,
		DrydockBindingSHA256: scope.DrydockBindingSHA256,
		ProfileSnapshotID:    scope.ProfileSnapshotID, ProfileRevision: scope.ProfileRevision,
		PermissionSnapshotID: scope.PermissionSnapshotID,
		PermissionRevision:   scope.PermissionRevision,
		CapabilityGeneration: scope.CapabilityGeneration,
		CommandSHA256:        commandSHA256,
		StdinPolicy:          stdinPolicy,
		Toolchain:            command.Toolchain, WorkingDirectory: command.WorkingDirectory,
		Arguments:      append([]string(nil), command.Arguments...),
		TimeoutSeconds: command.TimeoutSeconds,
	})
}

type BackendReadiness struct {
	ProtocolVersion string    `json:"protocol_version"`
	Backend         string    `json:"backend"`
	Status          string    `json:"status"`
	ReasonCode      string    `json:"reason_code"`
	RemediationCode string    `json:"remediation_code"`
	BlockedBy       []string  `json:"blocked_by"`
	Remediation     []string  `json:"remediation"`
	CheckedAt       time.Time `json:"checked_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	CapabilityGrant bool      `json:"capability_grant"`
	EvidenceSHA256  string    `json:"evidence_sha256"`
}

func DockerReadiness(value sandbox.DockerReadiness) (BackendReadiness, error) {
	if err := value.Validate(); err != nil {
		return BackendReadiness{}, err
	}
	result := BackendReadiness{
		ProtocolVersion: ReadinessProtocolVersion, Backend: BackendDocker,
		Status: value.Status, ReasonCode: value.ReasonCode,
		RemediationCode: value.RemediationCode, CheckedAt: value.CheckedAt,
		ExpiresAt: value.ExpiresAt, EvidenceSHA256: value.ReadinessFingerprint,
	}
	switch value.ReasonCode {
	case sandbox.DockerReadinessReasonNone:
		result.BlockedBy, result.Remediation = []string{}, []string{}
	case sandbox.DockerReadinessReasonFeatureDisabled:
		result.BlockedBy = []string{"startup_gate_closed"}
		result.Remediation = []string{"restart_with_startup_gate"}
	case sandbox.DockerReadinessReasonDaemonUnreachable:
		result.BlockedBy = []string{"docker_unavailable"}
		result.Remediation = []string{"install_or_start_docker"}
	default:
		result.BlockedBy = []string{"backend_not_ready"}
		result.Remediation = []string{"retry_backend_readiness"}
	}
	return result, result.Validate()
}

func (value BackendReadiness) Validate() error {
	if value.ProtocolVersion != ReadinessProtocolVersion || value.Backend != BackendDocker ||
		(value.Status != ReadinessReady && value.Status != ReadinessDisabled &&
			value.Status != ReadinessUnavailable) || value.ReasonCode == "" ||
		value.RemediationCode == "" || value.CheckedAt.IsZero() ||
		!value.ExpiresAt.After(value.CheckedAt) || value.CapabilityGrant ||
		!runworktree.ValidDigest(value.EvidenceSHA256) {
		return errors.New("Standard Code backend readiness is invalid")
	}
	expectedBlocker, expectedRemediation := "", ""
	switch value.ReasonCode {
	case sandbox.DockerReadinessReasonNone:
	case sandbox.DockerReadinessReasonFeatureDisabled:
		expectedBlocker, expectedRemediation = "startup_gate_closed", "restart_with_startup_gate"
	case sandbox.DockerReadinessReasonDaemonUnreachable:
		expectedBlocker, expectedRemediation = "docker_unavailable", "install_or_start_docker"
	default:
		expectedBlocker, expectedRemediation = "backend_not_ready", "retry_backend_readiness"
	}
	if expectedBlocker == "" {
		if len(value.BlockedBy) != 0 || len(value.Remediation) != 0 {
			return errors.New("Standard Code ready backend has blockers")
		}
	} else if len(value.BlockedBy) != 1 || value.BlockedBy[0] != expectedBlocker ||
		len(value.Remediation) != 1 || value.Remediation[0] != expectedRemediation {
		return errors.New("Standard Code backend blocker projection is invalid")
	}
	return nil
}

type CheckpointResult struct {
	DrydockID        string `json:"drydock_id"`
	GenerationBefore int64  `json:"generation_before"`
	GenerationAfter  int64  `json:"generation_after"`
	BeforeID         string `json:"before_id"`
	AfterID          string `json:"after_id"`
	ReceiptID        string `json:"receipt_id"`
}

func (value CheckpointResult) Validate() error {
	for _, identity := range []string{value.DrydockID, value.BeforeID,
		value.AfterID, value.ReceiptID} {
		if !domain.ValidAgentID(identity) {
			return errors.New("Standard Code checkpoint identity is invalid")
		}
	}
	if value.GenerationBefore < 1 || value.GenerationAfter != value.GenerationBefore+1 {
		return errors.New("Standard Code checkpoint generation is invalid")
	}
	return nil
}

type ArtifactResult struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
	FileCount int    `json:"file_count"`
	Redacted  bool   `json:"redacted"`
}

func (value ArtifactResult) Validate() error {
	if !domain.ValidAgentID(value.ID) || (value.Kind != "logs" && value.Kind != "files") ||
		!runworktree.ValidDigest(value.SHA256) || value.SizeBytes < 0 ||
		value.SizeBytes > sandbox.MaxDockerOutputTotalBytes || value.FileCount < 0 ||
		value.FileCount > sandbox.MaxDockerOutputFiles {
		return errors.New("Standard Code Artifact result is invalid")
	}
	return nil
}

type Result struct {
	ProtocolVersion string           `json:"protocol_version"`
	Backend         string           `json:"backend"`
	ExecutionID     string           `json:"execution_id"`
	RunID           string           `json:"run_id"`
	DrydockID       string           `json:"drydock_id"`
	Status          string           `json:"status"`
	ExitCode        *int             `json:"exit_code,omitempty"`
	Network         string           `json:"network"`
	Credentials     string           `json:"credentials"`
	StartedAt       time.Time        `json:"started_at"`
	CompletedAt     time.Time        `json:"completed_at"`
	Checkpoint      CheckpointResult `json:"checkpoint"`
	Artifacts       []ArtifactResult `json:"artifacts"`
	Replayed        bool             `json:"replayed"`
}

func (result Result) Validate() error {
	if result.ProtocolVersion != ResultProtocolVersion ||
		(result.Backend != BackendDocker && result.Backend != BackendLocal) ||
		!domain.ValidAgentID(result.ExecutionID) || !domain.ValidAgentID(result.RunID) ||
		!domain.ValidAgentID(result.DrydockID) || !validStatus(result.Status) ||
		(result.ExitCode != nil && (*result.ExitCode < 0 || *result.ExitCode > 255)) ||
		result.Network != NetworkDisabled ||
		result.Credentials != CredentialsNone || result.StartedAt.IsZero() ||
		result.CompletedAt.Before(result.StartedAt) || result.Checkpoint.Validate() != nil ||
		len(result.Artifacts) > MaxArtifacts {
		return errors.New("Standard Code command result is invalid")
	}
	if result.Status == StatusSucceeded &&
		(result.ExitCode == nil || *result.ExitCode != 0) {
		return errors.New("Standard Code successful result requires exit code zero")
	}
	for _, artifact := range result.Artifacts {
		if artifact.Validate() != nil {
			return errors.New("Standard Code command Artifact result is invalid")
		}
	}
	return nil
}

func validStatus(value string) bool {
	switch value {
	case StatusSucceeded, StatusFailed, StatusCancelled, StatusTimedOut:
		return true
	default:
		return false
	}
}
