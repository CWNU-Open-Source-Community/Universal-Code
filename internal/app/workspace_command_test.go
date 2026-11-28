package app

import (
	"strings"
	"testing"
)

func TestMissingWorkspaceEntriesUseRelativePaths(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	if _, stderr, code := executeTestCommand(t, "workspace", "init", "files"); code != 0 {
		t.Fatal(stderr)
	}
	created, stderr, code := executeTestCommand(t, "session", "create", "--workspace", "files")
	sessionID := sessionIDPattern.FindString(created)
	if code != 0 || sessionID == "" {
		t.Fatalf("create session: %s%s", created, stderr)
	}
	for _, tc := range []struct{ command, slash, kind, prefix string }{
		{"read", "/read", "file", "Workspace read failed: "},
		{"tree", "/ls", "directory", "Workspace list failed: "},
	} {
		want := tc.kind + " does not exist in this workspace: missing"
		_, stderr, code := executeTestCommand(t, "workspace", tc.command, "files", "missing")
		if code != 3 || stderr != "error: "+want+"\n" {
			t.Errorf("workspace %s: exit %d, stderr %q", tc.command, code, stderr)
		}
		stdout, stderr, code := executeTestCommand(t, "session", "send", sessionID, tc.slash+" missing")
		if code != 0 || stderr != "" || strings.TrimSuffix(stdout, "\n") != tc.prefix+want {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", tc.slash, code, stdout, stderr)
		}
	}
}
