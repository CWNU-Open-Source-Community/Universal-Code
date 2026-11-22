package contextmgr

import (
	"context"
	"slices"
	"strings"
	"testing"
)

type sessionPromptArchive struct {
	memoryStore
	rows  []Message
	calls int
}

func (st *sessionPromptArchive) ListCompleteCompactedSessionPrefix(_ context.Context, _ string, _ int64, _, _ int) ([]Message, error) {
	st.calls++
	return st.rows, nil
}

func TestSessionPromptReadsOnlyItsOwnCompleteArchiveForRestatedUserChoice(t *testing.T) {
	a := rollingMessage(1, "user", "Use compact labels.", "operator_message", true)
	b := rollingMessage(3, "user", "Use verbose labels.", "operator_message", true)
	last := rollingMessage(5, "user", a.Content, "operator_message", true)
	tail := rollingMessage(6, "assistant", "The check is still pending.", "model_response", false)
	a.SourceRef, b.SourceRef, last.SourceRef, tail.SourceRef = "", "", "", ""
	st := &sessionPromptArchive{rows: []Message{a, b, last}}
	const stale = "The earlier model considered the intermediate label final."
	m := NewManager(st, Config{PreserveRecentMessages: 1}).WithSummaryGenerator(summaryGeneratorFunc(
		func(_ context.Context, req SummaryGenerationRequest) (SummaryGenerationResponse, error) {
			return SummaryGenerationResponse{Text: stale, Receipt: generationReceipt(req)}, nil
		}), handoffContentSHA256("label choices"))
	result, err := m.Compact(t.Context(), "local-session", "ws", append(slices.Clone(st.rows), tail))
	if err != nil || !result.Generated {
		t.Fatalf("test history did not produce a summary: %v", err)
	}
	for _, tc := range []struct {
		name      string
		sessionID string
		originals []Message
		wantRead  bool
	}{
		{"different session", "another-session", st.rows, false},
		{"different choices", "local-session", []Message{a, b}, false},
		{"same session and restatement", "local-session", st.rows, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := st.calls
			prompt, err := m.BuildSessionPrompt(t.Context(), tc.sessionID, "", result.Summary, result.Preserved, tc.originals)
			if err != nil {
				t.Fatal(err)
			}
			handoff := prompt[len(tc.originals)]
			if (st.calls == calls+1) != tc.wantRead || strings.Contains(handoff.Content, "Archived messages, earliest to latest") != tc.wantRead ||
				strings.Contains(handoff.Content, stale) == tc.wantRead || handoff.Role != "assistant" || handoff.InstructionAuthorized || prompt[len(prompt)-1] != tail {
				t.Fatalf("unexpected source-backed handoff (reads=%d): %s", st.calls-calls, handoff.Content)
			}
		})
	}
}
