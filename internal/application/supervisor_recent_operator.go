package application

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"cyberagent-workbench/internal/contextmgr"
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
