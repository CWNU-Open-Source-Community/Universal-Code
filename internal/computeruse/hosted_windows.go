//go:build windows

package computeruse

import (
	"errors"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

var enumChildWindows = user32.NewProc("EnumChildWindows")
var activeChildProcesses atomic.Pointer[[]uint32]
var childProcessCallback = windows.NewCallback(func(hwnd, lparam uintptr) uintptr {
	processes := activeChildProcesses.Load()
	if processes == nil {
		return 0
	}
	var pid uint32
	getWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	*processes = append(*processes, pid)
	return 1
})

// ApplicationFrameHost owns the frame, while a descendant HWND belongs to the
// actual packaged app. PowerToys uses the same child-process relationship:
// https://github.com/microsoft/PowerToys/blob/main/src/common/utils/process_path.h
// Unlike a display-only lookup, consent must reject absent/ambiguous children.
// The caller holds the desktop mutex, serializing this synchronous callback.
func applicationFramePID(hwnd uintptr, ownerPID uint32) (uint32, error) {
	pids := []uint32{}
	activeChildProcesses.Store(&pids)
	defer activeChildProcesses.Store(nil)
	enumChildWindows.Call(hwnd, childProcessCallback, 0)
	return uniqueHostedPID(ownerPID, pids)
}

func uniqueHostedPID(ownerPID uint32, children []uint32) (uint32, error) {
	var appPID uint32
	for _, pid := range children {
		if pid == 0 {
			return 0, errors.New("ApplicationFrameHost child identity is unavailable")
		}
		if pid == ownerPID {
			continue
		}
		if appPID != 0 && appPID != pid {
			return 0, errors.New("ApplicationFrameHost hosts multiple application processes; cannot uniquely identify the application")
		}
		appPID = pid
	}
	if appPID == 0 {
		return 0, errors.New("ApplicationFrameHost has no uniquely identifiable application child")
	}
	return appPID, nil
}
