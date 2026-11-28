package app

import (
	"regexp"
	"strings"
	"testing"
)

var workItemIDPattern = regexp.MustCompile(`work-[0-9]{14}-[a-f0-9]{12}`)

func TestTodoCLIEndToEndWorkBoard(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	createdRun, stderr, code := executeTestCommand(t, "run", "create", "todo cli work board", "--profile", "code")
	if code != 0 {
		t.Fatalf("run create failed: %s", stderr)
	}
	runID := runIDPattern.FindString(createdRun)
	if runID == "" {
		t.Fatalf("missing run id: %s", createdRun)
	}
	graph, stderr, code := executeTestCommand(t, "run", "graph", runID)
	if code != 0 {
		t.Fatalf("run graph failed: %s", stderr)
	}
	rootAgentID := agentIDPattern.FindString(graph)
	if rootAgentID == "" {
		t.Fatalf("missing root Agent id: %s", graph)
	}

	createdDependency, stderr, code := executeTestCommand(t, "todo", "create", runID, "prepare fixtures",
		"--priority", "high", "--owner", "planner", "--owner-agent", rootAgentID)
	if code != 0 {
		t.Fatalf("dependency create failed: %s", stderr)
	}
	dependencyID := workItemIDPattern.FindString(createdDependency)
	if dependencyID == "" || !strings.Contains(createdDependency, "version: 1") {
		t.Fatalf("unexpected dependency output: %s", createdDependency)
	}
	if !strings.Contains(createdDependency, "plan item ") || strings.Contains(createdDependency, "work item ") {
		t.Fatalf("todo presentation did not use Plan item: %s", createdDependency)
	}
	createdItem, stderr, code := executeTestCommand(t, "todo", "create", runID, "implement parser",
		"--priority", "critical", "--owner", "coder", "--owner-agent", rootAgentID,
		"--depends-on", dependencyID,
		"--acceptance", "unit tests pass", "--acceptance", "lint clean")
	if code != 0 {
		t.Fatalf("work item create failed: %s", stderr)
	}
	itemID := workItemIDPattern.FindString(createdItem)
	if itemID == "" {
		t.Fatalf("missing work item id: %s", createdItem)
	}

	listed, stderr, code := executeTestCommand(t, "todo", "list", runID, "--status", "pending,blocked",
		"--owner-agent", rootAgentID)
	if code != 0 || !strings.Contains(listed, itemID) || !strings.Contains(listed, dependencyID) || !strings.Contains(listed, "deps=1") {
		t.Fatalf("unexpected todo list output=%s stderr=%s code=%d", listed, stderr, code)
	}
	shown, stderr, code := executeTestCommand(t, "todo", "show", itemID)
	if code != 0 || !strings.Contains(shown, "owner_agent: "+rootAgentID) ||
		!strings.Contains(shown, "acceptance[1]: lint clean") || !strings.Contains(shown, "dependencies[1]: "+dependencyID) {
		t.Fatalf("unexpected todo show output=%s stderr=%s code=%d", shown, stderr, code)
	}

	if _, stderr, code := executeTestCommand(t, "todo", "block", itemID); code != 2 || !strings.Contains(stderr, "block reason is required") {
		t.Fatalf("missing block reason returned code=%d stderr=%s", code, stderr)
	}
	blocked, stderr, code := executeTestCommand(t, "todo", "block", itemID, "--reason", "waiting for fixtures", "--version", "1")
	if code != 0 || !strings.Contains(blocked, "status: blocked") || !strings.Contains(blocked, "version: 2") {
		t.Fatalf("unexpected block output=%s stderr=%s code=%d", blocked, stderr, code)
	}
	updated, stderr, code := executeTestCommand(t, "todo", "update", itemID, "--owner", "reviewer", "--clear-acceptance", "--version", "2")
	if code != 0 || !strings.Contains(updated, "version: 3") {
		t.Fatalf("unexpected update output=%s stderr=%s code=%d", updated, stderr, code)
	}
	shown, stderr, code = executeTestCommand(t, "todo", "show", itemID)
	if code != 0 || !strings.Contains(shown, "owner: reviewer") ||
		!strings.Contains(shown, "owner_agent: "+rootAgentID) || !strings.Contains(shown, "acceptance: -") ||
		!strings.Contains(shown, "blocked_reason: waiting for fixtures") {
		t.Fatalf("updated item not visible output=%s stderr=%s code=%d", shown, stderr, code)
	}
	if _, stderr, code := executeTestCommand(t, "todo", "reopen", itemID, "--version", "2"); code != 4 || !strings.Contains(stderr, "changed concurrently") {
		t.Fatalf("stale transition returned code=%d stderr=%s", code, stderr)
	}
	reopened, stderr, code := executeTestCommand(t, "todo", "reopen", itemID, "--version", "3")
	if code != 0 || !strings.Contains(reopened, "status: pending") || !strings.Contains(reopened, "version: 4") {
		t.Fatalf("unexpected reopen output=%s stderr=%s code=%d", reopened, stderr, code)
	}
	if _, stderr, code := executeTestCommand(t, "todo", "start", itemID, "--version", "4"); code != 4 || !strings.Contains(stderr, "incomplete dependencies") {
		t.Fatalf("dependency gate returned code=%d stderr=%s", code, stderr)
	}
	if _, stderr, code := executeTestCommand(t, "todo", "complete", dependencyID, "--version", "1"); code != 0 {
		t.Fatalf("dependency completion failed: %s", stderr)
	}
	started, stderr, code := executeTestCommand(t, "todo", "start", itemID, "--version", "4")
	if code != 0 || !strings.Contains(started, "status: in_progress") || !strings.Contains(started, "version: 5") {
		t.Fatalf("unexpected start output=%s stderr=%s code=%d", started, stderr, code)
	}
	completed, stderr, code := executeTestCommand(t, "todo", "complete", itemID, "--version", "5")
	if code != 0 || !strings.Contains(completed, "status: completed") || !strings.Contains(completed, "version: 6") {
		t.Fatalf("unexpected completion output=%s stderr=%s code=%d", completed, stderr, code)
	}

	completedList, stderr, code := executeTestCommand(t, "todo", "list", runID, "--status", "completed")
	if code != 0 || !strings.Contains(completedList, itemID) || !strings.Contains(completedList, dependencyID) {
		t.Fatalf("unexpected completed list output=%s stderr=%s code=%d", completedList, stderr, code)
	}
	timeline, stderr, code := executeTestCommand(t, "run", "events", runID)
	if code != 0 || strings.Count(timeline, "work_item.created") != 2 || strings.Count(timeline, "work_item.changed") != 6 {
		t.Fatalf("unexpected work board timeline output=%s stderr=%s code=%d", timeline, stderr, code)
	}
}

func TestTodoCLIHelpAndValidation(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	help, stderr, code := executeTestCommand(t, "help")
	if code != 0 || stderr != "" || !strings.Contains(help, "cyberagent todo create|list|show|update") {
		t.Fatalf("todo command missing from help output=%s stderr=%s code=%d", help, stderr, code)
	}
	if _, stderr, code := executeTestCommand(t, "todo", "list", "run-missing", "--status", "not-real"); code != 2 || !strings.Contains(stderr, "invalid plan item status") {
		t.Fatalf("invalid status returned code=%d stderr=%s", code, stderr)
	}
}

// Transition usage text must reference the subcommand the operator typed:
// the domain status names (completed, cancelled, blocked) are not
// dispatchable subcommands, and following the old text hit
// `unknown todo subcommand`.
func TestTodoTransitionUsageUsesSubcommandNames(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	for _, sub := range []string{"complete", "cancel", "block", "start"} {
		_, stderr, code := executeTestCommand(t, "todo", sub)
		if code != 2 || !strings.Contains(stderr, "usage: cyberagent todo "+sub+" <work-id>") {
			t.Fatalf("todo %s usage must reference its own subcommand: stderr=%q code=%d", sub, stderr, code)
		}
	}
}
