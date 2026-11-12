package application_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
)

func assertRootToolExposure(t *testing.T, request llm.ChatRequest, root string) {
	t.Helper()
	var exposure struct {
		Version       string            `json:"version"`
		Tools         []string          `json:"tools"`
		WorkspaceRoot string            `json:"workspace_root"`
		Unavailable   map[string]string `json:"unavailable"`
	}
	found := false
	for _, message := range request.Messages {
		if message.Role != "system" || !strings.Contains(message.Content, `"version":"root_tool_exposure.v1"`) {
			continue
		}
		start := strings.Index(message.Content, "{")
		if start < 0 || json.Unmarshal([]byte(message.Content[start:]), &exposure) != nil {
			t.Fatal("invalid Go tool availability context")
		}
		found = true
	}
	if !found || exposure.WorkspaceRoot != root || len(exposure.Tools) != len(request.Tools) {
		t.Fatalf("actual request/exposure mismatch: root=%q tools=%v schemas=%d", exposure.WorkspaceRoot, exposure.Tools, len(request.Tools))
	}
	for i, spec := range request.Tools {
		if exposure.Tools[i] != spec.Name {
			t.Fatalf("exposure offered %s instead of %s", exposure.Tools[i], spec.Name)
		}
	}
	if !strings.Contains(exposure.Unavailable["command_runtime"], "Execution profile preview") || !strings.Contains(exposure.Unavailable["browser_actions"], "host has not connected") {
		t.Fatalf("missing runtime refusal reasons: %v", exposure.Unavailable)
	}
}

func TestRootFullPermissionColdProcessStopsBeforeModelAndReconfirmationRecoversFileTools(t *testing.T) {
	st, run, root, _ := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
	original := domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, DangerFullAccessEnabled: true, RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
	if _, err := application.NewRunExecutionPermissionService(st, original).Change(t.Context(), application.ChangeRunExecutionPermissionRequest{
		RunID: run.ID, Mode: "full", ConfirmFull: true, OperationKey: "initial-full-tool-exposure", RequestedBy: "test_operator", Reason: "test Full tool readiness",
	}); err != nil {
		t.Fatal(err)
	}
	// A durable Full preference survives a process restart; its grant does not.
	cold := original
	cold.RuntimeAuthority = domain.NewExecutionPermissionRuntimeAuthority()
	provider := &scriptedToolProvider{responses: []*llm.ChatResponse{
		boundaryRead("reconfirmed-read", 1), textResponse(rootActionResponse(domain.RootActionFinish, "Read the file", "done", "")),
	}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	runner := application.NewAgentRunnerWithRuntime(st, router, policy.NewDefaultChecker(), application.RunRuntimeDependencies{ExecutionCapabilities: cold}).WithGeneratedContextCompaction(false)
	if _, err := application.NewRunService(st).Start(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	_, err := runner.StepWithInput(t.Context(), run.ID, "Read README.md")
	if apperror.CodeOf(err) != apperror.CodeFailedPrecondition || !strings.Contains(err.Error(), "Reconfirm Full") || !strings.Contains(err.Error(), "Ask or Auto") || len(provider.Requests()) != 0 {
		t.Fatalf("stale Full silently sent a model request: requests=%d error=%v", len(provider.Requests()), err)
	}
	snapshot, err := st.GetRunExecutionPermission(t.Context(), run.ID)
	if err != nil || snapshot.Mode != domain.RunExecutionPermissionFull {
		t.Fatalf("preflight changed the user's permission: %+v %v", snapshot, err)
	}
	if _, err = application.NewRunExecutionPermissionService(st, cold).Change(t.Context(), application.ChangeRunExecutionPermissionRequest{
		RunID: run.ID, Mode: "full", ConfirmFull: true, OperationKey: "reconfirmed-full-tool-exposure", RequestedBy: "test_operator", Reason: "explicit same-mode Full reconfirmation",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = application.NewRunService(st).Resume(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = runner.StepWithInput(t.Context(), run.ID, "Read README.md"); err != nil {
		t.Fatal(err)
	}
	requests := provider.Requests()
	if len(requests) != 2 || !hasToolResult(requests[1], "original text") {
		t.Fatalf("activated Full did not execute the rooted read: requests=%d", len(requests))
	}
	for _, request := range requests {
		assertRootToolExposure(t, request, root)
		for _, name := range []string{"workspace_list", "workspace_read", "workspace_change", "workspace_apply"} {
			if !hasToolSpec(request, name) {
				t.Fatalf("activated Full lost %s", name)
			}
		}
	}
}

func TestRootAskAndAutoRequestToolsRemainVisibleAcrossSchedulingBoundary(t *testing.T) {
	for _, mode := range []string{"ask", "auto"} {
		t.Run(mode, func(t *testing.T) {
			st, run, root, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
			if mode == "auto" {
				if _, err := application.NewRunExecutionPermissionService(st, domain.ExecutionPermissionRuntimeCapabilities{}).Change(t.Context(), application.ChangeRunExecutionPermissionRequest{RunID: run.ID, Mode: mode, OperationKey: "auto-tool-exposure", RequestedBy: "test_operator", Reason: "Auto request tools"}); err != nil {
					t.Fatal(err)
				}
			}
			provider := &boundaryJourneyProvider{}
			provider.respond = func(_ context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
				assertRootToolExposure(t, request, root)
				if index == 5 {
					assertBoundaryPrompt(t, request)
					content := request.Messages[len(request.Messages)-1].Content
					if !strings.Contains(content, "previously offered") || !strings.Contains(content, "workspace_read") || !strings.Contains(content, "revalidate") {
						t.Fatal("scheduling boundary lost the actual suspended tool names")
					}
					return textResponse(rootActionResponse(domain.RootActionContinue, "One check remains", "", "")), nil
				}
				for _, name := range []string{"workspace_list", "workspace_read", "workspace_change", "workspace_apply"} {
					if !hasToolSpec(request, name) {
						t.Fatalf("%s request %d lost %s", mode, index, name)
					}
				}
				if index <= 4 {
					return boundaryRead(fmt.Sprintf("exposure-read-%d", index), 1), nil
				}
				if index == 6 {
					return textResponse(rootActionResponse(domain.RootActionFinish, "Read the file", "done", "")), nil
				}
				return nil, fmt.Errorf("unexpected request %d", index)
			}
			if _, err := toolBoundaryService(st, st, provider).Execute(t.Context(), input); err != nil {
				t.Fatal(err)
			}
			if len(provider.Requests()) != 6 {
				t.Fatalf("expected tools to return in the next segment, got %d requests", len(provider.Requests()))
			}
		})
	}
}
