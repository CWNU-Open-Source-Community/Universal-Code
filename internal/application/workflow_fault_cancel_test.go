package application_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/store"
)

// SQLite, workers, cancellation and host continuation are real; only model
// responses use the deterministic test provider.
func TestWorkflowFaultCancelRunConverges(t *testing.T) {
	for _, cancelRun := range []bool{false, true} {
		name := "normal_completion"
		if cancelRun {
			name = "cancel_between_rounds"
		}
		t.Run(name, func(t *testing.T) {
			settled := func(status string) bool {
				if cancelRun {
					return status == "failed" || status == "cancelled"
				}
				return status == "completed"
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			continued := llm.ChatResponse{Text: specialistResponse(t, domain.SpecialistAction{
				Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue,
				Message: "continue in the next host round",
			}), Usage: llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}
			p := &specialistTestProvider{responses: []llm.ChatResponse{
				continued, workflowResponse(t, domain.CompletionSucceeded),
			}}
			st, run, service, path := newWorkflowFixture(t, p)
			spec := workflowSpec(workflowWorker("one"))
			spec.Limits.TimeoutMillis = 3000
			e := startHostWorkflow(t, service, run.ID, "fault-cancel-"+name, spec)
			deadline := e.CreatedAt.Add(time.Duration(spec.Limits.TimeoutMillis) * time.Millisecond)
			var stepID, childID string

			observe := func(phase string) domain.WorkflowExecution {
				t.Helper()
				current, err := service.Get(ctx, e.ID)
				if err != nil {
					t.Fatal(err)
				}
				for _, group := range current.State.Maps {
					if len(group.Items) != 1 {
						t.Fatalf("expected one frozen worker: %+v", group)
					}
					stepID = group.Items[0].StepExecutionID
				}
				job, found, err := st.GetWorkflowStep(ctx, stepID)
				if err != nil || !found {
					t.Fatalf("workflow step missing: found=%t err=%v", found, err)
				}
				childID = job.AgentID
				child, err := st.GetAgentNode(ctx, childID)
				if err != nil {
					t.Fatal(err)
				}
				attempts, err := st.ListAgentAttempts(ctx, childID)
				if err != nil {
					t.Fatal(err)
				}
				statuses := make([]domain.AgentAttemptStatus, len(attempts))
				for i, attempt := range attempts {
					statuses[i] = attempt.Status
				}
				currentRun, err := st.GetRun(ctx, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("%s: run=%s workflow=%s revision=%d reason=%q step=%s child=%s active_attempt=%q attempts=%v model_calls=%d",
					phase, currentRun.Status, current.Status, current.Version, current.Reason,
					job.Status, child.Status, child.ActiveAttemptID, statuses, p.calls)
				return current
			}
			tick := func(phase string) domain.WorkflowExecution {
				t.Helper()
				ran, err := service.RunDue(ctx, "fault-cancel-host", time.Now().UTC())
				t.Logf("%s: RunDue ran=%t error=%v", phase, ran, err)
				var logCauses func(error)
				logCauses = func(cause error) {
					if joined, ok := cause.(interface{ Unwrap() []error }); ok {
						for _, child := range joined.Unwrap() {
							logCauses(child)
						}
					} else if cause != nil {
						t.Logf("%s: cause code=%s error=%v", phase, apperror.CodeOf(cause), cause)
						logCauses(errors.Unwrap(cause))
					}
				}
				logCauses(err)
				if err != nil {
					t.Errorf("host should settle the workflow without an error: code=%s err=%v", apperror.CodeOf(err), err)
				}
				return observe(phase)
			}
			reopen := func() {
				t.Helper()
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
				var err error
				st, err = store.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				service = newWorkflowService(st, p)
			}
			t.Cleanup(func() { _ = st.Close() })

			e = tick("first_continue")
			if p.calls != 1 || e.State.Jobs != 0 || !time.Now().Before(deadline) {
				t.Fatalf("first round did not yield before the workflow deadline: calls=%d jobs=%d", p.calls, e.State.Jobs)
			}
			if cancelRun {
				if _, err := application.NewRunService(st).Cancel(ctx, run.ID); err != nil {
					t.Fatal(err)
				}
				observe("after_cancel")
			}
			for i := 1; i <= 3; i++ {
				e = tick(fmt.Sprintf("host_tick_%d", i))
			}
			if cancelRun && !settled(e.Status) {
				t.Errorf("cancelled Run must settle its unfinished workflow, got %q", e.Status)
			}
			reopen()
			observe("database_reopened")
			for i := 1; i <= 3; i++ {
				e = tick(fmt.Sprintf("reopened_host_tick_%d", i))
			}
			if cancelRun {
				// Let the real deadline elapse; do not mutate SQLite timestamps or
				// pass a synthetic clock that advanceClaimed would ignore.
				timer := time.NewTimer(max(0, time.Until(deadline)+10*time.Millisecond))
				defer timer.Stop()
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-timer.C:
				}
				for i := 1; i <= 3; i++ {
					e = tick(fmt.Sprintf("expired_host_tick_%d", i))
				}
				reopen()
				e = tick("expired_after_second_reopen")
			}
			wantCalls, wantTokens := 2, int64(10)
			if cancelRun {
				wantCalls, wantTokens = 1, 5
			}
			if !settled(e.Status) {
				t.Errorf("workflow should have settled, got status=%q", e.Status)
			}
			job, found, err := st.GetWorkflowStep(ctx, stepID)
			if err != nil || !found || !settled(job.Status) {
				t.Errorf("workflow step should have settled: found=%t status=%q err=%v", found, job.Status, err)
			}
			child, err := st.GetAgentNode(ctx, childID)
			if err != nil || !child.Terminal() || child.ActiveAttemptID != "" {
				t.Errorf("child should be terminal without an active attempt: child=%+v err=%v", child, err)
			}
			usage, err := st.GetRunAgentUsage(ctx, run.ID)
			if err != nil || p.calls != wantCalls || usage.TotalTokens != wantTokens {
				t.Errorf("unexpected model replay or budget loss: calls=%d tokens=%d err=%v", p.calls, usage.TotalTokens, err)
			}
			lease, found, err := st.GetRunExecutionLease(ctx, run.ID)
			if err != nil || (found && lease.ActiveAt(time.Now().UTC())) {
				t.Errorf("execution lease should be released: found=%t lease=%+v err=%v", found, lease, err)
			}
			jobs, err := st.ListCommandRuntimeJobs(ctx, runner.CommandRuntimeListFilter{RunID: run.ID})
			if err != nil || len(jobs) != 0 {
				t.Errorf("no command tools were requested: jobs=%d err=%v", len(jobs), err)
			}
			t.Logf("final: workflow=%s step=%s model_calls=%d total_tokens=%d lease_status=%s lease_active=%t command_jobs=%d",
				e.Status, job.Status, p.calls, usage.TotalTokens, lease.Status, found && lease.ActiveAt(time.Now().UTC()), len(jobs))
		})
	}
}
