package application_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
)

func workflowWorker(id string) domain.WorkflowStep {
	return domain.WorkflowStep{ID: id, Kind: "worker", Task: &domain.ChildTask{Title: id, Goal: "Report the result for " + id, Skills: []string{"model.chat"}, TurnLimit: 2, TokenLimit: 64, TimeoutMillis: 60000}}
}
func workflowOutcome(id, outcome string) *domain.WorkflowCondition {
	return &domain.WorkflowCondition{Ref: domain.WorkflowValueRef{Step: id, Path: "/outcome"}, Equals: json.RawMessage(`"` + outcome + `"`)}
}
func workflowSpec(steps ...domain.WorkflowStep) domain.WorkflowSpec {
	return domain.WorkflowSpec{Version: domain.WorkflowVersion, Name: "saved test workflow", Limits: domain.WorkflowLimits{MaxSteps: 32, MaxJobs: 8, MaxTurns: 16, MaxTokens: 1024, TimeoutMillis: 600000}, Steps: steps}
}
func workflowResponse(t *testing.T, outcome domain.CompletionOutcome) llm.ChatResponse {
	return llm.ChatResponse{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionFinish, Message: "worker completed", Report: &domain.CompletionReport{Version: domain.CompletionReportVersion, Outcome: outcome, Summary: "committed proof " + string(outcome), WorkItemIDs: []string{}, NoteIDs: []string{}}}), Usage: llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}
}
func newWorkflowFixture(t *testing.T, p *specialistTestProvider) (*store.SQLiteStore, domain.Run, *application.WorkflowService, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workflow.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	runs := application.NewRunService(st)
	_, run, err := runs.Create(t.Context(), application.CreateRunRequest{Goal: "run saved workflow", Profile: "code", ModelRoute: p.Name() + "/model", Budget: domain.Budget{MaxTurns: 64, MaxTokens: 100000}})
	if err != nil {
		t.Fatal(err)
	}
	run, err = runs.Start(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return st, run, newWorkflowService(st, p), path
}
func newWorkflowService(st *store.SQLiteStore, p *specialistTestProvider) *application.WorkflowService {
	router := llm.NewRouter(llm.ModelRef{Provider: p.Name(), Model: "model"})
	router.RegisterProvider(p)
	return application.NewWorkflowService(st, application.NewSpecialistExecutionService(st, router, policy.NewDefaultChecker()))
}

func TestWorkflowRunsSavedBranchAndRepeatAcrossRestartWithRealChildren(t *testing.T) {
	p := &specialistTestProvider{responses: []llm.ChatResponse{workflowResponse(t, domain.CompletionPartial), workflowResponse(t, domain.CompletionPartial), workflowResponse(t, domain.CompletionSucceeded), workflowResponse(t, domain.CompletionSucceeded)}}
	st, run, service, path := newWorkflowFixture(t, p)
	final := workflowWorker("final")
	final.Inputs = map[string]domain.WorkflowValueRef{"proof": {Step: "repair", Path: "/summary"}}
	spec := workflowSpec(workflowWorker("probe"), domain.WorkflowStep{ID: "choose", Kind: "if", Condition: workflowOutcome("probe", "partial"), Then: []domain.WorkflowStep{{ID: "retry", Kind: "repeat", Condition: workflowOutcome("repair", "succeeded"), MaxIterations: 3, Body: []domain.WorkflowStep{workflowWorker("repair")}}}, Else: []domain.WorkflowStep{workflowWorker("skipped")}}, final)
	definition, err := service.Save(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	again, err := service.Save(t.Context(), spec)
	if err != nil || again.ID != definition.ID {
		t.Fatalf("save replay: %#v %v", again, err)
	}
	e, err := service.Start(t.Context(), application.StartWorkflowRequest{RunID: run.ID, DefinitionID: definition.ID, OperationKey: "workflow-start-first", Inputs: json.RawMessage(`{"topic":"proof"}`)})
	if err != nil {
		t.Fatal(err)
	}
	e, err = service.Advance(t.Context(), e.ID)
	if err != nil || e.State.Jobs != 1 {
		t.Fatalf("first worker: %#v %v", e, err)
	}
	e, err = service.Pause(t.Context(), e.ID, e.Version)
	if err != nil {
		t.Fatal(err)
	}
	before := p.calls
	paused, err := service.Advance(t.Context(), e.ID)
	if err != nil || paused.Status != "paused" || p.calls != before {
		t.Fatalf("pause dispatched worker: %#v %v", paused, err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	service = newWorkflowService(reopened, p)
	e, err = service.Resume(t.Context(), e.ID, e.Version)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12 && e.Status != "completed" && e.Status != "failed"; i++ {
		e, err = service.Advance(t.Context(), e.ID)
		if err != nil {
			t.Fatalf("advance: %v (%#v)", err, e)
		}
	}
	if e.Status != "completed" || e.State.Jobs != 4 || p.calls != 4 {
		t.Fatalf("workflow did not complete four distinct jobs: %#v calls=%d", e, p.calls)
	}
	if _, exists := e.State.Outputs["skipped"]; exists {
		t.Fatal("wrong branch ran")
	}
	nodes, err := reopened.ListAgentNodes(t.Context(), run.ID)
	if err != nil || len(nodes) != 5 {
		t.Fatalf("historical children: %d %v", len(nodes), err)
	}
	for _, node := range nodes {
		if node.Role == domain.AgentRoleSpecialist && !node.Terminal() {
			t.Fatalf("worker not completed: %#v", node)
		}
	}
	snapshot, err := reopened.SnapshotAgentGraph(t.Context(), run.ID)
	if err != nil || snapshot.NodeCount != 5 {
		t.Fatalf("history snapshot: %#v %v", snapshot, err)
	}
	request, _ := json.Marshal(p.requests[len(p.requests)-1])
	if !strings.Contains(string(request), "committed proof succeeded") {
		t.Fatal("downstream worker did not receive committed intermediate output")
	}
	continued, err := service.Advance(t.Context(), e.ID)
	if err != nil || continued.Status != "completed" || p.calls != 4 {
		t.Fatal("completed replay ran a worker")
	}
	rerun, err := service.Rerun(t.Context(), e.ID, "", "workflow-rerun-second")
	if err != nil || rerun.ID == e.ID || rerun.DefinitionID != definition.ID || string(rerun.Inputs) != string(e.Inputs) || len(rerun.State.Outputs) != 0 {
		t.Fatalf("rerun did not start fresh: %#v %v", rerun, err)
	}
}

func TestWorkflowRepeatBoundAndExactBudgetCompletion(t *testing.T) {
	for _, bounded := range []bool{false, true} {
		t.Run(map[bool]string{true: "repeat-bound", false: "exact-token-budget"}[bounded], func(t *testing.T) {
			p := &specialistTestProvider{responses: []llm.ChatResponse{workflowResponse(t, domain.CompletionPartial), workflowResponse(t, domain.CompletionPartial)}}
			_, run, service, _ := newWorkflowFixture(t, p)
			spec := workflowSpec(workflowWorker("one"))
			if bounded {
				spec.Steps = []domain.WorkflowStep{{ID: "loop", Kind: "repeat", Condition: workflowOutcome("one", "succeeded"), MaxIterations: 2, Body: []domain.WorkflowStep{workflowWorker("one")}}}
			} else {
				spec.Limits.MaxTokens = 5
				spec.Steps[0].Task.TokenLimit = 5
			}
			d, err := service.Save(t.Context(), spec)
			if err != nil {
				t.Fatal(err)
			}
			e, err := service.Start(t.Context(), application.StartWorkflowRequest{RunID: run.ID, DefinitionID: d.ID, OperationKey: "workflow-test-bounded"})
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 10 && e.Status != "completed" && e.Status != "failed"; i++ {
				e, err = service.Advance(t.Context(), e.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			if bounded {
				if e.Status != "failed" || !strings.Contains(e.Reason, "iteration bound") || p.calls != 2 {
					t.Fatalf("repeat escaped bound: %#v calls=%d", e, p.calls)
				}
			} else if e.Status != "completed" || p.calls != 1 {
				t.Fatalf("exact budget rejected completed job: %#v calls=%d", e, p.calls)
			}
		})
	}
}

func TestWorkflowPauseDuringWorkerKeepsCommittedResultForResume(t *testing.T) {
	p := &specialistTestProvider{}
	_, run, service, _ := newWorkflowFixture(t, p)
	d, err := service.Save(t.Context(), workflowSpec(workflowWorker("one")))
	if err != nil {
		t.Fatal(err)
	}
	e, err := service.Start(t.Context(), application.StartWorkflowRequest{RunID: run.ID, DefinitionID: d.ID, OperationKey: "workflow-pause-inflight"})
	if err != nil {
		t.Fatal(err)
	}
	p.respond = func(_ int, _ llm.ChatRequest) (llm.ChatResponse, error) {
		current, err := service.Get(context.Background(), e.ID)
		if err != nil {
			return llm.ChatResponse{}, err
		}
		_, err = service.Pause(context.Background(), e.ID, current.Version)
		return workflowResponse(t, domain.CompletionSucceeded), err
	}
	e, err = service.Advance(t.Context(), e.ID)
	if err != nil || e.Status != "paused" {
		t.Fatalf("pause lost: %#v %v", e, err)
	}
	e, err = service.Resume(t.Context(), e.ID, e.Version)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3 && e.Status != "completed"; i++ {
		e, err = service.Advance(t.Context(), e.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if e.Status != "completed" || p.calls != 1 {
		t.Fatalf("resume repeated committed effect: %#v calls=%d", e, p.calls)
	}
}
