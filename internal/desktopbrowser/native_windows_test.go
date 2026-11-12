//go:build windows

package desktopbrowser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type probeUI struct {
	dispatcher *Dispatcher
	hwnd       uintptr
	done       chan struct{}
}
type winMessage struct {
	HWND           uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	X, Y           int32
	Private        uint32
}

func startProbeUI(t *testing.T) *probeUI {
	t.Helper()
	ready := make(chan *probeUI, 1)
	failed := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		d, err := NewDispatcher()
		if err != nil {
			failed <- err
			return
		}
		class := windows.StringToUTF16Ptr("STATIC")
		title := windows.StringToUTF16Ptr("Universal Code native WebView2 probe")
		style := uintptr(0x00cf0000)
		if os.Getenv("UC_NATIVE_BROWSER_VISIBLE") == "1" {
			style |= 0x10000000
		}
		hwnd, _, err := createWindowEx.Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)), style, 80, 80, 1100, 800, 0, 0, 0, 0)
		if hwnd == 0 {
			failed <- err
			_ = d.Close()
			return
		}
		ui := &probeUI{d, hwnd, make(chan struct{})}
		ready <- ui
		var message winMessage
		for {
			result, _, _ := user32.NewProc("GetMessageW").Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
			if int32(result) <= 0 {
				break
			}
			user32.NewProc("TranslateMessage").Call(uintptr(unsafe.Pointer(&message)))
			user32.NewProc("DispatchMessageW").Call(uintptr(unsafe.Pointer(&message)))
		}
		close(ui.done)
	}()
	select {
	case err := <-failed:
		t.Fatal(err)
	case ui := <-ready:
		t.Cleanup(func() {
			_ = ui.dispatcher.Invoke(context.Background(), func() error {
				destroyWindow.Call(ui.hwnd)
				_ = ui.dispatcher.Close()
				user32.NewProc("PostQuitMessage").Call(0)
				return nil
			})
			select {
			case <-ui.done:
			case <-time.After(5 * time.Second):
				t.Error("native probe UI did not terminate")
			}
		})
		return ui
	case <-time.After(10 * time.Second):
		t.Fatal("native probe UI did not initialize")
	}
	return nil
}

type probeCDP struct {
	t       *testing.T
	conn    JSONConnection
	id      int64
	session string
}

func (c *probeCDP) call(method string, params any) json.RawMessage {
	c.t.Helper()
	c.id++
	if err := c.conn.WriteJSON(map[string]any{"id": c.id, "method": method, "params": params, "sessionId": c.session}); err != nil {
		c.t.Fatalf("%s dispatch: %v", method, err)
	}
	for {
		var message struct {
			ID     int64           `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  any             `json:"error"`
		}
		if err := c.conn.ReadJSON(&message); err != nil {
			c.t.Fatal(err)
		}
		if message.ID == c.id {
			if message.Error != nil {
				c.t.Fatalf("%s: %v", method, message.Error)
			}
			return message.Result
		}
	}
}
func (c *probeCDP) evaluate(expression string) string {
	c.t.Helper()
	raw := c.call("Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true, "awaitPromise": true})
	var result struct {
		Result struct {
			Value any `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		c.t.Fatal(err)
	}
	return fmt.Sprint(result.Result.Value)
}

// TestNativeBrowserProbe uses the installed WebView2 runtime and real child
// controllers. Opt in on Windows; CI without an interactive desktop skips it.
func TestNativeBrowserProbe(t *testing.T) {
	if os.Getenv("UC_NATIVE_BROWSER_PROBE") != "1" {
		t.Skip("set UC_NATIVE_BROWSER_PROBE=1 to exercise installed WebView2")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	ui := startProbeUI(t)
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/write" {
			writes.Add(1)
			fmt.Fprint(w, "accepted")
			return
		}
		if r.URL.Path == "/worker.js" {
			w.Header().Set("Content-Type", "text/javascript")
			fmt.Fprint(w, `onmessage=()=>fetch('/write',{method:'POST',body:'worker body'}).then(r=>postMessage(r.status))`)
			return
		}
		if r.URL.Path == "/shared.js" {
			w.Header().Set("Content-Type", "text/javascript")
			fmt.Fprint(w, `onconnect=e=>{e.ports[0].onmessage=()=>fetch('/write',{method:'POST',body:'shared body'}).then(r=>e.ports[0].postMessage(r.status))}`)
			return
		}
		if r.URL.Path == "/sw.js" {
			w.Header().Set("Content-Type", "text/javascript")
			fmt.Fprint(w, `self.addEventListener('activate',e=>e.waitUntil(self.clients.claim()));self.addEventListener('message',e=>e.waitUntil(fetch('/write',{method:'POST',body:'service body'})))`)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/a" {
			http.SetCookie(w, &http.Cookie{Name: "shared_login", Value: "persisted", Path: "/", MaxAge: 3600})
		}
		fmt.Fprintf(w, `<!doctype html><title>Native %s</title><button id="increment" onclick="this.dataset.count=String(Number(this.dataset.count||0)+1)">Increment</button><script>window.worker=new Worker('/worker.js')</script>`, r.URL.Path)
	}))
	defer server.Close()
	profile := t.TempDir()
	host, err := NewHost(ctx, HostOptions{ParentHWND: ui.hwnd, Dispatcher: ui.dispatcher, UserDataDir: profile})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close(context.Background()) })
	page, err := host.CreateTab(ctx, TabOptions{ID: "probe-page-a", URL: server.URL + "/a"})
	if err != nil {
		t.Fatal(err)
	}
	if err = page.SetSurface(ctx, Bounds{X: 10, Y: 40, Width: 800, Height: 600}, true); err != nil {
		t.Fatal(err)
	}
	connection, err := page.OpenCDP(ctx, CDPOptions{Events: []string{"Page.frameNavigated", "Page.lifecycleEvent", "DOM.documentUpdated"}, Dispatch: func(ctx context.Context, send func() error) error { return send() }})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	go func() { <-ctx.Done(); _ = connection.Close() }()
	cdp := &probeCDP{t: t, conn: connection, session: page.ID()}
	cdp.call("Runtime.enable", map[string]any{})
	waitProbe(t, ctx, func() bool { return page.State().Title == "Native /a" })
	if got := cdp.evaluate(`document.getElementById('increment').click();document.getElementById('increment').dataset.count`); got != "1" {
		t.Fatalf("visible page DOM mismatch: %s", got)
	}
	if got := cdp.evaluate(`document.cookie`); !strings.Contains(got, "shared_login=persisted") {
		t.Fatalf("missing login cookie: %s", got)
	}
	for _, kind := range []string{"mouseMoved", "mousePressed", "mouseReleased"} {
		cdp.call("Input.dispatchMouseEvent", map[string]any{"type": kind, "x": 16, "y": 16, "button": "left", "clickCount": 1})
	}
	if got := cdp.evaluate(`document.getElementById('increment').dataset.count`); got != "2" {
		t.Fatalf("actual native page mouse click did not affect DOM: %s", got)
	}
	if page.(*nativePage).cursor == 0 {
		t.Fatalf("CDP mouse input did not create a native agent cursor: %v", page.(*nativePage).cursorError)
	}
	if err = ui.dispatcher.Invoke(ctx, func() error {
		showWindow.Call(ui.hwnd, 5)
		p := page.(*nativePage)
		p.positionCursor()
		var before, after nativeRect
		user32.NewProc("GetWindowRect").Call(p.cursor, uintptr(unsafe.Pointer(&before)))
		moveWindow.Call(ui.hwnd, 280, 180, 1100, 800, 1)
		user32.NewProc("GetWindowRect").Call(p.cursor, uintptr(unsafe.Pointer(&after)))
		if after.Left-before.Left != 200 || after.Top-before.Top != 100 {
			return fmt.Errorf("agent cursor did not follow native parent move: before=%+v after=%+v", before, after)
		}
		showWindow.Call(ui.hwnd, 0)
		if visible, _, _ := user32.NewProc("IsWindowVisible").Call(p.cursor); visible != 0 {
			return fmt.Errorf("agent cursor stayed visible after its desktop parent was hidden")
		}
		showWindow.Call(ui.hwnd, 5)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = page.Navigate(ctx, server.URL+"/b"); err != nil {
		t.Fatal(err)
	}
	waitProbe(t, ctx, func() bool { return page.State().Title == "Native /b" && page.State().CanGoBack })
	if err = page.Back(ctx); err != nil {
		t.Fatal(err)
	}
	waitProbe(t, ctx, func() bool { return page.State().Title == "Native /a" && page.State().CanGoForward })
	second, err := host.CreateTab(ctx, TabOptions{ID: "probe-page-b", URL: server.URL + "/second"})
	if err != nil {
		t.Fatal(err)
	}
	if err = second.SetSurface(ctx, Bounds{}, false); err != nil {
		t.Fatal(err)
	}
	if err = connection.WriteJSON(map[string]any{"id": 99, "method": "Runtime.evaluate", "params": map[string]any{"expression": "1"}, "sessionId": second.ID()}); err == nil {
		t.Fatal("connection accepted a sibling controller identity")
	}
	if err = connection.WriteJSON(map[string]any{"id": 99, "method": "Target.createTarget", "params": map[string]any{}, "sessionId": page.ID()}); err == nil {
		t.Fatal("page connection accepted a Target mutation")
	}
	if got := cdp.evaluate(`window.shared=new SharedWorker('/shared.js');shared.port.start();'ready'`); got != "ready" {
		t.Fatal(got)
	}
	if got := cdp.evaluate(`navigator.serviceWorker.register('/sw.js').then(()=>navigator.serviceWorker.ready).then(r=>{window.sw=r.active;return 'ready'})`); got != "ready" {
		t.Fatal(got)
	}
	lease, err := host.AcquireControl(ctx, page.ID())
	if err != nil {
		t.Fatal(err)
	}
	var seen atomic.Int32
	var bodyMu sync.Mutex
	bodies := map[string]bool{}
	err = lease.InstallNetworkPolicy(ctx, NetworkPolicy{Done: lease.Done(), ReviewRequest: func(ctx context.Context, request NetworkRequest, dispatch func() error) error {
		seen.Add(1)
		if strings.HasSuffix(request.URL, "/write") {
			bodyMu.Lock()
			bodies[string(request.Body)] = request.BodyComplete
			bodyMu.Unlock()
			return fmt.Errorf("denied by probe")
		}
		return dispatch()
	}})
	if err != nil {
		t.Fatal(err)
	}
	cdp.call("Runtime.evaluate", map[string]any{"expression": "worker.postMessage('go')"})
	cdp.call("Runtime.evaluate", map[string]any{"expression": "shared.port.postMessage('go');sw.postMessage('go')"})
	waitProbe(t, ctx, func() bool { bodyMu.Lock(); defer bodyMu.Unlock(); return len(bodies) >= 3 })
	bodyMu.Lock()
	for _, kind := range []string{"worker body", "shared body", "service body"} {
		if !bodies[kind] {
			t.Errorf("native request body missing/incomplete for %q: %#v", kind, bodies)
		}
	}
	bodyMu.Unlock()
	if writes.Load() != 0 {
		t.Fatal("worker POST bypassed native policy")
	}
	var enabled uintptr
	_ = ui.dispatcher.Invoke(ctx, func() error {
		enabled, _, _ = user32.NewProc("IsWindowEnabled").Call(page.(*nativePage).container)
		return nil
	})
	if enabled == 0 {
		t.Fatal("agent lease disabled native human input")
	}
	leaseConnection, err := page.OpenCDP(ctx, CDPOptions{Dispatch: lease.Dispatch})
	if err != nil {
		t.Fatal(err)
	}
	blocked, unblock := make(chan struct{}), make(chan struct{})
	if err = ui.dispatcher.Post(func() { close(blocked); <-unblock }); err != nil {
		t.Fatal(err)
	}
	<-blocked
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- leaseConnection.WriteJSON(map[string]any{"id": 1, "method": "Runtime.evaluate", "params": map[string]any{"expression": "window.revokedWrite=true"}, "sessionId": page.ID()})
	}()
	waitProbe(t, ctx, func() bool {
		ui.dispatcher.mu.Lock()
		defer ui.dispatcher.mu.Unlock()
		return len(ui.dispatcher.queue) > 0
	})
	lease.Cancel()
	close(unblock)
	if err = <-writeDone; err == nil {
		t.Fatal("revoked queued CDP write was dispatched")
	}
	_ = leaseConnection.Close()
	if got := cdp.evaluate(`String(window.revokedWrite)`); got != "undefined" {
		t.Fatalf("revoked UI write mutated native document: %s", got)
	}
	if err = lease.Release(ctx); err != nil {
		t.Fatal(err)
	}
	cdp.call("Runtime.evaluate", map[string]any{"expression": "worker.postMessage('go')"})
	waitProbe(t, ctx, func() bool { return writes.Load() == 1 })
	if err = page.Focus(ctx); err != nil {
		t.Fatal(err)
	}
	if err = connection.Close(); err != nil {
		t.Fatal(err)
	}
	if page.State().Closed {
		t.Fatal("detaching CDP closed user's page")
	}
	if err = second.Close(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("real WebView2 parent=%x child=%x cursor=%x: DOM/mouse interaction, history, cookie, exact-page CDP, dedicated/shared/service-worker request+body denial, native human input and queued lease cancellation passed", ui.hwnd, page.(*nativePage).container, page.(*nativePage).cursor)
}

func waitProbe(t *testing.T, ctx context.Context, predicate func() bool) {
	t.Helper()
	for !predicate() {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestNativeParentDestroyClosesPages(t *testing.T) {
	if os.Getenv("UC_NATIVE_BROWSER_PROBE") != "1" {
		t.Skip("requires native WebView2 probe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ui := startProbeUI(t)
	host, err := NewHost(ctx, HostOptions{ParentHWND: ui.hwnd, Dispatcher: ui.dispatcher, UserDataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	page, err := host.CreateTab(ctx, TabOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = ui.dispatcher.Invoke(ctx, func() error { destroyWindow.Call(ui.hwnd); return nil }); err != nil {
		t.Fatal(err)
	}
	if !page.State().Closed {
		t.Fatal("destroying the native parent left the browser page alive")
	}
	if _, err = host.CreateTab(ctx, TabOptions{}); err == nil {
		t.Fatal("destroyed native parent accepted a new page")
	}
}
