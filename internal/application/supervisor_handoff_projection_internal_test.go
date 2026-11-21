package application

import (
	"strconv"
	"strings"
	"testing"

	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/skills"
)

func TestHandoffKeepsLaterCorrectionWhenOnlyItsReferenceFits(t *testing.T) {
	const corrected = "Use the second interface."
	earlier := session.NewMessage("session", "user", "Use the first interface.")
	earlier.ID = 1
	later := session.NewMessage("session", "user", strings.Repeat("Migration details. ", 80)+corrected)
	later.ID = 2
	tail := session.NewMessage("session", "assistant", "Changes are pending.")
	tail.ID = 3
	result, err := contextmgr.NewManager(nil, contextmgr.Config{PreserveRecentMessages: 1}).Compact(
		t.Context(), "session", "workspace", []contextmgr.Message{
			session.ProjectContextMessage(earlier), session.ProjectContextMessage(later), session.ProjectContextMessage(tail),
		})
	if err != nil {
		t.Fatal(err)
	}
	originals, err := loadRecentOperatorContext(t.Context(), recentOperatorFixture{later}, "run", 8192)
	if err != nil || len(originals) != 1 || originals[0].Kind != recentOperatorHistory {
		t.Fatalf("missing candidate original: %v", err)
	}
	result.Summary.ID = 13
	memory, err := supervisorMemoryContext(result.Summary, true, nil, nil, nil, originals)
	if err != nil {
		t.Fatal(err)
	}
	messages, layout := supervisorMessagesWithLayout([]session.Message{tail}, "Which interface?", memory,
		skills.ContextAssembly{}, skills.ExternalContextAssembly{}, domain.RunModeSnapshot{}, true)
	req := llm.ChatRequest{Messages: messages}
	raw := estimateModelMessageTokens(recentOperatorWireMessage(originals[0]))
	ref := estimateModelMessageTokens(recentOperatorWireMessage(operatorReadback(originals[0])))
	without := estimateModelRequestTokens(req) - raw
	for _, tc := range []struct {
		name                        string
		available                   int
		wantOriginal, wantReference bool
	}{
		{"whole original", raw, true, false},
		{"readback reference", ref, false, true},
		{"no replay", 0, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limit := without + tc.available
			got, final, selected, err := fitRecentOperatorRequest(req, layout, memory, limit, originals, limit)
			if err != nil {
				t.Fatal(err)
			}
			handoff := got.Messages[final.HistoryStart-1]
			if handoff.Role != "assistant" || !strings.Contains(handoff.Content, earlier.Content) ||
				!strings.Contains(handoff.Content, "summary:13") || strings.Contains(handoff.Content, corrected) == tc.wantOriginal {
				t.Fatal("request lost or repeated the correction instead of projecting the sources actually sent")
			}
			if tc.wantOriginal && got.Messages[final.HistoryStart-2].Content != later.Content {
				t.Fatal("complete original changed")
			}
			if tc.wantReference && (got.Messages[final.HistoryStart-2].Role != "system" ||
				!strings.Contains(got.Messages[final.HistoryStart-2].Content, later.Provenance.ContentSHA256)) {
				t.Fatal("readback lost the original identity")
			}
			if !tc.wantOriginal && (!strings.Contains(handoff.Content, "message:2") ||
				!strings.Contains(handoff.Content, later.Provenance.ContentSHA256)) {
				t.Fatal("sole remaining user excerpt lost its source")
			}
			if err := supervisorModelContextAudit(selected).Validate(); err != nil {
				t.Fatal(err)
			}
			if estimateModelRequestTokens(got) > without+tc.available || got.Metadata["memory_tokens"] != strconv.Itoa(selected.EstimatedTokens) {
				t.Fatal("the final budget or the attached audit does not match the projected handoff")
			}
		})
	}
}
