package application

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
)

func TestWorkerDefaultShellLowersOnlyCurrentlyAdvertisedHostCommands(t *testing.T) {
	host := commandruntimeadapter.HostUnsandboxed(strings.Repeat("a", 64))
	authority, _ := json.Marshal(commandruntimeadapter.NewAuthority("run-worker-shell", host))
	catalogue := workerToolCatalogue{tools: map[string]workerToolEntry{
		"command_runtime": {definition: toolgateway.CommandRuntimeDefinitionForAdapter(host), authority: authority},
	}}
	request := []llm.ToolCall{{ID: "provider-original-id", Name: "command_runtime", Arguments: json.RawMessage(`{"command":"node server.cjs","run_in_background":true}`)},
		{ID: "file-id", Name: "workspace_read", Arguments: json.RawMessage(`{"path":"index.html"}`)}}
	calls, err := catalogue.normalizeCommandCalls(request)
	if err != nil {
		t.Fatal(err)
	}
	if calls[0].ID != request[0].ID || !bytes.Equal(calls[1].Arguments, request[1].Arguments) || !bytes.Contains(request[0].Arguments, []byte(`"command"`)) {
		t.Fatal("normalization changed provider replay identity, another tool, or original arguments")
	}
	canonical, err := toolgateway.NormalizeSupervisorToolPayload(toolgateway.CommandRuntimeTool, calls[0].Arguments)
	var durable toolgateway.CommandRuntimeInput
	if err != nil || !bytes.Equal(canonical, calls[0].Arguments) || json.Unmarshal(canonical, &durable) != nil || len(durable.Commands) != 1 ||
		durable.Action != toolgateway.CommandRuntimeActionStart || durable.Commands[0].WorkspaceMode != runner.CommandRuntimeWorkspaceSharedService ||
		durable.Commands[0].Profile != runner.DefaultCommandRuntimeShellProfile() {
		t.Fatalf("worker recording did not receive canonical durable background intent: %s err=%v", canonical, err)
	}
	guest := commandruntimeadapter.SandboxedWorkspace("docker", "guest", strings.Repeat("b", 64))
	guestAuthority, _ := json.Marshal(commandruntimeadapter.NewAuthority("run-worker-shell", guest))
	for _, c := range []workerToolCatalogue{
		{tools: map[string]workerToolEntry{}},
		{tools: map[string]workerToolEntry{"command_runtime": {authority: guestAuthority}}},
		{tools: map[string]workerToolEntry{"command_runtime": {authority: json.RawMessage(`{}`)}}},
	} {
		if _, err := c.normalizeCommandCalls(request); err == nil {
			t.Fatal("worker accepted unadvertised, guest, or invalid host authority")
		}
	}
}
