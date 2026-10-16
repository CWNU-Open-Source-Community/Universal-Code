//go:build windows && desktop && wv2runtime.error

package main

import (
	"context"
	"errors"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/desktop"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"
)

func TestWindowsRiskConfirmationsUseCancelableNativeButtonsAndExactResultMapping(t *testing.T) {
	restart, err := desktopRiskRestartDialogOptions(desktop.DesktopRiskProfileVerification)
	if err != nil {
		t.Fatal(err)
	}
	for _, options := range []runtime.MessageDialogOptions{
		restart, desktopVerificationSettingsDialogOptions(application.DesktopVerificationSettings{UIEvidenceEnabled: true}),
	} {
		for _, button := range []int32{6, 7, 2, 0, 1, -1} {
			confirmed, err := confirmWindowsDesktopRiskDialog(t.Context(), options, 42,
				func(owner windows.HWND, message, title *uint16, flags uint32) (int32, error) {
					if owner != 42 || windows.UTF16PtrToString(message) != options.Message || windows.UTF16PtrToString(title) != options.Title {
						t.Fatal("native confirmation lost its Go-owned window or exact operation description")
					}
					if flags&0xf != windows.MB_YESNOCANCEL || flags&0xf00 != windows.MB_DEFBUTTON3 || flags&windows.MB_ICONWARNING == 0 {
						t.Fatalf("native confirmation cannot cancel safely: flags=%#x", flags)
					}
					return button, nil
				})
			if err != nil || confirmed != (button == 6) {
				t.Fatalf("Win32 result %d confirmed=%t err=%v", button, confirmed, err)
			}
		}
	}
}

func TestWindowsRiskConfirmationFailureOrCancelledContextCannotConfirm(t *testing.T) {
	options := desktopVerificationSettingsDialogOptions(application.DesktopVerificationSettings{})
	cause := errors.New("native MessageBox failed")
	confirmed, err := confirmWindowsDesktopRiskDialog(t.Context(), options, 42,
		func(windows.HWND, *uint16, *uint16, uint32) (int32, error) { return 6, cause })
	if confirmed || !errors.Is(err, cause) {
		t.Fatalf("native failure confirmed: %t %v", confirmed, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	confirmed, err = confirmWindowsDesktopRiskDialog(ctx, options, 42,
		func(windows.HWND, *uint16, *uint16, uint32) (int32, error) { cancel(); return 6, nil })
	if confirmed || !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown during native dialog confirmed: %t %v", confirmed, err)
	}
}

func TestWindowsRiskConfirmationOwnerLookupReusesNativeCallback(t *testing.T) {
	// Repeated save/cancel actions must not allocate Go's finite, permanent
	// native callback slots. Exercise actual EnumWindows past its 2,000 slots.
	for range 2048 {
		owner, err := desktopRiskConfirmationOwner()
		if (err == nil) != (owner != 0) {
			t.Fatalf("invalid owner lookup: owner=%d err=%v", owner, err)
		}
		if owner != 0 {
			var processID uint32
			if _, err := windows.GetWindowThreadProcessId(owner, &processID); err != nil || processID != windows.GetCurrentProcessId() {
				t.Fatalf("native confirmation selected a foreign owner: process=%d err=%v", processID, err)
			}
		}
	}
}
