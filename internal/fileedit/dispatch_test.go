package fileedit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/rootfs"
)

func TestManagerDeleteRechecksAuthorityAndTargetBeforeRemoval(t *testing.T) {
	for _, change := range []string{"revoke", "before approval", "replace", "allow"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "delete.txt")
			if err := os.WriteFile(target, []byte("original\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			hash, _ := CurrentHash(root, "delete.txt")
			manager := NewManager(newMemoryStore())
			edit, err := manager.Propose(context.Background(), Proposal{WorkspaceID: "workspace", WorkspaceRoot: root,
				Path: "delete.txt", Operation: OperationDelete, ExpectedOriginalHash: hash})
			if err != nil {
				t.Fatal(err)
			}
			wantChecks := 1
			if change == "before approval" {
				if err := os.WriteFile(target, []byte("concurrent\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				wantChecks = 0
			}
			checks := 0
			_, err = manager.ApproveWithPreWriteCheck(context.Background(), edit.ID, root, func() error {
				checks++
				if change == "revoke" {
					return errors.New("authority revoked")
				}
				if change == "replace" {
					return os.WriteFile(target, []byte("concurrent\n"), 0o644)
				}
				return nil
			})
			if checks != wantChecks {
				t.Fatalf("checks=%d, want %d", checks, wantChecks)
			}
			data, readErr := os.ReadFile(target)
			if change == "allow" {
				if err != nil || !os.IsNotExist(readErr) {
					t.Fatalf("delete err=%v read=%v", err, readErr)
				}
			} else {
				want := "original\n"
				if change == "replace" || change == "before approval" {
					want = "concurrent\n"
					if apperror.CodeOf(err) != apperror.CodeConflict {
						t.Fatalf("concurrent delete returned %v", err)
					}
				}
				if err == nil || readErr != nil || string(data) != want {
					t.Fatalf("denied delete data=%q err=%v read=%v", data, err, readErr)
				}
			}
		})
	}
}

func TestManagerReplaceRechecksContentAfterDispatchCheck(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "replace.txt")
	if err := os.WriteFile(target, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(newMemoryStore())
	edit, err := manager.Propose(context.Background(), Proposal{WorkspaceID: "workspace", WorkspaceRoot: root,
		Path: "replace.txt", ProposedText: "proposed\n"})
	if err != nil {
		t.Fatal(err)
	}
	checks := 0
	_, err = manager.ApproveWithPreWriteCheck(context.Background(), edit.ID, root, func() error {
		checks++
		if checks == 2 {
			return os.WriteFile(target, []byte("concurrent\n"), 0o644)
		}
		return nil
	})
	data, readErr := os.ReadFile(target)
	if apperror.CodeOf(err) != apperror.CodeConflict || checks != 2 || readErr != nil || string(data) != "concurrent\n" {
		t.Fatalf("replace checks=%d data=%q err=%v read=%v", checks, data, err, readErr)
	}
}

func TestFileEditChecksPreserveIOErrors(t *testing.T) {
	root, err := rootfs.OpenExactRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		checkEditHash(root, "source.txt", "expected", "source changed"),
		checkEditIdentity(root, "source.txt", "dest.txt", "move changed"),
	} {
		if !errors.Is(err, os.ErrClosed) || apperror.CodeOf(err) == apperror.CodeConflict {
			t.Fatalf("closed-root check returned %v", err)
		}
	}
	for _, cause := range []error{os.ErrExist, os.ErrPermission} {
		err := classifyLinkConflict(cause, "destination already exists")
		if !errors.Is(err, cause) || (apperror.CodeOf(err) == apperror.CodeConflict) != (cause == os.ErrExist) {
			t.Fatalf("link error %v returned %v", cause, err)
		}
	}
}
