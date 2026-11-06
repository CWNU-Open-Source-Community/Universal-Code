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
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/toolgateway"
)

func TestAgentBrowserSchemaV220PreservesV219RowsAndStoresUploads(t *testing.T) {
	ctx := context.Background()
	st := openHistoricalTestDatabase(t, filepath.Join(t.TempDir(), "agent-browser-v219.db"), 219)
	defer st.Close()
	_, run := createStructuredToolTestRun(t, ctx, st, "Agent browser upload migration")
	if _, e := application.NewRunService(st).Start(ctx, run.ID); e != nil {
		t.Fatal(e)
	}
	turn, e := st.BeginSupervisorTurn(ctx, acquireTestRunExecutionLease(t, ctx, st, run.ID), "browser upload migration")
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
	if e = st.applyMigration(ctx, migrationPlan()[219]); e != nil {
		t.Fatal(e)
	}
	after := readV165SupervisorCallRows(t, ctx, st, run.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("migration changed existing bytes/rowids: %#v %#v", before, after)
	}
	assertSupervisorToolCallSchemaV150(t, st)
	var ddl string
	_ = st.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='run_supervisor_tool_calls'`).Scan(&ddl)
	if strings.Count(ddl, "'browser_upload'") != 3 {
		t.Fatalf("authority checks missing browser_upload: %s", ddl)
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
	a := toolgateway.AgentBrowserCallAuthority{ProtocolVersion: toolgateway.AgentBrowserAuthorityVersion, RunID: run.ID, MissionID: run.MissionID, SessionID: run.SessionID, WorkspaceID: mission.WorkspaceID, RootAgentID: root.ID, Surface: domain.ExecutionSurfaceCode, Phase: domain.ExecutionPhaseDeliver, Role: domain.AgentRoleRoot, Profile: domain.ProfileCode, PermissionMode: domain.RunExecutionPermissionFullAccess, ModeRevision: 1, PermissionSnapshotID: "permission-browser-test", PermissionRevision: 1, PermissionActivation: 2, RunAuthorizationFence: 3, ManagerBootID: "browser-boot-test", BrowserSessionID: "browser-session-test", SessionGeneration: 1}
	a.Generation = a.Fingerprint()
	auth, _ := json.Marshal(a)
	normalized, e := toolgateway.NormalizeAgentBrowserPayload(toolgateway.BrowserUploadTool, json.RawMessage(`{"version":"browser_upload.v2","snapshot_id":"current-snapshot","element_ref":"current-ref-1","workspace_path":"evidence/sample.txt","sensitive_intent":{"version":"browser_sensitive_intent.v1","effect":"external_write","target":"https://example.org/form","description":"Attach the local evidence file","document_epoch":2}}`))
	if e != nil {
		t.Fatal(e)
	}
	k := runmutation.SupervisorToolOperationKey(run.ID, 1, string(toolgateway.BrowserUploadTool), string(normalized))
	callID, _ := runmutation.SupervisorToolCallID(k, 2)
	attempt.Number = 2
	attempt.TransportAttempt = 1
	attempt.ToolRound = 1
	attempt.Outcome = ""
	if _, e = st.RecordSupervisorModelStarted(ctx, checkpoint, attempt); e != nil {
		t.Fatal(e)
	}
	attempt.Outcome = llm.OutcomeSuccess
	checkpoint, e = st.RecordSupervisorModelCompleted(ctx, checkpoint, attempt, llm.ChatResponse{Provider: "test", Model: "model", Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}, ToolCalls: []llm.ToolCall{{ID: callID, Name: string(toolgateway.BrowserUploadTool), Arguments: normalized, Authority: auth}}})
	if e != nil {
		t.Fatal(e)
	}
	rounds, e := st.ListSupervisorToolRounds(ctx, checkpoint)
	if e != nil || len(rounds) != 2 || len(rounds[1].Calls) != 1 {
		t.Fatalf("upload readback %v %#v", e, rounds)
	}
	upload := rounds[1].Calls[0]
	now := time.Now().UTC()
	proposal := approval.Proposal{IdempotencyKey: "browser-upload-approval-test", ProposalID: upload.CallID, SessionID: run.SessionID, WorkspaceID: mission.WorkspaceID, ToolName: toolgateway.AgentBrowserApprovalTool, ActionClass: "browser_external_write", Mode: "per_call", Status: approval.StatusPending, RequestFingerprint: toolgateway.AgentBrowserApprovalFingerprint(upload), DecisionReason: "Attach the local evidence file", RequestedBy: "run_supervisor", CreatedAt: now, UpdatedAt: now}
	bad := proposal
	bad.RequestFingerprint = strings.Repeat("0", 64)
	if _, e = st.EnsureApproval(ctx, bad); e == nil {
		t.Fatal("wrong browser approval fingerprint accepted")
	}
	record, e := st.EnsureApproval(ctx, proposal)
	if e != nil || record.RunID != run.ID {
		t.Fatalf("persisted source approval %v %+v", e, record)
	}
	if _, e = st.DecideApproval(ctx, approval.DecisionRequest{ProposalID: upload.CallID, IdempotencyKey: "approve-browser-upload-test", Action: approval.ActionApprove, ReviewedBy: "operator"}); e != nil {
		t.Fatal(e)
	}
	fresh, e := st.RecordSupervisorToolExecutionStarted(ctx, checkpoint, upload.CallID)
	if e != nil || !fresh {
		t.Fatal(e)
	}
	if _, e = st.EnsureApproval(ctx, proposal); e == nil {
		t.Fatal("already dispatched approval source admitted")
	}
	assertNoForeignKeyViolations(t, st.db)
}
