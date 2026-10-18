package application

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/codeintel"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/toolgateway"
)

type workerCodeIntelFixture struct {
	actor    *workerCommandTestActor
	runtime  *AgentRunner
	executor *CodeIntelToolExecutor
	caps     domain.ExecutionPermissionRuntimeCapabilities
}

func newWorkerCodeIntelFixture(t *testing.T, profile *domain.WorkerProfile) workerCodeIntelFixture {
	t.Helper()
	st, run, root, lease, caps := newCommandRuntimeTestRuntime(t, t.Context())
	actor := newWorkerCommandTestActor(t, st, &CommandRuntimeService{}, run, root, lease, profile)
	runtime := NewAgentRunner(st, nil, policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(caps)
	// Contract tests stop before server dispatch; the real smoke below supplies
	// a reviewed gopls manager and executes the same durable worker calls.
	executor := NewCodeIntelToolExecutor(st, policy.NewDefaultChecker(), &codeintel.Manager{}).WithExecutionPermissionCapabilities(caps)
	return workerCodeIntelFixture{actor: actor, runtime: runtime, executor: executor, caps: caps}
}

func workerCodeIntelTestPayload(t *testing.T, name toolgateway.ToolName) json.RawMessage {
	t.Helper()
	input := toolgateway.CodeIntelPayload{Version: toolgateway.CodeIntelProtocolVersion,
		ServerID: "test-lsp", ServerGeneration: strings.Repeat("a", 64),
		CapabilityFingerprint: strings.Repeat("b", 64), Path: "sample.go", Limit: 20}
	if name == toolgateway.CodeWorkspaceSymbolsTool {
		input.Path, input.Query = "", "Helper"
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	_, canonical, err := toolgateway.NormalizeCodeIntelPayload(name, raw)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func (f workerCodeIntelFixture) parentAuthority(t *testing.T) json.RawMessage {
	t.Helper()
	a := f.actor
	mission, err := a.store.GetMission(t.Context(), a.run.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	mode, err := a.store.GetRunMode(t.Context(), a.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	permission, err := a.store.GetRunExecutionPermission(t.Context(), a.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, authority, err := f.runtime.supervisorAgentCodeCapabilities(t.Context(),
		domain.SupervisorTurn{Run: a.run, Mission: mission, Agent: a.root, Mode: mode}, permission)
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func (f workerCodeIntelFixture) prepare(t *testing.T, name toolgateway.ToolName, payload json.RawMessage) (domain.SpecialistToolCall, toolgateway.ToolCall) {
	t.Helper()
	a := f.actor
	authority, err := f.executor.authority.BindSpecialistAgentCodeAuthority(t.Context(), a.ref, name, f.parentAuthority(t), payload)
	if err != nil {
		t.Fatal(err)
	}
	model := llm.ModelAttempt{Number: a.round + 1, ToolRound: a.round, TransportAttempt: 1,
		MaxAttempts: 1, Provider: "worker-fixture", Model: "fixture"}
	if _, err := a.store.RecordSpecialistModelStarted(t.Context(), a.ref, model); err != nil {
		t.Fatal(err)
	}
	model.Elapsed, model.Outcome = time.Millisecond, llm.OutcomeSuccess
	callID := idgen.New("worker-lsp-call")
	response := llm.ChatResponse{Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
		ToolCalls: []llm.ToolCall{{ID: callID, Name: string(name), Arguments: payload}}}
	if _, err := a.store.RecordSpecialistToolModelCompleted(t.Context(), a.ref, model, response,
		"execute the authenticated worker brief", policy.Decision{Allowed: true, Risk: "low", Reason: "fixture model output"}); err != nil {
		t.Fatal(err)
	}
	a.round++
	call, err := a.store.BindSpecialistToolAuthority(t.Context(), a.ref, callID, string(authority))
	if err != nil {
		t.Fatal(err)
	}
	bound, err := toolgateway.DecodeAgentCodeCallAuthority(authority)
	if err != nil {
		t.Fatal(err)
	}
	return call, toolgateway.ToolCall{Name: name, Payload: payload,
		OperationKey: domain.SpecialistToolApprovalID(a.ref, callID), RunID: a.run.ID,
		MissionID: a.run.MissionID, WorkspaceID: bound.WorkspaceID, SessionID: a.child.SessionID,
		RootAgentID: a.root.ID, AgentID: a.child.ID, AgentAttemptID: a.ref.AttemptID,
		SpecialistToolCallID: callID, Role: domain.AgentRoleSpecialist, RequestedBy: "specialist_runner",
		LeaseID: a.lease.LeaseID, LeaseGeneration: a.lease.Generation,
		Surface: bound.Surface, Phase: bound.Phase, Profile: bound.Profile, ModeRevision: bound.ModeRevision,
		RootFingerprint: bound.RootFingerprint, CapabilityGeneration: bound.CapabilityGeneration,
		PermissionMode: bound.PermissionMode, PermissionRevision: bound.PermissionRevision,
		PermissionSnapshotID: bound.PermissionSnapshotID, PermissionGeneration: bound.PermissionGeneration,
		PermissionRuntimeEpoch: bound.PermissionRuntimeEpoch, RunAuthorizationFence: bound.RunAuthorizationFence}
}

type workerCodeIntelCapture struct {
	executor *CodeIntelToolExecutor
	scope    toolgateway.AgentCodeExecutionScope
	calls    int
}

func (e *workerCodeIntelCapture) ResolveWorkerCodeIntelWorkspace(ctx context.Context, call toolgateway.ToolCall) (string, string, error) {
	return e.executor.ResolveWorkerCodeIntelWorkspace(ctx, call)
}

func (e *workerCodeIntelCapture) ExecuteCodeIntel(ctx context.Context, scope toolgateway.AgentCodeExecutionScope,
	name toolgateway.ToolName, payload json.RawMessage,
) (toolgateway.CodeIntelExecutionResult, error) {
	if err := e.executor.authority.validateScope(ctx, scope, name); err != nil {
		return toolgateway.CodeIntelExecutionResult{}, err
	}
	if err := e.executor.validateWorkerPayload(ctx, scope, name, payload); err != nil {
		return toolgateway.CodeIntelExecutionResult{}, err
	}
	e.scope, e.calls = scope, e.calls+1
	return toolgateway.CodeIntelExecutionResult{JSON: `{"items":[]}`}, nil
}

func TestWorkerCodeIntelAuthorityKeepsDelegatedReadBoundary(t *testing.T) {
	f := newWorkerCodeIntelFixture(t, &domain.WorkerProfile{Tools: []string{string(toolgateway.CodeHoverTool)}})
	parent := f.parentAuthority(t)
	payload := workerCodeIntelTestPayload(t, toolgateway.CodeHoverTool)
	bound, err := f.executor.authority.BindSpecialistAgentCodeAuthority(t.Context(), f.actor.ref,
		toolgateway.CodeHoverTool, parent, payload)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := toolgateway.DecodeAgentCodeCallAuthority(bound)
	if err != nil || authority.Worker == nil || authority.Worker.AgentID != f.actor.child.ID ||
		authority.RootAgentID != f.actor.root.ID || authority.SessionID != f.actor.child.SessionID {
		t.Fatalf("semantic authority lost the delegated actor: %+v %v", authority, err)
	}
	if _, err := f.executor.authority.BindSpecialistAgentCodeAuthority(t.Context(), f.actor.ref,
		toolgateway.CodeDefinitionTool, parent, workerCodeIntelTestPayload(t, toolgateway.CodeDefinitionTool)); err == nil {
		t.Fatal("worker acquired an undelegated semantic tool")
	}
	if _, err := f.executor.authority.BindSpecialistAgentCodeAuthority(t.Context(), f.actor.ref,
		toolgateway.CodeHoverTool, parent, json.RawMessage(`{"version":"agent-code-tools.v1","path":"sample.go"}`)); err == nil {
		t.Fatal("semantic authority accepted a workspace-tool payload")
	}
	f.caps.RuntimeAuthority.RevokeRun(f.actor.run.ID)
	if _, err := f.executor.authority.BindSpecialistAgentCodeAuthority(t.Context(), f.actor.ref,
		toolgateway.CodeHoverTool, parent, payload); err == nil {
		t.Fatal("revoked runtime authority could bind semantic reads")
	}
}

func TestWorkerCodeIntelGatewayPreservesActorAndCurrentPermission(t *testing.T) {
	f := newWorkerCodeIntelFixture(t, &domain.WorkerProfile{})
	capture := &workerCodeIntelCapture{executor: f.executor}
	f.runtime.tools.WithCodeIntelExecutor(capture)
	payload := workerCodeIntelTestPayload(t, toolgateway.CodeHoverTool)
	stored, call := f.prepare(t, toolgateway.CodeHoverTool, payload)
	for _, mutate := range []func(*toolgateway.ToolCall){
		func(c *toolgateway.ToolCall) { c.AgentID = f.actor.root.ID },
		func(c *toolgateway.ToolCall) { c.AgentAttemptID = "foreign-attempt" },
		func(c *toolgateway.ToolCall) { c.SpecialistToolCallID = "" },
	} {
		forged := call
		mutate(&forged)
		if _, err := f.runtime.tools.Invoke(t.Context(), forged); err == nil {
			t.Fatal("semantic dispatch accepted a different worker identity")
		}
	}
	outcome, err := f.runtime.tools.Invoke(t.Context(), call)
	if err != nil || outcome.Result == nil || outcome.Result.Status != toolgateway.StatusCompleted || capture.calls != 1 {
		t.Fatalf("worker semantic dispatch: %+v %v", outcome, err)
	}
	scope := capture.scope
	if scope.AgentID != f.actor.child.ID || scope.RootAgentID != f.actor.root.ID ||
		scope.AgentAttemptID != f.actor.ref.AttemptID || scope.SpecialistToolCallID != stored.CallID ||
		scope.SessionID != f.actor.child.SessionID || scope.PermissionSnapshotID != call.PermissionSnapshotID ||
		scope.PermissionGeneration != call.PermissionGeneration || scope.PermissionRuntimeEpoch != call.PermissionRuntimeEpoch ||
		scope.RunAuthorizationFence != call.RunAuthorizationFence {
		t.Fatalf("gateway dropped worker scope or permission fence: %+v", scope)
	}
	if _, err := f.runtime.tools.Invoke(t.Context(), call); err == nil || capture.calls != 1 {
		t.Fatalf("started semantic call was replayed: calls=%d err=%v", capture.calls, err)
	}
	changed := []byte(strings.Replace(string(payload), "sample.go", "other.go", 1))
	if _, err := f.executor.ExecuteCodeIntel(t.Context(), scope, call.Name, changed); err == nil ||
		!strings.Contains(err.Error(), "payload differs") {
		t.Fatalf("semantic executor accepted a different durable payload: %v", err)
	}
	f.caps.RuntimeAuthority.RevokeRun(f.actor.run.ID)
	if _, err := capture.ExecuteCodeIntel(t.Context(), scope, call.Name, payload); err == nil || capture.calls != 1 {
		t.Fatalf("revoked semantic invocation executed: calls=%d err=%v", capture.calls, err)
	}
}
