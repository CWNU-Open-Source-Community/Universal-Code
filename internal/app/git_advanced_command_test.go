package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/store"
)

func TestGitAdvancedCLIStatusPrintsJSONByDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CYBERAGENT_HOME", home)
	if _, stderr, code := executeTestCommand(t, "workspace", "init", "git-status"); code != 0 {
		t.Fatalf("workspace init failed: %s", stderr)
	}
	root := filepath.Join(home, "workspaces", "git-status")
	runGitAdvancedCLITestGit(t, root, "init", "-b", "main")
	runGitAdvancedCLITestGit(t, root, "config", "user.email", "test@example.invalid")
	runGitAdvancedCLITestGit(t, root, "config", "user.name", "Git Status Test")
	runGitAdvancedCLITestGit(t, root, "commit", "--allow-empty", "-m", "base")
	created, stderr, code := executeTestCommand(t, "run", "create", "advanced git status",
		"--workspace", "git-status", "--profile", "code", "--surface", "code")
	runID := runIDPattern.FindString(created)
	if code != 0 || runID == "" {
		t.Fatalf("run create output=%q stderr=%q code=%d", created, stderr, code)
	}
	for _, flags := range [][]string{nil, {"--json"}} {
		args := []string{"git-advanced", "status", "--run", runID,
			"--enable-git-advanced", "--enable-permission-control"}
		stdout, stderr, code := executeTestCommand(t, append(args, flags...)...)
		if code != 0 || stderr != "" {
			t.Fatalf("status %v output=%q stderr=%q code=%d", flags, stdout, stderr, code)
		}
		var projection application.GitAdvancedProjection
		if err := json.Unmarshal([]byte(stdout), &projection); err != nil || projection.RunID != runID ||
			projection.ProtocolVersion != application.GitAdvancedAPIProtocolVersion || projection.WorkspaceID == "" {
			t.Fatalf("status %v projection=%#v err=%v", flags, projection, err)
		}
	}
}

func TestGitAdvancedCLIRequiresExplicitCapabilityFlagsAndRejectsRawArgv(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	if _, stderr, code := executeTestCommand(t, "git-advanced", "status",
		"--run", "run-placeholder"); code == 0 ||
		!strings.Contains(stderr, "--enable-git-advanced") {
		t.Fatalf("missing startup gates stderr=%q code=%d", stderr, code)
	}
	if _, stderr, code := executeTestCommand(t, "git-advanced", "preview", "hunk_stage",
		"--run", "run-placeholder", "--enable-git-advanced",
		"--enable-permission-control", "--argv", "reset --hard"); code == 0 ||
		!strings.Contains(stderr, "flag provided but not defined") {
		t.Fatalf("raw argv was not rejected stderr=%q code=%d", stderr, code)
	}
	if _, stderr, code := executeTestCommand(t, "git-advanced", "preview", "force_push",
		"--run", "run-placeholder", "--enable-git-advanced",
		"--enable-permission-control"); code == 0 ||
		!strings.Contains(stderr, "closed git-advanced.v1 schema") {
		t.Fatalf("unknown operation was not rejected stderr=%q code=%d", stderr, code)
	}
}

func TestGitAdvancedCLIStashPreviewIsNonAuthorizingAndConfirmedRunIsCheckpointed(t *testing.T) {
	for _, mode := range []string{"ask", "auto", "full"} {
		t.Run(mode, func(t *testing.T) { testGitAdvancedCLIPermission(t, mode) })
	}
}

func testGitAdvancedCLIPermission(t *testing.T, mode string) {
	home := t.TempDir()
	t.Setenv("CYBERAGENT_HOME", home)
	if _, stderr, code := executeTestCommand(t, "workspace", "init", "git-advanced-cli"); code != 0 {
		t.Fatalf("workspace init failed: %s", stderr)
	}
	root := filepath.Join(home, "workspaces", "git-advanced-cli")
	runGitAdvancedCLITestGit(t, root, "init", "-b", "main")
	runGitAdvancedCLITestGit(t, root, "config", "user.email", "test@example.invalid")
	runGitAdvancedCLITestGit(t, root, "config", "user.name", "Git Advanced Test")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitAdvancedCLITestGit(t, root, "add", "tracked.txt")
	runGitAdvancedCLITestGit(t, root, "commit", "-m", "base")

	created, stderr, code := executeTestCommand(t, "run", "create", "advanced git CLI",
		"--workspace", "git-advanced-cli", "--profile", "code", "--surface", "code",
		"--phase", "deliver", "--max-turns", "3")
	if code != 0 || stderr != "" {
		t.Fatalf("run create output=%q stderr=%q code=%d", created, stderr, code)
	}
	runID := runIDPattern.FindString(created)
	if runID == "" {
		t.Fatalf("missing Run identity: %s", created)
	}
	if _, stderr, code = executeTestCommand(t, "run", "execution-profile", "set", runID,
		"local", "--operation-key", "git-advanced-cli-profile-0001"); code != 0 {
		t.Fatalf("local execution profile failed: %s", stderr)
	}
	if shown, stderr, code := executeTestCommand(t, "run", "execution-permission", runID); code != 0 || stderr != "" || !strings.Contains(shown, "mode: ask") {
		t.Fatalf("initial Ask permission=%s stderr=%s code=%d", shown, stderr, code)
	}
	if mode != "ask" {
		change := []string{"run", "execution-permission", "set", runID, mode,
			"--operation-key", "cli-permission-" + mode, "--enable-permission-control"}
		if mode == "full" {
			change = append(change, "--enable-danger-full-access", "--confirm-full")
		}
		if _, stderr, code := executeTestCommand(t, change...); code != 0 {
			t.Fatalf("set current %s preference: code=%d stderr=%s", mode, code, stderr)
		}
	}
	if _, stderr, code = executeTestCommand(t, "run", "start", runID); code != 0 {
		t.Fatalf("run start failed: %s", stderr)
	}
	state, err := store.Open(filepath.Join(home, "cyberagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = state.AcquireRunExecutionLease(context.Background(),
		domain.AcquireRunExecutionLeaseRequest{RunID: runID,
			OwnerID: "git-advanced-cli-fixture", TTL: 5 * time.Minute}); err != nil {
		_ = state.Close()
		t.Fatal(err)
	}
	if err = state.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("base\nchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	base := []string{"git-advanced", "run", "stash_create", "--run", runID,
		"--enable-git-advanced", "--enable-permission-control",
		"--operation-key", "git-advanced-cli-stash-0001", "--message", "CLI exact stash"}
	if mode == "full" {
		base = append(base, "--enable-danger-full-access")
	}
	preview, stderr, code := executeTestCommand(t, base...)
	if code != 0 || stderr != "" || !strings.Contains(preview, "review_only: true") ||
		!strings.Contains(preview, "checkpoint_required: true") ||
		!strings.Contains(preview, "operation: stash_create") {
		t.Fatalf("preview output=%q stderr=%q code=%d", preview, stderr, code)
	}
	if got := strings.TrimSpace(runGitAdvancedCLITestGit(t, root, "stash", "list")); got != "" {
		t.Fatalf("non-authorizing preview created stash: %q", got)
	}
	if got := strings.TrimSpace(runGitAdvancedCLITestGit(t, root, "status", "--porcelain")); !strings.Contains(got, "tracked.txt") {
		t.Fatalf("non-authorizing preview changed worktree: %q", got)
	}

	confirmed := append(append([]string{}, base...), "--confirm")
	if mode == "full" {
		if output, stderr, code := executeTestCommand(t, confirmed...); code == 0 {
			t.Fatalf("exact operation confirmation activated cold Full: %s %s", output, stderr)
		}
		if got := strings.TrimSpace(runGitAdvancedCLITestGit(t, root, "stash", "list")); got != "" {
			t.Fatalf("cold Full created stash: %q", got)
		}
		confirmed = append(confirmed, "--confirm-full")
	}
	receipt, stderr, code := executeTestCommand(t, confirmed...)
	if code != 0 || stderr != "" || !strings.Contains(receipt, "status: succeeded") ||
		!strings.Contains(receipt, "checkpoint_id: wcp-") ||
		!strings.Contains(receipt, "approval_id: approval-") {
		t.Fatalf("confirmed output=%q stderr=%q code=%d", receipt, stderr, code)
	}
	if got := strings.TrimSpace(runGitAdvancedCLITestGit(t, root, "stash", "list")); !strings.Contains(got, "CLI exact stash") {
		t.Fatalf("confirmed mutation did not create exact stash: %q", got)
	}
	if got := strings.TrimSpace(runGitAdvancedCLITestGit(t, root, "status", "--porcelain", "--",
		"tracked.txt")); got != "" {
		t.Fatalf("stash receipt left unexpected tracked-file state: %q", got)
	}
	if mode == "full" {
		if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("next change\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cold := append(append([]string{}, base...), "--confirm")
		for i := range cold {
			if cold[i] == "git-advanced-cli-stash-0001" {
				cold[i] = "git-advanced-cli-stash-0002"
			}
		}
		if _, stderr, code := executeTestCommand(t, cold...); code == 0 {
			t.Fatalf("next invocation inherited Full: %s", stderr)
		}
		if count := strings.Count(strings.TrimSpace(runGitAdvancedCLITestGit(t, root, "stash", "list")), "CLI exact stash"); count != 1 {
			t.Fatalf("cold follow-up created another stash: %d", count)
		}
	}

}

func runGitAdvancedCLITestGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v: %s", args, err, output)
	}
	return string(output)
}
