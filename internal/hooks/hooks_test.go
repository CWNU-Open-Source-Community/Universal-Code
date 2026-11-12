package hooks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
)

func TestEngineOrdersDeclarativeHooksAndOnlyNarrowsPayload(t *testing.T) {
	engine := NewEngine(nil)
	fingerprint := testHookDigest("plugin")
	values := []Registration{
		{PluginID: "plugin-b", PluginFingerprint: fingerprint, Declaration: Declaration{
			ProtocolVersion: ProtocolVersion, ID: "annotate", Event: PreTool,
			Action: ActionAnnotate, FailurePolicy: FailureDeny, TimeoutMillis: 100,
			Message: "reviewed by plugin-b"}},
		{PluginID: "plugin-a", PluginFingerprint: fingerprint, Declaration: Declaration{
			ProtocolVersion: ProtocolVersion, ID: "narrow", Event: PreTool,
			Action: ActionNarrow, FailurePolicy: FailureDeny, TimeoutMillis: 100,
			RemoveFields: []string{"network"}}},
	}
	if err := engine.Replace(values); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Execute(context.Background(), Input{Event: PreTool,
		ToolName: "mcp_tool_call", Payload: json.RawMessage(`{"path":"a","network":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Denied || string(result.Payload) != `{"path":"a"}` || len(result.Annotations) != 1 ||
		len(result.Executed) != 2 || result.Executed[0] != "plugin-a/narrow" {
		t.Fatalf("unexpected hook result: %#v", result)
	}
}

func TestEngineRejectsReentryAndDenyRuleFailsClosed(t *testing.T) {
	engine := NewEngine(nil)
	if err := engine.Replace([]Registration{{PluginID: "guard",
		PluginFingerprint: testHookDigest("guard"), Declaration: Declaration{
			ProtocolVersion: ProtocolVersion, ID: "deny-shell", Event: PreTool,
			Action: ActionDeny, FailurePolicy: FailureDeny, TimeoutMillis: 100,
			ToolNames: []string{"shell"}, Message: "shell disabled by reviewed plugin"}}}); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Execute(context.Background(), Input{Event: PreTool,
		ToolName: "shell", Payload: json.RawMessage(`{}`)})
	if err != nil || !result.Denied || result.Reason == "" {
		t.Fatalf("deny result=%#v err=%v", result, err)
	}
	if _, err := engine.Execute(context.Background(), Input{Event: PreTool,
		ToolName: "shell", Payload: json.RawMessage(`{}`), Depth: 1}); err == nil {
		t.Fatal("reentrant hook execution was accepted")
	}
}

func TestDeclarationRejectsRetroactivePostToolDenialPolicy(t *testing.T) {
	declaration := Declaration{ProtocolVersion: ProtocolVersion, ID: "post-audit",
		Event: PostTool, Action: ActionRecord, FailurePolicy: FailureDeny, TimeoutMillis: 100}
	if err := declaration.Validate(); err == nil {
		t.Fatal("post_tool declaration accepted a failure policy that cannot be enforced retroactively")
	}
	declaration.FailurePolicy = FailureContinue
	if err := declaration.Validate(); err != nil {
		t.Fatalf("post_tool continue policy was rejected: %v", err)
	}
}

func TestExecuteBoundaryFailsClosedWithoutLeakingPluginMessage(t *testing.T) {
	engine := NewEngine(nil)
	if err := engine.Replace([]Registration{{PluginID: "guard",
		PluginFingerprint: testHookDigest("guard"), Declaration: Declaration{
			ProtocolVersion: ProtocolVersion, ID: "deny-run", Event: RunStarted,
			Action: ActionDeny, FailurePolicy: FailureDeny, TimeoutMillis: 100,
			Message: "plugin-controlled secret-like detail"}}}); err != nil {
		t.Fatal(err)
	}
	result, err := ExecuteBoundary(context.Background(), engine, Input{
		Event: RunStarted, RunID: "run-1", WorkspaceID: "workspace-1",
	}, map[string]any{"transition": "start"})
	var denied DeniedError
	if !errors.As(err, &denied) || !result.Denied ||
		err.Error() != "restricted lifecycle hook denied the operation" {
		t.Fatalf("boundary result=%#v err=%v", result, err)
	}
	if _, err := ExecuteBoundary(context.Background(), engine, Input{
		Event: RunStarted, Payload: json.RawMessage(`{}`),
	}, map[string]any{}); err == nil {
		t.Fatal("caller-supplied raw lifecycle payload was accepted")
	}
}

type failingHookAuditSink struct{}

func (failingHookAuditSink) RecordHookAudit(context.Context, AuditRecord) error {
	return errors.New("audit unavailable")
}

func TestEngineAppliesFailurePolicyWhenAuditCannotCommit(t *testing.T) {
	registration := Registration{PluginID: "guard", PluginFingerprint: testHookDigest("guard"),
		Declaration: Declaration{ProtocolVersion: ProtocolVersion, ID: "record-run",
			Event: RunStarted, Action: ActionRecord, FailurePolicy: FailureDeny,
			TimeoutMillis: 100}}
	engine := NewEngine(failingHookAuditSink{})
	if err := engine.Replace([]Registration{registration}); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Execute(t.Context(), Input{Event: RunStarted, RunID: "run-1"})
	if err != nil || !result.Denied || len(result.Executed) != 0 {
		t.Fatalf("failed audit did not deny fail-closed hook: %#v err=%v", result, err)
	}
	registration.Declaration.FailurePolicy = FailureContinue
	if err := engine.Replace([]Registration{registration}); err != nil {
		t.Fatal(err)
	}
	result, err = engine.Execute(t.Context(), Input{Event: RunStarted, RunID: "run-1"})
	if err != nil || result.Denied || len(result.Executed) != 0 {
		t.Fatalf("failed audit did not follow continue policy: %#v err=%v", result, err)
	}
}

func testHookDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func TestApprovalRequestedEventRules(t *testing.T) {
	if !ApprovalRequested.Valid() {
		t.Fatal("approval_requested event is not valid")
	}
	if err := (Declaration{ProtocolVersion: ProtocolVersion, ID: "approval-tap",
		Event: ApprovalRequested, Action: ActionRecord, FailurePolicy: FailureContinue,
		TimeoutMillis: 100}).Validate(); err != nil {
		t.Fatalf("record hook on approval_requested was rejected: %v", err)
	}
	for _, broken := range []struct {
		name   string
		action Action
	}{
		{"auto-deny", ActionDeny},
		{"annotation", ActionAnnotate},
		{"narrow", ActionNarrow},
	} {
		declaration := Declaration{ProtocolVersion: ProtocolVersion, ID: "approval",
			Event: ApprovalRequested, Action: broken.action, FailurePolicy: FailureDeny,
			TimeoutMillis: 100, Message: "hook-controlled text"}
		if err := declaration.Validate(); err == nil {
			t.Fatalf("%s was accepted on approval_requested", broken.name)
		}
	}
	if err := (Declaration{ProtocolVersion: ProtocolVersion, ID: "approval-tool",
		Event: ApprovalRequested, Action: ActionRecord, FailurePolicy: FailureContinue,
		TimeoutMillis: 100, ToolNames: []string{"shell"}}).Validate(); err == nil {
		t.Fatal("approval_requested declaration accepted tool filters")
	}
}

func TestPostToolFailureEventRules(t *testing.T) {
	if !PostToolFailure.Valid() {
		t.Fatal("post_tool_failure event is not valid")
	}
	if err := (Declaration{ProtocolVersion: ProtocolVersion, ID: "failure-tap",
		Event: PostToolFailure, Action: ActionRecord, FailurePolicy: FailureContinue,
		TimeoutMillis: 100, ToolNames: []string{"shell"}}).Validate(); err != nil {
		t.Fatalf("record hook with tool filter on post_tool_failure was rejected: %v", err)
	}
	// Deny is rejected (same rule as post_tool)
	if err := (Declaration{ProtocolVersion: ProtocolVersion, ID: "failure-deny",
		Event: PostToolFailure, Action: ActionDeny, FailurePolicy: FailureDeny,
		TimeoutMillis: 100, Message: "retroactive"}).Validate(); err == nil {
		t.Fatal("deny on post_tool_failure was accepted")
	}
	// FailurePolicy=deny is rejected (same rule as post_tool)
	if err := (Declaration{ProtocolVersion: ProtocolVersion, ID: "failure-policy",
		Event: PostToolFailure, Action: ActionRecord, FailurePolicy: FailureDeny,
		TimeoutMillis: 100}).Validate(); err == nil {
		t.Fatal("FailureDeny on post_tool_failure was accepted")
	}
	// Tool filter works (same as post_tool)
	if err := (Declaration{ProtocolVersion: ProtocolVersion, ID: "failure-narrow",
		Event: PostToolFailure, Action: ActionRecord, FailurePolicy: FailureContinue,
		TimeoutMillis: 100, ToolNames: []string{"shell"}}).Validate(); err != nil {
		t.Fatalf("tool filter on post_tool_failure was rejected: %v", err)
	}
}

func TestEnginePostToolFailureFiresOnlyForFailures(t *testing.T) {
	sink := &scriptedAuditSink{}
	engine := NewEngine(sink)
	if err := engine.Replace([]Registration{{PluginID: "plugin-a",
		PluginFingerprint: testHookDigest("plugin"), Declaration: Declaration{
			ProtocolVersion: ProtocolVersion, ID: "failure-tap", Event: PostToolFailure,
			Action: ActionRecord, FailurePolicy: FailureContinue, TimeoutMillis: 100}}}); err != nil {
		t.Fatal(err)
	}
	// A successful tool call fires post_tool but NOT post_tool_failure.
	successInput := Input{Event: PostTool, ToolName: "shell", Payload: json.RawMessage(`{"status":"completed"}`)}
	result, err := engine.Execute(t.Context(), successInput)
	if err != nil || len(result.Executed) != 0 {
		t.Fatalf("post_tool_failure fired for a successful call: %#v err=%v", result, err)
	}
	// A failed tool call fires post_tool_failure.
	failureInput := Input{Event: PostToolFailure, ToolName: "shell", Payload: json.RawMessage(`{"status":"failed"}`)}
	result, err = engine.Execute(t.Context(), failureInput)
	if err != nil || len(result.Executed) != 1 {
		t.Fatalf("post_tool_failure did not fire for a failed call: %#v err=%v", result, err)
	}
	if len(sink.records) != 1 || sink.records[0].Event != PostToolFailure {
		t.Fatalf("audit records=%#v", sink.records)
	}
}

func TestDeclarationRejectsTurnEndDenialPolicy(t *testing.T) {
	declaration := Declaration{ProtocolVersion: ProtocolVersion, ID: "turn-audit",
		Event: TurnEnd, Action: ActionRecord, FailurePolicy: FailureDeny, TimeoutMillis: 100}
	if err := declaration.Validate(); err == nil {
		t.Fatal("turn_end declaration accepted a failure policy that cannot be enforced on a completed turn")
	}
	declaration.FailurePolicy = FailureContinue
	if err := declaration.Validate(); err != nil {
		t.Fatalf("turn_end continue policy was rejected: %v", err)
	}
}

func TestTurnEndEventRules(t *testing.T) {
	if !TurnEnd.Valid() {
		t.Fatal("turn_end event is not valid")
	}
	if err := (Declaration{ProtocolVersion: ProtocolVersion, ID: "turn-tap",
		Event: TurnEnd, Action: ActionRecord, FailurePolicy: FailureContinue,
		TimeoutMillis: 100}).Validate(); err != nil {
		t.Fatalf("record hook on turn_end was rejected: %v", err)
	}
	for _, broken := range []struct {
		name   string
		action Action
	}{
		{"deny", ActionDeny},
		{"annotate", ActionAnnotate},
		{"narrow", ActionNarrow},
	} {
		declaration := Declaration{ProtocolVersion: ProtocolVersion, ID: "turn-end",
			Event: TurnEnd, Action: broken.action, FailurePolicy: FailureContinue,
			TimeoutMillis: 100, Message: "hook-controlled"}
		if err := declaration.Validate(); err == nil {
			t.Fatalf("%s was accepted on turn_end", broken.name)
		}
	}
	if err := (Declaration{ProtocolVersion: ProtocolVersion, ID: "turn-tool",
		Event: TurnEnd, Action: ActionRecord, FailurePolicy: FailureContinue,
		TimeoutMillis: 100, ToolNames: []string{"shell"}}).Validate(); err == nil {
		t.Fatal("turn_end declaration accepted tool filters")
	}
}
