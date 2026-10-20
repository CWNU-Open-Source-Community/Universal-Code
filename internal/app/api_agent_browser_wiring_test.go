package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/httpapi"
	"cyberagent-workbench/internal/modelregistry"
)

type apiAgentBrowserTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type apiAgentBrowserProviderRequest struct {
	Model  string                `json:"model"`
	Stream bool                  `json:"stream"`
	Tools  []apiAgentBrowserTool `json:"tools"`
}

func TestAPIServeWiresOrdinaryAgentBrowserIntoModelRequests(t *testing.T) {
	const model = "api-agent-browser-wiring-model"
	const providerSecret = "api-agent-browser-wiring-secret"
	const readToken = "api-agent-browser-read-token-0123456789"
	const controlToken = "api-agent-browser-control-token-012345"
	qualificationFixture := &anthropicHarnessFixture{}

	var requestMu sync.Mutex
	modelRequests := make([]apiAgentBrowserProviderRequest, 0, 2)
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/messages" || request.Method != http.MethodPost {
			t.Errorf("unexpected Provider request %s %s", request.Method, request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if request.Header.Get("x-api-key") != providerSecret ||
			request.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("Provider request omitted its credential or streaming contract")
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(request.Body, 256*1024))
		if err != nil {
			t.Errorf("read Provider request: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		var captured apiAgentBrowserProviderRequest
		if err := json.Unmarshal(raw, &captured); err != nil || !captured.Stream || captured.Model != model {
			t.Errorf("invalid Provider request body: %s err=%v", string(raw), err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		phase, nonce, err := qualificationFixture.phase(raw)
		if err != nil {
			t.Error(err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		switch phase {
		case "tool_result":
			writeAPIAgentBrowserAnthropicTextSSE(t, writer, model, fmt.Sprintf(
				`{"version":"model_harness_probe.v1","status":"ok","nonce":"%s"}`, nonce))
		case "tool_call":
			writeAPIAgentBrowserAnthropicToolSSE(t, writer, model, nonce)
		default:
			requestMu.Lock()
			modelRequests = append(modelRequests, captured)
			requestMu.Unlock()
			writeAPIAgentBrowserAnthropicTextSSE(t, writer, model,
				`{"version":"root_lifecycle.v1","action":"wait","message":"API browser wiring observed","reason":"operator turn boundary"}`)
		}
	}))
	defer provider.Close()

	home := t.TempDir()
	t.Setenv("CYBERAGENT_HOME", home)
	t.Setenv("CYBERAGENT_ANTHROPIC_API_KEY", providerSecret)
	t.Setenv("CYBERAGENT_ANTHROPIC_BASE_URL", provider.URL)
	t.Setenv("CYBERAGENT_ANTHROPIC_MODEL", model)
	t.Setenv("MIMO_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv(apiTokenEnvironment, readToken)
	t.Setenv(apiControlTokenEnvironment, controlToken)

	ctx, cancel := context.WithCancel(context.Background())
	var stdout synchronizedBuffer
	var stderr synchronizedBuffer
	done := make(chan int, 1)
	go func() {
		done <- ExecuteContext(ctx, []string{
			"api", "serve", "--listen", "127.0.0.1:0",
			"--enable-workspace-import", "--enable-permission-control",
			"--enable-danger-full-access",
		}, &stdout, &stderr)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Errorf("API command did not stop during cleanup: stdout=%s stderr=%s",
				stdout.String(), stderr.String())
		}
	})

	output := waitForAPIProcessOutput(t, &stdout, &stderr, done, func(output string) bool {
		return outputField(output, "api_url") != ""
	})
	baseURL := outputField(output, "api_url")
	origin := strings.TrimSuffix(baseURL, "/api/v1")
	client := &http.Client{Timeout: 10 * time.Second}

	workspaceDirectory := t.TempDir()
	importBody, err := json.Marshal(httpapi.WorkspaceImportRequestView{
		Version: httpapi.WorkspaceImportProtocolVersion, DirectoryPath: workspaceDirectory, Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	var imported httpapi.WorkspaceImportView
	apiAgentBrowserControlJSON(t, client, origin+httpapi.WorkspaceImportPath, controlToken,
		"api-agent-browser-import-0001", importBody, http.StatusOK, &imported)
	if imported.Workspace.ID == "" || imported.DirectoryContentModified {
		t.Fatalf("workspace import mismatch: %#v", imported)
	}

	var qualification httpapi.ModelHarnessQualificationView
	apiAgentBrowserControlJSON(t, client, origin+httpapi.ModelHarnessQualificationPath, controlToken,
		"api-agent-browser-harness-0001", []byte(fmt.Sprintf(
			`{"version":%q,"provider":"anthropic","model":%q,"confirm_qualification":true}`,
			modelregistry.HarnessQualificationProtocolVersion, model)), http.StatusAccepted, &qualification)
	if qualification.Status != modelregistry.HarnessDiagnosticQualified || qualification.ModelCalls != 2 || qualification.SyntheticToolCalls != 1 || !qualification.Harness.RootEligible {
		t.Fatalf("Harness qualification did not become root eligible: %#v", qualification)
	}
	apiAgentBrowserControlJSON(t, client, baseURL+"/models/routes/code", controlToken,
		"api-agent-browser-route-0001", []byte(fmt.Sprintf(
			`{"version":%q,"provider":"anthropic","model":%q}`,
			modelregistry.RouteControlProtocolVersion, model)), http.StatusAccepted, nil)

	fullThread := apiAgentBrowserCreateThread(t, client, baseURL, controlToken,
		imported.Workspace.ID, "Current Full ordinary browser wiring", "api-agent-browser-full-thread-0001")
	apiAgentBrowserControlJSON(t, client,
		baseURL+"/threads/"+fullThread.Thread.ID+"/execution-permission", controlToken,
		"api-agent-browser-full-permission-0001",
		[]byte(`{"mode":"full","reason":"inspect ordinary browser tools","confirm_full":true}`),
		http.StatusAccepted, nil)
	apiAgentBrowserCompleteTurn(t, client, baseURL, controlToken, fullThread,
		"Inspect the ordinary browser tools available to this Run", "api-agent-browser-full-turn-0001")

	restrictedThread := apiAgentBrowserCreateThread(t, client, baseURL, controlToken,
		imported.Workspace.ID, "Ask ordinary browser boundary", "api-agent-browser-restricted-thread-0001")
	apiAgentBrowserCompleteTurn(t, client, baseURL, controlToken, restrictedThread,
		"Inspect tools without elevated execution permission", "api-agent-browser-restricted-turn-0001")

	requestMu.Lock()
	captured := append([]apiAgentBrowserProviderRequest(nil), modelRequests...)
	requestMu.Unlock()
	if len(captured) != 2 {
		t.Fatalf("Supervisor model request count=%d, want one current Full and one Ask request", len(captured))
	}
	fullTools := apiAgentBrowserToolsByName(captured[0].Tools)
	restrictedTools := apiAgentBrowserToolsByName(captured[1].Tools)
	for _, name := range []string{"browser_navigate", "browser_scroll", "browser_key"} {
		_, fullFound := fullTools[name]
		_, restrictedFound := restrictedTools[name]
		if browserruntime.AgentBrowserPlatformAvailable() && !fullFound {
			t.Fatalf("native current Full API Provider tools omitted %s: %v", name,
				apiAgentBrowserToolNames(captured[0].Tools))
		}
		if !browserruntime.AgentBrowserPlatformAvailable() && fullFound {
			t.Fatalf("API host without an adapter advertised ordinary browser tool %s", name)
		}
		if restrictedFound {
			t.Fatalf("Ask API Run received ordinary browser tool %s: %v", name,
				apiAgentBrowserToolNames(captured[1].Tools))
		}
	}
	if !browserruntime.AgentBrowserPlatformAvailable() {
		return
	}

	var status httpapi.AgentBrowserStatusView
	apiAgentBrowserReadJSON(t, client,
		baseURL+"/runs/"+fullThread.Run.ID+"/agent-browser", readToken,
		http.StatusOK, &status)
	if status.SessionID == "" || status.State != "idle" {
		t.Fatalf("API model request did not create an observable idle Agent browser session: %#v", status)
	}
	var closed httpapi.AgentBrowserStatusView
	apiAgentBrowserControlJSON(t, client,
		baseURL+"/runs/"+fullThread.Run.ID+"/agent-browser/close", controlToken,
		"api-agent-browser-close-0001",
		[]byte(fmt.Sprintf(`{"version":"agent_browser_close.v1","session_id":%q}`, status.SessionID)),
		http.StatusOK, &closed)
	processesStopped := closed.TreeReaped
	if runtime.GOOS == "darwin" {
		processesStopped = !closed.TreeReaped && closed.ProcessScope == "no_owned_process_resources" && closed.ProcessScopeVerified
	}
	if closed.State != "closed" || !processesStopped || !closed.ProfileRemoved || closed.CleanupPending {
		t.Fatalf("idle API Agent browser session did not close cleanly: %#v", closed)
	}
}

func apiAgentBrowserCreateThread(t *testing.T, client *http.Client, baseURL, token,
	workspaceID, goal, operationKey string,
) httpapi.ThreadCreationControlView {
	t.Helper()
	var created httpapi.ThreadCreationControlView
	apiAgentBrowserControlJSON(t, client, baseURL+"/threads", token, operationKey,
		[]byte(fmt.Sprintf(`{"version":%q,"goal":%q,"workspace_id":%q,"profile":"code","surface":"code","phase":"deliver"}`,
			domain.ThreadCreationProtocolVersion, goal, workspaceID)), http.StatusAccepted, &created)
	return created
}

func apiAgentBrowserCompleteTurn(t *testing.T, client *http.Client, baseURL, token string,
	thread httpapi.ThreadCreationControlView, content, operationKey string,
) {
	t.Helper()
	var executed httpapi.ThreadMessageControlView
	apiAgentBrowserControlJSON(t, client, baseURL+"/threads/"+thread.Thread.ID+"/turns",
		token, operationKey,
		[]byte(fmt.Sprintf(`{"version":"thread_message_submission.v1","content":%q}`, content)),
		http.StatusAccepted, &executed)
	if !executed.ExecutionStarted || !executed.ModelCalled || executed.ToolCalled ||
		executed.RunID != thread.Run.ID ||
		executed.Steering.Status != string(domain.OperatorSteeringCommitted) {
		t.Fatalf("API Thread did not complete one model-only turn: %#v", executed)
	}
}

func apiAgentBrowserControlJSON(t *testing.T, client *http.Client, url, token, operationKey string,
	body []byte, wantStatus int, target any,
) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", operationKey)
	apiAgentBrowserDoJSON(t, client, request, wantStatus, target)
}

func apiAgentBrowserReadJSON(t *testing.T, client *http.Client, url, token string,
	wantStatus int, target any,
) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	apiAgentBrowserDoJSON(t, client, request, wantStatus, target)
}

func apiAgentBrowserDoJSON(t *testing.T, client *http.Client, request *http.Request,
	wantStatus int, target any,
) {
	t.Helper()
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != wantStatus {
		t.Fatalf("%s %s status=%d want=%d body=%s err=%v", request.Method,
			request.URL.Path, response.StatusCode, wantStatus, raw, readErr)
	}
	if target == nil {
		return
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode %s response envelope: %v body=%s", request.URL.Path, err, raw)
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		t.Fatalf("decode %s response data: %v body=%s", request.URL.Path, err, raw)
	}
}

func apiAgentBrowserToolsByName(tools []apiAgentBrowserTool) map[string]apiAgentBrowserTool {
	indexed := make(map[string]apiAgentBrowserTool, len(tools))
	for _, tool := range tools {
		indexed[tool.Name] = tool
	}
	return indexed
}

func apiAgentBrowserToolNames(tools []apiAgentBrowserTool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func writeAPIAgentBrowserAnthropicTextSSE(t *testing.T, writer http.ResponseWriter,
	model, text string,
) {
	t.Helper()
	writeAPIAgentBrowserAnthropicSSE(t, writer,
		map[string]any{"type": "message_start", "message": map[string]any{
			"model": model, "usage": map[string]any{"input_tokens": 12, "output_tokens": 0}}},
		map[string]any{"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "text", "text": ""}},
		map[string]any{"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": text}},
		map[string]any{"type": "content_block_stop", "index": 0},
		map[string]any{"type": "message_delta", "usage": map[string]any{"output_tokens": 8}},
		map[string]any{"type": "message_stop"},
	)
}

func writeAPIAgentBrowserAnthropicToolSSE(t *testing.T, writer http.ResponseWriter,
	model, nonce string,
) {
	t.Helper()
	writeAPIAgentBrowserAnthropicSSE(t, writer,
		map[string]any{"type": "message_start", "message": map[string]any{
			"model": model, "usage": map[string]any{"input_tokens": 12, "output_tokens": 0}}},
		map[string]any{"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "tool_use", "id": "qualification-tool-1",
				"name": "prayu_harness_echo", "input": map[string]any{}}},
		map[string]any{"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": fmt.Sprintf(`{"nonce":"%s"}`, nonce)}},
		map[string]any{"type": "content_block_stop", "index": 0},
		map[string]any{"type": "message_delta", "usage": map[string]any{"output_tokens": 8}},
		map[string]any{"type": "message_stop"},
	)
}

func writeAPIAgentBrowserAnthropicSSE(t *testing.T, writer http.ResponseWriter, events ...any) {
	t.Helper()
	writer.Header().Set("Content-Type", "text/event-stream")
	flusher, ok := writer.(http.Flusher)
	if !ok {
		t.Error("test Provider does not support streaming flush")
		return
	}
	for _, event := range events {
		payload, err := json.Marshal(event)
		if err != nil {
			t.Errorf("encode test Provider event: %v", err)
			return
		}
		_, _ = fmt.Fprintf(writer, "data: %s\n\n", payload)
		flusher.Flush()
	}
}
