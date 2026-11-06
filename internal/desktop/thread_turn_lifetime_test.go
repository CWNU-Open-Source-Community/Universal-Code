package desktop

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/httpapi"
	"cyberagent-workbench/internal/modelregistry"
	"cyberagent-workbench/internal/store"
)

func TestControlPlaneOwnsAcceptedChatLifetime(t *testing.T) {
	for _, boundary := range []string{"complete", "close"} {
		t.Run(boundary, func(t *testing.T) {
			const model = "desktop-host-turn-model"
			qualification := &anthropicHarnessFixture{}
			started, release, cancelled := make(chan struct{}), make(chan struct{}), make(chan struct{})
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				phase, nonce, err := qualification.phase(raw)
				if err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				switch phase {
				case "tool_call":
					writeAnthropicToolSSE(t, w, model, nonce)
				case "tool_result":
					writeAnthropicTextSSE(t, w, model, fmt.Sprintf(`{"version":"model_harness_probe.v1","status":"ok","nonce":%q}`, nonce))
				default:
					close(started)
					select {
					case <-release:
						writeAnthropicTextSSE(t, w, model, `{"version":"root_lifecycle.v1","action":"finish","message":"Host kept the accepted turn","summary":"done"}`)
					case <-r.Context().Done():
						close(cancelled)
					}
				}
			}))
			defer provider.Close()
			t.Setenv("CYBERAGENT_ANTHROPIC_API_KEY", "desktop-host-turn-fixture-secret")
			t.Setenv("CYBERAGENT_ANTHROPIC_BASE_URL", provider.URL)
			t.Setenv("CYBERAGENT_ANTHROPIC_MODEL", model)
			path := filepath.Join(t.TempDir(), "host-turn.db")
			plane, err := OpenControlPlane(ControlPlaneConfig{
				DatabasePath: path, ReadToken: desktopControlPlaneTestToken, ControlToken: desktopControlPlaneControlToken,
				RunCreationEnabled: true, SessionMessageEnabled: true, RunLifecycleEnabled: true,
				RunExecutionEnabled: true, ModelControlEnabled: true, CredentialStore: credential.NewMemoryStore(),
			})
			if err != nil {
				t.Fatal(err)
			}
			workspace, err := plane.RegisterWorkspaceDirectory(t.Context(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			response := desktopControlRequest(plane.Handler(), http.MethodPost, httpapi.ModelHarnessQualificationPath,
				"host-turn-qualification", fmt.Sprintf(`{"version":%q,"provider":"anthropic","model":%q,"confirm_qualification":true}`, modelregistry.HarnessQualificationProtocolVersion, model))
			if response.Code != http.StatusAccepted {
				t.Fatalf("qualification status=%d body=%s", response.Code, response.Body.String())
			}
			response = desktopControlRequest(plane.Handler(), http.MethodPost, "/api/v1/models/routes/code",
				"host-turn-model-route", fmt.Sprintf(`{"version":%q,"provider":"anthropic","model":%q}`, modelregistry.RouteControlProtocolVersion, model))
			if response.Code != http.StatusAccepted {
				t.Fatalf("route status=%d body=%s", response.Code, response.Body.String())
			}
			thread := createDesktopSourceWiringThread(t, plane, workspace.ID, "Host-owned chat", "host-turn-create")
			server := httptest.NewServer(plane.Handler())
			defer server.Close()
			defer plane.Close()
			caller, disconnect := context.WithCancel(t.Context())
			defer disconnect()
			request, _ := http.NewRequestWithContext(caller, http.MethodPost,
				server.URL+"/api/v1/threads/"+thread.Thread.ID+"/turns", strings.NewReader(`{"version":"thread_message_submission.v1","content":"Keep running across refresh"}`))
			request.Header.Set("Authorization", "Bearer "+desktopControlPlaneControlToken)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "host-turn-original")
			done := make(chan error, 1)
			go func() {
				response, err := server.Client().Do(request)
				if response != nil {
					_ = response.Body.Close()
				}
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("Desktop did not dispatch the model")
			}
			disconnect()
			if err := <-done; err == nil {
				t.Fatal("client did not disconnect")
			}
			select {
			case <-cancelled:
				t.Fatal("renderer disconnect cancelled the Desktop-owned turn")
			case <-time.After(50 * time.Millisecond):
			}
			if boundary == "close" {
				if err := plane.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := store.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				failure, found, err := reopened.GetLatestThreadTurnFailure(t.Context(), thread.Thread.ID)
				if err != nil || !found || failure.ErrorCode != string(apperror.CodeCancelled) {
					t.Fatalf("Desktop closed SQLite before recording cancellation: %#v found=%t err=%v", failure, found, err)
				}
				return
			}
			close(release)
			deadline := time.Now().Add(5 * time.Second)
			for {
				observation, err := plane.stateStore.InspectThreadTurnRequest(t.Context(), thread.Thread.ID, "host-turn-original", "http_thread_operator")
				if err != nil {
					t.Fatal(err)
				}
				if observation.State == "completed" && observation.MessageStatus == domain.OperatorSteeringCommitted {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("Desktop lost accepted input after disconnect: %#v", observation)
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}
