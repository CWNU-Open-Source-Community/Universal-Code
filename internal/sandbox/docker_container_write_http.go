package sandbox

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	DockerContainerWriteFailureDisabled        = "transport_disabled"
	DockerContainerWriteFailureUnsupported     = "transport_unsupported"
	DockerContainerWriteFailureConnection      = "connection_failed"
	DockerContainerWriteFailureInvalidResponse = "invalid_response"
	DockerContainerWriteFailureUnsafeExisting  = "unsafe_existing_container"
	DockerContainerWriteFailureUnsafeImage     = "unsafe_image_profile"
	DockerContainerWriteFailureCreateConflict  = "create_conflict"
	DockerContainerWriteFailureConfigMismatch  = "configuration_mismatch"
	DockerContainerWriteFailureCleanup         = "cleanup_failed"
	maxDockerContainerWriteResponseBytes       = 2 * 1024 * 1024
	maxDockerContainerWriteRequestBytes        = 256 * 1024
	dockerContainerWriteCleanupTimeout         = 5 * time.Second
)

type DockerContainerWriteError struct {
	code string
}

func (err *DockerContainerWriteError) Error() string {
	return "docker container write rehearsal failed: " + err.code
}

func newDockerContainerWriteError(code string) error {
	return &DockerContainerWriteError{code: code}
}

func DockerContainerWriteErrorCode(err error) string {
	var writeError *DockerContainerWriteError
	if errors.As(err, &writeError) {
		return writeError.code
	}
	return ""
}

type dockerContainerWriteHTTPDoer interface {
	Do(request *http.Request) (*http.Response, error)
}

type dockerEngineContainerWriteTransport struct {
	doer     dockerContainerWriteHTTPDoer
	endpoint DockerObservationEndpoint
}

func newDockerEngineContainerWriteTransport(doer dockerContainerWriteHTTPDoer,
	endpoint DockerObservationEndpoint,
) (dockerEngineContainerWriteTransport, error) {
	if doer == nil {
		return dockerEngineContainerWriteTransport{}, errors.New("docker write HTTP client is required")
	}
	if !validDockerContainerLocalEndpoint(endpoint) {
		return dockerEngineContainerWriteTransport{}, errors.New("docker write transport requires a fixed local endpoint")
	}
	return dockerEngineContainerWriteTransport{doer: doer, endpoint: endpoint}, nil
}

func validDockerContainerLocalEndpoint(endpoint DockerObservationEndpoint) bool {
	return endpoint.Validate() == nil &&
		(endpoint.Class == DockerObservationEndpointLocalUnix ||
			endpoint.Class == DockerObservationEndpointLocalNPipe)
}

func (transport dockerEngineContainerWriteTransport) Endpoint() DockerObservationEndpoint {
	return transport.endpoint
}

func (transport dockerEngineContainerWriteTransport) Rehearse(ctx context.Context,
	request DockerContainerWriteRequest,
) (result DockerContainerWriteResult, returnedErr error) {
	if err := ctx.Err(); err != nil {
		return DockerContainerWriteResult{}, err
	}
	if err := request.Validate(); err != nil {
		return DockerContainerWriteResult{}, err
	}
	if transport.doer == nil || transport.endpoint.Class != DockerObservationEndpointLocalUnix {
		return DockerContainerWriteResult{}, newDockerContainerWriteError(DockerContainerWriteFailureUnsupported)
	}
	if err := transport.verifyImageProfile(ctx, request.Spec.ImageDigest); err != nil {
		return DockerContainerWriteResult{}, err
	}

	reconciled := 0
	existing, found, err := transport.inspect(ctx, request.Spec.ContainerName)
	if err != nil {
		return DockerContainerWriteResult{}, err
	}
	if found {
		if verifyDockerContainerInspection(existing, request) != nil || existing.State.Running ||
			existing.State.Pid != 0 || existing.State.Status != "created" {
			return DockerContainerWriteResult{}, newDockerContainerWriteError(DockerContainerWriteFailureUnsafeExisting)
		}
		if err := transport.remove(ctx, existing.ID, false); err != nil {
			return DockerContainerWriteResult{}, err
		}
		reconciled = 1
	}

	containerID, err := transport.create(ctx, request)
	if err != nil {
		if cleanupErr := transport.cleanupExactContainer(request, request.Spec.ContainerName); cleanupErr != nil {
			return DockerContainerWriteResult{}, errors.Join(err,
				newDockerContainerWriteError(DockerContainerWriteFailureCleanup))
		}
		return DockerContainerWriteResult{}, err
	}
	cleanupPending := true
	defer func() {
		if !cleanupPending {
			return
		}
		if cleanupErr := transport.cleanupExactContainer(request, containerID,
			request.Spec.ContainerName); cleanupErr != nil {
			returnedErr = errors.Join(returnedErr,
				newDockerContainerWriteError(DockerContainerWriteFailureCleanup))
		}
	}()

	inspection, found, err := transport.inspect(ctx, containerID)
	if err != nil {
		return DockerContainerWriteResult{}, err
	}
	if !found || verifyDockerContainerInspection(inspection, request) != nil ||
		inspection.ID != containerID {
		return DockerContainerWriteResult{}, newDockerContainerWriteError(DockerContainerWriteFailureConfigMismatch)
	}
	if err := ctx.Err(); err != nil {
		return DockerContainerWriteResult{}, err
	}
	if err := transport.remove(ctx, containerID, false); err != nil {
		return DockerContainerWriteResult{}, err
	}
	cleanupPending = false

	return NewDockerContainerWriteResult(transport.endpoint, request, containerID, reconciled)
}

func (transport dockerEngineContainerWriteTransport) Stage(ctx context.Context,
	request DockerContainerWriteRequest,
) (DockerContainerStageResult, error) {
	if err := ctx.Err(); err != nil {
		return DockerContainerStageResult{}, err
	}
	if err := request.Validate(); err != nil {
		return DockerContainerStageResult{}, err
	}
	if transport.doer == nil || !validDockerContainerLocalEndpoint(transport.endpoint) {
		return DockerContainerStageResult{}, newDockerContainerWriteError(
			DockerContainerWriteFailureUnsupported)
	}
	if err := transport.verifyImageProfile(ctx, request.Spec.ImageDigest); err != nil {
		return DockerContainerStageResult{}, err
	}

	inspection, found, err := transport.inspect(ctx, request.Spec.ContainerName)
	if err != nil {
		return DockerContainerStageResult{}, err
	}
	containerID, adopted := "", found
	if found {
		if verifyDockerContainerInspection(inspection, request) != nil {
			return DockerContainerStageResult{}, newDockerContainerWriteError(
				DockerContainerWriteFailureUnsafeExisting)
		}
		containerID = inspection.ID
	} else {
		containerID, err = transport.create(ctx, request)
		if err != nil {
			// A timed-out create may still have reached the daemon. The durable attempt owns
			// the deterministic name, so a later generation must inspect and adopt it.
			return DockerContainerStageResult{}, err
		}
	}

	inspection, found, err = transport.inspect(ctx, containerID)
	if err != nil {
		return DockerContainerStageResult{}, err
	}
	if !found || inspection.ID != containerID ||
		verifyDockerContainerInspection(inspection, request) != nil {
		return DockerContainerStageResult{}, newDockerContainerWriteError(
			DockerContainerWriteFailureConfigMismatch)
	}
	return NewDockerContainerStageResult(transport.endpoint, request, containerID, adopted)
}

func (transport dockerEngineContainerWriteTransport) Cleanup(ctx context.Context,
	request DockerContainerWriteRequest, stage DockerContainerStageResult,
) (DockerContainerCleanupResult, error) {
	if err := ctx.Err(); err != nil {
		return DockerContainerCleanupResult{}, err
	}
	if request.Validate() != nil || stage.Validate() != nil ||
		stage.RequestFingerprint != request.RequestFingerprint ||
		stage.SpecFingerprint != request.Spec.SpecFingerprint ||
		stage.EndpointFingerprint != transport.endpoint.Fingerprint {
		return DockerContainerCleanupResult{}, newDockerContainerWriteError(
			DockerContainerWriteFailureConfigMismatch)
	}
	inspection, found, err := transport.inspect(ctx, request.Spec.ContainerName)
	if err != nil {
		return DockerContainerCleanupResult{}, err
	}
	if !found {
		return NewDockerContainerCleanupResult(transport.endpoint, request, stage, false)
	}
	if verifyDockerContainerInspection(inspection, request) != nil ||
		fingerprint("sandbox_docker_container_id.v1", inspection.ID) !=
			stage.ContainerIDFingerprint {
		return DockerContainerCleanupResult{}, newDockerContainerWriteError(
			DockerContainerWriteFailureUnsafeExisting)
	}
	if err := transport.remove(ctx, inspection.ID, false); err != nil {
		return DockerContainerCleanupResult{}, err
	}
	return NewDockerContainerCleanupResult(transport.endpoint, request, stage, true)
}

func NewDockerContainerWriteResult(endpoint DockerObservationEndpoint,
	request DockerContainerWriteRequest, containerID string, reconciled int,
) (DockerContainerWriteResult, error) {
	if err := endpoint.Validate(); err != nil || endpoint.Class != DockerObservationEndpointLocalUnix ||
		request.Validate() != nil || !validDockerContainerID(containerID) ||
		reconciled < 0 || reconciled > 1 {
		return DockerContainerWriteResult{}, errors.New("docker container write result input is invalid")
	}
	steps := []DockerContainerWriteStep{
		newDockerContainerWriteStep(request.RequestFingerprint, 1, 1, 0),
		newDockerContainerWriteStep(request.RequestFingerprint, 2, 1, reconciled),
		newDockerContainerWriteStep(request.RequestFingerprint, 3, 0, 1),
		newDockerContainerWriteStep(request.RequestFingerprint, 4, 1, 0),
		newDockerContainerWriteStep(request.RequestFingerprint, 5, 0, 1),
	}
	result := DockerContainerWriteResult{
		ProtocolVersion: DockerContainerWriteProtocolVersion,
		Source:          DockerContainerRehearsalSourceLocal, Status: DockerContainerWriteStatusComplete,
		EndpointClass: endpoint.Class, EndpointFingerprint: endpoint.Fingerprint,
		RequestFingerprint: request.RequestFingerprint, SpecFingerprint: request.Spec.SpecFingerprint,
		ContainerIDFingerprint: fingerprint("sandbox_docker_container_id.v1", containerID),
		InspectionFingerprint: fingerprint("sandbox_docker_container_inspection.v1", containerID,
			request.Spec.SpecFingerprint, request.MountFingerprint),
		StepCount: len(steps), DaemonReadCount: 3, DaemonWriteCount: 2 + reconciled,
		ReconciledContainerCount: reconciled, ConfigurationMatched: true,
		ContainerCreated: true, ContainerInspected: true, ContainerRemoved: true,
		CleanupConfirmed: true, DaemonReachable: true, DaemonWriteSubmitted: true,
		Steps: steps,
	}
	result.TransportFingerprint = dockerContainerWriteResultFingerprint(result)
	if err := result.Validate(); err != nil {
		return DockerContainerWriteResult{}, err
	}
	return result, nil
}

func NewDockerContainerWriteResultFromRecovery(endpoint DockerObservationEndpoint,
	request DockerContainerWriteRequest, stage DockerContainerStageResult,
	cleanup DockerContainerCleanupResult,
) (DockerContainerWriteResult, error) {
	if endpoint.Validate() != nil || endpoint.Class != DockerObservationEndpointLocalUnix ||
		request.Validate() != nil || stage.Validate() != nil || cleanup.Validate() != nil ||
		stage.EndpointFingerprint != endpoint.Fingerprint ||
		stage.RequestFingerprint != request.RequestFingerprint ||
		stage.SpecFingerprint != request.Spec.SpecFingerprint ||
		cleanup.EndpointFingerprint != endpoint.Fingerprint ||
		cleanup.RequestFingerprint != request.RequestFingerprint ||
		cleanup.ContainerIDFingerprint != stage.ContainerIDFingerprint {
		return DockerContainerWriteResult{}, errors.New(
			"docker container recovery result input is invalid")
	}
	steps := []DockerContainerWriteStep{
		newDockerContainerWriteStep(request.RequestFingerprint, 1, 1, 0),
		newDockerContainerWriteStep(request.RequestFingerprint, 2, 1, 0),
		newDockerContainerWriteStep(request.RequestFingerprint, 3, 0, 1),
		newDockerContainerWriteStep(request.RequestFingerprint, 4, 1, 0),
		newDockerContainerWriteStep(request.RequestFingerprint, 5, 0, 1),
	}
	result := DockerContainerWriteResult{
		ProtocolVersion: DockerContainerWriteProtocolVersion,
		Source:          DockerContainerRehearsalSourceLocal, Status: DockerContainerWriteStatusComplete,
		EndpointClass: endpoint.Class, EndpointFingerprint: endpoint.Fingerprint,
		RequestFingerprint:     request.RequestFingerprint,
		SpecFingerprint:        request.Spec.SpecFingerprint,
		ContainerIDFingerprint: stage.ContainerIDFingerprint,
		InspectionFingerprint:  stage.InspectionFingerprint, StepCount: len(steps),
		DaemonReadCount: 3, DaemonWriteCount: 2, ConfigurationMatched: true,
		ContainerCreated: true, ContainerInspected: true, ContainerRemoved: true,
		CleanupConfirmed: true, DaemonReachable: true, DaemonWriteSubmitted: true,
		Steps: steps,
	}
	result.TransportFingerprint = dockerContainerWriteResultFingerprint(result)
	if err := result.Validate(); err != nil {
		return DockerContainerWriteResult{}, err
	}
	return result, nil
}

func newDockerContainerWriteStep(requestFingerprint string, ordinal, reads, writes int,
) DockerContainerWriteStep {
	step := DockerContainerWriteStep{Ordinal: ordinal,
		Name: dockerContainerWriteStepNames[ordinal-1], State: DockerContainerWriteStepStateComplete,
		DaemonReads: reads, DaemonWrites: writes, ProductionApplied: writes == 1}
	step.StepDigest = fingerprint("sandbox_docker_write_transport_step.v1",
		requestFingerprint, strconv.Itoa(step.Ordinal), step.Name, step.State,
		strconv.Itoa(step.DaemonReads), strconv.Itoa(step.DaemonWrites),
		strconv.FormatBool(step.ProductionApplied))
	return step
}

type dockerCreateContainerPayload struct {
	Image            string                       `json:"Image"`
	Entrypoint       []string                     `json:"Entrypoint"`
	Cmd              []string                     `json:"Cmd"`
	Env              []string                     `json:"Env"`
	WorkingDir       string                       `json:"WorkingDir"`
	User             string                       `json:"User"`
	NetworkDisabled  bool                         `json:"NetworkDisabled"`
	AttachStdin      bool                         `json:"AttachStdin"`
	AttachStdout     bool                         `json:"AttachStdout"`
	AttachStderr     bool                         `json:"AttachStderr"`
	OpenStdin        bool                         `json:"OpenStdin"`
	StdinOnce        bool                         `json:"StdinOnce"`
	Tty              bool                         `json:"Tty"`
	StopSignal       string                       `json:"StopSignal"`
	Labels           map[string]string            `json:"Labels"`
	ExposedPorts     map[string]json.RawMessage   `json:"ExposedPorts"`
	HostConfig       dockerCreateHostConfig       `json:"HostConfig"`
	NetworkingConfig dockerCreateNetworkingConfig `json:"NetworkingConfig"`
}

type dockerCreateHostConfig struct {
	ReadonlyRootfs  bool                `json:"ReadonlyRootfs"`
	SecurityOpt     []string            `json:"SecurityOpt"`
	CapDrop         []string            `json:"CapDrop"`
	Init            *bool               `json:"Init"`
	NetworkMode     string              `json:"NetworkMode"`
	NanoCPUs        int64               `json:"NanoCpus"`
	Memory          int64               `json:"Memory"`
	MemorySwap      int64               `json:"MemorySwap"`
	PidsLimit       int64               `json:"PidsLimit"`
	Privileged      bool                `json:"Privileged"`
	AutoRemove      bool                `json:"AutoRemove"`
	RestartPolicy   dockerRestartPolicy `json:"RestartPolicy"`
	LogConfig       dockerLogConfig     `json:"LogConfig"`
	Mounts          []dockerCreateMount `json:"Mounts"`
	Tmpfs           map[string]string   `json:"Tmpfs"`
	DNS             []string            `json:"Dns"`
	DNSOptions      []string            `json:"DnsOptions"`
	DNSSearch       []string            `json:"DnsSearch"`
	ExtraHosts      []string            `json:"ExtraHosts"`
	Links           []string            `json:"Links"`
	PortBindings    map[string]any      `json:"PortBindings"`
	PublishAllPorts bool                `json:"PublishAllPorts"`
}

type dockerRestartPolicy struct {
	Name              string `json:"Name"`
	MaximumRetryCount int    `json:"MaximumRetryCount"`
}

type dockerLogConfig struct {
	Type   string            `json:"Type"`
	Config map[string]string `json:"Config"`
}

type dockerCreateMount struct {
	Type          string                     `json:"Type"`
	Source        string                     `json:"Source"`
	Target        string                     `json:"Target"`
	ReadOnly      bool                       `json:"ReadOnly"`
	BindOptions   *dockerCreateBindOptions   `json:"BindOptions,omitempty"`
	VolumeOptions *dockerCreateVolumeOptions `json:"VolumeOptions,omitempty"`
}

type dockerCreateBindOptions struct {
	Propagation string `json:"Propagation"`
}

type dockerCreateVolumeOptions struct {
	NoCopy bool `json:"NoCopy"`
}

type dockerCreateNetworkingConfig struct {
	EndpointsConfig map[string]json.RawMessage `json:"EndpointsConfig"`
}

type dockerContainerWriteImageInspection struct {
	ID          string   `json:"Id"`
	RepoDigests []string `json:"RepoDigests"`
	Config      struct {
		Volumes map[string]json.RawMessage `json:"Volumes"`
		Env     []string                   `json:"Env"`
	} `json:"Config"`
}

func dockerCreatePayload(request DockerContainerWriteRequest) dockerCreateContainerPayload {
	return dockerCreatePayloadWithLabels(request, request.Spec.Labels)
}

func dockerCreatePayloadWithLabels(request DockerContainerWriteRequest,
	expectedLabels []DockerContainerLabel,
) dockerCreateContainerPayload {
	spec := request.Spec
	labels := make(map[string]string, len(expectedLabels))
	for _, label := range expectedLabels {
		labels[label.Name] = label.Value
	}
	mounts := make([]dockerCreateMount, len(request.HostMounts))
	for index, mount := range request.HostMounts {
		mounts[index] = dockerCreateMount{Type: "bind", Source: mount.Source,
			Target: mount.Target, ReadOnly: mount.ReadOnly,
			BindOptions: &dockerCreateBindOptions{Propagation: mount.Propagation}}
	}
	initEnabled := true
	tmpfs := dockerExpectedContainerTmpfs(spec)
	return dockerCreateContainerPayload{
		Image: spec.ImageDigest, Entrypoint: []string{spec.Executable},
		Cmd: append([]string(nil), spec.Arguments...), Env: []string{},
		WorkingDir: spec.WorkingDirectory,
		User:       spec.User, NetworkDisabled: true, StopSignal: DockerTerminationSignalGraceful,
		AttachStdin: spec.StdinPipe, OpenStdin: spec.StdinPipe,
		StdinOnce: spec.StdinPipe,
		Labels:    labels, ExposedPorts: map[string]json.RawMessage{},
		HostConfig: dockerCreateHostConfig{
			ReadonlyRootfs: true, SecurityOpt: []string{"no-new-privileges"},
			CapDrop: []string{"ALL"}, Init: &initEnabled, NetworkMode: DockerNetworkDriverNone,
			NanoCPUs: spec.Resources.NanoCPUs, Memory: spec.Resources.MemoryBytes,
			MemorySwap: spec.Resources.MemoryBytes, PidsLimit: int64(spec.Resources.PIDs),
			RestartPolicy: dockerRestartPolicy{Name: "no"},
			LogConfig: dockerLogConfig{Type: "local", Config: map[string]string{
				"max-size": "256k", "max-file": "1", "compress": "false",
			}},
			Mounts: mounts, Tmpfs: tmpfs, DNS: []string{}, DNSOptions: []string{},
			DNSSearch: []string{}, ExtraHosts: []string{}, Links: []string{},
			PortBindings: map[string]any{},
		},
		NetworkingConfig: dockerCreateNetworkingConfig{
			EndpointsConfig: map[string]json.RawMessage{},
		},
	}
}

func (transport dockerEngineContainerWriteTransport) verifyImageProfile(ctx context.Context,
	imageDigest string,
) error {
	path := "/v" + DockerContainerWriteAPIVersion + "/images/" +
		url.PathEscape(imageDigest) + "/json"
	response, err := transport.do(ctx, http.MethodGet, path, "", nil, true)
	if err != nil {
		return err
	}
	if response.status == http.StatusNotFound {
		return newDockerContainerWriteError(DockerContainerWriteFailureUnsafeImage)
	}
	var inspection dockerContainerWriteImageInspection
	if err := decodeDockerContainerWriteJSON(response.body, &inspection); err != nil {
		return err
	}
	if !ValidOCIImageDigest(inspection.ID) || len(inspection.RepoDigests) == 0 ||
		len(inspection.RepoDigests) > 128 || len(inspection.Config.Volumes) != 0 ||
		len(inspection.Config.Env) != 0 {
		return newDockerContainerWriteError(DockerContainerWriteFailureUnsafeImage)
	}
	matched := false
	for _, repoDigest := range inspection.RepoDigests {
		if len(repoDigest) == 0 || len(repoDigest) > 1024 ||
			strings.TrimSpace(repoDigest) != repoDigest {
			return newDockerContainerWriteError(DockerContainerWriteFailureUnsafeImage)
		}
		if strings.HasSuffix(repoDigest, "@"+imageDigest) {
			matched = true
		}
	}
	if !matched {
		return newDockerContainerWriteError(DockerContainerWriteFailureUnsafeImage)
	}
	return nil
}

type dockerContainerInspection struct {
	ID      string `json:"Id"`
	Name    string `json:"Name"`
	Created string `json:"Created"`
	Config  struct {
		Image           string            `json:"Image"`
		Entrypoint      []string          `json:"Entrypoint"`
		Cmd             []string          `json:"Cmd"`
		Env             []string          `json:"Env"`
		WorkingDir      string            `json:"WorkingDir"`
		User            string            `json:"User"`
		NetworkDisabled bool              `json:"NetworkDisabled"`
		AttachStdin     bool              `json:"AttachStdin"`
		AttachStdout    bool              `json:"AttachStdout"`
		AttachStderr    bool              `json:"AttachStderr"`
		OpenStdin       bool              `json:"OpenStdin"`
		StdinOnce       bool              `json:"StdinOnce"`
		Tty             bool              `json:"Tty"`
		Labels          map[string]string `json:"Labels"`
		ExposedPorts    map[string]any    `json:"ExposedPorts"`
		StopSignal      string            `json:"StopSignal"`
	} `json:"Config"`
	State struct {
		Status     string `json:"Status"`
		Running    bool   `json:"Running"`
		Paused     bool   `json:"Paused"`
		Restarting bool   `json:"Restarting"`
		OOMKilled  bool   `json:"OOMKilled"`
		Dead       bool   `json:"Dead"`
		Pid        int    `json:"Pid"`
		ExitCode   int    `json:"ExitCode"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
	} `json:"State"`
	HostConfig struct {
		ReadonlyRootfs  bool                `json:"ReadonlyRootfs"`
		SecurityOpt     []string            `json:"SecurityOpt"`
		CapAdd          []string            `json:"CapAdd"`
		CapDrop         []string            `json:"CapDrop"`
		Binds           []string            `json:"Binds"`
		Devices         []json.RawMessage   `json:"Devices"`
		DeviceRequests  []json.RawMessage   `json:"DeviceRequests"`
		PortBindings    map[string]any      `json:"PortBindings"`
		PublishAllPorts bool                `json:"PublishAllPorts"`
		DNS             []string            `json:"Dns"`
		DNSOptions      []string            `json:"DnsOptions"`
		DNSSearch       []string            `json:"DnsSearch"`
		ExtraHosts      []string            `json:"ExtraHosts"`
		Links           []string            `json:"Links"`
		Init            *bool               `json:"Init"`
		NetworkMode     string              `json:"NetworkMode"`
		NanoCPUs        int64               `json:"NanoCpus"`
		Memory          int64               `json:"Memory"`
		MemorySwap      int64               `json:"MemorySwap"`
		PidsLimit       int64               `json:"PidsLimit"`
		Privileged      bool                `json:"Privileged"`
		AutoRemove      bool                `json:"AutoRemove"`
		RestartPolicy   dockerRestartPolicy `json:"RestartPolicy"`
		LogConfig       dockerLogConfig     `json:"LogConfig"`
		Mounts          []dockerCreateMount `json:"Mounts"`
		Tmpfs           map[string]string   `json:"Tmpfs"`
	} `json:"HostConfig"`
	Mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		Driver      string `json:"Driver"`
		Mode        string `json:"Mode"`
		RW          bool   `json:"RW"`
		Propagation string `json:"Propagation"`
	} `json:"Mounts"`
	NetworkSettings struct {
		Ports    map[string]any `json:"Ports"`
		Networks map[string]struct {
			IPAddress         string   `json:"IPAddress"`
			GlobalIPv6Address string   `json:"GlobalIPv6Address"`
			Gateway           string   `json:"Gateway"`
			IPv6Gateway       string   `json:"IPv6Gateway"`
			MacAddress        string   `json:"MacAddress"`
			Aliases           []string `json:"Aliases"`
			DNSNames          []string `json:"DNSNames"`
			Links             []string `json:"Links"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

func verifyDockerContainerInspection(inspection dockerContainerInspection,
	request DockerContainerWriteRequest,
) error {
	return verifyDockerContainerInspectionWithLabels(inspection, request, request.Spec.Labels)
}

func verifyDockerContainerInspectionWithLabels(inspection dockerContainerInspection,
	request DockerContainerWriteRequest, expectedLabels []DockerContainerLabel,
) error {
	if verifyDockerContainerConfigurationWithLabels(inspection, request, expectedLabels) != nil ||
		inspection.State.Status != "created" || inspection.State.Running ||
		inspection.State.Paused || inspection.State.Restarting ||
		inspection.State.OOMKilled || inspection.State.Dead || inspection.State.Pid != 0 {
		return newDockerContainerWriteError(DockerContainerWriteFailureConfigMismatch)
	}
	return nil
}

func verifyDockerContainerConfiguration(inspection dockerContainerInspection,
	request DockerContainerWriteRequest,
) error {
	return verifyDockerContainerConfigurationWithLabels(inspection, request, request.Spec.Labels)
}

func verifyDockerContainerConfigurationWithLabels(inspection dockerContainerInspection,
	request DockerContainerWriteRequest, expectedLabels []DockerContainerLabel,
) error {
	spec := request.Spec
	if !validDockerContainerID(inspection.ID) || inspection.Name != "/"+spec.ContainerName ||
		inspection.Config.Image != spec.ImageDigest || inspection.Config.User != spec.User ||
		inspection.Config.WorkingDir != spec.WorkingDirectory ||
		!inspection.Config.NetworkDisabled || inspection.Config.StopSignal != DockerTerminationSignalGraceful ||
		inspection.Config.AttachStdin != spec.StdinPipe || inspection.Config.AttachStdout ||
		inspection.Config.AttachStderr || inspection.Config.OpenStdin != spec.StdinPipe ||
		inspection.Config.StdinOnce != spec.StdinPipe || inspection.Config.Tty ||
		len(inspection.Config.Env) != 0 || len(inspection.Config.ExposedPorts) != 0 ||
		!equalStrings(inspection.Config.Entrypoint, []string{spec.Executable}) ||
		!equalStrings(inspection.Config.Cmd, spec.Arguments) ||
		!dockerContainerProfileLabelsMatch(inspection.Config.Labels, spec, expectedLabels) ||
		!inspection.HostConfig.ReadonlyRootfs ||
		inspection.HostConfig.Privileged || inspection.HostConfig.AutoRemove ||
		inspection.HostConfig.Init == nil || !*inspection.HostConfig.Init ||
		inspection.HostConfig.NetworkMode != DockerNetworkDriverNone ||
		inspection.HostConfig.NanoCPUs != spec.Resources.NanoCPUs ||
		inspection.HostConfig.Memory != spec.Resources.MemoryBytes ||
		inspection.HostConfig.MemorySwap != spec.Resources.MemoryBytes ||
		inspection.HostConfig.PidsLimit != int64(spec.Resources.PIDs) ||
		inspection.HostConfig.RestartPolicy.Name != "no" ||
		inspection.HostConfig.RestartPolicy.MaximumRetryCount != 0 ||
		inspection.HostConfig.LogConfig.Type != "local" ||
		!equalStringMap(inspection.HostConfig.LogConfig.Config, map[string]string{
			"max-size": "256k", "max-file": "1", "compress": "false",
		}) ||
		len(inspection.HostConfig.SecurityOpt) != 1 ||
		!containsFold(inspection.HostConfig.SecurityOpt, "no-new-privileges") ||
		len(inspection.HostConfig.CapAdd) != 0 || len(inspection.HostConfig.CapDrop) != 1 ||
		!containsFold(inspection.HostConfig.CapDrop, "ALL") ||
		!equalStringMap(inspection.HostConfig.Tmpfs, dockerExpectedContainerTmpfs(spec)) ||
		len(inspection.HostConfig.Binds) != 0 || len(inspection.HostConfig.Devices) != 0 ||
		len(inspection.HostConfig.DeviceRequests) != 0 ||
		len(inspection.HostConfig.PortBindings) != 0 || inspection.HostConfig.PublishAllPorts ||
		len(inspection.HostConfig.DNS) != 0 || len(inspection.HostConfig.DNSOptions) != 0 ||
		len(inspection.HostConfig.DNSSearch) != 0 || len(inspection.HostConfig.ExtraHosts) != 0 ||
		len(inspection.HostConfig.Links) != 0 || len(inspection.NetworkSettings.Ports) != 0 ||
		!dockerNetworkSettingsAreNone(inspection.NetworkSettings.Networks) ||
		len(inspection.Mounts) != len(request.HostMounts) {
		return newDockerContainerWriteError(DockerContainerWriteFailureConfigMismatch)
	}
	byTarget := make(map[string]DockerHostMount, len(request.HostMounts))
	for _, mount := range request.HostMounts {
		byTarget[mount.Target] = mount
	}
	for _, observed := range inspection.Mounts {
		expected, ok := byTarget[observed.Destination]
		if !ok || observed.Type != "bind" || observed.Source != expected.Source ||
			observed.RW == expected.ReadOnly || observed.Propagation != expected.Propagation {
			return newDockerContainerWriteError(DockerContainerWriteFailureConfigMismatch)
		}
		delete(byTarget, observed.Destination)
	}
	if len(byTarget) != 0 {
		return newDockerContainerWriteError(DockerContainerWriteFailureConfigMismatch)
	}
	return nil
}

func dockerExpectedContainerTmpfs(spec DockerContainerSpec) map[string]string {
	value := map[string]string{}
	if dockerContainerSpecUsesStandardCodeWorkspace(spec) {
		options := DockerStandardCodeCacheTmpfsOptions
		toolchainIndex := 17
		if spec.Arguments[0] == dockerStandardCodeRunnerLegacyProtocol {
			toolchainIndex--
		}
		// The complete fixed binding was validated above. Go needs enough
		// bounded private scratch for both compiled standard-library archives
		// and its build cache; this does not enlarge the host projection.
		if toolchain := spec.Arguments[toolchainIndex]; toolchain == DockerStandardCodeToolchainGo || toolchain == DockerStandardCodeToolchainGoPrepare {
			options = DockerStandardCodeGoCacheTmpfsOptions
		}
		value[DockerStandardCodeCacheTarget] = options
	}
	return value
}

func dockerNetworkSettingsAreNone(networks map[string]struct {
	IPAddress         string   `json:"IPAddress"`
	GlobalIPv6Address string   `json:"GlobalIPv6Address"`
	Gateway           string   `json:"Gateway"`
	IPv6Gateway       string   `json:"IPv6Gateway"`
	MacAddress        string   `json:"MacAddress"`
	Aliases           []string `json:"Aliases"`
	DNSNames          []string `json:"DNSNames"`
	Links             []string `json:"Links"`
}) bool {
	if len(networks) > 1 {
		return false
	}
	for name, network := range networks {
		if name != DockerNetworkDriverNone || network.IPAddress != "" ||
			network.GlobalIPv6Address != "" || network.Gateway != "" ||
			network.IPv6Gateway != "" || network.MacAddress != "" ||
			len(network.Aliases) != 0 || len(network.DNSNames) != 0 ||
			len(network.Links) != 0 {
			return false
		}
	}
	return true
}

func (transport dockerEngineContainerWriteTransport) inspect(ctx context.Context,
	reference string,
) (dockerContainerInspection, bool, error) {
	path := "/v" + DockerContainerWriteAPIVersion + "/containers/" +
		url.PathEscape(reference) + "/json"
	response, err := transport.do(ctx, http.MethodGet, path, "", nil, true)
	if err != nil {
		return dockerContainerInspection{}, false, err
	}
	if response.status == http.StatusNotFound {
		return dockerContainerInspection{}, false, nil
	}
	var inspection dockerContainerInspection
	if err := decodeDockerContainerWriteJSON(response.body, &inspection); err != nil {
		return dockerContainerInspection{}, false, err
	}
	return inspection, true, nil
}

func (transport dockerEngineContainerWriteTransport) create(ctx context.Context,
	request DockerContainerWriteRequest,
) (string, error) {
	return transport.createWithLabels(ctx, request, request.Spec.Labels, nil)
}

func (transport dockerEngineContainerWriteTransport) createWithLabels(ctx context.Context,
	request DockerContainerWriteRequest, expectedLabels []DockerContainerLabel,
	fence DockerContainerLifecycleFence,
) (string, error) {
	body, err := json.Marshal(dockerCreatePayloadWithLabels(request, expectedLabels))
	if err != nil || len(body) == 0 || len(body) > maxDockerContainerWriteRequestBytes {
		return "", newDockerContainerWriteError(DockerContainerWriteFailureInvalidResponse)
	}
	query := "name=" + url.QueryEscape(request.Spec.ContainerName)
	path := "/v" + DockerContainerWriteAPIVersion + "/containers/create"
	if fence != nil {
		if err := fence(ctx, DockerContainerLifecycleActionCreate); err != nil {
			return "", err
		}
	}
	response, err := transport.do(ctx, http.MethodPost, path, query, body, true)
	if err != nil {
		return "", err
	}
	if response.status == http.StatusConflict {
		return "", newDockerContainerWriteError(DockerContainerWriteFailureCreateConflict)
	}
	var payload struct {
		ID       string   `json:"Id"`
		Warnings []string `json:"Warnings"`
	}
	if err := decodeDockerContainerWriteJSON(response.body, &payload); err != nil ||
		!validDockerContainerID(payload.ID) || len(payload.Warnings) != 0 {
		return "", newDockerContainerWriteError(DockerContainerWriteFailureInvalidResponse)
	}
	return payload.ID, nil
}

func (transport dockerEngineContainerWriteTransport) remove(ctx context.Context,
	containerID string, allowNotFound bool,
) error {
	path := "/v" + DockerContainerWriteAPIVersion + "/containers/" + url.PathEscape(containerID)
	response, err := transport.do(ctx, http.MethodDelete, path, "v=1", nil, false)
	if err != nil {
		return err
	}
	if response.status == http.StatusNotFound && allowNotFound {
		return nil
	}
	if response.status != http.StatusNoContent {
		return newDockerContainerWriteError(DockerContainerWriteFailureInvalidResponse)
	}
	return nil
}

func (transport dockerEngineContainerWriteTransport) cleanupExactContainer(
	request DockerContainerWriteRequest, references ...string,
) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), dockerContainerWriteCleanupTimeout)
	defer cancel()
	seen := make(map[string]struct{}, len(references))
	foundUnsafe := false
	for _, reference := range references {
		if reference == "" {
			continue
		}
		if _, ok := seen[reference]; ok {
			continue
		}
		seen[reference] = struct{}{}
		inspection, found, err := transport.inspect(cleanupCtx, reference)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		if verifyDockerContainerInspection(inspection, request) != nil ||
			(validDockerContainerID(reference) && inspection.ID != reference) {
			foundUnsafe = true
			continue
		}
		return transport.remove(cleanupCtx, inspection.ID, true)
	}
	if foundUnsafe {
		return newDockerContainerWriteError(DockerContainerWriteFailureUnsafeExisting)
	}
	return nil
}

type dockerContainerWriteHTTPResponse struct {
	status int
	body   []byte
}

func (transport dockerEngineContainerWriteTransport) do(ctx context.Context, method, path,
	rawQuery string, body []byte, wantJSON bool,
) (dockerContainerWriteHTTPResponse, error) {
	if err := ctx.Err(); err != nil {
		return dockerContainerWriteHTTPResponse{}, err
	}
	if !validDockerContainerWriteOperation(method, path, rawQuery, body) {
		return dockerContainerWriteHTTPResponse{}, newDockerContainerWriteError(DockerContainerWriteFailureInvalidResponse)
	}
	requestURL := "http://docker" + path
	if rawQuery != "" {
		requestURL += "?" + rawQuery
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL, bytes.NewReader(body))
	if err != nil {
		return dockerContainerWriteHTTPResponse{}, newDockerContainerWriteError(DockerContainerWriteFailureInvalidResponse)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "cyberagent-workbench/docker-write-rehearsal-v1")
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := transport.doer.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return dockerContainerWriteHTTPResponse{}, ctx.Err()
		}
		return dockerContainerWriteHTTPResponse{}, newDockerContainerWriteError(DockerContainerWriteFailureConnection)
	}
	if response == nil || response.Body == nil {
		return dockerContainerWriteHTTPResponse{}, newDockerContainerWriteError(DockerContainerWriteFailureInvalidResponse)
	}
	defer response.Body.Close()
	if response.Request == nil || response.Request.URL == nil ||
		response.Request.Method != method || response.Request.URL.Scheme != "http" ||
		response.Request.URL.Host != "docker" || response.Request.URL.Path != path ||
		response.Request.URL.RawQuery != rawQuery {
		return dockerContainerWriteHTTPResponse{}, newDockerContainerWriteError(DockerContainerWriteFailureInvalidResponse)
	}
	allowed := false
	switch method {
	case http.MethodGet:
		allowed = response.StatusCode == http.StatusOK || response.StatusCode == http.StatusNotFound
	case http.MethodPost:
		allowed = response.StatusCode == http.StatusCreated || response.StatusCode == http.StatusConflict
	case http.MethodDelete:
		allowed = response.StatusCode == http.StatusNoContent || response.StatusCode == http.StatusNotFound
	}
	if !allowed {
		return dockerContainerWriteHTTPResponse{}, newDockerContainerWriteError(DockerContainerWriteFailureInvalidResponse)
	}
	if response.StatusCode == http.StatusNoContent || response.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return dockerContainerWriteHTTPResponse{status: response.StatusCode}, nil
	}
	if response.StatusCode == http.StatusConflict {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return dockerContainerWriteHTTPResponse{status: response.StatusCode}, nil
	}
	if wantJSON {
		contentType := response.Header.Get("Content-Type")
		if contentType != "" {
			mediaType, _, parseErr := mime.ParseMediaType(contentType)
			if parseErr != nil || !strings.EqualFold(mediaType, "application/json") {
				return dockerContainerWriteHTTPResponse{}, newDockerContainerWriteError(DockerContainerWriteFailureInvalidResponse)
			}
		}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxDockerContainerWriteResponseBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxDockerContainerWriteResponseBytes {
		return dockerContainerWriteHTTPResponse{}, newDockerContainerWriteError(DockerContainerWriteFailureInvalidResponse)
	}
	return dockerContainerWriteHTTPResponse{status: response.StatusCode, body: data}, nil
}

func validDockerContainerWriteOperation(method, path, rawQuery string, body []byte) bool {
	containerPrefix := "/v" + DockerContainerWriteAPIVersion + "/containers/"
	imagePrefix := "/v" + DockerContainerWriteAPIVersion + "/images/"
	switch method {
	case http.MethodGet:
		if rawQuery != "" || len(body) != 0 || !strings.HasSuffix(path, "/json") {
			return false
		}
		if strings.HasPrefix(path, containerPrefix) {
			reference, err := url.PathUnescape(strings.TrimSuffix(
				strings.TrimPrefix(path, containerPrefix), "/json"))
			return err == nil && path == containerPrefix+url.PathEscape(reference)+"/json" &&
				(validDockerContainerID(reference) || validDockerContainerName(reference))
		}
		if strings.HasPrefix(path, imagePrefix) {
			digest, err := url.PathUnescape(strings.TrimSuffix(
				strings.TrimPrefix(path, imagePrefix), "/json"))
			return err == nil && path == imagePrefix+url.PathEscape(digest)+"/json" &&
				ValidOCIImageDigest(digest)
		}
		return false
	case http.MethodPost:
		if path != containerPrefix+"create" || len(body) == 0 || len(body) > maxDockerContainerWriteRequestBytes {
			return false
		}
		values, err := url.ParseQuery(rawQuery)
		return err == nil && len(values) == 1 && len(values["name"]) == 1 &&
			validDockerContainerName(values.Get("name")) &&
			rawQuery == "name="+url.QueryEscape(values.Get("name")) && json.Valid(body)
	case http.MethodDelete:
		if rawQuery != "v=1" || len(body) != 0 || !strings.HasPrefix(path, containerPrefix) {
			return false
		}
		containerID, err := url.PathUnescape(strings.TrimPrefix(path, containerPrefix))
		return err == nil && path == containerPrefix+url.PathEscape(containerID) &&
			validDockerContainerID(containerID)
	default:
		return false
	}
}

func decodeDockerContainerWriteJSON(data []byte, target any) error {
	if !json.Valid(data) || rejectDuplicateDockerObservationJSON(data) != nil {
		return newDockerContainerWriteError(DockerContainerWriteFailureInvalidResponse)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return newDockerContainerWriteError(DockerContainerWriteFailureInvalidResponse)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return newDockerContainerWriteError(DockerContainerWriteFailureInvalidResponse)
	}
	return nil
}

func validDockerContainerID(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func validDockerContainerName(value string) bool {
	if len(value) != len("cyberagent-")+24 || !strings.HasPrefix(value, "cyberagent-") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "cyberagent-"))
	return err == nil
}

func equalStrings(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}

func equalDockerLabels(actual map[string]string, expected []DockerContainerLabel) bool {
	if len(actual) != len(expected) {
		return false
	}
	for _, label := range expected {
		if actual[label.Name] != label.Value {
			return false
		}
	}
	return true
}

// The fixed Standard Code image carries version metadata that Docker inherits
// into Config.Labels. Those fixed values grant no ownership: every managed
// and lifecycle label must still match exactly, and generic profiles retain
// strict label equality.
func dockerContainerProfileLabelsMatch(actual map[string]string, spec DockerContainerSpec,
	expected []DockerContainerLabel,
) bool {
	if spec.Validate() != nil || !dockerContainerSpecUsesStandardCodeWorkspace(spec) {
		return equalDockerLabels(actual, expected)
	}
	filtered := make(map[string]string, len(actual))
	for name, value := range actual {
		switch name {
		case DockerStandardCodeProjectActionsLabel, DockerStandardCodePreparationLabel:
			if value != "1" {
				return false
			}
		case DockerStandardCodePNPMVersionLabel:
			if value != DockerStandardCodePNPMVersion {
				return false
			}
		case DockerStandardCodeYarnVersionLabel:
			if value != DockerStandardCodeYarnVersion {
				return false
			}
		case DockerStandardCodeBunVersionLabel:
			if value != DockerStandardCodeBunVersion {
				return false
			}
		default:
			filtered[name] = value
		}
	}
	return equalDockerLabels(filtered, expected)
}

func containsFold(values []string, target string) bool {
	copyValues := append([]string(nil), values...)
	sort.Strings(copyValues)
	for _, value := range copyValues {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}
