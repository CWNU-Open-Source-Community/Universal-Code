package sandbox

import "testing"

func TestYarnPreparationRequiresItsPinnedImageLabel(t *testing.T) {
	transport, digest := readyDockerReadinessTestTransport(t)
	probe := fixedDockerReadinessTestProbe(t, transport)
	transport.image.ProjectActionsVersion, transport.image.PreparationVersion = "1", "1"
	for _, version := range []string{"", "1.22.21", DockerStandardCodeYarnVersion} {
		transport.image.YarnVersion = version
		if ready, err := probe.ProjectPreparationAvailable(t.Context(), digest); err != nil || !ready {
			t.Fatalf("older npm/Go image disabled: %v", err)
		}
		if ready, err := probe.YarnPreparationAvailable(t.Context(), digest); err != nil || ready != (version == DockerStandardCodeYarnVersion) {
			t.Fatalf("version=%s ready=%t err=%v", version, ready, err)
		}
	}
}
