package sandbox

import (
	"context"
	"errors"
	"strconv"
	"time"
)

const (
	DockerReadinessProtocolVersion = "sandbox.readiness.v1"
	DockerReadinessTTL             = 30 * time.Second

	DockerReadinessStatusReady       = "ready"
	DockerReadinessStatusDisabled    = "disabled"
	DockerReadinessStatusUnavailable = "unavailable"

	DockerReadinessReasonNone                         = "none"
	DockerReadinessReasonFeatureDisabled              = "feature_disabled"
	DockerReadinessReasonInvalidRequest               = "invalid_request"
	DockerReadinessReasonDaemonUnreachable            = "daemon_unreachable"
	DockerReadinessReasonAPIUnsupported               = "api_unsupported"
	DockerReadinessReasonPlatformUnsupported          = "platform_unsupported"
	DockerReadinessReasonPIDsLimitUnavailable         = "pids_limit_unavailable"
	DockerReadinessReasonResourceCapacityInsufficient = "resource_capacity_insufficient"
	DockerReadinessReasonImageUnavailable             = "image_unavailable"
	DockerReadinessReasonManagedEgressUnavailable     = "managed_egress_unavailable"

	DockerReadinessRemediationNone                   = "none"
	DockerReadinessRemediationEnableFeature          = "enable_docker_sandbox"
	DockerReadinessRemediationCorrectRequest         = "correct_sandbox_request"
	DockerReadinessRemediationStartDaemon            = "start_docker_engine"
	DockerReadinessRemediationUpgradeDaemon          = "upgrade_docker_engine"
	DockerReadinessRemediationUseLinuxContainers     = "use_linux_containers"
	DockerReadinessRemediationEnablePIDsLimit        = "enable_pids_limit"
	DockerReadinessRemediationReduceResourceLimits   = "reduce_resource_limits"
	DockerReadinessRemediationProvideCompatibleImage = "provide_compatible_image"
	DockerReadinessRemediationDisableNetwork         = "use_network_disabled"
)

// DockerRuntimeCapabilities are process-local startup grants. The zero value
// is deliberately disabled, and callers must never persist this value as Run
// authority. Managed egress remains fail-closed until a Go-owned enforcement
// transport is implemented.
type DockerRuntimeCapabilities struct {
	Enabled              bool
	ManagedEgressEnabled bool
}

func (capabilities DockerRuntimeCapabilities) Validate() error {
	if capabilities.ManagedEgressEnabled {
		return errors.New("Docker managed egress is not implemented")
	}
	return nil
}

// ReadinessProbe is the bounded, read-only Docker product readiness surface.
// Its contract intentionally contains no daemon address or Docker flags.
type ReadinessProbe interface {
	Check(ctx context.Context, capabilities DockerRuntimeCapabilities, manifest Manifest,
		imageDigest string) (DockerReadiness, error)
}

type DockerReadinessProbe struct {
	Transport DockerReadOnlyTransport
	now       func() time.Time
}

func NewDockerReadinessProbe(transport DockerReadOnlyTransport) (DockerReadinessProbe, error) {
	if transport == nil {
		return DockerReadinessProbe{}, errors.New("Docker readiness transport is required")
	}
	if err := transport.Endpoint().Validate(); err != nil {
		return DockerReadinessProbe{}, errors.New("Docker readiness requires a fixed local endpoint")
	}
	return DockerReadinessProbe{Transport: transport}, nil
}

func NewLocalDockerReadinessProbe() (DockerReadinessProbe, error) {
	return NewDockerReadinessProbe(NewLocalDockerReadOnlyTransport())
}

type DockerReadiness struct {
	ProtocolVersion      string    `json:"protocol_version"`
	Status               string    `json:"status"`
	ReasonCode           string    `json:"reason_code"`
	RemediationCode      string    `json:"remediation_code"`
	CheckedAt            time.Time `json:"checked_at"`
	ExpiresAt            time.Time `json:"expires_at"`
	EndpointClass        string    `json:"endpoint_class"`
	EndpointFingerprint  string    `json:"endpoint_fingerprint"`
	ImageDigest          string    `json:"image_digest,omitempty"`
	NetworkMode          string    `json:"network_mode,omitempty"`
	FeatureEnabled       bool      `json:"feature_enabled"`
	ManagedEgressEnabled bool      `json:"managed_egress_enabled"`
	DaemonReachable      bool      `json:"daemon_reachable"`
	ImageInspected       bool      `json:"image_inspected"`
	ImageProfileSafe     bool      `json:"image_profile_safe"`
	Ready                bool      `json:"ready"`
	APIVersion           string    `json:"api_version,omitempty"`
	MinAPIVersion        string    `json:"min_api_version,omitempty"`
	OSType               string    `json:"os_type,omitempty"`
	Architecture         string    `json:"architecture,omitempty"`
	NCPU                 int       `json:"ncpu,omitempty"`
	MemoryBytes          int64     `json:"memory_bytes,omitempty"`
	PIDsLimitSupported   bool      `json:"pids_limit_supported"`
	RequestedCPUQuotaMS  int       `json:"requested_cpu_quota_ms,omitempty"`
	RequestedMemoryBytes int64     `json:"requested_memory_bytes,omitempty"`
	RequestedPIDs        int       `json:"requested_pids,omitempty"`
	ReadinessFingerprint string    `json:"readiness_fingerprint"`
}

// This metadata read is bound to the configured immutable image digest. It
// starts no container and preserves ordinary readiness for older images.
func (probe DockerReadinessProbe) ProjectActionsAvailable(ctx context.Context, digest string) (bool, error) {
	image, err := probe.Transport.InspectImage(ctx, digest)
	if err != nil {
		return false, err
	}
	image, err = image.Normalize(digest)
	return err == nil && image.ProjectActionsVersion == "1", err
}

func (probe DockerReadinessProbe) ProjectPreparationAvailable(ctx context.Context, digest string) (bool, error) {
	image, err := probe.Transport.InspectImage(ctx, digest)
	if err != nil {
		return false, err
	}
	image, err = image.Normalize(digest)
	return err == nil && image.ProjectActionsVersion == "1" && image.PreparationVersion == "1", err
}

func (readiness DockerReadiness) Validate() error {
	status, remediation, ok := dockerReadinessDisposition(readiness.ReasonCode)
	if !ok || readiness.ProtocolVersion != DockerReadinessProtocolVersion ||
		readiness.Status != status || readiness.RemediationCode != remediation ||
		readiness.Ready != (readiness.Status == DockerReadinessStatusReady) ||
		readiness.CheckedAt.IsZero() || readiness.CheckedAt.Location() != time.UTC ||
		!readiness.ExpiresAt.Equal(readiness.CheckedAt.Add(DockerReadinessTTL)) ||
		readiness.ExpiresAt.Location() != time.UTC {
		return errors.New("Docker readiness disposition is invalid")
	}
	endpoint := DockerObservationEndpoint{
		Class: readiness.EndpointClass, Fingerprint: readiness.EndpointFingerprint,
	}
	if err := endpoint.Validate(); err != nil {
		return errors.New("Docker readiness endpoint is invalid")
	}
	if readiness.ImageDigest != "" && !ValidOCIImageDigest(readiness.ImageDigest) {
		return errors.New("Docker readiness image digest is invalid")
	}
	if readiness.NetworkMode != "" && readiness.NetworkMode != "disabled" &&
		readiness.NetworkMode != "allowlist" {
		return errors.New("Docker readiness network mode is invalid")
	}
	if !validDockerReadinessRequestResources(readiness) ||
		!validDockerReadinessObservations(readiness) {
		return errors.New("Docker readiness facts are invalid")
	}
	switch readiness.ReasonCode {
	case DockerReadinessReasonNone:
		if !readiness.FeatureEnabled || readiness.ManagedEgressEnabled ||
			!readiness.DaemonReachable || !readiness.ImageInspected ||
			!readiness.ImageProfileSafe ||
			readiness.NetworkMode != "disabled" ||
			!ValidOCIImageDigest(readiness.ImageDigest) ||
			!DockerObservationSupportsContainerWrite(DockerObservationReport{
				APIVersion: readiness.APIVersion, MinAPIVersion: readiness.MinAPIVersion,
			}) || readiness.OSType != "linux" || readiness.Architecture == "" ||
			readiness.NCPU < 1 || readiness.MemoryBytes < MinMemoryBytes ||
			!readiness.PIDsLimitSupported {
			return errors.New("ready Docker readiness is missing required facts")
		}
	case DockerReadinessReasonFeatureDisabled:
		if readiness.FeatureEnabled || readiness.DaemonReachable || readiness.ImageInspected ||
			readiness.ImageProfileSafe {
			return errors.New("disabled Docker readiness contains runtime claims")
		}
	case DockerReadinessReasonInvalidRequest:
		if readiness.DaemonReachable || readiness.ImageInspected || readiness.ImageProfileSafe {
			return errors.New("invalid Docker readiness request contains runtime claims")
		}
	case DockerReadinessReasonDaemonUnreachable:
		if readiness.DaemonReachable || readiness.ImageInspected || readiness.ImageProfileSafe {
			return errors.New("unreachable Docker readiness contains runtime claims")
		}
	case DockerReadinessReasonAPIUnsupported:
		if !readiness.DaemonReachable || readiness.ImageInspected || readiness.ImageProfileSafe {
			return errors.New("unsupported Docker API readiness is invalid")
		}
	case DockerReadinessReasonPlatformUnsupported,
		DockerReadinessReasonPIDsLimitUnavailable,
		DockerReadinessReasonResourceCapacityInsufficient:
		if !readiness.DaemonReachable || readiness.ImageInspected {
			return errors.New("Docker daemon capability failure is invalid")
		}
		if readiness.ImageProfileSafe {
			return errors.New("Docker daemon capability failure is invalid")
		}
	case DockerReadinessReasonImageUnavailable:
		if !readiness.DaemonReachable || readiness.ImageInspected || readiness.ImageProfileSafe {
			return errors.New("unavailable Docker image readiness is invalid")
		}
	case DockerReadinessReasonManagedEgressUnavailable:
		if readiness.DaemonReachable || readiness.ImageInspected || readiness.ImageProfileSafe ||
			(!readiness.ManagedEgressEnabled && readiness.NetworkMode != "allowlist") {
			return errors.New("managed-egress Docker readiness is invalid")
		}
	}
	if readiness.ReadinessFingerprint != dockerReadinessFingerprint(readiness) {
		return errors.New("Docker readiness fingerprint is invalid")
	}
	return nil
}

func (readiness DockerReadiness) ReadyAt(now time.Time) bool {
	if readiness.Validate() != nil || !readiness.Ready {
		return false
	}
	now = now.UTC()
	return !now.Before(readiness.CheckedAt) && now.Before(readiness.ExpiresAt)
}

func (probe DockerReadinessProbe) Check(ctx context.Context,
	capabilities DockerRuntimeCapabilities, manifest Manifest, imageDigest string,
) (DockerReadiness, error) {
	if ctx == nil {
		return DockerReadiness{}, errors.New("Docker readiness context is required")
	}
	if err := ctx.Err(); err != nil {
		return DockerReadiness{}, err
	}
	if probe.Transport == nil {
		return DockerReadiness{}, errors.New("Docker readiness transport is required")
	}
	endpoint := probe.Transport.Endpoint()
	if err := endpoint.Validate(); err != nil {
		return DockerReadiness{}, errors.New("Docker readiness requires a fixed local endpoint")
	}
	checkedAt := time.Now().UTC()
	if probe.now != nil {
		checkedAt = probe.now().UTC()
	}
	readiness := DockerReadiness{
		ProtocolVersion: DockerReadinessProtocolVersion,
		CheckedAt:       checkedAt,
		EndpointClass:   endpoint.Class, EndpointFingerprint: endpoint.Fingerprint,
		FeatureEnabled:       capabilities.Enabled,
		ManagedEgressEnabled: capabilities.ManagedEgressEnabled,
	}
	if capabilities.ManagedEgressEnabled {
		return finalizeDockerReadiness(readiness,
			DockerReadinessReasonManagedEgressUnavailable), nil
	}
	if err := capabilities.Validate(); err != nil {
		return finalizeDockerReadiness(readiness, DockerReadinessReasonInvalidRequest), nil
	}
	if !capabilities.Enabled {
		return finalizeDockerReadiness(readiness,
			DockerReadinessReasonFeatureDisabled), nil
	}
	normalized, err := NormalizeManifest(manifest)
	if err != nil || normalized.Backend != BackendDocker || !ValidOCIImageDigest(imageDigest) {
		return finalizeDockerReadiness(readiness, DockerReadinessReasonInvalidRequest), nil
	}
	readiness.ImageDigest = imageDigest
	readiness.NetworkMode = normalized.Network.Mode
	readiness.RequestedCPUQuotaMS = normalized.Resources.CPUQuotaMillis
	readiness.RequestedMemoryBytes = normalized.Resources.MemoryBytes
	readiness.RequestedPIDs = normalized.Resources.PIDs
	if normalized.Network.Mode != "disabled" {
		return finalizeDockerReadiness(readiness,
			DockerReadinessReasonManagedEgressUnavailable), nil
	}

	if err := probe.Transport.Ping(ctx); err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return DockerReadiness{}, contextError
		}
		return finalizeDockerReadiness(readiness,
			DockerReadinessReasonDaemonUnreachable), nil
	}
	readiness.DaemonReachable = true

	version, err := probe.Transport.Version(ctx)
	if err != nil {
		return probe.transportFailure(ctx, readiness, err,
			DockerReadinessReasonAPIUnsupported)
	}
	if err := version.Validate(); err != nil || version.OSType != "linux" {
		reason := DockerReadinessReasonAPIUnsupported
		if err == nil {
			reason = DockerReadinessReasonPlatformUnsupported
		}
		return finalizeDockerReadiness(readiness, reason), nil
	}
	readiness.APIVersion = version.APIVersion
	readiness.MinAPIVersion = version.MinAPIVersion
	readiness.OSType = version.OSType
	readiness.Architecture = version.Architecture
	if !DockerObservationSupportsContainerWrite(DockerObservationReport{
		APIVersion: version.APIVersion, MinAPIVersion: version.MinAPIVersion,
	}) {
		return finalizeDockerReadiness(readiness,
			DockerReadinessReasonAPIUnsupported), nil
	}

	info, err := probe.Transport.Info(ctx)
	if err != nil {
		return probe.transportFailure(ctx, readiness, err,
			DockerReadinessReasonPlatformUnsupported)
	}
	info, err = info.Normalize()
	if err != nil || info.OSType != "linux" || info.OSType != version.OSType ||
		info.Architecture != version.Architecture {
		return finalizeDockerReadiness(readiness,
			DockerReadinessReasonPlatformUnsupported), nil
	}
	readiness.NCPU = info.NCPU
	readiness.MemoryBytes = info.MemoryBytes
	readiness.PIDsLimitSupported = info.PidsLimit
	if !info.PidsLimit {
		return finalizeDockerReadiness(readiness,
			DockerReadinessReasonPIDsLimitUnavailable), nil
	}
	if info.NCPU < 1 || info.MemoryBytes < MinMemoryBytes ||
		int64(normalized.Resources.CPUQuotaMillis) > int64(info.NCPU)*1000 ||
		normalized.Resources.MemoryBytes > info.MemoryBytes {
		return finalizeDockerReadiness(readiness,
			DockerReadinessReasonResourceCapacityInsufficient), nil
	}

	image, err := probe.Transport.InspectImage(ctx, imageDigest)
	if err != nil {
		return probe.transportFailure(ctx, readiness, err,
			DockerReadinessReasonImageUnavailable)
	}
	image, err = image.Normalize(imageDigest)
	if err != nil {
		return finalizeDockerReadiness(readiness,
			DockerReadinessReasonImageUnavailable), nil
	}
	if image.OSType != "linux" || image.OSType != info.OSType ||
		image.Architecture != info.Architecture {
		return finalizeDockerReadiness(readiness,
			DockerReadinessReasonPlatformUnsupported), nil
	}
	if image.EnvironmentCount != 0 || image.VolumeCount != 0 {
		return finalizeDockerReadiness(readiness,
			DockerReadinessReasonImageUnavailable), nil
	}
	readiness.ImageInspected = true
	readiness.ImageProfileSafe = true
	return finalizeDockerReadiness(readiness, DockerReadinessReasonNone), nil
}

func (probe DockerReadinessProbe) transportFailure(ctx context.Context,
	readiness DockerReadiness, err error, fallbackReason string,
) (DockerReadiness, error) {
	if contextError := ctx.Err(); contextError != nil {
		return DockerReadiness{}, contextError
	}
	if DockerObservationErrorCode(err) == DockerObservationFailureConnection ||
		DockerObservationErrorCode(err) == DockerObservationFailureTransportUnsupported {
		readiness.DaemonReachable = false
		return finalizeDockerReadiness(readiness,
			DockerReadinessReasonDaemonUnreachable), nil
	}
	return finalizeDockerReadiness(readiness, fallbackReason), nil
}

func finalizeDockerReadiness(readiness DockerReadiness, reason string) DockerReadiness {
	status, remediation, ok := dockerReadinessDisposition(reason)
	if !ok {
		status = DockerReadinessStatusUnavailable
		reason = DockerReadinessReasonInvalidRequest
		remediation = DockerReadinessRemediationCorrectRequest
	}
	readiness.Status = status
	readiness.ReasonCode = reason
	readiness.RemediationCode = remediation
	readiness.Ready = status == DockerReadinessStatusReady
	readiness.ExpiresAt = readiness.CheckedAt.Add(DockerReadinessTTL)
	readiness.ReadinessFingerprint = dockerReadinessFingerprint(readiness)
	return readiness
}

func dockerReadinessDisposition(reason string) (string, string, bool) {
	switch reason {
	case DockerReadinessReasonNone:
		return DockerReadinessStatusReady, DockerReadinessRemediationNone, true
	case DockerReadinessReasonFeatureDisabled:
		return DockerReadinessStatusDisabled, DockerReadinessRemediationEnableFeature, true
	case DockerReadinessReasonInvalidRequest:
		return DockerReadinessStatusUnavailable, DockerReadinessRemediationCorrectRequest, true
	case DockerReadinessReasonDaemonUnreachable:
		return DockerReadinessStatusUnavailable, DockerReadinessRemediationStartDaemon, true
	case DockerReadinessReasonAPIUnsupported:
		return DockerReadinessStatusUnavailable, DockerReadinessRemediationUpgradeDaemon, true
	case DockerReadinessReasonPlatformUnsupported:
		return DockerReadinessStatusUnavailable, DockerReadinessRemediationUseLinuxContainers, true
	case DockerReadinessReasonPIDsLimitUnavailable:
		return DockerReadinessStatusUnavailable, DockerReadinessRemediationEnablePIDsLimit, true
	case DockerReadinessReasonResourceCapacityInsufficient:
		return DockerReadinessStatusUnavailable, DockerReadinessRemediationReduceResourceLimits, true
	case DockerReadinessReasonImageUnavailable:
		return DockerReadinessStatusUnavailable, DockerReadinessRemediationProvideCompatibleImage, true
	case DockerReadinessReasonManagedEgressUnavailable:
		return DockerReadinessStatusUnavailable, DockerReadinessRemediationDisableNetwork, true
	default:
		return "", "", false
	}
}

func validDockerReadinessRequestResources(readiness DockerReadiness) bool {
	if readiness.RequestedCPUQuotaMS == 0 && readiness.RequestedMemoryBytes == 0 &&
		readiness.RequestedPIDs == 0 {
		return true
	}
	return readiness.RequestedCPUQuotaMS >= 1 &&
		readiness.RequestedCPUQuotaMS <= MaxCPUQuotaMillis &&
		readiness.RequestedMemoryBytes >= MinMemoryBytes &&
		readiness.RequestedMemoryBytes <= MaxMemoryBytes &&
		readiness.RequestedPIDs >= 1 && readiness.RequestedPIDs <= MaxPIDs
}

func validDockerReadinessObservations(readiness DockerReadiness) bool {
	if readiness.APIVersion != "" && !validDockerAPIVersion(readiness.APIVersion) {
		return false
	}
	if readiness.MinAPIVersion != "" && !validDockerAPIVersion(readiness.MinAPIVersion) {
		return false
	}
	if (readiness.OSType == "") != (readiness.Architecture == "") ||
		(readiness.OSType != "" && !validDockerPlatform(readiness.OSType,
			readiness.Architecture)) || readiness.NCPU < 0 || readiness.NCPU > 1_000_000 ||
		readiness.MemoryBytes < 0 {
		return false
	}
	return true
}

func dockerReadinessFingerprint(readiness DockerReadiness) string {
	return fingerprint("sandbox_docker_readiness_fingerprint.v1",
		readiness.ProtocolVersion, readiness.Status, readiness.ReasonCode,
		readiness.RemediationCode, readiness.CheckedAt.Format(time.RFC3339Nano),
		readiness.ExpiresAt.Format(time.RFC3339Nano), readiness.EndpointClass,
		readiness.EndpointFingerprint, readiness.ImageDigest, readiness.NetworkMode,
		strconv.FormatBool(readiness.FeatureEnabled),
		strconv.FormatBool(readiness.ManagedEgressEnabled),
		strconv.FormatBool(readiness.DaemonReachable),
		strconv.FormatBool(readiness.ImageInspected),
		strconv.FormatBool(readiness.ImageProfileSafe), strconv.FormatBool(readiness.Ready),
		readiness.APIVersion, readiness.MinAPIVersion, readiness.OSType,
		readiness.Architecture, strconv.Itoa(readiness.NCPU),
		strconv.FormatInt(readiness.MemoryBytes, 10),
		strconv.FormatBool(readiness.PIDsLimitSupported),
		strconv.Itoa(readiness.RequestedCPUQuotaMS),
		strconv.FormatInt(readiness.RequestedMemoryBytes, 10),
		strconv.Itoa(readiness.RequestedPIDs))
}
