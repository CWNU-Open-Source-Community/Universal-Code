package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DockerContainerRehearsalProtocolVersion = "sandbox_docker_container_rehearsal.v1"
	DockerContainerWriteProtocolVersion     = "sandbox_docker_write_transport.v1"
	DockerContainerRehearsalSourceLocal     = "local_unix_create_inspect_remove"
	DockerContainerRehearsalTrustClass      = "production_daemon_rehearsal_unverified"
	DockerContainerRehearsalStatusComplete  = "container_config_rehearsed_removed"
	DockerContainerWriteStatusComplete      = "create_inspect_remove_complete"
	DockerContainerWriteStepStateComplete   = "completed"
	DockerContainerWriteAPIVersion          = "1.40"
	MaxDockerContainerWriteSteps            = 5
	MaxDockerContainerRehearsalsPerPlan     = 1
)

var dockerContainerWriteStepNames = [...]string{
	"verify_image_profile",
	"reconcile_container_name",
	"create_container",
	"inspect_container",
	"remove_container",
}

type DockerHostMount struct {
	Source      string
	Target      string
	ReadOnly    bool
	Propagation string
}

func (mount DockerHostMount) Validate() error {
	if mount.Source == "" || !utf8.ValidString(mount.Source) || strings.ContainsRune(mount.Source, 0) ||
		!filepath.IsAbs(mount.Source) || filepath.Clean(mount.Source) != mount.Source ||
		validateVirtualPath("Docker host mount target", mount.Target) != nil ||
		mount.Propagation != DockerMountPropagationPrivate {
		return errors.New("docker host mount is invalid")
	}
	info, err := os.Lstat(mount.Source)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || (!info.Mode().IsRegular() && !info.IsDir()) {
		return errors.New("docker host mount source must be an existing non-symlink file or directory")
	}
	return nil
}

type DockerContainerWriteRequest struct {
	ProtocolVersion    string
	NativeGitSource    bool // Internal source-root execution; an empty directory masks metadata.
	Spec               DockerContainerSpec
	HostMounts         []DockerHostMount
	MountFingerprint   string
	RequestFingerprint string
}

func NewDockerContainerWriteRequest(ctx context.Context, workspaceRoot string,
	spec DockerContainerSpec,
) (DockerContainerWriteRequest, error) {
	if dockerContainerSpecUsesStandardCodeWorkspace(spec) {
		return DockerContainerWriteRequest{}, errors.New(
			"Standard Code Docker requires its fixed Git metadata mask")
	}
	return newDockerContainerWriteRequest(ctx, workspaceRoot, "", spec, false)
}

// NewDockerStandardCodeContainerWriteRequest adds the one fixed read-only
// metadata mask to the exact writable Drydock mount. The mask source is trusted
// application configuration, never a Manifest or command field.
func NewDockerStandardCodeContainerWriteRequest(ctx context.Context, workspaceRoot,
	gitMetadataMask string, spec DockerContainerSpec,
) (DockerContainerWriteRequest, error) {
	if !dockerContainerSpecUsesStandardCodeWorkspace(spec) {
		return DockerContainerWriteRequest{}, errors.New(
			"Git metadata masking is restricted to the fixed Standard Code profile")
	}
	return newDockerContainerWriteRequest(ctx, workspaceRoot, gitMetadataMask, spec, false)
}

func newDockerContainerWriteRequest(ctx context.Context, workspaceRoot,
	gitMetadataMask string, spec DockerContainerSpec, nativeGitSource bool,
) (DockerContainerWriteRequest, error) {
	if err := ctx.Err(); err != nil {
		return DockerContainerWriteRequest{}, err
	}
	if err := ValidateDockerContainerRehearsalProfile(spec); err != nil {
		return DockerContainerWriteRequest{}, err
	}
	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return DockerContainerWriteRequest{}, err
	}
	root = filepath.Clean(root)
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return DockerContainerWriteRequest{}, fmt.Errorf("resolve Docker rehearsal workspace root: %w", err)
	}
	resolvedRoot = filepath.Clean(resolvedRoot)
	info, err := os.Stat(resolvedRoot)
	if err != nil || !info.IsDir() {
		return DockerContainerWriteRequest{}, errors.New("docker rehearsal workspace root is not a directory")
	}

	standardCode := dockerContainerSpecUsesStandardCodeWorkspace(spec)
	if nativeGitSource {
		if !standardCode || validateDockerNativeGitSource(resolvedRoot) != nil {
			return DockerContainerWriteRequest{}, errors.New("native Git Docker source metadata is invalid")
		}
		var maskErr error
		gitMetadataMask, maskErr = validateDockerNativeGitMetadataMask(gitMetadataMask)
		if maskErr != nil {
			return DockerContainerWriteRequest{}, maskErr
		}
	} else if standardCode {
		if err := validateDockerStandardCodeWorkspaceGitMetadata(resolvedRoot); err != nil {
			return DockerContainerWriteRequest{}, err
		}
		var maskErr error
		gitMetadataMask, maskErr = validateDockerStandardCodeGitMetadataMask(gitMetadataMask)
		if maskErr != nil {
			return DockerContainerWriteRequest{}, maskErr
		}
	} else if gitMetadataMask != "" {
		return DockerContainerWriteRequest{}, errors.New("generic Docker request cannot carry a Git metadata mask")
	}

	mountCount := len(spec.Mounts)
	if standardCode {
		mountCount++
	}
	mounts := make([]DockerHostMount, 0, mountCount)
	for index, planned := range spec.Mounts {
		if err := ctx.Err(); err != nil {
			return DockerContainerWriteRequest{}, err
		}
		source, err := resolveDockerRehearsalMountSource(resolvedRoot, planned.Source)
		if err != nil {
			return DockerContainerWriteRequest{}, fmt.Errorf("resolve Docker rehearsal mount %d: %w", index+1, err)
		}
		mounts = append(mounts, DockerHostMount{Source: source, Target: planned.Target,
			ReadOnly: planned.Access == MountReadOnly, Propagation: planned.Propagation})
	}
	if standardCode {
		mounts = append(mounts, DockerHostMount{Source: gitMetadataMask,
			Target: DockerStandardCodeGitMetadataTarget, ReadOnly: true,
			Propagation: DockerMountPropagationPrivate})
	}
	request := DockerContainerWriteRequest{ProtocolVersion: DockerContainerWriteProtocolVersion,
		Spec: spec, HostMounts: mounts, NativeGitSource: nativeGitSource}
	request.MountFingerprint = dockerHostMountFingerprint(mounts)
	request.RequestFingerprint = fingerprint(DockerContainerWriteProtocolVersion,
		spec.SpecFingerprint, request.MountFingerprint)
	return request, request.Validate()
}

func (request DockerContainerWriteRequest) Validate() error {
	standardCode := dockerContainerSpecUsesStandardCodeWorkspace(request.Spec)
	expectedMounts := len(request.Spec.Mounts)
	if standardCode {
		expectedMounts++
	}
	if request.NativeGitSource && !standardCode || request.ProtocolVersion != DockerContainerWriteProtocolVersion ||
		ValidateDockerContainerRehearsalProfile(request.Spec) != nil ||
		len(request.HostMounts) != expectedMounts ||
		!validDigest(request.MountFingerprint) || !validDigest(request.RequestFingerprint) {
		return errors.New("docker container write request is invalid")
	}
	for index, mount := range request.HostMounts[:len(request.Spec.Mounts)] {
		planned := request.Spec.Mounts[index]
		if mount.Validate() != nil || mount.Target != planned.Target ||
			mount.ReadOnly != (planned.Access == MountReadOnly) ||
			mount.Propagation != planned.Propagation {
			return errors.New("docker container write mount does not match the compiled plan")
		}
	}
	if standardCode {
		workspaceMount := request.HostMounts[0]
		mask := request.HostMounts[len(request.HostMounts)-1]
		if workspaceMount.Source == mask.Source ||
			mask.Validate() != nil || !mask.ReadOnly ||
			mask.Target != DockerStandardCodeGitMetadataTarget ||
			mask.Propagation != DockerMountPropagationPrivate {
			return errors.New("Standard Code Git metadata mask is invalid")
		}
		if request.NativeGitSource {
			if resolved, err := validateDockerNativeGitMetadataMask(mask.Source); validateDockerNativeGitSource(workspaceMount.Source) != nil || err != nil || resolved != mask.Source || mask.Source == filepath.Join(workspaceMount.Source, ".git") {
				return errors.New("native Git Docker metadata mount changed")
			}
		} else {
			if validateDockerStandardCodeWorkspaceGitMetadata(workspaceMount.Source) != nil {
				return errors.New("Standard Code Git metadata shape changed")
			}
			if resolved, err := validateDockerStandardCodeGitMetadataMask(mask.Source); err != nil || resolved != mask.Source {
				return errors.New("Standard Code Git metadata mask changed")
			}
		}
	}
	if request.MountFingerprint != dockerHostMountFingerprint(request.HostMounts) ||
		request.RequestFingerprint != fingerprint(DockerContainerWriteProtocolVersion,
			request.Spec.SpecFingerprint, request.MountFingerprint) {
		return errors.New("docker container write request fingerprint is invalid")
	}
	return nil
}

func validateDockerStandardCodeWorkspaceGitMetadata(root string) error {
	target := filepath.Join(root, ".git")
	info, err := os.Lstat(target)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < int64(len("gitdir: x\n")) || info.Size() > 4*1024 {
		return errors.New("Standard Code Drydock Git metadata is not a bounded linked-worktree file")
	}
	content, err := os.ReadFile(target)
	text := string(content)
	value := strings.TrimSuffix(strings.TrimPrefix(text, "gitdir: "), "\n")
	if err != nil || !strings.HasPrefix(text, "gitdir: ") ||
		!strings.HasSuffix(text, "\n") || strings.Count(text, "\n") != 1 ||
		value == "" || value != strings.TrimSpace(value) ||
		strings.ContainsAny(value, "\r\n") {
		return errors.New("Standard Code Drydock Git metadata is invalid")
	}
	return nil
}

func validateDockerStandardCodeGitMetadataMask(source string) (string, error) {
	if source == "" || strings.ContainsRune(source, 0) {
		return "", errors.New("Standard Code Git metadata mask is unavailable")
	}
	abs, err := filepath.Abs(source)
	if err != nil || filepath.Clean(abs) != source {
		return "", errors.New("Standard Code Git metadata mask path is invalid")
	}
	info, err := os.Lstat(abs)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() != int64(len(DockerStandardCodeGitMetadataMask)) {
		return "", errors.New("Standard Code Git metadata mask is not an exact regular file")
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil || filepath.Clean(resolved) != abs {
		return "", errors.New("Standard Code Git metadata mask identity changed")
	}
	content, err := os.ReadFile(abs)
	if err != nil || string(content) != DockerStandardCodeGitMetadataMask {
		return "", errors.New("Standard Code Git metadata mask content changed")
	}
	return abs, nil
}

func ValidateDockerContainerRehearsalProfile(spec DockerContainerSpec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	if spec.Network.Mode != "disabled" || spec.Network.Driver != DockerNetworkDriverNone ||
		len(spec.Network.AllowedTargets) != 0 || spec.Network.ExactAllowlist ||
		spec.Network.GuardRequired || len(spec.Environment) != 0 || spec.SecretMountTarget != "" {
		return errors.New("docker rehearsal requires the network-disabled, environment-free, no-secret profile")
	}
	return nil
}

func DockerContainerPlanMatchesSpec(plan DockerContainerPlan, spec DockerContainerSpec) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	if err := ValidateDockerContainerRehearsalProfile(spec); err != nil {
		return err
	}
	readOnly := 0
	for _, mount := range spec.Mounts {
		if mount.InputReadOnly {
			readOnly++
		}
	}
	if plan.ObservationID != spec.ObservationID || plan.RunID != spec.RunID ||
		plan.ExecutionID != spec.ExecutionID || plan.ManifestFingerprint != spec.ManifestFingerprint ||
		plan.AuthorityFingerprint != spec.AuthorityFingerprint || plan.ImageDigest != spec.ImageDigest ||
		plan.OSType != spec.OSType || plan.Architecture != spec.Architecture ||
		plan.ContainerUser != spec.User || plan.SpecFingerprint != spec.SpecFingerprint ||
		plan.CommandFingerprint != spec.CommandFingerprint ||
		plan.MountPlanFingerprint != spec.MountPlanFingerprint ||
		plan.NetworkPlanFingerprint != spec.NetworkPlanFingerprint ||
		plan.SecretPlanFingerprint != spec.SecretPlanFingerprint ||
		plan.ContainerConfigFingerprint != spec.ContainerConfigFingerprint ||
		plan.ResourcePlanFingerprint != spec.ResourcePlanFingerprint ||
		plan.TerminationPlanFingerprint != spec.TerminationPlanFingerprint ||
		plan.LabelPlanFingerprint != spec.LabelPlanFingerprint ||
		plan.OrphanPlanFingerprint != spec.OrphanPlanFingerprint ||
		plan.ContainerNameFingerprint != fingerprint("sandbox_docker_container_name.v1", spec.ContainerName) ||
		plan.MountCount != len(spec.Mounts) || plan.ReadOnlyMountCount != readOnly ||
		plan.WritableMountCount != len(spec.Mounts)-readOnly ||
		plan.EnvironmentCount != len(spec.Environment) || plan.SecretReferenceCount != 0 ||
		plan.InputArtifactCount != spec.InputArtifactCount || plan.OutputCount != spec.OutputCount ||
		plan.NetworkMode != spec.Network.Mode || plan.NetworkTargetCount != 0 ||
		plan.NanoCPUs != spec.Resources.NanoCPUs || plan.MemoryBytes != spec.Resources.MemoryBytes ||
		plan.PIDs != spec.Resources.PIDs || plan.MaxOutputBytes != spec.Resources.MaxOutputBytes ||
		plan.TimeoutSeconds != spec.Termination.TimeoutSeconds ||
		plan.GracePeriodMillis != spec.Termination.GracePeriodMillis {
		return errors.New("docker container plan does not match the recompiled specification")
	}
	return nil
}

func DockerObservationSupportsContainerWrite(report DockerObservationReport) bool {
	requestedMajor, requestedMinor, ok := parseDockerAPIVersion(DockerContainerWriteAPIVersion)
	if !ok {
		return false
	}
	maximumMajor, maximumMinor, maximumOK := parseDockerAPIVersion(report.APIVersion)
	minimumMajor, minimumMinor, minimumOK := parseDockerAPIVersion(report.MinAPIVersion)
	if !maximumOK || !minimumOK {
		return false
	}
	return compareDockerAPIVersion(minimumMajor, minimumMinor, requestedMajor, requestedMinor) <= 0 &&
		compareDockerAPIVersion(requestedMajor, requestedMinor, maximumMajor, maximumMinor) <= 0
}

func parseDockerAPIVersion(value string) (int, int, bool) {
	if !validDockerAPIVersion(value) {
		return 0, 0, false
	}
	parts := strings.Split(value, ".")
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	return major, minor, majorErr == nil && minorErr == nil
}

func compareDockerAPIVersion(firstMajor, firstMinor, secondMajor, secondMinor int) int {
	if firstMajor < secondMajor || (firstMajor == secondMajor && firstMinor < secondMinor) {
		return -1
	}
	if firstMajor == secondMajor && firstMinor == secondMinor {
		return 0
	}
	return 1
}

func resolveDockerRehearsalMountSource(root, source string) (string, error) {
	if validateWorkspacePath("Docker rehearsal mount source", source) != nil {
		return "", errors.New("docker rehearsal mount source is invalid")
	}
	cursor := root
	for _, component := range strings.Split(source, "/") {
		cursor = filepath.Join(cursor, filepath.FromSlash(component))
		info, err := os.Lstat(cursor)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("docker rehearsal mount source cannot contain symlinks")
		}
	}
	resolved, err := filepath.EvalSymlinks(cursor)
	if err != nil {
		return "", err
	}
	resolved = filepath.Clean(resolved)
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(relative) {
		return "", errors.New("docker rehearsal mount source escaped the workspace")
	}
	info, err := os.Lstat(resolved)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || (!info.Mode().IsRegular() && !info.IsDir()) {
		return "", errors.New("docker rehearsal mount source is not a regular file or directory")
	}
	return resolved, nil
}

func dockerHostMountFingerprint(mounts []DockerHostMount) string {
	parts := []string{"sandbox_docker_host_mounts.v1", strconv.Itoa(len(mounts))}
	for _, mount := range mounts {
		parts = append(parts, mount.Source, mount.Target, strconv.FormatBool(mount.ReadOnly),
			mount.Propagation)
	}
	return fingerprint(parts...)
}

type DockerContainerWriteStep struct {
	Ordinal           int
	Name              string
	State             string
	DaemonReads       int
	DaemonWrites      int
	ProductionApplied bool
	StepDigest        string
}

func (step DockerContainerWriteStep) Validate(requestFingerprint string) error {
	if step.Ordinal < 1 || step.Ordinal > len(dockerContainerWriteStepNames) ||
		step.Name != dockerContainerWriteStepNames[step.Ordinal-1] ||
		step.State != DockerContainerWriteStepStateComplete || step.DaemonReads < 0 ||
		step.DaemonWrites < 0 || step.DaemonReads > 1 || step.DaemonWrites > 1 ||
		step.ProductionApplied != (step.DaemonWrites == 1) ||
		step.StepDigest != fingerprint("sandbox_docker_write_transport_step.v1",
			requestFingerprint, strconv.Itoa(step.Ordinal), step.Name, step.State,
			strconv.Itoa(step.DaemonReads), strconv.Itoa(step.DaemonWrites),
			strconv.FormatBool(step.ProductionApplied)) {
		return errors.New("docker container write step is invalid")
	}
	switch step.Ordinal {
	case 1:
		if step.DaemonReads != 1 || step.DaemonWrites != 0 {
			return errors.New("docker image-profile step must remain read-only")
		}
	case 2:
		if step.DaemonReads != 1 {
			return errors.New("docker reconciliation step must inspect the deterministic name")
		}
	case 3, 5:
		if step.DaemonReads != 0 || step.DaemonWrites != 1 {
			return errors.New("docker create/remove step must perform one bounded write")
		}
	case 4:
		if step.DaemonReads != 1 || step.DaemonWrites != 0 {
			return errors.New("docker verification step must remain read-only")
		}
	}
	return nil
}

type DockerContainerWriteResult struct {
	ProtocolVersion              string
	Source                       string
	Status                       string
	EndpointClass                string
	EndpointFingerprint          string
	RequestFingerprint           string
	SpecFingerprint              string
	ContainerIDFingerprint       string
	InspectionFingerprint        string
	TransportFingerprint         string
	StepCount                    int
	DaemonReadCount              int
	DaemonWriteCount             int
	ReconciledContainerCount     int
	ConfigurationMatched         bool
	ContainerCreated             bool
	ContainerInspected           bool
	ContainerRemoved             bool
	ContainerStarted             bool
	ProcessExecuted              bool
	ImagePulled                  bool
	OutputExported               bool
	CleanupConfirmed             bool
	DaemonReachable              bool
	DaemonWriteSubmitted         bool
	ProductionExecutionSubmitted bool
	ProductionVerified           bool
	BackendEnabled               bool
	ExecutionAuthorized          bool
	ArtifactCommitAuthorized     bool
	Steps                        []DockerContainerWriteStep
}

func (result DockerContainerWriteResult) Validate() error {
	endpoint, err := NewDockerObservationEndpoint(result.EndpointClass)
	if err != nil || result.ProtocolVersion != DockerContainerWriteProtocolVersion ||
		result.Source != DockerContainerRehearsalSourceLocal ||
		result.Status != DockerContainerWriteStatusComplete ||
		result.EndpointClass != DockerObservationEndpointLocalUnix ||
		result.EndpointFingerprint != endpoint.Fingerprint ||
		!validDigest(result.RequestFingerprint) || !validDigest(result.SpecFingerprint) ||
		!validDigest(result.ContainerIDFingerprint) || !validDigest(result.InspectionFingerprint) ||
		!validDigest(result.TransportFingerprint) ||
		result.StepCount != MaxDockerContainerWriteSteps || len(result.Steps) != result.StepCount ||
		result.DaemonReadCount != 3 || result.ReconciledContainerCount < 0 ||
		result.ReconciledContainerCount > 1 ||
		result.DaemonWriteCount != 2+result.ReconciledContainerCount ||
		!result.ConfigurationMatched || !result.ContainerCreated || !result.ContainerInspected ||
		!result.ContainerRemoved || result.ContainerStarted || result.ProcessExecuted ||
		result.ImagePulled || result.OutputExported || !result.CleanupConfirmed ||
		!result.DaemonReachable || !result.DaemonWriteSubmitted ||
		result.ProductionExecutionSubmitted || result.ProductionVerified ||
		result.BackendEnabled || result.ExecutionAuthorized || result.ArtifactCommitAuthorized {
		return errors.New("docker container write result violates the create-inspect-remove boundary")
	}
	reads, writes := 0, 0
	for index, step := range result.Steps {
		if step.Ordinal != index+1 || step.Validate(result.RequestFingerprint) != nil {
			return errors.New("docker container write step sequence is invalid")
		}
		reads += step.DaemonReads
		writes += step.DaemonWrites
	}
	if reads != result.DaemonReadCount || writes != result.DaemonWriteCount ||
		result.Steps[1].DaemonWrites != result.ReconciledContainerCount ||
		result.TransportFingerprint != dockerContainerWriteResultFingerprint(result) {
		return errors.New("docker container write aggregate is invalid")
	}
	return nil
}

func dockerContainerWriteResultFingerprint(result DockerContainerWriteResult) string {
	parts := []string{DockerContainerWriteProtocolVersion, result.Source, result.Status,
		result.EndpointClass, result.EndpointFingerprint, result.RequestFingerprint,
		result.SpecFingerprint, result.ContainerIDFingerprint, result.InspectionFingerprint,
		strconv.Itoa(result.StepCount), strconv.Itoa(result.DaemonReadCount),
		strconv.Itoa(result.DaemonWriteCount), strconv.Itoa(result.ReconciledContainerCount),
		strconv.FormatBool(result.ConfigurationMatched), strconv.FormatBool(result.ContainerCreated),
		strconv.FormatBool(result.ContainerInspected), strconv.FormatBool(result.ContainerRemoved),
		strconv.FormatBool(result.ContainerStarted), strconv.FormatBool(result.ProcessExecuted),
		strconv.FormatBool(result.ImagePulled), strconv.FormatBool(result.OutputExported),
		strconv.FormatBool(result.CleanupConfirmed), strconv.FormatBool(result.DaemonReachable),
		strconv.FormatBool(result.DaemonWriteSubmitted),
		strconv.FormatBool(result.ProductionExecutionSubmitted),
		strconv.FormatBool(result.ProductionVerified), strconv.FormatBool(result.BackendEnabled),
		strconv.FormatBool(result.ExecutionAuthorized),
		strconv.FormatBool(result.ArtifactCommitAuthorized)}
	for _, step := range result.Steps {
		parts = append(parts, step.StepDigest)
	}
	return fingerprint(parts...)
}

type DockerContainerWriteTransport interface {
	Endpoint() DockerObservationEndpoint
	Rehearse(ctx context.Context, request DockerContainerWriteRequest) (DockerContainerWriteResult, error)
	Stage(ctx context.Context, request DockerContainerWriteRequest) (DockerContainerStageResult, error)
	Cleanup(ctx context.Context, request DockerContainerWriteRequest,
		stage DockerContainerStageResult) (DockerContainerCleanupResult, error)
}

type UnavailableDockerContainerWriteTransport struct {
	endpoint DockerObservationEndpoint
	code     string
}

func NewUnavailableDockerContainerWriteTransport() UnavailableDockerContainerWriteTransport {
	endpoint, _ := NewDockerObservationEndpoint(DockerObservationEndpointLocalUnix)
	return UnavailableDockerContainerWriteTransport{endpoint: endpoint,
		code: DockerContainerWriteFailureDisabled}
}

func newUnsupportedDockerContainerWriteTransport() UnavailableDockerContainerWriteTransport {
	endpoint, _ := NewDockerObservationEndpoint(DockerObservationEndpointLocalUnix)
	return UnavailableDockerContainerWriteTransport{endpoint: endpoint,
		code: DockerContainerWriteFailureUnsupported}
}

func (transport UnavailableDockerContainerWriteTransport) Endpoint() DockerObservationEndpoint {
	return transport.endpoint
}

func (transport UnavailableDockerContainerWriteTransport) Rehearse(ctx context.Context,
	_ DockerContainerWriteRequest,
) (DockerContainerWriteResult, error) {
	if err := ctx.Err(); err != nil {
		return DockerContainerWriteResult{}, err
	}
	code := transport.code
	if code != DockerContainerWriteFailureDisabled && code != DockerContainerWriteFailureUnsupported {
		code = DockerContainerWriteFailureDisabled
	}
	return DockerContainerWriteResult{}, newDockerContainerWriteError(code)
}

func (transport UnavailableDockerContainerWriteTransport) Stage(ctx context.Context,
	_ DockerContainerWriteRequest,
) (DockerContainerStageResult, error) {
	if err := ctx.Err(); err != nil {
		return DockerContainerStageResult{}, err
	}
	code := transport.code
	if code != DockerContainerWriteFailureDisabled && code != DockerContainerWriteFailureUnsupported {
		code = DockerContainerWriteFailureDisabled
	}
	return DockerContainerStageResult{}, newDockerContainerWriteError(code)
}

func (transport UnavailableDockerContainerWriteTransport) Cleanup(ctx context.Context,
	_ DockerContainerWriteRequest, _ DockerContainerStageResult,
) (DockerContainerCleanupResult, error) {
	if err := ctx.Err(); err != nil {
		return DockerContainerCleanupResult{}, err
	}
	code := transport.code
	if code != DockerContainerWriteFailureDisabled && code != DockerContainerWriteFailureUnsupported {
		code = DockerContainerWriteFailureDisabled
	}
	return DockerContainerCleanupResult{}, newDockerContainerWriteError(code)
}

type DockerContainerRehearsal struct {
	ID                           string
	PlanID                       string
	ObservationID                string
	EvidenceID                   string
	OutputSimulationID           string
	PreflightID                  string
	ExecutionID                  string
	CandidateID                  string
	PreparationID                string
	RunID                        string
	MissionID                    string
	WorkspaceID                  string
	ProtocolVersion              string
	Source                       string
	TrustClass                   string
	Status                       string
	ManifestFingerprint          string
	AuthorizationFingerprint     string
	PolicyFingerprint            string
	MountBindingFingerprint      string
	InputArtifactDigest          string
	ThreatModelFingerprint       string
	OutputPlanFingerprint        string
	ObservationFingerprint       string
	AuthorityFingerprint         string
	SpecFingerprint              string
	PlanFingerprint              string
	ImageDigest                  string
	NetworkMode                  string
	EnvironmentCount             int
	SecretReferenceCount         int
	RequestFingerprint           string
	EndpointClass                string
	EndpointFingerprint          string
	ContainerIDFingerprint       string
	InspectionFingerprint        string
	TransportFingerprint         string
	RehearsalFingerprint         string
	StepCount                    int
	DaemonReadCount              int
	DaemonWriteCount             int
	ReconciledContainerCount     int
	ConfigurationMatched         bool
	ContainerNeverStarted        bool
	ProcessNeverExecuted         bool
	ImageNeverPulled             bool
	OutputNeverExported          bool
	CleanupConfirmed             bool
	DaemonReachable              bool
	DaemonWriteSubmitted         bool
	ProductionExecutionSubmitted bool
	ProductionVerified           bool
	BackendEnabled               bool
	ExecutionAuthorized          bool
	ArtifactCommitAuthorized     bool
	Result                       DockerContainerWriteResult
	RequestedBy                  string
	CreatedAt                    time.Time
	Replayed                     bool
}

func NewDockerContainerRehearsal(id string, plan DockerContainerPlan,
	spec DockerContainerSpec, result DockerContainerWriteResult, requestedBy string,
	createdAt time.Time,
) (DockerContainerRehearsal, error) {
	if err := DockerContainerPlanMatchesSpec(plan, spec); err != nil {
		return DockerContainerRehearsal{}, err
	}
	if err := result.Validate(); err != nil || result.SpecFingerprint != spec.SpecFingerprint ||
		result.RequestFingerprint == "" || requestedBy != plan.RequestedBy {
		return DockerContainerRehearsal{}, errors.New("docker write result does not bind the plan authority")
	}
	rehearsal := DockerContainerRehearsal{
		ID: id, PlanID: plan.ID, ObservationID: plan.ObservationID, EvidenceID: plan.EvidenceID,
		OutputSimulationID: plan.OutputSimulationID, PreflightID: plan.PreflightID,
		ExecutionID: plan.ExecutionID, CandidateID: plan.CandidateID,
		PreparationID: plan.PreparationID, RunID: plan.RunID, MissionID: plan.MissionID,
		WorkspaceID: plan.WorkspaceID, ProtocolVersion: DockerContainerRehearsalProtocolVersion,
		Source: DockerContainerRehearsalSourceLocal, TrustClass: DockerContainerRehearsalTrustClass,
		Status:                   DockerContainerRehearsalStatusComplete,
		ManifestFingerprint:      plan.ManifestFingerprint,
		AuthorizationFingerprint: plan.AuthorizationFingerprint,
		PolicyFingerprint:        plan.PolicyFingerprint, MountBindingFingerprint: plan.MountBindingFingerprint,
		InputArtifactDigest:    plan.InputArtifactDigest,
		ThreatModelFingerprint: plan.ThreatModelFingerprint,
		OutputPlanFingerprint:  plan.OutputPlanFingerprint,
		ObservationFingerprint: plan.ObservationFingerprint,
		AuthorityFingerprint:   plan.AuthorityFingerprint, SpecFingerprint: plan.SpecFingerprint,
		PlanFingerprint: plan.PlanFingerprint, ImageDigest: plan.ImageDigest,
		NetworkMode: plan.NetworkMode, EnvironmentCount: plan.EnvironmentCount,
		SecretReferenceCount: plan.SecretReferenceCount,
		RequestFingerprint:   result.RequestFingerprint, EndpointClass: result.EndpointClass,
		EndpointFingerprint:    result.EndpointFingerprint,
		ContainerIDFingerprint: result.ContainerIDFingerprint,
		InspectionFingerprint:  result.InspectionFingerprint,
		TransportFingerprint:   result.TransportFingerprint,
		StepCount:              result.StepCount, DaemonReadCount: result.DaemonReadCount,
		DaemonWriteCount:         result.DaemonWriteCount,
		ReconciledContainerCount: result.ReconciledContainerCount,
		ConfigurationMatched:     result.ConfigurationMatched,
		ContainerNeverStarted:    !result.ContainerStarted,
		ProcessNeverExecuted:     !result.ProcessExecuted, ImageNeverPulled: !result.ImagePulled,
		OutputNeverExported: !result.OutputExported, CleanupConfirmed: result.CleanupConfirmed,
		DaemonReachable:              result.DaemonReachable,
		DaemonWriteSubmitted:         result.DaemonWriteSubmitted,
		ProductionExecutionSubmitted: result.ProductionExecutionSubmitted,
		ProductionVerified:           result.ProductionVerified, BackendEnabled: result.BackendEnabled,
		ExecutionAuthorized:      result.ExecutionAuthorized,
		ArtifactCommitAuthorized: result.ArtifactCommitAuthorized,
		Result:                   result, RequestedBy: requestedBy, CreatedAt: createdAt,
	}
	rehearsal.RehearsalFingerprint = dockerContainerRehearsalFingerprint(rehearsal)
	return rehearsal, rehearsal.Validate()
}

func (rehearsal DockerContainerRehearsal) Validate() error {
	for _, value := range []string{rehearsal.ID, rehearsal.PlanID, rehearsal.ObservationID,
		rehearsal.EvidenceID, rehearsal.OutputSimulationID, rehearsal.PreflightID,
		rehearsal.ExecutionID, rehearsal.CandidateID, rehearsal.PreparationID,
		rehearsal.RunID, rehearsal.MissionID, rehearsal.WorkspaceID, rehearsal.RequestedBy} {
		if validateStoredIdentity("Docker rehearsal identity", value) != nil {
			return errors.New("docker container rehearsal identity is invalid")
		}
	}
	if rehearsal.ProtocolVersion != DockerContainerRehearsalProtocolVersion ||
		rehearsal.Source != DockerContainerRehearsalSourceLocal ||
		rehearsal.TrustClass != DockerContainerRehearsalTrustClass ||
		rehearsal.Status != DockerContainerRehearsalStatusComplete ||
		!ValidOCIImageDigest(rehearsal.ImageDigest) || rehearsal.NetworkMode != "disabled" ||
		rehearsal.EnvironmentCount != 0 || rehearsal.SecretReferenceCount != 0 ||
		rehearsal.StepCount != MaxDockerContainerWriteSteps || rehearsal.DaemonReadCount != 3 ||
		rehearsal.DaemonWriteCount != 2+rehearsal.ReconciledContainerCount ||
		rehearsal.ReconciledContainerCount < 0 || rehearsal.ReconciledContainerCount > 1 ||
		!rehearsal.ConfigurationMatched || !rehearsal.ContainerNeverStarted ||
		!rehearsal.ProcessNeverExecuted || !rehearsal.ImageNeverPulled ||
		!rehearsal.OutputNeverExported || !rehearsal.CleanupConfirmed ||
		!rehearsal.DaemonReachable || !rehearsal.DaemonWriteSubmitted ||
		rehearsal.ProductionExecutionSubmitted || rehearsal.ProductionVerified ||
		rehearsal.BackendEnabled || rehearsal.ExecutionAuthorized ||
		rehearsal.ArtifactCommitAuthorized || rehearsal.CreatedAt.IsZero() {
		return errors.New("docker container rehearsal widened execution authority")
	}
	for _, value := range []string{rehearsal.ManifestFingerprint,
		rehearsal.AuthorizationFingerprint, rehearsal.PolicyFingerprint,
		rehearsal.MountBindingFingerprint, rehearsal.InputArtifactDigest,
		rehearsal.ThreatModelFingerprint, rehearsal.OutputPlanFingerprint,
		rehearsal.ObservationFingerprint, rehearsal.AuthorityFingerprint,
		rehearsal.SpecFingerprint, rehearsal.PlanFingerprint, rehearsal.RequestFingerprint,
		rehearsal.EndpointFingerprint, rehearsal.ContainerIDFingerprint,
		rehearsal.InspectionFingerprint, rehearsal.TransportFingerprint,
		rehearsal.RehearsalFingerprint} {
		if !validDigest(value) {
			return errors.New("docker container rehearsal fingerprint is invalid")
		}
	}
	if err := rehearsal.Result.Validate(); err != nil ||
		rehearsal.Result.RequestFingerprint != rehearsal.RequestFingerprint ||
		rehearsal.Result.SpecFingerprint != rehearsal.SpecFingerprint ||
		rehearsal.Result.EndpointClass != rehearsal.EndpointClass ||
		rehearsal.Result.EndpointFingerprint != rehearsal.EndpointFingerprint ||
		rehearsal.Result.ContainerIDFingerprint != rehearsal.ContainerIDFingerprint ||
		rehearsal.Result.InspectionFingerprint != rehearsal.InspectionFingerprint ||
		rehearsal.Result.TransportFingerprint != rehearsal.TransportFingerprint ||
		rehearsal.RehearsalFingerprint != dockerContainerRehearsalFingerprint(rehearsal) {
		return errors.New("docker container rehearsal result binding is invalid")
	}
	return nil
}

func dockerContainerRehearsalFingerprint(value DockerContainerRehearsal) string {
	return fingerprint(DockerContainerRehearsalProtocolVersion, value.Source, value.TrustClass,
		value.Status, value.PlanID, value.ObservationID, value.EvidenceID,
		value.OutputSimulationID, value.PreflightID, value.ExecutionID, value.CandidateID,
		value.PreparationID, value.RunID, value.MissionID, value.WorkspaceID,
		value.ManifestFingerprint, value.AuthorizationFingerprint, value.PolicyFingerprint,
		value.MountBindingFingerprint, value.InputArtifactDigest, value.ThreatModelFingerprint,
		value.OutputPlanFingerprint, value.ObservationFingerprint, value.AuthorityFingerprint,
		value.SpecFingerprint, value.PlanFingerprint, value.ImageDigest, value.NetworkMode,
		strconv.Itoa(value.EnvironmentCount), strconv.Itoa(value.SecretReferenceCount),
		value.RequestFingerprint, value.EndpointClass, value.EndpointFingerprint,
		value.ContainerIDFingerprint, value.InspectionFingerprint, value.TransportFingerprint,
		strconv.Itoa(value.StepCount), strconv.Itoa(value.DaemonReadCount),
		strconv.Itoa(value.DaemonWriteCount), strconv.Itoa(value.ReconciledContainerCount),
		strconv.FormatBool(value.ConfigurationMatched),
		strconv.FormatBool(value.ContainerNeverStarted),
		strconv.FormatBool(value.ProcessNeverExecuted), strconv.FormatBool(value.ImageNeverPulled),
		strconv.FormatBool(value.OutputNeverExported), strconv.FormatBool(value.CleanupConfirmed),
		strconv.FormatBool(value.DaemonReachable), strconv.FormatBool(value.DaemonWriteSubmitted),
		strconv.FormatBool(value.ProductionExecutionSubmitted),
		strconv.FormatBool(value.ProductionVerified), strconv.FormatBool(value.BackendEnabled),
		strconv.FormatBool(value.ExecutionAuthorized),
		strconv.FormatBool(value.ArtifactCommitAuthorized), value.Result.TransportFingerprint)
}

type DockerContainerRehearsalOperation struct {
	KeyDigest          string
	RequestFingerprint string
	RehearsalID        string
	PlanID             string
	RunID              string
	RequestedBy        string
	CreatedAt          time.Time
}

func (operation DockerContainerRehearsalOperation) Validate() error {
	for _, value := range []string{operation.RehearsalID, operation.PlanID,
		operation.RunID, operation.RequestedBy} {
		if validateStoredIdentity("Docker rehearsal operation identity", value) != nil {
			return errors.New("docker rehearsal operation identity is invalid")
		}
	}
	if !validDigest(operation.KeyDigest) || !validDigest(operation.RequestFingerprint) ||
		operation.CreatedAt.IsZero() {
		return errors.New("docker rehearsal operation is invalid")
	}
	return nil
}

func DockerContainerRehearsalRequestFingerprint(value DockerContainerRehearsal) string {
	return fingerprint("sandbox_docker_container_rehearsal_request.v1", value.PlanID,
		value.ManifestFingerprint, value.AuthorityFingerprint, value.SpecFingerprint,
		value.PlanFingerprint, value.RequestFingerprint, value.RequestedBy)
}

// NewDockerNativeGitContainerWriteRequest is only for a natively authorized
// source repository. An empty directory hides its metadata behind a read-only mount.
func NewDockerNativeGitContainerWriteRequest(ctx context.Context, root, mask string, spec DockerContainerSpec) (DockerContainerWriteRequest, error) {
	return newDockerContainerWriteRequest(ctx, root, mask, spec, true)
}

func validateDockerNativeGitSource(root string) error {
	target := filepath.Join(root, ".git")
	info, err := os.Lstat(target)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("native Git Docker metadata must be a directory")
	}
	canonical, err := filepath.EvalSymlinks(target)
	if err != nil || filepath.Clean(canonical) != target {
		return errors.New("native Git Docker metadata directory changed")
	}
	return nil
}

func validateDockerNativeGitMetadataMask(source string) (string, error) {
	if source == "" || !filepath.IsAbs(source) || filepath.Clean(source) != source {
		return "", errors.New("native Git metadata mask path is invalid")
	}
	info, err := os.Lstat(source)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("native Git metadata mask is not a directory")
	}
	canonical, err := filepath.EvalSymlinks(source)
	if err != nil || filepath.Clean(canonical) != source {
		return "", errors.New("native Git metadata mask identity changed")
	}
	entries, err := os.ReadDir(source)
	if err != nil || len(entries) != 0 {
		return "", errors.New("native Git metadata mask must be empty")
	}
	return source, nil
}
