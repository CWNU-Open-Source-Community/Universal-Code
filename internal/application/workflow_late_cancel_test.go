package application_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
)

// Use public Run controls after final verification has returned
// and immediately before the old interpreter writes its completed checkpoint.
type lateWorkflowCancelStore struct {
	*store.SQLiteStore
	action  string
	applied bool
}

func (s *lateWorkflowCancelStore) CheckpointWorkflowExecution(ctx context.Context, e domain.WorkflowExecution, owner string) (domain.WorkflowExecution, error) {
	if e.Status == "completed" && !s.applied {
		runs := application.NewRunService(s.SQLiteStore)
		var err error
		if s.action == "cancel" {
			_, err = runs.Cancel(ctx, e.RunID)
		} else {
			_, err = runs.Pause(ctx, e.RunID)
		}
		if err != nil {
			return e, err
		}
		s.applied = true
	}
	return s.SQLiteStore.CheckpointWorkflowExecution(ctx, e, owner)
}

func TestWorkflowLateCompletionCannotOverrideRunControlAfterReopen(t *testing.T) {
	for _, action := range []string{"cancel", "pause"} {
		t.Run(action, func(t *testing.T) { testWorkflowLateCompletionRunControl(t, action) })
	}
}

func testWorkflowLateCompletionRunControl(t *testing.T, action string) {
	t.Helper()
	p := &specialistTestProvider{responses: []llm.ChatResponse{workflowResponse(t, domain.CompletionSucceeded)}}
	raw, run, _, path := newWorkflowFixture(t, p)
	st := &lateWorkflowCancelStore{SQLiteStore: raw, action: action}
	router := llm.NewRouter(llm.ModelRef{Provider: p.Name(), Model: "model"})
	router.RegisterProvider(p)
	svc := application.NewWorkflowService(st, application.NewSpecialistExecutionService(st, router, policy.NewDefaultChecker()))
	e := startHostWorkflow(t, svc, run.ID, "cancel-final-checkpoint", workflowSpec(workflowWorker("one")))
	e, err := svc.Advance(t.Context(), e.ID)
	if err != nil || e.State.Jobs != 1 || e.Status != "running" {
		t.Fatalf("worker did not finish before cancellation boundary: %+v err=%v", e, err)
	}
	var stepID string
	for _, group := range e.State.Maps {
		stepID = group.Items[0].StepExecutionID
	}
	before, found, err := raw.GetWorkflowStep(t.Context(), stepID)
	if err != nil || !found || before.Status != "completed" {
		t.Fatalf("committed worker result missing: %+v found=%t err=%v", before, found, err)
	}
	completion, found, err := raw.GetAgentCompletion(t.Context(), before.AgentID)
	if err != nil || !found {
		t.Fatalf("native report missing: found=%t err=%v", found, err)
	}
	_, lateErr := svc.Advance(t.Context(), e.ID)
	t.Logf("old interpreter completion returned: %v", lateErr)
	if !st.applied {
		t.Fatal("did not reach final checkpoint cancellation boundary")
	}
	currentRun, err := raw.GetRun(t.Context(), run.ID)
	wantRun, wantWorkflow := domain.RunCancelled, "failed"
	if action == "pause" {
		wantRun, wantWorkflow = domain.RunPaused, "waiting"
	}
	if err != nil || currentRun.Status != wantRun {
		t.Fatalf("user %s did not commit: %+v err=%v", action, currentRun, err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	svc = newWorkflowService(reopened, p)
	for range 2 {
		if _, err := svc.RunDue(t.Context(), "reopened-after-cancel", time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	e, err = svc.Get(t.Context(), e.ID)
	if apperror.CodeOf(lateErr) != apperror.CodeConflict {
		t.Errorf("late completed checkpoint should retain Conflict semantics: %v", lateErr)
	}
	if err != nil || e.Status != wantWorkflow || action == "cancel" && e.Reason != "Run ended as cancelled" || p.calls != 1 {
		t.Fatalf("late completion overrode user %s: workflow=%s reason=%q calls=%d err=%v", action, e.Status, e.Reason, p.calls, err)
	}
	after, found, err := reopened.GetWorkflowStep(t.Context(), stepID)
	report, reportFound, reportErr := reopened.GetAgentCompletion(t.Context(), before.AgentID)
	attempts, attemptsErr := reopened.ListAgentAttempts(t.Context(), before.AgentID)
	usage, usageErr := reopened.GetRunAgentUsage(t.Context(), run.ID)
	if err != nil || !found || after.Status != "completed" || !bytes.Equal(after.Output, before.Output) || reportErr != nil || !reportFound || report.ID != completion.ID || report.AttemptID != completion.AttemptID || attemptsErr != nil || len(attempts) != 1 || attempts[0].ID != completion.AttemptID || usageErr != nil || usage.TotalTokens != 5 {
		t.Fatalf("Run %s lost or replayed completed child: step=%+v report=%+v attempts=%+v usage=%+v errs=%v/%v/%v/%v", action, after, report, attempts, usage, err, reportErr, attemptsErr, usageErr)
	}
	pending, err := reopened.ListWorkflowSummaryCandidates(t.Context(), time.Now().UTC(), 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("rejected completion published a summary: %+v err=%v", pending, err)
	}
	t.Logf("after reopen: Run=%s workflow=%s child=completed same_report=%s calls=%d tokens=%d", currentRun.Status, e.Status, report.ID, p.calls, usage.TotalTokens)
	if action == "pause" {
		if _, err := application.NewRunService(reopened).Start(t.Context(), run.ID); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if _, err := svc.RunDue(t.Context(), "explicitly-resumed-host", time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
		}
		e, err = svc.Get(t.Context(), e.ID)
		if err != nil || e.Status != "completed" || e.State.Jobs != 1 || p.calls != 1 {
			t.Fatalf("explicit resume did not reuse completed work: %+v calls=%d err=%v", e, p.calls, err)
		}
	}
}
