package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/apperror"
)

func TestAgentIgnorePreservesSignificantWhitespace(t *testing.T) {
	root := t.TempDir()
	mustAgentCodeWrite(t, filepath.Join(root, ".gitignore"),
		"notes\\ \r\n  draft.txt\nreport.txt   \n")
	rules := loadAgentIgnoreRules(root)
	for _, test := range []struct {
		index   int
		match   string
		noMatch string
	}{
		{0, "notes ", "notes"},
		{1, "  draft.txt", "draft.txt"},
		{2, "report.txt", "report.txt "},
	} {
		if test.index >= len(rules) || !agentIgnoreMatches(rules[test.index], test.match, false) ||
			agentIgnoreMatches(rules[test.index], test.noMatch, false) {
			t.Fatalf("ignore rule %d did not preserve the spelling of %q", test.index, test.match)
		}
	}
}

func TestAgentCodeReadListGlobAndGrepAreBoundedAndStable(t *testing.T) {
	root := t.TempDir()
	mustAgentCodeMkdir(t, filepath.Join(root, "src", "nested"))
	mustAgentCodeWrite(t, filepath.Join(root, ".gitignore"), "ignored.txt\n")
	mustAgentCodeWrite(t, filepath.Join(root, "ignored.txt"), "do not expose\n")
	mustAgentCodeWrite(t, filepath.Join(root, ".secret"), "do not expose\n")
	mustAgentCodeMkdir(t, filepath.Join(root, ".github", "workflows"))
	mustAgentCodeWrite(t, filepath.Join(root, ".github", "workflows", "ci.yml"), "name: ci\n")
	content := "alpha\r\nbeta alpha\r\ngamma\r\n"
	mustAgentCodeWrite(t, filepath.Join(root, "src", "nested", "main.go"), content)
	mustAgentCodeWrite(t, filepath.Join(root, "src", "other.go"), "alpha\n")

	first, err := AgentCodeListDirectory(root, "workspace-1", "src", "", 1, false)
	if err != nil || len(first.Items) != 1 || !first.Truncated || first.NextCursor == "" {
		t.Fatalf("first list page=%#v err=%v", first, err)
	}
	second, err := AgentCodeListDirectory(root, "workspace-1", "src", first.NextCursor, 1, false)
	if err != nil || len(second.Items) != 1 || second.Items[0].Path <= first.Items[0].Path {
		t.Fatalf("second list page=%#v err=%v", second, err)
	}
	if _, err := AgentCodeListDirectory(root, "workspace-2", "src", first.NextCursor, 1,
		false); err == nil || apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeConflict {
		t.Fatalf("cursor crossed Workspace identity: %v", err)
	}
	rootList, err := AgentCodeListDirectory(root, "workspace-1", ".", "", 20, false)
	if err != nil {
		t.Fatal(err)
	}
	foundGitHub := false
	for _, item := range rootList.Items {
		if item.Path == "ignored.txt" || item.Path == ".secret" || item.Path == ".git" {
			t.Fatalf("hidden or ignored entry leaked: %#v", rootList.Items)
		}
		foundGitHub = foundGitHub || item.Path == ".github"
	}
	if !foundGitHub {
		t.Fatalf("Go-allowlisted .github evidence was not listed: %#v", rootList.Items)
	}

	read, err := AgentCodeReadFile(root, "workspace-1", "src/nested/main.go", 2, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	if read.Content != "beta alpha" || read.Encoding != "utf-8" || read.Newline != "crlf" ||
		read.ContentSHA256 != hex.EncodeToString(sum[:]) || !read.Truncated || read.TotalLines != 3 {
		t.Fatalf("unexpected bounded read: %#v", read)
	}

	glob, err := AgentCodeGlobFiles(root, "workspace-1", "src/**/*.go", "", 20, false)
	if err != nil || len(glob.Paths) != 2 || glob.Paths[0] != "src/nested/main.go" ||
		glob.Paths[1] != "src/other.go" {
		t.Fatalf("recursive glob=%#v err=%v", glob, err)
	}
	grep1, err := AgentCodeGrepFiles(root, "workspace-1", "alpha", "**/*.go", "", 1,
		true, false)
	if err != nil || len(grep1.Matches) != 1 || grep1.NextCursor == "" || !grep1.Truncated {
		t.Fatalf("first grep page=%#v err=%v", grep1, err)
	}
	grep2, err := AgentCodeGrepFiles(root, "workspace-1", "alpha", "**/*.go",
		grep1.NextCursor, 10, true, false)
	if err != nil || len(grep2.Matches) != 2 ||
		grep2.Matches[0].Path+string(rune(grep2.Matches[0].Line)) ==
			grep1.Matches[0].Path+string(rune(grep1.Matches[0].Line)) {
		t.Fatalf("second grep page=%#v err=%v", grep2, err)
	}
}

func TestAgentCodePathsFailClosed(t *testing.T) {
	root := t.TempDir()
	mustAgentCodeMkdir(t, filepath.Join(root, "src"))
	mustAgentCodeWrite(t, filepath.Join(root, "src", "Case.txt"), "text")
	mustAgentCodeWrite(t, filepath.Join(root, ".gitignore"), "generated/\n")
	mustAgentCodeMkdir(t, filepath.Join(root, "generated"))

	for _, path := range []string{"../escape.txt", ".secret", "generated/new.txt"} {
		if _, _, err := AgentCodeResolveWritePath(root, path, true); err == nil {
			t.Fatalf("unsafe write path %q was accepted", path)
		}
	}
	if _, _, err := AgentCodeResolveWritePath(root, "src/case.txt", false); err == nil ||
		apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeConflict {
		t.Fatalf("case alias was not rejected as a conflict: %v", err)
	}
	mustAgentCodeWrite(t, filepath.Join(root, "binary.bin"), string([]byte{0, 1, 2, 3}))
	if _, err := AgentCodeReadFile(root, "workspace-1", "binary.bin", 1, 10, false); err == nil {
		t.Fatal("binary workspace file was read")
	}
	if _, err := AgentCodeReadFile(root, "workspace-1", "src/Case.txt", 1,
		MaxAgentCodeReadLines+1, false); err == nil {
		t.Fatal("unbounded line range was accepted")
	}
	if _, err := AgentCodeReadFile(root, "workspace-1", "src/Case.txt", 2, 2,
		false); err == nil {
		t.Fatal("line range starting beyond EOF was accepted")
	}
	if text, hash, err := AgentCodeReadMutationSource(root, "src/Case.txt"); err != nil || text != "text" || len(hash) != 64 {
		t.Fatalf("mutation source text=%q hash=%q err=%v", text, hash, err)
	}
}

func TestAgentCodeGrepPaginatesAfterExactLimitAndSkipsBinary(t *testing.T) {
	root := t.TempDir()
	mustAgentCodeWrite(t, filepath.Join(root, "a.txt"), "needle\n")
	mustAgentCodeWrite(t, filepath.Join(root, "b.txt"), "needle\n")
	mustAgentCodeWrite(t, filepath.Join(root, "binary.txt"),
		string([]byte{1, 'n', 'e', 'e', 'd', 'l', 'e', 2}))

	first, err := AgentCodeGrepFiles(root, "workspace-1", "needle", "**", "", 1,
		true, false)
	if err != nil || len(first.Matches) != 1 || !first.Truncated || first.NextCursor == "" {
		t.Fatalf("first exact-limit grep page=%#v err=%v", first, err)
	}
	second, err := AgentCodeGrepFiles(root, "workspace-1", "needle", "**",
		first.NextCursor, 1, true, false)
	if err != nil || len(second.Matches) != 1 || second.Matches[0].Path != "b.txt" {
		t.Fatalf("second exact-limit grep page=%#v err=%v", second, err)
	}
	for _, match := range append(first.Matches, second.Matches...) {
		if match.Path == "binary.txt" {
			t.Fatalf("binary grep match leaked: %#v", match)
		}
	}
}

func TestAgentCodeRootFingerprintChangesWhenDirectoryIsReplaced(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	mustAgentCodeMkdir(t, root)
	before, err := AgentCodeRootFingerprint(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, filepath.Join(parent, "old-workspace")); err != nil {
		t.Fatal(err)
	}
	mustAgentCodeMkdir(t, root)
	after, err := AgentCodeRootFingerprint(root)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatalf("workspace root replacement retained fingerprint %s", before)
	}
}

func TestAgentCodeToolsRejectLinkedWorkspacePaths(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustAgentCodeWrite(t, filepath.Join(outside, "secret.txt"), "outside\n")
	link := filepath.Join(root, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if _, err := AgentCodeReadFile(root, "workspace-1", "linked/secret.txt", 1, 10,
		false); err == nil || apperror.CodeOf(apperror.Normalize(err)) != apperror.CodePolicyDenied {
		t.Fatalf("linked workspace read was not denied: %v", err)
	}
	if _, _, err := AgentCodeResolveWritePath(root, "linked/new.txt", true); err == nil ||
		apperror.CodeOf(apperror.Normalize(err)) != apperror.CodePolicyDenied {
		t.Fatalf("linked workspace write was not denied: %v", err)
	}
	glob, err := AgentCodeGlobFiles(root, "workspace-1", "**", "", 20, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range glob.Paths {
		if candidate == "linked/secret.txt" {
			t.Fatalf("linked workspace file leaked through glob: %#v", glob)
		}
	}
}

func mustAgentCodeMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustAgentCodeWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
