package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/modelregistry"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
)

type workflowHostProvider struct {
	llm.MockProvider
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
	text    string
}

func (p *workflowHostProvider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	if p.calls.Add(1) == 1 {
		close(p.started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-p.release:
		}
	}
	return &llm.ChatResponse{Text: p.text, Model: req.Model, Provider: p.Name(),
		Usage: llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}, nil
}

func (p *workflowHostProvider) StreamChat(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	response, err := p.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	chunks := make(chan llm.ChatChunk, 2)
	chunks <- llm.ChatChunk{Text: response.Text}
	chunks <- llm.FinalChatChunk(response)
	close(chunks)
	return chunks, nil
}

func TestWorkflowHostContinuesAfterHTTPClientDisconnects(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "workflow-host.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	action, err := json.Marshal(domain.SpecialistAction{
		Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionFinish,
		Message: "finished", Report: &domain.CompletionReport{
			Version: domain.CompletionReportVersion, Outcome: domain.CompletionSucceeded,
			Summary: "host continued the saved workflow", WorkItemIDs: []string{}, NoteIDs: []string{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &workflowHostProvider{started: make(chan struct{}), release: make(chan struct{}), text: string(action)}
	router := llm.NewRouter(llm.ModelRef{Provider: "mock", Model: "model"})
	router.RegisterProvider(provider)
	service := application.NewWorkflowService(st, application.NewSpecialistExecutionService(st, router, policy.NewDefaultChecker()))
	runs := application.NewRunService(st)
	_, run, err := runs.Create(t.Context(), application.CreateRunRequest{Goal: "continue without the page", Profile: "code",
		ModelRoute: "mock/model", Budget: domain.Budget{MaxTurns: 8, MaxTokens: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runs.Start(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	spec := domain.WorkflowSpec{Version: domain.WorkflowVersion, Name: "host workflow",
		Limits: domain.WorkflowLimits{MaxSteps: 8, MaxJobs: 2, MaxTokens: 1000, MaxTurns: 4, TimeoutMillis: 60000}}
	for _, id := range []string{"first", "second"} {
		spec.Steps = append(spec.Steps, domain.WorkflowStep{ID: id, Kind: "worker", Task: &domain.ChildTask{
			Title: id, Goal: "report completion", Skills: []string{"model.chat"}, TurnLimit: 1, TokenLimit: 100, TimeoutMillis: 10000,
		}})
	}
	definition, err := service.Save(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	models := modelregistry.New(nil)
	api, err := New(st, Config{AccessToken: testAccessToken, ControlToken: testControlToken,
		ModelControlEnabled: true, WorkflowController: service,
		ModelControlController:     application.NewModelControlService(models, st),
		ThreadModelRouteController: application.NewThreadModelRouteService(st, models)})
	if err != nil {
		t.Fatal(err)
	}
	hostCtx, stopHost := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- service.Run(hostCtx) }()
	t.Cleanup(func() {
		stopHost()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("workflow host did not stop")
		}
	})
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	pageCtx, closePage := context.WithCancel(t.Context())
	t.Cleanup(closePage)
	body := `{"version":"workflow_start.v1","definition_id":"` + definition.ID + `","confirm_start":true}`
	request, err := http.NewRequestWithContext(pageCtx, http.MethodPost,
		server.URL+"/api/v1/runs/"+run.ID+"/workflows", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testControlToken)
	request.Header.Set("Idempotency-Key", "workflow-http-host-start")
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var envelope apiTestEnvelope
	err = json.NewDecoder(response.Body).Decode(&envelope)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("start: status=%d error=%v body=%+v", response.StatusCode, err, envelope)
	}
	var execution domain.WorkflowExecution
	if err = json.Unmarshal(envelope.Data, &execution); err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.started:
	case <-time.After(8 * time.Second):
		t.Fatal("host did not start the first worker")
	}
	// Remove the client and HTTP server while the first worker is still active.
	// No client sends advance, resume, or another control request.
	closePage()
	client.CloseIdleConnections()
	server.CloseClientConnections()
	server.Close()
	close(provider.release)
	deadline := time.After(10 * time.Second)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for execution.Status != "completed" {
		select {
		case <-deadline:
			t.Fatalf("workflow did not finish without HTTP: %+v", execution)
		case <-tick.C:
			execution, err = service.Get(t.Context(), execution.ID)
			if err != nil || execution.Status == "failed" {
				t.Fatalf("workflow: %+v %v", execution, err)
			}
		}
	}
	if provider.calls.Load() != 2 || execution.State.Jobs != 2 {
		t.Fatalf("expected two distinct completed workers: calls=%d execution=%+v", provider.calls.Load(), execution)
	}
	// A newly connected page reads the finished state from the same backend.
	reopened := httptest.NewServer(api)
	defer reopened.Close()
	read, _ := http.NewRequest(http.MethodGet, reopened.URL+"/api/v1/runs/"+run.ID+"/workflows/"+execution.ID, nil)
	read.Header.Set("Authorization", "Bearer "+testAccessToken)
	response, err = client.Do(read)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if err = json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	var observed domain.WorkflowExecution
	if err = json.Unmarshal(envelope.Data, &observed); err != nil || response.StatusCode != http.StatusOK || observed.Status != "completed" {
		t.Fatalf("reconnected page did not observe completion: %+v %v", observed, err)
	}
}
