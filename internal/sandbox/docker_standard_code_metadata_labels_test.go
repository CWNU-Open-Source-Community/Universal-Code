package sandbox

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Docker inherits these fixed labels from the admitted runner image, even
// though the create request only supplies the lifecycle ownership labels.
type standardCodeImageLabelsDoer struct {
	base   dockerContainerWriteHTTPDoer
	change func(map[string]any)
}

func (d standardCodeImageLabelsDoer) Do(request *http.Request) (*http.Response, error) {
	response, err := d.base.Do(request)
	if err != nil || response.StatusCode != http.StatusOK || request.Method != http.MethodGet ||
		!strings.Contains(request.URL.Path, "/containers/") || !strings.HasSuffix(request.URL.Path, "/json") {
		return response, err
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		return nil, err
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, err
	}
	labels := document["Config"].(map[string]any)["Labels"].(map[string]any)
	labels[DockerStandardCodeProjectActionsLabel] = "1"
	labels[DockerStandardCodePreparationLabel] = "1"
	labels[DockerStandardCodePNPMVersionLabel] = DockerStandardCodePNPMVersion
	labels[DockerStandardCodeYarnVersionLabel] = DockerStandardCodeYarnVersion
	if d.change != nil {
		d.change(labels)
	}
	body, err = json.Marshal(document)
	if err != nil {
		return nil, err
	}
	return dockerWriteTestResponse(request, response.StatusCode, body), nil
}

func standardCodeImageLabelsWriteRequest(t *testing.T, toolchain string) DockerContainerWriteRequest {
	t.Helper()
	binding := dockerStandardCodeBindingFixture(toolchain)
	manifest, err := DockerStandardCodeManifest(binding)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := CompileDockerContainerSpec(t.Context(),
		dockerContainerCompilerObservation(t, t.Context(), manifest, true, 8, 8*1024*1024*1024), manifest)
	if err != nil {
		t.Fatal(err)
	}
	root, mask := t.TempDir(), filepath.Join(t.TempDir(), "git-mask")
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+filepath.Join(t.TempDir(), ".git", "worktrees", "fixture")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mask, []byte(DockerStandardCodeGitMetadataMask), 0600); err != nil {
		t.Fatal(err)
	}
	request, err := NewDockerStandardCodeContainerWriteRequest(t.Context(), root, mask, spec)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestDockerStandardCodeImageLabelsReachOwnedLifecycleAndIO(t *testing.T) {
	for _, toolchain := range []string{DockerStandardCodeToolchainNodePrepare, DockerStandardCodeToolchainGoPrepare} {
		t.Run(toolchain, func(t *testing.T) {
			write := standardCodeImageLabelsWriteRequest(t, toolchain)
			endpoint := mustDockerLifecycleEndpoint(t)
			daemon := &dockerLifecycleTestDaemon{mode: dockerLifecycleExitNatural}
			transport, err := newDockerEngineContainerLifecycleTransport(standardCodeImageLabelsDoer{base: daemon}, endpoint)
			if err != nil {
				t.Fatal(err)
			}
			ownership, err := NewDockerContainerLifecycleOwnership("metadata-attempt", 1,
				fingerprint("metadata-intent"), write.Spec.LabelPlanFingerprint)
			if err != nil {
				t.Fatal(err)
			}
			stage, err := transport.StageOwned(t.Context(), write, ownership, allowDockerLifecycleFence)
			if err != nil {
				t.Fatalf("create inspection rejected inherited image metadata: %v", err)
			}
			request, err := NewOwnedDockerContainerLifecycleRequest(ownership.AttemptID, 1,
				write, stage, ownership, DockerContainerLifecycleConfirmation)
			if err != nil {
				t.Fatal(err)
			}
			observation, err := transport.Observe(t.Context(), request)
			if err != nil || observation.State != DockerContainerLifecycleStateCreated {
				t.Fatalf("owned observation rejected image metadata: %+v %v", observation, err)
			}
			if _, started, err := transport.Start(t.Context(), request, allowDockerLifecycleFence); err != nil || !started {
				t.Fatalf("owned start rejected image metadata: %t %v", started, err)
			}
			ioDoer := &ownedDockerIOTestDoer{lifecycle: daemon}
			ioTransport, err := newDockerEngineContainerIOTransport(standardCodeImageLabelsDoer{base: ioDoer}, endpoint)
			if err != nil {
				t.Fatal(err)
			}
			logs, err := ioTransport.AttachOwnedLogs(t.Context(), request, testOwnedDockerLogPlan(t, request))
			if err != nil {
				t.Fatalf("owned logs rejected image metadata: %v", err)
			}
			_ = logs.Close()
			outputPlan, err := NewDockerOutputExportPlan(request.AttemptID, 1, write.Spec.RunID,
				request.ResourceIDFingerprint, DockerStandardCodeWorkspaceTarget, MaxDockerOutputFiles,
				MaxDockerOutputFileBytes, MaxDockerOutputTotalBytes)
			if err != nil {
				t.Fatal(err)
			}
			archive, err := ioTransport.ExportOwnedOutputs(t.Context(), request, outputPlan)
			if err != nil {
				t.Fatalf("owned archive rejected image metadata: %v", err)
			}
			_ = archive.Close()
			if daemon.starts != 1 {
				t.Fatalf("ownership flow started %d times", daemon.starts)
			}
		})
	}
}

func TestDockerStandardCodeImageLabelsPreserveStrictOwnershipAndGenericProfiles(t *testing.T) {
	write := standardCodeImageLabelsWriteRequest(t, DockerStandardCodeToolchainNodePrepare)
	ownership, err := NewDockerContainerLifecycleOwnership("metadata-attempt", 1,
		fingerprint("metadata-intent"), write.Spec.LabelPlanFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := dockerContainerLifecycleOwnedLabels(write.Spec.Labels, ownership)
	if err != nil {
		t.Fatal(err)
	}
	payload := dockerCreatePayloadWithLabels(write, expected)
	daemon := &dockerWriteTestDaemon{containerID: dockerWriteTestContainerID, name: write.Spec.ContainerName, payload: payload}
	var inspection dockerContainerInspection
	if err := json.Unmarshal(daemon.inspectPayload(), &inspection); err != nil {
		t.Fatal(err)
	}
	inspection.Config.Labels[DockerStandardCodeProjectActionsLabel] = "1"
	inspection.Config.Labels[DockerStandardCodePreparationLabel] = "1"
	inspection.Config.Labels[DockerStandardCodePNPMVersionLabel] = DockerStandardCodePNPMVersion
	if equalDockerLabels(inspection.Config.Labels, expected) {
		t.Fatal("generic label equality accepted inherited metadata")
	}
	for _, label := range expected {
		for _, missing := range []bool{false, true} {
			mode := "changed/"
			if missing {
				mode = "missing/"
			}
			t.Run(mode+label.Name, func(t *testing.T) {
				copy := inspection
				copy.Config.Labels = make(map[string]string, len(inspection.Config.Labels))
				for name, value := range inspection.Config.Labels {
					copy.Config.Labels[name] = value
				}
				if missing {
					delete(copy.Config.Labels, label.Name)
				} else {
					copy.Config.Labels[label.Name] = "different-owner"
				}
				if verifyDockerContainerInspectionWithLabels(copy, write, expected) == nil {
					t.Fatal("image metadata weakened exact lifecycle ownership")
				}
			})
		}
	}
	for _, name := range []string{DockerStandardCodeProjectActionsLabel, DockerStandardCodePreparationLabel, DockerStandardCodePNPMVersionLabel, "unknown.label", "desktop.docker.io/ports.scheme"} {
		t.Run("unexpected/"+name, func(t *testing.T) {
			copy := inspection
			copy.Config.Labels = make(map[string]string, len(inspection.Config.Labels))
			for key, value := range inspection.Config.Labels {
				copy.Config.Labels[key] = value
			}
			copy.Config.Labels[name] = "unexpected"
			if verifyDockerContainerInspectionWithLabels(copy, write, expected) == nil {
				t.Fatal("unknown metadata or changed fixed label value was accepted")
			}
		})
	}
	generic := newDockerContainerWriteTestRequest(t)
	genericPayload := dockerCreatePayload(generic)
	daemon.payload, daemon.name = genericPayload, generic.Spec.ContainerName
	inspection = dockerContainerInspection{}
	if err := json.Unmarshal(daemon.inspectPayload(), &inspection); err != nil {
		t.Fatal(err)
	}
	inspection.Config.Labels[DockerStandardCodeProjectActionsLabel] = "1"
	inspection.Config.Labels[DockerStandardCodePreparationLabel] = "1"
	if verifyDockerContainerInspection(inspection, generic) == nil {
		t.Fatal("generic profile accepted Standard Code image metadata")
	}
}
