package store

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/toolgateway"
)

func TestSchemaV131PreservesV130CommandRuntimeHistory(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "command-runtime-call-history.db")
	legacy := openSchemaV130Store(t, path)
	restoreLegacyInputs := addCurrentInputColumnsForLegacySeed(t, legacy)
	runs := newMigrationFixtureRunService(t, legacy)
	_, run, err := runs.Create(ctx, application.CreateRunRequest{
		Goal: "preserve historical command calls", Profile: "code", WorkspaceID: "ws-structured",
		Budget: domain.Budget{MaxTurns: 5, MaxToolCalls: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	turn, err := legacy.BeginSupervisorTurn(ctx,
		acquireTestRunExecutionLease(t, ctx, legacy, run.ID), "historical command calls")
	if err != nil {
		t.Fatal(err)
	}
	statuses := []domain.SupervisorToolCallStatus{domain.SupervisorToolPending,
		domain.SupervisorToolCompleted, domain.SupervisorToolDenied, domain.SupervisorToolFailed}
	seedCalls := make([]llm.ToolCall, len(statuses))
	for i, status := range statuses {
		payload, err := toolgateway.NormalizeStructuredMemoryPayload(toolgateway.NoteCreateTool,
			json.RawMessage(`{"title":"Migration","content":"retain `+string(status)+` command audit history"}`))
		if err != nil {
			t.Fatal(err)
		}
		operationKey := runmutation.SupervisorToolOperationKey(run.ID,
			turn.Checkpoint.NextTurn, string(toolgateway.NoteCreateTool), string(payload))
		callID, err := runmutation.SupervisorToolCallID(operationKey, 1)
		if err != nil {
			t.Fatal(err)
		}
		seedCalls[i] = llm.ToolCall{ID: callID, Name: string(toolgateway.NoteCreateTool), Arguments: payload}
	}
	attempt := llm.ModelAttempt{Number: 1, TransportAttempt: 1, MaxAttempts: 1,
		Provider: "test", Model: "model"}
	if inserted, err := legacy.RecordSupervisorModelStarted(ctx, turn.Checkpoint, attempt); err != nil || !inserted {
		t.Fatalf("record v130 model start: inserted=%t err=%v", inserted, err)
	}
	attempt.Outcome = llm.OutcomeSuccess
	if _, err := legacy.RecordSupervisorModelCompleted(ctx, turn.Checkpoint, attempt,
		llm.ChatResponse{Provider: "test", Model: "model",
			Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}, ToolCalls: seedCalls}); err != nil {
		t.Fatal(err)
	}
	// The current writer requires authority, whereas the real v130 schema
	// requires an empty authority for command_runtime. Seed that historical
	// shape without disabling any legacy constraint or trigger.
	for i, status := range statuses {
		commandPayload, err := toolgateway.NormalizeSupervisorToolPayload(toolgateway.CommandRuntimeTool,
			json.RawMessage(`{"version":"command-runtime.v2","action":"read","job_id":"command-history-`+string(status)+`","cursor":0,"max_bytes":4096,"wait_milliseconds":0}`))
		if err != nil {
			t.Fatal(err)
		}
		operationKey := runmutation.SupervisorToolOperationKey(run.ID,
			turn.Checkpoint.NextTurn, string(toolgateway.CommandRuntimeTool), string(commandPayload))
		callID, err := runmutation.SupervisorToolCallID(operationKey, 1)
		if err != nil {
			t.Fatal(err)
		}
		result, code := "", ""
		var completedAt any
		if status != domain.SupervisorToolPending {
			result = `{"historical_output":"retained"}`
			completedAt = ts(time.Now().UTC())
		}
		if status == domain.SupervisorToolDenied || status == domain.SupervisorToolFailed {
			code = "historical_command_error"
		}
		if _, err := legacy.db.ExecContext(ctx, `UPDATE run_supervisor_tool_calls
			SET call_id = ?, tool_name = 'command_runtime', payload_json = ?, authority_json = '',
				status = ?, result_json = ?, error_code = ?, completed_at = ?
			WHERE run_id = ? AND call_id = ?`, callID, string(commandPayload), status, result, code,
			completedAt, run.ID, seedCalls[i].ID); err != nil {
			t.Fatal(err)
		}
	}
	before := legacyFixtureRows(t, legacy, "run_supervisor_tool_calls")
	restoreLegacyInputs()
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(path)
	if err != nil {
		t.Fatalf("upgrade valid v130 command history: %v", err)
	}
	defer upgraded.Close()
	rounds, err := upgraded.ListSupervisorToolRounds(ctx, turn.Checkpoint)
	if err != nil || len(rounds) != 1 || len(rounds[0].Calls) != len(statuses) {
		t.Fatalf("read migrated command history: rounds=%#v err=%v", rounds, err)
	}
	page, err := upgraded.ListRunSupervisorToolRoundsPage(ctx, run.ID, 0, 10)
	if err != nil || !reflect.DeepEqual(page, rounds) {
		t.Fatalf("read migrated command history page: rounds=%#v err=%v", page, err)
	}
	for i, call := range rounds[0].Calls {
		if call.Status != statuses[i] || call.ToolName != string(toolgateway.CommandRuntimeTool) {
			t.Fatalf("historical command changed: %#v", call)
		}
		loaded, _, err := upgraded.GetSupervisorApprovalCall(ctx, run.ID, call.CallID)
		if err != nil || !reflect.DeepEqual(loaded, call) {
			t.Fatalf("read migrated command call: call=%#v err=%v", loaded, err)
		}
		if authority, err := commandruntimeadapter.DecodeAuthority(json.RawMessage(call.AuthorityJSON)); err == nil || authority.Adapter.Executable() {
			t.Fatalf("historical call restored executable authority: %#v err=%v", authority, err)
		}
		service := &application.CommandRuntimeService{}
		if _, err := service.BindCommandRuntimeAuthority(ctx, json.RawMessage(call.AuthorityJSON),
			json.RawMessage(call.PayloadJSON)); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
			t.Fatalf("historical command authority could be rebound: %v", err)
		}
		if _, err := normalizeSupervisorToolCallsForStore([]llm.ToolCall{{ID: call.CallID,
			Name: call.ToolName, Arguments: json.RawMessage(call.PayloadJSON),
			Authority: json.RawMessage(call.AuthorityJSON)}}, call.RunID, call.Turn, call.Round); apperror.CodeOf(err) != apperror.CodeInvalidArgument || !strings.Contains(err.Error(), "adapter authority") {
			t.Fatalf("new command call did not reject historical authority: %v", err)
		}
	}
	after := legacyFixtureRows(t, upgraded, "run_supervisor_tool_calls")
	if len(before) != len(statuses) || len(after) != len(before) {
		t.Fatalf("historical call count changed: before=%d after=%d", len(before), len(after))
	}
	for i := range before {
		// rowid plus the v130 column order puts authority_json at index 10.
		// All other persisted bytes, including stream identities and results,
		// must survive unchanged through the complete upgrade.
		const authorityColumn = 10
		if before[i][authorityColumn] != "" || after[i][authorityColumn] !=
			`{"protocol_version":"command-runtime-legacy-unbound.v1","authority_json":""}` {
			t.Fatalf("unexpected historical authority projection: %v -> %v", before[i][authorityColumn], after[i][authorityColumn])
		}
		after[i][authorityColumn] = before[i][authorityColumn]
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("migration changed command audit history: before=%#v after=%#v", before, after)
	}
	if _, err := upgraded.db.ExecContext(ctx, `UPDATE run_supervisor_tool_calls
		SET authority_json = '' WHERE run_id = ?`, run.ID); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("command authority JSON constraint was relaxed: %v", err)
	}
	assertNoForeignKeyViolations(t, upgraded.db)
}

func TestSchemaV131AcceptsExactReleasedHistory(t *testing.T) {
	ctx := t.Context()
	plan := migrationPlan()
	canonical := plan[130]
	released := canonical
	released.Statements = append([]string(nil), canonical.Statements...)
	for i, statement := range released.Statements {
		if strings.HasPrefix(statement, "INSERT INTO run_supervisor_tool_calls_v131") {
			released.Statements[i] = `INSERT INTO run_supervisor_tool_calls_v131 SELECT *
			FROM run_supervisor_tool_calls_v130;`
		}
	}
	if got := migrationChecksum(released); got != legacyCommandRuntimeMigration131Checksum {
		t.Fatalf("released v131 checksum=%s want=%s", got, legacyCommandRuntimeMigration131Checksum)
	}
	if !acceptedMigrationChecksum(canonical, legacyCommandRuntimeMigration131Checksum) ||
		acceptedMigrationChecksum(plan[131], legacyCommandRuntimeMigration131Checksum) ||
		acceptedMigrationChecksum(canonical, strings.Repeat("0", 64)) {
		t.Fatal("v131 compatibility must accept only the exact released checksum and version")
	}
	plan[130] = released
	path := filepath.Join(t.TempDir(), "released-v131.db")
	legacy := openUnmigratedSQLiteStore(t, path)
	if err := applyMigrationPrefixForTest(ctx, legacy, plan, 131); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(path)
	if err != nil {
		t.Fatalf("open released v131 history: %v", err)
	}
	defer upgraded.Close()
	assertLatestMigrationLedger(t, upgraded, migrationPlan())
	applied, err := upgraded.loadAppliedMigrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if applied[131].Checksum != legacyCommandRuntimeMigration131Checksum {
		t.Fatalf("released migration history was rewritten: %#v", applied[131])
	}
	applied[131] = appliedMigration{Name: "wrong migration name", Checksum: legacyCommandRuntimeMigration131Checksum}
	if err := validateMigrationPlan(migrationPlan(), applied); err == nil {
		t.Fatal("v131 compatibility accepted the released checksum under a different name")
	}
}
