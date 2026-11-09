package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func TestScriptSpecValidateRejectsInvalidSpecifications(t *testing.T) {
	digest := testHookDigest("script")
	cases := []struct {
		name string
		spec ScriptSpec
	}{
		{"unsupported interpreter", ScriptSpec{Interpreter: "cmd", Path: "check.py", SHA256: digest}},
		{"absolute path", ScriptSpec{Interpreter: "python", Path: "/usr/bin/check.py", SHA256: digest}},
		{"windows drive path", ScriptSpec{Interpreter: "python", Path: "C:/tools/check.py", SHA256: digest}},
		{"parent escape", ScriptSpec{Interpreter: "python", Path: "../check.py", SHA256: digest}},
		{"dot-prefixed path", ScriptSpec{Interpreter: "python", Path: "./check.py", SHA256: digest}},
		{"windows separator", ScriptSpec{Interpreter: "python", Path: `scripts\check.py`, SHA256: digest}},
		{"trailing slash", ScriptSpec{Interpreter: "python", Path: "scripts/", SHA256: digest}},
		{"empty path", ScriptSpec{Interpreter: "python", SHA256: digest}},
		{"oversized path", ScriptSpec{Interpreter: "python", Path: strings.Repeat("a", MaxScriptPathBytes+1) + ".py", SHA256: digest}},
		{"invalid digest", ScriptSpec{Interpreter: "python", Path: "check.py", SHA256: "nothex"}},
		{"too many arguments", ScriptSpec{Interpreter: "python", Path: "check.py", SHA256: digest,
			Args: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}}},
		{"argument with shell metacharacters", ScriptSpec{Interpreter: "python", Path: "check.py",
			SHA256: digest, Args: []string{"--flag; rm -rf"}}},
		{"oversized argument", ScriptSpec{Interpreter: "python", Path: "check.py", SHA256: digest,
			Args: []string{strings.Repeat("a", MaxScriptArgRunes+1)}}},
	}
	for _, testCase := range cases {
		if err := testCase.spec.Validate(); err == nil {
			t.Fatalf("%s: invalid script specification was accepted", testCase.name)
		}
	}
	if err := (ScriptSpec{Interpreter: "python", Path: "scripts/check.py", SHA256: digest,
		Args: []string{"--strict", "profile=code"}}).Validate(); err != nil {
		t.Fatalf("valid script specification was rejected: %v", err)
	}
}

func TestDeclarationValidateAppliesScriptActionRules(t *testing.T) {
	spec := ScriptSpec{Interpreter: "python", Path: "check.py", SHA256: testHookDigest("script")}
	valid := Declaration{ProtocolVersion: ProtocolVersion, ID: "guard", Event: PreTool,
		Action: ActionScript, FailurePolicy: FailureDeny, TimeoutMillis: 9_000,
		Script: &spec}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid script declaration was rejected: %v", err)
	}
	if err := (Declaration{ProtocolVersion: ProtocolVersion, ID: "guard", Event: PreTool,
		Action: ActionScript, FailurePolicy: FailureDeny, TimeoutMillis: 10_001,
		Script: &spec}).Validate(); err == nil {
		t.Fatal("script declaration above the script timeout ceiling was accepted")
	}
	if err := (Declaration{ProtocolVersion: ProtocolVersion, ID: "guard", Event: PreTool,
		Action: ActionDeny, FailurePolicy: FailureDeny, TimeoutMillis: 10_000,
		Message: "deny", Script: &spec}).Validate(); err == nil {
		t.Fatal("script specification on a declarative action was accepted")
	}
	if err := (Declaration{ProtocolVersion: ProtocolVersion, ID: "guard", Event: PreTool,
		Action: ActionScript, FailurePolicy: FailureDeny, TimeoutMillis: 100}).Validate(); err == nil {
		t.Fatal("script declaration without a script specification was accepted")
	}
	if err := (Declaration{ProtocolVersion: ProtocolVersion, ID: "guard", Event: PostTool,
		Action: ActionScript, FailurePolicy: FailureContinue, TimeoutMillis: 100,
		Script: &spec}).Validate(); err == nil {
		t.Fatal("post_tool script declaration was accepted")
	}
	if err := (Declaration{ProtocolVersion: ProtocolVersion, ID: "guard", Event: PreTool,
		Action: ActionDeny, FailurePolicy: FailureDeny, TimeoutMillis: 2_001,
		Message: "deny"}).Validate(); err == nil {
		t.Fatal("declarative declaration above the declarative timeout ceiling was accepted")
	}
}

func TestScriptOutputValidateAndParseRejectsUntrustedOutput(t *testing.T) {
	valid := ScriptOutput{ProtocolVersion: ProtocolVersion, Denied: false}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid script output was rejected: %v", err)
	}
	if err := (ScriptOutput{ProtocolVersion: "other"}).Validate(); err == nil {
		t.Fatal("output with a foreign protocol version was accepted")
	}
	oversized := ScriptOutput{ProtocolVersion: ProtocolVersion,
		Reason: strings.Repeat("r", MaxAnnotationBytes+1)}
	if err := oversized.Validate(); err == nil {
		t.Fatal("oversized reason was accepted")
	}
	tooMany := ScriptOutput{ProtocolVersion: ProtocolVersion}
	for index := 0; index <= MaxAnnotations; index++ {
		tooMany.Annotations = append(tooMany.Annotations, ScriptAnnotation{Message: "note"})
	}
	if err := tooMany.Validate(); err == nil {
		t.Fatal("too many annotations were accepted")
	}
	if _, err := parseScriptOutput(nil); err == nil {
		t.Fatal("empty script output was accepted")
	}
	if _, err := parseScriptOutput([]byte(`{"protocol_version":"restricted-hooks.v1"}` +
		"\n" + `{"protocol_version":"restricted-hooks.v1"}`)); err == nil {
		t.Fatal("trailing JSON was accepted")
	}
	if _, err := parseScriptOutput([]byte(`{"protocol_version":"restricted-hooks.v1","unexpected":true}`)); err == nil {
		t.Fatal("unknown output fields were accepted")
	}
	if _, err := parseScriptOutput([]byte(`not json`)); err == nil {
		t.Fatal("non-JSON output was accepted")
	}
	parsed, err := parseScriptOutput([]byte(`{"protocol_version":"restricted-hooks.v1","denied":true,"reason":"blocked","annotations":[{"message":"check"}]}`))
	if err != nil || !parsed.Denied || parsed.Reason != "blocked" || len(parsed.Annotations) != 1 {
		t.Fatalf("valid output did not parse: %#v err=%v", parsed, err)
	}
}

type scriptedAuditSink struct {
	records []AuditRecord
}

func (s *scriptedAuditSink) RecordHookAudit(_ context.Context, record AuditRecord) error {
	s.records = append(s.records, record)
	return nil
}

type stubScriptExecutor struct {
	output ScriptOutput
	err    error
	seen   ScriptInvocation
}

func (s *stubScriptExecutor) ExecuteScript(_ context.Context, invocation ScriptInvocation) (ScriptOutput, error) {
	s.seen = invocation
	return s.output, s.err
}

type waitingScriptExecutor struct{}

func (waitingScriptExecutor) ExecuteScript(ctx context.Context, _ ScriptInvocation) (ScriptOutput, error) {
	<-ctx.Done()
	return ScriptOutput{}, ctx.Err()
}

func scriptRegistration(event Event, policy FailurePolicy, timeout int) Registration {
	return Registration{PluginID: OperatorPluginID, PluginFingerprint: testHookDigest("script"),
		Declaration: Declaration{ProtocolVersion: ProtocolVersion, ID: "guard", Event: event,
			Action: ActionScript, FailurePolicy: policy, TimeoutMillis: timeout,
			Script: &ScriptSpec{Interpreter: "python", Path: "check.py",
				SHA256: testHookDigest("script")}}}
}

func TestEngineScriptActionDeniesAnnotatesAndNeverModifiesPayload(t *testing.T) {
	sink := &scriptedAuditSink{}
	engine := NewEngine(sink).WithScriptExecutor(&stubScriptExecutor{output: ScriptOutput{
		ProtocolVersion: ProtocolVersion, Denied: true, Reason: "script blocked the call",
		Annotations: []ScriptAnnotation{{Message: "reviewed"}}}})
	if err := engine.Replace([]Registration{scriptRegistration(PreTool, FailureDeny, 100)}); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Execute(t.Context(), Input{Event: PreTool, RunID: "run-1",
		WorkspaceID: "ws-1", ToolName: "shell", Payload: json.RawMessage(`{"path":"a"}`)})
	if err != nil || !result.Denied || result.Reason != "script blocked the call" ||
		len(result.Annotations) != 1 || len(result.Executed) != 1 {
		t.Fatalf("script deny result=%#v err=%v", result, err)
	}
	if string(result.Payload) != `{"path":"a"}` {
		t.Fatalf("script hook modified the payload: %s", result.Payload)
	}
	if len(sink.records) != 1 || sink.records[0].Outcome != "completed" ||
		sink.records[0].Rejected == nil || !*sink.records[0].Rejected ||
		sink.records[0].PluginID != OperatorPluginID ||
		sink.records[0].Action != ActionScript {
		t.Fatalf("script denial audit records=%#v", sink.records)
	}
}

func TestEngineScriptActionWithoutExecutorFailsClosed(t *testing.T) {
	engine := NewEngine(nil)
	if err := engine.Replace([]Registration{scriptRegistration(PreTool, FailureDeny, 100)}); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Execute(t.Context(), Input{Event: PreTool, ToolName: "shell",
		Payload: json.RawMessage(`{}`)})
	if err != nil || !result.Denied || len(result.Executed) != 0 {
		t.Fatalf("missing executor with deny policy result=%#v err=%v", result, err)
	}
}

func TestEngineScriptActionFollowsFailurePolicyOnExecutorError(t *testing.T) {
	sink := &scriptedAuditSink{}
	executor := &stubScriptExecutor{err: errors.New("interpreter unavailable")}
	engine := NewEngine(sink).WithScriptExecutor(executor)
	if err := engine.Replace([]Registration{scriptRegistration(PreTool, FailureDeny, 100)}); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Execute(t.Context(), Input{Event: PreTool, ToolName: "shell",
		Payload: json.RawMessage(`{}`)})
	if err != nil || !result.Denied || len(result.Executed) != 0 ||
		sink.records[0].Outcome != "failed_closed" ||
		sink.records[0].Rejected == nil || !*sink.records[0].Rejected {
		t.Fatalf("executor failure with deny policy result=%#v records=%#v err=%v", result, sink.records, err)
	}
	*sink = scriptedAuditSink{}
	if err := engine.Replace([]Registration{scriptRegistration(PreTool, FailureContinue, 100)}); err != nil {
		t.Fatal(err)
	}
	result, err = engine.Execute(t.Context(), Input{Event: PreTool, ToolName: "shell",
		Payload: json.RawMessage(`{}`)})
	if err != nil || result.Denied || len(result.Executed) != 0 ||
		sink.records[0].Outcome != "failed_continue" ||
		sink.records[0].Rejected == nil || *sink.records[0].Rejected {
		t.Fatalf("executor failure with continue policy result=%#v records=%#v err=%v", result, sink.records, err)
	}
	if executor.seen.Interpreter != "python" || executor.seen.RelativePath != "check.py" ||
		executor.seen.HookID != "guard" {
		t.Fatalf("script invocation was not built from the declaration: %#v", executor.seen)
	}
}

func TestEngineScriptActionIntegrityFailureFailsClosedRegardlessOfPolicy(t *testing.T) {
	sink := &scriptedAuditSink{}
	executor := &stubScriptExecutor{err: ErrScriptIntegrity}
	engine := NewEngine(sink).WithScriptExecutor(executor)
	if err := engine.Replace([]Registration{scriptRegistration(PreTool, FailureContinue, 100)}); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Execute(t.Context(), Input{Event: PreTool, ToolName: "shell",
		Payload: json.RawMessage(`{}`)})
	if err != nil || !result.Denied || len(result.Executed) != 0 ||
		sink.records[0].Outcome != "failed_closed" ||
		sink.records[0].Rejected == nil || !*sink.records[0].Rejected {
		t.Fatalf("integrity failure with continue policy did not fail closed: result=%#v records=%#v err=%v", result, sink.records, err)
	}
}

func TestEngineScriptActionTimeoutFollowsFailurePolicy(t *testing.T) {
	engine := NewEngine(nil).WithScriptExecutor(waitingScriptExecutor{})
	if err := engine.Replace([]Registration{scriptRegistration(PreTool, FailureDeny, 80)}); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Execute(t.Context(), Input{Event: PreTool, ToolName: "shell",
		Payload: json.RawMessage(`{}`)})
	if err != nil || !result.Denied {
		t.Fatalf("timed-out script with deny policy result=%#v err=%v", result, err)
	}
}

func TestEngineScriptAnnotationOutputOnlyAnnotates(t *testing.T) {
	engine := NewEngine(nil).WithScriptExecutor(&stubScriptExecutor{output: ScriptOutput{
		ProtocolVersion: ProtocolVersion,
		Annotations:     []ScriptAnnotation{{Message: "policy checked"}}}})
	if err := engine.Replace([]Registration{scriptRegistration(RunStarted, FailureContinue, 100)}); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Execute(t.Context(), Input{Event: RunStarted, RunID: "run-1"})
	if err != nil || result.Denied || len(result.Annotations) != 1 ||
		result.Annotations[0].PluginID != OperatorPluginID ||
		result.Annotations[0].Message != "policy checked" {
		t.Fatalf("annotate-only script result=%#v err=%v", result, err)
	}
}

func TestScriptInvocationStdinJSONCarriesBoundedContext(t *testing.T) {
	invocation := ScriptInvocation{HookID: "guard", Event: PreTool, RunID: "run-1",
		WorkspaceID: "ws-1", ToolName: "shell", Payload: json.RawMessage(`{"path":"a"}`)}
	encoded, err := invocation.stdinJSON()
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["event"] != "pre_tool" || decoded["run_id"] != "run-1" ||
		decoded["workspace_id"] != "ws-1" || decoded["tool_name"] != "shell" ||
		len(decoded) != 5 {
		t.Fatalf("unexpected stdin payload: %s", encoded)
	}
	if _, found := decoded["hook_id"]; found {
		t.Fatal("stdin payload leaked the hook identity")
	}
}

func TestBoundedBufferRejectsOverflowWithoutTruncating(t *testing.T) {
	buffer := &boundedBuffer{Limit: 8}
	if _, err := buffer.Write([]byte("12345678")); err != nil {
		t.Fatal(err)
	}
	if _, err := buffer.Write([]byte("9")); err == nil || !buffer.overflowed {
		t.Fatal("bounded buffer accepted output beyond its limit")
	}
}

func TestProcessScriptExecutorRejectsInvalidConstruction(t *testing.T) {
	if _, err := NewProcessScriptExecutor("relative/path"); err == nil {
		t.Fatal("relative script hook directory was accepted")
	}
	if _, err := NewProcessScriptExecutor(""); err == nil {
		t.Fatal("empty script hook directory was accepted")
	}
}

func TestScriptEnvironmentEntriesDoNotInheritAmbientSecrets(t *testing.T) {
	t.Setenv("HOOK_TEST_SECRET", "leaked")
	entries := scriptEnvironmentVars()
	for _, entry := range entries {
		if strings.HasPrefix(entry, "HOOK_TEST_SECRET=") {
			t.Fatalf("ambient secret environment passed to scripts: %q", entry)
		}
	}
	prefix := "PATH="
	if runtime.GOOS == "windows" {
		prefix = "SystemRoot="
	}
	found := false
	for _, entry := range entries {
		if strings.HasPrefix(entry, prefix) {
			found = true
		}
	}
	if !found {
		t.Fatalf("script environment lost the operational %s entry", prefix)
	}
}

func TestValidateDeclarativeRejectsScriptAction(t *testing.T) {
	declaration := scriptRegistration(PreTool, FailureDeny, 100).Declaration
	if err := ValidateDeclarative(declaration); err == nil {
		t.Fatal("declarative validation accepted a script action")
	}
	if err := ValidateDeclarative(Declaration{ProtocolVersion: ProtocolVersion, ID: "guard",
		Event: PreTool, Action: ActionRecord, FailurePolicy: FailureContinue,
		TimeoutMillis: 100, Script: &ScriptSpec{Interpreter: "python", Path: "a.py",
			SHA256: testHookDigest("script")}}); err == nil {
		t.Fatal("declarative validation accepted a script specification")
	}
}

func TestEngineReplaceRejectsScriptRegistrationsOutsideOperatorSource(t *testing.T) {
	engine := NewEngine(nil)
	plugin := Registration{PluginID: "plugin-a", PluginFingerprint: testHookDigest("plugin"),
		Declaration: scriptRegistration(PreTool, FailureDeny, 100).Declaration}
	if err := engine.Replace([]Registration{plugin}); err == nil {
		t.Fatal("engine accepted a plugin-sourced script hook registration")
	}
	operator := scriptRegistration(PreTool, FailureDeny, 100)
	if err := engine.Replace([]Registration{operator}); err != nil {
		t.Fatalf("operator script registration was rejected: %v", err)
	}
}

func TestMergeRegistrationsEnforcesSharedDeclarationCeiling(t *testing.T) {
	plugin := make([]Registration, MaxDeclarations)
	for index := range plugin {
		plugin[index] = Registration{PluginID: fmt.Sprintf("plugin-%d", index),
			PluginFingerprint: testHookDigest("plugin"), Declaration: Declaration{
				ProtocolVersion: ProtocolVersion, ID: "hook", Event: RunStarted,
				Action: ActionRecord, FailurePolicy: FailureContinue, TimeoutMillis: 100}}
	}
	operator, err := MergeRegistrations(plugin, nil)
	if err != nil || len(operator) != MaxDeclarations {
		t.Fatalf("plugin-only registry at the ceiling failed: %d err=%v", len(operator), err)
	}
	if _, err := MergeRegistrations(plugin, []Registration{
		scriptRegistration(RunStarted, FailureDeny, 100)}); err == nil {
		t.Fatal("combined registry above the shared ceiling was accepted")
	}
	merged, err := MergeRegistrations(plugin[:MaxDeclarations-1],
		[]Registration{scriptRegistration(RunStarted, FailureDeny, 100)})
	if err != nil || len(merged) != MaxDeclarations {
		t.Fatalf("combined registry at the shared ceiling failed: %d err=%v", len(merged), err)
	}
}

func TestEngineScriptActionWithMissingExecutorAndContinuePolicySkips(t *testing.T) {
	engine := NewEngine(nil)
	if err := engine.Replace([]Registration{scriptRegistration(PreTool, FailureContinue, 100)}); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Execute(t.Context(), Input{Event: PreTool, ToolName: "shell",
		Payload: json.RawMessage(`{}`)})
	if err != nil || result.Denied || len(result.Executed) != 0 || len(result.Annotations) != 0 {
		t.Fatalf("missing executor with continue policy result=%#v err=%v", result, err)
	}
}

func TestEngineOutputLimitExcludesEchoedPayload(t *testing.T) {
	engine := NewEngine(nil)
	if err := engine.Replace([]Registration{{PluginID: "plugin-a",
		PluginFingerprint: testHookDigest("plugin"), Declaration: Declaration{
			ProtocolVersion: ProtocolVersion, ID: "annotate", Event: PreTool,
			Action: ActionAnnotate, FailurePolicy: FailureContinue, TimeoutMillis: 100,
			Message: "reviewed"}}}); err != nil {
		t.Fatal(err)
	}
	payload := `{"content":"` + strings.Repeat("x", MaxPayloadBytes-64) + `"}`
	result, err := engine.Execute(t.Context(), Input{Event: PreTool, ToolName: "file_write",
		Payload: json.RawMessage(payload)})
	if err != nil || result.Denied || len(result.Annotations) != 1 ||
		string(result.Payload) != payload {
		t.Fatalf("large payload failed hook execution: len=%d err=%v", len(result.Payload), err)
	}
}
