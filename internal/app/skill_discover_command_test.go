package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/skills"
)

func seedWorkspaceSkill(t *testing.T, name string) string {
	t.Helper()
	home := os.Getenv("CYBERAGENT_HOME")
	root := filepath.Join(home, "workspaces", name)
	skillMD := filepath.Join(root, "skills", "recon-notes", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillMD), 0o700); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: recon-notes\ndescription: Guided reconnaissance checklist\n---\n\n# Instructions\n"
	if err := os.WriteFile(skillMD, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return skillMD
}

func TestSkillDiscoverCLIReportsUntrustedCandidates(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	if _, stderr, code := executeTestCommand(t, "workspace", "init", "discover-demo"); code != 0 || stderr != "" {
		t.Fatalf("workspace init failed: stderr=%q code=%d", stderr, code)
	}
	skillMD := seedWorkspaceSkill(t, "discover-demo")
	stdout, stderr, code := executeTestCommand(t, "skill", "discover", "--workspace", "discover-demo", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("skill discover failed: stdout=%q stderr=%q code=%d", stdout, stderr, code)
	}
	var report skills.WorkspaceSkillDiscovery
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("discovery output is not the report JSON: %v\n%s", err, stdout)
	}
	if report.ProtocolVersion != skills.WorkspaceSkillDiscoveryProtocolVersion || !report.RootPresent {
		t.Fatalf("unexpected report: %+v", report)
	}
	if len(report.Candidates) != 1 || report.Candidates[0].Directory != "skills/recon-notes" {
		t.Fatalf("unexpected candidates: %+v", report.Candidates)
	}
	raw, err := os.ReadFile(skillMD)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	if report.Candidates[0].SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("JSON digest does not pin SKILL.md bytes: %q", report.Candidates[0].SHA256)
	}
	if strings.Contains(stdout, "# Instructions") {
		t.Fatal("discovery output must never include skill instruction bodies")
	}
}

func TestSkillDiscoverCLITextKeepsUntrustedBoundary(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	if _, stderr, code := executeTestCommand(t, "workspace", "init", "discover-text"); code != 0 || stderr != "" {
		t.Fatalf("workspace init failed: stderr=%q code=%d", stderr, code)
	}
	seedWorkspaceSkill(t, "discover-text")
	stdout, stderr, code := executeTestCommand(t, "skill", "discover", "--workspace", "discover-text")
	if code != 0 || stderr != "" {
		t.Fatalf("skill discover failed: stdout=%q stderr=%q code=%d", stdout, stderr, code)
	}
	for _, want := range []string{
		"root_present: true",
		"candidate_count: 1",
		"skills/recon-notes\trecon-notes\t",
		"untrusted: workspace files are untrusted data",
		"skill import-dir",
		"--confirm-untrusted-skill",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("text output missing %q:\n%s", want, stdout)
		}
	}
}

func TestSkillDiscoverCLIIgnoresMissingRootQuietly(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	if _, stderr, code := executeTestCommand(t, "workspace", "init", "discover-empty"); code != 0 || stderr != "" {
		t.Fatalf("workspace init failed: stderr=%q code=%d", stderr, code)
	}
	stdout, stderr, code := executeTestCommand(t, "skill", "discover", "--workspace", "discover-empty")
	if code != 0 || stderr != "" {
		t.Fatalf("discovery without a skills root must succeed: stderr=%q code=%d", stderr, code)
	}
	if !strings.Contains(stdout, "root_present: false") || !strings.Contains(stdout, "candidate_count: 0") {
		t.Fatalf("missing root must be reported as empty: %q", stdout)
	}
}

func TestSkillDiscoverCLIRequiresWorkspace(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	_, stderr, code := executeTestCommand(t, "skill", "discover")
	if code == 0 || !strings.Contains(stderr, "usage: cyberagent skill discover --workspace <name>") {
		t.Fatalf("missing workspace must return usage: stderr=%q code=%d", stderr, code)
	}
	_, stderr, code = executeTestCommand(t, "skill", "discover", "--workspace", "missing-workspace")
	if code == 0 {
		t.Fatalf("unknown workspace must fail: stderr=%q", stderr)
	}
}
