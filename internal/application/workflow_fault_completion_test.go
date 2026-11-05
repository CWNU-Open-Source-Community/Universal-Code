package application_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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

const completionFaultSummary = "The saved workflow completed with committed proof."
const completionFaultExitCode = 86

// Only the completed checkpoint and its own summary are faulted. Every other
// operation, including worker execution, uses the embedded real SQLite store.
type completionFaultStore struct {
	*store.SQLiteStore
	mode        string
	completedID string
	injections  int
}

func (s *completionFaultStore) CheckpointWorkflowExecution(ctx context.Context, execution domain.WorkflowExecution, owner string) (domain.WorkflowExecution, error) {
	execution, err := s.SQLiteStore.CheckpointWorkflowExecution(ctx, execution, owner)
	if err == nil && execution.Status == "completed" {
		s.completedID = execution.ID
		if s.mode == "process-exit" {
			// SQLite's UPDATE and readback have returned. Exit the actual test
			// subprocess before WorkflowService can enqueue the summary or defer
			// its workflow claim release; do not run Close or Go test cleanup.
			fmt.Printf("FAULT: completed checkpoint committed; exiting process before summary (exit=%d)\n", completionFaultExitCode)
			os.Exit(completionFaultExitCode)
		}
	}
	return execution, err
}

func (s *completionFaultStore) GetRun(ctx context.Context, id string) (domain.Run, error) {
	if s.mode == "get-run-once" && s.completedID != "" && s.injections == 0 {
		s.injections++
		return domain.Run{}, apperror.New(apperror.CodeUnavailable, "injected completion-summary GetRun failure")
	}
	return s.SQLiteStore.GetRun(ctx, id)
}

func (s *completionFaultStore) EnqueueOperatorSteering(ctx context.Context, request domain.EnqueueOperatorSteeringRequest) (domain.OperatorSteeringEnqueueResult, error) {
	key := "specialist-summary-" + runmutation.Fingerprint("workflow-summary.v1", s.completedID)
	if (s.mode == "enqueue-always" || (s.mode == "enqueue-once" && s.injections == 0)) && s.completedID != "" && request.OperationKey == key && request.RequestedBy == "specialist_completion" {
		s.injections++
		return domain.OperatorSteeringEnqueueResult{}, apperror.New(apperror.CodeUnavailable, "injected completion-summary EnqueueOperatorSteering failure")
	}
	return s.SQLiteStore.EnqueueOperatorSteering(ctx, request)
}

func (s *completionFaultStore) FinishWorkflowSummary(ctx context.Context, executionID, owner, status, reason string, next time.Time) error {
	if s.mode == "finish-once" && s.injections == 0 {
		s.injections++
		return apperror.New(apperror.CodeUnavailable, "injected workflow summary acknowledgement failure")
	}
	return s.SQLiteStore.FinishWorkflowSummary(ctx, executionID, owner, status, reason, next)
}

func completionFaultService(st *completionFaultStore, provider *specialistTestProvider) *application.WorkflowService {
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	checker := policy.NewDefaultChecker()
	handoff := application.NewRunExecutionHandoffService(st.SQLiteStore, router, checker)
	workers := application.NewSpecialistExecutionService(st, router, checker).WithCompletionSummary(handoff)
	return application.NewWorkflowService(st, workers)
}

func completionFaultFixture(t *testing.T, mode string) (*completionFaultStore, domain.Run, *application.WorkflowService, domain.WorkflowExecution, *specialistTestProvider, string) {
	t.Helper()
	provider := &specialistTestProvider{responses: []llm.ChatResponse{
		workflowResponse(t, domain.CompletionSucceeded),
		{Text: rootActionResponse(domain.RootActionContinue, completionFaultSummary, "", "")},
	}}
	raw, run, _, path := newWorkflowFixture(t, provider)
	st := &completionFaultStore{SQLiteStore: raw, mode: mode}
	service := completionFaultService(st, provider)
	execution := startHostWorkflow(t, service, run.ID, "completion-fault", workflowSpec(workflowWorker("one")))
	workflowTick(t, service)
	before := completionFaultEvidence(t, raw, run, execution.ID)
	if before.Status != "running" || before.Jobs != 1 || before.Notifications != 0 || before.Summaries != 0 || provider.calls != 1 || st.injections != 0 {
		t.Fatalf("worker was not settled before completion boundary: %+v calls=%d injections=%d", before, provider.calls, st.injections)
	}
	t.Logf("before completion: %+v provider_calls=%d", before, provider.calls)
	return st, run, service, execution, provider, path
}

type completionFaultState struct {
	Status        string
	Jobs          int
	Notifications int
	Committed     int
	Summaries     int
	WorkerTokens  int64
}

func completionFaultEvidence(t *testing.T, st *store.SQLiteStore, run domain.Run, executionID string) completionFaultState {
	t.Helper()
	execution, err := st.GetWorkflowExecution(t.Context(), executionID)
	if err != nil {
		t.Fatal(err)
	}
	result := completionFaultState{Status: execution.Status, Jobs: execution.State.Jobs}
	if _, ok := execution.State.Outputs["one"]; result.Jobs == 1 && !ok {
		t.Fatal("committed worker output was lost")
	}
	notifications, err := st.ListOperatorSteering(t.Context(), run.ID, domain.MaxOperatorSteeringListLimit)
	if err != nil {
		t.Fatal(err)
	}
	for _, notification := range notifications {
		if strings.Contains(notification.Content, "[Authorized workflow completion notification]") && strings.Contains(notification.Content, executionID) {
			result.Notifications++
			if notification.Status == domain.OperatorSteeringCommitted {
				result.Committed++
			}
		}
	}
	messages, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if string(message.Role) == "assistant" && message.Content == completionFaultSummary {
			result.Summaries++
		}
	}
	usage, err := st.GetRunAgentUsage(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	result.WorkerTokens = usage.TotalTokens
	return result
}

func requireOneCompletionFaultSummary(t *testing.T, st *store.SQLiteStore, run domain.Run, executionID string, provider *specialistTestProvider) {
	t.Helper()
	state := completionFaultEvidence(t, st, run, executionID)
	if state.Status != "completed" || state.Jobs != 1 || state.Notifications != 1 || state.Committed != 1 || state.Summaries != 1 || state.WorkerTokens != 5 || provider.calls != 2 {
		t.Fatalf("completion was lost or duplicated: %+v provider_calls=%d", state, provider.calls)
	}
	var delivered bool
	for _, message := range provider.requests[len(provider.requests)-1].Messages {
		delivered = delivered || (strings.Contains(message.Content, "[Authorized workflow completion notification]") && strings.Contains(message.Content, executionID) && strings.Contains(message.Content, "committed proof succeeded"))
	}
	if !delivered {
		t.Fatal("root model request did not receive the saved workflow's actual completion output")
	}
	t.Logf("exactly one delivered summary: %+v provider_calls=%d", state, provider.calls)
}

func TestWorkflowFaultCompletionNormalExactlyOnce(t *testing.T) {
	st, run, service, execution, provider, path := completionFaultFixture(t, "")
	workflowTick(t, service)
	for i := 0; i < 2; i++ {
		if _, err := service.Advance(t.Context(), execution.ID); err != nil {
			t.Fatal(err)
		}
	}
	requireOneCompletionFaultSummary(t, st.SQLiteStore, run, execution.ID, provider)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	service = completionFaultService(&completionFaultStore{SQLiteStore: reopened}, provider)
	if _, err := service.Advance(t.Context(), execution.ID); err != nil {
		t.Fatal(err)
	}
	requireOneCompletionFaultSummary(t, reopened, run, execution.ID, provider)
}

// Storage faults retain the completed output and a retryable notification.
// A healthy host recovers it without running the completed worker again.
func TestWorkflowFaultCompletionStorageFailureRecovery(t *testing.T) {
	for _, mode := range []string{"get-run-once", "enqueue-once"} {
		t.Run(mode, func(t *testing.T) {
			st, run, service, execution, provider, path := completionFaultFixture(t, mode)
			ran, err := service.RunDue(t.Context(), "completion-fault-host", time.Now().UTC())
			if !ran || err == nil || !strings.Contains(err.Error(), "main-conversation summary is pending") || st.injections != 1 {
				t.Fatalf("completion summary fault was not reached exactly once: ran=%t injections=%d err=%v", ran, st.injections, err)
			}
			state := completionFaultEvidence(t, st.SQLiteStore, run, execution.ID)
			if state.Status != "completed" || state.Notifications != 0 || state.Summaries != 0 || provider.calls != 1 {
				t.Fatalf("fault did not occur after completed checkpoint and before enqueue: %+v calls=%d", state, provider.calls)
			}
			t.Logf("method-level one-shot %s: ran=%t injections=%d error=%v state=%+v", mode, ran, st.injections, err, state)
			ran, err = service.RunDue(t.Context(), "completion-same-process-retry", time.Now().UTC())
			if err != nil || ran {
				t.Fatalf("notification ignored retry backoff: ran=%t err=%v", ran, err)
			}
			t.Logf("healthy same-process RunDue retry: ran=%t injections=%d state=%+v", ran, st.injections, completionFaultEvidence(t, st.SQLiteStore, run, execution.ID))
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			completionFaultRecover(t, path, run, execution.ID, provider)
		})
	}
}

func TestWorkflowFaultCompletionProcessExitRecovery(t *testing.T) {
	st, run, _, execution, provider, path := completionFaultFixture(t, "")
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(t.Context(), testBinary, "-test.run=^TestWorkflowFaultCompletionCrashChild$", "-test.v")
	child.Env = append(os.Environ(), "UC_WORKFLOW_FAULT_COMPLETION_CHILD_DB="+path, "UC_WORKFLOW_FAULT_COMPLETION_CHILD_ID="+execution.ID)
	output, err := child.CombinedOutput()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != completionFaultExitCode || !strings.Contains(string(output), "completed checkpoint committed; exiting process before summary") {
		t.Fatalf("child did not exit at the completed checkpoint: error=%v output=%s", err, output)
	}
	t.Logf("real subprocess terminated with exit=%d (no Close/deferred claim release):\n%s", exit.ExitCode(), output)
	completionFaultRecover(t, path, run, execution.ID, provider)
}

func TestWorkflowFaultCompletionCrashChild(t *testing.T) {
	path, executionID := os.Getenv("UC_WORKFLOW_FAULT_COMPLETION_CHILD_DB"), os.Getenv("UC_WORKFLOW_FAULT_COMPLETION_CHILD_ID")
	if path == "" || executionID == "" {
		t.Skip("only run as the completion-checkpoint crash subprocess")
	}
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	provider := &specialistTestProvider{respond: func(int, llm.ChatRequest) (llm.ChatResponse, error) {
		return llm.ChatResponse{}, fmt.Errorf("unexpected model call before completion-checkpoint process exit")
	}}
	service := completionFaultService(&completionFaultStore{SQLiteStore: st, mode: "process-exit"}, provider)
	if _, err := service.RunDue(t.Context(), "completion-crashing-host", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	t.Fatalf("execution %s never reached the completed-checkpoint exit", executionID)
}

func completionFaultRecover(t *testing.T, path string, run domain.Run, executionID string, provider *specialistTestProvider) {
	t.Helper()
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	service := completionFaultService(&completionFaultStore{SQLiteStore: reopened}, provider)
	state := completionFaultEvidence(t, reopened, run, executionID)
	if state.Status != "completed" || state.Jobs != 1 || state.Notifications != 0 || state.Summaries != 0 || state.WorkerTokens != 5 || provider.calls != 1 {
		t.Fatalf("reopened SQLite did not retain exactly the pre-notification checkpoint: %+v calls=%d", state, provider.calls)
	}
	t.Logf("SQLite reopened with healthy store and summary handoff: %+v", state)
	// Recovery uses the independent notification obligation, even while the
	// abandoned interpreter claim remains live after the subprocess exit.
	for _, offset := range []time.Duration{0, 6 * time.Second, 16 * time.Minute, 24 * time.Hour} {
		now := time.Now().UTC().Add(offset)
		candidates, err := reopened.ListWorkflowCandidates(t.Context(), time.Time{}, "", now, 32)
		if err != nil {
			t.Fatal(err)
		}
		ran, err := service.RunDue(t.Context(), "completion-recovered-host", now)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("host RunDue scheduler_offset=%s candidates=%d ran=%t provider_calls=%d", offset, len(candidates), ran, provider.calls)
	}
	state = completionFaultEvidence(t, reopened, run, executionID)
	if state.Notifications != 1 || state.Committed != 1 || state.Summaries != 1 || provider.calls != 2 {
		t.Fatalf("completed workflow did not automatically recover after healthy database reopen and RunDue: %+v provider_calls=%d; want one committed notification and one summary without operator Advance", state, provider.calls)
	}
	// Explicit advances after host delivery must also remain idempotent.
	for i := 0; i < 2; i++ {
		if _, err := service.Advance(t.Context(), executionID); err != nil {
			t.Fatalf("explicit Advance recovery %d: %v", i+1, err)
		}
		requireOneCompletionFaultSummary(t, reopened, run, executionID, provider)
	}
}
