package fileedit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/rootfs"
)

func TestApprovePostPublishVerificationFailureKeepsTheRecordReconcilable(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "README.md")
	if err := os.WriteFile(path, []byte("# Old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(newMemoryStore())
	edit, err := manager.Propose(context.Background(), Proposal{
		WorkspaceID: "ws-demo", WorkspaceRoot: root, Path: "README.md", ProposedText: "# New\n",
	})
	if err != nil {
		t.Fatal(err)
	}

	original := verifyPublishedTextFn
	defer func() { verifyPublishedTextFn = original }()
	verifyPublishedTextFn = func(*rootfs.ExactRoot, string, string) error {
		return errors.New("transient post-publish read failure")
	}
	if _, approveErr := manager.Approve(context.Background(), edit.ID, root); approveErr == nil {
		t.Fatal("post-publish verification failure did not surface")
	}

	// The workspace already holds the proposed content, and the record stayed
	// in its reconcilable approved state instead of sealing StatusFailed.
	written, readErr := os.ReadFile(path)
	if readErr != nil || string(written) != "# New\n" {
		t.Fatalf("workspace content after publish: %q %v", written, readErr)
	}
	stored, getErr := manager.Get(context.Background(), edit.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if stored.Status != StatusApproved {
		t.Fatalf("post-publish failure downgraded the record: %s", stored.Status)
	}

	// The retry reconciles through the hash recovery path without writing.
	reconciled, approveErr := manager.Approve(context.Background(), edit.ID, root)
	if approveErr != nil {
		t.Fatalf("retry after a post-publish failure did not reconcile: %v", approveErr)
	}
	if reconciled.Status != StatusApplied {
		t.Fatalf("reconciled record status: %s", reconciled.Status)
	}
	written, readErr = os.ReadFile(path)
	if readErr != nil || string(written) != "# New\n" {
		t.Fatalf("reconciled content changed: %q %v", written, readErr)
	}
}

func TestApproveMovePostPublishFailureKeepsTheRecordReconcilable(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.txt")
	if err := os.WriteFile(source, []byte("move me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
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
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ApproveIntent(context.Background(), move.ID); err != nil {
		t.Fatal(err)
	}

	original := verifyPublishedMoveFn
	defer func() { verifyPublishedMoveFn = original }()
	verifyPublishedMoveFn = func(*rootfs.ExactRoot, string, string, string) error {
		return errors.New("transient post-publish read failure")
	}
	if _, approveErr := manager.Approve(context.Background(), move.ID, root); approveErr == nil {
		t.Fatal("post-publish move verification failure did not surface")
	}

	// The move happened: the source is gone and the destination holds the
	// proposed content, while the record stays reconcilable.
	if _, statErr := os.Stat(source); !os.IsNotExist(statErr) {
		t.Fatalf("move source still exists: %v", statErr)
	}
	moved, readErr := os.ReadFile(filepath.Join(root, "nested", "destination.txt"))
	if readErr != nil || string(moved) != "move me\n" {
		t.Fatalf("move destination content: %q %v", moved, readErr)
	}
	stored, getErr := manager.Get(context.Background(), move.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if stored.Status != StatusApproved {
		t.Fatalf("post-publish failure downgraded the record: %s", stored.Status)
	}

	// The retry reconciles through the entry recovery path without moving
	// anything again.
	reconciled, approveErr := manager.Approve(context.Background(), move.ID, root)
	if approveErr != nil {
		t.Fatalf("retry after a post-publish failure did not reconcile: %v", approveErr)
	}
	if reconciled.Status != StatusApplied {
		t.Fatalf("reconciled record status: %s", reconciled.Status)
	}
	if _, statErr := os.Stat(source); !os.IsNotExist(statErr) {
		t.Fatal("reconciled retry moved the source back")
	}
}

func TestApproveDeletePostPublishFailureKeepsTheRecordReconcilable(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "gone.txt")
	if err := os.WriteFile(path, []byte("delete me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(newMemoryStore())
	hash, err := CurrentHash(root, "gone.txt")
	if err != nil {
		t.Fatal(err)
	}
	deletion, err := manager.Propose(context.Background(), Proposal{WorkspaceID: "ws-demo",
		WorkspaceRoot: root, Path: "gone.txt", Operation: OperationDelete,
		ExpectedOriginalHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ApproveIntent(context.Background(), deletion.ID); err != nil {
		t.Fatal(err)
	}

	original := verifyPublishedDeleteFn
	defer func() { verifyPublishedDeleteFn = original }()
	verifyPublishedDeleteFn = func(*rootfs.ExactRoot, string) error {
		return errors.New("transient post-publish read failure")
	}
	if _, approveErr := manager.Approve(context.Background(), deletion.ID, root); approveErr == nil {
		t.Fatal("post-publish delete verification failure did not surface")
	}

	// The deletion happened and the record stays reconcilable.
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("delete target still exists: %v", statErr)
	}
	stored, getErr := manager.Get(context.Background(), deletion.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if stored.Status != StatusApproved {
		t.Fatalf("post-publish failure downgraded the record: %s", stored.Status)
	}
}
