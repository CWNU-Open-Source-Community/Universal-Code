package application

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
)

func renderSupervisorSegmentReceipt(entries []supervisorSegmentReceiptRound, nativeCount int) (string, error) {
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		encoded, err := json.Marshal(entry)
		if err != nil {
			return "", err
		}
		lines = append(lines, string(encoded))
	}
	suffix := "\nNo native tool call or result pairs of this segment remain in this request."
	if nativeCount > 0 {
		suffix = fmt.Sprintf("\nThe %d most recent tool round(s) of this segment remain below as native call and result pairs.", nativeCount)
	}
	return supervisorSegmentReceiptPrefix + strings.Join(lines, "\n") + suffix, nil
}

func supervisorSegmentReceiptTokens(content, attemptID string, sessionIDs ...string) int {
	sessionID := segmentReceiptBudgetSessionID
	if len(sessionIDs) > 0 {
		sessionID = sessionIDs[0]
	}
	return estimateModelRequestTokens(llm.ChatRequest{Messages: []llm.Message{supervisorSegmentReceiptMessage(sessionID, attemptID, content)}}) - 8
}

// Start with identities only. The complete prepared request, including images,
// skills, replay and correction guidance, determines optional capacity later.
func minimalSupervisorSegmentReceiptPlan(rounds []domain.SupervisorToolRound, count int, sessionID, attemptID string) (supervisorSegmentReceipt, error) {
	if count <= 0 || count > len(rounds) {
		return supervisorSegmentReceipt{}, errors.New("invalid minimal segment receipt count")
	}
	for _, round := range rounds {
		if err := round.Validate(); err != nil {
			return supervisorSegmentReceipt{}, err
		}
		if !round.Complete() {
			return supervisorSegmentReceipt{}, fmt.Errorf(
				"current segment contains an incomplete supervisor tool round %d of attempt %s",
				round.Round, round.AttemptID)
		}
	}
	receipted := append([]domain.SupervisorToolRound(nil), rounds[:count]...)
	native := append([]domain.SupervisorToolRound(nil), rounds[count:]...)
	entries, err := supervisorSegmentReceiptEntries(receipted)
	if err != nil {
		return supervisorSegmentReceipt{}, err
	}
	rawLines := make([]string, len(entries))
	for i, entry := range entries {
		encoded, marshalErr := json.Marshal(entry)
		if marshalErr != nil {
			return supervisorSegmentReceipt{}, marshalErr
		}
		rawLines[i] = string(encoded)
	}
	suffix := "\nNo native tool call or result pairs of this segment remain in this request."
	if len(native) > 0 {
		suffix = fmt.Sprintf("\nThe %d most recent tool round(s) of this segment remain below as native call and result pairs.", len(native))
	}
	resolvedSessionID := segmentReceiptBudgetSessionID
	if sessionID != "" {
		resolvedSessionID = sessionID
	}
	sourceRef := fmt.Sprintf("tool-segment-receipt-%s", attemptID)
	tree := newReceiptTokenTree(sourceRef, resolvedSessionID, supervisorSegmentReceiptPrefix, suffix, nil, rawLines)
	return supervisorSegmentReceipt{
		ReceiptedRounds: receipted,
		NativeRounds:    native,
		ReceiptContent:  tree.renderWith(supervisorSegmentReceiptPrefix, suffix, nil),
		ReceiptTokens:   tree.totalTokens(),
	}, nil
}

// The capacity is an input allocation, not an output limit. A recovery ceiling
// is optional; a non-nil zero is a real bound, never an unlimited allowance.
func supervisorReceiptInputLimit(request llm.ChatRequest, window llm.ContextWindow, budget domain.Budget, checkpoint domain.SupervisorCheckpoint, recoveryLimit *int) (int, error) {
	output := request.PlannedOutputTokens(window)
	limit, err := window.InputLimit(output)
	if err != nil {
		return 0, err
	}
	if budget.MaxTokens > 0 {
		remaining := budget.MaxTokens - checkpoint.TotalTokens - int64(output)
		if remaining < 0 {
			remaining = 0
		}
		if remaining < int64(limit) {
			limit = int(remaining)
		}
	}
	if recoveryLimit != nil && *recoveryLimit < limit {
		limit = *recoveryLimit
	}
	return limit, nil
}

// Replace exactly the Go-authored message already inserted for this plan.
// No text-prefix matching, extra message, native-pair edit or history slicing.
func fitSupervisorSegmentReceiptRequest(request llm.ChatRequest, plan supervisorSegmentReceipt, sessionID, attemptID string, inputLimit int) (llm.ChatRequest, error) {
	return fitSupervisorSegmentReceiptStage(request, plan, sessionID, attemptID, inputLimit, false)
}

func fitSupervisorSegmentReceiptStage(request llm.ChatRequest, plan supervisorSegmentReceipt, sessionID, attemptID string, inputLimit int, contractsOnly bool) (llm.ChatRequest, error) {
	if len(plan.ReceiptedRounds) == 0 {
		return request, nil
	}
	owned := supervisorSegmentReceiptMessage(sessionID, attemptID, plan.ReceiptContent)
	index, err := ownedReceiptMessageIndex(request, owned)
	if err != nil {
		return llm.ChatRequest{}, err
	}
	without := request
	without.Messages = append(append([]llm.Message(nil), request.Messages[:index]...), request.Messages[index+1:]...)
	capacity := inputLimit - estimateModelRequestTokens(without)
	minimalTokens := plan.ReceiptTokens
	if minimalTokens <= 0 {
		minimalTokens = supervisorSegmentReceiptTokens(plan.ReceiptContent, attemptID, sessionID)
	}
	workingRounds := plan.ReceiptedRounds
	var summaryLines []string
	if capacity < minimalTokens {
		foldedRounds, summary, ok := foldSupervisorSegmentReceiptRounds(plan.ReceiptedRounds, plan.NativeRounds, capacity, attemptID, sessionID)
		if !ok {
			return request, nil
		}
		workingRounds, summaryLines = foldedRounds, summary
	}
	content, tokens, err := supervisorSegmentReceiptContentStageWithSummary(workingRounds, plan.NativeRounds, capacity, attemptID, contractsOnly, summaryLines, sessionID)
	if err != nil {
		return llm.ChatRequest{}, err
	}
	request.Messages = append([]llm.Message(nil), request.Messages...)
	request.Messages[index] = supervisorSegmentReceiptMessage(sessionID, attemptID, content)
	metadata := make(map[string]string, len(request.Metadata)+2)
	for key, value := range request.Metadata {
		metadata[key] = value
	}
	metadata["context_segment_receipt_tokens"] = strconv.Itoa(tokens)
	metadata["context_segment_receipt_budget"] = strconv.Itoa(capacity)
	request.Metadata = metadata
	if estimateModelRequestTokens(request) > inputLimit {
		return llm.ChatRequest{}, apperror.New(apperror.CodeResourceExhausted, "dynamic receipt exceeds prepared input capacity")
	}
	return request, nil
}

func ownedReceiptMessageIndex(request llm.ChatRequest, owned llm.Message) (int, error) {
	index := -1
	for i, message := range request.Messages {
		if message.Role == owned.Role && message.Content == owned.Content && len(message.ToolCalls) == 0 && len(message.ToolResults) == 0 && message.Replay == nil && len(message.Images) == 0 {
			if index >= 0 {
				return -1, errors.New("duplicate owned receipt")
			}
			index = i
		}
	}
	if index < 0 {
		return -1, errors.New("owned receipt missing from prepared request")
	}
	return index, nil
}

// Newer current contracts precede boundary contracts; both precede either view's
// optional file bodies. Each stage measures the other view's actual message.
func fitSupervisorReceiptViews(request llm.ChatRequest, segment supervisorSegmentReceipt, boundary supervisorBoundaryReceipt, sessionID, attemptID string, inputLimit int, blockers []domain.SupervisorToolCall) (llm.ChatRequest, error) {
	for _, contractsOnly := range []bool{true, false} {
		if len(segment.ReceiptedRounds) > 0 {
			owned := supervisorSegmentReceiptMessage(sessionID, attemptID, segment.ReceiptContent)
			index, err := ownedReceiptMessageIndex(request, owned)
			if err != nil {
				return llm.ChatRequest{}, err
			}
			request, err = fitSupervisorSegmentReceiptStage(request, segment, sessionID, attemptID, inputLimit, contractsOnly)
			if err != nil {
				return llm.ChatRequest{}, err
			}
			// The wrapper was generated above, not selected by a text prefix.
			parts := strings.SplitN(request.Messages[index].Content, "\n", 2)
			var record struct {
				Content string `json:"content"`
			}
			if len(parts) != 2 || json.Unmarshal([]byte(parts[1]), &record) != nil || record.Content == "" {
				return llm.ChatRequest{}, errors.New("receipt envelope lost its content")
			}
			segment.ReceiptContent = record.Content
		}
		var err error
		request, err = fitSupervisorBoundaryReceiptRequest(request, boundary, inputLimit, blockers, contractsOnly)
		if err != nil {
			return llm.ChatRequest{}, err
		}
	}
	metadata := map[string]string{}
	for k, v := range request.Metadata {
		if k != "context_boundary_receipt_content" {
			metadata[k] = v
		}
	}
	request.Metadata = metadata
	return request, nil
}
