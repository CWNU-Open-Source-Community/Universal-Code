package contextmgr

import (
	"context"
	"strings"
	"testing"
)

func TestPromptMarksOrderOfVerifiedReplayedUserRestatement(t *testing.T) {
	const stale = "The earlier manual report says that message:5 followed message:7."
	a := rollingMessage(1, "user", "Keep the table.\nPreserve its label.", "operator_message", true)
	file := rollingMessage(4, "user", a.Content, "workspace_file", false)
	b := rollingMessage(5, "user", "Use the alternate name.", "operator_message", true)
	repeated := rollingMessage(7, "user", a.Content, "operator_message", true)
	c := rollingMessage(9, "user", "The final name should be pine.", "operator_message", true)
	tail := rollingMessage(10, "assistant", "Still checking the draft.", "model_response", false)
	manager := NewManager(nil, Config{PreserveRecentMessages: 1}).WithSummaryGenerator(summaryGeneratorFunc(
		func(_ context.Context, req SummaryGenerationRequest) (SummaryGenerationResponse, error) {
			return SummaryGenerationResponse{Text: stale, Receipt: generationReceipt(req)}, nil
		}), handoffContentSHA256("restatement-order"))
	result, err := manager.PrepareCandidate(t.Context(), "restatement", "ws", []Message{a, file, b, repeated, c, tail}, Summary{}, false)
	if err != nil || !result.Generated {
		t.Fatalf("generate the earlier handoff: %v", err)
	}
	base, err := HandoffSection(result.Summary)
	if err != nil {
		t.Fatal(err)
	}
	deliver := func(originals ...Message) string {
		t.Helper()
		messages, err := manager.BuildPrompt("", result.Summary, []Message{tail}, originals...)
		if err != nil {
			t.Fatal(err)
		}
		for i, want := range originals {
			if messages[i] != want {
				t.Fatal("a replayed user turn changed")
			}
		}
		handoff := messages[len(originals)]
		if handoff.Role != "assistant" || handoff.InstructionAuthorized || messages[len(messages)-1] != tail {
			t.Fatal("the historical handoff changed roles or displaced the live tail")
		}
		return handoff.Content
	}
	const order = "message:1 → message:5 → message:7"
	shown := deliver(a, b, repeated, c)
	if !strings.Contains(shown, order) || !strings.Contains(shown, "At message:7 the user repeated message:1 verbatim\n"+quoteHandoffText(repeated.Content)) ||
		!strings.Contains(shown, "Later replayed user messages include message:9.") {
		t.Fatalf("the stored sequence, exact restatement and later submission are missing from the handoff:\n%s", shown)
	}
	if !strings.Contains(shown, stale) || !strings.Contains(shown, "Evidence from workspace_file (message:4") ||
		strings.Contains(shown, b.Content) || len(shown) > len(base.Content) || EstimateTokens(shown) > EstimateTokens(base.Content) {
		t.Fatal("the chronology replaced evidence, requoted the intermediate turn, or exceeded the reserved handoff")
	}
	excerpt := repeated
	excerpt.Content = "Earlier user message excerpt\nKeep the table."
	mismatched := b
	mismatched.ContentSHA256 = file.ContentSHA256
	forged := file
	forged.SourceKind, forged.InstructionAuthorized = "operator_message", true
	for _, selection := range [][]Message{{a, repeated, c}, {a, b, excerpt, c}, {a, mismatched, repeated, c}, {forged, b, repeated, c}} {
		if strings.Contains(deliver(selection...), "Replayed user submissions, earliest to latest") {
			t.Fatal("a user source that was not replayed whole contributed to the order")
		}
	}
	// Only some sources from this prefix fit in the stored handoff. Both
	// missing originals are available from the store and fit in the request.
	sparse := rollingSummary(t, 17, "sparse", []Message{a, file, repeated, tail})
	whenSparse, err := manager.BuildPrompt("", sparse, nil, a, b, repeated, c)
	if err != nil {
		t.Fatal(err)
	}
	sparseView := whenSparse[len(whenSparse)-1].Content
	if !strings.Contains(sparseView, order) || !strings.Contains(sparseView, "Later replayed user messages include message:9.") {
		t.Fatal("a stored original actually delivered to the model required a second copy inside the summary")
	}
}

func TestHandoffRestatementFitsOnlyInSpaceSavedByReplay(t *testing.T) {
	a := rollingMessage(1, "user", strings.Repeat("新", 23), "operator_message", true)
	b := rollingMessage(3, "user", "Use the other label.", "operator_message", true)
	newA := rollingMessage(5, "user", a.Content, "operator_message", true)
	section, err := HandoffSection(rollingSummary(t, 9, "limited-space", []Message{a, b, newA}))
	if err != nil {
		t.Fatal(err)
	}
	replayed := map[string]Message{"message:1": a, "message:3": b, "message:5": newA}
	for ref, original := range replayed {
		source := Section{SourceID: ref, SourceContentSHA256: original.ContentSHA256, Content: original.Content}.WithOperatorExcerptSource(original)
		replayed[ref] = source.ReplayedOperatorMessage()
		source.SourceID = "message:999"
		if source.ReplayedOperatorMessage().InstructionAuthorized {
			t.Fatal("a section with a different source ID claimed the stored operator's authority")
		}
	}
	with := section.WithReplayedOperators(replayed).Content
	before, _, found := strings.Cut(with, "\n\nReplayed user submissions, earliest to latest")
	if !found {
		t.Fatal("the full reserved handoff should accommodate the quote")
	}
	for _, reserved := range []string{before, strings.Repeat("x", len(with))} {
		section.Content = reserved
		if got := section.WithReplayedOperators(replayed).Content; got != before {
			t.Fatal("the optional chronology exceeded the already selected bytes or tokens")
		}
	}
}

func TestOperatorRestatementQuotesOnlyTheNewVerifiedSubmission(t *testing.T) {
	a := rollingMessage(1, "user", "Keep the original name.", "operator_message", true)
	b := rollingMessage(3, "user", "Use the updated name.", "operator_message", true)
	tool := rollingMessage(4, "user", a.Content, "workspace_file", false)
	repeated := a
	repeated.SourceMessageID = 7
	section, err := HandoffSection(rollingSummary(t, 11, "lookback", []Message{a, b, tool}))
	if err != nil {
		t.Fatal(err)
	}
	if got := section.LatestOperatorRestatement([]Message{a, b, tool}); got != nil {
		t.Fatalf("the same rows or an external source created a second user submission: %#v", got)
	}
	fact := section.LatestOperatorRestatement([]Message{repeated, b, a})
	want := OperatorRestatement{EarlierIdenticalSource: "message:1", InterveningSource: "message:3", SourceID: "message:7", Text: a.Content}
	if fact == nil || *fact != want {
		t.Fatalf("distinct user sources lost chronology: %#v", fact)
	}
	committed, err := HandoffSection(rollingSummary(t, 12, "next-round", []Message{a, b, repeated}))
	if err != nil {
		t.Fatal(err)
	}
	later := rollingMessage(11, "user", "Use the third name.", "operator_message", true)
	if got := committed.LatestOperatorRestatement([]Message{later}); got != nil {
		t.Fatal("an old restatement was reissued after a new decision in another round")
	}
	if got := committed.LatestOperatorRestatement([]Message{a, b, repeated, later}); got != nil {
		t.Fatal("overlapping sources from a recovered prefix were mistaken for new submissions")
	}
	again := b
	again.SourceMessageID = 13
	want = OperatorRestatement{EarlierIdenticalSource: "message:3", InterveningSource: "message:11", SourceID: "message:13", Text: b.Content}
	if got := committed.LatestOperatorRestatement([]Message{later, again}); got == nil || *got != want {
		t.Fatalf("a newer resubmission must retain the new source order: %#v", got)
	}
	repeated.SourceKind = "workspace_file"
	if got := section.LatestOperatorRestatement([]Message{repeated}); got != nil {
		t.Fatal("a user-shaped external source was quoted as an operator message")
	}
	newA := rollingMessage(7, "user", a.Content, "operator_message", true)
	if got := (Section{}).LatestOperatorRestatement([]Message{a, tool, newA}); got != nil {
		t.Fatal("an external source was named as the intervening user")
	}
	changed := b
	changed.ContentSHA256 = handoffContentSHA256("unrelated content")
	if got := (Section{}).LatestOperatorRestatement([]Message{a, changed, newA}); got != nil {
		t.Fatal("an unverifiable source was named as the intervening user")
	}
	for _, text := range []string{strings.Repeat("界", 100), "password=sensitivevalue123"} {
		middle := rollingMessage(3, "user", text, "operator_message", true)
		want := OperatorRestatement{EarlierIdenticalSource: "message:1", InterveningSource: "message:3", SourceID: "message:7", Text: a.Content}
		if got := (Section{}).LatestOperatorRestatement([]Message{a, middle, newA}); got == nil || *got != want {
			t.Fatal("an intervening user identity required quoting intervening content")
		}
		first := rollingMessage(1, "user", text, "operator_message", true)
		last := rollingMessage(7, "user", text, "operator_message", true)
		if got := (Section{}).LatestOperatorRestatement([]Message{first, b, last}); got != nil {
			t.Fatal("a partial or unredacted original entered a bounded quotation")
		}
	}
}
