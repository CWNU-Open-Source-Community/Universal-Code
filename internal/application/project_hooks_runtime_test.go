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
	"cyberagent-workbench/internal/workspacecheckpoint"
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
	for _, state := range []string{"approved", "disabled", "disabled_denied", "changed"} {
		t.Run(state, func(t *testing.T) {
			f, trust := newProjectHookRuntimeFixture(t, domain.RunExecutionPermissionAsk, false, true)
			result, retry, err := projectHookFinish(t, f, 1, nil)
			if err != nil || retry || result.RunStatus != domain.RunWaitingApproval {
				t.Fatalf("result=%+v retry=%v err=%v", result, retry, err)
			}
			rounds, _ := f.st.ListSupervisorToolRounds(t.Context(), f.turn.Checkpoint)
			f.call = rounds[0].Calls[0]
			decision := ApprovalControlApproveOnce
			if state == "disabled_denied" {
				decision = ApprovalControlDeny
			}
			f.decide(t, decision)
			if strings.HasPrefix(state, "disabled") {
				trust.selection = ProjectHookSelection{}
			}
			if state == "changed" {
				trust.err = apperror.New(apperror.CodeFailedPrecondition, "project hook configuration changed")
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
			if state == "changed" {
				if continued.State != "failed" || len(jobs) != 0 || provider.calls != 0 {
					t.Fatalf("changed trust dispatched: %+v %+v calls=%d", continued, jobs, provider.calls)
				}
				return
			}
			wantJobs := 1
			if strings.HasPrefix(state, "disabled") {
				wantJobs = 0
				recovered, _ := f.st.ListSupervisorToolRounds(t.Context(), f.turn.Checkpoint)
				if len(recovered) != 1 || recovered[0].Calls[0].Status != domain.SupervisorToolDenied {
					checkpoint, _, _ := f.st.GetSupervisorCheckpoint(t.Context(), f.turn.Run.ID)
					t.Fatalf("disabled hook left pending: continuation=%+v checkpoint=%+v rounds=%+v", continued, checkpoint, recovered)
				}
			}
			if continued.State != "completed" || len(jobs) != wantJobs || provider.calls != 1 {
				checkpoint, _, _ := f.st.GetSupervisorCheckpoint(t.Context(), f.turn.Run.ID)
				t.Fatalf("resume: %+v checkpoint=%+v calls=%d", continued, checkpoint, provider.calls)
			}
			turns.ResumeApproval(t.Context(), resume)
			again, _ := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.turn.Run.ID, Limit: 10})
			if len(again) != wantJobs || provider.calls != 1 {
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

func TestProjectHookWaitsForContextPinConfirmation(t *testing.T) {
	f, trust := newProjectHookRuntimeFixture(t, domain.RunExecutionPermissionFull, true, true)
	path := filepath.Join(f.root, "format.go")
	const unformatted = "package hooksfixture\nvar X=  1\n"
	if err := os.WriteFile(path, []byte("package hooksfixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	edit, err := fileedit.NewManager(f.st).Propose(t.Context(), fileedit.Proposal{SessionID: f.turn.Run.SessionID, WorkspaceID: f.turn.Mission.WorkspaceID, WorkspaceRoot: f.root, Path: "format.go", ProposedText: unformatted})
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
	snapshot, generation, epoch, fence, _ := bindAgentCodeRuntime(f.caps, permission)
	if _, err := NewFileEditApplyService(f.st, f.checker).WithExecutionPermissionCapabilities(f.caps).Apply(t.Context(), ApplyFileEditRequest{Version: fileedit.FileEditApplyProtocolVersion, RunID: f.turn.Run.ID, EditID: edit.ID, OperationKey: "pin-confirmation-hook-edit", AppliedBy: "test_operator", LeaseID: f.turn.Checkpoint.LeaseID, LeaseGeneration: f.turn.Checkpoint.LeaseGeneration, PermissionSnapshotID: snapshot, PermissionGeneration: generation, PermissionRuntimeEpoch: epoch, RunAuthorizationFence: fence}); err != nil {
		t.Fatal(err)
	}
	provider := &projectHookFinishProvider{}
	provider.respond = func(request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index == 0 {
			return &llm.ChatResponse{Provider: provider.Name(), Model: "fixture", ToolCalls: []llm.ToolCall{{ID: "pin-before-hooks", Name: "context_pin_propose", Arguments: json.RawMessage(`{"version":"context_pin_proposal.v1","text":"Keep the interface stable","question":"Pin this?"}`)}}}, nil
		}
		if index != 1 || len(request.Tools) != 0 {
			t.Fatalf("pin confirmation did not reach a tool-free wait: index=%d tools=%d", index, len(request.Tools))
		}
		return &llm.ChatResponse{Provider: provider.Name(), Model: "fixture", Text: `{"version":"root_lifecycle.v1","action":"wait","message":"Pin this?","reason":"user context pin confirmation required"}`}, nil
	}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	f.supervisor = NewAgentRunner(f.st, router, f.checker).WithExecutionPermissionCapabilities(f.caps).WithCommandRuntime(f.service).WithProjectHooks(trust)
	lease, _, err := f.st.GetRunExecutionLease(t.Context(), f.turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.supervisor.stepWithLease(t.Context(), lease, "")
	if err != nil || result.RequestedAction != domain.RootActionWait || len(provider.requests) != 2 {
		t.Fatalf("pin did not wait: result=%+v requests=%d err=%v", result, len(provider.requests), err)
	}
	state, err := f.st.ThreadContextPinsForRun(t.Context(), f.turn.Run.ID)
	if err != nil || len(state.Proposals) != 1 || state.Proposals[0].Status != "pending" || len(state.Pins) != 0 {
		t.Fatalf("pin proposal not pending: %+v %v", state, err)
	}
	jobs, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.turn.Run.ID, Limit: 10})
	if err != nil || len(jobs) != 0 {
		t.Fatalf("host hook ran while awaiting pin confirmation: %+v %v", jobs, err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != unformatted {
		t.Fatalf("formatting crossed the confirmation boundary: %q %v", content, err)
	}
}

func TestProjectHookWorkspaceVersionRejectsIncompleteContents(t *testing.T) {
	for _, reason := range []string{"workspace manifest exceeds the entry limit", "case-colliding Workspace paths cannot be restored safely", "Workspace path casing differs from the Git index"} {
		if _, err := projectHookSnapshotVersion(workspacecheckpoint.Snapshot{Checkpoint: workspacecheckpoint.Checkpoint{IncompleteReasons: []string{reason}}}); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
			t.Fatalf("accepted %s: %v", reason, err)
		}
	}
	for _, policy := range []workspacecheckpoint.StoragePolicy{workspacecheckpoint.StorageUnreadable, workspacecheckpoint.StorageExcludedLink, workspacecheckpoint.StorageExcludedSpecial} {
		if _, err := projectHookSnapshotVersion(workspacecheckpoint.Snapshot{Entries: []workspacecheckpoint.Entry{{Path: "file.go", State: workspacecheckpoint.StatePresent, StoragePolicy: policy, WorktreeSHA256: "missing"}}}); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
			t.Fatalf("accepted %s: %v", policy, err)
		}
	}
	if _, err := projectHookSnapshotVersion(workspacecheckpoint.Snapshot{Checkpoint: workspacecheckpoint.Checkpoint{RecoveryLevel: workspacecheckpoint.RecoveryUnavailable, IncompleteReasons: []string{"workspace is not an exact Git worktree"}}, Entries: []workspacecheckpoint.Entry{{Path: "deleted.go", Tracked: true, State: workspacecheckpoint.StateMissing, StoragePolicy: workspacecheckpoint.StorageMissing, WorktreeSHA256: "missing"}}}); err != nil {
		t.Fatalf("complete non-Git inventory or tracked deletion rejected: %v", err)
	}
}

func TestProjectHookMoveAndDeleteSupersedeOldFormatTargets(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(map[bool]string{false: "move", true: "move_then_delete"}[deleted], func(t *testing.T) {
			f, _ := newProjectHookRuntimeFixture(t, domain.RunExecutionPermissionFull, true, false)
			if err := os.WriteFile(filepath.Join(f.root, "a.go"), []byte("package hooksfixture\n"), 0600); err != nil {
				t.Fatal(err)
			}
			apply := func(operation, path, destination, content string) {
				t.Helper()
				destinationHash := ""
				if operation == fileedit.OperationMove {
					destinationHash = "missing"
				}
				edit, err := fileedit.NewManager(f.st).Propose(t.Context(), fileedit.Proposal{SessionID: f.turn.Run.SessionID, WorkspaceID: f.turn.Mission.WorkspaceID, WorkspaceRoot: f.root, Path: path, Operation: operation, DestinationPath: destination, ProposedText: content, ExpectedDestinationHash: destinationHash})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = NewFileEditReviewService(f.st).Review(t.Context(), ReviewFileEditRequest{Version: FileEditReviewProtocolVersion, RunID: f.turn.Run.ID, EditID: edit.ID, Action: FileEditApproveIntent}); err != nil {
					t.Fatal(err)
				}
				permission, err := f.st.GetRunExecutionPermission(t.Context(), f.turn.Run.ID)
				if err != nil {
					t.Fatal(err)
				}
				snapshot, generation, epoch, fence, _ := bindAgentCodeRuntime(f.caps, permission)
				_, err = NewFileEditApplyService(f.st, f.checker).WithExecutionPermissionCapabilities(f.caps).Apply(t.Context(), ApplyFileEditRequest{Version: fileedit.FileEditApplyProtocolVersion, RunID: f.turn.Run.ID, EditID: edit.ID, OperationKey: "hook-" + edit.ID, AppliedBy: "test_operator", LeaseID: f.turn.Checkpoint.LeaseID, LeaseGeneration: f.turn.Checkpoint.LeaseGeneration, PermissionSnapshotID: snapshot, PermissionGeneration: generation, PermissionRuntimeEpoch: epoch, RunAuthorizationFence: fence})
				if err != nil {
					t.Fatal(err)
				}
			}
			apply(fileedit.OperationReplace, "a.go", "", "package hooksfixture\nvar X=  1\n")
			apply(fileedit.OperationMove, "a.go", "b.go", "")
			if deleted {
				apply(fileedit.OperationDelete, "b.go", "", "")
			}
			_, retry, err := projectHookFinish(t, f, 1, nil)
			if err != nil || retry == deleted {
				t.Fatalf("retry=%v err=%v", retry, err)
			}
			rounds, err := f.st.ListSupervisorToolRounds(t.Context(), f.turn.Checkpoint)
			if err != nil {
				t.Fatal(err)
			}
			if deleted {
				if len(rounds) != 0 {
					t.Fatal("formatted deleted source")
				}
				return
			}
			if len(rounds) != 1 || rounds[0].Calls[0].ProjectHook.Target != "b.go" || !projectHookCommandPassed(rounds[0].Calls[0]) {
				t.Fatalf("wrong formatting: %+v", rounds)
			}
		})
	}
}

func TestProjectHookFormatCannotCertifyStandardCodeVerification(t *testing.T) {
	machine, _ := newStandardCodeSupervisorTestMachine(domain.StandardCodeSupervisorExecute, domain.ExecutionPhaseDeliver)
	machine.snapshot.MutationEpoch = 1
	machine.snapshot.VerifiedMutationEpoch = 1
	machine.snapshot.VerificationJobIDs = []string{"old-test"}
	call := standardCodeCommandCall(t, "format-hook", toolgateway.CommandRuntimeActionRun, "gofmt -w value.go", "", 0)
	call.ProjectHook = &domain.ProjectHookCallOrigin{Slot: "format"}
	exit := 0
	standardCodeObserveCompleted(t, machine, call, nil, standardCodeCommandOutput(t, toolgateway.CommandRuntimeActionRun, []runner.CommandRuntimeJobSnapshot{{ID: "formatter", State: runner.CommandRuntimeJobCompleted, ExitCode: &exit, TreeReaped: true}}, nil))
	if machine.snapshot.MutationEpoch != 2 || machine.snapshot.VerifiedMutationEpoch != 1 || len(machine.snapshot.VerificationJobIDs) != 0 || machine.snapshot.State != domain.StandardCodeSupervisorExecute {
		t.Fatalf("formatter certified verification: %+v", machine.snapshot)
	}
}

func TestProjectHookFailedTestRepairsAcrossNativeRoundBoundary(t *testing.T) {
	f, trust := newProjectHookRuntimeFixture(t, domain.RunExecutionPermissionFull, true, true)
	before := "package hooksfixture\nvar Answer = 1\n"
	after := "package hooksfixture\nvar Answer=  2\n"
	if err := os.WriteFile(filepath.Join(f.root, "value.go"), []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "fixture_test.go"), []byte("package hooksfixture\nimport \"testing\"\nfunc TestFixture(t *testing.T) { if Answer != 1 { t.Fatal(Answer) } }\n"), 0600); err != nil {
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
		case 2, 6:
			edits, err := f.st.ListFileEdits(t.Context(), fileedit.ListFilter{SessionID: f.turn.Run.SessionID})
			if err != nil || len(edits) != map[int]int{2: 1, 6: 2}[index] {
				t.Fatalf("proposal missing: %+v %v", edits, err)
			}
			name = "workspace_apply"
			payload = toolgateway.WorkspaceApplyPayload{Version: toolgateway.AgentCodeRegistryVersion, EditID: edits[0].ID, ExpectedAction: "replace", ExpectedOriginalSHA256: edits[0].OriginalHash, ExpectedProposedSHA256: edits[0].ProposedHash}
		case 4:
			content, err := os.ReadFile(filepath.Join(f.root, "value.go"))
			if err != nil {
				t.Fatal(err)
			}
			name = "workspace_change"
			payload = toolgateway.WorkspaceChangePayload{Version: toolgateway.AgentCodeRegistryVersion, Action: "replace", Path: "value.go", ExpectedSHA256: fileedit.HashText(string(content)), Content: "package hooksfixture\nvar Answer=  1\n"}
		case 5:
			if len(request.Tools) != 0 {
				t.Fatal("native fourth round did not close tool dispatch")
			}
			return &llm.ChatResponse{Provider: provider.Name(), Model: "fixture", Text: `{"version":"root_lifecycle.v1","action":"continue","message":"Apply the prepared repair in the next turn."}`}, nil
		default:
			if index > 8 {
				t.Fatal("unexpected model loop")
			}
			content, err := os.ReadFile(filepath.Join(f.root, "value.go"))
			if err != nil || !strings.Contains(string(content), map[bool]string{true: "var Answer = 2", false: "var Answer = 1"}[index == 3]) {
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
	if err != nil || result.Status != LifecycleTurnCompleted || result.Action.Kind != domain.RootActionContinue {
		t.Fatalf("failed verification boundary: %+v %v", result, err)
	}
	rounds, err := f.st.ListSupervisorToolRounds(t.Context(), f.turn.Checkpoint)
	if err != nil || supervisorNativeToolRounds(rounds) != 4 || len(rounds) != 6 || projectHookCommandPassed(rounds[4].Calls[0]) {
		t.Fatalf("native cap/failing test lost: %+v %v", rounds, err)
	}
	result, err = f.supervisor.stepWithLease(t.Context(), lease, "")
	if err != nil || result.Status != LifecycleTurnCompleted || result.Action.Kind != domain.RootActionFinish {
		t.Fatalf("repair continuation failed: %+v %v", result, err)
	}
	jobs, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.turn.Run.ID, Limit: 10})
	if err != nil || len(jobs) != 4 || provider.calls != 9 {
		t.Fatalf("repair did not format and retest once: jobs=%+v calls=%d err=%v", jobs, provider.calls, err)
	}
}

func TestProjectHookDisabledDispatchedCallRemainsUnknown(t *testing.T) {
	f, trust := newProjectHookRuntimeFixture(t, domain.RunExecutionPermissionAsk, false, true)
	if _, _, err := projectHookFinish(t, f, 1, nil); err != nil {
		t.Fatal(err)
	}
	rounds, err := f.st.ListSupervisorToolRounds(t.Context(), f.turn.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	f.call = rounds[0].Calls[0]
	f.decide(t, ApprovalControlApproveOnce)
	if _, err = f.st.RecordSupervisorToolExecutionStarted(t.Context(), f.turn.Checkpoint, f.call.CallID); err != nil {
		t.Fatal(err)
	}
	trust.selection = ProjectHookSelection{}
	if disabled, err := f.supervisor.checkPendingProjectHook(t.Context(), f.call); disabled || apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("uncertain dispatch was declared cancelled: %v %v", disabled, err)
	}
}
