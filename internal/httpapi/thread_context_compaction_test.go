package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/store"
)

type threadCompactHTTPProvider struct {
	llm.Provider
	calls int
}

func (p *threadCompactHTTPProvider) Name() string { return "manual-http" }
func (p *threadCompactHTTPProvider) Chat(_ context.Context, r llm.ChatRequest) (*llm.ChatResponse, error) {
	p.calls++
	return &llm.ChatResponse{Text: "{\"version\":\"generated_handoff.v1\",\"summary\":\"已审阅旧会话，后续继续检查尚未完成的工作。\"}", Usage: llm.Usage{InputTokens: 11, OutputTokens: 4, TotalTokens: 15}}, nil
}
func TestThreadContextCompactionHTTPUsesRealServiceAndControlBoundary(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "manual-http.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{Goal: "当前对话", Profile: "review", ModelRoute: "manual-http/model", Budget: domain.Budget{MaxTurns: 5}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		if _, err = st.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, "user", strings.Repeat("历史事实 ", 100))); err != nil {
			t.Fatal(err)
		}
	}
	p := &threadCompactHTTPProvider{Provider: llm.NewMockProvider()}
	router := llm.NewRouter(llm.ModelRef{Provider: p.Name(), Model: "model"})
	router.RegisterProvider(p)
	api, err := New(st, Config{AccessToken: testAccessToken, ControlToken: testControlToken, RunExecutionEnabled: true, RunExecutionController: application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker()), ThreadContextCompactionController: application.NewThreadContextCompactionService(st, router)})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/threads/" + domain.InitialThreadID(run.ID) + "/context/compact"
	body, _ := json.Marshal(ThreadContextCompactionRequestView{Version: domain.ThreadContextCompactionVersion, ExpectedRunID: run.ID, Focus: "保留未完成任务"})
	unauthorized := performSessionMessageRequest(t, api, http.MethodPost, path, testAccessToken, "manual-http-operation-key", "application/json", bytes.NewReader(body))
	if unauthorized.Code != http.StatusUnauthorized || p.calls != 0 {
		t.Fatalf("control auth=%d calls=%d", unauthorized.Code, p.calls)
	}
	response := performSessionMessageRequest(t, api, http.MethodPost, path, testControlToken, "manual-http-operation-key", "application/json", bytes.NewReader(body))
	var result domain.ThreadContextCompactionResult
	decodeDataStatus(t, response, http.StatusOK, &result)
	if result.Status != "completed" || !result.Generated || p.calls != 1 {
		t.Fatalf("real result=%+v calls=%d", result, p.calls)
	}
	response = performSessionMessageRequest(t, api, http.MethodPost, path, testControlToken, "manual-http-operation-key", "application/json", bytes.NewReader(body))
	decodeDataStatus(t, response, http.StatusOK, &result)
	if !result.Replayed || p.calls != 1 {
		t.Fatalf("HTTP replay=%+v calls=%d", result, p.calls)
	}
	invalid := append([]byte(nil), body[:len(body)-1]...)
	invalid = append(invalid, []byte(",\"unexpected\":true}")...)
	response = performSessionMessageRequest(t, api, http.MethodPost, path, testControlToken, "manual-http-other", "application/json", bytes.NewReader(invalid))
	if response.Code != http.StatusBadRequest || p.calls != 1 {
		t.Fatalf("unknown field accepted status=%d calls=%d", response.Code, p.calls)
	}
	operationsPath := path + "ions"
	for _, token := range []string{"", testControlToken} {
		response = performSessionMessageRequest(t, api, http.MethodGet, operationsPath, token, "", "", nil)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("operation query accepted token without read authority: %d", response.Code)
		}
	}
	response = performSessionMessageRequest(t, api, http.MethodGet, operationsPath, testAccessToken, "", "", nil)
	var operations domain.ThreadContextCompactionOperations
	decodeDataStatus(t, response, http.StatusOK, &operations)
	if len(operations.Operations) != 1 || operations.Operations[0].Status != "completed" {
		t.Fatalf("operations=%+v", operations)
	}
	response = performSessionMessageRequest(t, api, http.MethodGet, operationsPath, testAccessToken, "", "application/json", strings.NewReader(`{}`))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("GET accepted a body: %d", response.Code)
	}
	recoverPath := operationsPath + "/" + operations.Operations[0].OperationID + "/recover"
	recoveryBody, _ := json.Marshal(domain.ThreadContextCompactionRecoveryRequest{Version: domain.ThreadContextCompactionRecoveryVersion, ExpectedRunID: run.ID, Action: "resume"})
	for _, token := range []string{"", testAccessToken} {
		response = performSessionMessageRequest(t, api, http.MethodPost, recoverPath, token, "", "application/json", bytes.NewReader(recoveryBody))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("recovery accepted token without control: %d", response.Code)
		}
	}
	response = performSessionMessageRequest(t, api, http.MethodPost, recoverPath, testControlToken, "", "application/json", bytes.NewReader(recoveryBody))
	var recovered domain.ThreadContextCompactionOperationView
	decodeDataStatus(t, response, http.StatusOK, &recovered)
	if recovered.Result == nil || !recovered.Result.Replayed || recovered.Result.SummaryID != result.SummaryID || p.calls != 1 {
		t.Fatalf("recovery=%+v calls=%d", recovered, p.calls)
	}
}
