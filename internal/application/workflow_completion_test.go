package application_test

import (
	"context"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
)

type deadlineWorkflowProvider struct {
	*specialistTestProvider
	deadline time.Time
}

func (p *deadlineWorkflowProvider) Chat(ctx context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	p.deadline, _ = ctx.Deadline()
	return p.specialistTestProvider.Chat(ctx, request)
}

func (p *deadlineWorkflowProvider) StreamChat(ctx context.Context, request llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	p.deadline, _ = ctx.Deadline()
	return p.specialistTestProvider.StreamChat(ctx, request)
}

func TestWorkflowTurnUsesRemainingOverallDeadline(t *testing.T) {
	p := &deadlineWorkflowProvider{specialistTestProvider: &specialistTestProvider{responses: []llm.ChatResponse{workflowResponse(t, domain.CompletionSucceeded)}}}
	st, run, _, _ := newWorkflowFixture(t, p.specialistTestProvider)
	router := llm.NewRouter(llm.ModelRef{Provider: p.Name(), Model: "model"})
	router.RegisterProvider(p)
	service := application.NewWorkflowService(st, application.NewSpecialistExecutionService(st, router, policy.NewDefaultChecker()))
	spec := workflowSpec(workflowWorker("one"))
	spec.Limits.TimeoutMillis = 30000
	spec.Steps[0].Task.TimeoutMillis = 120000
	d, err := service.Save(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	e, err := service.Start(t.Context(), application.StartWorkflowRequest{RunID: run.ID, DefinitionID: d.ID, OperationKey: "workflow-overall-deadline"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := e.CreatedAt.Add(30 * time.Second)
	e, err = service.Advance(t.Context(), e.ID)
	if err != nil || e.State.Jobs != 1 {
		t.Fatalf("advance: %+v %v", e, err)
	}
	if p.deadline.IsZero() || p.deadline.After(deadline) {
		t.Fatalf("worker deadline %s exceeds workflow deadline %s", p.deadline, deadline)
	}
}

func TestWorkflowCompletionSummarizesOnlyOnceAfterAllSteps(t *testing.T) {
	p := &specialistTestProvider{responses: []llm.ChatResponse{
		workflowResponse(t, domain.CompletionSucceeded),
		workflowResponse(t, domain.CompletionSucceeded),
		{Text: rootActionResponse(domain.RootActionContinue, "Both workflow jobs completed", "", "")},
	}}
	st, run, _, _ := newWorkflowFixture(t, p)
	router := llm.NewRouter(llm.ModelRef{Provider: p.Name(), Model: "model"})
	router.RegisterProvider(p)
	handoff := application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker())
	workers := application.NewSpecialistExecutionService(st, router, policy.NewDefaultChecker()).WithCompletionSummary(handoff)
	service := application.NewWorkflowService(st, workers)
	d, err := service.Save(t.Context(), workflowSpec(workflowWorker("one"), workflowWorker("two")))
	if err != nil {
		t.Fatal(err)
	}
	e, err := service.Start(t.Context(), application.StartWorkflowRequest{RunID: run.ID, DefinitionID: d.ID, OperationKey: "workflow-one-final-summary"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		e, err = service.Advance(t.Context(), e.ID)
		if err != nil || e.State.Jobs != i || p.calls != i {
			t.Fatalf("step %d emitted early summary: %+v calls=%d %v", i, e, p.calls, err)
		}
	}
	for i := 0; i < 2; i++ {
		e, err = service.Advance(t.Context(), e.ID)
		if err != nil || e.Status != "completed" || p.calls != 3 {
			t.Fatalf("summary replay: %+v calls=%d %v", e, p.calls, err)
		}
	}
	queue, err := st.GetOperatorSteeringQueueSummary(context.Background(), run.ID)
	if err != nil || queue.Pending != 0 {
		t.Fatalf("undelivered summary: %+v %v", queue, err)
	}
}
