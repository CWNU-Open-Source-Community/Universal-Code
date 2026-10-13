package workspace

import (
	"path/filepath"
	"slices"
	"testing"

	"cyberagent-workbench/internal/apperror"
)

func TestAgentCodeIgnoreRulesAgreeForDiscoveryReadAndMutation(t *testing.T) {
	for _, test := range []struct {
		name    string
		rules   string
		blocked []string
		allowed []string
	}{
		{
			name: "unanchored directory at any depth", rules: "generated/\n",
			blocked: []string{"generated/private.txt", "nested/generated/private.txt", "nested/deep/generated/private.txt"},
			allowed: []string{"nested/generated.txt", "other/generated"},
		},
		{
			name: "anchored directory", rules: "/generated/\n",
			blocked: []string{"generated/private.txt"},
			allowed: []string{"nested/generated/private.txt", "other/generated"},
		},
		{
			name: "path directory wildcard", rules: "nested/gen*/\n",
			blocked: []string{"nested/generated/private.txt"},
			allowed: []string{"other/nested/generated/private.txt", "nested/generic.txt"},
		},
		{
			name: "negation reopens directory", rules: "generated/\n!nested/generated/\n",
			blocked: []string{"generated/private.txt", "other/generated/private.txt"},
			allowed: []string{"nested/generated/private.txt"},
		},
		{
			name: "leaf negation cannot reopen parent", rules: "generated/\n!nested/generated/private.txt\n",
			blocked: []string{"nested/generated/private.txt"},
			allowed: []string{"nested/private.txt"},
		},
		{
			name: "file negation preserves rule order", rules: "*.txt\n!keep.txt\n/nested/keep.txt\n",
			blocked: []string{"nested/private.txt", "nested/keep.txt"},
			allowed: []string{"keep.txt", "other/keep.txt"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			mustAgentCodeWrite(t, filepath.Join(root, ".gitignore"), test.rules)
			for _, file := range append(slices.Clone(test.blocked), test.allowed...) {
				mustAgentCodeMkdir(t, filepath.Dir(filepath.Join(root, file)))
				mustAgentCodeWrite(t, filepath.Join(root, file), "evidence\n")
				wantBlocked := slices.Contains(test.blocked, file)
				_, readErr := AgentCodeReadFile(root, "ws", file, 1, 10, false)
				_, _, sourceErr := AgentCodeReadMutationSource(root, file)
				_, _, writeErr := AgentCodeResolveWritePath(root, file, false)
				for operation, err := range map[string]error{"read": readErr, "mutation source": sourceErr, "write": writeErr} {
					if wantBlocked && apperror.CodeOf(err) != apperror.CodePolicyDenied || !wantBlocked && err != nil {
						t.Fatalf("%s %s: %v, want blocked=%t", operation, file, err, wantBlocked)
					}
				}
			}
			glob, err := AgentCodeGlobFiles(root, "ws", "**", "", 20, false)
			want := slices.Clone(test.allowed)
			slices.Sort(want)
			if err != nil || !slices.Equal(glob.Paths, want) {
				t.Fatalf("glob = %v, %v; want %v", glob.Paths, err, want)
			}
		})
	}
}

func TestAgentCodeIgnoreDirectoryBlocksCreatingMissingParents(t *testing.T) {
	root := t.TempDir()
	mustAgentCodeWrite(t, filepath.Join(root, ".gitignore"), "generated/\n")
	if _, _, err := AgentCodeResolveCreatePath(root, "nested/generated/missing/private.txt"); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("ignored create = %v", err)
	}
}
