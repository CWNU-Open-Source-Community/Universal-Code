package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

type batchWorkbenchWorkerFunc func(context.Context, BatchDeliveryWorkRequest) (BatchDeliveryWorkResult, error)

func (f batchWorkbenchWorkerFunc) ExecuteBatchChild(ctx context.Context, request BatchDeliveryWorkRequest) (BatchDeliveryWorkResult, error) {
	return f(ctx, request)
}

func prepareBatchWorkbenchFixture(t *testing.T, fixture batchDeliveryApplicationFixture) (*BatchDeliveryWorkbenchService, BatchDeliveryWorkbenchSnapshot) {
	t.Helper()
	s := NewBatchDeliveryWorkbenchService(fixture.service)
	inputs := make([]BatchDeliveryWorkbenchTask, len(fixture.spec.Tasks))
	for i, task := range fixture.spec.Tasks {
		inputs[i] = BatchDeliveryWorkbenchTask{Ordinal: task.Ordinal, OwnershipHints: task.OwnershipHints, Validations: task.Validations}
	}
	result, err := s.PrepareWorkbench(t.Context(), PrepareBatchDeliveryWorkbenchRequest{RunID: fixture.run.ID, ProposalID: fixture.proposal.ID,
		OperationKey: "workbench-prepare-0001", RequestedBy: fixture.root.ID, Confirm: true, Tasks: inputs})
	if err != nil {
		t.Fatal(err)
	}
	return s, result
}

func TestBatchWorkbenchRealScopedToolsReviewReworkAndReplay(t *testing.T) {
	fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionAsk)
	s, prepared := prepareBatchWorkbenchFixture(t, fixture)
	if prepared.WorkerAvailable || len(prepared.Children) != 2 || !prepared.Children[0].OwnerAvailable {
		t.Fatalf("prepared: %#v", prepared)
	}
	planID := prepared.Snapshot.Plan.ID
	calls := 0
	var feedback string
	s.WithWorker(batchWorkbenchWorkerFunc(func(ctx context.Context, request BatchDeliveryWorkRequest) (BatchDeliveryWorkResult, error) {
		calls++
		feedback = request.Feedback
		if request.Task.Goal != fixture.proposal.Spec.Tasks[request.Workspace.Ordinal-1].Goal {
			t.Fatal("admitted task changed")
		}
		authority := request.Authority
		authority.OperationKey += "-create"
		path := fmt.Sprintf("internal/one/revision%d.txt", request.Workspace.Generation)
		preview, err := fixture.service.BatchProposeChange(ctx, BatchDeliveryChangeRequest{Authority: authority, Action: "create", Path: path, ExpectedSHA256: "missing", Content: "tested child change\n"})
		if err != nil {
			return BatchDeliveryWorkResult{}, err
		}
		authority.OperationKey = request.Authority.OperationKey + "-apply"
		_, err = fixture.service.BatchApplyChange(ctx, BatchDeliveryApplyRequest{Authority: authority, EditID: preview.Value.ID,
			ExpectedOriginalSHA256: preview.Value.OriginalHash, ExpectedProposedSHA256: preview.Value.ProposedHash})
		if err != nil {
			return BatchDeliveryWorkResult{}, err
		}
		authority.OperationKey = request.Authority.OperationKey + "-commit"
		_, err = fixture.service.BatchGitCommit(ctx, BatchDeliveryGitRequest{Authority: authority, Message: "deliver isolated revision"})
		return BatchDeliveryWorkResult{EvidenceRefs: []string{"test://scoped-real-git"}, Limitations: []string{"Git diff checks only"}}, err
	}))
	request := ExecuteBatchDeliveryWorkbenchRequest{PlanID: planID, Ordinal: 1, ExpectedGeneration: 1, OperationKey: "workbench-execute-0001", Confirm: true}
	result, err := s.ExecuteWorkbench(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Snapshot.Receipts) != 1 || result.Snapshot.Workspaces[0].Status != domain.BatchWorkspaceReadyForReview || result.Children[0].Executing {
		t.Fatalf("executed: %#v", result)
	}
	result, err = s.ExecuteWorkbench(t.Context(), request)
	if err != nil || !result.Replayed || calls != 1 {
		t.Fatalf("execution replay calls=%d replay=%t err=%v", calls, result.Replayed, err)
	}
	_, _, err = s.Review(t.Context(), ReviewBatchDeliveryRequest{PlanID: planID, Ordinal: 1, Generation: 1,
		Reviewer: fixture.root.ID, Verdict: domain.BatchReviewChangesRequested, Summary: "Add the bounded regression example", FullDiffReviewed: true, CallChainReviewed: true, TestsReviewed: true, OperationKey: "workbench-review-0001"})
	if err != nil {
		t.Fatal(err)
	}
	ownerRequest := BatchDeliveryWorkbenchOwnerRequest{PlanID: planID, Ordinal: 1, ExpectedGeneration: 1, Retry: true, Confirm: true,
		OperationKey: "workbench-retry-0001", RequestedBy: fixture.root.ID}
	retried, err := s.RenewWorkbenchOwner(t.Context(), ownerRequest)
	if err != nil || retried.Children[0].Generation != 2 || retried.Snapshot.Workspaces[0].Status != domain.BatchWorkspaceWorking {
		t.Fatalf("retry=%#v err=%v", retried, err)
	}
	retried, err = s.RenewWorkbenchOwner(t.Context(), ownerRequest)
	if err != nil || !retried.Replayed || retried.Children[0].Generation != 2 || retried.Snapshot.Workspaces[0].Status != domain.BatchWorkspaceWorking {
		t.Fatalf("retry replay=%#v err=%v", retried, err)
	}
	request.ExpectedGeneration, request.OperationKey = 2, "workbench-execute-0002"
	_, err = s.ExecuteWorkbench(t.Context(), request)
	if err != nil || calls != 2 || feedback != "Add the bounded regression example" {
		t.Fatalf("rework calls=%d feedback=%q err=%v", calls, feedback, err)
	}
}

func TestBatchWorkbenchRestartAndUnknownOutcomeRequireExplicitRecovery(t *testing.T) {
	fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionAsk)
	s, prepared := prepareBatchWorkbenchFixture(t, fixture)
	calls := 0
	worker := batchWorkbenchWorkerFunc(func(context.Context, BatchDeliveryWorkRequest) (BatchDeliveryWorkResult, error) {
		calls++
		return BatchDeliveryWorkResult{}, errors.New("model outcome unknown")
	})
	s.WithWorker(worker)
	request := ExecuteBatchDeliveryWorkbenchRequest{PlanID: prepared.Snapshot.Plan.ID, Ordinal: 1,
		ExpectedGeneration: 1, OperationKey: "workbench-uncertain-0001", Confirm: true}
	if _, err := s.ExecuteWorkbench(t.Context(), request); err == nil {
		t.Fatal("unknown outcome was swallowed")
	}
	if _, err := s.ExecuteWorkbench(t.Context(), request); err != nil || calls != 1 {
		t.Fatalf("unknown replay dispatched calls=%d err=%v", calls, err)
	}
	restarted := NewBatchDeliveryWorkbenchService(fixture.service).WithWorker(worker)
	state, err := restarted.WorkbenchSnapshot(t.Context(), request.PlanID)
	if err != nil || state.Children[0].OwnerAvailable || !state.Children[0].OutcomeUnresolved {
		t.Fatalf("restart resurrected owner: %#v %v", state, err)
	}
	diagnosticFound := false
	for _, message := range state.Snapshot.Mailbox[1] {
		if message.Kind == domain.BatchMailboxQuestion && strings.HasPrefix(message.Summary, "child execution stopped:") {
			diagnosticFound = true
			if strings.Contains(message.Summary, "model outcome unknown") {
				t.Fatal("arbitrary worker error was persisted")
			}
		}
	}
	if !diagnosticFound {
		t.Fatal("stable execution diagnostic was not persisted")
	}
	request.OperationKey = "workbench-after-restart-0001"
	if _, err := restarted.ExecuteWorkbench(t.Context(), request); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("restart did not fence: %v", err)
	}
	recovered, err := restarted.RenewWorkbenchOwner(t.Context(), BatchDeliveryWorkbenchOwnerRequest{PlanID: request.PlanID,
		Ordinal: 1, ExpectedGeneration: 1, Confirm: true, OperationKey: "workbench-recover-0001", RequestedBy: fixture.root.ID})
	if err != nil || recovered.Children[0].Generation != 2 || !recovered.Children[0].OwnerAvailable || recovered.Snapshot.Workspaces[0].Status != domain.BatchWorkspaceAcknowledged {
		t.Fatalf("recover=%#v err=%v", recovered, err)
	}
	if _, err := restarted.ExecuteWorkbench(t.Context(), request); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale writer accepted: %v", err)
	}
	request.ExpectedGeneration = 2
	if _, err := restarted.ExecuteWorkbench(t.Context(), request); err == nil || !strings.Contains(err.Error(), "unknown") || calls != 2 {
		t.Fatalf("explicit new generation calls=%d err=%v", calls, err)
	}
}

func TestBatchWorkbenchExecutionFencesDependenciesConfirmationAndLongRecoveryKeys(t *testing.T) {
	fixture := newBatchDeliveryApplicationFixture(t, true, domain.RunExecutionPermissionAsk)
	s, prepared := prepareBatchWorkbenchFixture(t, fixture)
	calls := 0
	s.WithWorker(batchWorkbenchWorkerFunc(func(context.Context, BatchDeliveryWorkRequest) (BatchDeliveryWorkResult, error) {
		calls++
		return BatchDeliveryWorkResult{}, nil
	}))
	request := ExecuteBatchDeliveryWorkbenchRequest{PlanID: prepared.Snapshot.Plan.ID, Ordinal: 1,
		ExpectedGeneration: 1, OperationKey: "workbench-confirm-0001"}
	if _, err := s.ExecuteWorkbench(t.Context(), request); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("unconfirmed execution: %v", err)
	}
	request.Ordinal, request.Confirm = 2, true
	if _, err := s.ExecuteWorkbench(t.Context(), request); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("unaccepted dependency: %v", err)
	}
	if calls != 0 {
		t.Fatal("fenced request called worker")
	}
	state, err := s.WorkbenchSnapshot(t.Context(), request.PlanID)
	if err != nil || state.Children[1].OutcomeUnresolved {
		t.Fatalf("preflight was recorded as initiated execution: %#v %v", state.Children, err)
	}
	_, err = s.RenewWorkbenchOwner(t.Context(), BatchDeliveryWorkbenchOwnerRequest{PlanID: request.PlanID, Ordinal: 1,
		ExpectedGeneration: 1, Confirm: true, OperationKey: strings.Repeat("x", domain.MaxAgentOperationKeyBytes), RequestedBy: fixture.root.ID})
	if apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("long recovery key: %v", err)
	}
	state, err = s.WorkbenchSnapshot(t.Context(), request.PlanID)
	if err != nil || state.Children[0].Generation != 1 {
		t.Fatalf("invalid key rotated owner: %#v %v", state.Children, err)
	}
	ownerRequest := BatchDeliveryWorkbenchOwnerRequest{PlanID: request.PlanID, Ordinal: 2,
		ExpectedGeneration: 1, Confirm: true, OperationKey: "workbench-dependent-owner", RequestedBy: fixture.root.ID}
	state, err = s.RenewWorkbenchOwner(t.Context(), ownerRequest)
	if err != nil || state.Children[1].Generation != 2 || !state.Children[1].OwnerAvailable ||
		state.Snapshot.Workspaces[1].Status != domain.BatchWorkspaceAcknowledged {
		t.Fatalf("dependent owner recovery: %#v %v", state, err)
	}
	state, err = s.RenewWorkbenchOwner(t.Context(), ownerRequest)
	if err != nil || !state.Replayed || state.Children[1].Generation != 2 {
		t.Fatalf("dependent recovery replay: %#v %v", state, err)
	}
	request.ExpectedGeneration = 2
	if _, err := s.ExecuteWorkbench(t.Context(), request); apperror.CodeOf(err) != apperror.CodeFailedPrecondition || calls != 0 {
		t.Fatalf("recovered child bypassed dependency: calls=%d err=%v", calls, err)
	}
}

func TestBatchWorkbenchReplaysHistoricalQuestionOwnerMarker(t *testing.T) {
	fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionAsk)
	s, prepared := prepareBatchWorkbenchFixture(t, fixture)
	request := BatchDeliveryWorkbenchOwnerRequest{PlanID: prepared.Snapshot.Plan.ID, Ordinal: 1,
		ExpectedGeneration: 1, Confirm: true, OperationKey: "workbench-legacy-owner-001", RequestedBy: fixture.root.ID}
	child, owner, err := s.RenewOwner(t.Context(), RenewBatchDeliveryOwnerRequest{PlanID: request.PlanID,
		Ordinal: request.Ordinal, ExpectedGeneration: request.ExpectedGeneration, RequestedBy: request.RequestedBy, Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	// Persist the marker emitted by earlier versions, including its old kind.
	for _, message := range []struct {
		kind    domain.BatchDeliveryMailboxKind
		summary string
		suffix  string
	}{
		{domain.BatchMailboxAck, "child owner recovered", "-ack"},
		{domain.BatchMailboxQuestion, "owner recovery from generation 1; retry=false", "-owner"},
	} {
		_, _, _, err = s.SendMessage(t.Context(), SendBatchDeliveryMessageRequest{PlanID: request.PlanID,
			Ordinal: child.Ordinal, Generation: child.Generation, OwnerToken: owner.OwnerToken, Kind: message.kind,
			Summary: message.summary, OperationKey: request.OperationKey + message.suffix})
		if err != nil {
			t.Fatal(err)
		}
	}
	replayed, err := s.RenewWorkbenchOwner(t.Context(), request)
	if err != nil || !replayed.Replayed || replayed.Children[0].Generation != 2 || replayed.Children[0].OwnerAvailable ||
		replayed.Snapshot.Workspaces[0].Status != domain.BatchWorkspaceQuestion {
		t.Fatalf("historical recovery replay=%+v err=%v", replayed, err)
	}
}

func TestBatchWorkbenchSerializesOneChildAndFencesOwnerRotationWhileExecuting(t *testing.T) {
	fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionAsk)
	s, prepared := prepareBatchWorkbenchFixture(t, fixture)
	started, release := make(chan struct{}), make(chan struct{})
	s.WithWorker(batchWorkbenchWorkerFunc(func(context.Context, BatchDeliveryWorkRequest) (BatchDeliveryWorkResult, error) {
		close(started)
		<-release
		return BatchDeliveryWorkResult{}, errors.New("bounded worker stopped")
	}))
	request := ExecuteBatchDeliveryWorkbenchRequest{PlanID: prepared.Snapshot.Plan.ID, Ordinal: 1,
		ExpectedGeneration: 1, OperationKey: "workbench-concurrent-0001", Confirm: true}
	finished := make(chan error, 1)
	go func() { _, err := s.ExecuteWorkbench(t.Context(), request); finished <- err }()
	<-started
	state, err := s.WorkbenchSnapshot(t.Context(), request.PlanID)
	if err != nil || !state.Children[0].Executing || state.Children[0].OutcomeUnresolved {
		t.Errorf("running projection=%#v err=%v", state.Children, err)
	}
	if _, err := s.ExecuteWorkbench(t.Context(), request); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Errorf("concurrent execution=%v", err)
	}
	if _, err := s.RenewWorkbenchOwner(t.Context(), BatchDeliveryWorkbenchOwnerRequest{PlanID: request.PlanID, Ordinal: 1,
		ExpectedGeneration: 1, Confirm: true, OperationKey: "workbench-concurrent-owner", RequestedBy: fixture.root.ID}); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Errorf("concurrent rotation=%v", err)
	}
	close(release)
	if err := <-finished; err == nil {
		t.Fatal("worker failure was swallowed")
	}
}
