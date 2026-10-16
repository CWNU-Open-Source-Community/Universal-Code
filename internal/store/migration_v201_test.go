package store

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cyberagent-workbench/internal/llm"
)

func TestSchemaV201PreservesSupervisorCallsAndActorReferences(t *testing.T) {
	st := openHistoricalTestDatabase(t, filepath.Join(t.TempDir(), "project-tests.db"), 200)
	defer st.Close()
	f := providerReplayFixtureAtStore(t, st)
	f.start(t)
	f.attempt.Outcome = llm.OutcomeSuccess
	cp, err := st.RecordSupervisorModelCompleted(t.Context(), f.turn.Checkpoint, f.attempt, f.response)
	if err != nil {
		t.Fatal(err)
	}
	before, err := st.ListSupervisorToolRounds(t.Context(), cp)
	if err != nil {
		t.Fatal(err)
	}
	var rowBefore int64
	if err := st.db.QueryRow(`SELECT rowid FROM run_supervisor_tool_calls WHERE call_id=?`, f.response.ToolCalls[0].ID).Scan(&rowBefore); err != nil {
		t.Fatal(err)
	}
	if err := st.applyMigration(t.Context(), migrationPlan()[200]); err != nil {
		t.Fatal(err)
	}
	after, err := st.ListSupervisorToolRounds(t.Context(), cp)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("stored call or actor changed: %v", err)
	}
	var rowAfter int64
	if err := st.db.QueryRow(`SELECT rowid FROM run_supervisor_tool_calls WHERE call_id=?`, f.response.ToolCalls[0].ID).Scan(&rowAfter); err != nil || rowBefore != rowAfter {
		t.Fatalf("rowid changed: %d/%d %v", rowBefore, rowAfter, err)
	}
	var actorSchema string
	if err := st.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='run_supervisor_tool_call_agents'`).Scan(&actorSchema); err != nil || strings.Contains(actorSchema, "_v200") || strings.Contains(actorSchema, "_v201") {
		t.Fatalf("actor FK retargeted: %s %v", actorSchema, err)
	}
	assertNoForeignKeyViolations(t, st.db)
	var toolSchema string
	if err := st.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='run_supervisor_tool_calls'`).Scan(&toolSchema); err != nil || !strings.Contains(toolSchema, "'run_tests'") {
		t.Fatalf("test tool missing: %v", err)
	}
}
