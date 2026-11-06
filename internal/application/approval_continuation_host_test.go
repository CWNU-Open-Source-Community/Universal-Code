package application_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/httpapi"
	"cyberagent-workbench/internal/llm"
)

func TestHostOwnedApprovalStopPreventsLateRevival(t *testing.T) {
	testApprovalContinuationStopPreventsLateApprovalRevival(t, true)
}

func TestHostOwnedApprovalDoesNotOverrideReviewScopeOrNewInput(t *testing.T) {
	testApprovalContinuationDoesNotWakeManualReviewOrOverrideNewInput(t, true)
}

func TestHostOwnedApprovalContinuesAfterReviewHTTPDisconnect(t *testing.T) {
	for _, timing := range []string{"after_review_commit", "during_continuation"} {
		t.Run(timing, func(t *testing.T) { testHostOwnedApprovalHTTPDisconnect(t, timing == "after_review_commit") })
	}
}

type disconnectAfterFileReview struct {
	*application.FileEditReviewService
	disconnect context.CancelFunc
}

func (s disconnectAfterFileReview) Review(ctx context.Context, request application.ReviewFileEditRequest) (application.ReviewFileEditResult, error) {
	result, err := s.FileEditReviewService.Review(ctx, request)
	if err == nil && !result.Replayed {
		s.disconnect()
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			return result, fmt.Errorf("review HTTP connection did not close after the committed decision")
		}
	}
	return result, err
}

func testHostOwnedApprovalHTTPDisconnect(t *testing.T, disconnectAtReview bool) {
	t.Helper()
	state, run, root, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
	started, release, cancelled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var edit fileedit.Edit
	provider := &boundaryJourneyProvider{}
	provider.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 1:
			return boundaryPropose("host-approval-proposal"), nil
		case 2:
			return textResponse(rootActionResponse(domain.RootActionWait, "Review existing proposal", "", "operator review")), nil
		case 3:
			close(started)
			select {
			case <-release:
				return boundaryApply("host-approved-apply", edit), nil
			case <-ctx.Done():
				close(cancelled)
				return nil, ctx.Err()
			}
		case 4:
			return textResponse(rootActionResponse(domain.RootActionFinish, "Approved exact edit completed", "done", "")), nil
		default:
			return nil, fmt.Errorf("unexpected model call %d", index)
		}
	}
	checker := &fileOperationPolicy{review: true}
	turns := toolBoundaryServiceWithPolicy(state, state, provider, checker).WithHostLifetime(t.Context())
	if _, err := turns.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	edits, err := state.ListFileEdits(t.Context(), fileedit.ListFilter{SessionID: run.SessionID})
	if err != nil || len(edits) != 1 {
		t.Fatalf("edits=%#v err=%v", edits, err)
	}
	edit = edits[0]
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	const access = "host-review-access-token-long-0001"
	const control = "host-review-control-token-long-0001"
	caller, disconnect := context.WithCancel(t.Context())
	defer disconnect()
	var review httpapi.FileEditReviewController = application.NewFileEditReviewService(state)
	if disconnectAtReview {
		review = disconnectAfterFileReview{application.NewFileEditReviewService(state), disconnect}
	}
	api, err := httpapi.New(state, httpapi.Config{AccessToken: access, ControlToken: control,
		RunCreationEnabled: true, SessionMessageEnabled: true, RunLifecycleEnabled: true, RunExecutionEnabled: true,
		RunLifecycleController: application.NewRunLifecycleControlService(state),
		RunExecutionController: application.NewRunExecutionHandoffService(state, router, checker), ThreadTurnController: turns,
		FileEditReviewEnabled: true, FileEditReviewController: review,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api)
	defer server.Close()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := turns.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	reviewPath := server.URL + "/api/v1/runs/" + run.ID + "/file-edits/" + edit.ID + "/review"
	reviewRequest := func(ctx context.Context) *http.Request {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, reviewPath,
			strings.NewReader(`{"version":"file_edit_review.v1","action":"approve_intent"}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+control)
		request.Header.Set("Content-Type", "application/json")
		return request
	}
	request := reviewRequest(caller)
	done := make(chan error, 1)
	go func() {
		response, err := server.Client().Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		done <- err
	}()
	awaitTurnSignal(t, started)
	decision, err := state.GetApprovalByProposal(t.Context(), edit.ID)
	reviewed, editErr := state.GetFileEdit(t.Context(), edit.ID)
	if err != nil || editErr != nil || decision.Status != approval.StatusApproved || reviewed.Status != fileedit.StatusApproved {
		t.Fatalf("continuation began before durable review: approval=%#v edit=%#v err=%v/%v", decision, reviewed, err, editErr)
	}
	disconnect()
	if err := awaitTurnResult(t, done); err == nil {
		t.Fatal("review HTTP client did not disconnect")
	}
	select {
	case <-cancelled:
		t.Fatal("disconnect cancelled continuation after the exact review was committed")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		live, err := turns.ExecutionState(t.Context(), input.ThreadID)
		if err != nil {
			t.Fatal(err)
		}
		if live.State == "idle" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("review continuation did not settle: %#v", live)
		}
		time.Sleep(time.Millisecond)
	}
	body, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil || string(body) != "reviewed text\n" {
		t.Fatalf("approved effect missing after disconnect: %q err=%v", body, err)
	}
	response, err := server.Client().Do(reviewRequest(t.Context()))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var envelope struct {
		Data httpapi.FileEditReviewView `json:"data"`
	}
	err = json.NewDecoder(response.Body).Decode(&envelope)
	if err != nil || response.StatusCode != http.StatusAccepted || !envelope.Data.Replayed ||
		envelope.Data.Continuation == nil || envelope.Data.Continuation.State != "completed" || !envelope.Data.Continuation.Replayed || len(provider.Requests()) != 4 {
		t.Fatalf("review replay repeated approved effect: result=%#v calls=%d err=%v", envelope.Data, len(provider.Requests()), err)
	}
	assertOneBoundaryTranscriptInput(t, state, input.ThreadID, input.Content)
}
