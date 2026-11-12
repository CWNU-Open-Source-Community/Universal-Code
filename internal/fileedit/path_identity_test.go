package fileedit

import (
	"os"
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/rootfs"
)

func TestManagerPreservesWhitespaceInFileOperations(t *testing.T) {
	root := t.TempDir()
	native := func(path string) string {
		t.Helper()
		full, err := rootfs.ExactAbsolutePath(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		return full
	}
	write := func(path, data string) {
		t.Helper()
		full := native(path)
		if err := os.WriteFile(full, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(full) })
	}
	assertFile := func(path, want string) {
		t.Helper()
		data, err := os.ReadFile(native(path))
		if err != nil || string(data) != want {
			t.Fatalf("file %q = %q, %v; want %q", path, data, err, want)
		}
	}
	const regular = "file.txt"
	const spaced = "file.txt "
	const moved = " file.txt "
	write(regular, "ordinary")
	write(spaced, "spaced")
	manager := NewManager(newMemoryStore())
	apply := func(proposal Proposal) {
		t.Helper()
		proposal.WorkspaceID, proposal.WorkspaceRoot = "ws-exact", root
		edit, err := manager.Propose(t.Context(), proposal)
		if err != nil {
			t.Fatal(err)
		}
		if edit.Path != proposal.Path || edit.DestinationPath != proposal.DestinationPath {
			t.Fatalf("stored names changed: %q -> %q", edit.Path, edit.DestinationPath)
		}
		if proposal.Operation != "" && proposal.Operation != OperationReplace {
			if _, err := manager.ApproveIntent(t.Context(), edit.ID); err != nil {
				t.Fatal(err)
			}
		}
		if applied, err := manager.Approve(t.Context(), edit.ID, root); err != nil || applied.Status != StatusApplied {
			t.Fatalf("apply %q: %s, %v", edit.Path, applied.Status, err)
		}
	}
	apply(Proposal{Path: spaced, ExpectedOriginalHash: HashText("spaced"), ProposedText: "updated"})
	assertFile(spaced, "updated")
	assertFile(regular, "ordinary")

	t.Cleanup(func() { _ = os.Remove(native(moved)) })
	apply(Proposal{Operation: OperationMove, Path: spaced, DestinationPath: moved,
		ExpectedOriginalHash: HashText("updated"), ExpectedDestinationHash: missingHash})
	assertFile(moved, "updated")
	if _, err := os.Stat(native(spaced)); !os.IsNotExist(err) {
		t.Fatalf("moved source still exists: %v", err)
	}
	apply(Proposal{Operation: OperationDelete, Path: moved, ExpectedOriginalHash: HashText("updated")})
	if _, err := os.Stat(native(moved)); !os.IsNotExist(err) {
		t.Fatalf("deleted path still exists: %v", err)
	}
	assertFile(regular, "ordinary")

	const created = " new / "
	t.Cleanup(func() { _ = os.RemoveAll(native(" new ")) })
	apply(Proposal{Operation: OperationCreate, Path: created,
		ExpectedOriginalHash: missingHash, ProposedText: "created"})
	assertFile(created, "created")
}
