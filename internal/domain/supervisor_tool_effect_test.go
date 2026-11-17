package domain

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestSupervisorFileEffectDoesNotConfuseProposalOrReplayWithWrite(t *testing.T) {
	for _, tc := range []struct{ name, tool, status, inner, want string }{
		{"proposal with long diff", "workspace_change", "completed", `"status":"proposed","apply_authorized":false,"review_required":true,"diff":"` + strings.Repeat("x", 6000) + `"`, "proposal_only"},
		{"automatic approval is not write", "workspace_change", "completed", `"status":"approved","apply_authorized":true,"review_required":false`, "proposal_only"},
		{"proposal replay sees applied edit", "workspace_change", "completed", `"status":"applied"`, "proposal_only"},
		{"proposal explicit not written", "workspace_change", "completed", `"status":"proposed","file_written":false,"replayed":false,"apply_authorized":false,"review_required":true`, "proposal_only"},
		{"single-step workspace_change applied", "workspace_change", "completed", `"status":"applied","file_written":true,"replayed":false`, "recorded_applied"},
		{"single-step workspace_change applied replay", "workspace_change", "completed", `"status":"applied","file_written":false,"replayed":true`, "recorded_applied"},
		{"single-step workspace_change missing replayed", "workspace_change", "completed", `"status":"applied","file_written":true`, "unknown"},
		{"single-step workspace_change no write proof", "workspace_change", "completed", `"status":"applied","file_written":false,"replayed":false`, "unknown"},
		{"contradictory proposed write", "workspace_change", "completed", `"status":"proposed","file_written":true,"replayed":false`, "unknown"},
		{"contradictory approved write", "workspace_change", "completed", `"status":"approved","file_written":true,"replayed":false`, "unknown"},
		{"applied", "workspace_apply", "completed", `"status":"applied","file_written":true,"replayed":false`, "recorded_applied"},
		{"apply receipt replay", "workspace_apply", "completed", `"status":"applied","file_written":false,"replayed":true`, "recorded_applied"},
		{"no write proof", "workspace_apply", "completed", `"status":"applied","file_written":false,"replayed":false`, "unknown"},
		{"failure after possible write", "workspace_apply", "failed", `"status":"applied","file_written":true,"replayed":false`, "unknown"},
		{"missing write field", "workspace_apply", "completed", `"status":"applied"`, "unknown"},
		{"invalid status", "workspace_change", "completed", `"status":"executed"`, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout := `{"version":"agent-code-tools.v1","edit_id":"edit-bound","path":"index.html","operation":"create","proposed_sha256":"` + strings.Repeat("a", 64) + `",` + tc.inner + `}`
			raw, _ := json.Marshal(map[string]any{"version": "supervisor_tool_result.v1", "tool": tc.tool, "status": tc.status, "stdout": stdout})
			call := SupervisorToolCall{RunID: "run-original", Turn: 2, AttemptID: "attempt-original", CallID: "call-original", ToolName: tc.tool, Status: SupervisorToolCallStatus(tc.status), ResultJSON: string(raw)}
			effect := ObservedSupervisorToolEffect(call)
			if effect == nil || effect.Effect != tc.want {
				t.Fatalf("effect=%+v want=%s", effect, tc.want)
			}
			if tc.name == "apply receipt replay" && (effect.FileWritten == nil || *effect.FileWritten || effect.Replayed == nil || !*effect.Replayed) {
				t.Fatal("replay invented a new write")
			}
			ref := SupervisorToolResultReference(call)
			identity, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(ref.SourceID, "tool:"))
			if err != nil || !strings.Contains(string(identity), `"r":"run-original"`) || !strings.Contains(string(identity), `"c":"call-original"`) || len(ref.ExpectedSHA256) != 64 || ref.Part != "result" {
				t.Fatal("unreadable original result reference", ref)
			}
		})
	}
}

func TestSupervisorFileEffectMalformedConflictAndNotDispatched(t *testing.T) {
	call := SupervisorToolCall{ToolName: "workspace_apply", Status: SupervisorToolCompleted}
	for _, raw := range []string{`{`, `{}`, `{"version":"supervisor_tool_result.v1","tool":"workspace_change","status":"completed","stdout":"{}"}`,
		`{"version":"supervisor_tool_result.v1","tool":"workspace_apply","status":"completed","metadata":{"status":"proposed"},"stdout":"{\"version\":\"agent-code-tools.v1\",\"status\":\"applied\",\"edit_id\":\"edit-1\",\"path\":\"a.txt\",\"file_written\":true,\"replayed\":false}"}`} {
		call.ResultJSON = raw
		if effect := ObservedSupervisorToolEffect(call); effect == nil || effect.Effect != "unknown" {
			t.Fatalf("invalid result claimed effect: %+v", effect)
		}
	}
	call.Status = SupervisorToolDenied
	call.ErrorCode = "steering_superseded"
	call.ResultJSON = `{"version":"supervisor_tool_result.v1","tool":"workspace_apply","status":"denied","outcome":"not_dispatched","reason":"steering_superseded"}`
	if e := ObservedSupervisorToolEffect(call); e.Effect != "not_dispatched" {
		t.Fatalf("lost non-dispatch %+v", e)
	}
	call.ErrorCode = "other"
	if e := ObservedSupervisorToolEffect(call); e.Effect != "unknown" {
		t.Fatalf("invented non-dispatch %+v", e)
	}
}
