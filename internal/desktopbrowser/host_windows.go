//go:build windows

package desktopbrowser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	"github.com/google/uuid"
	"github.com/wailsapp/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

// Host owns the native pages of one persistent browser session. All COM objects
// and native containers are accessed exclusively through its UI dispatcher.
type Host struct {
	options    HostOptions
	profileID  string
	env        *comObject
	mu         sync.Mutex
	tabs       map[string]*nativePage
	closed     bool
	events     []Event
	wake       chan struct{}
	finished   chan struct{}
	finishOnce sync.Once
	lease      *ControlLease
	pending    map[*nativeRequest]struct{}
}

type nativePage struct {
	host             *Host
	id               string
	container        uintptr
	cursor           uintptr
	cursorError      error
	cursorPoint      nativePoint
	emulatedWidth    float64
	controller, core *comObject
	handlers         []*comHandler
	mu               sync.RWMutex
	state            State
	connections      map[*pageConnection]struct{}
	closed           bool
	networkCapable   bool
}

func NewHost(ctx context.Context, options HostOptions) (*Host, error) {
	if options.Dispatcher == nil || options.ParentHWND == 0 {
		return nil, errors.New("native browser requires parent HWND and UI dispatcher")
	}
	if windows.GetCurrentThreadId() == options.Dispatcher.thread {
		return nil, errors.New("NewHost must be awaited outside the UI thread")
	}
	if !filepath.IsAbs(options.UserDataDir) || strings.TrimSpace(options.UserDataDir) != options.UserDataDir {
		return nil, errors.New("browser user data directory must be an absolute path without surrounding whitespace")
	}
	if err := os.MkdirAll(options.UserDataDir, 0700); err != nil {
		return nil, err
	}
	profileHash := sha256.Sum256([]byte(filepath.Clean(options.UserDataDir)))
	h := &Host{options: options, profileID: hex.EncodeToString(profileHash[:]), tabs: map[string]*nativePage{}, wake: make(chan struct{}, 1), finished: make(chan struct{}), pending: map[*nativeRequest]struct{}{}}
	go h.dispatchEvents()
	ready := make(chan error, 1)
	err := options.Dispatcher.Invoke(ctx, func() error {
		var pid uint32
		thread, _, _ := getWindowThreadProcessID.Call(options.ParentHWND, uintptr(unsafe.Pointer(&pid)))
		if uint32(thread) != options.Dispatcher.thread || pid != windows.GetCurrentProcessId() {
			return errors.New("native browser parent is not an owned window on the UI thread")
		}
		return webviewloader.CreateCoreWebView2EnvironmentWithOptions(&environmentCallback{host: h, ready: ready}, webviewloader.WithUserDataFolder(options.UserDataDir))
	})
	if err != nil {
		h.finish()
		return nil, err
	}
	select {
	case err = <-ready:
		if err != nil {
			h.finish()
			return nil, err
		}
		return h, nil
	case <-ctx.Done():
		_ = h.Close(context.Background())
		return nil, ctx.Err()
	}
}

type environmentCallback struct {
	host  *Host
	ready chan error
}

func (c *environmentCallback) EnvironmentCompleted(hr webviewloader.HRESULT, env *webviewloader.ICoreWebView2Environment) webviewloader.HRESULT {
	err := hresult("create browser environment", uintptr(hr))
	if err == nil && env == nil {
		err = errors.New("WebView2 returned no environment")
	}
	c.host.mu.Lock()
	if err == nil && !c.host.closed {
		c.host.env = (*comObject)(unsafe.Pointer(env))
		c.host.env.addRef()
	} else if err == nil {
		err = ErrClosed
	}
	c.host.mu.Unlock()
	c.ready <- err
	return 0
}

func (h *Host) CreateTab(ctx context.Context, options TabOptions) (Page, error) {
	if options.ID == "" {
		options.ID = uuid.NewString()
	}
	if err := validURL(options.URL); err != nil {
		return nil, err
	}
	page := &nativePage{host: h, id: options.ID, state: State{ID: options.ID, URL: "about:blank"}, connections: map[*pageConnection]struct{}{}}
	ready := make(chan error, 1)
	err := h.options.Dispatcher.Invoke(ctx, func() error {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.closed || h.env == nil {
			return ErrClosed
		}
		if _, exists := h.tabs[page.id]; exists {
			return errors.New("native browser tab ID already exists")
		}
		class := windows.StringToUTF16Ptr("STATIC")
		// A native child owns input, clipping, position and z-order. Only this
		// container is disabled during agent control, never the Wails window.
		container, _, err := createWindowEx.Call(0, uintptr(unsafe.Pointer(class)), 0, 0x40000000|0x04000000|0x02000000, 0, 0, 1, 1, h.options.ParentHWND, 0, 0, 0)
		if container == 0 {
			return fmt.Errorf("create browser child container: %w", err)
		}
		page.container = container
		h.tabs[page.id] = page
		completed := newHandler("{6C4819F3-C9B7-4260-8127-C9F5BDE7F68C}", func(hr, controller uintptr) uintptr {
			err := hresult("create browser controller", hr)
			if err == nil && controller == 0 {
				err = errors.New("WebView2 returned no controller")
			}
			if err == nil {
				page.mu.RLock()
				closed := page.closed
				page.mu.RUnlock()
				if closed {
					_ = comCall((*comObject)(unsafe.Pointer(controller)), controllerClose)
					err = ErrClosed
				} else {
					page.controller = (*comObject)(unsafe.Pointer(controller))
					page.controller.addRef()
					err = comCall(page.controller, controllerGetCore, uintptr(unsafe.Pointer(&page.core)))
					if err == nil {
						err = page.installEvents()
					}
					if err == nil {
						err = page.installNetwork()
					}
					if err == nil {
						h.mu.Lock()
						leased := h.lease != nil
						h.mu.Unlock()
						if leased {
							if !page.networkCapable {
								err = errors.New("native browser agent control requires WebView2 request-source-kinds support")
							}
						}
					}
					if err == nil {
						err = comCall(page.controller, controllerPutIsVisible, 0)
					}
					if err == nil && options.URL != "" {
						err = page.navigateOnUI(options.URL)
					}
				}
			}
			if err != nil {
				page.closeOnUI()
			}
			ready <- err
			return 0
		})
		err = comCall(h.env, 3, container, completed.pointer())
		completed.release()
		if err != nil {
			delete(h.tabs, page.id)
			destroyWindow.Call(container)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	select {
	case err = <-ready:
		if err != nil {
			return nil, err
		}
		h.emit(Event{Kind: "state", State: page.State()})
		return page, nil
	case <-ctx.Done():
		_ = page.Close(context.Background())
		return nil, ctx.Err()
	}
}

func (h *Host) Tab(id string) (Page, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p, ok := h.tabs[id]
	return p, ok
}

func (h *Host) Close(ctx context.Context) error {
	return h.options.Dispatcher.Invoke(ctx, func() error {
		h.mu.Lock()
		if h.closed {
			h.mu.Unlock()
			return nil
		}
		h.closed = true
		if h.lease != nil {
			h.lease.Cancel()
		}
		pages := make([]*nativePage, 0, len(h.tabs))
		for _, p := range h.tabs {
			pages = append(pages, p)
		}
		h.mu.Unlock()
		for _, p := range pages {
			p.closeOnUI()
		}
		if h.env != nil {
			h.env.release()
			h.env = nil
		}
		h.finish()
		return nil
	})
}

func (h *Host) emit(event Event) {
	h.mu.Lock()
	h.events = append(h.events, event)
	h.mu.Unlock()
	select {
	case h.wake <- struct{}{}:
	default:
	}
}
func (h *Host) finish() {
	h.finishOnce.Do(func() { close(h.finished) })
}
func (h *Host) dispatchEvents() {
	for {
		select {
		case <-h.wake:
		case <-h.finished:
		}
		h.mu.Lock()
		events := h.events
		h.events = nil
		h.mu.Unlock()
		if h.options.OnEvent != nil {
			for _, event := range events {
				h.options.OnEvent(event)
			}
		}
		select {
		case <-h.finished:
			return
		default:
		}
	}
}

func (p *nativePage) ID() string        { return p.id }
func (p *nativePage) ProfileID() string { return p.host.profileID }
func (p *nativePage) State() State      { p.mu.RLock(); defer p.mu.RUnlock(); return p.state }
func (p *nativePage) withUI(ctx context.Context, fn func() error) error {
	return p.host.options.Dispatcher.Invoke(ctx, func() error {
		p.mu.RLock()
		closed := p.closed
		p.mu.RUnlock()
		if closed || p.core == nil {
			return ErrClosed
		}
		return fn()
	})
}
func (p *nativePage) Navigate(ctx context.Context, url string) error {
	if err := validURL(url); err != nil {
		return err
	}
	return p.withUI(ctx, func() error { return p.navigateOnUI(url) })
}
func (p *nativePage) navigateOnUI(url string) error {
	value, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	return comCall(p.core, coreNavigate, uintptr(unsafe.Pointer(value)))
}
func (p *nativePage) Back(ctx context.Context) error {
	return p.withUI(ctx, func() error { return comCall(p.core, coreGoBack) })
}
func (p *nativePage) Forward(ctx context.Context) error {
	return p.withUI(ctx, func() error { return comCall(p.core, coreGoForward) })
}
func (p *nativePage) Reload(ctx context.Context) error {
	return p.withUI(ctx, func() error { return comCall(p.core, coreReload) })
}
func (p *nativePage) Stop(ctx context.Context) error {
	return p.withUI(ctx, func() error { return comCall(p.core, coreStop) })
}
func (p *nativePage) Focus(ctx context.Context) error {
	return p.withUI(ctx, func() error { return comCall(p.controller, controllerMoveFocus, 0) })
}
func (p *nativePage) SetUserInputEnabled(ctx context.Context, enabled bool) error {
	return p.withUI(ctx, func() error {
		var value uintptr
		if enabled {
			value = 1
		}
		user32.NewProc("EnableWindow").Call(p.container, value)
		return nil
	})
}
func (p *nativePage) SetSurface(ctx context.Context, bounds Bounds, visible bool) error {
	if bounds.X < 0 || bounds.Y < 0 || bounds.Width < 0 || bounds.Height < 0 {
		return errors.New("invalid native browser bounds")
	}
	return p.withUI(ctx, func() error {
		var parent nativeRect
		if ok, _, _ := getClientRect.Call(p.host.options.ParentHWND, uintptr(unsafe.Pointer(&parent))); ok == 0 {
			return ErrClosed
		}
		// Renderer layout can race with WM_SIZE/DPI changes. Keep the native
		// child within the current client area until the next surface update.
		if bounds.X > parent.Right {
			bounds.X = parent.Right
		}
		if bounds.Y > parent.Bottom {
			bounds.Y = parent.Bottom
		}
		if int64(bounds.X)+int64(bounds.Width) > int64(parent.Right) {
			bounds.Width = parent.Right - bounds.X
		}
		if int64(bounds.Y)+int64(bounds.Height) > int64(parent.Bottom) {
			bounds.Height = parent.Bottom - bounds.Y
		}
		if ok, _, err := moveWindow.Call(p.container, uintptr(bounds.X), uintptr(bounds.Y), uintptr(bounds.Width), uintptr(bounds.Height), 1); ok == 0 {
			return err
		}
		rect := nativeRect{Right: bounds.Width, Bottom: bounds.Height}
		if err := comCall(p.controller, controllerPutBounds, uintptr(unsafe.Pointer(&rect))); err != nil {
			return err
		}
		var value uintptr
		if visible && bounds.Width > 0 && bounds.Height > 0 {
			value = 1
			showWindow.Call(p.container, 5)
			setWindowPos.Call(p.container, 0, 0, 0, 0, 0, 0x1|0x2|0x10)
		} else {
			showWindow.Call(p.container, 0)
		}
		if err := comCall(p.controller, controllerPutIsVisible, value); err != nil {
			return err
		}
		p.positionCursor()
		return comCall(p.controller, controllerNotifyPosition)
	})
}
func (p *nativePage) Close(ctx context.Context) error {
	return p.host.options.Dispatcher.Invoke(ctx, func() error { p.closeOnUI(); return nil })
}
func (p *nativePage) closeOnUI() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.state.Closed = true
	connections := make([]*pageConnection, 0, len(p.connections))
	for c := range p.connections {
		connections = append(connections, c)
	}
	p.mu.Unlock()
	for pending := range p.host.pending {
		if pending.page == p {
			pending.finishOnUI(false)
		}
	}
	for _, c := range connections {
		c.closeOnUI()
	}
	if p.controller != nil {
		_ = comCall(p.controller, controllerClose)
	}
	if p.core != nil {
		p.core.release()
		p.core = nil
	}
	if p.controller != nil {
		p.controller.release()
		p.controller = nil
	}
	for _, handler := range p.handlers {
		handler.release()
	}
	p.handlers = nil
	if p.container != 0 {
		if p.cursor != 0 {
			destroyWindow.Call(p.cursor)
			p.cursor = 0
		}
		destroyWindow.Call(p.container)
		p.container = 0
	}
	p.host.mu.Lock()
	delete(p.host.tabs, p.id)
	p.host.mu.Unlock()
	p.host.emit(Event{Kind: "closed", State: p.State()})
}

func (p *nativePage) refreshState() {
	if p.core == nil {
		return
	}
	url, _ := getString(p.core, coreSource)
	title, _ := getString(p.core, coreTitle)
	back, forward := getBool(p.core, coreCanGoBack), getBool(p.core, coreCanGoForward)
	p.mu.Lock()
	p.state.URL = url
	p.state.Title = title
	p.state.CanGoBack = back
	p.state.CanGoForward = forward
	p.mu.Unlock()
	p.host.emit(Event{Kind: "state", State: p.State()})
}

func (p *nativePage) installEvents() error {
	events := []struct {
		slot int
		iid  string
		fn   func(uintptr, uintptr) uintptr
	}{
		{coreNavigationStarting, "{9ADBE429-F36D-432B-9DDC-F8881FBD76E3}", func(_, args uintptr) uintptr {
			p.mu.Lock()
			p.state.Loading = true
			p.state.Error = ""
			p.mu.Unlock()
			p.refreshState()
			return 0
		}},
		{coreNavigationCompleted, "{D33A35BF-1C49-4F98-93AB-006E0533FE1C}", func(_, args uintptr) uintptr {
			p.mu.Lock()
			p.state.Loading = false
			p.mu.Unlock()
			p.refreshState()
			return 0
		}},
		{coreSourceChanged, "{3C067F9F-5388-4772-8B48-79F7EF1AB37C}", func(_, args uintptr) uintptr { p.refreshState(); return 0 }},
		{coreHistoryChanged, "{C8E8AC3B-3D5F-4CA6-9A4C-6D0DB19DAF98}", func(_, args uintptr) uintptr { p.refreshState(); return 0 }},
		{coreTitleChanged, "{F5F2B923-953E-4042-9F95-F3A118E1AFD4}", func(_, args uintptr) uintptr { p.refreshState(); return 0 }},
		{coreFullscreenChanged, "{E45D98B1-AFEF-45BE-8BAF-6C7728867F73}", func(_, args uintptr) uintptr {
			p.host.emit(Event{Kind: "fullscreen", State: p.State(), Fullscreen: getBool(p.core, coreIsFullscreen)})
			return 0
		}},
		{coreNewWindowRequested, "{D4C185FE-C81C-4989-97AF-2D3FA7AB5651}", func(_, args uintptr) uintptr {
			a := (*comObject)(unsafe.Pointer(args))
			_ = comCall(a, 6, 1)
			url, _ := getString(a, 3)
			p.host.emit(Event{Kind: "popup", State: p.State(), URL: url})
			return 0
		}},
		{coreWindowClose, "{5C19E9E0-092F-486B-AFFA-CA8231913039}", func(_, args uintptr) uintptr { _ = p.host.options.Dispatcher.Post(func() { p.closeOnUI() }); return 0 }},
		{coreProcessFailed, "{79E0AEA4-990B-42D9-AA1D-0FCC2E5BC7F1}", func(_, args uintptr) uintptr {
			p.mu.Lock()
			p.state.Error = "browser process failed"
			p.mu.Unlock()
			p.host.emit(Event{Kind: "state", State: p.State()})
			_ = p.host.options.Dispatcher.Post(func() { p.closeOnUI() })
			return 0
		}},
	}
	for _, event := range events {
		handler := newHandler(event.iid, event.fn)
		var token int64
		if err := comCall(p.core, event.slot, handler.pointer(), uintptr(unsafe.Pointer(&token))); err != nil {
			handler.release()
			return err
		}
		p.handlers = append(p.handlers, handler)
	}
	return nil
}

func validURL(raw string) error {
	if strings.ContainsRune(raw, 0) {
		return errors.New("invalid browser URL")
	}
	if raw == "" || raw == "about:blank" {
		return nil
	}
	if strings.TrimSpace(raw) != raw {
		return errors.New("browser URL has surrounding whitespace")
	}
	if !strings.HasPrefix(strings.ToLower(raw), "https://") && !strings.HasPrefix(strings.ToLower(raw), "http://") {
		return errors.New("browser URL must use http or https")
	}
	return nil
}
