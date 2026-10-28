package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/toolgateway"
)

type agentBrowserTestStore struct {
	AgentRunnerStore
	base       *fakeFullCDPProductionStore
	mode       domain.RunModeSnapshot
	root       domain.AgentNode
	lease      domain.RunExecutionLease
	calls      map[string]domain.SupervisorToolCall
	started    map[string]bool
	approvals  map[string]approval.Record
	workspace  session.WorkspaceInfo
	checkpoint domain.SupervisorCheckpoint
}

func (s *agentBrowserTestStore) GetRun(c context.Context, id string) (domain.Run, error) {
	return s.base.GetRun(c, id)
}
func (s *agentBrowserTestStore) GetMission(c context.Context, id string) (domain.Mission, error) {
	return s.base.GetMission(c, id)
}
func (s *agentBrowserTestStore) GetRunExecutionPermission(c context.Context, id string) (domain.RunExecutionPermissionSnapshot, error) {
	return s.base.GetRunExecutionPermission(c, id)
}
func (s *agentBrowserTestStore) GetRunMode(context.Context, string) (domain.RunModeSnapshot, error) {
	return s.mode, nil
}
func (s *agentBrowserTestStore) GetRootAgent(context.Context, string) (domain.AgentNode, bool, error) {
	return s.root, true, nil
}
func (s *agentBrowserTestStore) GetRunExecutionLease(context.Context, string) (domain.RunExecutionLease, bool, error) {
	return s.lease, true, nil
}
func (s *agentBrowserTestStore) GetWorkspaceInfo(context.Context, string) (session.WorkspaceInfo, error) {
	return s.workspace, nil
}
func (s *agentBrowserTestStore) GetSupervisorApprovalCall(_ context.Context, runID, id string) (domain.SupervisorToolCall, bool, error) {
	c, ok := s.calls[id]
	if !ok || c.RunID != runID {
		return c, false, errors.New("call not found")
	}
	return c, s.started[id], nil
}
func (s *agentBrowserTestStore) EnsureApproval(_ context.Context, p approval.Proposal) (approval.Record, error) {
	if a, ok := s.approvals[p.ProposalID]; ok {
		return a, nil
	}
	a := approval.Record{ID: "approval-" + p.ProposalID, ProposalID: p.ProposalID, RunID: s.base.run.ID, SessionID: p.SessionID, WorkspaceID: p.WorkspaceID, ToolName: p.ToolName, ActionClass: p.ActionClass, Mode: p.Mode, Status: p.Status, RequestFingerprint: p.RequestFingerprint, DecisionReason: p.DecisionReason}
	s.approvals[p.ProposalID] = a
	return a, nil
}
func (s *agentBrowserTestStore) GetApprovalByProposal(_ context.Context, id string) (approval.Record, error) {
	a, ok := s.approvals[id]
	if !ok {
		return a, sql.ErrNoRows
	}
	return a, nil
}
func (s *agentBrowserTestStore) DecideApproval(_ context.Context, r approval.DecisionRequest) (approval.DecisionResult, error) {
	a := s.approvals[r.ProposalID]
	a.Status = approval.StatusApproved
	if r.Action == approval.ActionDeny {
		a.Status = approval.StatusDenied
	}
	s.approvals[r.ProposalID] = a
	return approval.DecisionResult{Approval: a}, nil
}
func (s *agentBrowserTestStore) RecordSupervisorToolExecutionStarted(_ context.Context, _ domain.SupervisorCheckpoint, id string) (bool, error) {
	fresh := !s.started[id]
	s.started[id] = true
	return fresh, nil
}
func (s *agentBrowserTestStore) RecordSupervisorToolResult(_ context.Context, _ domain.SupervisorCheckpoint, r domain.SupervisorToolResult) (domain.SupervisorToolCall, bool, error) {
	c := s.calls[r.CallID]
	c.Status = r.Status
	c.ResultJSON = r.ResultJSON
	c.ErrorCode = r.ErrorCode
	c.CompletedAt = &r.CompletedAt
	s.calls[c.CallID] = c
	return c, true, nil
}
func (s *agentBrowserTestStore) ListSupervisorToolRounds(_ context.Context, checkpoint domain.SupervisorCheckpoint) ([]domain.SupervisorToolRound, error) {
	var out []domain.SupervisorToolRound
	for _, c := range s.calls {
		if c.RunID == checkpoint.RunID && c.AttemptID == checkpoint.AttemptID {
			out = append(out, domain.SupervisorToolRound{RunID: c.RunID, Turn: c.Turn, AttemptID: c.AttemptID, Round: c.Round, ModelAttempt: 1, Calls: []domain.SupervisorToolCall{c}, CreatedAt: c.CreatedAt, CompletedAt: c.CompletedAt})
		}
	}
	return out, nil
}

type agentBrowserTestRuntime struct {
	done              chan struct{}
	cancelOnce        sync.Once
	screencastOnce    sync.Once
	screencastStarted chan struct{}
	status            browserruntime.AgentBrowserStatus
	actions           []string
	png               []byte
	cancelled         bool
	after             func()
	block             bool
}

func (r *agentBrowserTestRuntime) Navigate(ctx context.Context, url string) (browserruntime.AgentBrowserNavigation, error) {
	r.actions = append(r.actions, "navigate")
	if r.block {
		<-ctx.Done()
		return browserruntime.AgentBrowserNavigation{}, ctx.Err()
	}
	r.status.CanonicalURL = url
	r.status.DocumentEpoch++
	if r.after != nil {
		r.after()
	}
	return browserruntime.AgentBrowserNavigation{SessionID: r.status.SessionID, CanonicalURL: url, DocumentEpoch: r.status.DocumentEpoch}, nil
}
func (r *agentBrowserTestRuntime) Snapshot(context.Context) (browserruntime.AgentBrowserSnapshot, error) {
	r.actions = append(r.actions, "snapshot")
	return browserruntime.AgentBrowserSnapshot{SessionID: r.status.SessionID, CanonicalURL: r.status.CanonicalURL, DocumentEpoch: r.status.DocumentEpoch, SnapshotID: "snapshot-live", Text: "dynamic evidence", Title: "Fixture", Elements: []browserruntime.AgentBrowserElement{{Ref: "ref-live", Role: "button", Name: "Publish"}}}, nil
}
func (r *agentBrowserTestRuntime) Click(context.Context, string, string) (browserruntime.AgentBrowserInteraction, error) {
	r.actions = append(r.actions, "click")
	return browserruntime.AgentBrowserInteraction{Dispatched: true}, nil
}
func (r *agentBrowserTestRuntime) Type(context.Context, string, string, string, bool) (browserruntime.AgentBrowserInteraction, error) {
	r.actions = append(r.actions, "type")
	return browserruntime.AgentBrowserInteraction{Dispatched: true}, nil
}
func (r *agentBrowserTestRuntime) Key(context.Context, string) (browserruntime.AgentBrowserInteraction, error) {
	r.actions = append(r.actions, "key")
	return browserruntime.AgentBrowserInteraction{Dispatched: true}, nil
}
func (r *agentBrowserTestRuntime) Scroll(context.Context, float64, float64) (browserruntime.AgentBrowserInteraction, error) {
	r.actions = append(r.actions, "scroll")
	return browserruntime.AgentBrowserInteraction{Dispatched: true}, nil
}
func (r *agentBrowserTestRuntime) Screenshot(context.Context) (browserruntime.AgentBrowserScreenshot, error) {
	h := sha256.Sum256(r.png)
	return browserruntime.AgentBrowserScreenshot{SessionID: r.status.SessionID, CanonicalURL: r.status.CanonicalURL, DocumentEpoch: r.status.DocumentEpoch, MediaType: "image/png", PNG: r.png, Bytes: len(r.png), SHA256: hex.EncodeToString(h[:])}, nil
}
func (r *agentBrowserTestRuntime) Status() browserruntime.AgentBrowserStatus { return r.status }
func (r *agentBrowserTestRuntime) Cancel() {
	r.cancelOnce.Do(func() {
		r.cancelled = true
		if r.done != nil {
			close(r.done)
		}
	})
}
func (r *agentBrowserTestRuntime) Close(context.Context) (browserruntime.AgentBrowserCleanup, error) {
	r.Cancel()
	return browserruntime.AgentBrowserCleanup{SessionID: r.status.SessionID, TreeReaped: true, ProfileRemoved: true}, nil
}
func (r *agentBrowserTestRuntime) Done() <-chan struct{} { return r.done }

// StartScreencast returns an idle stream: the live view blocks on its request
// context until the test cancels it, which is what the lifecycle test needs.
func (r *agentBrowserTestRuntime) StartScreencast(context.Context) (browserruntime.AgentBrowserScreencastStream, error) {
	if r.screencastStarted != nil {
		r.screencastOnce.Do(func() { close(r.screencastStarted) })
	}
	return browserruntime.AgentBrowserScreencastStream{}, nil
}

func newAgentBrowserFixture(t *testing.T) (*AgentBrowserService, *agentBrowserTestStore, *agentBrowserTestRuntime, *AgentRunner, domain.SupervisorTurn) {
	t.Helper()
	legacy, base, _, _ := newFullCDPProductionServiceFixture(t)
	base.run.Status = domain.RunRunning
	now := time.Now().UTC()
	st := &agentBrowserTestStore{base: base, mode: domain.RunModeSnapshot{Surface: domain.ExecutionSurfaceCode, Phase: domain.ExecutionPhaseDeliver, Profile: domain.ProfileCode, Revision: 1}, root: domain.AgentNode{ID: "agent-browser-root", RunID: base.run.ID, Role: domain.AgentRoleRoot}, calls: map[string]domain.SupervisorToolCall{}, started: map[string]bool{}, approvals: map[string]approval.Record{}, workspace: session.WorkspaceInfo{ID: base.mission.WorkspaceID, RootPath: t.TempDir()}}
	st.lease = domain.RunExecutionLease{RunID: base.run.ID, LeaseID: "lease-browser", OwnerID: "browser-test", Generation: 1, Status: domain.RunExecutionLeaseActive, AcquiredAt: now, RenewedAt: now, ExpiresAt: now.Add(time.Hour)}
	st.checkpoint = domain.SupervisorCheckpoint{RunID: base.run.ID, NextTurn: 1, AttemptID: "attempt-browser", Phase: domain.SupervisorTurnStarted, LeaseID: st.lease.LeaseID, LeaseGeneration: 1, UpdatedAt: now}
	service := NewAgentBrowserService(st, AgentBrowserOptions{HomePath: t.TempDir(), Capabilities: legacy.executionCapabilities, Headless: true})
	service.available = true
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 3)))
	r := &agentBrowserTestRuntime{png: buf.Bytes(), done: make(chan struct{})}
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	service.launch = func(ctx context.Context, request browserruntime.AgentBrowserStartRequest) (agentBrowserRuntime, error) {
		if e := request.CheckAuthority(ctx, request.Authority); e != nil {
			return nil, e
		}
		r.status = browserruntime.AgentBrowserStatus{SessionID: request.Authority.SessionID, State: "ready", Generation: request.Authority.Generation}
		return r, nil
	}
	supervisor := (&AgentRunner{store: st, tools: toolgateway.New(nil, policy.NewDefaultChecker())}).WithAgentBrowser(service)
	turn := domain.SupervisorTurn{Run: base.run, Mission: base.mission, Mode: st.mode, Agent: st.root, Checkpoint: st.checkpoint}
	return service, st, r, supervisor, turn
}
func agentBrowserFixtureCall(t *testing.T, s *AgentBrowserService, st *agentBrowserTestStore, name toolgateway.ToolName, payload string) domain.SupervisorToolCall {
	t.Helper()
	a, e := s.authority(t.Context(), st.base.run.ID)
	if e != nil {
		t.Fatal(e)
	}
	auth, _ := json.Marshal(a)
	p, e := toolgateway.NormalizeAgentBrowserPayload(name, json.RawMessage(payload))
	if e != nil {
		t.Fatal(e)
	}
	c := domain.SupervisorToolCall{RunID: a.RunID, AgentID: a.RootAgentID, AgentAttemptID: st.checkpoint.AttemptID, AgentAttribution: domain.AgentAttributionSupervisorRoot, AttemptID: st.checkpoint.AttemptID, Turn: 1, Round: 1, Position: 1, ModelAttempt: 1, CallID: fmt.Sprintf("call-browser-%d", len(st.calls)+1), ToolName: string(name), PayloadJSON: string(p), AuthorityJSON: string(auth), Status: domain.SupervisorToolPending, CreatedAt: time.Now().UTC()}
	st.calls[c.CallID] = c
	return c
}
func runAgentBrowserFixtureCall(t *testing.T, supervisor *AgentRunner, turn domain.SupervisorTurn, c domain.SupervisorToolCall) (bool, error) {
	t.Helper()
	_, waiting, e := supervisor.resumeSupervisorTools(t.Context(), turn, []domain.SupervisorToolRound{{Calls: []domain.SupervisorToolCall{c}}})
	return waiting, e
}

func TestAgentBrowserFirstNavigationAdvertisedDurableAndNeverRepeated(t *testing.T) {
	service, st, r, s, turn := newAgentBrowserFixture(t)
	caps, auth, e := s.supervisorBrowserActionCapabilities(t.Context(), turn, st.base.executionPermission)
	if e != nil || !caps.Available || len(r.actions) != 0 {
		t.Fatalf("cold capability %+v %v", caps, e)
	}
	options := supervisorToolOptions{BrowserActions: supervisorBrowserActionTools{Capabilities: caps, Authority: auth}}
	specs := supervisorStructuredToolSpecs(turn.Mode.Surface, turn.Mode.Phase, st.base.executionPermission.Mode, false, false, options)
	seen := map[string]bool{}
	for _, spec := range specs {
		if strings.HasPrefix(spec.Name, "browser_") {
			seen[spec.Name] = true
			if !strings.Contains(string(spec.Parameters), spec.Name+".v2") {
				t.Fatalf("legacy advertised: %s", spec.Parameters)
			}
		}
	}
	if len(seen) != 8 {
		t.Fatalf("tools %v", seen)
	}
	prepared, e := prepareSupervisorToolCalls([]llm.ToolCall{{ID: "model-1", Name: "browser_navigate", Arguments: json.RawMessage(`{"version":"browser_navigate.v2","url":"https://example.org/#dynamic"}`)}}, turn.Run.ID, 1, 1, turn.Mode.Surface, turn.Mode.Phase, st.base.executionPermission.Mode, false, false, options)
	if e != nil || len(prepared) != 1 {
		t.Fatalf("normalize v2 %v", e)
	}
	call := agentBrowserFixtureCall(t, service, st, toolgateway.BrowserNavigateTool, string(prepared[0].Arguments))
	waiting, e := runAgentBrowserFixtureCall(t, s, turn, call)
	if e != nil || waiting || len(r.actions) != 1 || st.calls[call.CallID].Status != domain.SupervisorToolCompleted {
		t.Fatalf("first navigate %v %v actions=%v result=%+v", waiting, e, r.actions, st.calls[call.CallID])
	}
	// Simulate a started dispatch whose completion receipt was lost. No replay.
	st.calls[call.CallID] = call
	_, e = runAgentBrowserFixtureCall(t, s, turn, call)
	if e != nil || len(r.actions) != 1 || !strings.Contains(st.calls[call.CallID].ResultJSON, "outcome_unknown") {
		t.Fatalf("repeated navigation actions=%v err=%v result=%s", r.actions, e, st.calls[call.CallID].ResultJSON)
	}
}
func TestAgentBrowserSensitivePreflightBeforeStartedApproveOnceAndDeny(t *testing.T) {
	for _, approve := range []bool{true, false} {
		t.Run(fmt.Sprint(approve), func(t *testing.T) {
			service, st, r, s, turn := newAgentBrowserFixture(t)
			nav := agentBrowserFixtureCall(t, service, st, toolgateway.BrowserNavigateTool, `{"version":"browser_navigate.v2","url":"https://example.org/form"}`)
			if _, e := runAgentBrowserFixtureCall(t, s, turn, nav); e != nil {
				t.Fatal(e)
			}
			call := agentBrowserFixtureCall(t, service, st, toolgateway.BrowserClickTool, `{"version":"browser_click.v2","snapshot_id":"snapshot-live","element_ref":"ref-live","sensitive_intent":{"version":"browser_sensitive_intent.v1","effect":"external_write","target":"https://example.org/form","description":"Publish the visible draft","document_epoch":1}}`)
			waiting, e := runAgentBrowserFixtureCall(t, s, turn, call)
			if e != nil || !waiting || st.started[call.CallID] || len(r.actions) != 1 {
				t.Fatalf("preflight dispatched: waiting=%t err=%v actions=%v", waiting, e, r.actions)
			}
			record := st.approvals[call.CallID]
			record.Status = approval.StatusDenied
			if approve {
				record.Status = approval.StatusApproved
			}
			st.approvals[call.CallID] = record
			waiting, e = runAgentBrowserFixtureCall(t, s, turn, call)
			expected := 1
			if approve {
				expected = 2
			}
			if e != nil || waiting || len(r.actions) != expected {
				t.Fatalf("decision result waiting=%t e=%v actions=%v", waiting, e, r.actions)
			}
			if _, e = runAgentBrowserFixtureCall(t, s, turn, st.calls[call.CallID]); e != nil || len(r.actions) != expected {
				t.Fatal("terminal call replayed")
			}
		})
	}
}
func TestAgentBrowserLiveActivationAndRevocationPostcheck(t *testing.T) {
	service, st, r, s, turn := newAgentBrowserFixture(t)
	original := service.launch
	service.launch = func(ctx context.Context, request browserruntime.AgentBrowserStartRequest) (agentBrowserRuntime, error) {
		if request.Authority.PermissionActivation == 0 {
			t.Fatal("runtime private activation missing")
		}
		a, e := service.authority(ctx, turn.Run.ID)
		generation, live := service.options.Capabilities.FullAccessGeneration(st.base.executionPermission)
		if e != nil || !live || generation == 0 || a.PermissionActivation != generation {
			t.Fatalf("lost current Full activation %+v %v", a, e)
		}
		wrong := request.Authority
		wrong.PermissionActivation++
		if request.CheckAuthority(ctx, wrong) == nil {
			t.Fatal("foreign private token accepted")
		}
		return original(ctx, request)
	}
	call := agentBrowserFixtureCall(t, service, st, toolgateway.BrowserNavigateTool, `{"version":"browser_navigate.v2","url":"https://example.org"}`)
	r.after = func() { service.options.Capabilities.RuntimeAuthority.RotateRunAuthorizationFence(turn.Run.ID) }
	if _, e := runAgentBrowserFixtureCall(t, s, turn, call); e != nil {
		t.Fatal(e)
	}
	if st.calls[call.CallID].Status == domain.SupervisorToolCompleted || !r.cancelled {
		t.Fatalf("revoked result published %+v", st.calls[call.CallID])
	}
}
func TestAgentBrowserRestartCannotRestoreLiveGrant(t *testing.T) {
	service, st, _, _, _ := newAgentBrowserFixture(t)
	a, e := service.authority(t.Context(), st.base.run.ID)
	if e != nil {
		t.Fatal(e)
	}
	options := service.options
	options.Capabilities.RuntimeAuthority = domain.NewExecutionPermissionRuntimeAuthority()
	fresh := NewAgentBrowserService(st, options)
	fresh.available = true
	if fresh.check(t.Context(), a) == nil {
		t.Fatal("persisted snapshot reactivated after restart")
	}
}

func TestAgentBrowserScrollKeyAndClosedApprovalNeverDispatch(t *testing.T) {
	service, st, r, s, turn := newAgentBrowserFixture(t)
	for _, item := range []struct {
		name toolgateway.ToolName
		raw  string
	}{{toolgateway.BrowserNavigateTool, `{"version":"browser_navigate.v2","url":"https://example.org/form"}`}, {toolgateway.BrowserScrollTool, `{"version":"browser_scroll.v2","delta_x":0,"delta_y":500}`}, {toolgateway.BrowserKeyTool, `{"version":"browser_key.v2","key":"Tab"}`}} {
		c := agentBrowserFixtureCall(t, service, st, item.name, item.raw)
		if waiting, e := runAgentBrowserFixtureCall(t, s, turn, c); e != nil || waiting || st.calls[c.CallID].Status != domain.SupervisorToolCompleted {
			t.Fatalf("typed action %s %v %s", item.name, e, st.calls[c.CallID].ResultJSON)
		}
	}
	c := agentBrowserFixtureCall(t, service, st, toolgateway.BrowserKeyTool, `{"version":"browser_key.v2","key":"Enter","sensitive_intent":{"version":"browser_sensitive_intent.v1","effect":"external_delete","target":"https://example.org/form","description":"Delete selected draft","document_epoch":1}}`)
	if waiting, e := runAgentBrowserFixtureCall(t, s, turn, c); e != nil || !waiting {
		t.Fatal(e)
	}
	record := st.approvals[c.CallID]
	record.Status = approval.StatusApproved
	st.approvals[c.CallID] = record
	view, _ := service.GetStatus(t.Context(), turn.Run.ID)
	if _, e := service.Close(t.Context(), turn.Run.ID, view.SessionID); e != nil {
		t.Fatal(e)
	}
	if _, e := runAgentBrowserFixtureCall(t, s, turn, c); e != nil || st.started[c.CallID] || len(r.actions) != 3 || st.calls[c.CallID].Status != domain.SupervisorToolFailed {
		t.Fatalf("closed approved input did not settle without dispatch: %v %+v", e, st.calls[c.CallID])
	}
}

func TestAgentBrowserDurableLeaseRequiredBeforeFirstLaunch(t *testing.T) {
	service, st, r, s, turn := newAgentBrowserFixture(t)
	c := agentBrowserFixtureCall(t, service, st, toolgateway.BrowserNavigateTool, `{"version":"browser_navigate.v2","url":"https://example.org"}`)
	st.lease.Generation++
	if _, e := runAgentBrowserFixtureCall(t, s, turn, c); e != nil {
		t.Fatal(e)
	}
	if len(r.actions) != 0 || st.calls[c.CallID].Status == domain.SupervisorToolCompleted {
		t.Fatal("stale lease launched browser")
	}
}

func TestAgentBrowserScreenshotPairsPixelsAndReadOnlySessionEvidence(t *testing.T) {
	service, st, _, s, turn := newAgentBrowserFixture(t)
	nav := agentBrowserFixtureCall(t, service, st, toolgateway.BrowserNavigateTool, `{"version":"browser_navigate.v2","url":"https://different.example.org/article"}`)
	if _, e := runAgentBrowserFixtureCall(t, s, turn, nav); e != nil {
		t.Fatal(e)
	}
	shot := agentBrowserFixtureCall(t, service, st, toolgateway.BrowserScreenshotTool, `{"version":"browser_screenshot.v2"}`)
	if _, e := runAgentBrowserFixtureCall(t, s, turn, shot); e != nil {
		t.Fatal(e)
	}
	c := st.calls[shot.CallID]
	if c.Status != domain.SupervisorToolCompleted {
		t.Fatalf("screenshot %+v", c)
	}
	rounds := []domain.SupervisorToolRound{{RunID: c.RunID, Turn: c.Turn, AttemptID: c.AttemptID, Round: 1, ModelAttempt: 1, CreatedAt: c.CreatedAt, CompletedAt: c.CompletedAt, Calls: []domain.SupervisorToolCall{c}}}
	request, e := supervisorRequestWithToolRounds(llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "inspect"}}}, rounds)
	if e != nil {
		t.Fatal(e)
	}
	ref := llm.ModelRef{Provider: "screenshot-vision", Model: "vision-test"}
	s.router = llm.NewRouter(ref)
	s.router.RegisterProvider(screenshotVisionProvider{state: llm.VisionSupported})
	actual, e := s.supervisorBrowserImages(t.Context(), st.checkpoint, ref, request, rounds)
	if e != nil {
		t.Fatal(e)
	}
	if len(actual.Messages[2].Images) != 1 || actual.Messages[2].ToolResults[0].ToolCallID != c.CallID || actual.Messages[2].ToolResults[0].Content != c.ResultJSON {
		t.Fatal("image lost native result pairing")
	}
	view, e := service.GetStatus(t.Context(), turn.Run.ID)
	if e != nil {
		t.Fatal(e)
	}
	data, hash, e := service.ReadScreenshot(t.Context(), turn.Run.ID, view.SessionID, view.ArtifactLocator)
	if e != nil || len(data) == 0 || hash == "" {
		t.Fatalf("UI read %v", e)
	}
	if _, e = service.Close(t.Context(), turn.Run.ID, view.SessionID); e != nil {
		t.Fatal(e)
	}
	if _, _, e = service.ReadScreenshot(t.Context(), turn.Run.ID, view.SessionID, view.ArtifactLocator); e != nil {
		t.Fatal(e)
	}
	if _, _, e = service.ReadScreenshot(t.Context(), turn.Run.ID, "different-session", view.ArtifactLocator); e == nil {
		t.Fatal("cross session screenshot read")
	}
}

func TestAgentBrowserClosedOrStaleApprovalSettlesWithoutDispatch(t *testing.T) {
	for _, scenario := range []string{"deny_closed", "approve_closed", "approve_stale"} {
		t.Run(scenario, func(t *testing.T) {
			service, st, r, s, turn := newAgentBrowserFixture(t)
			nav := agentBrowserFixtureCall(t, service, st, toolgateway.BrowserNavigateTool, `{"version":"browser_navigate.v2","url":"https://example.org/form"}`)
			if _, e := runAgentBrowserFixtureCall(t, s, turn, nav); e != nil {
				t.Fatal(e)
			}
			c := agentBrowserFixtureCall(t, service, st, toolgateway.BrowserKeyTool, `{"version":"browser_key.v2","key":"Enter","sensitive_intent":{"version":"browser_sensitive_intent.v1","effect":"external_write","target":"https://example.org/form","description":"Submit synthetic draft","document_epoch":1}}`)
			if waiting, e := runAgentBrowserFixtureCall(t, s, turn, c); e != nil || !waiting {
				t.Fatalf("preflight %v %t", e, waiting)
			}
			record := st.approvals[c.CallID]
			record.Status = approval.StatusApproved
			want := domain.SupervisorToolFailed
			if scenario == "deny_closed" {
				record.Status = approval.StatusDenied
				want = domain.SupervisorToolDenied
			}
			st.approvals[c.CallID] = record
			if scenario == "approve_stale" {
				r.status.DocumentEpoch++
				r.status.CanonicalURL = "https://example.org/changed"
			} else {
				v, _ := service.GetStatus(t.Context(), turn.Run.ID)
				if _, e := service.Close(t.Context(), turn.Run.ID, v.SessionID); e != nil {
					t.Fatal(e)
				}
			}
			if waiting, e := runAgentBrowserFixtureCall(t, s, turn, c); e != nil || waiting {
				t.Fatalf("resume %v %t", e, waiting)
			}
			done := st.calls[c.CallID]
			if done.Status != want || st.started[c.CallID] || len(r.actions) != 1 {
				t.Fatalf("terminal without dispatch %+v", done)
			}
			rounds, _ := st.ListSupervisorToolRounds(t.Context(), turn.Checkpoint)
			request, e := supervisorRequestWithToolRounds(llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "continue"}}}, rounds)
			if e != nil {
				t.Fatal(e)
			}
			paired := false
			for _, m := range request.Messages {
				for _, tr := range m.ToolResults {
					if tr.ToolCallID == c.CallID && tr.Content == done.ResultJSON {
						paired = true
					}
				}
			}
			if !paired {
				t.Fatal("terminal preflight did not reach model tool result")
			}
		})
	}
}

func TestAgentBrowserReopensOnlyOnNewAdvertisementAndKeepsOldEvidence(t *testing.T) {
	service, st, r, s, turn := newAgentBrowserFixture(t)
	nav := agentBrowserFixtureCall(t, service, st, toolgateway.BrowserNavigateTool, `{"version":"browser_navigate.v2","url":"https://example.org/a"}`)
	if _, e := runAgentBrowserFixtureCall(t, s, turn, nav); e != nil {
		t.Fatal(e)
	}
	shot := agentBrowserFixtureCall(t, service, st, toolgateway.BrowserScreenshotTool, `{"version":"browser_screenshot.v2"}`)
	if _, e := runAgentBrowserFixtureCall(t, s, turn, shot); e != nil {
		t.Fatal(e)
	}
	old, _ := service.GetStatus(t.Context(), turn.Run.ID)
	oldAuthority, _ := service.authority(t.Context(), turn.Run.ID)
	if _, e := service.Close(t.Context(), turn.Run.ID, old.SessionID); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		v, _ := service.GetStatus(t.Context(), turn.Run.ID)
		if v.SessionID != old.SessionID || !v.Capabilities.CanStart {
			t.Fatalf("GET rotated or disabled %+v", v)
		}
	}
	nextRuntime := &agentBrowserTestRuntime{png: r.png, done: make(chan struct{})}
	service.launch = func(ctx context.Context, req browserruntime.AgentBrowserStartRequest) (agentBrowserRuntime, error) {
		if e := req.CheckAuthority(ctx, req.Authority); e != nil {
			return nil, e
		}
		nextRuntime.status = browserruntime.AgentBrowserStatus{SessionID: req.Authority.SessionID, Generation: req.Authority.Generation, State: "ready"}
		return nextRuntime, nil
	}
	cap, raw, e := s.agentBrowserCapabilities(t.Context(), turn)
	if e != nil || !cap.Available {
		t.Fatal(e)
	}
	next, e := toolgateway.DecodeAgentBrowserAuthority(raw)
	if e != nil || next.BrowserSessionID == old.SessionID || next.Fingerprint() == oldAuthority.Fingerprint() {
		t.Fatal("new advertisement reused authority")
	}
	if service.check(t.Context(), oldAuthority) == nil {
		t.Fatal("old authority accepted")
	}
	nav2 := agentBrowserFixtureCall(t, service, st, toolgateway.BrowserNavigateTool, `{"version":"browser_navigate.v2","url":"https://example.org/b"}`)
	if _, e := runAgentBrowserFixtureCall(t, s, turn, nav2); e != nil || st.calls[nav2.CallID].Status != domain.SupervisorToolCompleted {
		t.Fatal(e)
	}
	if _, e := service.Close(t.Context(), turn.Run.ID, old.SessionID); e != nil {
		t.Fatal(e)
	}
	if nextRuntime.cancelled || len(nextRuntime.actions) != 1 {
		t.Fatal("old close affected new runtime")
	}
	if _, _, e := service.ReadScreenshot(t.Context(), turn.Run.ID, old.SessionID, old.ArtifactLocator); e != nil {
		t.Fatal(e)
	}
}

func TestAgentBrowserAutonomousDocumentChangeHidesStalePresentation(t *testing.T) {
	service, st, r, s, turn := newAgentBrowserFixture(t)
	for _, item := range []struct {
		name toolgateway.ToolName
		raw  string
	}{{toolgateway.BrowserNavigateTool, `{"version":"browser_navigate.v2","url":"https://example.org/a"}`}, {toolgateway.BrowserSnapshotTool, `{"version":"browser_snapshot.v2"}`}, {toolgateway.BrowserScreenshotTool, `{"version":"browser_screenshot.v2"}`}} {
		c := agentBrowserFixtureCall(t, service, st, item.name, item.raw)
		if _, e := runAgentBrowserFixtureCall(t, s, turn, c); e != nil {
			t.Fatal(e)
		}
	}
	old, _ := service.GetStatus(t.Context(), turn.Run.ID)
	if old.Title == "" || old.ArtifactLocator == "" || old.ScreenshotSHA256 == "" || old.ScreenshotBytes == 0 {
		t.Fatalf("missing capture %+v", old)
	}
	r.status.DocumentEpoch++
	r.status.CanonicalURL = "https://example.org/b"
	changed, _ := service.GetStatus(t.Context(), turn.Run.ID)
	if changed.Title != "" || changed.ArtifactLocator != "" || changed.ScreenshotSHA256 != "" || changed.ScreenshotBytes != 0 || changed.CanonicalURL == old.CanonicalURL {
		t.Fatalf("stale presentation %+v", changed)
	}
	if _, _, e := service.ReadScreenshot(t.Context(), turn.Run.ID, old.SessionID, old.ArtifactLocator); e != nil {
		t.Fatal(e)
	}
}

func TestAgentBrowserRuntimeDoneAllowsReopenOnlyAfterCleanup(t *testing.T) {
	service, st, r, s, turn := newAgentBrowserFixture(t)
	nav := agentBrowserFixtureCall(t, service, st, toolgateway.BrowserNavigateTool, `{"version":"browser_navigate.v2","url":"https://example.org"}`)
	if _, e := runAgentBrowserFixtureCall(t, s, turn, nav); e != nil {
		t.Fatal(e)
	}
	old, _ := service.GetStatus(t.Context(), turn.Run.ID)
	r.Cancel()
	deadline := time.Now().Add(time.Second)
	for {
		v, _ := service.GetStatus(t.Context(), turn.Run.ID)
		if v.Capabilities.CanStart && v.State == "closed" {
			if v.SessionID != old.SessionID {
				t.Fatal("GET rotated")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Done not reflected %+v", v)
		}
		time.Sleep(time.Millisecond)
	}
	service.mu.Lock()
	slot := service.slots[turn.Run.ID]
	slot.view.Cleanup.CleanupPending = true
	service.mu.Unlock()
	if _, e := service.authorityForAdvertisement(t.Context(), turn.Run.ID); e == nil {
		t.Fatal("cleanup-pending rotated")
	}
	service.mu.Lock()
	slot.view.Cleanup.CleanupPending = false
	service.mu.Unlock()
	next, e := service.authorityForAdvertisement(t.Context(), turn.Run.ID)
	if e != nil || next.BrowserSessionID == old.SessionID {
		t.Fatalf("cleaned runtime not replaceable %v", e)
	}
}

func TestAgentBrowserFailedLaunchCannotForgeCleanupOrReopen(t *testing.T) {
	service, st, _, s, turn := newAgentBrowserFixture(t)
	service.launch = func(context.Context, browserruntime.AgentBrowserStartRequest) (agentBrowserRuntime, error) {
		return nil, errors.New("launch failed and cleanup unknown")
	}
	c := agentBrowserFixtureCall(t, service, st, toolgateway.BrowserNavigateTool, `{"version":"browser_navigate.v2","url":"https://example.org"}`)
	if _, e := runAgentBrowserFixtureCall(t, s, turn, c); e != nil {
		t.Fatal(e)
	}
	v, _ := service.GetStatus(t.Context(), turn.Run.ID)
	closed, e := service.Close(t.Context(), turn.Run.ID, v.SessionID)
	if e == nil || closed.Cleanup == nil || closed.Cleanup.TreeReaped || closed.Cleanup.ProfileRemoved || !closed.Cleanup.CleanupPending {
		t.Fatalf("forged cleanup %+v %v", closed, e)
	}
	if _, e := service.authorityForAdvertisement(t.Context(), turn.Run.ID); e == nil {
		t.Fatal("unknown cleanup allowed fresh launch")
	}
}

func TestAgentBrowserLaunchReceiptMustMatchSessionAndProveCleanup(t *testing.T) {
	for _, scenario := range []string{"clean", "partial", "wrong_session"} {
		t.Run(scenario, func(t *testing.T) {
			service, st, _, s, turn := newAgentBrowserFixture(t)
			service.launch = func(_ context.Context, req browserruntime.AgentBrowserStartRequest) (agentBrowserRuntime, error) {
				receipt := browserruntime.AgentBrowserCleanup{SessionID: req.Authority.SessionID, TreeReaped: true, ProfileRemoved: true}
				if scenario == "partial" {
					receipt.ProfileRemoved = false
				}
				if scenario == "wrong_session" {
					receipt.SessionID = "other"
				}
				return nil, &browserruntime.AgentBrowserLaunchError{Err: errors.New("failed launch"), Cleanup: receipt}
			}
			c := agentBrowserFixtureCall(t, service, st, toolgateway.BrowserNavigateTool, `{"version":"browser_navigate.v2","url":"https://example.org"}`)
			if _, e := runAgentBrowserFixtureCall(t, s, turn, c); e != nil {
				t.Fatal(e)
			}
			v, _ := service.GetStatus(t.Context(), turn.Run.ID)
			closed, e := service.Close(t.Context(), turn.Run.ID, v.SessionID)
			if scenario == "clean" {
				if e != nil || closed.Cleanup == nil || closed.Cleanup.CleanupPending {
					t.Fatalf("clean receipt lost %+v %v", closed, e)
				}
				next, e := service.authorityForAdvertisement(t.Context(), turn.Run.ID)
				if e != nil || next.BrowserSessionID == v.SessionID {
					t.Fatal("proven cleanup not replaceable")
				}
			} else {
				if e == nil || closed.Cleanup == nil || !closed.Cleanup.CleanupPending {
					t.Fatalf("unproven cleanup accepted %+v %v", closed, e)
				}
				if _, e := service.authorityForAdvertisement(t.Context(), turn.Run.ID); e == nil {
					t.Fatal("unproven cleanup rotated")
				}
			}
		})
	}
}

func TestAgentBrowserCancelledStartedCallNeverDispatchesAgain(t *testing.T) {
	service, st, r, s, turn := newAgentBrowserFixture(t)
	r.block = true
	c := agentBrowserFixtureCall(t, service, st, toolgateway.BrowserNavigateTool, `{"version":"browser_navigate.v2","url":"https://example.org"}`)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, _, _ = s.resumeSupervisorTools(ctx, turn, []domain.SupervisorToolRound{{Calls: []domain.SupervisorToolCall{c}}})
	if !st.started[c.CallID] || len(r.actions) != 1 {
		t.Fatal("cancellation did not exercise a started action")
	}
	// The persisted source is replayed as pending to model an interrupted result
	// commit; the fresh marker must prevent dispatch even with a live caller.
	st.calls[c.CallID] = c
	if _, e := runAgentBrowserFixtureCall(t, s, turn, c); e != nil {
		t.Fatal(e)
	}
	if len(r.actions) != 1 || st.calls[c.CallID].Status == domain.SupervisorToolCompleted {
		t.Fatal("cancelled started action was replayed")
	}
}

func TestAgentBrowserRetainedPermissionsNeverLaunch(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionFullAccess, domain.RunExecutionPermissionDebug} {
		t.Run(string(mode), func(t *testing.T) {
			service, st, runtime, supervisor, turn := newAgentBrowserFixture(t)
			call := agentBrowserFixtureCall(t, service, st, toolgateway.BrowserNavigateTool, `{"version":"browser_navigate.v2","url":"https://example.org"}`)
			retained, err := st.base.executionPermission.Next("retained-browser-permission", mode, true, "operator", "read historical permission", st.base.executionPermission.CreatedAt.Add(time.Millisecond))
			if err != nil || retained.Validate() != nil {
				t.Fatalf("invalid historical fixture: %+v %v", retained, err)
			}
			st.base.executionPermission = retained
			launches := 0
			original := service.launch
			service.launch = func(ctx context.Context, request browserruntime.AgentBrowserStartRequest) (agentBrowserRuntime, error) {
				launches++
				return original(ctx, request)
			}
			if _, err := service.authority(t.Context(), turn.Run.ID); err == nil {
				t.Fatal("historical permission acquired browser authority")
			}
			caps, _, _ := supervisor.supervisorBrowserActionCapabilities(t.Context(), turn, retained)
			if caps.Available {
				t.Fatal("historical browser authority was advertised")
			}
			// Supervisor writes its dispatch intent before invoking the tool.
			// The native binder then rejects stale authority. Recovery must settle
			// that uncompleted intent as unknown without launching or replaying.
			if waiting, err := runAgentBrowserFixtureCall(t, supervisor, turn, call); apperror.CodeOf(err) != apperror.CodeFailedPrecondition || waiting ||
				launches != 0 || len(runtime.actions) != 0 || !st.started[call.CallID] || st.calls[call.CallID] != call {
				t.Fatalf("historical permission changed or dispatched saved call: launches=%d actions=%v call=%+v err=%v", launches, runtime.actions, st.calls[call.CallID], err)
			}
			if waiting, err := runAgentBrowserFixtureCall(t, supervisor, turn, call); err != nil || waiting ||
				launches != 0 || len(runtime.actions) != 0 || st.calls[call.CallID].Status != domain.SupervisorToolFailed ||
				!strings.Contains(st.calls[call.CallID].ResultJSON, "outcome_unknown") {
				t.Fatalf("historical intent was resent or lost unknown outcome: %+v err=%v", st.calls[call.CallID], err)
			}
		})
	}
}
