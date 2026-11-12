package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/hooks"
)

type stubScriptAuditExecutor struct {
	denied bool
}

func (s stubScriptAuditExecutor) ExecuteScript(context.Context,
	hooks.ScriptInvocation,
) (hooks.ScriptOutput, error) {
	output := hooks.ScriptOutput{ProtocolVersion: hooks.ProtocolVersion}
	if s.denied {
		output.Denied = true
		output.Reason = "script denied the call"
	} else {
		output.Annotations = []hooks.ScriptAnnotation{{Message: "script annotation"}}
	}
	return output, nil
}

func operatorScriptRegistration(action hooks.Action) hooks.Registration {
	declaration := hooks.Declaration{ProtocolVersion: hooks.ProtocolVersion,
		ID: "operator-guard", Event: hooks.PreTool, Action: action,
		FailurePolicy: hooks.FailureDeny, TimeoutMillis: 100}
	if action == hooks.ActionScript {
		declaration.Script = &hooks.ScriptSpec{Interpreter: "python", Path: "check.py",
			SHA256: strings.Repeat("b", 64)}
	}
	return hooks.Registration{PluginID: hooks.OperatorPluginID,
		PluginFingerprint: strings.Repeat("b", 64), Declaration: declaration}
}

func TestHookAuditBindsScriptDenialsAndAnnotations(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "script-hooks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	engine := hooks.NewEngine(state).WithScriptExecutor(stubScriptAuditExecutor{denied: true})
	if err := engine.Replace([]hooks.Registration{
		operatorScriptRegistration(hooks.ActionScript)}); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Execute(t.Context(), hooks.Input{Event: hooks.PreTool,
		RunID: "run-script", WorkspaceID: "workspace-one", ToolName: "file_write"})
	if err != nil || !result.Denied {
		t.Fatalf("script denial result=%#v err=%v", result, err)
	}
	values, err := state.ListHookAudits(t.Context(), "run-script", "workspace-one", 10)
	if err != nil || len(values) != 1 {
		t.Fatalf("script denial audit=%#v err=%v", values, err)
	}
	record := values[0]
	if record.Action != hooks.ActionScript || record.Outcome != "completed" ||
		record.Rejected == nil || !*record.Rejected ||
		record.PluginID != hooks.OperatorPluginID ||
		record.PluginFingerprint != strings.Repeat("b", 64) {
		t.Fatalf("script denial audit binding is wrong: %#v", record)
	}

	if err := engine.Replace([]hooks.Registration{
		operatorScriptRegistration(hooks.ActionScript)}); err != nil {
		t.Fatal(err)
	}
	engine = hooks.NewEngine(state).WithScriptExecutor(stubScriptAuditExecutor{denied: false})
	if err := engine.Replace([]hooks.Registration{
		operatorScriptRegistration(hooks.ActionScript)}); err != nil {
		t.Fatal(err)
	}
	result, err = engine.Execute(t.Context(), hooks.Input{Event: hooks.PreTool,
		RunID: "run-script", WorkspaceID: "workspace-one", ToolName: "file_write"})
	if err != nil || result.Denied || len(result.Annotations) != 1 {
		t.Fatalf("script annotation result=%#v err=%v", result, err)
	}
	values, err = state.ListHookAudits(t.Context(), "run-script", "workspace-one", 10)
	if err != nil || len(values) != 2 {
		t.Fatalf("script annotation audit=%#v err=%v", values, err)
	}
	record = values[0]
	if record.Action != hooks.ActionScript || record.Outcome != "completed" ||
		record.Rejected == nil || *record.Rejected {
		t.Fatalf("script annotation audit binding is wrong: %#v", record)
	}
}

func TestHookAuditRejectsInvalidScriptDecisionBindings(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "script-audit-bad.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	at := time.Now().UTC()
	cases := []struct {
		name     string
		outcome  string
		rejected bool
	}{
		{"script failed_closed must be rejected", "failed_closed", false},
		{"script failed_continue cannot be rejected", "failed_continue", true},
	}
	for _, testCase := range cases {
		rejected := testCase.rejected
		err := state.RecordHookAudit(t.Context(), hooks.AuditRecord{
			PluginID: hooks.OperatorPluginID, PluginFingerprint: strings.Repeat("b", 64),
			Action: hooks.ActionScript, Rejected: &rejected, HookID: "operator-guard",
			Event: hooks.PreTool, RunID: "run-1", WorkspaceID: "workspace-one",
			ToolName: "file_write", Outcome: testCase.outcome, CreatedAt: at})
		if err == nil {
			t.Fatalf("%s: invalid script audit binding was accepted", testCase.name)
		}
	}
}
