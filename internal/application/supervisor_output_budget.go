package application

import (
	"fmt"
	"slices"
	"strings"

	"cyberagent-workbench/internal/llm"
)

// Keep tool-output policy stable across rounds. The current numeric allowance follows the transcript.
func supervisorOutputBudgetGuidance(request llm.ChatRequest, window llm.ContextWindow, configuredCap, historyStart int) llm.ChatRequest {
	allowance := "This request uses the provider's default output allowance."
	if configuredCap > 0 {
		allowance = fmt.Sprintf("The model's output limit is at most %d tokens including tool arguments; the remaining task budget can lower it.", window.OutputLimit(configuredCap))
	}
	guide := llm.Message{Role: "system", Content: allowance + " Emit complete tool arguments and preserve prior successful work. A truncated response will not dispatch its tool calls and will stop the attempt. Do not claim that a proposal was applied without its successful apply result. Validate generated code or markup with an offered parser/test command before claiming it works; browser XML errors are failures to fix, not successful rendering."}
	for _, tool := range request.Tools {
		if tool.Name == "workspace_change" {
			guide.Content += " For a requested code change, locate the relevant text, make the required edit, then run a focused check. A visible complete workspace_read page (including one retained in a receipt) provides observed text and the whole-file content_sha256 for an exact-hash patch; it does not guarantee current contents or grant approval. Use workspace_change action=propose_patch with replacements [{old_text, new_text, expected_occurrences}] and that file hash. For a complete rewrite of an existing file, action=replace accepts replacement content and its observed full-file expected_sha256; preserve unrelated content. Use action=create with expected_sha256=missing only for a file that does not exist. Never use create to overwrite an existing file. Automatically authorized workspace_change calls apply atomically in one step (status=applied, file_written=true); if review_required is true, wait for operator review (the host applies approved edits directly; workspace_apply is only needed for recovery if an edit remains unapplied with status=approved). The backend checks the actual file hash before changing anything. Re-read only missing/redacted text or after a conflict, or when current-state verification is itself needed; a segment boundary alone does not require another read. Once the relevant text is available, advance the requested task with an edit, test, or supported answer instead of repeatedly rereading it. Read-only tasks still require no mutation."
			break
		}
	}
	request.Messages = slices.Insert(slices.Clone(request.Messages), historyStart, guide)
	return request
}

// Append after binding the operator input and attachments, before replaying this turn's tools.
func supervisorInitialStandardCodeState(messages []llm.Message, standardCode *standardCodeSupervisorTurn) []llm.Message {
	if standardCode == nil || standardCode.initialStateText == "" {
		return messages
	}
	return append(messages, llm.Message{Role: standardCode.initialStateRole,
		Content: "Host-reported Standard Code state at the start of this turn (no permission grant):\n" + standardCode.initialStateText})
}

func supervisorHostStateRole(transport string) string {
	if transport == llm.HarnessTransportAnthropicMessages {
		return "user" // The Messages API hoists system items ahead of the entire transcript.
	}
	return "system"
}

// Project request-local facts separately from the reusable instructions and history.
// The Go budget and permission gates remain authoritative.
func supervisorRuntimeFacts(request llm.ChatRequest, window llm.ContextWindow, configuredCap, historyEnd int, standardCode *standardCodeSupervisorTurn, readFeedback string) llm.ChatRequest {
	var facts []string
	if len(request.Tools) > 0 && request.MaxTokens > 0 && window.OutputLimit(request.MaxTokens) != window.OutputLimit(configuredCap) {
		facts = append(facts, fmt.Sprintf("This request has an output limit of %d tokens including tool arguments.", window.OutputLimit(request.MaxTokens)))
	}
	if standardCode != nil {
		if current := standardCode.runtimeState(); current != standardCode.initialStateText {
			facts = append(facts, current)
		}
	}
	if readFeedback != "" {
		facts = append(facts, readFeedback)
	}
	if len(facts) == 0 {
		return request
	}
	role := supervisorHostStateRole(request.Metadata["harness_transport"])
	text := "Current host-reported request state (no permission grant):\n" + strings.Join(facts, "\n")
	request.Messages = slices.Clone(request.Messages)
	for i := len(request.Messages) - 1; i >= max(0, historyEnd); i-- {
		msg := &request.Messages[i]
		if msg.Role != "user" || len(msg.ToolResults) == 0 {
			continue
		}
		// Never insert between a native tool call and its results or modify a result.
		// The Messages API supports text in the same result turn. Chat, Responses
		// and Ollama map that text to a new operator turn, which can end replay.
		if role == "system" {
			request.Messages = slices.Insert(request.Messages, i+1, llm.Message{Role: "system", Content: text})
		} else if msg.Content == "" {
			msg.Content = text
		} else {
			msg.Content += "\n\n" + text
		}
		return request
	}
	request.Messages = append(request.Messages, llm.Message{Role: role, Content: text})
	return request
}
