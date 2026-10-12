//go:build windows

package application

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/runner"
)

func TestLocalCommandProjectActionsMapsGoDistributionAndExternalNPM(t *testing.T) {
	for _, executable := range []string{"go.exe", "gofmt.exe", "node.exe"} {
		t.Run(executable, func(t *testing.T) {
			spec := localLaunchTestSpec(t)
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			filename := filepath.Join(root, executable)
			if executable != "node.exe" {
				filename = filepath.Join(root, "bin", executable)
				if err := os.MkdirAll(filepath.Join(root, "src", "runtime"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(root, "bin"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Rename(spec.ExecutablePath, filename); err != nil {
				t.Fatal(err)
			}
			spec.Spec.Executable = filename
			spec.Spec.Arguments = []string{"test", "./..."}
			if executable == "gofmt.exe" {
				spec.Spec.Arguments = []string{"-w", "./work/file.go"}
			}
			if executable == "node.exe" {
				npmRoot, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(npmRoot, "bin"), 0700); err != nil {
					t.Fatal(err)
				}
				launcher := filepath.Join(npmRoot, "bin", "npm-cli.js")
				if err := os.WriteFile(launcher, []byte("// installed CLI"), 0600); err != nil {
					t.Fatal(err)
				}
				spec.Spec.Arguments = []string{launcher, "test"}
			}
			// Pin the relocated fixture through the production normalizer, which
			// resolves Windows temporary-directory aliases before launch checks.
			spec, err = runner.NormalizeCommandRuntimeSpec(spec.Spec, spec.WorkspaceRoot)
			if err != nil {
				t.Fatal(err)
			}
			request, err := localLaunchTestCompile(spec)
			if err != nil {
				t.Fatal(err)
			}
			if request.Manifest.Command.WorkingDirectory != "/workspace/work" {
				t.Fatalf("test subdirectory lost: %+v", request.Manifest.Command)
			}
			if executable == "node.exe" {
				if len(request.ToolchainInputs) != 2 || request.Manifest.Command.Arguments[0] != "/toolchains/npm/bin/npm-cli.js" {
					t.Fatalf("external npm CLI was not bound read-only: %+v", request)
				}
			} else {
				if len(request.ToolchainInputs) != 1 || request.ToolchainInputs[0].Root != root || !strings.Contains(request.Manifest.Command.Executable, "/bin/"+executable) {
					t.Fatalf("Go standard library missing from binding: %+v", request)
				}
			}
		})
	}
}
