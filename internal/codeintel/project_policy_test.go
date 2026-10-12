package codeintel

import (
	"context"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/workspaceidentity"
)

func TestManagerProjectExclusionsFilterEvidenceAndBindCursor(t *testing.T) {
	root := testWorkspace(t)
	manager := testManager(t, root, "normal", 2*time.Second)
	snapshot := manager.Capabilities(context.Background(), helperWorkspaceID, root)[0]
	request := semanticRequest(root, snapshot, ToolWorkspaceSymbols, "", "main", "")
	request.Limit = 1
	first, err := manager.Execute(t.Context(), request)
	if err != nil || first.Page.NextCursor == "" {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	request.PathPolicy = workspaceidentity.PathPolicy{ExcludePaths: []string{"main.go"}}
	request.Cursor = first.Page.NextCursor
	if _, err := manager.Execute(t.Context(), request); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("cursor crossed project filter: %v", err)
	}
	request.Cursor = ""
	filtered, err := manager.Execute(t.Context(), request)
	if err != nil || len(filtered.Items) != 0 || filtered.Page.Total != 0 {
		t.Fatalf("excluded semantic evidence = %+v, %v", filtered, err)
	}
	request = semanticRequest(root, snapshot, ToolDefinition, "main.go", "", "")
	request.PathPolicy = workspaceidentity.PathPolicy{ExcludePaths: []string{"main.go"}}
	if _, err := manager.Execute(t.Context(), request); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("excluded document query: %v", err)
	}
}
