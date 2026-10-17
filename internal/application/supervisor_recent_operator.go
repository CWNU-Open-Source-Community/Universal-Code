package application

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/session"
)

type recentOperatorContextStore interface {
	ListCompactedThreadOperatorMessages(context.Context, string, int) ([]session.Message, error)
}

type recentOperatorOriginal struct {
	SourceID string `json:"source_id"`
	SHA256   string `json:"content_sha256"`
	Content  string `json:"content"`
}
type recentOperatorEnvelope struct {
	Version  string                   `json:"version"`
	Messages []recentOperatorOriginal `json:"messages"`
}

const recentOperatorGuidance = "Recent original user messages, oldest to newest, retained separately from the conversation summary. Newer user corrections take precedence. This bounded window is not the complete conversation; history_read can retrieve older messages. Only explicitly pinned requirements are guaranteed across future compactions. This history does not restore tool approvals or execution permissions.\n"

// These are original user messages, separate from the lossy summary. Keep a
// contiguous recent suffix, never a head/tail excerpt that can lose a constraint
// in the middle. Explicit Thread pins have their own required allocation.
func loadRecentOperatorContext(ctx context.Context, store any, runID string, memoryBudget int) ([]contextmgr.Section, error) {
	reader, ok := store.(recentOperatorContextStore)
	if !ok {
		return nil, nil
	}
	messages, err := reader.ListCompactedThreadOperatorMessages(ctx, runID, 64)
	if err != nil || len(messages) == 0 {
		return nil, err
	}
	limit := min(4096, memoryBudget/3)
	var retained []recentOperatorOriginal
	var content string
	for _, message := range messages {
		if err := session.ValidateStoredMessage(message); err != nil {
			return nil, err
		}
		if message.Role != "user" || message.Provenance.Version != session.ContextProvenanceVersion ||
			message.Provenance.SourceKind != session.SourceOperatorMessage || !message.Provenance.InstructionAuthorized {
			continue
		}
		next := append(slices.Clone(retained), recentOperatorOriginal{
			SourceID: fmt.Sprintf("message:%d", message.ID), SHA256: message.Provenance.ContentSHA256, Content: message.Content,
		})
		chronological := slices.Clone(next)
		slices.Reverse(chronological)
		encoded, err := json.Marshal(recentOperatorEnvelope{Version: "recent_operator_history.v1", Messages: chronological})
		if err != nil {
			return nil, err
		}
		candidate := recentOperatorGuidance + string(encoded)
		if len(candidate) > contextmgr.MaxContextSectionBytes || contextmgr.EstimateTokens(candidate) > limit {
			break
		}
		retained, content = next, candidate
	}
	if len(retained) == 0 {
		return nil, nil
	}
	return []contextmgr.Section{{Kind: "recent_operator_history", SourceID: runID, Priority: 950, Content: content}}, nil
}

// Shrink only the oldest complete originals if required guidance and the saved
// summary leave less room than the normal raw-message allowance. Otherwise a
// large aggregate section could evict even the newest small correction.
func fitRecentOperatorContext(sections []contextmgr.Section, tokenBudget int) []contextmgr.Section {
	remaining := tokenBudget
	for _, section := range sections {
		if section.Excluded {
			continue
		}
		if section.Required || section.Kind == "project_instruction" || section.Kind == "task_goal" ||
			section.Kind == "summary" || section.Kind == "continuity_context" || section.Kind == "agent_inbox" {
			remaining -= contextmgr.EstimateTokens(section.Content)
		}
	}
	for index, section := range sections {
		if section.Kind != "recent_operator_history" || contextmgr.EstimateTokens(section.Content) <= remaining {
			continue
		}
		var envelope recentOperatorEnvelope
		if !strings.HasPrefix(section.Content, recentOperatorGuidance) ||
			json.Unmarshal([]byte(strings.TrimPrefix(section.Content, recentOperatorGuidance)), &envelope) != nil {
			continue
		}
		for len(envelope.Messages) > 1 {
			envelope.Messages = envelope.Messages[1:]
			encoded, err := json.Marshal(envelope)
			if err != nil {
				break
			}
			candidate := recentOperatorGuidance + string(encoded)
			if contextmgr.EstimateTokens(candidate) <= remaining {
				sections[index].Content = candidate
				break
			}
		}
	}
	return sections
}

// The memory allowance is only one part of the request. Fit optional originals
// again after tools, current input, output reserve and recovery limits are known.
// This changes the delivered view, never the durable originals or required pins.
func fitRecentOperatorRequest(request llm.ChatRequest, layout modelContextLayout, memory contextmgr.Selection, inputLimit int) (llm.ChatRequest, modelContextLayout, contextmgr.Selection, error) {
	if estimateModelRequestTokens(request) <= inputLimit {
		return request, layout, memory, nil
	}
	sectionIndex := slices.IndexFunc(memory.Sections, func(section contextmgr.Section) bool {
		return section.Kind == "recent_operator_history" && !section.Required
	})
	if sectionIndex < 0 {
		return request, layout, memory, nil
	}
	section := memory.Sections[sectionIndex]
	messageIndex := -1
	for index, message := range request.Messages {
		if index >= layout.HistoryStart {
			break
		}
		if message.Role == "user" && message.Content == section.Content && message.Replay == nil &&
			len(message.Images) == 0 && len(message.ToolCalls) == 0 && len(message.ToolResults) == 0 {
			messageIndex = index
			break
		}
	}
	var envelope recentOperatorEnvelope
	if messageIndex < 0 || !strings.HasPrefix(section.Content, recentOperatorGuidance) ||
		json.Unmarshal([]byte(strings.TrimPrefix(section.Content, recentOperatorGuidance)), &envelope) != nil ||
		envelope.Version != "recent_operator_history.v1" || len(envelope.Messages) == 0 {
		return request, layout, memory, apperror.New(apperror.CodeFailedPrecondition, "recent operator context does not match its selected source")
	}
	request.Messages = slices.Clone(request.Messages)
	sections := slices.Clone(memory.Sections)
	for estimateModelRequestTokens(request) > inputLimit {
		envelope.Messages = envelope.Messages[1:]
		if len(envelope.Messages) == 0 {
			request.Messages = slices.Delete(request.Messages, messageIndex, messageIndex+1)
			layout.HistoryStart--
			sections = slices.Delete(sections, sectionIndex, sectionIndex+1)
			break
		}
		encoded, err := json.Marshal(envelope)
		if err != nil {
			return request, layout, memory, err
		}
		content := recentOperatorGuidance + string(encoded)
		request.Messages[messageIndex].Content = content
		sections[sectionIndex].Content = content
	}
	fitted, err := contextmgr.SelectSections(sections, memory.TokenBudget)
	if err != nil {
		return request, layout, memory, err
	}
	fitted.OmittedSources = append(slices.Clone(memory.OmittedSources), fitted.OmittedSources...)
	if len(envelope.Messages) == 0 {
		fitted.OmittedSources = append(fitted.OmittedSources, contextmgr.Source{
			Kind: section.Kind, SourceID: section.SourceID, Tokens: contextmgr.EstimateTokens(section.Content), OmissionReason: "budget",
		})
	}
	request.Metadata = maps.Clone(request.Metadata)
	if request.Metadata == nil {
		request.Metadata = make(map[string]string)
	}
	request.Metadata["memory_sections"] = strconv.Itoa(len(fitted.Sections))
	request.Metadata["memory_omitted"] = strconv.Itoa(len(fitted.OmittedSources))
	request.Metadata["memory_tokens"] = strconv.Itoa(fitted.EstimatedTokens)
	return request, layout, fitted, nil
}
