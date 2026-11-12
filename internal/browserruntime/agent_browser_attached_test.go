package browserruntime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type attachedTestLease struct {
	life     context.Context
	cancel   context.CancelFunc
	released atomic.Int32
}

func newAttachedTestLease() *attachedTestLease {
	c, f := context.WithCancel(context.Background())
	return &attachedTestLease{life: c, cancel: f}
}
func (l *attachedTestLease) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return l.life.Err()
}
func (l *attachedTestLease) Done() <-chan struct{} { return l.life.Done() }
func (l *attachedTestLease) Cancel()               { l.cancel() }
func (l *attachedTestLease) Release(context.Context) error {
	l.Cancel()
	l.released.CompareAndSwap(0, 1)
	return nil
}

type attachedTestConnection struct {
	mu       sync.Mutex
	lease    *attachedTestLease
	messages chan []byte
	closed   chan struct{}
	once     sync.Once
	methods  []string
	mouse    []map[string]any
	url      string
	session  string
}

func (c *attachedTestConnection) ReadJSON(out any) error {
	select {
	case <-c.closed:
		return io.EOF
	case b := <-c.messages:
		return json.Unmarshal(b, out)
	}
}
func (c *attachedTestConnection) WriteJSON(value any) error {
	if err := c.lease.Check(context.Background()); err != nil {
		return err
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var in cdpWireMessage
	if err = json.Unmarshal(b, &in); err != nil {
		return err
	}
	if in.SessionID != c.session {
		return errors.New("foreign native page")
	}
	c.mu.Lock()
	c.methods = append(c.methods, in.Method)
	if in.Method == "Input.dispatchMouseEvent" {
		var params map[string]any
		if err := json.Unmarshal(in.Params, &params); err != nil {
			c.mu.Unlock()
			return err
		}
		c.mouse = append(c.mouse, params)
	}
	c.mu.Unlock()
	var result any = map[string]any{}
	switch in.Method {
	case "Page.getFrameTree":
		result = map[string]any{"frameTree": map[string]any{"frame": map[string]any{"id": "frame", "loaderId": "loader", "url": c.url}}}
	case "Page.getLayoutMetrics":
		result = map[string]any{"cssLayoutViewport": map[string]any{"clientWidth": 800, "clientHeight": 600}}
	case "Page.captureScreenshot":
		var data bytes.Buffer
		if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 1600, 1200))); err != nil {
			return err
		}
		result = map[string]any{"data": base64.StdEncoding.EncodeToString(data.Bytes())}
	case "DOM.getDocument":
		result = map[string]any{"root": map[string]any{"backendNodeId": 1}}
	}
	reply, _ := json.Marshal(cdpWireMessage{ID: in.ID, SessionID: in.SessionID, Result: mustAttachedJSON(result)})
	select {
	case c.messages <- reply:
		return nil
	case <-c.closed:
		return io.EOF
	}
}
func mustAttachedJSON(v any) json.RawMessage                       { b, _ := json.Marshal(v); return b }
func (c *attachedTestConnection) SetWriteDeadline(time.Time) error { return nil }
func (c *attachedTestConnection) Close() error                     { c.once.Do(func() { close(c.closed) }); return nil }
func (c *attachedTestConnection) called(method string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range c.methods {
		if m == method {
			return true
		}
	}
	return false
}

func attachedFixture(t *testing.T) (AgentBrowserStartRequest, AgentBrowserAttachedPage, *attachedTestConnection, *attachedTestLease, *AgentBrowserAttachedNetworkPolicy) {
	t.Helper()
	lease := newAttachedTestLease()
	wire := &attachedTestConnection{lease: lease, messages: make(chan []byte, 64), closed: make(chan struct{}), url: "https://example.com/account", session: "native-page"}
	policy := &AgentBrowserAttachedNetworkPolicy{}
	req := AgentBrowserStartRequest{HomePath: t.TempDir(), Product: BrowserProductEdge, RuntimeDeadline: time.Now().Add(time.Minute), Authority: AgentBrowserAuthority{RunID: "run", ManagerBootID: "boot", SessionID: "agent-session", Generation: 1, PermissionSnapshotID: "permission", PermissionRevision: 1, PermissionActivation: 1, RunAuthorizationFence: 1, PermissionMode: "ask"}, NetworkMode: AgentBrowserNetworkInternet, CheckAuthority: func(context.Context, AgentBrowserAuthority) error { return nil }, ReviewNetworkWrite: func(_ context.Context, _ AgentBrowserNetworkRequest, dispatch func() error) error { return dispatch() }}
	page := AgentBrowserAttachedPage{PageID: "native-page", SessionID: wire.session, Connection: wire, Lease: lease, InstallNetworkPolicy: func(_ context.Context, p AgentBrowserAttachedNetworkPolicy) error { *policy = p; return nil }}
	return req, page, wire, lease, policy
}

func TestAttachedBrowserUsesExactExistingPageAndRetainsItsProfile(t *testing.T) {
	req, page, wire, lease, _ := attachedFixture(t)
	marker := filepath.Join(req.HomePath, "login-cookie-marker")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := AttachAgentBrowser(t.Context(), req, page)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status().CanonicalURL != wire.url || r.Status().SessionID != req.Authority.SessionID {
		t.Fatalf("attached wrong page: %+v", r.Status())
	}
	for _, method := range []string{"Target.createBrowserContext", "Target.createTarget", "Target.attachToTarget", "Browser.setDownloadBehavior", "Fetch.enable", "Page.navigate"} {
		if wire.called(method) {
			t.Fatalf("created or navigated a different page: %s", method)
		}
	}
	receipt, err := r.Close(t.Context())
	if err != nil || !receipt.AgentDetached || !receipt.Complete() || receipt.TreeReaped || receipt.ProfileRemoved || lease.released.Load() != 1 {
		t.Fatalf("untruthful cleanup=%+v err=%v", receipt, err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
		t.Fatalf("user profile changed: %s %v", data, err)
	}
}

func TestAttachedBrowserFailsClosedWithoutNativeNetworkGate(t *testing.T) {
	req, page, wire, lease, _ := attachedFixture(t)
	failure := errors.New("worker network interception unsupported")
	page.InstallNetworkPolicy = func(context.Context, AgentBrowserAttachedNetworkPolicy) error { return failure }
	r, err := AttachAgentBrowser(t.Context(), req, page)
	var launch *AgentBrowserLaunchError
	if r != nil || !errors.Is(err, failure) || !errors.As(err, &launch) || !launch.Cleanup.Complete() || lease.released.Load() != 1 || wire.called("Page.enable") {
		t.Fatalf("unsupported gate accepted: runtime=%v err=%v", r, err)
	}
	select {
	case <-wire.closed:
	default:
		t.Fatal("failed attachment leaked its wire")
	}
}

func TestAttachedBrowserTakeoverCancelsApprovalAndOldReferences(t *testing.T) {
	req, page, _, lease, policy := attachedFixture(t)
	started := make(chan struct{})
	var dispatched atomic.Int32
	req.ReviewNetworkWrite = func(ctx context.Context, _ AgentBrowserNetworkRequest, _ func() error) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	r, err := AttachAgentBrowser(t.Context(), req, page)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close(context.Background())
	r.mu.Lock()
	r.refs["old"] = agentBrowserRef{epoch: r.epoch, snapshot: "old-snapshot"}
	r.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		done <- policy.ReviewRequest(context.Background(), AgentBrowserAttachedNetworkRequest{ID: "post", Method: "POST", URL: "https://example.com/submit", Body: []byte("private"), BodyComplete: true}, func() error { dispatched.Add(1); return nil })
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("approval did not start")
	}
	lease.Cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("takeover approved old write")
		}
	case <-time.After(time.Second):
		t.Fatal("takeover did not cancel pending approval")
	}
	select {
	case <-r.Done():
	case <-time.After(time.Second):
		t.Fatal("lease did not detach")
	}
	r.mu.Lock()
	refs, pending := len(r.refs), len(r.networkPending)
	r.mu.Unlock()
	if refs != 0 || pending != 0 || dispatched.Load() != 0 {
		t.Fatalf("takeover retained state: refs=%d pending=%d sends=%d", refs, pending, dispatched.Load())
	}
	if _, err := r.Navigate(t.Context(), "https://example.com/next"); err == nil {
		t.Fatal("revoked slot navigated")
	}
}

func TestAttachedBrowserNetworkRechecksPermissionAndExactBody(t *testing.T) {
	req, page, _, _, policy := attachedFixture(t)
	var denied atomic.Bool
	var reviews, sends atomic.Int32
	req.CheckAuthority = func(context.Context, AgentBrowserAuthority) error {
		if denied.Load() {
			return errors.New("permission revoked")
		}
		return nil
	}
	req.ReviewNetworkWrite = func(_ context.Context, r AgentBrowserNetworkRequest, send func() error) error {
		reviews.Add(1)
		if r.BodyBytes != 4 || len(r.BodySHA256) != 64 {
			t.Fatalf("inexact body: %+v", r)
		}
		if err := send(); err != nil {
			return err
		}
		if err := send(); err == nil {
			t.Error("request replayed")
		}
		return nil
	}
	r, err := AttachAgentBrowser(t.Context(), req, page)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close(context.Background())
	request := AgentBrowserAttachedNetworkRequest{ID: "request", Method: "POST", URL: "https://example.com/submit", Body: []byte("body")}
	dispatch := func() error { sends.Add(1); return nil }
	if err := policy.ReviewRequest(t.Context(), request, dispatch); err == nil {
		t.Fatal("incomplete body approved")
	}
	request.BodyComplete = true
	if err := policy.ReviewRequest(t.Context(), request, dispatch); err != nil {
		t.Fatal(err)
	}
	denied.Store(true)
	request.Method = "GET"
	if err := policy.ReviewRequest(t.Context(), request, dispatch); err == nil {
		t.Fatal("read ignored revoked permission")
	}
	if reviews.Load() != 1 || sends.Load() != 1 {
		t.Fatalf("review=%d sends=%d", reviews.Load(), sends.Load())
	}
}

func TestAttachedBrowserRefusesExistingPageOutsideDeclaredOrigins(t *testing.T) {
	req, page, wire, _, _ := attachedFixture(t)
	req.NetworkMode = AgentBrowserNetworkRestricted
	req.ScopedOrigins = []string{"https://allowed.example"}
	if r, err := AttachAgentBrowser(t.Context(), req, page); err == nil || r != nil {
		t.Fatal("attached outside Run scope")
	}
	if wire.called("DOM.getDocument") {
		t.Fatal("read DOM outside declared origin")
	}
}

func TestAttachedBrowserInvalidConfigurationReleasesLease(t *testing.T) {
	req, page, _, lease, _ := attachedFixture(t)
	page.InstallNetworkPolicy = nil
	if _, err := AttachAgentBrowser(t.Context(), req, page); err == nil {
		t.Fatal("missing network contract accepted")
	}
	if lease.released.Load() != 1 {
		t.Fatal("rejected attachment retained native control")
	}
}

func TestAttachedBrowserCoordinatesUseSamePageAndRejectResizeOrNavigation(t *testing.T) {
	req, page, wire, _, _ := attachedFixture(t)
	r, err := AttachAgentBrowser(t.Context(), req, page)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close(context.Background())
	point := AgentBrowserPoint{X: 40, Y: 30, DocumentEpoch: r.Status().DocumentEpoch, Viewport: AgentBrowserViewport{Width: 800, Height: 600}}
	if result, err := r.MovePointer(t.Context(), point); err != nil || result.SessionID != req.Authority.SessionID {
		t.Fatalf("move=%+v err=%v", result, err)
	}
	if result, err := r.ClickPoint(t.Context(), point); err != nil || !result.Dispatched {
		t.Fatalf("point click=%+v err=%v", result, err)
	}
	wire.mu.Lock()
	before := len(wire.methods)
	wire.mu.Unlock()
	resized := point
	resized.Viewport.Width = 900
	if _, err := r.ClickPoint(t.Context(), resized); !errors.Is(err, ErrAgentBrowserStaleReference) {
		t.Fatalf("resized point accepted: %v", err)
	}
	r.onEvent(cdpWireMessage{SessionID: wire.session, Method: "Page.navigatedWithinDocument", Params: json.RawMessage(`{"frameId":"frame","url":"https://example.com/new"}`)})
	if _, err := r.ClickPoint(t.Context(), point); !errors.Is(err, ErrAgentBrowserStaleReference) {
		t.Fatalf("old document point accepted: %v", err)
	}
	wire.mu.Lock()
	after := append([]string(nil), wire.methods[before:]...)
	wire.mu.Unlock()
	for _, method := range after {
		if method == "Input.dispatchMouseEvent" {
			t.Fatal("stale point reached native input")
		}
	}
	if err := r.life.Err(); err != nil {
		t.Fatal("ordinary human navigation revoked agent authorization")
	}
}

func TestAttachedBrowserUsesVisibleViewportAndScreenshotPixelScale(t *testing.T) {
	req, page, wire, _, _ := attachedFixture(t)
	r, err := AttachAgentBrowser(t.Context(), req, page)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close(context.Background())
	if _, err := r.NavigateWithViewport(t.Context(), wire.url, AgentBrowserViewport{Width: 390, Height: 844}); err == nil || wire.called("Emulation.setDeviceMetricsOverride") || wire.called("Page.navigate") {
		t.Fatalf("native pane was emulated or navigated: %v", err)
	}
	shot, err := r.Screenshot(t.Context())
	if err != nil || shot.Viewport == nil || *shot.Viewport != (AgentBrowserViewport{Width: 800, Height: 600}) || shot.ImageWidth != 1600 || shot.ImageHeight != 1200 {
		t.Fatalf("incorrect CSS to screenshot pixel mapping: viewport=%+v image=%dx%d err=%v", shot.Viewport, shot.ImageWidth, shot.ImageHeight, err)
	}
	if _, err := r.Scroll(t.Context(), 0, 120); err != nil {
		t.Fatal(err)
	}
	wire.mu.Lock()
	event := wire.mouse[len(wire.mouse)-1]
	wire.mu.Unlock()
	if event["type"] != "mouseWheel" || event["x"] != float64(400) || event["y"] != float64(300) {
		t.Fatalf("scroll outside visible native viewport: %+v", event)
	}
	before := r.Status().DocumentEpoch
	r.onEvent(cdpWireMessage{SessionID: wire.session, Method: "Page.frameResized", Params: json.RawMessage(`{}`)})
	if r.Status().DocumentEpoch <= before || r.life.Err() != nil {
		t.Fatal("native resize did not invalidate document references or cancelled agent")
	}
}
