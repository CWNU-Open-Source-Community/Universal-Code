package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStructuredToolCLIListsSchemasAndCreatesRunMemory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CYBERAGENT_HOME", home)
	createdRun, stderr, code := executeTestCommand(t,
		"run", "create", "structured tool CLI", "--profile", "code", "--max-tool-calls", "10",
	)
	if code != 0 {
		t.Fatalf("run create failed: code=%d stderr=%s", code, stderr)
	}
	runID := runIDPattern.FindString(createdRun)
	if runID == "" {
		t.Fatalf("run id is missing: %s", createdRun)
	}

	schemas, stderr, code := executeTestCommand(t, "tool", "schema")
	if code != 0 || !strings.Contains(schemas, `"name": "work_item_create"`) ||
		!strings.Contains(schemas, `"name": "note_create"`) ||
		!strings.Contains(schemas, `"name": "specialist_delegation_propose"`) ||
		!strings.Contains(schemas, `"name": "plan_delivery_propose"`) ||
		!strings.Contains(schemas, `"additionalProperties": false`) {
		t.Fatalf("structured tool schemas are unavailable: code=%d stderr=%s output=%s", code, stderr, schemas)
	}
	for _, name := range []string{"code_workspace_symbols", "browser_scroll", "browser_key", "browser_upload"} {
		if !strings.Contains(schemas, `"name": "`+name+`"`) {
			t.Fatalf("tool schema catalog is missing %s", name)
		}
		schema, stderr, code := executeTestCommand(t, "tool", "schema", name)
		if code != 0 || !strings.Contains(schema, `"name": "`+name+`"`) ||
			!strings.Contains(schema, `"input_schema": {`) {
			t.Fatalf("schema lookup for %s failed: code=%d stderr=%s output=%s", name, code, stderr, schema)
		}
	}

	workPayload := `{"title":"Inspect parser","description":"Use strict JSON","priority":"high","acceptance_criteria":["tests pass"]}`
	createdWork, stderr, code := executeTestCommand(t,
		"tool", "invoke", "work_item_create", "--run", runID,
		"--operation-key", "cli-work-create", "--payload", workPayload,
	)
	workID := workItemIDPattern.FindString(createdWork)
	if code != 0 || workID == "" || !strings.Contains(createdWork, "entity_kind: work_item") ||
		!strings.Contains(createdWork, "replayed: false") {
		t.Fatalf("structured WorkItem create failed: code=%d stderr=%s output=%s", code, stderr, createdWork)
	}
	replayed, stderr, code := executeTestCommand(t,
		"tool", "invoke", "work_item_create", "--run", runID,
		"--operation-key", "cli-work-create", "--payload", workPayload,
	)
	if code != 0 || workItemIDPattern.FindString(replayed) != workID || !strings.Contains(replayed, "replayed: true") {
		t.Fatalf("structured WorkItem replay failed: code=%d stderr=%s output=%s", code, stderr, replayed)
	}
	changed, stderr, code := executeTestCommand(t,
		"tool", "invoke", "work_item_create", "--run", runID,
		"--operation-key", "cli-work-create", "--payload", `{"title":"Changed intent"}`,
	)
	if code != 4 || !strings.Contains(stderr, "different intent") || changed != "" {
		t.Fatalf("structured WorkItem conflict was unstable: code=%d stderr=%s output=%s", code, stderr, changed)
	}
	shownWork, stderr, code := executeTestCommand(t, "todo", "show", workID)
	if code != 0 || !strings.Contains(shownWork, "title: Inspect parser") || !strings.Contains(shownWork, "status: pending") {
		t.Fatalf("structured WorkItem is not visible to todo CLI: code=%d stderr=%s output=%s", code, stderr, shownWork)
	}

	token := "s" + "k-" + strings.Repeat("n", 28)
	payloadPath := filepath.Join(t.TempDir(), "note.json")
	if err := os.WriteFile(payloadPath, []byte(`{"title":"Provider result","content":"token=`+token+`","category":"observation","visibility":"root","pinned":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	createdNote, stderr, code := executeTestCommand(t,
		"tool", "invoke", "note_create", "--run", runID,
		"--operation-key", "cli-note-create", "--payload-file", payloadPath,
	)
	noteID := noteIDPattern.FindString(createdNote)
	if code != 0 || noteID == "" || strings.Contains(createdNote, token) ||
		!strings.Contains(createdNote, "entity_kind: note") {
		t.Fatalf("structured Note create failed: code=%d stderr=%s output=%s", code, stderr, createdNote)
	}
	shownNote, stderr, code := executeTestCommand(t, "note", "show", noteID)
	if code != 0 || strings.Contains(shownNote, token) || !strings.Contains(shownNote, "[REDACTED:") ||
		!strings.Contains(shownNote, "visibility: root") {
		t.Fatalf("structured Note is unsafe or unavailable: code=%d stderr=%s output=%s", code, stderr, shownNote)
	}

	invalid, stderr, code := executeTestCommand(t,
		"tool", "invoke", "note_create", "--run", runID,
		"--operation-key", "invalid-note", "--payload", `{"title":"x","content":"y","unknown":true}`,
	)
	if code != 2 || invalid != "" || !strings.Contains(stderr, "unknown field") {
		t.Fatalf("invalid structured payload was not rejected: code=%d stderr=%s output=%s", code, stderr, invalid)
	}
	denied, stderr, code := executeTestCommand(t,
		"tool", "invoke", "note_create", "--run", runID,
		"--operation-key", "denied-note", "--payload", `{"title":"Unsafe","content":"masscan 0.0.0.0/0"}`,
	)
	if code != 5 || !strings.Contains(denied, "tool note_create denied") || !strings.Contains(stderr, "policy denied") {
		t.Fatalf("structured Policy denial was unstable: code=%d stderr=%s output=%s", code, stderr, denied)
	}

	usage, stderr, code := executeTestCommand(t, "run", "usage", runID)
	if code != 0 || !strings.Contains(usage, "tool_calls: 5") {
		t.Fatalf("structured tool budget is inconsistent: code=%d stderr=%s output=%s", code, stderr, usage)
	}
	timeline, stderr, code := executeTestCommand(t, "run", "events", runID)
	if code != 0 || strings.Count(timeline, "work_item.created") != 1 || strings.Count(timeline, "note.created") != 1 ||
		strings.Count(timeline, "tool.completed") != 2 || strings.Count(timeline, "policy.decision") != 3 ||
		strings.Contains(timeline, token) || strings.Contains(timeline, "cli-work-create") ||
		strings.Contains(timeline, "cli-note-create") {
		t.Fatalf("structured tool timeline is incomplete or unsafe: code=%d stderr=%s output=%s", code, stderr, timeline)
	}
}
