package sandbox

import (
	"strings"
	"testing"
)

func TestBunPreparationRequiresExactImageVersion(t *testing.T) {
	transport, digest := readyDockerReadinessTestTransport(t)
	probe := fixedDockerReadinessTestProbe(t, transport)
	transport.image.ProjectActionsVersion, transport.image.PreparationVersion = "1", "1"
	for _, version := range []string{"", "1.4.2", DockerStandardCodeBunVersion} {
		transport.image.BunVersion = version
		if ready, err := probe.BunPreparationAvailable(t.Context(), digest); err != nil || ready != (version == DockerStandardCodeBunVersion) {
			t.Fatalf("Bun %q ready=%t %v", version, ready, err)
		}
		if ready, err := probe.ProjectPreparationAvailable(t.Context(), digest); err != nil || !ready {
			t.Fatal("Bun label disabled older toolchains", ready, err)
		}
	}
	if ready, err := probe.BunPreparationAvailable(t.Context(), "sha256:"+strings.Repeat("f", 64)); err == nil || ready {
		t.Fatal("other image accepted", ready, err)
	}
	for _, call := range transport.calls {
		if call != "image" {
			t.Fatal("readiness executed a container", call)
		}
	}
	for _, toolchain := range []string{DockerStandardCodeToolchainBun, DockerStandardCodeToolchainBunPrepare} {
		binding := dockerStandardCodeBindingFixture(toolchain)
		manifest, err := DockerStandardCodeManifest(binding)
		if err != nil {
			t.Fatal("Bun runner binding rejected", err)
		}
		if manifest.Network.Mode != "disabled" {
			t.Fatal("Bun namespace changed network policy", manifest)
		}
	}
}
