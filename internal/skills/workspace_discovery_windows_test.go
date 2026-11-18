//go:build windows

package skills

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func createJunction(t *testing.T, link, target string) {
	t.Helper()
	command := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, target)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("controlled temporary fixture junction: %v %s", err, output)
	}
}

func TestDiscoverWorkspaceSkillsIgnoresJunctionedSkillDirectory(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceSkill(t, outside, "outside-skill", "Must never surface")
	createJunction(t, filepath.Join(root, "skills", "outside-skill"), filepath.Join(outside, "outside-skill"))
	report, err := DiscoverWorkspaceSkills(t.Context(), root)
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}
	if len(report.Candidates) != 0 {
		t.Fatalf("junctioned skill directory must not surface as a candidate: %+v", report.Candidates)
	}
	if len(report.Ignored) != 1 || !strings.Contains(report.Ignored[0].Reason, "junction") {
		t.Fatalf("junction must be reported as ignored: %+v", report.Ignored)
	}
}

func TestDiscoverWorkspaceSkillsIgnoresJunctionedRoot(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	createJunction(t, filepath.Join(root, "skills"), outside)
	report, err := DiscoverWorkspaceSkills(t.Context(), root)
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}
	if len(report.Candidates) != 0 || len(report.Ignored) != 1 {
		t.Fatalf("junctioned skills root must be ignored: %+v", report)
	}
	if report.Ignored[0].Directory != "skills" {
		t.Fatalf("root ignore entry must address the root: %+v", report.Ignored[0])
	}
}
