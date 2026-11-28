package app

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"cyberagent-workbench/internal/rootfs"
)

func TestReadEditContentPreservesWhitespace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.txt")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if content, err := readEditContent(path + " "); !os.IsNotExist(err) {
		t.Fatalf("missing spaced path read %q, error %v", content, err)
	}
	exact, err := rootfs.ExactAbsolutePath(path + " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exact, []byte("spaced"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(exact) })
	if content, err := readEditContent(path + " "); err != nil || content != "spaced" {
		t.Fatalf("spaced path read %q, error %v", content, err)
	}
}

// An out-of-band change to the proposed file between proposal and approval
// is a conflict (two writers on one file), not an internal failure: exit 4
// with the operator's on-disk content preserved.
func TestEditApproveConflictExitCodeOnExternalModification(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CYBERAGENT_HOME", home)
	if _, stderr, code := executeTestCommand(t, "workspace", "init", "conflict-edit-ws"); code != 0 {
		t.Fatalf("workspace init failed: %s", stderr)
	}
	proposed, stderr, code := executeTestCommand(t, "edit", "propose",
		"--workspace", "conflict-edit-ws", "--path", "integrity.txt", "--content", "proposed v1")
	if code != 0 {
		t.Fatalf("edit propose failed: %s", stderr)
	}
	editID := regexp.MustCompile(`edit-[0-9]+-[a-f0-9]+`).FindString(proposed)
	if editID == "" {
		t.Fatalf("edit id missing: %q", proposed)
	}
	target := filepath.Join(home, "workspaces", "conflict-edit-ws", "integrity.txt")
	if err := os.WriteFile(target, []byte("operator changed this\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = executeTestCommand(t, "edit", "approve", editID)
	if code != 4 || !strings.Contains(stderr, "changed after the proposal") {
		t.Fatalf("external modification must be a conflict with exit 4: stderr=%q code=%d", stderr, code)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "operator changed this\n" {
		t.Fatalf("operator content must survive the refused apply: %q err=%v", data, err)
	}
}
