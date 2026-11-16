package gitadvanced

import (
	"strings"
	"testing"
	"time"
)

func TestSpecValidateRejectsFieldsOutsideSelectedOperation(t *testing.T) {
	oid := strings.Repeat("a", 40)
	tests := []Spec{
		{ProtocolVersion: ProtocolVersion, Operation: HunkStage,
			Message: "ignored but approval-bound"},
		{ProtocolVersion: ProtocolVersion, Operation: StashDrop,
			StashOID: oid, RestoreIndex: true},
		{ProtocolVersion: ProtocolVersion, Operation: BisectReset,
			SequenceID: "sequence-1", ExpectedCurrent: oid},
		{ProtocolVersion: ProtocolVersion, Operation: WorktreePrune,
			WorktreeName: "ignored"},
	}
	for _, spec := range tests {
		if err := spec.Validate(); err == nil || !strings.Contains(err.Error(), "outside") {
			t.Fatalf("operation %s accepted an unrelated field: %v", spec.Operation, err)
		}
	}
}

func TestSpecAndCapabilityRejectNonCanonicalDuplicateOrPartialContracts(t *testing.T) {
	spec := Spec{ProtocolVersion: ProtocolVersion, Operation: HunkStage,
		Paths: []string{"same.txt", "same.txt"}}
	if err := spec.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate paths were accepted: %v", err)
	}
	now := time.Now().UTC()
	capability := CapabilitySnapshot{ProtocolVersion: CapabilityProtocolVersion,
		Enabled: true, Generation: strings.Repeat("a", 64),
		ManagedRootSHA256: strings.Repeat("b", 64), Operations: Operations(),
		MaxHunks: MaxHunks, MaxPaths: MaxPaths, MaxCommits: MaxCommits, CapturedAt: now}
	capability.Operations = capability.Operations[:len(capability.Operations)-1]
	if err := capability.Validate(); err == nil {
		t.Fatal("enabled capability accepted a partial operation set")
	}
}

func TestSpecValidateRejectsGitInvalidWorktreeBranches(t *testing.T) {
	for _, branch := range []string{"@", ".hidden/topic", "topic/.hidden", "topic.lock",
		"topic/component.LOCK", "topic\x1fcontrol"} {
		spec := Spec{ProtocolVersion: ProtocolVersion, Operation: WorktreeCreate,
			WorktreeName: "review", Branch: branch, Commit: strings.Repeat("b", 40)}
		if err := spec.Validate(); err == nil {
			t.Fatalf("invalid branch %q was accepted", branch)
		}
	}
}

func TestSpecValidateRejectsNonPortableOrMetadataPaths(t *testing.T) {
	for _, candidate := range []string{
		"file:stream", "line\nbreak.txt", "line\u0085break.txt", "folder/.",
		"./file.txt", "folder/.git/config", "folder/ .git /config", ".git./config",
	} {
		spec := Spec{ProtocolVersion: ProtocolVersion, Operation: HunkStage,
			Paths: []string{candidate}}
		if err := spec.Validate(); err == nil {
			t.Fatalf("unsafe path %q was accepted", candidate)
		}
	}
}

func TestSpecPreservesExactSpacedPaths(t *testing.T) {
	spec := Spec{ProtocolVersion: ProtocolVersion, Operation: HunkStage,
		Paths: []string{"draft.txt", " draft.txt", "draft.txt ", " notes / draft.txt "}}
	if err := spec.Validate(); err != nil {
		t.Fatalf("distinct Git paths were rejected: %v", err)
	}
}

func TestReceiptValidateRequiresTypedTerminalEvidence(t *testing.T) {
	now := time.Now().UTC()
	digest := strings.Repeat("a", 64)
	binding := RepositoryBinding{ProtocolVersion: ProtocolVersion,
		RepositorySHA256: digest, CommonDirSHA256: digest, Head: strings.Repeat("b", 40),
		IndexSHA256: digest, WorktreeSHA256: digest, StatusSHA256: digest,
		StashSHA256: digest, SequenceSHA256: digest, ObjectFormat: "sha1", CapturedAt: now}
	receipt := Receipt{ProtocolVersion: ReceiptProtocolVersion, ID: "receipt-1",
		PreviewID: "preview-1", Operation: StashCreate, Status: ReceiptFailed,
		PreBinding: binding, ErrorCode: FailureInterrupted, ErrorSummary: "interrupted",
		StartedAt: now, CompletedAt: now}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("valid failed receipt was rejected: %v", err)
	}
	receipt.ErrorCode = "made_up"
	if err := receipt.Validate(); err == nil {
		t.Fatal("receipt accepted an unknown failure code")
	}
	receipt.ErrorCode = FailureInterrupted
	receipt.Status = ReceiptSucceeded
	if err := receipt.Validate(); err == nil {
		t.Fatal("successful receipt accepted failure-only evidence")
	}
}
