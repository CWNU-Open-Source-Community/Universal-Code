package contextmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func actualCorrectionHistory(t *testing.T) ([]Message, []Message) {
	t.Helper()
	data, err := os.ReadFile("testdata/operator_correction_history.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct{ First, Second []Message }
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, messages := range [][]Message{fixture.First, fixture.Second} {
		for _, message := range messages {
			if message.ContentSHA256 != handoffContentSHA256(message.Content) {
				t.Fatalf("actual source %d hash mismatch", message.SourceMessageID)
			}
		}
	}
	return fixture.First, fixture.Second
}

func TestHandoffNewOperatorRequirementsCanEvictOlderUpdatesWithinFixedBudget(t *testing.T) {
	manager := NewManager(nil, Config{PreserveRecentMessages: 1})
	first := retentionMessage(1, "user", "operator_message", "Original task and protected repository boundary.", true)
	old := retentionMessage(2, "user", "operator_message", "An early setting that later requests may replace.", true)
	previous := correctionCandidate(t, manager, []Message{first, old}, Summary{})
	var next []Message
	for id := int64(3); id <= 32; id++ {
		next = append(next, retentionMessage(id, "user", "operator_message", fmt.Sprintf("Requirement revision %d: preserve the current external interface; verify result %d.", id, id), true))
	}
	summary := correctionCandidate(t, manager, next, previous)
	envelope := retentionEnvelope(t, summary)
	if envelope.RecordsOmitted == 0 || utf8.RuneCountInString(summary.Content) > MaxHandoffMemoryChars || len(envelope.Records) > MaxHandoffMemoryRecords {
		t.Fatal("bounded omission was hidden or cap grew")
	}
	for _, record := range envelope.Records {
		if record.SourceMessageID == old.SourceMessageID {
			t.Fatal("old follow-up became permanently privileged over newer requirements")
		}
	}
	requireActualCorrection(t, summary, next[len(next)-1])
	requireActualCorrection(t, summary, next[len(next)-2])
	if summary.PreviousSummaryID != previous.ID || previous.ContentSHA256 != handoffContentSHA256(previous.Content) {
		t.Fatal("original summary chain was rewritten instead of retaining a readable source")
	}
}

func TestHandoffKeepsChronologyWhenOperatorRestatesOriginalRequirement(t *testing.T) {
	const original = "Accept signed amounts for refunds."
	messages := []Message{
		retentionMessage(1, "user", "operator_message", original, true),
		retentionMessage(2, "user", "operator_message", "Reject negative amounts.", true),
		retentionMessage(3, "user", "operator_message", original, true),
		retentionMessage(4, "user", "operator_message", "Write validation tests.", true),
		retentionMessage(5, "user", "operator_message", "Keep the report format.", true),
	}
	manager := NewManager(nil, Config{PreserveRecentMessages: 1})
	for _, end := range []int{3, 5} {
		summary := correctionCandidate(t, manager, messages[:end], Summary{})
		for _, original := range messages[:3] {
			requireActualCorrection(t, summary, original)
		}
	}
}

func correctionCandidate(t *testing.T, manager *Manager, messages []Message, previous Summary) Summary {
	t.Helper()
	input := append([]Message(nil), messages...)
	input = append(input, retentionMessage(input[len(input)-1].SourceMessageID+1, "user", "operator_message", "continue", true))
	result, err := manager.PrepareCandidate(context.Background(), "correction-history", "ws-corrections", input, previous, previous.ID != 0)
	if err != nil {
		t.Fatal(err)
	}
	result.Summary.ID = previous.ID + 1
	return result.Summary
}

func requireActualCorrection(t *testing.T, summary Summary, original Message) {
	t.Helper()
	envelope := retentionEnvelope(t, summary)
	var ids []int64
	for _, record := range envelope.Records {
		ids = append(ids, record.SourceMessageID)
		if record.SourceMessageID != original.SourceMessageID {
			continue
		}
		if record.Role != original.Role || record.SourceKind != original.SourceKind ||
			record.SourceContentSHA256 != original.ContentSHA256 || record.SourceRef != original.SourceRef ||
			record.InstructionAuthorized != original.InstructionAuthorized || record.Content != original.Content {
			t.Fatalf("correction %d is not a complete, exact original source: %#v", original.SourceMessageID, record)
		}
		return
	}
	t.Fatalf("operator correction %d missing; retained %v (%d chars): %s", original.SourceMessageID, ids,
		utf8.RuneCountInString(summary.Content), summary.Content)
}

func logCorrectionProjection(t *testing.T, stage string, content string, envelope handoffMemoryEnvelope) {
	t.Helper()
	var ids, completeOperators []int64
	for _, r := range envelope.Records {
		ids = append(ids, r.SourceMessageID)
		if r.Category == "operator_intent" && r.ContentSHA256 == r.SourceContentSHA256 {
			completeOperators = append(completeOperators, r.SourceMessageID)
		}
	}
	t.Logf("%s: chars=%d bytes=%d retained=%v complete_operator=%v omitted=%d", stage, utf8.RuneCountInString(content), len(content), ids, completeOperators, envelope.RecordsOmitted)
}

func TestActualLaterOperatorCorrectionsSurviveDelayedAndRepeatedCompaction(t *testing.T) {
	first, second := actualCorrectionHistory(t)
	manager := NewManager(nil, Config{PreserveRecentMessages: 1})
	for name, boundaries := range map[string][][]Message{
		"actual two compactions":                            {first, second},
		"first compaction after six more operator messages": {append(append([]Message(nil), first...), second...)},
	} {
		t.Run(name, func(t *testing.T) {
			var summary Summary
			for _, messages := range boundaries {
				summary = correctionCandidate(t, manager, messages, summary)
				logCorrectionProjection(t, fmt.Sprintf("summary-%d", summary.ID), summary.Content, retentionEnvelope(t, summary))
			}
			for _, id := range []int64{13, 18, 20, 22, 24, 30} {
				for _, original := range append(append([]Message(nil), first...), second...) {
					if original.SourceMessageID == id {
						requireActualCorrection(t, summary, original)
					}
				}
			}
			for round := int64(0); round < 3; round++ {
				summary = correctionCandidate(t, manager, []Message{
					retentionMessage(100+round*3, "assistant", "model_response", "Current progress is not a new operator request.", false),
					retentionMessage(101+round*3, "assistant", "model_response", "Implementation and verification still pending.", false),
				}, summary)
				requireActualCorrection(t, summary, first[12])
			}
			if strings.Contains(summary.Content, "generated_handoff") {
				t.Fatal("extractive fixture claimed generation")
			}
		})
	}
}

func TestActualOperatorCorrectionAcrossRollingSuccessors(t *testing.T) {
	first, second := actualCorrectionHistory(t)
	manager := NewManager(nil, Config{PreserveRecentMessages: 1})
	initial := correctionCandidate(t, manager, append(append([]Message(nil), first...), second...), Summary{})
	previous := initial.Content
	for round := int64(0); round < 3; round++ {
		current := correctionCandidate(t, manager, []Message{
			retentionMessage(100+round*2, "assistant", "model_response", "Implementation and verification still pending.", false),
		}, Summary{})
		current.ID = 2 + round
		source := rollingSource(previous, fmt.Sprintf("run-correction-holder-%d", round))
		content, err := RollContinuitySummaries(previous, current, source)
		if err != nil {
			t.Fatal(err)
		}
		var window continuitySummaryWindow
		if err := strictContinuityWindowJSON(content, &window); err != nil {
			t.Fatal(err)
		}
		if err := validateContinuityWindow(window, "ws-corrections"); err != nil {
			t.Fatal(err)
		}
		found := make(map[int64]bool)
		for _, record := range window.Projection.Records {
			for _, original := range append(append([]Message(nil), first...), second...) {
				if record.SourceMessageID == original.SourceMessageID {
					if record.SourceContentSHA256 != original.ContentSHA256 || record.Role != original.Role || record.SourceKind != original.SourceKind || record.SourceRef != original.SourceRef || record.InstructionAuthorized != original.InstructionAuthorized {
						t.Fatalf("rolling source identity changed: %#v", record)
					}
					found[record.SourceMessageID] = record.Content == original.Content
					if record.SourceMessageID == 1 || record.SourceMessageID == 31 {
						found[record.SourceMessageID] = true
					}
				}
			}
		}
		for _, id := range []int64{1, 13, 18, 20, 22, 24, 30, 31} {
			if !found[id] {
				t.Fatalf("rolling cycle %d lost actual operator/evidence source %d: %s", round, id, content)
			}
		}
		if len(window.Sources) != 2 || window.Sources[0] != source || window.Sources[1].ContentSHA256 != current.ContentSHA256 || !window.Lossy || window.InstructionAuthorized {
			t.Fatal("exact readback source or loss boundary changed")
		}
		logCorrectionProjection(t, fmt.Sprintf("rolling-%d", round), content, window.Projection)
		previous = content
	}
}

func TestGeneratedHandoffRetainsRefundThroughFurtherCompaction(t *testing.T) {
	first, second := actualCorrectionHistory(t)
	// Leave the provider's stale description intact. The original operator
	// message must independently carry the corrected amount rule.
	const latest = "已读取样例与项目约定。初版 CLI 接受非负金额；程序入口、错误行号、测试和文档仍待核对。"
	earlier := latest + strings.Repeat("已完成样例读取与目录核对，尚待运行验证。", 60)
	manager := NewManager(&memoryStore{}, DefaultConfig()).WithSummaryGenerator(summaryGeneratorFunc(
		func(_ context.Context, request SummaryGenerationRequest) (SummaryGenerationResponse, error) {
			text := latest
			if !request.HasPrevious {
				text = earlier
			}
			return SummaryGenerationResponse{Text: text, Receipt: generationReceipt(request)}, nil
		}), handoffContentSHA256("refund-history"))
	diagnostics := func(start int64) []Message {
		var messages []Message
		for id := start; id < start+6; id++ {
			messages = append(messages, retentionMessage(id, "tool", "tool_result",
				"Read-only verification output\n"+strings.Repeat("checked fixture\n", 120), false))
		}
		return messages
	}
	var history []Message
	for pass, batch := range [][]Message{first, append(second[:10:10], diagnostics(32)...), diagnostics(38)} {
		history = append(history, batch...)
		result, err := manager.MaybeCompact(t.Context(), "refund-history", "ws-corrections", history)
		if err != nil || !result.Compacted {
			t.Fatalf("compaction %d: compacted=%v, err=%v", pass+1, result.Compacted, err)
		}
		if !result.Generated {
			t.Fatalf("compaction %d discarded a fitting generated summary: %s", pass+1, result.GenerationFallbackReason)
		}
		envelope := retentionEnvelope(t, result.Summary)
		expected := latest
		if pass == 0 {
			expected = earlier
		}
		if envelope.Generated == nil || envelope.Generated.Text != expected || envelope.Generated.InstructionAuthorized {
			t.Fatalf("compaction %d changed generated text or authority", pass+1)
		}
		requireActualCorrection(t, result.Summary, first[12])
		if pass > 0 {
			for _, original := range []Message{second[0], second[2], second[4], second[6], second[9]} {
				requireActualCorrection(t, result.Summary, original)
			}
		}
		history = result.Preserved
	}
}
