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
	TaskID         string                           `json:"task_id"`
	WorkspaceID    string                           `json:"workspace_id"`
	SourceSHA256   string                           `json:"source_sha256"`
	MaxOutputChars int                              `json:"max_output_chars"`
	Focus          string                           `json:"optional_summary_focus,omitempty"`
	Omitted        int                              `json:"input_messages_omitted"`
	Timeline       []summaryHistoryBlock            `json:"timeline"`
	Chronology     []contextmgr.OperatorRestatement `json:"verified_operator_chronology,omitempty"`
}

const quotedSubmissionGuidance = `

For the subject of verified_operator_chronology, report what the user wrote. Start the summary string with the later listed submission's source_id and its exact text, including punctuation. It is a separate user submission after the intervening message. If a still later user message in the timeline changes that subject, give its source_id and exact words next. Let the quotations stand on their own. Omit any paraphrase, conclusion or proposed action on that subject. Use the remaining space for independent user constraints, observed progress, attributed tool evidence, and still-pending work. Preserve any restriction on carrying it out.`

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
		payload.Timeline = append(payload.Timeline, summaryHistoryBlock{PreviousHandoff: prior.Content})
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
		payload.Chronology = []contextmgr.OperatorRestatement{*fact}
		annotated, err := render()
		// A citation at the JSON byte or node bound can also be left out.
		// Rendering the original payload below still reports errors in source data.
		if err == nil {
			base := request.Messages[0].Content
			request.Messages[0].Content = base + quotedSubmissionGuidance
			available := min(int64(limit), runInputLimit) - int64(estimateModelRequestTokens(*request))
			if int64(estimateModelMessageTokens(annotated)) <= available {
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
