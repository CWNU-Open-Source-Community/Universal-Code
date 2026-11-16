package application_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/session"
)

func TestThreadShortExchangesKeepOriginalHistory(t *testing.T) {
	st := openHistoryRecallStore(t, filepath.Join(t.TempDir(), "short.db"))
	defer st.Close()
	_, run := createHistoryRecallRun(t, st, "short-inputs", "")
	p := newGeneratedProtocolProvider()
	turns, _ := generatedThreadServices(st, p, nil)
	for i := 1; i <= 22; i++ {
		submitHistoryRecall(t, turns, run.ID, fmt.Sprintf("short-history-input-%d", i), fmt.Sprintf("记录第 %d 项。", i))
	}
	full, err := st.ListSessionMessages(t.Context(), run.SessionID, false)
	if err != nil || len(full) != 44 || p.generatedCount() != 0 {
		t.Fatalf("active messages=%d, summary calls=%d, err=%v", len(full), p.generatedCount(), err)
	}
	_, calls := p.splitRequests()
	if !strings.Contains(generatedRequestText(calls[len(calls)-1].Request), "记录第 1 项。") {
		t.Fatal("last normal request omitted the first user message")
	}
}

func TestThreadAutomaticCompactionUsesSelectedModelWindow(t *testing.T) {
	const original = "原方案是用英文写交付报告。"
	const correction = "更正：交付报告必须用中文，保留 LegacyLookup 的拼写。"
	const pending = "418 兼容性核验仍未完成。"
	inputs := []string{original, correction, pending, "继续。"}
	observation := strings.Repeat("已有观测。", 295)
	var estimates []int
	for _, windowSize := range []int{128 * 1024, 32 * 1024} {
		st := openHistoryRecallStore(t, filepath.Join(t.TempDir(), "context.db"))
		defer st.Close()
		_, run := createHistoryRecallRun(t, st, "window-comparison", "")
		p := newGeneratedProtocolProvider()
		p.normal = func(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
			return textResponse(rootActionResponse(domain.RootActionFinish, observation, "Recorded", "")), nil
		}
		// Deliberately omit the operator's correction from the generated text.
		p.auxiliary = func(context.Context, llm.ChatRequest, int) (*llm.ChatResponse, error) {
			return textResponse(`{"version":"generated_handoff.v1","summary":"已收集背景资料。"}`), nil
		}
		r := generatedFixtureRouter(p)
		w := llm.DefaultContextWindow()
		w.WindowTokens = windowSize
		if err := r.SetContextWindow(llm.ModelRef{Provider: p.Name(), Model: "model"}, w); err != nil {
			t.Fatal(err)
		}
		turns := application.NewThreadTurnService(st, application.NewRunLifecycleControlService(st),
			application.NewRunExecutionHandoffService(st, r, policy.NewDefaultChecker()))
		for i, input := range inputs {
			submitHistoryRecall(t, turns, run.ID, fmt.Sprintf("window-comparison-input-%d", i), input)
		}
		generated, normal := p.splitRequests()
		last := normal[len(normal)-1].Request
		if windowSize == 128*1024 {
			if len(generated) != 0 {
				t.Fatal("large window compacted the same four exchanges")
			}
			for _, call := range normal {
				n, err := strconv.Atoi(call.Request.Metadata["context_input_estimate"])
				if err != nil {
					t.Fatal(err)
				}
				estimates = append(estimates, n)
			}
			continue
		}
		limit, err := strconv.Atoi(last.Metadata["context_input_limit"])
		if err != nil || len(estimates) != len(inputs) || estimates[2] >= limit-limit/10 ||
			estimates[3] < limit-limit/10 || estimates[3] >= limit {
			t.Fatalf("fixture must cross the proactive threshold before overflow: estimates=%v limit=%d", estimates, limit)
		}
		if len(generated) != 1 || last.Metadata["context_summary_id"] == "" || last.Metadata["context_history_omitted"] != "0" {
			t.Fatal("small window did not send one durably compacted request")
		}
		body := generatedRequestText(last)
		if !strings.Contains(body, correction) || !strings.Contains(body, pending) {
			t.Fatal("next model request lost a recent correction or unfinished work")
		}
		rows, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
		if err != nil || len(rows) == 0 || rows[0].Content != original || !rows[0].Compacted {
			t.Fatalf("original history missing after automatic compaction: %v", err)
		}
		read, err := st.ReadThreadHistory(t.Context(), run.ID, domain.HistoryReadRequest{SourceID: fmt.Sprintf("message:%d", rows[0].ID), Limit: 1024})
		if err != nil || read.Content != original || rows[0].Provenance.ContentSHA256 != session.ContentSHA256(read.Content) {
			t.Fatalf("exact archived source could not be read: %v", err)
		}
	}
}
