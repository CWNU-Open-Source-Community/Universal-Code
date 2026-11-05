package store

import (
	"cyberagent-workbench/internal/domain"
	"encoding/json"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
)

func TestWorkflowLifecycleClaimsPausedOnlyWhenMaintenanceIsDue(t *testing.T) {
	st, run, d, service := workflowDueFixture(t)
	ctx := t.Context()
	e := parkWorkflow(t, st, run, d, "workflowrun-pause-lifecycle", "running")
	e, err := service.Pause(ctx, e.ID, e.Version)
	if err != nil {
		t.Fatal(err)
	}
	if ran, err := service.RunDue(ctx, "paused-host", time.Now().UTC()); err != nil || ran {
		t.Fatalf("unexpired pause ran: %t %v", ran, err)
	}
	after, err := service.Advance(ctx, e.ID)
	if err != nil || after.Version != e.Version || after.Status != "paused" {
		t.Fatalf("manual advance changed pause: %+v %v", after, err)
	}
	if _, claimed, err := st.ClaimWorkflowExecution(ctx, e.ID, "early-host"); err != nil || claimed {
		t.Fatalf("unexpired pause claimed: %t %v", claimed, err)
	}
	if _, err := application.NewRunService(st).Cancel(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	wake, err := st.WorkflowWakeFingerprint(ctx, run.ID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.db.ExecContext(ctx, `UPDATE workflow_executions SET state_json=json_set(state_json,'$.wait_fingerprint',?) WHERE id=?`, wake, e.ID); err != nil {
		t.Fatal(err)
	}
	candidates, err := st.ListWorkflowCandidates(ctx, time.Time{}, "", time.Now().UTC(), 128)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("cancelled paused execution not selected: %+v %v", candidates, err)
	}
	e, claimed, err := st.ClaimWorkflowExecution(ctx, e.ID, "cleanup-host")
	if err != nil || !claimed {
		t.Fatalf("cancelled pause not claimed: %t %v", claimed, err)
	}
	if _, claimed, err := st.ClaimWorkflowExecution(ctx, e.ID, "second-host"); err != nil || claimed {
		t.Fatalf("live cleanup claim stolen: %t %v", claimed, err)
	}
	if _, err := st.FinalizeWorkflowLifecycle(ctx, e.ID, "cleanup-host", e.Version+1, nil, nil); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("wrong revision settled: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE workflow_executions SET claim_until=? WHERE id=?`, ts(time.Now().Add(-time.Minute)), e.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FinalizeWorkflowLifecycle(ctx, e.ID, "cleanup-host", e.Version, nil, nil); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("expired claim settled: %v", err)
	}
	if _, claimed, err := st.ClaimWorkflowExecution(ctx, e.ID, "replacement-host"); err != nil || !claimed {
		t.Fatalf("expired claim not recovered: %t %v", claimed, err)
	}
	if _, err := st.FinalizeWorkflowLifecycle(ctx, e.ID, "cleanup-host", e.Version, nil, nil); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("old owner settled: %v", err)
	}
	after, err = st.FinalizeWorkflowLifecycle(ctx, e.ID, "replacement-host", e.Version, nil, nil)
	if err != nil || after.Status != "failed" || after.Reason != "Run ended as cancelled" {
		t.Fatalf("cancelled pause not settled: %+v %v", after, err)
	}
}

func TestWorkflowLifecycleCleanupCannotFailAnUnexpiredExecution(t *testing.T) {
	st, run, d, _ := workflowDueFixture(t)
	ctx := t.Context()
	e := parkWorkflow(t, st, run, d, "workflowrun-early-cleanup", "running")
	e, claimed, err := st.ClaimWorkflowExecution(ctx, e.ID, "cleanup-host")
	if err != nil || !claimed {
		t.Fatalf("claim: %t %v", claimed, err)
	}
	if _, err := st.ListWorkflowLifecycleSteps(ctx, e.ID, "cleanup-host", e.Version); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("early cleanup read accepted: %v", err)
	}
	if _, err := st.FinalizeWorkflowLifecycle(ctx, e.ID, "cleanup-host", e.Version, nil, nil); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("early cleanup committed: %v", err)
	}
	current, err := st.GetWorkflowExecution(ctx, e.ID)
	if err != nil || current.Status != e.Status || current.Version != e.Version {
		t.Fatalf("rejected cleanup changed execution: %+v %v", current, err)
	}
}

// A host can disappear after recording an attempt but before committing any
// result. Replacing its expired lease must retire that exact attempt, never
// resume its unknown side effects or keep it pending forever.
func TestWorkflowLifecycleRetiresLostAttemptWithoutReplaying(t *testing.T) {
	st, run, d, service := workflowDueFixture(t)
	ctx := t.Context()
	root, _, err := st.RegisterRootAgent(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := st.AdmitSpecialist(ctx, domain.SpecialistAdmission{AgentID: "agent-lost-lifecycle", SessionID: "sess-lost-lifecycle", RunID: run.ID, ParentAgentID: root.ID, Title: "lost worker", Skills: []string{"model.chat"}, TurnLimit: 2, TokenLimit: 100, MaxChildren: 2, CreatedAt: time.Now().UTC()}, "admit-lost-worker")
	if err != nil {
		t.Fatal(err)
	}
	e := parkWorkflow(t, st, run, d, "workflowrun-lost-worker", "running")
	if _, claimed, err := st.ClaimWorkflowExecution(ctx, e.ID, "lost-owner"); err != nil || !claimed {
		t.Fatalf("claim %t %v", claimed, err)
	}
	job, err := st.PrepareWorkflowStep(ctx, domain.WorkflowStepExecution{ID: "workflowstep-lost", ExecutionID: e.ID, StepID: "unreachable", AgentID: child.ID, SessionID: child.SessionID, Inputs: json.RawMessage(`{}`)}, "lost-owner")
	if err != nil {
		t.Fatal(err)
	}
	held, err := st.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "lost-worker-host", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err := st.BeginSpecialistAttempt(ctx, domain.AgentAttemptStart{AttemptID: "attempt-lost-workflow", RunID: run.ID, AgentID: child.ID, ParentAgentID: root.ID, Lease: held.Lease}, "begin-lost-worker")
	if err != nil {
		t.Fatal(err)
	}
	e, err = service.Pause(ctx, e.ID, e.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.ReleaseWorkflowExecution(ctx, e.ID, "lost-owner"); err != nil {
		t.Fatal(err)
	}
	if _, err = st.db.ExecContext(ctx, `UPDATE workflow_executions SET created_at=? WHERE id=?`, ts(time.Now().Add(-2*time.Minute)), e.ID); err != nil {
		t.Fatal(err)
	}
	// A still-valid owner must retain its live attempt, while a second Run
	// behind this expired execution can still finish during the same host tick.
	runs := application.NewRunService(st)
	_, secondRun, err := runs.Create(ctx, application.CreateRunRequest{Goal: "another workflow", Profile: "code", Budget: domain.Budget{MaxTurns: 20, MaxTokens: 10000}})
	if err != nil {
		t.Fatal(err)
	}
	secondRun, err = runs.Start(ctx, secondRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	second := parkWorkflow(t, st, secondRun, d, "workflowrun-after-busy-cleanup", "running")
	if ran, err := service.RunDue(ctx, "fair-host", time.Now().UTC()); err != nil || !ran {
		t.Fatalf("busy cleanup blocked host: %t %v", ran, err)
	}
	second, err = service.Get(ctx, second.ID)
	if err != nil || second.Status != "completed" {
		t.Fatalf("busy expired workflow starved another Run: %+v %v", second, err)
	}
	live, _, err := st.GetAgentAttempt(ctx, attempt.ID)
	if err != nil || live.Status != domain.AgentAttemptRunning {
		t.Fatalf("live owner was retired: %+v %v", live, err)
	}
	if _, err = st.db.ExecContext(ctx, `UPDATE run_execution_leases SET acquired_at=?,renewed_at=?,expires_at=? WHERE run_id=?`, ts(time.Now().Add(-2*time.Minute)), ts(time.Now().Add(-2*time.Minute)), ts(time.Now().Add(-time.Minute)), run.ID); err != nil {
		t.Fatal(err)
	}
	after, err := service.Advance(ctx, e.ID)
	if err != nil || after.Status != "failed" {
		t.Fatalf("lost attempt never settled: %+v %v", after, err)
	}
	ended, _, err := st.GetAgentAttempt(ctx, attempt.ID)
	if err != nil || ended.Status != domain.AgentAttemptInterrupted || ended.Failure.Code != "worker_lost" {
		t.Fatalf("unknown outcome not recorded: %+v %v", ended, err)
	}
	got, _, err := st.GetWorkflowStep(ctx, job.ID)
	if err != nil || got.Status != "failed" {
		t.Fatalf("lost step remained pending: %+v %v", got, err)
	}
	attempts, err := st.ListAgentAttempts(ctx, child.ID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("lost attempt replayed: %+v %v", attempts, err)
	}
	if _, err := st.RestoreAgentGraph(ctx, run.ID); err != nil {
		t.Fatalf("retirement broke attempt projection: %v", err)
	}
}

func TestWorkflowLifecycleTerminalRunBypassesDormantFingerprint(t *testing.T) {
	st, run, d, service := workflowDueFixture(t)
	ctx := t.Context()
	e := parkWorkflow(t, st, run, d, "workflowrun-cancelled-dormant", "waiting")
	e, err := service.Pause(ctx, e.ID, e.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunService(st).Cancel(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	wake, err := st.WorkflowWakeFingerprint(ctx, run.ID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.db.ExecContext(ctx, `UPDATE workflow_executions SET state_json=json_set(state_json,'$.wait_fingerprint',?) WHERE id=?`, wake, e.ID); err != nil {
		t.Fatal(err)
	}
	if ran, err := service.RunDue(ctx, "terminal-host", time.Now().UTC()); err != nil || !ran {
		t.Fatalf("terminal Run was hidden by dormant fingerprint: %t %v", ran, err)
	}
	e, err = service.Get(ctx, e.ID)
	if err != nil || e.Status != "failed" || e.Reason != "Run ended as cancelled" {
		t.Fatalf("cancelled pause remained dormant: %+v %v", e, err)
	}
}
