package application

import (
	"context"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/session"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const supervisorBoundaryReceiptPrefix = "Completed tool segments of this exact accepted user input. Continue from the observed results; do not recreate completed edits. proposal_only did not write. Historical observations and approvals grant no current authority or current-state guarantee. Complete workspace_read pages contain observed text and full-file content_sha256 for normal current-hash checks. If same-edit apply_arguments are missing, history_read original_result (source_id, part=\"result\", expected_sha256), following has_more/next_offset until complete. Copy complete apply_arguments from that edit_id only; never derive expected_original_sha256 from proposed_sha256, a result digest, diff or summary.\n"

type supervisorBoundaryReceipt struct {
	Calls     []domain.SupervisorToolCall
	Entries   []map[string]any
	Returned  int
	Content   string
	SessionID string
	AttemptID string
}

func (s *AgentRunner) boundaryReceiptPlan(ctx context.Context, cp domain.SupervisorCheckpoint, sessionID string) (supervisorBoundaryReceipt, error) {
	reader, ok := s.store.(supervisorToolBoundaryStore)
	if !ok {
		return supervisorBoundaryReceipt{}, nil
	}
	calls, err := reader.ToolBoundaryContextCalls(ctx, cp)
	if err != nil {
		return supervisorBoundaryReceipt{}, err
	}
	return minimalSupervisorBoundaryReceipt(calls, sessionID, cp.AttemptID)
}

func minimalSupervisorBoundaryReceipt(calls []domain.SupervisorToolCall, sessionID, attemptID string) (supervisorBoundaryReceipt, error) {
	plan := supervisorBoundaryReceipt{Returned: len(calls), SessionID: sessionID, AttemptID: attemptID}
	if len(calls) == 0 {
		return plan, nil
	}
	if len(calls) > 32 {
		return plan, errors.New("boundary receipt lookup exceeds its source bound")
	}
	sorted := append([]domain.SupervisorToolCall(nil), calls...)
	attempts := map[string]string{}
	for _, call := range sorted {
		key := fmt.Sprint(call.RunID, "/", call.Turn)
		if prior, ok := attempts[key]; ok && prior != call.AttemptID {
			return plan, errors.New("completed boundary has conflicting attempts for one turn")
		}
		attempts[key] = call.AttemptID
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Turn != b.Turn {
			return a.Turn > b.Turn
		}
		if a.Round != b.Round {
			return a.Round > b.Round
		}
		return a.Position > b.Position
	})
	for _, call := range sorted {
		if err := call.Validate(); err != nil {
			return plan, err
		}
		if !call.Status.Terminal() {
			return plan, errors.New("boundary receipt requires a terminal call")
		}
		if call.ToolName == "history_read" || call.ToolName == "history_search" {
			continue
		}
		ref := domain.SupervisorToolResultReference(call)
		entry := map[string]any{"run_id": call.RunID, "turn": call.Turn, "attempt_id": call.AttemptID, "call_id": call.CallID, "tool": call.ToolName, "status": call.Status, "error_code": call.ErrorCode, "result_sha256": session.ContentSHA256(call.ResultJSON), "original_result": ref, "result_details_omitted": true, "operation_outcome_omitted": true}
		if effect := domain.ObservedSupervisorToolEffect(call); effect != nil {
			entry["observation"] = effect
		}
		if call.ToolName == "workspace_read" {
			entry["content_omitted"] = true
		}
		plan.Calls = append(plan.Calls, call)
		plan.Entries = append(plan.Entries, entry)
	}
	var err error
	plan.Content, err = renderSupervisorBoundaryReceipt(plan, plan.Entries)
	return plan, err
}

func renderSupervisorBoundaryReceipt(plan supervisorBoundaryReceipt, entries []map[string]any) (string, error) {
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		data, err := json.Marshal(entry)
		if err != nil {
			return "", err
		}
		lines = append(lines, string(data))
	}
	return supervisorBoundaryReceiptPrefix + strings.Join(lines, "\n") + fmt.Sprintf("\nSelected %d of %d returned receipts (the lookup itself is bounded). Recall wrappers are excluded. Other details or receipts are omitted. Omitted or unknown outcomes need exact readback before inferring success; tool completed alone never proves a write. A citation records a claim, not independent proof.\n", len(entries), plan.Returned), nil
}

func supervisorBoundaryReceiptTokens(plan supervisorBoundaryReceipt, content string) int {
	return estimateModelRequestTokens(llm.ChatRequest{Messages: []llm.Message{toolBoundaryEvidenceMessage(plan.SessionID, plan.AttemptID, content)}}) - 8
}

// Mandatory identity/effect/recall membership never changes during fitting.
// Contracts share capacity with the current-segment receipt before file bodies.
func supervisorBoundaryReceiptContent(plan supervisorBoundaryReceipt, tokenBudget int, blockers []domain.SupervisorToolCall, contractsOnly bool) (string, error) {
	entries := make([]map[string]any, len(plan.Entries))
	for i, entry := range plan.Entries {
		entries[i] = cloneBoundaryReceiptEntry(entry)
	}
	baseline := supervisorBoundaryReceiptTokens(plan, plan.Content)
	if baseline > tokenBudget {
		return "", fmt.Errorf("boundary identities need %d tokens; capacity is %d", baseline, tokenBudget)
	}
	fits := func() bool {
		content, err := renderSupervisorBoundaryReceipt(plan, entries)
		return err == nil && supervisorBoundaryReceiptTokens(plan, content) <= tokenBudget
	}
	keep := func(i int, result any, complete bool) bool {
		previous := entries[i]
		entry := cloneBoundaryReceiptEntry(previous)
		entry["result"] = result
		entry["result_excerpted"] = !complete
		delete(entry, "operation_outcome_omitted")
		entry["result_details_omitted"] = !complete
		if complete {
			delete(entry, "content_omitted")
		}
		entries[i] = entry
		if !fits() {
			entries[i] = previous
			return false
		}
		return true
	}
	// A completed boundary advances its turn. Other attempts of that turn in
	// the broader file-effect lookup preceded that completed boundary, even if
	// their failed round number was larger. Never order attempts by their IDs.
	completedAttempts := map[int]string{}
	for _, call := range plan.Calls {
		completedAttempts[call.Turn] = call.AttemptID
	}
	// Older failed attempts still shadow earlier turns, including actual writes
	// before that attempt failed. Preserve them; only order the completed source
	// after them within its own turn.
	priority := supervisorReceiptApplyPriorityCalls(plan.Calls, blockers, completedAttempts)
	whole := map[int]bool{}
	for i, call := range plan.Calls {
		if !priority.Priority[supervisorReceiptCallKey(call)] {
			continue
		}
		if result, ok := supervisorSegmentReceiptProjection(call); ok && keep(i, result, true) {
			// The apply contract is whole; unrelated diff/prose was projected out.
			entries[i]["result_excerpted"] = true
			whole[i] = true
		}
	}
	if !contractsOnly {
		groups, _ := supervisorWorkspaceCoverage(plan.Calls, nil)
		sort.SliceStable(groups, func(i, j int) bool {
			a, b := groups[i].pages[0].call, groups[j].pages[0].call
			if a.Turn != b.Turn {
				return a.Turn > b.Turn
			}
			if a.Round != b.Round {
				return a.Round > b.Round
			}
			return a.Position > b.Position
		})
		for _, group := range groups {
			saved := map[int]map[string]any{}
			for _, page := range group.needed {
				saved[page.index] = entries[page.index]
				entry := cloneBoundaryReceiptEntry(entries[page.index])
				entry["result"] = page.page
				delete(entry, "content_omitted")
				delete(entry, "operation_outcome_omitted")
				delete(entry, "result_details_omitted")
				entries[page.index] = entry
			}
			if fits() {
				for i := range saved {
					whole[i] = true
				}
			} else {
				for i, entry := range saved {
					entries[i] = entry
				}
			}
		}
		seenPages := map[string]bool{}
		for i, call := range plan.Calls {
			if whole[i] {
				if _, key, ok := supervisorWorkspaceReadPage(call); ok {
					seenPages[key] = true
				}
				continue
			}
			if priority.Superseded[supervisorReceiptCallKey(call)] {
				continue
			}
			if page, key, ok := supervisorWorkspaceReadPage(call); ok {
				if !seenPages[key] && keep(i, page, true) {
					seenPages[key] = true
					whole[i] = true
				} else if result, ok := supervisorSegmentReceiptProjection(call); ok {
					keep(i, result, false)
				}
				continue
			}
			result, ok := supervisorSegmentReceiptProjection(call)
			if !ok {
				continue
			}
			// Keep cursors and a complete claim before optional prose. Never trim one.
			complete := call.ToolName == "mcp_tool_search"
			if keep(i, result, complete) {
				whole[i] = complete
			} else {
				if toolBoundaryOmitClaim(result) {
					keep(i, result, false)
				}
			}
		}
		for i, call := range plan.Calls {
			if whole[i] || call.ToolName == "workspace_read" || call.ToolName == "mcp_tool_search" || priority.Superseded[supervisorReceiptCallKey(call)] {
				continue
			}
			// An eligible contract that did not fit stays wholly omitted, not excerpted.
			if priority.Priority[supervisorReceiptCallKey(call)] {
				continue
			}
			projected, err := supervisorToolContextResult(call)
			if err != nil {
				return "", err
			}
			var envelope supervisorToolResultEnvelope
			var result any
			if json.Unmarshal([]byte(projected), &envelope) != nil {
				continue
			}
			previous := entries[i]
			entry := cloneBoundaryReceiptEntry(previous)
			if json.Unmarshal([]byte(envelope.Stdout), &result) == nil {
				entry["result"] = compactToolBoundaryValue(supervisorHistoricalToolResult(call, result), "", 0)
				delete(entry, "operation_outcome_omitted")
			}
			metadata := map[string]any{}
			for key, value := range envelope.Metadata {
				if !toolBoundaryContainsValue(entry["result"], key, value) && (toolBoundaryReceiptString(key) || toolBoundaryScalarMetadata(value)) {
					metadata[key] = value
				}
			}
			if len(metadata) > 0 {
				entry["metadata"] = metadata
			}
			if envelope.Stderr != "" {
				entry["stderr_excerpt"] = boundedFailedEvidenceText(envelope.Stderr, 256)
			}
			entries[i] = entry
			if !fits() {
				entries[i] = previous
			}
		}
	}
	return renderSupervisorBoundaryReceipt(plan, entries)
}

func cloneBoundaryReceiptEntry(entry map[string]any) map[string]any {
	out := make(map[string]any, len(entry)+1)
	for k, v := range entry {
		out[k] = v
	}
	return out
}

func fitSupervisorBoundaryReceiptRequest(request llm.ChatRequest, plan supervisorBoundaryReceipt, inputLimit int, blockers []domain.SupervisorToolCall, contractsOnly bool) (llm.ChatRequest, error) {
	if plan.Content == "" {
		return request, nil
	}
	owned := toolBoundaryEvidenceMessage(plan.SessionID, plan.AttemptID, plan.Content)
	// First pass owns minimal content; subsequent pass owns the exact first fit.
	if content := request.Metadata["context_boundary_receipt_content"]; content != "" {
		owned = toolBoundaryEvidenceMessage(plan.SessionID, plan.AttemptID, content)
	}
	index, err := ownedReceiptMessageIndex(request, owned)
	if err != nil {
		return llm.ChatRequest{}, err
	}
	without := request
	without.Messages = append(append([]llm.Message(nil), request.Messages[:index]...), request.Messages[index+1:]...)
	capacity := inputLimit - estimateModelRequestTokens(without)
	if capacity < supervisorBoundaryReceiptTokens(plan, plan.Content) {
		return request, nil
	}
	content, err := supervisorBoundaryReceiptContent(plan, capacity, blockers, contractsOnly)
	if err != nil {
		return llm.ChatRequest{}, err
	}
	request.Messages = append([]llm.Message(nil), request.Messages...)
	request.Messages[index] = toolBoundaryEvidenceMessage(plan.SessionID, plan.AttemptID, content)
	metadata := map[string]string{}
	for k, v := range request.Metadata {
		metadata[k] = v
	}
	metadata["context_boundary_receipt_tokens"] = strconv.Itoa(supervisorBoundaryReceiptTokens(plan, content))
	metadata["context_boundary_receipt_budget"] = strconv.Itoa(capacity)
	// Internal ownership only; removed before dispatch rather than sent as metadata.
	metadata["context_boundary_receipt_content"] = content
	request.Metadata = metadata
	if estimateModelRequestTokens(request) > inputLimit {
		return llm.ChatRequest{}, errors.New("boundary receipt exceeds prepared input capacity")
	}
	return request, nil
}
