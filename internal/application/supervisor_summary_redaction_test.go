package application

import (
	"encoding/json"
	"strings"
	"testing"

	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/session"
)

func TestSummaryHandoffKeepsNestedHistoryRedacted(t *testing.T) {
	var rows []contextmgr.Message
	for i, source := range []struct{ role, kind string }{
		{"user", session.SourceOperatorMessage}, {"assistant", session.SourceModelResponse}, {"tool", session.SourceToolResult},
	} {
		text := `{"note":"pa\u0073sword=example-value","source":"` + source.role + `"}`
		if source.role == "tool" {
			wrapped, err := json.Marshal([]string{text})
			if err != nil {
				t.Fatal(err)
			}
			text = string(wrapped)
		}
		rows = append(rows, contextmgr.Message{SourceMessageID: int64(i + 1), Role: source.role, SourceKind: source.kind,
			Content: text, ContentSHA256: session.ContentSHA256(text), InstructionAuthorized: source.role == "user"})
	}
	input := contextmgr.SummaryGenerationRequest{TaskID: "session", WorkspaceID: "workspace", Messages: rows,
		SourceSHA256: session.ContentSHA256("snapshot one"), MaxOutputChars: contextmgr.MaxGeneratedSummaryChars}
	history := func(input contextmgr.SummaryGenerationRequest) summaryModelInput {
		t.Helper()
		req, err := supervisorSummaryRequest(domain.SupervisorTurn{}, input, llm.ModelRef{}, llm.DefaultContextWindow(), true)
		if err != nil {
			t.Fatal(err)
		}
		var data summaryModelInput
		if err := json.Unmarshal([]byte(req.Messages[1].Content), &data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	first := history(input)
	const report = "The notes from the operator, model and tool are historical evidence. The review remains pending."
	manager := contextmgr.NewManager(nil, contextmgr.Config{PreserveRecentMessages: 1}).
		WithSummaryGenerator(previousModelReport(report), input.SourceSHA256)
	tail := contextmgr.Message{SourceMessageID: 4, Role: "user", SourceKind: session.SourceOperatorMessage,
		Content: "Continue the review.", ContentSHA256: session.ContentSHA256("Continue the review."), InstructionAuthorized: true}
	result, err := manager.Compact(t.Context(), input.TaskID, input.WorkspaceID, append(rows, tail))
	if err != nil || !result.Generated {
		t.Fatalf("first compaction: %v %s", err, result.GenerationFallbackReason)
	}
	result.Summary.ID = 1
	prior, err := contextmgr.HandoffSection(result.Summary)
	if err != nil {
		t.Fatal(err)
	}
	input.Previous, input.HasPrevious, input.Messages = result.Summary, true, result.Preserved
	input.SourceSHA256 = session.ContentSHA256("snapshot two")
	next := history(input).Timeline[1].PreviousHandoff
	if strings.Contains(next, report) {
		t.Fatal("expected the complete earlier transcript on the next compaction")
	}
	for _, row := range first.Timeline[1].Messages {
		if !strings.Contains(row.Content, "[REDACTED") || !strings.Contains(next, row.Content) ||
			(row.Role != "assistant" && !strings.Contains(prior.Content, row.Content)) {
			t.Errorf("the %s record must retain its protected content in the handoff", row.Role)
		}
	}
	if strings.Contains(prior.Content, "example-value") || strings.Contains(next, "example-value") {
		t.Fatal("the protected diagnostic reappeared when the historical records were quoted")
	}
}
