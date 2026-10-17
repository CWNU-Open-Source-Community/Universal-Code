package application_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
)

func automaticTurnFixture(t *testing.T, budget domain.Budget) (*store.SQLiteStore, *application.ThreadTurnService, *lifecycleProvider, application.ExecuteThreadTurnRequest, domain.Run) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "automatic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{Goal: "follow existing PR", Profile: "review", ModelRoute: "lifecycle-test/model", Interactive: true, Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	provider := &lifecycleProvider{responses: []string{rootActionResponse(domain.RootActionFinish, "ready for review", "done", ""), rootActionResponse(domain.RootActionFinish, "followup ready", "done", "")}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	turns := application.NewThreadTurnService(st, application.NewRunLifecycleControlService(st), application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker()))
	request := application.ExecuteThreadTurnRequest{Version: domain.ThreadMessageProtocolVersion, ThreadID: domain.InitialThreadID(run.ID), Content: "Repair the changed PR checks and verify", OperationKey: "automatic-first-0001", RequestedBy: "pr_followup"}
	return st, turns, provider, request, run
}

func TestAutomaticThreadTurnExhaustionDoesNotRefillAndCompletedReplaySurvives(t *testing.T) {
	st, turns, provider, request, run := automaticTurnFixture(t, domain.Budget{MaxTurns: 1})
	first, err := turns.ExecuteAutomatic(t.Context(), request)
	if err != nil || !first.ModelCalled {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	observed, observeErr := turns.ObserveAutomaticTurn(t.Context(), request.ThreadID, run.ID, first.Submission.Message.ID)
	if observeErr != nil || observed.State != "completed" {
		t.Fatalf("completed observation=%+v err=%v", observed, observeErr)
	}
	next := request
	next.OperationKey = "automatic-next-0002"
	if _, err := turns.ExecuteAutomatic(t.Context(), next); apperror.CodeOf(err) != apperror.CodeResourceExhausted {
		t.Fatalf("exhaustion=%v", err)
	}
	thread, _ := st.GetThread(t.Context(), request.ThreadID)
	if thread.ActiveRunID != run.ID || provider.calls != 1 {
		t.Fatalf("automatic budget refill: thread=%+v calls=%d", thread, provider.calls)
	}
	facts, err := st.GetThreadExecutionFacts(t.Context(), []string{request.ThreadID})
	if err != nil || facts[request.ThreadID].Unsettled {
		t.Fatalf("rejected automatic input left unfinished work: %+v %v", facts, err)
	}
	replay, err := turns.ExecuteAutomatic(t.Context(), request)
	if err != nil || !replay.Replayed || provider.calls != 1 {
		t.Fatalf("replay=%+v err=%v calls=%d", replay, err, provider.calls)
	}
	explicit, err := turns.Execute(t.Context(), next)
	if err != nil || explicit.Submission.Run.ID == run.ID || provider.calls != 2 {
		t.Fatalf("explicit behavior changed: result=%+v err=%v calls=%d", explicit, err, provider.calls)
	}
}

func TestAutomaticThreadTurnObservesExistingApprovalContinuation(t *testing.T) {
	for _, failContinuation := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "failed"}[failContinuation], func(t *testing.T) {
			st, run, _, request := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
			p := &boundaryJourneyProvider{}
			p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
				switch index {
				case 1:
					return boundaryPropose("automatic-approval-proposal"), nil
				case 2:
					return textResponse(rootActionResponse(domain.RootActionWait, "Await review", "", "operator review required")), nil
				default:
					if failContinuation {
						return nil, apperror.New(apperror.CodeFailedPrecondition, "approval continuation provider failed")
					}
					return textResponse(rootActionResponse(domain.RootActionFinish, "Denial observed", "done", "")), nil
				}
			}
			turns := toolBoundaryServiceWithPolicy(st, st, p, &fileOperationPolicy{review: true})
			result, err := turns.ExecuteAutomatic(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			observed, err := turns.ObserveAutomaticTurn(t.Context(), request.ThreadID, run.ID, result.Submission.Message.ID)
			if err != nil || observed.State != "waiting_approval" {
				t.Fatalf("waiting observation=%+v err=%v", observed, err)
			}
			edits, err := st.ListFileEdits(t.Context(), fileedit.ListFilter{SessionID: run.SessionID})
			if err != nil || len(edits) != 1 {
				t.Fatalf("proposals=%+v err=%v", edits, err)
			}
			if _, err := application.NewFileEditReviewService(st).Review(t.Context(), application.ReviewFileEditRequest{Version: application.FileEditReviewProtocolVersion, RunID: run.ID, EditID: edits[0].ID, Action: application.FileEditDeny}); err != nil {
				t.Fatal(err)
			}
			resumed := turns.ResumeApproval(t.Context(), application.ApprovalContinuationRequest{RunID: run.ID, Kind: "file_edit", ProposalID: edits[0].ID})
			if failContinuation {
				if resumed.State != "failed" {
					t.Fatalf("expected failed approval continuation: %+v", resumed)
				}
				observed, err = turns.ObserveAutomaticTurn(t.Context(), request.ThreadID, run.ID, result.Submission.Message.ID)
				if err != nil || observed.State != "blocked" || !observed.TerminalFailure || len(p.Requests()) != 3 {
					t.Fatalf("failed approval observation=%+v err=%v model_calls=%d", observed, err, len(p.Requests()))
				}
				return
			}
			if resumed.State != "completed" || !resumed.ModelCalled {
				t.Fatalf("resume=%+v", resumed)
			}
			observed, err = turns.ObserveAutomaticTurn(t.Context(), request.ThreadID, run.ID, result.Submission.Message.ID)
			if err != nil || observed.State != "completed" || len(p.Requests()) != 3 {
				t.Fatalf("completed observation=%+v err=%v model_calls=%d", observed, err, len(p.Requests()))
			}
		})
	}
}

func TestAutomaticThreadTurnRespectsStoppedFailedAndWaitingRuns(t *testing.T) {
	for _, status := range []domain.RunStatus{domain.RunPaused, domain.RunCancelled, domain.RunFailed, domain.RunWaitingApproval} {
		t.Run(string(status), func(t *testing.T) {
			st, turns, provider, request, run := automaticTurnFixture(t, domain.Budget{MaxTurns: 3})
			runs := application.NewRunService(st)
			if _, err := runs.Start(t.Context(), run.ID); err != nil {
				t.Fatal(err)
			}
			var err error
			switch status {
			case domain.RunPaused:
				_, err = runs.Pause(t.Context(), run.ID)
			case domain.RunCancelled:
				_, err = runs.Cancel(t.Context(), run.ID)
			case domain.RunFailed:
				_, err = runs.Fail(t.Context(), run.ID, "failed work")
			case domain.RunWaitingApproval:
				provider.responses = []string{rootActionResponse(domain.RootActionWait, "review this change", "", "operator approval")}
				var waiting application.ExecuteThreadTurnResult
				waiting, err = turns.Execute(t.Context(), request)
				if err == nil {
					observed, observeErr := turns.ObserveAutomaticTurn(t.Context(), request.ThreadID, run.ID, waiting.Submission.Message.ID)
					if observeErr != nil || observed.State != "blocked" || observed.TerminalFailure {
						t.Fatalf("root wait observation=%+v err=%v", observed, observeErr)
					}
				}
				request.OperationKey = "automatic-after-wait"
			}
			if err != nil {
				t.Fatal(err)
			}
			before, _ := st.GetThread(t.Context(), request.ThreadID)
			calls := provider.calls
			if _, err := turns.ExecuteAutomatic(t.Context(), request); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
				t.Fatalf("status %s: %v", status, err)
			}
			after, _ := st.GetThread(t.Context(), request.ThreadID)
			if before.ActiveRunID != after.ActiveRunID || before.LastRunID != after.LastRunID || provider.calls != calls {
				t.Fatalf("automatic continuation crossed %s", status)
			}
		})
	}
}

func TestAutomaticThreadTurnDoesNotQueueBehindActiveUser(t *testing.T) {
	provider := blockingProvider{started: make(chan struct{}, 1)}
	st, turns, request := threadControlFixture(t, provider)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := turns.Execute(ctx, request); done <- err }()
	awaitTurnSignal(t, provider.started)
	next := request
	next.OperationKey = "automatic-while-user-active"
	next.Content = "automatic followup"
	if _, err := turns.ExecuteAutomatic(t.Context(), next); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("active admission=%v", err)
	}
	thread, _ := st.GetThread(t.Context(), request.ThreadID)
	queue, err := st.GetOperatorSteeringQueueSummary(t.Context(), thread.ActiveRunID)
	if err != nil || queue.Pending+queue.Prepared != 1 {
		t.Fatalf("automatic input entered queue: %+v %v", queue, err)
	}
	cancel()
	_ = awaitTurnResult(t, done)
}

func TestAutomaticThreadTurnContinuesSuccessfulTerminalRun(t *testing.T) {
	st, turns, provider, request, run := automaticTurnFixture(t, domain.Budget{MaxTurns: 3})
	first, err := turns.ExecuteAutomatic(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunService(st).Complete(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	request.OperationKey = "automatic-after-completed"
	result, err := turns.ExecuteAutomatic(t.Context(), request)
	if err != nil || !result.Submission.SuccessorCreated || result.Submission.Run.ID == first.Submission.Run.ID || result.Submission.Thread.ID != first.Submission.Thread.ID || provider.calls != 2 {
		t.Fatalf("continuation=%+v err=%v calls=%d", result, err, provider.calls)
	}
}

func TestAutomaticThreadTurnColdFullAccessNeedsActivation(t *testing.T) {
	st, turns, provider, request, _ := automaticTurnFixture(t, domain.Budget{MaxTurns: 3})
	authority := domain.NewExecutionPermissionRuntimeAuthority()
	capabilities := domain.ExecutionPermissionRuntimeCapabilities{WorkspaceSandboxEnabled: true, OperatorApprovalEnabled: true, DangerFullAccessEnabled: true, RuntimeAuthority: authority}
	_, err := application.NewThreadExecutionPermissionService(st, capabilities).Change(t.Context(), application.ChangeThreadExecutionPermissionRequest{ThreadID: request.ThreadID, Mode: string(domain.RunExecutionPermissionFull), OperationKey: "automatic-full-choice", RequestedBy: "operator", Reason: "authorized task", ConfirmFull: true})
	if err != nil {
		t.Fatal(err)
	}
	turns.WithExecutionPermissionRuntimeAuthority(domain.NewExecutionPermissionRuntimeAuthority())
	if _, err := turns.ExecuteAutomatic(t.Context(), request); apperror.CodeOf(err) != apperror.CodeFailedPrecondition || !strings.Contains(err.Error(), "reactivate Full Access") {
		t.Fatalf("cold full=%v", err)
	}
	if provider.calls != 0 {
		t.Fatal("cold full started a model")
	}
}

func TestAutomaticThreadTurnRecoversOnlyItsDurablePreparedInput(t *testing.T) {
	st, turns, provider, request, run := automaticTurnFixture(t, domain.Budget{MaxTurns: 3})
	queued, err := application.NewThreadService(st).Submit(t.Context(), application.SubmitThreadMessageRequest{Version: request.Version, ThreadID: request.ThreadID, Content: request.Content, OperationKey: request.OperationKey, RequestedBy: request.RequestedBy})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunService(st).Start(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	lease, err := st.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "crashed-process", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginSupervisorSteeringTurn(t.Context(), lease.Lease); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ReleaseRunExecutionLease(t.Context(), lease.Lease); err != nil {
		t.Fatal(err)
	}
	if err := turns.CheckAutomaticTurn(t.Context(), request.ThreadID); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("new input gate=%v", err)
	}
	result, err := turns.ExecuteAutomatic(t.Context(), request)
	if err != nil || result.Submission.Message.ID != queued.Message.ID || provider.calls != 1 {
		t.Fatalf("prepared recovery=%+v err=%v calls=%d", result, err, provider.calls)
	}
	replay, err := turns.ExecuteAutomatic(t.Context(), request)
	if err != nil || !replay.Replayed || provider.calls != 1 {
		t.Fatalf("prepared replay=%+v %v", replay, err)
	}
}

func TestAutomaticThreadTurnDoesNotContinueSealedFailedTurn(t *testing.T) {
	st, turns, provider, request, run := automaticTurnFixture(t, domain.Budget{MaxTurns: 4})
	provider.failures = []error{apperror.New(apperror.CodeFailedPrecondition, "provider request rejected")}
	if _, err := turns.Execute(t.Context(), request); err == nil {
		t.Fatal("expected failed turn")
	}
	failure, found, err := st.GetLatestThreadTurnFailure(t.Context(), request.ThreadID)
	if err != nil || !found || failure.RunID != run.ID {
		t.Fatalf("failed turn=%+v found=%v err=%v", failure, found, err)
	}
	observed, observeErr := turns.ObserveAutomaticTurn(t.Context(), request.ThreadID, run.ID, failure.MessageID)
	if observeErr != nil || observed.State != "blocked" || !observed.TerminalFailure {
		t.Fatalf("failed observation=%+v err=%v", observed, observeErr)
	}
	calls := provider.calls
	request.OperationKey = "automatic-after-failed-turn"
	if _, err := turns.ExecuteAutomatic(t.Context(), request); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("failure admission=%v", err)
	}
	if provider.calls != calls {
		t.Fatal("automatic continuation restarted failed work")
	}
}
