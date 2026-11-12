package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/hooks"
)

func TestSchemaV228AdmitsPostCompactionLifecycleHookAudits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v227-post-compaction.db")
	old := openHistoricalTestDatabase(t, path, 227)
	at := time.Now().UTC()
	if _, err := old.db.Exec(`INSERT INTO plugin_hook_audits(id,plugin_id,hook_id,event,run_id,workspace_id,tool_name,outcome,created_at) VALUES('old-audit','guard','hook-1','pre_tool','run-1','workspace-1','file_write','completed',?)`, ts(at)); err != nil {
		t.Fatal(err)
	}
	// Verify that under schema v227, inserting post_compaction fails CHECK constraint.
	if _, err := old.db.Exec(`INSERT INTO plugin_hook_audits(id,plugin_id,hook_id,event,run_id,workspace_id,tool_name,outcome,created_at) VALUES('rejected-audit','guard','hook-2','post_compaction','run-1','workspace-1','','completed',?)`, ts(at)); err == nil {
		t.Fatal("expected schema v227 to reject post_compaction event")
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	state, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	// 1. Historical receipt is preserved across migration v228.
	historical, err := state.ListHookAudits(t.Context(), "run-1", "workspace-1", 200)
	if err != nil || len(historical) != 1 || historical[0].ID != "old-audit" || historical[0].Event != hooks.PreTool {
		t.Fatalf("historical hook audit not preserved: %#v %v", historical, err)
	}

	// 2. Inserting post_compaction now succeeds.
	fp := strings.Repeat("b", 64)
	isFalse := false
	audit := hooks.AuditRecord{
		PluginID:          "watch-plugin",
		PluginFingerprint: fp,
		HookID:            "compaction-observer",
		Event:             hooks.PostCompaction,
		RunID:             "run-1",
		WorkspaceID:       "workspace-1",
		Action:            hooks.ActionRecord,
		Outcome:           "completed",
		Rejected:          &isFalse,
		CreatedAt:         at.Add(time.Second),
	}
	if err := state.RecordHookAudit(t.Context(), audit); err != nil {
		t.Fatalf("failed to record post_compaction hook audit in SQLite: %v", err)
	}

	// 3. Verify reading back the post_compaction audit.
	audits, err := state.ListHookAudits(t.Context(), "run-1", "workspace-1", 200)
	if err != nil || len(audits) != 2 {
		t.Fatalf("expected 2 hook audits, got %#v (err=%v)", audits, err)
	}
	var foundApproval bool
	for _, a := range audits {
		if a.Event == hooks.PostCompaction {
			foundApproval = true
			if a.PluginID != "watch-plugin" || a.Action != hooks.ActionRecord || a.Outcome != "completed" {
				t.Fatalf("unexpected approval audit fields: %#v", a)
			}
		}
	}
	if !foundApproval {
		t.Fatal("recorded post_compaction audit was not returned")
	}

	// 4. Verify invalid events are still rejected by the CHECK constraint.
	if _, err := state.db.Exec(`INSERT INTO plugin_hook_audits(id,plugin_id,hook_id,event,run_id,workspace_id,tool_name,outcome,created_at) VALUES('invalid-event','guard','hook-3','unknown_lifecycle_event','run-1','workspace-1','','completed',?)`, ts(at)); err == nil {
		t.Fatal("expected CHECK constraint to reject unknown_lifecycle_event")
	}

	// 5. Verify immutability triggers are preserved.
	if _, err := state.db.Exec(`UPDATE plugin_hook_audits SET outcome='failed_closed' WHERE id='old-audit'`); err == nil {
		t.Fatal("expected immutable update trigger to fail")
	}
	if _, err := state.db.Exec(`DELETE FROM plugin_hook_audits WHERE id='old-audit'`); err == nil {
		t.Fatal("expected immutable delete trigger to fail")
	}
}
