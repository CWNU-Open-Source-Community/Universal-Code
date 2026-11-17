package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/toolgateway"
)

func TestSchemaV239UpgradePreservesRowsAndWidensBatchAndRoundLimits(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "throughput-v238.db")
	st := openHistoricalTestDatabase(t, path, 238)

	_, run := createStructuredToolTestRun(t, ctx, st, "v239 migration")
	if _, err := application.NewRunService(st).Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	turn, err := st.BeginSupervisorTurn(ctx, acquireTestRunExecutionLease(t, ctx, st, run.ID), "preserve v238 rows")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := toolgateway.NormalizeStructuredMemoryPayload(toolgateway.NoteCreateTool, json.RawMessage(`{"title":"keep","content":"immutable v238 note"}`))
	if err != nil {
		t.Fatal(err)
	}
	key := runmutation.SupervisorToolOperationKey(run.ID, 1, "note_create", string(payload))
	callID, err := runmutation.SupervisorToolCallID(key, 1)
	if err != nil {
		t.Fatal(err)
	}
	attempt := llm.ModelAttempt{Number: 1, TransportAttempt: 1, MaxAttempts: 1, Provider: "test", Model: "model"}
	if _, err := st.RecordSupervisorModelStarted(ctx, turn.Checkpoint, attempt); err != nil {
		t.Fatal(err)
	}
	attempt.Outcome = llm.OutcomeSuccess
	resp := llm.ChatResponse{
		Provider: "test", Model: "model",
		Usage:     llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
		ToolCalls: []llm.ToolCall{{ID: callID, Name: "note_create", Arguments: payload}},
		Replay:    testProviderReplay(t, []llm.ToolCall{{ID: callID, Name: "note_create", Arguments: payload}}, "v238-replay"),
	}
	cp, err := st.RecordSupervisorModelCompleted(ctx, turn.Checkpoint, attempt, resp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE run_supervisor_tool_calls SET rowid=91 WHERE call_id=?`, callID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordSupervisorToolExecutionStarted(ctx, cp, callID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RecordSupervisorToolResult(ctx, cp, domain.SupervisorToolResult{
		CallID: callID, Status: domain.SupervisorToolCompleted, ResultJSON: `{"status":"completed"}`, CompletedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	beforeCalls := readV165SupervisorCallRows(t, ctx, st, run.ID)
	beforeRounds := legacyFixtureRows(t, st, "run_supervisor_tool_rounds")
	beforeActors := legacyFixtureRows(t, st, "run_supervisor_tool_call_agents")
	beforeReplay := legacyFixtureRows(t, st, "run_supervisor_provider_replay")
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(path)
	if err != nil {
		t.Fatalf("upgrade v238->v239: %v", err)
	}
	defer upgraded.Close()

	version, err := upgraded.SchemaVersion(ctx)
	if err != nil || version != 239 {
		t.Fatalf("schema version=%d err=%v, want 239", version, err)
	}
	assertLatestMigrationLedger(t, upgraded, migrationPlan())
	assertNoForeignKeyViolations(t, upgraded.db)

	if !reflect.DeepEqual(beforeCalls, readV165SupervisorCallRows(t, ctx, upgraded, run.ID)) {
		t.Fatal("v239 changed existing run_supervisor_tool_calls rows or rowids")
	}
	if !reflect.DeepEqual(beforeRounds, legacyFixtureRows(t, upgraded, "run_supervisor_tool_rounds")) {
		t.Fatal("v239 changed existing run_supervisor_tool_rounds rows or rowids")
	}
	if !reflect.DeepEqual(beforeActors, legacyFixtureRows(t, upgraded, "run_supervisor_tool_call_agents")) {
		t.Fatal("v239 changed existing run_supervisor_tool_call_agents rows")
	}
	if !reflect.DeepEqual(beforeReplay, legacyFixtureRows(t, upgraded, "run_supervisor_provider_replay")) {
		t.Fatal("v239 changed existing run_supervisor_provider_replay rows")
	}

	verifyV239WidenedSupervisorLimits(t, ctx, upgraded, cp, 2)
}

func TestSchemaV239CleanInstallEnforcesWidenedBatchAndRoundLimits(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "clean-v239.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	version, err := st.SchemaVersion(ctx)
	if err != nil || version != 239 {
		t.Fatalf("schema version=%d err=%v, want 239", version, err)
	}
	assertLatestMigrationLedger(t, st, migrationPlan())
	assertNoForeignKeyViolations(t, st.db)

	_, run := createStructuredToolTestRun(t, ctx, st, "clean v239 limits")
	if _, err := application.NewRunService(st).Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	turn, err := st.BeginSupervisorTurn(ctx, acquireTestRunExecutionLease(t, ctx, st, run.ID), "clean v239")
	if err != nil {
		t.Fatal(err)
	}
	verifyV239WidenedSupervisorLimits(t, ctx, st, turn.Checkpoint, 1)
}

func verifyV239WidenedSupervisorLimits(t *testing.T, ctx context.Context, st *SQLiteStore, cp domain.SupervisorCheckpoint, startRound int) {
	t.Helper()

	// Round with 8 calls (positions 1..8) succeeds through the normal store API.
	for round := startRound; round <= domain.MaxSupervisorToolRounds; round++ {
		attempt := llm.ModelAttempt{
			Number: round, TransportAttempt: 1, MaxAttempts: 1,
			ToolRound: round - 1, Provider: "test", Model: "model",
		}
		if _, err := st.RecordSupervisorModelStarted(ctx, cp, attempt); err != nil {
			t.Fatalf("round %d start: %v", round, err)
		}
		callCount := 1
		if round == startRound {
			callCount = domain.MaxSupervisorToolCallsPerRound
		}
		calls := make([]llm.ToolCall, 0, callCount)
		for pos := 1; pos <= callCount; pos++ {
			raw := fmt.Sprintf(`{"title":"r%d-p%d","content":"body-%d-%d"}`, round, pos, round, pos)
			payload, err := toolgateway.NormalizeStructuredMemoryPayload(toolgateway.NoteCreateTool, json.RawMessage(raw))
			if err != nil {
				t.Fatal(err)
			}
			key := runmutation.SupervisorToolOperationKey(cp.RunID, cp.NextTurn, "note_create", string(payload))
			callID, err := runmutation.SupervisorToolCallID(key, round)
			if err != nil {
				t.Fatal(err)
			}
			calls = append(calls, llm.ToolCall{ID: callID, Name: "note_create", Arguments: payload})
		}
		attempt.Outcome = llm.OutcomeSuccess
		var err error
		cp, err = st.RecordSupervisorModelCompleted(ctx, cp, attempt, llm.ChatResponse{
			Provider: "test", Model: "model",
			Usage:     llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
			ToolCalls: calls,
		})
		if err != nil {
			t.Fatalf("round %d (%d calls) rejected: %v", round, callCount, err)
		}
		for _, call := range calls {
			if _, err := st.RecordSupervisorToolExecutionStarted(ctx, cp, call.ID); err != nil {
				t.Fatal(err)
			}
			if _, _, err := st.RecordSupervisorToolResult(ctx, cp, domain.SupervisorToolResult{
				CallID: call.ID, Status: domain.SupervisorToolCompleted, ResultJSON: `{"status":"completed"}`, CompletedAt: time.Now().UTC(),
			}); err != nil {
				t.Fatal(err)
			}
		}
	}

	rounds, err := st.ListSupervisorToolRounds(ctx, cp)
	if err != nil || len(rounds) != domain.MaxSupervisorToolRounds || len(rounds[startRound-1].Calls) != domain.MaxSupervisorToolCallsPerRound {
		t.Fatalf("stored rounds mismatch: len=%d err=%v", len(rounds), err)
	}

	// Verify SQLite CHECK(position BETWEEN 1 AND 16) accepts position 16 and rejects 17.
	now := ts(time.Now().UTC())
	if _, err := st.db.ExecContext(ctx, `INSERT INTO run_supervisor_tool_calls
		(run_id,turn,attempt_id,round,position,model_attempt,call_id,tool_name,payload_json,authority_json,status,result_json,error_code,created_at,completed_at)
		VALUES (?,?,?,?,16,?,'sql-pos-16','note_create','{}','','pending','','',?,NULL)`,
		cp.RunID, cp.NextTurn, cp.AttemptID, startRound, startRound, now); err != nil {
		t.Fatalf("position 16 unexpectedly rejected by SQLite schema: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO run_supervisor_tool_calls
		(run_id,turn,attempt_id,round,position,model_attempt,call_id,tool_name,payload_json,authority_json,status,result_json,error_code,created_at,completed_at)
		VALUES (?,?,?,?,17,?,'sql-pos-17','note_create','{}','','pending','','',?,NULL)`,
		cp.RunID, cp.NextTurn, cp.AttemptID, startRound, startRound, now); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("position 17 should fail CHECK constraint, got: %v", err)
	}

	// Verify 25th native model round is rejected by trg_supervisor_native_round_limit.
	overAttempt := llm.ModelAttempt{
		Number: domain.MaxSupervisorToolRounds + 1, TransportAttempt: 1, MaxAttempts: 1,
		ToolRound: domain.MaxSupervisorToolRounds, Provider: "test", Model: "model",
	}
	if _, err := st.RecordSupervisorModelStarted(ctx, cp, overAttempt); err != nil {
		t.Fatal(err)
	}
	overAttempt.Outcome = llm.OutcomeSuccess
	if cp, err = st.RecordSupervisorModelCompleted(ctx, cp, overAttempt, llm.ChatResponse{
		Provider: "test", Model: "model", Text: `{"version":"root_lifecycle.v1","action":"continue","message":"ok"}`,
		Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO run_supervisor_tool_rounds
		(run_id,turn,attempt_id,round,model_attempt,origin,created_at,completed_at)
		VALUES (?,?,?,?,?,'model',?,NULL)`,
		cp.RunID, cp.NextTurn, cp.AttemptID, domain.MaxSupervisorToolRounds+1, overAttempt.Number, now); err == nil || !strings.Contains(err.Error(), "native model tool round limit exceeded") {
		t.Fatalf("25th native round should fail trigger, got: %v", err)
	}

	// Verify round 217 (project_hook) succeeds while 218 fails CHECK(round BETWEEN 1 AND 217).
	if _, err := st.db.ExecContext(ctx, `INSERT INTO run_supervisor_tool_rounds
		(run_id,turn,attempt_id,round,model_attempt,origin,created_at,completed_at)
		VALUES (?,?,?,?,?,'project_hook',?,NULL)`,
		cp.RunID, cp.NextTurn, cp.AttemptID, domain.MaxSupervisorExecutionRounds, domain.MaxSupervisorToolRounds, now); err != nil {
		t.Fatalf("round 217 unexpectedly rejected: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO run_supervisor_tool_rounds
		(run_id,turn,attempt_id,round,model_attempt,origin,created_at,completed_at)
		VALUES (?,?,?,?,?,'project_hook',?,NULL)`,
		cp.RunID, cp.NextTurn, cp.AttemptID, domain.MaxSupervisorExecutionRounds+1, domain.MaxSupervisorToolRounds, now); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("round 218 should fail CHECK constraint, got: %v", err)
	}
}
