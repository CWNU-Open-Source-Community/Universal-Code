package store

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/toolgateway"
)

func TestSchemaV194PreservesCallsAndActorBindingsAndAdmitsMCPSearch(t *testing.T) {
	st := openHistoricalTestDatabase(t, filepath.Join(t.TempDir(), "mcp-search-migration.db"), 193)
	defer st.Close()
	_, run := createStructuredToolTestRun(t, t.Context(), st, "Preserve MCP search history")
	if _, err := application.NewRunService(st).Start(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	turn, err := st.BeginSupervisorTurn(t.Context(), acquireTestRunExecutionLease(t, t.Context(), st, run.ID), "original source")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := toolgateway.NormalizeStructuredMemoryPayload(toolgateway.NoteCreateTool, json.RawMessage(`{"title":"before194","content":"原始 exact call"}`))
	if err != nil {
		t.Fatal(err)
	}
	key := runmutation.SupervisorToolOperationKey(run.ID, turn.Checkpoint.NextTurn, "note_create", string(payload))
	callID, err := runmutation.SupervisorToolCallID(key, 1)
	if err != nil {
		t.Fatal(err)
	}
	attempt := llm.ModelAttempt{Number: 1, TransportAttempt: 1, MaxAttempts: 1, Provider: "test", Model: "model"}
	if _, err := st.RecordSupervisorModelStarted(t.Context(), turn.Checkpoint, attempt); err != nil {
		t.Fatal(err)
	}
	attempt.Outcome = llm.OutcomeSuccess
	cp, err := st.RecordSupervisorModelCompleted(t.Context(), turn.Checkpoint, attempt,
		llm.ChatResponse{Provider: "test", Model: "model", ToolCalls: []llm.ToolCall{{ID: callID, Name: "note_create", Arguments: payload}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordSupervisorToolExecutionStarted(t.Context(), cp, callID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RecordSupervisorToolResult(t.Context(), cp, domain.SupervisorToolResult{CallID: callID, Status: domain.SupervisorToolCompleted,
		ResultJSON: `{"stdout":"原始结果","stderr":""}`, CompletedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	before, err := st.ListSupervisorToolRounds(t.Context(), cp)
	if err != nil {
		t.Fatal(err)
	}
	var rowID int64
	if err := st.db.QueryRow(`SELECT rowid FROM run_supervisor_tool_calls WHERE run_id=? AND call_id=?`, run.ID, callID).Scan(&rowID); err != nil {
		t.Fatal(err)
	}
	_, searchPayload, err := toolgateway.NormalizeMCPSearchPayload(json.RawMessage(`{"query":"归档"}`))
	if err != nil {
		t.Fatal(err)
	}
	permission, err := st.GetRunExecutionPermission(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := mcp.EncodeSupervisorCallAuthority(mcp.SupervisorCallAuthority{Version: mcp.SupervisorCatalogAuthorityVersion,
		RunID: run.ID, MissionID: turn.Mission.ID, WorkspaceID: turn.Mission.WorkspaceID,
		PermissionSnapshotID: permission.ID, PermissionRevision: permission.Revision, PermissionMode: permission.Mode})
	if err != nil {
		t.Fatal(err)
	}
	searchKey := runmutation.SupervisorToolOperationKey(run.ID, cp.NextTurn, "mcp_tool_search", string(searchPayload))
	searchCallID, err := runmutation.SupervisorToolCallID(searchKey, 2)
	if err != nil {
		t.Fatal(err)
	}
	attempt.Number, attempt.ToolRound, attempt.Outcome = 2, 1, ""
	if _, err := st.RecordSupervisorModelStarted(t.Context(), cp, attempt); err != nil {
		t.Fatal(err)
	}
	attempt.Outcome = llm.OutcomeSuccess
	response := llm.ChatResponse{Provider: "test", Model: "model", ToolCalls: []llm.ToolCall{{ID: searchCallID,
		Name: "mcp_tool_search", Arguments: searchPayload, Authority: authority}}}
	if _, err := st.RecordSupervisorModelCompleted(t.Context(), cp, attempt, response); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("v193 did not reject search at its actual SQL allowlist: %v", err)
	}
	if err := st.applyMigration(t.Context(), migrationPlan()[193]); err != nil {
		t.Fatal(err)
	}
	after, err := st.ListSupervisorToolRounds(t.Context(), cp)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("historical call bytes or actor binding changed: %v", err)
	}
	var rowAfter int64
	if err := st.db.QueryRow(`SELECT rowid FROM run_supervisor_tool_calls WHERE run_id=? AND call_id=?`, run.ID, callID).Scan(&rowAfter); err != nil || rowID != rowAfter {
		t.Fatalf("call rowid changed: %d/%d %v", rowID, rowAfter, err)
	}
	if _, err := st.RecordSupervisorModelCompleted(t.Context(), cp, attempt, response); err != nil {
		t.Fatalf("v194 rejected the actual normalized search call: %v", err)
	}
	if _, err := st.db.Exec(`UPDATE run_supervisor_tool_calls SET authority_json='' WHERE run_id=? AND call_id=?`, run.ID, searchCallID); err == nil {
		t.Fatal("search authority became mutable")
	}
	assertSupervisorToolCallSchemaV150(t, st)
	assertNoForeignKeyViolations(t, st.db)
}
