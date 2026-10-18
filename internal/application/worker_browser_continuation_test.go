package application_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
)

func TestExecutionWorkerRealBrowserSurvivesContinue(t *testing.T) {
	if runtime.GOOS != "windows" || os.Getenv("TRAVERSE_TEST_AGENT_BROWSER") != "1" {
		t.Skip("opt-in real installed Windows browser")
	}
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><title>Continuation</title><button onclick="document.querySelector('p').textContent='Retained browser clicked'">Continue action</button><p>Waiting</p>`)
	}))
	defer page.Close()
	provider := &specialistTestProvider{}
	st, run, child, _ := newScopedSubagentFixture(t, provider, domain.Budget{MaxTurns: 20, MaxTokens: 10000, MaxToolCalls: 30}, 4, 1000, false, domain.SpecialistAdmission{Worker: &domain.WorkerProfile{}, ToolCallLimit: 12}, "", t.TempDir())
	caps := workerIsolationFullMode(t, st, run.ID)
	product := browserruntime.BrowserProductEdge
	if configured := os.Getenv("TRAVERSE_TEST_AGENT_BROWSER_PRODUCT"); configured != "" {
		product = browserruntime.BrowserProduct(configured)
	}
	browser := application.NewAgentBrowserService(st, application.AgentBrowserOptions{HomePath: t.TempDir(), Capabilities: caps, Headless: true, Product: product, SessionLifetime: 2 * time.Minute})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := browser.Shutdown(ctx); err != nil {
			t.Errorf("browser cleanup: %v", err)
		}
	})
	var before browserruntime.AgentBrowserSnapshot
	var button string
	tool := func(id, name string, payload any) (llm.ChatResponse, error) {
		raw, _ := json.Marshal(payload)
		return llm.ChatResponse{ToolCalls: []llm.ToolCall{{ID: id, Name: name, Arguments: raw}}, Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil
	}
	provider.respond = func(index int, request llm.ChatRequest) (llm.ChatResponse, error) {
		last := func() string {
			messages := request.Messages
			if len(messages) == 0 || len(messages[len(messages)-1].ToolResults) != 1 {
				t.Fatalf("missing browser result at %d", index)
			}
			content := messages[len(messages)-1].ToolResults[0].Content
			if !strings.Contains(content, `"status":"completed"`) {
				t.Fatalf("browser call failed: %s", content)
			}
			return content
		}
		switch index {
		case 0:
			return tool("action", "browser_navigate", map[string]any{"version": "browser_navigate.v2", "url": page.URL})
		case 1:
			last()
			return tool("snapshot", "browser_snapshot", map[string]any{"version": "browser_snapshot.v2"})
		case 2:
			var receipt struct {
				Result browserruntime.AgentBrowserSnapshot `json:"result"`
			}
			if err := json.Unmarshal([]byte(last()), &receipt); err != nil {
				t.Fatal(err)
			}
			before = receipt.Result
			for _, element := range before.Elements {
				if element.Name == "Continue action" && element.Role == "button" {
					button = element.Ref
				}
			}
			if before.SessionID == "" || button == "" {
				t.Fatalf("missing owned snapshot: %+v", before)
			}
			return llm.ChatResponse{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue, Message: "Continue with the existing browser page."}), Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil
		case 3:
			// Provider call IDs are only unique inside their attempt.
			return tool("action", "browser_click", map[string]any{"version": "browser_click.v2", "snapshot_id": before.SnapshotID, "element_ref": button})
		case 4:
			last()
			return tool("snapshot", "browser_snapshot", map[string]any{"version": "browser_snapshot.v2"})
		case 5:
			var receipt struct {
				Result browserruntime.AgentBrowserSnapshot `json:"result"`
			}
			content := last()
			if err := json.Unmarshal([]byte(content), &receipt); err != nil || receipt.Result.SessionID != before.SessionID || !strings.Contains(content, "Retained browser clicked") {
				t.Fatalf("browser continuation lost session or page: %s %v", content, err)
			}
			return workerIsolationFinish(t), nil
		default:
			t.Fatalf("unexpected browser model call %d", index)
			return llm.ChatResponse{}, nil
		}
	}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	host := application.NewAgentRunner(st, router, policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(caps).WithAgentBrowser(browser)
	service := application.NewSpecialistExecutionService(st, router, policy.NewDefaultChecker()).WithWorkerRuntime(host)
	for i, status := range []domain.AgentAttemptStatus{domain.AgentAttemptContinued, domain.AgentAttemptFinished} {
		result, err := service.ExecuteSpecialist(t.Context(), application.ExecuteSpecialistRequest{RunID: run.ID, AgentID: child.ID, OperationKey: fmt.Sprintf("browser-continue-%d", i)})
		if err != nil || result.AttemptStatus != status {
			t.Fatalf("browser turn %d: %+v %v", i, result, err)
		}
	}
}
