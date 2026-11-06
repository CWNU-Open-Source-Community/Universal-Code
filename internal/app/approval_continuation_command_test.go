package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/webevidence"
)

type cliApprovalProvider struct {
	mu        sync.Mutex
	responses []*llm.ChatResponse
	requests  int
	lastTools []llm.ToolSpec
}

func (*cliApprovalProvider) Name() string { return "cli-approval-test" }

func (*cliApprovalProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "model", Provider: "cli-approval-test",
		Capabilities: []string{"chat", "tools"}}}, nil
}

func (p *cliApprovalProvider) Chat(_ context.Context,
	request llm.ChatRequest,
) (*llm.ChatResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests++
	p.lastTools = append([]llm.ToolSpec(nil), request.Tools...)
	if len(p.responses) == 0 {
		return nil, errors.New("unexpected CLI approval model request")
	}
	response := *p.responses[0]
	p.responses = p.responses[1:]
	response.ToolCalls = append([]llm.ToolCall(nil), response.ToolCalls...)
	return &response, nil
}

func (p *cliApprovalProvider) StreamChat(ctx context.Context,
	request llm.ChatRequest,
) (<-chan llm.ChatChunk, error) {
	response, err := p.Chat(ctx, request)
	if err != nil {
		return nil, err
	}
	chunks := make(chan llm.ChatChunk, 2)
	if response.Text != "" {
		chunks <- llm.ChatChunk{Text: response.Text}
	}
	chunks <- llm.FinalChatChunk(response)
	close(chunks)
	return chunks, nil
}

func (*cliApprovalProvider) SupportsTools(string) bool    { return true }
func (*cliApprovalProvider) SupportsVision(string) bool   { return false }
func (*cliApprovalProvider) SupportsJSONMode(string) bool { return true }

func (p *cliApprovalProvider) requestCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests
}

func (p *cliApprovalProvider) lastRequestHasTool(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, tool := range p.lastTools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

type cliApprovalFetchBackend struct {
	mu    sync.Mutex
	calls int
}

func (b *cliApprovalFetchBackend) Fetch(_ context.Context, rawURL string,
	_ webevidence.NetworkAuthority, _ webevidence.RobotsPolicy,
) (webevidence.FetchedContent, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	return webevidence.FetchedContent{RequestedURL: rawURL, FinalURL: rawURL,
		HTTPStatus: http.StatusOK,
		RawDigest:  webevidence.DigestBytes([]byte("approval continuation evidence")),
		Robots:     "allowed", Parsed: webevidence.ParsedDocument{
			Title: "Evidence", Body: "bounded evidence", MIME: "text/html",
			Charset: "utf-8",
		}}, nil
}

func (b *cliApprovalFetchBackend) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

type cliApprovalFixture struct {
	state      *store.SQLiteStore
	run        domain.Run
	approvalID string
	auth       domain.WebFetchAuthorization
	provider   *cliApprovalProvider
	backend    *cliApprovalFetchBackend
	router     *llm.Router
	handoff    *application.RunExecutionHandoffService
}

func newCLIApprovalFixture(t *testing.T, home string,
	continuation *llm.ChatResponse,
) cliApprovalFixture {
	t.Helper()
	return newCLIApprovalFixtureWithPermission(t, home, continuation, domain.RunExecutionPermissionAsk)
}

func newCLIApprovalFixtureWithPermission(t *testing.T, home string,
	continuation *llm.ChatResponse, permission domain.RunExecutionPermissionMode,
) cliApprovalFixture {
	t.Helper()
	state, err := store.Open(filepath.Join(home, "cyberagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	workspaceID, profile := "", "review"
	if permission == domain.RunExecutionPermissionFull {
		profile = "code"
		workspaceID = "cli-approval-workspace"
		if err := state.SaveWorkspace(t.Context(), store.WorkspaceRecord{
			ID: workspaceID, Name: "cli-approval", RootPath: home,
		}); err != nil {
			_ = state.Close()
			t.Fatal(err)
		}
	}
	_, run, err := application.NewRunService(state).Create(t.Context(),
		application.CreateRunRequest{
			Goal: "review one public source", Profile: profile, Surface: "code",
			Phase: "deliver", ModelRoute: "cli-approval-test/model", Interactive: true,
			WorkspaceID: workspaceID,
			NetworkMode: "disabled",
			Budget:      domain.Budget{MaxTurns: 4, MaxToolCalls: 4},
		})
	if err != nil {
		_ = state.Close()
		t.Fatal(err)
	}
	if permission != domain.RunExecutionPermissionAsk {
		if _, err := application.NewThreadExecutionPermissionService(state,
			cliExecutionPermissionCapabilities(true, true)).Change(t.Context(),
			application.ChangeThreadExecutionPermissionRequest{
				ThreadID: domain.InitialThreadID(run.ID), Mode: string(permission), OperationKey: "cli-approval-initial-permission",
				RequestedBy: "test_operator", ConfirmFull: permission == domain.RunExecutionPermissionFull,
			}); err != nil {
			_ = state.Close()
			t.Fatal(err)
		}
	}
	responses := []*llm.ChatResponse{{
		Provider: "cli-approval-test", Model: "model",
		Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
		ToolCalls: []llm.ToolCall{{ID: "web-fetch-review",
			Name: string(toolgateway.WebFetchTool), Arguments: json.RawMessage(
				`{"version":"web_fetch.v1","url":"https://docs.example.com/report"}`)}},
	}}
	if continuation != nil {
		responses = append(responses, continuation)
	}
	provider := &cliApprovalProvider{responses: responses}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	checker := policy.NewDefaultChecker()
	backend := &cliApprovalFetchBackend{}
	handoff := application.NewRunExecutionHandoffService(state, router, checker).
		WithWebEvidence(webevidence.NewService(state, nil, backend)).
		WithWebFetchAuthorizationScheduler(true)
	turns := application.NewThreadTurnService(state,
		application.NewRunLifecycleControlService(state), handoff)
	first, err := turns.Execute(t.Context(), application.ExecuteThreadTurnRequest{
		Version:  domain.ThreadMessageProtocolVersion,
		ThreadID: domain.InitialThreadID(run.ID), Content: "Read the reviewed source",
		OperationKey: "cli-approval-original-input", RequestedBy: "test_operator",
	})
	if err != nil || first.Submission.Run.Status != domain.RunWaitingApproval {
		_ = state.Close()
		t.Fatalf("initial approval boundary=%#v err=%v", first, err)
	}
	run = first.Submission.Run
	actualPermission, err := state.GetRunExecutionPermission(t.Context(), run.ID)
	if err != nil || actualPermission.Mode != permission {
		_ = state.Close()
		t.Fatalf("approval Run permission=%+v want=%s err=%v", actualPermission, permission, err)
	}
	records, err := state.ListApprovals(t.Context(), approval.ListFilter{
		RunID: run.ID, Status: approval.StatusPending, Limit: 10,
	})
	if err != nil || len(records) != 1 {
		_ = state.Close()
		t.Fatalf("pending approvals=%#v err=%v", records, err)
	}
	auth, err := state.GetWebFetchAuthorizationByApproval(t.Context(), records[0].ID)
	if err != nil {
		_ = state.Close()
		t.Fatal(err)
	}
	if records[0].RunID != run.ID || records[0].SessionID != run.SessionID ||
		auth.RunID != run.ID || auth.SessionID != run.SessionID || auth.ApprovalID != records[0].ID {
		_ = state.Close()
		t.Fatalf("approval fixture crossed Run or Session: run=%+v approval=%+v authorization=%+v", run, records[0], auth)
	}
	return cliApprovalFixture{state: state, run: run, approvalID: records[0].ID,
		auth: auth, provider: provider, backend: backend, router: router, handoff: handoff}
}

func cliApprovalTextResponse(t *testing.T, message string) *llm.ChatResponse {
	t.Helper()
	encoded, err := json.Marshal(domain.RootAction{Version: domain.RootLifecycleVersion,
		Kind: domain.RootActionFinish, Message: message, Summary: "done"})
	if err != nil {
		t.Fatal(err)
	}
	return &llm.ChatResponse{Text: string(encoded), Provider: "cli-approval-test",
		Model: "model", Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}
}

func executeCLIApproval(t *testing.T, router *llm.Router,
	action, approvalID string, flags ...string,
) (string, string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	args := []string{"approval", action, approvalID}
	if action == "deny" {
		args = append(args, "--reason", "operator review")
	}
	args = append(args, flags...)
	code := executeContextWithConfig(t.Context(), args, &out, &errOut, func(app *App) {
		app.router = router
	})
	return out.String(), errOut.String(), code
}

func decideCLIApprovalFixture(t *testing.T, fixture cliApprovalFixture,
	action application.ApprovalControlAction,
) {
	t.Helper()
	_, err := application.NewApprovalControlService(fixture.state,
		toolgateway.New(fixture.state, policy.NewDefaultChecker()), policy.NewDefaultChecker()).
		Decide(t.Context(), application.DecideApprovalControlRequest{
			Version: application.ApprovalControlProtocolVersion, RunID: fixture.run.ID,
			ApprovalID: fixture.approvalID, Action: action,
			OperationKey: "cli-approval-" + fixture.approvalID + "-approve-once",
			ReviewedBy:   "cli_operator",
		})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCLIApprovalCompletedContinuationReplaysWithoutModelOrTool(t *testing.T) {
	home := newCanonicalCLIHome(t)
	t.Setenv("CYBERAGENT_HOME", home)
	fixture := newCLIApprovalFixture(t, home,
		cliApprovalTextResponse(t, "reviewed source observed"))
	defer fixture.state.Close()
	decideCLIApprovalFixture(t, fixture, application.ApprovalControlApproveOnce)
	result, replayed, err := fixture.handoff.ResumeWebFetchAuthorization(t.Context(),
		fixture.run.ID, fixture.auth.ID)
	if err != nil || replayed || result.Turn == 0 || fixture.backend.callCount() != 1 {
		t.Fatalf("initial continuation=%#v replayed=%t fetches=%d err=%v",
			result, replayed, fixture.backend.callCount(), err)
	}
	modelsBefore, fetchesBefore := fixture.provider.requestCount(), fixture.backend.callCount()
	for attempt := 0; attempt < 2; attempt++ {
		stdout, stderr, code := executeCLIApproval(t, fixture.router, "approve-once",
			fixture.approvalID)
		if code != 0 || stderr != "" ||
			!strings.Contains(stdout, "decision_replayed: true") ||
			!strings.Contains(stdout, "continuation: completed") ||
			!strings.Contains(stdout, "continuation_replayed: true") {
			t.Fatalf("replay %d stdout=%q stderr=%q code=%d", attempt, stdout, stderr, code)
		}
	}
	if fixture.provider.requestCount() != modelsBefore ||
		fixture.backend.callCount() != fetchesBefore {
		t.Fatalf("replay executed work: models=%d/%d fetches=%d/%d",
			fixture.provider.requestCount(), modelsBefore,
			fixture.backend.callCount(), fetchesBefore)
	}
}

func TestCLIApprovalDenyContinuesOnceAndReplaysAcrossApps(t *testing.T) {
	home := newCanonicalCLIHome(t)
	t.Setenv("CYBERAGENT_HOME", home)
	fixture := newCLIApprovalFixture(t, home,
		cliApprovalTextResponse(t, "denial observed"))
	defer fixture.state.Close()
	first, stderr, code := executeCLIApproval(t, fixture.router, "deny", fixture.approvalID)
	if code != 0 || stderr != "" || !strings.Contains(first, "continuation: completed") ||
		fixture.provider.requestCount() != 2 || fixture.backend.callCount() != 0 {
		t.Fatalf("first deny stdout=%q stderr=%q code=%d models=%d fetches=%d",
			first, stderr, code, fixture.provider.requestCount(), fixture.backend.callCount())
	}
	second, stderr, code := executeCLIApproval(t, fixture.router, "deny", fixture.approvalID)
	if code != 0 || stderr != "" ||
		!strings.Contains(second, "decision_replayed: true") ||
		!strings.Contains(second, "continuation_replayed: true") ||
		fixture.provider.requestCount() != 2 || fixture.backend.callCount() != 0 {
		t.Fatalf("replayed deny stdout=%q stderr=%q code=%d models=%d fetches=%d",
			second, stderr, code, fixture.provider.requestCount(), fixture.backend.callCount())
	}
}

func TestCLIApprovalContinuationRequiresInvocationFullConfirmation(t *testing.T) {
	for _, test := range []struct {
		name       string
		permission domain.RunExecutionPermissionMode
		flags      []string
		wantError  string
		full       bool
	}{
		{name: "confirmed Full", permission: domain.RunExecutionPermissionFull, full: true,
			flags: []string{"--enable-permission-control", "--enable-danger-full-access", "--confirm-full"}},
		{name: "cold Full", permission: domain.RunExecutionPermissionFull,
			flags: []string{"--enable-permission-control", "--enable-danger-full-access"}},
		{name: "no gates", permission: domain.RunExecutionPermissionFull},
		{name: "missing gates", permission: domain.RunExecutionPermissionFull,
			flags: []string{"--confirm-full"}, wantError: "requires --enable-permission-control"},
		{name: "Ask cannot confirm Full", permission: domain.RunExecutionPermissionAsk,
			flags: []string{"--enable-permission-control", "--enable-danger-full-access", "--confirm-full"}, wantError: "current Full preference"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := newCanonicalCLIHome(t)
			t.Setenv("CYBERAGENT_HOME", home)
			fixture := newCLIApprovalFixtureWithPermission(t, home,
				cliApprovalTextResponse(t, "exact denial observed"), test.permission)
			defer fixture.state.Close()
			if _, err := application.ChangeBrowserNetworkAccess(t.Context(), fixture.state, nil, domain.ChangeBrowserNetworkAccessRequest{
				Version: domain.BrowserNetworkAccessVersion, RunID: fixture.run.ID, ExpectedRevision: 1,
				BrowserNetworkAccess: domain.BrowserNetworkInternet, OperationKey: "cli-continuation-browser-network", RequestedBy: "test_operator",
			}); err != nil {
				t.Fatal(err)
			}
			stdout, stderr, code := executeCLIApproval(t, fixture.router, "deny", fixture.approvalID, test.flags...)
			decision, err := fixture.state.GetApproval(t.Context(), fixture.approvalID)
			if err != nil || decision.Status != approval.StatusDenied || !strings.Contains(stdout, "decision_saved: true") {
				t.Fatalf("exact decision was not saved: %+v err=%v code=%d stdout=%q stderr=%q", decision, err, code, stdout, stderr)
			}
			if fixture.backend.callCount() != 0 {
				t.Fatal("denied fetch executed")
			}
			if test.wantError != "" {
				if code == 0 || !strings.Contains(stderr, test.wantError) || fixture.provider.requestCount() != 1 {
					t.Fatalf("invalid continuation: code=%d stderr=%s models=%d", code, stderr, fixture.provider.requestCount())
				}
				return
			}
			if code != 0 || stderr != "" || !strings.Contains(stdout, "continuation: completed") || fixture.provider.requestCount() != 2 {
				t.Fatalf("continuation: code=%d stdout=%s stderr=%s models=%d", code, stdout, stderr, fixture.provider.requestCount())
			}
			if got, want := fixture.provider.lastRequestHasTool("browser_status"), test.full && browserruntime.AgentBrowserPlatformAvailable(); got != want {
				t.Fatalf("continuation browser advertised=%t want=%t", got, want)
			}
			permission, err := fixture.state.GetRunExecutionPermission(t.Context(), fixture.run.ID)
			if err != nil || permission.Mode != test.permission ||
				cliExecutionPermissionCapabilities(true, true).AllowsSnapshot(permission) {
				t.Fatalf("continuation persisted Full authority: %+v err=%v", permission, err)
			}
		})
	}
}

func TestCLIApprovalDoesNotWakePausedOrCancelledRun(t *testing.T) {
	for _, status := range []domain.RunStatus{domain.RunPaused, domain.RunCancelled} {
		t.Run(string(status), func(t *testing.T) {
			home := newCanonicalCLIHome(t)
			t.Setenv("CYBERAGENT_HOME", home)
			continuation := cliApprovalTextResponse(t, "must not be used")
			if status == domain.RunPaused {
				continuation = nil
			}
			fixture := newCLIApprovalFixture(t, home, continuation)
			defer fixture.state.Close()
			decideCLIApprovalFixture(t, fixture, application.ApprovalControlApproveOnce)
			if status == domain.RunPaused {
				if _, _, err := fixture.handoff.ResumeWebFetchAuthorization(t.Context(),
					fixture.run.ID, fixture.auth.ID); err == nil {
					t.Fatal("expected the injected provider failure to pause the Run")
				}
			} else if _, err := application.NewRunService(fixture.state).Cancel(t.Context(),
				fixture.run.ID); err != nil {
				t.Fatal(err)
			}
			modelsBefore := fixture.provider.requestCount()
			fetchesBefore := fixture.backend.callCount()
			stdout, stderr, code := executeCLIApproval(t, fixture.router, "approve-once",
				fixture.approvalID)
			if code != 0 || stderr != "" ||
				!strings.Contains(stdout, "continuation: not_started") ||
				!strings.Contains(stdout, "continuation_replayed: false") {
				t.Fatalf("status=%s stdout=%q stderr=%q code=%d", status, stdout, stderr, code)
			}
			stored, err := fixture.state.GetRun(t.Context(), fixture.run.ID)
			if err != nil || stored.Status != status ||
				fixture.provider.requestCount() != modelsBefore ||
				fixture.backend.callCount() != fetchesBefore {
				t.Fatalf("status=%s stored=%#v models=%d/%d fetches=%d err=%v",
					status, stored, fixture.provider.requestCount(), modelsBefore,
					fixture.backend.callCount(), err)
			}
		})
	}
}

func TestCLIFileReviewKeepsExactDecisionSeparateFromContinuationFull(t *testing.T) {
	for _, test := range []struct {
		name, action string
		flags        []string
		denied       bool
	}{
		{name: "approved with Full", action: "review-approve",
			flags: []string{"--enable-permission-control", "--enable-danger-full-access", "--confirm-full"}},
		{name: "denied with Full", action: "review-deny",
			flags: []string{"--enable-permission-control", "--enable-danger-full-access", "--confirm-full"}},
		{name: "cold Full", action: "review-approve"},
		{name: "missing gates", action: "review-approve", flags: []string{"--confirm-full"}, denied: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := newCanonicalCLIHome(t)
			t.Setenv("CYBERAGENT_HOME", home)
			runID := createCLIAgentBrowserRun(t, home, domain.RunExecutionPermissionFull, domain.BrowserNetworkOff)
			state, err := store.Open(filepath.Join(home, "cyberagent.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer state.Close()
			run, err := state.GetRun(t.Context(), runID)
			if err != nil {
				t.Fatal(err)
			}
			edit, err := fileedit.NewManager(state).Propose(t.Context(), fileedit.Proposal{
				SessionID: run.SessionID, WorkspaceID: "cli-agent-browser-workspace", WorkspaceRoot: home,
				Path: "manual-review.txt", ProposedText: "review does not apply this file\n",
			})
			if err != nil {
				t.Fatal(err)
			}
			args := append([]string{"edit", test.action, runID, edit.ID}, test.flags...)
			stdout, stderr, code := executeTestCommand(t, args...)
			if !strings.Contains(stdout, "file_written: false") {
				t.Fatalf("review was not saved: code=%d stdout=%s stderr=%s", code, stdout, stderr)
			}
			if test.denied {
				if code != 5 || !strings.Contains(stderr, "review saved; continuation setup failed") {
					t.Fatalf("missing continuation gates: code=%d stderr=%s", code, stderr)
				}
			} else if code != 0 || stderr != "" || !strings.Contains(stdout, "continuation: not_started") {
				t.Fatalf("manual review started a continuation: code=%d stdout=%s stderr=%s", code, stdout, stderr)
			}
			stored, err := state.GetFileEdit(t.Context(), edit.ID)
			want := fileedit.StatusApproved
			if test.action == "review-deny" {
				want = fileedit.StatusDenied
			}
			if err != nil || stored.Status != want {
				t.Fatalf("saved review=%+v err=%v", stored, err)
			}
			if _, err := os.Stat(filepath.Join(home, "manual-review.txt")); !os.IsNotExist(err) {
				t.Fatalf("review wrote the file: %v", err)
			}
		})
	}
}
