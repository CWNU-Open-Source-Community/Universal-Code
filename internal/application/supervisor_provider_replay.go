package application

import (
	"context"
	"errors"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/providerhistory"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/toolgateway"
)

// Private continuation data is only loaded for this fenced native tool segment.
// It never becomes a session message, event payload, or public round DTO.
type supervisorProviderReplayStore interface {
	LoadSupervisorProviderReplay(context.Context, domain.SupervisorCheckpoint) (map[int]*llm.ProviderReplay, error)
}

type supervisorAssistantHistoryStore interface {
	LoadSupervisorAssistantHistory(context.Context, domain.SupervisorCheckpoint, []int64) (map[int64]providerhistory.Assistant, error)
}

type supervisorContextRecoveryStore interface {
	ClaimSupervisorContextRecovery(context.Context, domain.SupervisorCheckpoint, llm.ModelAttempt) (bool, error)
}

type supervisorContextRecoveryInputStore interface {
	CheckSupervisorContextRecoveryInput(context.Context, domain.SupervisorCheckpoint, llm.ModelAttempt) error
}

type supervisorContextRecoveryLimitStore interface {
	SupervisorContextRecoveryInputLimit(context.Context, domain.SupervisorCheckpoint, int, int) (int, bool, error)
}

func (s *AgentRunner) attachSupervisorProviderReplay(ctx context.Context, checkpoint domain.SupervisorCheckpoint,
	request llm.ChatRequest, rounds []domain.SupervisorToolRound,
) (llm.ChatRequest, error) {
	store, ok := s.store.(supervisorProviderReplayStore)
	if !ok || len(rounds) == 0 {
		return request, nil
	}
	states, err := store.LoadSupervisorProviderReplay(ctx, checkpoint)
	if err != nil {
		return llm.ChatRequest{}, err
	}
	return attachRecordedSupervisorProviderReplay(request, rounds, states)
}

// Used for both the live fenced segment and read-only recorded history. Only
// the live path loads the active checkpoint; historical rounds never enter
// resumeSupervisorTools or regain their former execution authority.
func attachRecordedSupervisorProviderReplay(request llm.ChatRequest, rounds []domain.SupervisorToolRound,
	states map[int]*llm.ProviderReplay,
) (llm.ChatRequest, error) {
	count := 0
	for _, round := range rounds {
		if round.ProjectHook() {
			count += len(round.Calls)
		} else {
			count += 2
		}
	}
	start := len(request.Messages) - count
	if start < 0 {
		return llm.ChatRequest{}, errors.New("provider replay has no native tool segment")
	}
	for _, round := range rounds {
		if round.ProjectHook() {
			start += len(round.Calls)
			continue
		}
		message := &request.Messages[start]
		start += 2
		replay := states[round.Round]
		if replay == nil {
			continue
		}
		if message.Role != "assistant" {
			return llm.ChatRequest{}, errors.New("provider replay has no assistant tool batch")
		}
		if err := replay.ValidateToolCalls(message.ToolCalls); err != nil {
			return llm.ChatRequest{}, err
		}
		message.Replay = replay.Clone()
		message.Content = replay.AssistantText()
	}
	return request, nil
}

func (s *AgentRunner) requiresSupervisorPrivateHistory(route string) (bool, error) {
	if s.router == nil {
		return false, nil
	}
	ref, err := supervisorModelRef(s.router, route)
	return err == nil && s.router.RequiresPrivateAssistantHistory(ref), err
}

func (s *AgentRunner) supervisorMessagesWithPrivateHistory(ctx context.Context, turn domain.SupervisorTurn,
	history []session.Message, messages []llm.Message, layout modelContextLayout,
) ([]llm.Message, modelContextLayout, error) {
	required, err := s.requiresSupervisorPrivateHistory(turn.Run.Config.ModelRoute)
	if err != nil || !required {
		return messages, layout, err
	}
	store, ok := s.store.(supervisorAssistantHistoryStore)
	if !ok {
		return nil, layout, apperror.New(apperror.CodeFailedPrecondition, "this provider requires durable private assistant history")
	}
	ids := make([]int64, 0, len(history)/2)
	for _, message := range history {
		if session.ProjectContextMessage(message).Role == "assistant" {
			ids = append(ids, message.ID)
		}
	}
	states, err := store.LoadSupervisorAssistantHistory(ctx, turn.Checkpoint, ids)
	if err != nil {
		return nil, layout, err
	}
	if layout.HistoryStart < 0 || layout.HistoryCount < 0 || layout.HistoryStart+layout.HistoryCount > len(messages) {
		return nil, layout, errors.New("private assistant history has an invalid message layout")
	}
	replacedEvidence := make(map[string]bool, len(states))
	for _, state := range states {
		if len(state.Rounds) > 0 {
			replacedEvidence["supervisor-tools:"+state.AttemptID] = true
		}
	}
	result := append([]llm.Message(nil), messages[:layout.HistoryStart]...)
	position := layout.HistoryStart
	for _, source := range history {
		projected := session.ProjectContextMessage(source)
		if projected.Role != "user" && projected.Role != "assistant" && projected.Role != "system" {
			continue
		}
		if position >= layout.HistoryStart+layout.HistoryCount {
			return nil, layout, errors.New("private assistant history source layout changed")
		}
		message := messages[position]
		position++
		// The public bounded evidence record remains stored/exportable. The
		// native request uses its exact recorded calls and results instead.
		if source.Provenance.SourceKind == session.SourceToolResult && replacedEvidence[source.Provenance.SourceRef] {
			continue
		}
		if projected.Role != "assistant" {
			result = append(result, message)
			continue
		}
		state, present := states[source.ID]
		if !present || !state.Replay.RequiresPrivateAssistantHistory() || len(message.Images) != 0 {
			return nil, layout, apperror.New(apperror.CodeFailedPrecondition, "assistant history lacks its native private source")
		}
		for _, round := range state.Rounds {
			for _, call := range round.Calls {
				if call.ToolName == string(toolgateway.BrowserScreenshotTool) && call.Status == domain.SupervisorToolCompleted {
					// The live screenshot reader is fenced to its original turn.
					// Do not silently omit its image/note from native history or
					// reuse an old turn's read authority under this new lease.
					return nil, layout, apperror.New(apperror.CodeFailedPrecondition,
						"native history with a browser screenshot requires its original image delivery; historical screenshot replay is unsupported")
				}
			}
		}
		segment, err := supervisorRequestWithToolRounds(llm.ChatRequest{}, state.Rounds)
		if err != nil {
			return nil, layout, err
		}
		segment, err = attachRecordedSupervisorProviderReplay(segment, state.Rounds, state.RoundReplay)
		if err != nil {
			return nil, layout, err
		}
		for _, native := range segment.Messages {
			if native.Role == "assistant" && !native.Replay.RequiresPrivateAssistantHistory() {
				return nil, layout, apperror.New(apperror.CodeFailedPrecondition, "assistant history lacks its complete native tool segment")
			}
		}
		result = append(result, segment.Messages...)
		result = append(result, llm.Message{Role: "assistant", Content: state.Replay.AssistantText(), Replay: state.Replay.Clone()})
	}
	if position != layout.HistoryStart+layout.HistoryCount {
		return nil, layout, errors.New("private assistant history has unmatched sources")
	}
	layout.HistoryCount = len(result) - layout.HistoryStart
	result = append(result, messages[position:]...)
	return result, layout, nil
}

func (s *AgentRunner) requestWithSupervisorToolRounds(ctx context.Context, checkpoint domain.SupervisorCheckpoint,
	base llm.ChatRequest, rounds []domain.SupervisorToolRound,
) (llm.ChatRequest, error) {
	request, err := supervisorRequestWithToolRounds(base, rounds)
	if err != nil {
		return llm.ChatRequest{}, err
	}
	return s.attachSupervisorProviderReplay(ctx, checkpoint, request, rounds)
}
