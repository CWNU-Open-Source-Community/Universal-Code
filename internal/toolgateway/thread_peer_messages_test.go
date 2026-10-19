package toolgateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"cyberagent-workbench/internal/policy"
)

func TestThreadPeerMessagingRejectsForgedSenderAndUnboundedInput(t *testing.T) {
	for _, raw := range []string{
		`null`, `[]`, `{} {}`, `{"source_thread_id":"forged"}`, `{"requested_by":"operator"}`,
	} {
		if _, err := NormalizeThreadPeerMessagePayload(ThreadListTool, json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted list payload %s", raw)
		}
	}
	for _, raw := range []string{
		`{}`, `{"target_thread_id":"other","content":" "}`,
		`{"target_thread_id":"other","content":"hello","source_thread_id":"forged"}`,
		`{"target_thread_id":"other","content":"hello","model_initiated":false}`,
		`{"target_thread_id":"other","content":"hello","operation_key":"forged"}`,
		`{"target_thread_id":"other","content":"hello","reply_to":" bad identity "}`,
		`{"target_thread_id":"other","content":"` + strings.Repeat("x", 16385) + `"}`,
	} {
		if _, err := NormalizeThreadPeerMessagePayload(ThreadSendMessageTool, json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted invalid send payload %s", raw[:min(len(raw), 160)])
		}
	}
	if _, err := NormalizeThreadPeerMessagePayload(ThreadSendMessageTool, json.RawMessage(`{"target_thread_id":"other","content":"请检查函数","reply_to":"peer-1"}`)); err != nil {
		t.Fatal(err)
	}
}

type threadPeerTestExecutor struct{ calls int }

func (e *threadPeerTestExecutor) ExecuteThreadPeerMessages(context.Context, ToolCall) (json.RawMessage, error) {
	e.calls++
	return json.RawMessage(`{"instruction_authorized":false,"accepted":true}`), nil
}

func TestThreadPeerMessagingRequiresRootFenceAndHonorsPolicy(t *testing.T) {
	executor := &threadPeerTestExecutor{}
	gateway := New(nil, policy.NewDefaultChecker()).WithThreadPeerMessagesExecutor(executor)
	call := ToolCall{Name: ThreadSendMessageTool, Payload: json.RawMessage(`{"target_thread_id":"target","content":"review result"}`), RunID: "run", SessionID: "session", AgentID: "root", AgentAttemptID: "attempt", RequestedBy: "run_supervisor", OperationKey: "send-1", LeaseID: "lease", LeaseGeneration: 1}
	for _, mutate := range []func(*ToolCall){
		func(c *ToolCall) { c.RequestedBy = "specialist_runner" },
		func(c *ToolCall) { c.LeaseID = "" },
		func(c *ToolCall) { c.OperationKey = "" },
		func(c *ToolCall) { c.AgentAttemptID = "" },
	} {
		bad := call
		mutate(&bad)
		if _, err := gateway.Invoke(t.Context(), bad); err == nil || executor.calls != 0 {
			t.Fatal("unfenced send reached peer service")
		}
	}
	outcome, err := gateway.Invoke(t.Context(), call)
	if err != nil || outcome.Result == nil || executor.calls != 1 {
		t.Fatalf("send outcome=%+v err=%v calls=%d", outcome, err, executor.calls)
	}
	denied := New(nil, historyRecallDenyChecker{}).WithThreadPeerMessagesExecutor(executor)
	outcome, err = denied.Invoke(t.Context(), call)
	if err != nil || outcome.Decision.Allowed || executor.calls != 1 {
		t.Fatalf("policy denial bypassed: %+v %v", outcome, err)
	}
	if _, ok := TypedActionIDs()[string(ThreadSendMessageTool)]; ok {
		t.Fatal("peer send expanded project-config typed actions")
	}
}
