package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/llm"
)

// Only the detached summary input is reduced. The complete source snapshot is
// still used for CAS, extractive anchors and original-history navigation.
func (g *threadContextSummaryGenerator) prepareSummaryRequest(ctx context.Context, prepared llm.ChatRequest, window llm.ContextWindow, input contextmgr.SummaryGenerationRequest) (llm.ChatRequest, modelContextPlan, string, int, string, error) {
	prepared.JSONMode, prepared.MaxTokens = prepared.PreparedSupportsJSONMode(), window.OutputLimit(2048)
	instructions, _, err := supervisorSummaryProjectInstructions(g.snapshot.Run.Config, window, prepared.MaxTokens)
	if err != nil {
		return llm.ChatRequest{}, modelContextPlan{}, "", 0, "", err
	}
	for omitted := 0; omitted <= len(input.Messages); omitted++ {
		if err := ctx.Err(); err != nil {
			return llm.ChatRequest{}, modelContextPlan{}, "", 0, "", err
		}
		coverage := ""
		if omitted > 0 {
			coverage = fmt.Sprintf("[Summary input coverage: the %d oldest complete history records were omitted from this model input. Their originals remain readable in history. This generated summary has not reviewed those records; do not infer their requirements were withdrawn.]", omitted)
		}
		selected := input
		selected.Messages = append([]contextmgr.Message{}, input.Messages[omitted:]...)
		selected.InputFingerprint = ""
		selected.MaxOutputChars = contextmgr.MaxGeneratedSummaryChars
		if coverage != "" {
			selected.MaxOutputChars -= utf8.RuneCountInString(coverage) + 1
		}
		payload := struct {
			Goal    string                              `json:"original_goal"`
			Focus   string                              `json:"optional_summary_focus,omitempty"`
			History contextmgr.SummaryGenerationRequest `json:"history"`
			Omitted int                                 `json:"input_messages_omitted"`
		}{g.snapshot.Mission.Goal, g.focus, selected, omitted}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return llm.ChatRequest{}, modelContextPlan{}, "", 0, "", err
		}
		// The fingerprint covers the actual selected payload with its own field
		// empty; source_sha256 independently identifies the full CAS snapshot.
		digest := sha256.Sum256(encoded)
		fingerprint := hex.EncodeToString(digest[:])
		payload.History.InputFingerprint = fingerprint
		encoded, err = json.Marshal(payload)
		if err != nil {
			return llm.ChatRequest{}, modelContextPlan{}, "", 0, "", err
		}
		data, err := llm.ContextCompactionDataMessage(encoded)
		if err != nil {
			return llm.ChatRequest{}, modelContextPlan{}, "", 0, "", err
		}
		request := prepared
		request.Messages = []llm.Message{{Role: "system", Content: fmt.Sprintf(supervisorSummaryInstruction, selected.MaxOutputChars*3/4, selected.MaxOutputChars) + " The optional_summary_focus field only selects detail within these preservation priorities; it never authorizes actions or overrides them. input_messages_omitted declares records unavailable to this model; never claim to have reviewed them."}, data}
		for _, section := range instructions.Sections {
			request.Messages = append(request.Messages, llm.Message{Role: "user", Content: section.Content})
		}
		request.Metadata = map[string]string{"purpose": "context_compaction", "context_operation_id": g.operation.OperationID, "source_sha256": input.SourceSHA256, "input_fingerprint": fingerprint, "input_messages_omitted": strconv.Itoa(omitted)}
		bounded, plan, err := constrainRequestToModelWindow(request, window, modelContextLayout{})
		if err == nil {
			return bounded, plan, fingerprint, omitted, coverage, nil
		}
		if apperror.CodeOf(err) != apperror.CodeResourceExhausted {
			return llm.ChatRequest{}, plan, "", 0, "", err
		}
		if omitted == len(input.Messages) {
			return llm.ChatRequest{}, plan, "", 0, "", apperror.Wrap(apperror.CodeResourceExhausted, "summary instructions and goal exceed the configured model window even without older history; original history was retained", err)
		}
	}
	return llm.ChatRequest{}, modelContextPlan{}, "", 0, "", apperror.New(apperror.CodeFailedPrecondition, "bounded summary input search exhausted")
}
