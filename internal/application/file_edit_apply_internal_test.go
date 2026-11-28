package application

import (
	"strings"
	"testing"

	"cyberagent-workbench/internal/fileedit"
)

// The operation-key contract (normalized UTF-8, 16-256 bytes, no
// whitespace) must reach the operator: the proposal hint invites
// human-scale keys like "op-edit-1", which silently failed as a bare
// "idempotency key is invalid" before the rule was surfaced.
func TestFileEditApplyInvalidOperationKeyNamesTheContract(t *testing.T) {
	_, err := normalizeFileEditApplyRequest(ApplyFileEditRequest{
		Version: fileedit.FileEditApplyProtocolVersion, RunID: "run-x",
		EditID: "edit-x", AppliedBy: "op", OperationKey: "op-edit-1",
	})
	if err == nil {
		t.Fatal("short operation key must be rejected")
	}
	if !strings.Contains(err.Error(), "between 16 and 256 bytes") {
		t.Fatalf("short key error must state the length contract: %v", err)
	}
	_, err = normalizeFileEditApplyRequest(ApplyFileEditRequest{
		Version: fileedit.FileEditApplyProtocolVersion, RunID: "run-x",
		EditID: "edit-x", AppliedBy: "op", OperationKey: "file-edit-apply-operation-0001 ",
	})
	if err == nil {
		t.Fatal("whitespace-padded key must be rejected")
	}
	if !strings.Contains(err.Error(), "normalized UTF-8") {
		t.Fatalf("padded key error must state the normalization contract: %v", err)
	}
}
