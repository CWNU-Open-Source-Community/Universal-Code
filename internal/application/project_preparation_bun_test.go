package application

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/projectprep"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
)

func TestBunPreparationPinsOfflineDockerAndRejectsUnreadyTests(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{"package.json": `{"packageManager":"bun@1.4.3","scripts":{"test":"bun test"}}`, "bun.lock": `{"lockfileVersion":2,"configVersion":1,"workspaces":{"":{}},"packages":{}}`} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := projectprep.Detect(t.Context(), root, ".")
	if err != nil || !plan.Ready {
		t.Fatalf("plan=%+v %v", plan, err)
	}
	docker := commandruntimeadapter.SandboxedWorkspace(CommandRuntimeDockerSandboxBackend, "fixture", "generation")
	install, err := PrepareProjectPreparationCommand(plan, docker, root, false)
	if err != nil || install.Executable != sandbox.DockerStandardCodeBunExecutable || !slices.Contains(install.Arguments, "--offline") || !slices.Contains(install.Arguments, "--frozen-lockfile") || !slices.Contains(install.Arguments, "--ignore-scripts") || !slices.Contains(install.Arguments, "--backend=copyfile") || install.Network != runner.CommandRuntimeNetworkDisabled || install.Credentials != runner.CommandRuntimeCredentialsNone {
		t.Fatalf("install=%+v %v", install, err)
	}
	test, err := PrepareProjectPreparationTestCommand(plan, docker, root)
	if err != nil || strings.Join(test.Arguments, " ") != "run --no-install test" {
		t.Fatalf("test=%+v %v", test, err)
	}
	command, err := commandRuntimeDockerCommand(runner.CommandRuntimeResolvedSpec{ExecutablePath: install.Executable, CanonicalArgv: install.Arguments, Spec: install})
	if err != nil || command.Toolchain != sandbox.DockerStandardCodeToolchainBunPrepare {
		t.Fatal("Bun install lost preparation receipt", command, err)
	}
	command, err = commandRuntimeDockerCommand(runner.CommandRuntimeResolvedSpec{ExecutablePath: test.Executable, CanonicalArgv: test.Arguments, Spec: test})
	if err != nil || command.Toolchain != sandbox.DockerStandardCodeToolchainBun {
		t.Fatal("Bun test acquired installation profile", command, err)
	}
	for _, adapter := range []commandruntimeadapter.Identity{commandruntimeadapter.HostUnsandboxed("fixture"), commandruntimeadapter.SandboxedWorkspace(CommandRuntimeLocalSandboxBackend, "fixture", "generation"), commandruntimeadapter.SandboxedWorkspace(CommandRuntimeSBXBackend, "fixture", "generation")} {
		if _, err := PrepareProjectPreparationCommand(plan, adapter, root, false); err == nil || !strings.Contains(err.Error(), "1.4.3") {
			t.Fatal("unsupported adapter was ready", adapter, err)
		}
	}
	if _, err := PrepareProjectPreparationCommand(plan, docker, root, true); err == nil {
		t.Fatal("Bun lifecycle scripts enabled")
	}
	plan.Ready = false
	if _, err := PrepareProjectPreparationTestCommand(plan, docker, root); err == nil {
		t.Fatal("unreviewed Bun plan generated a test command")
	}
}

func TestBunGuestBindingAndOlderImageReadiness(t *testing.T) {
	f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionAsk, domain.RunExecutionProfileDocker, true)
	spec := runner.CommandRuntimeSpec{Version: runner.CommandRuntimeProtocolVersion, Profile: runner.CommandRuntimeProcess, Executable: sandbox.DockerStandardCodeBunExecutable, Arguments: []string{"install", "--offline", "--frozen-lockfile", "--ignore-scripts"}, WorkingDirectory: ".", Environment: []runner.CommandRuntimeEnvironment{}, StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true, TimeoutMilliseconds: 10000, Network: runner.CommandRuntimeNetworkDisabled, Credentials: runner.CommandRuntimeCredentialsNone, Output: runner.CommandRuntimeOutputPolicy{InlineBytes: 16384, ArtifactBytes: 524288}, Purpose: "Reviewed Bun fixture"}
	if _, err := f.service.normalizeCommandRuntimeSpec(spec, f.owned.Path); err != nil {
		t.Fatal("fixed Bun guest path rejected", err)
	}
	spec.Executable = "/opt/toolchains/bun/other/bun"
	if _, err := f.service.normalizeCommandRuntimeSpec(spec, f.owned.Path); err == nil {
		t.Fatal("foreign Bun path admitted")
	}
	if err := f.service.projectPreparationReady(t.Context(), "bun"); err == nil || !strings.Contains(err.Error(), "rebuild") || !strings.Contains(err.Error(), "1.4.3") {
		t.Fatal("old image was Bun-ready", err)
	}
	request, plan := strings.Repeat("a", 64), strings.Repeat("b", 64)
	if id, ok := detectedProjectActionPurpose("Detected project action test.bun "+request+" "+plan, request); !ok || id != "test.bun" {
		t.Fatal("Bun Job cannot be replayed", id, ok)
	}
}
