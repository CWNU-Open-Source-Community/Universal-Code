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

func TestSessionClosedPayloadEchoOverflowDropsPayloadOnly(t *testing.T) {
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
	if result.Payload != nil || len(result.Executed) != 1 {
		t.Fatalf("payload echo was not capped in place: payload=%d executed=%v",
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
