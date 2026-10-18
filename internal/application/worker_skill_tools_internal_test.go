package application

import (
	"encoding/json"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/toolgateway"
)

func TestWorkerSkillDispatchRequiresDurableIntentAndDoesNotRepeatUnknownOutcome(t *testing.T) {
	st, run, root, lease, caps := newCommandRuntimeTestRuntime(t, t.Context())
	worker := newWorkerCommandTestActor(t, st, &CommandRuntimeService{}, run, root, lease,
		&domain.WorkerProfile{Tools: []string{"skill_read"}})
	runtime := NewAgentRunner(st, nil, policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(caps)
	mode, err := st.GetRunMode(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	entry, available, err := runtime.workerSkillToolEntry(t.Context(), worker.ref, domain.SupervisorTurn{Run: run, Agent: root, Mode: mode})
	if err != nil || !available {
		t.Fatalf("worker Skill capability: %t %v", available, err)
	}
	attempt, found, err := st.GetAgentAttempt(t.Context(), worker.ref.AttemptID)
	if err != nil || !found {
		t.Fatal(err)
	}
	mission, err := st.GetMission(t.Context(), run.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"catalog":true}`)
	intent := domain.SpecialistToolCall{Ref: worker.ref, CallID: "worker-discovery", Name: "skill_read", PayloadJSON: string(payload), AuthorityJSON: string(entry.authority)}
	call, err := workerGatewayCall(intent, run, mission, worker.child, attempt)
	if err != nil {
		t.Fatal(err)
	}
	call.Surface, call.Phase, call.Profile, call.ModeRevision = mode.Surface, mode.Phase, mode.Profile, mode.Revision
	if _, err := runtime.tools.Invoke(t.Context(), call); err == nil {
		t.Fatal("fabricated Skill intent reached the reader")
	}
	model := llm.ModelAttempt{Number: 1, TransportAttempt: 1, MaxAttempts: 1, Provider: "worker-skill-fixture", Model: "fixture"}
	if _, err := st.RecordSpecialistModelStarted(t.Context(), worker.ref, model); err != nil {
		t.Fatal(err)
	}
	model.Elapsed, model.Outcome = time.Millisecond, llm.OutcomeSuccess
	if _, err := st.RecordSpecialistToolModelCompleted(t.Context(), worker.ref, model,
		llm.ChatResponse{ToolCalls: []llm.ToolCall{{ID: intent.CallID, Name: intent.Name, Arguments: payload}}},
		"read the relevant workflow", policy.Decision{Allowed: true, Risk: "low", Reason: "controlled provider output"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BindSpecialistToolAuthority(t.Context(), worker.ref, intent.CallID, intent.AuthorityJSON); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*toolgateway.ToolCall){
		func(c *toolgateway.ToolCall) { c.AgentAttemptID = "stale-worker-attempt" },
		func(c *toolgateway.ToolCall) { c.LeaseGeneration++ },
		func(c *toolgateway.ToolCall) {
			c.Payload = json.RawMessage(`{"catalog":true,"offset":1,"catalog_revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
		},
	} {
		forged := call
		mutate(&forged)
		if _, err := runtime.tools.Invoke(t.Context(), forged); err == nil {
			t.Fatal("changed worker Skill source reached the reader")
		}
	}
	// The original exact call still works after rejected attempts.
	outcome, err := runtime.tools.Invoke(t.Context(), call)
	if err != nil || outcome.Result == nil || outcome.Result.Status != toolgateway.StatusCompleted {
		t.Fatalf("exact native discovery: %+v %v", outcome, err)
	}
	before, err := st.GetAgentNode(t.Context(), worker.child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.tools.Invoke(t.Context(), call); err == nil {
		t.Fatal("started Skill call without a durable result was repeated")
	}
	after, err := st.GetAgentNode(t.Context(), worker.child.ID)
	if err != nil || before.ToolCallsUsed != 1 || after.ToolCallsUsed != before.ToolCallsUsed {
		t.Fatalf("Skill dispatch charging drifted: before=%d after=%d err=%v", before.ToolCallsUsed, after.ToolCallsUsed, err)
	}
	reads, err := st.ListWorkerSkillReadCalls(t.Context(), worker.ref)
	if err != nil || len(reads) != 0 {
		t.Fatalf("unknown result activated Skill guidance: %+v %v", reads, err)
	}
}
