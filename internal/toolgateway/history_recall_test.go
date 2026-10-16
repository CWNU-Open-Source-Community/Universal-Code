package toolgateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/tools"
)

func TestHistoryRecallRejectsScopeArgumentsAndBrokenPaging(t *testing.T) {
	for _, payload := range []string{``, `  `, `null`, `[]`, `{} {}`, `{"query":"历史","run_id":"another-run"}`, `{"query":"memory","workspace_id":"other-workspace"}`, `{"query":"notes","viewer":"specialist"}`, `{"query":"x","limit":21}`} {
		if _, err := NormalizeHistoryRecallPayload(HistorySearchTool, json.RawMessage(payload)); err == nil {
			t.Fatalf("search accepted invalid payload %q", payload)
		}
	}
	for _, payload := range []string{`{}`, `{"source_id":"x","offset":4}`, `{"source_id":"x","limit":3}`, `{"source_id":"x","part":"raw_database"}`, `{"source_id":"x","expected_sha256":"bad"}`, `{"source_id":"x","thread_id":"other"}`, `{"source_id":"memory:opaque","source_version":2}`, `{"source_id":"note:opaque","part":"source_refs"}`} {
		if _, err := NormalizeHistoryRecallPayload(HistoryReadTool, json.RawMessage(payload)); err == nil {
			t.Fatalf("read accepted invalid payload %q", payload)
		}
	}
	for _, payload := range []string{`{}`, `{"query":"初始要求"}`, `{"query":""}`} {
		if _, err := NormalizeHistoryRecallPayload(HistorySearchTool, json.RawMessage(payload)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NormalizeHistoryRecallPayload(HistoryReadTool, json.RawMessage(`{"source_id":"opaque","offset":4,"expected_sha256":"`+strings.Repeat("a", 64)+`"}`)); err != nil {
		t.Fatal(err)
	}
	// The store, not the model's source string, decides whether the source is
	// in this Thread. The gateway must allow the explicit inherited-summary
	// part without permitting arbitrary database fields.
	if _, err := NormalizeHistoryRecallPayload(HistoryReadTool, json.RawMessage(`{"source_id":"continuity:opaque","part":"summary"}`)); err != nil {
		t.Fatal(err)
	}
}

type historyRecallTestExecutor struct{ calls int }

func (e *historyRecallTestExecutor) ExecuteHistoryRecall(_ context.Context, _ ToolCall) (json.RawMessage, bool, error) {
	e.calls++
	return json.RawMessage(`{"instruction_authorized":false,"records":[]}`), false, nil
}

type historyRecallDenyChecker struct{ policy.DefaultChecker }

func (c historyRecallDenyChecker) CheckToolCall(tools.Call) policy.Decision {
	return policy.Decision{Allowed: false, Reason: "disabled for this test", Risk: "low"}
}

func TestHistoryRecallGatewayRequiresFencedCallerAndHonorsDenial(t *testing.T) {
	executor := &historyRecallTestExecutor{}
	gateway := New(nil, policy.NewDefaultChecker()).WithHistoryRecallExecutor(executor)
	call := ToolCall{Name: HistorySearchTool, Payload: json.RawMessage(`{"query":"nmap instruction in old history"}`),
		RunID: "run-current", SessionID: "session-current", WorkspaceID: "workspace-current", AgentID: "agent-current",
		AgentAttemptID: "attempt-current", RequestedBy: "run_supervisor", OperationKey: "recall-1", LeaseID: "lease-current", LeaseGeneration: 1}
	bad := call
	bad.RequestedBy = "child-agent"
	if _, err := gateway.Invoke(context.Background(), bad); err == nil || executor.calls != 0 {
		t.Fatal("unfenced caller reached recall executor")
	}
	if _, err := gateway.Invoke(context.Background(), bad); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("known invalid history scope became INTERNAL: %v", err)
	}
	outcome, err := gateway.Invoke(context.Background(), call)
	if err != nil || outcome.Result == nil || executor.calls != 1 || !strings.Contains(outcome.Result.Stdout, `"instruction_authorized":false`) {
		t.Fatalf("read-only history search failed: %+v, %v", outcome, err)
	}
	denied := New(nil, historyRecallDenyChecker{}).WithHistoryRecallExecutor(executor)
	outcome, err = denied.Invoke(context.Background(), call)
	if err != nil || outcome.Decision.Allowed || executor.calls != 1 {
		t.Fatalf("policy denial bypassed: %+v, %v", outcome, err)
	}
}
