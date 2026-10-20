//go:build !windows

package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandRuntimePlatformPythonVenv(t *testing.T) {
	resolved, venv := commandRuntimePythonVenvFixture(t)
	sites, err := filepath.Glob(filepath.Join(venv, "lib", "python*", "site-packages"))
	if err != nil || len(sites) != 1 {
		t.Fatalf("venv site-packages: %v %v", sites, err)
	}
	if err := os.WriteFile(filepath.Join(sites[0], "venv_dependency.py"), []byte("VALUE = 42\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prefix, _ := json.Marshal(venv)
	test := "import sys, unittest, venv_dependency\nclass VenvTest(unittest.TestCase):\n def test_environment(self):\n  self.assertEqual(venv_dependency.VALUE, 42)\n  self.assertEqual(sys.prefix, " + string(prefix) + ")\n"
	if err := os.WriteFile(filepath.Join(resolved.AbsoluteDirectory, "test_venv.py"), []byte(test), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy := resolved
	legacy.Spec.Executable = legacy.ExecutablePath
	if code, output := runCommandRuntimePythonVenv(t, legacy); code != 1 || !strings.Contains(output, "ModuleNotFoundError") {
		t.Fatalf("canonical-only launch did not expose the venv regression: exit=%d output=%s", code, output)
	}
	if code, output := runCommandRuntimePythonVenv(t, resolved); code != 0 || !strings.Contains(output, "OK") {
		t.Fatalf("venv launch failed: exit=%d output=%s", code, output)
	} else {
		t.Logf("canonical image=%s sha256=%s invocation=%s\n%s", resolved.ExecutablePath, resolved.ExecutableSHA256, resolved.Spec.Executable, output)
	}
}

func TestCommandRuntimePlatformPythonVenvBindings(t *testing.T) {
	resolved, _ := commandRuntimePythonVenvFixture(t)
	if invocation := commandRuntimePythonInvocationPath(resolved); invocation != resolved.Spec.Executable {
		t.Fatalf("invocation path was lost: %q", invocation)
	}
	var intent struct {
		InvocationPath string `json:"invocation_path"`
	}
	if err := json.Unmarshal([]byte(commandRuntimeIntentJSON(resolved)), &intent); err != nil || intent.InvocationPath != resolved.Spec.Executable {
		t.Fatalf("stored intent did not bind the invocation: %+v %v", intent, err)
	}
	alias := filepath.Join(t.TempDir(), "python3")
	if err := os.Symlink(resolved.ExecutablePath, alias); err != nil {
		t.Fatal(err)
	}
	other := resolved.Spec
	other.Executable = alias
	second, err := NormalizeCommandRuntimeSpec(other, resolved.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if second.ExecutablePath != resolved.ExecutablePath || second.ExecutableSHA256 != resolved.ExecutableSHA256 ||
		CommandRuntimeSpecFingerprint(second) == CommandRuntimeSpecFingerprint(resolved) {
		t.Fatal("different invocation paths did not retain one image and distinct fingerprints")
	}
	legacy := resolved
	legacy.Spec.Executable = legacy.ExecutablePath
	if strings.Contains(commandRuntimeIntentJSON(legacy), "invocation_path") ||
		CommandRuntimeSpecFingerprint(legacy) == CommandRuntimeSpecFingerprint(resolved) {
		t.Fatal("the historical canonical-only binding can authorize new venv semantics")
	}
	store := newCommandRuntimeMemoryStore()
	starter := &commandRuntimeFakeStarter{}
	manager, err := NewCommandRuntimeManager(store, starter, "python-venv-binding-owner")
	if err != nil {
		t.Fatal(err)
	}
	request := commandRuntimeTestRequest(manager, 5000)
	request.Spec = legacy
	request.Scope.WorkspaceRootSHA256 = legacy.WorkspaceRootSHA256
	job, replayed, err := manager.Start(t.Context(), request)
	if err != nil || replayed {
		t.Fatalf("prepare historical binding: replayed=%v err=%v", replayed, err)
	}
	starter.last().finish(0)
	waitCommandRuntimeTerminal(t, manager, job.ID)
	request.Spec = resolved
	if _, replayed, err := manager.Start(t.Context(), request); !replayed || !errors.Is(err, ErrCommandRuntimeUncertain) || starter.starts != 1 {
		t.Fatalf("historical authorization reused new argv[0]: replayed=%v starts=%d err=%v", replayed, starter.starts, err)
	}
}

func TestCommandRuntimePlatformPythonVenvRejectsAliasReplacement(t *testing.T) {
	for _, timing := range []string{"before-launch", "during-dispatch"} {
		t.Run(timing, func(t *testing.T) {
			resolved, _ := commandRuntimePythonVenvFixture(t)
			if err := ValidateCommandRuntimeLaunchSpec(resolved); err != nil {
				t.Fatal(err)
			}
			replace := func() {
				t.Helper()
				executable, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(resolved.Spec.Executable); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(executable, resolved.Spec.Executable); err != nil {
					t.Fatal(err)
				}
			}
			ctx := t.Context()
			if timing == "before-launch" {
				replace()
				if err := ValidateCommandRuntimeLaunchSpec(resolved); !errors.Is(err, ErrCommandRuntimeBoundary) {
					t.Fatalf("adapter preflight accepted replaced Python alias: %v", err)
				}
			} else {
				ctx = withCommandRuntimeDispatchCheck(ctx, func(context.Context, CommandRuntimeResolvedSpec) error { replace(); return nil })
			}
			if process, err := newPlatformCommandRuntimeStarter().Start(ctx, CommandRuntimeScope{}, resolved); !errors.Is(err, ErrCommandRuntimeBoundary) || process != nil {
				if process != nil {
					_ = process.Kill()
					_ = process.Close()
				}
				t.Fatalf("starter accepted replaced Python alias: %v", err)
			}
		})
	}
}

func commandRuntimePythonVenvFixture(t *testing.T) (CommandRuntimeResolvedSpec, string) {
	t.Helper()
	python := os.Getenv("TRAVERSE_TEST_PYTHON_PATH")
	if python == "" {
		var err error
		python, err = exec.LookPath("python3")
		if err != nil {
			t.Skip("python3 is unavailable")
		}
	}
	base := t.TempDir()
	venv := filepath.Join(base, "venv")
	command := exec.Command(python, "-m", "venv", "--without-pip", "--symlinks", venv)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create real Python venv: %v\n%s", err, output)
	}
	venv, err := filepath.EvalSymlinks(venv)
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(base, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	spec := CommandRuntimeSpec{
		Version: CommandRuntimeProtocolVersion, Profile: CommandRuntimeProcess,
		Executable: filepath.Join(venv, "bin", "python3"), Arguments: []string{"-m", "unittest", "discover", "-v"},
		WorkingDirectory: ".", Environment: []CommandRuntimeEnvironment{},
		StdinPolicy: CommandRuntimeStdinClosed, CloseInitialStdin: true, TimeoutMilliseconds: 5000,
		Output:  CommandRuntimeOutputPolicy{InlineBytes: MinCommandRuntimeInlineBytes, ArtifactBytes: MinCommandRuntimeInlineBytes},
		Network: CommandRuntimeNetworkDisabled, Credentials: CommandRuntimeCredentialsNone, Purpose: "test Python venv execution",
	}
	resolved, err := NormalizeCommandRuntimeSpec(spec, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if commandRuntimePathEqual(resolved.Spec.Executable, resolved.ExecutablePath) {
		t.Fatal("venv interpreter is not a symlink")
	}
	return resolved, venv
}

func runCommandRuntimePythonVenv(t *testing.T, spec CommandRuntimeResolvedSpec) (int, string) {
	t.Helper()
	process, err := newPlatformCommandRuntimeStarter().Start(t.Context(), CommandRuntimeScope{}, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	command := process.(*unixCommandRuntimeProcess).command
	if command.Path != spec.ExecutablePath || command.Args[0] != spec.Spec.Executable {
		_ = process.Kill()
		t.Fatalf("launch identity changed: path=%s argv=%q", command.Path, command.Args)
	}
	stdout, outErr := io.ReadAll(process.Stdout())
	stderr, errErr := io.ReadAll(process.Stderr())
	code, waitErr := process.Wait()
	if outErr != nil || errErr != nil || waitErr != nil {
		t.Fatal(errors.Join(outErr, errErr, waitErr))
	}
	return code, fmt.Sprintf("%s%s", stdout, stderr)
}
