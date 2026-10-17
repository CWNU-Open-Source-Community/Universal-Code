package application

import (
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
)

func TestSpecialistReceiptCapacityPreservesBriefAndNativePairs(t *testing.T) {
	at := time.Now()
	rounds := []domain.SpecialistToolRound{}
	for i := 1; i <= 2; i++ {
		rounds = append(rounds, domain.SpecialistToolRound{Round: i, AssistantText: "observations", Calls: []domain.SpecialistToolCall{{CallID: strings.Repeat("c", i), Name: "workspace_read", PayloadJSON: `{}`, ResultJSON: strings.Repeat("中", 10000), Status: domain.SupervisorToolCompleted, CompletedAt: &at}}})
	}
	input := "authenticated task brief: do not change files"
	original := llm.ChatRequest{Messages: []llm.Message{{Role: "system", Content: "permission policy"}, {Role: "user", Content: input}}, MaxTokens: 1024}
	request, err := specialistRequestWithNativeRounds(original, rounds)
	if err != nil {
		t.Fatal(err)
	}
	fit, through, err := fitSpecialistContext(request, llm.DefaultContextWindow(), modelContextLayout{}, rounds, domain.SpecialistContextState{}, input, false)
	if err != nil || through != 1 {
		t.Fatalf("fit=%d err=%v", through, err)
	}
	if err := verifyNativeSpecialistRequest(fit, input, rounds[through:], false); err != nil {
		t.Fatal(err)
	}
	if fit.Messages[0].Content != "permission policy" || fit.Messages[len(fit.Messages)-3].Content != input {
		t.Fatal("policy or brief changed")
	}
	minimal, err := specialistRequestWithContextReceipts(request, rounds, 2, input)
	if err != nil {
		t.Fatal(err)
	}
	state := domain.SpecialistContextState{ThroughRound: 2, SourceSHA256: domain.SpecialistContextSourceSHA256(rounds), RecoveryInputLimit: estimateModelRequestTokens(minimal) - 1}
	if _, _, err := fitSpecialistContext(request, llm.DefaultContextWindow(), modelContextLayout{}, rounds, state, input, false); apperror.CodeOf(err) != apperror.CodeResourceExhausted || !strings.Contains(err.Error(), "minimal tool receipts") {
		t.Fatalf("minimal receipt overflow not explicit: %v", err)
	}
	state.RecoveryInputLimit = 0
	rounds[0].Calls[0].ResultJSON = "mutated"
	if _, _, err := fitSpecialistContext(request, llm.DefaultContextWindow(), modelContextLayout{}, rounds, state, input, false); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("changed source accepted: %v", err)
	}
	if _, _, err := fitSpecialistContext(request, llm.DefaultContextWindow(), modelContextLayout{}, rounds, domain.SpecialistContextState{}, input, true); apperror.CodeOf(err) != apperror.CodeResourceExhausted {
		t.Fatalf("private route was compressed: %v", err)
	}
}
