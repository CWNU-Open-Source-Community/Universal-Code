package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolgateway"
)

type sharedTestLease struct {
	ctx      context.Context
	cancel   context.CancelFunc
	released atomic.Bool
}

func (l *sharedTestLease) Check(ctx context.Context) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	return l.ctx.Err()
}
func (l *sharedTestLease) Done() <-chan struct{} { return l.ctx.Done() }
func (l *sharedTestLease) Cancel()               { l.cancel() }
func (l *sharedTestLease) Release(context.Context) error {
	l.Cancel()
	l.released.Store(true)
	return nil
}

type sharedTestWire struct {
	lease    *sharedTestLease
	messages chan []byte
	closed   chan struct{}
	once     sync.Once
	mu       sync.Mutex
	methods  []string
	session  string
}

func (w *sharedTestWire) ReadJSON(out any) error {
	select {
	case <-w.closed:
		return io.EOF
	case raw := <-w.messages:
		return json.Unmarshal(raw, out)
	}
}
func (w *sharedTestWire) WriteJSON(v any) error {
	if e := w.lease.Check(context.Background()); e != nil {
		return e
	}
	raw, e := json.Marshal(v)
	if e != nil {
		return e
	}
	var m struct {
		ID                int64
		SessionID, Method string
	}
	if e = json.Unmarshal(raw, &m); e != nil {
		return e
	}
	if m.SessionID != w.session {
		return errors.New("foreign native target")
	}
	w.mu.Lock()
	w.methods = append(w.methods, m.Method)
	w.mu.Unlock()
	result := any(map[string]any{})
	switch m.Method {
	case "Page.getFrameTree":
		result = map[string]any{"frameTree": map[string]any{"frame": map[string]any{"id": "main", "loaderId": "logged-in-loader", "url": "https://example.org/account"}}}
	case "DOM.getDocument":
		result = map[string]any{"root": map[string]any{"backendNodeId": 1}}
	case "Page.getLayoutMetrics":
		result = map[string]any{"cssLayoutViewport": map[string]any{"clientWidth": 800, "clientHeight": 600}}
	case "Page.createIsolatedWorld":
		result = map[string]any{"executionContextId": 1}
	case "DOM.resolveNode":
		result = map[string]any{"object": map[string]any{"objectId": "document"}}
	case "Runtime.callFunctionOn":
		result = map[string]any{"result": map[string]any{"value": map[string]any{"title": "Signed-in account", "text": "Existing signed-in page", "ready": "complete"}}}
	case "Accessibility.getFullAXTree":
		result = map[string]any{"nodes": []any{}}
	}
	reply, _ := json.Marshal(map[string]any{"id": m.ID, "sessionId": m.SessionID, "result": result})
	select {
	case w.messages <- reply:
		return nil
	case <-w.closed:
		return io.EOF
	}
}
func (w *sharedTestWire) SetWriteDeadline(time.Time) error { return nil }
func (w *sharedTestWire) Close() error                     { w.once.Do(func() { close(w.closed) }); return nil }
func (w *sharedTestWire) called(name string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, m := range w.methods {
		if m == name {
			return true
		}
	}
	return false
}

func sharedBrowserTestProvider(t *testing.T, s *AgentBrowserService) (*atomic.Int32, *[]*sharedTestWire) {
	t.Helper()
	calls := &atomic.Int32{}
	wires := &[]*sharedTestWire{}
	s.options.SharedPage = func(_ context.Context, request browserruntime.AgentBrowserStartRequest) (browserruntime.AgentBrowserAttachedPage, error) {
		calls.Add(1)
		ctx, cancel := context.WithCancel(context.Background())
		lease := &sharedTestLease{ctx: ctx, cancel: cancel}
		wire := &sharedTestWire{lease: lease, messages: make(chan []byte, 128), closed: make(chan struct{}), session: "native-page"}
		*wires = append(*wires, wire)
		if request.Authority.RunID == "" {
			t.Fatal("missing bound Run")
		}
		return browserruntime.AgentBrowserAttachedPage{PageID: "native-page", SessionID: wire.session, Connection: wire, Lease: lease, InstallNetworkPolicy: func(context.Context, browserruntime.AgentBrowserAttachedNetworkPolicy) error { return nil }}, nil
	}
	return calls, wires
}

func TestSharedBrowserFirstAuthorizedSnapshotAttachesLoggedInPage(t *testing.T) {
	service, st, _, supervisor, turn := newAgentBrowserFixture(t)
	calls, wires := sharedBrowserTestProvider(t, service)
	service.launch = func(context.Context, browserruntime.AgentBrowserStartRequest) (agentBrowserRuntime, error) {
		t.Error("opened shadow browser")
		return nil, errors.New("must attach exact page")
	}
	call := agentBrowserFixtureCall(t, service, st, toolgateway.BrowserSnapshotTool, `{"version":"browser_snapshot.v2"}`)
	waiting, err := runAgentBrowserFixtureCall(t, supervisor, turn, call)
	if err != nil || waiting || calls.Load() != 1 || st.calls[call.CallID].Status != domain.SupervisorToolCompleted {
		t.Fatalf("first snapshot=%+v waiting=%t calls=%d err=%v", st.calls[call.CallID], waiting, calls.Load(), err)
	}
	wire := (*wires)[0]
	if wire.called("Page.navigate") || wire.called("Target.createTarget") {
		t.Fatal("replaced existing logged-in document")
	}
	if !wire.called("Runtime.callFunctionOn") {
		t.Fatal("snapshot was not observed")
	}
}

func TestSharedBrowserToolbarAttachAndDetachPreserveRunAndRetireOldAuthority(t *testing.T) {
	service, st, _, _, _ := newAgentBrowserFixture(t)
	calls, wires := sharedBrowserTestProvider(t, service)
	before := st.base.run.Status
	first, err := service.AttachSharedPage(t.Context(), st.base.run.ID)
	if err != nil || first.SessionID == "" || first.Headless || !first.Capabilities.Available {
		t.Fatalf("attach=%+v err=%v", first, err)
	}
	if (*wires)[0].called("Runtime.callFunctionOn") || (*wires)[0].called("Page.navigate") {
		t.Fatal("toolbar attachment dispatched model content/input")
	}
	old, err := service.authority(t.Context(), st.base.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	released, err := service.DetachForUser(t.Context(), st.base.run.ID, first.SessionID)
	if err != nil || released.Cleanup == nil || !released.Cleanup.AgentDetached || !released.Cleanup.Complete() {
		t.Fatalf("release=%+v err=%v", released, err)
	}
	if st.base.run.Status != before || !(*wires)[0].lease.released.Load() {
		t.Fatal("takeover changed the Run or kept old control")
	}
	next, err := service.AttachSharedPage(t.Context(), st.base.run.ID)
	if err != nil || next.SessionID == first.SessionID || next.Generation <= first.Generation || calls.Load() != 2 {
		t.Fatalf("fresh binding=%+v first=%+v calls=%d err=%v", next, first, calls.Load(), err)
	}
	if _, err := service.ExecuteAgentBrowserAction(t.Context(), toolgateway.AgentBrowserExecutionScope{Authority: old}, toolgateway.BrowserSnapshotTool, json.RawMessage(`{"version":"browser_snapshot.v2"}`)); err == nil {
		t.Fatal("rebound old durable authority to new native lease")
	}
}

func TestSharedBrowserAttachmentCannotBypassBrowserNetworkPermission(t *testing.T) {
	service, st, _, _, _ := newAgentBrowserFixture(t)
	calls, _ := sharedBrowserTestProvider(t, service)
	st.base.run.Config.BrowserNetworkAccess = domain.BrowserNetworkOff
	if _, err := service.AttachSharedPage(t.Context(), st.base.run.ID); err == nil {
		t.Fatal("manual page bypassed Run browser permission")
	}
	if calls.Load() != 0 {
		t.Fatal("permission checked after native binding")
	}
}

func TestSharedBrowserCoordinatesUseAuthorizedDurableNativeSession(t *testing.T) {
	service, st, _, supervisor, turn := newAgentBrowserFixture(t)
	_, wires := sharedBrowserTestProvider(t, service)
	view, err := service.AttachSharedPage(t.Context(), st.base.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []toolgateway.ToolName{toolgateway.BrowserMoveTool, toolgateway.BrowserClickTool} {
		payload := fmt.Sprintf(`{"version":"%s.v2","point":{"x":30,"y":40,"document_epoch":%d,"viewport":{"width":800,"height":600}}}`, name, view.DocumentEpoch)
		call := agentBrowserFixtureCall(t, service, st, name, payload)
		waiting, err := runAgentBrowserFixtureCall(t, supervisor, turn, call)
		if err != nil || waiting || st.calls[call.CallID].Status != domain.SupervisorToolCompleted {
			t.Fatalf("native point %s: call=%+v waiting=%t err=%v", name, st.calls[call.CallID], waiting, err)
		}
	}
	wire := (*wires)[0]
	wire.mu.Lock()
	before := 0
	for _, m := range wire.methods {
		if m == "Input.dispatchMouseEvent" {
			before++
		}
	}
	wire.mu.Unlock()
	if before != 4 || wire.called("Target.createTarget") || wire.called("Page.navigate") {
		t.Fatalf("coordinate path escaped native page: inputs=%d", before)
	}
	stale := agentBrowserFixtureCall(t, service, st, toolgateway.BrowserClickTool, fmt.Sprintf(`{"version":"browser_click.v2","point":{"x":30,"y":40,"document_epoch":%d,"viewport":{"width":800,"height":600}}}`, view.DocumentEpoch+1))
	_, _ = runAgentBrowserFixtureCall(t, supervisor, turn, stale)
	if st.calls[stale.CallID].Status == domain.SupervisorToolCompleted {
		t.Fatal("stale coordinate completed")
	}
	wire.mu.Lock()
	after := 0
	for _, m := range wire.methods {
		if m == "Input.dispatchMouseEvent" {
			after++
		}
	}
	wire.mu.Unlock()
	if after != before {
		t.Fatal("stale coordinate reached native page")
	}
}
