package application_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/toolgateway"
)

func TestSpecialistNativeToolsPersistOwnReceiptsAndContinueWithResults(t *testing.T) {
	usage := llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}
	read := json.RawMessage(`{"version":"agent-code-tools.v1","path":"proof.txt","start_line":1,"end_line":2}`)
	write := json.RawMessage(`{"version":"agent-code-tools.v1","action":"create","path":"forbidden.txt","expected_sha256":"missing","content":"not allowed"}`)
	provider := &specialistTestProvider{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{ID: "child-read", Name: string(toolgateway.WorkspaceReadTool), Arguments: read}, {ID: "child-write", Name: string(toolgateway.WorkspaceChangeTool), Arguments: write}}, Usage: usage},
		{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue, Message: "Read proof.txt; no mutation authority."}), Usage: usage},
	}}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "proof.txt"), []byte("unique child observation\n"), 0600); err != nil {
		t.Fatal(err)
	}
	st, run, child, _ := newSubagentRunnerFixtureWithExternal(t, provider, domain.Budget{MaxTurns: 10, MaxToolCalls: 4}, 2, 64, false, root)
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	service := application.NewSpecialistExecutionService(st, router, policy.NewDefaultChecker())
	request := application.ExecuteSpecialistRequest{RunID: run.ID, AgentID: child.ID, OperationKey: "native-specialist-step-0001", WorkspaceTools: true}
	result, err := service.ExecuteSpecialist(context.Background(), request)
	if err != nil {
		t.Fatalf("native loop: %+v %v", result, err)
	}
	if result.ToolCalls != 2 || result.ToolRounds != 1 || result.ModelAttempts != 2 || result.Usage.TotalTokens != 10 || result.AttemptStatus != domain.AgentAttemptContinued {
		t.Fatalf("native accounting: %+v", result)
	}
	rounds, err := st.ListSpecialistToolRounds(t.Context(), domain.AgentAttemptRef{RunID: run.ID, AgentID: child.ID, AttemptID: result.AttemptID})
	if err != nil {
		t.Fatal(err)
	}
	if len(rounds) != 1 || len(rounds[0].Calls) != 2 || rounds[0].Calls[0].Status != domain.SupervisorToolCompleted || rounds[0].Calls[1].Status != domain.SupervisorToolFailed || rounds[0].Calls[0].ChargeID == "" || rounds[0].Calls[1].ChargeID == "" {
		t.Fatalf("native receipts: %+v", rounds)
	}
	if _, err = os.Stat(filepath.Join(root, "forbidden.txt")); !os.IsNotExist(err) {
		t.Fatalf("read-only child wrote a file: %v", err)
	}
	if len(provider.requests) != 2 || len(provider.requests[0].Tools) != 4 || provider.requests[0].Metadata["harness_workload"] != string(llm.HarnessWorkloadSpecialistTools) {
		t.Fatalf("native harness request: %+v", provider.requests)
	}
	next := provider.requests[1].Messages
	if len(next) < 2 || len(next[len(next)-2].ToolCalls) != 2 || len(next[len(next)-1].ToolResults) != 2 || !strings.Contains(next[len(next)-1].ToolResults[0].Content, "unique child observation") || !next[len(next)-1].ToolResults[1].IsError {
		t.Fatalf("native continuation did not carry exact call/result pairs: %+v", next)
	}
	budget, err := st.GetToolCallUsage(t.Context(), run.ID)
	if err != nil || budget.Consumed != 2 {
		t.Fatalf("native shared tool budget: %+v %v", budget, err)
	}
	replayed, err := service.ExecuteSpecialist(t.Context(), request)
	if err != nil || !replayed.Replayed || replayed.AttemptID != result.AttemptID || replayed.Usage != result.Usage || replayed.ModelAttempts != 2 || len(provider.requests) != 2 {
		t.Fatalf("operator replay dispatched or lost receipt: %+v %v", replayed, err)
	}
	request.WorkspaceTools = false
	if _, err = service.ExecuteSpecialist(t.Context(), request); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("operation replay widened or changed tool profile: %v", err)
	}
	request.WorkspaceTools = true
	runs := application.NewRunService(st)
	if _, err = runs.Pause(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	replayed, err = service.ExecuteSpecialist(t.Context(), request)
	if err != nil || !replayed.Replayed || replayed.ModelAttempts != 2 || len(provider.requests) != 2 {
		t.Fatalf("paused parent lost completed child replay: %+v %v", replayed, err)
	}
	if _, err = runs.Resume(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = runs.Complete(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	replayed, err = service.ExecuteSpecialist(t.Context(), request)
	if err != nil || !replayed.Replayed || replayed.ModelAttempts != 2 || len(provider.requests) != 2 {
		t.Fatalf("completed parent lost child replay: %+v %v", replayed, err)
	}
	request.OperationKey = "native-specialist-new-0001"
	if _, err = service.ExecuteSpecialist(t.Context(), request); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("terminal parent dispatched new child attempt: %v", err)
	}
}
