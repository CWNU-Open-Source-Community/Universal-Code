//go:build windows

package desktopbrowser

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Slots below follow the Microsoft WebView2 Win32 IDL, including all inherited
// methods. The dependency's generated newer-interface vtables omit inheritance.
type comObject struct{ vtable *[128]uintptr }

//go:uintptrescapes
func comCall(object *comObject, slot int, args ...uintptr) error {
	all := make([]uintptr, 1, len(args)+1)
	all[0] = uintptr(unsafe.Pointer(object))
	all = append(all, args...)
	hr, _, _ := syscall.SyscallN(object.vtable[slot], all...)
	runtime.KeepAlive(object)
	return hresult(fmt.Sprintf("WebView2 slot %d", slot), hr)
}

func hresult(operation string, value uintptr) error {
	if int32(value) < 0 {
		return fmt.Errorf("%s: HRESULT 0x%08x", operation, uint32(value))
	}
	return nil
}

func (o *comObject) addRef() {
	if o != nil {
		_ = comCall(o, 1)
	}
}
func (o *comObject) release() {
	if o != nil {
		_ = comCall(o, 2)
	}
}

func (o *comObject) query(iid string) (*comObject, error) {
	guid, err := windows.GUIDFromString(iid)
	if err != nil {
		return nil, err
	}
	var result *comObject
	err = comCall(o, 0, uintptr(unsafe.Pointer(&guid)), uintptr(unsafe.Pointer(&result)))
	return result, err
}

func getString(o *comObject, slot int) (string, error) {
	var value *uint16
	if err := comCall(o, slot, uintptr(unsafe.Pointer(&value))); err != nil {
		return "", err
	}
	if value == nil {
		return "", nil
	}
	s := windows.UTF16PtrToString(value)
	ole32.NewProc("CoTaskMemFree").Call(uintptr(unsafe.Pointer(value)))
	return s, nil
}

func getBool(o *comObject, slot int) bool {
	var value int32
	_ = comCall(o, slot, uintptr(unsafe.Pointer(&value)))
	return value != 0
}

func putControllerBounds(controller *comObject, bounds nativeRect) error {
	// RECT is a 16-byte by-value aggregate: Windows x64 passes its address,
	// while Windows ARM64 passes the two double-words in consecutive registers.
	if runtime.GOARCH == "arm64" {
		first := uint64(uint32(bounds.Left)) | uint64(uint32(bounds.Top))<<32
		second := uint64(uint32(bounds.Right)) | uint64(uint32(bounds.Bottom))<<32
		return comCall(controller, controllerPutBounds, uintptr(first), uintptr(second))
	}
	if runtime.GOARCH == "386" {
		return comCall(controller, controllerPutBounds, uintptr(bounds.Left), uintptr(bounds.Top), uintptr(bounds.Right), uintptr(bounds.Bottom))
	}
	return comCall(controller, controllerPutBounds, uintptr(unsafe.Pointer(&bounds)))
}

type comHandler struct {
	vtable *[4]uintptr
	refs   atomic.Int32
	iid    windows.GUID
	invoke func(uintptr, unsafe.Pointer) uintptr
}

var handlerRoots sync.Map
var handlerVTable = [4]uintptr{
	windows.NewCallback(handlerQuery), windows.NewCallback(handlerAddRef),
	windows.NewCallback(handlerRelease), windows.NewCallback(handlerInvoke),
}
var iidIUnknown = windows.GUID{Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}

// The first Invoke argument is either a sender address or an HRESULT; the
// second is always a native pointer. Keep it pointer-typed across the callback.
func newHandler(iid string, invoke func(uintptr, unsafe.Pointer) uintptr) *comHandler {
	guid, err := windows.GUIDFromString(iid)
	if err != nil {
		panic(err)
	}
	h := &comHandler{vtable: &handlerVTable, iid: guid, invoke: invoke}
	h.refs.Store(1)
	handlerRoots.Store(uintptr(unsafe.Pointer(h)), h)
	return h
}
func (h *comHandler) pointer() uintptr { return uintptr(unsafe.Pointer(h)) }
func (h *comHandler) release()         { handlerRelease(h.pointer()) }
func handlerQuery(this uintptr, iid *windows.GUID, out **comHandler) uintptr {
	value, ok := handlerRoots.Load(this)
	if !ok {
		return uintptr(0x80004002)
	}
	h := value.(*comHandler)
	if out == nil {
		return uintptr(0x80004003)
	}
	*out = nil
	if iid == nil {
		return uintptr(0x80004002)
	}
	guid := *iid
	if guid != h.iid && guid != iidIUnknown {
		return uintptr(0x80004002)
	}
	*out = h
	handlerAddRef(this)
	return 0
}
func handlerAddRef(this uintptr) uintptr {
	value, ok := handlerRoots.Load(this)
	if !ok {
		return 0
	}
	return uintptr(value.(*comHandler).refs.Add(1))
}
func handlerRelease(this uintptr) uintptr {
	value, ok := handlerRoots.Load(this)
	if !ok {
		return 0
	}
	h := value.(*comHandler)
	n := h.refs.Add(-1)
	if n == 0 {
		handlerRoots.Delete(this)
	}
	return uintptr(n)
}
func handlerInvoke(this, a uintptr, b unsafe.Pointer) uintptr {
	value, ok := handlerRoots.Load(this)
	if !ok {
		return uintptr(0x8000ffff)
	}
	return value.(*comHandler).invoke(a, b)
}

const (
	controllerGetIsVisible   = 3
	controllerPutIsVisible   = 4
	controllerPutBounds      = 6
	controllerMoveFocus      = 12
	controllerNotifyPosition = 23
	controllerClose          = 24
	controllerGetCore        = 25
	coreSource               = 4
	coreNavigate             = 5
	coreNavigationStarting   = 7
	coreSourceChanged        = 11
	coreHistoryChanged       = 13
	coreNavigationCompleted  = 15
	coreProcessFailed        = 25
	coreReload               = 31
	coreCallDevTools         = 36
	coreCanGoBack            = 38
	coreCanGoForward         = 39
	coreGoBack               = 40
	coreGoForward            = 41
	coreGetDevToolsReceiver  = 42
	coreStop                 = 43
	coreNewWindowRequested   = 44
	coreTitleChanged         = 46
	coreTitle                = 48
	coreFullscreenChanged    = 52
	coreIsFullscreen         = 54
	coreWindowClose          = 59
)
