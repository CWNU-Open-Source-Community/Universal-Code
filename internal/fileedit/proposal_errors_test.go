package fileedit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/rootfs"
)

func TestProposalKnownPreconditionsHaveRecoverableCodesWithoutSaving(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Proposal)
		code   apperror.Code
	}{
		{"create existing seed", func(p *Proposal) { p.Operation = OperationCreate; p.ExpectedOriginalHash = missingHash }, apperror.CodeConflict},
		{"stale source", func(p *Proposal) { p.ExpectedOriginalHash = HashText("different source") }, apperror.CodeConflict},
		{"occupied move destination", func(p *Proposal) {
			p.Operation = OperationMove
			p.DestinationPath = "occupied.mjs"
			p.ExpectedDestinationHash = missingHash
			p.ProposedText = ""
		}, apperror.CodeConflict},
		{"missing move source", func(p *Proposal) {
			p.Operation = OperationMove
			p.Path = "absent.mjs"
			p.ExpectedOriginalHash = missingHash
			p.DestinationPath = "new.mjs"
			p.ExpectedDestinationHash = missingHash
			p.ProposedText = ""
		}, apperror.CodeConflict},
		{"missing delete source", func(p *Proposal) {
			p.Operation = OperationDelete
			p.Path = "absent.mjs"
			p.ExpectedOriginalHash = missingHash
			p.ProposedText = ""
		}, apperror.CodeConflict},
		{"create missing declaration", func(p *Proposal) { p.Operation = OperationCreate; p.Path = "absent.mjs"; p.ExpectedOriginalHash = "" }, apperror.CodeInvalidArgument},
		{"move missing destination hash", func(p *Proposal) { p.Operation = OperationMove; p.DestinationPath = "new.mjs"; p.ProposedText = "" }, apperror.CodeInvalidArgument},
		{"unchanged content", func(p *Proposal) { p.ProposedText = "export const seed = true;\n" }, apperror.CodeInvalidArgument},
		{"delete replacement data", func(p *Proposal) { p.Operation = OperationDelete }, apperror.CodeInvalidArgument},
		{"invalid relative path", func(p *Proposal) { p.Path = "../outside.mjs" }, apperror.CodeInvalidArgument},
		{"invalid operation", func(p *Proposal) { p.Operation = "invented" }, apperror.CodeInvalidArgument},
		{"invalid proposed UTF8", func(p *Proposal) { p.ProposedText = string([]byte{0xff}) }, apperror.CodeInvalidArgument},
		{"oversized proposed body", func(p *Proposal) { p.ProposedText = strings.Repeat("x", MaxContentBytes+1) }, apperror.CodeInvalidArgument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "src"), 0o755); err != nil {
				t.Fatal(err)
			}
			original := "export const seed = true;\n"
			path := filepath.Join(root, "src", "event-ledger.mjs")
			if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "occupied.mjs"), []byte("unrelated\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			store := newMemoryStore()
			manager := NewManager(store)
			proposal := Proposal{WorkspaceID: "fixture-workspace", WorkspaceRoot: root, SessionID: "fixture-session", Path: "src/event-ledger.mjs", Operation: OperationReplace, ExpectedOriginalHash: HashText(original), ProposedText: "export const seed = false;\n"}
			tc.change(&proposal)
			_, err := manager.Propose(context.Background(), proposal)
			if apperror.CodeOf(err) != tc.code || apperror.CodeOf(apperror.Normalize(err)) != tc.code {
				t.Fatalf("precondition classification=%s normalized=%s want=%s err=%v", apperror.CodeOf(err), apperror.CodeOf(apperror.Normalize(err)), tc.code, err)
			}
			if len(store.edits) != 0 {
				t.Fatal("rejected proposal was saved")
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != original {
				t.Fatalf("preflight changed original: %q %v", data, err)
			}
			data, err = os.ReadFile(filepath.Join(root, "occupied.mjs"))
			if err != nil || string(data) != "unrelated\n" {
				t.Fatal("preflight changed destination")
			}
		})
	}
}

type proposalFailingStore struct {
	*memoryStore
	failure error
}

func (s proposalFailingStore) SaveFileEdit(context.Context, Edit) (Edit, error) {
	return Edit{}, s.failure
}

func TestProposalUnknownIOAndPersistenceErrorsKeepTheirCause(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "source.txt")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	handle, err := rootfs.OpenExactRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, err = readCurrentTextFromRoot(handle, "source.txt")
	if err == nil || !errors.Is(err, os.ErrClosed) || apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeInternal {
		t.Fatalf("unknown file IO became recoverable or lost cause: %v", err)
	}
	failure := &os.PathError{Op: "write", Path: "isolated-proposal-ledger", Err: errors.New("device I/O failure")}
	manager := NewManager(proposalFailingStore{memoryStore: newMemoryStore(), failure: failure})
	_, err = manager.Propose(context.Background(), Proposal{WorkspaceID: "fixture", WorkspaceRoot: root, Path: "source.txt", ProposedText: "after", ExpectedOriginalHash: HashText("before")})
	if !errors.Is(err, failure) || apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeInternal {
		t.Fatalf("uncertain persistence was misclassified: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "before" {
		t.Fatal("proposal wrote its workspace")
	}
}

func TestProposalReadDistinguishesKnownSourceLimitsFromIOFailure(t *testing.T) {
	for _, kind := range []string{"directory", "non UTF8", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "source")
			switch kind {
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "non UTF8":
				if err := os.WriteFile(path, []byte{0xff, 0xfe}, 0o600); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				if err := os.WriteFile(path, []byte(strings.Repeat("x", MaxContentBytes+1)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			handle, err := rootfs.OpenExactRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			defer handle.Close()
			_, _, err = readProposalTextFromRoot(handle, "source")
			if apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
				t.Fatalf("known source limitation classified as %s: %v", apperror.CodeOf(err), err)
			}
		})
	}
}

func TestSharedReadFailureWithUnknownSaveFailureRemainsInternal(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source"), []byte{0xff}, 0o600); err != nil {
		t.Fatal(err)
	}
	handle, err := rootfs.OpenExactRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	_, _, readErr := readCurrentTextFromRoot(handle, "source")
	if readErr == nil {
		t.Fatal("expected invalid UTF8 read failure")
	}
	saveErr := &os.PathError{Op: "write", Path: "isolated-edit-ledger", Err: errors.New("device I/O failure")}
	manager := NewManager(proposalFailingStore{memoryStore: newMemoryStore(), failure: saveErr})
	_, err = manager.fail(context.Background(), Edit{}, readErr)
	if !errors.Is(err, readErr) || !errors.Is(err, saveErr) || apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeInternal {
		t.Fatalf("shared read / uncertain save became recoverable or lost a cause: %v", err)
	}
	if apperror.CodeOf(apperror.Normalize(readErr)) != apperror.CodeInternal {
		t.Fatalf("shared read itself became recoverable outside proposal preparation: %v", readErr)
	}
}
