package application_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/toolgateway"
)

func TestAgentCodeUsesPinnedProjectExclusions(t *testing.T) {
	f := newAgentCodeReplaceFixtureForPermission(t, domain.RunExecutionPermissionFull,
		&projectconfig.Effective{Protocol: projectconfig.ProtocolVersion, ExcludePaths: []string{"secrets/**"}})
	for _, dir := range []string{"secrets", ".prayu"} {
		if err := os.Mkdir(filepath.Join(f.root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.root, "secrets", "hidden.txt"), []byte("excluded evidence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Editing the live config does not widen or replace the active Run snapshot.
	if err := os.WriteFile(filepath.Join(f.root, ".prayu", "config.yaml"), []byte("protocol: project_config.v1\nexclude_paths: [target.txt]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	version := toolgateway.AgentCodeRegistryVersion
	for _, test := range []struct {
		name    string
		tool    toolgateway.ToolName
		payload any
	}{
		{"read", toolgateway.WorkspaceReadTool, toolgateway.WorkspaceReadPayload{Version: version, Path: "secrets/hidden.txt", StartLine: 1, EndLine: 10}},
		{"list", toolgateway.WorkspaceListTool, toolgateway.WorkspaceListPayload{Version: version, Path: "secrets", Limit: 10}},
		{"create", toolgateway.WorkspaceChangeTool, toolgateway.WorkspaceChangePayload{Version: version, Action: "create", Path: "secrets/new.txt", ExpectedSHA256: "missing", Content: "new\n"}},
		{"replace", toolgateway.WorkspaceChangeTool, toolgateway.WorkspaceChangePayload{Version: version, Action: "replace", Path: "secrets/hidden.txt", ExpectedSHA256: fileedit.HashText("excluded evidence\n"), Content: "new\n"}},
		{"move", toolgateway.WorkspaceChangeTool, toolgateway.WorkspaceChangePayload{Version: version, Action: "move", Path: "target.txt", ExpectedSHA256: fileedit.HashText("before\n"), DestinationPath: "secrets/moved.txt", DestinationExpectedSHA256: "missing"}},
		{"delete", toolgateway.WorkspaceDeleteTool, toolgateway.WorkspaceDeletePayload{Version: version, Action: "propose", Path: "secrets/hidden.txt", ExpectedSHA256: fileedit.HashText("excluded evidence\n")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := f.execute(t, test.tool, mustAgentCodePayload(t, test.payload), "project-excluded-"+test.name+"-0001")
			if apperror.CodeOf(err) != apperror.CodePolicyDenied {
				t.Fatalf("excluded operation: %v", err)
			}
		})
	}
	for _, tool := range []toolgateway.ToolName{toolgateway.WorkspaceGlobTool, toolgateway.WorkspaceGrepTool} {
		input := map[string]any{"version": version, "pattern": "**", "limit": 20}
		if tool == toolgateway.WorkspaceGrepTool {
			input["query"] = "evidence"
			input["case_sensitive"] = true
		}
		result, err := f.execute(t, tool, mustAgentCodePayload(t, input), "project-filter-"+string(tool)+"-0001")
		if err != nil || strings.Contains(result.JSON, "hidden.txt") {
			t.Fatalf("excluded search result = %s, %v", result.JSON, err)
		}
	}
	if _, err := application.NewFileEditProposalService(f.state, policy.NewDefaultChecker()).IssueSource(t.Context(), f.scope.RunID, "secrets/hidden.txt"); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("editor issued excluded source: %v", err)
	}
	// An ordinary edit still uses the existing review/apply path.
	proposal := f.propose(t, "allowed\n", "project-allowed-replace-0001")
	if !proposal.ApplyAuthorized {
		t.Fatal("allowed file became unavailable")
	}
}
