//go:build windows && desktop

package main

import (
	"context"
	"errors"
	"os"
	"runtime"
	"unsafe"

	"cyberagent-workbench/internal/desktop"
	"cyberagent-workbench/internal/desktopbrowser"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"
)

func prepareSharedBrowser(home string) (*desktop.SharedBrowserOptions, func(), error) {
	runtime.LockOSThread()
	dispatcher, err := desktopbrowser.NewDispatcher()
	if err != nil {
		runtime.UnlockOSThread()
		return nil, nil, err
	}
	cleanup := func() { _ = dispatcher.Close(); runtime.UnlockOSThread() }
	return &desktop.SharedBrowserOptions{HomePath: home, CreateHost: func(ctx context.Context, profile string, onEvent func(desktopbrowser.Event)) (*desktopbrowser.Host, error) {
		hwnd, err := sharedBrowserParent(dispatcher.ThreadID())
		if err != nil {
			return nil, err
		}
		return desktopbrowser.NewHost(ctx, desktopbrowser.HostOptions{ParentHWND: hwnd, Dispatcher: dispatcher, UserDataDir: profile, OnEvent: onEvent})
	}, SetFullscreen: func(ctx context.Context, enabled bool) error {
		if enabled {
			wailsruntime.WindowFullscreen(ctx)
		} else {
			wailsruntime.WindowUnfullscreen(ctx)
		}
		return nil
	}}, cleanup, nil
}

// Select the shell's configured class on its exact UI thread and process.
// Foreground windows, captions, and browser child windows are not candidates.
func sharedBrowserParent(threadID uint32) (uintptr, error) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	var matches []uintptr
	callback := windows.NewCallback(func(hwnd, _ uintptr) uintptr {
		var pid uint32
		owner, _, _ := user32.NewProc("GetWindowThreadProcessId").Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if uint32(owner) != threadID || pid != uint32(os.Getpid()) {
			return 1
		}
		var class [256]uint16
		n, _, _ := user32.NewProc("GetClassNameW").Call(hwnd, uintptr(unsafe.Pointer(&class[0])), uintptr(len(class)))
		if n > 0 && windows.UTF16ToString(class[:]) == "CyberAgentWorkbench" {
			matches = append(matches, hwnd)
		}
		return 1
	})
	user32.NewProc("EnumThreadWindows").Call(uintptr(threadID), callback, 0)
	if len(matches) != 1 {
		return 0, errors.New("shared browser requires the running desktop window")
	}
	return matches[0], nil
}
