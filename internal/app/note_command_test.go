package app

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"cyberagent-workbench/internal/domain"
)

var noteIDPattern = regexp.MustCompile(`note-[0-9]{14}-[a-f0-9]{12}`)

func TestNoteCLIEndToEndLifecycle(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	createdRun, stderr, code := executeTestCommand(t, "run", "create", "note cli lifecycle", "--profile", "code")
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
	contentPath := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(contentPath, []byte("Strict parser decision.\nKeep source metadata."), 0o600); err != nil {
		t.Fatal(err)
	}
	created, stderr, code := executeTestCommand(t, "note", "create", runID, "parser decision",
		"--content-file", contentPath, "--category", "decision", "--visibility", "run", "--pin",
		"--owner-agent", rootAgentID, "--tag", "Security", "--tag", "parser",
		"--source", "docs/spec.md", "--evidence", "evidence-1")
	if code != 0 {
		t.Fatalf("note create failed: %s", stderr)
	}
	noteID := noteIDPattern.FindString(created)
	if noteID == "" || !strings.Contains(created, "version: 1") || !strings.Contains(created, "pinned: true") {
		t.Fatalf("unexpected note create output: %s", created)
	}
	listed, stderr, code := executeTestCommand(t, "note", "list", runID, "--category", "decision",
		"--owner-agent", rootAgentID, "--tag", "security", "--tag", "parser", "--pinned", "true")
	if code != 0 || !strings.Contains(listed, noteID) || !strings.Contains(listed, "pinned") || !strings.Contains(listed, "tags=2") {
		t.Fatalf("unexpected note list output=%s stderr=%s code=%d", listed, stderr, code)
	}
	shown, stderr, code := executeTestCommand(t, "note", "show", noteID)
	if code != 0 || !strings.Contains(shown, "owner_agent: "+rootAgentID) ||
		!strings.Contains(shown, "Strict parser decision.") || !strings.Contains(shown, "tags[1]: parser") ||
		!strings.Contains(shown, "sources[1]: docs/spec.md") || !strings.Contains(shown, "evidence[1]: evidence-1") {
		t.Fatalf("unexpected note show output=%s stderr=%s code=%d", shown, stderr, code)
	}
	if _, stderr, code := executeTestCommand(t, "note", "update", noteID, "--tag", "new", "--clear-tags"); code != 2 || !strings.Contains(stderr, "usage:") {
		t.Fatalf("conflicting tag flags returned code=%d stderr=%s", code, stderr)
	}
	updated, stderr, code := executeTestCommand(t, "note", "update", noteID, "--content", "Updated decision.",
		"--visibility", "root", "--clear-tags", "--unpin", "--version", "1")
	if code != 0 || !strings.Contains(updated, "version: 2") || !strings.Contains(updated, "visibility: root") || !strings.Contains(updated, "pinned: false") {
		t.Fatalf("unexpected note update output=%s stderr=%s code=%d", updated, stderr, code)
	}
	shown, stderr, code = executeTestCommand(t, "note", "get", noteID)
	if code != 0 || !strings.Contains(shown, "Updated decision.") || !strings.Contains(shown, "tags: -") {
		t.Fatalf("updated note not visible output=%s stderr=%s code=%d", shown, stderr, code)
	}
	if _, stderr, code := executeTestCommand(t, "note", "archive", noteID, "--version", "1"); code != 4 || !strings.Contains(stderr, "changed concurrently") {
		t.Fatalf("stale archive returned code=%d stderr=%s", code, stderr)
	}
	archived, stderr, code := executeTestCommand(t, "note", "archive", noteID, "--version", "2")
	if code != 0 || !strings.Contains(archived, "status: archived") || !strings.Contains(archived, "version: 3") {
		t.Fatalf("unexpected archive output=%s stderr=%s code=%d", archived, stderr, code)
	}
	archivedList, stderr, code := executeTestCommand(t, "note", "list", runID, "--status", "archived")
	if code != 0 || !strings.Contains(archivedList, noteID) {
		t.Fatalf("unexpected archived list output=%s stderr=%s code=%d", archivedList, stderr, code)
	}
	restored, stderr, code := executeTestCommand(t, "note", "restore", noteID, "--version", "3")
	if code != 0 || !strings.Contains(restored, "status: active") || !strings.Contains(restored, "version: 4") {
		t.Fatalf("unexpected restore output=%s stderr=%s code=%d", restored, stderr, code)
	}
	timeline, stderr, code := executeTestCommand(t, "run", "events", runID)
	if code != 0 || strings.Count(timeline, "note.created") != 1 || strings.Count(timeline, "note.changed") != 3 {
		t.Fatalf("unexpected note timeline output=%s stderr=%s code=%d", timeline, stderr, code)
	}
}

func TestNoteCLIValidationAndHelp(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	help, stderr, code := executeTestCommand(t, "help")
	if code != 0 || stderr != "" || !strings.Contains(help, "cyberagent note create|list|show") {
		t.Fatalf("note command missing from help output=%s stderr=%s code=%d", help, stderr, code)
	}
	createdRun, stderr, code := executeTestCommand(t, "run", "create", "note validation", "--profile", "review")
	if code != 0 {
		t.Fatalf("run create failed: %s", stderr)
	}
	runID := runIDPattern.FindString(createdRun)
	if _, stderr, code := executeTestCommand(t, "note", "create", runID, "private", "--content", "content", "--visibility", "owner"); code != 2 || !strings.Contains(stderr, "requires an owner") {
		t.Fatalf("owner validation returned code=%d stderr=%s", code, stderr)
	}
	if _, stderr, code := executeTestCommand(t, "note", "list", runID, "--category", "invalid"); code != 2 || !strings.Contains(stderr, "invalid note category") {
		t.Fatalf("category validation returned code=%d stderr=%s", code, stderr)
	}
	if _, stderr, code := executeTestCommand(t, "note", "list", runID, "--pinned", "maybe"); code != 2 || !strings.Contains(stderr, "invalid note pinned filter") {
		t.Fatalf("pinned validation returned code=%d stderr=%s", code, stderr)
	}
}

func TestReadNoteContentBoundsAndUTF8(t *testing.T) {
	large := filepath.Join(t.TempDir(), "large.txt")
	if err := os.WriteFile(large, []byte(strings.Repeat("x", domain.MaxNoteContentBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readNoteContent(large); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected oversized note rejection, got %v", err)
	}
	invalid := filepath.Join(t.TempDir(), "invalid.txt")
	if err := os.WriteFile(invalid, []byte{0xff, 0xfe}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readNoteContent(invalid); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("expected invalid UTF-8 rejection, got %v", err)
	}
}
