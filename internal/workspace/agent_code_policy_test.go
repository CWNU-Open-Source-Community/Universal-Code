package workspace

import (
	"path/filepath"
	"slices"
	"testing"

	"cyberagent-workbench/internal/apperror"
)

func TestAgentCodeProjectExclusions(t *testing.T) {
	root := t.TempDir()
	for _, file := range []string{"src/main.go", "src/other.go", "secrets/key.txt", "src/generated/out.go"} {
		mustAgentCodeMkdir(t, filepath.Dir(filepath.Join(root, file)))
		mustAgentCodeWrite(t, filepath.Join(root, file), "evidence\n")
	}
	mustAgentCodeWrite(t, filepath.Join(root, ".gitignore"), "!secrets/key.txt\n")
	policy := AgentCodePathPolicy{ExcludePaths: []string{"secrets/**", "**/generated"}}
	for _, file := range []string{"secrets/key.txt", "src/generated/out.go"} {
		if _, err := AgentCodeReadFile(root, "ws", file, 1, 10, true, policy); apperror.CodeOf(err) != apperror.CodePolicyDenied {
			t.Fatalf("excluded read %s: %v", file, err)
		}
		if _, _, err := AgentCodeReadMutationSource(root, file, policy); apperror.CodeOf(err) != apperror.CodePolicyDenied {
			t.Fatalf("excluded mutation source %s: %v", file, err)
		}
		if _, _, err := AgentCodeResolveWritePath(root, file, false, policy); apperror.CodeOf(err) != apperror.CodePolicyDenied {
			t.Fatalf("excluded write %s: %v", file, err)
		}
	}
	if _, _, err := AgentCodeResolveCreatePath(root, "secrets/new/deep.txt", policy); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("excluded new path: %v", err)
	}
	if _, err := AgentCodeListDirectory(root, "ws", "secrets", "", 20, true, policy); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("excluded directory: %v", err)
	}
	want := []string{"src/main.go", "src/other.go"}
	glob, err := AgentCodeGlobFiles(root, "ws", "**", "", 20, false, policy)
	if err != nil || !slices.Equal(glob.Paths, want) {
		t.Fatalf("filtered glob = %v, %v", glob.Paths, err)
	}
	grep, err := AgentCodeGrepFiles(root, "ws", "evidence", "**", "", 20, true, true, policy)
	if err != nil || len(grep.Matches) != 2 {
		t.Fatalf("filtered grep = %+v, %v", grep, err)
	}
	scopes := []AgentCodeScopePath{{Path: "src", Directory: true}, {Path: "secrets", Directory: true}}
	scoped, err := AgentCodeScopedGlobFiles(root, "ws", "**", "", 20, scopes, policy)
	if err != nil || !slices.Equal(scoped.Paths, want) {
		t.Fatalf("filtered scoped glob = %v, %v", scoped.Paths, err)
	}
	scopedGrep, err := AgentCodeScopedGrepFiles(root, "ws", "evidence", "**", "", 20, true, scopes, policy)
	if err != nil || len(scopedGrep.Matches) != 2 {
		t.Fatalf("filtered scoped grep = %+v, %v", scopedGrep, err)
	}
	// Independent Runs on the same directory retain their own visibility.
	unrestricted, err := AgentCodeReadFile(root, "ws", "secrets/key.txt", 1, 10, false)
	if err != nil || unrestricted.Content != "evidence" {
		t.Fatalf("unrestricted Run read = %+v, %v", unrestricted, err)
	}
	page, err := AgentCodeGlobFiles(root, "ws", "**", "", 1, false, policy)
	if err != nil || page.NextCursor == "" {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	if _, err := AgentCodeGlobFiles(root, "ws", "**", page.NextCursor, 1, false); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("cursor crossed policy: %v", err)
	}
}

func TestAgentCodeRejectsMalformedProjectExclusions(t *testing.T) {
	for _, pattern := range []string{"[", "../secret", "src/../secret", "/secret", "src\\secret", "C:secret", "secret\npath", ".", "a//b"} {
		t.Run(pattern, func(t *testing.T) {
			policy := AgentCodePathPolicy{ExcludePaths: []string{pattern}}
			if _, err := AgentCodeGlobFiles(t.TempDir(), "ws", "**", "", 10, false, policy); err == nil {
				t.Fatal("malformed exclusion accepted")
			}
		})
	}
}
