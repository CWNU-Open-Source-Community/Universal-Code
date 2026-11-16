//go:build windows

package desktopbrowser

import (
	"fmt"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/windows"
)

var (
	comctl32             = windows.NewLazySystemDLL("comctl32.dll")
	setWindowSubclass    = comctl32.NewProc("SetWindowSubclass")
	removeWindowSubclass = comctl32.NewProc("RemoveWindowSubclass")
	defSubclassProc      = comctl32.NewProc("DefSubclassProc")
	parentCallback       uintptr
	parentHosts          sync.Map
	nextSubclassID       atomic.Uint64
)

func init() { parentCallback = windows.NewCallback(parentWindowProc) }

func (h *Host) watchParent() error {
	h.subclassID = uintptr(nextSubclassID.Add(1))
	parentHosts.Store(h.subclassID, h)
	if ok, _, err := setWindowSubclass.Call(h.options.ParentHWND, parentCallback, h.subclassID, 0); ok == 0 {
		parentHosts.Delete(h.subclassID)
		h.subclassID = 0
		return fmt.Errorf("watch browser parent: %w", err)
	}
	return nil
}
func (h *Host) unwatchParent() {
	if h.subclassID != 0 {
		removeWindowSubclass.Call(h.options.ParentHWND, parentCallback, h.subclassID)
		parentHosts.Delete(h.subclassID)
		h.subclassID = 0
	}
}

func parentWindowProc(hwnd uintptr, message uint32, wparam, lparam, id, _ uintptr) uintptr {
	value, ok := parentHosts.Load(id)
	if !ok {
		result, _, _ := defSubclassProc.Call(hwnd, uintptr(message), wparam, lparam)
		return result
	}
	h := value.(*Host)
	if message == 0x82 {
		h.closeOnUI()
	} // WM_NCDESTROY, while the parent is still valid.
	result, _, _ := defSubclassProc.Call(hwnd, uintptr(message), wparam, lparam)
	if message == 0x47 || message == 0x18 || message == 0x5 { // WINDOWPOSCHANGED, SHOWWINDOW, SIZE
		h.mu.Lock()
		pages := make([]*nativePage, 0, len(h.tabs))
		for _, page := range h.tabs {
			pages = append(pages, page)
		}
		h.mu.Unlock()
		for _, page := range pages {
			if page.controller != nil {
				_ = comCall(page.controller, controllerNotifyPosition)
			}
			if message == 0x5 {
				if page.cursor != 0 {
					showWindow.Call(page.cursor, 0)
				}
			} else {
				page.positionCursor()
			}
		}
	}
	return result
}
