package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func dockerStandardCodeBindingFixture(toolchain string) DockerStandardCodeRunnerBinding {
	return DockerStandardCodeRunnerBinding{
		RunID: "standard-code-run-1", MissionID: "standard-code-mission-1",
		SessionID: "standard-code-session-1", WorkspaceID: "standard-code-workspace-1",
		DrydockID:          "standard-code-drydock-1",
		DrydockWorkspaceID: "standard-code-drydock-workspace-1",
		DrydockGeneration:  1, CheckpointID: "standard-code-checkpoint-1",
		DrydockBindingSHA256: strings.Repeat("a", 64),
		ProfileSnapshotID:    "standard-code-profile-1", ProfileRevision: 1,
		PermissionSnapshotID: "standard-code-permission-1", PermissionRevision: 1,
		CapabilityGeneration: strings.Repeat("b", 64),
		CommandSHA256:        strings.Repeat("c", 64),
		StdinPolicy:          DockerStandardCodeStdinClosed, Toolchain: toolchain,
		WorkingDirectory: ".", Arguments: []string{"test", "./..."},
		TimeoutSeconds: 120,
	}
}

func TestDockerPreparationLimitsAreBoundToPreparationToolchain(t *testing.T) {
	binding := dockerStandardCodeBindingFixture(DockerStandardCodeToolchainGoPrepare)
	binding.Arguments = []string{"mod", "download"}
	manifest, err := DockerStandardCodeManifest(binding)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Command.Arguments[19] != "1073741824" || manifest.Command.Arguments[20] != "200000" || manifest.Command.Arguments[21] != "67108864" || manifest.Network.Mode != "disabled" {
		t.Fatal("preparation write bounds or network boundary drifted")
	}
	if _, ok := ParseDockerStandardCodeManifest(manifest); !ok {
		t.Fatal("preparation profile failed round trip")
	}
	manifest.Command.Arguments[17] = DockerStandardCodeToolchainGo
	if _, ok := ParseDockerStandardCodeManifest(manifest); ok {
		t.Fatal("ordinary command acquired preparation write bounds")
	}
}

func TestDockerStandardCodeManifestIsBackendNeutralAndExact(t *testing.T) {
	expectedTmpfs := fmt.Sprintf(
		"rw,exec,nosuid,nodev,size=%d,nr_inodes=%d,mode=0700,uid=65532,gid=65532",
		DockerStandardCodeCacheBytes, DockerStandardCodeCacheEntries)
	if DockerStandardCodeCacheTmpfsOptions != expectedTmpfs {
		t.Fatalf("fixed cache tmpfs=%q want=%q",
			DockerStandardCodeCacheTmpfsOptions, expectedTmpfs)
	}
	for _, toolchain := range []string{DockerStandardCodeToolchainGo,
		DockerStandardCodeToolchainNode, DockerStandardCodeToolchainPython,
		DockerStandardCodeToolchainRust} {
		t.Run(toolchain, func(t *testing.T) {
			binding := dockerStandardCodeBindingFixture(toolchain)
			manifest, err := DockerStandardCodeManifest(binding)
			if err != nil {
				t.Fatal(err)
			}
			parsed, ok := ParseDockerStandardCodeManifest(manifest)
			if !ok || parsed.Toolchain != toolchain ||
				parsed.StdinPolicy != DockerStandardCodeStdinClosed ||
				parsed.DrydockBindingSHA256 != binding.DrydockBindingSHA256 ||
				strings.Join(parsed.Arguments, "\x00") != strings.Join(binding.Arguments, "\x00") {
				t.Fatalf("fixed manifest did not round-trip: %#v ok=%t", parsed, ok)
			}
			if manifest.Network.Mode != "disabled" || len(manifest.Environment) != 0 ||
				len(manifest.InputArtifactIDs) != 0 || len(manifest.Mounts) != 1 ||
				manifest.Mounts[0] != (Mount{Source: ".",
					Target: DockerStandardCodeWorkspaceTarget, Access: MountReadWrite}) {
				t.Fatalf("fixed Standard Code boundary drifted: %#v", manifest)
			}
		})
	}
}

func TestDockerGoScratchIsBoundToFixedToolchain(t *testing.T) {
	expectedGo := fmt.Sprintf(
		"rw,exec,nosuid,nodev,size=%d,nr_inodes=%d,mode=0700,uid=65532,gid=65532",
		DockerStandardCodeGoCacheBytes, DockerStandardCodeCacheEntries)
	if DockerStandardCodeGoCacheTmpfsOptions != expectedGo {
		t.Fatal("Go scratch capacity drifted from its fixed bound")
	}
	for _, toolchain := range []string{DockerStandardCodeToolchainGo,
		DockerStandardCodeToolchainGoPrepare, DockerStandardCodeToolchainNode,
		DockerStandardCodeToolchainNodePrepare, DockerStandardCodeToolchainPython,
		DockerStandardCodeToolchainRust} {
		t.Run(toolchain, func(t *testing.T) {
			manifest, err := DockerStandardCodeManifest(dockerStandardCodeBindingFixture(toolchain))
			if err != nil {
				t.Fatal(err)
			}
			spec, err := CompileDockerContainerSpec(t.Context(),
				dockerContainerCompilerObservation(t, t.Context(), manifest, true, 8, 8*1024*1024*1024), manifest)
			if err != nil {
				t.Fatal(err)
			}
			want := DockerStandardCodeCacheTmpfsOptions
			if toolchain == DockerStandardCodeToolchainGo || toolchain == DockerStandardCodeToolchainGoPrepare {
				want = expectedGo
			}
			payload := dockerCreatePayload(DockerContainerWriteRequest{Spec: spec})
			if len(payload.HostConfig.Tmpfs) != 1 || payload.HostConfig.Tmpfs[DockerStandardCodeCacheTarget] != want ||
				payload.HostConfig.Memory != DockerStandardCodeMemoryBytes || payload.HostConfig.MemorySwap != DockerStandardCodeMemoryBytes {
				t.Fatalf("scratch capacity or total memory changed: %+v", payload.HostConfig)
			}
		})
	}
}

func TestDockerStandardCodeManifestBindsPipeStdinAndReadsLegacyClosedReceipts(t *testing.T) {
	binding := dockerStandardCodeBindingFixture(DockerStandardCodeToolchainPython)
	legacy, err := dockerStandardCodeManifest(binding,
		dockerStandardCodeRunnerLegacyProtocol)
	if err != nil {
		t.Fatal(err)
	}
	legacyBinding, ok := ParseDockerStandardCodeManifest(legacy)
	if !ok || legacyBinding.StdinPolicy != DockerStandardCodeStdinClosed ||
		legacy.Command.Arguments[0] != dockerStandardCodeRunnerLegacyProtocol {
		t.Fatalf("legacy Standard Code manifest was not retained: %#v ok=%t",
			legacyBinding, ok)
	}

	binding.StdinPolicy = DockerStandardCodeStdinPipe
	manifest, err := DockerStandardCodeManifest(binding)
	if err != nil {
		t.Fatal(err)
	}
	parsed, ok := ParseDockerStandardCodeManifest(manifest)
	if !ok || parsed.StdinPolicy != DockerStandardCodeStdinPipe ||
		manifest.Command.Arguments[0] != DockerStandardCodeRunnerProtocolVersion {
		t.Fatalf("pipe Standard Code manifest did not round-trip: %#v ok=%t", parsed, ok)
	}
	observation := dockerContainerCompilerObservation(t, t.Context(), manifest, true, 8,
		8*1024*1024*1024)
	spec, err := CompileDockerContainerSpec(t.Context(), observation, manifest)
	if err != nil || !spec.StdinPipe {
		t.Fatalf("pipe Standard Code spec=%#v err=%v", spec, err)
	}
	payload := dockerCreatePayload(DockerContainerWriteRequest{Spec: spec})
	if !payload.AttachStdin || !payload.OpenStdin || !payload.StdinOnce ||
		payload.AttachStdout || payload.AttachStderr || payload.Tty {
		t.Fatalf("pipe Standard Code Docker config drifted: %#v", payload)
	}
}

func TestDockerStandardCodeManifestRejectsEveryBackendEscapeHatch(t *testing.T) {
	binding := dockerStandardCodeBindingFixture(DockerStandardCodeToolchainGo)
	manifest, err := DockerStandardCodeManifest(binding)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*Manifest){
		"network": func(value *Manifest) {
			value.Network = NetworkScope{Mode: "allowlist",
				AllowedTargets: []string{"example.invalid:443"}}
		},
		"environment": func(value *Manifest) {
			value.Environment = []EnvironmentBinding{{Name: "HOME",
				Source: EnvironmentLiteral, Value: "/host"}}
		},
		"extra mount": func(value *Manifest) {
			value.Mounts = append(value.Mounts, Mount{Source: "secrets",
				Target: "/host-secrets", Access: MountReadOnly})
		},
		"docker socket": func(value *Manifest) {
			value.Mounts[0].Source = "/var/run/docker.sock"
		},
		"workdir":     func(value *Manifest) { value.Command.WorkingDirectory = "/tmp" },
		"executable":  func(value *Manifest) { value.Command.Executable = "/bin/sh" },
		"resource":    func(value *Manifest) { value.Resources.PIDs++ },
		"file export": func(value *Manifest) { value.Output.Paths = []string{"/workspace"} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := manifest
			candidate.Mounts = append([]Mount(nil), manifest.Mounts...)
			mutate(&candidate)
			if _, ok := ParseDockerStandardCodeManifest(candidate); ok {
				t.Fatal("tampered Standard Code manifest was recognized")
			}
		})
	}
	binding.WorkingDirectory = "../source"
	if _, err := DockerStandardCodeManifest(binding); err == nil {
		t.Fatal("relative workspace escape was accepted")
	}
}

func TestDockerStandardCodeContainerSpecKeepsOnlyOneWritableDrydockMount(t *testing.T) {
	ctx := context.Background()
	manifest, err := DockerStandardCodeManifest(
		dockerStandardCodeBindingFixture(DockerStandardCodeToolchainPython))
	if err != nil {
		t.Fatal(err)
	}
	observation := dockerContainerCompilerObservation(t, ctx, manifest, true, 8,
		8*1024*1024*1024)
	spec, err := CompileDockerContainerSpec(ctx, observation, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if spec.User != DockerContainerFixedUser || !spec.ReadOnlyRootFS ||
		!spec.NoNewPrivileges || !spec.DropAllCapabilities || !spec.InitEnabled ||
		spec.Network.Mode != "disabled" || spec.Network.Driver != DockerNetworkDriverNone ||
		len(spec.Environment) != 0 ||
		spec.InputArtifactCount != 0 || len(spec.Mounts) != 1 ||
		spec.Mounts[0].Source != "." ||
		spec.Mounts[0].Target != DockerStandardCodeWorkspaceTarget ||
		spec.Mounts[0].Access != MountReadWrite || !spec.Mounts[0].DedicatedOutput ||
		spec.Resources.MemoryBytes != DockerStandardCodeMemoryBytes ||
		spec.Resources.PIDs != DockerStandardCodePIDs {
		t.Fatalf("compiled Standard Code container boundary drifted: %#v", spec)
	}
}

func TestDockerStandardCodeWriteRequestMasksOnlyLinkedWorktreeMetadata(t *testing.T) {
	ctx := context.Background()
	manifest, err := DockerStandardCodeManifest(
		dockerStandardCodeBindingFixture(DockerStandardCodeToolchainNode))
	if err != nil {
		t.Fatal(err)
	}
	observation := dockerContainerCompilerObservation(t, ctx, manifest, true, 8,
		8*1024*1024*1024)
	spec, err := CompileDockerContainerSpec(ctx, observation, manifest)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	hostGitdir := filepath.Join(t.TempDir(), ".git", "worktrees", "drydock")
	original := "gitdir: " + hostGitdir + "\n"
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	mask := filepath.Join(t.TempDir(), "git-metadata-mask")
	if err := os.WriteFile(mask, []byte(DockerStandardCodeGitMetadataMask), 0o600); err != nil {
		t.Fatal(err)
	}
	request, err := NewDockerStandardCodeContainerWriteRequest(ctx, root, mask, spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.HostMounts) != 2 || request.HostMounts[0].Source != root ||
		request.HostMounts[0].ReadOnly || request.HostMounts[1].Source != mask ||
		!request.HostMounts[1].ReadOnly ||
		request.HostMounts[1].Target != DockerStandardCodeGitMetadataTarget {
		t.Fatalf("fixed Standard Code mounts drifted: %#v", request.HostMounts)
	}
	payload := dockerCreatePayload(request)
	if len(payload.HostConfig.Mounts) != 2 ||
		payload.HostConfig.Mounts[1].Target != DockerStandardCodeGitMetadataTarget ||
		!payload.HostConfig.Mounts[1].ReadOnly ||
		payload.HostConfig.Tmpfs[DockerStandardCodeCacheTarget] !=
			DockerStandardCodeCacheTmpfsOptions || len(payload.HostConfig.Tmpfs) != 1 {
		t.Fatalf("Docker payload omitted the fixed metadata mask: %#v",
			payload.HostConfig.Mounts)
	}
	if content, readErr := os.ReadFile(filepath.Join(root, ".git")); readErr != nil ||
		string(content) != original {
		t.Fatalf("host Drydock metadata changed: %q err=%v", content, readErr)
	}
	if _, err := NewDockerContainerWriteRequest(ctx, root, spec); err == nil {
		t.Fatal("generic Docker constructor accepted an unmasked Standard Code request")
	}
	if err := os.WriteFile(mask, []byte("gitdir: attacker-controlled\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if request.Validate() == nil {
		t.Fatal("write request accepted a changed Git metadata mask")
	}
}

func TestDockerStandardCodeWriteRequestFailsClosedForUnprovenGitMetadata(t *testing.T) {
	ctx := context.Background()
	manifest, err := DockerStandardCodeManifest(
		dockerStandardCodeBindingFixture(DockerStandardCodeToolchainPython))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := CompileDockerContainerSpec(ctx,
		dockerContainerCompilerObservation(t, ctx, manifest, true, 8,
			8*1024*1024*1024), manifest)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	mask := filepath.Join(t.TempDir(), "git-metadata-mask")
	if err := os.WriteFile(mask, []byte(DockerStandardCodeGitMetadataMask), 0o444); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDockerStandardCodeContainerWriteRequest(ctx, root, mask, spec); err == nil {
		t.Fatal("Standard Code accepted Git metadata whose ownership shape was not proven")
	}
}
