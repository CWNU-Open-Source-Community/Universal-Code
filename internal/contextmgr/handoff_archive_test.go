package contextmgr

import (
	"context"
	"slices"
	"strings"
	"testing"

	"cyberagent-workbench/internal/redact"
)

func TestCompleteArchiveReplacesTheInterpretationOnlyForAnActualReplayedRestatement(t *testing.T) {
	msg := func(id int64, role, text, kind string, authority bool) Message {
		m := rollingMessage(id, role, text, kind, authority)
		if role != "tool" {
			m.SourceRef = ""
		}
		return m
	}
	a := msg(2, "user", "Keep the published name.", "operator_message", true)
	b := msg(6, "user", "Use the temporary name.", "operator_message", true)
	repeat := msg(9, "user", a.Content, "operator_message", true)
	c := msg(12, "user", "Use the name on the next sheet.", "operator_message", true)
	tool := msg(14, "tool", "Old draft\n"+b.Content, "tool_result", false)
	protected := msg(15, "tool", `{"note":"pa\u0073sword=archive-fixture-secret"}`, "tool_result", false)
	input := []Message{a, msg(4, "assistant", "Acknowledged.", "model_response", false), b, repeat, c, tool,
		protected, msg(16, "assistant", "Reported the old draft.", "model_response", false)}
	manager := NewManager(nil, Config{PreserveRecentMessages: 1}).WithSummaryGenerator(summaryGeneratorFunc(
		func(_ context.Context, r SummaryGenerationRequest) (SummaryGenerationResponse, error) {
			return SummaryGenerationResponse{Text: strings.Repeat("The previous report says to keep the temporary name. ", 12), Receipt: generationReceipt(r)}, nil
		}), handoffContentSHA256("archive-regression"))
	result, err := manager.PrepareCandidate(t.Context(), "archive", "ws", append(append([]Message{}, input...), msg(18, "assistant", "Live tail.", "model_response", false)), Summary{}, false)
	if err != nil || !result.Generated {
		t.Fatalf("generate historical handoff: %v", err)
	}
	result.Summary.ID = 7
	base, err := HandoffSection(result.Summary)
	if err != nil {
		t.Fatal(err)
	}
	count, through, limit := base.ArchiveWindow()
	if count != len(input) || through != input[len(input)-1].SourceMessageID || limit != len(base.Content) {
		t.Fatal("the store lookup was not bounded by the committed count, last ID and actual reservation")
	}
	copied := slices.Clone(input)
	candidate, err := base.WithCompleteArchive(copied)
	if err != nil || candidate.Content != base.Content {
		t.Fatalf("attaching a possible projection changed its reservation: %v", err)
	}
	copied[0].Content = "overwritten after attachment"
	replayed := map[string]Message{"message:2": a, "message:6": b, "message:9": repeat, "message:12": c}
	got := candidate.WithReplayedOperators(replayed).Content
	if !strings.Contains(got, "Archived messages, earliest to latest") || strings.Contains(got, "Previous model's handoff") {
		t.Fatalf("the archived evidence did not replace the earlier model interpretation: %s", got)
	}
	if !strings.Contains(got, "message:2 → message:6 → message:9") || !strings.Contains(got, "Later replayed user messages include message:12.") ||
		!strings.Contains(got, "message:14 (tool, tool_result, \"source-14\", SHA-256 "+tool.ContentSHA256+")\n> Old draft\n> "+b.Content) ||
		strings.Contains(got, "archive-fixture-secret") || strings.Contains(got, copied[0].Content) || len(got) > len(base.Content) || EstimateTokens(got) > EstimateTokens(base.Content) {
		t.Fatal("the archive lost source attribution or exceeded its reservation")
	}
	previous := -1
	for _, record := range input {
		safe, err := redact.ModelHistoryText(record.Content)
		if err != nil {
			t.Fatal(err)
		}
		pos := strings.Index(got, quoteHandoffText(safe))
		if record.SourceMessageID == repeat.SourceMessageID {
			pos = strings.Index(got, "message:9 (user, operator_message)")
		}
		if pos <= previous {
			t.Fatalf("archived message %d is absent or out of order", record.SourceMessageID)
		}
		previous = pos
	}
	if strings.Contains(base.WithReplayedOperators(replayed).Content, "Archived messages, earliest to latest") {
		t.Fatal("attaching originals affected a different copy of the selected section")
	}
	t.Run("only the prefix fits before its footer", func(t *testing.T) {
		tight := candidate
		tight.Content = candidate.archive.text
		if view := tight.WithReplayedOperators(replayed).Content; strings.Contains(view, "Archived messages, earliest to latest") || !strings.Contains(view, "Previous model's handoff") {
			t.Fatal("the source view consumed more than the reserved handoff")
		}
	})
	for _, tc := range []struct {
		name    string
		change  func([]Message) []Message
		wantErr bool
	}{
		{"missing", func(m []Message) []Message { return m[1:] }, false},
		{"other end", func(m []Message) []Message { m[len(m)-1].SourceMessageID++; return m }, false},
		{"old transcript", func(m []Message) []Message { m[1].SourceKind = "compacted_transcript"; return m }, false},
		{"oversized", func(m []Message) []Message { m[2].Content = strings.Repeat("x", limit+1); return m }, false},
		{"out of order", func(m []Message) []Message { m[0], m[1] = m[1], m[0]; return m }, true},
		{"wrong digest", func(m []Message) []Message { m[3].Content += " changed"; return m }, true},
		{"changed origin", func(m []Message) []Message { m[5].SourceRef = "different tool"; return m }, true},
		{"false authority", func(m []Message) []Message { m[5].Role = "user"; m[5].InstructionAuthorized = true; return m }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ineligible, err := candidate.WithCompleteArchive(tc.change(slices.Clone(input)))
			if (err != nil) != tc.wantErr || ineligible.archive != nil || ineligible.Content != base.Content {
				t.Fatalf("unexpected archive result or integrity status: %v", err)
			}
		})
	}
	for _, mutate := range []func(*modelHandoff){
		func(h *modelHandoff) { h.compactedMessageCount = 2*MaxHandoffMemoryRecords + 1 },
		func(h *modelHandoff) { h.records = slices.Clone(h.records); h.records[0].Category = "prior_handoff" },
	} {
		ineligible := base
		h := *base.handoff
		mutate(&h)
		ineligible.handoff = &h
		if count, through, bytes := ineligible.ArchiveWindow(); count != 0 || through != 0 || bytes != 0 {
			t.Fatal("ineligible handoff requested the raw prefix")
		}
	}
	delete(replayed, "message:6")
	if shown := candidate.WithReplayedOperators(replayed).Content; !strings.Contains(shown, "Previous model's handoff") || strings.Contains(shown, "Archived messages, earliest to latest") {
		t.Fatal("a missing replayed user turn still enabled the complete archive")
	}
}
