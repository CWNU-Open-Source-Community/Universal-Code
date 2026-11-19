package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/session"
)

func TestManualSummaryWindowKeepsOperatorCorrectionAndFittingDiagnostics(t *testing.T) {
	window := llm.ContextWindow{ProtocolVersion: llm.ContextWindowProtocolVersion, Source: "test",
		WindowTokens: 4096, SafetyMarginTokens: 128, DefaultOutputTokens: 256, MaxOutputTokens: 512}
	const correction = "只检查工作树。\n发布要等我另行确认。"
	for _, tc := range []struct {
		name       string
		correction string
		extra      contextmgr.Message
	}{
		{"oversized final tool", correction, contextmgr.Message{Role: "tool", SourceKind: session.SourceToolResult, Content: strings.Repeat("日志", 1500)}},
		{"user-shaped external evidence", correction + strings.Repeat("条款", 300), contextmgr.Message{Role: "user", SourceKind: session.SourceWorkspaceFile, Content: strings.Repeat("文档", 300)}},
		{"unauthorized user record", correction + strings.Repeat("条款", 300), contextmgr.Message{Role: "user", SourceKind: session.SourceOperatorMessage, Content: strings.Repeat("引文", 300)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			records := []contextmgr.Message{
				{Role: "tool", SourceKind: session.SourceToolResult, Content: "lint: import missing"},
				{Role: "user", SourceKind: session.SourceOperatorMessage, InstructionAuthorized: true, Content: tc.correction},
				{Role: "tool", SourceKind: session.SourceGoCommandResult, Content: "unit test still fails at line 19"},
				tc.extra,
			}
			for i := range records {
				records[i].SourceMessageID = int64(i + 1)
				digest := sha256.Sum256([]byte(records[i].Content))
				records[i].ContentSHA256 = hex.EncodeToString(digest[:])
			}
			original := slices.Clone(records)
			input := contextmgr.SummaryGenerationRequest{Messages: records, SourceSHA256: strings.Repeat("a", 64)}
			g := threadContextSummaryGenerator{}
			request, plan, fingerprint, omitted, coverage, err := g.prepareSummaryRequest(t.Context(), llm.ChatRequest{}, window, input)
			if err != nil {
				t.Fatal(err)
			}
			var payload struct {
				Goal    string                              `json:"original_goal"`
				Focus   string                              `json:"optional_summary_focus,omitempty"`
				History contextmgr.SummaryGenerationRequest `json:"history"`
				Omitted int                                 `json:"input_messages_omitted"`
			}
			if err := json.Unmarshal([]byte(request.Messages[1].Content), &payload); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(payload.History.Messages, original[:3]) {
				var ids []int64
				for _, message := range payload.History.Messages {
					ids = append(ids, message.SourceMessageID)
				}
				t.Fatalf("summary received records %v, want [1 2 3] with their complete original text and provenance", ids)
			}
			if omitted != 1 || payload.Omitted != omitted || request.Metadata["input_messages_omitted"] != strconv.Itoa(omitted) ||
				!strings.Contains(coverage, "3 of 4") || strings.Contains(coverage, "oldest") {
				t.Fatalf("inaccurate omission of final record: omitted=%d coverage=%q", omitted, coverage)
			}
			if plan.EstimatedInput > plan.InputLimitTokens || !slices.Equal(input.Messages, original) ||
				payload.History.SourceSHA256 != input.SourceSHA256 || request.Metadata["source_sha256"] != input.SourceSHA256 {
				t.Fatal("window or complete source snapshot changed")
			}
			if payload.History.InputFingerprint != fingerprint || request.Metadata["input_fingerprint"] != fingerprint {
				t.Fatal("inconsistent input fingerprint")
			}
			payload.History.InputFingerprint = ""
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(encoded)
			if fingerprint != hex.EncodeToString(digest[:]) {
				t.Fatal("fingerprint does not describe the selected records")
			}
		})
	}
}

func TestManualSummaryWindowKeepsCorrectionBeforeOperatorContinue(t *testing.T) {
	window := llm.ContextWindow{ProtocolVersion: llm.ContextWindowProtocolVersion, Source: "test",
		WindowTokens: 4096, SafetyMarginTokens: 128, DefaultOutputTokens: 256, MaxOutputTokens: 512}
	records := []contextmgr.Message{
		{SourceMessageID: 1, Role: "user", SourceKind: session.SourceOperatorMessage, InstructionAuthorized: true, Content: "更正：先审阅，暂不修改发布脚本。\n" + strings.Repeat("适用条款。", 125)},
		{SourceMessageID: 2, Role: "tool", SourceKind: session.SourceToolResult, Content: strings.Repeat("检查日志。", 125)},
		{SourceMessageID: 3, Role: "user", SourceKind: session.SourceOperatorMessage, InstructionAuthorized: true, Content: "继续"},
		{SourceMessageID: 4, Role: "tool", SourceKind: session.SourceGoCommandResult, Content: "lint failed at line 19"},
	}
	g := threadContextSummaryGenerator{}
	request, _, _, omitted, _, err := g.prepareSummaryRequest(t.Context(), llm.ChatRequest{}, window, contextmgr.SummaryGenerationRequest{Messages: records})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		History contextmgr.SummaryGenerationRequest `json:"history"`
	}
	if err := json.Unmarshal([]byte(request.Messages[1].Content), &payload); err != nil {
		t.Fatal(err)
	}
	if omitted != 1 || !slices.Equal(payload.History.Messages, []contextmgr.Message{records[0], records[2], records[3]}) {
		var ids []int64
		for _, message := range payload.History.Messages {
			ids = append(ids, message.SourceMessageID)
		}
		t.Fatalf("summary received records %v, want the correction, follow-up and diagnosis [1 3 4]", ids)
	}
}
