package skills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeWorkspaceSkill(t *testing.T, root, name, description string) string {
	t.Helper()
	skillDir := filepath.Join(root, "skills", name)
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# Instructions\n"
	skillMD := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillMD, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return skillMD
}

func TestDiscoverWorkspaceSkillsWithoutRootIsEmpty(t *testing.T) {
	root := t.TempDir()
	report, err := DiscoverWorkspaceSkills(context.Background(), root)
	if err != nil {
		t.Fatalf("discovery without a skills root must not fail: %v", err)
	}
	if report.RootPresent || len(report.Candidates) != 0 || len(report.Ignored) != 0 {
		t.Fatalf("missing root must yield an empty report: %+v", report)
	}
	if report.ProtocolVersion != WorkspaceSkillDiscoveryProtocolVersion {
		t.Fatalf("unexpected protocol version %q", report.ProtocolVersion)
	}
}

func TestDiscoverWorkspaceSkillsReportsCandidate(t *testing.T) {
	root := t.TempDir()
	skillMD := writeWorkspaceSkill(t, root, "recon-notes", "Guided reconnaissance checklist")
	report, err := DiscoverWorkspaceSkills(context.Background(), root)
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}
	if !report.RootPresent || len(report.Candidates) != 1 || len(report.Ignored) != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
	candidate := report.Candidates[0]
	if candidate.Directory != "skills/recon-notes" {
		t.Fatalf("directory must stay workspace-relative and slash-separated: %q", candidate.Directory)
	}
	if candidate.Name != "recon-notes" || candidate.Description != "Guided reconnaissance checklist" {
		t.Fatalf("unexpected summary: %+v", candidate)
	}
	if candidate.SkillMDPath != "SKILL.md" {
		t.Fatalf("unexpected skill content path %q", candidate.SkillMDPath)
	}
	raw, err := os.ReadFile(skillMD)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	if candidate.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("content digest %q does not pin SKILL.md bytes", candidate.SHA256)
	}
}

func TestDiscoverWorkspaceSkillsIgnoresMismatchedName(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "skills", "wrong-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "wrong-dir", "SKILL.md"),
		[]byte("---\nname: recon\ndescription: mismatched\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := DiscoverWorkspaceSkills(context.Background(), root)
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}
	if len(report.Candidates) != 0 || len(report.Ignored) != 1 {
		t.Fatalf("mismatched name must be ignored, not parsed: %+v", report)
	}
	if !strings.Contains(report.Ignored[0].Reason, "skill_name_invalid") {
		t.Fatalf("reason must name the failure: %q", report.Ignored[0].Reason)
	}
}

func TestDiscoverWorkspaceSkillsIgnoresDirectoryWithoutSkillMD(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "skills", "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	report, err := DiscoverWorkspaceSkills(context.Background(), root)
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}
	if len(report.Candidates) != 0 || len(report.Ignored) != 1 {
		t.Fatalf("directory without SKILL.md must be ignored: %+v", report)
	}
}

func TestDiscoverWorkspaceSkillsIgnoresSymlinkedDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "outside"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(root, "skills", "link")); err != nil {
		t.Skip("symlinks are unavailable in this environment")
	}
	report, err := DiscoverWorkspaceSkills(context.Background(), root)
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}
	if len(report.Candidates) != 0 || len(report.Ignored) != 1 {
		t.Fatalf("symlinked skill directory must be ignored: %+v", report)
	}
	if !strings.Contains(report.Ignored[0].Reason, "symlink") {
		t.Fatalf("reason must say the symlink was skipped: %q", report.Ignored[0].Reason)
	}
}

func TestDiscoverWorkspaceSkillsIgnoresSymlinkedRoot(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(root, "skills")); err != nil {
		t.Skip("symlinks are unavailable in this environment")
	}
	report, err := DiscoverWorkspaceSkills(context.Background(), root)
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}
	if !report.RootPresent || len(report.Candidates) != 0 || len(report.Ignored) != 1 {
		t.Fatalf("symlinked root must be ignored: %+v", report)
	}
	if report.Ignored[0].Directory != "skills" {
		t.Fatalf("root ignore entry must address the root: %+v", report.Ignored[0])
	}
}

func TestDiscoverWorkspaceSkillsIgnoresLegacyPackage(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "legacy")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "manifest.json"),
		[]byte(`{"protocol": "skill.v1", "name": "legacy"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: legacy\ndescription: legacy format\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := DiscoverWorkspaceSkills(context.Background(), root)
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}
	if len(report.Candidates) != 0 || len(report.Ignored) != 1 {
		t.Fatalf("legacy package must be ignored: %+v", report)
	}
	if !strings.Contains(report.Ignored[0].Reason, "legacy") {
		t.Fatalf("reason must point at the legacy import path: %q", report.Ignored[0].Reason)
	}
}

func TestDiscoverWorkspaceSkillsRejectsOverLimitEntries(t *testing.T) {
	root := t.TempDir()
	skillsRoot := filepath.Join(root, "skills")
	if err := os.MkdirAll(skillsRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= MaxWorkspaceSkillEntries; i++ {
		if err := os.MkdirAll(filepath.Join(skillsRoot, "dir-"+strings.Repeat("a", i+1)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := DiscoverWorkspaceSkills(context.Background(), root); err == nil {
		t.Fatal("entries beyond the bound must fail closed")
	}
}
