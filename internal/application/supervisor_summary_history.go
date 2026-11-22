package application

import (
	"encoding/json"
	"slices"

	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/session"
)

type summaryHistoryBlock struct {
	OriginalGoal    string               `json:"original_goal,omitempty"`
	Inherited       []contextmgr.Section `json:"inherited_context,omitempty"`
	PreviousHandoff string               `json:"previous_handoff,omitempty"`
	Messages        []contextmgr.Message `json:"messages,omitempty"`
}

type summaryModelInput struct {
	TaskID         string                          `json:"task_id"`
	WorkspaceID    string                          `json:"workspace_id"`
	SourceSHA256   string                          `json:"source_sha256"`
	MaxOutputChars int                             `json:"max_output_chars"`
	Focus          string                          `json:"optional_summary_focus,omitempty"`
	Omitted        int                             `json:"input_messages_omitted"`
	Timeline       []summaryHistoryBlock           `json:"timeline"`
	Chronology     *contextmgr.OperatorRestatement `json:"verified_operator_chronology,omitempty"`
}

const quotedSubmissionGuidance = `

The client retains verified_operator_chronology and its original user text separately. Leave that submission and its ordering to the source record. Use the summary for other requirements, subsequent changes, observed work and remaining tasks. Attribute statements about an older document to that document. A tool call completed in the history is already done.`

// ContextCompactionDataMessage re-encodes object keys during redaction.
// The array keeps the original goal and older handoff before newer source turns.
func addSummaryHistory(request *llm.ChatRequest, window llm.ContextWindow, runInputLimit int64, goal, focus string, inherited []contextmgr.Section, input contextmgr.SummaryGenerationRequest, omitted int) error {
	payload := summaryModelInput{TaskID: input.TaskID, WorkspaceID: input.WorkspaceID,
		SourceSHA256: input.SourceSHA256, MaxOutputChars: input.MaxOutputChars, Focus: focus, Omitted: omitted,
		Timeline: []summaryHistoryBlock{{OriginalGoal: goal, Inherited: inherited}}}
	var prior contextmgr.Section
	var err error
	if input.HasPrevious {
		prior, err = contextmgr.HandoffSection(input.Previous)
		if err != nil {
			return err
		}
		payload.Timeline = append(payload.Timeline, summaryHistoryBlock{PreviousHandoff: prior.ForNextSummary()})
	}
	payload.Timeline = append(payload.Timeline, summaryHistoryBlock{Messages: input.Messages})
	render := func() (llm.Message, error) {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return llm.Message{}, err
		}
		return llm.ContextCompactionDataMessage(encoded)
	}
	var data llm.Message
	var fact *contextmgr.OperatorRestatement
	// A missing user turn could have changed the subject again.
	if omitted == 0 {
		fact = prior.LatestOperatorRestatement(input.Messages)
	}
	if fact != nil {
		// The citation is optional. It cannot displace a selected original,
		// the goal, a required project rule, or the reserved model output.
		limit, err := window.InputLimit(request.PlannedOutputTokens(window))
		if err != nil {
			return err
		}
		payload.Chronology = fact
		annotated, err := render()
		// A citation at the JSON byte or node bound can also be left out.
		// Rendering the original payload below still reports errors in source data.
		if err == nil {
			base := request.Messages[0].Content
			request.Messages[0].Content = base + quotedSubmissionGuidance
			available := min(int64(limit), runInputLimit) - int64(estimateModelRequestTokens(*request))
			if int64(estimateModelMessageTokens(annotated)) <= available && exactSummaryQuotation(annotated, *fact) {
				data = annotated
			} else {
				request.Messages[0].Content = base
			}
		}
		payload.Chronology = nil
	}
	if data.Content == "" {
		data, err = render()
		if err != nil {
			return err
		}
	}
	request.Messages = slices.Insert(request.Messages, 1, data)
	request.Metadata["input_fingerprint"] = session.ContentSHA256(data.Content)
	return nil
}

// Nested JSON strings can change during provider redaction even after the
// original row passed plain-text redaction. Only label unchanged words as exact.
func exactSummaryQuotation(data llm.Message, expected contextmgr.OperatorRestatement) bool {
	var delivered struct {
		Chronology *contextmgr.OperatorRestatement `json:"verified_operator_chronology"`
	}
	return json.Unmarshal([]byte(data.Content), &delivered) == nil &&
		delivered.Chronology != nil && *delivered.Chronology == expected
}
