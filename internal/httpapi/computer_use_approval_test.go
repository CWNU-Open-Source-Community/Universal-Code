package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/plugins"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

type computerUseApprovalHTTPPeer struct{ manager *mcp.Manager }

func (p computerUseApprovalHTTPPeer) Capabilities(ctx context.Context, runID, workspaceID string) (mcp.ScopedCapabilities, error) {
	return p.manager.Capabilities(ctx, runID, workspaceID)
}

func (computerUseApprovalHTTPPeer) Invoke(context.Context, mcp.InvokeRequest) (mcp.ClientCallResult, error) {
	return mcp.ClientCallResult{}, errors.New("approval preview must not control a desktop")
}

// The HTTP contract uses the real pending Supervisor call and durable approval
// transaction. The peer only supplies the shipped catalog; no desktop is used.
func TestComputerUseApprovalHTTPAppScope(t *testing.T) {
	for _, shortcut := range []bool{false, true} {
		name := "application"
		if shortcut {
			name = "system_shortcut"
		}
		t.Run(name, func(t *testing.T) { testComputerUseApprovalHTTP(t, shortcut) })
	}
}

func testComputerUseApprovalHTTP(t *testing.T, shortcut bool) {
	st, err := store.Open(filepath.Join(t.TempDir(), "computer-use-http.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	workspace := store.WorkspaceRecord{ID: "computer-use-http", Name: "Desktop approval fixture", RootPath: t.TempDir()}
	if err = st.SaveWorkspace(t.Context(), workspace); err != nil {
		t.Fatal(err)
	}
	provider := &mcpApprovalProductProvider{}
	runs := application.NewRunService(st)
	_, run, err := runs.Create(t.Context(), application.CreateRunRequest{Goal: "在测试窗口输入中文并检查结果", Profile: "code", Surface: "code", Phase: "deliver", WorkspaceID: workspace.ID,
		Interactive: true, ModelRoute: provider.Name() + "/fixture", Budget: domain.Budget{MaxTurns: 4, MaxTokens: 50000, MaxToolCalls: 8}})
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	manager, err := mcp.NewClientManager(st, mcpApprovalNoCredentials{}, mcp.ManagerOptions{ComputerUseExecutable: executable})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	pluginService, err := plugins.NewService(st)
	if err != nil {
		t.Fatal(err)
	}
	extensions, err := application.NewExtensionControlService(st, manager, pluginService)
	if err != nil {
		t.Fatal(err)
	}
	// Listing the chosen workspace installs the built-in descriptor; operators
	// need neither an executable path nor a separate discovery approval.
	inventory, err := extensions.InventoryForScope(t.Context(), run.ID, workspace.ID)
	if err != nil || len(inventory.MCPServers) != 1 {
		t.Fatalf("builtin inventory: %+v %v", inventory, err)
	}
	server := inventory.MCPServers[0]
	appPath := `c:\universal-code\test-fixture.exe`
	appID := "winapp:" + base64.RawURLEncoding.EncodeToString([]byte(appPath)) + ":" + strings.Repeat("a", 64)
	tool := "computer_activate"
	arguments := map[string]string{"app_id": appID, "window_id": "fixture-window"}
	if shortcut {
		tool = "computer_key"
		arguments["key"], arguments["observation_id"] = "WIN+D", "fixture-observation"
	}
	args, _ := json.Marshal(arguments)
	provider.payload, _ = json.Marshal(toolgateway.MCPToolCallPayload{Version: toolgateway.MCPClientToolProtocolVersion, ServerID: server.Descriptor.ID,
		ToolName: tool, CapabilityFingerprint: server.Capabilities.Fingerprint, Arguments: args})
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "fixture"})
	router.RegisterProvider(provider)
	checker := policy.NewDefaultChecker()
	caps := domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
	supervisor := application.NewAgentRunner(st, router, checker).WithExecutionPermissionCapabilities(caps).WithMCPClient(computerUseApprovalHTTPPeer{manager})
	controller := application.NewApprovalControlService(st, toolgateway.New(st, checker), checker)
	api, err := New(st, Config{AccessToken: testAccessToken, ControlToken: testControlToken, ApprovalControlEnabled: true, ApprovalController: controller,
		ExtensionControlEnabled: true, ExtensionController: extensions})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runs.Start(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	step, err := supervisor.Step(t.Context(), run.ID)
	if err != nil || step.RunStatus != domain.RunWaitingApproval {
		t.Fatalf("app approval not requested: %+v %v", step, err)
	}
	approvals, err := st.ListApprovals(t.Context(), approval.ListFilter{RunID: run.ID, Status: approval.StatusPending})
	if err != nil || len(approvals) != 1 {
		t.Fatalf("pending approval: %+v %v", approvals, err)
	}
	base := "/api/v1/runs/" + run.ID + "/approvals/" + approvals[0].ID
	queueResponse := performRequest(t, api, http.MethodGet, "/api/v1/runs/"+run.ID+"/approvals", testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil)
	var queue ApprovalQueueView
	decodeData(t, queueResponse, &queue)
	if len(queue.Items) != 1 || slices.Contains(queue.Items[0].AllowedActions, application.ApprovalControlApproveForRunApp) == shortcut {
		t.Fatalf("wrong application authorization actions: %+v", queue)
	}
	previewResponse := performRequest(t, api, http.MethodGet, base+"/preview", testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil)
	var preview ApprovalPreviewView
	decodeData(t, previewResponse, &preview)
	if !preview.SourceCurrent || preview.Effect != "computer_use_app" {
		t.Fatalf("wrong app preview: %+v", preview)
	}
	fields := map[string]string{}
	for _, field := range preview.Fields {
		fields[field.Name] = field.Value
	}
	if fields["app_path"] != appPath || fields["app_id"] != appID || fields["window_id"] != "fixture-window" || !strings.Contains(fields["authorization_scope"], run.ID) {
		t.Fatalf("preview lost its exact application or task: %+v", fields)
	}
	var decision ApprovalDecisionControlView
	action := application.ApprovalControlApproveForRunApp
	if shortcut {
		action = application.ApprovalControlApproveOnce
		if !strings.Contains(fields["authorization_scope"], "仅本次系统快捷键") {
			t.Fatal("shortcut preview did not describe its desktop effect")
		}
	}
	for replay := 0; replay < 2; replay++ {
		body, _ := json.Marshal(map[string]string{"version": "approval_control.v1", "action": string(action)})
		response := performControlPathRequest(t, api, base+"/decision", "computer-use-app-decision", strings.NewReader(string(body)))
		decodeDataStatus(t, response, http.StatusAccepted, &decision)
		if decision.Action != action || decision.Replayed != (replay == 1) || decision.SessionGrantCreated != (replay == 0 && !shortcut) || decision.BoundedGrant != nil || decision.CapabilityGrant {
			t.Fatalf("invalid app decision response: %+v", decision)
		}
	}
	prefix := "computer-use"
	if shortcut {
		prefix += "-shortcut"
	}
	writeExtensionOnboardingEvidence(t, prefix+"-queue.json", queueResponse)
	writeExtensionOnboardingEvidence(t, prefix+"-preview.json", previewResponse)
}
