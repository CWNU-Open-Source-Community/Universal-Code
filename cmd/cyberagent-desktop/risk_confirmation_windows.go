//go:build windows && desktop && wv2runtime.error

package main

import (
	"context"
	"errors"
	"unsafe"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"
)

// Wails 2.14 Windows maps WarningDialog to MB_OK and ignores custom buttons.
// These two risk confirmations need a real Cancel action and safe ESC/close,
// so use Win32's fixed Yes/No/Cancel set with Cancel as the default button.
const desktopRiskConfirmationFlags = windows.MB_YESNOCANCEL | windows.MB_ICONWARNING | windows.MB_DEFBUTTON3 | windows.MB_SETFOREGROUND

// Native callback slots are never released. Reuse one callback and pass the
// current lookup's destination through EnumWindows' synchronous lParam.
var desktopRiskConfirmationWindowCallback = windows.NewCallback(findDesktopRiskConfirmationWindow)

func findDesktopRiskConfirmationWindow(handle uintptr, owner *windows.HWND) uintptr {
	window := windows.HWND(handle)
	var processID uint32
	if _, err := windows.GetWindowThreadProcessId(window, &processID); err == nil &&
		processID == windows.GetCurrentProcessId() && windows.IsWindowVisible(window) {
		*owner = window
		return 0
	}
	return 1
}

func confirmDesktopRiskDialog(ctx context.Context, options runtime.MessageDialogOptions, _ string) (bool, error) {
	owner, err := desktopRiskConfirmationOwner()
	if err != nil {
		return false, err
	}
	return confirmWindowsDesktopRiskDialog(ctx, options, owner, windows.MessageBox)
}

func desktopRiskConfirmationOwner() (windows.HWND, error) {
	var owner windows.HWND
	// EnumWindows reports false when our callback stops at the owned window.
	// The captured handle, rather than that stop code, establishes success.
	_ = windows.EnumWindows(desktopRiskConfirmationWindowCallback, unsafe.Pointer(&owner))
	if owner == 0 {
		return 0, errors.New("desktop native confirmation window is unavailable")
	}
	return owner, nil
}

func confirmWindowsDesktopRiskDialog(ctx context.Context, options runtime.MessageDialogOptions, owner windows.HWND,
	show func(windows.HWND, *uint16, *uint16, uint32) (int32, error),
) (bool, error) {
	if ctx == nil || owner == 0 || show == nil {
		return false, errors.New("desktop native confirmation is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	message, err := windows.UTF16PtrFromString(options.Message)
	if err != nil {
		return false, err
	}
	title, err := windows.UTF16PtrFromString(options.Title)
	if err != nil {
		return false, err
	}
	button, err := show(owner, message, title, desktopRiskConfirmationFlags)
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	// IDYES is 6. IDNO (7), IDCANCEL (2, including ESC/titlebar close), API
	// failure (0), and any unexpected return must never confirm the action.
	return button == 6, nil
}
