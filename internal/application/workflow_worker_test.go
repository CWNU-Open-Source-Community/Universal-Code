package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/store"
)

func startHostWorkflow(t *testing.T, service *application.WorkflowService, runID, key string, spec domain.WorkflowSpec) domain.WorkflowExecution {
	t.Helper()
	definition, err := service.Save(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := service.Start(t.Context(), application.StartWorkflowRequest{RunID: runID, DefinitionID: definition.ID, OperationKey: "workflow-" + key})
	if err != nil {
		t.Fatal(err)
	}
	return execution
}

func workflowTick(t *testing.T, service *application.WorkflowService) bool {
	t.Helper()
	ran, err := service.RunDue(t.Context(), "workflow-test-host", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return ran
}

func TestWorkflowHostContinuesPausedCheckpointAfterDatabaseReopen(t *testing.T) {
	continued := llm.ChatResponse{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue, Message: "continue the saved work"}), Usage: llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}
	p := &specialistTestProvider{responses: []llm.ChatResponse{continued, workflowResponse(t, domain.CompletionSucceeded), workflowResponse(t, domain.CompletionSucceeded)}}
	st, run, service, path := newWorkflowFixture(t, p)
	e := startHostWorkflow(t, service, run.ID, "host-reopen", workflowSpec(workflowWorker("one"), workflowWorker("two")))
	workflowTick(t, service)
	e, _ = service.Get(t.Context(), e.ID)
	if e.State.Jobs != 0 || p.calls != 1 {
		t.Fatalf("first turn: %+v calls=%d", e, p.calls)
	}
	e, err := service.Pause(t.Context(), e.ID, e.Version)
	if err != nil {
		t.Fatal(err)
	}
	if workflowTick(t, service) || p.calls != 1 {
		t.Fatal("paused workflow advanced")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service = newWorkflowService(st, p)
	if workflowTick(t, service) {
		t.Fatal("restart resumed an operator pause")
	}
	if _, err = service.Resume(t.Context(), e.ID, e.Version); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		workflowTick(t, service)
	}
	e, _ = service.Get(t.Context(), e.ID)
	usage, err := st.GetRunAgentUsage(t.Context(), run.ID)
	if err != nil || e.Status != "completed" || e.State.Jobs != 2 || p.calls != 3 || usage.TotalTokens != 15 {
		t.Fatalf("reopened workflow duplicated or lost work: %+v usage=%+v calls=%d err=%v", e, usage, p.calls, err)
	}
}

func TestWorkflowHostWaitsWithoutChargingAndWakesWhenRunLeaseIsReleased(t *testing.T) {
	p := &specialistTestProvider{responses: []llm.ChatResponse{workflowResponse(t, domain.CompletionSucceeded)}}
	st, run, service, _ := newWorkflowFixture(t, p)
	e := startHostWorkflow(t, service, run.ID, "host-lease-wait", workflowSpec(workflowWorker("one")))
	held, err := st.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "other-host", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Advance(t.Context(), e.ID); err != nil {
		t.Fatal(err)
	}
	e, _ = service.Get(t.Context(), e.ID)
	before, err := st.GetRunAgentUsage(t.Context(), run.ID)
	if err != nil || e.Status != "waiting" {
		t.Fatalf("missing lease wait: %+v %v", e, err)
	}
	for i := 0; i < 3; i++ {
		if workflowTick(t, service) {
			t.Fatal("unchanged wait was dispatched again")
		}
	}
	after, _ := service.Get(t.Context(), e.ID)
	usage, _ := st.GetRunAgentUsage(t.Context(), run.ID)
	if after.Version != e.Version || usage != before || p.calls != 0 {
		t.Fatalf("waiting changed budget or checkpoint: %+v %+v calls=%d", before, usage, p.calls)
	}
	if _, _, err := st.ReleaseRunExecutionLease(t.Context(), held.Lease); err != nil {
		t.Fatal(err)
	}
	workflowTick(t, service)
	workflowTick(t, service)
	after, _ = service.Get(t.Context(), e.ID)
	if after.Status != "completed" || p.calls != 1 {
		t.Fatalf("released lease did not wake: %+v calls=%d", after, p.calls)
	}
}

func TestWorkflowHostClaimsExcludeSecondDatabaseOwner(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	p := &specialistTestProvider{respond: func(_ int, _ llm.ChatRequest) (llm.ChatResponse, error) {
		close(entered)
		<-release
		return workflowResponse(t, domain.CompletionSucceeded), nil
	}}
	_, run, first, path := newWorkflowFixture(t, p)
	e := startHostWorkflow(t, first, run.ID, "host-concurrent", workflowSpec(workflowWorker("one")))
	secondStore, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	second := newWorkflowService(secondStore, p)
	done := make(chan error, 1)
	go func() {
		_, err := first.RunDue(t.Context(), "host-one", time.Now().UTC())
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first host did not dispatch")
	}
	ran, secondErr := second.RunDue(t.Context(), "host-two", time.Now().UTC())
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if secondErr != nil || ran || p.calls != 1 {
		t.Fatalf("second host dispatched: ran=%t calls=%d err=%v", ran, p.calls, secondErr)
	}
	workflowTick(t, second)
	e, _ = second.Get(t.Context(), e.ID)
	if e.Status != "completed" || p.calls != 1 {
		t.Fatalf("handoff duplicated effect: %+v calls=%d", e, p.calls)
	}
}

func TestWorkflowHostResumesReviewedFileIntentAfterDatabaseReopen(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	var editID string
	p := &specialistTestProvider{respond: func(index int, request llm.ChatRequest) (llm.ChatResponse, error) {
		call := func(name, id string, input json.RawMessage) llm.ChatResponse {
			return llm.ChatResponse{ToolCalls: []llm.ToolCall{{Name: name, ID: id, Arguments: input}}, Usage: llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}
		}
		switch index {
		case 0:
			return call("workspace_change", "saved-proposal", json.RawMessage(`{"version":"agent-code-tools.v1","action":"create","path":"output.txt","expected_sha256":"missing","content":"approved workflow output\n"}`)), nil
		case 1:
			var receipt struct {
				Result struct {
					EditID string          `json:"edit_id"`
					Apply  json.RawMessage `json:"apply_arguments"`
				} `json:"result"`
			}
			if err := json.Unmarshal([]byte(scopedLatestResult(t, request)), &receipt); err != nil || receipt.Result.EditID == "" {
				return llm.ChatResponse{}, fmt.Errorf("missing file proposal: %v", err)
			}
			editID = receipt.Result.EditID
			return call("workspace_apply", "saved-apply", receipt.Result.Apply), nil
		case 2:
			if !strings.Contains(scopedLatestResult(t, request), `"status":"applied"`) {
				return llm.ChatResponse{}, errors.New("approved file was not applied")
			}
			return workflowResponse(t, domain.CompletionSucceeded), nil
		default:
			return llm.ChatResponse{}, errors.New("unexpected extra model call")
		}
	}}
	st, run, _, _ := newScopedSubagentFixture(t, p, domain.Budget{MaxTurns: 20, MaxTokens: 10000, MaxToolCalls: 20}, 2, 128, false, domain.SpecialistAdmission{MaxChildren: domain.MaxAgentChildren}, home, root)
	router := llm.NewRouter(llm.ModelRef{Provider: p.Name(), Model: "model"})
	router.RegisterProvider(p)
	checker := executionWorkerReviewPolicy{Checker: policy.NewDefaultChecker(), review: true}
	capabilities := domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
	makeService := func(st *store.SQLiteStore) *application.WorkflowService {
		runtime := application.NewAgentRunner(st, router, checker).WithExecutionPermissionCapabilities(capabilities)
		return application.NewWorkflowService(st, application.NewSpecialistExecutionService(st, router, checker).WithWorkerRuntime(runtime))
	}
	service := makeService(st)
	step := workflowWorker("write")
	step.Task.Worker, step.Task.ToolCallLimit = &domain.WorkerProfile{}, 4
	e := startHostWorkflow(t, service, run.ID, "host-file-approval", workflowSpec(step))
	workflowTick(t, service)
	workflowTick(t, service)
	e, _ = service.Get(t.Context(), e.ID)
	before, _ := st.GetRunAgentUsage(t.Context(), run.ID)
	if e.Status != "waiting_approval" || p.calls != 2 {
		t.Fatalf("missing exact approval wait: %+v calls=%d", e, p.calls)
	}
	if workflowTick(t, service) {
		t.Fatal("unresolved approval was repeatedly dispatched")
	}
	if _, err := os.Stat(filepath.Join(root, "output.txt")); !os.IsNotExist(err) {
		t.Fatal("unapproved file was written")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(home, "cyberagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service = makeService(st)
	if workflowTick(t, service) {
		t.Fatal("restart bypassed pending approval")
	}
	if _, err := application.NewFileEditReviewService(st).Review(t.Context(), application.ReviewFileEditRequest{Version: application.FileEditReviewProtocolVersion, RunID: run.ID, EditID: editID, Action: application.FileEditApproveIntent}); err != nil {
		t.Fatal(err)
	}
	workflowTick(t, service)
	workflowTick(t, service)
	e, _ = service.Get(t.Context(), e.ID)
	usage, _ := st.GetRunAgentUsage(t.Context(), run.ID)
	content, err := os.ReadFile(filepath.Join(root, "output.txt"))
	if err != nil || string(content) != "approved workflow output\n" || e.Status != "completed" || p.calls != 3 || usage.TotalTokens != before.TotalTokens+5 {
		t.Fatalf("approval recovery: %+v calls=%d before=%+v after=%+v content=%q err=%v", e, p.calls, before, usage, content, err)
	}
}

func TestWorkflowHostDoesNotReplayUnfinishedAttemptAfterDatabaseReopen(t *testing.T) {
	p := &specialistTestProvider{}
	st, run, service, path := newWorkflowFixture(t, p)
	e := startHostWorkflow(t, service, run.ID, "host-lost-attempt", workflowSpec(workflowWorker("one")))
	_, _, err := st.ClaimWorkflowExecution(t.Context(), e.ID, "previous-host")
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := json.Marshal(e.State.Stack)
	key := runmutation.Fingerprint("workflow_step.v1", e.ID, string(identity), "one")
	job, err := st.PrepareWorkflowStep(t.Context(), domain.WorkflowStepExecution{ID: "workflowstep-" + key, ExecutionID: e.ID, StepID: "one", AgentID: "agent-" + key, SessionID: "sess-" + key, Inputs: json.RawMessage(`{}`)}, "previous-host")
	if err != nil {
		t.Fatal(err)
	}
	root, _, _ := st.GetRootAgent(t.Context(), run.ID)
	child, _, err := st.AdmitSpecialist(t.Context(), domain.SpecialistAdmission{AgentID: job.AgentID, SessionID: job.SessionID, RunID: run.ID, ParentAgentID: root.ID, Title: "one", Skills: []string{"model.chat"}, TurnLimit: 2, TokenLimit: 64, MaxChildren: domain.MaxAgentChildren}, "workflow-admit-"+job.ID)
	if err != nil {
		t.Fatal(err)
	}
	held, err := st.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "lost-host", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	operation := "workflow-turn-" + runmutation.Fingerprint(job.ID, "1")
	attemptID := "attempt-" + runmutation.Fingerprint("specialist_operator_step.v1", run.ID, child.ID, operation)
	if _, _, err := st.BeginSpecialistAttempt(t.Context(), domain.AgentAttemptStart{AttemptID: attemptID, RunID: run.ID, AgentID: child.ID, ParentAgentID: root.ID, Lease: held.Lease}, "specialist-start-"+attemptID); err != nil {
		t.Fatal(err)
	}
	// Relinquish host ownership without completing the attempt, then reopen the
	// database. Recovery must retain the unfinished attempt's operation identity.
	if _, _, err := st.ReleaseRunExecutionLease(t.Context(), held.Lease); err != nil {
		t.Fatal(err)
	}
	if err := st.ReleaseWorkflowExecution(t.Context(), e.ID, "previous-host"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service = newWorkflowService(st, p)
	workflowTick(t, service)
	e, _ = service.Get(t.Context(), e.ID)
	attempt, found, err := st.GetAgentAttempt(t.Context(), attemptID)
	if err != nil || !found || e.Status != "failed" || attempt.Failure.Code != "worker_lost" || p.calls != 0 {
		t.Fatalf("unfinished work was replayed: %+v attempt=%+v calls=%d err=%v", e, attempt, p.calls, err)
	}
}

type workflowHostFaultStore struct {
	*store.SQLiteStore
	leaseConflicts int
	usageFailure   bool
}

func (s *workflowHostFaultStore) AcquireRunExecutionLease(ctx context.Context, request domain.AcquireRunExecutionLeaseRequest) (domain.RunExecutionLeaseAcquisition, error) {
	if s.leaseConflicts > 0 {
		s.leaseConflicts--
		// Another host can acquire and release between two observations, leaving
		// the same idle state after this acquisition reported a conflict.
		return domain.RunExecutionLeaseAcquisition{}, apperror.New(apperror.CodeConflict, "competing host acquired the Run lease")
	}
	return s.SQLiteStore.AcquireRunExecutionLease(ctx, request)
}

func (s *workflowHostFaultStore) GetRunAgentUsage(ctx context.Context, runID string) (domain.RunAgentUsage, error) {
	if s.usageFailure {
		s.usageFailure = false
		return domain.RunAgentUsage{}, errors.New("temporary usage read failure")
	}
	return s.SQLiteStore.GetRunAgentUsage(ctx, runID)
}

func TestWorkflowHostRetriesTransientLeaseConflictWithoutDependencyChanges(t *testing.T) {
	p := &specialistTestProvider{responses: []llm.ChatResponse{workflowResponse(t, domain.CompletionSucceeded)}}
	st, run, _, _ := newWorkflowFixture(t, p)
	faults := &workflowHostFaultStore{SQLiteStore: st, leaseConflicts: 2}
	router := llm.NewRouter(llm.ModelRef{Provider: p.Name(), Model: "model"})
	router.RegisterProvider(p)
	service := application.NewWorkflowService(faults, application.NewSpecialistExecutionService(faults, router, policy.NewDefaultChecker()))
	e := startHostWorkflow(t, service, run.ID, "transient-conflict", workflowSpec(workflowWorker("one")))
	for i := 0; i < 2; i++ {
		workflowTick(t, service)
	}
	e, _ = service.Get(t.Context(), e.ID)
	if e.Status != "waiting" || e.State.WaitFingerprint != "" || p.calls != 0 {
		t.Fatalf("transient conflict became dormant: %+v calls=%d", e, p.calls)
	}
	workflowTick(t, service)
	workflowTick(t, service)
	e, _ = service.Get(t.Context(), e.ID)
	if e.Status != "completed" || p.calls != 1 {
		t.Fatalf("transient lease conflict did not recover: %+v calls=%d", e, p.calls)
	}
}

func TestWorkflowHostTransientReadFailureDoesNotHideLaterWork(t *testing.T) {
	p := &specialistTestProvider{responses: []llm.ChatResponse{workflowResponse(t, domain.CompletionSucceeded), workflowResponse(t, domain.CompletionSucceeded)}}
	st, run, _, _ := newWorkflowFixture(t, p)
	faults := &workflowHostFaultStore{SQLiteStore: st}
	router := llm.NewRouter(llm.ModelRef{Provider: p.Name(), Model: "model"})
	router.RegisterProvider(p)
	service := application.NewWorkflowService(faults, application.NewSpecialistExecutionService(faults, router, policy.NewDefaultChecker()))
	first := startHostWorkflow(t, service, run.ID, "transient-read-first", workflowSpec(workflowWorker("one")))
	second := startHostWorkflow(t, service, run.ID, "transient-read-second", workflowSpec(workflowWorker("two")))
	faults.usageFailure = true
	ran, err := service.RunDue(t.Context(), "fault-test-host", time.Now().UTC())
	if !ran || err == nil || !strings.Contains(err.Error(), "temporary usage read failure") {
		t.Fatalf("missing observable first failure: ran=%t err=%v", ran, err)
	}
	first, _ = service.Get(t.Context(), first.ID)
	second, _ = service.Get(t.Context(), second.ID)
	if first.Status != "running" || first.State.Jobs != 0 || second.State.Jobs != 1 || p.calls != 1 {
		t.Fatalf("temporary read failure hid runnable work: first=%+v second=%+v calls=%d", first, second, p.calls)
	}
	workflowTick(t, service)
	first, _ = service.Get(t.Context(), first.ID)
	if first.State.Jobs != 1 || p.calls != 2 {
		t.Fatalf("temporary read failure became a persistent wait: %+v calls=%d", first, p.calls)
	}
}
