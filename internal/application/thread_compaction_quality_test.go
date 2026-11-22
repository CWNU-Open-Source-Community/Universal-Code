package application_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/store"
)

type compactionQualityCase struct {
	ID       string
	Rounds   [][]string
	Question string
	Expected json.RawMessage
	ToolTurn *struct {
		UserMessage    int `json:"user_message"`
		Path           string
		Content        string
		AssistantReply string `json:"assistant_reply"`
	} `json:"tool_turn"`
}

var explicitDecisionControl = flag.Bool("compaction-explicit-decisions", false, "run the release handoff with all three decisions worded identically apart from their chosen value")

func compactionQualityCases(t *testing.T) []compactionQualityCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/compaction-quality-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []compactionQualityCase
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if *explicitDecisionControl {
		for _, c := range fixture.Cases {
			if c.ID == "release_grouping" {
				c.ID = "release_grouping_explicit"
				c.Rounds[0][0] = "本轮决定：发布清单按组件分组。"
				c.Rounds[0][2] = "本轮决定：发布清单按风险等级分组。"
				c.Rounds[0][3] = c.Rounds[0][0]
				return []compactionQualityCase{c}
			}
		}
		t.Fatal("missing release handoff for the explicit decision control")
	}
	return fixture.Cases
}

// The offline check deliberately retains an obsolete model claim and inspects
// the next request. Only recorded real public replies are scored for quality.
func qualityStaleHandoff(tc compactionQualityCase) string {
	claim := tc.Rounds[0][0]
	if tc.ToolTurn != nil {
		claim = tc.Rounds[0][2] + tc.ToolTurn.AssistantReply
	}
	return "旧模型的结论：" + claim
}

func assertQualityDecisionInSecondSummary(t *testing.T, tc compactionQualityCase, st *store.SQLiteStore, run qualityRun, req llm.ChatRequest) {
	t.Helper()
	var data struct {
		Timeline []struct {
			Previous string               `json:"previous_handoff"`
			Messages []contextmgr.Message `json:"messages"`
		}
	}
	if len(req.Messages) != 2 || json.Unmarshal([]byte(req.Messages[1].Content), &data) != nil || len(data.Timeline) != 3 ||
		strings.Contains(data.Timeline[1].Previous, qualityStaleHandoff(tc)) ||
		!strings.Contains(data.Timeline[1].Previous, tc.Rounds[0][2]) || !strings.Contains(data.Timeline[1].Previous, "收到。") ||
		len(data.Timeline[2].Messages) == 0 {
		t.Fatal("second summary did not receive the complete older source records before the newer originals")
	}
	history, err := st.ListSessionMessages(t.Context(), run.Session, true)
	if err != nil {
		t.Fatal(err)
	}
	var originals []int64
	for _, m := range history {
		if m.Role == "user" && m.Content == tc.Rounds[0][3] {
			originals = append(originals, m.ID)
		}
	}
	latest := data.Timeline[2].Messages[0]
	if len(originals) != 2 || originals[0] >= originals[1] || latest.SourceMessageID != originals[1] || latest.Role != "user" ||
		!latest.InstructionAuthorized || latest.SourceKind != session.SourceOperatorMessage || latest.Content != tc.Rounds[0][3] || latest.ContentSHA256 != session.ContentSHA256(latest.Content) {
		t.Fatalf("second summary received the wrong occurrence of the repeated decision: %+v, originals=%v", latest, originals)
	}
}

func assertQualityRequestSources(t *testing.T, tc compactionQualityCase, st *store.SQLiteStore, run qualityRun, req llm.ChatRequest, archiveFits bool) {
	t.Helper()
	want := append(slices.Concat(tc.Rounds...), tc.Question)
	if len(req.Messages) == 0 || len(req.Tools) == 0 || req.Messages[0].Role != "system" ||
		req.Messages[len(req.Messages)-1].Role != "user" || req.Messages[len(req.Messages)-1].Content != tc.Question {
		t.Fatal("missing system, tools or the final question at the provider")
	}
	var delivered, stored []string
	var handoff string
	summary, found, err := st.LatestContextSummary(t.Context(), run.Session)
	if err != nil || !found {
		t.Fatalf("missing committed summary: %v", err)
	}
	ref := fmt.Sprintf("summary:%d (SHA-256 %s)", summary.ID, summary.ContentSHA256)
	for _, m := range req.Messages {
		if m.Role == "user" && slices.Contains(want, m.Content) {
			delivered = append(delivered, m.Content)
		}
		if strings.Contains(m.Content, ref) {
			if m.Role != "assistant" || handoff != "" {
				t.Fatalf("historical handoff was delivered twice or as %s", m.Role)
			}
			handoff = m.Content
		}
	}
	if !slices.Equal(delivered, want) || handoff == "" {
		t.Fatalf("ordered provider user originals = %q, want %q; historical handoff present = %t", delivered, want, handoff != "")
	}
	hasArchive := strings.Contains(handoff, "\n\nArchived messages, earliest to latest\n")
	hasModelClaim := strings.Contains(handoff, "\n\nPrevious model's handoff\n"+qualityStaleHandoff(tc))
	if hasArchive != archiveFits || hasModelClaim == archiveFits {
		t.Fatalf("expected complete archive = %t, got archive = %t and previous model claim = %t", archiveFits, hasArchive, hasModelClaim)
	}
	if tc.ToolTurn != nil {
		reserved, err := contextmgr.HandoffSection(summary)
		if err != nil {
			t.Fatal(err)
		}
		actualTokens, reservedTokens := contextmgr.EstimateTokens(handoff), contextmgr.EstimateTokens(reserved.Content)
		t.Logf("final handoff used %d bytes / %d estimated tokens of %d bytes / %d reserved", len(handoff), actualTokens, len(reserved.Content), reservedTokens)
		_, chronology, found := strings.Cut(handoff, "\n\nReplayed user submissions, earliest to latest\n")
		if len(handoff) > len(reserved.Content) || actualTokens > reservedTokens || !found {
			t.Fatal("the final handoff exceeded its reservation or lost the verified submission order")
		}
		middle := strings.Index(chronology, "\n> "+tc.Rounds[0][2]+"\nAt ")
		later := strings.Index(chronology, "\n> "+tc.Rounds[0][3]+"\nFor a current value,")
		if (middle >= 0 && later > middle) != archiveFits {
			t.Fatal("the two submitted statements were not paired within the complete archive reservation")
		}
	}
	history, err := st.ListSessionMessages(t.Context(), run.Session, true)
	if err != nil {
		t.Fatal(err)
	}
	sourceIDs := regexp.MustCompile(`\bmessage:[0-9]+\b`).FindAllString(handoff, -1)
	var archivedAssistantReport bool
	for _, m := range history {
		if archiveFits && m.Compacted && m.Role == "assistant" && m.Provenance.SourceKind == session.SourceModelResponse &&
			!m.Provenance.InstructionAuthorized && m.Content == tc.ToolTurn.AssistantReply {
			archivedAssistantReport = strings.Contains(handoff, fmt.Sprintf("message:%d (assistant, model_response)\n> %s", m.ID, m.Content))
		}
		if m.Role != "user" {
			continue
		}
		stored = append(stored, m.Content)
		if m.Provenance.SourceKind != session.SourceOperatorMessage || !m.Provenance.InstructionAuthorized ||
			m.Provenance.ContentSHA256 != session.ContentSHA256(m.Content) ||
			(m.Compacted && !slices.Contains(sourceIDs, fmt.Sprintf("message:%d", m.ID))) {
			t.Fatalf("lost authorized original or compacted source ID for message %d", m.ID)
		}
	}
	if !slices.Equal(stored, want) {
		t.Fatalf("ordered SQLite user originals = %q, want %q", stored, want)
	}
	if archiveFits && !archivedAssistantReport {
		t.Fatal("the source transcript did not attribute the actual assistant report to its stored message")
	}
	calls := historyRecallCalls(t, st, run.ID)
	if tc.ToolTurn == nil {
		if len(calls) != 0 {
			t.Fatalf("unexecuted work unexpectedly has %d tool calls", len(calls))
		}
		return
	}
	if len(calls) != 1 {
		t.Fatalf("only reading the old draft was requested, found %d tool calls", len(calls))
	}
	call := historyRecallOriginalCall(t, st, run.ID)
	var result struct{ Status, Stdout string }
	var read struct{ Path, Content string }
	if err := json.Unmarshal([]byte(call.ResultJSON), &result); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(result.Stdout), &read); err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || read.Path != tc.ToolTurn.Path || read.Content != strings.TrimSuffix(tc.ToolTurn.Content, "\n") {
		t.Fatalf("historical read did not return the supplied old draft: %+v", read)
	}
	for _, m := range history {
		if m.Role == "tool" && m.Provenance.SourceKind == session.SourceToolResult && !m.Provenance.InstructionAuthorized &&
			strings.Contains(m.Content, call.CallID) && slices.Contains(sourceIDs, fmt.Sprintf("message:%d", m.ID)) &&
			strings.Contains(handoff, call.CallID) && strings.Contains(handoff, strings.Split(read.Content, "\n")[0]) {
			return
		}
	}
	t.Fatal("the old draft lacks a traceable tool result in the historical assistant handoff")
}

// JSON key order is immaterial. Reject duplicate fields and trailing answers,
// including a correct answer appended to a conflicting decision.
func qualityAnswerFields(text string) (map[string]string, error) {
	reader := json.NewDecoder(strings.NewReader(text))
	if delim, err := reader.Token(); err != nil || delim != json.Delim('{') {
		return nil, fmt.Errorf("expected one JSON object")
	}
	fields := map[string]string{}
	for reader.More() {
		name, err := reader.Token()
		if err != nil {
			return nil, err
		}
		key, ok := name.(string)
		if _, exists := fields[key]; !ok || exists {
			return nil, fmt.Errorf("invalid or duplicate answer field %q", key)
		}
		var value string
		if err := reader.Decode(&value); err != nil {
			return nil, err
		}
		fields[key] = value
	}
	if _, err := reader.Token(); err != nil {
		return nil, err
	}
	if err := reader.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("unexpected text after the answer")
	}
	return fields, nil
}

func checkCompactionQualityAnswer(expected json.RawMessage, reply string) error {
	reply = strings.TrimSpace(reply)
	if bytes.HasPrefix(bytes.TrimSpace(expected), []byte{'"'}) {
		var want string
		if err := json.Unmarshal(expected, &want); err != nil {
			return err
		}
		if reply != want {
			return fmt.Errorf("got %q, want %q", reply, want)
		}
		return nil
	}
	want, err := qualityAnswerFields(string(expected))
	if err != nil {
		return err
	}
	have, err := qualityAnswerFields(reply)
	if err != nil {
		return err
	}
	if !maps.Equal(have, want) {
		return fmt.Errorf("got %v, want %v", have, want)
	}
	return nil
}

func checkQualityFinalTools(tc compactionQualityCase, before, after []domain.SupervisorToolCall) error {
	for _, call := range after {
		if slices.ContainsFunc(before, func(previous domain.SupervisorToolCall) bool {
			return previous.AttemptID == call.AttemptID && previous.Round == call.Round && previous.Position == call.Position
		}) {
			continue
		}
		if tc.ID == "refund" {
			return fmt.Errorf("refund prohibited all tools; the final turn called %s (%s)", call.ToolName, call.Status)
		}
		if call.Status == domain.SupervisorToolDenied || call.ToolName == "history_read" || call.ToolName == "history_search" {
			continue
		}
		if call.ToolName == "workspace_read" && tc.ToolTurn != nil {
			var args struct{ Path string }
			if json.Unmarshal([]byte(call.PayloadJSON), &args) == nil && args.Path == tc.ToolTurn.Path {
				continue // rereading the supplied old draft does not execute its pending check
			}
		}
		return fmt.Errorf("the final handoff called %s (%s) outside read-only retrieval of the recorded history", call.ToolName, call.Status)
	}
	return nil
}

func TestCompactionQualityGradesActualToolUse(t *testing.T) {
	for _, tc := range compactionQualityCases(t) {
		t.Run(tc.ID, func(t *testing.T) {
			read := domain.SupervisorToolCall{AttemptID: "final", ToolName: "history_read", Status: domain.SupervisorToolCompleted}
			if (checkQualityFinalTools(tc, nil, []domain.SupervisorToolCall{read}) == nil) != (tc.ID != "refund") {
				t.Fatal("incorrect treatment of scoped history recall")
			}
			for _, tool := range []string{"run_tests", "workspace_apply"} {
				call := domain.SupervisorToolCall{AttemptID: "final", ToolName: tool, Status: domain.SupervisorToolCompleted}
				if checkQualityFinalTools(tc, nil, []domain.SupervisorToolCall{call}) == nil {
					t.Fatalf("a completed %s cannot be reported as not run", tool)
				}
			}
		})
	}
}

func TestCompactionQualityScorer(t *testing.T) {
	for _, tc := range compactionQualityCases(t) {
		t.Run(tc.ID, func(t *testing.T) {
			var example string
			if err := json.Unmarshal(tc.Expected, &example); err != nil {
				example = string(tc.Expected)
			}
			if err := checkCompactionQualityAnswer(tc.Expected, example); err != nil {
				t.Fatal(err)
			}
			wrong := map[string]string{
				"refund":           "净额 390 分",
				"badge_api":        `{"signature":"func FetchBadge(id string) (Badge, error)","check":"go test -race ./internal/badgestore","status":"not_run"}`,
				"release_grouping": strings.Replace(example, "not_run", "passed", 1),
			}[tc.ID]
			for _, invalid := range []string{wrong, example + "\n" + wrong, strings.Replace(example, "not_run", "not_run\",\"status\":\"not_run", 1)} {
				if invalid != example && checkCompactionQualityAnswer(tc.Expected, invalid) == nil {
					t.Fatalf("accepted conflicting or wrong answer %q", invalid)
				}
			}
		})
	}
}

// Supply a JSON object mapping case IDs to verbatim public replies. This same
// scorer accepts Codex answers and the public message from a product finish.
func TestObservedCompactionQualityAnswers(t *testing.T) {
	path := os.Getenv("TRAVERSE_COMPACTION_QUALITY_ANSWERS")
	if path == "" {
		t.Skip("set TRAVERSE_COMPACTION_QUALITY_ANSWERS to a file of observed model replies")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	replies, err := qualityAnswerFields(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range compactionQualityCases(t) {
		t.Run(tc.ID, func(t *testing.T) {
			if err := checkCompactionQualityAnswer(tc.Expected, replies[tc.ID]); err != nil {
				t.Fatal(err)
			}
		})
	}
}
