package application

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/toolgateway"
)

type batchWorkerToolFixture struct {
	batchDeliveryApplicationFixture
	runner   *SubagentRunner
	executor *specialistWorkspaceExecutor
	gateway  *toolgateway.Gateway
	mission  domain.Mission
	child    domain.AgentNode
	attempt  domain.AgentAttempt
}

func newBatchWorkerToolFixture(t *testing.T, profile *domain.WorkerProfile) batchWorkerToolFixture {
	t.Helper()
	f := batchWorkerToolFixture{batchDeliveryApplicationFixture: newBatchDeliveryApplicationFixtureWithWorker(t, false, domain.RunExecutionPermissionFull, profile)}
	_, work, _ := batchModelWorkerFixture(t, f.batchDeliveryApplicationFixture, &batchEditTestProvider{}, f.store)
	lease, err := f.store.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: f.run.ID, OwnerID: "batch-worker-tool-test", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	f.attempt, _, err = f.store.BeginSpecialistAttempt(t.Context(), domain.AgentAttemptStart{AttemptID: "batch-tool-attempt", RunID: f.run.ID, AgentID: work.Workspace.AgentID, ParentAgentID: f.root.ID, Lease: lease.Lease, StartedAt: time.Now().UTC()}, "batch-tool-start")
	if err != nil {
		t.Fatal(err)
	}
	brief, err := f.store.PrepareSpecialistContext(t.Context(), specialistAttemptRef(f.attempt))
	if err != nil {
		t.Fatal(err)
	}
	f.child, err = f.store.GetAgentNode(t.Context(), work.Workspace.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	f.mission, err = f.store.GetMission(t.Context(), f.run.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	checker := policy.NewDefaultChecker()
	runtime := NewAgentRunner(f.store, nil, checker).WithExecutionPermissionCapabilities(f.caps)
	f.runner = NewSubagentRunner(f.store, nil, checker).WithBatchTools(f.service, work.Authority).WithWorkerRuntime(runtime)
	f.executor, f.gateway, err = f.runner.prepareToolExecutor(t.Context(), f.run, f.mission, f.child, f.attempt, brief.TaskBrief)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f batchWorkerToolFixture) record(t *testing.T, calls ...llm.ToolCall) error {
	t.Helper()
	ref := specialistAttemptRef(f.attempt)
	model := llm.ModelAttempt{Number: 1, TransportAttempt: 1, MaxAttempts: 1, Provider: "batch-worker-test", Model: "model"}
	if _, err := f.store.RecordSpecialistModelStarted(t.Context(), ref, model); err != nil {
		t.Fatal(err)
	}
	model.Elapsed, model.Outcome = time.Millisecond, llm.OutcomeSuccess
	_, err := f.store.RecordSpecialistToolModelCompleted(t.Context(), ref, model, llm.ChatResponse{ToolCalls: calls}, "use the owned Batch task", policy.Decision{Allowed: true, Risk: "low", Reason: "controlled provider response"})
	return err
}

func TestBatchWorkerDispatchKeepsOwnedPathsAndRejectsUnadvertisedMutation(t *testing.T) {
	f := newBatchWorkerToolFixture(t, &domain.WorkerProfile{})
	deletePayload, _ := json.Marshal(toolgateway.WorkspaceDeletePayload{Version: toolgateway.AgentCodeRegistryVersion, Action: "propose", Path: "internal/two/base.txt", ConfirmPath: "internal/two/base.txt", ExpectedSHA256: fileedit.HashText("base\n")})
	if err := f.record(t,
		llm.ToolCall{ID: "foreign-read", Name: "workspace_read", Arguments: json.RawMessage(`{"version":"agent-code-tools.v1","path":"internal/two/base.txt","start_line":1,"end_line":4}`)},
		llm.ToolCall{ID: "foreign-write", Name: "workspace_change", Arguments: json.RawMessage(`{"version":"agent-code-tools.v1","action":"create","path":"internal/two/foreign.txt","expected_sha256":"missing","content":"foreign"}`)},
		llm.ToolCall{ID: "foreign-delete", Name: "workspace_delete", Arguments: deletePayload}); err != nil {
		t.Fatal(err)
	}
	ref := specialistAttemptRef(f.attempt)
	rounds, err := f.store.ListSpecialistToolRounds(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	var result SpecialistTurnResult
	if waiting, err := f.runner.resolveWorkerTools(t.Context(), &result, f.run, f.mission, f.child, f.attempt, rounds, f.executor, f.gateway); err != nil || waiting {
		t.Fatalf("scope resolution: waiting=%t err=%v", waiting, err)
	}
	rounds, err = f.store.ListSpecialistToolRounds(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range rounds[0].Calls {
		if call.Status != domain.SupervisorToolFailed {
			t.Fatalf("out-of-scope operation succeeded: %+v", call)
		}
		if call.Name == "workspace_delete" && (call.AuthorityJSON != "" || call.ChargeID != "" || !strings.Contains(call.ResultJSON, "delegated catalogue")) {
			t.Fatalf("unadvertised root mutation obtained authority: %+v", call)
		}
	}
	if _, err := os.Stat(filepath.Join(f.executor.root, "internal", "two", "foreign.txt")); !os.IsNotExist(err) {
		t.Fatalf("out-of-scope file was written: %v", err)
	}
}

func TestBatchWorkerProfileNarrowingAppliesToCatalogueAndDurableIntent(t *testing.T) {
	for _, tools := range [][]string{{"workspace_read"}, {}} {
		t.Run(strings.Join(tools, "+"), func(t *testing.T) {
			f := newBatchWorkerToolFixture(t, &domain.WorkerProfile{Tools: tools})
			catalogue, err := f.runner.workerRuntime.workerToolCatalogue(t.Context(), specialistAttemptRef(f.attempt))
			if err != nil {
				t.Fatal(err)
			}
			catalogue = f.runner.batchWorkerCatalogue(catalogue, f.child)
			if len(catalogue.tools) != len(tools) {
				t.Fatalf("narrowed worker catalogue: %+v", catalogue.specs())
			}
			for _, name := range tools {
				if _, ok := catalogue.tools[name]; !ok {
					t.Fatalf("allowed tool missing: %s", name)
				}
			}
			err = f.record(t, llm.ToolCall{ID: "forbidden-write", Name: "workspace_change", Arguments: json.RawMessage(`{"version":"agent-code-tools.v1","action":"create","path":"internal/one/forbidden.txt","expected_sha256":"missing","content":"forbidden"}`)})
			if apperror.CodeOf(err) != apperror.CodePolicyDenied {
				t.Fatalf("profile narrowing did not reject provider intent: %v", err)
			}
		})
	}
}

func TestBatchWorkerNativeDispatchRequiresExactAttemptAndDoesNotRepeatUnknown(t *testing.T) {
	f := newBatchWorkerToolFixture(t, &domain.WorkerProfile{})
	if err := f.record(t, llm.ToolCall{ID: "owned-read", Name: "workspace_read", Arguments: json.RawMessage(`{"version":"agent-code-tools.v1","path":"internal/one/base.txt","start_line":1,"end_line":4}`)}); err != nil {
		t.Fatal(err)
	}
	ref := specialistAttemptRef(f.attempt)
	authority, err := workerBatchAuthority(f.executor.binding)
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.store.BindSpecialistToolAuthority(t.Context(), ref, "owned-read", authority)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(toolgateway.SpecialistToolPayload{Tool: c.Name, Arguments: json.RawMessage(c.PayloadJSON)})
	call := toolgateway.ToolCall{Name: toolgateway.SpecialistTool, Payload: payload, RunID: f.run.ID, MissionID: f.mission.ID, AgentID: f.child.ID, AgentAttemptID: f.attempt.ID, SpecialistToolCallID: c.CallID, SessionID: f.child.SessionID, WorkspaceID: f.executor.binding.WorkspaceID, Role: domain.AgentRoleSpecialist, RequestedBy: "specialist_runner", LeaseID: f.attempt.LeaseID, LeaseGeneration: f.attempt.LeaseGeneration, OperationKey: "batch-worker-exact-call"}
	for _, change := range []func(*toolgateway.ToolCall){
		func(c *toolgateway.ToolCall) { c.AgentAttemptID = "stale-batch-attempt" },
		func(c *toolgateway.ToolCall) { c.LeaseGeneration++ },
		func(c *toolgateway.ToolCall) { c.SpecialistToolCallID = "fabricated-batch-call" },
	} {
		forged := call
		change(&forged)
		if _, err := f.gateway.Invoke(t.Context(), forged); err == nil {
			t.Fatal("changed dispatch source reached the Batch tool")
		}
	}
	outcome, err := f.gateway.Invoke(t.Context(), call)
	if err != nil || outcome.Result == nil || !strings.Contains(outcome.Result.Stdout, "base") {
		t.Fatalf("exact Batch call: %+v %v", outcome, err)
	}
	// Simulate loss of the result after dispatch but before its durable receipt.
	if _, err := f.gateway.Invoke(t.Context(), call); err == nil {
		t.Fatal("started Batch call without a durable result was dispatched again")
	}
	rounds, err := f.store.ListSpecialistToolRounds(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	var result SpecialistTurnResult
	if waiting, err := f.runner.resolveWorkerTools(t.Context(), &result, f.run, f.mission, f.child, f.attempt, rounds, f.executor, f.gateway); err != nil || waiting {
		t.Fatalf("unknown result recovery: waiting=%t err=%v", waiting, err)
	}
	resolved, err := f.store.GetSpecialistToolCall(t.Context(), ref, c.CallID)
	if err != nil || resolved.Status != domain.SupervisorToolFailed || resolved.ErrorCode != "outcome_unknown" || resolved.ChargeID == "" {
		t.Fatalf("unknown dispatch was not fenced: %+v %v", resolved, err)
	}
	child, err := f.store.GetAgentNode(t.Context(), f.child.ID)
	if err != nil || child.ToolCallsUsed != 1 {
		t.Fatalf("unknown replay charged again: %+v %v", child, err)
	}
}
