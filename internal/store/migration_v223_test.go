package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/hooks"
)

func TestSchemaV223PreservesHookReceiptsAndAdmitsScriptBindings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v222.db")
	old := openHistoricalTestDatabase(t, path, 222)
	at := time.Now().UTC()
	if _, err := old.db.Exec(`INSERT INTO plugin_hook_audits(id,plugin_id,hook_id,event,run_id,workspace_id,tool_name,outcome,created_at) VALUES('old','guard','same-hook','pre_tool','run-old','workspace-one','file_write','completed',?)`, ts(at)); err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.Exec(`INSERT INTO plugin_hook_audits(id,plugin_id,hook_id,event,run_id,workspace_id,tool_name,outcome,created_at,plugin_fingerprint,declared_action,rejected) VALUES('old-deny','guard','deny-hook','pre_tool','run-old','workspace-one','shell','completed',?,?,?,1)`, ts(at), strings.Repeat("a", 64), "deny"); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	values, err := state.ListHookAudits(t.Context(), "run-old", "workspace-one", 200)
	if err != nil || len(values) != 2 {
		t.Fatalf("historical receipts changed: %#v %v", values, err)
	}
	if values[0].Action != hooks.ActionDeny || values[0].Rejected == nil || !*values[0].Rejected {
		t.Fatalf("bound historical receipt changed: %#v", values[0])
	}
	if values[1].Outcome != "completed" || values[1].Rejected != nil ||
		values[1].PluginFingerprint != "" {
		t.Fatalf("unbound historical receipt changed: %#v", values[1])
	}

	scriptDigest := strings.Repeat("b", 64)
	registration := hooks.Registration{PluginID: hooks.OperatorPluginID,
		PluginFingerprint: scriptDigest,
		Declaration: hooks.Declaration{ProtocolVersion: hooks.ProtocolVersion,
			ID: "operator-guard", Event: hooks.PreTool, Action: hooks.ActionScript,
			FailurePolicy: hooks.FailureDeny, TimeoutMillis: 100,
			Script: &hooks.ScriptSpec{Interpreter: "python", Path: "check.py",
				SHA256: scriptDigest}}}
	engine := hooks.NewEngine(state).WithScriptExecutor(stubScriptAuditExecutor{denied: true})
	if err := engine.Replace([]hooks.Registration{registration}); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Execute(t.Context(), hooks.Input{Event: hooks.PreTool,
		RunID: "run-new", WorkspaceID: "workspace-one", ToolName: "shell"})
	if err != nil || !result.Denied {
		t.Fatalf("script denial result=%#v err=%v", result, err)
	}
	values, err = state.ListHookAudits(t.Context(), "run-new", "workspace-one", 200)
	if err != nil || len(values) != 1 || values[0].Action != hooks.ActionScript ||
		values[0].Outcome != "completed" || values[0].Rejected == nil || !*values[0].Rejected ||
		values[0].PluginFingerprint != scriptDigest {
		t.Fatalf("script denial receipt=%#v err=%v", values, err)
	}

	if _, err := state.db.Exec(`INSERT INTO plugin_hook_audits(id,plugin_id,hook_id,event,run_id,workspace_id,tool_name,outcome,created_at,plugin_fingerprint,declared_action,rejected) VALUES('bad-binding','operator','h','pre_tool','run-new','workspace-one','shell','failed_continue',?,?,?,1)`, ts(at), scriptDigest, "script"); err == nil {
		t.Fatal("contradictory script binding was accepted by the trigger")
	}
	if _, err := state.db.Exec(`INSERT INTO plugin_hook_audits(id,plugin_id,hook_id,event,run_id,workspace_id,tool_name,outcome,created_at,plugin_fingerprint,declared_action,rejected) VALUES('bad-action','operator','h','pre_tool','run-new','workspace-one','shell','completed',?,?,?,1)`, ts(at), strings.Repeat("c", 64), "explode"); err == nil {
		t.Fatal("unknown declared action was accepted by the CHECK constraint")
	}
	if _, err := state.db.Exec(`UPDATE plugin_hook_audits SET rejected=0 WHERE id=?`, values[0].ID); err == nil {
		t.Fatal("immutable receipt changed")
	}
}
