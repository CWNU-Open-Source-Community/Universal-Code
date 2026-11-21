package contextmgr

import (
	"strings"
	"testing"
)

func TestOperatorRestatementQuotesSeparateVerifiedSubmissions(t *testing.T) {
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
	if fact == nil || fact.EarlierIdenticalSource != "message:1" ||
		fact.Submissions != [2]OperatorSubmission{{"message:3", b.Content}, {"message:7", a.Content}} {
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
	if got := committed.LatestOperatorRestatement([]Message{later, again}); got == nil || got.EarlierIdenticalSource != "message:3" ||
		got.Submissions != [2]OperatorSubmission{{"message:11", later.Content}, {"message:13", b.Content}} {
		t.Fatalf("a newer resubmission must retain the new source order: %#v", got)
	}
	repeated.SourceKind = "workspace_file"
	if got := section.LatestOperatorRestatement([]Message{repeated}); got != nil {
		t.Fatal("a user-shaped external source was quoted as an operator message")
	}
	for _, text := range []string{strings.Repeat("界", 100), "password=sensitivevalue123"} {
		oversized := rollingMessage(3, "user", text, "operator_message", true)
		if got := (Section{}).LatestOperatorRestatement([]Message{a, oversized, rollingMessage(7, "user", a.Content, "operator_message", true)}); got != nil {
			t.Fatal("a partial or unredacted original entered a bounded quotation")
		}
	}
}
