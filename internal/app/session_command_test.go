package app

import (
	"strings"
	"testing"
)

func TestSessionHistoryMissingSessionIsNotFound(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	_, stderr, code := executeTestCommand(t, "session", "history", "sess-missing")
	if code != 3 || stderr == "" {
		t.Fatalf("history of a missing session must fail with not-found exit 3: stderr=%q code=%d", stderr, code)
	}
}

func TestSessionHistoryExistingEmptySessionStillReportsNoMessages(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	created, stderr, code := executeTestCommand(t, "session", "create", "history probe")
	if code != 0 {
		t.Fatalf("session create failed: %s", stderr)
	}
	sessionID := strings.Fields(strings.SplitN(created, "\n", 2)[0])[1]
	if !strings.HasPrefix(sessionID, "sess-") {
		t.Fatalf("session id missing from create output: %q", created)
	}
	stdout, stderr, code := executeTestCommand(t, "session", "history", sessionID)
	if code != 0 || stderr != "" || stdout != "no messages\n" {
		t.Fatalf("existing empty session must report no messages: stdout=%q stderr=%q code=%d", stdout, stderr, code)
	}
}
