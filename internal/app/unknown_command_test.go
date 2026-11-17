package app

import (
	"strings"
	"testing"
)

func TestSuggestCommandsReturnsCloseMatches(t *testing.T) {
	got := suggestCommands("wrkspace")
	if len(got) == 0 || got[0] != "workspace" {
		t.Fatalf("suggestCommands(%q) = %v, want workspace", "wrkspace", got)
	}
}

func TestSuggestCommandsRejectsShortInput(t *testing.T) {
	got := suggestCommands("a")
	if len(got) != 0 {
		t.Fatalf("single-character input must not produce suggestions: %v", got)
	}
}

func TestSuggestCommandsCapsAtThree(t *testing.T) {
	got := suggestCommands("run")
	if len(got) > 3 {
		t.Fatalf("suggestions must cap at 3: %v", got)
	}
}

func TestUnknownCommandErrorIncludesSuggestionsAndHelp(t *testing.T) {
	a := &App{}
	err := a.unknownCommandError("wrkspace")
	if !strings.Contains(err.Error(), "did you mean") || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("error missing suggestion: %v", err)
	}
	if !strings.Contains(err.Error(), "cyberagent help") {
		t.Fatalf("error missing help pointer: %v", err)
	}
}

func TestUnknownCommandErrorWithoutSuggestionsIncludesHelp(t *testing.T) {
	a := &App{}
	err := a.unknownCommandError("zzzzzzzzzzz")
	if strings.Contains(err.Error(), "did you mean") {
		t.Fatalf("nonsense input must not suggest: %v", err)
	}
	if !strings.Contains(err.Error(), "cyberagent help") {
		t.Fatalf("error missing help pointer: %v", err)
	}
}
