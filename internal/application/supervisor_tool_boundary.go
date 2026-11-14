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
)

type supervisorToolBoundaryStore interface {
	CompleteSupervisorToolBoundary(context.Context, domain.SupervisorCheckpoint, llm.ChatResponse,
		domain.RootAction, policy.Decision, time.Duration) (domain.Run, domain.SupervisorCheckpoint, session.TurnMessages, error)
	ToolBoundaryContextCalls(context.Context, domain.SupervisorCheckpoint) ([]domain.SupervisorToolCall, error)
}

func supervisorToolBoundaryRequest(request llm.ChatRequest) llm.ChatRequest {
	availability := supervisorToolBoundaryAvailability(request.Tools)
	request.Tools = nil
	request.Messages = append(append([]llm.Message(nil), request.Messages...), llm.Message{Role: "user",
		Content: "The Harness has completed the four tool rounds available in this internal segment. No tools are offered for this response. This is a scheduling boundary, not a task failure or task completion." + availability + " Return continue if the same accepted user task needs more work; the Harness can continue it in another segment within its existing budget. Return finish only if that task is actually complete, or wait only for real external input or approval. Do not claim a new user request or repeat completed actions.\n" + rootLifecycleBoundaryContinueFormat + "\n" + rootProtocolRepairOutputInstruction})
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
