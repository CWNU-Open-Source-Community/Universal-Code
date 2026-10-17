package application

import (
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
)

func TestAutomaticThreadTurnPreservesCompletedRunWorkingDirectory(t *testing.T) {
	f := newDrydockApplicationFixture(t, "automatic working directory")
	_, run, err := NewRunService(f.state).Create(t.Context(), CreateRunRequest{Goal: "continue existing PR", Profile: "code", Surface: "code", Phase: "deliver", WorkspaceID: f.workspace.ID, ModelRoute: "mock/mock-cyber-agent", Interactive: true, Budget: domain.Budget{MaxTurns: 8, MaxTokens: 100_000, MaxToolCalls: 32}})
	if err != nil {
		t.Fatal(err)
	}
	f.run = run
	owned := configureFileEditDrydockFixture(t, f)
	f.run, _ = f.state.GetRun(t.Context(), run.ID)
	writeDrydockTestFile(t, filepath.Join(owned.Path, "followup.txt"), "existing uncommitted work\n")
	if _, err := NewRunService(f.state).Pause(t.Context(), f.run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunService(f.state).ChangePhase(t.Context(), ChangeRunPhaseRequest{RunID: f.run.ID, Phase: "deliver", OperationKey: "automatic-workspace-deliver", RequestedBy: "operator", Reason: "complete existing work before PR followup"}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunService(f.state).Resume(t.Context(), f.run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunService(f.state).Complete(t.Context(), f.run.ID); err != nil {
		t.Fatal(err)
	}
	router := llm.NewRouter(llm.ModelRef{Provider: "mock", Model: "mock-cyber-agent"})
	router.RegisterProvider(llm.NewMockProvider())
	turns := NewThreadTurnService(f.state, NewRunLifecycleControlService(f.state), NewRunExecutionHandoffService(f.state, router, policy.NewDefaultChecker()).WithRunWorktree(f.service))
	result, err := turns.ExecuteAutomatic(t.Context(), ExecuteThreadTurnRequest{Version: domain.ThreadMessageProtocolVersion, ThreadID: domain.InitialThreadID(f.run.ID), Content: "Continue the PR verification in the existing working directory", OperationKey: "automatic-workspace-0001", RequestedBy: "pr_followup"})
	if err != nil || !result.Submission.SuccessorCreated || !result.ModelCalled {
		t.Fatalf("continued successor=%t model_called=%t err=%v", result.Submission.SuccessorCreated, result.ModelCalled, err)
	}
	physical, found, err := f.state.GetRunFileDrydock(t.Context(), result.Submission.Run.ID)
	if err != nil || !found || physical.ID != owned.ID || physical.Path != owned.Path {
		t.Fatalf("workspace=%+v found=%v err=%v", physical, found, err)
	}
	if got := readDrydockTestFile(t, filepath.Join(physical.Path, "followup.txt")); got != "existing uncommitted work\n" {
		t.Fatalf("lost existing work: %q", got)
	}
}
