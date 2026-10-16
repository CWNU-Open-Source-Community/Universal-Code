//go:build windows && desktop && wv2runtime.error

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsRiskRestartRejectsAParentOtherThanItsCreator(t *testing.T) {
	if _, err := prepareRiskRestartParent(os.Getpid()); err == nil ||
		!strings.Contains(err.Error(), "parent identity is invalid") {
		t.Fatalf("unrelated restart parent error = %v", err)
	}
}

func TestWindowsRiskRestartExecutableIdentityIsCanonicalAndCaseInsensitive(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	if !sameWindowsExecutable(absolute, strings.ToUpper(absolute)) {
		t.Fatal("the same Windows executable was not recognized")
	}
	if sameWindowsExecutable(absolute, filepath.Join(filepath.Dir(absolute), "other.exe")) {
		t.Fatal("a different Windows executable was accepted as the restart parent")
	}
}

func TestWindowsRiskRestartHelperShowsFirstApplicationWindow(t *testing.T) {
	const childEnvironment = "TRAVERSE_TEST_RISK_RESTART_WINDOW"
	if os.Getenv(childEnvironment) == "1" {
		goruntime.LockOSThread()
		defer goruntime.UnlockOSThread()
		user32 := windows.NewLazySystemDLL("user32.dll")
		createWindow := user32.NewProc("CreateWindowExW")
		destroyWindow := user32.NewProc("DestroyWindow")
		showWindow := user32.NewProc("ShowWindow")
		class, err := windows.UTF16PtrFromString("STATIC")
		if err != nil {
			t.Fatal(err)
		}
		title, err := windows.UTF16PtrFromString("Desktop restart visibility regression")
		if err != nil {
			t.Fatal(err)
		}
		// Match Wails' initially hidden top-level window followed by its first
		// ShowWindow(SW_SHOW). Only this test-owned window is ever touched.
		const overlappedWindow = 0x00cf0000
		handle, _, createErr := createWindow.Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)),
			overlappedWindow, 0, 0, 160, 80, 0, 0, 0, 0)
		if handle == 0 {
			t.Fatalf("create test window: %v", createErr)
		}
		defer destroyWindow.Call(handle)
		showWindow.Call(handle, windows.SW_SHOW)
		var startup windows.StartupInfo
		if err := windows.GetStartupInfo(&startup); err != nil {
			t.Fatal(err)
		}
		if !windows.IsWindowVisible(windows.HWND(handle)) {
			t.Fatalf("restart helper hid its first application window: startup flags=%#x show=%d", startup.Flags, startup.ShowWindow)
		}
		console, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
		if console != 0 {
			t.Fatal("restart helper created or inherited a console window")
		}
		t.Logf("first ShowWindow(SW_SHOW) is visible; startup flags=%#x show=%d; console=0", startup.Flags, startup.ShowWindow)
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ready, err := newRiskRestartReadyChannel()
	if err != nil {
		t.Fatal(err)
	}
	defer ready.close()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestWindowsRiskRestartHelperShowsFirstApplicationWindow$", "-test.v")
	command.Env = append(os.Environ(), childEnvironment+"=1")
	configureRiskRestartHelperCommand(command, ready)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("real restart window regression: %v\n%s", err, output)
	}
	t.Logf("%s", output)
}
