//go:build windows

package application

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/runner"
	"golang.org/x/sys/windows"
)

func TestLocalCommandLaunchRejectsFilesystemAliasDrift(t *testing.T) {
	for _, target := range []string{"executable parent", "cwd"} {
		t.Run(target, func(t *testing.T) {
			spec := localLaunchTestSpec(t)
			if _, err := localLaunchTestCompile(spec); err != nil {
				t.Fatal(err)
			}
			original := spec.AbsoluteDirectory
			if target == "executable parent" {
				original = filepath.Dir(spec.ExecutablePath)
			}
			moved := original + "-moved"
			if err := os.Rename(original, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, original); err != nil {
				// Match the existing runner alias regression: directory junctions
				// do not require Windows' optional symbolic-link privilege.
				system, systemErr := windows.GetSystemDirectory()
				if systemErr != nil {
					t.Fatal(systemErr)
				}
				command := exec.Command(filepath.Join(system, "cmd.exe"), "/d", "/c", "mklink", "/J", original, moved)
				hideSubprocessWindow(command)
				if output, junctionErr := command.CombinedOutput(); junctionErr != nil {
					t.Fatalf("create temporary directory alias: symlink=%v junction=%v output=%s", err, junctionErr, output)
				}
			}
			// The same native bytes still exist at the old spelling. A digest-
			// only check would accept the changed directory authority.
			request, err := localLaunchTestCompile(spec)
			if !errors.Is(err, runner.ErrCommandRuntimeBoundary) || request.Manifest.Command.Executable != "" {
				t.Fatalf("alias drift produced a runnable manifest: executable=%q err=%v", request.Manifest.Command.Executable, err)
			}
		})
	}
}
