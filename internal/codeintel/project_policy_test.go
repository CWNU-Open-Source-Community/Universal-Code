package codeintel

import (
	"context"
	"strings"
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

func TestManagerDelegatedReadScopeFiltersEvidenceAndBindsCursor(t *testing.T) {
	root := testWorkspace(t)
	manager := testManager(t, root, "normal", 2*time.Second)
	snapshot := manager.Capabilities(t.Context(), helperWorkspaceID, root)[0]
	request := semanticRequest(root, snapshot, ToolWorkspaceSymbols, "", "main", "")
	request.Limit = 1
	request.ReadScopeFingerprint = strings.Repeat("a", 64)
	request.ReadPathAllowed = func(path string) bool { return path == "main.go" }
	first, err := manager.Execute(t.Context(), request)
	if err != nil || first.Page.NextCursor == "" {
		t.Fatalf("scoped first page = %+v, %v", first, err)
	}
	request.ReadScopeFingerprint = strings.Repeat("b", 64)
	request.Cursor = first.Page.NextCursor
	if _, err := manager.Execute(t.Context(), request); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("cursor crossed delegated scope: %v", err)
	}
	request.Cursor = ""
	request.ReadPathAllowed = func(string) bool { return false }
	filtered, err := manager.Execute(t.Context(), request)
	if err != nil || len(filtered.Items) != 0 || len(filtered.Edges) != 0 || filtered.Page.Total != 0 {
		t.Fatalf("foreign semantic evidence was counted or returned: %+v %v", filtered, err)
	}
	request.Tool, request.Path, request.Query = ToolDefinition, "main.go", ""
	if _, err := manager.Execute(t.Context(), request); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("foreign document query was sent: %v", err)
	}
	request.ReadScopeFingerprint = ""
	if _, err := manager.Execute(t.Context(), request); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("unbound scope predicate was accepted: %v", err)
	}
	items, edges := filterProjectEvidence([]EvidenceItem{{ID: "owned", Path: "main.go"}, {ID: "foreign", Path: "other.go"}},
		[]GraphEdge{{From: "owned", To: "foreign"}}, workspaceidentity.PathPolicy{}, func(path string) bool { return path == "main.go" })
	if len(items) != 1 || len(edges) != 0 {
		t.Fatalf("foreign graph location survived filtering: %+v %+v", items, edges)
	}
}
