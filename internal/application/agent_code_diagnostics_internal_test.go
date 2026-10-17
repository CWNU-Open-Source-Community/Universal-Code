package application

import (
	"strings"
	"testing"

	"cyberagent-workbench/internal/codeintel"
	"cyberagent-workbench/internal/fileedit"
)

func TestAppliedDiagnosticDeltaRequiresReviewedServerIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*appliedDiagnosticsFeedback)
		want   string
	}{
		{name: "same configuration new generation", want: "current"},
		{name: "changed descriptor", change: func(after *appliedDiagnosticsFeedback) {
			after.snapshot.DescriptorFingerprint = strings.Repeat("f", 64)
		}, want: "unavailable"},
		{name: "changed capability", change: func(after *appliedDiagnosticsFeedback) {
			after.Result.Provenance.CapabilityFingerprint = strings.Repeat("f", 64)
		}, want: "unavailable"},
		{name: "different server", change: func(after *appliedDiagnosticsFeedback) {
			after.Result.Provenance.ServerID = "other-server"
		}, want: "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			originalHash, proposedHash := fileedit.HashText("before\n"), fileedit.HashText("after\n")
			original := codeintel.Provenance{WorkspaceID: "workspace", RootFingerprint: strings.Repeat("a", 64),
				DocumentPath: "target.go", DocumentURI: "file:///workspace/target.go", DocumentSHA256: originalHash, DocumentVersion: 1,
				ServerID: "server", ServerGeneration: strings.Repeat("b", 64), CapabilityFingerprint: strings.Repeat("c", 64)}
			current := original
			current.DocumentSHA256, current.ServerGeneration = proposedHash, strings.Repeat("d", 64)
			before := appliedDiagnosticsFeedback{Result: &codeintel.Result{State: codeintel.EvidenceCurrent, Provenance: original},
				snapshot: codeintel.CapabilitySnapshot{DescriptorFingerprint: strings.Repeat("e", 64), Generation: original.ServerGeneration}}
			after := appliedDiagnosticsFeedback{Result: &codeintel.Result{State: codeintel.EvidenceCurrent, Provenance: current},
				snapshot: codeintel.CapabilitySnapshot{DescriptorFingerprint: strings.Repeat("e", 64), Generation: current.ServerGeneration}}
			if tc.change != nil {
				tc.change(&after)
			}
			delta := compareAppliedDiagnostics(before, after, ApplyFileEditResult{Edit: fileedit.Edit{
				Operation: fileedit.OperationReplace, OriginalHash: originalHash, ProposedHash: proposedHash}})
			if delta.Status != tc.want || (tc.want == "unavailable" && (delta.Reason != "server_changed" || delta.Added != nil || delta.Resolved != nil || delta.Persisting != nil)) {
				t.Fatalf("incorrect comparison for server identity: %+v", delta)
			}
		})
	}
}
