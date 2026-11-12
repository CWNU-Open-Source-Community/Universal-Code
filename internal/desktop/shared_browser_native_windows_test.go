//go:build windows

package desktop

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
	"unsafe"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/desktopbrowser"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/store"
	"golang.org/x/sys/windows"
)

type sharedBrowserNativeUI struct {
	dispatcher *desktopbrowser.Dispatcher
	hwnd       uintptr
	done       chan struct{}
}
type sharedBrowserNativeMessage struct {
	HWND           uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	X, Y           int32
	Private        uint32
}

// This hidden STATIC window owns real WebView2 child controllers. It has no
// WS_VISIBLE style and never calls ShowWindow, SendInput or SetCursorPos.
func startSharedBrowserNativeUI(t *testing.T) *sharedBrowserNativeUI {
	t.Helper()
	ready := make(chan *sharedBrowserNativeUI, 1)
	failed := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		dispatcher, err := desktopbrowser.NewDispatcher()
		if err != nil {
			failed <- err
			return
		}
		user32 := windows.NewLazySystemDLL("user32.dll")
		class := windows.StringToUTF16Ptr("STATIC")
		hwnd, _, err := user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(class)), 0, 0x00cf0000, 0, 0, 1100, 800, 0, 0, 0, 0)
		if hwnd == 0 {
			_ = dispatcher.Close()
			failed <- err
			return
		}
		ui := &sharedBrowserNativeUI{dispatcher: dispatcher, hwnd: hwnd, done: make(chan struct{})}
		ready <- ui
		var message sharedBrowserNativeMessage
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
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := ui.dispatcher.Invoke(ctx, func() error {
				user32 := windows.NewLazySystemDLL("user32.dll")
				user32.NewProc("DestroyWindow").Call(ui.hwnd)
				_ = ui.dispatcher.Close()
				user32.NewProc("PostQuitMessage").Call(0)
				return nil
			}); err != nil {
				t.Error(err)
			}
			select {
			case <-ui.done:
			case <-ctx.Done():
				t.Error("hidden browser UI did not exit")
			}
		})
		return ui
	case <-time.After(10 * time.Second):
		t.Fatal("hidden native browser UI did not start")
	}
	return nil
}

type sharedBrowserNativeObserver struct {
	t          *testing.T
	ctx        context.Context
	page       desktopbrowser.Page
	connection desktopbrowser.JSONConnection
	id         int64
}

func observeSharedBrowserNativePage(t *testing.T, ctx context.Context, page desktopbrowser.Page) *sharedBrowserNativeObserver {
	t.Helper()
	connection, err := page.OpenCDP(ctx, desktopbrowser.CDPOptions{Dispatch: func(ctx context.Context, send func() error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return send()
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	go func() { <-ctx.Done(); _ = connection.Close() }()
	return &sharedBrowserNativeObserver{t: t, ctx: ctx, page: page, connection: connection}
}
func (o *sharedBrowserNativeObserver) evaluate(expression string) string {
	o.t.Helper()
	o.id++
	_ = o.connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := o.connection.WriteJSON(map[string]any{"id": o.id, "sessionId": o.page.ID(), "method": "Runtime.evaluate", "params": map[string]any{"expression": expression, "returnByValue": true, "awaitPromise": true}}); err != nil {
		o.t.Fatal(err)
	}
	for {
		var response struct {
			ID     int64 `json:"id"`
			Error  any   `json:"error"`
			Result struct {
				Result struct {
					Value any `json:"value"`
				} `json:"result"`
				ExceptionDetails any `json:"exceptionDetails"`
			} `json:"result"`
		}
		if err := o.connection.ReadJSON(&response); err != nil {
			o.t.Fatal(err)
		}
		if response.ID != o.id {
			continue
		}
		if response.Error != nil || response.Result.ExceptionDetails != nil {
			o.t.Fatalf("native observation failed: %+v", response)
		}
		return fmt.Sprint(response.Result.Result.Value)
	}
}

func waitSharedBrowserNative(t *testing.T, ctx context.Context, description string, ready func() bool) {
	t.Helper()
	for !ready() {
		select {
		case <-ctx.Done():
			t.Fatalf("%s: %v", description, ctx.Err())
		case <-time.After(15 * time.Millisecond):
		}
	}
}

// This test validates native/service/runtime composition with a real durable
// Run and thread. CheckAuthority is a bounded test callback; no model is called,
// and the test does not claim to exercise the full model/tool approval pipeline.
func TestSharedBrowserNativeServiceAndAgentShareAuthenticatedPage(t *testing.T) {
	if os.Getenv("UC_NATIVE_BROWSER_PROBE") != "1" {
		t.Skip("set UC_NATIVE_BROWSER_PROBE=1 for hidden installed-WebView2 integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	ui := startSharedBrowserNativeUI(t)
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "native-shared-browser.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	workspaceID := "workspace-native-shared-browser"
	if err = st.SaveWorkspace(ctx, store.WorkspaceRecord{ID: workspaceID, Name: "Native shared browser", RootPath: home, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	_, run, err := application.NewRunService(st).Create(ctx, application.CreateRunRequest{Goal: "share an authenticated native page", Profile: "code", Surface: "code", Phase: "deliver", WorkspaceID: workspaceID, Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := st.GetThreadByRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "shared-user", Path: "/", HttpOnly: true, MaxAge: 3600})
			http.Redirect(w, r, "/protected", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/popup" {
			fmt.Fprint(w, `<!doctype html><title>Shared login popup</title><script>window.opener.postMessage('oauth-complete','*')</script>`)
			return
		}
		cookie, err := r.Cookie("session")
		if err != nil || cookie.Value != "shared-user" {
			fmt.Fprint(w, `<!doctype html><title>Signed out</title><p>Login required</p>`)
			return
		}
		fmt.Fprint(w, `<!doctype html><title>Authenticated shared page</title><h1>Signed in as shared-user</h1><button id="agent" onclick="document.getElementById('agent-count').textContent=String(++window.agentClicks)">Agent increment</button><span id="agent-count">0</span><button id="operator" onclick="document.getElementById('operator-count').textContent=String(++window.operatorClicks)">Operator increment</button><span id="operator-count">0</span><script>window.agentClicks=0;window.operatorClicks=0;window.inputLog=[];for(const k of ['mousedown','mouseup','click'])window.addEventListener(k,e=>window.inputLog.push([k,e.clientX,e.clientY,e.target.id]));window.addEventListener('message',e=>window.popupMessage=e.data)</script>`)
	}))
	defer server.Close()
	options := SharedBrowserOptions{HomePath: home, CreateHost: func(ctx context.Context, profile string, onEvent func(desktopbrowser.Event)) (*desktopbrowser.Host, error) {
		return desktopbrowser.NewHost(ctx, desktopbrowser.HostOptions{ParentHWND: ui.hwnd, Dispatcher: ui.dispatcher, UserDataDir: profile, OnEvent: onEvent})
	}}
	service, err := newSharedBrowserService(st, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		if err := service.Close(closeCtx); err != nil {
			t.Error(err)
		}
	})
	bridge := &DesktopBridge{sharedBrowser: service, contextProvider: func() context.Context { return ctx }}
	view, err := bridge.OpenSharedBrowser(SharedBrowserOpenRequest{ProtocolVersion: SharedBrowserProtocolVersion, WorkspaceID: workspaceID})
	if err != nil {
		t.Fatal(err)
	}
	draftID, originalID := view.SessionID, view.ActiveTabID
	session, err := service.session(draftID)
	if err != nil {
		t.Fatal(err)
	}
	page, ok := session.host.Tab(originalID)
	if !ok {
		t.Fatal("service did not create a native page")
	}
	profileID := page.ProfileID()
	command := func(sessionID, kind, tabID, url string) SharedBrowserState {
		t.Helper()
		result, err := bridge.SharedBrowserCommand(SharedBrowserCommandRequest{ProtocolVersion: SharedBrowserProtocolVersion, SessionID: sessionID, Command: kind, TabID: tabID, URL: url, OperationKey: idgen.New("native-operation")})
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		return result
	}
	get := func(sessionID string) SharedBrowserState {
		t.Helper()
		result, err := bridge.GetSharedBrowser(SharedBrowserGetRequest{ProtocolVersion: SharedBrowserProtocolVersion, SessionID: sessionID})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	command(draftID, "navigate", originalID, server.URL+"/login")
	waitSharedBrowserNative(t, ctx, "login redirect", func() bool { return page.State().Title == "Authenticated shared page" && !page.State().Loading })
	surface := SharedBrowserSurfaceRequest{ProtocolVersion: SharedBrowserProtocolVersion, SessionID: draftID, TabID: originalID, Bounds: SharedBrowserBounds{X: 10, Y: 20, Width: 800, Height: 600}, DeviceScaleFactor: 1, Visible: true, LayoutRevision: 10}
	if result, err := bridge.SetSharedBrowserSurface(surface); err != nil || !result.Visible {
		t.Fatalf("native surface: %+v %v", result, err)
	}
	surface.LayoutRevision = 9
	surface.Visible = false
	if result, err := bridge.SetSharedBrowserSurface(surface); err != nil || result.LayoutRevision != 10 || !result.Visible {
		t.Fatalf("stale surface changed native visibility: %+v %v", result, err)
	}
	view, err = bridge.OpenSharedBrowser(SharedBrowserOpenRequest{ProtocolVersion: SharedBrowserProtocolVersion, WorkspaceID: workspaceID, ThreadID: thread.ID, RunID: run.ID, AdoptSessionID: draftID})
	if err != nil {
		t.Fatal(err)
	}
	if view.SessionID != draftID || view.ActiveTabID != originalID || view.RunID != run.ID || page.ProfileID() != profileID {
		t.Fatalf("adoption replaced native identity: %+v", view)
	}
	observer := observeSharedBrowserNativePage(t, ctx, page)
	if got := observer.evaluate(`[typeof window.go,typeof window.runtime,typeof window.wails,typeof window.external?.invoke].join(',')`); got != "undefined,undefined,undefined,undefined" {
		t.Fatalf("guest page received Wails bindings: %s", got)
	}
	if got := observer.evaluate(`document.cookie`); strings.Contains(got, "session=") {
		t.Fatal("fixture's authenticated session cookie was not HttpOnly")
	}
	authority := browserruntime.AgentBrowserAuthority{RunID: run.ID, ManagerBootID: "native-test-boot", SessionID: "native-test-agent-session", Generation: 1, PermissionSnapshotID: "native-test-permission", PermissionRevision: 1, PermissionActivation: 1, RunAuthorizationFence: 1, PermissionMode: "ask"}
	var authorityChecks atomic.Int32
	request := browserruntime.AgentBrowserStartRequest{HomePath: home, Authority: authority, Product: browserruntime.BrowserProduct("edge"), RuntimeDeadline: time.Now().Add(45 * time.Second), NetworkMode: browserruntime.AgentBrowserNetworkRestricted, ScopedOrigins: []string{server.URL}, CheckAuthority: func(ctx context.Context, received browserruntime.AgentBrowserAuthority) error {
		authorityChecks.Add(1)
		if err := ctx.Err(); err != nil {
			return err
		}
		if received != authority {
			return errors.New("test authority changed")
		}
		_, err := st.GetRun(ctx, received.RunID)
		return err
	}, ReviewNetworkWrite: func(ctx context.Context, request browserruntime.AgentBrowserNetworkRequest, dispatch func() error) error {
		return dispatch()
	}}
	attached, err := service.attachPage(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if attached.PageID != originalID || attached.SessionID != originalID {
		t.Fatal("agent attached a new native page instead of the logged-in page")
	}
	agent, err := browserruntime.AttachAgentBrowser(ctx, request, attached)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, _ = agent.Close(closeCtx)
	})
	snapshot, err := agent.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CanonicalURL != server.URL+"/protected" || !strings.Contains(snapshot.Text, "Signed in as shared-user") {
		t.Fatalf("agent did not see real authenticated page: %+v", snapshot)
	}
	var button browserruntime.AgentBrowserElement
	for _, element := range snapshot.Elements {
		if element.Name == "Agent increment" {
			button = element
			break
		}
	}
	if button.Ref == "" {
		t.Fatal("authenticated page button absent from agent snapshot")
	}
	interaction, err := agent.Click(ctx, snapshot.SnapshotID, button.Ref)
	if err != nil || !interaction.Dispatched {
		t.Fatalf("real DOM click: %+v %v", interaction, err)
	}
	clickCtx, clickCancel := context.WithTimeout(ctx, 3*time.Second)
	for observer.evaluate(`document.getElementById('agent-count').textContent`) != "1" && clickCtx.Err() == nil {
		time.Sleep(20 * time.Millisecond)
	}
	if clickCtx.Err() != nil {
		t.Fatalf("agent input did not click: %s", observer.evaluate(`JSON.stringify({log:inputLog,rect:document.getElementById('agent').getBoundingClientRect().toJSON(),dpr:devicePixelRatio,width:innerWidth,height:innerHeight})`))
	}
	clickCancel()
	// Exercise the operator's already-open document directly, without an OS
	// mouse or model call, while the agent runtime and lease remain alive.
	if got := observer.evaluate(`document.getElementById('operator').click();document.getElementById('operator-count').textContent`); got != "1" {
		t.Fatalf("shared operator document stopped responding: %s", got)
	}
	if agent.Status().State == "closed" {
		t.Fatal("operator page interaction cancelled the agent")
	}
	var inputEnabled uintptr
	_ = ui.dispatcher.Invoke(ctx, func() error {
		user32 := windows.NewLazySystemDLL("user32.dll")
		container, _, _ := user32.NewProc("GetWindow").Call(ui.hwnd, 5)
		inputEnabled, _, _ = user32.NewProc("IsWindowEnabled").Call(container)
		return nil
	})
	if inputEnabled == 0 {
		t.Fatal("agent attachment disabled native user input")
	}
	if state := get(draftID); state.ControlOwner != "agent" || state.RunID != run.ID {
		t.Fatalf("agent lease lost durable shared-page scope: %+v", state)
	}
	if got := observer.evaluate(`window.loginWindow=window.open('/popup','login-popup');String(!!window.loginWindow)`); got != "true" {
		t.Fatal("native popup lost WindowProxy")
	}
	waitSharedBrowserNative(t, ctx, "service popup registration", func() bool { state := get(draftID); return len(state.Tabs) == 2 && state.ActiveTabID != originalID })
	view = get(draftID)
	popupID := view.ActiveTabID
	popupPage, ok := session.host.Tab(popupID)
	if !ok {
		t.Fatal("service popup is not its existing native page")
	}
	waitSharedBrowserNative(t, ctx, "popup opener response", func() bool { return observer.evaluate(`String(window.popupMessage)`) == "oauth-complete" })
	if popupPage.ProfileID() != profileID {
		t.Fatal("login popup left the authenticated native profile")
	}
	command(draftID, "select_tab", originalID, "")
	created := command(draftID, "new_tab", "", server.URL+"/protected")
	secondID := created.ActiveTabID
	second, ok := session.host.Tab(secondID)
	if !ok {
		t.Fatal("new tab is not native")
	}
	waitSharedBrowserNative(t, ctx, "new tab shared login", func() bool { return second.State().Title == "Authenticated shared page" })
	backgroundSnapshot, err := agent.Snapshot(ctx)
	if err != nil {
		t.Fatalf("background stable tab snapshot: %v", err)
	}
	var backgroundButton browserruntime.AgentBrowserElement
	for _, element := range backgroundSnapshot.Elements {
		if element.Name == "Agent increment" {
			backgroundButton = element
			break
		}
	}
	if backgroundButton.Ref == "" {
		t.Fatal("background native page lost its DOM layout")
	}
	if interaction, err = agent.Click(ctx, backgroundSnapshot.SnapshotID, backgroundButton.Ref); err != nil || !interaction.Dispatched {
		t.Fatalf("background stable tab click: %+v %v", interaction, err)
	}
	backgroundCtx, backgroundCancel := context.WithTimeout(ctx, 3*time.Second)
	waitSharedBrowserNative(t, backgroundCtx, "agent click reaches its background stable tab", func() bool { return observer.evaluate(`document.getElementById('agent-count').textContent`) == "2" })
	backgroundCancel()
	command(draftID, "close_tab", secondID, "")
	if !second.State().Closed {
		t.Fatal("close_tab did not dispose native controller")
	}
	popupObserver := observeSharedBrowserNativePage(t, ctx, popupPage)
	popupObserver.evaluate(`window.close();'closing'`)
	waitSharedBrowserNative(t, ctx, "script-closed popup fallback", func() bool { state := get(draftID); return len(state.Tabs) == 1 && state.ActiveTabID == originalID })
	waitSharedBrowserNative(t, ctx, "real native navigation history", func() bool {
		history := get(draftID).History
		seenPage, seenPopup := false, false
		for _, entry := range history {
			if entry.URL == server.URL+"/protected" {
				seenPage = true
			}
			if entry.URL == server.URL+"/popup" {
				seenPopup = true
			}
			if entry.URL == "about:blank" {
				t.Fatal("about:blank was recorded as history")
			}
		}
		return seenPage && seenPopup
	})
	cleanup, err := agent.Close(ctx)
	if err != nil || !cleanup.AgentDetached || !cleanup.Complete() || cleanup.ProfileRemoved || cleanup.TreeReaped {
		t.Fatalf("agent detach claimed user's page/profile ownership: %+v %v", cleanup, err)
	}
	if page.State().Closed {
		t.Fatal("agent detach closed the user's native tab")
	}
	if state := get(draftID); state.ControlOwner != "user" {
		t.Fatalf("native agent detach did not update desktop ownership: %+v", state)
	}
	command(draftID, "navigate", originalID, server.URL+"/protected")
	waitSharedBrowserNative(t, ctx, "manual page retains login after agent detach", func() bool { return page.State().Title == "Authenticated shared page" && !page.State().Loading })
	newDraft, err := bridge.OpenSharedBrowser(SharedBrowserOpenRequest{ProtocolVersion: SharedBrowserProtocolVersion, WorkspaceID: workspaceID})
	if err != nil {
		t.Fatal(err)
	}
	newSession, err := service.session(newDraft.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	newPage, _ := newSession.host.Tab(newDraft.ActiveTabID)
	if newDraft.SessionID == draftID || newPage.ProfileID() == profileID {
		t.Fatal("fresh draft reused the adopted chat's profile")
	}
	command(newDraft.SessionID, "navigate", newDraft.ActiveTabID, server.URL+"/protected")
	waitSharedBrowserNative(t, ctx, "new draft has isolated login", func() bool { return newPage.State().Title == "Signed out" })
	if _, err = bridge.OpenSharedBrowser(SharedBrowserOpenRequest{ProtocolVersion: SharedBrowserProtocolVersion, WorkspaceID: workspaceID, ThreadID: thread.ID, AdoptSessionID: draftID}); err != nil {
		t.Fatal(err)
	}
	if current, err := bridge.OpenSharedBrowser(SharedBrowserOpenRequest{ProtocolVersion: SharedBrowserProtocolVersion, WorkspaceID: workspaceID}); err != nil || current.SessionID != newDraft.SessionID {
		t.Fatalf("repeat adoption consumed another native draft: %+v %v", current, err)
	}
	indexBytes, err := os.ReadFile(service.profileIndex())
	if err != nil {
		t.Fatal(err)
	}
	var index map[string]string
	if err = json.Unmarshal(indexBytes, &index); err != nil {
		t.Fatal(err)
	}
	if index["draft:"+workspaceID] == index["thread:"+thread.ID] || index["draft:"+workspaceID] == "" {
		t.Fatalf("profile isolation lost on disk: %#v", index)
	}
	if authorityChecks.Load() < 2 {
		t.Fatal("agent actions bypassed their authority callback")
	}
	var parentVisible uintptr
	_ = ui.dispatcher.Invoke(ctx, func() error {
		parentVisible, _, _ = windows.NewLazySystemDLL("user32.dll").NewProc("IsWindowVisible").Call(ui.hwnd)
		return nil
	})
	if parentVisible != 0 {
		t.Fatal("integration probe unexpectedly exposed a foreground-capable parent window")
	}
	t.Log("real hidden WebView2 + desktop bridge/service + durable Run: authenticated page identity, agent snapshot/click, concurrent operator document, popup/opener/history, tab lifecycle and profile adoption passed; no model called")
}
