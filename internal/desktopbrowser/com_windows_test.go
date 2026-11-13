//go:build windows

package desktopbrowser

import (
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

func TestCOMHandlerCallbackPointerArguments(t *testing.T) {
	var received uint32
	handler := newHandler("{5C4889F0-5EF6-4C5A-952C-D8F1B92D0574}", func(status uintptr, result unsafe.Pointer) uintptr {
		runtime.GC()
		if status != 7 || result == nil {
			return 0x80004003
		}
		received = *(*uint32)(result)
		return 0
	})
	defer handler.release()

	// Exercise the actual Windows callback adapter, including pointer outputs,
	// rather than calling the Go handlers directly.
	var queried *comHandler
	hr, _, _ := syscall.SyscallN(handlerVTable[0], handler.pointer(), uintptr(unsafe.Pointer(&handler.iid)), uintptr(unsafe.Pointer(&queried)))
	if hr != 0 || queried != handler {
		t.Fatalf("QueryInterface pointer output=%p want=%p HRESULT=%x", queried, handler, hr)
	}
	queried.release()
	if refs := handler.refs.Load(); refs != 1 {
		t.Fatalf("QueryInterface reference count after release=%d want=1", refs)
	}

	iid := handler.iid
	iid.Data1++
	hr, _, _ = syscall.SyscallN(handlerVTable[0], handler.pointer(), uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&queried)))
	if hr != 0x80004002 || queried != nil {
		t.Fatalf("unsupported QueryInterface did not clear output: pointer=%p HRESULT=%x", queried, hr)
	}

	result := uint32(0x12345678)
	hr, _, _ = syscall.SyscallN(handlerVTable[3], handler.pointer(), 7, uintptr(unsafe.Pointer(&result)))
	if hr != 0 || received != result {
		t.Fatalf("Invoke pointer after GC=%x want=%x HRESULT=%x", received, result, hr)
	}
}
