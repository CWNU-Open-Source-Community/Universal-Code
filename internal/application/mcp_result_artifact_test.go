package application

import (
	"encoding/json"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/artifact"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/toolgateway"
)

func TestMCPLongResultArtifactRetainsSanitizedJSONAndRemoteError(t *testing.T) {
	for _, mode := range []string{"noninteractive", "run_interactive"} {
		t.Run(mode, func(t *testing.T) { testMCPLongResultArtifact(t, mode) })
	}
}

func testMCPLongResultArtifact(t *testing.T, mode string) {
	f := newMCPOperationApprovalFixture(t, domain.RunExecutionPermissionFull, false, false, "large_result", mode)
	if f.turn.Run.Config.Interactive != (mode == "run_interactive") {
		t.Fatal("fixture did not exercise the requested Run mode")
	}
	thread, err := f.st.GetThreadByRun(t.Context(), f.call.RunID)
	if err != nil || thread.Status != domain.ThreadActive || thread.ActiveRunID != f.call.RunID {
		t.Fatalf("Run creation did not establish the current Thread: %+v %v", thread, err)
	}
	text := strings.Repeat("长文本🙂结果。", 14000) + "unique retained tail"
	secret := "private credential canary"
	raw, err := json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": text}},
		"structuredContent": map[string]any{"precise": json.Number("9007199254740993"), "password": secret}, "isError": true})
	if err != nil || len(raw) >= mcp.MaxMessageBytes || len(raw) <= mcp.MaxClientResultBytes {
		t.Fatal("invalid long response fixture", len(raw), err)
	}
	f.response.Store(json.RawMessage(raw))
	if waiting, err := f.resume(t); err != nil || waiting || f.calls.Load() != 1 {
		t.Fatalf("long response did not settle once: waiting=%t calls=%d err=%v", waiting, f.calls.Load(), err)
	}
	call, _, err := f.st.GetSupervisorApprovalCall(t.Context(), f.call.RunID, f.call.CallID)
	if err != nil || call.Status != domain.SupervisorToolFailed || call.ErrorCode != "remote_tool_error" {
		t.Fatalf("remote error semantics lost: %+v %v", call, err)
	}
	var envelope supervisorToolResultEnvelope
	if json.Unmarshal([]byte(call.ResultJSON), &envelope) != nil || !envelope.Truncated ||
		len(envelope.Stdout) > f.server.Descriptor.MaxResultBytes || !json.Valid([]byte(envelope.Stdout)) {
		t.Fatal("preview is not bounded valid JSON", call.ResultJSON)
	}
	id := envelope.Metadata["artifact_stdout_id"]
	blob, err := f.st.GetRunArtifact(t.Context(), id)
	if err != nil || blob.SizeBytes <= mcp.MaxClientResultBytes || strings.Contains(blob.Content, secret) ||
		!strings.Contains(blob.Content, "unique retained tail") || !strings.Contains(blob.Content, "9007199254740993") ||
		!strings.Contains(blob.Content, "[REDACTED:sensitive-field]") || !json.Valid([]byte(blob.Content)) {
		t.Fatalf("full sanitized result was lost: bytes=%d err=%v", blob.SizeBytes, err)
	}
	var request domain.HistoryReadRequest
	if json.Unmarshal([]byte(envelope.Metadata["full_result_read"]), &request) != nil || request.SourceID != "artifact:"+id {
		t.Fatal("missing model read request", envelope.Metadata)
	}
	var reconstructed strings.Builder
	for {
		page, err := f.st.ReadThreadHistory(t.Context(), f.call.RunID, request)
		if err != nil || page.InstructionAuthorized || page.ContentSHA256 != blob.SHA256 {
			t.Fatalf("artifact page lost scope or digest: %+v %v", page, err)
		}
		reconstructed.WriteString(page.Content)
		if !page.HasMore {
			break
		}
		request.Offset, request.ExpectedSHA256 = page.NextOffset, page.ContentSHA256
	}
	if reconstructed.String() != blob.Content {
		t.Fatal("paged result differs from the complete stored JSON")
	}
	request.ExpectedSHA256 = strings.Repeat("a", 64)
	if _, err := f.st.ReadThreadHistory(t.Context(), f.call.RunID, request); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatal("wrong artifact digest was accepted", err)
	}
	// A different Thread cannot read the reference, even within the same DB.
	_, other, err := NewRunService(f.st).Create(t.Context(), CreateRunRequest{Goal: "other conversation", Profile: "code", WorkspaceID: f.turn.Mission.WorkspaceID, Interactive: true})
	if err != nil {
		t.Fatal(err)
	}
	request.Offset, request.ExpectedSHA256 = 0, blob.SHA256
	if _, err := f.st.ReadThreadHistory(t.Context(), other.ID, request); apperror.CodeOf(err) != apperror.CodeNotFound {
		t.Fatal("artifact crossed Thread scope", err)
	}
	// Also exercise the existing fenced model reader, rather than only storage.
	reader := NewAgentRunner(f.st, nil, policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(f.capabilities)
	if available, err := reader.historyRecallForTurn(t.Context(), f.turn); err != nil || !available {
		t.Fatalf("model was not offered history recall: available=%t err=%v", available, err)
	}
	readCall := call
	readCall.ToolName, readCall.AuthorityJSON = string(toolgateway.HistoryReadTool), ""
	readPayload, _ := json.Marshal(request)
	readCall.PayloadJSON = string(readPayload)
	if result, err := reader.invokeSupervisorTool(t.Context(), f.turn, readCall); err != nil || result.Status != domain.SupervisorToolCompleted {
		t.Fatalf("model could not read retained result: %+v %v", result, err)
	}
	if waiting, err := f.resume(t); err != nil || waiting || f.calls.Load() != 1 {
		t.Fatal("reading/recovery repeated the remote operation", err)
	}
	artifacts, err := f.st.ListRunArtifacts(t.Context(), artifact.ListFilter{RunID: f.call.RunID})
	if err != nil || len(artifacts) != 1 || artifacts[0].ID != id {
		t.Fatal("artifact identity changed on recovery", artifacts, err)
	}
}
