package skills

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitImportDoesNotExecuteInheritedHooksOrFilters(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	for _, source := range []string{"global-config", "environment-config", "template"} {
		t.Run(source, func(t *testing.T) {
			repo := t.TempDir()
			runGit(t, repo, "init", "--quiet")
			runGit(t, repo, "config", "user.name", "Inert import test")
			runGit(t, repo, "config", "user.email", "inert@example.invalid")
			runGit(t, repo, "config", "core.autocrlf", "false")
			runGit(t, repo, "config", "commit.gpgsign", "false")
			marker := filepath.Join(t.TempDir(), "executed")
			quotedMarker := "'" + strings.ReplaceAll(filepath.ToSlash(marker), "'", "'\\''") + "'"
			hook := []byte("#!/bin/sh\nprintf executed > " + quotedMarker + "\n")
			hookDir := filepath.Join(repo, ".githooks")
			if err := os.Mkdir(hookDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(hookDir, "post-checkout"), hook, 0o700); err != nil {
				t.Fatal(err)
			}
			original := []byte("exact resource\r\n")
			for name, content := range map[string][]byte{
				"SKILL.md":     []byte("---\nname: inert-import\ndescription: static import fixture\n---\n# Inert skill\n"),
				"resource.txt": original, ".gitattributes": []byte("*.txt -text filter=untrusted\n"),
			} {
				if err := os.WriteFile(filepath.Join(repo, name), content, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			runGit(t, repo, "add", ".")
			runGit(t, repo, "update-index", "--chmod=+x", ".githooks/post-checkout")
			runGit(t, repo, "commit", "--quiet", "-m", "pinned content")
			commit := strings.TrimSpace(runGitOutput(t, repo, "rev-parse", "HEAD"))
			if err := os.WriteFile(filepath.Join(repo, "resource.txt"), []byte("later revision\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runGit(t, repo, "add", "resource.txt")
			runGit(t, repo, "commit", "--quiet", "-m", "later content")

			global := filepath.Join(t.TempDir(), "gitconfig")
			switch source {
			case "global-config":
				runGit(t, repo, "config", "--file", global, "core.hooksPath", ".githooks")
				runGit(t, repo, "config", "--file", global, "filter.untrusted.smudge", "printf filtered > "+quotedMarker+"; cat")
				runGit(t, repo, "config", "--file", global, "filter.untrusted.required", "true")
				t.Setenv("GIT_CONFIG_GLOBAL", global)
			case "environment-config":
				t.Setenv("GIT_CONFIG_COUNT", "1")
				t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
				t.Setenv("GIT_CONFIG_VALUE_0", ".githooks")
			case "template":
				template := t.TempDir()
				if err := os.Mkdir(filepath.Join(template, "hooks"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(template, "hooks", "post-checkout"), hook, 0o700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("GIT_TEMPLATE_DIR", template)
			}
			staging := filepath.Join(t.TempDir(), "staging")
			if err := stageGitCommit(t.Context(), "file://"+filepath.ToSlash(repo), commit, staging); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("import executed inherited repository code: %v", err)
			}
			content, err := os.ReadFile(filepath.Join(staging, "resource.txt"))
			if err != nil || string(content) != string(original) {
				t.Fatalf("pinned resource bytes changed: %q %v", content, err)
			}
		})
	}
}
