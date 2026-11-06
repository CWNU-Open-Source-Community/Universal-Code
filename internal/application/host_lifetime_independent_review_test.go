package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/store"
)

type independentCommitCancelStore struct {
	*store.SQLiteStore
	cancel context.CancelFunc
}

func (s *independentCommitCancelStore) CommitThreadMessage(ctx context.Context, request domain.ThreadMessageIntentRequest, runID string, prepared []session.PreparedEvidenceAttachment) (domain.OperatorSteeringEnqueueResult, error) {
	result, err := s.SQLiteStore.CommitThreadMessage(ctx, request, runID, prepared)
	if err == nil {
		s.cancel()
	}
	return result, err
}

func TestIndependentHostTurnDisconnectAtDurableCommit(t *testing.T) {
	provider := &lifecycleProvider{responses: []string{rootActionResponse(domain.RootActionFinish, "finished", "done", "")}}
	state, _, input := threadControlFixture(t, provider)
	caller, cancel := context.WithCancel(t.Context())
	defer cancel()
	fault := &independentCommitCancelStore{state, cancel}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	turns := application.NewThreadTurnService(fault, application.NewRunLifecycleControlService(state), application.NewRunExecutionHandoffService(state, router, policy.NewDefaultChecker())).WithHostLifetime(t.Context())
	_, err := turns.Execute(caller, input)
	observation, readErr := state.InspectThreadTurnRequest(t.Context(), input.ThreadID, input.OperationKey, input.RequestedBy)
	if err != nil || readErr != nil || observation.State != "completed" {
		t.Fatalf("durably accepted host turn stopped at caller disconnect: state=%s message=%s err=%v read=%v", observation.State, observation.MessageStatus, err, readErr)
	}
}

type independentRejectGateStore struct {
	*store.SQLiteStore
	entered, release chan struct{}
}

func (s *independentRejectGateStore) RejectThreadMessageIntent(ctx context.Context, request domain.ThreadMessageIntentRequest) (bool, error) {
	close(s.entered)
	<-s.release
	return s.SQLiteStore.RejectThreadMessageIntent(ctx, request)
}

func TestIndependentHostShutdownWaitsForRejection(t *testing.T) {
	provider := &lifecycleProvider{}
	state, _, input := threadControlFixture(t, provider)
	fault := &independentRejectGateStore{state, make(chan struct{}), make(chan struct{})}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	turns := application.NewThreadTurnService(fault, application.NewRunLifecycleControlService(state), application.NewRunExecutionHandoffService(state, router, policy.NewDefaultChecker())).WithHostLifetime(t.Context())
	input.Files = []domain.WorkspaceFileReference{{SourceKind: "workspace_file", Path: "missing.go", ExpectedSHA256: strings.Repeat("a", 64)}}
	done := make(chan error, 1)
	go func() { _, err := turns.Execute(t.Context(), input); done <- err }()
	select {
	case <-fault.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("reject gate not reached")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	err := turns.Shutdown(ctx)
	cancel()
	close(fault.release)
	if executeErr := <-done; executeErr == nil {
		t.Fatal("fixture failed to reject input")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown declared drained while RejectThreadMessageIntent still borrowed SQLite: %v", err)
	}
}

type independentSteeringGateStore struct {
	*store.SQLiteStore
	entered, release chan struct{}
}

func (s *independentSteeringGateStore) EnqueueOperatorSteering(ctx context.Context, request domain.EnqueueOperatorSteeringRequest) (domain.OperatorSteeringEnqueueResult, error) {
	close(s.entered)
	<-s.release
	return s.SQLiteStore.EnqueueOperatorSteering(ctx, request)
}

func TestIndependentHostShutdownDeadlineWhileSteeringHoldsLock(t *testing.T) {
	provider := &lifecycleProvider{}
	state, _, input := threadControlFixture(t, provider)
	fault := &independentSteeringGateStore{state, make(chan struct{}), make(chan struct{})}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	turns := application.NewThreadTurnService(fault, application.NewRunLifecycleControlService(state), application.NewRunExecutionHandoffService(state, router, policy.NewDefaultChecker())).WithHostLifetime(t.Context())
	thread, err := state.GetThread(t.Context(), input.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := state.GetRun(t.Context(), thread.ActiveRunID)
	if err != nil {
		t.Fatal(err)
	}
	steeringDone := make(chan struct{})
	go func() {
		defer close(steeringDone)
		_, _ = turns.SubmitCurrentSteering(t.Context(), application.SubmitSessionMessageRequest{Version: domain.SessionMessageSubmissionProtocolVersion, SessionID: run.SessionID, Content: "correct pending input", OperationKey: "independent-steering", RequestedBy: "test_operator"})
	}()
	select {
	case <-fault.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("steering write gate not reached")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- turns.Shutdown(ctx) }()
	select {
	case err := <-shutdownDone:
		close(fault.release)
		<-steeringDone
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		close(fault.release)
		<-steeringDone
		<-shutdownDone
		t.Fatal("Shutdown(ctx) ignored deadline while waiting for steering's turnMu")
	}
}
