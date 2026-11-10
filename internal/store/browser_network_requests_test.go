package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/toolrun"
)

type networkApprovalProvider struct {
	llm.MockProvider
	url    string
	resume chan struct{}
	calls  int
}

func TestListApprovalsInvalidatesExpiredBrowserRequestsWithoutRuntime(t *testing.T) {
	ctx := t.Context()
	st, err := Open(filepath.Join(t.TempDir(), "expired-browser-queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	mission, run := createStructuredToolTestRun(t, ctx, st, "browser approval expiry")
	if _, err := application.ChangeBrowserNetworkAccess(ctx, st, nil, domain.ChangeBrowserNetworkAccessRequest{Version: domain.BrowserNetworkAccessVersion, RunID: run.ID, ExpectedRevision: 1, BrowserNetworkAccess: domain.BrowserNetworkRestricted, BrowserOrigins: []string{"http://127.0.0.1:4321"}, OperationKey: "expiry-browser-policy-0001", RequestedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err = st.SaveToolRun(ctx, toolrun.ToolRun{ID: "unrelated-shell-approval", SessionID: run.SessionID, WorkspaceID: mission.WorkspaceID, ToolName: toolrun.ShellTool, Command: "echo pending", Status: toolrun.StatusProposed, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	scope, err := browserruntime.NewAgentBrowserOriginScope([]string{"http://127.0.0.1:4321"})
	if err != nil {
		t.Fatal(err)
	}
	a := toolgateway.AgentBrowserCallAuthority{ProtocolVersion: toolgateway.AgentBrowserAuthorityVersion, RunID: run.ID, MissionID: run.MissionID, SessionID: run.SessionID, WorkspaceID: mission.WorkspaceID, RootAgentID: "root", Surface: domain.ExecutionSurfaceCode, Phase: domain.ExecutionPhaseDeliver, Role: domain.AgentRoleRoot, Profile: domain.ProfileCode, PermissionMode: domain.RunExecutionPermissionAsk, ModeRevision: 1, PermissionSnapshotID: "snapshot", PermissionRevision: 1, RunAuthorizationFence: 1, ManagerBootID: "current-boot", BrowserSessionID: "browser-session", SessionGeneration: 1, DeclaredOrigins: []string{"http://127.0.0.1:4321"}, ScopeFingerprint: scope.Fingerprint}
	a.BrowserNetworkAccess, a.BrowserNetworkRevision = domain.BrowserNetworkRestricted, 2
	a.Generation = a.Fingerprint()
	raw, _ := json.Marshal(a)
	for _, name := range []string{"live", "expired"} {
		v := domain.BrowserNetworkRequest{ID: domain.BrowserNetworkRequestPrefix + name, RunID: run.ID, SessionID: run.SessionID, WorkspaceID: mission.WorkspaceID, ManagerBootID: a.ManagerBootID, RuntimeID: "paused-runtime", Fingerprint: strings.Repeat("a", 64), AuthorityJSON: string(raw), Method: "POST", DisplayURL: "http://127.0.0.1:4321/write", BodySHA256: strings.Repeat("b", 64), ExpiresAt: time.Now().Add(time.Minute)}
		if err = st.CreateBrowserNetworkRequest(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = st.db.Exec(`UPDATE browser_network_requests SET expires_at=? WHERE id=?`, ts(time.Now().Add(-time.Second)), domain.BrowserNetworkRequestPrefix+"expired"); err != nil {
		t.Fatal(err)
	}
	filter := approval.ListFilter{RunID: run.ID, Status: approval.StatusPending, Limit: 1}
	for i := 0; i < 2; i++ {
		pending, e := st.ListApprovals(ctx, filter)
		if e != nil || len(pending) != 1 || pending[0].ProposalID != domain.BrowserNetworkRequestPrefix+"live" {
			t.Fatalf("pending queue=%+v err=%v", pending, e)
		}
	}
	source, err := st.GetBrowserNetworkRequest(ctx, domain.BrowserNetworkRequestPrefix+"expired")
	if err != nil || source.Status != "invalidated" {
		t.Fatalf("expired source=%+v err=%v", source, err)
	}
	record, err := st.GetApprovalByProposal(ctx, source.ID)
	if err != nil || record.Status != approval.StatusDenied || record.Version != 2 {
		t.Fatalf("expired approval=%+v err=%v", record, err)
	}
	if err = st.ConsumeBrowserNetworkRequest(ctx, source.ID, source.Fingerprint); err == nil {
		t.Fatal("expired request consumed")
	}
	unrelated, err := st.GetApprovalByProposal(ctx, "unrelated-shell-approval")
	if err != nil || unrelated.Status != approval.StatusPending || unrelated.Version != 1 {
		t.Fatalf("unrelated approval=%+v err=%v", unrelated, err)
	}
}

func TestSchemaV222BrowserRequestsMigrateAndNeverReplayAfterRestart(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "v221-browser.db")
	st := openHistoricalTestDatabase(t, path, 221)
	mission, run := createStructuredToolTestRun(t, ctx, st, "paused browser request recovery")
	// Represent the released v221 config before explicit browser selections.
	if _, err := st.db.Exec(`UPDATE runs SET config_json=json_set(json_remove(config_json,'$.browser_network_access','$.browser_network_revision'),'$.browser_origins',json('["http://127.0.0.1:4321"]')) WHERE id=?`, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.applyMigration(ctx, migrationPlan()[221]); err != nil {
		t.Fatal(err)
	}
	scope, err := browserruntime.NewAgentBrowserOriginScope([]string{"http://127.0.0.1:4321"})
	if err != nil {
		t.Fatal(err)
	}
	a := toolgateway.AgentBrowserCallAuthority{ProtocolVersion: toolgateway.AgentBrowserAuthorityVersion, RunID: run.ID, MissionID: run.MissionID, SessionID: run.SessionID, WorkspaceID: mission.WorkspaceID, RootAgentID: "root", Surface: domain.ExecutionSurfaceCode, Phase: domain.ExecutionPhaseDeliver, Role: domain.AgentRoleRoot, Profile: domain.ProfileCode, PermissionMode: domain.RunExecutionPermissionAsk, ModeRevision: 1, PermissionSnapshotID: "snapshot", PermissionRevision: 1, RunAuthorizationFence: 1, ManagerBootID: "old-boot", BrowserSessionID: "browser-session", SessionGeneration: 1, DeclaredOrigins: []string{"http://127.0.0.1:4321"}, ScopeFingerprint: scope.Fingerprint}
	a.Generation = a.Fingerprint()
	raw, _ := json.Marshal(a)
	for _, name := range []string{"paused", "consumed", "expired"} {
		id := domain.BrowserNetworkRequestPrefix + name
		v := domain.BrowserNetworkRequest{ID: id, RunID: run.ID, SessionID: run.SessionID, WorkspaceID: mission.WorkspaceID, ManagerBootID: "old-boot", RuntimeID: "dead-runtime", Fingerprint: strings.Repeat("a", 64), AuthorityJSON: string(raw), Method: "POST", DisplayURL: "http://127.0.0.1:4321/write", BodySHA256: strings.Repeat("b", 64), BodyBytes: 12, ExpiresAt: time.Now().Add(time.Minute)}
		if err = st.CreateBrowserNetworkRequest(ctx, v); err != nil {
			t.Fatal(err)
		}
		if name == "expired" {
			if _, err = st.db.Exec(`UPDATE browser_network_requests SET expires_at=? WHERE id=?`, ts(time.Now().Add(-time.Second)), id); err != nil {
				t.Fatal(err)
			}
			if _, err = st.DecideApproval(ctx, approval.DecisionRequest{ProposalID: id, IdempotencyKey: "late-approval", Action: approval.ActionApprove, ReviewedBy: "operator"}); err == nil {
				t.Fatal("expired request approved")
			}
		}
		if name == "consumed" {
			if _, err = st.DecideApproval(ctx, approval.DecisionRequest{ProposalID: id, IdempotencyKey: "consume-approval", Action: approval.ActionApprove, ReviewedBy: "operator"}); err != nil {
				t.Fatal(err)
			}
			if err = st.ConsumeBrowserNetworkRequest(ctx, id, v.Fingerprint); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.InvalidateOldBrowserNetworkRequests(ctx, "new-boot"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"paused", "consumed", "expired"} {
		v, e := st.GetBrowserNetworkRequest(ctx, domain.BrowserNetworkRequestPrefix+name)
		if e != nil {
			t.Fatal(e)
		}
		want := "invalidated"
		if name == "consumed" {
			want = "consumed"
		}
		if v.Status != want {
			t.Fatalf("%s state=%s", name, v.Status)
		}
		if e = st.ConsumeBrowserNetworkRequest(ctx, v.ID, v.Fingerprint); e == nil {
			t.Fatalf("restarted %s request replayed", name)
		}
	}
	if err = verifySQLiteForeignKeys(ctx, st.db); err != nil {
		t.Fatal(err)
	}
}

func (p *networkApprovalProvider) Name() string              { return "network-approval" }
func (p *networkApprovalProvider) SupportsTools(string) bool { return true }
func (p *networkApprovalProvider) Chat(ctx context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	p.calls++
	response := &llm.ChatResponse{Provider: p.Name(), Model: "fixture", Usage: llm.Usage{InputTokens: 4, OutputTokens: 4, TotalTokens: 8}}
	if p.calls == 1 {
		payload, _ := json.Marshal(map[string]any{"version": "browser_navigate.v2", "url": p.url})
		response.ToolCalls = []llm.ToolCall{{ID: "navigate", Name: "browser_navigate", Arguments: payload}}
		return response, nil
	}
	select {
	case <-p.resume:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	action, _ := json.Marshal(domain.RootAction{Version: domain.RootLifecycleVersion, Kind: domain.RootActionContinue, Message: "Observed the page request result."})
	response.Text = string(action)
	return response, nil
}
func (p *networkApprovalProvider) StreamChat(ctx context.Context, r llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	v, err := p.Chat(ctx, r)
	if err != nil {
		return nil, err
	}
	out := make(chan llm.ChatChunk, 2)
	if v.Text != "" {
		out <- llm.ChatChunk{Text: v.Text}
	}
	out <- llm.FinalChatChunk(v)
	close(out)
	return out, nil
}

func TestBrowserNetworkRequestRealApprovalControl(t *testing.T) {
	if os.Getenv("AGENT_BROWSER_PRODUCT_TEST") != "1" {
		t.Skip("opt-in real installed browser")
	}
	for _, decision := range []string{"approve", "deny", "close"} {
		t.Run(decision, func(t *testing.T) {
			timeout := 45 * time.Second
			if runtime.GOOS == "darwin" {
				timeout = 4 * time.Minute
			}
			ctx, cancel := context.WithTimeout(t.Context(), timeout)
			defer cancel()
			var writes, undeclared atomic.Int32
			other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { undeclared.Add(1); w.WriteHeader(204) }))
			defer other.Close()
			site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					writes.Add(1)
					w.WriteHeader(204)
					return
				}
				w.Header().Set("Content-Type", "text/html")
				fmt.Fprintf(w, `<!doctype html><title>Scoped approval fixture</title><h1>Declared site</h1><script>fetch('/write?token=do-not-persist',{method:'POST',body:'do-not-persist-body'}).catch(()=>{});fetch(%q,{method:'POST',mode:'no-cors',body:'out-of-scope'}).catch(()=>{});</script>`, other.URL+"/write")
			}))
			defer site.Close()
			st, err := Open(filepath.Join(t.TempDir(), "browser.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			home, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			workspace := WorkspaceRecord{ID: "network-workspace", Name: "network", RootPath: home}
			if err = st.SaveWorkspace(ctx, workspace); err != nil {
				t.Fatal(err)
			}
			_, run, err := application.NewRunService(st).Create(ctx, application.CreateRunRequest{Goal: "Inspect the declared page", Profile: "review", Surface: "code", Phase: "deliver", WorkspaceID: workspace.ID, Interactive: true, ModelRoute: "network-approval/fixture", BrowserOrigins: []string{site.URL}, Budget: domain.Budget{MaxTurns: 4, MaxTokens: 10000, MaxToolCalls: 10}})
			if err != nil {
				t.Fatal(err)
			}
			capabilities := domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, DangerFullAccessEnabled: true, RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
			browser := application.NewAgentBrowserService(st, application.AgentBrowserOptions{HomePath: home, Capabilities: capabilities, Headless: true, SessionLifetime: 5 * time.Minute})
			defer browser.Shutdown(context.Background())
			provider := &networkApprovalProvider{url: site.URL, resume: make(chan struct{})}
			defer close(provider.resume)
			router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "fixture"})
			router.RegisterProvider(provider)
			checker := policy.NewDefaultChecker()
			supervisor := application.NewAgentRunner(st, router, checker).WithExecutionPermissionCapabilities(capabilities).WithAgentBrowser(browser)
			if _, err = application.NewRunService(st).Start(ctx, run.ID); err != nil {
				t.Fatal(err)
			}
			finished := make(chan error, 1)
			go func() { _, e := supervisor.Step(ctx, run.ID); finished <- e }()
			var record approval.Record
			for record.ID == "" {
				records, e := st.ListApprovals(ctx, approval.ListFilter{RunID: run.ID, ToolName: toolgateway.AgentBrowserApprovalTool})
				if e != nil {
					t.Fatal(e)
				}
				for _, v := range records {
					if strings.HasPrefix(v.ProposalID, domain.BrowserNetworkRequestPrefix) {
						record = v
						break
					}
				}
				if record.ID != "" {
					break
				}
				select {
				case e := <-finished:
					t.Fatalf("run ended before request approval: %v", e)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(20 * time.Millisecond):
				}
			}
			source, err := st.GetBrowserNetworkRequest(ctx, record.ProposalID)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(source)
			if strings.Contains(string(raw), "do-not-persist") {
				t.Fatalf("request persisted plaintext secret/body: %s", raw)
			}
			if writes.Load() != 0 || undeclared.Load() != 0 {
				t.Fatal("unapproved page network request dispatched")
			}
			control := application.NewApprovalControlService(st, toolgateway.New(st, checker), checker)
			request := application.DecideApprovalControlRequest{Version: application.ApprovalControlProtocolVersion, RunID: run.ID, ApprovalID: record.ID, Action: application.ApprovalControlApproveOnce, OperationKey: "browser-network-test-operator-review", ReviewedBy: "test_operator"}
			if decision == "deny" {
				request.Action = application.ApprovalControlDeny
			}
			if decision == "close" {
				view, e := browser.GetStatus(ctx, run.ID)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = browser.Close(ctx, run.ID, view.SessionID); e != nil {
					t.Fatal(e)
				}
			}
			_, err = control.Decide(ctx, request)
			if decision == "close" {
				if err == nil {
					t.Fatal("closed request approved")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			want := int32(0)
			if decision == "approve" {
				want = 1
			}
			until := time.Now().Add(3 * time.Second)
			for writes.Load() != want && time.Now().Before(until) {
				time.Sleep(20 * time.Millisecond)
			}
			if writes.Load() != want {
				t.Fatalf("write count=%d want=%d", writes.Load(), want)
			}
			time.Sleep(150 * time.Millisecond)
			if writes.Load() != want || undeclared.Load() != 0 {
				t.Fatalf("writes=%d undeclared=%d", writes.Load(), undeclared.Load())
			}
			if decision == "approve" {
				if err = st.ConsumeBrowserNetworkRequest(ctx, source.ID, source.Fingerprint); err == nil {
					t.Fatal("request approval consumed twice")
				}
			}
			cancel()
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("run did not stop")
			}
			t.Logf("production approval control %s: writes=%d undeclared=%d", decision, writes.Load(), undeclared.Load())
		})
	}
}
