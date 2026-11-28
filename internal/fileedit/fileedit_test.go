package fileedit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
)

type memoryStore struct {
	edits map[string]Edit
}

func newMemoryStore() *memoryStore {
	return &memoryStore{edits: map[string]Edit{}}
}

func (s *memoryStore) SaveFileEdit(ctx context.Context, edit Edit) (Edit, error) {
	s.edits[edit.ID] = edit
	return edit, nil
}

func (s *memoryStore) GetFileEdit(ctx context.Context, id string) (Edit, error) {
	edit, ok := s.edits[id]
	if !ok {
		return Edit{}, errors.New("edit not found")
	}
	return edit, nil
}

func (s *memoryStore) ListFileEdits(ctx context.Context, filter ListFilter) ([]Edit, error) {
	var edits []Edit
	for _, edit := range s.edits {
		if filter.WorkspaceID != "" && edit.WorkspaceID != filter.WorkspaceID {
			continue
		}
		if filter.SessionID != "" && edit.SessionID != filter.SessionID {
			continue
		}
		if filter.Status != "" && edit.Status != filter.Status {
			continue
		}
		edits = append(edits, edit)
	}
	return edits, nil
}

func TestManagerProposeAndApproveExistingFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "README.md")
	if err := os.WriteFile(path, []byte("# Old\n\nBody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(newMemoryStore())
	edit, err := manager.Propose(context.Background(), Proposal{
		WorkspaceID: "ws-demo", WorkspaceRoot: root, Path: "README.md", ProposedText: "# New\n\nBody\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if edit.Status != StatusProposed || !strings.Contains(edit.Diff, "-# Old") || !strings.Contains(edit.Diff, "+# New") {
		t.Fatalf("unexpected proposal: %#v", edit)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != "# Old\n\nBody\n" {
		t.Fatalf("proposal changed file: %q", before)
	}
	applied, err := manager.Approve(context.Background(), edit.ID, root)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Status != StatusApplied {
		t.Fatalf("expected applied status, got %#v", applied)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != "# New\n\nBody\n" {
		t.Fatalf("unexpected applied content: %q", after)
	}
	if leftovers, err := filepath.Glob(filepath.Join(root, ".cyberagent-edit-*")); err != nil || len(leftovers) != 0 {
		t.Fatalf("atomic replacement leftovers=%v err=%v", leftovers, err)
	}
}

func TestManagerApproveIntentDoesNotWriteWorkspace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "README.md")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(newMemoryStore())
	edit, err := manager.Propose(context.Background(), Proposal{
		WorkspaceID: "ws-demo", WorkspaceRoot: root, Path: "README.md", ProposedText: "new\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	reviewed, err := manager.ApproveIntent(context.Background(), edit.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reviewed.Status != StatusApproved {
		t.Fatalf("unexpected review state: %#v", reviewed)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old\n" {
		t.Fatalf("approval intent wrote the workspace: %q", data)
	}
	replayed, err := manager.ApproveIntent(context.Background(), edit.ID)
	if err != nil || replayed.Status != StatusApproved {
		t.Fatalf("approval intent replay failed: %#v %v", replayed, err)
	}
}

func TestManagerCreatesNewFileUnderExistingDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(newMemoryStore())
	edit, err := manager.Propose(context.Background(), Proposal{
		WorkspaceID: "ws-demo", WorkspaceRoot: root, Path: "scripts/new.go", ProposedText: "package main\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(edit.Diff, "+package main") {
		t.Fatalf("unexpected creation diff: %s", edit.Diff)
	}
	if _, err := manager.Approve(context.Background(), edit.ID, root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "scripts", "new.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "package main\n" {
		t.Fatalf("unexpected new file: %q", data)
	}
}

func TestManagerRejectsTraversalAndStaleProposal(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(newMemoryStore())
	if _, err := manager.Propose(context.Background(), Proposal{
		WorkspaceID: "ws-demo", WorkspaceRoot: root, Path: "../outside.txt", ProposedText: "bad\n",
	}); err == nil || !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("expected traversal rejection, got %v", err)
	}
	edit, err := manager.Propose(context.Background(), Proposal{
		WorkspaceID: "ws-demo", WorkspaceRoot: root, Path: "notes.txt", ProposedText: "new\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	failed, err := manager.Approve(context.Background(), edit.ID, root)
	if err == nil || !strings.Contains(err.Error(), "changed after") {
		t.Fatalf("expected stale proposal failure, got edit=%#v err=%v", failed, err)
	}
	if failed.Status != StatusFailed {
		t.Fatalf("expected failed status, got %#v", failed)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "changed elsewhere\n" {
		t.Fatalf("stale approval overwrote file: %q", data)
	}
}

func TestManagerRedactsSecretsBeforePersistenceAndWrite(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "outputs"), 0o755); err != nil {
		t.Fatal(err)
	}
	token := "t" + "p-" + strings.Repeat("a", 40)
	manager := NewManager(newMemoryStore())
	edit, err := manager.Propose(context.Background(), Proposal{
		WorkspaceID: "ws-demo", WorkspaceRoot: root, Path: "outputs/env.txt", ProposedText: "MIMO_API_KEY=" + token + "\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !edit.SecretsRedacted || strings.Contains(edit.ProposedText, token[:11]) {
		t.Fatalf("secret was not redacted: %#v", edit)
	}
	if _, err := manager.Approve(context.Background(), edit.ID, root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "outputs", "env.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), token[:11]) || !strings.Contains(string(data), "[REDACTED:secret]") {
		t.Fatalf("secret reached workspace file: %q", data)
	}
}

func TestManagerShowsSafeDiffWhenRedactedPreviewsMatch(t *testing.T) {
	root := t.TempDir()
	token := "t" + "p-" + strings.Repeat("b", 40)
	raw := "MIMO_API_KEY=" + token + "\n"
	path := filepath.Join(root, "env.txt")
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(newMemoryStore())
	edit, err := manager.Propose(context.Background(), Proposal{
		WorkspaceID: "ws-demo", WorkspaceRoot: root, Path: "env.txt", ProposedText: "MIMO_API_KEY=[REDACTED:secret]\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(edit.Diff, "existing sensitive content omitted") || strings.Contains(edit.Diff, token[:11]) {
		t.Fatalf("unexpected safe redaction diff: %s", edit.Diff)
	}
}

func TestManagerMoveAndDeleteUseExactHashes(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.txt")
	if err := os.WriteFile(source, []byte("move me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(newMemoryStore())
	sourceHash, err := CurrentHash(root, "source.txt")
	if err != nil {
		t.Fatal(err)
	}
	move, err := manager.Propose(context.Background(), Proposal{WorkspaceID: "ws-demo",
		WorkspaceRoot: root, Path: "source.txt", Operation: OperationMove,
		DestinationPath: "nested/destination.txt", ExpectedOriginalHash: sourceHash,
		ExpectedDestinationHash: missingHash})
	if err == nil {
		t.Fatal("move unexpectedly created a missing parent directory")
	}
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	move, err = manager.Propose(context.Background(), Proposal{WorkspaceID: "ws-demo",
		WorkspaceRoot: root, Path: "source.txt", Operation: OperationMove,
		DestinationPath: "nested/destination.txt", ExpectedOriginalHash: sourceHash,
		ExpectedDestinationHash: missingHash})
	if err != nil || move.ProposedHash != missingHash ||
		move.DestinationProposedHash != sourceHash || !strings.Contains(move.Diff, "move source.txt") {
		t.Fatalf("move proposal=%#v err=%v", move, err)
	}
	if _, err := manager.ApproveIntent(context.Background(), move.ID); err != nil {
		t.Fatal(err)
	}
	appliedMove, err := manager.Approve(context.Background(), move.ID, root)
	if err != nil || appliedMove.Status != StatusApplied {
		t.Fatalf("move apply=%#v err=%v", appliedMove, err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("move source still exists: %v", err)
	}
	destination := filepath.Join(root, "nested", "destination.txt")
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "move me\n" {
		t.Fatalf("move destination=%q err=%v", data, err)
	}
	destinationHash, err := CurrentHash(root, "nested/destination.txt")
	if err != nil {
		t.Fatal(err)
	}
	deletion, err := manager.Propose(context.Background(), Proposal{WorkspaceID: "ws-demo",
		WorkspaceRoot: root, Path: "nested/destination.txt", Operation: OperationDelete,
		ExpectedOriginalHash: destinationHash})
	if err != nil || deletion.ProposedHash != missingHash ||
		!strings.Contains(deletion.Diff, "delete nested/destination.txt") {
		t.Fatalf("delete proposal=%#v err=%v", deletion, err)
	}
	if _, err := manager.ApproveIntent(context.Background(), deletion.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Approve(context.Background(), deletion.ID, root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("delete target still exists: %v", err)
	}
}

func TestManagerMoveRefusesOccupiedDestinationAfterReview(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sourceHash, _ := CurrentHash(root, "source.txt")
	manager := NewManager(newMemoryStore())
	move, err := manager.Propose(context.Background(), Proposal{WorkspaceID: "ws-demo",
		WorkspaceRoot: root, Path: "source.txt", Operation: OperationMove,
		DestinationPath: "destination.txt", ExpectedOriginalHash: sourceHash,
		ExpectedDestinationHash: missingHash})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ApproveIntent(context.Background(), move.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "destination.txt"), []byte("occupied\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	failed, err := manager.Approve(context.Background(), move.ID, root)
	if apperror.CodeOf(err) != apperror.CodeConflict || failed.Status != StatusFailed {
		t.Fatalf("occupied destination move=%#v err=%v", failed, err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "source.txt"))
	if string(data) != "source\n" {
		t.Fatalf("failed move changed source: %q", data)
	}
}

func TestManagerMoveRecoversPublishedHardLinkAfterInterruptedApply(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.txt")
	destination := filepath.Join(root, "destination.txt")
	if err := os.WriteFile(source, []byte("recover me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sourceHash, err := CurrentHash(root, "source.txt")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(newMemoryStore())
	move, err := manager.Propose(context.Background(), Proposal{WorkspaceID: "ws-demo",
		WorkspaceRoot: root, Path: "source.txt", Operation: OperationMove,
		DestinationPath: "destination.txt", ExpectedOriginalHash: sourceHash,
		ExpectedDestinationHash: missingHash})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ApproveIntent(context.Background(), move.ID); err != nil {
		t.Fatal(err)
	}
	// This is the durable intermediate state of the no-clobber move: the
	// destination was published, but the process stopped before source removal.
	if err := os.Link(source, destination); err != nil {
		t.Fatal(err)
	}
	applied, err := manager.Approve(context.Background(), move.ID, root)
	if err != nil || applied.Status != StatusApplied {
		t.Fatalf("recover move=%#v err=%v", applied, err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("recovered move left its source: %v", err)
	}
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "recover me\n" {
		t.Fatalf("recovered destination=%q err=%v", data, err)
	}
}

func TestManagerMoveRechecksAutomaticAuthorityAtBothWriteBoundaries(t *testing.T) {
	for _, test := range []struct {
		name   string
		failAt int
	}{
		{name: "before link", failAt: 1},
		{name: "before source removal", failAt: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source.txt")
			destination := filepath.Join(root, "destination.txt")
			if err := os.WriteFile(source, []byte("bounded move\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			sourceHash, err := CurrentHash(root, "source.txt")
			if err != nil {
				t.Fatal(err)
			}
			manager := NewManager(newMemoryStore())
			move, err := manager.Propose(context.Background(), Proposal{WorkspaceID: "ws-demo",
				WorkspaceRoot: root, Path: "source.txt", Operation: OperationMove,
				DestinationPath: "destination.txt", ExpectedOriginalHash: sourceHash,
				ExpectedDestinationHash: missingHash})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.ApproveIntent(context.Background(), move.ID); err != nil {
				t.Fatal(err)
			}
			checks := 0
			_, err = manager.ApproveWithPreWriteCheck(context.Background(), move.ID, root,
				func() error {
					checks++
					if checks == test.failAt {
						return errors.New("Full Access was revoked")
					}
					return nil
				})
			if err == nil || checks != test.failAt {
				t.Fatalf("move authority checks=%d err=%v", checks, err)
			}
			if data, readErr := os.ReadFile(source); readErr != nil || string(data) != "bounded move\n" {
				t.Fatalf("revoked move changed source=%q err=%v", data, readErr)
			}
			if test.failAt == 1 {
				if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
					t.Fatalf("first-boundary rejection published destination: %v", statErr)
				}
			} else {
				if data, readErr := os.ReadFile(destination); readErr != nil || string(data) != "bounded move\n" {
					t.Fatalf("second-boundary rejection lost recoverable destination=%q err=%v", data, readErr)
				}
			}
		})
	}
}

func TestManagerMoveRecoveryRechecksAuthorityBeforeRemovingSource(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.txt")
	destination := filepath.Join(root, "destination.txt")
	if err := os.WriteFile(source, []byte("recoverable\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sourceHash, _ := CurrentHash(root, "source.txt")
	manager := NewManager(newMemoryStore())
	move, err := manager.Propose(context.Background(), Proposal{WorkspaceID: "ws-demo",
		WorkspaceRoot: root, Path: "source.txt", Operation: OperationMove,
		DestinationPath: "destination.txt", ExpectedOriginalHash: sourceHash,
		ExpectedDestinationHash: missingHash})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ApproveIntent(context.Background(), move.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(source, destination); err != nil {
		t.Fatal(err)
	}
	checks := 0
	if _, err := manager.ApproveWithPreWriteCheck(context.Background(), move.ID, root,
		func() error { checks++; return errors.New("grant revoked during recovery") }); err == nil || checks != 1 {
		t.Fatalf("linked recovery authority checks=%d err=%v", checks, err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("revoked linked recovery removed source: %v", err)
	}
	if _, err := os.Stat(destination); err != nil {
		t.Fatalf("revoked linked recovery removed destination: %v", err)
	}
}

func TestManagerMoveRefusesDestinationReplacementBetweenAuthorityChecks(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.txt")
	destination := filepath.Join(root, "destination.txt")
	if err := os.WriteFile(source, []byte("owned\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sourceHash, _ := CurrentHash(root, "source.txt")
	manager := NewManager(newMemoryStore())
	move, err := manager.Propose(context.Background(), Proposal{WorkspaceID: "ws-demo",
		WorkspaceRoot: root, Path: "source.txt", Operation: OperationMove,
		DestinationPath: "destination.txt", ExpectedOriginalHash: sourceHash,
		ExpectedDestinationHash: missingHash})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ApproveIntent(context.Background(), move.ID); err != nil {
		t.Fatal(err)
	}
	checks := 0
	failed, err := manager.ApproveWithPreWriteCheck(context.Background(), move.ID, root,
		func() error {
			checks++
			if checks == 2 {
				if err := os.Remove(destination); err != nil {
					return err
				}
				return os.WriteFile(destination, []byte("foreign\n"), 0o644)
			}
			return nil
		})
	if apperror.CodeOf(err) != apperror.CodeConflict || failed.Status != StatusFailed || checks != 2 {
		t.Fatalf("replaced destination move=%+v checks=%d err=%v", failed, checks, err)
	}
	if data, readErr := os.ReadFile(source); readErr != nil || string(data) != "owned\n" {
		t.Fatalf("destination replacement changed source=%q err=%v", data, readErr)
	}
	if data, readErr := os.ReadFile(destination); readErr != nil || string(data) != "foreign\n" {
		t.Fatalf("foreign destination was overwritten=%q err=%v", data, readErr)
	}
}

func TestManagerMoveRevalidatesSourceAfterAuthorizationCheck(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.txt")
	destination := filepath.Join(root, "destination.txt")
	if err := os.WriteFile(source, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sourceHash, _ := CurrentHash(root, "source.txt")
	manager := NewManager(newMemoryStore())
	move, err := manager.Propose(context.Background(), Proposal{WorkspaceID: "ws-demo",
		WorkspaceRoot: root, Path: "source.txt", Operation: OperationMove,
		DestinationPath: "destination.txt", ExpectedOriginalHash: sourceHash,
		ExpectedDestinationHash: missingHash})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ApproveIntent(context.Background(), move.ID); err != nil {
		t.Fatal(err)
	}
	checks := 0
	failed, err := manager.ApproveWithPreWriteCheck(context.Background(), move.ID, root,
		func() error {
			checks++
			return os.WriteFile(source, []byte("changed during check\n"), 0o644)
		})
	if apperror.CodeOf(err) != apperror.CodeConflict || failed.Status != StatusFailed || checks != 1 {
		t.Fatalf("source drift move=%+v checks=%d err=%v", failed, checks, err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("source drift was linked to destination: %v", err)
	}
	if data, readErr := os.ReadFile(source); readErr != nil || string(data) != "changed during check\n" {
		t.Fatalf("source drift was overwritten=%q err=%v", data, readErr)
	}
}

func TestManagerCreateRefusesConcurrentTargetAfterReview(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(newMemoryStore())
	created, err := manager.Propose(context.Background(), Proposal{WorkspaceID: "ws-demo",
		WorkspaceRoot: root, Path: "created.txt", Operation: OperationCreate,
		ExpectedOriginalHash: missingHash, ProposedText: "proposal\n"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ApproveIntent(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "created.txt")
	if err := os.WriteFile(target, []byte("concurrent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	failed, err := manager.Approve(context.Background(), created.ID, root)
	if apperror.CodeOf(err) != apperror.CodeConflict || failed.Status != StatusFailed {
		t.Fatalf("concurrent create=%#v err=%v", failed, err)
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil || string(data) != "concurrent\n" {
		t.Fatalf("concurrent target was overwritten: %q err=%v", data, readErr)
	}
}

func TestUnifiedDiffHandlesEmptyFile(t *testing.T) {
	diff := UnifiedDiff("new.txt", "", "hello\n")
	if !strings.Contains(diff, "@@ -0,0 +1,1 @@") || !strings.Contains(diff, "+hello") {
		t.Fatalf("unexpected diff: %s", diff)
	}
}
