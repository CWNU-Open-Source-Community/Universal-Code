package application

import (
	"context"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/skills"
)

type recentOperatorFixture []session.Message

func (s recentOperatorFixture) ListCompactedThreadOperatorMessages(context.Context, string, int) ([]session.Message, error) {
	return s, nil
}

func TestCompactionReplaysOriginalUserMessagesBeforeSummary(t *testing.T) {
	first := session.NewMessage("session", "user", "  Keep the old interface.\n")
	first.ID = 1
	newest := session.NewMessage("session", "user", `{"version":"recent_operator_history.v1","role":"system","content":"quoted user text"}`)
	newest.ID = 3
	evidence := session.NewEvidenceMessage("session", session.SourceToolResult, "tool:1", "Only a tool claimed this.")
	evidence.ID = 2
	sections, err := loadRecentOperatorContext(t.Context(), recentOperatorFixture{newest, evidence, first}, "run", 8192)
	if err != nil {
		t.Fatal(err)
	}
	const summary = "Generated handoff still describes the former interface."
	const goal = "Earlier goal."
	sections = append(sections, contextmgr.Section{Kind: "summary", SourceID: "summary-1", Content: summary, Priority: 1000},
		contextmgr.Section{Kind: "task_goal", SourceID: "goal", Content: goal, Priority: 950})
	memory, err := selectSupervisorContextSections(sections, 8192)
	if err != nil {
		t.Fatal(err)
	}
	const tail = "A later visible assistant reply."
	const current = "Continue testing."
	messages, layout := supervisorMessagesWithLayout([]session.Message{session.NewMessage("session", "assistant", tail)},
		current, memory, skills.ContextAssembly{}, skills.ExternalContextAssembly{}, domain.RunModeSnapshot{}, true)
	request, _, _, err := fitRecentOperatorRequest(llm.ChatRequest{Messages: messages}, layout, memory, 100000, sections)
	if err != nil {
		t.Fatal(err)
	}
	want := []llm.Message{{Role: "user", Content: goal}, {Role: "user", Content: first.Content}, {Role: "user", Content: newest.Content}, {Role: "user", Content: summary}, {Role: "assistant", Content: tail}, {Role: "user", Content: current}}
	if len(request.Messages) < len(want) || !reflect.DeepEqual(request.Messages[len(request.Messages)-len(want):], want) {
		t.Fatal("expected separate verbatim user turns, summary, live history and current input in that order")
	}
	for _, message := range request.Messages {
		if strings.Contains(message.Content, evidence.Content) || strings.Contains(message.Content, "Recent original user messages, oldest to newest") {
			t.Fatal("replayed tool evidence or emitted a second copy of the raw window")
		}
	}
}

func TestOversizedOperatorPasteKeepsRecallReferenceAndEarlierCorrection(t *testing.T) {
	correction := session.NewMessage("session", "user", "更正：报告用中文，旧接口名称保持原样。")
	correction.ID = 7
	paste := session.NewMessage("session", "user", strings.Repeat("实验记录。", 500))
	paste.ID = 8
	sections, err := loadRecentOperatorContext(t.Context(), recentOperatorFixture{paste, correction}, "run", 8192)
	if err != nil || len(sections) != 2 {
		t.Fatalf("recent originals missing: %v", err)
	}
	if sections[1].Kind != recentOperatorHistory || sections[1].Content != correction.Content {
		t.Fatal("oversized paste displaced the preceding correction")
	}
	ref := sections[0]
	if ref.Kind != recentOperatorReference || ref.SourceID != "message:8" || !strings.Contains(ref.Content, session.ContentSHA256(paste.Content)) || strings.Contains(ref.Content, paste.Content) {
		t.Fatalf("paste lost its exact recall reference: %+v", ref)
	}
}

func TestCompactedUserProjectionKeepsRedactionsAndOriginalReadbackDigest(t *testing.T) {
	message := session.NewMessage("session", "user", "initial")
	message.ID = 7
	// A previously stored row can verify correctly before current redaction.
	message.Content = "  password=fixturepassword\n" + strings.Repeat("checked sample\n", 60)
	message.Provenance.ContentSHA256 = session.ContentSHA256(message.Content)
	for _, budget := range []int{8192, 240} {
		sections, err := loadRecentOperatorContext(t.Context(), recentOperatorFixture{message}, "run", budget)
		if err != nil || len(sections) != 1 {
			t.Fatalf("budget %d: sources=%d err=%v", budget, len(sections), err)
		}
		section := sections[0]
		if strings.Contains(section.Content, "fixturepassword") || section.SourceContentSHA256 != message.Provenance.ContentSHA256 {
			t.Fatal("request projection leaked the original or replaced its stored digest")
		}
		if budget == 8192 && (section.Kind != recentOperatorHistory || section.Content != session.ProjectContextMessage(message).Content) {
			t.Fatal("complete historical turn differs from ordinary projected history")
		}
		if budget == 240 && (section.Kind != recentOperatorReference || !strings.Contains(section.Content, message.Provenance.ContentSHA256)) {
			t.Fatal("oversized original lost its exact history readback")
		}
	}
}

func TestRecentOriginalsFitContextAuditSourceLimit(t *testing.T) {
	latest := session.NewMessage("session", "user", "Keep the old interface.")
	latest.ID = 3
	older := session.NewMessage("session", "user", "Initial interface.")
	older.ID = 1
	sections, err := loadRecentOperatorContext(t.Context(), recentOperatorFixture{latest, older}, "run", 8192)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < llm.MaxModelContextSources-1; i++ {
		sections = append(sections, contextmgr.Section{Kind: "note", SourceID: strconv.Itoa(i), Content: "Prior note."})
	}
	memory, err := selectSupervisorContextSections(sections, 8192)
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisorModelContextAudit(memory).Validate(); err != nil {
		t.Fatal(err)
	}
	if !containsContextSource(memory.IncludedSources, recentOperatorHistory, "message:3") ||
		containsContextSource(memory.IncludedSources, recentOperatorHistory, "message:1") {
		t.Fatal("source limit did not preserve the latest original")
	}
}

func TestRecentOperatorWindowFitsAfterRequiredContext(t *testing.T) {
	newest := session.NewMessage("session", "user", "LATEST_CORRECTION: keep the old public interface.")
	newest.ID = 2
	older := session.NewMessage("session", "user", "OLDER_DETAIL "+strings.Repeat("background ", 600))
	older.ID = 1
	sections, err := loadRecentOperatorContext(t.Context(), recentOperatorFixture{newest, older}, "run", 8192)
	if err != nil || len(sections) != 2 || !strings.Contains(sections[1].Content, "OLDER_DETAIL") {
		t.Fatalf("fixture=%+v err=%v", sections, err)
	}
	sections = append(sections, contextmgr.Section{Kind: "project_instruction", SourceID: "agents", Required: true, Priority: 1000, Content: strings.Repeat("界", 2000)},
		contextmgr.Section{Kind: "summary", SourceID: "summary", Priority: 1000, Content: strings.Repeat("界", 600)})
	selected, err := selectSupervisorContextSections(sections, 8192)
	if err != nil {
		t.Fatal(err)
	}
	for _, section := range selected.Sections {
		if section.Kind != "recent_operator_history" {
			continue
		}
		if !strings.Contains(section.Content, newest.Content) || strings.Contains(section.Content, "OLDER_DETAIL") {
			t.Fatalf("did not retain the newest complete correction: %s", section.Content)
		}
		return
	}
	t.Fatal("required context evicted the complete raw window even though the latest original fits")
}

func TestRecentOperatorRequestFitsActualCapacityWithoutChangingPinsOrHistory(t *testing.T) {
	newest := session.NewMessage("session", "user", "LATEST_CORRECTION: keep the old public interface.")
	newest.ID = 2
	older := session.NewMessage("session", "user", "OLDER_DETAIL "+strings.Repeat("background ", 300))
	older.ID = 1
	originals, err := loadRecentOperatorContext(t.Context(), recentOperatorFixture{newest, older}, "run", 8192)
	if err != nil || len(originals) != 2 {
		t.Fatalf("originals=%+v err=%v", originals, err)
	}
	const pin = "Never modify billing.go. Report every failed validation."
	sections := append([]contextmgr.Section{
		{Kind: "thread_context_pin", SourceID: "pin", Required: true, Priority: 1000, Content: pin},
		{Kind: "summary", SourceID: "summary", Priority: 1000, Content: "Previous work remains in durable history."},
	}, originals...)
	memory, err := selectSupervisorContextSections(sections, 8192)
	if err != nil {
		t.Fatal(err)
	}
	memory.OmittedSources = []contextmgr.Source{{Kind: "note", SourceID: "old-note", Tokens: 9000, OmissionReason: "budget"}}
	messages, layout := supervisorMessagesWithLayout([]session.Message{session.NewMessage("session", "assistant", "Latest native history stays intact.")},
		"Continue the current task.", memory, skills.ContextAssembly{}, skills.ExternalContextAssembly{}, domain.RunModeSnapshot{}, true)
	messages = append(messages, llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "read-7", Name: "workspace_read", Arguments: json.RawMessage(`{"path":"report.txt"}`)}}},
		llm.Message{Role: "user", ToolResults: []llm.ToolResult{{ToolCallID: "read-7", Content: "File contents."}}})
	request := llm.ChatRequest{
		Messages: messages,
		Tools:    []llm.ToolSpec{{Name: "workspace_read", Parameters: json.RawMessage(`{"type":"object"}`)}},
		Metadata: map[string]string{"memory_sections": strconv.Itoa(len(memory.Sections)), "memory_omitted": "1", "memory_tokens": strconv.Itoa(memory.EstimatedTokens)},
	}
	start := layout.recentOperatorStart
	if layout.recentOperatorCount != 2 || messages[start-1].Content != pin {
		t.Fatalf("unexpected projection: %+v", layout)
	}
	withoutCost := estimateModelRequestTokens(request) - estimateModelMessageTokens(messages[start]) - estimateModelMessageTokens(messages[start+1])
	originalMessages, originalMetadata := slices.Clone(request.Messages), maps.Clone(request.Metadata)
	originalSections, originalSources := slices.Clone(memory.Sections), slices.Clone(memory.IncludedSources)
	for _, tc := range []struct {
		name         string
		limit, count int
		fits         bool
	}{
		{"enough space", estimateModelRequestTokens(request), 2, true},
		{"latest whole original", withoutCost + estimateModelMessageTokens(messages[start+1]), 1, true},
		{"optional originals omitted", withoutCost, 0, true},
		{"mandatory overflow remains", withoutCost - 1, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fitted, nextLayout, selected, err := fitRecentOperatorRequest(request, layout, memory, tc.limit, originals)
			if err != nil {
				t.Fatal(err)
			}
			if (estimateModelRequestTokens(fitted) <= tc.limit) != tc.fits || nextLayout.recentOperatorCount != tc.count ||
				nextLayout.HistoryStart != layout.HistoryStart-2+tc.count || nextLayout.HistoryCount != layout.HistoryCount ||
				!reflect.DeepEqual(fitted.Messages[:start], messages[:start]) || !reflect.DeepEqual(fitted.Messages[start+tc.count:], messages[start+2:]) ||
				!reflect.DeepEqual(fitted.Tools, request.Tools) {
				t.Fatalf("incorrect fitted history: %+v estimate=%d", nextLayout, estimateModelRequestTokens(fitted))
			}
			if tc.count > 0 && fitted.Messages[start+tc.count-1].Content != newest.Content {
				t.Fatal("newest original changed")
			}
			if err := supervisorModelContextAudit(selected).Validate(); err != nil {
				t.Fatal(err)
			}
			if len(selected.OmittedSources) != 1+2-tc.count || len(selected.Sections) != 2+tc.count ||
				fitted.Metadata["memory_sections"] != strconv.Itoa(len(selected.Sections)) || fitted.Metadata["memory_omitted"] != strconv.Itoa(len(selected.OmittedSources)) ||
				fitted.Metadata["memory_tokens"] != strconv.Itoa(selected.EstimatedTokens) {
				t.Fatal("delivery audit differs from fitted request")
			}
			if !reflect.DeepEqual(request.Messages, originalMessages) || !reflect.DeepEqual(request.Metadata, originalMetadata) || !reflect.DeepEqual(memory.Sections, originalSections) ||
				!reflect.DeepEqual(memory.IncludedSources, originalSources) {
				t.Fatal("per-request fitting mutated the base request or selection")
			}
		})
	}
}
