package application_test

import (
	"slices"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/projectprep"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
)

func TestYarnPreparationUsesFixedOfflineDockerToolchain(t *testing.T) {
	plan := projectprep.Plan{Version: projectprep.ProtocolVersion, Fingerprint: strings.Repeat("a", 64), Ready: true, Ecosystem: "yarn", Target: "packages/web", RequestedVersion: projectprep.YarnVersion, Test: projectprep.Command{ID: "test.yarn"}}
	adapter := commandruntimeadapter.SandboxedWorkspace(application.CommandRuntimeDockerSandboxBackend, "fixture.v1", "generation")
	for _, testingProject := range []bool{false, true} {
		spec, err := application.PrepareProjectPreparationCommand(plan, adapter, t.TempDir(), false)
		if testingProject {
			spec, err = application.PrepareProjectPreparationTestCommand(plan, adapter, t.TempDir())
		}
		if err != nil || spec.Executable != "/opt/toolchains/node/bin/node" || spec.Arguments[0] != sandbox.DockerStandardCodeYarnCLI || spec.WorkingDirectory != plan.Target || spec.Network != runner.CommandRuntimeNetworkDisabled || spec.Credentials != runner.CommandRuntimeCredentialsNone || len(spec.Environment) != 0 {
			t.Fatalf("command %+v %v", spec, err)
		}
		for _, required := range []string{"--no-default-rc", "--use-yarnrc", "--cache-folder", "--offline", "--non-interactive"} {
			if !slices.Contains(spec.Arguments, required) {
				t.Fatal("missing fixed argument", required, spec.Arguments)
			}
		}
		if !testingProject && (!slices.Contains(spec.Arguments, "--ignore-scripts") || !slices.Contains(spec.Arguments, "--frozen-lockfile")) {
			t.Fatal("install may run lifecycle scripts or change lock", spec.Arguments)
		}
		if testingProject && strings.Join(spec.Arguments[len(spec.Arguments)-2:], " ") != "run test" {
			t.Fatal("test arguments forwarded to script", spec.Arguments)
		}
	}
	for _, adapter := range []commandruntimeadapter.Identity{commandruntimeadapter.HostUnsandboxed("host"), commandruntimeadapter.SandboxedWorkspace(application.CommandRuntimeLocalSandboxBackend, "fixture.v1", "generation"), commandruntimeadapter.SandboxedWorkspace(application.CommandRuntimeSBXBackend, "fixture.v1", "generation")} {
		if _, err := application.PrepareProjectPreparationCommand(plan, adapter, t.TempDir(), false); err == nil {
			t.Fatal("unsupported runtime admitted")
		}
	}
}
