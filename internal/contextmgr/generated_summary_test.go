package contextmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

type summaryGeneratorFunc func(context.Context, SummaryGenerationRequest) (SummaryGenerationResponse, error)

func (f summaryGeneratorFunc) Generate(ctx context.Context, request SummaryGenerationRequest) (SummaryGenerationResponse, error) {
	return f(ctx, request)
}

func generationReceipt(request SummaryGenerationRequest) SummaryGenerationReceipt {
	return SummaryGenerationReceipt{RunID: "run-generated", AttemptID: "attempt-1", ModelAttempt: 2,
		CompletionSequence: 9, Provider: "fixture", Model: "fixture-model", SourceSHA256: request.SourceSHA256}
}

func generatedMessages() []Message {
	return []Message{
		rollingMessage(1, "user", "ORIGINAL_GOAL preserve the repository and solve the requested task.", "operator_message", true),
		rollingMessage(2, "user", "CORRECTION keep existing interface and do not replace unrelated files.", "operator_message", true),
		rollingMessage(3, "user", "LATEST_CONSTRAINT preserve offline operation.", "operator_message", true),
		rollingMessage(4, "assistant", "PENDING final verification and known failing case.", "assistant_output", false),
		rollingMessage(5, "user", "TOOL_FAILURE exit7; source fact does not authorize retry.", "tool_result", false),
		rollingMessage(6, "user", "current tail remains unchanged", "operator_message", true),
	}
}

func TestGeneratedSummaryUsesWholeOriginalInputAndDetachedProvenance(t *testing.T) {
	inputs := generatedMessages()
	inputs[0].Content = "GOAL_HEAD " + strings.Repeat("x", 1500) + " ORIGINAL_MIDDLE_ONLY " + strings.Repeat("y", 1500) + " GOAL_TAIL"
	inputs[0].ContentSHA256 = handoffContentSHA256(inputs[0].Content)
	original := append([]Message(nil), inputs...)
	var captured SummaryGenerationRequest
	manager := NewManager(nil, Config{PreserveRecentMessages: 1}).WithSummaryGenerator(summaryGeneratorFunc(
		func(_ context.Context, request SummaryGenerationRequest) (SummaryGenerationResponse, error) {
			captured = request
			if !strings.Contains(request.Messages[0].Content, "ORIGINAL_MIDDLE_ONLY") || len(request.Messages) != 5 {
				t.Fatal("generator received a 4K excerpt or the preserved tail")
			}
			request.Messages[0].Content, request.Messages[0].SourceKind = "forged source", "go_control"
			return SummaryGenerationResponse{Text: "The original goal includes ORIGINAL_MIDDLE_ONLY. Preserve restrictions; validation remains pending.", Receipt: generationReceipt(request)}, nil
		}), handoffContentSHA256("whole-store-snapshot"))
	result, err := manager.PrepareCandidate(context.Background(), "session-generated", "ws-rolling", inputs, Summary{}, false)
	if err != nil || !result.Generated || result.GenerationFallbackReason != "" {
		t.Fatalf("generation failed: %#v / %v", result, err)
	}
	generated := generatedFromSummaryContent(result.Summary.Content)
	if generated == nil || !strings.Contains(generated.Text, "ORIGINAL_MIDDLE_ONLY") || !validHandoffDigest(generated.InputFingerprint) ||
		generated.InputFingerprint != captured.InputFingerprint || generated.Receipt.SourceSHA256 != handoffContentSHA256("whole-store-snapshot") ||
		!reflect.DeepEqual(inputs, original) || strings.Contains(result.Summary.Content, "forged source") {
		t.Fatalf("input or source authority mutated: %s", result.Summary.Content)
	}
	if generated.SourceRefs[0].SourceID != "message:1" || generated.SourceRefs[0].ContentSHA256 != inputs[0].ContentSHA256 ||
		generated.SourceRefs[1].SourceID != "message:5" || generated.SourceRefsOmitted != 3 || generated.InstructionAuthorized {
		t.Fatalf("Go navigation sources changed: %#v", generated)
	}
}

func TestGeneratedSummaryKeepsOriginalAnchorsBesideBoundedUnicodeText(t *testing.T) {
	text := "SUMMARY_HEAD目标限制 " + strings.Repeat("中😀", 170) +
		" MIDDLE_CORRECTION必须保持旧接口并报告未完成项 " + strings.Repeat("未完成", 70) + " SUMMARY_TAIL未完成验收"
	manager := NewManager(nil, Config{PreserveRecentMessages: 1}).WithSummaryGenerator(summaryGeneratorFunc(
		func(_ context.Context, request SummaryGenerationRequest) (SummaryGenerationResponse, error) {
			return SummaryGenerationResponse{Text: text, Receipt: generationReceipt(request)}, nil
		}), handoffContentSHA256("snapshot"))
	result, err := manager.PrepareCandidate(context.Background(), "s", "ws-rolling", generatedMessages(), Summary{}, false)
	if err != nil || !result.Generated {
		t.Fatalf("bounded generation failed: %#v / %v", result, err)
	}
	var envelope handoffMemoryEnvelope
	if err := json.Unmarshal([]byte(result.Summary.Content), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(result.Summary.Content) > MaxContinuitySummaryBytes || utf8.RuneCountInString(result.Summary.Content) > MaxHandoffMemoryChars ||
		len(envelope.Records) < 4 || envelope.Generated == nil || envelope.Generated.TextExcerpted || envelope.Generated.Text != text {
		t.Fatalf("budget displaced original source anchors: %s", result.Summary.Content)
	}
	found := make(map[int64]bool)
	for _, record := range envelope.Records {
		original := generatedMessages()[record.SourceMessageID-1]
		found[record.SourceMessageID] = true
		if record.SourceMessageID != original.SourceMessageID || record.InstructionAuthorized != original.InstructionAuthorized ||
			record.SourceContentSHA256 != original.ContentSHA256 || record.SourceKind != original.SourceKind {
			t.Fatal("model text changed original provenance")
		}
	}
	for _, id := range []int64{1, 2, 3, 5} {
		if !found[id] {
			t.Fatalf("generated handoff lost core source %d", id)
		}
	}
	if err := ValidateStoredSummary(result.Summary); err != nil {
		t.Fatal(err)
	}
}

func TestGeneratedSummaryFallsBackInsteadOfCuttingValidTextToFitMetadata(t *testing.T) {
	// Legal decoded length, but JSON escaping plus exact source/receipt metadata
	// cannot fit. This must not silently drop the middle of a valid handoff.
	text := "HEAD " + strings.Repeat("中😀\"\\", 290) + " MIDDLE_CORRECTION remains required TAIL"
	if utf8.RuneCountInString(text) > MaxGeneratedSummaryChars {
		t.Fatal("fixture is not a valid length")
	}
	calls := 0
	manager := NewManager(nil, Config{PreserveRecentMessages: 1}).WithSummaryGenerator(summaryGeneratorFunc(
		func(_ context.Context, request SummaryGenerationRequest) (SummaryGenerationResponse, error) {
			calls++
			return SummaryGenerationResponse{Text: text, Receipt: generationReceipt(request)}, nil
		}), handoffContentSHA256("snapshot"))
	result, err := manager.PrepareCandidate(context.Background(), "s", "ws-rolling", generatedMessages(), Summary{}, false)
	if err != nil || !result.Compacted || result.Generated || calls != 1 || !strings.Contains(result.GenerationFallbackReason, "could not fit") {
		t.Fatalf("unfittable handoff did not produce one explicit fallback: %#v %v", result, err)
	}
	envelope := retentionEnvelope(t, result.Summary)
	if envelope.Generated != nil || len(envelope.Records) != 5 {
		t.Fatal("fallback lost original records or claimed generation")
	}
}

func TestGeneratedSummaryFailureFallsBackButStopAndUnknownMustAbort(t *testing.T) {
	for name, cause := range map[string]error{
		"invalid model output": errors.New("invalid model summary JSON"),
		"cancel":               context.Canceled,
		"deadline":             context.DeadlineExceeded,
		"unknown billed call":  fmt.Errorf("unknown terminal: %w", ErrSummaryGenerationAborted),
	} {
		t.Run(name, func(t *testing.T) {
			manager := NewManager(nil, Config{PreserveRecentMessages: 1}).WithSummaryGenerator(summaryGeneratorFunc(
				func(context.Context, SummaryGenerationRequest) (SummaryGenerationResponse, error) {
					return SummaryGenerationResponse{}, cause
				}), handoffContentSHA256("snapshot"))
			result, err := manager.PrepareCandidate(context.Background(), "s", "ws-rolling", generatedMessages(), Summary{}, false)
			if name == "invalid model output" {
				if err != nil || result.Generated || result.GenerationFallbackReason == "" || generatedFromSummaryContent(result.Summary.Content) != nil {
					t.Fatalf("failed generation claimed success: %#v / %v", result, err)
				}
			} else if err == nil || result.Compacted {
				t.Fatalf("abort incorrectly fell back: %#v / %v", result, err)
			}
		})
	}
	manager := NewManager(nil, Config{PreserveRecentMessages: 1}).WithSummaryGenerator(summaryGeneratorFunc(
		func(_ context.Context, request SummaryGenerationRequest) (SummaryGenerationResponse, error) {
			receipt := generationReceipt(request)
			receipt.SourceSHA256 = handoffContentSHA256("other snapshot")
			return SummaryGenerationResponse{Text: "valid text", Receipt: receipt}, nil
		}), handoffContentSHA256("snapshot"))
	if _, err := manager.PrepareCandidate(context.Background(), "s", "ws-rolling", generatedMessages(), Summary{}, false); !errors.Is(err, ErrSummaryGenerationAborted) {
		t.Fatalf("source drift did not abort: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.WithSummaryGenerator(summaryGeneratorFunc(func(_ context.Context, request SummaryGenerationRequest) (SummaryGenerationResponse, error) {
		cancel()
		return SummaryGenerationResponse{Text: "a late successful response", Receipt: generationReceipt(request)}, nil
	}), handoffContentSHA256("snapshot"))
	if result, err := manager.PrepareCandidate(ctx, "s", "ws-rolling", generatedMessages(), Summary{}, false); !errors.Is(err, context.Canceled) || result.Compacted {
		t.Fatalf("late response overrode cancellation: %#v / %v", result, err)
	}
}

func TestGeneratedSummarySurvivesNextCompressionAndRollingSuccessor(t *testing.T) {
	var generationCalls int
	manager := NewManager(nil, Config{PreserveRecentMessages: 1}).WithSummaryGenerator(summaryGeneratorFunc(
		func(_ context.Context, request SummaryGenerationRequest) (SummaryGenerationResponse, error) {
			generationCalls++
			if generationCalls > 1 && (!request.HasPrevious || !strings.Contains(request.Previous.Content, "GENERATED_HANDOFF_1")) {
				t.Fatal("next generation input lost previous generated explanation")
			}
			return SummaryGenerationResponse{Text: fmt.Sprintf("GENERATED_HANDOFF_%d keep original restrictions; remaining work is verification.", generationCalls), Receipt: generationReceipt(request)}, nil
		}), handoffContentSHA256("snapshot"))
	first, err := manager.PrepareCandidate(context.Background(), "s", "ws-rolling", generatedMessages(), Summary{}, false)
	if err != nil || !first.Generated {
		t.Fatalf("first: %v / %s", err, first.GenerationFallbackReason)
	}
	first.Summary.ID = 1
	secondMessages := []Message{rollingMessage(7, "user", "Continue with constraints", "operator_message", true), rollingMessage(8, "assistant", "still pending", "assistant_output", false)}
	second, err := manager.PrepareCandidate(context.Background(), "s", "ws-rolling", secondMessages, first.Summary, true)
	if err != nil || !second.Generated {
		t.Fatalf("second: %v / %s", err, second.GenerationFallbackReason)
	}
	second.Summary.ID = 2
	if err := ValidateStoredSummary(second.Summary); err != nil {
		t.Fatal(err)
	}
	current := rollingSummary(t, 3, "s-successor", []Message{rollingMessage(20, "assistant", "new session pending", "assistant_output", false)})
	window, err := RollContinuitySummaries(second.Summary.Content, current, rollingSource(second.Summary.Content, "run-holder"))
	if err != nil {
		t.Fatal(err)
	}
	generated := rollingWindow(t, window).Projection.Generated
	if generated == nil || !strings.Contains(generated.Text, "GENERATED_HANDOFF_2") || generated.InstructionAuthorized {
		t.Fatalf("successor lost non-authorizing generated handoff: %s", window)
	}
}

func TestFailedGenerationRetainsPreviousTextAndNewOperatorSource(t *testing.T) {
	const previousText = "The initial CLI accepts nonnegative amounts."
	next := rollingMessage(7, "user", "Accept signed amounts for refunds.", "operator_message", true)
	calls := 0
	manager := NewManager(&memoryStore{}, Config{PreserveRecentMessages: 1}).WithSummaryGenerator(summaryGeneratorFunc(
		func(_ context.Context, request SummaryGenerationRequest) (SummaryGenerationResponse, error) {
			calls++
			if request.HasPrevious {
				return SummaryGenerationResponse{}, errors.New("provider unavailable")
			}
			return SummaryGenerationResponse{Text: previousText, Receipt: generationReceipt(request)}, nil
		}), handoffContentSHA256("fallback-history"))
	first, err := manager.Compact(t.Context(), "s", "ws-rolling", generatedMessages())
	if err != nil || !first.Generated {
		t.Fatalf("first compaction: generated=%v err=%v", first.Generated, err)
	}
	messages := append(first.Preserved, next, rollingMessage(8, "assistant", "Verification pending.", "assistant_output", false))
	second, err := manager.Compact(t.Context(), "s", "ws-rolling", messages)
	if err != nil || second.Generated || second.GenerationFallbackReason != "provider unavailable" || calls != 2 {
		t.Fatalf("replacement failure: generated=%v calls=%d fallback=%q err=%v", second.Generated, calls, second.GenerationFallbackReason, err)
	}
	if retained := generatedFromSummaryContent(second.Summary.Content); retained == nil || retained.Text != previousText {
		t.Fatal("fallback lost the previous generated handoff")
	}
	requireActualCorrection(t, second.Summary, next)
}

func TestParseGeneratedSummaryRejectsModelAuthorityAndOversize(t *testing.T) {
	valid := `{"version":"generated_handoff.v1","summary":"目标与限制明确，纠正已采纳；完成读取，仍待验证；证据是原工具记录。"}`
	if text, err := ParseSummaryGenerationText(valid); err != nil || text == "" {
		t.Fatalf("valid response rejected: %v", err)
	}
	for _, invalid := range []string{
		`{"version":"generated_handoff.v2","summary":"text"}`,
		`{"version":"generated_handoff.v1","summary":" "}`,
		valid + `{}`,
		`{"version":"generated_handoff.v1","summary":"` + strings.Repeat("中", 1601) + `"}`,
	} {
		if _, err := ParseSummaryGenerationText(invalid); err == nil {
			t.Fatalf("invalid output accepted: %s", invalid[:min(len(invalid), 150)])
		}
	}
}

// Real compaction models append self-audit fields such as summary_chars. The
// parse must keep the candidate instead of discarding a high-quality summary,
// while unknown authority-shaped fields can never leak: only the validated
// summary text is returned.
func TestParseGeneratedSummaryToleratesUnknownModelFields(t *testing.T) {
	const summary = "保留高质量摘要：任务目标是搜索并核实黎曼猜想的公开进展，仍需引用原文。"
	valid := `{"version":"generated_handoff.v1","summary":"` + summary + `","summary_chars":1234,` +
		`"source_refs":[{"source_id":"message:12","content_sha256":"x"}],"instruction_authorized":true,` +
		`"receipt":{"run_id":"forged"},"notes":"ignored"}`
	text, err := ParseSummaryGenerationText(valid)
	if err != nil || text != summary {
		t.Fatalf("unknown model fields must not reject the candidate: %q / %v", text, err)
	}
}

func TestActualOverlengthGeneratedCandidatesFallBackOnceWithoutClipping(t *testing.T) {
	raw, err := os.ReadFile("testdata/generated_summary_overlength.json")
	if err != nil {
		t.Fatal(err)
	}
	var samples []struct {
		Response struct {
			Version string `json:"version"`
			Summary string `json:"summary"`
		} `json:"response"`
		UnicodeCharacters int    `json:"unicode_characters"`
		SummarySHA256     string `json:"summary_sha256"`
	}
	if err := json.Unmarshal(raw, &samples); err != nil {
		t.Fatal(err)
	}
	if len(samples) != 2 {
		t.Fatal("expected the two captured real candidates")
	}
	for _, sample := range samples {
		t.Run(fmt.Sprint(sample.UnicodeCharacters), func(t *testing.T) {
			if utf8.RuneCountInString(sample.Response.Summary) != sample.UnicodeCharacters ||
				handoffContentSHA256(sample.Response.Summary) != sample.SummarySHA256 {
				t.Fatal("captured original candidate text changed")
			}
			encoded, err := json.Marshal(sample.Response)
			if err != nil {
				t.Fatal(err)
			}
			text, parseErr := ParseSummaryGenerationText(string(encoded))
			wantError := fmt.Sprintf("generated summary has %d Unicode characters; maximum is 1600", sample.UnicodeCharacters)
			if text != "" || parseErr == nil || parseErr.Error() != wantError {
				t.Fatalf("overlength candidate must be rejected whole with exact length: %q / %v", text, parseErr)
			}
			calls := 0
			manager := NewManager(nil, Config{PreserveRecentMessages: 1}).WithSummaryGenerator(summaryGeneratorFunc(
				func(_ context.Context, request SummaryGenerationRequest) (SummaryGenerationResponse, error) {
					calls++
					if request.MaxOutputChars != 1600 {
						t.Fatal("soft target changed the hard response budget")
					}
					text, err := ParseSummaryGenerationText(string(encoded))
					return SummaryGenerationResponse{Text: text, Receipt: generationReceipt(request)}, err
				}), handoffContentSHA256("actual-candidate-boundary"))
			original := generatedMessages()
			before := append([]Message(nil), original...)
			result, err := manager.PrepareCandidate(context.Background(), "s", "ws-rolling", original, Summary{}, false)
			if err != nil || !result.Compacted || result.Generated || calls != 1 || result.GenerationFallbackReason != wantError {
				t.Fatalf("unexpected retry, success claim, or fallback: calls=%d result=%+v err=%v", calls, result, err)
			}
			if generatedFromSummaryContent(result.Summary.Content) != nil || !reflect.DeepEqual(original, before) ||
				utf8.RuneCountInString(result.Summary.Content) > MaxHandoffMemoryChars || len(result.Summary.Content) > MaxContinuitySummaryBytes {
				t.Fatal("fallback clipped the candidate into a generated success, changed originals, or raised envelope limits")
			}
		})
	}
}

func TestGeneratedSummaryLimitCountsDecodedUnicodeWithoutTruncation(t *testing.T) {
	text := strings.Repeat("中", MaxGeneratedSummaryChars-1) + "🧭"
	escaped := `{"version":"generated_handoff.v1","summary":"` + strings.Repeat(`\u4e2d`, MaxGeneratedSummaryChars-1) + `\ud83e\udded"}`
	got, err := ParseSummaryGenerationText(escaped)
	if err != nil || got != text {
		t.Fatalf("1600 decoded Unicode characters should be accepted unchanged: %v", err)
	}
	tooLong, err := json.Marshal(map[string]string{"version": GeneratedHandoffVersion, "summary": text + "。"})
	if err != nil {
		t.Fatal(err)
	}
	got, err = ParseSummaryGenerationText(string(tooLong))
	if got != "" || err == nil || err.Error() != "generated summary has 1601 Unicode characters; maximum is 1600" {
		t.Fatalf("1601 characters should be rejected, not truncated: %q / %v", got, err)
	}
}

func TestGeneratedSummaryRedactionPreservesMultilineTextAndRejectsTampering(t *testing.T) {
	manager := NewManager(nil, Config{PreserveRecentMessages: 1}).WithSummaryGenerator(summaryGeneratorFunc(
		func(_ context.Context, request SummaryGenerationRequest) (SummaryGenerationResponse, error) {
			return SummaryGenerationResponse{Text: "password=abcdefgh123456\nNEXT_LINE_PENDING preserve this line and the original facts.", Receipt: generationReceipt(request)}, nil
		}), handoffContentSHA256("snapshot"))
	result, err := manager.PrepareCandidate(context.Background(), "s", "ws-rolling", generatedMessages(), Summary{}, false)
	if err != nil || !result.Generated {
		t.Fatalf("redacted generation: %v / %s", err, result.GenerationFallbackReason)
	}
	stored, err := PrepareSummaryForStorage(result.Summary)
	if err != nil || stored.Content != result.Summary.Content || strings.Contains(stored.Content, "abcdefgh123456") || !strings.Contains(stored.Content, "NEXT_LINE_PENDING") {
		t.Fatalf("serialized re-redaction changed text or digest: %v / %s", err, stored.Content)
	}
	for name, mutate := range map[string]func(*handoffMemoryEnvelope){
		"text digest": func(v *handoffMemoryEnvelope) { v.Generated.Text = "changed" },
		"authority":   func(v *handoffMemoryEnvelope) { v.Generated.InstructionAuthorized = true },
		"receipt":     func(v *handoffMemoryEnvelope) { v.Generated.Receipt.ModelAttempt = 0 },
		"source":      func(v *handoffMemoryEnvelope) { v.Generated.SourceRefs[0].SourceID = "file:/secret" },
		"raw secret": func(v *handoffMemoryEnvelope) {
			v.Generated.Text = "password=abcdefgh123456"
			v.Generated.TextSHA256 = handoffContentSHA256(v.Generated.Text)
		},
	} {
		t.Run(name, func(t *testing.T) {
			var value handoffMemoryEnvelope
			_ = json.Unmarshal([]byte(stored.Content), &value)
			mutate(&value)
			encoded, _ := json.Marshal(value)
			candidate := stored
			candidate.Content = string(encoded)
			candidate.ContentSHA256 = handoffContentSHA256(candidate.Content)
			if err := ValidateStoredSummary(candidate); err == nil {
				t.Fatal("tampered generated summary accepted")
			}
		})
	}
}
