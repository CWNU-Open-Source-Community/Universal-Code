package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var _ ReadinessProbe = DockerReadinessProbe{}

func TestPNPMPreparationRequiresExactVersionWithoutDisablingOlderPreparation(t *testing.T) {
	transport, digest := readyDockerReadinessTestTransport(t)
	probe := fixedDockerReadinessTestProbe(t, transport)
	transport.image.ProjectActionsVersion, transport.image.PreparationVersion = "1", "1"
	for _, version := range []string{"", "10.28.0", DockerStandardCodePNPMVersion} {
		transport.image.PNPMVersion = version
		if ready, err := probe.ProjectPreparationAvailable(t.Context(), digest); err != nil || !ready {
			t.Fatalf("pnpm metadata disabled npm/Go preparation: %q %t %v", version, ready, err)
		}
		if ready, err := probe.PNPMPreparationAvailable(t.Context(), digest); err != nil || ready != (version == DockerStandardCodePNPMVersion) {
			t.Fatalf("pnpm version %q ready=%t error=%v", version, ready, err)
		}
	}
	if ready, err := probe.PNPMPreparationAvailable(t.Context(), "sha256:"+strings.Repeat("f", 64)); err == nil || ready {
		t.Fatalf("pnpm metadata accepted another image: %t %v", ready, err)
	}
	for _, call := range transport.calls {
		if call != "image" {
			t.Fatalf("capability inspection performed %q", call)
		}
	}
}

func TestPreparationImageMetadataRequiresIndependentUpdatedMarker(t *testing.T) {
	transport, digest := readyDockerReadinessTestTransport(t)
	probe := fixedDockerReadinessTestProbe(t, transport)
	transport.image.ProjectActionsVersion = "1"
	if ready, err := probe.ProjectPreparationAvailable(t.Context(), digest); err != nil || ready {
		t.Fatalf("old image promised preparation: %t %v", ready, err)
	}
	transport.image.PreparationVersion = "1"
	if ready, err := probe.ProjectPreparationAvailable(t.Context(), digest); err != nil || !ready {
		t.Fatalf("prepared image unavailable: %t %v", ready, err)
	}
	if ready, err := probe.ProjectPreparationAvailable(t.Context(), "sha256:"+strings.Repeat("f", 64)); err == nil || ready {
		t.Fatalf("wrong image accepted: %t %v", ready, err)
	}
}

func TestProjectActionsImageMetadataRequiresUpdatedExactImage(t *testing.T) {
	transport, digest := readyDockerReadinessTestTransport(t)
	probe := fixedDockerReadinessTestProbe(t, transport)
	if ready, err := probe.ProjectActionsAvailable(t.Context(), digest); err != nil || ready {
		t.Fatalf("old image promised project tools: %t %v", ready, err)
	}
	transport.image.ProjectActionsVersion = "1"
	if ready, err := probe.ProjectActionsAvailable(t.Context(), digest); err != nil || !ready {
		t.Fatalf("updated image capability lost: %t %v", ready, err)
	}
	if ready, err := probe.ProjectActionsAvailable(t.Context(), "sha256:"+strings.Repeat("f", 64)); err == nil || ready {
		t.Fatalf("unbound image capability accepted: %t %v", ready, err)
	}
}

type dockerReadinessTestTransport struct {
	endpoint   DockerObservationEndpoint
	pingErr    error
	version    DockerDaemonVersion
	versionErr error
	info       DockerDaemonInfo
	infoErr    error
	image      DockerImageInspection
	imageErr   error
	calls      []string
}

func (transport *dockerReadinessTestTransport) Endpoint() DockerObservationEndpoint {
	return transport.endpoint
}

func (transport *dockerReadinessTestTransport) Ping(context.Context) error {
	transport.calls = append(transport.calls, "ping")
	return transport.pingErr
}

func (transport *dockerReadinessTestTransport) Version(context.Context) (DockerDaemonVersion, error) {
	transport.calls = append(transport.calls, "version")
	return transport.version, transport.versionErr
}

func (transport *dockerReadinessTestTransport) Info(context.Context) (DockerDaemonInfo, error) {
	transport.calls = append(transport.calls, "info")
	return transport.info, transport.infoErr
}

func (transport *dockerReadinessTestTransport) InspectImage(_ context.Context,
	_ string,
) (DockerImageInspection, error) {
	transport.calls = append(transport.calls, "image")
	return transport.image, transport.imageErr
}

func TestDockerReadinessDefaultsClosedWithoutProbing(t *testing.T) {
	transport, imageDigest := readyDockerReadinessTestTransport(t)
	probe := fixedDockerReadinessTestProbe(t, transport)
	readiness, err := probe.Check(context.Background(), DockerRuntimeCapabilities{},
		dockerReadinessTestManifest(), imageDigest)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.Status != DockerReadinessStatusDisabled || readiness.Ready ||
		readiness.ReasonCode != DockerReadinessReasonFeatureDisabled ||
		readiness.RemediationCode != DockerReadinessRemediationEnableFeature ||
		len(transport.calls) != 0 {
		t.Fatalf("default-closed Docker readiness = %#v calls=%v", readiness, transport.calls)
	}
	if err := readiness.Validate(); err != nil {
		t.Fatal(err)
	}
	if readiness.ReadyAt(readiness.CheckedAt) {
		t.Fatal("disabled Docker readiness became usable")
	}
	if err := (DockerRuntimeCapabilities{Enabled: true,
		ManagedEgressEnabled: true}).Validate(); err == nil {
		t.Fatal("unimplemented managed egress capability validated")
	}
}

func TestDockerReadinessReportsDaemonUnreachable(t *testing.T) {
	transport, imageDigest := readyDockerReadinessTestTransport(t)
	transport.pingErr = newDockerObservationError(DockerObservationFailureConnection)
	probe := fixedDockerReadinessTestProbe(t, transport)
	readiness, err := probe.Check(context.Background(),
		DockerRuntimeCapabilities{Enabled: true}, dockerReadinessTestManifest(), imageDigest)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.Status != DockerReadinessStatusUnavailable || readiness.Ready ||
		readiness.ReasonCode != DockerReadinessReasonDaemonUnreachable ||
		readiness.RemediationCode != DockerReadinessRemediationStartDaemon ||
		strings.Join(transport.calls, ",") != "ping" {
		t.Fatalf("daemon-unreachable Docker readiness = %#v calls=%v", readiness, transport.calls)
	}
	if err := readiness.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDockerReadinessFailsClosedOnUnsupportedCapabilities(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*dockerReadinessTestTransport, *Manifest, *DockerRuntimeCapabilities)
		wantReason string
		wantCalls  string
	}{
		{
			name: "API window",
			mutate: func(transport *dockerReadinessTestTransport, _ *Manifest,
				_ *DockerRuntimeCapabilities,
			) {
				transport.version.APIVersion = "1.39"
			},
			wantReason: DockerReadinessReasonAPIUnsupported,
			wantCalls:  "ping,version",
		},
		{
			name: "non-Linux daemon",
			mutate: func(transport *dockerReadinessTestTransport, _ *Manifest,
				_ *DockerRuntimeCapabilities,
			) {
				transport.version.OSType = "windows"
			},
			wantReason: DockerReadinessReasonPlatformUnsupported,
			wantCalls:  "ping,version",
		},
		{
			name: "PIDs controller",
			mutate: func(transport *dockerReadinessTestTransport, _ *Manifest,
				_ *DockerRuntimeCapabilities,
			) {
				transport.info.PidsLimit = false
			},
			wantReason: DockerReadinessReasonPIDsLimitUnavailable,
			wantCalls:  "ping,version,info",
		},
		{
			name: "CPU capacity",
			mutate: func(transport *dockerReadinessTestTransport, manifest *Manifest,
				_ *DockerRuntimeCapabilities,
			) {
				transport.info.NCPU = 1
				manifest.Resources.CPUQuotaMillis = 2000
			},
			wantReason: DockerReadinessReasonResourceCapacityInsufficient,
			wantCalls:  "ping,version,info",
		},
		{
			name: "network allowlist",
			mutate: func(_ *dockerReadinessTestTransport, manifest *Manifest,
				_ *DockerRuntimeCapabilities,
			) {
				manifest.Network = NetworkScope{Mode: "allowlist",
					AllowedTargets: []string{"example.invalid:443"}}
			},
			wantReason: DockerReadinessReasonManagedEgressUnavailable,
			wantCalls:  "",
		},
		{
			name: "process managed egress grant",
			mutate: func(_ *dockerReadinessTestTransport, _ *Manifest,
				capabilities *DockerRuntimeCapabilities,
			) {
				capabilities.ManagedEgressEnabled = true
			},
			wantReason: DockerReadinessReasonManagedEgressUnavailable,
			wantCalls:  "",
		},
		{
			name: "image unavailable",
			mutate: func(transport *dockerReadinessTestTransport, _ *Manifest,
				_ *DockerRuntimeCapabilities,
			) {
				transport.imageErr = newDockerObservationError(
					DockerObservationFailureImageNotFound)
			},
			wantReason: DockerReadinessReasonImageUnavailable,
			wantCalls:  "ping,version,info,image",
		},
		{
			name: "image declares environment",
			mutate: func(transport *dockerReadinessTestTransport, _ *Manifest,
				_ *DockerRuntimeCapabilities,
			) {
				transport.image.EnvironmentCount = 1
			},
			wantReason: DockerReadinessReasonImageUnavailable,
			wantCalls:  "ping,version,info,image",
		},
		{
			name: "image declares volume",
			mutate: func(transport *dockerReadinessTestTransport, _ *Manifest,
				_ *DockerRuntimeCapabilities,
			) {
				transport.image.VolumeCount = 1
			},
			wantReason: DockerReadinessReasonImageUnavailable,
			wantCalls:  "ping,version,info,image",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport, imageDigest := readyDockerReadinessTestTransport(t)
			manifest := dockerReadinessTestManifest()
			capabilities := DockerRuntimeCapabilities{Enabled: true}
			test.mutate(transport, &manifest, &capabilities)
			readiness, err := fixedDockerReadinessTestProbe(t, transport).Check(
				context.Background(), capabilities, manifest, imageDigest)
			if err != nil {
				t.Fatal(err)
			}
			if readiness.Status != DockerReadinessStatusUnavailable || readiness.Ready ||
				readiness.ReasonCode != test.wantReason ||
				strings.Join(transport.calls, ",") != test.wantCalls {
				t.Fatalf("fail-closed Docker readiness = %#v calls=%v", readiness,
					transport.calls)
			}
			if err := readiness.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDockerReadinessAcceptsBoundedLinuxDaemonAndImage(t *testing.T) {
	transport, imageDigest := readyDockerReadinessTestTransport(t)
	probe := fixedDockerReadinessTestProbe(t, transport)
	readiness, err := probe.Check(context.Background(),
		DockerRuntimeCapabilities{Enabled: true}, dockerReadinessTestManifest(), imageDigest)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.Status != DockerReadinessStatusReady || !readiness.Ready ||
		readiness.ReasonCode != DockerReadinessReasonNone ||
		readiness.RemediationCode != DockerReadinessRemediationNone ||
		!readiness.DaemonReachable || !readiness.ImageInspected ||
		!readiness.ImageProfileSafe ||
		readiness.NetworkMode != "disabled" || readiness.EndpointClass == "" ||
		readiness.EndpointFingerprint == "" || readiness.ReadinessFingerprint == "" ||
		strings.Join(transport.calls, ",") != "ping,version,info,image" {
		t.Fatalf("ready Docker readiness = %#v calls=%v", readiness, transport.calls)
	}
	if !readiness.ExpiresAt.Equal(readiness.CheckedAt.Add(DockerReadinessTTL)) {
		t.Fatalf("readiness TTL drifted: checked=%s expires=%s", readiness.CheckedAt,
			readiness.ExpiresAt)
	}
	if err := readiness.Validate(); err != nil {
		t.Fatal(err)
	}
	if !readiness.ReadyAt(readiness.CheckedAt.Add(10*time.Second)) ||
		readiness.ReadyAt(readiness.ExpiresAt) {
		t.Fatal("readiness TTL boundary was not enforced")
	}
	mutated := readiness
	mutated.RequestedPIDs++
	if err := mutated.Validate(); err == nil {
		t.Fatal("mutated Docker readiness retained authority")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := probe.Check(cancelled, DockerRuntimeCapabilities{Enabled: true},
		dockerReadinessTestManifest(), imageDigest); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled readiness error = %v", err)
	}
}

func readyDockerReadinessTestTransport(t *testing.T) (*dockerReadinessTestTransport, string) {
	t.Helper()
	endpoint, err := NewDockerObservationEndpoint(DockerObservationEndpointLocalNPipe)
	if err != nil {
		t.Fatal(err)
	}
	imageDigest := "sha256:" + strings.Repeat("d", 64)
	return &dockerReadinessTestTransport{
		endpoint: endpoint,
		version: DockerDaemonVersion{
			APIVersion: "1.47", MinAPIVersion: "1.24", EngineVersion: "27.5.1",
			GitCommit: "abc123", OSType: "linux", Architecture: "amd64",
		},
		info: DockerDaemonInfo{
			ID: "daemon-id", ServerVersion: "27.5.1", OSType: "linux",
			Architecture: "amd64", NCPU: 8, MemoryBytes: 8 * 1024 * 1024 * 1024,
			PidsLimit: true,
		},
		image: DockerImageInspection{
			ID:           "sha256:" + strings.Repeat("a", 64),
			RepoDigests:  []string{"example.invalid/workbench@" + imageDigest},
			OSType:       "linux",
			Architecture: "amd64",
			SizeBytes:    1024,
			User:         "65532:65532",
			RootFSType:   "layers",
			GraphDriver:  "overlay2",
		},
	}, imageDigest
}

func fixedDockerReadinessTestProbe(t *testing.T,
	transport DockerReadOnlyTransport,
) DockerReadinessProbe {
	t.Helper()
	probe, err := NewDockerReadinessProbe(transport)
	if err != nil {
		t.Fatal(err)
	}
	probe.now = func() time.Time {
		return time.Date(2026, time.August, 14, 12, 0, 0, 123_000_000, time.UTC)
	}
	return probe
}

func dockerReadinessTestManifest() Manifest {
	manifest := validManifest()
	manifest.Backend = BackendDocker
	return manifest
}
