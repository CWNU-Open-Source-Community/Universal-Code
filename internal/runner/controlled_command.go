package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/domain"
)

const (
	ControlledCommandPlanProtocolVersion = "controlled_command_plan.v1"
	ControlledCommandPolicyVersion       = "controlled_command_policy.v1"
	DefaultControlledCommandTimeout      = 30 * time.Second
	MaxControlledCommandTimeout          = 2 * time.Minute
	MaxControlledRelativePathRunes       = 512
	// Load the known system manifests directly before cmdlet discovery can
	// analyze available binary modules in Windows PowerShell 5.
	controlledPowerShellWorkspaceListScript = `& { param([string]$RelativePathHex) if ($RelativePathHex -notmatch '\Ah[0-9a-f]+\z' -or (($RelativePathHex.Length - 1) % 2) -ne 0) { throw 'invalid relative path' }; $Hex = $RelativePathHex.Substring(1); $Bytes = [byte[]]@(for ($Index = 0; $Index -lt $Hex.Length; $Index += 2) { [Convert]::ToByte($Hex.Substring($Index, 2), 16) }); $RelativePath = [Text.Encoding]::UTF8.GetString($Bytes); Microsoft.PowerShell.Core\Import-Module -Name ($PSHOME + '\Modules\Microsoft.PowerShell.Management\Microsoft.PowerShell.Management.psd1'),($PSHOME + '\Modules\Microsoft.PowerShell.Utility\Microsoft.PowerShell.Utility.psd1') -ErrorAction Stop; Get-ChildItem -LiteralPath $RelativePath -Force | Select-Object Name,Length,Attributes | ConvertTo-Json -Compress }`
)

var ErrControlledCommandBoundary = errors.New(
	"controlled command plan boundary is invalid")

type ControlledCommandKind string

const (
	ControlledCommandGitStatus               ControlledCommandKind = "git-status"
	ControlledCommandGitDiffCheck            ControlledCommandKind = "git-diff-check"
	ControlledCommandGoVersion               ControlledCommandKind = "go-version"
	ControlledCommandPowerShellWorkspaceList ControlledCommandKind = "powershell-workspace-list"
)

func ParseControlledCommandKind(value string) (ControlledCommandKind, error) {
	kind := ControlledCommandKind(strings.ToLower(strings.TrimSpace(value)))
	switch kind {
	case ControlledCommandGitStatus, ControlledCommandGitDiffCheck,
		ControlledCommandGoVersion, ControlledCommandPowerShellWorkspaceList:
		return kind, nil
	default:
		return "", fmt.Errorf("%w: unsupported command kind %q",
			ErrControlledCommandBoundary, value)
	}
}

type ControlledCommandPlanRequest struct {
	ID             string
	WorkspaceID    string
	WorkspaceRoot  string
	Interaction    domain.RunExecutionInteractionSnapshot
	CurrentProfile domain.RunExecutionProfileSnapshot
	CurrentSurface domain.ExecutionSurface
	Kind           ControlledCommandKind
	RelativePath   string
	Timeout        time.Duration
}

type ControlledCommandPlan struct {
	ID                       string
	ProtocolVersion          string
	PolicyVersion            string
	RunID                    string
	WorkspaceID              string
	WorkspaceRootSHA256      string
	InteractionSnapshotID    string
	InteractionRevision      int64
	ExecutionProfileRevision int64
	Kind                     ControlledCommandKind
	ExecutableID             string
	Argv                     []string
	RelativePath             string
	TimeoutMilliseconds      int64
	WorkingDirectoryBound    bool
	StdinClosed              bool
	EnvironmentInherited     bool
	ProfileLoadingEnabled    bool
	PersistentProcess        bool
	CallerShellTextAccepted  bool
	GoOwnedPowerShellScript  bool
	NetworkRequested         bool
	OSSandboxRequired        bool
	StartBlocked             bool
	ProductExecutionEnabled  bool
	Fingerprint              string
}

func PlanControlledCommand(request ControlledCommandPlanRequest) (
	ControlledCommandPlan, error,
) {
	if err := validateControlledCommandPlanRequest(request); err != nil {
		return ControlledCommandPlan{}, err
	}
	timeout := request.Timeout
	if timeout == 0 {
		timeout = DefaultControlledCommandTimeout
	}
	root := filepath.Clean(request.WorkspaceRoot)
	rootDigest := sha256.Sum256([]byte(root))
	plan := ControlledCommandPlan{
		ID: request.ID, ProtocolVersion: ControlledCommandPlanProtocolVersion,
		PolicyVersion: ControlledCommandPolicyVersion,
		RunID:         request.Interaction.RunID, WorkspaceID: request.WorkspaceID,
		WorkspaceRootSHA256:      hex.EncodeToString(rootDigest[:]),
		InteractionSnapshotID:    request.Interaction.ID,
		InteractionRevision:      request.Interaction.Revision,
		ExecutionProfileRevision: request.CurrentProfile.Revision,
		Kind:                     request.Kind, TimeoutMilliseconds: timeout.Milliseconds(),
		WorkingDirectoryBound: true, StdinClosed: true,
		EnvironmentInherited: false, ProfileLoadingEnabled: false,
		PersistentProcess: false, CallerShellTextAccepted: false,
		NetworkRequested: false, OSSandboxRequired: true,
		StartBlocked: true, ProductExecutionEnabled: false,
	}
	switch request.Kind {
	case ControlledCommandGitStatus:
		plan.ExecutableID = "git"
		plan.Argv = []string{"status", "--short", "--branch", "--untracked-files=no"}
	case ControlledCommandGitDiffCheck:
		plan.ExecutableID = "git"
		plan.Argv = []string{"diff", "--check", "--no-ext-diff", "--no-textconv"}
	case ControlledCommandGoVersion:
		plan.ExecutableID = "go"
		plan.Argv = []string{"version"}
	case ControlledCommandPowerShellWorkspaceList:
		plan.ExecutableID = "windows-powershell"
		plan.RelativePath = normalizeControlledRelativePath(request.RelativePath)
		plan.GoOwnedPowerShellScript = true
		plan.Argv = []string{
			"-NoLogo", "-NoProfile", "-NonInteractive",
			"-ExecutionPolicy", "Restricted", "-Command",
			controlledPowerShellWorkspaceListScript,
			encodeControlledRelativePath(plan.RelativePath),
		}
	default:
		return ControlledCommandPlan{}, ErrControlledCommandBoundary
	}
	plan.Fingerprint = controlledCommandPlanFingerprint(plan)
	if err := plan.Validate(); err != nil {
		return ControlledCommandPlan{}, err
	}
	return plan, nil
}

func validateControlledCommandPlanRequest(request ControlledCommandPlanRequest) error {
	if !validIdentity(request.ID) ||
		!domain.ValidAgentID(request.WorkspaceID) ||
		strings.ContainsRune(request.WorkspaceID, 0) {
		return ErrControlledCommandBoundary
	}
	if err := request.Interaction.Validate(); err != nil ||
		request.Interaction.Mode != domain.RunExecutionInteractionControlled ||
		request.Interaction.Surface != domain.ExecutionSurfaceCode ||
		request.Interaction.ExecutionProfile != domain.RunExecutionProfileLocal ||
		request.Interaction.WorkspaceTrust != domain.WorkspaceTrustTrusted ||
		request.Interaction.CommandForm != domain.ExecutionCommandStructuredArgv ||
		request.Interaction.AgentInputDefault ||
		request.Interaction.ProcessEnabled ||
		request.Interaction.ExecutionAuthorized ||
		request.Interaction.CapabilityGrant {
		return fmt.Errorf("%w: a current closed controlled interaction is required",
			ErrControlledCommandBoundary)
	}
	if err := request.CurrentProfile.Validate(); err != nil ||
		request.CurrentProfile.RunID != request.Interaction.RunID ||
		request.CurrentProfile.MissionID != request.Interaction.MissionID ||
		request.CurrentProfile.Profile != domain.RunExecutionProfileLocal ||
		request.CurrentProfile.Revision !=
			request.Interaction.ExecutionProfileRevision ||
		request.CurrentSurface != domain.ExecutionSurfaceCode {
		return fmt.Errorf("%w: execution profile or surface binding is stale",
			ErrControlledCommandBoundary)
	}
	if request.Kind == "" {
		return ErrControlledCommandBoundary
	}
	if _, err := ParseControlledCommandKind(string(request.Kind)); err != nil {
		return err
	}
	if !filepath.IsAbs(request.WorkspaceRoot) ||
		filepath.Clean(request.WorkspaceRoot) != request.WorkspaceRoot ||
		strings.ContainsRune(request.WorkspaceRoot, 0) {
		return fmt.Errorf("%w: Workspace root must be an exact absolute path",
			ErrControlledCommandBoundary)
	}
	if request.Timeout < 0 || request.Timeout > MaxControlledCommandTimeout {
		return fmt.Errorf("%w: timeout exceeds %s", ErrControlledCommandBoundary,
			MaxControlledCommandTimeout)
	}
	if request.Kind == ControlledCommandPowerShellWorkspaceList {
		if err := validateControlledRelativePath(request.RelativePath); err != nil {
			return err
		}
	} else if strings.TrimSpace(request.RelativePath) != "" {
		return fmt.Errorf("%w: relative path is not accepted by this command kind",
			ErrControlledCommandBoundary)
	}
	return nil
}

func (p ControlledCommandPlan) Validate() error {
	if !validIdentity(p.ID) || p.ProtocolVersion !=
		ControlledCommandPlanProtocolVersion ||
		p.PolicyVersion != ControlledCommandPolicyVersion ||
		!domain.ValidAgentID(p.RunID) || !domain.ValidAgentID(p.WorkspaceID) ||
		!validIdentity(p.InteractionSnapshotID) ||
		p.InteractionRevision <= 0 || p.ExecutionProfileRevision <= 0 ||
		!validSHA256(p.WorkspaceRootSHA256) ||
		p.TimeoutMilliseconds < time.Millisecond.Milliseconds() ||
		p.TimeoutMilliseconds > MaxControlledCommandTimeout.Milliseconds() ||
		!p.WorkingDirectoryBound || !p.StdinClosed || p.EnvironmentInherited ||
		p.ProfileLoadingEnabled || p.PersistentProcess ||
		p.CallerShellTextAccepted || p.NetworkRequested || !p.OSSandboxRequired ||
		!p.StartBlocked || p.ProductExecutionEnabled ||
		!validSHA256(p.Fingerprint) {
		return ErrControlledCommandBoundary
	}
	if _, err := ParseControlledCommandKind(string(p.Kind)); err != nil {
		return err
	}
	switch p.Kind {
	case ControlledCommandGitStatus:
		if p.ExecutableID != "git" ||
			!equalStrings(p.Argv,
				[]string{"status", "--short", "--branch", "--untracked-files=no"}) ||
			p.RelativePath != "" || p.GoOwnedPowerShellScript {
			return ErrControlledCommandBoundary
		}
	case ControlledCommandGitDiffCheck:
		if p.ExecutableID != "git" ||
			!equalStrings(p.Argv,
				[]string{"diff", "--check", "--no-ext-diff", "--no-textconv"}) ||
			p.RelativePath != "" || p.GoOwnedPowerShellScript {
			return ErrControlledCommandBoundary
		}
	case ControlledCommandGoVersion:
		if p.ExecutableID != "go" ||
			!equalStrings(p.Argv, []string{"version"}) ||
			p.RelativePath != "" || p.GoOwnedPowerShellScript {
			return ErrControlledCommandBoundary
		}
	case ControlledCommandPowerShellWorkspaceList:
		if p.ExecutableID != "windows-powershell" ||
			!p.GoOwnedPowerShellScript || len(p.Argv) != 8 ||
			p.Argv[0] != "-NoLogo" || p.Argv[1] != "-NoProfile" ||
			p.Argv[2] != "-NonInteractive" ||
			p.Argv[3] != "-ExecutionPolicy" || p.Argv[4] != "Restricted" ||
			p.Argv[5] != "-Command" ||
			p.Argv[6] != controlledPowerShellWorkspaceListScript ||
			p.Argv[7] != encodeControlledRelativePath(p.RelativePath) ||
			decodeControlledRelativePath(p.Argv[7]) != p.RelativePath ||
			validateControlledRelativePath(p.RelativePath) != nil {
			return ErrControlledCommandBoundary
		}
	default:
		return ErrControlledCommandBoundary
	}
	if controlledCommandPlanFingerprint(p) != p.Fingerprint {
		return fmt.Errorf("%w: fingerprint mismatch", ErrControlledCommandBoundary)
	}
	return nil
}

func validateControlledRelativePath(value string) error {
	if value == "" {
		value = "."
	}
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value ||
		strings.ContainsRune(value, 0) ||
		len([]rune(value)) > MaxControlledRelativePathRunes ||
		filepath.IsAbs(value) {
		return fmt.Errorf("%w: relative path is invalid",
			ErrControlledCommandBoundary)
	}
	clean := filepath.Clean(value)
	if clean != value || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) ||
		strings.ContainsAny(clean, "*?[]$`;&|><\r\n") {
		return fmt.Errorf("%w: relative path escapes or contains command syntax",
			ErrControlledCommandBoundary)
	}
	return nil
}

func normalizeControlledRelativePath(value string) string {
	if value == "" {
		return "."
	}
	return filepath.Clean(value)
}

func encodeControlledRelativePath(value string) string {
	return "h" + hex.EncodeToString([]byte(value))
}

func decodeControlledRelativePath(value string) string {
	const maxEncodedBytes = 1 + MaxControlledRelativePathRunes*utf8.UTFMax*2
	if len(value) < 3 || len(value) > maxEncodedBytes || value[0] != 'h' ||
		(len(value)-1)%2 != 0 {
		return ""
	}
	decoded, err := hex.DecodeString(value[1:])
	if err != nil || !utf8.Valid(decoded) {
		return ""
	}
	path := string(decoded)
	if encodeControlledRelativePath(path) != value ||
		validateControlledRelativePath(path) != nil {
		return ""
	}
	return path
}

func controlledCommandPlanFingerprint(plan ControlledCommandPlan) string {
	plan.Fingerprint = ""
	encoded, err := json.Marshal(plan)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func equalStrings(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
