//go:build windows

package application

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalCommandProjectActionsMapsGoDistributionAndExternalNPM(t *testing.T) {
	for _, executable := range []string{"go.exe", "gofmt.exe", "node.exe"} {
		t.Run(executable, func(t *testing.T) {
			spec := localLaunchTestSpec(t)
			root := t.TempDir()
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
			spec.Spec.Executable, spec.ExecutablePath = filename, filename
			spec.Spec.Arguments, spec.CanonicalArgv = []string{"test", "./..."}, []string{"test", "./..."}
			if executable == "gofmt.exe" {
				spec.Spec.Arguments, spec.CanonicalArgv = []string{"-w", "./work/file.go"}, []string{"-w", "./work/file.go"}
			}
			if executable == "node.exe" {
				npmRoot := t.TempDir()
				if err := os.Mkdir(filepath.Join(npmRoot, "bin"), 0700); err != nil {
					t.Fatal(err)
				}
				launcher := filepath.Join(npmRoot, "bin", "npm-cli.js")
				if err := os.WriteFile(launcher, []byte("// installed CLI"), 0600); err != nil {
					t.Fatal(err)
				}
				spec.Spec.Arguments, spec.CanonicalArgv = []string{launcher, "test"}, []string{launcher, "test"}
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
