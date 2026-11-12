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
	raw := c.call("Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true})
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
		w.Header().Set("Content-Type", "text/html")
		http.SetCookie(w, &http.Cookie{Name: "shared_login", Value: "persisted", Path: "/", MaxAge: 3600})
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
	lease, err := host.AcquireControl(ctx, page.ID())
	if err != nil {
		t.Fatal(err)
	}
	var seen atomic.Int32
	err = lease.InstallNetworkPolicy(ctx, NetworkPolicy{Done: lease.Done(), ReviewRequest: func(ctx context.Context, request NetworkRequest, dispatch func() error) error {
		seen.Add(1)
		if strings.HasSuffix(request.URL, "/write") {
			return fmt.Errorf("denied by probe")
		}
		return dispatch()
	}})
	if err != nil {
		t.Fatal(err)
	}
	cdp.call("Runtime.evaluate", map[string]any{"expression": "worker.postMessage('go')"})
	waitProbe(t, ctx, func() bool { return seen.Load() > 0 })
	if writes.Load() != 0 {
		t.Fatal("worker POST bypassed native policy")
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
	t.Logf("real WebView2 parent=%x child=%x: DOM interaction, navigation/history, shared cookie, exact-page CDP, worker network deny and manual resume passed", ui.hwnd, page.(*nativePage).container)
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
