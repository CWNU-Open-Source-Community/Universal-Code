package store

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/redact"
)

func TestCompletionDataPersistsThroughReopenAndIdempotentReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "completion-data.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	_, run, root, child, attemptID := prepareRunningSpecialist(t, ctx, st, "preserve discovered structured results")
	input := newCompletionTestValue(run.ID, root.ID, child.ID, attemptID, domain.CompletionSucceeded, nil, nil, "probe completed")
	input.Report.Data = json.RawMessage(`{"targets":[{"path":"internal/one.go","line":9007199254740993}],"password":"private-value","ready":true}`)
	want := `{"password":"` + redact.SensitiveJSONFieldPlaceholder + `","ready":true,"targets":[{"line":9007199254740993,"path":"internal/one.go"}]}`
	completed, replayed, err := st.FinishSpecialist(ctx, input, "completion-data-operation")
	if err != nil || replayed || string(completed.Report.Data) != want {
		t.Fatalf("completion data was not saved safely: %+v replayed=%t %v", completed, replayed, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st = reopened
	stored, found, err := st.GetAgentCompletion(ctx, child.ID)
	if err != nil || !found || string(stored.Report.Data) != want {
		t.Fatalf("completion data was lost after reopening: %+v found=%t %v", stored, found, err)
	}
	// Workflow workers serialize this durable report into their step output.
	output, err := json.Marshal(stored.Report)
	if err != nil {
		t.Fatal(err)
	}
	var projected struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(output, &projected); err != nil || string(projected.Data) != want {
		t.Fatalf("durable report omitted structured workflow output: %s %v", output, err)
	}
	retry := input
	retry.ID, retry.MessageID = idgen.New("completion"), idgen.New("agentmsg")
	retry.Report.Data = json.RawMessage(` { "ready":true,"password":"private-value","targets":[{"line":9007199254740993,"path":"internal/one.go"}] } `)
	repeated, replayed, err := st.FinishSpecialist(ctx, retry, "completion-data-operation")
	if err != nil || !replayed || repeated.ID != completed.ID || string(repeated.Report.Data) != want {
		t.Fatalf("same completion data did not replay: %+v replayed=%t %v", repeated, replayed, err)
	}
	retry.Report.Data = json.RawMessage(`{"ready":false}`)
	if _, _, err := st.FinishSpecialist(ctx, retry, "completion-data-operation"); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("changed completion data replay did not conflict: %v", err)
	}
	graph, err := st.RestoreAgentGraph(ctx, run.ID)
	if err != nil || len(graph.PendingMessages) != 1 || strings.Contains(graph.PendingMessages[0].PayloadJSON, "private-value") {
		t.Fatalf("structured completion graph did not restore: %+v %v", graph, err)
	}
}

func TestCompletionDataReaderRequiresItsBoundResultMessage(t *testing.T) {
	st := openWorkItemTestStore(t)
	ctx := t.Context()
	_, run, root, child, attemptID := prepareRunningSpecialist(t, ctx, st, "reject unrelated completion data")
	input := newCompletionTestValue(run.ID, root.ID, child.ID, attemptID, domain.CompletionSucceeded, nil, nil, "probe completed")
	input.Report.Data = json.RawMessage(`{"ready":true}`)
	completed, _, err := st.FinishSpecialist(ctx, input, "completion-data-bound-message")
	if err != nil {
		t.Fatal(err)
	}
	var original string
	if err := st.db.QueryRowContext(ctx, `SELECT payload_json FROM agent_messages WHERE id=?`, completed.MessageID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	mismatched := domain.AgentCompletionInboxPayload{CompletionReportID: completed.ID, AgentID: child.ID, Report: completed.Report}
	mismatched.Report.Summary = "another report"
	mismatchedJSON, err := json.Marshal(mismatched)
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{
		`{"message":"prose with forged data","data":{"ready":false}}`,
		string(mismatchedJSON),
		strings.Replace(original, completed.ID, "completion-unrelated", 1),
	} {
		if _, err := st.db.ExecContext(ctx, `UPDATE agent_messages SET payload_json=? WHERE id=?`, payload, completed.MessageID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.GetAgentCompletion(ctx, child.ID); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
			t.Fatalf("reader accepted an unbound result payload: %s %v", payload, err)
		}
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE agent_messages SET payload_json=?,kind='notification' WHERE id=?`, original, completed.MessageID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.GetAgentCompletion(ctx, child.ID); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("reader accepted a non-result message: %v", err)
	}
}

func TestSpecialistModelCompletionDataParticipatesInSafeReplayIdentity(t *testing.T) {
	st := openWorkItemTestStore(t)
	ctx := t.Context()
	fixture := prepareSpecialistAttemptFixture(t, ctx, st, "model structured completion", 1, 64)
	attempt, _, err := st.BeginSpecialistAttempt(ctx, newAttemptStart(fixture, idgen.New("attempt")), "completion-data-model-start")
	if err != nil {
		t.Fatal(err)
	}
	ref := attemptRef(attempt)
	modelAttempt := llm.ModelAttempt{Number: 1, TransportAttempt: 1, MaxAttempts: 3, Provider: "mock", Model: "mock-code"}
	if _, err := st.RecordSpecialistModelStarted(ctx, ref, modelAttempt); err != nil {
		t.Fatal(err)
	}
	action := domain.SpecialistAction{
		Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionFinish, Message: "probe completed",
		Report: &domain.CompletionReport{
			Version: domain.CompletionReportVersion, Outcome: domain.CompletionSucceeded, Summary: "probe completed",
			WorkItemIDs: []string{}, NoteIDs: []string{},
			Data: json.RawMessage(`{"password":"private-value","count":9007199254740993}`),
		},
	}
	safe, err := redactSpecialistAction(action)
	if err != nil || safe.Report == nil || strings.Contains(string(safe.Report.Data), "private-value") ||
		!strings.Contains(string(safe.Report.Data), `"count":9007199254740993`) ||
		!strings.Contains(string(safe.Report.Data), redact.SensitiveJSONFieldPlaceholder) {
		t.Fatalf("store action did not preserve safely redacted data: %+v %v", safe, err)
	}
	response := llm.ChatResponse{Text: specialistActionResponse(t, action), Usage: llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}
	decision := policy.Decision{Allowed: true, Reason: "allowed observed data", Risk: "low"}
	charged, err := st.RecordSpecialistModelCompleted(ctx, ref, modelAttempt, response, "probe results", action, decision)
	if err != nil {
		t.Fatal(err)
	}
	action.Report.Data = json.RawMessage(` { "count":9007199254740993,"password":"private-value" } `)
	if repeated, err := st.RecordSpecialistModelCompleted(ctx, ref, modelAttempt, response, "probe results", action, decision); err != nil || repeated.ID != charged.ID {
		t.Fatalf("same structured model result failed replay: %+v %v", repeated, err)
	}
	action.Report.Data = json.RawMessage(`{"count":9007199254740994}`)
	if _, err := st.RecordSpecialistModelCompleted(ctx, ref, modelAttempt, response, "probe results", action, decision); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("model accepted data differing from its provider response: %v", err)
	}
	response.Text = specialistActionResponse(t, action)
	if _, err := st.RecordSpecialistModelCompleted(ctx, ref, modelAttempt, response, "probe results", action, decision); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("model replay accepted changed structured data: %v", err)
	}
}
