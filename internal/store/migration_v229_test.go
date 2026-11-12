package store

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/hooks"
)

func TestSchemaV229PreservesHookAuditsAndAcceptsCapOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v228.db")
	old := openHistoricalTestDatabase(t, path, 228)
	at := time.Now().UTC()
	if _, err := old.db.Exec(`INSERT INTO plugin_hook_audits(id,plugin_id,hook_id,event,run_id,workspace_id,tool_name,outcome,created_at) VALUES('old','guard','same-hook','run_completed','run-old','workspace-one','','completed',?)`, ts(at)); err != nil {
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
	// Historical rows survive the outcome CHECK rebuild.
	values, err := state.ListHookAudits(t.Context(), "run-old", "workspace-one", 200)
	if err != nil || len(values) != 1 || values[0].Outcome != "completed" || values[0].Rejected != nil {
		t.Fatalf("historical receipt changed: %#v %v", values, err)
	}
	// A post-occurrence fire that caps its observation output records the
	// drop through the store sink and the rebuilt CHECK accepts it.
	engine := hooks.NewEngine(state)
	fingerprint := strings.Repeat("b", 64)
	registrations := make([]hooks.Registration, 0, hooks.MaxAnnotations+4)
	for i := 0; i < hooks.MaxAnnotations+4; i++ {
		registrations = append(registrations, hooks.Registration{PluginID: "guard",
			PluginFingerprint: fingerprint, Declaration: hooks.Declaration{
				ProtocolVersion: hooks.ProtocolVersion, ID: fmt.Sprintf("annotate-%02d", i),
				Event: hooks.RunCompleted, Action: hooks.ActionAnnotate,
				FailurePolicy: hooks.FailureContinue, TimeoutMillis: 100,
				Message: fmt.Sprintf("observation %d", i)}})
	}
	if err := engine.Replace(registrations); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Execute(t.Context(), hooks.Input{Event: hooks.RunCompleted,
		RunID: "run-new", WorkspaceID: "workspace-one"})
	if err != nil || len(result.Executed) != hooks.MaxAnnotations+4 || len(result.Annotations) != hooks.MaxAnnotations {
		t.Fatalf("capped fire: result=%#v err=%v", result, err)
	}
	values, err = state.ListHookAudits(t.Context(), "run-new", "workspace-one", 200)
	if err != nil {
		t.Fatal(err)
	}
	capped := 0
	for _, value := range values {
		if value.Outcome == "output_capped" {
			capped++
			if value.Rejected == nil || *value.Rejected {
				t.Fatalf("capped receipt decision binding: %#v", value)
			}
		}
	}
	if capped == 0 {
		t.Fatalf("cap drop was not audited: %#v", values)
	}
	// The rebuilt CHECK still rejects unknown outcomes.
	if _, err := state.db.Exec(`INSERT INTO plugin_hook_audits(id,plugin_id,hook_id,event,outcome,created_at) VALUES('bogus','guard','h','run_completed','failed_other',?)`, ts(at)); err == nil {
		t.Fatal("unknown outcome accepted by rebuilt CHECK")
	}
}

func TestSchemaV229PreservesAllHookEventsAndScriptDecisions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v228-all-hooks.db")
	old := openHistoricalTestDatabase(t, path, 228)
	at := time.Now().UTC()
	events := []hooks.Event{hooks.UserPromptSubmit, hooks.ApprovalRequested, hooks.PostToolFailure, hooks.TurnEnd, hooks.PostCompaction}
	for i, event := range events {
		if _, err := old.db.Exec(`INSERT INTO plugin_hook_audits(id,plugin_id,hook_id,event,outcome,created_at,plugin_fingerprint,declared_action,rejected) VALUES(?, 'guard','script-guard',?,'completed',?,?,'script',1)`, fmt.Sprintf("old-%d", i), string(event), ts(at), strings.Repeat("b", 64)); err != nil {
			t.Fatal(err)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	audits, err := state.ListHookAudits(t.Context(), "", "", 20)
	if err != nil || len(audits) != len(events) {
		t.Fatalf("cumulative audits=%+v err=%v", audits, err)
	}
	for _, audit := range audits {
		if audit.Action != hooks.ActionScript || audit.Rejected == nil || !*audit.Rejected {
			t.Fatalf("script decision lost: %+v", audit)
		}
	}
	for i, event := range events {
		if _, err := state.db.Exec(`INSERT INTO plugin_hook_audits(id,plugin_id,hook_id,event,outcome,created_at,plugin_fingerprint,declared_action,rejected) VALUES(?, 'guard','record-guard',?,'output_capped',?,?,'record',0)`, fmt.Sprintf("cap-%d", i), string(event), ts(at), strings.Repeat("b", 64)); err != nil {
			t.Fatalf("lost earlier event %s: %v", event, err)
		}
	}
	if _, err := state.db.Exec(`UPDATE plugin_hook_audits SET rejected=0 WHERE id='old-0'`); err == nil {
		t.Fatal("old audit became mutable")
	}
}
