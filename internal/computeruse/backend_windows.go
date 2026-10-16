//go:build windows

package computeruse

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var user32 = windows.NewLazySystemDLL("user32.dll")
var enumWindows = user32.NewProc("EnumWindows")
var isWindowVisible = user32.NewProc("IsWindowVisible")
var isIconic = user32.NewProc("IsIconic")
var getWindowRect = user32.NewProc("GetWindowRect")
var getWindowText = user32.NewProc("GetWindowTextW")
var getWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
var getForegroundWindow = user32.NewProc("GetForegroundWindow")
var setForegroundWindow = user32.NewProc("SetForegroundWindow")
var getAncestor = user32.NewProc("GetAncestor")
var windowFromPoint = user32.NewProc("WindowFromPoint")
var getSystemMetrics = user32.NewProc("GetSystemMetrics")
var setThreadDPI = user32.NewProc("SetThreadDpiAwarenessContext")
var dwm = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmGetWindowAttribute")
var ntMapViewOfSection = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtMapViewOfSection")

type winBackend struct {
	mutex         windows.Handle
	mapping       windows.Handle
	epoch         *uint64
	excluded      map[uint32]bool
	excludedPaths map[string]bool
}

func newBackend() backend { return &winBackend{} }

// The named mutex and generation mapping are shared by helper processes in the
// current logon session. Busy calls fail without replaying an action later.
func (b *winBackend) enter(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runtime.LockOSThread()
	fail := func(err error) (func(), error) { runtime.UnlockOSThread(); return nil, err }
	if b.mutex == 0 {
		n, _ := windows.UTF16PtrFromString(`Local\UniversalCodeComputerUseDesktop-v1`)
		h, err := windows.CreateMutex(nil, false, n)
		if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return fail(err)
		}
		b.mutex = h
	}
	status, err := windows.WaitForSingleObject(b.mutex, 0)
	if err != nil {
		return fail(err)
	}
	if status != windows.WAIT_OBJECT_0 && status != windows.WAIT_ABANDONED {
		return fail(errors.New("desktop executor busy; obtain a new observation before retrying"))
	}
	if b.epoch == nil {
		n, _ := windows.UTF16PtrFromString(`Local\UniversalCodeComputerUseGeneration-v1`)
		h, err := windows.CreateFileMapping(windows.InvalidHandle, nil, windows.PAGE_READWRITE, 0, 8, n)
		if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			windows.ReleaseMutex(b.mutex)
			return fail(err)
		}
		p, err := mapGeneration(h)
		if err != nil {
			windows.CloseHandle(h)
			windows.ReleaseMutex(b.mutex)
			return fail(err)
		}
		b.mapping = h
		b.epoch = p
	}
	// Per-monitor-aware coordinates on this locked OS thread make capture and
	// input use the same physical pixels, without changing the host's DPI mode.
	prev, _, _ := setThreadDPI.Call(^uintptr(3)) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (-4)
	if prev == 0 {
		windows.ReleaseMutex(b.mutex)
		return fail(errors.New("per-monitor DPI awareness unavailable"))
	}
	if b.excluded == nil {
		b.loadExcluded()
	}
	return func() { setThreadDPI.Call(prev); windows.ReleaseMutex(b.mutex); runtime.UnlockOSThread() }, nil
}
func (b *winBackend) generation(advance bool) uint64 {
	if advance {
		*b.epoch++
	}
	return *b.epoch
}

// NtMapViewOfSection returns the address through PVOID *BaseAddress, so the
// native pointer can remain a pointer throughout its lifetime. In contrast,
// x/sys MapViewOfFile returns uintptr, which cannot safely be stored and later
// converted to a Go pointer. The view is owned by this helper until process exit.
// https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/wdm/nf-wdm-zwmapviewofsection
func mapGeneration(section windows.Handle) (*uint64, error) {
	var address unsafe.Pointer
	size := uintptr(8)
	status, _, _ := ntMapViewOfSection.Call(
		uintptr(section), uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&address)),
		0, 0, 0, uintptr(unsafe.Pointer(&size)), 2, 0, windows.PAGE_READWRITE, // ViewUnmap
	)
	if int32(status) < 0 {
		return nil, windows.NTStatus(status)
	}
	if address == nil || size < 8 {
		return nil, errors.New("invalid shared generation mapping")
	}
	return (*uint64)(address), nil
}

func (b *winBackend) loadExcluded() {
	b.excluded = map[uint32]bool{}
	b.excludedPaths = map[string]bool{}
	parents := map[uint32]uint32{}
	parents[uint32(os.Getpid())] = uint32(os.Getppid())
	s, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err == nil {
		defer windows.CloseHandle(s)
		entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
		for err = windows.Process32First(s, &entry); err == nil; err = windows.Process32Next(s, &entry) {
			parents[entry.ProcessID] = entry.ParentProcessID
		}
	}
	for pid := uint32(os.Getpid()); pid != 0 && !b.excluded[pid]; pid = parents[pid] {
		b.excluded[pid] = true
		if path, _, err := processIdentity(pid); err == nil {
			b.excludedPaths[path] = true
		}
	}
}
func processIdentity(pid uint32) (string, uint64, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", 0, err
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, 32768)
	n := uint32(len(buf))
	if err = windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return "", 0, err
	}
	var creation, exit, kernel, user windows.Filetime
	if err = windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return "", 0, err
	}
	return strings.ToLower(filepath.Clean(windows.UTF16ToString(buf[:n]))), uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime), nil
}
func applicationID(path string) (string, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	h, err := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(h, &info); err != nil {
		return "", err
	}
	id := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%d", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow)))
	return "winapp:" + base64.RawURLEncoding.EncodeToString([]byte(path)) + fmt.Sprintf(":%x", id), nil
}

type winRect struct{ Left, Top, Right, Bottom int32 }

func (b *winBackend) inspect(hwnd uintptr) (nativeWindow, error) {
	var result nativeWindow
	visible, _, _ := isWindowVisible.Call(hwnd)
	iconic, _, _ := isIconic.Call(hwnd)
	root, _, _ := getAncestor.Call(hwnd, 2)
	if visible == 0 || iconic != 0 || root != hwnd {
		return result, errors.New("window is not a visible non-minimized top-level window")
	}
	var cloaked uint32
	if hr, _, _ := dwm.Call(hwnd, 14, uintptr(unsafe.Pointer(&cloaked)), 4); hr == 0 && cloaked != 0 {
		return result, errors.New("window is on another desktop")
	}
	var pid uint32
	getWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	path, birth, err := processIdentity(pid)
	if err != nil {
		return result, err
	}
	if b.excluded[pid] || b.excludedPaths[path] {
		return result, errors.New("the executor and its host processes cannot be controlled")
	}
	ownerPID, ownerPath, ownerBirth := pid, path, birth
	ownerApp, err := applicationID(ownerPath)
	if err != nil {
		return result, err
	}
	if filepath.Base(path) == "applicationframehost.exe" {
		pid, err = applicationFramePID(hwnd, ownerPID)
		if err != nil {
			return result, err
		}
		path, birth, err = processIdentity(pid)
		if err != nil {
			return result, errors.New("hosted application process identity is unavailable")
		}
		if filepath.Base(path) == "applicationframehost.exe" {
			return result, errors.New("hosted application resolves to another shared ApplicationFrameHost")
		}
		if b.excluded[pid] || b.excludedPaths[path] {
			return result, errors.New("the hosted application is the executor or its host process")
		}
	}
	app := ownerApp
	if pid != ownerPID {
		app, err = applicationID(path)
		if err != nil {
			return result, err
		}
	}
	var rect winRect
	if ok, _, _ := getWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rect))); ok == 0 {
		return result, errors.New("cannot read window bounds")
	}
	// DWM's visible frame omits invisible resize borders (including maximized
	// windows' off-screen borders), and uses physical pixels.
	var frame winRect
	if hr, _, _ := dwm.Call(hwnd, 9, uintptr(unsafe.Pointer(&frame)), unsafe.Sizeof(frame)); hr == 0 && frame.Right > frame.Left && frame.Bottom > frame.Top {
		rect = frame
	}
	bounds := Bounds{int(rect.Left), int(rect.Top), int(rect.Right - rect.Left), int(rect.Bottom - rect.Top)}
	if bounds.Width <= 0 || bounds.Height <= 0 || int64(bounds.Width)*int64(bounds.Height) > 32*1024*1024 {
		return result, errors.New("window dimensions are unsupported")
	}
	title := make([]uint16, 1024)
	getWindowText.Call(hwnd, uintptr(unsafe.Pointer(&title[0])), uintptr(len(title)))
	foreground, _, _ := getForegroundWindow.Call()
	return nativeWindow{Window: Window{AppID: app, Title: windows.UTF16ToString(title), PID: pid, ProcessPath: path, HWND: fmt.Sprintf("0x%x", hwnd), Bounds: bounds, Foreground: foreground == hwnd, OwnerPID: ownerPID, OwnerProcessPath: ownerPath}, handle: hwnd, birth: birth, ownerBirth: ownerBirth, ownerAppID: ownerApp}, nil
}
func (b *winBackend) list(ctx context.Context) ([]nativeWindow, error) {
	state := windowListState{backend: b, ctx: ctx, windows: []nativeWindow{}}
	// enter holds the process-shared desktop mutex for the entire enumeration.
	// Keep callback state as a Go pointer instead of passing it through LPARAM.
	activeWindowList.Store(&state)
	defer activeWindowList.Store(nil)
	enumWindows.Call(windowListCallback, 0)
	return state.windows, ctx.Err()
}

type windowListState struct {
	backend *winBackend
	ctx     context.Context
	windows []nativeWindow
}

var activeWindowList atomic.Pointer[windowListState]
var windowListCallback = windows.NewCallback(func(hwnd, lparam uintptr) uintptr {
	state := activeWindowList.Load()
	if state == nil {
		return 0
	}
	if state.ctx.Err() != nil {
		return 0
	}
	if w, err := state.backend.inspect(hwnd); err == nil && w.Title != "" {
		state.windows = append(state.windows, w)
	}
	return 1
})

func (b *winBackend) activate(ctx context.Context, w nativeWindow) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	setForegroundWindow.Call(w.handle)
	for i := 0; i < 10; i++ {
		h, _, _ := getForegroundWindow.Call()
		if h == w.handle {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	return errors.New("Windows refused foreground activation; select the target window and observe again")
}
func (b *winBackend) guard(ctx context.Context, w nativeWindow) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	now, err := b.inspect(w.handle)
	if err != nil {
		return err
	}
	if !sameWindow(now, w) || now.Bounds != w.Bounds || !now.Foreground {
		return errors.New("target window changed before input")
	}
	return nil
}
func metric(n uintptr) int { v, _, _ := getSystemMetrics.Call(n); return int(int32(v)) }
func screenContains(b Bounds) bool {
	x, y, w, h := metric(76), metric(77), metric(78), metric(79)
	return b.X >= x && b.Y >= y && b.X+b.Width <= x+w && b.Y+b.Height <= y+h
}
