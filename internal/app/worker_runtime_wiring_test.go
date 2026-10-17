package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

type cliWorkerRuntimeProvider struct {
	cliAgentBrowserProvider
	expectCommand bool
	payload       json.RawMessage
}

func (p *cliWorkerRuntimeProvider) Chat(_ context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	request.Tools = append([]llm.ToolSpec(nil), request.Tools...)
	request.Messages = cloneCLIAgentBrowserMessages(request.Messages)
	p.requests = append(p.requests, request)
	response := &llm.ChatResponse{Provider: p.Name(), Model: "model", Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}
	if p.expectCommand && len(p.requests) == 1 {
		response.ToolCalls = []llm.ToolCall{{ID: "cli-worker-native", Name: string(toolgateway.CommandRuntimeTool), Arguments: p.payload}}
		return response, nil
	}
	action, _ := json.Marshal(domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionFinish,
		Message: "CLI worker runtime observed.", Report: &domain.CompletionReport{Version: domain.CompletionReportVersion,
			Outcome: domain.CompletionSucceeded, Summary: "Verified the delegated CLI runtime.", WorkItemIDs: []string{}, NoteIDs: []string{}}})
	response.Text = string(action)
	return response, nil
}

func (p *cliWorkerRuntimeProvider) StreamChat(ctx context.Context, request llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	response, err := p.Chat(ctx, request)
	if err != nil {
		return nil, err
	}
	chunks := make(chan llm.ChatChunk, 2)
	if response.Text != "" {
		chunks <- llm.ChatChunk{Text: response.Text}
	}
	chunks <- llm.FinalChatChunk(response)
	close(chunks)
	return chunks, nil
}

func TestCLISpecialistStepWiresRealWorkerRuntimeWithCurrentGates(t *testing.T) {
	testCLISpecialistWorkerRuntime(t, false)
}

func TestCLISpecialistStepPreservesTruncatedCommandResults(t *testing.T) {
	testCLISpecialistWorkerRuntime(t, true)
}

func testCLISpecialistWorkerRuntime(t *testing.T, largeResult bool) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion,
		Action: toolgateway.CommandRuntimeActionRun, FailurePolicy: toolgateway.CommandRuntimeFailFast, MaxBytes: intWorkerRuntime(4096),
		Commands: []runner.CommandRuntimeSpec{{Version: runner.CommandRuntimeProtocolVersion, Profile: runner.CommandRuntimeProcess,
			Executable: node, Arguments: []string{"-e", "console.log('cli-worker-runtime-result')"}, WorkingDirectory: ".",
			Environment: []runner.CommandRuntimeEnvironment{}, StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true,
			TimeoutMilliseconds: 10000, Output: runner.CommandRuntimeOutputPolicy{InlineBytes: 4096, ArtifactBytes: 4096},
			Network: runner.CommandRuntimeNetworkHost, Credentials: runner.CommandRuntimeCredentialsNone, Purpose: "Verify the delegated CLI process runtime"}}})
	if err != nil {
		t.Fatal(err)
	}
	activeCases := []bool{false, true}
	jobCount := 1
	if largeResult {
		activeCases, jobCount = []bool{true}, 4
		var input toolgateway.CommandRuntimeInput
		if err := json.Unmarshal(payload, &input); err != nil {
			t.Fatal(err)
		}
		input.MaxBytes = intWorkerRuntime(32768)
		input.Commands[0].Arguments = []string{"-e", "process.stdout.write('cli-worker-runtime-result'+'x'.repeat(32000))"}
		input.Commands[0].TimeoutMilliseconds = 3000
		input.Commands[0].Output = runner.CommandRuntimeOutputPolicy{InlineBytes: 65536, ArtifactBytes: 65536}
		for len(input.Commands) < jobCount {
			input.Commands = append(input.Commands, input.Commands[0])
		}
		payload, _ = json.Marshal(input)
	}
	for _, active := range activeCases {
		name := "disabled"
		if active {
			name = "confirmed Full"
		}
		t.Run(name, func(t *testing.T) {
			home := newCanonicalCLIHome(t)
			t.Setenv("CYBERAGENT_HOME", home)
			runID := createCLIAgentBrowserRun(t, home, domain.RunExecutionPermissionFull)
			st, err := store.Open(filepath.Join(home, "cyberagent.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if _, err := application.NewRunService(st).Pause(t.Context(), runID); err != nil {
				t.Fatal(err)
			}
			if _, err := application.NewRunExecutionProfileService(st).Change(t.Context(), application.ChangeRunExecutionProfileRequest{
				RunID: runID, Profile: "local", OperationKey: "cli-worker-profile-0001", RequestedBy: "test_operator", Reason: "exercise native CLI execution"}); err != nil {
				t.Fatal(err)
			}
			if _, err := application.NewRunService(st).Start(t.Context(), runID); err != nil {
				t.Fatal(err)
			}
			root, _, err := st.RegisterRootAgent(t.Context(), runID)
			if err != nil {
				t.Fatal(err)
			}
			skill := ""
			for _, value := range root.Skills {
				if domain.DelegableAgentSkill(value) {
					skill = value
					break
				}
			}
			child, _, err := st.AdmitSpecialist(t.Context(), domain.SpecialistAdmission{
				AgentID: "cli-worker-native", SessionID: "cli-worker-session", RunID: runID, ParentAgentID: root.ID,
				Title: "CLI runtime worker", Skills: []string{skill}, Worker: &domain.WorkerProfile{Tools: []string{"command_runtime"}},
				TurnLimit: 1, TokenLimit: 100, ToolCallLimit: 2, MaxChildren: 2, CreatedAt: time.Now().UTC()}, "cli-worker-admission-0001")
			if err != nil {
				t.Fatal(err)
			}
			provider := &cliWorkerRuntimeProvider{expectCommand: active, payload: payload}
			router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
			router.RegisterProvider(provider)
			if largeResult {
				window := llm.DefaultContextWindow()
				window.WindowTokens, window.Source = 256*1024, "worker_result_fixture"
				if err := router.SetContextWindow(llm.ModelRef{Provider: provider.Name(), Model: "model"}, window); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"run", "specialist-step", runID, "--agent", child.ID, "--operation-key", "cli-worker-execution-0001"}
			if active {
				args = append(args, "--enable-permission-control", "--enable-danger-full-access", "--confirm-full")
			}
			var stdout, stderr bytes.Buffer
			code := executeContextWithConfig(t.Context(), args, &stdout, &stderr, func(app *App) { app.router = router })
			if code != 0 || stderr.Len() != 0 {
				t.Fatalf("worker CLI failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
			requests, err := provider.snapshot()
			if err != nil || len(requests) == 0 {
				t.Fatalf("worker model requests=%d err=%v", len(requests), err)
			}
			wantRequests := 1
			// Reading this attempt's existing receipts is context maintenance;
			// command execution still requires the current runtime gates.
			wantTools := []string{domain.SpecialistResultReadName}
			if active {
				wantRequests = 2
				wantTools = append([]string{"command_runtime"}, wantTools...)
			}
			if len(requests) != wantRequests {
				t.Fatalf("worker model requests=%d, want %d", len(requests), wantRequests)
			}
			for index, request := range requests {
				names := make([]string, len(request.Tools))
				for toolIndex, tool := range request.Tools {
					names[toolIndex] = tool.Name
				}
				if !slices.Equal(names, wantTools) {
					t.Fatalf("worker request %d tools=%v, want exactly %v", index, names, wantTools)
				}
			}
			if !active {
				return
			}
			found := false
			for _, message := range requests[1].Messages {
				for _, result := range message.ToolResults {
					found = found || !result.IsError && strings.Contains(result.Content, "cli-worker-runtime-result")
					if largeResult && (!json.Valid([]byte(result.Content)) || len(result.Content) > llm.MaxProviderToolResultSize || !strings.Contains(result.Content, `"truncated":true`)) {
						t.Fatal("large durable result did not produce a bounded provider preview")
					}
				}
			}
			if !found {
				t.Fatalf("native process receipt did not reach worker: %+v", requests[1].Messages)
			}
			if largeResult {
				attempts, err := st.ListAgentAttempts(t.Context(), child.ID)
				if err != nil || len(attempts) != 1 {
					t.Fatalf("expected one worker native attempt: %+v %v", attempts, err)
				}
				rounds, err := st.ListSpecialistToolRounds(t.Context(), domain.AgentAttemptRef{RunID: runID, AgentID: child.ID, AttemptID: attempts[0].ID})
				if err != nil || len(rounds) != 1 || !strings.Contains(rounds[0].Calls[0].ResultJSON, "[truncated by tool gateway]") || !json.Valid([]byte(rounds[0].Calls[0].ResultJSON)) {
					t.Fatalf("gateway-truncated text was discarded from the durable worker receipt: rounds=%d err=%v", len(rounds), err)
				}
			}
			jobs, err := st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: runID})
			if err != nil || len(jobs) != jobCount || jobs[0].SessionID != child.SessionID || jobs[0].RootAgentID != root.ID {
				t.Fatalf("worker native job identity: %+v %v", jobs, err)
			}
			actor, err := st.GetCommandRuntimeJobAttribution(t.Context(), jobs[0].ID)
			if err != nil || actor.AgentID != child.ID || actor.AgentAttemptID == "" || actor.Source != domain.AgentAttributionRecorded {
				t.Fatalf("worker native job actor: %+v %v", actor, err)
			}
		})
	}
}

func intWorkerRuntime(value int) *int { return &value }
