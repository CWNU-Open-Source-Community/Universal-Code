package mcp_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/toolcontract"
)

func TestManualConfigRuntimeSubprocessReceivesReviewedConfiguration(t *testing.T) {
	t.Setenv("TRAVERSE_MANAGER_PARENT_CANARY", "must-not-enter-child")
	f := newManagerStdioFixture(t, "manual_config_result")
	for _, event := range managerStdioEvents(t, f.dir, "review") {
		if event.Method == "spawn" && (!event.ManualConfigMatched || event.CanaryInherited) {
			t.Fatal("discovery subprocess received the wrong directory/environment")
		}
	}
	scope, _ := f.scope(t)
	executor, err := application.NewMCPClientToolExecutor(f.manager, f.state, f.capabilities)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.ExecuteMCP(t.Context(), scope, f.payload())
	if err != nil || result.Metadata["execution_receipt"] != string(toolcontract.ReceiptResultReceived) || strings.Contains(result.Content, "synthetic-environment-secret") {
		t.Fatal("manual configuration invocation failed or leaked the environment secret", err)
	}
	events := managerStdioEvents(t, f.dir, "runtime")
	if countManagerStdioEvents(events, "spawn") != 1 || countManagerStdioEvents(events, "tools/call") != 1 {
		t.Fatal("manual runtime did not dispatch exactly one process/call")
	}
	for _, event := range events {
		if event.Method == "spawn" && (!event.ManualConfigMatched || event.CanaryInherited) {
			t.Fatal("runtime subprocess lost reviewed configuration")
		}
	}
	record, err := f.state.GetMCPClientServer(t.Context(), f.reviewed.Descriptor.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(record)
	if strings.Contains(string(raw), "synthetic-environment-secret") {
		t.Fatal("credential entered persisted server state")
	}
}

func TestManualConfigCredentialRotationBeforeCallStopsDispatch(t *testing.T) {
	f := newManagerStdioFixture(t, "manual_config_discovery")
	scope, _ := f.scope(t)
	executor, err := application.NewMCPClientToolExecutor(f.manager, f.state, f.capabilities)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := executor.ExecuteMCP(t.Context(), scope, f.payload()); done <- err }()
	waitManagerStdioEvent(t, f.dir, "tools/list")
	if err := f.credentials.Put(t.Context(), "manual-config-secret", "rotated-environment-secret"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "release-list"), []byte("continue"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
	case <-time.After(25 * time.Second):
		t.Fatal("manual configuration invocation did not finish")
	}
	receipt, found := mcp.InvocationReceipt(err)
	if err == nil || !found || receipt.State != toolcontract.ReceiptNotDispatched {
		t.Fatal("credential rotation allowed dispatch", err)
	}
	if countManagerStdioEvents(managerStdioEvents(t, f.dir, "runtime"), "tools/call") != 0 {
		t.Fatal("rotated credential dispatched a tool")
	}
}
