package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/httpapi"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
)

func TestHostOwnedThreadTurnSurvivesHTTPDisconnect(t *testing.T) {
	for _, boundary := range []string{"complete", "stop", "shutdown"} {
		t.Run(boundary, func(t *testing.T) {
			provider := &gatedThreadProvider{started: make(chan struct{}), release: make(chan struct{}),
				lifecycleProvider: lifecycleProvider{responses: []string{
					rootActionResponse(domain.RootActionFinish, "Original accepted turn completed", "done", ""),
				}},
			}
			state, turns, input := threadControlFixture(t, provider)
			turns.WithHostLifetime(t.Context())
			router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
			router.RegisterProvider(provider)
			const access = "http-host-turn-access-token-long-0001"
			const control = "http-host-turn-control-token-long-0001"
			api, err := httpapi.New(state, httpapi.Config{AccessToken: access, ControlToken: control,
				RunCreationEnabled: true, SessionMessageEnabled: true, RunLifecycleEnabled: true, RunExecutionEnabled: true,
				RunLifecycleController: application.NewRunLifecycleControlService(state),
				RunExecutionController: application.NewRunExecutionHandoffService(state, router, policy.NewDefaultChecker()),
				ThreadTurnController:   turns,
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
			body, _ := json.Marshal(map[string]string{"version": input.Version, "content": input.Content})
			caller, disconnect := context.WithCancel(t.Context())
			defer disconnect()
			request, err := http.NewRequestWithContext(caller, http.MethodPost,
				server.URL+"/api/v1/threads/"+input.ThreadID+"/turns", strings.NewReader(string(body)))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer "+control)
			request.Header.Set("Idempotency-Key", input.OperationKey)
			request.Header.Set("Content-Type", "application/json")
			done := make(chan error, 1)
			go func() {
				response, err := server.Client().Do(request)
				if response != nil {
					_ = response.Body.Close()
				}
				done <- err
			}()
			awaitTurnSignal(t, provider.started)
			disconnect()
			if err := awaitTurnResult(t, done); err == nil {
				t.Fatal("client did not disconnect from its outstanding POST")
			}
			// Give net/http time to deliver the closed connection's cancellation.
			time.Sleep(50 * time.Millisecond)
			live, err := turns.ExecutionState(t.Context(), input.ThreadID)
			if err != nil || live.State != "running" {
				t.Fatalf("disconnect cancelled accepted host execution: %#v err=%v", live, err)
			}
			if boundary == "complete" {
				retry, err := http.NewRequestWithContext(t.Context(), http.MethodPost, request.URL.String(), strings.NewReader(string(body)))
				if err != nil {
					t.Fatal(err)
				}
				retry.Header = request.Header.Clone()
				response, err := server.Client().Do(retry)
				if err != nil {
					t.Fatal(err)
				}
				_ = response.Body.Close()
				if response.StatusCode != http.StatusAccepted {
					t.Fatalf("same-key readmission status=%d", response.StatusCode)
				}
			}
			switch boundary {
			case "complete":
				close(provider.release)
			case "stop":
				if _, err := turns.Interrupt(t.Context(), input.ThreadID, live.ExecutionID); err != nil {
					t.Fatal(err)
				}
			case "shutdown":
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				err := turns.Shutdown(ctx)
				cancel()
				if err != nil {
					t.Fatal(err)
				}
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				live, err = turns.ExecutionState(t.Context(), input.ThreadID)
				if err != nil {
					t.Fatal(err)
				}
				if live.State == "idle" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("host execution did not drain: %#v", live)
				}
				time.Sleep(time.Millisecond)
			}
			observation, err := state.InspectThreadTurnRequest(t.Context(), input.ThreadID, input.OperationKey, "http_thread_operator")
			expected := "completed"
			if boundary != "complete" {
				expected = "failed"
			}
			if err != nil || observation.State != expected || !observation.Settled || live.LastTurnInterrupted != (boundary != "complete") {
				t.Fatalf("host boundary was not recorded: observation=%#v live=%#v err=%v", observation, live, err)
			}
			if lease, found, err := state.GetRunExecutionLease(t.Context(), observation.RunID); err != nil || found && lease.ActiveAt(time.Now().UTC()) {
				t.Fatalf("host boundary retained execution ownership: %#v found=%t err=%v", lease, found, err)
			}
			if boundary == "complete" {
				if _, err := turns.Execute(t.Context(), application.ExecuteThreadTurnRequest{
					Version: input.Version, ThreadID: input.ThreadID, Content: input.Content,
					OperationKey: input.OperationKey, RequestedBy: "http_thread_operator",
				}); err != nil || provider.calls != 1 {
					t.Fatalf("reconnect replay repeated the model: calls=%d err=%v", provider.calls, err)
				}
			}
		})
	}
}

type stalledThreadTurnSettlement struct {
	*store.SQLiteStore
	started chan struct{}
	release chan struct{}
}

func (s *stalledThreadTurnSettlement) EndFailedThreadTurn(ctx context.Context, threadID, runID, handoffID string) (domain.ThreadTurnFailure, bool, error) {
	close(s.started)
	<-s.release
	return s.SQLiteStore.EndFailedThreadTurn(ctx, threadID, runID, handoffID)
}

func TestHostOwnedThreadShutdownDeadlineLeavesDrainObservable(t *testing.T) {
	provider := blockingProvider{started: make(chan struct{}, 1)}
	state, _, input := threadControlFixture(t, provider)
	fault := &stalledThreadTurnSettlement{SQLiteStore: state, started: make(chan struct{}), release: make(chan struct{})}
	defer func() {
		select {
		case <-fault.release:
		default:
			close(fault.release)
		}
	}()
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	turns := application.NewThreadTurnService(fault, application.NewRunLifecycleControlService(state),
		application.NewRunExecutionHandoffService(state, router, policy.NewDefaultChecker())).WithHostLifetime(t.Context())
	done := make(chan error, 1)
	go func() { _, err := turns.Execute(t.Context(), input); done <- err }()
	awaitTurnSignal(t, provider.started)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	err := turns.Shutdown(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stalled drain ignored shutdown deadline: %v", err)
	}
	awaitTurnSignal(t, fault.started)
	if _, err := state.GetThread(t.Context(), input.ThreadID); err != nil {
		t.Fatalf("unfinished drain lost its borrowed store: %v", err)
	}
	close(fault.release)
	ctx, cancel = context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := turns.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := awaitTurnResult(t, done); err == nil {
		t.Fatal("shutdown was reported as a successful turn")
	}
	live, err := turns.ExecutionState(t.Context(), input.ThreadID)
	if err != nil || live.State != "idle" || !live.LastTurnInterrupted {
		t.Fatalf("second shutdown did not finish settlement: %#v err=%v", live, err)
	}
}

func TestHostLifetimeDoesNotDetachAutomaticThreadTurn(t *testing.T) {
	provider := blockingProvider{started: make(chan struct{}, 1)}
	_, turns, input := threadControlFixture(t, provider)
	turns.WithHostLifetime(t.Context())
	caller, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := turns.ExecuteAutomatic(caller, input); done <- err }()
	awaitTurnSignal(t, provider.started)
	cancel()
	if err := awaitTurnResult(t, done); err == nil {
		t.Fatal("automatic execution ignored caller cancellation")
	}
	live, err := turns.ExecutionState(t.Context(), input.ThreadID)
	if err != nil || live.State != "idle" || !live.LastTurnInterrupted {
		t.Fatalf("automatic cancellation did not drain: %#v err=%v", live, err)
	}
}
