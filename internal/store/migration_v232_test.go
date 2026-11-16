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

func TestWorkspaceImageSchemaV232PreservesV231RowsAndAuthority(t *testing.T) {
	ctx := context.Background()
	st := openHistoricalTestDatabase(t, filepath.Join(t.TempDir(), "workspace-image-v231.db"), 231)
	defer st.Close()
	_, run := createStructuredToolTestRun(t, ctx, st, "Agent workspace image migration")
	if _, e := application.NewRunService(st).Start(ctx, run.ID); e != nil {
		t.Fatal(e)
	}
	turn, e := st.BeginSupervisorTurn(ctx, acquireTestRunExecutionLease(t, ctx, st, run.ID), "workspace image migration")
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
	if e = st.applyMigration(ctx, migrationPlan()[231]); e != nil {
		t.Fatal(e)
	}
	after := readV165SupervisorCallRows(t, ctx, st, run.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("migration changed existing bytes/rowids: %#v %#v", before, after)
	}
	assertSupervisorToolCallSchemaV150(t, st)
	var ddl string
	_ = st.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='run_supervisor_tool_calls'`).Scan(&ddl)
	if strings.Count(ddl, "'workspace_view_image'") != 3 {
		t.Fatalf("authority checks missing workspace_view_image: %s", ddl)
	}
	if _, e = st.db.ExecContext(ctx, `UPDATE run_supervisor_tool_calls SET tool_name='workspace_arbitrary_read' WHERE call_id=?`, id); e == nil {
		t.Fatal("unknown workspace tool admitted")
	}

}
