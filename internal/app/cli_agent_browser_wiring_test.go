package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

// Workspace authority compares the supplied root with its resolved identity.
// Use one canonical path for the CLI home, database and workspace fixture.
func newCanonicalCLIHome(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return home
}

type cliAgentBrowserProvider struct {
	mu            sync.Mutex
	expectBrowser bool
	requests      []llm.ChatRequest
	unexpected    error
}

func (*cliAgentBrowserProvider) Name() string { return "cli-agent-browser-test" }

func (*cliAgentBrowserProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "model", Provider: "cli-agent-browser-test",
		Capabilities: []string{"chat", "tools"}}}, nil
}

func (p *cliAgentBrowserProvider) Chat(_ context.Context,
	request llm.ChatRequest,
) (*llm.ChatResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	request.Tools = append([]llm.ToolSpec(nil), request.Tools...)
	request.Messages = cloneCLIAgentBrowserMessages(request.Messages)
	p.requests = append(p.requests, request)
	call := len(p.requests)
	if p.expectBrowser && call == 1 {
		return &llm.ChatResponse{Provider: p.Name(), Model: "model",
			Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
			ToolCalls: []llm.ToolCall{{ID: "cli-browser-status-1",
				Name:      string(toolgateway.BrowserStatusTool),
				Arguments: json.RawMessage(`{"version":"browser_status.v2"}`)}}}, nil
	}
	if p.expectBrowser && call > 2 {
		p.unexpected = fmt.Errorf("received %d model requests, want two", call)
		return nil, p.unexpected
	}
	return &llm.ChatResponse{Provider: p.Name(), Model: "model",
		Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
		Text:  `{"version":"root_lifecycle.v1","action":"wait","message":"CLI browser wiring observed","reason":"operator turn boundary"}`}, nil
}

func (p *cliAgentBrowserProvider) StreamChat(ctx context.Context,
	request llm.ChatRequest,
) (<-chan llm.ChatChunk, error) {
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

func (*cliAgentBrowserProvider) SupportsTools(string) bool    { return true }
func (*cliAgentBrowserProvider) SupportsVision(string) bool   { return false }
func (*cliAgentBrowserProvider) SupportsJSONMode(string) bool { return true }

func (p *cliAgentBrowserProvider) snapshot() ([]llm.ChatRequest, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.ChatRequest(nil), p.requests...), p.unexpected
}

func cloneCLIAgentBrowserMessages(messages []llm.Message) []llm.Message {
	cloned := append([]llm.Message(nil), messages...)
	for index := range cloned {
		cloned[index].ToolCalls = append([]llm.ToolCall(nil), cloned[index].ToolCalls...)
		cloned[index].ToolResults = append([]llm.ToolResult(nil), cloned[index].ToolResults...)
	}
	return cloned
}

func TestCLIExecutionEntrypointsWireOrdinaryAgentBrowser(t *testing.T) {
	t.Setenv("MIMO_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("CYBERAGENT_ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	tests := []struct {
		name          string
		command       string
		permission    domain.RunExecutionPermissionMode
		flags         []string
		browserWanted bool
		denied        bool
	}{
		{name: "step confirmed Full", command: "step", permission: domain.RunExecutionPermissionFull,
			flags: []string{"--enable-permission-control", "--enable-danger-full-access", "--confirm-full"}, browserWanted: true},
		{name: "execute confirmed Full", command: "execute", permission: domain.RunExecutionPermissionFull,
			flags: []string{"--max-steps", "1", "--enable-permission-control", "--enable-danger-full-access", "--confirm-full"}, browserWanted: true},
		{name: "step without process gates", command: "step", permission: domain.RunExecutionPermissionFull},
		{name: "step cold Full", command: "step", permission: domain.RunExecutionPermissionFull,
			flags: []string{"--enable-permission-control", "--enable-danger-full-access"}},
		{name: "execute cold Full", command: "execute", permission: domain.RunExecutionPermissionFull,
			flags: []string{"--enable-permission-control", "--enable-danger-full-access"}},
		{name: "step Ask", command: "step", permission: domain.RunExecutionPermissionAsk,
			flags: []string{"--enable-permission-control", "--enable-danger-full-access"}},
		{name: "step Auto", command: "step", permission: domain.RunExecutionPermissionAuto,
			flags: []string{"--enable-permission-control", "--enable-danger-full-access"}},
		{name: "confirm without process gates", command: "step", permission: domain.RunExecutionPermissionFull,
			flags: []string{"--confirm-full"}, denied: true},
		{name: "confirm without Full preference", command: "execute", permission: domain.RunExecutionPermissionAsk,
			flags: []string{"--enable-permission-control", "--enable-danger-full-access", "--confirm-full"}, denied: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := newCanonicalCLIHome(t)
			t.Setenv("CYBERAGENT_HOME", home)
			runID := createCLIAgentBrowserRun(t, home, test.permission)
			wantBrowser := test.browserWanted && browserruntime.AgentBrowserPlatformAvailable()
			provider := &cliAgentBrowserProvider{expectBrowser: wantBrowser}
			router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
			router.RegisterProvider(provider)
			args := append([]string{"run", test.command, runID}, test.flags...)
			var stdout, stderr bytes.Buffer
			code := executeContextWithConfig(t.Context(), args, &stdout, &stderr, func(app *App) {
				app.router = router
			})
			if test.denied {
				requests, _ := provider.snapshot()
				if code != 5 || len(requests) != 0 {
					t.Fatalf("unconfirmed CLI dispatch: code=%d requests=%d stderr=%s", code, len(requests), stderr.String())
				}
				assertCLIAgentBrowserDurableResult(t, home, runID, false)
				return
			}
			if code != 0 || stderr.Len() != 0 {
				t.Fatalf("CLI %s failed: code=%d stdout=%s stderr=%s", test.command,
					code, stdout.String(), stderr.String())
			}
			requests, providerErr := provider.snapshot()
			if providerErr != nil {
				t.Fatal(providerErr)
			}
			assertCLIAgentBrowserRequests(t, requests, wantBrowser)
			assertCLIAgentBrowserDurableResult(t, home, runID, wantBrowser)
		})
	}
}

func TestCLIWakeConsumeWiresOrdinaryAgentBrowser(t *testing.T) {
	if !browserruntime.AgentBrowserPlatformAvailable() {
		t.Skip("ordinary Agent browser adapter is unavailable")
	}
	for _, confirmFull := range []bool{false, true} {
		t.Run(fmt.Sprintf("confirm-full=%t", confirmFull), func(t *testing.T) {
			home := newCanonicalCLIHome(t)
			t.Setenv("CYBERAGENT_HOME", home)
			runID := createCLIAgentBrowserRun(t, home, domain.RunExecutionPermissionFull)
			state, err := store.Open(filepath.Join(home, "cyberagent.db"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := application.NewThreadService(state).Submit(t.Context(),
				application.SubmitThreadMessageRequest{
					Version: domain.ThreadMessageProtocolVersion, ThreadID: domain.InitialThreadID(runID),
					Content:      "consume this pending CLI wake through the shared runtime",
					OperationKey: "cli-agent-browser-wake-message", RequestedBy: "test_operator",
				}); err != nil {
				_ = state.Close()
				t.Fatal(err)
			}
			if _, err := application.NewRunWakeControlService(state).Schedule(t.Context(),
				application.ScheduleRunWakeRequest{
					Version: domain.RunWakeControlProtocolVersion, RunID: runID,
					OperationKey: "cli-agent-browser-wake-schedule", RequestedBy: "test_operator",
					MaxAttempts: 1, BaseBackoffSeconds: 5, MaxBackoffSeconds: 5,
					MaxElapsedSeconds: 60,
				}); err != nil {
				_ = state.Close()
				t.Fatal(err)
			}
			provider := &cliAgentBrowserProvider{expectBrowser: confirmFull}
			router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
			router.RegisterProvider(provider)
			var stdout, stderr bytes.Buffer
			app := &App{home: home, store: state, router: router, checker: policy.NewDefaultChecker(),
				calls: application.NewActiveCallRegistry(), out: &stdout, errOut: &stderr}
			args := []string{"consume", runID, "--max-steps", "1",
				"--enable-permission-control", "--enable-danger-full-access"}
			if confirmFull {
				args = append(args, "--confirm-full")
			}
			err = app.runWake(t.Context(), args)
			if err != nil || stderr.Len() != 0 ||
				!strings.Contains(stdout.String(), "consumption_status: completed") {
				_ = state.Close()
				t.Fatalf("CLI wake consume failed: stdout=%s stderr=%s err=%v",
					stdout.String(), stderr.String(), err)
			}
			if err := state.Close(); err != nil {
				t.Fatal(err)
			}
			requests, providerErr := provider.snapshot()
			if providerErr != nil {
				t.Fatal(providerErr)
			}
			assertCLIAgentBrowserRequests(t, requests, confirmFull)
			assertCLIAgentBrowserDurableResult(t, home, runID, confirmFull)
		})
	}
}

func TestCLIApprovalExecutionHandoffWiresOrdinaryAgentBrowser(t *testing.T) {
	if !browserruntime.AgentBrowserPlatformAvailable() {
		t.Skip("ordinary Agent browser adapter is unavailable")
	}
	for _, test := range []struct {
		name                       string
		permission                 domain.RunExecutionPermissionMode
		permissionGate, fullGate   bool
		confirmFull, browserWanted bool
		wantError                  string
	}{
		{name: "confirmed Full", permission: domain.RunExecutionPermissionFull,
			permissionGate: true, fullGate: true, confirmFull: true, browserWanted: true},
		{name: "cold Full", permission: domain.RunExecutionPermissionFull,
			permissionGate: true, fullGate: true},
		{name: "no gates", permission: domain.RunExecutionPermissionFull},
		{name: "missing Full gate", permission: domain.RunExecutionPermissionFull,
			permissionGate: true, confirmFull: true, wantError: "requires --enable-permission-control"},
		{name: "missing permission gate", permission: domain.RunExecutionPermissionFull,
			fullGate: true, confirmFull: true, wantError: "invalid CLI runtime capabilities"},
		{name: "Ask cannot confirm Full", permission: domain.RunExecutionPermissionAsk,
			permissionGate: true, fullGate: true, confirmFull: true, wantError: "current Full preference"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := newCanonicalCLIHome(t)
			runID := createCLIAgentBrowserRun(t, home, test.permission)
			state, err := store.Open(filepath.Join(home, "cyberagent.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer state.Close()
			permission, err := state.GetRunExecutionPermission(t.Context(), runID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := application.NewThreadService(state).Submit(t.Context(),
				application.SubmitThreadMessageRequest{
					Version: domain.ThreadMessageProtocolVersion, ThreadID: domain.InitialThreadID(runID),
					Content:      "continue this reviewed turn through the CLI approval handoff",
					OperationKey: "cli-agent-browser-approval-message", RequestedBy: "test_operator",
				}); err != nil {
				t.Fatal(err)
			}
			provider := &cliAgentBrowserProvider{expectBrowser: test.browserWanted}
			router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
			router.RegisterProvider(provider)
			app := &App{home: home, store: state, router: router, checker: policy.NewDefaultChecker(),
				calls: application.NewActiveCallRegistry()}
			capabilities := cliExecutionPermissionCapabilities(test.permissionGate, test.fullGate)
			runtimeCtx, cancelRuntime := context.WithCancel(t.Context())
			defer cancelRuntime()
			handoff, closeRuntime, err := app.newCLIApprovalExecution(runtimeCtx, runID, capabilities, test.confirmFull)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) || handoff != nil || closeRuntime != nil {
					t.Fatalf("invalid confirmation created a runtime: %v", err)
				}
				if _, active := capabilities.RuntimeAuthority.AllowsFullAccess(permission); active {
					t.Fatal("rejected continuation retained Full authority")
				}
				requests, _ := provider.snapshot()
				if len(requests) != 0 {
					t.Fatal("rejected continuation called the provider")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := closeRuntime(); err != nil {
					t.Errorf("close approval runtime: %v", err)
				}
			}()
			if _, active := capabilities.RuntimeAuthority.AllowsFullAccess(permission); active != test.browserWanted {
				t.Fatalf("approval runtime Full authority=%t want=%t", active, test.browserWanted)
			}
			result, err := handoff.Execute(t.Context(), application.ExecuteRunHandoffRequest{
				Version: domain.RunExecutionHandoffProtocolVersion, RunID: runID, MaxSteps: 1,
				OperationKey: "cli-agent-browser-approval-handoff", RequestedBy: "cli_approval",
			})
			if err != nil || result.Handoff.Result == nil ||
				result.Handoff.Result.Status != domain.RunExecutionHandoffCompleted {
				t.Fatalf("approval execution handoff=%#v err=%v", result, err)
			}
			requests, providerErr := provider.snapshot()
			if providerErr != nil {
				t.Fatal(providerErr)
			}
			assertCLIAgentBrowserRequests(t, requests, test.browserWanted)
			assertCLIAgentBrowserDurableResult(t, home, runID, test.browserWanted)
			cancelRuntime()
			if err := closeRuntime(); err != nil {
				t.Fatal(err)
			}
			if _, active := capabilities.RuntimeAuthority.AllowsFullAccess(permission); active {
				t.Fatal("closed continuation retained Full authority")
			}
			persisted, err := state.GetRunExecutionPermission(t.Context(), runID)
			if err != nil || !reflect.DeepEqual(persisted, permission) {
				t.Fatalf("continuation changed durable permission: %+v err=%v", persisted, err)
			}
		})
	}
}

func createCLIAgentBrowserRun(t *testing.T, home string,
	permission domain.RunExecutionPermissionMode,
) string {
	t.Helper()
	state, err := store.Open(filepath.Join(home, "cyberagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	workspace := store.WorkspaceRecord{ID: "cli-agent-browser-workspace", Name: "cli-agent-browser",
		RootPath: home}
	if err := state.SaveWorkspace(t.Context(), workspace); err != nil {
		t.Fatal(err)
	}
	_, run, err := application.NewRunService(state).Create(t.Context(), application.CreateRunRequest{
		Goal: "verify CLI Agent browser runtime wiring", Profile: "code", Surface: "code",
		Phase: "deliver", WorkspaceID: workspace.ID, ModelRoute: "cli-agent-browser-test/model",
		Interactive: true, Budget: domain.Budget{MaxTurns: 4, MaxToolCalls: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	if permission != domain.RunExecutionPermissionAsk {
		request := application.ChangeRunExecutionPermissionRequest{
			RunID: run.ID, Mode: string(permission), OperationKey: "cli-agent-browser-permission",
			RequestedBy: "test_operator", Reason: "exercise exact CLI runtime assembly",
			ConfirmFull: permission == domain.RunExecutionPermissionFull,
		}
		if _, err := application.NewRunExecutionPermissionService(state,
			cliExecutionPermissionCapabilities(true, true)).Change(t.Context(), request); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := application.NewRunService(state).Start(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	return run.ID
}

func assertCLIAgentBrowserRequests(t *testing.T, requests []llm.ChatRequest, wantBrowser bool) {
	t.Helper()
	wantCount := 1
	if wantBrowser {
		wantCount = 2
	}
	if len(requests) != wantCount {
		t.Fatalf("model request count=%d want=%d", len(requests), wantCount)
	}
	for requestIndex, request := range requests {
		browserTools := make(map[string]llm.ToolSpec)
		for _, tool := range request.Tools {
			if toolgateway.IsBrowserActionTool(toolgateway.ToolName(tool.Name)) {
				browserTools[tool.Name] = tool
			}
		}
		if !wantBrowser {
			if len(browserTools) != 0 {
				t.Fatalf("request %d advertised browser tools without exact host and Run gates: %v",
					requestIndex+1, sortedCLIAgentBrowserToolNames(browserTools))
			}
			continue
		}
		for _, name := range []toolgateway.ToolName{
			toolgateway.BrowserStatusTool, toolgateway.BrowserNavigateTool,
			toolgateway.BrowserScrollTool, toolgateway.BrowserKeyTool,
		} {
			tool, found := browserTools[string(name)]
			if !found {
				t.Fatalf("request %d omitted %s: %v", requestIndex+1, name,
					sortedCLIAgentBrowserToolNames(browserTools))
			}
			expected, ok := toolgateway.AgentBrowserToolDefinition(name)
			if !ok || !jsonEqualCLIAgentBrowser(tool.Parameters, expected.InputSchema) {
				t.Fatalf("request %d %s schema is not its Agent browser v2 contract: got=%s want=%s",
					requestIndex+1, name, tool.Parameters, expected.InputSchema)
			}
		}
	}
	if !wantBrowser {
		return
	}
	foundResult := false
	for _, message := range requests[1].Messages {
		for _, result := range message.ToolResults {
			if strings.Contains(result.Content, `"tool":"browser_status"`) {
				foundResult = true
				if result.IsError || !strings.Contains(result.Content, `agent_browser_status.v1`) ||
					!strings.Contains(result.Content, `state\":\"idle`) {
					t.Fatalf("second model request did not receive successful browser status: %#v", result)
				}
			}
		}
	}
	if !foundResult {
		t.Fatalf("second model request omitted the durable browser_status tool result: %#v",
			requests[1].Messages)
	}
}

func assertCLIAgentBrowserDurableResult(t *testing.T, home, runID string, wantBrowser bool) {
	t.Helper()
	state, err := store.Open(filepath.Join(home, "cyberagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	rounds, err := state.ListRunSupervisorToolRoundsPage(t.Context(), runID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !wantBrowser {
		for _, round := range rounds {
			for _, call := range round.Calls {
				if toolgateway.IsBrowserActionTool(toolgateway.ToolName(call.ToolName)) {
					t.Fatalf("browser call was persisted without exact host and Run gates: %#v", call)
				}
			}
		}
		return
	}
	if len(rounds) != 1 || len(rounds[0].Calls) != 1 {
		t.Fatalf("durable browser rounds=%#v", rounds)
	}
	call := rounds[0].Calls[0]
	if call.ToolName != string(toolgateway.BrowserStatusTool) ||
		call.Status != domain.SupervisorToolCompleted || call.CompletedAt == nil ||
		call.PayloadJSON != `{"version":"browser_status.v2"}` || call.ResultJSON == "" {
		t.Fatalf("browser_status did not complete durably: %#v", call)
	}
}

func TestCLIExecutionRuntimeClosesAgentBrowserWithoutGrantingPersistedFullAccess(t *testing.T) {
	if !browserruntime.AgentBrowserPlatformAvailable() {
		t.Skip("ordinary Agent browser adapter is unavailable")
	}
	home := newCanonicalCLIHome(t)
	runID := createCLIAgentBrowserRun(t, home, domain.RunExecutionPermissionFull)
	state, err := store.Open(filepath.Join(home, "cyberagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	permission, err := state.GetRunExecutionPermission(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	capabilities := cliExecutionPermissionCapabilities(true, true)
	if _, granted := capabilities.RuntimeAuthority.AllowsFullAccess(permission); granted {
		t.Fatal("CLI capability construction silently activated persisted Full Access")
	}
	app := &App{home: home, store: state}
	runtimeContext, cancelRuntime := context.WithCancel(t.Context())
	owned, err := app.newCLIExecutionRuntime(runtimeContext, capabilities, false)
	if err != nil {
		t.Fatal(err)
	}
	cold, err := owned.browser.GetStatus(t.Context(), runID)
	if err != nil || cold.Capabilities.Available || cold.SessionID != "" || cold.State != "unavailable" {
		t.Fatalf("cold Full gained browser authority: status=%#v err=%v", cold, err)
	}
	releaseFull, err := app.activateCLIInvocationFull(t.Context(), runID, capabilities, true)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseFull()
	before, err := owned.browser.GetStatus(t.Context(), runID)
	if err != nil || !before.Capabilities.Available || before.SessionID == "" || before.State != "idle" {
		t.Fatalf("owned browser before close=%#v err=%v", before, err)
	}
	cancelRuntime()
	closed := make(chan [2]error, 1)
	go func() {
		closed <- [2]error{owned.close(), owned.close()}
	}()
	select {
	case closeErrors := <-closed:
		if closeErrors[0] != nil || closeErrors[1] != nil {
			t.Fatalf("repeated runtime close errors=%v", closeErrors)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("repeated runtime close hung after its owner context was cancelled")
	}
	after, err := owned.browser.GetStatus(t.Context(), runID)
	if err != nil || after.Capabilities.Available || after.State != "closed" ||
		after.Cleanup == nil || !after.Cleanup.ProcessesStopped() || !after.Cleanup.ProfileRemoved ||
		after.Cleanup.CleanupPending {
		t.Fatalf("owned browser after close=%#v err=%v", after, err)
	}
	releaseFull()
	if _, granted := capabilities.RuntimeAuthority.AllowsFullAccess(permission); granted {
		t.Fatal("the CLI invocation retained its Full grant")
	}
	persisted, err := state.GetRunExecutionPermission(t.Context(), runID)
	if err != nil || !reflect.DeepEqual(persisted, permission) {
		t.Fatalf("CLI activation changed durable permission: %+v err=%v", persisted, err)
	}
}

func jsonEqualCLIAgentBrowser(left, right json.RawMessage) bool {
	var l, r any
	return json.Unmarshal(left, &l) == nil && json.Unmarshal(right, &r) == nil &&
		reflect.DeepEqual(l, r)
}

func sortedCLIAgentBrowserToolNames(tools map[string]llm.ToolSpec) []string {
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	return names
}

var _ llm.Provider = (*cliAgentBrowserProvider)(nil)
