package application_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
)

// Stop settlement after the real child commits its completion, leaving the
// durable workflow step pending as it would be after an interrupted host.
type lifecycleCompletionStopStore struct {
	*store.SQLiteStore
	id       string
	stopped  bool
	verified bool
}

func (s *lifecycleCompletionStopStore) ValidateWorkflowSettlement(ctx context.Context, id, owner string, revision int64, steps []string) error {
	if !s.stopped {
		s.stopped = true
		e, err := s.GetWorkflowExecution(ctx, id)
		if err != nil {
			return err
		}
		if _, err = s.ControlWorkflowExecution(ctx, id, "pause", e.Version); err != nil {
			return err
		}
	}
	return s.SQLiteStore.ValidateWorkflowSettlement(ctx, id, owner, revision, steps)
}

func (s *lifecycleCompletionStopStore) GetAgentCompletion(ctx context.Context, id string) (domain.AgentCompletion, bool, error) {
	c, found, err := s.SQLiteStore.GetAgentCompletion(ctx, id)
	if err == nil && found && s.verified && !s.stopped {
		e, readErr := s.GetWorkflowExecution(ctx, s.id)
		if readErr != nil {
			return c, found, readErr
		}
		_, err = s.ControlWorkflowExecution(ctx, e.ID, "pause", e.Version)
		s.stopped = err == nil
	}
	return c, found, err
}

func TestWorkflowCancelledPausePreservesCommittedChildResult(t *testing.T) {
	for _, verified := range []bool{false, true} {
		name := "report"
		if verified {
			name = "verified_code"
		}
		t.Run(name, func(t *testing.T) { testWorkflowCancelledPausePreservesCommittedChildResult(t, verified) })
	}
}

func testWorkflowCancelledPausePreservesCommittedChildResult(t *testing.T, verified bool) {
	p := &specialistTestProvider{responses: []llm.ChatResponse{workflowResponse(t, domain.CompletionSucceeded)}}
	st, run, _, _ := newWorkflowFixture(t, p)
	if verified {
		st, run, _ = pauseFaultFixture(t, t.Context(), p, t.TempDir())
	}
	faults := &lifecycleCompletionStopStore{SQLiteStore: st, verified: verified}
	router := llm.NewRouter(llm.ModelRef{Provider: p.Name(), Model: "model"})
	router.RegisterProvider(p)
	workers := application.NewSpecialistExecutionService(faults, router, policy.NewDefaultChecker()).WithWorkerRuntime(application.NewAgentRunner(st, router, policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}))
	service := application.NewWorkflowService(faults, workers)
	step := workflowWorker("one")
	if verified {
		step.Completion = &domain.WorkflowCompletion{Mode: "verified_code"}
		step.Task.Worker = &domain.WorkerProfile{}
	}
	e := startHostWorkflow(t, service, run.ID, "cancel-completed-child", workflowSpec(step))
	faults.id = e.ID
	e, err := service.Advance(t.Context(), e.ID)
	if err != nil || e.Status != "paused" || !faults.stopped {
		t.Fatalf("completion stop not established: %+v %v", e, err)
	}
	nodes, err := st.ListAgentNodes(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var child domain.AgentNode
	for _, n := range nodes {
		if n.Role == domain.AgentRoleSpecialist {
			child = n
		}
	}
	completion, found, err := st.GetAgentCompletion(t.Context(), child.ID)
	if err != nil || !found || child.Status != domain.AgentCompleted {
		t.Fatalf("native completion missing: %+v %v", child, err)
	}
	stepID := "workflowstep-" + strings.TrimPrefix(child.ID, "agent-")
	job, found, err := st.GetWorkflowStep(t.Context(), stepID)
	if err != nil || !found || job.Status != "pending" {
		t.Fatalf("step should await settlement: %+v %v", job, err)
	}
	if _, err := application.NewRunService(st).Cancel(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := service.RunDue(t.Context(), "cancel-settlement-host", time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	e, err = service.Get(t.Context(), e.ID)
	if err != nil || e.Status != "failed" || e.Reason != "Run ended as cancelled" {
		t.Fatalf("cancelled paused workflow did not settle: %+v %v", e, err)
	}
	after, err := st.GetAgentNode(t.Context(), child.ID)
	if err != nil || after.Status != domain.AgentCompleted || after.Version != child.Version || after.ActiveAttemptID != "" {
		t.Fatalf("cleanup changed completed child: %+v %v", after, err)
	}
	job, _, err = st.GetWorkflowStep(t.Context(), stepID)
	wantStatus := "completed"
	if verified {
		wantStatus = "failed"
	}
	if err != nil || job.Status != wantStatus || !strings.Contains(string(job.Output), completion.Report.Summary) {
		t.Fatalf("native report not settled: %+v %v", job, err)
	}
	attempts, err := st.ListAgentAttempts(t.Context(), child.ID)
	if err != nil || len(attempts) != 1 || attempts[0].ID != completion.AttemptID || p.calls != 1 {
		t.Fatalf("cleanup replayed child: attempts=%+v calls=%d err=%v", attempts, p.calls, err)
	}
}

// Exercise the retirement API with real approval attempts and prove rejection
// leaves all pending steps unchanged before accepting the current owner.
type lifecycleFenceStore struct {
	*store.SQLiteStore
	t *testing.T
}

func (s *lifecycleFenceStore) FinalizeWorkflowLifecycle(ctx context.Context, id, owner string, revision int64, lease *domain.RunExecutionLease, expected []domain.AgentAttempt) (domain.WorkflowExecution, error) {
	reject := func(label string, e domain.WorkflowExecution, err error) {
		s.t.Helper()
		if apperror.CodeOf(err) != apperror.CodeConflict {
			s.t.Fatalf("%s accepted: %+v %v", label, e, err)
		}
	}
	e, err := s.SQLiteStore.FinalizeWorkflowLifecycle(ctx, id, owner, revision+1, lease, expected)
	reject("stale revision", e, err)
	e, err = s.SQLiteStore.FinalizeWorkflowLifecycle(ctx, id, "wrong-owner", revision, lease, expected)
	reject("another claim", e, err)
	if lease != nil {
		wrong := *lease
		wrong.Generation++
		e, err = s.SQLiteStore.FinalizeWorkflowLifecycle(ctx, id, owner, revision, &wrong, expected)
		reject("old Run lease", e, err)
	}
	if len(expected) > 0 {
		wrong := append([]domain.AgentAttempt(nil), expected...)
		wrong[0].LeaseGeneration++
		e, err = s.SQLiteStore.FinalizeWorkflowLifecycle(ctx, id, owner, revision, lease, wrong)
		reject("another attempt lease", e, err)
		wrong = append([]domain.AgentAttempt(nil), expected...)
		wrong[0].ID = "attempt-wrong"
		e, err = s.SQLiteStore.FinalizeWorkflowLifecycle(ctx, id, owner, revision, lease, wrong)
		reject("another attempt", e, err)
	}
	if _, claimed, err := s.ClaimWorkflowExecution(ctx, id, "competing-host"); err != nil || claimed {
		s.t.Fatalf("concurrent host stole cleanup: %t %v", claimed, err)
	}
	return s.SQLiteStore.FinalizeWorkflowLifecycle(ctx, id, owner, revision, lease, expected)
}
