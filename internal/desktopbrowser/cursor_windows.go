//go:build windows

package desktopbrowser

import (
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	gdi32                = windows.NewLazySystemDLL("gdi32.dll")
	cursorClass          = windows.StringToUTF16Ptr("UniversalCode.NativeBrowser.AgentCursor.v1")
	cursorClassOnce      sync.Once
	cursorClassError     error
	cursorWindowCallback = windows.NewCallback(cursorWindowProc)
)

type nativePoint struct{ X, Y int32 }

func cursorWindowProc(hwnd uintptr, message uint32, wparam, lparam uintptr) uintptr {
	if message == 0x84 {
		return ^uintptr(0)
	} // HTTRANSPARENT, never handles user input.
	value, _, _ := defWindowProc.Call(hwnd, uintptr(message), wparam, lparam)
	return value
}

func (p *nativePage) updateAgentCursor(method string, params json.RawMessage) {
	if method == "Emulation.setDeviceMetricsOverride" {
		var metrics struct {
			Width float64 `json:"width"`
		}
		if json.Unmarshal(params, &metrics) == nil {
			p.emulatedWidth = metrics.Width
		}
		return
	}
	if method == "Emulation.clearDeviceMetricsOverride" {
		p.emulatedWidth = 0
		return
	}
	if method != "Input.dispatchMouseEvent" {
		return
	}
	var input struct {
		X, Y float64
		Type string
	}
	if json.Unmarshal(params, &input) != nil || math.IsNaN(input.X) || math.IsNaN(input.Y) || math.IsInf(input.X, 0) || math.IsInf(input.Y, 0) {
		return
	}
	if p.cursor == 0 {
		cursorClassOnce.Do(func() {
			brush, _, _ := gdi32.NewProc("CreateSolidBrush").Call(0x00d5b12d)
			wc := windowClass{Size: uint32(unsafe.Sizeof(windowClass{})), WndProc: cursorWindowCallback, ClassName: cursorClass, Background: brush}
			atom, _, err := user32.NewProc("RegisterClassExW").Call(uintptr(unsafe.Pointer(&wc)))
			if atom == 0 {
				cursorClassError = fmt.Errorf("register agent cursor: %w", err)
			}
		})
		if cursorClassError != nil {
			p.cursorError = cursorClassError
			return
		}
		var err error
		// An owned layered popup also works in Go test/probe executables that
		// lack the Windows-8 manifest required for layered child windows.
		p.cursor, _, err = createWindowEx.Call(0x80000|0x20|0x80|0x08000000, uintptr(unsafe.Pointer(cursorClass)), 0, 0x80000000, 0, 0, 22, 30, p.host.options.ParentHWND, 0, 0, 0)
		if p.cursor == 0 {
			p.cursorError = fmt.Errorf("create agent cursor: %w", err)
			return
		}
		points := []nativePoint{{0, 0}, {0, 24}, {6, 18}, {11, 29}, {16, 26}, {11, 16}, {21, 16}}
		region, _, _ := gdi32.NewProc("CreatePolygonRgn").Call(uintptr(unsafe.Pointer(&points[0])), uintptr(len(points)), 1)
		if region != 0 {
			user32.NewProc("SetWindowRgn").Call(p.cursor, region, 1)
		}
		user32.NewProc("SetLayeredWindowAttributes").Call(p.cursor, 0, 240, 2)
	}
	var bounds nativeRect
	if ok, _, _ := getClientRect.Call(p.container, uintptr(unsafe.Pointer(&bounds))); ok == 0 {
		return
	}
	scale := float64(DPI(p.container)) / 96
	var zoom float64
	if comCall(p.controller, 7, uintptr(unsafe.Pointer(&zoom))) == nil && zoom > 0 {
		scale *= zoom
	}
	if p.emulatedWidth > 0 {
		scale = float64(bounds.Right) / p.emulatedWidth
	}
	x, y := int32(math.Round(input.X*scale)), int32(math.Round(input.Y*scale))
	if x < 0 || y < 0 || x >= bounds.Right || y >= bounds.Bottom {
		showWindow.Call(p.cursor, 0)
		return
	}
	// HWND_TOP and SWP_NOACTIVATE preserve the operator's real OS pointer/focus.
	p.cursorPoint = nativePoint{x, y}
	p.positionCursor()
}

func (p *nativePage) positionCursor() {
	if p.cursor == 0 {
		return
	}
	visible, _, _ := user32.NewProc("IsWindowVisible").Call(p.container)
	if visible == 0 {
		showWindow.Call(p.cursor, 0)
		return
	}
	point := p.cursorPoint
	if ok, _, _ := user32.NewProc("ClientToScreen").Call(p.container, uintptr(unsafe.Pointer(&point))); ok == 0 {
		return
	}
	setWindowPos.Call(p.cursor, 0, uintptr(point.X), uintptr(point.Y), 22, 30, 0x10|0x40)
}
