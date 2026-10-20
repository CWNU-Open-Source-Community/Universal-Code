package application

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
)

func TestNativeLanguageProjectTestsUseSelectedToolchainAndPinnedConfiguration(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".prayu"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "tests"), 0700); err != nil {
		t.Fatal(err)
	}
	toolchain := t.TempDir()
	pythonName, suffix := "python3", ""
	if runtime.GOOS == "windows" {
		pythonName, suffix = "python", ".exe"
	}
	for _, name := range []string{pythonName, "cargo"} {
		if err := os.WriteFile(filepath.Join(toolchain, name+suffix), []byte("intent only; never executed"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", toolchain)
	for _, tc := range []struct {
		id, name, guest string
		args            []string
	}{
		{"test.python", pythonName, "/opt/toolchains/python/usr-local/bin/python3", []string{"-m", "unittest", "discover", "-v"}},
		{"test.pytest", pythonName, "/opt/toolchains/python/usr-local/bin/python3", []string{"-m", "pytest"}},
		{"test.rust", "cargo", "/usr/local/cargo/bin/cargo", []string{"test", "--offline"}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			configPath := filepath.Join(root, ".prayu", "config.yaml")
			body := "protocol: project_config.v1\ntest_command_id: " + tc.id + "\nexclude_paths: [private/**]\nhooks:\n  before_finish_test: true\n"
			if err := os.WriteFile(configPath, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			project, rejected, err := projectconfig.ResolveWorkspace(t.Context(), root, domain.ProfileCode, domain.Budget{}, toolgateway.TypedActionIDs())
			if err != nil || len(rejected) != 0 || project == nil || project.HooksFingerprint() == "" {
				t.Fatalf("configuration rejected: %v %v", rejected, err)
			}
			run := domain.Run{Config: pinActionConfig(*project)}
			// Later workspace edits cannot substitute commands into the pinned task.
			if err := os.WriteFile(configPath, []byte("test_command_id: shell\n"), 0600); err != nil {
				t.Fatal(err)
			}
			for _, selected := range []struct {
				adapter    commandruntimeadapter.Identity
				executable string
			}{
				{commandruntimeadapter.HostUnsandboxed("native-tests"), filepath.Join(toolchain, tc.name+suffix)},
				{commandruntimeadapter.SandboxedWorkspace(CommandRuntimeDockerSandboxBackend, "docker-standard-code-guest.v1", "guest"), tc.guest},
			} {
				spec, err := PrepareProjectCommandForAdapter(run, "test", "tests", selected.adapter, root)
				if err != nil || spec.Executable != selected.executable || !reflect.DeepEqual(spec.Arguments, tc.args) || spec.WorkingDirectory != "tests" || spec.Network != runner.CommandRuntimeNetworkDisabled || spec.Credentials != runner.CommandRuntimeCredentialsNone {
					t.Fatalf("selected toolchain changed: %+v %v", spec, err)
				}
				if _, err := PrepareProjectCommandForAdapter(run, "test", "private", selected.adapter, root); apperror.CodeOf(err) != apperror.CodePolicyDenied {
					t.Fatalf("excluded directory accepted: %v", err)
				}
				if _, err := PrepareProjectCommandForAdapter(run, "test", "../outside", selected.adapter, root); err == nil {
					t.Fatal("escaping directory accepted")
				}
			}
			sbx := commandruntimeadapter.SandboxedWorkspace(CommandRuntimeSBXBackend, "sbx-template", "guest")
			if _, err := PrepareProjectCommandForAdapter(run, "test", ".", sbx, root); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
				t.Fatalf("unsupported SBX preset fell back to host: %v", err)
			}
		})
	}
}

func TestNativeLanguageProjectTestsMissingHostToolchainDoesNotChangeAdapter(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, id := range []string{"test.python", "test.pytest", "test.rust"} {
		run := domain.Run{Config: pinActionConfig(projectconfig.Effective{Protocol: projectconfig.ProtocolVersion, TestCommandID: id})}
		if _, err := PrepareProjectCommandForAdapter(run, "test", ".", commandruntimeadapter.HostUnsandboxed("missing")); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
			t.Fatalf("missing %s accepted: %v", id, err)
		}
		docker := commandruntimeadapter.SandboxedWorkspace(CommandRuntimeDockerSandboxBackend, "docker-standard-code-guest.v1", "guest")
		if _, err := PrepareProjectCommandForAdapter(run, "test", ".", docker); err != nil {
			t.Fatalf("Docker depended on host PATH: %v", err)
		}
	}
}
