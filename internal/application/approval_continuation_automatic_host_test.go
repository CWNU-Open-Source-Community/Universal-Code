package application_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/store"
)

type automaticApprovalWaitReadStore struct {
	*store.SQLiteStore
	once             sync.Once
	entered, release chan struct{}
}

func (s *automaticApprovalWaitReadStore) GetRunExecutionHandoff(ctx context.Context, key string) (domain.RunExecutionHandoff, bool, error) {
	value, found, err := s.SQLiteStore.GetRunExecutionHandoff(ctx, key)
	if err == nil && found && value.Result != nil && value.Result.StopReason == "root_wait" && value.Operation.RequestedBy == "pr_followup" {
		first := false
		s.once.Do(func() { first = true; close(s.entered) })
		if first {
			<-s.release
			// Read through the actual SQLite API after the injected delay. A
			// cancelled caller now observes the real context-cancelled read.
			return s.SQLiteStore.GetRunExecutionHandoff(ctx, key)
		}
	}
	return value, found, err
}

func TestHostOwnedQueuedApprovalSurvivesAutomaticCallerCancel(t *testing.T) {
	for _, boundary := range []string{"complete", "stop_before", "shutdown_before", "stop_after", "shutdown_after", "different_proposal"} {
		t.Run(boundary, func(t *testing.T) { testHostOwnedQueuedAutomaticApproval(t, boundary) })
	}
}

func testHostOwnedQueuedAutomaticApproval(t *testing.T, boundary string) {
	t.Helper()
	state, run, root, automatic := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
	automatic.RequestedBy = "pr_followup"
	var edit fileedit.Edit
	continuationStarted := make(chan struct{})
	provider := &boundaryJourneyProvider{}
	provider.respond = func(ctx context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 1:
			return boundaryPropose("automatic-host-approval-proposal"), nil
		case 2:
			return textResponse(rootActionResponse(domain.RootActionWait, "Review the exact proposal", "", "operator review required")), nil
		case 3:
			if strings.HasSuffix(boundary, "_after") {
				close(continuationStarted)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return boundaryApply("automatic-host-approved-apply", edit), nil
		default:
			return textResponse(rootActionResponse(domain.RootActionFinish, "Applied the approved proposal", "done", "")), nil
		}
	}
	fault := &automaticApprovalWaitReadStore{SQLiteStore: state, entered: make(chan struct{}), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(fault.release) })
	turns := toolBoundaryServiceWithPolicy(fault, state, provider, &fileOperationPolicy{review: true}).WithHostLifetime(t.Context())
	caller, cancel := context.WithCancel(t.Context())
	t.Cleanup(func() {
		cancel()
		release()
		ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := turns.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	done := make(chan error, 1)
	go func() { _, err := turns.ExecuteAutomatic(caller, automatic); done <- err }()
	awaitTurnSignal(t, fault.entered)
	edits, err := state.ListFileEdits(t.Context(), fileedit.ListFilter{SessionID: run.SessionID})
	if err != nil || len(edits) != 1 || edits[0].Status != fileedit.StatusProposed {
		t.Fatalf("actual proposal unavailable: %+v %v", edits, err)
	}
	edit = edits[0]
	if _, err := application.NewFileEditReviewService(state).Review(t.Context(), application.ReviewFileEditRequest{Version: application.FileEditReviewProtocolVersion, RunID: run.ID, EditID: edit.ID, Action: application.FileEditApproveIntent}); err != nil {
		t.Fatal(err)
	}
	approval := application.ApprovalContinuationRequest{RunID: run.ID, Kind: "file_edit", ProposalID: edit.ID}
	if boundary == "different_proposal" {
		approval.ProposalID = "file-edit-different-proposal-0001"
	}
	if queued := turns.ResumeApproval(t.Context(), approval); queued.State != "queued" {
		t.Fatalf("saved approval was not admitted behind automatic owner: %+v", queued)
	}
	if duplicate := turns.ResumeApproval(t.Context(), approval); duplicate.State != "queued" || !duplicate.Replayed {
		t.Fatalf("same review continuation duplicated its queued entry: %+v", duplicate)
	}
	live, err := turns.ExecutionState(t.Context(), automatic.ThreadID)
	if err != nil || live.ExecutionID == "" {
		t.Fatal("automatic owner missing before approval handoff", err)
	}
	executionID := live.ExecutionID
	stopBoundary := func(before bool) {
		t.Helper()
		if strings.HasPrefix(boundary, "shutdown") {
			grace := 5 * time.Second
			if before {
				grace = 100 * time.Millisecond
			}
			ctx, stop := context.WithTimeout(t.Context(), grace)
			err := turns.Shutdown(ctx)
			stop()
			if before && !errors.Is(err, context.DeadlineExceeded) || !before && err != nil {
				t.Fatalf("approval host drain lost its shutdown boundary: %v", err)
			}
		} else {
			stopping, err := turns.Interrupt(t.Context(), automatic.ThreadID, executionID)
			if err != nil || stopping.ExecutionID != executionID || stopping.State != "stopping" {
				t.Fatalf("Stop did not own the same approval execution: %+v %v", stopping, err)
			}
		}
	}
	if strings.HasSuffix(boundary, "_before") {
		stopBoundary(true)
	}
	cancel()
	release()
	if err := awaitTurnResult(t, done); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeCancelled {
		t.Fatalf("automatic caller cancellation changed: %v", err)
	}
	if strings.HasSuffix(boundary, "_after") {
		awaitTurnSignal(t, continuationStarted)
		live, err := turns.ExecutionState(t.Context(), automatic.ThreadID)
		if err != nil || live.ExecutionID != executionID {
			t.Fatalf("approval handoff replaced its Stop identity: %+v %v", live, err)
		}
		stopBoundary(false)
	}
	awaitAutomaticQueueIdle(t, &automaticQueueFixture{turns: turns, queued: []application.ExecuteThreadTurnRequest{automatic}})
	wantCalls := 2
	if boundary == "complete" {
		wantCalls = 4
	} else if strings.HasSuffix(boundary, "_after") {
		wantCalls = 3
	}
	if len(provider.Requests()) != wantCalls {
		// Confirm that the exact public continuation is eligible, rather than
		// treating an intentionally rejected review/scope as lost execution.
		if boundary == "complete" {
			manual := turns.ResumeApproval(t.Context(), approval)
			t.Fatalf("queued saved approval lost its host driver; explicit recovery=%+v calls_after_recovery=%d", manual, len(provider.Requests()))
		}
		t.Fatalf("approval ignored its stop/proposal boundary: calls=%d want=%d", len(provider.Requests()), wantCalls)
	}
	wantBody := "original text\n"
	if boundary == "complete" {
		wantBody = "reviewed text\n"
	}
	if body, err := os.ReadFile(filepath.Join(root, "README.md")); err != nil || string(body) != wantBody {
		t.Fatalf("approval boundary executed an unexpected file effect: %q %v", body, err)
	}
	if boundary == "complete" || boundary == "different_proposal" || strings.HasSuffix(boundary, "_after") {
		replay := turns.ResumeApproval(t.Context(), approval)
		wantState := "failed"
		if boundary == "complete" {
			wantState = "completed"
			if !replay.Replayed {
				t.Fatal("completed approval did not replay its existing handoff")
			}
		} else if boundary == "different_proposal" {
			wantState = "not_started"
		}
		if replay.State != wantState || len(provider.Requests()) != wantCalls {
			t.Fatalf("late approval revived stopped/different work: %+v", replay)
		}
	}
}
