package desktop

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/httpapi"
	"cyberagent-workbench/internal/modelregistry"
)

func TestControlPlaneCompletesARealAnthropicCompatibleDesktopThreadTurn(t *testing.T) {
	const model = "integration-model"
	const assistantReply = "真实模型桌面对话已提交"
	var calls atomic.Int32
	qualificationFixture := &anthropicHarnessFixture{}
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/messages" || request.Method != http.MethodPost {
			t.Errorf("unexpected Provider request %s %s", request.Method, request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if request.Header.Get("x-api-key") != "desktop-integration-secret" ||
			request.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("Provider request omitted its credential or streaming contract")
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(request.Body, 128*1024))
		if err != nil {
			t.Errorf("read Provider request: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		var requestBody map[string]any
		if err := json.Unmarshal(raw, &requestBody); err != nil || requestBody["stream"] != true ||
			requestBody["model"] != model {
			t.Errorf("invalid Provider request body: %s err=%v", string(raw), err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		calls.Add(1)
		phase, nonce, err := qualificationFixture.phase(raw)
		if err != nil {
			t.Error(err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		switch phase {
		case "tool_result":
			writeAnthropicTextSSE(t, writer, model, fmt.Sprintf(
				`{"version":"model_harness_probe.v1","status":"ok","nonce":"%s"}`, nonce))
		case "tool_call":
			writeAnthropicToolSSE(t, writer, model, nonce)
		default:
			writeAnthropicTextSSE(t, writer, model,
				`{"version":"root_lifecycle.v1","action":"wait","message":"`+
					assistantReply+`","reason":"operator turn boundary"}`)
		}
	}))
	defer provider.Close()

	t.Setenv("CYBERAGENT_ANTHROPIC_API_KEY", "desktop-integration-secret")
	t.Setenv("CYBERAGENT_ANTHROPIC_BASE_URL", provider.URL)
	t.Setenv("CYBERAGENT_ANTHROPIC_MODEL", model)
	plane, err := OpenControlPlane(ControlPlaneConfig{
		DatabasePath: filepath.Join(t.TempDir(), "real-model-chat.db"),
		ReadToken:    desktopControlPlaneTestToken, ControlToken: desktopControlPlaneControlToken,
		RunCreationEnabled: true, SessionMessageEnabled: true,
		RunLifecycleEnabled: true, RunExecutionEnabled: true, ModelControlEnabled: true,
		CredentialStore: credential.NewMemoryStore(), AppVersion: "desktop-real-model-chat-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()

	workspace, err := plane.RegisterWorkspaceDirectory(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	harness := desktopControlRequest(plane.Handler(), http.MethodPost,
		httpapi.ModelHarnessQualificationPath, "desktop-real-model-harness-0001",
		fmt.Sprintf(`{"version":%q,"provider":"anthropic","model":%q,"confirm_qualification":true}`,
			modelregistry.HarnessQualificationProtocolVersion, model))
	if harness.Code != http.StatusAccepted {
		t.Fatalf("Harness qualification status=%d body=%s", harness.Code, harness.Body.String())
	}
	var qualification httpapi.ModelHarnessQualificationView
	decodeDesktopControlData(t, harness, &qualification)
	if qualification.Status != modelregistry.HarnessDiagnosticQualified || qualification.ModelCalls != 2 || qualification.SyntheticToolCalls != 1 ||
		!qualification.Harness.RootEligible {
		t.Fatalf("Harness qualification did not become root eligible: %#v", qualification)
	}
	route := desktopControlRequest(plane.Handler(), http.MethodPost,
		"/api/v1/models/routes/code", "desktop-real-model-route-0001",
		fmt.Sprintf(`{"version":%q,"provider":"anthropic","model":%q}`,
			modelregistry.RouteControlProtocolVersion, model))
	if route.Code != http.StatusAccepted {
		t.Fatalf("route selection status=%d body=%s", route.Code, route.Body.String())
	}

	createdResponse := desktopControlRequest(plane.Handler(), http.MethodPost, "/api/v1/threads",
		"desktop-real-model-run-0001",
		fmt.Sprintf(`{"version":%q,"goal":"Verify a real Desktop model turn",`+
			`"workspace_id":%q,"profile":"code","surface":"code","phase":"deliver"}`,
			domain.ThreadCreationProtocolVersion, workspace.ID))
	if createdResponse.Code != http.StatusAccepted {
		t.Fatalf("Thread creation status=%d body=%s", createdResponse.Code,
			createdResponse.Body.String())
	}
	var created httpapi.ThreadCreationControlView
	decodeDesktopControlData(t, createdResponse, &created)
	// This text-only fixture has no command runtime; explicitly select Preview.
	selected, err := application.NewRunExecutionProfileService(plane.stateStore).Change(t.Context(), application.ChangeRunExecutionProfileRequest{
		RunID: created.Run.ID, Profile: string(domain.RunExecutionProfilePreview),
		OperationKey: "desktop-real-model-preview", RequestedBy: "test_operator", Reason: "Verify a text-only model turn",
	})
	if err != nil || selected.Profile.Profile != domain.RunExecutionProfilePreview || selected.Profile.IsDefaultPreview() {
		t.Fatalf("select explicit Preview: %+v err=%v", selected, err)
	}

	executedResponse := desktopControlRequest(plane.Handler(), http.MethodPost,
		"/api/v1/threads/"+created.Thread.ID+"/turns", "desktop-real-model-message-0001",
		`{"version":"thread_message_submission.v1","content":"请验证真实模型桌面对话闭环"}`)
	if executedResponse.Code != http.StatusAccepted {
		t.Fatalf("Thread turn status=%d body=%s", executedResponse.Code,
			executedResponse.Body.String())
	}
	var executed httpapi.ThreadMessageControlView
	decodeDesktopControlData(t, executedResponse, &executed)
	if !executed.ExecutionStarted || !executed.ModelCalled || executed.ToolCalled ||
		executed.RunID != created.Run.ID ||
		executed.Steering.Status != string(domain.OperatorSteeringCommitted) {
		checkpoint, _, checkpointErr := plane.stateStore.GetSupervisorCheckpoint(
			t.Context(), created.Run.ID)
		t.Fatalf("real model Thread facade did not complete one safe turn: %#v checkpoint=%#v err=%v",
			executed, checkpoint, checkpointErr)
	}

	messagesResponse := desktopAPIRequest(plane.Handler(),
		"/api/v1/sessions/"+created.Session.ID+"/messages?include_compacted=true")
	if messagesResponse.Code != http.StatusOK {
		t.Fatalf("messages status=%d body=%s", messagesResponse.Code, messagesResponse.Body.String())
	}
	var messages []httpapi.MessageView
	decodeDesktopControlData(t, messagesResponse, &messages)
	if len(messages) != 2 || messages[0].Role != "user" ||
		messages[0].Content != "请验证真实模型桌面对话闭环" ||
		messages[1].Role != "assistant" || messages[1].Content != assistantReply {
		t.Fatalf("Desktop chat messages were not durably committed: %#v", messages)
	}
	if calls.Load() != 3 {
		t.Fatalf("Provider call count=%d, want two qualification calls and one chat call", calls.Load())
	}
}

func decodeDesktopControlData(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	var envelope desktopAPIEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		t.Fatal(err)
	}
}

func writeAnthropicTextSSE(t *testing.T, writer http.ResponseWriter, model string, text string) {
	t.Helper()
	writeAnthropicSSE(t, writer,
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

func writeAnthropicToolSSE(t *testing.T, writer http.ResponseWriter, model string, nonce string) {
	t.Helper()
	writeAnthropicSSE(t, writer,
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

func writeAnthropicSSE(t *testing.T, writer http.ResponseWriter, events ...any) {
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
