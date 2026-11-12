package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSchemaV224PreservesHookReceiptsAndAdmitsPromptSubmitEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v223.db")
	old := openHistoricalTestDatabase(t, path, 223)
	at := time.Now().UTC()
	if _, err := old.db.Exec(`INSERT INTO plugin_hook_audits(id,plugin_id,hook_id,event,run_id,workspace_id,tool_name,outcome,created_at) VALUES('old','guard','same-hook','pre_tool','run-old','workspace-one','file_write','completed',?)`, ts(at)); err != nil {
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
	if err != nil || len(values) != 1 || values[0].Outcome != "completed" || values[0].Rejected != nil {
		t.Fatalf("historical receipt changed: %#v %v", values, err)
	}

	at = time.Now().UTC()
	if _, err := state.db.Exec(`INSERT INTO plugin_hook_audits(id,plugin_id,hook_id,event,run_id,workspace_id,tool_name,outcome,created_at,plugin_fingerprint,declared_action,rejected) VALUES('prompt','guard','prompt-guard','user_prompt_submit','run-new','workspace-one','','completed',?,?,?,0)`, ts(at), strings.Repeat("d", 64), "record"); err != nil {
		t.Fatalf("user_prompt_submit audit row was rejected: %v", err)
	}
	if _, err := state.db.Exec(`INSERT INTO plugin_hook_audits(id,plugin_id,hook_id,event,run_id,workspace_id,tool_name,outcome,created_at) VALUES('bad-event','guard','h','prompt_started','run-new','workspace-one','','completed',?)`, ts(at)); err == nil {
		t.Fatal("unknown event was accepted by the CHECK constraint")
	}
	if _, err := state.db.Exec(`UPDATE plugin_hook_audits SET outcome='failed_closed' WHERE id='prompt'`); err == nil {
		t.Fatal("immutable receipt changed")
	}
}
