package application

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

// Opt-in third-party acceptance. Install the exact official package outside the
// repository and point UNIVERSAL_CODE_MCP_FILESYSTEM_PACKAGE at its directory.
func TestMCPWorkspaceRootsOfficialFilesystem(t *testing.T) {
	packageRoot := os.Getenv("UNIVERSAL_CODE_MCP_FILESYSTEM_PACKAGE")
	if packageRoot == "" {
		t.Skip("requires the official @modelcontextprotocol/server-filesystem@1.0.0 package")
	}
	var manifest struct{ Name, Version string }
	data, err := os.ReadFile(filepath.Join(packageRoot, "package.json"))
	if err != nil || json.Unmarshal(data, &manifest) != nil || manifest.Name != "@modelcontextprotocol/server-filesystem" || manifest.Version != "1.0.0" {
		t.Fatal("acceptance requires the pinned official filesystem package", err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Open(filepath.Join(t.TempDir(), "roots.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	selected := store.WorkspaceRecord{ID: "roots-workspace", Name: "Roots workspace", RootPath: t.TempDir()}
	if err := state.SaveWorkspace(t.Context(), selected); err != nil {
		t.Fatal(err)
	}
	inside, outside := filepath.Join(selected.RootPath, "inside.txt"), filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(inside, []byte("selected Workspace 中文"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("outside content must remain unread"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := mcp.NewClientManager(state, nil, mcp.ManagerOptions{InputBroker: mcp.NewInputBroker(), WorkspaceRoot: NewMCPWorkspaceRootProvider(state, nil)})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	descriptor := mcp.ServerDescriptor{ProtocolVersion: mcp.ConfigClientProtocolVersion, ID: "official-filesystem", Name: "Official filesystem",
		Transport: mcp.TransportStdio, Target: node, Arguments: []string{filepath.Join(packageRoot, "dist", "index.js")},
		Cwd: filepath.Dir(outside), Scope: mcp.ScopeWorkspace, WorkspaceID: selected.ID,
		DeclaredCapabilities: []mcp.CapabilityKind{mcp.CapabilityTools}, Source: mcp.Source{Kind: "manual", URI: "operator://roots-acceptance"},
		CallTimeoutMillis: 10000, MaxResultBytes: 8192}
	// Registration and discovery happen before any Run exists, and pass no
	// allowed-directory argument to the actual filesystem server.
	record, _, err := manager.Stage(t.Context(), descriptor)
	if err != nil {
		t.Fatal(err)
	}
	record, err = manager.Review(t.Context(), descriptor.ID, mcp.ReviewRequest{Action: mcp.ReviewApproveDiscovery,
		ExpectedDescriptorFingerprint: record.DescriptorFingerprint, ReviewedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	record, err = manager.Refresh(t.Context(), descriptor.ID)
	if err != nil {
		t.Fatalf("initial roots-only discovery failed: %v (%s)", err, record.HealthMessage)
	}
	record, err = manager.Review(t.Context(), descriptor.ID, mcp.ReviewRequest{Action: mcp.ReviewEnableCapabilities,
		ExpectedDescriptorFingerprint: record.DescriptorFingerprint, ExpectedCapabilityFingerprint: record.Capabilities.Fingerprint, ReviewedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	runs := NewRunService(state)
	_, run, err := runs.Create(t.Context(), CreateRunRequest{Goal: "Read the selected Workspace", Profile: "code", WorkspaceID: selected.ID,
		Budget: domain.Budget{MaxTurns: 4, MaxTokens: 1000, MaxToolCalls: 8}})
	if err != nil {
		t.Fatal(err)
	}
	capabilities := domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
		RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
	if _, err := NewRunExecutionPermissionService(state, capabilities).Change(t.Context(), ChangeRunExecutionPermissionRequest{RunID: run.ID,
		Mode: "full", ConfirmFull: true, OperationKey: "roots-permission-0001", RequestedBy: "operator", Reason: "filesystem acceptance"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Start(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	lease, err := state.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "roots-worker", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := state.BeginSupervisorTurn(t.Context(), lease.Lease, "")
	if err != nil {
		t.Fatal(err)
	}
	supervisor := NewAgentRunner(state, nil, policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(capabilities).WithMCPClient(manager)
	permission, err := state.GetRunExecutionPermission(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := supervisor.supervisorMCPCapabilities(t.Context(), turn, permission)
	if err != nil || len(catalog.Authority) == 0 {
		t.Fatal("current Run did not receive its enabled catalog", err)
	}
	var calls []llm.ToolCall
	for i, item := range []struct{ name, path string }{{"list_allowed_directories", ""}, {"read_text_file", inside}, {"read_text_file", outside}} {
		arguments := map[string]string{}
		if item.path != "" {
			arguments["path"] = item.path
		}
		raw, _ := json.Marshal(arguments)
		payload, _ := json.Marshal(toolgateway.MCPToolCallPayload{Version: toolgateway.MCPClientToolProtocolVersion, ServerID: descriptor.ID,
			ToolName: item.name, CapabilityFingerprint: record.Capabilities.Fingerprint, Arguments: raw})
		calls = append(calls, llm.ToolCall{ID: []string{"allowed-directories", "inside-read", "outside-read"}[i], Name: mcp.OperationApprovalTool, Arguments: payload})
	}
	prepared, err := prepareSupervisorToolCalls(calls, run.ID, turn.Checkpoint.NextTurn, 1, turn.Mode.Surface, turn.Mode.Phase,
		domain.RunExecutionPermissionFull, false, false, supervisorToolOptions{MCP: catalog})
	if err != nil {
		t.Fatal(err)
	}
	attempt := llm.ModelAttempt{Number: 1, TransportAttempt: 1, MaxAttempts: 1, Provider: "offline-fixture", Model: "fixture"}
	if _, err := state.RecordSupervisorModelStarted(t.Context(), turn.Checkpoint, attempt); err != nil {
		t.Fatal(err)
	}
	attempt.Outcome = llm.OutcomeSuccess
	turn.Checkpoint, err = state.RecordSupervisorModelCompleted(t.Context(), turn.Checkpoint, attempt, llm.ChatResponse{Provider: attempt.Provider, Model: attempt.Model, ToolCalls: prepared})
	if err != nil {
		t.Fatal(err)
	}
	rounds, err := state.ListSupervisorToolRounds(t.Context(), turn.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if _, waiting, err := supervisor.resumeSupervisorTools(t.Context(), turn, rounds); err != nil || waiting {
		t.Fatalf("actual host dispatch failed: waiting=%t err=%v", waiting, err)
	}
	rounds, err = state.ListSupervisorToolRounds(t.Context(), turn.Checkpoint)
	if err != nil || len(rounds) != 1 || len(rounds[0].Calls) != 3 {
		t.Fatal("actual tool receipts are missing", err)
	}
	for i, call := range rounds[0].Calls {
		var envelope supervisorToolResultEnvelope
		var native struct {
			Content []struct{ Text string }
			IsError bool
		}
		if json.Unmarshal([]byte(call.ResultJSON), &envelope) != nil || json.Unmarshal([]byte(envelope.Stdout), &native) != nil {
			t.Fatal("filesystem result is not native JSON", call.ResultJSON)
		}
		text := ""
		for _, content := range native.Content {
			text += content.Text
		}
		switch i {
		case 0:
			if native.IsError || !strings.Contains(text, selected.RootPath) || strings.Contains(text, filepath.Dir(outside)) {
				t.Fatal("roots widened to process cwd or lost selected Workspace", text)
			}
		case 1:
			if native.IsError || call.Status != domain.SupervisorToolCompleted || !strings.Contains(text, "selected Workspace 中文") {
				t.Fatal("selected Workspace was not readable", text)
			}
		case 2:
			if !native.IsError || call.Status != domain.SupervisorToolFailed || strings.Contains(text, "outside content must remain unread") {
				t.Fatal("filesystem server read outside its supplied roots", text)
			}
		}
	}
}
