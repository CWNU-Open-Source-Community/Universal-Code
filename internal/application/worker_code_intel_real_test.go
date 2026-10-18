package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/codeintel"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/toolgateway"
)

func TestWorkerCodeIntelRealGopls(t *testing.T) {
	executable := strings.TrimSpace(os.Getenv("CYBERAGENT_GOPLS_SMOKE_BINARY"))
	if executable == "" {
		t.Skip("CYBERAGENT_GOPLS_SMOKE_BINARY is not set")
	}
	f := newWorkerCodeIntelFixture(t, &domain.WorkerProfile{})
	a := f.actor
	mission, err := a.store.GetMission(t.Context(), a.run.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := a.store.GetWorkspaceInfo(t.Context(), mission.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		"go.mod":    "module example.invalid/workerlsp\n\ngo 1.25\n",
		"sample.go": "package sample\nfunc Helper(value string) string { return value }\nfunc Use() string { return Helper(\"x\") }\n",
	} {
		if err := os.WriteFile(filepath.Join(workspace.RootPath, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	manager := newWorkerGoplsTestManager(t, executable, workspace.ID)
	f.runtime.WithCodeIntel(manager)
	f.executor = NewCodeIntelToolExecutor(a.store, policy.NewDefaultChecker(), manager).WithExecutionPermissionCapabilities(f.caps)
	f.runtime.tools.WithCodeIntelExecutor(f.executor)
	mode, err := a.store.GetRunMode(t.Context(), a.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := f.runtime.workerCodeIntelCapabilities(t.Context(), a.ref,
		domain.SupervisorTurn{Run: a.run, Mission: mission, Agent: a.root, Mode: mode})
	if err != nil || len(capabilities.Servers) != 1 {
		t.Fatalf("worker LSP capabilities: %+v %v", capabilities, err)
	}
	server := capabilities.Servers[0]
	if len(capabilities.VisibleDefinitions()) != 10 {
		t.Fatalf("gopls did not provide the complete semantic family: %+v", capabilities)
	}
	for _, name := range []toolgateway.ToolName{toolgateway.CodeDocumentSymbolsTool, toolgateway.CodeDefinitionTool} {
		input := toolgateway.CodeIntelPayload{Version: toolgateway.CodeIntelProtocolVersion,
			ServerID: server.ServerID, ServerGeneration: server.Generation, CapabilityFingerprint: server.CapabilityFingerprint,
			Path: "sample.go", Limit: 20}
		if name == toolgateway.CodeDefinitionTool {
			input.Line, input.Character = 2, 27
		}
		raw, _ := json.Marshal(input)
		_, payload, err := toolgateway.NormalizeCodeIntelPayload(name, raw)
		if err != nil {
			t.Fatal(err)
		}
		stored, call := f.prepare(t, name, payload)
		outcome, err := f.runtime.tools.Invoke(t.Context(), call)
		if err != nil || outcome.Result == nil || outcome.Result.Status != toolgateway.StatusCompleted {
			t.Fatalf("worker real LSP %s: %+v %v", name, outcome, err)
		}
		var result codeintel.Result
		if err := json.Unmarshal([]byte(outcome.Result.Stdout), &result); err != nil || len(result.Items) == 0 ||
			result.Provenance.WorkspaceID != workspace.ID || result.Provenance.ServerID != server.ServerID ||
			result.Provenance.DocumentPath != "sample.go" || result.Provenance.DocumentSHA256 == "" {
			t.Fatalf("worker LSP evidence lost its source: %s %v", outcome.Result.Stdout, err)
		}
		stored.Status, stored.ResultJSON = domain.SupervisorToolCompleted, outcome.Result.Stdout
		if err := a.store.CompleteSpecialistToolCall(t.Context(), stored); err != nil {
			t.Fatal(err)
		}
		t.Logf("worker=%s tool=%s server=%s items=%d document=%s", a.child.ID, name,
			result.Provenance.ServerID, len(result.Items), result.Provenance.DocumentPath)
	}
	if err := f.runtime.closeWorkerCodeIntelResources(t.Context(), a.ref); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RefreshInitializedServer(t.Context(), workspace.ID, workspace.RootPath,
		server.ServerID, server.Generation); err != nil {
		t.Fatalf("shared worker cleanup retired the parent semantic runtime: %v", err)
	}
}

func newWorkerGoplsTestManager(t *testing.T, executable, workspaceID string) *codeintel.Manager {
	t.Helper()
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(binary)
	manager, err := codeintel.NewManager([]codeintel.ServerDescriptor{{
		ProtocolVersion: codeintel.ProtocolVersion, ID: "worker-gopls", Name: "worker gopls", WorkspaceID: workspaceID,
		Languages:  []codeintel.Language{{ID: "go", Extensions: []string{".go"}}},
		Executable: executable, ExecutableSHA256: hex.EncodeToString(digest[:]), Arguments: []string{"serve"},
		RequestTimeoutMillis: 30000, ReviewedBy: "worker-lsp-test", ReviewedAt: time.Now().UTC(),
		Source: codeintel.Source{Kind: "operator_config", Label: "worker-lsp-smoke.json", SHA256: strings.Repeat("c", 64)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	return manager
}

func TestWorkerCodeIntelLegacyBatchCleanupPreservesParent(t *testing.T) {
	executable := strings.TrimSpace(os.Getenv("CYBERAGENT_GOPLS_SMOKE_BINARY"))
	if executable == "" {
		t.Skip("CYBERAGENT_GOPLS_SMOKE_BINARY is not set")
	}
	fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionAsk)
	prepared, err := fixture.service.Prepare(t.Context(), PrepareBatchDeliveryRequest{
		RunID: fixture.run.ID, ProposalID: fixture.proposal.ID, Spec: fixture.spec,
		OperationKey: "batch-legacy-lsp-cleanup-0001", RequestedBy: fixture.root.ID,
		WorktreeParent: t.TempDir(), Confirm: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	manager := newWorkerGoplsTestManager(t, executable, fixture.proposal.WorkspaceID)
	parent, err := manager.InitializeServer(t.Context(), fixture.proposal.WorkspaceID, fixture.repository, "worker-gopls")
	if err != nil {
		t.Fatal(err)
	}
	runner := NewAgentRunner(fixture.store, nil, policy.NewDefaultChecker()).WithCodeIntel(manager)
	for _, work := range prepared.Workspaces {
		child, err := fixture.store.GetAgentNode(t.Context(), work.AgentID)
		if err != nil || child.Worker != nil {
			t.Fatalf("expected legacy Batch child: %+v %v", child, err)
		}
		ref := domain.AgentAttemptRef{RunID: fixture.run.ID, AgentID: child.ID}
		foreign := ref
		foreign.RunID = "foreign-run"
		if err := runner.closeWorkerCodeIntelResources(t.Context(), foreign); apperror.CodeOf(err) != apperror.CodePolicyDenied {
			t.Fatalf("legacy cleanup accepted a different run: %v", err)
		}
		if err := runner.closeWorkerResources(ref); err != nil {
			t.Fatalf("legacy Batch child blocked cleanup: %v", err)
		}
	}
	if _, err := manager.RefreshInitializedServer(t.Context(), fixture.proposal.WorkspaceID,
		fixture.repository, parent.ServerID, parent.Generation); err != nil {
		t.Fatalf("legacy Batch cleanup retired the parent semantic runtime: %v", err)
	}
}
