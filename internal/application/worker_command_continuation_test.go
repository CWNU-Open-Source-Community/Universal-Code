package application_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
)

func TestExecutionWorkerCommandSurvivesContinueAndStopsOnFinish(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	provider := &specialistTestProvider{}
	st, run, child, _ := newScopedSubagentFixture(t, provider, domain.Budget{MaxTurns: 12, MaxTokens: 10000, MaxToolCalls: 12}, 4, 1000, false, domain.SpecialistAdmission{Worker: &domain.WorkerProfile{}, ToolCallLimit: 12}, "", t.TempDir())
	caps := workerIsolationFullMode(t, st, run.ID)
	runs := application.NewRunService(st)
	if _, err = runs.Pause(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = application.NewRunExecutionProfileService(st).Change(t.Context(), application.ChangeRunExecutionProfileRequest{RunID: run.ID, Profile: "local", OperationKey: "resource-local-profile", RequestedBy: "operator", Reason: "verify worker process continuation"}); err != nil {
		t.Fatal(err)
	}
	if _, err = runs.Resume(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	manager, err := runner.NewPlatformCommandRuntimeManager(st, "resource-production-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})
	commands, err := application.NewCommandRuntimeService(st, manager, caps)
	if err != nil {
		t.Fatal(err)
	}
	var jobID string
	provider.respond = func(index int, request llm.ChatRequest) (llm.ChatResponse, error) {
		tool := func(input toolgateway.CommandRuntimeInput) (llm.ChatResponse, error) {
			if err := input.Validate(); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(input)
			return llm.ChatResponse{ToolCalls: []llm.ToolCall{{ID: "command", Name: "command_runtime", Arguments: raw}}, Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil
		}
		last := func() string {
			ms := request.Messages
			if len(ms) == 0 || len(ms[len(ms)-1].ToolResults) != 1 {
				t.Fatal("missing command receipt")
			}
			content := ms[len(ms)-1].ToolResults[0].Content
			if !strings.Contains(content, `"status":"completed"`) {
				t.Fatalf("command failed: %s", content)
			}
			return content
		}
		switch index {
		case 0:
			return tool(toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion, Action: "start", Commands: []runner.CommandRuntimeSpec{{Version: runner.CommandRuntimeProtocolVersion, Profile: runner.CommandRuntimeProcess, Executable: node, Arguments: []string{"-e", "console.log('retained process');setInterval(()=>{},1000)"}, WorkingDirectory: ".", Environment: []runner.CommandRuntimeEnvironment{}, StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true, TimeoutMilliseconds: 60000, Output: runner.CommandRuntimeOutputPolicy{InlineBytes: 4096, ArtifactBytes: 4096}, Network: runner.CommandRuntimeNetworkHost, Credentials: runner.CommandRuntimeCredentialsNone, Purpose: "retain the worker service across continue"}}})
		case 1:
			var receipt struct {
				Result struct {
					Jobs []struct {
						ID string `json:"id"`
					} `json:"jobs"`
				} `json:"result"`
			}
			content := last()
			if err := json.Unmarshal([]byte(content), &receipt); err != nil || len(receipt.Result.Jobs) != 1 || receipt.Result.Jobs[0].ID == "" {
				t.Fatalf("job missing: %s %v", content, err)
			}
			jobID = receipt.Result.Jobs[0].ID
			return llm.ChatResponse{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue, Message: "Continue with the running service."}), Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil
		case 2:
			cursor := uint64(0)
			return tool(toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion, Action: "read", JobID: jobID, Cursor: &cursor, MaxBytes: ptrWorker(4096), WaitMilliseconds: ptrWorker(0)})
		case 3:
			if !strings.Contains(last(), "retained process") {
				t.Fatal("continued worker could not read its process")
			}
			return workerIsolationFinish(t), nil
		default:
			t.Fatalf("unexpected command model call %d", index)
			return llm.ChatResponse{}, nil
		}
	}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	host := application.NewAgentRunner(st, router, policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(caps).WithCommandRuntime(commands)
	service := application.NewSpecialistExecutionService(st, router, policy.NewDefaultChecker()).WithWorkerRuntime(host)
	var origin string
	for index, status := range []domain.AgentAttemptStatus{domain.AgentAttemptContinued, domain.AgentAttemptFinished} {
		result, err := service.ExecuteSpecialist(t.Context(), application.ExecuteSpecialistRequest{RunID: run.ID, AgentID: child.ID, OperationKey: fmt.Sprintf("resource-command-turn-%d", index)})
		if err != nil || result.AttemptStatus != status {
			t.Fatalf("command turn %d: %+v %v", index, result, err)
		}
		if index == 0 {
			origin = result.AttemptID
			job, err := st.GetCommandRuntimeJob(t.Context(), jobID)
			if err != nil || job.State.Terminal() || !manager.OwnsActiveJob(job) {
				t.Fatalf("continue lost native process: %+v %v", job, err)
			}
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		job, err := st.GetCommandRuntimeJob(t.Context(), jobID)
		if err != nil {
			t.Fatal(err)
		}
		if job.State.Terminal() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("finish left worker process alive")
		}
		time.Sleep(20 * time.Millisecond)
	}
	attribution, err := st.GetCommandRuntimeJobAttribution(t.Context(), jobID)
	if err != nil || attribution.AgentID != child.ID || attribution.AgentAttemptID != origin {
		t.Fatalf("job creation attribution changed: %+v %v", attribution, err)
	}
}
