package application_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
)

const workflowRestartExit = 87

// The subprocess exits only after the real SQLite method commits. Neither Go
// cleanup nor the workflow/Run lease release defers run.
type workflowRestartCrashStore struct {
	*store.SQLiteStore
	boundary string
}

func (s *workflowRestartCrashStore) exit(boundary string, err error) {
	if err == nil && s.boundary == boundary {
		fmt.Println("workflow crash after committed " + boundary)
		os.Exit(workflowRestartExit)
	}
}

func (s *workflowRestartCrashStore) PrepareWorkflowStep(ctx context.Context, step domain.WorkflowStepExecution, owner string) (domain.WorkflowStepExecution, error) {
	step, err := s.SQLiteStore.PrepareWorkflowStep(ctx, step, owner)
	s.exit("prepared", err)
	return step, err
}

func (s *workflowRestartCrashStore) AcquireRunExecutionLease(ctx context.Context, request domain.AcquireRunExecutionLeaseRequest) (domain.RunExecutionLeaseAcquisition, error) {
	lease, err := s.SQLiteStore.AcquireRunExecutionLease(ctx, request)
	s.exit("leased", err)
	return lease, err
}

func (s *workflowRestartCrashStore) BeginSpecialistAttempt(ctx context.Context, start domain.AgentAttemptStart, operation string) (domain.AgentAttempt, bool, error) {
	attempt, replay, err := s.SQLiteStore.BeginSpecialistAttempt(ctx, start, operation)
	s.exit("attempt", err)
	return attempt, replay, err
}

func TestWorkflowRestartProcessChild(t *testing.T) {
	path, boundary := os.Getenv("UC_WORKFLOW_RESTART_DB"), os.Getenv("UC_WORKFLOW_RESTART_BOUNDARY")
	if path == "" || boundary == "" {
		t.Skip("only run in the workflow restart subprocess")
	}
	raw, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st := &workflowRestartCrashStore{SQLiteStore: raw, boundary: boundary}
	p := &specialistTestProvider{respond: func(int, llm.ChatRequest) (llm.ChatResponse, error) {
		return llm.ChatResponse{}, fmt.Errorf("model must not start before crash boundary %s", boundary)
	}}
	router := llm.NewRouter(llm.ModelRef{Provider: p.Name(), Model: "model"})
	router.RegisterProvider(p)
	svc := application.NewWorkflowService(st, application.NewSpecialistExecutionService(st, router, policy.NewDefaultChecker()))
	_, err = svc.RunDue(t.Context(), "crashing-workflow-host", time.Now().UTC())
	t.Fatalf("did not reach %s process exit: %v", boundary, err)
}

func TestWorkflowRestartProcessRetainsPreparedIdentityAndFencesLostAttempt(t *testing.T) {
	for _, test := range []struct{ boundary, control string }{
		{boundary: "prepared"}, {boundary: "leased"}, {boundary: "attempt"},
		{boundary: "leased", control: "pause"}, {boundary: "leased", control: "cancel"},
	} {
		t.Run(test.boundary+"/"+test.control, func(t *testing.T) {
			boundary := test.boundary
			p := &specialistTestProvider{responses: []llm.ChatResponse{workflowResponse(t, domain.CompletionSucceeded)}}
			st, run, svc, path := newWorkflowFixture(t, p)
			spec := workflowSpec(workflowWorker("one"))
			// Leave enough overall time for the existing 15-minute claim to
			// expire. The timestamp shift below represents 16 minutes elapsed.
			spec.Limits.TimeoutMillis = int64((time.Hour).Milliseconds())
			e := startHostWorkflow(t, svc, run.ID, "process-restart-"+boundary, spec)
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			child := exec.CommandContext(t.Context(), binary, "-test.run=^TestWorkflowRestartProcessChild$", "-test.v")
			child.Env = append(os.Environ(), "UC_WORKFLOW_RESTART_DB="+path, "UC_WORKFLOW_RESTART_BOUNDARY="+boundary)
			output, err := child.CombinedOutput()
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != workflowRestartExit || !strings.Contains(string(output), "workflow crash after committed "+boundary) {
				t.Fatalf("wrong crash boundary: err=%v output=%s", err, output)
			}
			t.Logf("actual subprocess exit %d after SQLite %s commit", exit.ExitCode(), boundary)
			st, err = store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			svc = newWorkflowService(st, p)
			e, err = svc.Get(t.Context(), e.ID)
			if err != nil || len(e.State.Maps) != 1 {
				t.Fatalf("frozen task group missing: %+v err=%v", e, err)
			}
			var stepID string
			for _, group := range e.State.Maps {
				stepID = group.Items[0].StepExecutionID
			}
			before, found, err := st.GetWorkflowStep(t.Context(), stepID)
			if err != nil || !found || before.Status != "pending" {
				t.Fatalf("committed preparation missing: %+v found=%t err=%v", before, found, err)
			}
			oldLease, hadLease, err := st.GetRunExecutionLease(t.Context(), run.ID)
			if err != nil || hadLease != (boundary != "prepared") {
				t.Fatalf("crash lease boundary changed: lease=%+v found=%t err=%v", oldLease, hadLease, err)
			}
			// Opening a second host must not steal a live workflow claim.
			if ran, err := svc.RunDue(t.Context(), "reopened-workflow-host", time.Now().UTC()); ran || err != nil || p.calls != 0 {
				t.Fatalf("restart bypassed claim: ran=%t calls=%d err=%v", ran, p.calls, err)
			}
			switch test.control {
			case "pause":
				if _, err := svc.Pause(t.Context(), e.ID, e.Version); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				if _, err := application.NewRunService(st).Cancel(t.Context(), run.ID); err != nil {
					t.Fatal(err)
				}
			}
			if test.control != "" {
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
				st, err = store.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer st.Close()
				svc = newWorkflowService(st, p)
			}
			shiftWorkflowCrashTime(t, path, e.ID, run.ID)
			for range 3 {
				if _, err := svc.RunDue(t.Context(), "reopened-workflow-host", time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			after, _, err := st.GetWorkflowStep(t.Context(), stepID)
			if err != nil || after.ID != before.ID || after.AgentID != before.AgentID || after.SessionID != before.SessionID {
				t.Fatalf("prepared identity changed: before=%+v after=%+v err=%v", before, after, err)
			}
			e, err = svc.Get(t.Context(), e.ID)
			attempts, attemptsErr := st.ListAgentAttempts(t.Context(), after.AgentID)
			usage, usageErr := st.GetRunAgentUsage(t.Context(), run.ID)
			if test.control != "" {
				wantStatus, wantStep := "paused", "pending"
				if test.control == "cancel" {
					wantStatus, wantStep = "failed", "failed"
				}
				if err != nil || attemptsErr != nil || usageErr != nil || e.Status != wantStatus || after.Status != wantStep || len(attempts) != 0 || p.calls != 0 || usage.TotalTokens != 0 {
					t.Fatalf("restart revived %s work: execution=%+v step=%+v attempts=%+v calls=%d usage=%+v errs=%v/%v/%v", test.control, e, after, attempts, p.calls, usage, err, attemptsErr, usageErr)
				}
				t.Logf("%s survives crash reopen and claim expiry: execution=%s step=%s provider_calls=%d", test.control, e.Status, after.Status, p.calls)
				return
			}
			if err != nil || attemptsErr != nil || usageErr != nil || len(attempts) != 1 {
				t.Fatalf("recovery records: execution=%+v attempts=%+v usage=%+v errs=%v/%v/%v", e, attempts, usage, err, attemptsErr, usageErr)
			}
			if hadLease {
				lease, found, err := st.GetRunExecutionLease(t.Context(), run.ID)
				if err != nil || !found || lease.Generation != oldLease.Generation+1 || lease.LeaseID == oldLease.LeaseID || lease.OwnerID == oldLease.OwnerID || lease.Status != domain.RunExecutionLeaseReleased {
					t.Fatalf("second host did not take over and release the expired lease: old=%+v new=%+v found=%t err=%v", oldLease, lease, found, err)
				}
				if _, err := st.RenewRunExecutionLease(t.Context(), oldLease, time.Minute); err == nil {
					t.Fatal("dead host's lease can still be renewed after takeover")
				}
			}
			if boundary == "attempt" {
				if e.Status != "failed" || after.Status != "failed" || attempts[0].Failure.Code != "worker_lost" || p.calls != 0 || usage.TotalTokens != 0 {
					t.Fatalf("unknown attempt replayed: execution=%+v step=%+v attempts=%+v calls=%d usage=%+v", e, after, attempts, p.calls, usage)
				}
			} else if e.Status != "completed" || e.State.Jobs != 1 || after.Status != "completed" || attempts[0].Turn != 1 || p.calls != 1 || usage.TotalTokens != 5 {
				t.Fatalf("prepared work lost or duplicated: execution=%+v step=%+v attempts=%+v calls=%d usage=%+v", e, after, attempts, p.calls, usage)
			}
			t.Logf("after simulated claim/lease expiry: execution=%s step=%s attempt=%s turn=%d provider_calls=%d tokens=%d", e.Status, after.Status, attempts[0].ID, attempts[0].Turn, p.calls, usage.TotalTokens)
		})
	}
}

// Only time is fault-injected: move the workflow creation/claim and any active
// Run lease back together. No identity, status, revision or result is seeded.
func shiftWorkflowCrashTime(t *testing.T, path, executionID, runID string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var created, claim string
	if err := tx.QueryRowContext(t.Context(), `SELECT created_at,claim_until FROM workflow_executions WHERE id=?`, executionID).Scan(&created, &claim); err != nil {
		t.Fatal(err)
	}
	shift := func(value string) string {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			t.Fatal(err)
		}
		return parsed.Add(-16 * time.Minute).UTC().Format(time.RFC3339Nano)
	}
	if _, err := tx.ExecContext(t.Context(), `UPDATE workflow_executions SET created_at=?,claim_until=? WHERE id=?`, shift(created), shift(claim), executionID); err != nil {
		t.Fatal(err)
	}
	var acquired, renewed, expires string
	err = tx.QueryRowContext(t.Context(), `SELECT acquired_at,renewed_at,expires_at FROM run_execution_leases WHERE run_id=?`, runID).Scan(&acquired, &renewed, &expires)
	if err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	if err == nil {
		if _, err := tx.ExecContext(t.Context(), `UPDATE run_execution_leases SET acquired_at=?,renewed_at=?,expires_at=? WHERE run_id=?`, shift(acquired), shift(renewed), shift(expires), runID); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
