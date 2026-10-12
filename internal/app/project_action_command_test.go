package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/store"
)

func TestRunProjectActionPreparesPinnedSpecWithoutStartingAJob(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CYBERAGENT_HOME", home)
	if _, stderr, code := executeTestCommand(t, "workspace", "init", "project-actions"); code != 0 {
		t.Fatal(stderr)
	}
	state, err := store.Open(filepath.Join(home, "cyberagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := state.GetWorkspaceByName(t.Context(), "project-actions")
	_ = state.Close()
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(workspace.RootPath, ".prayu", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte("protocol: project_config.v1\ntest_command_id: test.go\nformat_command_id: format.go\nexclude_paths: [private/**]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	created, stderr, code := executeTestCommand(t, "run", "create", "verify typed project action", "--workspace", "project-actions")
	if code != 0 {
		t.Fatal(stderr)
	}
	runID := runIDPattern.FindString(created)
	// A later invalid configuration cannot replace the stored action ID.
	if err := os.WriteFile(filename, []byte("test_command_id: shell text\n"), 0600); err != nil {
		t.Fatal(err)
	}
	prepared, stderr, code := executeTestCommand(t, "run", "project-action", runID, "test", "--prepare")
	if code != 0 {
		t.Fatal(stderr)
	}
	var spec runner.CommandRuntimeSpec
	if err := json.Unmarshal([]byte(prepared), &spec); err != nil {
		t.Fatal(err)
	}
	if spec.Profile != runner.CommandRuntimeProcess || spec.Script != "" || !reflect.DeepEqual(spec.Arguments, []string{"test", "./..."}) {
		t.Fatalf("unexpected intent %#v", spec)
	}
	if _, stderr, code := executeTestCommand(t, "run", "project-action", runID, "test", "--operation-key", "project-action-no-confirm-0001"); code == 0 || !strings.Contains(stderr, "host-execution acknowledgement") {
		t.Fatalf("missing acknowledgement accepted %d %s", code, stderr)
	}
	if _, stderr, code := executeTestCommand(t, "run", "project-action", runID, "format", "--path", "private/example.go", "--prepare"); code == 0 || !strings.Contains(stderr, "excluded") {
		t.Fatalf("excluded format prepared %d %s", code, stderr)
	}
	state, err = store.Open(filepath.Join(home, "cyberagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	jobs, err := state.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: runID, Limit: 10})
	if err != nil || len(jobs) != 0 {
		t.Fatalf("preparation/refusal created a Job: %#v %v", jobs, err)
	}
}
