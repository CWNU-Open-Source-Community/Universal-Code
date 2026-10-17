package application

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/githubreview"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/scheduler"
	"cyberagent-workbench/internal/store"
)

type followupTestTurns struct {
	requests []ExecuteThreadTurnRequest
	state    string
	blocked  error
	observe  AutomaticThreadTurnObservation
	runID    string
	execute  func(context.Context, ExecuteThreadTurnRequest)
}

func (f *followupTestTurns) ExecuteAutomatic(ctx context.Context, request ExecuteThreadTurnRequest) (ExecuteThreadTurnResult, error) {
	f.requests = append(f.requests, request)
	if f.execute != nil {
		f.execute(ctx, request)
	}
	return ExecuteThreadTurnResult{Submission: SubmitThreadMessageResult{Run: domain.Run{ID: f.runID}, Message: domain.OperatorSteeringMessage{ID: "followup-message"}}}, ctx.Err()
}
func (f *followupTestTurns) CheckAutomaticTurn(context.Context, string) error { return f.blocked }
func (f *followupTestTurns) ExecutionState(context.Context, string) (ThreadExecutionState, error) {
	return ThreadExecutionState{State: f.state}, nil
}
func (f *followupTestTurns) ObserveAutomaticTurn(context.Context, string, string, string) (AutomaticThreadTurnObservation, error) {
	return f.observe, nil
}

type followupFixture struct {
	s            *PRFollowupService
	st           *store.SQLiteStore
	path, thread string
	now          time.Time
	bound        ThreadGitContext
	snapshot     githubreview.FollowupSnapshot
	turns        *followupTestTurns
	request      PRFollowupCreateRequest
	id           string
}

func newFollowupFixture(t *testing.T, include bool) *followupFixture {
	t.Helper()
	f := &followupFixture{path: filepath.Join(t.TempDir(), "followup.db"), now: time.Now().UTC()}
	var err error
	f.st, err = store.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.st.Close() })
	workspace := store.WorkspaceRecord{ID: "followup-workspace", Name: "followup", RootPath: t.TempDir(), CreatedAt: f.now}
	if err = f.st.SaveWorkspace(t.Context(), workspace); err != nil {
		t.Fatal(err)
	}
	created, err := NewControlledRunCreationService(f.st).Create(t.Context(), ControlledRunCreationRequest{Version: domain.RunCreationProtocolVersion, Goal: "follow PR", WorkspaceID: workspace.ID, OperationKey: "followup-fixture-run", RequestedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	f.thread = domain.InitialThreadID(created.Run.ID)
	f.bound = ThreadGitContext{ThreadID: f.thread, RunID: created.Run.ID, WorkspaceID: workspace.ID, RootPath: workspace.RootPath, Branch: "feature", HeadSHA: strings.Repeat("a", 40), RemoteURLs: []ThreadGitRemote{{Name: "origin", URL: "https://github.com/owner/repo.git"}}}
	repo, _ := githubreview.ParseRepository("owner/repo")
	f.snapshot = githubreview.FollowupSnapshot{ProtocolVersion: githubreview.FollowupSnapshotProtocolVersion, State: githubreview.EvidenceVerified, PullRequest: githubreview.PullRequest{Repository: repo, Number: 42, HeadSHA: f.bound.HeadSHA, HeadBranch: "feature", State: "open", URL: "https://github.com/owner/repo/pull/42"}, IssueComments: []githubreview.Comment{{ID: 1, Author: "reviewer", Body: githubreview.SanitizeRemoteText("existing comment", 1024)}}}
	f.turns = &followupTestTurns{state: "idle", runID: created.Run.ID, observe: AutomaticThreadTurnObservation{State: "completed"}}
	f.service(t)
	f.request = PRFollowupCreateRequest{Version: PRFollowupVersion, ConnectionID: "github-followup", PullRequest: 42, IntervalSeconds: 60, DeadlineAt: f.now.Add(time.Hour), MaxTurns: 3, IncludeExistingComments: include}
	value, err := f.s.Create(t.Context(), f.thread, "followup-create", f.request)
	if err != nil {
		t.Fatal(err)
	}
	f.id = value.ID
	return f
}
func (f *followupFixture) service(t *testing.T) {
	t.Helper()
	f.s = &PRFollowupService{store: f.st, turns: f.turns, now: func() time.Time { return f.now }, capture: func(context.Context, string) (ThreadGitContext, error) { return f.bound, nil }, read: func(context.Context, string, int64) (githubreview.FollowupSnapshot, error) { return f.snapshot, nil }, logs: func(context.Context, string, githubreview.FollowupSnapshot) (string, error) {
		return "fixture CI error", nil
	}}
	f.s.worker, _ = scheduler.NewWorker(f.s, scheduler.WorkerConfig{})
}
func (f *followupFixture) tick(t *testing.T) domain.PRFollowupState {
	t.Helper()
	f.now = f.now.Add(time.Minute)
	if found, err := f.s.RunDue(t.Context(), "followup-worker", f.now); err != nil || !found {
		t.Fatalf("tick claimed=%t error=%v", found, err)
	}
	state, err := f.st.GetPRFollowup(t.Context(), f.id)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestPRFollowupNewFeedbackAndCIResultDriveOneTurn(t *testing.T) {
	f := newFollowupFixture(t, false)
	if state := f.tick(t); len(f.turns.requests) != 0 || state.Record.Status != domain.PRFollowupActive {
		t.Fatalf("baseline triggered work: %+v", state.Record)
	}
	f.snapshot.IssueComments = append(f.snapshot.IssueComments, githubreview.Comment{ID: 2, Author: "reviewer", Body: githubreview.SanitizeRemoteText("Handle empty values before parsing", 1024)})
	f.snapshot.CheckRuns = []githubreview.CheckRun{{ID: 10, Name: "unit", HeadSHA: f.bound.HeadSHA, Status: "completed", Conclusion: "failure"}}
	state := f.tick(t)
	if len(f.turns.requests) != 1 || state.Record.TurnsStarted != 1 || state.Pending.State != domain.PRFollowupTurnCompleted {
		t.Fatalf("new feedback did not execute: %+v calls=%d", state.Record, len(f.turns.requests))
	}
	if !strings.Contains(f.turns.requests[0].Content, "Handle empty values") || !strings.Contains(f.turns.requests[0].Content, "fixture CI error") || strings.Contains(f.turns.requests[0].Content, "existing comment") {
		t.Fatal("wrong remote feedback entered the turn")
	}
	f.snapshot.CheckRuns[0].Conclusion = "success"
	state = f.tick(t)
	if len(f.turns.requests) != 1 || state.Record.Status != domain.PRFollowupReady {
		t.Fatalf("green CI was not read-only: %+v", state.Record)
	}
	listed, err := f.s.List(t.Context(), f.thread)
	if err != nil || len(listed.Items) != 1 || listed.Items[0].Status != domain.PRFollowupReady {
		t.Fatalf("list: %+v %v", listed, err)
	}
}

func TestPRFollowupKeepsCursorForIncompleteBusyAndBlockedTask(t *testing.T) {
	for _, kind := range []string{"partial", "active", "workspace", "budget"} {
		t.Run(kind, func(t *testing.T) {
			f := newFollowupFixture(t, true)
			switch kind {
			case "partial":
				f.snapshot.State = githubreview.EvidencePartial
			case "active":
				f.turns.state = "running"
			case "workspace":
				f.bound.RootPath = filepath.Join(f.bound.RootPath, "other")
			case "budget":
				f.turns.blocked = apperror.New(apperror.CodeResourceExhausted, "budget exhausted")
			}
			state := f.tick(t)
			if len(f.turns.requests) != 0 || len(state.Observed) != 0 || state.Record.TurnsStarted != 0 {
				t.Fatal("unadmitted feedback was consumed")
			}
			if (kind == "workspace" || kind == "budget") && state.Record.Status != domain.PRFollowupBlocked {
				t.Fatal("unavailable task was not blocked")
			}
		})
	}
}

func TestPRFollowupWaitingApprovalOnlyObservesUntilOriginalTurnFinishes(t *testing.T) {
	f := newFollowupFixture(t, true)
	f.turns.observe = AutomaticThreadTurnObservation{State: "waiting_approval"}
	state := f.tick(t)
	if state.Pending.State != domain.PRFollowupTurnWaitingApproval {
		t.Fatal("approval wait was not saved")
	}
	state = f.tick(t)
	if len(f.turns.requests) != 1 || state.Record.TurnsStarted != 1 {
		t.Fatal("approval wait submitted another turn")
	}
	f.turns.observe = AutomaticThreadTurnObservation{State: "completed"}
	state = f.tick(t)
	if state.Pending.State != domain.PRFollowupTurnCompleted || len(f.turns.requests) != 1 {
		t.Fatal("original approval completion was not observed")
	}
}

func TestPRFollowupRestartReusesExactPendingInput(t *testing.T) {
	f := newFollowupFixture(t, true)
	ctx, cancel := context.WithCancel(t.Context())
	f.turns.execute = func(context.Context, ExecuteThreadTurnRequest) { cancel() }
	if _, err := f.s.RunDue(ctx, "crashed-worker", f.now); !errors.Is(err, context.Canceled) {
		t.Fatalf("interruption: %v", err)
	}
	first := f.turns.requests[0]
	if err := f.st.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.st, err = store.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.turns.execute = nil
	f.service(t)
	state := f.tick(t)
	if len(f.turns.requests) != 2 || f.turns.requests[1].OperationKey != first.OperationKey || f.turns.requests[1].Content != first.Content || state.Record.TurnsStarted != 1 || state.Pending.State != domain.PRFollowupTurnCompleted {
		t.Fatalf("restart changed input/count: %+v", state.Record)
	}
}

func TestPRFollowupKnownApprovalFailureDoesNotReplayFailedInput(t *testing.T) {
	f := newFollowupFixture(t, true)
	f.turns.observe = AutomaticThreadTurnObservation{State: "waiting_approval"}
	f.tick(t)
	f.turns.observe = AutomaticThreadTurnObservation{State: "blocked", Reason: "approved command failed", TerminalFailure: true}
	state := f.tick(t)
	if state.Record.Status != domain.PRFollowupBlocked || state.Pending.State != domain.PRFollowupTurnFailed {
		t.Fatalf("known failure was not sealed: %+v", state)
	}
	if _, err := f.s.Transition(t.Context(), f.thread, f.id, "resume", PRFollowupTransitionRequest{Version: PRFollowupVersion, ExpectedRevision: state.Record.Revision}); err != nil {
		t.Fatal(err)
	}
	f.snapshot.IssueComments[0].Body = githubreview.SanitizeRemoteText("new feedback after manual recovery", 1024)
	f.turns.observe = AutomaticThreadTurnObservation{State: "completed"}
	state = f.tick(t)
	if len(f.turns.requests) != 2 || f.turns.requests[0].OperationKey == f.turns.requests[1].OperationKey || state.Record.TurnsStarted != 2 {
		t.Fatal("resumed follow-up replayed the sealed failure")
	}
}

func TestPRFollowupLimitsAndExpiredCreateReplay(t *testing.T) {
	f := newFollowupFixture(t, true)
	state := f.tick(t)
	// The existing record fixes its limit; repeated creates cannot reset it.
	f.now = f.request.DeadlineAt.Add(time.Second)
	if replay, err := f.s.Create(t.Context(), f.thread, "followup-create", f.request); err != nil || replay.TurnsStarted != 1 {
		t.Fatalf("expired original request: %+v %v", replay, err)
	}
	state = f.tick(t)
	if state.Record.Status != domain.PRFollowupExhausted || len(f.turns.requests) != 1 {
		t.Fatal("expired subscription executed")
	}
	if _, err := f.s.Transition(t.Context(), f.thread, state.Record.ID, "resume", PRFollowupTransitionRequest{Version: PRFollowupVersion, ExpectedRevision: state.Record.Revision}); err == nil {
		t.Fatal("expired subscription resumed")
	}
}

func TestPRFollowupRestoredQueuedTurnRechecksPR(t *testing.T) {
	for _, kind := range []string{"closed", "changed_head", "partial"} {
		t.Run(kind, func(t *testing.T) {
			f := newFollowupFixture(t, true)
			state, err := f.st.GetPRFollowup(t.Context(), f.id)
			if err != nil {
				t.Fatal(err)
			}
			state.Pending = &domain.PRFollowupTurn{ID: "queued-turn", OperationKey: "queued-turn", Content: "original feedback", State: domain.PRFollowupTurnQueued, CreatedAt: f.now, UpdatedAt: f.now}
			state.Record.TurnsStarted = 1
			if _, err = f.st.SavePRFollowup(t.Context(), state, state.Record.Revision); err != nil {
				t.Fatal(err)
			}
			want := domain.PRFollowupBlocked
			switch kind {
			case "closed":
				f.snapshot.PullRequest.State = "closed"
				want = domain.PRFollowupCompleted
			case "changed_head":
				f.snapshot.PullRequest.HeadSHA = strings.Repeat("b", 40)
				f.bound.HeadSHA = f.snapshot.PullRequest.HeadSHA
			case "partial":
				f.snapshot.State = githubreview.EvidencePartial
				want = domain.PRFollowupActive
			}
			state = f.tick(t)
			if len(f.turns.requests) != 0 || state.Record.Status != want || state.Record.TurnsStarted != 1 || state.Pending.Content != "original feedback" {
				t.Fatalf("restored work ignored remote change: %+v calls=%d", state, len(f.turns.requests))
			}
		})
	}
}

func TestPRFollowupResumeAfterReviewKeepsWatchingForNewFeedback(t *testing.T) {
	f := newFollowupFixture(t, true)
	f.tick(t)
	state := f.tick(t)
	if state.Record.Status != domain.PRFollowupReady {
		t.Fatalf("expected review: %+v", state.Record)
	}
	if _, err := f.s.Transition(t.Context(), f.thread, f.id, "resume", PRFollowupTransitionRequest{Version: PRFollowupVersion, ExpectedRevision: state.Record.Revision}); err != nil {
		t.Fatal(err)
	}
	state = f.tick(t)
	if state.Record.Status != domain.PRFollowupActive || len(f.turns.requests) != 1 || state.ReviewedTurnID != state.Pending.ID {
		t.Fatalf("reviewed result stopped watching again: %+v", state)
	}
	f.snapshot.IssueComments[0].Body = githubreview.SanitizeRemoteText("next review request", 1024)
	state = f.tick(t)
	if state.Record.TurnsStarted != 2 || len(f.turns.requests) != 2 {
		t.Fatal("new feedback after review was not executed")
	}
}

func TestPRFollowupPauseCancelsOnlyItsOwnedTurn(t *testing.T) {
	f := newFollowupFixture(t, true)
	started := make(chan struct{})
	f.turns.execute = func(ctx context.Context, _ ExecuteThreadTurnRequest) { close(started); <-ctx.Done() }
	done := make(chan error, 1)
	go func() { _, err := f.s.RunDue(t.Context(), "worker", f.now); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not start")
	}
	state, err := f.st.GetPRFollowup(t.Context(), f.id)
	if err != nil {
		t.Fatal(err)
	}
	paused, err := f.s.Transition(t.Context(), f.thread, f.id, "pause", PRFollowupTransitionRequest{Version: PRFollowupVersion, ExpectedRevision: state.Record.Revision})
	if err != nil || paused.Status != domain.PRFollowupPaused {
		t.Fatalf("pause: %+v %v", paused, err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pause did not cancel owned turn")
	}
	if _, err = f.s.Transition(t.Context(), "other-thread", f.id, "resume", PRFollowupTransitionRequest{Version: PRFollowupVersion, ExpectedRevision: paused.Revision}); apperror.CodeOf(err) != apperror.CodeNotFound {
		t.Fatalf("cross-thread control: %v", err)
	}
}

func TestPRFollowupUsesLatestWorkflowAndPreservesHistoricalCommentCursor(t *testing.T) {
	snapshot := githubreview.FollowupSnapshot{WorkflowRuns: []githubreview.FollowupWorkflowRun{
		{ID: 1, WorkflowID: 9, RunNumber: 1, RunAttempt: 1, Status: "completed", Conclusion: "failure"},
		{ID: 2, WorkflowID: 9, RunNumber: 2, RunAttempt: 1, Status: "completed", Conclusion: "success"},
	}}
	got := observePRFollowup(snapshot, map[string]string{"comment:old": "observed"}, true)
	if got.failed || got.pending || len(got.changes) != 0 || got.seen["comment:old"] != "observed" {
		t.Fatalf("wrong current workflow: %+v", got)
	}
}

func TestPRFollowupCommentRelocationDoesNotRepeatButNewLegacyFailureDoes(t *testing.T) {
	snapshot := githubreview.FollowupSnapshot{InlineComments: []githubreview.Comment{{ID: 1, Author: "reviewer", Body: githubreview.SanitizeRemoteText("handle empty values", 1024), Position: githubreview.Position{Path: "input.go", Line: 20}}}, CommitStatuses: []githubreview.FollowupCommitStatus{{ID: 10, Context: "legacy-ci", State: "failure"}}}
	first := observePRFollowup(snapshot, nil, true)
	snapshot.InlineComments[0].Position.Line = 0
	if got := observePRFollowup(snapshot, first.seen, true); len(got.changes) != 0 {
		t.Fatalf("relocation repeated feedback: %+v", got.changes)
	}
	snapshot.CommitStatuses[0].ID = 11
	if got := observePRFollowup(snapshot, first.seen, true); len(got.changes) != 1 || got.changes[0].Kind != "status" {
		t.Fatalf("new legacy failure was missed: %+v", got.changes)
	}
}

func TestPRFollowupCountsOnlyNewRepairTurns(t *testing.T) {
	f := newFollowupFixture(t, true)
	for n := 1; n <= f.request.MaxTurns; n++ {
		f.snapshot.IssueComments[0].Body = githubreview.SanitizeRemoteText(strings.Repeat("new feedback ", n), 1024)
		state := f.tick(t)
		if state.Record.TurnsStarted != n || len(f.turns.requests) != n {
			t.Fatalf("round %d: %+v", n, state.Record)
		}
	}
	f.snapshot.IssueComments[0].Body = githubreview.SanitizeRemoteText("one more issue", 1024)
	state := f.tick(t)
	if state.Record.Status != domain.PRFollowupExhausted || len(f.turns.requests) != f.request.MaxTurns {
		t.Fatalf("repair limit: %+v", state.Record)
	}
}

type followupEvidenceProvider struct {
	llm.MockProvider
	calls        int
	feedbackSeen bool
}

func (p *followupEvidenceProvider) Chat(_ context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	p.calls++
	for _, message := range request.Messages {
		if strings.Contains(message.Content, "existing comment") {
			p.feedbackSeen = true
		}
	}
	return &llm.ChatResponse{Provider: "mock", Model: request.Model, Text: `{"version":"root_lifecycle.v1","action":"finish","message":"Reviewed the PR evidence in this task.","summary":"Ready for review"}`, Usage: llm.Usage{InputTokens: 20, OutputTokens: 20, TotalTokens: 40}}, nil
}
func (p *followupEvidenceProvider) StreamChat(ctx context.Context, request llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	response, err := p.Chat(ctx, request)
	if err != nil {
		return nil, err
	}
	out := make(chan llm.ChatChunk, 1)
	out <- llm.ChatChunk{Text: response.Text, Done: true, Usage: &response.Usage}
	close(out)
	return out, nil
}

func TestPRFollowupExecutesExistingThreadAndConfirmsDurableModelTurn(t *testing.T) {
	f := newFollowupFixture(t, true)
	provider := &followupEvidenceProvider{}
	router := llm.NewDefaultRouter()
	router.RegisterProvider(provider)
	turns := NewThreadTurnService(f.st, NewRunLifecycleControlService(f.st), NewRunExecutionHandoffService(f.st, router, policy.NewDefaultChecker()))
	f.s.turns = turns
	state := f.tick(t)
	if state.Record.Status != domain.PRFollowupActive || state.Pending == nil || state.Pending.State != domain.PRFollowupTurnCompleted || provider.calls != 1 || !provider.feedbackSeen {
		t.Fatalf("PR evidence did not complete a real Thread turn: record=%+v pending=%+v calls=%d saw=%t", state.Record, state.Pending, provider.calls, provider.feedbackSeen)
	}
	message, err := f.st.GetOperatorSteering(t.Context(), state.Pending.MessageID)
	if err != nil || message.Status != domain.OperatorSteeringCommitted || message.RunID != f.bound.RunID {
		t.Fatalf("original input: %+v %v", message, err)
	}
	state = f.tick(t)
	if state.Record.Status != domain.PRFollowupReady || provider.calls != 1 {
		t.Fatal("unchanged PR restarted the model")
	}
}
