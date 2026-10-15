package codeintel

import (
	"context"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
)

func TestDiagnosticsExpectedAppliedVersionRejectsDifferentBytes(t *testing.T) {
	root := testWorkspace(t)
	manager := testManager(t, root, "normal", DefaultRequestTimeout)
	snapshot := manager.Capabilities(t.Context(), helperWorkspaceID, root)[0]
	request := semanticRequest(root, snapshot, ToolDiagnostics, "main.go", "", "")
	request.ExpectedDocumentSHA256 = strings.Repeat("0", 64)
	if _, err := manager.Execute(t.Context(), request); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("different document was treated as the applied version: %v", err)
	}
	document, err := captureDocumentBinding(root, "main.go", 1)
	if err != nil {
		t.Fatal(err)
	}
	request.ExpectedDocumentSHA256 = document.SHA256
	result, err := manager.Execute(t.Context(), request)
	if err != nil || result.Provenance.DocumentSHA256 != document.SHA256 || result.Provenance.DocumentVersion <= 0 || len(result.Items) == 0 {
		t.Fatalf("current diagnostics lost applied binding: %+v, %v", result, err)
	}
	request.Tool = ToolDocumentSymbols
	if err := request.Validate(); err == nil {
		t.Fatal("hash constraint was accepted for a different tool")
	}
}

func TestAutomaticDiagnosticsCannotInitializeReplacementConfiguration(t *testing.T) {
	root := testWorkspace(t)
	manager := testManager(t, root, "normal", DefaultRequestTimeout)
	snapshot := manager.Capabilities(t.Context(), helperWorkspaceID, root)[0]
	descriptors := manager.Descriptors()
	if err := manager.ReplaceConfiguration(context.Background(), descriptors, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RefreshInitializedServer(t.Context(), helperWorkspaceID, root, snapshot.ServerID, snapshot.Generation); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("replacement configuration was initialized by post-edit feedback: %v", err)
	}
	if manager.Inventory()[0].Health != HealthConfigured {
		t.Fatal("replacement server started")
	}
	request := semanticRequest(root, snapshot, ToolDiagnostics, "main.go", "", "")
	request.ExpectedDocumentSHA256 = strings.Repeat("a", 64)
	if _, err := manager.Execute(t.Context(), request); apperror.CodeOf(err) != apperror.CodeConflict || manager.Inventory()[0].Health != HealthConfigured {
		t.Fatalf("stale automatic diagnostics initialized a server: %v", err)
	}
}
