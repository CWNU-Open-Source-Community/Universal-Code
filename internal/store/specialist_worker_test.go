package store

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/toolbudget"
)

func workerToolFixture(t *testing.T, st *SQLiteStore, profile *domain.WorkerProfile, native ...llm.ToolCall) (domain.Run, domain.AgentNode, domain.AgentAttempt, domain.SpecialistToolBinding, domain.SpecialistToolCall, domain.RunExecutionLease) {
	t.Helper()
	ctx := t.Context()
	_, run := createWorkItemTestRun(t, ctx, st, "worker tool source")
	if _, err := application.NewRunService(st).Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	root, _, err := st.GetRootAgent(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	admission := domain.SpecialistAdmission{AgentID: idgen.New("agent"), SessionID: idgen.New("sess"), RunID: run.ID, ParentAgentID: root.ID, Title: "worker", Skills: []string{"model.chat"}, TurnLimit: 2, TokenLimit: 128, ToolCallLimit: 2, CostLimitMicros: 700, MaxChildren: 2, CreatedAt: time.Now().UTC(), Worker: profile}
	mode, name, payload := "worker", "command_runtime", `{"version":"command-runtime.v2","action":"list"}`
	if profile == nil {
		admission.AllowedReadPaths = []string{"."}
		mode, name, payload = "read", "workspace_read", `{"version":"agent-code-tools.v1","path":"proof.txt","start_line":1,"end_line":2}`
	}
	if len(native) == 1 {
		name, payload = native[0].Name, string(native[0].Arguments)
	}
	child, _, err := st.AdmitSpecialist(ctx, admission, "worker-admission-operation")
	if err != nil {
		t.Fatal(err)
	}
	if !domain.EqualWorkerProfiles(child.Worker, profile) {
		t.Fatalf("admission lost worker: %+v", child)
	}
	lease := acquireTestRunExecutionLease(t, ctx, st, run.ID)
	a, _, err := st.BeginSpecialistAttempt(ctx, domain.AgentAttemptStart{AttemptID: idgen.New("attempt"), RunID: run.ID, AgentID: child.ID, ParentAgentID: root.ID, Lease: lease, StartedAt: time.Now().UTC()}, "worker-attempt-operation")
	if err != nil {
		t.Fatal(err)
	}
	ref := attemptRef(a)
	brief, err := st.PrepareSpecialistContext(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	mission, err := st.GetMission(ctx, run.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	b := domain.SpecialistToolBinding{Ref: ref, SessionID: child.SessionID, WorkspaceID: mission.WorkspaceID, RootFingerprint: strings.Repeat("a", 64), BriefFingerprint: brief.TaskBrief.Fingerprint, Mode: mode}
	if profile != nil {
		b.WorkerProfileJSON, _ = domain.WorkerProfileJSON(profile)
	} else {
		b.AllowedReadPathsJSON = `["."]`
	}
	if err = st.BindSpecialistTools(ctx, b); err != nil {
		t.Fatal(err)
	}
	m := llm.ModelAttempt{Number: 1, TransportAttempt: 1, MaxAttempts: 1, Provider: "mock", Model: "model", Context: &llm.ModelContextAudit{TokenBudget: 1, EstimatedTokens: 1, Included: []llm.ModelContextSource{{Kind: "specialist_task_brief", SourceID: brief.TaskBrief.Fingerprint, Tokens: 1}}}}
	if _, err = st.RecordSpecialistModelStarted(ctx, ref, m); err != nil {
		t.Fatal(err)
	}
	response := llm.ChatResponse{ToolCalls: []llm.ToolCall{{ID: "same-provider-call", Name: name, Arguments: json.RawMessage(payload)}}, Usage: llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4}}
	if _, err = st.RecordSpecialistToolModelCompleted(ctx, ref, m, response, "worker input", policy.Decision{Allowed: true, Risk: "low", Reason: "allowed"}); err != nil {
		t.Fatal(err)
	}
	c, err := st.GetSpecialistToolCall(ctx, ref, "same-provider-call")
	if err != nil {
		t.Fatal(err)
	}
	return run, child, a, b, c, lease
}

func TestSpecialistWorkerAuthorityApprovalAndSameAttemptResume(t *testing.T) {
	st := openWorkItemTestStore(t)
	run, child, a, b, c, lease := workerToolFixture(t, st, &domain.WorkerProfile{})
	ctx := t.Context()
	ref := attemptRef(a)
	permission, err := st.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	authority, _ := json.Marshal(map[string]any{"permission_snapshot_id": permission.ID, "permission_revision": permission.Revision, "permission_mode": permission.Mode})
	c, err = st.BindSpecialistToolAuthority(ctx, ref, c.CallID, string(authority))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.BindSpecialistToolAuthority(ctx, ref, c.CallID, `{"permission":"changed"}`); err == nil {
		t.Fatal("authority changed after pinning")
	}
	source := domain.SpecialistToolSource{Ref: ref, CallID: c.CallID, SessionID: child.SessionID, WorkspaceID: b.WorkspaceID, Name: c.Name, PayloadJSON: c.PayloadJSON, AuthorityJSON: c.AuthorityJSON, LeaseID: lease.LeaseID, LeaseGeneration: lease.Generation}
	if _, err = st.ValidateSpecialistToolSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	forged := source
	forged.SessionID = run.SessionID
	if _, err = st.ValidateSpecialistToolSource(ctx, forged); err == nil {
		t.Fatal("child used root Session")
	}
	p := approval.Proposal{IdempotencyKey: "worker-approval-key", ProposalID: c.ApprovalSourceID, SessionID: child.SessionID, WorkspaceID: b.WorkspaceID, ToolName: "command_runtime", ActionClass: "command_process", Mode: "per_call", Status: approval.StatusPending, RequestFingerprint: domain.SpecialistToolApprovalFingerprint(c), RequestedBy: "specialist_runner"}
	review, err := st.EnsureApproval(ctx, p)
	if err != nil || review.RunID != run.ID {
		t.Fatalf("child approval binding: %+v %v", review, err)
	}
	if _, err = st.SetSpecialistToolApproval(ctx, ref, c.CallID, review.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.ReleaseRunExecutionLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	newLease := acquireTestRunExecutionLease(t, ctx, st, run.ID)
	recovered, err := st.RecoverSpecialistAttempts(ctx, newLease)
	if err != nil || len(recovered) != 0 {
		t.Fatalf("approval wait was recovered as worker lost: %+v %v", recovered, err)
	}
	if _, err = st.ResumeSpecialistToolAttempt(ctx, ref, newLease); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("pending approval resumed: %v", err)
	}
	if _, err = st.DecideApproval(ctx, approval.DecisionRequest{ProposalID: c.ApprovalSourceID, IdempotencyKey: "worker-review-key", Action: approval.ActionApprove, ReviewedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	resumed, err := st.ResumeSpecialistToolAttempt(ctx, ref, newLease)
	if err != nil || resumed.ID != a.ID || resumed.Turn != a.Turn || resumed.Usage.TotalTokens != 4 {
		t.Fatalf("resume changed attempt: %+v %v", resumed, err)
	}
	if _, err = st.ValidateSpecialistToolSource(ctx, source); err == nil {
		t.Fatal("old lease dispatched after resume")
	}
	charge, err := st.ChargeToolCall(ctx, toolbudget.ChargeRequest{RunID: run.ID, AgentID: child.ID, AgentAttemptID: a.ID, ToolCallID: c.CallID, SessionID: child.SessionID, WorkspaceID: b.WorkspaceID, ToolName: c.Name, ActionClass: "command_process", LeaseID: newLease.LeaseID, LeaseGeneration: newLease.Generation, RequestedBy: "specialist_runner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.MarkSpecialistToolStarted(ctx, ref, c.CallID, c.AuthorityJSON); err != nil {
		t.Fatal(err)
	}
	source.LeaseID = newLease.LeaseID
	source.LeaseGeneration = newLease.Generation
	source.ChargeID = charge.LastCharge
	source.RequireStarted = true
	if _, err = st.ValidateSpecialistToolSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	if _, err = st.RevokeSpecialistReadTools(ctx, run.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = st.ValidateSpecialistToolSource(ctx, source); err == nil {
		t.Fatal("revoked worker dispatched")
	}
}

func TestSchemaV202PreservesLegacySpecialistToolReceipts(t *testing.T) {
	st := openWorkItemTestStore(t)
	_, _, a, _, _, _ := workerToolFixture(t, st, nil)
	legacyPath := filepath.Join(t.TempDir(), "worker-v201.db")
	legacy := historicalTestDatabaseFromSeed(t, st, legacyPath, 201)
	legacy.Close()
	upgraded, err := Open(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	rows, err := upgraded.ListSpecialistToolRounds(t.Context(), attemptRef(a))
	if err != nil || len(rows) != 1 || len(rows[0].Calls) != 1 || rows[0].Calls[0].Name != "workspace_read" || rows[0].Calls[0].StartedAt != nil {
		t.Fatalf("legacy tool receipt changed: %+v %v", rows, err)
	}
	node, err := upgraded.GetAgentNode(t.Context(), a.AgentID)
	if err != nil || node.Worker != nil {
		t.Fatalf("legacy child gained worker tools: %+v %v", node, err)
	}
	assertNoForeignKeyViolations(t, upgraded.db)
}
