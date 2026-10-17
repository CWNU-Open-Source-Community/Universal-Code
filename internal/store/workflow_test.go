package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

func TestWorkflowClaimsPauseAndStableStartProtectDispatch(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "workflow-claims.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	runs := application.NewRunService(st)
	_, run, err := runs.Create(t.Context(), application.CreateRunRequest{Goal: "workflow claim test", Profile: "code", Budget: domain.Budget{MaxTurns: 20, MaxTokens: 10000}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := st.SaveWorkflowDefinition(t.Context(), domain.WorkflowSpec{Version: domain.WorkflowVersion, Name: "claims", Limits: domain.WorkflowLimits{MaxSteps: 4, MaxJobs: 2, MaxTokens: 1000, MaxTurns: 4, TimeoutMillis: 60000}, Steps: []domain.WorkflowStep{{ID: "read", Kind: "worker", Task: &domain.ChildTask{Title: "read", Goal: "read a file", Skills: []string{"model.chat"}, TurnLimit: 1, TokenLimit: 100, TimeoutMillis: 10000}}}})
	if err != nil {
		t.Fatal(err)
	}
	input := domain.WorkflowExecution{ID: "workflowrun-claims", DefinitionID: d.ID, RunID: run.ID, Inputs: json.RawMessage(`{"a":1}`)}
	e, err := st.StartWorkflowExecution(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.Inputs = json.RawMessage(`{"a":2}`)
	if _, err = st.StartWorkflowExecution(t.Context(), input); err == nil {
		t.Fatal("start key accepted changed input")
	}
	claimed, ok, err := st.ClaimWorkflowExecution(t.Context(), e.ID, "owner-a")
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if _, ok, err = st.ClaimWorkflowExecution(t.Context(), e.ID, "owner-b"); err != nil || ok {
		t.Fatalf("second owner acquired live claim: %v %v", ok, err)
	}
	paused, err := st.ControlWorkflowExecution(t.Context(), e.ID, "pause", claimed.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.CheckpointWorkflowExecution(t.Context(), claimed, "owner-a"); err == nil {
		t.Fatal("stale checkpoint overwrote pause")
	}
	if _, err = st.PrepareWorkflowStep(t.Context(), domain.WorkflowStepExecution{ID: "step-a", ExecutionID: e.ID, StepID: "read", AgentID: "agent-a", SessionID: "session-a", Inputs: json.RawMessage(`{}`)}, "owner-a"); err == nil {
		t.Fatal("paused workflow dispatched a step")
	}
	if _, err = st.ControlWorkflowExecution(t.Context(), e.ID, "resume", claimed.Version); err == nil {
		t.Fatal("stale resume revision accepted")
	}
	if _, err = st.ControlWorkflowExecution(t.Context(), e.ID, "resume", paused.Version); err != nil {
		t.Fatal(err)
	}
	if _, err = st.db.Exec(`UPDATE workflow_executions SET claim_until=? WHERE id=?`, ts(time.Now().Add(-time.Minute)), e.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err = st.ClaimWorkflowExecution(t.Context(), e.ID, "owner-b"); err != nil || !ok {
		t.Fatalf("expired claim not recovered: %v %v", ok, err)
	}
	if _, err = st.PrepareWorkflowStep(t.Context(), domain.WorkflowStepExecution{ID: "step-a", ExecutionID: e.ID, StepID: "read", AgentID: "agent-a", SessionID: "session-a", Inputs: json.RawMessage(`{}`)}, "owner-a"); err == nil {
		t.Fatal("stale owner dispatched after recovery")
	}
}

func TestSchemaV203PreservesHistoricalAgentSnapshots(t *testing.T) {
	st := openHistoricalTestDatabase(t, filepath.Join(t.TempDir(), "workflow-migration.db"), 202)
	defer st.Close()
	runs := application.NewRunService(st)
	_, run, err := runs.Create(t.Context(), application.CreateRunRequest{Goal: "migration snapshot", Profile: "code", Budget: domain.Budget{MaxTurns: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runs.Start(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.RegisterRootAgent(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	before, err := st.SnapshotAgentGraph(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.applyMigration(t.Context(), migrationPlan()[202]); err != nil {
		t.Fatal(err)
	}
	after, found, err := st.GetLatestAgentGraphSnapshot(t.Context(), run.ID)
	if err != nil || !found || after.ID != before.ID || after.StateJSON != before.StateJSON {
		t.Fatalf("snapshot changed during migration: %#v %v", after, err)
	}
	assertNoForeignKeyViolations(t, st.db)
}

func TestWorkflowFailureReleasesIdleWorkerAndKeepsRecoverableGraph(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "workflow-failure.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	runs := application.NewRunService(st)
	_, run, err := runs.Create(t.Context(), application.CreateRunRequest{Goal: "workflow failure cleanup", Profile: "code", Budget: domain.Budget{MaxTurns: 10, MaxTokens: 10000}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runs.Start(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	root, _, err := st.RegisterRootAgent(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := st.AdmitSpecialist(t.Context(), domain.SpecialistAdmission{AgentID: "agent-workflow-failed", SessionID: "sess-workflow-failed", RunID: run.ID, ParentAgentID: root.ID, Title: "idle worker", Skills: []string{"model.chat"}, TurnLimit: 8, TokenLimit: 9000, MaxChildren: 2, CreatedAt: time.Now().UTC()}, "workflow-failure-admit")
	if err != nil {
		t.Fatal(err)
	}
	d, err := st.SaveWorkflowDefinition(t.Context(), domain.WorkflowSpec{Version: domain.WorkflowVersion, Name: "failure", Limits: domain.WorkflowLimits{MaxSteps: 4, MaxJobs: 2, MaxTokens: 1000, MaxTurns: 4, TimeoutMillis: 60000}, Steps: []domain.WorkflowStep{{ID: "read", Kind: "worker", Task: &domain.ChildTask{Title: "read", Goal: "read", Skills: []string{"model.chat"}, TurnLimit: 1, TokenLimit: 100, TimeoutMillis: 10000}}}})
	if err != nil {
		t.Fatal(err)
	}
	e, err := st.StartWorkflowExecution(t.Context(), domain.WorkflowExecution{ID: "workflowrun-failed", DefinitionID: d.ID, RunID: run.ID, Inputs: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.ClaimWorkflowExecution(t.Context(), e.ID, "owner"); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	job, err := st.PrepareWorkflowStep(t.Context(), domain.WorkflowStepExecution{ID: "workflowstep-failed", ExecutionID: e.ID, StepID: "read", AgentID: child.ID, SessionID: child.SessionID, Inputs: json.RawMessage(`{}`)}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	job.Status, job.Reason, job.Output = "failed", "workflow deadline exceeded", json.RawMessage(`null`)
	if err := st.CompleteWorkflowStep(t.Context(), job, "owner"); err != nil {
		t.Fatal(err)
	}
	updated, err := st.GetAgentNode(t.Context(), child.ID)
	if err != nil || updated.Status != domain.AgentCancelled {
		t.Fatalf("idle child retained reservation: %#v %v", updated, err)
	}
	if _, err := st.RestoreAgentGraph(t.Context(), run.ID); err != nil {
		t.Fatalf("cleanup broke recovery: %v", err)
	}
	if _, _, err := st.AdmitSpecialist(t.Context(), domain.SpecialistAdmission{AgentID: "agent-workflow-next", SessionID: "sess-workflow-next", RunID: run.ID, ParentAgentID: root.ID, Title: "next worker", Skills: []string{"model.chat"}, TurnLimit: 8, TokenLimit: 9000, MaxChildren: 2, CreatedAt: time.Now().UTC()}, "workflow-next-admit"); err != nil {
		t.Fatalf("failed child held unused reservation: %v", err)
	}
}
