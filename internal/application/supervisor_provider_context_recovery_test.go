package application_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/store"
)

func TestSupervisorRecoversProviderContextLimitWithoutRepeatingTool(t *testing.T) {
	for _, finish := range []llm.FinishReason{llm.FinishReasonStop, llm.FinishReasonLength} {
		t.Run(string(finish), func(t *testing.T) { verifySupervisorProviderContextRecovery(t, finish) })
	}
}

func TestSupervisorContextRecoveryReceiptsPastSmallFirstRound(t *testing.T) {
	st, run, root, request := toolBoundaryFixture(t, domain.Budget{MaxTurns: 4, MaxToolCalls: 4})
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(strings.Repeat("Large local observation remains untrusted evidence. ", 300)), 0600); err != nil {
		t.Fatal(err)
	}
	var before int
	provider := &scriptedToolProvider{respond: func(req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 0:
			return toolResponse("small-first", "work_item_create", `{"title":"Observe local file","priority":"high"}`), nil
		case 1:
			return boundaryRead("large-second", 1), nil
		case 2:
			before, _ = strconv.Atoi(req.Metadata["context_input_estimate"])
			return nil, &llm.ProviderError{Kind: llm.OutcomePermanent, Reason: llm.ProviderFailureContextLimit, Provider: "tool-loop", StatusCode: 400, Message: "input context limit exceeded"}
		case 3:
			after, _ := strconv.Atoi(req.Metadata["context_input_estimate"])
			if after >= before || req.Metadata["context_segment_receipted_rounds"] != "2" {
				t.Errorf("did not skip ineffective first prefix: before=%d after=%d metadata=%v", before, after, req.Metadata)
			}
			return textResponse(rootActionResponse(domain.RootActionFinish, "The file was observed once.", "reply completed", "")), nil
		default:
			return nil, errors.New("unexpected extra receipt recovery request")
		}
	}}
	_, err := toolBoundaryService(st, st, provider).Execute(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.Requests()) != 4 {
		t.Fatalf("requests=%d", len(provider.Requests()))
	}
	rounds, err := st.ListRunSupervisorToolRoundsPage(t.Context(), run.ID, 0, 20)
	if err != nil || len(rounds) != 2 || len(rounds[0].Calls) != 1 || len(rounds[1].Calls) != 1 {
		t.Fatalf("tools repeated: rounds=%+v err=%v", rounds, err)
	}
}

func verifySupervisorProviderContextRecovery(t *testing.T, summaryFinish llm.FinishReason) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "provider-overflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	run := newStartedRunForProvider(t, st, "tool-loop", domain.Budget{MaxTurns: 3, MaxToolCalls: 4})
	for i := 0; i < 6; i++ {
		role := "user"
		if i%2 != 0 {
			role = "assistant"
		}
		if _, err := st.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, role,
			fmt.Sprintf("history-%d: %s", i, strings.Repeat("Historical observation. ", 200)))); err != nil {
			t.Fatal(err)
		}
	}
	var rejectedEstimate int
	provider := &scriptedToolProvider{respond: func(req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 0:
			return toolResponse("single-call", "work_item_create", `{"title":"Read parser","priority":"high"}`), nil
		case 1:
			rejectedEstimate, _ = strconv.Atoi(req.Metadata["context_input_estimate"])
			return nil, &llm.ProviderError{Kind: llm.OutcomePermanent, Reason: llm.ProviderFailureReason("context_limit"), Provider: "tool-loop", StatusCode: 400, Message: "input context limit exceeded"}
		case 2:
			if req.Metadata["purpose"] != "context_compaction" {
				t.Fatal("expected the normal durable summary generation path")
			}
			response := textResponse(`{"version":"generated_handoff.v1","summary":"The current task has one recorded work item. Continue without creating it again."}`)
			response.FinishReason = summaryFinish
			return response, nil
		case 3:
			estimate, _ := strconv.Atoi(req.Metadata["context_input_estimate"])
			if estimate >= rejectedEstimate || req.Metadata["context_summary_id"] == "" || !hasToolResults(req) {
				t.Errorf("recovery did not reduce history and preserve native results: before=%d after=%d summary=%s paired=%v", rejectedEstimate, estimate, req.Metadata["context_summary_id"], hasToolResults(req))
			}
			return textResponse(rootActionResponse(domain.RootActionContinue, "Work item recorded once", "", "")), nil
		default:
			t.Fatalf("unexpected extra request: %d", index)
			return nil, nil
		}
	}}
	result, err := newToolLoopSupervisor(st, provider).Step(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("provider overflow should recover after effective compaction: %v", err)
	}
	items, err := st.ListWorkItems(t.Context(), domain.WorkItemFilter{RunID: run.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.Requests()) != 4 || len(items) != 1 || result.ToolCalls != 1 {
		t.Fatalf("recovery repeated work or lost progress: requests=%d items=%d toolCalls=%d", len(provider.Requests()), len(items), result.ToolCalls)
	}
	if result.Checkpoint.TotalTokens != 12 {
		t.Fatalf("summary usage lost or charged twice: tokens=%d", result.Checkpoint.TotalTokens)
	}
	ledger, err := st.ListRunEvents(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range ledger {
		if summaryFinish == llm.FinishReasonLength && strings.Contains(event.PayloadJSON, `"purpose":"context_compaction"`) && event.Type == "model.completed" {
			t.Fatal("truncated summary was recorded as completed")
		}
	}
}

func TestSupervisorProviderContextRecoveryIsBounded(t *testing.T) {
	for _, test := range []struct {
		name         string
		history      bool
		reason       llm.ProviderFailureReason
		wantRequests int
	}{
		{"unchanged_request", false, llm.ProviderFailureContextLimit, 1},
		{"ordinary_bad_request", true, llm.ProviderFailureProtocolIncompatible, 1},
		{"second_context_limit", true, llm.ProviderFailureContextLimit, 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			st, err := store.Open(filepath.Join(t.TempDir(), "bounded.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			run := newStartedRunForProvider(t, st, "tool-loop", domain.Budget{MaxTurns: 3, MaxToolCalls: 4})
			if test.history {
				for i := 0; i < 6; i++ {
					if _, err := st.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, "user", strings.Repeat("Past work. ", 500))); err != nil {
						t.Fatal(err)
					}
				}
			}
			provider := &scriptedToolProvider{respond: func(req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
				if index >= test.wantRequests {
					return nil, errors.New("unexpected repeated request")
				}
				if req.Metadata["purpose"] == "context_compaction" {
					return textResponse(`{"version":"generated_handoff.v1","summary":"Prior observations remain available through history recall."}`), nil
				}
				return nil, &llm.ProviderError{Kind: llm.OutcomePermanent, Reason: test.reason, Provider: "tool-loop", StatusCode: 400, Message: "request rejected"}
			}}
			_, err = newToolLoopSupervisor(st, provider).Step(t.Context(), run.ID)
			if err == nil {
				t.Fatal("provider failure reported successful")
			}
			requests := provider.Requests()
			if len(requests) != test.wantRequests {
				t.Fatalf("requests=%d want=%d: %v", len(requests), test.wantRequests, err)
			}
			if test.name == "second_context_limit" {
				first, summary, retry := requests[0], requests[1], requests[2]
				before, firstErr := strconv.Atoi(first.Metadata["context_input_estimate"])
				after, retryErr := strconv.Atoi(retry.Metadata["context_input_estimate"])
				if firstErr != nil || retryErr != nil || after <= 0 || after >= before ||
					retry.Metadata["context_input_limit"] != strconv.Itoa(before-1) || retry.Metadata["context_summary_id"] == "" {
					t.Fatalf("recovery did not send a smaller request with the saved summary: before=%d after=%d", before, after)
				}
				if summary.Metadata["purpose"] != llm.ModelPurposeContextCompaction || len(summary.Tools) != 0 {
					t.Fatal("expected one tool-free auxiliary summary between root attempts")
				}
				if retry.Metadata["purpose"] != first.Metadata["purpose"] || retry.Model != first.Model ||
					retry.MaxTokens != first.MaxTokens || len(first.Tools) == 0 || !reflect.DeepEqual(retry.Tools, first.Tools) {
					t.Fatal("the root retry changed its model, output limit or offered tools")
				}
				t.Logf("rejected input %d, recovered input %d, unchanged tool schemas %d", before, after, len(first.Tools))
			}
			items, err := st.ListWorkItems(t.Context(), domain.WorkItemFilter{RunID: run.ID})
			if err != nil || len(items) != 0 {
				t.Fatalf("failure produced work items: %v %v", items, err)
			}
		})
	}
}
