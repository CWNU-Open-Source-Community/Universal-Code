package store

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/policy"
)

func workflowDueFixture(t *testing.T) (*SQLiteStore, domain.Run, domain.WorkflowDefinition, *application.WorkflowService) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "workflow-due.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	runs := application.NewRunService(st)
	_, run, err := runs.Create(t.Context(), application.CreateRunRequest{Goal: "saved workflow host", Profile: "code", Budget: domain.Budget{MaxTurns: 20, MaxTokens: 10000}})
	if err != nil {
		t.Fatal(err)
	}
	run, err = runs.Start(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The false branch completes without calling a model, so unexpected dispatch
	// cannot be hidden by a fake successful execution service.
	definition, err := st.SaveWorkflowDefinition(t.Context(), domain.WorkflowSpec{Version: domain.WorkflowVersion, Name: "conditional", Limits: domain.WorkflowLimits{MaxSteps: 4, MaxJobs: 2, MaxTokens: 1000, MaxTurns: 4, TimeoutMillis: 60000}, Steps: []domain.WorkflowStep{{ID: "choose", Kind: "if", Condition: &domain.WorkflowCondition{Ref: domain.WorkflowValueRef{Step: "$input"}, Equals: json.RawMessage(`true`)}, Then: []domain.WorkflowStep{{ID: "unreachable", Kind: "worker", Task: &domain.ChildTask{Title: "unreachable", Goal: "must not run", Skills: []string{"model.chat"}, TurnLimit: 1, TokenLimit: 100, TimeoutMillis: 10000}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	service := application.NewWorkflowService(st, application.NewSpecialistExecutionService(st, nil, policy.NewDefaultChecker()))
	return st, run, definition, service
}

func parkWorkflow(t *testing.T, st *SQLiteStore, run domain.Run, d domain.WorkflowDefinition, id, status string) domain.WorkflowExecution {
	t.Helper()
	e, err := st.StartWorkflowExecution(t.Context(), domain.WorkflowExecution{ID: id, RunID: run.ID, DefinitionID: d.ID, Inputs: json.RawMessage(`false`)})
	if err != nil {
		t.Fatal(err)
	}
	if status == "running" {
		return e
	}
	e, claimed, err := st.ClaimWorkflowExecution(t.Context(), e.ID, "fixture-owner")
	if err != nil || !claimed {
		t.Fatalf("claim: %t %v", claimed, err)
	}
	e.Status = status
	e.Reason = "unchanged dependency"
	e.State.WaitFingerprint, err = st.WorkflowWakeFingerprint(t.Context(), run.ID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	e, err = st.CheckpointWorkflowExecution(t.Context(), e, "fixture-owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ReleaseWorkflowExecution(t.Context(), e.ID, "fixture-owner"); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestWorkflowDueSkipsMoreThanOnePageOfUnchangedWaits(t *testing.T) {
	st, run, d, service := workflowDueFixture(t)
	var dormant []domain.WorkflowExecution
	for i := 0; i < 35; i++ {
		dormant = append(dormant, parkWorkflow(t, st, run, d, fmt.Sprintf("workflowrun-wait-%03d", i), "waiting"))
	}
	ready := parkWorkflow(t, st, run, d, "workflowrun-ready", "running")
	ran, err := service.RunDue(t.Context(), "page-test-host", time.Now().UTC())
	if err != nil || !ran {
		t.Fatalf("runnable work hidden behind waits: %t %v", ran, err)
	}
	ready, _ = st.GetWorkflowExecution(t.Context(), ready.ID)
	if ready.Status != "completed" {
		t.Fatalf("later execution did not complete: %+v", ready)
	}
	for _, before := range dormant {
		after, err := st.GetWorkflowExecution(t.Context(), before.ID)
		if err != nil || after.Version != before.Version {
			t.Fatalf("dormant wait was checkpointed: %+v %v", after, err)
		}
	}
}

func TestWorkflowDueExpiresDormantWaitsEvenWhenRunIsPausedOrBusy(t *testing.T) {
	for _, status := range []string{"waiting", "waiting_approval"} {
		t.Run(status, func(t *testing.T) {
			st, run, d, service := workflowDueFixture(t)
			if _, err := application.NewRunService(st).Pause(t.Context(), run.ID); err != nil {
				t.Fatal(err)
			}
			e := parkWorkflow(t, st, run, d, "workflowrun-expired", status)
			if _, err := st.db.ExecContext(t.Context(), `UPDATE workflow_executions SET created_at=? WHERE id=?`, ts(time.Now().Add(-2*time.Minute)), e.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := st.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "busy-host", TTL: time.Minute}); err != nil {
				t.Fatal(err)
			}
			ran, err := service.RunDue(t.Context(), "deadline-host", time.Now().UTC())
			if err != nil || !ran {
				t.Fatalf("deadline did not wake: %t %v", ran, err)
			}
			e, _ = st.GetWorkflowExecution(t.Context(), e.ID)
			if e.Status != "failed" || e.Reason != "workflow deadline exceeded" {
				t.Fatalf("expired wait persisted: %+v", e)
			}
		})
	}
}

func TestWorkflowDueRecoversExpiredClaimAndFencesPreviousOwner(t *testing.T) {
	st, run, d, service := workflowDueFixture(t)
	e := parkWorkflow(t, st, run, d, "workflowrun-expired-claim", "running")
	e, claimed, err := st.ClaimWorkflowExecution(t.Context(), e.ID, "lost-host")
	if err != nil || !claimed {
		t.Fatalf("claim: %t %v", claimed, err)
	}
	if ran, err := service.RunDue(t.Context(), "new-host", time.Now().UTC()); err != nil || ran {
		t.Fatalf("live claim was stolen: %t %v", ran, err)
	}
	if _, err := st.db.ExecContext(t.Context(), `UPDATE workflow_executions SET claim_until=? WHERE id=?`, ts(time.Now().Add(-time.Minute)), e.ID); err != nil {
		t.Fatal(err)
	}
	recovered, claimed, err := st.ClaimWorkflowExecution(t.Context(), e.ID, "new-host")
	if err != nil || !claimed || recovered.Version != e.Version || recovered.Status != e.Status {
		t.Fatalf("claim recovery changed checkpoint: %+v claimed=%t err=%v", recovered, claimed, err)
	}
	if _, err := st.CheckpointWorkflowExecution(t.Context(), e, "lost-host"); err == nil {
		t.Fatal("lost host overwrote recovered execution")
	}
	// Both writes use the same valid revision and running state; only ownership
	// differs, so a terminal state or revision change cannot explain rejection.
	if _, err := st.CheckpointWorkflowExecution(t.Context(), e, "new-host"); err != nil {
		t.Fatalf("current owner could not checkpoint: %v", err)
	}
	if _, err := st.db.ExecContext(t.Context(), `UPDATE workflow_executions SET claim_until=? WHERE id=?`, ts(time.Now().Add(-time.Minute)), e.ID); err != nil {
		t.Fatal(err)
	}
	if ran, err := service.RunDue(t.Context(), "next-host", time.Now().UTC()); err != nil || !ran {
		t.Fatalf("expired claim not recovered: %t %v", ran, err)
	}
}
