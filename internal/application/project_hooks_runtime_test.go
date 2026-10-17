package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
)

type projectHookFinishProvider struct {
	llm.MockProvider
	calls    int
	respond  func(llm.ChatRequest, int) (*llm.ChatResponse, error)
	requests []llm.ChatRequest
}

func TestProjectHookReadProposeApplyFormatAndFinishTest(t *testing.T) {
	f, trust := newProjectHookRuntimeFixture(t, domain.RunExecutionPermissionFull, true, true)
	before := "package hooksfixture\nvar Answer = 1\n"
	after := "package hooksfixture\nvar Answer=  2\n"
	if err := os.WriteFile(filepath.Join(f.root, "value.go"), []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	provider := &projectHookFinishProvider{}
	provider.respond = func(request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		name := ""
		var payload any
		switch index {
		case 0:
			name = "workspace_read"
			payload = toolgateway.WorkspaceReadPayload{Version: toolgateway.AgentCodeRegistryVersion, Path: "value.go", StartLine: 1, EndLine: 10}
		case 1:
			name = "workspace_change"
			payload = toolgateway.WorkspaceChangePayload{Version: toolgateway.AgentCodeRegistryVersion, Action: "replace", Path: "value.go", ExpectedSHA256: fileedit.HashText(before), Content: after}
		case 2:
			edits, err := f.st.ListFileEdits(t.Context(), fileedit.ListFilter{SessionID: f.turn.Run.SessionID})
			if err != nil || len(edits) != 1 {
				t.Fatalf("proposal missing: %+v %v", edits, err)
			}
			name = "workspace_apply"
			payload = toolgateway.WorkspaceApplyPayload{Version: toolgateway.AgentCodeRegistryVersion, EditID: edits[0].ID, ExpectedAction: "replace", ExpectedOriginalSHA256: edits[0].OriginalHash, ExpectedProposedSHA256: edits[0].ProposedHash}
		default:
			if index > 4 {
				t.Fatal("unexpected model loop")
			}
			content, err := os.ReadFile(filepath.Join(f.root, "value.go"))
			if err != nil || !strings.Contains(string(content), "var Answer = 2") {
				t.Fatalf("finish observed unformatted file: %q %v", content, err)
			}
			return &llm.ChatResponse{Provider: provider.Name(), Model: "fixture", Text: `{"version":"root_lifecycle.v1","action":"finish","message":"verified","summary":"verified"}`}, nil
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		return &llm.ChatResponse{Provider: provider.Name(), Model: "fixture", ToolCalls: []llm.ToolCall{{ID: "provider-hook-source", Name: name, Arguments: raw}}}, nil
	}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	f.supervisor = NewAgentRunner(f.st, router, f.checker).WithExecutionPermissionCapabilities(f.caps).WithCommandRuntime(f.service).WithProjectHooks(trust)
	lease, _, err := f.st.GetRunExecutionLease(t.Context(), f.turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.supervisor.stepWithLease(t.Context(), lease, "")
	if err != nil || result.Status != LifecycleTurnCompleted {
		t.Fatalf("full hook chain failed: %+v %v", result, err)
	}
	rounds, err := f.st.ListSupervisorToolRounds(t.Context(), f.turn.Checkpoint)
	if err != nil || len(rounds) != 5 || supervisorNativeToolRounds(rounds) != 3 || len(provider.requests) != 5 {
		t.Fatalf("rounds=%+v requests=%d err=%v", rounds, len(provider.requests), err)
	}
	if rounds[3].Calls[0].ProjectHook.Slot != "format" || rounds[4].Calls[0].ProjectHook.Slot != "test" || !projectHookCommandPassed(rounds[4].Calls[0]) {
		t.Fatal("host ordering or verification failed")
	}
	for _, request := range provider.requests {
		for _, message := range request.Messages {
			for _, call := range message.ToolCalls {
				if call.Name == "command_runtime" || call.Name == "run_tests" {
					t.Fatal("host hook forged model tool call")
				}
			}
		}
	}
}

func (*projectHookFinishProvider) Name() string { return "project-hooks-finish" }
func (p *projectHookFinishProvider) Chat(_ context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	p.calls++
	p.requests = append(p.requests, request)
	if p.respond != nil {
		return p.respond(request, p.calls-1)
	}
	return &llm.ChatResponse{Provider: p.Name(), Model: "fixture", Text: `{"version":"root_lifecycle.v1","action":"finish","message":"checked","summary":"checked"}`, Usage: llm.Usage{InputTokens: 10, OutputTokens: 10, TotalTokens: 20}}, nil
}
func (p *projectHookFinishProvider) StreamChat(ctx context.Context, request llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	response, err := p.Chat(ctx, request)
	if err != nil {
		return nil, err
	}
	chunks := make(chan llm.ChatChunk, 2)
	chunks <- llm.ChatChunk{Text: response.Text}
	chunks <- llm.FinalChatChunk(response)
	close(chunks)
	return chunks, nil
}

type projectHookTestTrust struct {
	selection ProjectHookSelection
	err       error
}

func (a *projectHookTestTrust) AuthorizeProjectHooks(context.Context, string) (ProjectHookSelection, error) {
	return a.selection, a.err
}

func newProjectHookRuntimeFixture(t *testing.T, mode domain.RunExecutionPermissionMode, format, test bool) (*commandApprovalFixture, *projectHookTestTrust) {
	t.Helper()
	f := newCommandApprovalFixture(t, mode, false)
	db, err := sql.Open("sqlite3", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var hasOrigin int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('run_supervisor_tool_rounds') WHERE name='origin'`).Scan(&hasOrigin); err != nil {
		t.Fatal(err)
	}
	if hasOrigin == 0 {
		if _, err := db.Exec(`ALTER TABLE run_supervisor_tool_rounds ADD COLUMN origin TEXT NOT NULL DEFAULT 'model'`); err != nil {
			t.Fatal(err)
		}
	}
	// The runtime patch is developed beside migration 203. IF NOT EXISTS also
	// makes this fixture use the production table after the branches integrate.
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS project_hook_calls(run_id TEXT NOT NULL,turn INTEGER NOT NULL,attempt_id TEXT NOT NULL,call_id TEXT NOT NULL,trigger_key TEXT NOT NULL,slot TEXT NOT NULL CHECK(slot IN ('format','test')),target TEXT NOT NULL,config_fingerprint TEXT NOT NULL,workspace_version TEXT NOT NULL,PRIMARY KEY(run_id,turn,attempt_id,call_id),UNIQUE(run_id,trigger_key),FOREIGN KEY(run_id,turn,attempt_id,call_id) REFERENCES run_supervisor_tool_calls(run_id,turn,attempt_id,call_id))`)
	if err != nil {
		t.Fatal(err)
	}
	pin := pinActionConfig(projectconfig.Effective{Protocol: projectconfig.ProtocolVersion, TestCommandID: "test.go", FormatCommandID: "format.go"})
	f.turn.Run.Config.ProjectConfig = pin.ProjectConfig
	f.turn.Run.Config.ProjectConfigFingerprint = pin.ProjectConfigFingerprint
	raw, _ := json.Marshal(f.turn.Run.Config)
	if _, err = db.Exec(`UPDATE runs SET config_json=? WHERE id=?`, string(raw), f.turn.Run.ID); err != nil {
		t.Fatal(err)
	}
	a := &projectHookTestTrust{selection: ProjectHookSelection{Fingerprint: strings.Repeat("a", 64), AfterEditFormat: format, BeforeFinishTest: test}}
	f.supervisor.WithProjectHooks(a)
	for name, content := range map[string]string{"go.mod": "module hooksfixture\n\ngo 1.23\n", "fixture_test.go": "package hooksfixture\nimport \"testing\"\nfunc TestFixture(t *testing.T) {}\n"} {
		if err := os.WriteFile(filepath.Join(f.root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return f, a
}

func projectHookModelBoundary(t *testing.T, f *commandApprovalFixture, number, round int) modelCallResult {
	t.Helper()
	attempt := llm.ModelAttempt{Number: number, ToolRound: round, TransportAttempt: 1, MaxAttempts: 1, Provider: "offline-command-fixture", Model: "fixture"}
	if _, err := f.st.RecordSupervisorModelStarted(t.Context(), f.turn.Checkpoint, attempt); err != nil {
		t.Fatal(err)
	}
	response := llm.ChatResponse{Provider: attempt.Provider, Model: attempt.Model, Text: `{"version":"root_lifecycle.v1","action":"finish","message":"done","summary":"done"}`}
	return modelCallResult{Attempt: attempt, Response: &response}
}

func projectHookFinish(t *testing.T, f *commandApprovalFixture, number int, rounds []domain.SupervisorToolRound) (LifecycleResult, bool, error) {
	t.Helper()
	model := projectHookModelBoundary(t, f, number, len(rounds))
	prepared := &agentTurnPreparation{toolRounds: rounds}
	return f.supervisor.commitAgentRootAction(t.Context(), &f.turn, LifecycleResult{Checkpoint: f.turn.Checkpoint}, model,
		domain.RootAction{Version: "root_lifecycle.v1", Kind: domain.RootActionFinish, Message: "done", Summary: "done"}, rootActionTrailingCommentaryRecovery{}, prepared)
}

func TestProjectHookBeforeFinishRunsRealTestsAndDeduplicates(t *testing.T) {
	f, _ := newProjectHookRuntimeFixture(t, domain.RunExecutionPermissionFull, false, true)
	first, retry, err := projectHookFinish(t, f, 1, nil)
	if err != nil || !retry || first.Status == LifecycleTurnCompleted {
		t.Fatalf("first=%+v retry=%v err=%v", first, retry, err)
	}
	rounds, err := f.st.ListSupervisorToolRounds(t.Context(), f.turn.Checkpoint)
	if err != nil || len(rounds) != 1 || !rounds[0].ProjectHook() || !projectHookCommandPassed(rounds[0].Calls[0]) {
		t.Fatalf("rounds=%+v err=%v", rounds, err)
	}
	request, err := f.supervisor.requestWithSupervisorToolRounds(t.Context(), f.turn.Checkpoint, llm.ChatRequest{}, rounds)
	if err != nil || len(request.Messages) != 1 || len(request.Messages[0].ToolCalls) != 0 || request.Messages[0].Role == "assistant" {
		t.Fatalf("host action forged model output: %+v %v", request, err)
	}
	last, retry, err := projectHookFinish(t, f, 2, rounds)
	if err != nil || retry || last.Status != LifecycleTurnCompleted {
		t.Fatalf("last=%+v retry=%v err=%v", last, retry, err)
	}
	jobs, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.turn.Run.ID, Limit: 10})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("duplicate validation jobs=%+v err=%v", jobs, err)
	}
}

func TestProjectHookFailedTestsBlockFinishAndChangedVersionCanRetry(t *testing.T) {
	f, _ := newProjectHookRuntimeFixture(t, domain.RunExecutionPermissionFull, false, true)
	if err := os.WriteFile(filepath.Join(f.root, "fixture_test.go"), []byte("package hooksfixture\nimport \"testing\"\nfunc TestFixture(t *testing.T) { t.Fatal(\"expected failure\") }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, retry, err := projectHookFinish(t, f, 1, nil)
	if err != nil || !retry {
		t.Fatalf("retry=%v err=%v", retry, err)
	}
	rounds, _ := f.st.ListSupervisorToolRounds(t.Context(), f.turn.Checkpoint)
	if projectHookCommandPassed(rounds[0].Calls[0]) {
		t.Fatal("failed test considered passed")
	}
	_, added, _, err := f.supervisor.beforeProjectHookFinish(t.Context(), f.turn, rounds, nil)
	if added || apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("failed version allowed finish: %v %v", added, err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "fixture_test.go"), []byte("package hooksfixture\nimport \"testing\"\nfunc TestFixture(t *testing.T) {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	model := projectHookModelBoundary(t, f, 2, len(rounds))
	model.Attempt.Outcome = llm.OutcomeSuccess
	f.turn.Checkpoint, err = f.st.RecordSupervisorModelCompleted(t.Context(), f.turn.Checkpoint, model.Attempt, *model.Response)
	if err != nil {
		t.Fatal(err)
	}
	rounds, added, waiting, err := f.supervisor.beforeProjectHookFinish(t.Context(), f.turn, rounds, nil)
	if err != nil || !added || waiting || !projectHookCommandPassed(rounds[len(rounds)-1].Calls[0]) {
		t.Fatalf("repair not verified: %+v %v %v %v", rounds, added, waiting, err)
	}
}

func TestProjectHookAskResumeRetainsIntentAndRechecksTrust(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "approved", true: "trust_revoked"}[changed], func(t *testing.T) {
			f, trust := newProjectHookRuntimeFixture(t, domain.RunExecutionPermissionAsk, false, true)
			result, retry, err := projectHookFinish(t, f, 1, nil)
			if err != nil || retry || result.RunStatus != domain.RunWaitingApproval {
				t.Fatalf("result=%+v retry=%v err=%v", result, retry, err)
			}
			rounds, _ := f.st.ListSupervisorToolRounds(t.Context(), f.turn.Checkpoint)
			f.call = rounds[0].Calls[0]
			f.decide(t, ApprovalControlApproveOnce)
			if changed {
				trust.selection = ProjectHookSelection{}
			}
			// Reconstruct the runner, release the old execution lease and enter
			// the public ResumeApproval path for the original durable call.
			provider := &projectHookFinishProvider{}
			router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "fixture"})
			router.RegisterProvider(provider)
			f.supervisor = NewAgentRunner(f.st, router, f.checker).WithExecutionPermissionCapabilities(f.caps).WithCommandRuntime(f.service).WithProjectHooks(trust)
			lease, _, err := f.st.GetRunExecutionLease(t.Context(), f.turn.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = f.st.ReleaseRunExecutionLease(t.Context(), lease); err != nil {
				t.Fatal(err)
			}
			handoff := &RunExecutionHandoffService{store: f.st, supervisor: f.supervisor}
			turns := NewThreadTurnServiceWithExecutionCapabilities(f.st, NewRunLifecycleControlService(f.st), handoff, f.caps)
			resume := ApprovalContinuationRequest{RunID: f.turn.Run.ID, Kind: "command_runtime", ProposalID: f.call.CallID}
			continued := turns.ResumeApproval(t.Context(), resume)
			jobs, _ := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.turn.Run.ID, Limit: 10})
			if changed {
				if continued.State != "failed" || len(jobs) != 0 || provider.calls != 0 {
					t.Fatalf("changed trust dispatched: %+v %+v calls=%d", continued, jobs, provider.calls)
				}
				return
			}
			if continued.State != "completed" || len(jobs) != 1 || provider.calls != 1 {
				checkpoint, _, _ := f.st.GetSupervisorCheckpoint(t.Context(), f.turn.Run.ID)
				t.Fatalf("resume: %+v checkpoint=%+v calls=%d", continued, checkpoint, provider.calls)
			}
			turns.ResumeApproval(t.Context(), resume)
			again, _ := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.turn.Run.ID, Limit: 10})
			if len(again) != 1 || provider.calls != 1 {
				t.Fatal("approval replay duplicated command or model")
			}

		})
	}
}

func TestProjectHookAppliedFileFormatsOnce(t *testing.T) {
	f, _ := newProjectHookRuntimeFixture(t, domain.RunExecutionPermissionFull, true, false)
	path := filepath.Join(f.root, "format.go")
	text := "package hooksfixture\nvar X=  1\n"
	if err := os.WriteFile(path, []byte("package hooksfixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	edit, err := fileedit.NewManager(f.st).Propose(t.Context(), fileedit.Proposal{SessionID: f.turn.Run.SessionID, WorkspaceID: f.turn.Mission.WorkspaceID, WorkspaceRoot: f.root, Path: "format.go", ProposedText: text})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileEditReviewService(f.st).Review(t.Context(), ReviewFileEditRequest{Version: FileEditReviewProtocolVersion, RunID: f.turn.Run.ID, EditID: edit.ID, Action: FileEditApproveIntent}); err != nil {
		t.Fatal(err)
	}
	permission, err := f.st.GetRunExecutionPermission(t.Context(), f.turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshotID, generation, epoch, fence, _ := bindAgentCodeRuntime(f.caps, permission)
	_, err = NewFileEditApplyService(f.st, f.checker).WithExecutionPermissionCapabilities(f.caps).Apply(t.Context(), ApplyFileEditRequest{Version: fileedit.FileEditApplyProtocolVersion, RunID: f.turn.Run.ID, EditID: edit.ID, OperationKey: "project-hook-edit", AppliedBy: "test_operator", LeaseID: f.turn.Checkpoint.LeaseID, LeaseGeneration: f.turn.Checkpoint.LeaseGeneration, PermissionSnapshotID: snapshotID, PermissionGeneration: generation, PermissionRuntimeEpoch: epoch, RunAuthorizationFence: fence})
	if err != nil {
		t.Fatal(err)
	}

	_, retry, err := projectHookFinish(t, f, 1, nil)
	if err != nil || !retry {
		t.Fatalf("retry=%v err=%v", retry, err)
	}
	formatted, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(formatted), "var X = 1") {
		t.Fatalf("gofmt not executed: %s %v", formatted, err)
	}
	rounds, _ := f.st.ListSupervisorToolRounds(t.Context(), f.turn.Checkpoint)
	if _, waiting, err := f.supervisor.resumeSupervisorTools(t.Context(), f.turn, rounds); err != nil || waiting {
		t.Fatalf("replay %v %v", waiting, err)
	}
	jobs, _ := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.turn.Run.ID, Limit: 10})
	if len(jobs) != 1 {
		t.Fatal("format recursed or duplicated")
	}
}
