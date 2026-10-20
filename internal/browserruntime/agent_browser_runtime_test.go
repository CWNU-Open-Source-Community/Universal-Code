package browserruntime

import (
	"context"
	"encoding/json"
	"errors"
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

	"github.com/gorilla/websocket"
)

func TestAgentBrowserURLBoundary(t *testing.T) {
	for _, raw := range []string{"https://developer.mozilla.org/en-US/", "http://127.0.0.1:4321/", "https://example.com/a?q=b#c"} {
		if _, err := agentBrowserURL(raw); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{"file:///a", "javascript:alert(1)", "https://u:p@example.com/", "https://example.com/\n", "data:text/html,abc"} {
		if _, err := agentBrowserURL(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	args := strings.Join(fixedAgentBrowserArguments(t.TempDir(), true), " ")
	for _, bad := range []string{"host-resolver-rules", "no-sandbox", "ignore-certificate-errors", "no-proxy-server", "disable-web-security"} {
		if strings.Contains(args, bad) {
			t.Fatalf("unsafe/incompatible browser args: %s", bad)
		}
	}
}

func TestAgentBrowserStartSpecAndAuthorityAreClosed(t *testing.T) {
	now := time.Now().UTC()
	base := t.TempDir()
	hash := strings.Repeat("a", 64)
	spec := BrowserStartSpec{ProtocolVersion: agentBrowserProcessVersion, AuthorizationFingerprint: hash, ExecutableIdentityFingerprint: hash, ExecutablePath: filepath.Join(base, "browser.exe"), ExecutableSHA256: hash, ProfileOwnershipFingerprint: hash, ProfileLeaseFingerprint: hash, ProfilePath: base, Arguments: fixedAgentBrowserArguments(base, true), InitialURL: "about:blank", RemoteDebuggingAddress: "127.0.0.1", ActiveProcessLimit: MaxBrowserProcessCount, JobMemoryLimitBytes: MaxBrowserJobMemoryBytes, CreatedAt: now, RuntimeDeadline: now.Add(time.Minute)}
	spec.Fingerprint = browserRuntimeFingerprint(spec)
	if err := validateAgentBrowserProcessSpec(spec); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*BrowserStartSpec){func(s *BrowserStartSpec) { s.Arguments = append(append([]string{}, s.Arguments...), "--no-sandbox") }, func(s *BrowserStartSpec) { s.ProtocolVersion = BrowserStartSpecProtocolVersion }, func(s *BrowserStartSpec) { s.PersonalProfileUsed = true }, func(s *BrowserStartSpec) { s.FullCDPUsed = true }, func(s *BrowserStartSpec) { s.RemoteDebuggingAddress = "0.0.0.0" }, func(s *BrowserStartSpec) { s.RuntimeDeadline = now.Add(-time.Second) }} {
		candidate := spec
		mutate(&candidate)
		candidate.Fingerprint = browserRuntimeFingerprint(candidate)
		if err := validateAgentBrowserProcessSpec(candidate); err == nil {
			t.Fatal("widened independent process contract accepted")
		}
	}
	req := AgentBrowserStartRequest{HomePath: base, RuntimeDeadline: now.Add(time.Minute), Authority: AgentBrowserAuthority{RunID: "r", ManagerBootID: "boot", SessionID: "s", Generation: 1, PermissionSnapshotID: "p", PermissionRevision: 1, PermissionActivation: 1, RunAuthorizationFence: 1, PermissionMode: "full"}, CheckAuthority: func(context.Context, AgentBrowserAuthority) error { return nil }}
	if err := validateAgentBrowserRequest(req); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*AgentBrowserStartRequest){func(r *AgentBrowserStartRequest) { r.Authority.PermissionMode = "read_only" }, func(r *AgentBrowserStartRequest) { r.Authority.PermissionMode = "full_access" }, func(r *AgentBrowserStartRequest) { r.Authority.PermissionMode = "debug" }, func(r *AgentBrowserStartRequest) { r.Authority.ManagerBootID = "" }, func(r *AgentBrowserStartRequest) { r.Authority.Generation = 0 }, func(r *AgentBrowserStartRequest) { r.Authority.PermissionActivation = 0 }, func(r *AgentBrowserStartRequest) { r.Authority.RunAuthorizationFence = 0 }, func(r *AgentBrowserStartRequest) { r.CheckAuthority = nil }} {
		candidate := req
		mutate(&candidate)
		if err := validateAgentBrowserRequest(candidate); err == nil {
			t.Fatal("missing runtime authority accepted")
		}
	}
}

func TestAgentBrowserProfileOwnershipAndPartialCleanup(t *testing.T) {
	home := t.TempDir()
	p, err := prepareAgentBrowserProfile(home, AgentBrowserAuthority{SessionID: "owned"})
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(p.path, ".agent-browser-owner.json")
	if err = os.WriteFile(marker, []byte("wrong owner"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = p.cleanup(); !errors.Is(err, ErrBrowserRuntimeBoundary) {
		t.Fatalf("foreign marker accepted: %v", err)
	}
	if _, err = os.Stat(p.path); err != nil {
		t.Fatalf("foreign profile removed: %v", err)
	}
	if err = os.WriteFile(marker, p.marker, 0600); err != nil {
		t.Fatal(err)
	}
	// Simulate an interrupted delete after the marker itself has gone away.
	proof := filepath.Join(p.root, ".cleanup-"+p.token+".json")
	quarantine := filepath.Join(p.root, "cleanup-"+p.token)
	if err = os.WriteFile(proof, p.marker, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(p.path, quarantine); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(quarantine, ".agent-browser-owner.json")); err != nil {
		t.Fatal(err)
	}
	if err = p.cleanup(); err != nil {
		t.Fatal(err)
	}
	if err = p.cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(quarantine); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("quarantine survives: %v", err)
	}
}

func TestAgentBrowserCDPLateReplyDoesNotSatisfyNextCall(t *testing.T) {
	upgrader := websocket.Upgrader{}
	events := make(chan cdpWireMessage, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var first, second cdpWireMessage
		if conn.ReadJSON(&first) != nil {
			return
		}
		if conn.ReadJSON(&second) != nil {
			return
		}
		_ = conn.WriteJSON(cdpWireMessage{Method: "Page.navigatedWithinDocument", SessionID: "session", Params: json.RawMessage(`{"frameId":"main","url":"https://example.com/next"}`)})
		_ = conn.WriteJSON(cdpWireMessage{ID: first.ID, SessionID: first.SessionID, Result: json.RawMessage(`{"marker":"late"}`)})
		_ = conn.WriteJSON(cdpWireMessage{ID: second.ID, SessionID: second.SessionID, Result: json.RawMessage(`{"marker":"current"}`)})
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	c := newAgentBrowserCDP(conn, func(event cdpWireMessage) { events <- event })
	defer c.close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	var got struct{ Marker string }
	if err = c.call(ctx, "session", "DOM.getDocument", map[string]any{}, &got); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first call: %v", err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err = c.call(ctx2, "session", "DOM.getDocument", map[string]any{}, &got); err != nil || got.Marker != "current" {
		t.Fatalf("late reply mixed into next call: %+v %v", got, err)
	}
	select {
	case event := <-events:
		if event.Method != "Page.navigatedWithinDocument" {
			t.Fatal(event.Method)
		}
	case <-ctx2.Done():
		t.Fatal("typed event lost")
	}
}

func TestAgentBrowserTypedEventsBindSessionAndInvalidateDocument(t *testing.T) {
	life, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &AgentBrowserRuntime{life: life, cancel: cancel, session: "owned-session", frame: "main", epoch: 1, refs: map[string]agentBrowserRef{"old": {epoch: 1}}}
	params := json.RawMessage(`{"frameId":"main","url":"https://example.com/next"}`)
	r.onEvent(cdpWireMessage{SessionID: "other-session", Method: "Page.navigatedWithinDocument", Params: params})
	if r.epoch != 1 || len(r.refs) != 1 {
		t.Fatal("foreign session invalidated owned document")
	}
	r.onEvent(cdpWireMessage{SessionID: "owned-session", Method: "Page.navigatedWithinDocument", Params: params})
	if r.epoch != 2 || len(r.refs) != 0 {
		t.Fatal("owned same-document navigation retained old refs")
	}
	r.onEvent(cdpWireMessage{Method: "Target.detachedFromTarget", Params: json.RawMessage(`{"sessionId":"owned-session"}`)})
	if life.Err() == nil {
		t.Fatal("detached target remained executable")
	}
}

func TestAgentBrowserRealFocusReplacementIsUnknownAndCloses(t *testing.T) {
	r, _ := agentBrowserTestRuntime(t)
	var leaked atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/leak" {
			leaked.Add(1)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><title>Focus change</title><input aria-label="Original" id="original"><input aria-label="Other" id="other"><script>original.onfocus=()=>{original.remove();other.focus()};other.oninput=()=>fetch('/leak')</script>`)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := r.Navigate(ctx, server.URL); err != nil {
		t.Fatal(err)
	}
	s, err := r.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e := agentBrowserElementByName(t, s, "Original")
	_, err = r.Type(ctx, s.SnapshotID, e.Ref, "must not enter other field", true)
	var action *AgentBrowserActionError
	if !errors.As(err, &action) || !action.Dispatched || !action.OutcomeUnknown {
		t.Fatalf("focus mutation must be unknown: %v", err)
	}
	select {
	case <-r.Done():
	case <-ctx.Done():
		t.Fatal("partial input runtime remained alive")
	}
	if leaked.Load() != 0 {
		t.Fatalf("text leaked into replacement field: %d", leaked.Load())
	}
}

func agentBrowserTestRuntime(t *testing.T) (*AgentBrowserRuntime, *atomic.Bool) {
	return agentBrowserTestRuntimeWithCheck(t, nil)
}

func agentBrowserTestRuntimeWithCheck(t *testing.T, extra func(context.Context) error) (*AgentBrowserRuntime, *atomic.Bool) {
	t.Helper()
	if os.Getenv("TRAVERSE_TEST_AGENT_BROWSER") != "1" {
		t.Skip("opt-in real installed browser")
	}
	if !AgentBrowserPlatformAvailable() {
		t.Fatal("requested real Agent Browser runtime is unavailable")
	}
	home := t.TempDir()
	if runtime.GOOS == "darwin" {
		var err error
		home, err = filepath.EvalSymlinks(home)
		if err != nil {
			t.Fatal(err)
		}
	}
	active := &atomic.Bool{}
	active.Store(true)
	startupTimeout, lifetime := 40*time.Second, 2*time.Minute
	if runtime.GOOS == "darwin" {
		// A cold full Chrome bundle signature check took 77s on the native
		// Intel runner. Keep it enabled and leave time for the actual actions.
		startupTimeout, lifetime = 3*time.Minute, 5*time.Minute
	}
	request := AgentBrowserStartRequest{HomePath: home, Headless: true, Product: BrowserProductEdge, RuntimeDeadline: time.Now().Add(lifetime), Authority: AgentBrowserAuthority{RunID: "runtime-test", ManagerBootID: agentBrowserToken(), SessionID: agentBrowserToken(), Generation: 1, PermissionSnapshotID: "snapshot-test", PermissionRevision: 1, PermissionActivation: 1, RunAuthorizationFence: 1, PermissionMode: "full"}, CheckAuthority: func(ctx context.Context, _ AgentBrowserAuthority) error {
		if !active.Load() {
			return errors.New("authority revoked")
		}
		if extra != nil {
			if err := extra(ctx); err != nil {
				return err
			}
		}
		return ctx.Err()
	}}
	if runtime.GOOS == "darwin" {
		request.Product = BrowserProductChrome
	}
	if product := os.Getenv("TRAVERSE_TEST_AGENT_BROWSER_PRODUCT"); product != "" {
		request.Product = BrowserProduct(product)
	}
	ctx, cancel := context.WithTimeout(context.Background(), startupTimeout)
	defer cancel()
	r, err := LaunchAgentBrowser(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		result, err := r.Close(ctx)
		if err != nil || !result.ProcessesStopped() || !result.ProfileRemoved || result.CleanupPending {
			t.Errorf("owned cleanup: %+v %v", result, err)
		}
		if _, err := os.Stat(r.profile.path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("profile survives: %v", err)
		}
	})
	t.Logf("owned browser launched: product=%s pid=%d profile=%s", r.product, r.process.PID(), r.profile.path)
	return r, active
}

func TestAgentBrowserRealCancelDoesNotWaitForAuthorityLookup(t *testing.T) {
	var block atomic.Bool
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	r, _ := agentBrowserTestRuntimeWithCheck(t, func(context.Context) error {
		if block.Load() {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
		}
		return nil
	})
	block.Store(true)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("monitor did not enter authority check")
	}
	r.Cancel()
	select {
	case <-r.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("Cancel waited behind authority callback")
	}
	if status := r.Status(); status.State != "closed" {
		t.Fatalf("cleanup incomplete: %+v", status)
	}
}

func agentBrowserWaitSnapshot(t *testing.T, r *AgentBrowserRuntime, predicate func(AgentBrowserSnapshot) bool) AgentBrowserSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		snapshot, err := r.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if predicate(snapshot) {
			return snapshot
		}
		select {
		case <-ctx.Done():
			t.Fatalf("snapshot condition timeout: %s", snapshot.Text)
		case <-time.After(50 * time.Millisecond):
		}
	}
}
func agentBrowserElementByName(t *testing.T, s AgentBrowserSnapshot, name string) AgentBrowserElement {
	t.Helper()
	for _, e := range s.Elements {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("missing %q in %+v", name, s.Elements)
	return AgentBrowserElement{}
}

func TestAgentBrowserRealDynamicAndCleanup(t *testing.T) {
	r, _ := agentBrowserTestRuntime(t)
	var replace, overlay atomic.Bool
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if req.URL.Path == "/ws" {
			conn, err := upgrader.Upgrade(w, req, nil)
			if err == nil {
				_ = conn.WriteMessage(websocket.TextMessage, []byte("socket-ready"))
				_ = conn.Close()
			}
			return
		}
		if req.URL.Path == "/next" {
			fmt.Fprint(w, `<!doctype html><title>Second origin</title><h1>Cross origin page</h1>`)
			return
		}
		fmt.Fprint(w, "remote:"+req.URL.Query().Get("q"))
	}))
	defer remote.Close()
	var local *httptest.Server
	local = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/state":
			fmt.Fprintf(w, `{"replace":%t,"overlay":%t}`, replace.Load(), overlay.Load())
			return
		case "/sw.js":
			w.Header().Set("Content-Type", "application/javascript")
			fmt.Fprint(w, `self.addEventListener('install',e=>self.skipWaiting());self.addEventListener('activate',e=>e.waitUntil(clients.claim()));self.addEventListener('fetch',e=>{if(new URL(e.request.url).pathname==='/sw-value')e.respondWith(new Response('worker-ready'))})`)
			return
		case "/redirect":
			http.Redirect(w, req, remote.URL+"/next", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!doctype html><title>Dynamic fixture</title><h1>Live dynamic fixture</h1><p id="clock"></p><p id="remote"></p><p id="socket"></p><p id="worker"></p><div id="mount"></div><button id="target">Stable target</button><p id="effect">untouched</p><a href="/redirect">Next origin</a>
<script>
setInterval(()=>clock.textContent='Clock '+Date.now(),25);
setTimeout(()=>{mount.innerHTML='<label>Query<input aria-label="Query"></label><button id="search">Search</button>';search.onclick=async()=>{remote.textContent=await(await fetch(%q+'/api?q='+encodeURIComponent(document.querySelector('input').value))).text()}},150);
new WebSocket(%q+'/ws').onmessage=e=>socket.textContent=e.data;
navigator.serviceWorker.register('/sw.js').then(()=>navigator.serviceWorker.ready).then(async()=>{await new Promise(r=>{if(navigator.serviceWorker.controller)r();else navigator.serviceWorker.addEventListener('controllerchange',r,{once:true})});worker.textContent=await(await fetch('/sw-value')).text()});
target.onclick=()=>effect.textContent='clicked';
setInterval(async()=>{const s=await(await fetch('/state')).json();if(s.replace&&!window.replaced){window.replaced=true;target.outerHTML='<button id="target">Stable target</button>';effect.textContent='replaced'}if(s.overlay&&!document.getElementById('overlay')){const e=document.createElement('div');e.id='overlay';e.style='position:fixed;inset:0;background:rgba(1,2,3,.2);z-index:999';document.body.append(e);effect.textContent='obscured'}},50);
</script>`, remote.URL, "ws"+strings.TrimPrefix(remote.URL, "http"))
	}))
	defer local.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	nav, err := r.Navigate(ctx, local.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("navigation epoch=%d url=%s", nav.DocumentEpoch, nav.CanonicalURL)
	snapshot := agentBrowserWaitSnapshot(t, r, func(s AgentBrowserSnapshot) bool {
		return strings.Contains(s.Text, "socket-ready") && strings.Contains(s.Text, "worker-ready") && strings.Contains(s.Text, "Search")
	})
	input := agentBrowserElementByName(t, snapshot, "Query")
	if _, err = r.Type(ctx, snapshot.SnapshotID, input.Ref, "cross origin", true); err != nil {
		t.Fatal(err)
	}
	snapshot, err = r.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	search := agentBrowserElementByName(t, snapshot, "Search")
	if _, err = r.Click(ctx, snapshot.SnapshotID, search.Ref); err != nil {
		t.Fatal(err)
	}
	snapshot = agentBrowserWaitSnapshot(t, r, func(s AgentBrowserSnapshot) bool { return strings.Contains(s.Text, "remote:cross origin") })
	stable := agentBrowserElementByName(t, snapshot, "Stable target")
	// The clock changes repeatedly while this reference remains valid.
	time.Sleep(120 * time.Millisecond)
	if _, err = r.Click(ctx, snapshot.SnapshotID, stable.Ref); err != nil {
		t.Fatalf("live clock invalidated unrelated target: %v", err)
	}
	snapshot = agentBrowserWaitSnapshot(t, r, func(s AgentBrowserSnapshot) bool { return strings.Contains(s.Text, "clicked") })
	stable = agentBrowserElementByName(t, snapshot, "Stable target")
	replace.Store(true)
	time.Sleep(200 * time.Millisecond)
	if _, err = r.Click(ctx, snapshot.SnapshotID, stable.Ref); !errors.Is(err, ErrAgentBrowserTargetChanged) {
		t.Fatalf("replaced backend node accepted: %v", err)
	}
	snapshot = agentBrowserWaitSnapshot(t, r, func(s AgentBrowserSnapshot) bool { return strings.Contains(s.Text, "replaced") })
	stable = agentBrowserElementByName(t, snapshot, "Stable target")
	overlay.Store(true)
	time.Sleep(200 * time.Millisecond)
	if _, err = r.Click(ctx, snapshot.SnapshotID, stable.Ref); !errors.Is(err, ErrAgentBrowserTargetChanged) {
		t.Fatalf("obscured target accepted: %v", err)
	}
	shot, err := r.Screenshot(ctx)
	if err != nil || len(shot.PNG) == 0 || shot.SHA256 == "" {
		t.Fatalf("screenshot: %+v %v", shot, err)
	}
	t.Logf("dynamic JS, cross-origin fetch, WebSocket, service worker, replacement and overlay checked; PNG bytes=%d sha256=%s", shot.Bytes, shot.SHA256)
	if _, err = r.Navigate(ctx, local.URL+"/redirect"); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Click(ctx, snapshot.SnapshotID, stable.Ref); !errors.Is(err, ErrAgentBrowserStaleReference) {
		t.Fatalf("prior-document reference accepted: %v", err)
	}
	snapshot, err = r.Snapshot(ctx)
	if err != nil || !strings.Contains(snapshot.Text, "Cross origin page") {
		t.Fatalf("redirect: %+v %v", snapshot, err)
	}
	r.Cancel()
	select {
	case <-r.Done():
	case <-ctx.Done():
		t.Fatal("cancel cleanup timeout")
	}
	if _, err = r.Snapshot(ctx); !errors.Is(err, ErrAgentBrowserClosed) {
		t.Fatalf("cancelled session reusable: %v", err)
	}
	result, err := r.Close(ctx)
	if err != nil || !result.ProcessesStopped() || !result.ProfileRemoved {
		t.Fatalf("cancel cleanup: %+v %v", result, err)
	}
	t.Logf("owned cancellation cleanup: %+v", result)
}

func TestAgentBrowserRealAuthorityRevocation(t *testing.T) {
	r, active := agentBrowserTestRuntime(t)
	active.Store(false)
	select {
	case <-r.Done():
	case <-time.After(12 * time.Second):
		t.Fatal("authority loss did not close runtime")
	}
	if _, err := r.Snapshot(context.Background()); !errors.Is(err, ErrAgentBrowserClosed) {
		t.Fatalf("revoked runtime accepted action: %v", err)
	}
}

func TestAgentBrowserPublicMDN(t *testing.T) {
	if os.Getenv("TRAVERSE_TEST_AGENT_BROWSER_PUBLIC") != "1" {
		t.Skip("separate bounded public read-only acceptance")
	}
	r, _ := agentBrowserTestRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	nav, err := r.Navigate(ctx, "https://developer.mozilla.org/en-US/docs/Web")
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(s.Title), "web") || len(s.Elements) == 0 {
		t.Fatalf("unexpected public document: %+v", s)
	}
	shot, err := r.Screenshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if evidence := os.Getenv("TRAVERSE_TEST_AGENT_BROWSER_EVIDENCE"); evidence != "" {
		if !filepath.IsAbs(evidence) {
			t.Fatal("evidence must be absolute")
		}
		if err = os.WriteFile(filepath.Join(evidence, "agent-browser-mdn.png"), shot.PNG, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("PUBLIC READ-ONLY: url=%s epoch=%d title=%q elements=%d png_bytes=%d png_sha256=%s", nav.CanonicalURL, s.DocumentEpoch, s.Title, len(s.Elements), shot.Bytes, shot.SHA256)
}

func TestAgentBrowserLaunchFailurePreservesCleanupEvidence(t *testing.T) {
	_, e := LaunchAgentBrowser(t.Context(), AgentBrowserStartRequest{Authority: AgentBrowserAuthority{SessionID: "early-invalid"}})
	var failure *AgentBrowserLaunchError
	if !errors.As(e, &failure) || failure.Cleanup.SessionID != "early-invalid" || !failure.Cleanup.ProcessesStopped() || !failure.Cleanup.ProfileRemoved || failure.Cleanup.CleanupPending {
		t.Fatalf("early boundary receipt %+v %v", failure, e)
	}
	if runtime.GOOS != "windows" {
		return
	}
	identities, e := DiscoverInstalledBrowsers()
	if e != nil || len(identities) == 0 {
		t.Skip("no installed Chromium for profile failure contract")
	}
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			home := t.TempDir()
			checks := 0
			req := AgentBrowserStartRequest{HomePath: home, Headless: true, RuntimeDeadline: time.Now().Add(time.Minute), Authority: AgentBrowserAuthority{RunID: "launch-failure", ManagerBootID: "boot", SessionID: "profile-failure", Generation: 1, PermissionSnapshotID: "p", PermissionRevision: 1, PermissionActivation: 1, RunAuthorizationFence: 1, PermissionMode: "full"}}
			req.CheckAuthority = func(context.Context, AgentBrowserAuthority) error {
				checks++
				if checks == 2 {
					if corrupt {
						markers, _ := filepath.Glob(filepath.Join(home, "runtime", "agent-browser", "profiles", "*", ".agent-browser-owner.json"))
						if len(markers) != 1 {
							t.Fatalf("profile not prepared %v", markers)
						}
						if e := os.WriteFile(markers[0], []byte("foreign marker"), 0600); e != nil {
							t.Fatal(e)
						}
					}
					return errors.New("authority revoked before process start")
				}
				return nil
			}
			_, e := LaunchAgentBrowser(t.Context(), req)
			var failure *AgentBrowserLaunchError
			if checks != 2 || !errors.As(e, &failure) || failure.Cleanup.SessionID != req.Authority.SessionID || !failure.Cleanup.TreeReaped || failure.Cleanup.ProfileRemoved == corrupt || failure.Cleanup.CleanupPending != corrupt {
				t.Fatalf("profile cleanup receipt checks=%d %+v %v", checks, failure, e)
			}
		})
	}
}
