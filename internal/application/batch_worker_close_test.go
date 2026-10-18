package application

import (
	"errors"
	"os"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/session"
)

type batchCleanupTestWorker struct {
	BatchDeliveryWorker
	close func(domain.AgentAttemptRef) error
}

func (w batchCleanupTestWorker) CloseBatchWorkerResources(ref domain.AgentAttemptRef) error {
	return w.close(ref)
}

func TestBatchCancelPreservesWorktreesUntilWorkerCleanupConfirmed(t *testing.T) {
	f := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionAsk)
	batch, prepared := prepareBatchWorkbenchFixture(t, f)
	if _, err := f.store.CloseTerminalBatchWorkers(t.Context(), prepared.Snapshot.Plan.ID); err == nil {
		t.Fatal("live Batch workers closed before the durable terminal fence")
	}
	fail := true
	closed := map[string]int{}
	batch.WithWorker(batchCleanupTestWorker{close: func(ref domain.AgentAttemptRef) error {
		plan, found, err := f.store.GetBatchDeliveryPlan(t.Context(), prepared.Snapshot.Plan.ID)
		if err != nil || !found || plan.Status != domain.BatchDeliveryAborted {
			t.Fatalf("cleanup ran before durable cancellation: %+v %v", plan, err)
		}
		if ref.RunID != f.run.ID || ref.AttemptID != "" {
			t.Fatalf("cleanup must cover this worker's continued attempts: %+v", ref)
		}
		node, err := f.store.GetAgentNode(t.Context(), ref.AgentID)
		if err != nil || !node.Terminal() || node.ActiveAttemptID != "" {
			t.Fatalf("cleanup ran before worker retirement: %+v %v", node, err)
		}
		closed[ref.AgentID]++
		if fail {
			return errors.New("process shutdown not confirmed")
		}
		return nil
	}})
	request := CancelBatchDeliveryRequest{PlanID: prepared.Snapshot.Plan.ID, OperationKey: "cancel-worker-cleanup-1", RequestedBy: "operator", Reason: "stop isolated workers", Confirm: true}
	if _, err := batch.Cancel(t.Context(), request); err == nil {
		t.Fatal("cancel reported success without confirming worker cleanup")
	}
	for _, workspace := range prepared.Snapshot.Workspaces {
		if _, err := os.Stat(workspace.WorktreeRoot); err != nil {
			t.Fatalf("unconfirmed process lost its worktree: %v", err)
		}
	}
	fail = false
	result, err := batch.Cancel(t.Context(), request)
	if err != nil || !result.Replayed {
		t.Fatalf("cancellation retry failed: %+v %v", result, err)
	}
	for _, workspace := range prepared.Snapshot.Workspaces {
		if closed[workspace.AgentID] != 2 {
			t.Fatalf("worker cleanup retry count: %+v", closed)
		}
		if _, err := os.Stat(workspace.WorktreeRoot); !os.IsNotExist(err) {
			t.Fatalf("clean cancelled worktree remains: %v", err)
		}
	}
}

func TestBatchCancelWaitingApprovalRetiresWorkersWithoutModelReplay(t *testing.T) {
	f := newBatchDeliveryApplicationFixtureWithWorker(t, false, domain.RunExecutionPermissionAsk, &domain.WorkerProfile{})
	batch, prepared := prepareBatchWorkbenchFixture(t, f)
	owned := prepared.Snapshot.Workspaces[0]
	provider := &workerBatchTestProvider{nativeBatchTestProvider: &nativeBatchTestProvider{t: t, root: owned.WorktreeRoot}, input: commandApprovalNativeInput(t, false)}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	runtime := batchWorkerTestRuntime(t, f, router)
	batch.WithWorker(NewBatchDeliveryModelWorker(f.service, f.store, router, policy.NewDefaultChecker()).WithNativeTools().WithWorkerRuntime(runtime))
	execute := ExecuteBatchDeliveryWorkbenchRequest{PlanID: prepared.Snapshot.Plan.ID, Ordinal: 1, ExpectedGeneration: 1, OperationKey: "batch-cancel-wait-execute", Confirm: true}
	waiting, err := batch.ExecuteWorkbench(t.Context(), execute)
	if err != nil || !waiting.Children[0].WaitingApproval || provider.calls != 1 {
		t.Fatalf("worker did not reach an exact approval wait: %+v calls=%d err=%v", waiting, provider.calls, err)
	}
	attempts, err := f.store.ListAgentAttempts(t.Context(), owned.AgentID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("waiting attempts: %+v %v", attempts, err)
	}
	root, err := f.store.GetAgentNode(t.Context(), f.root.ID)
	if err != nil {
		t.Fatal(err)
	}
	next := domain.SpecialistAdmission{AgentID: "batch-after-cancel", SessionID: "batch-after-cancel-session", RunID: f.run.ID, ParentAgentID: root.ID, Title: "use released Batch capacity", Skills: []string{"model.chat"}, TurnLimit: 17, TokenLimit: 99000, MaxChildren: root.ChildLimit, CreatedAt: time.Now().UTC()}
	if _, _, err := f.store.AdmitSpecialist(t.Context(), next, "batch-after-cancel-admit"); err == nil {
		t.Fatal("fixture did not reserve the Batch worker capacity and budget")
	}
	cancel := CancelBatchDeliveryRequest{PlanID: execute.PlanID, OperationKey: "batch-cancel-wait", RequestedBy: "operator", Reason: "cancel suspended work", Confirm: true}
	for replay := 0; replay < 2; replay++ {
		result, err := batch.Cancel(t.Context(), cancel)
		if err != nil || result.Replayed != (replay == 1) {
			t.Fatalf("cancel replay %d: %+v %v", replay, result, err)
		}
	}
	for _, workspace := range prepared.Snapshot.Workspaces {
		node, err := f.store.GetAgentNode(t.Context(), workspace.AgentID)
		if err != nil || node.Status != domain.AgentCancelled || node.ActiveAttemptID != "" {
			t.Fatalf("cancelled Batch kept a child reservation: %+v %v", node, err)
		}
		sess, err := f.store.GetSession(t.Context(), node.SessionID)
		if err != nil || sess.Status != session.StatusArchived {
			t.Fatalf("cancelled worker session: %+v %v", sess, err)
		}
	}
	interrupted, found, err := f.store.GetAgentAttempt(t.Context(), attempts[0].ID)
	if err != nil || !found || interrupted.Status != domain.AgentAttemptInterrupted || interrupted.Failure.Code != "batch_delivery_cancelled" || interrupted.Usage != attempts[0].Usage {
		t.Fatalf("cancelled approval lost attempt accounting: %+v %v", interrupted, err)
	}
	replayed, err := batch.ExecuteWorkbench(t.Context(), execute)
	if err != nil || !replayed.Replayed || replayed.Children[0].WaitingApproval || provider.calls != 1 {
		t.Fatalf("cancelled operation replay dispatched a model: %+v calls=%d err=%v", replayed, provider.calls, err)
	}
	if _, _, err := f.store.AdmitSpecialist(t.Context(), next, "batch-after-cancel-admit"); err != nil {
		t.Fatalf("cancelled Batch did not release remaining turn/token reservations: %v", err)
	}
}

func TestBatchStartupClosesCompletedWorkersAndReconcileRetriesCleanup(t *testing.T) {
	f := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionAsk)
	prepared, _ := prepareAcceptedBatchTextDeliveries(t, f, "terminal-close")
	// Simulate interruption after the durable final merge receipt but before
	// the public Merge wrapper retires workers and closes their resources.
	mergeRequest := MergeBatchDeliveryRequest{PlanID: " " + prepared.Plan.ID + " ", OperationKey: "terminal-close-merge", RequestedBy: f.root.ID, WorktreeParent: t.TempDir(), Confirm: true}
	merged, err := f.service.merge(t.Context(), mergeRequest)
	if err != nil || merged.Queue.Status != domain.BatchMergeQueueCompleted {
		t.Fatalf("prepare interrupted terminal close: %+v %v", merged, err)
	}
	closeFailure := errors.New("resource shutdown unconfirmed")
	closeCalls := 0
	f.service.workerResources = batchCleanupTestWorker{close: func(ref domain.AgentAttemptRef) error {
		closeCalls++
		node, err := f.store.GetAgentNode(t.Context(), ref.AgentID)
		if err != nil || !node.Terminal() || node.ActiveAttemptID != "" {
			t.Fatalf("runtime close preceded durable worker retirement: %+v %v", node, err)
		}
		return closeFailure
	}}
	results, err := f.service.ReconcileStartup(t.Context(), 256)
	if !errors.Is(err, closeFailure) || len(results) != 1 || !results[0].MergeCompleted || !results[0].NeedsOperatorAttention || closeCalls != len(prepared.Workspaces) {
		t.Fatalf("startup missed completed workers or hid cleanup failure: %+v closed=%d err=%v", results, closeCalls, err)
	}
	versions := map[string]int64{}
	for _, workspace := range prepared.Workspaces {
		node, err := f.store.GetAgentNode(t.Context(), workspace.AgentID)
		if err != nil || node.Status != domain.AgentCancelled || node.StatusReason != "isolated delivery integrated; worker closed" {
			t.Fatalf("completed worker still reserved: %+v %v", node, err)
		}
		versions[node.ID] = node.Version
	}
	closeFailure = nil
	result, err := f.service.Reconcile(t.Context(), prepared.Plan.ID)
	if err != nil || !result.MergeCompleted || closeCalls != 2*len(prepared.Workspaces) {
		t.Fatalf("completed replay did not retry only cleanup: %+v closed=%d err=%v", result, closeCalls, err)
	}
	queue, _, err := f.store.GetBatchDeliveryMergeQueue(t.Context(), merged.Queue.ID)
	if err != nil || queue.IntegrationHead != merged.Queue.IntegrationHead || queue.NextIndex != merged.Queue.NextIndex {
		t.Fatalf("cleanup replay changed integration: %+v %v", queue, err)
	}
	mergeReplay, err := f.service.Merge(t.Context(), mergeRequest)
	if err != nil || !mergeReplay.Replayed || mergeReplay.Queue.ID != merged.Queue.ID || closeCalls != 3*len(prepared.Workspaces) {
		t.Fatalf("normalized completed merge did not repeat cleanup: %+v closed=%d err=%v", mergeReplay, closeCalls, err)
	}
	for _, workspace := range prepared.Workspaces {
		node, err := f.store.GetAgentNode(t.Context(), workspace.AgentID)
		if err != nil || node.Version != versions[node.ID] {
			t.Fatalf("cleanup replay changed terminal node: %+v %v", node, err)
		}
	}
	if pending, err := f.store.ListRecoverableBatchDeliveryPlans(t.Context(), 256); err != nil || len(pending) != 0 {
		t.Fatalf("settled terminal plan kept a startup recovery slot: %+v %v", pending, err)
	}
}
