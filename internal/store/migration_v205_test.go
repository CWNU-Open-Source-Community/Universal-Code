package store

import (
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/llm"
)

func TestSchemaV203PreservesRoundsAndNativeModelUniqueness(t *testing.T) {
	st := openHistoricalTestDatabase(t, filepath.Join(t.TempDir(), "project-hooks.db"), 202)
	defer st.Close()
	f := providerReplayFixtureAtStore(t, st)
	f.start(t)
	f.attempt.Outcome = llm.OutcomeSuccess
	f.response.Replay = testProviderReplay(t, f.response.ToolCalls, "retained-native-replay")
	cp, err := st.RecordSupervisorModelCompleted(t.Context(), f.turn.Checkpoint, f.attempt, f.response)
	if err != nil {
		t.Fatal(err)
	}
	var rowBefore, callBefore int64
	if err := st.db.QueryRow(`SELECT rowid FROM run_supervisor_tool_rounds WHERE run_id=?`, cp.RunID).Scan(&rowBefore); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(`SELECT rowid FROM run_supervisor_tool_calls WHERE call_id=?`, f.response.ToolCalls[0].ID).Scan(&callBefore); err != nil {
		t.Fatal(err)
	}
	var replayBefore, replayAfter string
	var replayRowBefore, replayRowAfter int64
	if err := st.db.QueryRow(`SELECT rowid,CAST(replay_blob AS TEXT) FROM run_supervisor_provider_replay WHERE run_id=?`, cp.RunID).Scan(&replayRowBefore, &replayBefore); err != nil {
		t.Fatal(err)
	}
	if err := st.applyMigration(t.Context(), migrationPlan()[202]); err != nil {
		t.Fatal(err)
	}
	var rowAfter, callAfter int64
	var origin string
	if err := st.db.QueryRow(`SELECT rowid,origin FROM run_supervisor_tool_rounds WHERE run_id=?`, cp.RunID).Scan(&rowAfter, &origin); err != nil || rowAfter != rowBefore || origin != "model" {
		t.Fatalf("historical round changed: before=%d after=%d origin=%q err=%v", rowBefore, rowAfter, origin, err)
	}
	if err := st.db.QueryRow(`SELECT rowid FROM run_supervisor_tool_calls WHERE call_id=?`, f.response.ToolCalls[0].ID).Scan(&callAfter); err != nil || callAfter != callBefore {
		t.Fatalf("historical call changed: before=%d after=%d err=%v", callBefore, callAfter, err)
	}
	if err := st.db.QueryRow(`SELECT rowid,CAST(replay_blob AS TEXT) FROM run_supervisor_provider_replay WHERE run_id=?`, cp.RunID).Scan(&replayRowAfter, &replayAfter); err != nil || replayRowBefore != replayRowAfter || replayBefore != replayAfter {
		t.Fatalf("historical replay changed: before=%d after=%d err=%v", replayRowBefore, replayRowAfter, err)
	}
	// The same real model output cannot create two native tool rounds, but a
	// host hook can record a distinct round with that output as its source.
	insert := `INSERT INTO run_supervisor_tool_rounds(run_id,turn,attempt_id,round,model_attempt,origin,created_at)
		SELECT run_id,turn,attempt_id,round+1,model_attempt,?,created_at FROM run_supervisor_tool_rounds WHERE run_id=? AND round=1`
	if _, err := st.db.Exec(insert, "model", cp.RunID); err == nil {
		t.Fatal("duplicate native model round accepted")
	}
	if _, err := st.db.Exec(insert, "project_hook", cp.RunID); err != nil {
		t.Fatalf("host source rejected: %v", err)
	}
	assertNoForeignKeyViolations(t, st.db)
}
