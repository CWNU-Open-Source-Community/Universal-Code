package application_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/session"
)

// Regression for the original 30 KB Chinese read which completed its tool but
// exhausted mandatory context before the second model request.
func TestSpecialistLargeResultRecoversAndReadsScopedPages(t *testing.T) {
	for _, worker := range []bool{false, true} {
		t.Run(fmt.Sprint("worker=", worker), func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "proof.txt"), []byte(strings.Repeat("中", 10000)), 0600); err != nil {
				t.Fatal(err)
			}
			provider := &specialistTestProvider{}
			var digest string
			var checkRead func(string)
			var checkNewAttempt func()
			provider.respond = func(index int, request llm.ChatRequest) (llm.ChatResponse, error) {
				tool := func(name, id string, payload any) (llm.ChatResponse, error) {
					raw, _ := json.Marshal(payload)
					return llm.ChatResponse{ToolCalls: []llm.ToolCall{{ID: id, Name: name, Arguments: raw}}, Usage: llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}, nil
				}
				switch index {
				case 0:
					found := false
					for _, def := range request.Tools {
						if def.Name == domain.SpecialistResultReadName {
							found = true
						}
					}
					if !found {
						t.Fatal("result reader was not advertised")
					}
					return tool("workspace_read", "large-read", json.RawMessage(`{"version":"agent-code-tools.v1","path":"proof.txt","start_line":1,"end_line":1}`))
				case 1:
					if request.Metadata["context_specialist_receipted_rounds"] != "1" {
						t.Fatalf("large result not projected: %+v", request.Metadata)
					}
					for _, m := range request.Messages {
						marker := strings.Index(m.Content, "\n[")
						if marker < 0 {
							continue
						}
						var receipts []struct {
							CallID string `json:"call_id"`
							Digest string `json:"result_sha256"`
						}
						if json.Unmarshal([]byte(m.Content[marker+1:]), &receipts) == nil {
							for _, r := range receipts {
								if r.CallID == "large-read" {
									digest = r.Digest
								}
							}
						}
					}
					if digest == "" {
						t.Fatal("no exact result digest")
					}
					checkRead(digest)
					return tool(domain.SpecialistResultReadName, "read-page", domain.SpecialistResultReadRequest{CallID: "large-read", Part: "result", ExpectedSHA256: digest, Limit: 1024})
				case 2:
					var receipt struct {
						Status string                      `json:"status"`
						Result domain.SpecialistResultPage `json:"result"`
					}
					raw := request.Messages[len(request.Messages)-1].ToolResults[0].Content
					if err := json.Unmarshal([]byte(raw), &receipt); err != nil || receipt.Status != "completed" || !receipt.Result.HasMore || receipt.Result.InstructionAuthorized || !utf8.ValidString(receipt.Result.Content) || !strings.Contains(receipt.Result.Content, "中") {
						t.Fatalf("scoped page unavailable: %s %v", raw, err)
					}
					return llm.ChatResponse{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue, Message: "Read complete"}), Usage: llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}, nil
				case 3:
					if request.Metadata["context_specialist_receipted_rounds"] != "" {
						t.Fatal("previous attempt projection leaked into new attempt")
					}
					checkNewAttempt()
					return llm.ChatResponse{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue, Message: "New attempt uses its own task context"}), Usage: llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}, nil
				default:
					t.Fatalf("unexpected model call %d", index)
					return llm.ChatResponse{}, nil
				}
			}
			limits := domain.SpecialistAdmission{AllowedReadPaths: []string{"proof.txt"}, ToolCallLimit: 12}
			if worker {
				limits.Worker = &domain.WorkerProfile{Tools: []string{"workspace_read"}}
				limits.AllowedReadPaths = nil
			}
			st, run, child, _ := newScopedSubagentFixture(t, provider, domain.Budget{MaxTurns: 10, MaxTokens: 1000000, MaxToolCalls: 12}, 4, 500000, false, limits, "", root)
			checkRead = func(hash string) {
				node, err := st.GetAgentNode(t.Context(), child.ID)
				if err != nil {
					t.Fatal(err)
				}
				ref := domain.AgentAttemptRef{RunID: run.ID, AgentID: child.ID, AttemptID: node.ActiveAttemptID}
				req := domain.SpecialistResultReadRequest{CallID: "large-read", Part: "result", ExpectedSHA256: hash, Limit: 8192}
				var full strings.Builder
				for {
					page, err := st.ReadSpecialistToolResult(t.Context(), ref, req)
					if err != nil {
						t.Fatal(err)
					}
					if page.NextOffset <= req.Offset || !utf8.ValidString(page.Content) || page.InstructionAuthorized {
						t.Fatalf("bad page: %+v", page)
					}
					full.WriteString(page.Content)
					if !page.HasMore {
						break
					}
					req.Offset = page.NextOffset
				}
				if session.ContentSHA256(full.String()) != hash {
					t.Fatal("full pages did not reconstruct the original")
				}
				for _, bad := range []domain.SpecialistResultReadRequest{
					{CallID: "large-read", Part: "result", ExpectedSHA256: strings.Repeat("0", 64), Limit: 4},
					{CallID: "large-read", Part: "result", ExpectedSHA256: hash, Offset: strings.Index(full.String(), "中") + 1, Limit: 4},
					{CallID: "large-read", Part: "result", ExpectedSHA256: hash, Offset: len(full.String()) + 1, Limit: 4},
					{CallID: "large-read", Part: "result", ExpectedSHA256: hash, Limit: 8193},
					{CallID: "other-call", Part: "result", ExpectedSHA256: hash, Limit: 4},
				} {
					if _, err := st.ReadSpecialistToolResult(t.Context(), ref, bad); err == nil {
						t.Fatalf("invalid read allowed: %+v", bad)
					}
				}
				req.Offset = 0
				for _, bad := range []domain.AgentAttemptRef{{RunID: "other-run", AgentID: child.ID, AttemptID: ref.AttemptID}, {RunID: run.ID, AgentID: "other-agent", AttemptID: ref.AttemptID}, {RunID: run.ID, AgentID: child.ID, AttemptID: "other-attempt"}} {
					if _, err := st.ReadSpecialistToolResult(t.Context(), bad, req); err == nil {
						t.Fatalf("cross-scope read: %+v", bad)
					}
				}
			}
			router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
			router.RegisterProvider(provider)
			checker := policy.NewDefaultChecker()
			service := application.NewSpecialistExecutionService(st, router, checker).WithWorkerRuntime(application.NewAgentRunner(st, router, checker))
			result, err := service.ExecuteSpecialist(t.Context(), application.ExecuteSpecialistRequest{RunID: run.ID, AgentID: child.ID, OperationKey: "recover-large-read-0001", WorkspaceTools: !worker})
			if err != nil || len(provider.requests) != 3 {
				t.Fatalf("recovery failed: %+v %v calls=%d", result, err, len(provider.requests))
			}
			ref := domain.AgentAttemptRef{RunID: run.ID, AgentID: child.ID, AttemptID: result.AttemptID}
			rounds, err := st.ListSpecialistToolRounds(t.Context(), ref)
			if err != nil || len(rounds) != 2 || rounds[0].Calls[0].Status != domain.SupervisorToolCompleted || rounds[1].Calls[0].Status != domain.SupervisorToolCompleted {
				t.Fatalf("read repeated or failed: %+v %v", rounds, err)
			}
			if len(rounds[0].Calls[0].ResultJSON) < 30000 || session.ContentSHA256(rounds[0].Calls[0].ResultJSON) != digest {
				t.Fatal("original result changed")
			}
			state, err := st.GetSpecialistContextState(t.Context(), ref)
			if err != nil || state.ThroughRound != 1 {
				t.Fatalf("watermark not saved: %+v %v", state, err)
			}
			if _, err := st.ReadSpecialistToolResult(t.Context(), ref, domain.SpecialistResultReadRequest{CallID: "large-read", Part: "result", ExpectedSHA256: digest, Limit: 1024}); err == nil {
				t.Fatal("completed attempt remained readable by the maintenance executor")
			}
			usage, err := st.GetToolCallUsage(t.Context(), run.ID)
			if err != nil || usage.Consumed != 2 {
				t.Fatalf("maintenance read not charged: %+v %v", usage, err)
			}
			checkNewAttempt = func() {
				node, err := st.GetAgentNode(t.Context(), child.ID)
				if err != nil {
					t.Fatal(err)
				}
				if node.ActiveAttemptID == ref.AttemptID {
					t.Fatal("test did not start a new attempt")
				}
				newRef := domain.AgentAttemptRef{RunID: run.ID, AgentID: child.ID, AttemptID: node.ActiveAttemptID}
				if _, err := st.ReadSpecialistToolResult(t.Context(), newRef, domain.SpecialistResultReadRequest{CallID: "large-read", Part: "result", ExpectedSHA256: digest, Limit: 1024}); err == nil {
					t.Fatal("earlier attempt receipt read escaped scope")
				}
			}
			if _, err := service.ExecuteSpecialist(t.Context(), application.ExecuteSpecialistRequest{RunID: run.ID, AgentID: child.ID, OperationKey: "recover-large-next-0001", WorkspaceTools: !worker}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSpecialistResultReadRechecksRevokedTools(t *testing.T) {
	for _, worker := range []bool{false, true} {
		t.Run(fmt.Sprint("worker=", worker), func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "proof.txt"), []byte("original evidence"), 0600); err != nil {
				t.Fatal(err)
			}
			provider := &specialistTestProvider{}
			var check func()
			provider.respond = func(index int, request llm.ChatRequest) (llm.ChatResponse, error) {
				if index == 0 {
					return llm.ChatResponse{ToolCalls: []llm.ToolCall{{ID: "read-once", Name: "workspace_read", Arguments: json.RawMessage(`{"version":"agent-code-tools.v1","path":"proof.txt","start_line":1,"end_line":1}`)}}, Usage: llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}, nil
				}
				if index != 1 {
					t.Fatalf("unexpected model call %d", index)
				}
				check()
				return llm.ChatResponse{}, &llm.ProviderError{Kind: llm.OutcomePermanent, Reason: llm.ProviderFailureRefusal, Provider: provider.Name(), Message: "test complete after revocation"}
			}
			limits := domain.SpecialistAdmission{AllowedReadPaths: []string{"proof.txt"}, ToolCallLimit: 3}
			if worker {
				limits.Worker = &domain.WorkerProfile{Tools: []string{"workspace_read"}}
				limits.AllowedReadPaths = nil
			}
			st, run, child, _ := newScopedSubagentFixture(t, provider, domain.Budget{MaxTurns: 8, MaxToolCalls: 4}, 3, 1000, false, limits, "", root)
			checked := false
			check = func() {
				node, err := st.GetAgentNode(t.Context(), child.ID)
				if err != nil {
					t.Fatal(err)
				}
				ref := domain.AgentAttemptRef{RunID: run.ID, AgentID: child.ID, AttemptID: node.ActiveAttemptID}
				source, err := st.GetSpecialistToolCall(t.Context(), ref, "read-once")
				if err != nil {
					t.Fatal(err)
				}
				req := domain.SpecialistResultReadRequest{CallID: source.CallID, Part: "result", ExpectedSHA256: session.ContentSHA256(source.ResultJSON), Limit: 1024}
				if _, err := st.ReadSpecialistToolResult(t.Context(), ref, req); err != nil {
					t.Fatal(err)
				}
				if _, err := st.RevokeSpecialistReadTools(t.Context(), run.ID, child.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := st.ReadSpecialistToolResult(t.Context(), ref, req); err == nil {
					t.Fatal("revoked scope still read prior result")
				}
				checked = true
			}
			router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
			router.RegisterProvider(provider)
			checker := policy.NewDefaultChecker()
			service := application.NewSpecialistExecutionService(st, router, checker).WithWorkerRuntime(application.NewAgentRunner(st, router, checker))
			_, err := service.ExecuteSpecialist(t.Context(), application.ExecuteSpecialistRequest{RunID: run.ID, AgentID: child.ID, OperationKey: "revoke-result-read-0001", WorkspaceTools: !worker})
			if err == nil || !checked || len(provider.requests) != 2 {
				t.Fatalf("revocation test did not reach target: checked=%v calls=%d err=%v", checked, len(provider.requests), err)
			}
		})
	}
}

func TestWorkerContextRejectionRetriesOnceWithSmallerInput(t *testing.T) {
	for _, rejectAgain := range []bool{false, true} {
		t.Run(fmt.Sprint("reject_again=", rejectAgain), func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "proof.txt"), []byte(strings.Repeat("a", 15000)), 0600); err != nil {
				t.Fatal(err)
			}
			provider := &specialistTestProvider{}
			rejectedEstimate := 0
			provider.respond = func(index int, request llm.ChatRequest) (llm.ChatResponse, error) {
				switch index {
				case 0:
					return llm.ChatResponse{ToolCalls: []llm.ToolCall{{ID: "read-once", Name: "workspace_read", Arguments: json.RawMessage(`{"version":"agent-code-tools.v1","path":"proof.txt","start_line":1,"end_line":1}`)}}, Usage: llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}, nil
				case 1:
					rejectedEstimate, _ = strconv.Atoi(request.Metadata["context_input_estimate"])
					return llm.ChatResponse{}, &llm.ProviderError{Kind: llm.OutcomePermanent, Reason: llm.ProviderFailureContextLimit, Provider: provider.Name(), Message: "context window exceeded"}
				case 2:
					actual, _ := strconv.Atoi(request.Metadata["context_input_estimate"])
					if actual >= rejectedEstimate || request.Metadata["context_specialist_receipted_rounds"] != "1" {
						t.Fatalf("unchanged context retried: before=%d after=%d", rejectedEstimate, actual)
					}
					if rejectAgain {
						return llm.ChatResponse{}, &llm.ProviderError{Kind: llm.OutcomePermanent, Reason: llm.ProviderFailureContextLimit, Provider: provider.Name(), Message: "context window still exceeded"}
					}
					return llm.ChatResponse{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue, Message: "Recovered"}), Usage: llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}, nil
				default:
					t.Fatalf("unbounded retry: %d", index)
					return llm.ChatResponse{}, nil
				}
			}
			st, run, child, _ := newScopedSubagentFixture(t, provider, domain.Budget{MaxTurns: 10, MaxTokens: 1000000, MaxToolCalls: 12}, 4, 500000, false, domain.SpecialistAdmission{Worker: &domain.WorkerProfile{Tools: []string{"workspace_read"}}, ToolCallLimit: 12}, "", root)
			router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
			router.RegisterProvider(provider)
			checker := policy.NewDefaultChecker()
			runner := application.NewSubagentRunner(st, router, checker).WithWorkerRuntime(application.NewAgentRunner(st, router, checker)).WithModelRetryPolicy(application.ModelRetryPolicy{MaxAttempts: 1})
			result, err := runner.Step(t.Context(), run.ID, child.ID)
			if (err != nil) != rejectAgain || len(provider.requests) != 3 {
				t.Fatalf("rejection recovery: %+v %v calls=%d", result, err, len(provider.requests))
			}
			ref := domain.AgentAttemptRef{RunID: run.ID, AgentID: child.ID, AttemptID: result.AttemptID}
			rounds, readErr := st.ListSpecialistToolRounds(t.Context(), ref)
			if readErr != nil || len(rounds) != 1 {
				t.Fatalf("tool reran: %+v %v", rounds, readErr)
			}
			state, readErr := st.GetSpecialistContextState(t.Context(), ref)
			if readErr != nil || state.RejectedModel != 2 || state.RecoveryInputLimit >= rejectedEstimate {
				t.Fatalf("recovery state: %+v %v", state, readErr)
			}
		})
	}
}

func TestWorkerAccumulatesLargeToolRoundsWithoutReexecution(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "proof.txt"), []byte(strings.Repeat("中", 10000)), 0600); err != nil {
		t.Fatal(err)
	}
	provider := &specialistTestProvider{}
	provider.respond = func(index int, request llm.ChatRequest) (llm.ChatResponse, error) {
		if index < 10 {
			return llm.ChatResponse{ToolCalls: []llm.ToolCall{{ID: fmt.Sprintf("read-%d", index), Name: "workspace_read", Arguments: json.RawMessage(`{"version":"agent-code-tools.v1","path":"proof.txt","start_line":1,"end_line":1}`)}}, Usage: llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}, nil
		}
		if index != 10 {
			t.Fatalf("unexpected model call %d", index)
		}
		return llm.ChatResponse{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue, Message: "Ten independent reads complete"}), Usage: llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}, nil
	}
	st, run, child, _ := newScopedSubagentFixture(t, provider, domain.Budget{MaxTurns: 10, MaxTokens: 1000000, MaxToolCalls: 12}, 4, 500000, false, domain.SpecialistAdmission{Worker: &domain.WorkerProfile{Tools: []string{"workspace_read"}}, ToolCallLimit: 12}, "", root)
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	checker := policy.NewDefaultChecker()
	service := application.NewSpecialistExecutionService(st, router, checker).WithWorkerRuntime(application.NewAgentRunner(st, router, checker))
	result, err := service.ExecuteSpecialist(t.Context(), application.ExecuteSpecialistRequest{RunID: run.ID, AgentID: child.ID, OperationKey: "recover-many-reads-0001"})
	if err != nil || len(provider.requests) != 11 {
		t.Fatalf("cumulative context: %+v %v calls=%d", result, err, len(provider.requests))
	}
	ref := domain.AgentAttemptRef{RunID: run.ID, AgentID: child.ID, AttemptID: result.AttemptID}
	rounds, err := st.ListSpecialistToolRounds(t.Context(), ref)
	if err != nil || len(rounds) != 10 {
		t.Fatalf("source rounds: %d %v", len(rounds), err)
	}
	for i, r := range rounds {
		if len(r.Calls) != 1 || r.Calls[0].CallID != fmt.Sprintf("read-%d", i) || r.Calls[0].Status != domain.SupervisorToolCompleted || len(r.Calls[0].ResultJSON) < 30000 {
			t.Fatalf("round changed: %+v", r)
		}
	}
	state, err := st.GetSpecialistContextState(t.Context(), ref)
	if err != nil || state.ThroughRound != 10 {
		t.Fatalf("cumulative watermark: %+v %v", state, err)
	}
	if result.ToolCalls != 10 || result.Usage.TotalTokens != 55 {
		t.Fatalf("accounting changed: %+v", result)
	}
}
