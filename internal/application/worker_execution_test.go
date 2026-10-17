package application_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/tools"
)

type executionWorkerReviewPolicy struct {
	policy.Checker
	review bool
}

func (p executionWorkerReviewPolicy) CheckToolCall(call tools.Call) policy.Decision {
	d := p.Checker.CheckToolCall(call)
	if p.review && call.Name == "create_file" {
		d.Allowed = true
		d.NeedsApproval = true
		d.Reason = "review the exact worker file change"
	}
	return d
}

func TestExecutionWorkerReadsWritesAndResumesExactFileApproval(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionFull} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			input := "worker evidence\n"
			if mode == domain.RunExecutionPermissionAsk {
				input = "worker evidence " + strings.Repeat("中", 10000) + "\n"
			}
			if err := os.WriteFile(filepath.Join(root, "input.txt"), []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			var editID string
			provider := &specialistTestProvider{}
			provider.respond = func(index int, request llm.ChatRequest) (llm.ChatResponse, error) {
				respond := func(name, id string, payload json.RawMessage) (llm.ChatResponse, error) {
					return llm.ChatResponse{ToolCalls: []llm.ToolCall{{ID: id, Name: name, Arguments: payload}}, Usage: llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}, nil
				}
				last := func() string {
					ms := request.Messages
					if len(ms) == 0 || len(ms[len(ms)-1].ToolResults) != 1 {
						t.Fatalf("missing worker receipt: %+v", ms)
					}
					return ms[len(ms)-1].ToolResults[0].Content
				}
				switch index {
				case 0:
					return respond("workspace_read", "read-proof", json.RawMessage(`{"version":"agent-code-tools.v1","path":"input.txt","start_line":1,"end_line":2}`))
				case 1:
					if mode == domain.RunExecutionPermissionAsk && request.Metadata["context_specialist_receipted_rounds"] != "1" {
						t.Fatal("large read was not compacted before proposing edit")
					}
					if mode != domain.RunExecutionPermissionAsk && !strings.Contains(last(), "worker evidence") {
						t.Fatalf("read failed: %s", last())
					}
					return respond("workspace_change", "create-proof", json.RawMessage(`{"version":"agent-code-tools.v1","action":"create","path":"output.txt","expected_sha256":"missing","content":"verified worker output\n"}`))
				case 2:
					var receipt struct {
						Result struct {
							EditID string          `json:"edit_id"`
							Apply  json.RawMessage `json:"apply_arguments"`
						} `json:"result"`
					}
					if err := json.Unmarshal([]byte(last()), &receipt); err != nil || receipt.Result.EditID == "" {
						t.Fatalf("proposal failed: %s, %v", last(), err)
					}
					editID = receipt.Result.EditID
					return respond("workspace_apply", "apply-proof", receipt.Result.Apply)
				case 3:
					if !strings.Contains(last(), `"status":"applied"`) {
						t.Fatalf("apply failed: %s", last())
					}
					if mode == domain.RunExecutionPermissionFull {
						node, err := exec.LookPath("node")
						if err != nil {
							t.Fatal(err)
						}
						payload, _ := json.Marshal(toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion, Action: "run", FailurePolicy: "fail_fast", MaxBytes: ptrWorker(4096), Commands: []runner.CommandRuntimeSpec{{Version: runner.CommandRuntimeProtocolVersion, Profile: runner.CommandRuntimeProcess, Executable: node, Arguments: []string{"-e", "const fs=require('fs'); if(fs.readFileSync('output.txt','utf8')!=='verified worker output\\n')process.exit(1); console.log('worker verification passed')"}, WorkingDirectory: ".", Environment: []runner.CommandRuntimeEnvironment{}, StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true, TimeoutMilliseconds: 10000, Output: runner.CommandRuntimeOutputPolicy{InlineBytes: 4096, ArtifactBytes: 4096}, Network: runner.CommandRuntimeNetworkHost, Credentials: runner.CommandRuntimeCredentialsNone, Purpose: "Verify the worker's edited file"}}})
						return respond("command_runtime", "verify-output", payload)
					}
					fallthrough
				case 4:
					if index == 4 && !strings.Contains(last(), "worker verification passed") {
						t.Fatalf("command verification failed: %s", last())
					}
					return llm.ChatResponse{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionFinish, Message: "Read and applied verified output.", Report: &domain.CompletionReport{Version: domain.CompletionReportVersion, Outcome: domain.CompletionSucceeded, Summary: "Read input.txt and wrote output.txt with a durable receipt.", WorkItemIDs: []string{}, NoteIDs: []string{}}}), Usage: llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}, nil
				default:
					t.Fatalf("unexpected worker model round %d", index)
					return llm.ChatResponse{}, nil
				}
			}
			home := t.TempDir()
			st, run, child, _ := newScopedSubagentFixture(t, provider, domain.Budget{MaxTurns: 10, MaxTokens: 10000, MaxToolCalls: 12}, 4, 1000, false, domain.SpecialistAdmission{Worker: &domain.WorkerProfile{}, ToolCallLimit: 12}, home, root)
			capabilities := domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, DangerFullAccessEnabled: true, RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
			if mode != domain.RunExecutionPermissionAsk {
				if _, err := application.NewRunService(st).Pause(t.Context(), run.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := application.NewRunExecutionProfileService(st).Change(t.Context(), application.ChangeRunExecutionProfileRequest{RunID: run.ID, Profile: "local", OperationKey: "worker-native-profile-0001", RequestedBy: "operator", Reason: "verify native worker commands"}); err != nil {
					t.Fatal(err)
				}
				if _, err := application.NewRunExecutionPermissionService(st, capabilities).Change(t.Context(), application.ChangeRunExecutionPermissionRequest{RunID: run.ID, Mode: string(mode), OperationKey: "worker-full-mode-0001", RequestedBy: "operator", ConfirmFull: true}); err != nil {
					t.Fatal(err)
				}
				if _, err := application.NewRunService(st).Resume(t.Context(), run.ID); err != nil {
					t.Fatal(err)
				}
			}
			router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
			router.RegisterProvider(provider)
			checker := executionWorkerReviewPolicy{Checker: policy.NewDefaultChecker(), review: mode == domain.RunExecutionPermissionAsk}
			runtime := application.NewAgentRunner(st, router, checker).WithExecutionPermissionCapabilities(capabilities)
			if mode == domain.RunExecutionPermissionFull {
				manager, err := runner.NewPlatformCommandRuntimeManager(st, "worker-native-test")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_ = manager.Shutdown(ctx)
				})
				command, err := application.NewCommandRuntimeService(st, manager, capabilities)
				if err != nil {
					t.Fatal(err)
				}
				runtime.WithCommandRuntime(command)
			}
			service := application.NewSpecialistExecutionService(st, router, checker).WithWorkerRuntime(runtime)
			request := application.ExecuteSpecialistRequest{RunID: run.ID, AgentID: child.ID, OperationKey: "worker-read-write-0001"}
			result, err := service.ExecuteSpecialist(t.Context(), request)
			if err != nil {
				t.Fatalf("worker execution: %+v %v", result, err)
			}
			attemptID := result.AttemptID
			if mode == domain.RunExecutionPermissionAsk {
				if !result.WaitingApproval || len(provider.requests) != 3 {
					t.Fatalf("worker did not suspend for file review: %+v", result)
				}
				if _, err := os.Stat(filepath.Join(root, "output.txt")); !os.IsNotExist(err) {
					t.Fatalf("unapproved file was written: %v", err)
				}
				waiting, err := service.ExecuteSpecialist(t.Context(), request)
				if err != nil || !waiting.WaitingApproval || waiting.AttemptID != attemptID || len(provider.requests) != 3 {
					t.Fatalf("pending replay changed intent: %+v %v", waiting, err)
				}
				if _, err := application.NewFileEditReviewService(st).Review(t.Context(), application.ReviewFileEditRequest{Version: application.FileEditReviewProtocolVersion, RunID: run.ID, EditID: editID, Action: application.FileEditApproveIntent}); err != nil {
					t.Fatal(err)
				}
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
				st, err = store.Open(filepath.Join(home, "cyberagent.db"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = st.Close() })
				state, err := st.GetSpecialistContextState(t.Context(), domain.AgentAttemptRef{RunID: run.ID, AgentID: child.ID, AttemptID: attemptID})
				if err != nil || state.ThroughRound != 1 {
					t.Fatalf("approval reopen lost projection: %+v %v", state, err)
				}
				runtime = application.NewAgentRunner(st, router, checker).WithExecutionPermissionCapabilities(capabilities)
				service = application.NewSpecialistExecutionService(st, router, checker).WithWorkerRuntime(runtime)
				result, err = service.ExecuteSpecialist(t.Context(), request)
				if err != nil {
					t.Fatalf("worker resume: %+v %v", result, err)
				}
			}
			calls := 3
			if mode == domain.RunExecutionPermissionFull {
				calls = 4
			}
			if result.WaitingApproval || result.AttemptStatus != domain.AgentAttemptFinished || result.AttemptID != attemptID || result.ToolCalls != calls || result.Usage.TotalTokens != int64(5*(calls+1)) {
				t.Fatalf("worker completion: %+v", result)
			}
			content, err := os.ReadFile(filepath.Join(root, "output.txt"))
			if err != nil || string(content) != "verified worker output\n" {
				t.Fatalf("file result %q %v", content, err)
			}
			rounds, err := st.ListSpecialistToolRounds(context.Background(), domain.AgentAttemptRef{RunID: run.ID, AgentID: child.ID, AttemptID: attemptID})
			if err != nil {
				t.Fatal(err)
			}
			for _, round := range rounds {
				for _, call := range round.Calls {
					if call.Status != domain.SupervisorToolCompleted || call.StartedAt == nil || call.ChargeID == "" || call.AuthorityJSON == "" {
						t.Fatalf("missing worker source receipt: %+v", call)
					}
				}
			}
			replayed, err := service.ExecuteSpecialist(t.Context(), request)
			if err != nil || !replayed.Replayed || len(provider.requests) != calls+1 {
				t.Fatalf("completed replay dispatched: %+v %v", replayed, err)
			}
		})
	}
}

func TestExecutionWorkerNarrowedToolsRejectUnadvertisedWrite(t *testing.T) {
	provider := &specialistTestProvider{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{ID: "forbidden", Name: string(toolgateway.WorkspaceChangeTool), Arguments: json.RawMessage(`{"version":"agent-code-tools.v1","action":"create","path":"forbidden.txt","expected_sha256":"missing","content":"no"}`)}}, Usage: llm.Usage{TotalTokens: 1, InputTokens: 1}},
		{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue, Message: "Write was not delegated."}), Usage: llm.Usage{TotalTokens: 1, InputTokens: 1}},
	}}
	root := t.TempDir()
	st, run, child, _ := newScopedSubagentFixture(t, provider, domain.Budget{MaxTurns: 6, MaxToolCalls: 5}, 3, 1000, false, domain.SpecialistAdmission{Worker: &domain.WorkerProfile{Tools: []string{"workspace_read"}}, ToolCallLimit: 5}, "", root)
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	runtime := application.NewAgentRunner(st, router, policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()})
	service := application.NewSpecialistExecutionService(st, router, policy.NewDefaultChecker()).WithWorkerRuntime(runtime)
	result, err := service.ExecuteSpecialist(t.Context(), application.ExecuteSpecialistRequest{RunID: run.ID, AgentID: child.ID, OperationKey: "narrow-worker-tools-0001"})
	if err == nil || !strings.Contains(err.Error(), "outside its admitted profile") {
		t.Fatalf("narrowed tool was not rejected: %+v %v", result, err)
	}
	if len(provider.requests[0].Tools) != 2 || provider.requests[0].Tools[0].Name != "workspace_read" || provider.requests[0].Tools[1].Name != domain.SpecialistResultReadName {
		t.Fatalf("narrowed advertisement: %+v", provider.requests[0].Tools)
	}
	rounds, err := st.ListSpecialistToolRounds(t.Context(), domain.AgentAttemptRef{RunID: run.ID, AgentID: child.ID, AttemptID: result.AttemptID})
	if err != nil || len(rounds) != 0 {
		t.Fatalf("narrowed tool dispatched: %+v %v", rounds, err)
	}
	if _, err = os.Stat(filepath.Join(root, "forbidden.txt")); !os.IsNotExist(err) {
		t.Fatalf("write happened: %v", err)
	}
}

func ptrWorker[T any](value T) *T { return &value }
