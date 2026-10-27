//go:build windows

package projectconfig

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestDiscoverInstructionsRejectsClaudeDirectoryJunction(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	writeInstruction(t, root, "AGENTS.md", "workspace guidance\n")
	writeInstruction(t, outside, "CLAUDE.md", "outside guidance\n")
	system, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, ".claude")
	command := exec.Command(filepath.Join(system, "cmd.exe"), "/d", "/c", "mklink", "/J", link, outside)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("temporary directory junction: %v %s", err, output)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
	if _, err := DiscoverInstructions(t.Context(), root, "."); err == nil || !strings.Contains(err.Error(), ".claude") {
		t.Fatalf("Claude instruction directory junction was not rejected: %v", err)
	}
}
