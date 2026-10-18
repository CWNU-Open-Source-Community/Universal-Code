package application

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolbudget"
	"cyberagent-workbench/internal/toolgateway"
)

func workerHistoryExecutionFixture(t *testing.T) (*workerCommandTestActor, *HistoryRecallToolExecutor,
	domain.SpecialistToolCall, toolgateway.ToolCall, domain.ExecutionPermissionRuntimeCapabilities) {
	t.Helper()
	st, run, root, lease, caps := newCommandRuntimeTestRuntime(t, t.Context())
	actor := newWorkerCommandTestActor(t, st, &CommandRuntimeService{}, run, root, lease, &domain.WorkerProfile{})
	runtime := NewAgentRunner(st, nil, policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(caps)
	authority, err := runtime.workerHistoryToolAuthority(t.Context(), actor.ref)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := toolgateway.DecodeWorkerHistoryAuthority(authority)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := toolgateway.NormalizeHistoryRecallPayload(toolgateway.HistorySearchTool, json.RawMessage(`{"query":"own recalled text"}`))
	if err != nil {
		t.Fatal(err)
	}
	model := llm.ModelAttempt{Number: 1, TransportAttempt: 1, MaxAttempts: 1, Provider: "worker-history-fixture", Model: "fixture"}
	if _, err := st.RecordSpecialistModelStarted(t.Context(), actor.ref, model); err != nil {
		t.Fatal(err)
	}
	model.Elapsed, model.Outcome = time.Millisecond, llm.OutcomeSuccess
	callID := idgen.New("history-call")
	response := llm.ChatResponse{Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
		ToolCalls: []llm.ToolCall{{ID: callID, Name: string(toolgateway.HistorySearchTool), Arguments: payload}}}
	if _, err := st.RecordSpecialistToolModelCompleted(t.Context(), actor.ref, model, response, "execute the authenticated worker brief",
		policy.Decision{Allowed: true, Risk: "low", Reason: "fixture model output"}); err != nil {
		t.Fatal(err)
	}
	stored, err := st.BindSpecialistToolAuthority(t.Context(), actor.ref, callID, string(authority))
	if err != nil {
		t.Fatal(err)
	}
	charge, err := st.ChargeToolCall(t.Context(), toolbudget.ChargeRequest{RunID: run.ID, AgentID: actor.child.ID,
		AgentAttemptID: actor.ref.AttemptID, ToolCallID: callID, SessionID: actor.child.SessionID, WorkspaceID: bound.WorkspaceID,
		ToolName: stored.Name, ActionClass: "run_memory", LeaseID: lease.LeaseID, LeaseGeneration: lease.Generation, RequestedBy: "specialist_runner"})
	if err != nil {
		t.Fatal(err)
	}
	call := toolgateway.ToolCall{Name: toolgateway.HistorySearchTool, Payload: payload, OperationKey: stored.ApprovalSourceID,
		RunID: run.ID, MissionID: run.MissionID, AgentID: actor.child.ID, RootAgentID: root.ID, AgentAttemptID: actor.ref.AttemptID,
		SessionID: actor.child.SessionID, WorkspaceID: bound.WorkspaceID, SpecialistToolCallID: callID, InvocationID: charge.LastCharge,
		Role: domain.AgentRoleSpecialist, RequestedBy: "specialist_runner", LeaseID: lease.LeaseID, LeaseGeneration: lease.Generation,
		PermissionMode: bound.PermissionMode, PermissionRevision: bound.PermissionRevision, PermissionSnapshotID: bound.PermissionSnapshotID,
		PermissionGeneration: bound.PermissionGeneration, PermissionRuntimeEpoch: bound.PermissionRuntimeEpoch, RunAuthorizationFence: bound.RunAuthorizationFence}
	if _, err := st.SaveSessionMessage(t.Context(), session.NewMessage(actor.child.SessionID, "user", "own recalled text")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, "user", "parent private recalled text")); err != nil {
		t.Fatal(err)
	}
	return actor, NewHistoryRecallToolExecutor(st).WithExecutionPermissionCapabilities(caps), stored, call, caps
}

func TestWorkerHistoryExecutorRequiresExactStartedChargedCall(t *testing.T) {
	actor, executor, stored, call, caps := workerHistoryExecutionFixture(t)
	if _, _, err := executor.ExecuteHistoryRecall(t.Context(), call); err == nil {
		t.Fatal("uncharged/unstarted call read history")
	}
	if _, err := actor.store.MarkSpecialistToolStarted(t.Context(), actor.ref, stored.CallID, stored.AuthorityJSON); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*toolgateway.ToolCall){
		func(c *toolgateway.ToolCall) { c.SessionID = actor.run.SessionID },
		func(c *toolgateway.ToolCall) { c.RootAgentID = c.AgentID },
		func(c *toolgateway.ToolCall) { c.AgentID = actor.root.ID },
		func(c *toolgateway.ToolCall) { c.AgentAttemptID = "other-attempt" },
		func(c *toolgateway.ToolCall) { c.PermissionGeneration++ },
		func(c *toolgateway.ToolCall) { c.RunAuthorizationFence++ },
		func(c *toolgateway.ToolCall) { c.InvocationID = "foreign-charge" },
		func(c *toolgateway.ToolCall) { c.Payload = json.RawMessage(`{"query":"parent private recalled text"}`) },
	} {
		forged := call
		mutate(&forged)
		if raw, _, err := executor.ExecuteHistoryRecall(t.Context(), forged); err == nil || len(raw) != 0 {
			t.Fatalf("forged worker scope returned history: %s %v", raw, err)
		}
	}
	raw, _, err := executor.ExecuteHistoryRecall(t.Context(), call)
	var result domain.HistorySearchResult
	if err != nil || json.Unmarshal(raw, &result) != nil || len(result.Records) == 0 {
		t.Fatalf("worker search failed: %s %v", raw, err)
	}
	foundUser := false
	for _, record := range result.Records {
		if record.SessionID != actor.child.SessionID {
			t.Fatalf("worker search crossed child Session: %+v", record)
		}
		foundUser = foundUser || record.Role == "user" && record.Excerpt == "own recalled text"
	}
	if !foundUser {
		t.Fatal("worker search missed its exact stored user message")
	}
	caps.RuntimeAuthority.RevokeRun(actor.run.ID)
	if raw, _, err := executor.ExecuteHistoryRecall(t.Context(), call); err == nil || len(raw) != 0 {
		t.Fatalf("runtime revoke returned history: %s %v", raw, err)
	}
}

type workerHistoryRevokingStore struct {
	*store.SQLiteStore
	revoke func()
}

func (s workerHistoryRevokingStore) SearchWorkerHistory(ctx context.Context, ref domain.AgentAttemptRef,
	request domain.HistorySearchRequest) (domain.HistorySearchResult, error) {
	value, err := s.SQLiteStore.SearchWorkerHistory(ctx, ref, request)
	s.revoke()
	return value, err
}

func TestWorkerHistoryExecutorRevalidatesBeforeDeliveringResult(t *testing.T) {
	actor, executor, stored, call, caps := workerHistoryExecutionFixture(t)
	if _, err := actor.store.MarkSpecialistToolStarted(t.Context(), actor.ref, stored.CallID, stored.AuthorityJSON); err != nil {
		t.Fatal(err)
	}
	executor.store = workerHistoryRevokingStore{SQLiteStore: actor.store, revoke: func() { caps.RuntimeAuthority.RevokeRun(actor.run.ID) }}
	if raw, _, err := executor.ExecuteHistoryRecall(t.Context(), call); err == nil || len(raw) != 0 {
		t.Fatalf("history delivered after authority revoked during read: %s %v", raw, err)
	}
}
