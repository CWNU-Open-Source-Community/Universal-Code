package app

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cyberagent-workbench/internal/store"
)

func TestScriptRunPreservesArgumentsAfterEndOfOptions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CYBERAGENT_HOME", home)
	if _, stderr, code := executeTestCommand(t, "workspace", "init", "demo"); code != 0 {
		t.Fatalf("workspace init failed: %s", stderr)
	}
	path := filepath.Join(home, "workspaces", "demo", "scripts", "noop.py")
	if err := os.WriteFile(path, []byte("print('ok')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scriptArgs := []string{"scripts/noop.py", "--workspace", "script-argument", "--local", "--idempotency-key", "script-key"}
	command := append([]string{"script", "run", "--workspace", "demo", "--idempotency-key", "outer-key", "--"}, scriptArgs...)
	stdout, stderr, code := executeTestCommand(t, command...)
	if code != 0 {
		t.Fatalf("script proposal failed: code=%d stderr=%s stdout=%s", code, stderr, stdout)
	}
	state, err := store.Open(filepath.Join(home, "cyberagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	process, err := state.GetScriptProcess(t.Context(), processIDPattern.FindString(stdout))
	if err != nil {
		t.Fatal(err)
	}
	if process.RequestedBackend != "sandbox" || !reflect.DeepEqual(process.Arguments, scriptArgs) {
		t.Fatalf("script arguments changed outer options or were lost: backend=%q args=%q", process.RequestedBackend, process.Arguments)
	}
	command[len(command)-1] = "different-script-key"
	_, stderr, code = executeTestCommand(t, command...)
	if code != 4 || !strings.Contains(stderr, "idempotency key") {
		t.Fatalf("script argument replaced outer operation key: code=%d stderr=%s", code, stderr)
	}
}
