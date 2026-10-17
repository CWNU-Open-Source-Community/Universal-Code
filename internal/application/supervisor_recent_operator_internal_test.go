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
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/session"
)

type recentOperatorFixture []session.Message

func (s recentOperatorFixture) ListCompactedThreadOperatorMessages(context.Context, string, int) ([]session.Message, error) {
	return s, nil
}

func TestRecentOperatorWindowFitsAfterRequiredContext(t *testing.T) {
	newest := session.NewMessage("session", "user", "LATEST_CORRECTION: keep the old public interface.")
	newest.ID = 2
	older := session.NewMessage("session", "user", "OLDER_DETAIL "+strings.Repeat("background ", 600))
	older.ID = 1
	sections, err := loadRecentOperatorContext(t.Context(), recentOperatorFixture{newest, older}, "run", 8192)
	if err != nil || len(sections) != 1 || !strings.Contains(sections[0].Content, "OLDER_DETAIL") {
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
	if err != nil || len(originals) != 1 {
		t.Fatalf("originals=%+v err=%v", originals, err)
	}
	const pin = "Never modify billing.go. Report every failed validation."
	sections := append([]contextmgr.Section{
		{Kind: "thread_context_pin", SourceID: "pin", Required: true, Priority: 1000, Content: pin},
		{Kind: "summary", SourceID: "summary", Priority: 1000, Content: "Previous work remains in durable history."},
	}, originals...)
	memory, err := contextmgr.SelectSections(sections, 8192)
	if err != nil {
		t.Fatal(err)
	}
	memory.OmittedSources = []contextmgr.Source{{Kind: "note", SourceID: "old-note", Tokens: 9000, OmissionReason: "budget"}}
	request := llm.ChatRequest{
		Messages: []llm.Message{{Role: "system", Content: "Follow current task instructions."}},
		Tools:    []llm.ToolSpec{{Name: "workspace_read", Parameters: json.RawMessage(`{"description":"` + strings.Repeat("tool schema ", 400) + `"}`)}},
		Metadata: map[string]string{"memory_sections": "3", "memory_omitted": "1", "memory_tokens": strconv.Itoa(memory.EstimatedTokens)},
	}
	for _, section := range memory.Sections {
		request.Messages = append(request.Messages, llm.Message{Role: "user", Content: section.Content})
	}
	layout := modelContextLayout{HistoryStart: len(request.Messages), HistoryCount: 1}
	request.Messages = append(request.Messages, llm.Message{Role: "assistant", Content: "Latest native history stays intact."},
		llm.Message{Role: "user", Content: "Continue the current task."})
	recentIndex := layout.HistoryStart - 1
	var envelope recentOperatorEnvelope
	if err := json.Unmarshal([]byte(strings.TrimPrefix(originals[0].Content, recentOperatorGuidance)), &envelope); err != nil || len(envelope.Messages) != 2 {
		t.Fatalf("fixture envelope=%+v err=%v", envelope, err)
	}
	envelope.Messages = envelope.Messages[1:]
	latestJSON, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	latestContent := recentOperatorGuidance + string(latestJSON)
	latestOnly := request
	latestOnly.Messages = slices.Clone(request.Messages)
	latestOnly.Messages[recentIndex].Content = latestContent
	withoutRecent := request
	withoutRecent.Messages = slices.Delete(slices.Clone(request.Messages), recentIndex, recentIndex+1)
	originalMessages := slices.Clone(request.Messages)
	originalMetadata := maps.Clone(request.Metadata)
	originalSections := slices.Clone(memory.Sections)
	originalSources := slices.Clone(memory.IncludedSources)
	for _, tc := range []struct {
		name        string
		limit       int
		wantContent string
		fits        bool
	}{
		{"enough space", estimateModelRequestTokens(request), originals[0].Content, true},
		{"latest whole original", estimateModelRequestTokens(latestOnly), latestContent, true},
		{"optional original omitted", estimateModelRequestTokens(withoutRecent), "", true},
		{"mandatory overflow remains", estimateModelRequestTokens(withoutRecent) - 1, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fitted, fittedLayout, selected, err := fitRecentOperatorRequest(request, layout, memory, tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			if (estimateModelRequestTokens(fitted) <= tc.limit) != tc.fits {
				t.Fatalf("request estimate=%d limit=%d", estimateModelRequestTokens(fitted), tc.limit)
			}
			if fitted.Messages[1].Content != pin || !reflect.DeepEqual(fitted.Messages[2], request.Messages[2]) ||
				fittedLayout.HistoryCount != layout.HistoryCount ||
				!reflect.DeepEqual(fitted.Messages[fittedLayout.HistoryStart:], request.Messages[layout.HistoryStart:]) ||
				!reflect.DeepEqual(fitted.Tools, request.Tools) {
				t.Fatal("fitting optional originals changed pins, summary, history, current input or tools")
			}
			if tc.wantContent != "" {
				if fitted.Messages[recentIndex].Content != tc.wantContent || fittedLayout != layout || len(selected.OmittedSources) != 1 {
					t.Fatal("did not retain the expected complete recent originals")
				}
			} else if fittedLayout.HistoryStart != layout.HistoryStart-1 || len(selected.Sections) != 2 ||
				len(selected.OmittedSources) != 2 || selected.OmittedSources[1].OmissionReason != "budget" {
				t.Fatalf("omitted original not reflected in layout/audit: %+v %+v", fittedLayout, selected)
			}
			audit := supervisorModelContextAudit(selected)
			if err := audit.Validate(); err != nil {
				t.Fatal(err)
			}
			total := 0
			for _, source := range audit.Included {
				total += source.Tokens
			}
			if total != selected.EstimatedTokens || fitted.Metadata["memory_tokens"] != strconv.Itoa(total) ||
				fitted.Metadata["memory_sections"] != strconv.Itoa(len(selected.Sections)) ||
				fitted.Metadata["memory_omitted"] != strconv.Itoa(len(selected.OmittedSources)) {
				t.Fatalf("delivered context audit/metadata disagree: %+v %+v", audit, fitted.Metadata)
			}
			if !reflect.DeepEqual(request.Messages, originalMessages) || !reflect.DeepEqual(request.Metadata, originalMetadata) ||
				!reflect.DeepEqual(memory.Sections, originalSections) || !reflect.DeepEqual(memory.IncludedSources, originalSources) {
				t.Fatal("per-request fitting mutated the reusable base request or context selection")
			}
		})
	}
}
