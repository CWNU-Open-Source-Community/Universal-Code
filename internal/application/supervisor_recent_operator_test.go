package application_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
)

// Uses the existing default Thread fixture and the real request/store pipeline.
// The provider deterministically fails only the auxiliary summary request.
func TestRecentOperatorOriginalSurvivesRepeatedFallbackCompaction(t *testing.T) {
	const marker = "PRESERVATION_MIDDLE_ACTIVE_CONSTRAINT"
	const restriction = marker + ": do not modify billing.go for the entire task."
	st := openHistoryRecallStore(t, filepath.Join(t.TempDir(), "preservation.db"))
	defer st.Close()
	_, run := createHistoryRecallRun(t, st, "preservation-audit", "Fixture data.\n")
	mission, err := st.GetMission(t.Context(), run.MissionID)
	if err != nil || strings.Contains(mission.Goal, marker) {
		t.Fatalf("fixture restriction must be a later user request, separate from mission.Goal: %v", err)
	}
	p := newGeneratedProtocolProvider()
	p.auxiliary = func(context.Context, llm.ChatRequest, int) (*llm.ChatResponse, error) {
		return nil, llm.NewProviderError(llm.OutcomePermanent, p.Name(), "fixed auxiliary provider failure", nil)
	}
	turns, _ := generatedThreadServices(st, p, nil)
	var summaryIDs []int64
	var afterCompactionPresent, beforeCompactionPresent bool
	longCorrection := "Additional constraints for the same task. " + strings.Repeat("Background detail. ", 90) + restriction + strings.Repeat("More detail. ", 130) + "Final note: report findings."
	for index := 1; index <= 24; index++ {
		content := "continue"
		if index == 1 {
			content = "Review the existing parser implementation."
		} else if index == 2 {
			content = longCorrection
		}
		submitHistoryRecall(t, turns, run.ID, fmt.Sprintf("context-preservation-audit-message-%02d", index), content)
		generated, normal := p.splitRequests()
		if len(normal) == 0 {
			t.Fatal("normal request missing")
		}
		latestRequest := normal[len(normal)-1].Request
		summary, found, err := st.LatestContextSummary(t.Context(), run.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			if index >= 2 && strings.Contains(generatedRequestText(latestRequest), marker) {
				beforeCompactionPresent = true
			}
			continue
		}
		if len(summaryIDs) == 0 || summaryIDs[len(summaryIDs)-1] != summary.ID {
			summaryIDs = append(summaryIDs, summary.ID)
		}
		if strings.Contains(summary.Content, marker) || !strings.Contains(generatedRequestText(latestRequest), longCorrection) {
			t.Fatalf("original user text missing or fallback fixture changed at input %d", index)
		}
		if len(generated) == 0 {
			t.Fatal("default Thread never dispatched auxiliary generation")
		}
		afterCompactionPresent = true
	}
	generated, normal := p.splitRequests()
	if !beforeCompactionPresent || !afterCompactionPresent || len(summaryIDs) < 2 || len(generated) < 2 || !strings.Contains(generatedRequestText(generated[0].Request), marker) {
		t.Fatalf("did not exercise complete original, fallback and repeated normal requests: before=%v preserved=%v summaries=%v auxiliary=%d", beforeCompactionPresent, afterCompactionPresent, summaryIDs, len(generated))
	}
	if strings.Contains(generatedRequestText(generated[1].Request), marker) {
		t.Fatal("second generation source unexpectedly recovered the compacted middle")
	}
	history, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	var originalID int64
	for _, message := range history {
		if message.Content == longCorrection {
			originalID = message.ID
			if !message.Compacted || !message.Provenance.InstructionAuthorized {
				t.Fatal("original operator provenance or compaction flag changed")
			}
		}
	}
	if originalID == 0 {
		t.Fatal("durable original was deleted")
	}
	read, err := st.ReadThreadHistory(t.Context(), run.ID, domain.HistoryReadRequest{
		SourceID: fmt.Sprintf("message:%d", originalID), Limit: domain.HistoryReadMaxBytes,
	})
	if err != nil || read.Content != longCorrection || !strings.Contains(read.Content, marker) {
		t.Fatalf("scoped history read did not retain the exact original: %v", err)
	}
	t.Logf("reproduced through default Thread: later user source=%d; summaries=%v; auxiliary=%d; primary_requests=%d; constraint in pre-compaction request=true; in fallback/repeated request=true; original_read_exact=true", originalID, summaryIDs, len(generated), len(normal))
}
