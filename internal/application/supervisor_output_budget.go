package application

import (
	"cyberagent-workbench/internal/llm"
	"fmt"
)

// Explain the effective cap without raising it or retrying a truncated response.
// Every emitted tool request still has to be complete and valid.
func supervisorOutputBudgetGuidance(request llm.ChatRequest, window llm.ContextWindow) llm.ChatRequest {
	allowance := "This request uses the provider's default output allowance."
	if request.MaxTokens > 0 {
		allowance = fmt.Sprintf("This request has an output limit of %d tokens including tool arguments.", window.OutputLimit(request.MaxTokens))
	}
	guide := llm.Message{Role: "system", Content: allowance + " Emit complete tool arguments and preserve prior successful work. A truncated response will not dispatch its tool calls and will stop the attempt. Do not claim that a proposal was applied without its successful apply result. Validate generated code or markup with an offered parser/test command before claiming it works; browser XML errors are failures to fix, not successful rendering."}
	for _, tool := range request.Tools {
		if tool.Name == "workspace_change" {
			guide.Content += " For a requested code change, locate the relevant text, make the required edit, then run a focused check. A visible complete workspace_read page (including one retained in a receipt) provides observed text and the whole-file content_sha256 for an exact-hash patch; it does not guarantee current contents or grant approval. Use workspace_change action=propose_patch with replacements [{old_text, new_text, expected_occurrences}] and that file hash. For a complete rewrite of an existing file, action=replace accepts replacement content and its observed full-file expected_sha256; preserve unrelated content. Use action=create with expected_sha256=missing only for a file that does not exist. Never use create to overwrite an existing file. Automatically authorized workspace_change calls apply atomically in one step (status=applied, file_written=true); if review_required is true, wait for operator review (the host applies approved edits directly; workspace_apply is only needed for recovery if an edit remains unapplied with status=approved). The backend checks the actual file hash before changing anything. Re-read only missing/redacted text or after a conflict, or when current-state verification is itself needed; a segment boundary alone does not require another read. Once the relevant text is available, advance the requested task with an edit, test, or supported answer instead of repeatedly rereading it. Read-only tasks still require no mutation."
			break
		}
	}
	request.Messages = append([]llm.Message{guide}, request.Messages...)
	return request
}
