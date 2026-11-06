package workspaceidentity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
)

// PathPolicy narrows agent file access with the Run's pinned project
// configuration. It is a security boundary, so every refusal below is pinned
// to the exact stable error code callers depend on.

func TestPathPolicyValidateAcceptsBoundedRootRelativePatterns(t *testing.T) {
	patterns := [][]string{
		nil,
		{},
		{"node_modules"},
		{"build"},
		{"dist/cache"},
		{"*.log"},
		{"**/tmp"},
		{"vendor/**/generated"},
		{"a-b_c.1"},
	}
	for _, exclude := range patterns {
		policy := PathPolicy{ExcludePaths: exclude}
		if err := policy.Validate(); err != nil {
			t.Fatalf("validate %q: %v", exclude, err)
		}
	}
}

func TestPathPolicyValidateRefusesUnsafePatterns(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
	}{
		{"empty pattern", ""},
		{"leading space", " node_modules"},
		{"trailing space", "node_modules "},
		{"backslash separator", "dist\\cache"},
		{"drive or scheme separator", "dist:cache"},
		{"absolute pattern", "/node_modules"},
		{"unclean pattern", "dist//cache"},
		{"parent escape", "dist/../cache"},
		{"current directory", "."},
		{"parent directory", ".."},
		{"parent component", "dist/.."},
		{"malformed glob", "["},
		{"control character", "dist\x01cache"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			policy := PathPolicy{ExcludePaths: []string{testCase.pattern}}
			err := policy.Validate()
			if err == nil {
				t.Fatalf("validate accepted %q", testCase.pattern)
			}
			if code := apperror.CodeOf(err); code != apperror.CodeInvalidArgument {
				t.Fatalf("validate %q error code = %v, want %v", testCase.pattern, code, apperror.CodeInvalidArgument)
			}
		})
	}
}

func TestPathPolicyValidateRefusesTooManyAndTooLongPatterns(t *testing.T) {
	many := make([]string, 65)
	for index := range many {
		many[index] = "pattern"
	}
	policy := PathPolicy{ExcludePaths: many}
	if err := policy.Validate(); err == nil {
		t.Fatal("validate accepted 65 patterns")
	} else if code := apperror.CodeOf(err); code != apperror.CodeInvalidArgument {
		t.Fatalf("pattern count error code = %v, want %v", code, apperror.CodeInvalidArgument)
	}

	long := PathPolicy{ExcludePaths: []string{strings.Repeat("a", 1025)}}
	if err := long.Validate(); err == nil {
		t.Fatal("validate accepted an over-long pattern")
	} else if code := apperror.CodeOf(err); code != apperror.CodeInvalidArgument {
		t.Fatalf("pattern length error code = %v, want %v", code, apperror.CodeInvalidArgument)
	}
}

func TestPathPolicyCheckAcceptsWorkspaceRelativePaths(t *testing.T) {
	policy := PathPolicy{ExcludePaths: []string{"secrets"}}
	for _, requested := range []string{".", "README.md", "src/main.go", "src/nested/file.ts", ""} {
		if err := policy.Check(requested); err != nil {
			t.Fatalf("check %q: %v", requested, err)
		}
	}
}

func TestPathPolicyCheckRefusesAnInvalidPolicy(t *testing.T) {
	policy := PathPolicy{ExcludePaths: []string{"../escape"}}
	err := policy.Check("README.md")
	if err == nil {
		t.Fatal("check accepted a path under an invalid policy")
	}
	if code := apperror.CodeOf(err); code != apperror.CodeInvalidArgument {
		t.Fatalf("check error code = %v, want %v", code, apperror.CodeInvalidArgument)
	}
}

func TestPathPolicyCheckRefusesPathsThatLeaveTheWorkspace(t *testing.T) {
	cases := []struct {
		name      string
		requested string
	}{
		{"absolute path", "/etc/passwd"},
		{"parent directory", ".."},
		{"parent prefix", "../secret"},
		{"nested parent", "src/../../secret"},
		{"unclean path", "src//file.go"},
		{"backslash separator", "src\\file.go"},
		{"drive or scheme separator", "src:file.go"},
		{"control character", "src\x01file.go"},
		{"invalid utf8", "src/\xff.go"},
		{"over-long path", strings.Repeat("a", 4097)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			policy := PathPolicy{}
			err := policy.Check(testCase.requested)
			if err == nil {
				t.Fatalf("check accepted %q", testCase.requested)
			}
			if code := apperror.CodeOf(err); code != apperror.CodeInvalidArgument {
				t.Fatalf("check %q error code = %v, want %v", testCase.requested, code, apperror.CodeInvalidArgument)
			}
		})
	}
}

func TestPathPolicyCheckDeniesExcludedPathsAndTheirDescendants(t *testing.T) {
	policy := PathPolicy{ExcludePaths: []string{"secrets", "dist/cache"}}

	for _, requested := range []string{"secrets", "secrets/keys.json", "dist/cache", "dist/cache/blob.bin"} {
		err := policy.Check(requested)
		if err == nil {
			t.Fatalf("check allowed the excluded path %q", requested)
		}
		if code := apperror.CodeOf(err); code != apperror.CodePolicyDenied {
			t.Fatalf("check %q error code = %v, want %v", requested, code, apperror.CodePolicyDenied)
		}
	}

	for _, requested := range []string{"secretsx", "dist/cachex", "src/secrets"} {
		if err := policy.Check(requested); err != nil {
			t.Fatalf("check refused the unrelated path %q: %v", requested, err)
		}
	}
}

func TestPathPolicyExcludesMatchesEveryParentDirectory(t *testing.T) {
	policy := PathPolicy{ExcludePaths: []string{"a/b"}}
	for _, relative := range []string{"a/b", "a/b/c", "a/b/c/d.txt"} {
		if !policy.Excludes(relative) {
			t.Fatalf("excludes did not match %q", relative)
		}
	}
	for _, relative := range []string{"a", "a/bc", "a/bb/c", "."} {
		if policy.Excludes(relative) {
			t.Fatalf("excludes matched the unrelated path %q", relative)
		}
	}
}

func TestMatchPathAppliesRootRelativeGlobSemantics(t *testing.T) {
	cases := []struct {
		pattern   string
		candidate string
		want      bool
	}{
		{"*.log", "server.log", true},
		{"*.log", "server.txt", false},
		{"*.log", "logs/server.log", false},
		{"a/*", "a/b", true},
		{"a/*", "a/b/c", false},
		{"a/**", "a/b", true},
		{"a/**", "a/b/c", true},
		// "**" also covers zero segments, so "a/**" reaches the directory
		// itself and Excludes can hide the whole excluded subtree.
		{"a/**", "a", true},
		{"**", "a/b/c", true},
		{"**/b", "a/b", true},
		{"**/b", "b", true},
		{"**/b", "a/bc", false},
		{"dist/cache", "dist/cache", true},
		{"dist/cache", "dist/cachex", false},
	}
	for _, testCase := range cases {
		if got := MatchPath(testCase.pattern, testCase.candidate); got != testCase.want {
			t.Fatalf("MatchPath(%q, %q) = %v, want %v", testCase.pattern, testCase.candidate, got, testCase.want)
		}
	}
}

func TestFingerprintBindsOneDirectoryToAStableIdentity(t *testing.T) {
	root := t.TempDir()
	first, err := Fingerprint(root)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if first == "" {
		t.Fatal("fingerprint returned an empty identity")
	}
	again, err := Fingerprint(root)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if first != again {
		t.Fatalf("fingerprint is unstable: %q then %q", first, again)
	}
	other, err := Fingerprint(t.TempDir())
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if first == other {
		t.Fatal("two different roots share one fingerprint")
	}
}

func TestFingerprintRefusesFilesSymlinksAndMissingRoots(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("data"), 0600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	// Each root kind is its own subtest: a host without symlink support must
	// skip only the symlink case, never the file and missing-root refusals.
	t.Run("regular file", func(t *testing.T) {
		if _, err := Fingerprint(file); err == nil {
			t.Fatalf("fingerprint accepted %q", file)
		}
	})

	t.Run("missing root", func(t *testing.T) {
		missing := filepath.Join(dir, "missing")
		if _, err := Fingerprint(missing); err == nil {
			t.Fatalf("fingerprint accepted %q", missing)
		}
	})

	t.Run("symlinked root", func(t *testing.T) {
		link := filepath.Join(dir, "link")
		if err := os.Symlink(dir, link); err != nil {
			t.Skipf("symlinks are unavailable: %v", err)
		}
		if _, err := Fingerprint(link); err == nil {
			t.Fatalf("fingerprint accepted %q", link)
		}
	})
}
