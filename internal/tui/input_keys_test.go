package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/toolrun"
)

func TestModelInputKeepsJKAndNavigationRespectsFocus(t *testing.T) {
	input := textinput.New()
	input.Focus()
	model := &Model{
		input: input, focus: focusInput, activityView: activityTools,
		messages: []session.Message{{Role: "user", Content: "first message"}, {Role: "assistant", Content: "second message"}},
		toolRuns: []toolrun.ToolRun{{ID: "first"}, {ID: "second"}},
	}
	const message = "joke /work/project"
	for _, char := range message {
		model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{char}})
	}
	if model.input.Value() != message || model.messageScroll != 0 {
		t.Fatalf("typed input was intercepted: value=%q scroll=%d", model.input.Value(), model.messageScroll)
	}
	model.Update(tea.KeyMsg{Type: tea.KeyUp})
	if model.messageScroll != 1 {
		t.Fatalf("up arrow did not scroll messages: %d", model.messageScroll)
	}
	model.Update(tea.KeyMsg{Type: tea.KeyDown})
	if model.messageScroll != 0 || model.input.Value() != message {
		t.Fatalf("down arrow changed input or failed to scroll: value=%q scroll=%d", model.input.Value(), model.messageScroll)
	}
	model.Update(tea.KeyMsg{Type: tea.KeyTab})
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'j'}},
		{Type: tea.KeyDown},
	} {
		model.selectedTool = 0
		model.Update(key)
		if model.focus != focusTools || model.selectedTool != 1 {
			t.Fatalf("%s did not select next activity item: focus=%s selection=%d", key, model.focus, model.selectedTool)
		}
	}
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'k'}},
		{Type: tea.KeyUp},
	} {
		model.selectedTool = 1
		model.Update(key)
		if model.selectedTool != 0 || model.input.Value() != message {
			t.Fatalf("%s did not navigate without editing input: selection=%d value=%q", key, model.selectedTool, model.input.Value())
		}
	}
}
