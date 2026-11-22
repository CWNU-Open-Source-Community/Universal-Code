package application

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/session"
)

type recentOperatorContextStore interface {
	ListCompactedThreadOperatorMessages(context.Context, string, int) ([]session.Message, error)
}

const (
	recentOperatorHistory   = "recent_operator_history"
	recentOperatorReference = "recent_operator_reference"
	recentOperatorExcerpt   = "recent_operator_excerpt"
)

func isRecentOperatorSection(kind string) bool {
	return kind == recentOperatorHistory || kind == recentOperatorReference || kind == recentOperatorExcerpt
}

func loadSupervisorHandoffArchive(ctx context.Context, store any, sessionID string, selection *contextmgr.Selection) error {
	var handoff *contextmgr.Section
	var originals []contextmgr.Message
	for i := range selection.Sections {
		section := &selection.Sections[i]
		switch section.Kind {
		case "summary":
			handoff = section
		case recentOperatorHistory:
			originals = append(originals, section.ReplayedOperatorMessage())
		}
	}
	if handoff == nil || len(originals) < 3 {
		return nil
	}
	prepared, err := handoff.LoadCompleteArchive(ctx, store, sessionID, originals)
	if err != nil {
		return err
	}
	*handoff = prepared
	return nil
}

func operatorReadback(section contextmgr.Section) contextmgr.Section {
	section.Kind = recentOperatorReference
	section.Content = fmt.Sprintf("A previous user message exceeds the available context. Read its full stored text with history_read using %s (SHA-256 %s).", section.SourceID, section.SourceContentSHA256)
	return section
}

func recentOperatorWireMessage(section contextmgr.Section) llm.Message {
	role := "user"
	if section.Kind == recentOperatorReference {
		role = "system"
	}
	return llm.Message{Role: role, Content: section.Content}
}

// openai/codex build_compacted_history_with_limit, rust-v0.169.1 (c376cf7396ef):
// select actual user turns newest first, then replay them chronologically before the summary.
// The Go store keeps an exact readback reference for oversized text.
func loadRecentOperatorContext(ctx context.Context, store any, runID string, memoryBudget int) ([]contextmgr.Section, error) {
	reader, ok := store.(recentOperatorContextStore)
	if !ok {
		return nil, nil
	}
	messages, err := reader.ListCompactedThreadOperatorMessages(ctx, runID, session.MaxRecentOperatorMessages)
	if err != nil {
		return nil, err
	}
	remaining := min(session.MaxRecentOperatorTokens, memoryBudget)
	var sections []contextmgr.Section
	for _, message := range messages {
		projected, user, err := session.ProjectCompactedOperator(message)
		if err != nil {
			return nil, err
		}
		if !user {
			continue
		}
		section := contextmgr.Section{Kind: recentOperatorHistory, SourceID: fmt.Sprintf("message:%d", message.ID),
			Content: projected.Content, SourceContentSHA256: message.Provenance.ContentSHA256, Priority: 950}
		section = section.WithOperatorExcerptSource(projected)
		cost := estimateModelMessageTokens(recentOperatorWireMessage(section))
		if len(section.Content) > contextmgr.MaxContextSectionBytes || cost > remaining {
			section = operatorReadback(section)
			cost = estimateModelMessageTokens(recentOperatorWireMessage(section))
		}
		if cost > remaining {
			break
		}
		remaining -= cost
		sections = append(sections, section)
	}
	return sections, nil
}

// Reserve required sources before allocating the chronological suffix of originals.
func fitRecentOperatorContext(sections []contextmgr.Section, budget int) ([]contextmgr.Section, []contextmgr.Source) {
	remaining := budget
	background := 0
	slots := llm.MaxModelContextSources
	for _, section := range sections {
		if isRecentOperatorSection(section.Kind) {
			continue
		}
		slots--
		if section.Excluded {
			continue
		}
		tokens := contextmgr.EstimateTokens(section.Content)
		if section.Required || section.Kind == "project_instruction" || section.Kind == "task_goal" ||
			section.Kind == "summary" || section.Kind == "continuity_context" || section.Kind == "agent_inbox" {
			remaining -= tokens
		} else {
			background += tokens
		}
	}
	// Keep the previous allocation when current work and notes need the room.
	// A thread with spare capacity can replay more of its original conversation.
	reserved := min(4096, budget/3)
	remaining = min(remaining, max(reserved, remaining-background))
	fitted := make([]contextmgr.Section, 0, len(sections))
	var omitted []contextmgr.Source
	for _, section := range sections {
		if !isRecentOperatorSection(section.Kind) {
			fitted = append(fitted, section)
			continue
		}
		// Each restored source now has its own audit identity. Bound the
		// lookback by the available source slots as well as by its token budget.
		if slots <= 0 {
			continue
		}
		slots--
		cost := contextmgr.EstimateTokens(section.Content)
		if cost > remaining && section.Kind == recentOperatorHistory {
			ref := operatorReadback(section)
			if refCost := contextmgr.EstimateTokens(ref.Content); refCost <= remaining {
				section, cost = ref, refCost
			}
		}
		if cost > remaining {
			omitted = append(omitted, contextmgr.Source{Kind: section.Kind, SourceID: section.SourceID, Tokens: cost, OmissionReason: "budget"})
			continue
		}
		remaining -= cost
		fitted = append(fitted, section)
	}
	return fitted, omitted
}

// Use the Go-owned positions and source IDs to fit verified originals after all
// tools, current input, output reserve and recovery limits are known.
func fitRecentOperatorRequest(request llm.ChatRequest, layout modelContextLayout, memory contextmgr.Selection, inputLimit int, originals []contextmgr.Section, runInputLimit int) (llm.ChatRequest, modelContextLayout, contextmgr.Selection, error) {
	if layout.recentOperatorCount == 0 {
		return request, layout, memory, nil
	}
	start := layout.recentOperatorStart
	invalid := func() (llm.ChatRequest, modelContextLayout, contextmgr.Selection, error) {
		return request, layout, memory, apperror.New(apperror.CodeFailedPrecondition, "retained operator history does not match its selected sources")
	}
	if start < 0 || start > layout.HistoryStart || layout.HistoryStart > len(request.Messages) ||
		layout.recentOperatorCount < 0 || layout.recentOperatorCount > layout.HistoryStart-start ||
		len(memory.Sections) != len(memory.IncludedSources) {
		return invalid()
	}
	end := start + layout.recentOperatorCount
	sources := make(map[string]contextmgr.Section, len(originals))
	for _, section := range originals {
		if isRecentOperatorSection(section.Kind) {
			sources[section.SourceID] = section
		}
	}
	remaining := inputLimit - estimateModelRequestTokens(request)
	memoryRemaining := memory.TokenBudget - memory.EstimatedTokens
	for _, message := range request.Messages[start:end] {
		remaining += estimateModelMessageTokens(message)
	}
	for i, section := range memory.Sections {
		if isRecentOperatorSection(section.Kind) {
			memoryRemaining += memory.IncludedSources[i].Tokens
		}
	}
	selected := contextmgr.Selection{TokenBudget: memory.TokenBudget, OmittedSources: slices.Clone(memory.OmittedSources)}
	replayed := make(map[string]contextmgr.Message, layout.recentOperatorCount)
	var retained []llm.Message
	hasReference := false
	position := end
	for i, section := range memory.Sections {
		source := memory.IncludedSources[i]
		if isRecentOperatorSection(section.Kind) {
			position--
			original, ok := sources[section.SourceID]
			if position < start || !ok || source.Kind != section.Kind || source.SourceID != section.SourceID {
				return invalid()
			}
			actual := request.Messages[position]
			expected := recentOperatorWireMessage(section)
			if actual.Role != expected.Role || actual.Content != expected.Content || actual.Replay != nil ||
				len(actual.Images)+len(actual.ToolCalls)+len(actual.ToolResults) != 0 {
				return invalid()
			}
			if section.Kind == recentOperatorExcerpt {
				// Refit a partial delivery from its readback reference on a retry.
				// Its source may be restored only after fitting the whole user turns.
				section = operatorReadback(section)
			}
			if original.Kind == recentOperatorHistory && section.Kind == recentOperatorReference {
				original = operatorReadback(original)
			}
			if original.Kind != section.Kind {
				return invalid()
			}
			// SelectSections trims display text. Use the ordinary history
			// projection, including its original whitespace and redactions.
			section = original
			message := recentOperatorWireMessage(section)
			cost, contentTokens := estimateModelMessageTokens(message), contextmgr.EstimateTokens(message.Content)
			if (cost > remaining || contentTokens > memoryRemaining) && section.Kind == recentOperatorHistory {
				section = operatorReadback(section)
				message = recentOperatorWireMessage(section)
				cost, contentTokens = estimateModelMessageTokens(message), contextmgr.EstimateTokens(message.Content)
			}
			if cost > remaining || contentTokens > memoryRemaining {
				source.OmissionReason = "budget"
				selected.OmittedSources = append(selected.OmittedSources, source)
				continue
			}
			remaining -= cost
			memoryRemaining -= contentTokens
			source.Kind, source.Tokens = section.Kind, contentTokens
			retained = append(retained, message)
			if section.Kind == recentOperatorHistory {
				replayed[section.SourceID] = section.ReplayedOperatorMessage()
			} else {
				hasReference = true
			}
		}
		selected.Sections = append(selected.Sections, section)
		selected.IncludedSources = append(selected.IncludedSources, source)
		selected.EstimatedTokens += source.Tokens
	}
	if position != start {
		return invalid()
	}
	slices.Reverse(retained)
	request.Messages = slices.Clone(request.Messages)
	// Only originals that survived both budgets can replace their handoff excerpts.
	// The complete view was reserved above, so reference-only sources keep theirs.
	for i, section := range selected.Sections {
		if section.Kind != "summary" {
			continue
		}
		if end >= layout.HistoryStart || request.Messages[end].Role != "assistant" || request.Messages[end].Content != section.Content {
			return invalid()
		}
		section = section.WithReplayedOperators(replayed)
		request.Messages[end].Content = section.Content
		selected.Sections[i] = section
		source := &selected.IncludedSources[i]
		tokens := contextmgr.EstimateTokens(section.Content)
		selected.EstimatedTokens += tokens - source.Tokens
		source.Tokens = tokens
	}
	request.Messages = slices.Replace(request.Messages, start, end, retained...)
	layout.HistoryStart += len(retained) - layout.recentOperatorCount
	layout.recentOperatorCount = len(retained)
	if hasReference {
		if err := fitRecentOperatorExcerpt(&request, layout, &selected, inputLimit, runInputLimit); err != nil {
			return request, layout, memory, err
		}
	}
	request.Metadata = maps.Clone(request.Metadata)
	if request.Metadata == nil {
		request.Metadata = make(map[string]string)
	}
	request.Metadata["memory_sections"] = strconv.Itoa(len(selected.Sections))
	request.Metadata["memory_omitted"] = strconv.Itoa(len(selected.OmittedSources))
	request.Metadata["memory_tokens"] = strconv.Itoa(selected.EstimatedTokens)
	return request, layout, selected, nil
}

// All complete turns and selected workspace records already have their places.
// Spend only spare capacity on the newest missing head/tail excerpt.
func fitRecentOperatorExcerpt(request *llm.ChatRequest, layout modelContextLayout, selected *contextmgr.Selection, inputLimit, runInputLimit int) error {
	memorySpare := selected.TokenBudget - selected.EstimatedTokens
	if memorySpare <= 0 {
		return nil
	}
	if layout.HistoryCount > 1 {
		// An optional excerpt must not itself trigger another compaction.
		inputLimit = contextmgr.AutoCompactInputThreshold(inputLimit) - 1
	}
	inputLimit = min(inputLimit, runInputLimit)
	start := layout.recentOperatorStart
	end := start + layout.recentOperatorCount
	operatorSpare := session.MaxRecentOperatorTokens
	for _, message := range request.Messages[start:end] {
		operatorSpare -= estimateModelMessageTokens(message)
	}
	wireSpare := min(inputLimit-estimateModelRequestTokens(*request), operatorSpare)
	if wireSpare <= 0 {
		return nil
	}
	var handoff contextmgr.Section
	for _, section := range selected.Sections {
		if section.Kind == "summary" {
			handoff = section
			break
		}
	}
	position := end
	for i, section := range selected.Sections {
		if !isRecentOperatorSection(section.Kind) {
			continue
		}
		position--
		if section.Kind != recentOperatorReference {
			continue
		}
		ref := &selected.IncludedSources[i]
		limit := min(wireSpare+estimateModelMessageTokens(request.Messages[position]),
			memorySpare+ref.Tokens+contextmgr.EstimateMessageTokens("user", ""))
		excerpt, fits, err := section.FitStoredOperatorExcerpt(handoff, limit)
		if err != nil {
			return err
		}
		if fits {
			section.Kind, section.Content = recentOperatorExcerpt, excerpt.Content
			selected.Sections[i] = section
			tokens := contextmgr.EstimateTokens(excerpt.Content)
			selected.EstimatedTokens += tokens - ref.Tokens
			ref.Kind, ref.Tokens = section.Kind, tokens
			request.Messages[position] = recentOperatorWireMessage(section)
			break
		}
	}
	return nil
}
