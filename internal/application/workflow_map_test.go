package application_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/store"
)

func dynamicMapStep(id, source, path string) domain.WorkflowStep {
	step := workflowWorker(id)
	step.Kind = "map"
	step.Items = &domain.WorkflowValueRef{Step: source, Path: path}
	step.MaxItems = 8
	step.Parallelism = 2
	step.MaxAttempts = 3
	step.Task.TurnLimit = 1
	return step
}

func dynamicResponse(t *testing.T, data string) llm.ChatResponse {
	report := domain.CompletionReport{Version: domain.CompletionReportVersion, Outcome: domain.CompletionSucceeded, Summary: "structured result", WorkItemIDs: []string{}, NoteIDs: []string{}, Data: json.RawMessage(data)}
	return llm.ChatResponse{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionFinish, Message: "finished item", Report: &report}), Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}
}

func dynamicCurrentItem(t *testing.T, service *application.WorkflowService, id, agentID string) domain.WorkflowMapItem {
	t.Helper()
	e, err := service.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range e.State.Maps {
		for _, item := range group.Items {
			if "agent-"+strings.TrimPrefix(item.StepExecutionID, "workflowstep-") == agentID {
				return item
			}
		}
	}
	t.Fatalf("Agent %s has no frozen workflow item", agentID)
	return domain.WorkflowMapItem{}
}

func dynamicAdvance(t *testing.T, service *application.WorkflowService, e domain.WorkflowExecution) domain.WorkflowExecution {
	t.Helper()
	next, err := service.Advance(t.Context(), e.ID)
	if err != nil {
		t.Fatalf("advance: %v %+v", err, next)
	}
	return next
}

func TestWorkflowMapDataSharedLeaseAndOrderedJoin(t *testing.T) {
	p := &specialistTestProvider{}
	st, run, service, _ := newWorkflowFixture(t, p)
	spec := workflowSpec(workflowWorker("discover"), dynamicMapStep("each", "discover", "/data/items"))
	e := startHostWorkflow(t, service, run.ID, "map-data-parallel", spec)
	entered := make(chan domain.WorkflowMapItem, 2)
	release := []chan struct{}{make(chan struct{}), make(chan struct{})}
	p.respond = func(index int, request llm.ChatRequest) (llm.ChatResponse, error) {
		if index == 0 {
			return dynamicResponse(t, `{"items":[{"name":"one"},{"name":"two"}]}`), nil
		}
		item := dynamicCurrentItem(t, service, e.ID, request.Metadata["agent_id"])
		entered <- item
		<-release[item.Index]
		return dynamicResponse(t, fmt.Sprintf(`{"index":%d}`, item.Index)), nil
	}
	e = dynamicAdvance(t, service, e)
	type result struct {
		e   domain.WorkflowExecution
		err error
	}
	done := make(chan result, 1)
	go func() { next, err := service.Advance(context.Background(), e.ID); done <- result{next, err} }()
	var slots [2]domain.WorkflowMapItem
	for range 2 {
		select {
		case item := <-entered:
			slots[item.Index] = item
		case <-time.After(5 * time.Second):
			t.Fatal("two workers did not enter the provider concurrently")
		}
	}
	lease, found, err := st.GetRunExecutionLease(t.Context(), run.ID)
	if err != nil || !found {
		t.Fatalf("shared lease: %v", err)
	}
	for _, item := range slots {
		agentID := "agent-" + strings.TrimPrefix(item.StepExecutionID, "workflowstep-")
		child, err := st.GetAgentNode(t.Context(), agentID)
		if err != nil {
			t.Fatal(err)
		}
		attempt, found, err := st.GetAgentAttempt(t.Context(), child.ActiveAttemptID)
		if err != nil || !found || attempt.LeaseID != lease.LeaseID {
			t.Fatalf("worker used another Run lease: %+v %v", attempt, err)
		}
	}
	close(release[1])
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, found, err = st.GetAgentCompletion(t.Context(), "agent-"+strings.TrimPrefix(slots[1].StepExecutionID, "workflowstep-"))
		if err != nil {
			t.Fatal(err)
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second input did not finish before first")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release[0])
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	e = dynamicAdvance(t, service, r.e)
	var output struct {
		Items []struct {
			Index int `json:"index"`
			Data  struct {
				Index int `json:"index"`
			} `json:"data"`
		} `json:"items"`
	}
	if err = json.Unmarshal(e.State.Outputs["each"], &output); err != nil || e.Status != "completed" || len(output.Items) != 2 || output.Items[0].Data.Index != 0 || output.Items[1].Data.Index != 1 || e.State.AllocatedJobs != 3 {
		t.Fatalf("unordered or incomplete join: %s %+v %v", e.State.Outputs["each"], e, err)
	}
}

func TestWorkflowMapRetryOnlyFailedItemsKeepsHistoryAndBudgets(t *testing.T) {
	for _, kind := range []string{"map", "worker"} {
		t.Run(kind, func(t *testing.T) {
			p := &specialistTestProvider{}
			st, run, service, _ := newWorkflowFixture(t, p)
			step := dynamicMapStep("each", "$input", "/items")
			if kind == "worker" {
				step = workflowWorker("each")
				step.Task.TurnLimit = 1
			}
			def, err := service.Save(t.Context(), workflowSpec(step))
			if err != nil {
				t.Fatal(err)
			}
			e, err := service.Start(t.Context(), application.StartWorkflowRequest{RunID: run.ID, DefinitionID: def.ID, OperationKey: "dynamic-retry-start-0001", Inputs: json.RawMessage(`{"items":[0,1]}`)})
			if err != nil {
				t.Fatal(err)
			}
			p.respond = func(_ int, request llm.ChatRequest) (llm.ChatResponse, error) {
				item := dynamicCurrentItem(t, service, e.ID, request.Metadata["agent_id"])
				if item.Index == 0 && item.Generation == 1 {
					return llm.ChatResponse{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue, Message: "needs another turn"}), Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil
				}
				return dynamicResponse(t, fmt.Sprintf(`{"index":%d,"generation":%d}`, item.Index, item.Generation)), nil
			}
			e = dynamicAdvance(t, service, e)
			if e.Status != "failed" || e.State.Blocked == nil || !e.State.Blocked.Retryable {
				t.Fatalf("failed item not independently retryable: %+v", e)
			}
			original := e
			group := e.State.Maps[e.State.Blocked.Key]
			oldFailed := group.Items[0].StepExecutionID
			sibling := ""
			if len(group.Items) > 1 {
				sibling = group.Items[1].StepExecutionID
				if group.Items[1].Status != "completed" {
					t.Fatal("business failure cancelled successful sibling")
				}
			}
			e, err = service.RetryFailed(t.Context(), e.ID, e.Version, "retry-dynamic-failed-0001")
			if err != nil {
				t.Fatal(err)
			}
			if !e.CreatedAt.Equal(original.CreatedAt) || e.TurnsBefore != original.TurnsBefore || e.UsageBefore != original.UsageBefore || e.State.AllocatedJobs != original.State.AllocatedJobs+1 {
				t.Fatal("retry reset cumulative budget or time")
			}
			replay, err := service.RetryFailed(t.Context(), e.ID, original.Version, "retry-dynamic-failed-0001")
			if err != nil || replay.Version != e.Version || replay.State.AllocatedJobs != e.State.AllocatedJobs {
				t.Fatalf("retry replay allocated another attempt: %+v %v", replay, err)
			}
			if _, err = service.RetryFailed(t.Context(), e.ID, e.Version, "retry-dynamic-failed-0001"); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeConflict {
				t.Fatalf("retry key accepted another revision: %v", err)
			}
			e = dynamicAdvance(t, service, e)
			e = dynamicAdvance(t, service, e)
			wantCalls := 3
			if kind == "worker" {
				wantCalls = 2
			}
			if e.Status != "completed" || p.calls != wantCalls {
				t.Fatalf("retry repeated a successful worker: %+v calls=%d", e, p.calls)
			}
			old, found, err := st.GetWorkflowStep(t.Context(), oldFailed)
			if err != nil || !found || old.Status != "failed" {
				t.Fatalf("failed history was erased: %+v %v", old, err)
			}
			for _, g := range e.State.Maps {
				if sibling != "" && g.Items[1].StepExecutionID != sibling {
					t.Fatal("retry replaced successful sibling")
				}
			}
		})
	}
}

type dynamicFreezeStore struct {
	*store.SQLiteStore
	freeze bool
	paused bool
}

func (s *dynamicFreezeStore) CheckpointWorkflowExecution(ctx context.Context, e domain.WorkflowExecution, owner string) (domain.WorkflowExecution, error) {
	updated, err := s.SQLiteStore.CheckpointWorkflowExecution(ctx, e, owner)
	if err != nil {
		return updated, err
	}
	if s.freeze && !s.paused && len(updated.State.Maps) > 0 && updated.State.Jobs == 0 {
		s.paused = true
		return s.SQLiteStore.ControlWorkflowExecution(ctx, updated.ID, "pause", updated.Version)
	}
	return updated, nil
}

func TestWorkflowMapFreezeRestartAndReplayKeepInputIdentity(t *testing.T) {
	p := &specialistTestProvider{}
	st, run, _, path := newWorkflowFixture(t, p)
	faults := &dynamicFreezeStore{SQLiteStore: st, freeze: true}
	router := llm.NewRouter(llm.ModelRef{Provider: p.Name(), Model: "model"})
	router.RegisterProvider(p)
	service := application.NewWorkflowService(faults, application.NewSpecialistExecutionService(faults, router, policy.NewDefaultChecker()))
	def, err := service.Save(t.Context(), workflowSpec(dynamicMapStep("each", "$input", "/items")))
	if err != nil {
		t.Fatal(err)
	}
	e, err := service.Start(t.Context(), application.StartWorkflowRequest{RunID: run.ID, DefinitionID: def.ID, OperationKey: "freeze-restart-dynamic-0001", Inputs: json.RawMessage(`{"items":[{"name":"original"},{"name":"second"}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	e = dynamicAdvance(t, service, e)
	if e.Status != "paused" || p.calls != 0 || e.State.AllocatedJobs != 2 {
		t.Fatalf("freeze did not precede admission: %+v calls=%d", e, p.calls)
	}
	var before domain.WorkflowMapState
	for _, g := range e.State.Maps {
		before = g
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	service = application.NewWorkflowService(reopened, application.NewSpecialistExecutionService(reopened, router, policy.NewDefaultChecker()))
	p.respond = func(_ int, request llm.ChatRequest) (llm.ChatResponse, error) {
		item := dynamicCurrentItem(t, service, e.ID, request.Metadata["agent_id"])
		return dynamicResponse(t, string(item.Value)), nil
	}
	e, err = service.Resume(t.Context(), e.ID, e.Version)
	if err != nil {
		t.Fatal(err)
	}
	e = dynamicAdvance(t, service, e)
	for _, g := range e.State.Maps {
		if g.Items[0].StepExecutionID != before.Items[0].StepExecutionID || string(g.Items[0].Value) != string(before.Items[0].Value) {
			t.Fatal("restart unfolded inputs again")
		}
	}
	// The native step rows are ahead of a replayed interpreter slot checkpoint.
	claimed, ok, err := reopened.ClaimWorkflowExecution(t.Context(), e.ID, "replay-host-owner")
	if err != nil || !ok {
		t.Fatalf("claim: %v", err)
	}
	claimed.State.Stack = []domain.WorkflowFrame{{}}
	claimed.State.Jobs = 0
	for key, g := range claimed.State.Maps {
		for i := range g.Items {
			g.Items[i].Status = "pending"
		}
		claimed.State.Maps[key] = g
	}
	e, err = reopened.CheckpointWorkflowExecution(t.Context(), claimed, "replay-host-owner")
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.ReleaseWorkflowExecution(t.Context(), e.ID, "replay-host-owner"); err != nil {
		t.Fatal(err)
	}
	e = dynamicAdvance(t, service, e)
	e = dynamicAdvance(t, service, e)
	if e.Status != "completed" || p.calls != 2 || e.State.AllocatedJobs != 2 || e.State.Jobs != 2 {
		t.Fatalf("replay reran completed items: %+v calls=%d", e, p.calls)
	}
}

func TestWorkflowMapRejectsInvalidExpansionAndRoundBudgets(t *testing.T) {
	for _, scenario := range []string{"non_array", "too_many", "empty", "job_budget", "token_budget", "turn_budget", "deadline"} {
		t.Run(scenario, func(t *testing.T) {
			p := &specialistTestProvider{}
			_, run, service, _ := newWorkflowFixture(t, p)
			spec := workflowSpec(dynamicMapStep("each", "$input", "/items"))
			input := `{"items":[0,1]}`
			switch scenario {
			case "non_array":
				input = `{"items":{}}`
			case "too_many":
				spec.Steps[0].MaxItems = 1
			case "empty":
				input = `{"items":[]}`
			case "job_budget":
				spec.Limits.MaxJobs = 1
			case "token_budget":
				spec.Limits.MaxTokens = 100
			case "turn_budget":
				spec.Limits.MaxTurns = 1
			case "deadline":
				spec.Limits.TimeoutMillis = 1
			}
			def, err := service.Save(t.Context(), spec)
			if err != nil {
				t.Fatal(err)
			}
			e, err := service.Start(t.Context(), application.StartWorkflowRequest{RunID: run.ID, DefinitionID: def.ID, OperationKey: "map-invalid-bound-0001", Inputs: json.RawMessage(input)})
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "deadline" {
				time.Sleep(3 * time.Millisecond)
			}
			e = dynamicAdvance(t, service, e)
			if scenario == "empty" {
				e = dynamicAdvance(t, service, e)
				if e.Status != "completed" {
					t.Fatalf("empty map: %+v", e)
				}
			} else if e.Status != "failed" {
				t.Fatalf("invalid map dispatched: %+v", e)
			}
			if p.calls != 0 {
				t.Fatalf("invalid expansion spent model usage: %d", p.calls)
			}
		})
	}
}

func TestWorkflowMapApprovalCapacityAndSiblingProgressSurviveRestart(t *testing.T) {
	for _, scenario := range []struct {
		approvals int
		stagger   bool
		count     int
		maxTurns  int64
		spent     bool
		expired   bool
		legacy    bool
	}{
		{approvals: 1, count: 3, maxTurns: 16},
		{approvals: 2, stagger: true, count: 3, maxTurns: 16},
		{approvals: 2, count: 3, maxTurns: 16},
		{approvals: 2, count: 2, maxTurns: 2},
		{approvals: 2, count: 2, maxTurns: 16, spent: true},
		{approvals: 2, count: 3, maxTurns: 2},
		{approvals: 2, count: 3, maxTurns: 16, expired: true},
		{approvals: 1, count: 1, maxTurns: 16, expired: true, legacy: true},
	} {
		approvals := scenario.approvals
		t.Run(fmt.Sprintf("%d_pending_approvals_stagger_%t_turns_%d_spent_%t_expired_%t_legacy_%t", approvals, scenario.stagger, scenario.maxTurns, scenario.spent, scenario.expired, scenario.legacy), func(t *testing.T) {
			root, home := t.TempDir(), t.TempDir()
			p := &specialistTestProvider{responses: []llm.ChatResponse{workflowResponse(t, domain.CompletionSucceeded)}}
			budget := domain.Budget{MaxTurns: 20, MaxTokens: 10000, MaxToolCalls: 20}
			if scenario.spent {
				budget.TimeoutSeconds = 1
			}
			st, run, unused, runner := newScopedSubagentFixture(t, p, budget, 2, 128, false, domain.SpecialistAdmission{MaxChildren: domain.MaxAgentChildren}, home, root)
			if _, err := runner.Step(t.Context(), run.ID, unused.ID); err != nil {
				t.Fatal(err)
			}
			router := llm.NewRouter(llm.ModelRef{Provider: p.Name(), Model: "model"})
			router.RegisterProvider(p)
			checker := executionWorkerReviewPolicy{Checker: policy.NewDefaultChecker(), review: true}
			caps := domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
			makeService := func(st *store.SQLiteStore) *application.WorkflowService {
				host := application.NewAgentRunner(st, router, checker).WithExecutionPermissionCapabilities(caps)
				workers := application.NewSpecialistExecutionService(st, router, checker).WithWorkerRuntime(host)
				if scenario.spent {
					return application.NewWorkflowService(&dynamicCleanupFenceStore{SQLiteStore: st, t: t, workers: workers}, workers)
				}
				return application.NewWorkflowService(st, workers)
			}
			service := makeService(st)
			step := dynamicMapStep("each", "$input", "/items")
			step.Task.Worker = &domain.WorkerProfile{}
			step.Task.ToolCallLimit = 4
			if scenario.legacy {
				step.Kind, step.Items, step.MaxItems, step.Parallelism = "worker", nil, 0, 0
			}
			spec := workflowSpec(step)
			spec.Limits.MaxTurns = scenario.maxTurns
			if scenario.expired {
				spec.Limits.TimeoutMillis = 10000
			}
			def, err := service.Save(t.Context(), spec)
			if err != nil {
				t.Fatal(err)
			}
			input := json.RawMessage(`{"items":[0,1,2]}`)
			if scenario.count == 2 {
				input = json.RawMessage(`{"items":[0,1]}`)
			}
			e, err := service.Start(t.Context(), application.StartWorkflowRequest{RunID: run.ID, DefinitionID: def.ID, OperationKey: "map-approval-capacity-0001", Inputs: input})
			if err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			stages := map[int]int{}
			edits := map[int]string{}
			p.respond = func(_ int, request llm.ChatRequest) (llm.ChatResponse, error) {
				item := dynamicCurrentItem(t, service, e.ID, request.Metadata["agent_id"])
				if item.Index >= approvals {
					return dynamicResponse(t, fmt.Sprintf(`{"index":%d}`, item.Index)), nil
				}
				mu.Lock()
				stage := stages[item.Index]
				stages[item.Index]++
				mu.Unlock()
				var call llm.ToolCall
				switch stage {
				case 0:
					raw, _ := json.Marshal(map[string]any{"version": "agent-code-tools.v1", "action": "create", "path": fmt.Sprintf("item-%d.txt", item.Index), "expected_sha256": "missing", "content": "approved item\n"})
					call = llm.ToolCall{ID: fmt.Sprintf("proposal-%d", item.Index), Name: "workspace_change", Arguments: raw}
				case 1:
					var receipt struct {
						Result struct {
							EditID string          `json:"edit_id"`
							Apply  json.RawMessage `json:"apply_arguments"`
						} `json:"result"`
					}
					if err := json.Unmarshal([]byte(scopedLatestResult(t, request)), &receipt); err != nil || receipt.Result.EditID == "" {
						return llm.ChatResponse{}, fmt.Errorf("missing proposal: %v", err)
					}
					mu.Lock()
					edits[item.Index] = receipt.Result.EditID
					mu.Unlock()
					call = llm.ToolCall{ID: fmt.Sprintf("apply-%d", item.Index), Name: "workspace_apply", Arguments: receipt.Result.Apply}
				case 2:
					if !strings.Contains(scopedLatestResult(t, request), `"status":"applied"`) {
						t.Logf("original intent result for item %d: %s", item.Index, scopedLatestResult(t, request))
						return llm.ChatResponse{}, fmt.Errorf("approval did not apply original intent")
					}
					return dynamicResponse(t, fmt.Sprintf(`{"index":%d}`, item.Index)), nil
				default:
					return llm.ChatResponse{}, fmt.Errorf("replayed model item %d", item.Index)
				}
				return llm.ChatResponse{ToolCalls: []llm.ToolCall{call}, Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil
			}
			e = dynamicAdvance(t, service, e)
			if scenario.count == 3 && scenario.maxTurns == 2 {
				var frozen domain.WorkflowMapState
				for _, g := range e.State.Maps {
					frozen = g
				}
				ids := []string{}
				for i := range 2 {
					node, err := st.GetAgentNode(t.Context(), "agent-"+strings.TrimPrefix(frozen.Items[i].StepExecutionID, "workflowstep-"))
					if err != nil || node.ActiveAttemptID == "" {
						t.Fatalf("missing live approval before outer budget check: %+v %v", node, err)
					}
					ids = append(ids, node.ActiveAttemptID)
				}
				e = dynamicAdvance(t, service, e)
				if e.Status != "failed" || p.calls != 5 {
					t.Fatalf("outer budget rejection did not stop cleanly: %+v calls=%d", e, p.calls)
				}
				for i, id := range ids {
					a, found, err := st.GetAgentAttempt(t.Context(), id)
					if err != nil || !found || a.Status != domain.AgentAttemptInterrupted {
						t.Fatalf("outer budget rejection left approval active: %+v %v", a, err)
					}
					assertWorkflowChildRetired(t, st, frozen.Items[i].StepExecutionID)
				}
				for _, g := range e.State.Maps {
					if g.Items[0].Status != "failed" || g.Items[1].Status != "failed" {
						t.Fatal("cleared approvals were not reflected in map state")
					}
				}
				return
			}
			e = dynamicAdvance(t, service, e)
			if e.Status != "waiting_approval" {
				t.Fatalf("approval/capacity wait did not become dormant: %+v", e)
			}
			var group domain.WorkflowMapState
			for _, g := range e.State.Maps {
				group = g
			}
			if approvals == 1 && scenario.count == 3 && group.Items[2].Status != "completed" {
				t.Fatal("one pending approval blocked a ready sibling")
			}
			if approvals == 2 && scenario.count == 3 {
				if _, err := st.GetAgentNode(t.Context(), "agent-"+strings.TrimPrefix(group.Items[2].StepExecutionID, "workflowstep-")); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeNotFound {
					t.Fatalf("third item bypassed capacity: %v", err)
				}
			}
			attempts := map[int]string{}
			for i := range approvals {
				node, err := st.GetAgentNode(t.Context(), "agent-"+strings.TrimPrefix(group.Items[i].StepExecutionID, "workflowstep-"))
				if err != nil {
					t.Fatal(err)
				}
				attempts[i] = node.ActiveAttemptID
			}
			before := p.calls
			// A completed sibling changes the dependency fingerprint. One host
			// observation may consume that wake before the approval becomes dormant.
			workflowTick(t, service)
			if workflowTick(t, service) || p.calls != before {
				t.Fatal("unresolved approvals caused polling model calls")
			}
			if err = st.Close(); err != nil {
				t.Fatal(err)
			}
			st, err = store.Open(filepath.Join(home, "cyberagent.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			service = makeService(st)
			if workflowTick(t, service) {
				t.Fatal("restart dispatched unresolved approvals")
			}
			if scenario.legacy {
				claimed, acquired, err := st.ClaimWorkflowExecution(t.Context(), e.ID, "legacy-checkpoint-host")
				if err != nil || !acquired {
					t.Fatalf("claim legacy checkpoint: %t %v", acquired, err)
				}
				for key, g := range claimed.State.Maps {
					if g.Items[0].StepExecutionID != "workflowstep-"+key {
						t.Fatal("ordinary worker no longer uses its legacy step identity")
					}
				}
				claimed.State.Maps = nil
				e, err = st.CheckpointWorkflowExecution(t.Context(), claimed, "legacy-checkpoint-host")
				if err != nil {
					t.Fatal(err)
				}
				if err = st.ReleaseWorkflowExecution(t.Context(), e.ID, "legacy-checkpoint-host"); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.expired {
				// Expiry wakes dormant waits, but cannot close another Run owner's
				// resources. The same original attempts remain live until the lease
				// is available for the fenced cleanup tick.
				held, err := st.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "deadline-competing-host", TTL: time.Minute})
				if err != nil || held.Lease.LeaseID == "" {
					t.Fatalf("hold deadline lease: %+v %v", held, err)
				}
				time.Sleep(time.Until(e.CreatedAt.Add(10*time.Second)) + 5*time.Millisecond)
				if !workflowTick(t, service) {
					t.Fatal("expired dormant workflow did not wake")
				}
				current, err := service.Get(t.Context(), e.ID)
				if err != nil || current.Status == "failed" {
					t.Fatalf("deadline hid incomplete cleanup: %+v %v", current, err)
				}
				for _, attemptID := range attempts {
					a, found, err := st.GetAgentAttempt(t.Context(), attemptID)
					if err != nil || !found || a.Status != domain.AgentAttemptRunning {
						t.Fatalf("busy deadline tick changed live approval: %+v %v", a, err)
					}
				}
				if _, _, err = st.ReleaseRunExecutionLease(t.Context(), held.Lease); err != nil {
					t.Fatal(err)
				}
				if !workflowTick(t, service) {
					t.Fatal("deadline cleanup did not resume after lease release")
				}
				current, err = service.Get(t.Context(), e.ID)
				if err != nil || current.Status != "failed" || current.Reason != "workflow deadline exceeded" || p.calls != before {
					t.Fatalf("deadline cleanup did not settle: %+v %v calls=%d", current, err, p.calls)
				}
				for i, attemptID := range attempts {
					a, found, err := st.GetAgentAttempt(t.Context(), attemptID)
					if err != nil || !found || a.Status != domain.AgentAttemptInterrupted {
						t.Fatalf("deadline left approval active: %+v %v", a, err)
					}
					assertWorkflowChildRetired(t, st, group.Items[i].StepExecutionID)
				}
				if scenario.legacy && len(current.State.Maps) != 0 {
					t.Fatal("deadline recreated legacy worker instead of retiring its original attempt")
				}
				for _, g := range current.State.Maps {
					for i := range approvals {
						if g.Items[i].Status != "failed" {
							t.Fatal("deadline cleanup left approval slots pending")
						}
					}
					if scenario.count == 3 {
						if _, err := st.GetAgentNode(t.Context(), "agent-"+strings.TrimPrefix(g.Items[2].StepExecutionID, "workflowstep-")); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeNotFound {
							t.Fatalf("deadline admitted queued third item: %v", err)
						}
					}
				}
				return
			}
			for i := range approvals {
				if scenario.spent && i > 0 {
					continue
				}
				if _, err := application.NewFileEditReviewService(st).Review(t.Context(), application.ReviewFileEditRequest{Version: application.FileEditReviewProtocolVersion, RunID: run.ID, EditID: edits[i], Action: application.FileEditApproveIntent}); err != nil {
					t.Fatal(err)
				}
				// Decide exact intents individually. The first resumed child frees
				// its capacity for item 2 while the other intent remains pending.
				if scenario.stagger && i == 0 {
					e = dynamicAdvance(t, service, e)
					e = dynamicAdvance(t, service, e)
					var current domain.WorkflowMapState
					for _, g := range e.State.Maps {
						current = g
					}
					if current.Items[2].Status != "completed" || current.Items[1].Status != "pending" || e.Status != "waiting_approval" {
						t.Fatalf("resumed approval did not free capacity for sibling: %+v", e)
					}
				}
			}
			if scenario.spent {
				spendWorkflowRunExecutionTime(t, st, run.ID)
				for i, attemptID := range attempts {
					a, found, err := st.GetAgentAttempt(t.Context(), attemptID)
					if err != nil || !found || a.Status != domain.AgentAttemptRunning {
						s := a
						t.Fatalf("accounting fixture ended approval attempt: %+v %v", s, err)
					}
					node, err := st.GetAgentNode(t.Context(), "agent-"+strings.TrimPrefix(group.Items[i].StepExecutionID, "workflowstep-"))
					if err != nil || node.ActiveAttemptID != attemptID {
						t.Fatalf("accounting fixture changed pending child: %+v %v", node, err)
					}
				}
				e = dynamicAdvance(t, service, e)
				if e.Status != "failed" || p.calls != 1+2*approvals || e.State.Blocked != nil && e.State.Blocked.Retryable {
					t.Fatalf("spent Run attempted approval/model recovery: %+v calls=%d", e, p.calls)
				}
				for i, attemptID := range attempts {
					attempt, found, err := st.GetAgentAttempt(t.Context(), attemptID)
					if err != nil || !found || attempt.Status != domain.AgentAttemptInterrupted {
						t.Fatalf("approval attempt left active: %+v %v", attempt, err)
					}
					assertWorkflowChildRetired(t, st, group.Items[i].StepExecutionID)
					if _, err = os.Stat(filepath.Join(root, fmt.Sprintf("item-%d.txt", i))); !os.IsNotExist(err) {
						t.Fatal("spent approval applied a file")
					}
				}
				return
			}
			for range 4 {
				e = dynamicAdvance(t, service, e)
				if e.Status == "completed" {
					break
				}
			}
			if e.Status != "completed" || p.calls != 1+scenario.count+2*approvals {
				t.Fatalf("approval recovery repeated a model turn: %+v calls=%d", e, p.calls)
			}
			for i, attemptID := range attempts {
				completion, found, err := st.GetAgentCompletion(t.Context(), "agent-"+strings.TrimPrefix(group.Items[i].StepExecutionID, "workflowstep-"))
				if err != nil || !found || completion.AttemptID != attemptID {
					t.Fatalf("approval changed attempt: %+v %v", completion, err)
				}
				if raw, err := os.ReadFile(filepath.Join(root, fmt.Sprintf("item-%d.txt", i))); err != nil || string(raw) != "approved item\n" {
					t.Fatalf("approved result: %q %v", raw, err)
				}
			}
		})
	}
}

type dynamicCleanupFenceStore struct {
	*store.SQLiteStore
	t       *testing.T
	workers *application.SpecialistExecutionService
}

func (s *dynamicCleanupFenceStore) GetAgentAttempt(ctx context.Context, id string) (domain.AgentAttempt, bool, error) {
	a, found, err := s.SQLiteStore.GetAgentAttempt(ctx, id)
	if err != nil || !found {
		return a, found, err
	}
	// The cleanup host still holds its shared Run lease before closing any
	// resources. The real manual execution entry cannot resume this old wait.
	if _, err = s.workers.ExecuteSpecialist(ctx, application.ExecuteSpecialistRequest{RunID: a.RunID, AgentID: a.AgentID, OperationKey: "competing-cleanup-" + a.ID, WorkspaceTools: true}); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeConflict {
		s.t.Fatalf("manual runner entered approval cleanup: %v", err)
	}
	return a, true, nil
}
func (s *dynamicCleanupFenceStore) FailWorkflowStepWithLease(ctx context.Context, job domain.WorkflowStepExecution, owner string, revision int64, lease domain.RunExecutionLease, expected domain.AgentAttempt) error {
	if expected.ID == "" || expected.LeaseID == "" || expected.LeaseGeneration < 1 {
		s.t.Fatalf("cleanup fixture has no real suspended owner: %+v", expected)
	}
	wrongLease := lease
	wrongLease.Generation++
	if err := s.SQLiteStore.FailWorkflowStepWithLease(ctx, job, owner, revision, wrongLease, expected); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeConflict {
		s.t.Fatalf("cleanup accepted stale Run lease: %v", err)
	}
	wrongAttempt := expected
	wrongAttempt.LeaseGeneration++
	if err := s.SQLiteStore.FailWorkflowStepWithLease(ctx, job, owner, revision, lease, wrongAttempt); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeConflict {
		s.t.Fatalf("cleanup accepted another attempt owner: %v", err)
	}
	if err := s.SQLiteStore.FailWorkflowStepWithLease(ctx, job, owner, revision+1, lease, expected); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeConflict {
		s.t.Fatalf("cleanup accepted another workflow revision: %v", err)
	}
	stored, found, err := s.SQLiteStore.GetWorkflowStep(ctx, job.ID)
	if err != nil || !found || stored.Status != "pending" {
		s.t.Fatalf("rejected cleanup changed durable step: %+v %v", stored, err)
	}
	return s.SQLiteStore.FailWorkflowStepWithLease(ctx, job, owner, revision, lease, expected)
}

type dynamicControlStore struct {
	*store.SQLiteStore
	owner string
}

func (s *dynamicControlStore) ValidateWorkflowDispatch(ctx context.Context, id, owner string, revision int64, steps []string) error {
	s.owner = owner
	return s.SQLiteStore.ValidateWorkflowDispatch(ctx, id, owner, revision, steps)
}

func TestWorkflowMapPauseAndLostClaimRetainNativeCompletions(t *testing.T) {
	for _, control := range []string{"pause", "claim"} {
		t.Run(control, func(t *testing.T) {
			p := &specialistTestProvider{}
			st, run, _, _ := newWorkflowFixture(t, p)
			faults := &dynamicControlStore{SQLiteStore: st}
			router := llm.NewRouter(llm.ModelRef{Provider: p.Name(), Model: "model"})
			router.RegisterProvider(p)
			service := application.NewWorkflowService(faults, application.NewSpecialistExecutionService(faults, router, policy.NewDefaultChecker()))
			def, err := service.Save(t.Context(), workflowSpec(dynamicMapStep("each", "$input", "/items")))
			if err != nil {
				t.Fatal(err)
			}
			e, err := service.Start(t.Context(), application.StartWorkflowRequest{RunID: run.ID, DefinitionID: def.ID, OperationKey: "map-control-completion-0001", Inputs: json.RawMessage(`{"items":[0,1]}`)})
			if err != nil {
				t.Fatal(err)
			}
			var once sync.Once
			var controlErr error
			p.respond = func(_ int, _ llm.ChatRequest) (llm.ChatResponse, error) {
				once.Do(func() {
					current, err := service.Get(context.Background(), e.ID)
					if err != nil {
						controlErr = err
						return
					}
					if control == "pause" {
						_, controlErr = service.Pause(context.Background(), e.ID, current.Version)
					} else {
						controlErr = st.ReleaseWorkflowExecution(context.Background(), e.ID, faults.owner)
						if controlErr == nil {
							_, _, controlErr = st.ClaimWorkflowExecution(context.Background(), e.ID, "replacement-owner")
						}
					}
				})
				return dynamicResponse(t, `{"saved":true}`), controlErr
			}
			next, err := service.Advance(t.Context(), e.ID)
			if controlErr != nil {
				t.Fatal(controlErr)
			}
			if control == "pause" {
				if err != nil || next.Status != "paused" {
					t.Fatalf("pause lost: %+v %v", next, err)
				}
				e, err = service.Resume(t.Context(), e.ID, next.Version)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeConflict {
					t.Fatalf("old claim wrote state: %v", err)
				}
				if err = st.ReleaseWorkflowExecution(t.Context(), e.ID, "replacement-owner"); err != nil {
					t.Fatal(err)
				}
			}
			e = dynamicAdvance(t, service, e)
			e = dynamicAdvance(t, service, e)
			if e.Status != "completed" || p.calls != 2 {
				t.Fatalf("control replayed committed native children: %+v calls=%d", e, p.calls)
			}
		})
	}
}

func TestWorkflowMapOutputLimitPreservesAllNativeResults(t *testing.T) {
	p := &specialistTestProvider{respond: func(_ int, _ llm.ChatRequest) (llm.ChatResponse, error) {
		report := domain.CompletionReport{Version: domain.CompletionReportVersion, Outcome: domain.CompletionSucceeded, Summary: strings.Repeat("s", 4096), Data: json.RawMessage(`{"value":"` + strings.Repeat("d", 2000) + `"}`), WorkItemIDs: []string{}, NoteIDs: []string{}}
		return llm.ChatResponse{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionFinish, Message: "full result", Report: &report}), Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil
	}}
	st, run, service, _ := newWorkflowFixture(t, p)
	def, err := service.Save(t.Context(), workflowSpec(dynamicMapStep("each", "$input", "/items")))
	if err != nil {
		t.Fatal(err)
	}
	e, err := service.Start(t.Context(), application.StartWorkflowRequest{RunID: run.ID, DefinitionID: def.ID, OperationKey: "map-output-limit-0001", Inputs: json.RawMessage(`{"items":[0,1,2,3,4,5]}`)})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		e = dynamicAdvance(t, service, e)
	}
	if e.Status != "failed" || !strings.Contains(e.Reason, "output exceeds 32 KiB") || p.calls != 6 {
		t.Fatalf("oversized join was silently reduced: %+v calls=%d", e, p.calls)
	}
	for _, group := range e.State.Maps {
		for _, item := range group.Items {
			job, found, err := st.GetWorkflowStep(t.Context(), item.StepExecutionID)
			if err != nil || !found || job.Status != "completed" || !strings.Contains(string(job.Output), strings.Repeat("s", 4096)) {
				t.Fatalf("full native result lost: %+v %v", job, err)
			}
		}
	}
}

func TestWorkflowRetryFencesRevisionLeaseAndAttemptBound(t *testing.T) {
	p := &specialistTestProvider{respond: func(_ int, _ llm.ChatRequest) (llm.ChatResponse, error) {
		return llm.ChatResponse{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue, Message: "still failing"}), Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil
	}}
	st, run, service, _ := newWorkflowFixture(t, p)
	step := workflowWorker("one")
	step.MaxAttempts = 2
	step.Task.TurnLimit = 1
	e := startHostWorkflow(t, service, run.ID, "retry-fences", workflowSpec(step))
	e = dynamicAdvance(t, service, e)
	if e.Status != "failed" || e.State.Blocked == nil || !e.State.Blocked.Retryable {
		t.Fatalf("missing retry candidate: %+v", e)
	}
	if _, err := service.RetryFailed(t.Context(), e.ID, e.Version-1, "retry-stale-revision-0001"); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeConflict {
		t.Fatalf("accepted stale revision: %v", err)
	}
	held, err := st.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "another-host", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RetryFailed(t.Context(), e.ID, e.Version, "retry-active-lease-0001"); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeConflict {
		t.Fatalf("retry allocated during active lease: %v", err)
	}
	if _, _, err = st.ReleaseRunExecutionLease(t.Context(), held.Lease); err != nil {
		t.Fatal(err)
	}
	e, err = service.RetryFailed(t.Context(), e.ID, e.Version, "retry-attempt-two-0001")
	if err != nil {
		t.Fatal(err)
	}
	e = dynamicAdvance(t, service, e)
	if e.Status != "failed" || e.State.Blocked == nil || e.State.Blocked.Retryable || e.State.AllocatedJobs != 2 || p.calls != 2 {
		t.Fatalf("max_attempts did not bound generations: %+v calls=%d", e, p.calls)
	}
	if _, err = service.RetryFailed(t.Context(), e.ID, e.Version, "retry-forbidden-third-0001"); err == nil {
		t.Fatal("retry crossed max_attempts")
	}
}

func spendWorkflowRunExecutionTime(t *testing.T, st *store.SQLiteStore, runID string) {
	t.Helper()
	held, err := st.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: runID, OwnerID: "budget-accounting-host", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := st.BeginSupervisorTurn(t.Context(), held.Lease, "Account for execution time while leaving child approvals pending")
	if err != nil {
		t.Fatal(err)
	}
	selection, found, err := st.GetSkillSelectionByRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		registry, err := skills.BuiltinRegistry()
		if err != nil {
			t.Fatal(err)
		}
		assembly, err := registry.AssembleContextFor(selection, skills.ExecutionContext{Surface: turn.Mode.Surface, Phase: turn.Mode.Phase, Profile: turn.Mode.Profile, Role: domain.AgentRoleRoot})
		if err != nil {
			t.Fatal(err)
		}
		request, err := assembly.PreparationForMode(turn.Mode, turn.Agent.ID, turn.Checkpoint.AttemptID, turn.Checkpoint.NextTurn)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = st.PrepareRootSkillContext(t.Context(), turn.Checkpoint, request); err != nil {
			t.Fatal(err)
		}
	}
	attempt := llm.ModelAttempt{Number: 1, TransportAttempt: 1, MaxAttempts: 1, Provider: "budget-accounting-fixture", Model: "model"}
	if _, err = st.RecordSupervisorModelStarted(t.Context(), turn.Checkpoint, attempt); err != nil {
		t.Fatal(err)
	}
	action := domain.RootAction{Version: domain.RootLifecycleVersion, Kind: domain.RootActionContinue, Message: "Accounted execution time; preserve child approval ownership"}
	raw, _ := json.Marshal(action)
	response := llm.ChatResponse{Provider: attempt.Provider, Model: attempt.Model, Text: string(raw)}
	attempt.Outcome = llm.OutcomeSuccess
	checkpoint, err := st.RecordSupervisorModelCompleted(t.Context(), turn.Checkpoint, attempt, response)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = st.CompleteSupervisorTurn(t.Context(), checkpoint, response, action, policy.Decision{Allowed: true}, time.Second); err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.ReleaseRunExecutionLease(t.Context(), held.Lease); err != nil {
		t.Fatal(err)
	}
}

func assertWorkflowChildRetired(t *testing.T, st *store.SQLiteStore, stepID string) {
	t.Helper()
	job, found, err := st.GetWorkflowStep(t.Context(), stepID)
	if err != nil || !found || job.Status != "failed" {
		t.Fatalf("missing durable item failure: %+v %v", job, err)
	}
	child, err := st.GetAgentNode(t.Context(), job.AgentID)
	if err != nil || !child.Terminal() || child.ActiveAttemptID != "" {
		t.Fatalf("failed child kept its reservation: %+v %v", child, err)
	}
	linked, err := st.GetSession(t.Context(), child.SessionID)
	if err != nil || linked.Status != "archived" {
		t.Fatalf("failed child session remains active: %+v %v", linked, err)
	}
}

func TestWorkflowGlobalExecutionBudgetRetiresAdmittedChildren(t *testing.T) {
	for _, kind := range []string{"worker", "map"} {
		t.Run(kind, func(t *testing.T) {
			p := &specialistTestProvider{}
			st, run, service, _ := newWorkflowFixture(t, p, domain.Budget{MaxTurns: 20, MaxTokens: 10000, TimeoutSeconds: 1})
			spendWorkflowRunExecutionTime(t, st, run.ID)
			step := workflowWorker("each")
			if kind == "map" {
				step = dynamicMapStep("each", "$input", "/items")
			}
			def, err := service.Save(t.Context(), workflowSpec(step))
			if err != nil {
				t.Fatal(err)
			}
			e, err := service.Start(t.Context(), application.StartWorkflowRequest{RunID: run.ID, DefinitionID: def.ID, OperationKey: "global-execution-bound-0001", Inputs: json.RawMessage(`{"items":[0,1]}`)})
			if err != nil {
				t.Fatal(err)
			}
			e = dynamicAdvance(t, service, e)
			if e.Status != "failed" || !strings.Contains(e.Reason, "execution_budget") || p.calls != 0 || e.State.Blocked != nil && e.State.Blocked.Retryable {
				t.Fatalf("spent Run did not stop dispatch: %+v calls=%d", e, p.calls)
			}
			for _, group := range e.State.Maps {
				for _, item := range group.Items {
					if item.Status != "failed" {
						t.Fatalf("global stop left pending slot: %+v", item)
					}
					assertWorkflowChildRetired(t, st, item.StepExecutionID)
				}
			}
			allocated := e.State.AllocatedJobs
			if _, err = service.RetryFailed(t.Context(), e.ID, e.Version, "retry-spent-execution-0001"); err == nil {
				t.Fatal("permanent global stop was retryable")
			}
			after, err := service.Get(t.Context(), e.ID)
			if err != nil || after.State.AllocatedJobs != allocated || after.Version != e.Version {
				t.Fatalf("rejected retry allocated jobs: %+v %v", after, err)
			}
		})
	}
}

func TestWorkflowRetryRechecksSpentRunExecutionBudget(t *testing.T) {
	p := &specialistTestProvider{respond: func(_ int, _ llm.ChatRequest) (llm.ChatResponse, error) {
		return llm.ChatResponse{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue, Message: "unfinished"}), Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil
	}}
	st, run, service, _ := newWorkflowFixture(t, p, domain.Budget{MaxTurns: 20, MaxTokens: 10000, TimeoutSeconds: 1})
	step := workflowWorker("one")
	step.Task.TurnLimit = 1
	e := startHostWorkflow(t, service, run.ID, "retry-execution-bound", workflowSpec(step))
	e = dynamicAdvance(t, service, e)
	if e.Status != "failed" || e.State.Blocked == nil || !e.State.Blocked.Retryable {
		t.Fatalf("expected normal retryable failed item: %+v", e)
	}
	spendWorkflowRunExecutionTime(t, st, run.ID)
	if _, err := service.RetryFailed(t.Context(), e.ID, e.Version, "retry-newly-spent-time-0001"); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeResourceExhausted {
		t.Fatalf("retry ignored current Run execution usage: %v", err)
	}
	after, err := service.Get(t.Context(), e.ID)
	if err != nil || after.Version != e.Version || after.State.AllocatedJobs != e.State.AllocatedJobs || len(after.State.RetryOperations) != 0 {
		t.Fatalf("rejected retry allocated another generation: %+v %v", after, err)
	}
	for _, group := range after.State.Maps {
		if group.Items[0].Generation != 1 {
			t.Fatal("rejected retry advanced generation")
		}
	}
}
