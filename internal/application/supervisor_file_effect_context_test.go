package application

import (
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestFileEffectReceiptSurvivesBoundaryAndSegmentBudget(t *testing.T) {
	call := boundaryBudgetCall(t, "proposal-long-diff", "workspace_change", map[string]any{
		"version": "agent-code-tools.v1", "edit_id": "edit-receipt", "path": "app.js", "operation": "create",
		"status": "proposed", "proposed_sha256": strings.Repeat("a", 64), "apply_authorized": false, "review_required": true, "diff": strings.Repeat("large diff ", 10000)})
	call.Round = 1
	call.Position = 1
	call.ModelAttempt = 1
	call.PayloadJSON = `{}`
	call.AuthorityJSON = `{}`
	call.CreatedAt = time.Now().UTC()
	call.CompletedAt = &call.CreatedAt
	content, err := boundedToolBoundaryContext("attempt-next", []domain.SupervisorToolCall{call})
	if err != nil || !strings.Contains(content, `"effect":"proposal_only"`) {
		t.Fatalf("boundary dropped effect: %v %s", err, content)
	}
	entries, err := supervisorSegmentReceiptEntries([]domain.SupervisorToolRound{{RunID: call.RunID, Turn: call.Turn, AttemptID: call.AttemptID, Round: 1, Calls: []domain.SupervisorToolCall{call}}})
	if err != nil || entries[0].Calls[0].Observation == nil || entries[0].Calls[0].Observation.Effect != "proposal_only" || !entries[0].Calls[0].DetailsOmitted {
		t.Fatalf("minimal receipt omitted effect: %+v %v", entries, err)
	}
	encoded, _ := json.Marshal(entries)
	if strings.Contains(string(encoded), "large diff") || !strings.Contains(string(encoded), "expected_sha256") {
		t.Fatal("receipt copied body or lost exact reference")
	}
	cp := domain.SupervisorCheckpoint{RunID: call.RunID, NextTurn: 2, AttemptID: "attempt-next"}
	content, err = boundedSupervisorFileEffectContext(cp, []domain.SupervisorToolCall{call})
	if err != nil || !strings.Contains(content, `"effect":"proposal_only"`) || strings.Contains(content, "large diff") {
		t.Fatalf("projection=%s %v", content, err)
	}
	message := toolBoundaryEvidenceMessage("session-test", cp.AttemptID, content)
	if message.Role != "user" || !strings.Contains(message.Content, `"instruction_authorized":false`) {
		t.Fatal("historical effect was elevated", message)
	}
	if estimateModelRequestTokens(llm.ChatRequest{Messages: []llm.Message{message}})-8 > toolBoundaryContextTokens {
		t.Fatal("effect receipt exceeded its budget")
	}
	call.RunID = "other-run"
	content, _ = boundedSupervisorFileEffectContext(cp, []domain.SupervisorToolCall{call})
	if strings.Contains(content, "edit-receipt") {
		t.Fatal("another run leaked into effect context")
	}
}

func TestProtocolRepairMatchesInteractiveToolBoundary(t *testing.T) {
	for _, rounds := range []int{0, 2, domain.MaxSupervisorToolRounds} {
		request := llm.ChatRequest{Tools: []llm.ToolSpec{{Name: "workspace_change"}}, Messages: []llm.Message{{Role: "system", Content: "policy"}}}
		atBoundary := rounds == domain.MaxSupervisorToolRounds
		repaired := supervisorProtocolRepairRequest(request, "missing required message", supervisorRepairContext{ThreadEndTurn: true, ToolRounds: rounds, BoundaryReached: atBoundary, AllowContinue: atBoundary})
		last := repaired.Messages[len(repaired.Messages)-1].Content
		if len(repaired.Tools) != 0 {
			t.Fatal("format repair gained tools")
		}
		if rounds != domain.MaxSupervisorToolRounds && (!strings.Contains(last, "Do not use tool-free continue here") || strings.Contains(last, `action="continue"`) || !strings.Contains(last, "required nonempty message string")) {
			t.Fatal("repeated the observed 020/029 guidance contradiction", last)
		}
		if rounds == domain.MaxSupervisorToolRounds && !strings.Contains(last, `action="continue"`) {
			t.Fatal("removed continue at a permitted scheduling boundary")
		}
	}
	reason, _ := domain.NewSupervisorThreadContinueRepairReason(2)
	repaired := supervisorProtocolRepairRequest(llm.ChatRequest{Tools: []llm.ToolSpec{{Name: "workspace_change"}}}, reason, supervisorRepairContext{ThreadEndTurn: true, ToolRounds: 2})
	last := repaired.Messages[len(repaired.Messages)-1].Content
	if len(repaired.Tools) != 1 || !strings.Contains(last, "required nonempty message string") || strings.Contains(last, "Operator review is required") {
		t.Fatal("continuation correction lost tools or complete schema")
	}
}

func TestOutputBudgetGuidanceUsesActualCapWithoutIncreasingIt(t *testing.T) {
	for _, requested := range []int{0, 700, 9000} {
		r := llm.ChatRequest{MaxTokens: requested, Tools: []llm.ToolSpec{{Name: "workspace_change"}}, Messages: []llm.Message{{Role: "user", Content: "make a file"}}}
		window := llm.DefaultContextWindow()
		guided := supervisorRuntimeFacts(supervisorOutputBudgetGuidance(r, window, requested, 0), window, requested, 1, nil, "")
		allowance := "This request uses the provider's default output allowance."
		if requested > 0 {
			allowance = fmt.Sprintf("The model's output limit is at most %d tokens including tool arguments", window.OutputLimit(requested))
		}
		if guided.MaxTokens != requested || len(r.Messages) != 1 || r.Messages[0].Content != "make a file" ||
			len(guided.Messages) != 2 || guided.Messages[1].Content != r.Messages[0].Content ||
			!strings.Contains(guided.Messages[0].Content, allowance) ||
			!strings.Contains(guided.Messages[0].Content, "A truncated response will not dispatch its tool calls") ||
			!strings.Contains(guided.Messages[0].Content, "Do not claim that a proposal was applied without its successful apply result") {
			t.Fatal("current allowance, static tool policy or operator input changed")
		}
	}
}
