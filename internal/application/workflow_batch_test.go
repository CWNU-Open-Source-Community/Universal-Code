package application

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
)

type workflowAfterBatchProvider struct {
	batchEditTestProvider
	request llm.ChatRequest
}

func (p *workflowAfterBatchProvider) Chat(_ context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	p.request = request
	action, err := json.Marshal(domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion,
		Kind: domain.SpecialistActionFinish, Message: "integration result received",
		Report: &domain.CompletionReport{Version: domain.CompletionReportVersion, Outcome: domain.CompletionSucceeded,
			Summary: "reviewed the committed integration result", WorkItemIDs: []string{}, NoteIDs: []string{}}})
	return &llm.ChatResponse{Text: string(action), Usage: llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}, err
}

func (p *workflowAfterBatchProvider) StreamChat(ctx context.Context, request llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	response, err := p.Chat(ctx, request)
	if err != nil {
		return nil, err
	}
	chunks := make(chan llm.ChatChunk, 2)
	chunks <- llm.ChatChunk{Text: response.Text}
	chunks <- llm.FinalChatChunk(response)
	close(chunks)
	return chunks, nil
}

func TestWorkflowBatchRunsRealIsolatedDeliveryAndWaitsForReview(t *testing.T) {
	f := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionAsk)
	batch, prepared := prepareBatchWorkbenchFixture(t, f)
	calls := 0
	batch.WithWorker(batchWorkbenchWorkerFunc(func(ctx context.Context, request BatchDeliveryWorkRequest) (BatchDeliveryWorkResult, error) {
		calls++
		folder := []string{"one", "two"}[request.Workspace.Ordinal-1]
		path := "internal/" + folder + "/workflow.txt"
		authority := request.Authority
		authority.OperationKey += "-create"
		change, err := batch.BatchProposeChange(ctx, BatchDeliveryChangeRequest{Authority: authority, Action: "create", Path: path, ExpectedSHA256: "missing", Content: "isolated workflow proof\n"})
		if err != nil {
			return BatchDeliveryWorkResult{}, err
		}
		authority.OperationKey = request.Authority.OperationKey + "-apply"
		if _, err := batch.BatchApplyChange(ctx, BatchDeliveryApplyRequest{Authority: authority, EditID: change.Value.ID, ExpectedOriginalSHA256: change.Value.OriginalHash, ExpectedProposedSHA256: change.Value.ProposedHash}); err != nil {
			return BatchDeliveryWorkResult{}, err
		}
		authority.OperationKey = request.Authority.OperationKey + "-commit"
		_, err = batch.BatchGitCommit(ctx, BatchDeliveryGitRequest{Authority: authority, Message: "deliver isolated workflow change"})
		return BatchDeliveryWorkResult{EvidenceRefs: []string{"test://workflow-real-git"}, Limitations: []string{"Controlled worker; real Git and validation"}}, err
	}))
	provider := &workflowAfterBatchProvider{}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	newService := func(workbench *BatchDeliveryWorkbenchService) *WorkflowService {
		return NewWorkflowService(f.store, NewSpecialistExecutionService(f.store, router, policy.NewDefaultChecker())).WithBatchDelivery(workbench)
	}
	service := newService(batch)
	spec := domain.WorkflowSpec{Version: domain.WorkflowVersion, Name: "isolated delivery workflow",
		Limits: domain.WorkflowLimits{MaxSteps: 8, MaxJobs: 3, MaxTurns: 64, MaxTokens: 100000, TimeoutMillis: 600000},
		Steps: []domain.WorkflowStep{
			{ID: "deliver", Kind: "batch", BatchPlan: &domain.WorkflowValueRef{Step: "$input", Path: "/plan_id"}},
			{ID: "report", Kind: "worker", Task: &domain.ChildTask{Title: "report", Goal: "Report the integration result", Skills: []string{"model.chat"}, TurnLimit: 2, TokenLimit: 64, TimeoutMillis: 60000},
				Inputs: map[string]domain.WorkflowValueRef{"delivery": {Step: "deliver"}}},
		}}
	definition, err := service.Save(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]string{"plan_id": prepared.Snapshot.Plan.ID})
	execution, err := service.Start(t.Context(), StartWorkflowRequest{RunID: f.run.ID, DefinitionID: definition.ID, Inputs: input, OperationKey: "workflow-batch-start-01"})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err = batch.RenewWorkbenchOwner(t.Context(), BatchDeliveryWorkbenchOwnerRequest{PlanID: prepared.Snapshot.Plan.ID,
		Ordinal: 1, ExpectedGeneration: 1, Confirm: true, OperationKey: "workflow-batch-recover-01", RequestedBy: f.root.ID})
	if err != nil || prepared.Children[0].Generation != 2 {
		t.Fatalf("owner recovery: %+v err=%v", prepared, err)
	}
	for i := 0; i < 3; i++ {
		ran, tickErr := service.RunDue(t.Context(), "batch-workflow-host", time.Now().UTC())
		if tickErr != nil || !ran {
			t.Fatalf("recovered delivery was not dispatched: ran=%v err=%v", ran, tickErr)
		}
		execution, err = service.Get(t.Context(), execution.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 || execution.Status != "waiting" || !strings.Contains(execution.Reason, "independent review") {
		t.Fatalf("calls=%d execution=%+v", calls, execution)
	}
	if len(execution.State.Outputs) != 0 {
		t.Fatal("unreviewed delivery was exposed as a completed result")
	}
	before := execution.Version
	for i := 0; i < 2; i++ {
		ran, tickErr := service.RunDue(t.Context(), "batch-workflow-host", time.Now().UTC())
		if tickErr != nil || ran {
			t.Fatalf("unchanged review wait was dispatched: ran=%v err=%v", ran, tickErr)
		}
	}
	execution, err = service.Get(t.Context(), execution.ID)
	if err != nil || execution.Version != before || calls != 2 {
		t.Fatalf("review wait changed checkpoint or repeated work: %+v calls=%d err=%v", execution, calls, err)
	}
	// Workflow restart reuses the durable receipts without repeating child work.
	service = newService(NewBatchDeliveryWorkbenchService(f.service))
	execution, err = service.Advance(t.Context(), execution.ID)
	if err != nil || calls != 2 || execution.Status != "waiting" {
		t.Fatalf("restart calls=%d execution=%+v err=%v", calls, execution, err)
	}
	for ordinal := 1; ordinal <= 2; ordinal++ {
		_, _, err = f.service.Review(t.Context(), ReviewBatchDeliveryRequest{PlanID: prepared.Snapshot.Plan.ID, Ordinal: ordinal, Generation: prepared.Snapshot.Workspaces[ordinal-1].Generation, Reviewer: f.root.ID,
			Verdict: domain.BatchReviewAccepted, Summary: "independent workflow test review", FullDiffReviewed: true, CallChainReviewed: true, TestsReviewed: true,
			OperationKey: fmt.Sprintf("workflow-batch-review-%d", ordinal)})
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5 && execution.Status != "completed"; i++ {
		_, err = service.RunDue(t.Context(), "batch-workflow-host", time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		execution, err = service.Get(t.Context(), execution.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	var output workflowBatchOutput
	if err := json.Unmarshal(execution.State.Outputs["deliver"], &output); err != nil {
		t.Fatalf("execution=%+v: %v", execution, err)
	}
	if execution.Status != "completed" || execution.State.Jobs != 3 || output.SourceUpdated || output.IntegrationHead == "" || len(output.ReviewIDs) != 2 {
		t.Fatalf("execution=%+v output=%+v", execution, output)
	}
	requestJSON, _ := json.Marshal(provider.request)
	if !strings.Contains(string(requestJSON), output.IntegrationHead) || !strings.Contains(string(requestJSON), output.ReceiptIDs[0]) {
		t.Fatalf("next shared worker did not receive the committed delivery: %s", requestJSON)
	}
	for _, workspace := range prepared.Snapshot.Workspaces {
		child, err := f.store.GetAgentNode(t.Context(), workspace.AgentID)
		if err != nil || !child.Terminal() || child.StatusReason != "isolated delivery integrated; worker closed" {
			t.Fatalf("integrated delivery kept a child slot reserved: %+v %v", child, err)
		}
	}
	snapshot, err := batch.Snapshot(t.Context(), output.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	for _, folder := range []string{"one", "two"} {
		relative := filepath.Join("internal", folder, "workflow.txt")
		if _, err := os.Stat(filepath.Join(snapshot.MergeQueue.IntegrationRoot, relative)); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(f.repository, relative)); !os.IsNotExist(err) {
			t.Fatalf("isolated delivery changed source: %v", err)
		}
	}
}
