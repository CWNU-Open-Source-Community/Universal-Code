package application

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/toolbudget"
)

type supervisorToolBoundaryStore interface {
	CompleteSupervisorToolBoundary(context.Context, domain.SupervisorCheckpoint, llm.ChatResponse,
		domain.RootAction, policy.Decision, time.Duration) (domain.Run, domain.SupervisorCheckpoint, session.TurnMessages, error)
	ToolBoundaryContextCalls(context.Context, domain.SupervisorCheckpoint) ([]domain.SupervisorToolCall, error)
}

type supervisorToolUsageStore interface {
	GetToolCallUsage(context.Context, string) (toolbudget.Usage, error)
}

type supervisorSegmentBoundaryState struct {
	Reached          bool
	MaxRoundsReached bool
	ToolBudgetZero   bool
	ContextPressure  bool
}

func (s supervisorSegmentBoundaryState) AllowsContinue() bool {
	return s.MaxRoundsReached || s.ToolBudgetZero
}

func evaluateSupervisorSegmentBoundary(nativeRounds int, totalRounds int, toolUsage toolbudget.Usage,
	privateHistory bool, contextNearLimit bool,
) supervisorSegmentBoundaryState {
	state := supervisorSegmentBoundaryState{
		MaxRoundsReached: nativeRounds >= domain.MaxSupervisorToolRounds,
		ToolBudgetZero:   totalRounds > 0 && toolUsage.Limit > 0 && toolUsage.Remaining == 0,
		ContextPressure:  privateHistory && totalRounds > 0 && contextNearLimit,
	}
	state.Reached = state.MaxRoundsReached || state.ToolBudgetZero || state.ContextPressure
	return state
}

func (s *AgentRunner) evaluateTurnSegmentBoundary(ctx context.Context, turn domain.SupervisorTurn,
	rounds []domain.SupervisorToolRound, privateHistory bool, contextNearLimit bool,
) (supervisorSegmentBoundaryState, error) {
	totalRounds := len(rounds)
	nativeRounds := supervisorNativeToolRounds(rounds)
	var toolUsage toolbudget.Usage
	if totalRounds > 0 && turn.Run.Budget.MaxToolCalls > 0 {
		if usageStore, ok := any(s.store).(supervisorToolUsageStore); ok && s.store != nil {
			usage, err := usageStore.GetToolCallUsage(ctx, turn.Run.ID)
			if err != nil {
				return supervisorSegmentBoundaryState{}, err
			}
			toolUsage = usage
		}
	}
	return evaluateSupervisorSegmentBoundary(nativeRounds, totalRounds, toolUsage, privateHistory, contextNearLimit), nil
}

func supervisorToolBoundaryRequest(request llm.ChatRequest) llm.ChatRequest {
	availability := supervisorToolBoundaryAvailability(request.Tools)
	request.Tools = nil
	request.Messages = append(append([]llm.Message(nil), request.Messages...), llm.Message{Role: "user",
		Content: "The Harness has reached the internal segment tool boundary. No tools are offered for this response. This is a scheduling boundary, not a task failure or task completion." + availability + " Return continue if the same accepted user task needs more work; the Harness can continue it in another segment within its existing budget. Return finish only if that task is actually complete, or wait only for real external input or approval. Do not claim a new user request or repeat completed actions.\n" + rootLifecycleBoundaryContinueFormat + "\n" + rootProtocolRepairOutputInstruction})
	return request
}

func supervisorContextPressureBoundaryRequest(request llm.ChatRequest) llm.ChatRequest {
	request.Tools = nil
	request.Messages = append(append([]llm.Message(nil), request.Messages...), llm.Message{
		Role: "user",
		Content: "The Harness has reached the private-history context window limit for this internal segment. No tools are offered for this response. Do not call tools or return continue. Return finish if the verified answer is ready, or wait for required external input or approval.\n" +
			`Response schema: return exactly one JSON object with version="root_lifecycle.v1", action="finish" or "wait", and a required nonempty message string. finish requires a nonempty summary and omits reason; wait requires a nonempty reason and omits summary.`,
	})
	return request
}

// The store moves to the next prepared segment in the same transaction that
// closes the previous segment. The wrapper keeps its lease and exact accepted
// input; a restart resumes that prepared segment through the same entry point.
func (s *AgentRunner) stepWithLeaseMode(ctx context.Context, lease domain.RunExecutionLease,
	requestedInput string, requireSteering bool, steeringMessageID string,
) (LifecycleResult, error) {
	var previous LifecycleResult
	for {
		step, err := s.stepSegmentWithLeaseMode(ctx, lease, requestedInput, requireSteering, steeringMessageID)
		if step.Turn == 0 && previous.Turn > 0 {
			previous.ToolBoundary = false
			return previous, err
		}
		if previous.Turn > 0 {
			step.ModelAttempts += previous.ModelAttempts
			step.ProtocolRepairs += previous.ProtocolRepairs
			step.ToolRounds += previous.ToolRounds
			step.ToolCalls += previous.ToolCalls
			step.StreamEvents += previous.StreamEvents
			step.StreamBytes += previous.StreamBytes
			step.Recovered = previous.Recovered
			step.ContextCompacted = step.ContextCompacted || previous.ContextCompacted
			if step.ContextSummaryID == 0 {
				step.ContextSummaryID = previous.ContextSummaryID
			}
		}
		if err != nil || !step.ToolBoundary {
			return step, err
		}
		previous = step
	}
}

// Keep the established generic result shape when space allows. Only body-like
// fields become explicitly named excerpts; claims and identities remain whole.
func compactToolBoundaryValue(value any, key string, depth int) any {
	if depth > 8 {
		return "[nested output omitted]"
	}
	switch value := value.(type) {
	case map[string]any:
		out := make(map[string]any)
		for field, nested := range value {
			if field == "original_result" {
				continue
			}
			if text, ok := nested.(string); ok {
				switch field {
				case "content", "body", "diff", "command", "script", "replacement", "patch", "stdout", "stderr":
					if text != "" {
						excerpt, _ := boundedWebContextText(text, 128)
						out[field+"_excerpt"] = excerpt
					}
					continue
				}
			}
			out[field] = compactToolBoundaryValue(nested, field, depth+1)
		}
		return out
	case []any:
		out := make([]any, 0, min(len(value), 3))
		for _, nested := range value[:min(len(value), 3)] {
			out = append(out, compactToolBoundaryValue(nested, key, depth+1))
		}
		return out
	case string:
		if toolBoundaryReceiptString(key) {
			return value
		}
		return boundedFailedEvidenceText(value, 512)
	default:
		return value
	}
}

// Preserve complete identities, claims, status/qualification flags and numeric
// cursors. Body text and descriptive prose live in the sealed history result.
func toolBoundaryReceiptValue(value any, key string, depth int) any {
	if depth > 8 {
		return "[nested output omitted]"
	}
	switch value := value.(type) {
	case map[string]any:
		out := make(map[string]any)
		for field, nested := range value {
			if _, ok := nested.(string); ok && !toolBoundaryReceiptString(field) {
				continue
			}
			if field == "original_result" {
				continue
			} // The outer receipt already binds the original raw result.
			out[field] = toolBoundaryReceiptValue(nested, field, depth+1)
		}
		return out
	case []any:
		out := make([]any, 0, min(len(value), 3))
		for _, nested := range value[:min(len(value), 3)] {
			out = append(out, toolBoundaryReceiptValue(nested, key, depth+1))
		}
		return out
	case string:
		return value
	default:
		return value
	}
}

func toolBoundaryReceiptString(key string) bool {
	if key == "id" || key == "path" || key == "url" || key == "canonical_url" || key == "digest" || key == "fingerprint" ||
		strings.HasSuffix(key, "_id") || strings.HasSuffix(key, "_sha256") || strings.HasSuffix(key, "_hash") || strings.HasSuffix(key, "_path") {
		return true
	}
	switch key {
	case "status", "state", "action", "expected_action", "operation", "kind", "type", "version", "protocol_version", "claim", "provenance", "search_policy", "source_state_at", "robots", "robots_policy", "error_code", "code":
		return true
	}
	return false
}

func toolBoundaryScalarMetadata(value any) bool {
	text := fmt.Sprint(value)
	if text == "true" || text == "false" {
		return true
	}
	var number json.Number
	return json.Unmarshal([]byte(text), &number) == nil && number != ""
}

func toolBoundaryContainsValue(value any, key, expected string) bool {
	switch value := value.(type) {
	case map[string]any:
		for field, nested := range value {
			if field == key && fmt.Sprint(nested) == expected {
				return true
			}
			if toolBoundaryContainsValue(nested, key, expected) {
				return true
			}
		}
	case []any:
		for _, nested := range value {
			if toolBoundaryContainsValue(nested, key, expected) {
				return true
			}
		}
	}
	return false
}

func toolBoundaryOmitClaim(value any) bool {
	omitted := false
	switch value := value.(type) {
	case map[string]any:
		if _, ok := value["claim"]; ok {
			delete(value, "claim")
			value["claim_omitted"] = true
			omitted = true
		}
		for _, nested := range value {
			omitted = toolBoundaryOmitClaim(nested) || omitted
		}
	case []any:
		for _, nested := range value {
			omitted = toolBoundaryOmitClaim(nested) || omitted
		}
	}
	return omitted
}

func toolBoundaryEvidenceMessage(sessionID, attemptID, content string) llm.Message {
	message := session.ProjectContextMessage(session.NewEvidenceMessage(sessionID, session.SourceToolResult,
		fmt.Sprintf("tool-boundary-%s", attemptID), content))
	return llm.Message{Role: message.Role, Content: message.Content}
}
