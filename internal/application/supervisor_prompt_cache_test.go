package application

import (
	"reflect"
	"strings"
	"testing"

	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/skills"
)

func TestSupervisorWorkUpdatesKeepPriorConversationPrefix(t *testing.T) {
	history := []session.Message{
		session.NewMessage("session", "user", "Check the parser."),
		session.NewMessage("session", "assistant", strings.Repeat("Checked a fixture. ", 80)),
	}
	const pin = "Keep the public interface."
	build := func(status string) ([]llm.Message, modelContextLayout) {
		selection := contextmgr.Selection{Sections: []contextmgr.Section{
			{Kind: "work_board", Content: "Board: " + status},
			{Kind: "thread_context_pin", Required: true, Content: pin},
			{Kind: "note", Content: "Observation: " + status},
		}}
		return supervisorMessagesWithLayout(history, "Continue.", selection, skills.ContextAssembly{},
			skills.ExternalContextAssembly{}, domain.RunModeSnapshot{}, true)
	}
	before, _ := build("checking")
	after, layout := build("verified")
	end := layout.HistoryStart + layout.HistoryCount
	if layout.HistoryCount != len(history) || end+3 != len(after) ||
		after[layout.HistoryStart-1].Content != pin ||
		!reflect.DeepEqual(before[:end], after[:end]) {
		t.Fatal("updating a board or note rewrote the pin or the shared chat history")
	}
	wantCurrent := []llm.Message{
		{Role: "user", Content: "Board: verified"}, {Role: "user", Content: "Observation: verified"},
		{Role: "user", Content: "Continue."},
	}
	if !reflect.DeepEqual(after[end:], wantCurrent) {
		t.Fatal("updated work or the latest operator instruction was not delivered in order")
	}
	request := llm.ChatRequest{Messages: after}
	withoutHistory := estimateModelRequestTokens(request)
	for _, message := range after[layout.HistoryStart:end] {
		withoutHistory -= estimateModelMessageTokens(message)
	}
	bounded, plan, err := constrainRequestToModelWindow(request, llm.DefaultContextWindow(), layout, withoutHistory)
	if err != nil || plan.HistoryOmitted != len(history) ||
		!reflect.DeepEqual(bounded.Messages[layout.HistoryStart:], wantCurrent) {
		t.Fatalf("history trimming lost current work or operator input: omitted=%d err=%v", plan.HistoryOmitted, err)
	}
}

func TestSupervisorToolExposureKeepsSharedPolicyBeforeWorkspace(t *testing.T) {
	base := llm.ChatRequest{Messages: []llm.Message{
		{Role: "system", Content: "shared root policy"},
		{Role: "system", Content: "shared delivery instructions"},
		{Role: "system", Content: "historical control message"},
		{Role: "user", Content: "Inspect the files."},
	}}
	const historyStart = 2
	first := supervisorToolExposureRequest(base, supervisorToolExposureContext{WorkspaceRoot: "/sample/alpha"}, historyStart)
	second := supervisorToolExposureRequest(base, supervisorToolExposureContext{WorkspaceRoot: "/sample/bravo"}, historyStart)
	for _, request := range []llm.ChatRequest{first, second} {
		if len(request.Messages) != len(base.Messages)+1 ||
			!reflect.DeepEqual(request.Messages[:historyStart], base.Messages[:historyStart]) ||
			!reflect.DeepEqual(request.Messages[historyStart+1:], base.Messages[historyStart:]) {
			t.Fatal("workspace exposure displaced the shared policy or split the history")
		}
	}
	if !strings.Contains(first.Messages[historyStart].Content, "/sample/alpha") ||
		!strings.Contains(second.Messages[historyStart].Content, "/sample/bravo") {
		t.Fatal("the model did not receive the current workspace")
	}
}
