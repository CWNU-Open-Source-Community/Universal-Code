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
