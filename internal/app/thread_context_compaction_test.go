package app

import (
	"encoding/json"
	"strings"
	"testing"

	"cyberagent-workbench/internal/domain"
)

func TestThreadCompactCLINoopUsesCurrentThreadAndDurableKey(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	created, stderr, code := executeTestCommand(t, "thread", "create", "CLI 上下文压缩", "--profile", "review")
	if code != 0 || stderr != "" {
		t.Fatalf("create=%s %s %d", created, stderr, code)
	}
	id := threadIDPattern.FindString(created)
	if id == "" {
		t.Fatal("Thread identity missing")
	}
	args := []string{"thread", "compact", id, "--focus", "保留中文约束", "--operation-key", "cli-manual-context-key", "--json"}
	first, stderr, code := executeTestCommand(t, args...)
	var result domain.ThreadContextCompactionResult
	if code != 0 || stderr != "" || json.Unmarshal([]byte(first), &result) != nil || result.ThreadID != id || result.Status != "noop" || result.Replayed {
		t.Fatalf("compact=%s %s %d", first, stderr, code)
	}
	replay, stderr, code := executeTestCommand(t, args...)
	if code != 0 || stderr != "" || json.Unmarshal([]byte(replay), &result) != nil || !result.Replayed {
		t.Fatalf("replay=%s %s %d", replay, stderr, code)
	}
	history, stderr, code := executeTestCommand(t, "thread", "history", id, "--all")
	if code != 0 || stderr != "" || strings.Contains(history, "/compact") {
		t.Fatalf("command manufactured a user message: %s %s %d", history, stderr, code)
	}
	listed, stderr, code := executeTestCommand(t, "thread", "compactions", id, "--json")
	var operations domain.ThreadContextCompactionOperations
	if code != 0 || stderr != "" || json.Unmarshal([]byte(listed), &operations) != nil || len(operations.Operations) != 1 {
		t.Fatalf("operations=%s %s %d", listed, stderr, code)
	}
	op := operations.Operations[0]
	recovered, stderr, code := executeTestCommand(t, "thread", "compactions", id, "--operation", op.OperationID, "--action", "resume", "--expected-run", op.RunID, "--json")
	var view domain.ThreadContextCompactionOperationView
	if code != 0 || stderr != "" || json.Unmarshal([]byte(recovered), &view) != nil || view.Result == nil || !view.Result.Replayed {
		t.Fatalf("recovery=%s %s %d", recovered, stderr, code)
	}
}
