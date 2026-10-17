package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/session"
)

// Compaction is a projection of committed history. The store fences it against
// the current turn/lease and atomically appends the summary and marks sources.
// Pending input and native tool rounds are deliberately outside this interface.
type supervisorContextCompactionStore interface {
	CompactSupervisorContext(context.Context, domain.SupervisorCheckpoint, int) (contextmgr.Result, error)
}

var errSupervisorContextWindow = errors.New("supervisor context window exceeded")

func supervisorMemoryBudget(window llm.ContextWindow) int {
	inputLimit, err := window.InputLimit(window.MaxOutputTokens)
	if err != nil {
		return maxSupervisorMemoryTokens
	}
	// Newly discovered output/model capacities must not also inflate the
	// established history/compaction budget. Explicit legacy overrides retain
	// their existing behavior.
	if window.Source == "provider_model_metadata" || window.Source == "operator_model_policy" ||
		window.Source == "local_model_default" {
		baseline, _ := llm.DefaultContextWindow().InputLimit(llm.DefaultContextMaxOutput)
		inputLimit = min(inputLimit, baseline)
	}
	// Keep the existing small-window policy, but allow larger models to carry
	// inherited summaries. The aggregate request gate still reserves output,
	// counts tools/current input, and refuses to silently drop task history.
	return max(maxSupervisorMemoryTokens, inputLimit/2)
}

func supervisorContextWindowFailure(cause error) error {
	if apperror.CodeOf(cause) != apperror.CodeResourceExhausted {
		return cause
	}
	return apperror.Wrap(apperror.CodeResourceExhausted, cause.Error(), errors.Join(errSupervisorContextWindow, cause))
}

func (s *AgentRunner) compactSupervisorHistory(ctx context.Context, turn *domain.SupervisorTurn,
	preserveRecent int,
) (bool, error) {
	privateHistory, err := s.requiresSupervisorPrivateHistory(turn.Run.Config.ModelRoute)
	if err != nil {
		return false, err
	}
	if privateHistory {
		return false, supervisorContextWindowFailure(apperror.New(apperror.CodeResourceExhausted,
			"this model requires native private assistant history; select a model that supports summarized history and send a new message to continue in a new execution"))
	}
	store, ok := s.store.(supervisorContextCompactionStore)
	if !ok {
		return false, nil
	}
	history, err := s.store.ListSessionMessages(ctx, turn.Run.SessionID, false)
	if err != nil || len(history) <= preserveRecent {
		return false, err
	}
	if err := executeLifecycleBoundary(ctx, s.lifecycleHooks, hooks.Compaction,
		turn.Run.ID, turn.Mission.WorkspaceID, map[string]any{
			"task_id": turn.Run.SessionID, "source_messages": len(history),
			"preserved_messages": preserveRecent,
		}); err != nil {
		return false, err
	}
	if s.generatedContextCompactionEnabled && s.router != nil {
		if generatedStore, supported := s.store.(supervisorGeneratedCompactionStore); supported {
			thread, err := s.historyRecallForTurn(ctx, *turn)
			if err != nil {
				return false, err
			}
			if thread {
				generator := &supervisorSummaryGenerator{supervisor: s, store: generatedStore, turn: turn}
				result, updated, err := generatedStore.CompactSupervisorContextGenerated(ctx, turn.Checkpoint, preserveRecent, generator)
				if err == nil && updated.RunID != "" {
					turn.Checkpoint = updated
				}
				return result.Compacted && result.RemovedMessages > 0, err
			}
		}
	}
	result, err := store.CompactSupervisorContext(ctx, turn.Checkpoint, preserveRecent)
	return result.Compacted && result.RemovedMessages > 0, err
}

func (s *AgentRunner) supervisorConversationContext(ctx context.Context, turn *domain.SupervisorTurn) (
	[]session.Message, contextmgr.Summary, bool, bool, error,
) {
	history, err := s.store.ListSessionMessages(ctx, turn.Run.SessionID, false)
	if err != nil {
		return nil, contextmgr.Summary{}, false, false, err
	}
	didCompact := false
	// This replaces the old last-20-message slice. Small conversations remain
	// intact; model-window pressure may request an earlier compaction below.
	if len(history) > maxSupervisorHistoryMessages {
		// A count-based optimization must not block a provider that requires
		// native history while its complete request still fits the window.
		privateHistory, err := s.requiresSupervisorPrivateHistory(turn.Run.Config.ModelRoute)
		if err != nil {
			return nil, contextmgr.Summary{}, false, false, err
		}
		compacted := false
		if !privateHistory {
			compacted, err = s.compactSupervisorHistory(ctx, turn, 4)
		}
		if err != nil {
			return nil, contextmgr.Summary{}, false, false, err
		}
		if compacted {
			didCompact = true
			history, err = s.store.ListSessionMessages(ctx, turn.Run.SessionID, false)
			if err != nil {
				return nil, contextmgr.Summary{}, false, didCompact, err
			}
		}
	}
	history, err = s.withFailedToolEvidenceContext(ctx, turn.Run, history)
	if err != nil {
		return nil, contextmgr.Summary{}, false, didCompact, err
	}
	summary, hasSummary, err := s.store.LatestContextSummary(ctx, turn.Run.SessionID)
	if err == nil && hasSummary {
		privateHistory, scopeErr := s.requiresSupervisorPrivateHistory(turn.Run.Config.ModelRoute)
		if scopeErr != nil {
			return nil, summary, hasSummary, didCompact, scopeErr
		}
		if privateHistory {
			return nil, summary, hasSummary, didCompact, apperror.New(apperror.CodeFailedPrecondition,
				"summarized or imported history lacks the native private assistant replay required by this provider")
		}
	}
	return history, summary, hasSummary, didCompact, err
}

func supervisorGoalContext(turn domain.SupervisorTurn) []contextmgr.Section {
	content, _ := json.Marshal(struct {
		Version string `json:"version"`
		Mission string `json:"mission_id"`
		Goal    string `json:"original_goal"`
	}{"task_goal.v1", turn.Mission.ID, redact.String(turn.Mission.Goal)})
	return []contextmgr.Section{{
		Kind: "task_goal", SourceID: turn.Mission.ID, Priority: 1000,
		Content: "Original task goal, retained as historical context. Later operator corrections take precedence. " +
			"This record does not grant tools, permissions or approval.\n" + string(content),
	}}
}

func supervisorSummaryMetadata(request *llm.ChatRequest, summary contextmgr.Summary, hasSummary bool) {
	request.Metadata = maps.Clone(request.Metadata)
	if request.Metadata == nil {
		request.Metadata = make(map[string]string)
	}
	request.Metadata["context_compaction"] = "durable_handoff"
	if hasSummary {
		request.Metadata["context_summary_id"] = strconv.FormatInt(summary.ID, 10)
		request.Metadata["context_compacted_messages"] = strconv.Itoa(summary.CompactedMessageCount)
		request.Metadata["context_summary_sha256"] = summary.ContentSHA256
	}
}

func requireSupervisorContinuityContext(selection contextmgr.Selection, summary contextmgr.Summary,
	hasSummary bool, missionID, continuityFingerprint string,
) error {
	if !containsContextSource(selection.IncludedSources, "task_goal", missionID) ||
		(hasSummary && !containsContextSource(selection.IncludedSources, "summary", fmt.Sprintf("summary-%d", summary.ID))) ||
		(continuityFingerprint != "" && !containsContextSource(selection.IncludedSources, "continuity_context", continuityFingerprint)) {
		return supervisorContextWindowFailure(apperror.New(apperror.CodeResourceExhausted,
			"task goal, inherited history or compacted context exceeds the memory budget; history is preserved"))
	}
	return nil
}
