package application

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/projectprep"
	"cyberagent-workbench/internal/runner"
)

func TestCargoPreparationAndPinnedTestUseLockedVendor(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{"Cargo.toml": "[package]\nname='app'\nversion='0.1.0'\n", "Cargo.lock": "version=4\n[[package]]\nname='app'\nversion='0.1.0'\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := projectprep.Detect(t.Context(), root, ".")
	if err != nil || !plan.Ready {
		t.Fatalf("plan=%+v %v", plan, err)
	}
	adapter := commandruntimeadapter.SandboxedWorkspace(CommandRuntimeDockerSandboxBackend, "fixture", "generation")
	install, err := PrepareProjectPreparationCommand(plan, adapter, root, false)
	if err != nil || install.Executable != "/usr/local/cargo/bin/cargo" || install.WorkingDirectory != "." || install.Arguments[0] != "fetch" || !slices.Contains(install.Arguments, "--offline") || !slices.Contains(install.Arguments, "--locked") || install.Network != runner.CommandRuntimeNetworkDisabled || install.Credentials != runner.CommandRuntimeCredentialsNone {
		t.Fatalf("install=%+v %v", install, err)
	}
	if !strings.Contains(strings.Join(install.Arguments, " "), projectprep.CargoVendorDirectory(".")) {
		t.Fatal("Cargo source is not the prepared vendor directory")
	}
	run := domain.Run{Config: pinActionConfig(projectconfig.Effective{Protocol: projectconfig.ProtocolVersion, TestCommandID: "test.rust"})}
	test, err := PrepareProjectCommandForAdapter(run, "test", ".", adapter, root)
	if err != nil || test.Arguments[0] != "test" || !slices.Contains(test.Arguments, "--locked") || !slices.Contains(test.Arguments, `source.crates-io.replace-with="traverse-vendor"`) {
		t.Fatalf("test=%+v %v", test, err)
	}
	if _, err := PrepareProjectPreparationCommand(plan, commandruntimeadapter.HostUnsandboxed("fixture"), root, false); err == nil {
		t.Fatal("Cargo preparation granted Host execution")
	}
	local := commandruntimeadapter.SandboxedWorkspace(CommandRuntimeLocalSandboxBackend, "fixture", "generation")
	if _, err := PrepareProjectPreparationTestCommand(plan, local, root); err == nil || !strings.Contains(err.Error(), "完整 Rust") {
		t.Fatal("Local Rust failure was not explained before dispatch", err)
	}
	if _, err := PrepareProjectCommandForAdapter(run, "test", ".", local, root); err == nil || !strings.Contains(err.Error(), "Docker") {
		t.Fatal("configured Local Rust test bypassed readiness", err)
	}
}

func TestDetectedCargoActionPurposeBindsReplayIdentity(t *testing.T) {
	request, plan := strings.Repeat("a", 64), strings.Repeat("b", 64)
	purpose := "Detected project action test.rust " + request + " " + plan
	if id, ok := detectedProjectActionPurpose(purpose, request); !ok || id != "test.rust" {
		t.Fatal("Cargo action cannot be replayed", id, ok)
	}
	if _, ok := detectedProjectActionPurpose(purpose, strings.Repeat("c", 64)); ok {
		t.Fatal("different Cargo request borrowed the receipt")
	}
}
