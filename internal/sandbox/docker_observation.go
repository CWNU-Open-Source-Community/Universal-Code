package sandbox

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	DockerObservationProtocolVersion = "sandbox_docker_observation.v1"
	DockerObservationSourceEngineAPI = "docker_engine_api_read_only"
	DockerObservationTrustProduction = "production_observation"

	DockerObservationStatusComplete          = "observation_complete"
	DockerObservationStatusDaemonUnavailable = "daemon_unavailable"
	DockerObservationStatusImageUnavailable  = "image_unavailable"

	DockerObservationEndpointLocalUnix  = "local_unix"
	DockerObservationEndpointLocalNPipe = "local_npipe"

	DockerObservationFailureNone                 = "none"
	DockerObservationFailureConnection           = "connection_failed"
	DockerObservationFailureTransportUnsupported = "transport_unsupported"
	DockerObservationFailureImageNotFound        = "image_not_found"
	DockerObservationFailureInvalidResponse      = "invalid_response"

	DockerObservationStateObserved              = "observed"
	DockerObservationStateUnavailable           = "unavailable"
	DockerObservationStateNotFound              = "not_found"
	DockerObservationStateNotObservableReadOnly = "not_observable_read_only"

	DockerPrivateMountNotObservable = "not_observable_read_only"
	DockerPrivateMountUnknown       = "unknown"
	DockerImageUserExplicitNonRoot  = "explicit_non_root"
	DockerImageUserRootOrEmpty      = "root_or_empty"
	DockerImageUserUnknown          = "unknown"

	MaxDockerObservationItems          = 6
	MaxDockerObservationsPerSimulation = 8
)

var dockerObservationItemNames = [...]string{
	"daemon_identity",
	"api_capabilities",
	"rootless_security",
	"private_mount_support",
	"platform_limits",
	"image_inspection",
}

type DockerObservationEndpoint struct {
	Class       string
	Fingerprint string
}

func NewDockerObservationEndpoint(class string) (DockerObservationEndpoint, error) {
	class = strings.TrimSpace(class)
	if class != DockerObservationEndpointLocalUnix &&
		class != DockerObservationEndpointLocalNPipe {
		return DockerObservationEndpoint{}, errors.New("docker observation endpoint class is invalid")
	}
	return DockerObservationEndpoint{
		Class: class,
		Fingerprint: fingerprint("sandbox_docker_readonly_endpoint.v1", class,
			"local_only=true", "arbitrary_host=false"),
	}, nil
}

func (endpoint DockerObservationEndpoint) Validate() error {
	expected, err := NewDockerObservationEndpoint(endpoint.Class)
	if err != nil || endpoint.Fingerprint != expected.Fingerprint {
		return errors.New("docker observation endpoint must remain a fixed local endpoint")
	}
	return nil
}

type DockerDaemonVersion struct {
	APIVersion    string
	MinAPIVersion string
	EngineVersion string
	GitCommit     string
	OSType        string
	Architecture  string
}

func (version DockerDaemonVersion) Validate() error {
	if !validDockerAPIVersion(version.APIVersion) ||
		!validDockerAPIVersion(version.MinAPIVersion) ||
		!validObservationText(version.EngineVersion, 128, false) ||
		!validObservationText(version.GitCommit, 128, true) ||
		!validDockerPlatform(version.OSType, version.Architecture) {
		return errors.New("docker daemon version observation is invalid")
	}
	return nil
}

type DockerDaemonInfo struct {
	ID              string
	Name            string
	DockerRootDir   string
	ServerVersion   string
	OperatingSystem string
	OSType          string
	Architecture    string
	Driver          string
	CgroupDriver    string
	CgroupVersion   string
	DefaultRuntime  string
	NCPU            int
	MemoryBytes     int64
	PidsLimit       bool
	SecurityOptions []string
}

func (info DockerDaemonInfo) Normalize() (DockerDaemonInfo, error) {
	for _, value := range []struct {
		text     string
		maxBytes int
		emptyOK  bool
	}{
		{info.ID, 256, false}, {info.Name, 256, true}, {info.DockerRootDir, 1024, true},
		{info.ServerVersion, 128, false}, {info.OperatingSystem, 256, true},
		{info.OSType, 32, false}, {info.Architecture, 64, false}, {info.Driver, 128, true},
		{info.CgroupDriver, 64, true}, {info.CgroupVersion, 32, true},
		{info.DefaultRuntime, 128, true},
	} {
		if !validObservationText(value.text, value.maxBytes, value.emptyOK) {
			return DockerDaemonInfo{}, errors.New("docker daemon info contains invalid text")
		}
	}
	if !validDockerPlatform(info.OSType, info.Architecture) || info.NCPU < 0 ||
		info.NCPU > 1_000_000 || info.MemoryBytes < 0 || len(info.SecurityOptions) > 64 {
		return DockerDaemonInfo{}, errors.New("docker daemon info is outside observation bounds")
	}
	options := make([]string, 0, len(info.SecurityOptions))
	seen := make(map[string]struct{}, len(info.SecurityOptions))
	for _, option := range info.SecurityOptions {
		option = strings.TrimSpace(option)
		if !validObservationText(option, 512, false) {
			return DockerDaemonInfo{}, errors.New("docker security option is invalid")
		}
		if _, exists := seen[option]; exists {
			continue
		}
		seen[option] = struct{}{}
		options = append(options, option)
	}
	sort.Strings(options)
	info.SecurityOptions = options
	return info, nil
}

func (info DockerDaemonInfo) Rootless() bool {
	for _, option := range info.SecurityOptions {
		lower := strings.ToLower(option)
		if lower == "name=rootless" || strings.HasPrefix(lower, "name=rootless,") {
			return true
		}
	}
	return false
}

func (info DockerDaemonInfo) UserNamespaceEnabled() bool {
	for _, option := range info.SecurityOptions {
		lower := strings.ToLower(option)
		if lower == "name=userns" || strings.HasPrefix(lower, "name=userns,") {
			return true
		}
	}
	return false
}

type DockerImageInspection struct {
	ID                    string
	RepoDigests           []string
	OSType                string
	Architecture          string
	SizeBytes             int64
	User                  string
	RootFSType            string
	GraphDriver           string
	EnvironmentCount      int
	VolumeCount           int
	ProjectActionsVersion string
	PreparationVersion    string
	PNPMVersion           string
}

func (image DockerImageInspection) Normalize(requestedDigest string) (DockerImageInspection, error) {
	if !ValidOCIImageDigest(requestedDigest) ||
		!validObservationText(image.ID, 256, false) ||
		!validDockerPlatform(image.OSType, image.Architecture) || image.SizeBytes < 0 ||
		!validObservationText(image.User, 256, true) ||
		!validObservationText(image.RootFSType, 64, true) ||
		!validObservationText(image.GraphDriver, 128, true) || len(image.RepoDigests) > 128 ||
		!validObservationText(image.PNPMVersion, 64, true) || image.EnvironmentCount < 0 || image.EnvironmentCount > 4096 ||
		image.VolumeCount < 0 || image.VolumeCount > 4096 || (image.ProjectActionsVersion != "" && image.ProjectActionsVersion != "1") || (image.PreparationVersion != "" && image.PreparationVersion != "1") {
		return DockerImageInspection{}, errors.New("docker image inspection is invalid")
	}
	digests := make([]string, 0, len(image.RepoDigests))
	seen := make(map[string]struct{}, len(image.RepoDigests))
	matched := false
	for _, value := range image.RepoDigests {
		value = strings.TrimSpace(value)
		if !validObservationText(value, 1024, false) {
			return DockerImageInspection{}, errors.New("docker RepoDigest is invalid")
		}
		if strings.HasSuffix(value, "@"+requestedDigest) {
			matched = true
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		digests = append(digests, value)
	}
	if !matched {
		return DockerImageInspection{}, errors.New("docker image inspection did not bind the requested digest")
	}
	sort.Strings(digests)
	image.RepoDigests = digests
	return image, nil
}

func (image DockerImageInspection) UserState() string {
	value := strings.ToLower(strings.TrimSpace(image.User))
	if value == "" || value == "0" || value == "root" || strings.HasPrefix(value, "0:") ||
		strings.HasPrefix(value, "root:") {
		return DockerImageUserRootOrEmpty
	}
	return DockerImageUserExplicitNonRoot
}

type DockerReadOnlyTransport interface {
	Endpoint() DockerObservationEndpoint
	Ping(ctx context.Context) error
	Version(ctx context.Context) (DockerDaemonVersion, error)
	Info(ctx context.Context) (DockerDaemonInfo, error)
	InspectImage(ctx context.Context, imageDigest string) (DockerImageInspection, error)
}

type DockerObservationError struct {
	code string
}

func (err *DockerObservationError) Error() string {
	return "docker read-only observation failed: " + err.code
}

func newDockerObservationError(code string) error {
	return &DockerObservationError{code: code}
}

func DockerObservationErrorCode(err error) string {
	var observationError *DockerObservationError
	if errors.As(err, &observationError) {
		return observationError.code
	}
	return ""
}

type UnavailableDockerReadOnlyTransport struct {
	endpoint DockerObservationEndpoint
	code     string
}

func NewUnavailableDockerReadOnlyTransport(endpointClass, code string) UnavailableDockerReadOnlyTransport {
	endpoint, err := NewDockerObservationEndpoint(endpointClass)
	if err != nil {
		endpoint, _ = NewDockerObservationEndpoint(DockerObservationEndpointLocalNPipe)
	}
	if code != DockerObservationFailureConnection &&
		code != DockerObservationFailureTransportUnsupported {
		code = DockerObservationFailureTransportUnsupported
	}
	return UnavailableDockerReadOnlyTransport{endpoint: endpoint, code: code}
}

func (transport UnavailableDockerReadOnlyTransport) Endpoint() DockerObservationEndpoint {
	return transport.endpoint
}

func (transport UnavailableDockerReadOnlyTransport) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return newDockerObservationError(transport.code)
}

func (transport UnavailableDockerReadOnlyTransport) Version(ctx context.Context) (DockerDaemonVersion, error) {
	if err := ctx.Err(); err != nil {
		return DockerDaemonVersion{}, err
	}
	return DockerDaemonVersion{}, newDockerObservationError(transport.code)
}

func (transport UnavailableDockerReadOnlyTransport) Info(ctx context.Context) (DockerDaemonInfo, error) {
	if err := ctx.Err(); err != nil {
		return DockerDaemonInfo{}, err
	}
	return DockerDaemonInfo{}, newDockerObservationError(transport.code)
}

func (transport UnavailableDockerReadOnlyTransport) InspectImage(ctx context.Context,
	_ string,
) (DockerImageInspection, error) {
	if err := ctx.Err(); err != nil {
		return DockerImageInspection{}, err
	}
	return DockerImageInspection{}, newDockerObservationError(transport.code)
}

func (transport UnavailableDockerReadOnlyTransport) ListProductionEvidenceResources(
	ctx context.Context, _ string,
) (DockerProductionEvidenceHarnessInventory, error) {
	if err := ctx.Err(); err != nil {
		return DockerProductionEvidenceHarnessInventory{}, err
	}
	return DockerProductionEvidenceHarnessInventory{}, newDockerObservationError(transport.code)
}

type DockerObservationItem struct {
	Ordinal        int
	Name           string
	State          string
	EvidenceDigest string
	Observed       bool
	Verified       bool
}

func (item DockerObservationItem) Validate() error {
	if item.Ordinal < 1 || item.Ordinal > len(dockerObservationItemNames) ||
		item.Name != dockerObservationItemNames[item.Ordinal-1] ||
		!validDigest(item.EvidenceDigest) || item.Verified {
		return errors.New("docker observation item is invalid")
	}
	switch item.State {
	case DockerObservationStateObserved:
		if !item.Observed {
			return errors.New("docker observed item must be marked observed")
		}
	case DockerObservationStateUnavailable, DockerObservationStateNotFound,
		DockerObservationStateNotObservableReadOnly:
		if item.Observed {
			return errors.New("docker non-observed item cannot be marked observed")
		}
	default:
		return errors.New("docker observation item state is invalid")
	}
	return nil
}

type DockerObservationReport struct {
	ProtocolVersion           string
	Source                    string
	TrustClass                string
	Status                    string
	EndpointClass             string
	EndpointFingerprint       string
	BindingFingerprint        string
	ImageDigest               string
	FailureCode               string
	DaemonReachable           bool
	ImageInspected            bool
	ObservationComplete       bool
	ProductionObserved        bool
	ProductionVerified        bool
	BackendAvailable          bool
	BackendEnabled            bool
	ExecutionAuthorized       bool
	ArtifactCommitAuthorized  bool
	APIVersion                string
	MinAPIVersion             string
	EngineVersion             string
	OSType                    string
	Architecture              string
	Rootless                  bool
	UserNamespaceEnabled      bool
	PrivateMountState         string
	CgroupVersion             string
	NCPU                      int
	MemoryBytes               int64
	PidsLimitSupported        bool
	ImageOSType               string
	ImageArchitecture         string
	ImageSizeBytes            int64
	ImageUserState            string
	DaemonIdentityFingerprint string
	CapabilityFingerprint     string
	ImageFingerprint          string
	ObservationFingerprint    string
	Items                     []DockerObservationItem
}

func (report DockerObservationReport) Validate() error {
	if report.ProtocolVersion != DockerObservationProtocolVersion ||
		report.Source != DockerObservationSourceEngineAPI ||
		report.TrustClass != DockerObservationTrustProduction ||
		!ValidOCIImageDigest(report.ImageDigest) || !validDigest(report.EndpointFingerprint) ||
		!validDigest(report.BindingFingerprint) || !validDigest(report.ObservationFingerprint) ||
		len(report.Items) != MaxDockerObservationItems || report.ProductionVerified ||
		report.BackendAvailable || report.BackendEnabled || report.ExecutionAuthorized ||
		report.ArtifactCommitAuthorized {
		return errors.New("docker observation must remain read-only and non-authorizing")
	}
	endpoint := DockerObservationEndpoint{Class: report.EndpointClass,
		Fingerprint: report.EndpointFingerprint}
	if err := endpoint.Validate(); err != nil {
		return err
	}
	for index, item := range report.Items {
		if item.Ordinal != index+1 {
			return errors.New("docker observation item order is invalid")
		}
		if err := item.Validate(); err != nil {
			return err
		}
	}
	switch report.Status {
	case DockerObservationStatusDaemonUnavailable:
		if report.DaemonReachable || report.ImageInspected || report.ObservationComplete ||
			report.ProductionObserved ||
			(report.FailureCode != DockerObservationFailureConnection &&
				report.FailureCode != DockerObservationFailureTransportUnsupported) ||
			report.APIVersion != "" || report.MinAPIVersion != "" || report.EngineVersion != "" ||
			report.OSType != "" || report.Architecture != "" || report.Rootless ||
			report.UserNamespaceEnabled || report.PrivateMountState != DockerPrivateMountUnknown ||
			report.CgroupVersion != "" || report.NCPU != 0 || report.MemoryBytes != 0 ||
			report.PidsLimitSupported || report.ImageOSType != "" ||
			report.ImageArchitecture != "" || report.ImageSizeBytes != 0 ||
			report.ImageUserState != DockerImageUserUnknown ||
			report.DaemonIdentityFingerprint != "" || report.CapabilityFingerprint != "" ||
			report.ImageFingerprint != "" || countObservedDockerItems(report.Items) != 0 {
			return errors.New("unavailable Docker observation contains unsupported claims")
		}
	case DockerObservationStatusImageUnavailable:
		if !report.DaemonReachable || report.ImageInspected || report.ObservationComplete ||
			report.ProductionObserved || report.FailureCode != DockerObservationFailureImageNotFound ||
			!validObservedDockerDaemon(report) || report.ImageOSType != "" ||
			report.ImageArchitecture != "" || report.ImageSizeBytes != 0 ||
			report.ImageUserState != DockerImageUserUnknown || report.ImageFingerprint != "" ||
			countObservedDockerItems(report.Items) != 4 {
			return errors.New("image-unavailable Docker observation is invalid")
		}
	case DockerObservationStatusComplete:
		if !report.DaemonReachable || !report.ImageInspected || !report.ObservationComplete ||
			!report.ProductionObserved || report.FailureCode != DockerObservationFailureNone ||
			!validObservedDockerDaemon(report) ||
			!validDockerPlatform(report.ImageOSType, report.ImageArchitecture) ||
			report.ImageSizeBytes < 0 ||
			(report.ImageUserState != DockerImageUserExplicitNonRoot &&
				report.ImageUserState != DockerImageUserRootOrEmpty) ||
			!validDigest(report.ImageFingerprint) || countObservedDockerItems(report.Items) != 5 {
			return errors.New("complete Docker observation is invalid")
		}
	default:
		return errors.New("docker observation status is invalid")
	}
	if report.ObservationFingerprint != dockerObservationFingerprint(report) {
		return errors.New("docker observation aggregate fingerprint is invalid")
	}
	return nil
}

func validObservedDockerDaemon(report DockerObservationReport) bool {
	return validDockerAPIVersion(report.APIVersion) &&
		validDockerAPIVersion(report.MinAPIVersion) &&
		validObservationText(report.EngineVersion, 128, false) &&
		validDockerPlatform(report.OSType, report.Architecture) &&
		report.PrivateMountState == DockerPrivateMountNotObservable &&
		validObservationText(report.CgroupVersion, 32, true) && report.NCPU >= 0 &&
		report.NCPU <= 1_000_000 && report.MemoryBytes >= 0 &&
		validDigest(report.DaemonIdentityFingerprint) &&
		validDigest(report.CapabilityFingerprint)
}

func countObservedDockerItems(items []DockerObservationItem) int {
	count := 0
	for _, item := range items {
		if item.Observed {
			count++
		}
	}
	return count
}

type DockerObservationProbeRequest struct {
	BindingFingerprint string
	ImageDigest        string
}

type DockerProductionObserver interface {
	Observe(ctx context.Context, request DockerObservationProbeRequest) (DockerObservationReport, error)
}

type ReadOnlyDockerProductionObserver struct {
	transport DockerReadOnlyTransport
}

func NewReadOnlyDockerProductionObserver(transport DockerReadOnlyTransport) ReadOnlyDockerProductionObserver {
	return ReadOnlyDockerProductionObserver{transport: transport}
}

func (observer ReadOnlyDockerProductionObserver) Observe(ctx context.Context,
	request DockerObservationProbeRequest,
) (DockerObservationReport, error) {
	if observer.transport == nil || !validDigest(request.BindingFingerprint) ||
		!ValidOCIImageDigest(request.ImageDigest) {
		return DockerObservationReport{}, errors.New("docker production observation request is invalid")
	}
	if err := ctx.Err(); err != nil {
		return DockerObservationReport{}, err
	}
	endpoint := observer.transport.Endpoint()
	if err := endpoint.Validate(); err != nil {
		return DockerObservationReport{}, err
	}
	base := DockerObservationReport{
		ProtocolVersion: DockerObservationProtocolVersion,
		Source:          DockerObservationSourceEngineAPI,
		TrustClass:      DockerObservationTrustProduction,
		EndpointClass:   endpoint.Class, EndpointFingerprint: endpoint.Fingerprint,
		BindingFingerprint: request.BindingFingerprint, ImageDigest: request.ImageDigest,
		FailureCode:       DockerObservationFailureNone,
		PrivateMountState: DockerPrivateMountUnknown, ImageUserState: DockerImageUserUnknown,
	}
	if err := observer.transport.Ping(ctx); err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return DockerObservationReport{}, contextError
		}
		code := DockerObservationErrorCode(err)
		if code != DockerObservationFailureConnection &&
			code != DockerObservationFailureTransportUnsupported {
			return DockerObservationReport{}, newDockerObservationError(
				DockerObservationFailureInvalidResponse)
		}
		base.Status = DockerObservationStatusDaemonUnavailable
		base.FailureCode = code
		return finalizeDockerObservation(base), nil
	}
	version, err := observer.transport.Version(ctx)
	if err != nil {
		return DockerObservationReport{}, normalizeDockerObservationFailure(ctx, err)
	}
	if err := version.Validate(); err != nil {
		return DockerObservationReport{}, newDockerObservationError(
			DockerObservationFailureInvalidResponse)
	}
	info, err := observer.transport.Info(ctx)
	if err != nil {
		return DockerObservationReport{}, normalizeDockerObservationFailure(ctx, err)
	}
	info, err = info.Normalize()
	if err != nil || info.ServerVersion != version.EngineVersion ||
		info.OSType != version.OSType || info.Architecture != version.Architecture {
		return DockerObservationReport{}, newDockerObservationError(
			DockerObservationFailureInvalidResponse)
	}
	populateDockerDaemonObservation(&base, version, info)
	image, err := observer.transport.InspectImage(ctx, request.ImageDigest)
	if err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return DockerObservationReport{}, contextError
		}
		if DockerObservationErrorCode(err) != DockerObservationFailureImageNotFound {
			return DockerObservationReport{}, normalizeDockerObservationFailure(ctx, err)
		}
		base.Status = DockerObservationStatusImageUnavailable
		base.FailureCode = DockerObservationFailureImageNotFound
		return finalizeDockerObservation(base), nil
	}
	image, err = image.Normalize(request.ImageDigest)
	if err != nil {
		return DockerObservationReport{}, newDockerObservationError(
			DockerObservationFailureInvalidResponse)
	}
	base.Status = DockerObservationStatusComplete
	base.DaemonReachable = true
	base.ImageInspected = true
	base.ObservationComplete = true
	base.ProductionObserved = true
	base.ImageOSType = image.OSType
	base.ImageArchitecture = image.Architecture
	base.ImageSizeBytes = image.SizeBytes
	base.ImageUserState = image.UserState()
	base.ImageFingerprint = dockerImageObservationFingerprint(request.ImageDigest, image)
	return finalizeDockerObservation(base), nil
}

func normalizeDockerObservationFailure(ctx context.Context, err error) error {
	if contextError := ctx.Err(); contextError != nil {
		return contextError
	}
	if DockerObservationErrorCode(err) != "" {
		return err
	}
	return newDockerObservationError(DockerObservationFailureInvalidResponse)
}

func populateDockerDaemonObservation(report *DockerObservationReport, version DockerDaemonVersion,
	info DockerDaemonInfo,
) {
	report.DaemonReachable = true
	report.APIVersion = version.APIVersion
	report.MinAPIVersion = version.MinAPIVersion
	report.EngineVersion = version.EngineVersion
	report.OSType = version.OSType
	report.Architecture = version.Architecture
	report.Rootless = info.Rootless()
	report.UserNamespaceEnabled = info.UserNamespaceEnabled()
	report.PrivateMountState = DockerPrivateMountNotObservable
	report.CgroupVersion = info.CgroupVersion
	report.NCPU = info.NCPU
	report.MemoryBytes = info.MemoryBytes
	report.PidsLimitSupported = info.PidsLimit
	report.DaemonIdentityFingerprint = fingerprint("sandbox_docker_daemon_identity.v1",
		info.ID, info.Name, info.DockerRootDir, info.OperatingSystem, version.GitCommit)
	parts := []string{"sandbox_docker_capability_observation.v1", version.APIVersion,
		version.MinAPIVersion, version.EngineVersion, version.OSType, version.Architecture,
		info.Driver, info.CgroupDriver, info.CgroupVersion, info.DefaultRuntime,
		strconv.Itoa(info.NCPU), strconv.FormatInt(info.MemoryBytes, 10),
		strconv.FormatBool(info.PidsLimit), strconv.FormatBool(report.Rootless),
		strconv.FormatBool(report.UserNamespaceEnabled), DockerPrivateMountNotObservable}
	parts = append(parts, info.SecurityOptions...)
	report.CapabilityFingerprint = fingerprint(parts...)
}

func finalizeDockerObservation(report DockerObservationReport) DockerObservationReport {
	report.Items = dockerObservationItems(report)
	report.ObservationFingerprint = dockerObservationFingerprint(report)
	return report
}

func dockerObservationItems(report DockerObservationReport) []DockerObservationItem {
	items := make([]DockerObservationItem, len(dockerObservationItemNames))
	for index, name := range dockerObservationItemNames {
		state := DockerObservationStateUnavailable
		observed := false
		subject := report.EndpointFingerprint
		if report.DaemonReachable {
			switch name {
			case "daemon_identity":
				state, observed, subject = DockerObservationStateObserved, true,
					report.DaemonIdentityFingerprint
			case "api_capabilities", "rootless_security", "platform_limits":
				state, observed, subject = DockerObservationStateObserved, true,
					report.CapabilityFingerprint
			case "private_mount_support":
				state, subject = DockerObservationStateNotObservableReadOnly,
					report.CapabilityFingerprint
			case "image_inspection":
				if report.ImageInspected {
					state, observed, subject = DockerObservationStateObserved, true,
						report.ImageFingerprint
				} else {
					state = DockerObservationStateNotFound
					subject = report.ImageDigest
				}
			}
		}
		items[index] = DockerObservationItem{
			Ordinal: index + 1, Name: name, State: state, Observed: observed, Verified: false,
			EvidenceDigest: fingerprint("sandbox_docker_observation_item.v1",
				report.BindingFingerprint, report.Status, report.FailureCode, name, state, subject),
		}
	}
	return items
}

func dockerImageObservationFingerprint(requestedDigest string,
	image DockerImageInspection,
) string {
	parts := []string{"sandbox_docker_image_observation.v1", requestedDigest, image.ID,
		image.OSType, image.Architecture, strconv.FormatInt(image.SizeBytes, 10),
		image.UserState(), image.RootFSType, image.GraphDriver, strconv.Itoa(len(image.RepoDigests))}
	parts = append(parts, image.RepoDigests...)
	return fingerprint(parts...)
}

func dockerObservationFingerprint(report DockerObservationReport) string {
	parts := []string{DockerObservationProtocolVersion, report.Source, report.TrustClass,
		report.Status, report.EndpointClass, report.EndpointFingerprint,
		report.BindingFingerprint, report.ImageDigest, report.FailureCode,
		strconv.FormatBool(report.DaemonReachable), strconv.FormatBool(report.ImageInspected),
		strconv.FormatBool(report.ObservationComplete), strconv.FormatBool(report.ProductionObserved),
		strconv.FormatBool(report.ProductionVerified), strconv.FormatBool(report.BackendAvailable),
		strconv.FormatBool(report.BackendEnabled), strconv.FormatBool(report.ExecutionAuthorized),
		strconv.FormatBool(report.ArtifactCommitAuthorized), report.APIVersion,
		report.MinAPIVersion, report.EngineVersion, report.OSType, report.Architecture,
		strconv.FormatBool(report.Rootless), strconv.FormatBool(report.UserNamespaceEnabled),
		report.PrivateMountState, report.CgroupVersion, strconv.Itoa(report.NCPU),
		strconv.FormatInt(report.MemoryBytes, 10), strconv.FormatBool(report.PidsLimitSupported),
		report.ImageOSType, report.ImageArchitecture, strconv.FormatInt(report.ImageSizeBytes, 10),
		report.ImageUserState, report.DaemonIdentityFingerprint, report.CapabilityFingerprint,
		report.ImageFingerprint, strconv.Itoa(len(report.Items))}
	for _, item := range report.Items {
		parts = append(parts, strconv.Itoa(item.Ordinal), item.Name, item.State,
			item.EvidenceDigest, strconv.FormatBool(item.Observed), strconv.FormatBool(item.Verified))
	}
	return fingerprint(parts...)
}

type DockerObservation struct {
	ID                       string
	EvidenceID               string
	OutputSimulationID       string
	PreflightID              string
	ExecutionID              string
	CandidateID              string
	PreparationID            string
	RunID                    string
	MissionID                string
	WorkspaceID              string
	ManifestFingerprint      string
	AuthorizationFingerprint string
	PolicyFingerprint        string
	MountBindingFingerprint  string
	InputArtifactDigest      string
	ThreatModelFingerprint   string
	OutputPlanFingerprint    string
	Report                   DockerObservationReport
	RequestedBy              string
	CreatedAt                time.Time
	Replayed                 bool
}

func (observation DockerObservation) Validate() error {
	for label, value := range map[string]string{
		"Docker observation id":                   observation.ID,
		"Docker observation evidence id":          observation.EvidenceID,
		"Docker observation output simulation id": observation.OutputSimulationID,
		"Docker observation preflight id":         observation.PreflightID,
		"Docker observation execution id":         observation.ExecutionID,
		"Docker observation candidate id":         observation.CandidateID,
		"Docker observation preparation id":       observation.PreparationID,
		"Docker observation Run id":               observation.RunID,
		"Docker observation Mission id":           observation.MissionID,
		"Docker observation workspace id":         observation.WorkspaceID,
		"Docker observation requester":            observation.RequestedBy,
	} {
		if err := validateStoredIdentity(label, value); err != nil {
			return err
		}
	}
	for _, value := range []string{observation.ManifestFingerprint,
		observation.AuthorizationFingerprint, observation.PolicyFingerprint,
		observation.MountBindingFingerprint, observation.InputArtifactDigest,
		observation.ThreatModelFingerprint, observation.OutputPlanFingerprint} {
		if !validDigest(value) {
			return errors.New("docker observation authority fingerprint is invalid")
		}
	}
	if observation.CreatedAt.IsZero() ||
		observation.Report.BindingFingerprint != DockerObservationBindingFingerprint(observation) {
		return errors.New("docker observation authority binding is invalid")
	}
	return observation.Report.Validate()
}

func DockerObservationBindingFingerprint(observation DockerObservation) string {
	return fingerprint("sandbox_docker_observation_binding.v1", observation.EvidenceID,
		observation.OutputSimulationID, observation.PreflightID, observation.ExecutionID,
		observation.CandidateID, observation.PreparationID, observation.RunID,
		observation.MissionID, observation.WorkspaceID, observation.ManifestFingerprint,
		observation.AuthorizationFingerprint, observation.PolicyFingerprint,
		observation.MountBindingFingerprint, observation.InputArtifactDigest,
		observation.ThreatModelFingerprint, observation.OutputPlanFingerprint,
		observation.Report.ImageDigest)
}

type DockerObservationOperation struct {
	KeyDigest          string
	RequestFingerprint string
	ObservationID      string
	EvidenceID         string
	OutputSimulationID string
	RunID              string
	RequestedBy        string
	CreatedAt          time.Time
}

func (operation DockerObservationOperation) Validate() error {
	for label, value := range map[string]string{
		"Docker observation operation observation id":       operation.ObservationID,
		"Docker observation operation evidence id":          operation.EvidenceID,
		"Docker observation operation output simulation id": operation.OutputSimulationID,
		"Docker observation operation Run id":               operation.RunID,
		"Docker observation operation requester":            operation.RequestedBy,
	} {
		if err := validateStoredIdentity(label, value); err != nil {
			return err
		}
	}
	if !validDigest(operation.KeyDigest) || !validDigest(operation.RequestFingerprint) ||
		operation.CreatedAt.IsZero() {
		return errors.New("docker observation operation is invalid")
	}
	return nil
}

func DockerObservationRequestFingerprint(observation DockerObservation) string {
	return fingerprint("sandbox_docker_observation_request.v1", observation.EvidenceID,
		observation.OutputSimulationID, DockerObservationBindingFingerprint(observation),
		observation.Report.ImageDigest, observation.RequestedBy)
}

func validDockerAPIVersion(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if part == "" || len(part) > 4 {
			return false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return false
			}
		}
		if _, err := strconv.Atoi(part); err != nil {
			return false
		}
	}
	return true
}

func validDockerPlatform(osType, architecture string) bool {
	return validObservationText(osType, 32, false) &&
		validObservationText(architecture, 64, false)
}

func validObservationText(value string, maxBytes int, emptyOK bool) bool {
	if value != strings.TrimSpace(value) || !utf8.ValidString(value) ||
		len([]byte(value)) > maxBytes || (!emptyOK && value == "") {
		return false
	}
	return strings.IndexFunc(value, unicode.IsControl) < 0
}

func DockerObservationSummary(report DockerObservationReport) string {
	return fmt.Sprintf("status=%s daemon_reachable=%t image_inspected=%t observed=%d verified=0",
		report.Status, report.DaemonReachable, report.ImageInspected,
		countObservedDockerItems(report.Items))
}
