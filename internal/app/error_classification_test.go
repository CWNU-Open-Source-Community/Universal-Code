package app

import (
	"regexp"
	"strings"
	"testing"
)

// Precondition and argument failures must surface with their semantic exit
// codes (4 = failed precondition, 2 = invalid argument) instead of the
// internal default (1).
func TestPreconditionAndArgumentClassification(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	if _, stderr, code := executeTestCommand(t, "workspace", "init", "cls-ws"); code != 0 {
		t.Fatalf("workspace init failed: %s", stderr)
	}
	created, stderr, code := executeTestCommand(t, "session", "create", "cls probe")
	if code != 0 {
		t.Fatalf("session create failed: %s", stderr)
	}
	unbound := strings.Fields(strings.SplitN(created, "\n", 2)[0])[1]

	_, stderr, code = executeTestCommand(t, "session", "tree", unbound)
	if code != 4 || !strings.Contains(stderr, "Workspace-bound Session") {
		t.Fatalf("unbound session tree must be failed-precondition: stderr=%q code=%d", stderr, code)
	}

	_, stderr, code = executeTestCommand(t, "session", "send", unbound, "--operation-key=", "hi")
	if code != 2 || !strings.Contains(stderr, "cannot be blank") {
		t.Fatalf("blank operation key must be invalid-argument: stderr=%q code=%d", stderr, code)
	}

	// Bind a run to the (non-Git) workspace; the mock turn's exit code is
	// irrelevant here, only the durable run binding matters.
	_, _, _ = executeTestCommand(t, "exec", "--workspace", "cls-ws", "bind run")
	threads, _, _ := executeTestCommand(t, "thread", "list")
	match := regexp.MustCompile(`run-[0-9]+-[a-f0-9]+`).FindString(threads)
	if match == "" {
		t.Fatalf("no run bound to the workspace: %q", threads)
	}
	_, stderr, code = executeTestCommand(t, "drydock", "create", "--run", match,
		"--operation-key", "cls-drydock-key-00001", "--confirm")
	if code != 4 || !strings.Contains(stderr, "Git metadata") {
		t.Fatalf("drydock create on a non-Git workspace must be failed-precondition: stderr=%q code=%d", stderr, code)
	}
}
