package domain

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// SupervisorToolEffect is an observation of a sealed tool result, never a live
// authorization or a statement about the current filesystem. In particular a
// proposal replay can return an already-applied edit without writing anything.
type SupervisorToolEffect struct {
	Effect          string `json:"effect"`
	EditStatus      string `json:"edit_status,omitempty"`
	EditID          string `json:"edit_id,omitempty"`
	Path            string `json:"path,omitempty"`
	ProposedSHA256  string `json:"proposed_sha256,omitempty"`
	FileWritten     *bool  `json:"file_written,omitempty"`
	Replayed        *bool  `json:"replayed,omitempty"`
	ApplyAuthorized *bool  `json:"apply_authorized_at_observation,omitempty"`
	ReviewRequired  *bool  `json:"review_required_at_observation,omitempty"`
}

// ObservedSupervisorToolEffect deliberately recognizes only Go-owned file
// tools and the exact non-dispatch receipt. Missing, contradictory or malformed
// results stay unknown; outer status=completed alone proves no file effect.
func ObservedSupervisorToolEffect(call SupervisorToolCall) *SupervisorToolEffect {
	fileTool := call.ToolName == "workspace_change" || call.ToolName == "workspace_apply" || call.ToolName == "workspace_delete"
	var envelope struct {
		Version  string            `json:"version"`
		Tool     string            `json:"tool"`
		Status   string            `json:"status"`
		Outcome  string            `json:"outcome"`
		Reason   string            `json:"reason"`
		Stdout   string            `json:"stdout"`
		Metadata map[string]string `json:"metadata"`
	}
	unknown := &SupervisorToolEffect{Effect: "unknown"}
	if json.Unmarshal([]byte(call.ResultJSON), &envelope) != nil {
		if fileTool {
			return unknown
		}
		return nil
	}
	if call.Status == SupervisorToolDenied && call.ErrorCode == "steering_superseded" &&
		envelope.Version == "supervisor_tool_result.v1" && envelope.Tool == call.ToolName &&
		envelope.Status == "denied" && envelope.Outcome == "not_dispatched" && envelope.Reason == "steering_superseded" {
		return &SupervisorToolEffect{Effect: "not_dispatched"}
	}
	if !fileTool {
		return nil
	}
	if envelope.Version != "supervisor_tool_result.v1" || envelope.Tool != call.ToolName || envelope.Status != string(call.Status) {
		return unknown
	}
	if call.Status != SupervisorToolCompleted || call.ErrorCode != "" {
		return unknown
	}
	var result struct {
		Version         string `json:"version"`
		Status          string `json:"status"`
		EditID          string `json:"edit_id"`
		Path            string `json:"path"`
		Operation       string `json:"operation"`
		ProposedSHA256  string `json:"proposed_sha256"`
		FileWritten     *bool  `json:"file_written"`
		Replayed        *bool  `json:"replayed"`
		ApplyAuthorized *bool  `json:"apply_authorized"`
		ReviewRequired  *bool  `json:"review_required"`
	}
	if json.Unmarshal([]byte(envelope.Stdout), &result) != nil || result.Version != "agent-code-tools.v1" {
		return unknown
	}
	for key, value := range map[string]string{"status": result.Status, "edit_id": result.EditID, "operation": result.Operation} {
		if metadata := envelope.Metadata[key]; metadata != "" && metadata != value {
			return unknown
		}
	}
	if !boundedToolEffectIdentity(result.EditID) || !boundedToolEffectIdentity(result.Path) ||
		(result.Status != "proposed" && result.Status != "approved" && result.Status != "applied" && result.Status != "denied") {
		return unknown
	}
	effect := &SupervisorToolEffect{Effect: "unknown", EditStatus: result.Status, EditID: result.EditID, Path: result.Path}
	if hash, err := hex.DecodeString(result.ProposedSHA256); err == nil && len(hash) == sha256.Size {
		effect.ProposedSHA256 = result.ProposedSHA256
	}
	if (call.ToolName == "workspace_change" || call.ToolName == "workspace_delete") && result.FileWritten == nil {
		// This tool only returns a proposal, even when an idempotent replay
		// observes the edit's later status. It cannot prove a new write.
		effect.Effect = "proposal_only"
		effect.ApplyAuthorized, effect.ReviewRequired = result.ApplyAuthorized, result.ReviewRequired
		return effect
	}
	if call.ToolName == "workspace_change" && (result.Status == "proposed" || result.Status == "approved") && !*result.FileWritten {
		effect.Effect = "proposal_only"
		effect.ApplyAuthorized, effect.ReviewRequired = result.ApplyAuthorized, result.ReviewRequired
		return effect
	}
	effect.FileWritten, effect.Replayed = result.FileWritten, result.Replayed
	if result.Status == "applied" && effect.ProposedSHA256 != "" && result.FileWritten != nil && result.Replayed != nil &&
		(*result.FileWritten || *result.Replayed) {
		effect.Effect = "recorded_applied"
	}
	return effect
}

func boundedToolEffectIdentity(value string) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= 256 &&
		!strings.ContainsAny(value, "\x00\r\n")
}

// SupervisorToolResultReference uses the existing opaque history_read format.
// The reader still verifies Thread ownership and the exact sealed digest.
func SupervisorToolResultReference(call SupervisorToolCall) HistoryReadRequest {
	ref, _ := json.Marshal(struct {
		Run     string `json:"r"`
		Turn    int    `json:"t"`
		Attempt string `json:"a"`
		Call    string `json:"c"`
	}{call.RunID, call.Turn, call.AttemptID, call.CallID})
	hash := sha256.Sum256([]byte(call.ResultJSON))
	return HistoryReadRequest{SourceID: "tool:" + base64.RawURLEncoding.EncodeToString(ref),
		Part: "result", ExpectedSHA256: hex.EncodeToString(hash[:])}
}
