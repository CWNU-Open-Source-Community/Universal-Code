package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/httpapi"
	"cyberagent-workbench/internal/modelregistry"
)

func TestAPIServeOwnsAcceptedChatAcrossDisconnect(t *testing.T) {
	const model = "api-host-chat-model"
	const readToken = "api-host-chat-read-token-0123456789"
	const controlToken = "api-host-chat-control-token-012345"
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
			writeAPIAgentBrowserAnthropicToolSSE(t, w, model, nonce)
		case "tool_result":
			writeAPIAgentBrowserAnthropicTextSSE(t, w, model, fmt.Sprintf(`{"version":"model_harness_probe.v1","status":"ok","nonce":%q}`, nonce))
		default:
			close(started)
			select {
			case <-release:
				writeAPIAgentBrowserAnthropicTextSSE(t, w, model, `{"version":"root_lifecycle.v1","action":"finish","message":"Accepted chat survived disconnect","summary":"done"}`)
			case <-r.Context().Done():
				close(cancelled)
			}
		}
	}))
	defer provider.Close()
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	t.Setenv("CYBERAGENT_ANTHROPIC_API_KEY", "api-host-chat-fixture-secret")
	t.Setenv("CYBERAGENT_ANTHROPIC_BASE_URL", provider.URL)
	t.Setenv("CYBERAGENT_ANTHROPIC_MODEL", model)
	t.Setenv("MIMO_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv(apiTokenEnvironment, readToken)
	t.Setenv(apiControlTokenEnvironment, controlToken)
	host, shutdown := context.WithCancel(t.Context())
	var stdout, stderr synchronizedBuffer
	done := make(chan int, 1)
	go func() {
		done <- ExecuteContext(host, []string{"api", "serve", "--listen", "127.0.0.1:0", "--enable-workspace-import"}, &stdout, &stderr)
	}()
	defer func() {
		shutdown()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("API shutdown exit=%d stderr=%s", code, stderr.String())
			}
		case <-time.After(10 * time.Second):
			t.Error("API did not drain during shutdown")
		}
	}()
	output := waitForAPIProcessOutput(t, &stdout, &stderr, done, func(output string) bool { return outputField(output, "api_url") != "" })
	baseURL := outputField(output, "api_url")
	client := &http.Client{Timeout: 10 * time.Second}
	importBody, _ := json.Marshal(httpapi.WorkspaceImportRequestView{Version: httpapi.WorkspaceImportProtocolVersion, DirectoryPath: t.TempDir(), Confirmed: true})
	var imported httpapi.WorkspaceImportView
	apiAgentBrowserControlJSON(t, client, strings.TrimSuffix(baseURL, "/api/v1")+httpapi.WorkspaceImportPath,
		controlToken, "host-chat-import", importBody, http.StatusOK, &imported)
	apiAgentBrowserControlJSON(t, client, strings.TrimSuffix(baseURL, "/api/v1")+httpapi.ModelHarnessQualificationPath,
		controlToken, "host-chat-qualification", []byte(fmt.Sprintf(`{"version":%q,"provider":"anthropic","model":%q,"confirm_qualification":true}`, modelregistry.HarnessQualificationProtocolVersion, model)), http.StatusAccepted, nil)
	apiAgentBrowserControlJSON(t, client, baseURL+"/models/routes/code", controlToken, "host-chat-route",
		[]byte(fmt.Sprintf(`{"version":%q,"provider":"anthropic","model":%q}`, modelregistry.RouteControlProtocolVersion, model)), http.StatusAccepted, nil)
	thread := apiAgentBrowserCreateThread(t, client, baseURL, controlToken, imported.Workspace.ID, "Host-owned API chat", "host-chat-create")
	caller, disconnect := context.WithCancel(t.Context())
	defer disconnect()
	request, err := http.NewRequestWithContext(caller, http.MethodPost, baseURL+"/threads/"+thread.Thread.ID+"/turns",
		strings.NewReader(`{"version":"thread_message_submission.v1","content":"Keep running after refresh"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+controlToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "host-chat-original")
	postDone := make(chan error, 1)
	go func() {
		response, err := client.Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		postDone <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("API did not dispatch the accepted turn")
	}
	disconnect()
	if err := <-postDone; err == nil {
		t.Fatal("client did not disconnect")
	}
	select {
	case <-cancelled:
		t.Fatal("client disconnect cancelled the API-owned turn")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		var observation httpapi.ThreadRequestObservationView
		probe, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
			baseURL+"/threads/"+thread.Thread.ID+"/turn-request", nil)
		if err != nil {
			t.Fatal(err)
		}
		probe.Header.Set("Authorization", "Bearer "+readToken)
		probe.Header.Set("Idempotency-Key", "host-chat-original")
		apiAgentBrowserDoJSON(t, client, probe, http.StatusOK, &observation)
		if observation.State == "completed" && observation.Settled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("API lost the disconnected input: %#v", observation)
		}
		time.Sleep(time.Millisecond)
	}
}
