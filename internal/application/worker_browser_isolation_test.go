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

// Uses an owned headless browser, a local page and the production native worker
// dispatch path. Opt-in follows the existing installed-browser acceptance tests.
func TestExecutionWorkersRealBrowserRejectSiblingSnapshot(t *testing.T) {
	if runtime.GOOS != "windows" || os.Getenv("TRAVERSE_TEST_AGENT_BROWSER") != "1" {
		t.Skip("opt-in real installed Windows browser")
	}
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><title>Worker isolation fixture</title><button onclick="document.querySelector('p').textContent='Worker clicked locally'">Local action</button><p>Not clicked</p>`)
	}))
	defer page.Close()
	provider := &specialistTestProvider{}
	st, run, first, _ := newScopedSubagentFixture(t, provider, domain.Budget{MaxTurns: 20, MaxTokens: 10000, MaxToolCalls: 30}, 4, 1000, false, domain.SpecialistAdmission{Worker: &domain.WorkerProfile{}, ToolCallLimit: 12, MaxChildren: 2}, "", t.TempDir())
	caps := workerIsolationFullMode(t, st, run.ID)
	sibling := workerIsolationSibling(t, st, run, first)
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
	var firstSnapshot, ownSnapshot browserruntime.AgentBrowserSnapshot
	var ownRef, firstRef string
	stage := 0
	second := false
	tool := func(name string, payload any) (llm.ChatResponse, error) {
		raw, _ := json.Marshal(payload)
		return llm.ChatResponse{ToolCalls: []llm.ToolCall{{ID: fmt.Sprintf("browser-step-%d", stage), Name: name, Arguments: raw}}, Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil
	}
	provider.respond = func(_ int, request llm.ChatRequest) (llm.ChatResponse, error) {
		last := func() string {
			messages := request.Messages
			if len(messages) == 0 || len(messages[len(messages)-1].ToolResults) != 1 {
				t.Fatalf("missing browser receipt at stage %d: %+v", stage, messages)
			}
			return messages[len(messages)-1].ToolResults[0].Content
		}
		switch stage {
		case 0:
			stage++
			return tool("browser_navigate", map[string]any{"version": "browser_navigate.v2", "url": page.URL})
		case 1:
			if !strings.Contains(last(), `"status":"completed"`) {
				t.Fatalf("worker navigate failed: %s", last())
			}
			stage++
			return tool("browser_snapshot", map[string]any{"version": "browser_snapshot.v2"})
		case 2:
			var receipt struct {
				Result browserruntime.AgentBrowserSnapshot `json:"result"`
			}
			if err := json.Unmarshal([]byte(last()), &receipt); err != nil || receipt.Result.SnapshotID == "" {
				t.Fatalf("snapshot missing: %s %v", last(), err)
			}
			ownSnapshot = receipt.Result
			ownRef = ""
			for _, element := range ownSnapshot.Elements {
				if element.Role == "button" && element.Name == "Local action" {
					ownRef = element.Ref
				}
			}
			if ownRef == "" {
				t.Fatalf("local button missing: %+v", ownSnapshot)
			}
			stage++
			if second {
				if ownSnapshot.SessionID == firstSnapshot.SessionID || ownSnapshot.SnapshotID == firstSnapshot.SnapshotID {
					t.Fatal("siblings shared browser session or snapshot")
				}
				return tool("browser_click", map[string]any{"version": "browser_click.v2", "snapshot_id": firstSnapshot.SnapshotID, "element_ref": firstRef})
			}
			firstSnapshot, firstRef = ownSnapshot, ownRef
			return tool("browser_click", map[string]any{"version": "browser_click.v2", "snapshot_id": ownSnapshot.SnapshotID, "element_ref": ownRef})
		case 3:
			stage++
			if second {
				if strings.Contains(last(), `"status":"completed"`) {
					t.Fatalf("sibling snapshot was accepted: %s", last())
				}
				t.Logf("sibling snapshot rejected: %s", last())
				return tool("browser_click", map[string]any{"version": "browser_click.v2", "snapshot_id": ownSnapshot.SnapshotID, "element_ref": ownRef})
			}
			if !strings.Contains(last(), `"status":"completed"`) {
				t.Fatalf("own click failed: %s", last())
			}
			return tool("browser_snapshot", map[string]any{"version": "browser_snapshot.v2"})
		case 4:
			stage++
			if second {
				if !strings.Contains(last(), `"status":"completed"`) {
					t.Fatalf("own sibling click failed: %s", last())
				}
				return tool("browser_snapshot", map[string]any{"version": "browser_snapshot.v2"})
			}
			if !strings.Contains(last(), "Worker clicked locally") {
				t.Fatalf("local interaction not reflected: %s", last())
			}
			return workerIsolationFinish(t), nil
		case 5:
			if !second || !strings.Contains(last(), "Worker clicked locally") {
				t.Fatalf("sibling interaction not reflected: %s", last())
			}
			return workerIsolationFinish(t), nil
		default:
			t.Fatalf("unexpected browser model stage %d", stage)
			return llm.ChatResponse{}, nil
		}
	}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	host := application.NewAgentRunner(st, router, policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(caps).WithAgentBrowser(browser)
	service := application.NewSpecialistExecutionService(st, router, policy.NewDefaultChecker()).WithWorkerRuntime(host)
	for index, child := range []domain.AgentNode{first, sibling} {
		stage = 0
		second = index == 1
		result, err := service.ExecuteSpecialist(t.Context(), application.ExecuteSpecialistRequest{RunID: run.ID, AgentID: child.ID, OperationKey: "worker-browser-" + child.ID})
		if err != nil || result.WaitingApproval || result.AttemptStatus != domain.AgentAttemptFinished {
			t.Fatalf("browser worker: %+v %v", result, err)
		}
	}
	t.Logf("real %s workers used isolated browser sessions %s and %s", product, firstSnapshot.SessionID, ownSnapshot.SessionID)
}
