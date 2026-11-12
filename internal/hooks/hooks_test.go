package hooks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

func TestDeclarationRejectsPostCompactionDenialPolicy(t *testing.T) {
	declaration := Declaration{ProtocolVersion: ProtocolVersion, ID: "post-compact-audit",
		Event: PostCompaction, Action: ActionRecord, FailurePolicy: FailureDeny, TimeoutMillis: 100}
	if err := declaration.Validate(); err == nil {
		t.Fatal("post_compaction declaration accepted a failure policy that cannot be enforced after compaction")
	}
	declaration.FailurePolicy = FailureContinue
	if err := declaration.Validate(); err != nil {
		t.Fatalf("post_compaction continue policy was rejected: %v", err)
	}
}

type recordingHookAuditSink struct{ outcomes []string }

func (s *recordingHookAuditSink) RecordHookAudit(_ context.Context, record AuditRecord) error {
	s.outcomes = append(s.outcomes, record.Outcome)
	return nil
}

func annotateRegistrations(event Event, count, messageBytes int) []Registration {
	fingerprint := testHookDigest("plugin")
	values := make([]Registration, 0, count)
	for i := 0; i < count; i++ {
		message := fmt.Sprintf("observation %d", i)
		if messageBytes > 0 {
			message = strings.Repeat("x", messageBytes)
		}
		values = append(values, Registration{PluginID: "plugin", PluginFingerprint: fingerprint,
			Declaration: Declaration{ProtocolVersion: ProtocolVersion,
				ID: fmt.Sprintf("annotate-%02d", i), Event: event,
				Action: ActionAnnotate, FailurePolicy: FailureContinue, TimeoutMillis: 100,
				Message: message}})
	}
	return values
}

func TestPostOccurrenceAnnotationOverflowCapsInsteadOfFailing(t *testing.T) {
	sink := &recordingHookAuditSink{}
	engine := NewEngine(sink)
	total := MaxAnnotations + 4
	if err := engine.Replace(annotateRegistrations(RunCompleted, total, 0)); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Execute(context.Background(), Input{Event: RunCompleted, RunID: "run-1"})
	if err != nil {
		t.Fatalf("post-occurrence annotation overflow failed the boundary: %v", err)
	}
	if len(result.Annotations) != MaxAnnotations {
		t.Fatalf("annotations were not capped: %d", len(result.Annotations))
	}
	if len(result.Executed) != total {
		t.Fatalf("capped hooks did not stay executed: %d of %d", len(result.Executed), total)
	}
	capped := 0
	for _, outcome := range sink.outcomes {
		if outcome == "output_capped" {
			capped++
		}
	}
	if capped == 0 {
		t.Fatal("no audit row recorded the cap drop")
	}
}

func TestPostOccurrenceOutputSizeOverflowCapsInsteadOfFailing(t *testing.T) {
	engine := NewEngine(nil)
	// MaxAnnotations annotations of maximum message size overflow the output
	// bound even though the annotation count stays within its own cap.
	if err := engine.Replace(annotateRegistrations(PostTool, MaxAnnotations, MaxAnnotationBytes)); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Execute(context.Background(), Input{Event: PostTool,
		ToolName: "shell", RunID: "run-1"})
	if err != nil {
		t.Fatalf("post-occurrence output overflow failed the boundary: %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > MaxOutputBytes || len(result.Annotations) == MaxAnnotations {
		t.Fatalf("output was not capped: %d bytes, %d annotations", len(encoded), len(result.Annotations))
	}
}

func TestSessionClosedPreservesIndependentlyBoundedPayload(t *testing.T) {
	engine := NewEngine(nil)
	if err := engine.Replace([]Registration{{PluginID: "plugin",
		PluginFingerprint: testHookDigest("plugin"), Declaration: Declaration{
			ProtocolVersion: ProtocolVersion, ID: "record-close", Event: SessionClosed,
			Action: ActionRecord, FailurePolicy: FailureContinue, TimeoutMillis: 100}}}); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Execute(context.Background(), Input{Event: SessionClosed, RunID: "run-1",
		Payload: json.RawMessage("\"" + strings.Repeat("y", 20*1024) + "\"")})
	if err != nil {
		t.Fatalf("payload echo overflow failed the boundary: %v", err)
	}
	if len(result.Payload) != 20*1024+2 || len(result.Executed) != 1 {
		t.Fatalf("independently bounded payload was not preserved: payload=%d executed=%v",
			len(result.Payload), result.Executed)
	}
}

func TestPreOccurrenceAnnotationOverflowStillFailsClosed(t *testing.T) {
	engine := NewEngine(nil)
	if err := engine.Replace(annotateRegistrations(PreTool, MaxAnnotations+4, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Execute(context.Background(), Input{Event: PreTool, ToolName: "shell"}); err == nil {
		t.Fatal("pre-occurrence annotation overflow must keep failing closed")
	}
}

func TestPostOccurrenceExecutedListOverflowCapsInsteadOfFailing(t *testing.T) {
	engine := NewEngine(nil)
	// 64 registrations of max-length identities exceed MaxOutputBytes on the
	// executed list alone; the registry-wide identity bound makes this the
	// reachable worst case.
	longID := strings.Repeat("h", MaxHookIdentityRunes-1)
	values := make([]Registration, 0, MaxDeclarations)
	for i := 0; i < MaxDeclarations; i++ {
		values = append(values, Registration{PluginID: longID,
			PluginFingerprint: testHookDigest("plugin"),
			Declaration: Declaration{ProtocolVersion: ProtocolVersion,
				ID:    longID[:MaxHookIdentityRunes-5] + fmt.Sprintf("%04d", i),
				Event: RunCompleted, Action: ActionAnnotate,
				FailurePolicy: FailureContinue, TimeoutMillis: 100,
				Message: "m"}})
	}
	if err := engine.Replace(values); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Execute(context.Background(), Input{Event: RunCompleted, RunID: "run-1"})
	if err != nil {
		t.Fatalf("executed-list overflow failed the post-occurrence boundary: %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > MaxOutputBytes || len(result.Executed) == MaxDeclarations {
		t.Fatalf("executed list was not capped: %d bytes, %d entries", len(encoded), len(result.Executed))
	}
}

func TestPostOccurrenceAuditFailureStillCapsAnnotations(t *testing.T) {
	engine := NewEngine(failingHookAuditSink{})
	if err := engine.Replace(annotateRegistrations(RunCompleted, MaxAnnotations+4, 0)); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Execute(t.Context(), Input{Event: RunCompleted, RunID: "run-1"})
	if err != nil || len(result.Annotations) != MaxAnnotations || len(result.Executed) != 0 {
		t.Fatalf("failed sink output escaped cap: %+v err=%v", result, err)
	}
}

func TestPostOccurrenceCapPreservesPayloadForLaterScripts(t *testing.T) {
	capturing := &payloadCapturingExecutor{}
	engine := NewEngine(nil).WithScriptExecutor(capturing)
	values := annotateRegistrations(RunCompleted, MaxAnnotations+4, MaxAnnotationBytes)
	values = append(values, Registration{PluginID: OperatorPluginID, PluginFingerprint: testHookDigest("script"), Declaration: Declaration{ProtocolVersion: ProtocolVersion, ID: "later-script", Event: RunCompleted, Action: ActionScript, FailurePolicy: FailureContinue, TimeoutMillis: 100, Script: &ScriptSpec{Interpreter: "python", Path: "tap.py", SHA256: testHookDigest("script")}}})
	if err := engine.Replace(values); err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage("\"" + strings.Repeat("x", 20*1024) + "\"")
	result, err := engine.Execute(t.Context(), Input{Event: RunCompleted, Payload: payload})
	if err != nil || len(capturing.invocations) != 1 || string(capturing.invocations[0].Payload) != string(payload) || string(result.Payload) != string(payload) {
		t.Fatalf("capping changed script input: invocations=%+v payload=%d err=%v", capturing.invocations, len(result.Payload), err)
	}
	produced := result
	produced.Payload = nil
	encoded, _ := json.Marshal(produced)
	if len(encoded) > MaxOutputBytes {
		t.Fatalf("uncapped hook output: %d", len(encoded))
	}
}
