package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
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
	mu      sync.Mutex
	records []AuditRecord
}

func (s *scriptedAuditSink) RecordHookAudit(_ context.Context, record AuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
	return nil
}

func (s *scriptedAuditSink) snapshot() []AuditRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]AuditRecord(nil), s.records...)
}

func (s *scriptedAuditSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
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

func TestDeclarationToolPatternValidation(t *testing.T) {
	valid := Declaration{ProtocolVersion: ProtocolVersion, ID: "mcp-guard",
		Event: PreTool, Action: ActionDeny, FailurePolicy: FailureDeny,
		TimeoutMillis: 100, ToolPattern: `^mcp_github_.*`,
		Message: "github tools disabled by reviewed plugin"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid tool pattern was rejected: %v", err)
	}
	cases := []struct {
		name        string
		toolNames   []string
		toolPattern string
	}{
		{"pattern with exact names", []string{"shell"}, "^mcp_.*"},
		{"oversized pattern", nil, "^" + strings.Repeat("a", MaxToolPatternBytes+1)},
		{"uncompilable pattern", nil, "^mcp_([a-z"},
		{"pattern with NUL", nil, "mcp\x00"},
		{"pattern with byte order mark", nil, "\uFEFF^mcp_.*"},
		{"pattern on non-tool event", nil, "^mcp_.*"},
	}
	for _, testCase := range cases {
		declaration := Declaration{ProtocolVersion: ProtocolVersion, ID: "guard",
			Event: PreTool, Action: ActionDeny, FailurePolicy: FailureDeny,
			TimeoutMillis: 100, ToolNames: testCase.toolNames,
			ToolPattern: testCase.toolPattern, Message: "blocked"}
		if testCase.name == "pattern on non-tool event" {
			declaration.Event = RunStarted
		}
		if err := declaration.Validate(); err == nil {
			t.Fatalf("%s: invalid tool pattern declaration was accepted", testCase.name)
		}
	}
}

func TestEngineToolPatternMatchesUnanchored(t *testing.T) {
	engine := NewEngine(nil)
	if err := engine.Replace([]Registration{{PluginID: "plugin-a",
		PluginFingerprint: testHookDigest("plugin"), Declaration: Declaration{
			ProtocolVersion: ProtocolVersion, ID: "deny-mcp-writes", Event: PreTool,
			Action: ActionDeny, FailurePolicy: FailureDeny, TimeoutMillis: 100,
			ToolPattern: `mcp_github_(create|delete)_`,
			Message:     "github write tools disabled"}}}); err != nil {
		t.Fatal(err)
	}
	denied, err := engine.Execute(t.Context(), Input{Event: PreTool,
		ToolName: "mcp_github_create_issue", Payload: json.RawMessage(`{}`)})
	if err != nil || !denied.Denied {
		t.Fatalf("pattern did not deny a matching tool: %#v err=%v", denied, err)
	}
	allowed, err := engine.Execute(t.Context(), Input{Event: PreTool,
		ToolName: "mcp_github_read_issue", Payload: json.RawMessage(`{}`)})
	if err != nil || allowed.Denied || len(allowed.Executed) != 0 {
		t.Fatalf("pattern denied a non-matching tool: %#v err=%v", allowed, err)
	}
	nonTool, err := engine.Execute(t.Context(), Input{Event: PreTool,
		ToolName: "shell", Payload: json.RawMessage(`{}`)})
	if err != nil || nonTool.Denied {
		t.Fatalf("pattern denied an unrelated tool: %#v err=%v", nonTool, err)
	}
}

func TestEngineToolPatternChangesRegistryGeneration(t *testing.T) {
	engine := NewEngine(nil)
	registration := Registration{PluginID: "plugin-a",
		PluginFingerprint: testHookDigest("plugin"), Declaration: Declaration{
			ProtocolVersion: ProtocolVersion, ID: "annotate", Event: PreTool,
			Action: ActionAnnotate, FailurePolicy: FailureContinue, TimeoutMillis: 100,
			ToolPattern: `^mcp_.*`, Message: "reviewed"}}
	if err := engine.Replace([]Registration{registration}); err != nil {
		t.Fatal(err)
	}
	generation := engine.Generation()
	registration.Declaration.ToolPattern = `^mcp_github_.*`
	if err := engine.Replace([]Registration{registration}); err != nil {
		t.Fatal(err)
	}
	if engine.Generation() == generation {
		t.Fatal("tool pattern change did not advance the registry generation")
	}
	// An invalid pattern that slips past validation fails closed at Replace.
	registration.Declaration.ToolPattern = "^mcp_([a-z"
	if err := engine.Replace([]Registration{registration}); err == nil {
		t.Fatal("uncompilable tool pattern was accepted by the registry")
	}
}

func TestEngineToolPatternOnPostToolAnnotates(t *testing.T) {
	engine := NewEngine(&scriptedAuditSink{})
	if err := engine.Replace([]Registration{{PluginID: "plugin-a",
		PluginFingerprint: testHookDigest("plugin"), Declaration: Declaration{
			ProtocolVersion: ProtocolVersion, ID: "annotate-writes", Event: PostTool,
			Action: ActionAnnotate, FailurePolicy: FailureContinue, TimeoutMillis: 100,
			ToolPattern: `mcp_github_(create|delete)_`,
			Message:     "github write completed"}}}); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Execute(t.Context(), Input{Event: PostTool, ToolName: "mcp_github_delete_branch",
		Payload: json.RawMessage(`{"status":"completed"}`)})
	if err != nil || result.Denied || len(result.Annotations) != 1 {
		t.Fatalf("post_tool pattern annotate result=%#v err=%v", result, err)
	}
	skipped, err := engine.Execute(t.Context(), Input{Event: PostTool, ToolName: "shell",
		Payload: json.RawMessage(`{"status":"completed"}`)})
	if err != nil || len(skipped.Annotations) != 0 {
		t.Fatalf("post_tool pattern matched an unrelated tool: %#v err=%v", skipped, err)
	}
}

func TestLoadOperatorScriptHooksAcceptsToolPatternDeclarations(t *testing.T) {
	root := t.TempDir()
	digest := writeOperatorScript(t, root, "scripts/check.py", "# operator script\n")
	declaration := fmt.Sprintf(`{"protocol_version":%q,"id":"mcp-guard","event":"pre_tool",
		"action":"script","failure_policy":"deny","timeout_ms":2000,
		"tool_pattern":"^mcp_github_(create|delete)_",
		"script":{"interpreter":"python","path":"scripts/check.py","sha256":%q}}`,
		ProtocolVersion, digest)
	if err := os.WriteFile(filepath.Join(root, "guard.json"), []byte(declaration), 0o644); err != nil {
		t.Fatal(err)
	}
	registrations, err := LoadOperatorScriptHooks(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(registrations) != 1 || registrations[0].Declaration.ToolPattern != `^mcp_github_(create|delete)_` {
		t.Fatalf("operator pattern declaration did not load: %#v", registrations)
	}
}

func TestEngineLoaderFailsClosedOnUncompilablePattern(t *testing.T) {
	registration := Registration{PluginID: "plugin-a",
		PluginFingerprint: testHookDigest("plugin"), Declaration: Declaration{
			ProtocolVersion: ProtocolVersion, ID: "annotate", Event: PreTool,
			Action: ActionAnnotate, FailurePolicy: FailureContinue, TimeoutMillis: 100,
			ToolPattern: "^mcp_([a-z", Message: "reviewed"}}
	engine := NewEngine(nil).WithLoader(func(context.Context) ([]Registration, error) {
		return []Registration{registration}, nil
	})
	_, err := engine.Execute(t.Context(), Input{Event: PreTool, ToolName: "mcp_github_read",
		Payload: json.RawMessage(`{}`)})
	if err == nil {
		t.Fatal("loader-provided uncompilable pattern did not fail the boundary closed")
	}
}

type gatedScriptExecutor struct {
	started chan struct{}
	release chan struct{}
}

func (g *gatedScriptExecutor) ExecuteScript(ctx context.Context,
	invocation ScriptInvocation,
) (ScriptOutput, error) {
	if g.started != nil {
		g.started <- struct{}{}
	}
	if g.release != nil {
		<-g.release
	}
	return ScriptOutput{ProtocolVersion: ProtocolVersion}, nil
}

func TestDeclarationAsyncValidation(t *testing.T) {
	spec := ScriptSpec{Interpreter: "python", Path: "tap.py", SHA256: testHookDigest("script")}
	if err := (Declaration{ProtocolVersion: ProtocolVersion, ID: "tap",
		Event: PreTool, Action: ActionScript, FailurePolicy: FailureContinue,
		TimeoutMillis: 100, Async: true, Script: &spec}).Validate(); err != nil {
		t.Fatalf("async script hook was rejected: %v", err)
	}
	if err := (Declaration{ProtocolVersion: ProtocolVersion, ID: "tap",
		Event: PostTool, Action: ActionRecord, FailurePolicy: FailureContinue,
		TimeoutMillis: 100, Async: true}).Validate(); err != nil {
		t.Fatalf("async post_tool record hook was rejected: %v", err)
	}
	for _, broken := range []struct {
		name   string
		action Action
	}{
		{"async deny", ActionDeny},
		{"async annotate", ActionAnnotate},
		{"async narrow", ActionNarrow},
	} {
		declaration := Declaration{ProtocolVersion: ProtocolVersion, ID: "tap",
			Event: PreTool, Action: broken.action, FailurePolicy: FailureDeny,
			TimeoutMillis: 100, Async: true, Message: "blocked"}
		if err := declaration.Validate(); err == nil {
			t.Fatalf("%s was accepted asynchronously", broken.name)
		}
	}
}

func TestEngineAsyncScriptRunsAfterBoundaryReturns(t *testing.T) {
	executor := &gatedScriptExecutor{started: make(chan struct{}, 1), release: make(chan struct{})}
	sink := &scriptedAuditSink{}
	engine := NewEngine(sink).WithScriptExecutor(executor)
	if err := engine.Replace([]Registration{{PluginID: OperatorPluginID,
		PluginFingerprint: testHookDigest("plugin"), Declaration: Declaration{
			ProtocolVersion: ProtocolVersion, ID: "tap", Event: PreTool,
			Action: ActionScript, FailurePolicy: FailureContinue, TimeoutMillis: 5_000,
			Async: true, Script: &ScriptSpec{Interpreter: "python", Path: "tap.py",
				SHA256: testHookDigest("script")}}}}); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Execute(t.Context(), Input{Event: PreTool, ToolName: "shell",
		Payload: json.RawMessage(`{"path":"a"}`)})
	if err != nil || result.Denied || len(result.Executed) != 0 || len(result.Annotations) != 0 {
		t.Fatalf("async hook leaked into the synchronous result: %#v err=%v", result, err)
	}
	select {
	case <-executor.started:
	case <-time.After(2 * time.Second):
		t.Fatal("async script never started")
	}
	close(executor.release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sink.count() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	records := sink.snapshot()
	if len(records) != 1 || records[0].Outcome != "completed" ||
		records[0].Rejected == nil || *records[0].Rejected {
		t.Fatalf("async audit records=%#v", records)
	}
}

func TestEngineAsyncRecordHookAuditsWithoutBlockingResult(t *testing.T) {
	sink := &scriptedAuditSink{}
	engine := NewEngine(sink)
	if err := engine.Replace([]Registration{{PluginID: "plugin-a",
		PluginFingerprint: testHookDigest("plugin"), Declaration: Declaration{
			ProtocolVersion: ProtocolVersion, ID: "tap", Event: RunCompleted,
			Action: ActionRecord, FailurePolicy: FailureContinue, TimeoutMillis: 100,
			Async: true}}}); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Execute(t.Context(), Input{Event: RunCompleted, RunID: "run-1"})
	if err != nil || len(result.Executed) != 0 {
		t.Fatalf("async record hook touched the result: %#v err=%v", result, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sink.count() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	records := sink.snapshot()
	if len(records) != 1 || records[0].Event != RunCompleted ||
		records[0].Outcome != "completed" {
		t.Fatalf("async record audit records=%#v", records)
	}
}

func TestEngineAsyncHooksSkipWhenConcurrencyCeilingReached(t *testing.T) {
	executor := &gatedScriptExecutor{
		started: make(chan struct{}, MaxAsyncConcurrent),
		release: make(chan struct{}),
	}
	sink := &scriptedAuditSink{}
	engine := NewEngine(sink).WithScriptExecutor(executor)
	registrations := make([]Registration, 0, MaxAsyncConcurrent+1)
	for index := 0; index < MaxAsyncConcurrent+1; index++ {
		registrations = append(registrations, Registration{PluginID: OperatorPluginID,
			PluginFingerprint: testHookDigest("plugin"), Declaration: Declaration{
				ProtocolVersion: ProtocolVersion,
				ID:              fmt.Sprintf("tap-%d", index), Event: PreTool,
				Action: ActionScript, FailurePolicy: FailureContinue, TimeoutMillis: 5_000,
				Async: true, Script: &ScriptSpec{Interpreter: "python",
					Path:   fmt.Sprintf("tap-%d.py", index),
					SHA256: testHookDigest(fmt.Sprintf("tap-%d", index))}}})
	}
	if err := engine.Replace(registrations); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Execute(t.Context(), Input{Event: PreTool, ToolName: "shell",
		Payload: json.RawMessage(`{}`)})
	if err != nil || result.Denied {
		t.Fatalf("async ceiling execute failed: %#v err=%v", result, err)
	}
	started := 0
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		started = len(executor.started)
		if started == MaxAsyncConcurrent {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if started != MaxAsyncConcurrent {
		t.Fatalf("expected %d concurrent async hooks, saw %d", MaxAsyncConcurrent, started)
	}
	// Only the ceiling-exceeded hook has settled; the eight running taps have
	// not finished and have written nothing yet.
	skipDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(skipDeadline) && sink.count() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	settled := sink.snapshot()
	if len(settled) != 1 || settled[0].Outcome != "failed_continue" {
		t.Fatalf("expected exactly one skip audit row: %#v", settled)
	}
	close(executor.release)
	auditDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(auditDeadline) && sink.count() < MaxAsyncConcurrent+1 {
		time.Sleep(10 * time.Millisecond)
	}
	records := sink.snapshot()
	if len(records) != MaxAsyncConcurrent+1 {
		t.Fatalf("expected an audit row for every async hook: %d rows", len(records))
	}
	for _, record := range records[1:] {
		if record.Outcome != "completed" || record.Rejected == nil || *record.Rejected {
			t.Fatalf("released async hook audit is wrong: %#v", record)
		}
	}
}

func TestEngineAsyncScriptFailureRecordsFailedContinue(t *testing.T) {
	executor := &stubScriptExecutor{err: errors.New("interpreter unavailable")}
	sink := &scriptedAuditSink{}
	engine := NewEngine(sink).WithScriptExecutor(executor)
	if err := engine.Replace([]Registration{{PluginID: OperatorPluginID,
		PluginFingerprint: testHookDigest("script"), Declaration: Declaration{
			ProtocolVersion: ProtocolVersion, ID: "tap", Event: RunStarted,
			Action: ActionScript, FailurePolicy: FailureContinue, TimeoutMillis: 100,
			Async: true, Script: &ScriptSpec{Interpreter: "python", Path: "tap.py",
				SHA256: testHookDigest("script")}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Execute(t.Context(), Input{Event: RunStarted, RunID: "run-1"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sink.count() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	records := sink.snapshot()
	if len(records) != 1 || records[0].Outcome != "failed_continue" ||
		records[0].Rejected == nil || *records[0].Rejected {
		t.Fatalf("async failure audit records=%#v", records)
	}
}

type payloadCapturingExecutor struct {
	mu          sync.Mutex
	invocations []ScriptInvocation
}

func (c *payloadCapturingExecutor) ExecuteScript(_ context.Context,
	invocation ScriptInvocation,
) (ScriptOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invocations = append(c.invocations, invocation)
	return ScriptOutput{ProtocolVersion: ProtocolVersion}, nil
}

func TestEngineAsyncScriptReceivesPayloadAndSurvivesTimeoutAudit(t *testing.T) {
	capturing := &payloadCapturingExecutor{}
	sink := &scriptedAuditSink{}
	engine := NewEngine(sink).WithScriptExecutor(capturing)
	if err := engine.Replace([]Registration{{PluginID: OperatorPluginID,
		PluginFingerprint: testHookDigest("script"), Declaration: Declaration{
			ProtocolVersion: ProtocolVersion, ID: "tap", Event: PreTool,
			Action: ActionScript, FailurePolicy: FailureContinue, TimeoutMillis: 100,
			Async: true, ToolPattern: `^mcp_.*`,
			Script: &ScriptSpec{Interpreter: "python", Path: "tap.py",
				SHA256: testHookDigest("script")}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Execute(t.Context(), Input{Event: PreTool, ToolName: "mcp_github_read",
		Payload: json.RawMessage(`{"path":"a"}`)}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sink.count() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	records := sink.snapshot()
	if len(records) != 1 || records[0].Outcome != "completed" {
		t.Fatalf("async tap audit records=%#v", records)
	}
	if len(capturing.invocations) != 1 ||
		string(capturing.invocations[0].Payload) != `{"path":"a"}` {
		t.Fatalf("background script did not receive the payload: %#v",
			capturing.invocations)
	}
}

func TestEngineAsyncTimeoutStillWritesAudit(t *testing.T) {
	sink := &scriptedAuditSink{}
	engine := NewEngine(sink).WithScriptExecutor(waitingScriptExecutor{})
	if err := engine.Replace([]Registration{{PluginID: OperatorPluginID,
		PluginFingerprint: testHookDigest("script"), Declaration: Declaration{
			ProtocolVersion: ProtocolVersion, ID: "slow-tap", Event: PreTool,
			Action: ActionScript, FailurePolicy: FailureContinue, TimeoutMillis: 80,
			Async: true, Script: &ScriptSpec{Interpreter: "python", Path: "slow.py",
				SHA256: testHookDigest("script")}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Execute(t.Context(), Input{Event: PreTool, ToolName: "shell",
		Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && sink.count() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	records := sink.snapshot()
	if len(records) != 1 || records[0].Outcome != "failed_continue" ||
		records[0].Rejected == nil || *records[0].Rejected {
		t.Fatalf("timed-out async hook audit records=%#v", records)
	}
}

func TestDeclarationAsyncRejectsFailClosedPolicy(t *testing.T) {
	spec := ScriptSpec{Interpreter: "python", Path: "tap.py", SHA256: testHookDigest("script")}
	if err := (Declaration{ProtocolVersion: ProtocolVersion, ID: "tap",
		Event: PreTool, Action: ActionScript, FailurePolicy: FailureDeny,
		TimeoutMillis: 100, Async: true, Script: &spec}).Validate(); err == nil {
		t.Fatal("async script hook accepted the fail-closed policy")
	}
}

type slowAuditSink struct {
	delay time.Duration
	scriptedAuditSink
}

func (s *slowAuditSink) RecordHookAudit(ctx context.Context, record AuditRecord) error {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.scriptedAuditSink.RecordHookAudit(ctx, record)
}

func TestEngineAsyncCeilingDoesNotBlockCallerWhenAuditSinkIsSlow(t *testing.T) {
	executor := &gatedScriptExecutor{
		started: make(chan struct{}, MaxAsyncConcurrent),
		release: make(chan struct{}),
	}
	defer close(executor.release)
	sink := &slowAuditSink{delay: 300 * time.Millisecond}
	engine := NewEngine(sink).WithScriptExecutor(executor)
	registrations := make([]Registration, 0, MaxAsyncConcurrent+1)
	for index := 0; index < MaxAsyncConcurrent+1; index++ {
		registrations = append(registrations, Registration{PluginID: OperatorPluginID,
			PluginFingerprint: testHookDigest("plugin"), Declaration: Declaration{
				ProtocolVersion: ProtocolVersion,
				ID:              fmt.Sprintf("slow-audit-tap-%d", index), Event: PreTool,
				Action: ActionScript, FailurePolicy: FailureContinue, TimeoutMillis: 5_000,
				Async: true, Script: &ScriptSpec{Interpreter: "python",
					Path:   fmt.Sprintf("tap-%d.py", index),
					SHA256: testHookDigest(fmt.Sprintf("tap-%d", index))}}})
	}
	if err := engine.Replace(registrations); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	result, err := engine.Execute(t.Context(), Input{Event: PreTool, ToolName: "shell",
		Payload: json.RawMessage(`{}`)})
	elapsed := time.Since(start)
	if err != nil || result.Denied {
		t.Fatalf("async ceiling execute failed: %#v err=%v", result, err)
	}
	if elapsed >= 150*time.Millisecond {
		t.Fatalf("async boundary blocked caller for %v (expected < 150ms)", elapsed)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sink.count() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	records := sink.snapshot()
	if len(records) == 0 || records[0].Outcome != "failed_continue" {
		t.Fatalf("expected background skip audit row, got %#v", records)
	}
}
