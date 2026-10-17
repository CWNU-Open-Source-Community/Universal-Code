package store

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/toolgateway"
)

func TestSchemaV207PreservesPopulatedV205ProjectHookHistory(t *testing.T) {
	const mainPlanSHA256 = "a085d7d2f97b7d90ea2b10dd3647cdca96d946ef5e849e8674903c83d510d97b"
	if got := migrationPlanDigest(migrationPlan()[:205]); got != mainPlanSHA256 {
		t.Fatalf("released main migration history changed: %s", got)
	}
	st := openHistoricalTestDatabase(t, filepath.Join(t.TempDir(), "project-hooks-v205.db"), 205)
	restoreLegacyContext := addEmptyCurrentContextForLegacySeed(t, st)
	f := providerReplayFixtureAtStore(t, st)
	f.start(t)
	f.attempt.Outcome = llm.OutcomeSuccess
	f.response.Replay = testProviderReplay(t, f.response.ToolCalls, "v205-native-replay")
	cp, err := st.RecordSupervisorModelCompleted(t.Context(), f.turn.Checkpoint, f.attempt, f.response)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := toolgateway.NormalizeSupervisorToolPayload(toolgateway.CommandRuntimeTool,
		[]byte(`{"version":"command-runtime.v2","action":"list"}`))
	if err != nil {
		t.Fatal(err)
	}
	authority, err := commandruntimeadapter.EncodeAuthority(commandruntimeadapter.NewAuthority(cp.RunID,
		commandruntimeadapter.SandboxedWorkspace("local_windows_sandbox", "windows-local-sandbox.v1", strings.Repeat("a", 64))))
	if err != nil {
		t.Fatal(err)
	}
	var lastOrigin domain.ProjectHookCallOrigin
	var lastCallID string
	// Record one native round and the full host-owned physical sequence through
	// round 21. These are pending command-list receipts; no process is executed.
	for round := 2; round <= domain.MaxSupervisorExecutionRounds; round++ {
		key := runmutation.SupervisorToolOperationKey(cp.RunID, cp.NextTurn, string(toolgateway.CommandRuntimeTool), string(payload))
		callID, err := runmutation.SupervisorToolCallID(key, round)
		if err != nil {
			t.Fatal(err)
		}
		origin := domain.ProjectHookCallOrigin{TriggerKey: fmt.Sprintf("%064x", round), Slot: "format", Target: ".",
			ConfigFingerprint: strings.Repeat("b", 64), WorkspaceVersion: strings.Repeat("c", 64)}
		if err := st.RecordProjectHookCall(t.Context(), cp,
			llm.ToolCall{ID: callID, Name: string(toolgateway.CommandRuntimeTool), Arguments: payload, Authority: authority}, origin); err != nil {
			t.Fatalf("record historical hook round %d: %v", round, err)
		}
		lastOrigin, lastCallID = origin, callID
	}
	if _, err := st.db.ExecContext(t.Context(), `UPDATE run_supervisor_tool_calls SET rowid=7000+round WHERE run_id=?`, cp.RunID); err != nil {
		t.Fatal(err)
	}
	restoreLegacyContext()
	// Snapshot only after the donor has regained its exact historical schema.
	tables := []string{"run_supervisor_tool_calls", "run_supervisor_tool_rounds", "run_supervisor_tool_call_agents",
		"project_hook_calls", "run_supervisor_provider_replay"}
	before := make(map[string][][]any, len(tables))
	for _, table := range tables {
		before[table] = legacyFixtureRows(t, st, table)
	}
	ledger, err := st.loadAppliedMigrations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger) != 205 {
		t.Fatalf("seed changed historical migration prefix: entries=%d", len(ledger))
	}
	if err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		if !reflect.DeepEqual(before[table], legacyFixtureRows(t, st, table)) {
			t.Fatalf("upgrade changed historical bytes or rowids in %s", table)
		}
	}
	afterLedger, err := st.loadAppliedMigrations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for version, old := range ledger {
		if afterLedger[version] != old {
			t.Fatalf("upgrade changed historical migration %d", version)
		}
	}
	assertLatestMigrationLedger(t, st, migrationPlan())
	var physicalRound int
	var rowID int64
	var origin string
	if err := st.db.QueryRowContext(t.Context(), `SELECT c.rowid,c.round,r.origin
		FROM run_supervisor_tool_calls c JOIN run_supervisor_tool_rounds r
		ON r.run_id=c.run_id AND r.turn=c.turn AND r.attempt_id=c.attempt_id AND r.round=c.round
		WHERE c.run_id=? AND c.call_id=?`, cp.RunID, lastCallID).Scan(&rowID, &physicalRound, &origin); err != nil ||
		physicalRound != domain.MaxSupervisorExecutionRounds || rowID != int64(7000+physicalRound) || origin != "project_hook" {
		t.Fatalf("last historical hook lost source/round/rowid: rowid=%d round=%d origin=%q err=%v", rowID, physicalRound, origin, err)
	}
	call, found, err := st.GetProjectHookCall(t.Context(), cp.RunID, lastOrigin.TriggerKey)
	if err != nil || !found || call.Round != domain.MaxSupervisorExecutionRounds ||
		call.ProjectHook == nil || *call.ProjectHook != lastOrigin {
		t.Fatalf("historical project hook no longer resolves its exact call: found=%t call=%+v err=%v", found, call, err)
	}
	var actors int
	if err := st.db.QueryRowContext(t.Context(), `SELECT count(*) FROM run_supervisor_tool_call_agents a
		JOIN run_supervisor_tool_calls c ON c.run_id=a.run_id AND c.turn=a.turn AND c.attempt_id=a.attempt_id AND c.call_id=a.call_id
		WHERE a.run_id=? AND a.agent_id=? AND a.agent_attempt_id=?`, cp.RunID, f.turn.Agent.ID, cp.AttemptID).Scan(&actors); err != nil ||
		actors != domain.MaxSupervisorExecutionRounds {
		t.Fatalf("historical actor references lost: count=%d err=%v", actors, err)
	}
	for _, table := range []string{"project_hook_calls", "run_supervisor_tool_call_agents"} {
		rows, err := st.db.QueryContext(t.Context(), "PRAGMA foreign_key_list("+table+")")
		if err != nil {
			t.Fatal(err)
		}
		canonical := 0
		for rows.Next() {
			var id, sequence int
			var target, fromColumn, toColumn, update, deletion, match string
			if err := rows.Scan(&id, &sequence, &target, &fromColumn, &toColumn, &update, &deletion, &match); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			if strings.HasPrefix(target, "run_supervisor_tool_calls") {
				if target != "run_supervisor_tool_calls" || fromColumn != toColumn {
					_ = rows.Close()
					t.Fatalf("%s foreign key was rewritten to a migration table: %s %s -> %s", table, target, fromColumn, toColumn)
				}
				canonical++
			}
		}
		rowErr := rows.Err()
		_ = rows.Close()
		if rowErr != nil || canonical != 4 {
			t.Fatalf("%s lost canonical four-column call reference: columns=%d err=%v", table, canonical, rowErr)
		}
	}
	assertNoForeignKeyViolations(t, st.db)
}
