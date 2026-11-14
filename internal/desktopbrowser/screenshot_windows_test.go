//go:build windows

package desktopbrowser

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestHiddenNativeScreenshotRestoresSurfaceAndConnection(t *testing.T) {
	if os.Getenv("UC_NATIVE_BROWSER_PROBE") != "1" {
		t.Skip("set UC_NATIVE_BROWSER_PROBE=1 for hidden WebView2 integration")
	}
	t.Setenv("UC_NATIVE_BROWSER_VISIBLE", "0")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
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
	p := page.(*nativePage)
	var browserPID uint32
	if err := ui.dispatcher.Invoke(ctx, func() error { return comCall(p.core, 37, uintptr(unsafe.Pointer(&browserPID))) }); err != nil {
		t.Fatal(err)
	}
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, browserPID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer windows.CloseHandle(process)
		if err := host.Close(context.Background()); err != nil {
			t.Error(err)
		}
		// WebView2 releases profile metrics asynchronously after controller
		// Close; wait for this exact isolated-profile process before TempDir.
		if status, err := windows.WaitForSingleObject(process, 10000); err != nil || status != windows.WAIT_OBJECT_0 {
			t.Errorf("isolated browser process did not exit: status=%d err=%v", status, err)
		}
	})
	open := func() *pageConnection {
		t.Helper()
		connection, err := page.OpenCDP(ctx, CDPOptions{Dispatch: func(ctx context.Context, send func() error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return send()
		}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = connection.Close() })
		return connection.(*pageConnection)
	}
	regionKind := func() uintptr {
		region, _, _ := gdi32.NewProc("CreateRectRgn").Call(0, 0, 1, 1)
		defer gdi32.NewProc("DeleteObject").Call(region)
		kind, _, _ := user32.NewProc("GetWindowRgn").Call(p.container, region)
		return kind
	}
	begin := func(c *pageConnection, id int64) {
		t.Helper()
		if err := ui.dispatcher.Invoke(ctx, func() error {
			before, _, _ := user32.NewProc("GetFocus").Call()
			if _, err := c.beginHiddenCaptureOnUI(id); err != nil {
				return err
			}
			after, _, _ := user32.NewProc("GetFocus").Call()
			if before != after || !getBool(p.controller, controllerGetIsVisible) || regionKind() != 1 {
				return errors.New("background screenshot changed focus or lacked an empty native region")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	assertHidden := func() {
		t.Helper()
		if err := ui.dispatcher.Invoke(ctx, func() error {
			style, _, _ := user32.NewProc("GetWindowLongW").Call(p.container, ^uintptr(15))
			if p.hiddenCapture != nil || getBool(p.controller, controllerGetIsVisible) || style&0x10000000 != 0 || regionKind() != 0 {
				return errors.New("background screenshot did not restore controller, container and region")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	c := open()
	begin(c, 1)
	// Simulate a native capture that never calls back. Its bounded refusal must
	// restore visibility and leave the real CDP/DOM connection usable.
	var refused struct {
		ID    int64 `json:"id"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := c.ReadJSON(&refused); err != nil || refused.ID != 1 || !strings.Contains(refused.Error.Message, "browser_snapshot") {
		t.Fatalf("hidden screenshot timeout=%+v err=%v", refused, err)
	}
	assertHidden()
	cdp := &probeCDP{t: t, conn: c, session: p.id, id: 1}
	if got := cdp.evaluate("1+1"); got != "2" {
		t.Fatalf("DOM after screenshot refusal=%s", got)
	}
	begin(c, 3)
	if err := page.SetSurface(ctx, Bounds{}, false); err != nil {
		t.Fatal(err)
	}
	if err := ui.dispatcher.Invoke(ctx, func() error {
		if p.hiddenCapture == nil {
			return errors.New("unchanged hidden surface cancelled capture")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := page.SetSurface(ctx, Bounds{Width: 800, Height: 600}, true); err != nil {
		t.Fatal(err)
	}
	if err := ui.dispatcher.Invoke(ctx, func() error {
		if p.hiddenCapture != nil || !getBool(p.controller, controllerGetIsVisible) || regionKind() != 0 {
			return errors.New("surface update did not restore the requested visible tab")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := page.SetSurface(ctx, Bounds{}, false); err != nil {
		t.Fatal(err)
	}
	begin(c, 4)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	assertHidden()
	c = open()
	lease, err := host.AcquireControl(ctx, p.id)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.InstallNetworkPolicy(ctx, NetworkPolicy{Done: lease.Done(), ReviewRequest: func(ctx context.Context, _ NetworkRequest, send func(context.Context) error) error { return send(ctx) }}); err != nil {
		t.Fatal(err)
	}
	if err := ui.dispatcher.Invoke(ctx, func() error {
		enabled, _, _ := user32.NewProc("IsWindowEnabled").Call(p.container)
		if !p.maskedRendering || p.surfaceVisible || regionKind() != 1 || enabled == 0 {
			return errors.New("hidden Agent lease did not preserve isolated rendering and native human input")
		}
		p.updateAgentCursor("Input.dispatchMouseEvent", []byte(`{"x":10,"y":10}`))
		style, _, _ := user32.NewProc("GetWindowLongW").Call(p.cursor, ^uintptr(15))
		if style&0x10000000 != 0 {
			return errors.New("background Agent cursor became visible")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := page.SetSurface(ctx, Bounds{Width: 800, Height: 600}, true); err != nil {
		t.Fatal(err)
	}
	if err := ui.dispatcher.Invoke(ctx, func() error {
		if p.maskedRendering || !p.surfaceVisible || regionKind() != 0 {
			return errors.New("selecting Agent tab retained hidden rendering mask")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := page.SetSurface(ctx, Bounds{}, false); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(ctx); err != nil {
		t.Fatal(err)
	}
	assertHidden()
	begin(c, 5)
	if err := page.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ui.dispatcher.Invoke(ctx, func() error {
		if p.hiddenCapture != nil {
			return errors.New("page close retained screenshot state")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
