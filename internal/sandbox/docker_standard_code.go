package sandbox

import (
	"errors"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	DockerStandardCodeRunnerProtocolVersion = "standard-code-docker-runner.v2"
	dockerStandardCodeRunnerLegacyProtocol  = "standard-code-docker-runner.v1"
	DockerStandardCodeRunnerExecutable      = "/traverse-board/standard-code-runner"
	DockerStandardCodeProjectActionsLabel   = "org.traverse-board.project-actions"
	DockerStandardCodePreparationLabel      = "org.traverse-board.project-preparation"
	DockerStandardCodeWorkspaceTarget       = "/workspace"
	DockerStandardCodeGitMetadataTarget     = "/workspace/.git"
	DockerStandardCodeGitMetadataMask       = "gitdir: .traverse-board-git-disabled\n"
	DockerStandardCodeCacheTarget           = "/traverse-board/cache"
	DockerStandardCodeCacheTmpfsOptions     = "rw,exec,nosuid,nodev,size=134217728,nr_inodes=16384,mode=0700,uid=65532,gid=65532"
	DockerStandardCodeGoCacheTmpfsOptions   = "rw,exec,nosuid,nodev,size=536870912,nr_inodes=16384,mode=0700,uid=65532,gid=65532"

	DockerStandardCodeCPUQuotaMillis               = 2_000
	DockerStandardCodeMemoryBytes            int64 = 2 * 1024 * 1024 * 1024
	DockerStandardCodePIDs                         = 128
	DockerStandardCodeMaxOutputBytes         int64 = MaxDockerOutputTotalBytes
	DockerStandardCodeCancellationMS               = 2_000
	DockerStandardCodeWorkspaceGrowthBytes   int64 = 16 * 1024 * 1024
	DockerStandardCodeWorkspaceGrowthEntries       = 4_096
	DockerStandardCodeWorkspaceFileBytes     int64 = 16 * 1024 * 1024
	DockerStandardCodeWorkspaceFreeBytes     int64 = 2 * 1024 * 1024 * 1024
	DockerStandardCodeWorkspaceFreeEntries   int64 = 1_000_000
	DockerStandardCodeCacheBytes             int64 = 128 * 1024 * 1024
	DockerStandardCodeGoCacheBytes           int64 = 512 * 1024 * 1024
	DockerStandardCodeCacheEntries                 = 16_384

	DockerStandardCodeToolchainGo          = "go"
	DockerStandardCodeToolchainGoPrepare   = "go-prepare"
	DockerStandardCodeToolchainNodePrepare = "node-prepare"
	DockerStandardCodeToolchainGofmt       = "gofmt"
	DockerStandardCodeToolchainNode        = "node"
	DockerStandardCodeToolchainPython      = "python"
	DockerStandardCodeToolchainRust        = "rust"
	DockerStandardCodeStdinClosed          = "closed"
	DockerStandardCodeStdinPipe            = "pipe"

	dockerStandardCodeBindingFieldCount = 24
	dockerStandardCodeLegacyFieldCount  = 23
)

// DockerStandardCodeRunnerBinding is the complete authority and command wire
// passed to the fixed image runner. It contains no host path, environment,
// credential, network, image, daemon endpoint, mount, or Docker flag.
type DockerStandardCodeRunnerBinding struct {
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
	CommandSHA256        string
	StdinPolicy          string
	Toolchain            string
	WorkingDirectory     string
	Arguments            []string
	TimeoutSeconds       int
}

func (binding DockerStandardCodeRunnerBinding) Validate() error {
	for _, value := range []string{binding.RunID, binding.MissionID, binding.SessionID,
		binding.WorkspaceID, binding.DrydockID, binding.DrydockWorkspaceID,
		binding.CheckpointID, binding.ProfileSnapshotID, binding.PermissionSnapshotID} {
		if validateStoredIdentity("Standard Code Docker binding identity", value) != nil {
			return errors.New("Standard Code Docker binding identity is invalid")
		}
	}
	if binding.DrydockGeneration < 1 || binding.ProfileRevision < 1 ||
		binding.PermissionRevision < 1 || !validDigest(binding.DrydockBindingSHA256) ||
		!validDigest(binding.CapabilityGeneration) || !validDigest(binding.CommandSHA256) ||
		!validDockerStandardCodeStdinPolicy(binding.StdinPolicy) ||
		!validDockerStandardCodeToolchain(binding.Toolchain) ||
		!validDockerStandardCodeRelativePath(binding.WorkingDirectory) ||
		binding.TimeoutSeconds < 1 || binding.TimeoutSeconds > MaxTimeoutSeconds ||
		len(binding.Arguments) > MaxCommandArguments {
		return errors.New("Standard Code Docker binding is invalid")
	}
	total := 0
	for _, argument := range binding.Arguments {
		if !utf8.ValidString(argument) || strings.ContainsRune(argument, 0) ||
			len([]byte(argument)) > MaxCommandArgumentBytes {
			return errors.New("Standard Code Docker command argument is invalid")
		}
		for _, current := range argument {
			if unicode.IsControl(current) {
				return errors.New("Standard Code Docker command argument contains a control character")
			}
		}
		total += len(argument)
	}
	if total > MaxCommandBytes {
		return errors.New("Standard Code Docker command exceeds its fixed byte bound")
	}
	return nil
}

func validDockerStandardCodeStdinPolicy(value string) bool {
	return value == DockerStandardCodeStdinClosed || value == DockerStandardCodeStdinPipe
}

func validDockerStandardCodeToolchain(value string) bool {
	switch value {
	case DockerStandardCodeToolchainGo, DockerStandardCodeToolchainGofmt, DockerStandardCodeToolchainNode,
		DockerStandardCodeToolchainPython, DockerStandardCodeToolchainRust, DockerStandardCodeToolchainGoPrepare, DockerStandardCodeToolchainNodePrepare:
		return true
	default:
		return false
	}
}

func validDockerStandardCodeRelativePath(value string) bool {
	if value == "." {
		return true
	}
	return value != "" && value == strings.TrimSpace(value) && utf8.ValidString(value) &&
		!strings.ContainsRune(value, 0) && !strings.Contains(value, `\`) &&
		!strings.HasPrefix(value, "/") && path.Clean(value) == value && value != ".." &&
		!strings.HasPrefix(value, "../")
}

// DockerStandardCodeManifest compiles the backend-neutral command binding to
// the one supported Standard Code Docker shape. The exact Drydock root is the
// sole writable host projection; the transport adds only its fixed read-only
// Git metadata mask. Docker configuration remains entirely Go-owned.
func DockerStandardCodeManifest(binding DockerStandardCodeRunnerBinding) (Manifest, error) {
	return dockerStandardCodeManifest(binding, DockerStandardCodeRunnerProtocolVersion)
}

func dockerStandardCodeManifest(binding DockerStandardCodeRunnerBinding,
	protocol string,
) (Manifest, error) {
	if err := binding.Validate(); err != nil {
		return Manifest{}, err
	}
	if protocol != DockerStandardCodeRunnerProtocolVersion &&
		(protocol != dockerStandardCodeRunnerLegacyProtocol ||
			binding.StdinPolicy != DockerStandardCodeStdinClosed) {
		return Manifest{}, errors.New("Standard Code Docker runner protocol is invalid")
	}
	arguments := []string{
		protocol,
		binding.RunID,
		binding.MissionID,
		binding.SessionID,
		binding.WorkspaceID,
		binding.DrydockID,
		binding.DrydockWorkspaceID,
		strconv.FormatInt(binding.DrydockGeneration, 10),
		binding.CheckpointID,
		binding.DrydockBindingSHA256,
		binding.ProfileSnapshotID,
		strconv.FormatInt(binding.ProfileRevision, 10),
		binding.PermissionSnapshotID,
		strconv.FormatInt(binding.PermissionRevision, 10),
		binding.CapabilityGeneration,
		binding.CommandSHA256,
	}
	if protocol == DockerStandardCodeRunnerProtocolVersion {
		arguments = append(arguments, binding.StdinPolicy)
	}
	growthBytes, growthEntries, fileBytes := dockerStandardCodeWriteLimits(binding.Toolchain)
	arguments = append(arguments,
		binding.Toolchain,
		binding.WorkingDirectory,
		strconv.FormatInt(growthBytes, 10),
		strconv.Itoa(growthEntries),
		strconv.FormatInt(fileBytes, 10),
		strconv.FormatInt(DockerStandardCodeWorkspaceFreeBytes, 10),
		strconv.FormatInt(DockerStandardCodeWorkspaceFreeEntries, 10),
	)
	arguments = append(arguments, binding.Arguments...)
	manifest := Manifest{
		ProtocolVersion: ManifestProtocolVersion,
		Backend:         BackendDocker,
		Command: CommandSpec{
			Executable:       DockerStandardCodeRunnerExecutable,
			Arguments:        arguments,
			WorkingDirectory: DockerStandardCodeWorkspaceTarget,
		},
		Mounts: []Mount{{Source: ".", Target: DockerStandardCodeWorkspaceTarget,
			Access: MountReadWrite}},
		Network: NetworkScope{Mode: "disabled"},
		Resources: ResourceLimits{
			CPUQuotaMillis: DockerStandardCodeCPUQuotaMillis,
			MemoryBytes:    DockerStandardCodeMemoryBytes,
			PIDs:           DockerStandardCodePIDs,
			MaxOutputBytes: DockerStandardCodeMaxOutputBytes,
		},
		Output:         OutputSpec{CaptureStdout: true, CaptureStderr: true},
		TimeoutSeconds: binding.TimeoutSeconds,
		Cancellation: CancellationSpec{
			GracePeriodMillis: DockerStandardCodeCancellationMS,
		},
	}
	return NormalizeManifest(manifest)
}

// ParseDockerStandardCodeManifest recognizes only the exact fixed profile and
// returns the authority binding carried by it.
func ParseDockerStandardCodeManifest(manifest Manifest) (
	DockerStandardCodeRunnerBinding, bool,
) {
	normalized, err := NormalizeManifest(manifest)
	if err != nil || normalized.Backend != BackendDocker ||
		normalized.Command.Executable != DockerStandardCodeRunnerExecutable ||
		normalized.Command.WorkingDirectory != DockerStandardCodeWorkspaceTarget ||
		len(normalized.Command.Arguments) < dockerStandardCodeLegacyFieldCount ||
		len(normalized.Mounts) != 1 || normalized.Mounts[0] != (Mount{Source: ".",
		Target: DockerStandardCodeWorkspaceTarget, Access: MountReadWrite}) ||
		normalized.Network.Mode != "disabled" ||
		len(normalized.Network.AllowedTargets) != 0 || len(normalized.Environment) != 0 ||
		len(normalized.InputArtifactIDs) != 0 || !normalized.Output.CaptureStdout ||
		!normalized.Output.CaptureStderr || len(normalized.Output.Paths) != 0 ||
		normalized.Resources.CPUQuotaMillis != DockerStandardCodeCPUQuotaMillis ||
		normalized.Resources.MemoryBytes != DockerStandardCodeMemoryBytes ||
		normalized.Resources.PIDs != DockerStandardCodePIDs ||
		normalized.Resources.MaxOutputBytes != DockerStandardCodeMaxOutputBytes ||
		normalized.Cancellation.GracePeriodMillis != DockerStandardCodeCancellationMS {
		return DockerStandardCodeRunnerBinding{}, false
	}
	arguments := normalized.Command.Arguments
	protocol := arguments[0]
	fieldCount := dockerStandardCodeBindingFieldCount
	stdinIndex, toolchainIndex, workingDirectoryIndex, limitIndex := 16, 17, 18, 19
	stdinPolicy := DockerStandardCodeStdinClosed
	if protocol == dockerStandardCodeRunnerLegacyProtocol {
		fieldCount = dockerStandardCodeLegacyFieldCount
		stdinIndex, toolchainIndex, workingDirectoryIndex, limitIndex = -1, 16, 17, 18
	} else if protocol != DockerStandardCodeRunnerProtocolVersion {
		return DockerStandardCodeRunnerBinding{}, false
	}
	if len(arguments) < fieldCount {
		return DockerStandardCodeRunnerBinding{}, false
	}
	if stdinIndex >= 0 {
		stdinPolicy = arguments[stdinIndex]
	}
	generation, generationErr := strconv.ParseInt(arguments[7], 10, 64)
	profileRevision, profileErr := strconv.ParseInt(arguments[11], 10, 64)
	permissionRevision, permissionErr := strconv.ParseInt(arguments[13], 10, 64)
	workspaceGrowthBytes, growthBytesErr := strconv.ParseInt(arguments[limitIndex], 10, 64)
	workspaceGrowthEntries, growthEntriesErr := strconv.Atoi(arguments[limitIndex+1])
	workspaceFileBytes, fileBytesErr := strconv.ParseInt(arguments[limitIndex+2], 10, 64)
	workspaceFreeBytes, freeBytesErr := strconv.ParseInt(arguments[limitIndex+3], 10, 64)
	workspaceFreeEntries, freeEntriesErr := strconv.ParseInt(arguments[limitIndex+4], 10, 64)
	expectedGrowthBytes, expectedGrowthEntries, expectedFileBytes := dockerStandardCodeWriteLimits(arguments[toolchainIndex])
	if generationErr != nil || profileErr != nil || permissionErr != nil ||
		growthBytesErr != nil || growthEntriesErr != nil || fileBytesErr != nil ||
		freeBytesErr != nil || freeEntriesErr != nil || workspaceGrowthBytes !=
		expectedGrowthBytes || workspaceGrowthEntries !=
		expectedGrowthEntries || workspaceFileBytes !=
		expectedFileBytes || workspaceFreeBytes !=
		DockerStandardCodeWorkspaceFreeBytes || workspaceFreeEntries !=
		DockerStandardCodeWorkspaceFreeEntries {
		return DockerStandardCodeRunnerBinding{}, false
	}
	binding := DockerStandardCodeRunnerBinding{
		RunID: arguments[1], MissionID: arguments[2], SessionID: arguments[3],
		WorkspaceID: arguments[4], DrydockID: arguments[5],
		DrydockWorkspaceID: arguments[6], DrydockGeneration: generation,
		CheckpointID: arguments[8], DrydockBindingSHA256: arguments[9],
		ProfileSnapshotID: arguments[10], ProfileRevision: profileRevision,
		PermissionSnapshotID: arguments[12], PermissionRevision: permissionRevision,
		CapabilityGeneration: arguments[14], CommandSHA256: arguments[15],
		StdinPolicy: stdinPolicy, Toolchain: arguments[toolchainIndex],
		WorkingDirectory: arguments[workingDirectoryIndex],
		Arguments:        append([]string(nil), arguments[fieldCount:]...),
		TimeoutSeconds:   normalized.TimeoutSeconds,
	}
	if binding.Validate() != nil {
		return DockerStandardCodeRunnerBinding{}, false
	}
	recompiled, err := dockerStandardCodeManifest(binding, protocol)
	if err != nil {
		return DockerStandardCodeRunnerBinding{}, false
	}
	expected, expectedErr := recompiled.Fingerprint()
	actual, actualErr := normalized.Fingerprint()
	if expectedErr != nil || actualErr != nil || expected != actual {
		return DockerStandardCodeRunnerBinding{}, false
	}
	return binding, true
}

func dockerStandardCodeWriteLimits(toolchain string) (int64, int, int64) {
	if toolchain == DockerStandardCodeToolchainGoPrepare || toolchain == DockerStandardCodeToolchainNodePrepare {
		return 1024 * 1024 * 1024, 200000, 64 * 1024 * 1024
	}
	return DockerStandardCodeWorkspaceGrowthBytes, DockerStandardCodeWorkspaceGrowthEntries, DockerStandardCodeWorkspaceFileBytes
}
