package mcp_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/toolgateway"
)

// Exercise the shipped service over the real guarded stdio connection. The
// deliberately unknown window returns an error before inspecting or capturing
// any desktop window, so ordinary CI never sends native input.
func TestComputerUseRuntimeUsesProductionAuthorityAndCatalog(t *testing.T) {
	f := newManagerStdioFixture(t, "result")
	if _, err := f.manager.Review(t.Context(), f.reviewed.Descriptor.ID, mcp.ReviewRequest{
		Action: mcp.ReviewDisable, ExpectedDescriptorFingerprint: f.reviewed.DescriptorFingerprint, ReviewedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	_ = f.manager.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f.manager, err = mcp.NewClientManager(f.state, f.credentials, mcp.ManagerOptions{ComputerUseExecutable: executable})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.manager.Close() })
	caps, err := f.manager.RefreshCapabilities(t.Context(), f.run.ID, "stdio-workspace", func(ctx context.Context) error {
		return ctx.Err()
	}, nil)
	if err != nil || len(caps.Servers) != 1 {
		t.Fatalf("production discovery lost the built-in catalog: %+v %v", caps, err)
	}
	scope, _ := f.scope(t)
	executor, err := application.NewMCPClientToolExecutor(f.manager, f.state, f.capabilities)
	if err != nil {
		t.Fatal(err)
	}
	arguments, _ := json.Marshal(map[string]string{"window_id": "never-issued-window",
		"app_id": "winapp:" + base64.RawURLEncoding.EncodeToString([]byte(`c:\fixture\never-issued.exe`)) + ":" + strings.Repeat("0", 64)})
	result, err := executor.ExecuteMCP(t.Context(), scope, toolgateway.MCPToolCallPayload{
		Version: toolgateway.MCPClientToolProtocolVersion, ServerID: caps.Servers[0].ServerID,
		CapabilityFingerprint: caps.Servers[0].CapabilityFingerprint, ToolName: "computer_observe",
		Arguments: arguments,
	})
	if err != nil || !result.IsError || result.Metadata["execution_receipt"] != "result_received" {
		t.Fatalf("native refusal did not return through the common executor: %+v %v", result, err)
	}
}
