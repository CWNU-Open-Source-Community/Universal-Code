package store

import (
	"context"
	"encoding/json"
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

func TestAgentBrowserSchemaV231PreservesV230RowsAndStoresMoves(t *testing.T) {
	ctx := context.Background()
	st := openHistoricalTestDatabase(t, filepath.Join(t.TempDir(), "agent-browser-v230.db"), 230)
	defer st.Close()
	_, run := createStructuredToolTestRun(t, ctx, st, "Agent browser pointer migration")
	if _, e := application.NewRunService(st).Start(ctx, run.ID); e != nil {
		t.Fatal(e)
	}
	turn, e := st.BeginSupervisorTurn(ctx, acquireTestRunExecutionLease(t, ctx, st, run.ID), "browser pointer migration")
	if e != nil {
		t.Fatal(e)
	}
	payload, e := toolgateway.NormalizeStructuredMemoryPayload(toolgateway.NoteCreateTool, json.RawMessage(`{"title":"preserve","content":"immutable old note"}`))
	if e != nil {
		t.Fatal(e)
	}
	key := runmutation.SupervisorToolOperationKey(run.ID, 1, "note_create", string(payload))
	id, _ := runmutation.SupervisorToolCallID(key, 1)
	attempt := llm.ModelAttempt{Number: 1, TransportAttempt: 1, MaxAttempts: 1, Provider: "test", Model: "model"}
	if _, e = st.RecordSupervisorModelStarted(ctx, turn.Checkpoint, attempt); e != nil {
		t.Fatal(e)
	}
	attempt.Outcome = llm.OutcomeSuccess
	checkpoint, e := st.RecordSupervisorModelCompleted(ctx, turn.Checkpoint, attempt, llm.ChatResponse{Provider: "test", Model: "model", Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}, ToolCalls: []llm.ToolCall{{ID: id, Name: "note_create", Arguments: payload}}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.db.ExecContext(ctx, `UPDATE run_supervisor_tool_calls SET rowid=87 WHERE call_id=?`, id); e != nil {
		t.Fatal(e)
	}
	if _, e = st.RecordSupervisorToolExecutionStarted(ctx, checkpoint, id); e != nil {
		t.Fatal(e)
	}
	if _, _, e = st.RecordSupervisorToolResult(ctx, checkpoint, domain.SupervisorToolResult{CallID: id, Status: domain.SupervisorToolCompleted, ResultJSON: `{"ok":true}`, CompletedAt: time.Now().UTC()}); e != nil {
		t.Fatal(e)
	}
	before := readV165SupervisorCallRows(t, ctx, st, run.ID)
	if e = st.applyMigration(ctx, migrationPlan()[230]); e != nil {
		t.Fatal(e)
	}
	after := readV165SupervisorCallRows(t, ctx, st, run.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("migration changed existing bytes/rowids: %#v %#v", before, after)
	}
	assertSupervisorToolCallSchemaV150(t, st)
	var ddl string
	_ = st.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='run_supervisor_tool_calls'`).Scan(&ddl)
	if strings.Count(ddl, "'browser_move'") != 3 {
		t.Fatalf("authority checks missing browser_move: %s", ddl)
	}
	if _, e = st.db.ExecContext(ctx, `UPDATE run_supervisor_tool_calls SET tool_name='browser_arbitrary_execute' WHERE call_id=?`, id); e == nil {
		t.Fatal("unknown browser tool admitted")
	}

	root, found, e := st.GetRootAgent(ctx, run.ID)
	if e != nil || !found {
		t.Fatal(e)
	}
	mission, e := st.GetMission(ctx, run.MissionID)
	if e != nil {
		t.Fatal(e)
	}
	a := toolgateway.AgentBrowserCallAuthority{ProtocolVersion: toolgateway.AgentBrowserAuthorityVersion, RunID: run.ID, MissionID: run.MissionID, SessionID: run.SessionID, WorkspaceID: mission.WorkspaceID, RootAgentID: root.ID, Surface: domain.ExecutionSurfaceCode, Phase: domain.ExecutionPhaseDeliver, Role: domain.AgentRoleRoot, Profile: domain.ProfileCode, PermissionMode: domain.RunExecutionPermissionFullAccess, ModeRevision: 1, PermissionSnapshotID: "permission-pointer-test", PermissionRevision: 1, PermissionActivation: 2, RunAuthorizationFence: 3, ManagerBootID: "browser-boot-test", BrowserSessionID: "browser-session-test", BrowserNetworkAccess: domain.BrowserNetworkInternet, BrowserNetworkRevision: 1, SessionGeneration: 1}
	a.Generation = a.Fingerprint()
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	auth, _ := json.Marshal(a)
	normalized, e := toolgateway.NormalizeAgentBrowserPayload(toolgateway.BrowserMoveTool, json.RawMessage(`{"version":"browser_move.v2","point":{"x":40,"y":30,"document_epoch":2,"viewport":{"width":800,"height":600}}}`))
	if e != nil {
		t.Fatal(e)
	}
	k := runmutation.SupervisorToolOperationKey(run.ID, 1, string(toolgateway.BrowserMoveTool), string(normalized))
	callID, _ := runmutation.SupervisorToolCallID(k, 2)
	attempt.Number = 2
	attempt.TransportAttempt = 1
	attempt.ToolRound = 1
	attempt.Outcome = ""
	if _, e = st.RecordSupervisorModelStarted(ctx, checkpoint, attempt); e != nil {
		t.Fatal(e)
	}
	attempt.Outcome = llm.OutcomeSuccess
	checkpoint, e = st.RecordSupervisorModelCompleted(ctx, checkpoint, attempt, llm.ChatResponse{Provider: "test", Model: "model", Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}, ToolCalls: []llm.ToolCall{{ID: callID, Name: string(toolgateway.BrowserMoveTool), Arguments: normalized, Authority: auth}}})
	if e != nil {
		t.Fatal(e)
	}
	rounds, e := st.ListSupervisorToolRounds(ctx, checkpoint)
	if e != nil || len(rounds) != 2 || len(rounds[1].Calls) != 1 {
		t.Fatalf("pointer move readback %v %#v", e, rounds)
	}

	move := rounds[1].Calls[0]
	if move.ToolName != string(toolgateway.BrowserMoveTool) || move.PayloadJSON != string(normalized) || move.AuthorityJSON != string(auth) {
		t.Fatalf("changed pointer call: %+v", move)
	}
	fresh, e := st.RecordSupervisorToolExecutionStarted(ctx, checkpoint, move.CallID)
	if e != nil || !fresh {
		t.Fatalf("move could not start durably: fresh=%t err=%v", fresh, e)
	}
	if _, _, e = st.RecordSupervisorToolResult(ctx, checkpoint, domain.SupervisorToolResult{CallID: move.CallID, Status: domain.SupervisorToolCompleted, ResultJSON: `{"dispatched":true}`, CompletedAt: time.Now().UTC()}); e != nil {
		t.Fatal(e)
	}
	if _, e = st.db.ExecContext(ctx, `UPDATE run_supervisor_tool_calls SET authority_json='' WHERE call_id=?`, move.CallID); e == nil {
		t.Fatal("pointer durable authority was removable")
	}
	assertNoForeignKeyViolations(t, st.db)
}
