package application

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/session"
)

func boundaryBudgetCall(t *testing.T, id, tool string, result map[string]any) domain.SupervisorToolCall {
	t.Helper()
	stdout, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(supervisorToolResultEnvelope{Version: supervisorToolResultVersion,
		Tool: tool, Status: "completed", Stdout: string(stdout)})
	if err != nil {
		t.Fatal(err)
	}
	return domain.SupervisorToolCall{RunID: "run-budget", Turn: 1, AttemptID: "attempt-before", CallID: id,
		ToolName: tool, Status: domain.SupervisorToolCompleted, ResultJSON: string(encoded)}
}

func boundaryBudgetEntries(t *testing.T, content string) map[string]map[string]any {
	t.Helper()
	entries := make(map[string]map[string]any)
	for _, line := range strings.Split(content, "\n") {
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) == nil {
			if id, ok := entry["call_id"].(string); ok {
				entries[id] = entry
			}
		}
	}
	return entries
}

func TestToolBoundaryBudgetKeepsLatestReceiptIdentitiesAndWholeClaim(t *testing.T) {
	claim := "已读取的原始论文只证明明确边界，不能把这项局部结论称为完整问题已解。"
	page := boundaryBudgetCall(t, "latest-page", "web_fetch", map[string]any{
		"snapshot": map[string]any{"snapshot_id": "snapshot-latest", "source_id": "source-one", "canonical_url": "https://example.com/original",
			"body_offset": 1738, "next_offset": 2627, "body": strings.Repeat("中文正文", 500), "citeable": true, "partial": true, "stale": false},
	})
	citation := boundaryBudgetCall(t, "latest-citation", "web_citation", map[string]any{
		"citation": map[string]any{"citation_id": "citation-exact", "source_id": "source-one", "snapshot_id": "snapshot-latest",
			"claim": claim, "canonical_url": "https://example.com/original", "partial": true, "stale": false, "instruction_authorized": false},
	})
	calls := []domain.SupervisorToolCall{page, citation}
	for i := 0; i < 30; i++ {
		calls = append(calls, boundaryBudgetCall(t, fmt.Sprintf("older-%d", i), "workspace_read", map[string]any{"path": "README.md", "content": strings.Repeat("中文\"\\🙂", 400)}))
	}
	content, err := boundedToolBoundaryContext("attempt-current", calls)
	if err != nil {
		t.Fatal(err)
	}
	message := toolBoundaryEvidenceMessage("session-budget", "attempt-current", content)
	if tokens := estimateModelRequestTokens(llm.ChatRequest{Messages: []llm.Message{message}}) - 8; tokens > 2048 {
		t.Fatalf("wrapped receipt tokens=%d", tokens)
	}
	entries := boundaryBudgetEntries(t, content)
	if len(entries) >= len(calls) || !strings.Contains(content, "lookup itself is bounded") {
		t.Fatal("fixture did not exercise explicitly bounded receipt selection")
	}
	for _, original := range calls[:2] {
		entry := entries[original.CallID]
		if entry == nil || entry["result_sha256"] != session.ContentSHA256(original.ResultJSON) || entry["status"] != "completed" {
			t.Fatal("lost raw receipt identity", original.CallID)
		}
		ref := entry["original_result"].(map[string]any)
		if ref["expected_sha256"] != entry["result_sha256"] || ref["part"] != "result" {
			t.Fatal("readback hash changed")
		}
		decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(ref["source_id"].(string), "tool:"))
		if err != nil || string(decoded) != fmt.Sprintf(`{"r":"run-budget","t":1,"a":"attempt-before","c":%q}`, original.CallID) {
			t.Fatal("opaque read identity changed", string(decoded), err)
		}
	}
	pageResult := entries[page.CallID]["result"].(map[string]any)["snapshot"].(map[string]any)
	if pageResult["next_offset"] != float64(2627) || pageResult["body_offset"] != float64(1738) || pageResult["body"] != nil {
		t.Fatal("receipt must retain viewed cursor without pretending to contain a complete page")
	}
	cite := entries[citation.CallID]["result"].(map[string]any)["citation"].(map[string]any)
	if cite["claim"] != claim || cite["partial"] != true || cite["stale"] != false || cite["instruction_authorized"] != false {
		t.Fatal("citation claim or qualifications changed", cite)
	}
}

func TestToolBoundaryBudgetOmitsOversizeClaimExplicitlyAndKeepsFailedEdit(t *testing.T) {
	call := boundaryBudgetCall(t, "large-claim", "web_citation", map[string]any{"citation": map[string]any{
		"citation_id": "citation-long", "source_id": "source-one", "snapshot_id": "snapshot-one", "claim": strings.Repeat("完整引用主张", 1500), "partial": true}})
	content, err := boundedToolBoundaryContext("attempt-current", []domain.SupervisorToolCall{call})
	if err != nil {
		t.Fatal(err)
	}
	entry := boundaryBudgetEntries(t, content)[call.CallID]
	if entry == nil {
		t.Fatal("oversize claim lost its recoverable receipt")
	}
	claim := entry["result"].(map[string]any)["citation"].(map[string]any)
	if claim["claim"] != nil || claim["claim_omitted"] != true || claim["citation_id"] != "citation-long" {
		t.Fatal("oversize claim silently truncated", claim)
	}
	edit := boundaryBudgetCall(t, "failed-edit", "workspace_apply", map[string]any{"edit_id": "edit-exact", "expected_original_sha256": strings.Repeat("a", 64), "expected_proposed_sha256": strings.Repeat("b", 64), "file_written": false, "state": "failed"})
	edit.Status = domain.SupervisorToolFailed
	edit.ErrorCode = "CONFLICT"
	content, err = boundedToolBoundaryContext("attempt-current", []domain.SupervisorToolCall{edit})
	if err != nil {
		t.Fatal(err)
	}
	entry = boundaryBudgetEntries(t, content)[edit.CallID]
	result := entry["result"].(map[string]any)
	if entry["status"] != "failed" || entry["error_code"] != "CONFLICT" || result["file_written"] != false ||
		result["edit_id"] != "edit-exact" || result["expected_proposed_sha256"] != strings.Repeat("b", 64) {
		t.Fatal("failed edit was lost or upgraded", entry)
	}
}

func TestToolBoundaryBudgetRetainsInnerFailureAndNeverPromisesRecallWrapper(t *testing.T) {
	job := boundaryBudgetCall(t, "job", "command_runtime", map[string]any{"jobs": []any{map[string]any{
		"id": "job-original", "state": "failed", "exit_code": 7, "output_cursor": 91, "output_base_cursor": 0,
		"working_directory": "D:/work", "destination_path": "src/new.go", "stderr": "actual compiler error"}}})
	content, err := boundedToolBoundaryContext("attempt-current", []domain.SupervisorToolCall{job})
	if err != nil {
		t.Fatal(err)
	}
	entry := boundaryBudgetEntries(t, content)[job.CallID]
	state := entry["result"].(map[string]any)["jobs"].([]any)[0].(map[string]any)
	if state["state"] != "failed" || state["exit_code"] != float64(7) || state["output_cursor"] != float64(91) ||
		state["destination_path"] != "src/new.go" || state["stderr_excerpt"] != "actual compiler error" {
		t.Fatal("inner failed Job/body/path facts were lost", state)
	}
	unknown := boundaryBudgetCall(t, "unknown", "future_tool", map[string]any{"opaque_id": strings.Repeat("x", 16000)})
	content, err = boundedToolBoundaryContext("attempt-current", []domain.SupervisorToolCall{unknown})
	if err != nil {
		t.Fatal(err)
	}
	entry = boundaryBudgetEntries(t, content)[unknown.CallID]
	if entry == nil || entry["operation_outcome_omitted"] != true || entry["original_result"] == nil ||
		entry["result_sha256"] != session.ContentSHA256(unknown.ResultJSON) {
		t.Fatal("oversized unknown result lost its minimal receipt", entry)
	}
	for _, tool := range []string{"history_search", "history_read"} {
		wrapper := boundaryBudgetCall(t, "recall", tool, map[string]any{"content": "old result"})
		content, err = boundedToolBoundaryContext("attempt-current", []domain.SupervisorToolCall{wrapper})
		if err != nil || len(boundaryBudgetEntries(t, content)) != 0 || !strings.Contains(content, "Selected 0 of 1 returned") {
			t.Fatal("promised an unreadable recall-wrapper source", tool, err)
		}
	}
}

func TestToolBoundaryMCPHistoryOmitsBinaryWithoutLosingOutcome(t *testing.T) {
	encoded := strings.Repeat("cGl4ZWxzLW9yLWF1ZGlv", 500)
	call := boundaryBudgetCall(t, "mcp-image", "mcp_tool_call", map[string]any{
		"content": []any{
			map[string]any{"type": "image", "mimeType": "image/png", "data": encoded},
			map[string]any{"type": "audio", "mimeType": "audio/wav", "data": encoded},
			map[string]any{"type": "resource", "resource": map[string]any{"uri": "fixture://image", "mimeType": "image/png", "blob": encoded}},
		},
		"contents":          []any{map[string]any{"uri": "fixture://resource", "mimeType": "image/png", "blob": encoded}},
		"messages":          []any{map[string]any{"role": "assistant", "content": map[string]any{"type": "image", "mimeType": "image/png", "data": encoded}}},
		"structuredContent": map[string]any{"observation_id": "old-observation", "action_sent": true, "data": "business-data"},
	})
	call.Round, call.Position, call.ModelAttempt = 1, 1, 1
	call.PayloadJSON, call.AuthorityJSON = `{"tool_name":"computer_observe"}`, `{}`
	call.CreatedAt = time.Now().UTC()
	call.CompletedAt = &call.CreatedAt
	plan, err := minimalSupervisorBoundaryReceipt([]domain.SupervisorToolCall{call}, "session-budget", "attempt-current")
	if err != nil {
		t.Fatal(err)
	}
	content, err := supervisorBoundaryReceiptContent(plan, 12000, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	entry := boundaryBudgetEntries(t, content)[call.CallID]
	if entry == nil || entry["result"] == nil || entry["result_sha256"] != session.ContentSHA256(call.ResultJSON) || entry["original_result"] == nil {
		t.Fatal("historical result lost its outcome or exact durable readback")
	}
	for _, required := range []string{"old-observation", `"action_sent":true`, "business-data", "image/png", "audio/wav", "fixture://image", "fixture://resource", "Historical binary content"} {
		if !strings.Contains(content, required) {
			t.Fatal("historical result lost", required)
		}
	}
	if strings.Contains(content, encoded[:20]) || strings.Contains(content, `"blob"`) || strings.Contains(content, "vision support is not established") || !strings.Contains(call.ResultJSON, encoded) {
		t.Fatal("historical bytes were retained, vision was misrepresented, or the durable source changed")
	}
}
