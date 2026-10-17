package application

import (
	"context"
	"strings"
	"testing"

	"cyberagent-workbench/internal/contextmgr"
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
