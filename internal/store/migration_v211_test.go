package store

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cyberagent-workbench/internal/llm"
)

func TestSchemaV211PreservesNativeCallsAndPlanTestFences(t *testing.T) {
	st := openHistoricalTestDatabase(t, filepath.Join(t.TempDir(), "long-project-tests.db"), 210)
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
	if err := st.applyMigration(t.Context(), migrationPlan()[210]); err != nil {
		t.Fatal(err)
	}
	after, err := st.ListSupervisorToolRounds(t.Context(), cp)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("native calls changed: %v", err)
	}
	var trigger string
	if err := st.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='trg_command_runtime_job_insert_scope'`).Scan(&trigger); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"call.tool_name='run_tests'", "call.name='run_tests'", "NEW.timeout_milliseconds<=25000", "$.project_test_source.workspace_version", "$.project_test_source.test_fingerprint", "NEW.timeout_milliseconds BETWEEN 1 AND 1800000", "$.command_fingerprints[0]", "pin.value=NEW.spec_fingerprint", "$.permission_snapshot_id", "$.adapter.backend_identity", "call.started_at IS NOT NULL", "call.charge_id=NEW.invocation_id"} {
		if !strings.Contains(trigger, required) {
			t.Fatalf("Plan test admission lost %q", required)
		}
	}
	if strings.Count(trigger, "json_extract(call.payload_json,'$.action')='start'") != 2 {
		t.Fatal("long Plan start must retain both native source branches")
	}
	assertNoForeignKeyViolations(t, st.db)
}
