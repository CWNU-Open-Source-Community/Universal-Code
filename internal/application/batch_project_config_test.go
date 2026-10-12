package application

import (
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/projectconfig"
)

func TestBatchWorkspaceToolsUseProjectExclusions(t *testing.T) {
	fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionAsk,
		&projectconfig.Effective{Protocol: projectconfig.ProtocolVersion, ExcludePaths: []string{"**/base.txt"}})
	prepared, err := fixture.service.Prepare(t.Context(), PrepareBatchDeliveryRequest{
		RunID: fixture.run.ID, ProposalID: fixture.proposal.ID, Spec: fixture.spec,
		OperationKey: "batch-project-prepare-0001", RequestedBy: fixture.root.ID, WorktreeParent: t.TempDir(), Confirm: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	authority := BatchDeliveryToolAuthority{PlanID: prepared.Plan.ID, Ordinal: 1, Generation: 1, OwnerToken: prepared.Authorities[0].OwnerToken}
	if _, _, _, err := fixture.service.SendMessage(t.Context(), SendBatchDeliveryMessageRequest{
		PlanID: authority.PlanID, Ordinal: 1, Generation: 1, OwnerToken: authority.OwnerToken,
		Kind: domain.BatchMailboxAck, Summary: "ready", OperationKey: "batch-project-ack-0001",
	}); err != nil {
		t.Fatal(err)
	}
	authority.OperationKey = "batch-project-read-0001"
	if _, err := fixture.service.BatchRead(t.Context(), BatchDeliveryReadRequest{Authority: authority, Path: "internal/one/base.txt", StartLine: 1, EndLine: 10}); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("child read ignored project exclusions: %v", err)
	}
	authority.OperationKey = "batch-project-search-0001"
	result, err := fixture.service.BatchGlob(t.Context(), BatchDeliveryGlobRequest{Authority: authority, Pattern: "**", Limit: 20})
	if err != nil || len(result.Value.Paths) != 1 || result.Value.Paths[0] != "internal/one/value.go" {
		t.Fatalf("child search = %+v, %v", result.Value, err)
	}
	authority.OperationKey = "batch-project-create-0001"
	if _, err := fixture.service.BatchProposeChange(t.Context(), BatchDeliveryChangeRequest{Authority: authority, Action: "create", Path: "internal/one/base.txt", ExpectedSHA256: "missing", Content: "replacement"}); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("child proposal ignored project exclusions: %v", err)
	}
}
