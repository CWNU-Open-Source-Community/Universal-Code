//go:build windows

package desktopbrowser

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                   = windows.NewLazySystemDLL("user32.dll")
	ole32                    = windows.NewLazySystemDLL("ole32.dll")
	createWindowEx           = user32.NewProc("CreateWindowExW")
	destroyWindow            = user32.NewProc("DestroyWindow")
	defWindowProc            = user32.NewProc("DefWindowProcW")
	postMessage              = user32.NewProc("PostMessageW")
	getWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	getClientRect            = user32.NewProc("GetClientRect")
	moveWindow               = user32.NewProc("MoveWindow")
	showWindow               = user32.NewProc("ShowWindow")
	setWindowPos             = user32.NewProc("SetWindowPos")
	getDPIForWindow          = user32.NewProc("GetDpiForWindow")
	dispatchers              sync.Map
	registerDispatcher       sync.Once
	registerDispatcherErr    error
	dispatcherClass          = windows.StringToUTF16Ptr("UniversalCode.NativeBrowser.Dispatcher")
	dispatcherProc           = windows.NewCallback(dispatcherWindowProc)
)

const dispatchMessage = 0x8000 + 0x3b1

type windowClass struct {
	Size, Style                        uint32
	WndProc                            uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	MenuName, ClassName                *uint16
	SmallIcon                          uintptr
}

type nativeRect struct{ Left, Top, Right, Bottom int32 }

// Dispatcher marshals work onto the calling UI thread. The caller must lock its
// OS thread before NewDispatcher and subsequently run the Wails message pump on
// that thread. This package never starts a competing message loop.
type Dispatcher struct {
	hwnd   uintptr
	thread uint32
	mu     sync.Mutex
	queue  []func()
	closed bool
}

func NewDispatcher() (*Dispatcher, error) {
	hr, _, _ := ole32.NewProc("CoInitializeEx").Call(0, 2)
	if err := hresult("CoInitializeEx(STA)", hr); err != nil {
		return nil, err
	}
	registerDispatcher.Do(func() {
		wc := windowClass{Size: uint32(unsafe.Sizeof(windowClass{})), WndProc: dispatcherProc, ClassName: dispatcherClass}
		atom, _, err := user32.NewProc("RegisterClassExW").Call(uintptr(unsafe.Pointer(&wc)))
		if atom == 0 {
			registerDispatcherErr = fmt.Errorf("register browser dispatcher: %w", err)
		}
	})
	if registerDispatcherErr != nil {
		ole32.NewProc("CoUninitialize").Call()
		return nil, registerDispatcherErr
	}
	// HWND_MESSAGE: this window dispatches tasks and is never a browser surface.
	hwnd, _, err := createWindowEx.Call(0, uintptr(unsafe.Pointer(dispatcherClass)), 0, 0, 0, 0, 0, 0, ^uintptr(2), 0, 0, 0)
	if hwnd == 0 {
		ole32.NewProc("CoUninitialize").Call()
		return nil, fmt.Errorf("create browser dispatcher: %w", err)
	}
	d := &Dispatcher{hwnd: hwnd, thread: windows.GetCurrentThreadId()}
	dispatchers.Store(hwnd, d)
	return d, nil
}

func (d *Dispatcher) ThreadID() uint32 { return d.thread }

func (d *Dispatcher) Post(fn func()) error {
	if d == nil || fn == nil {
		return errors.New("invalid UI dispatcher task")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return ErrClosed
	}
	d.queue = append(d.queue, fn)
	ok, _, err := postMessage.Call(d.hwnd, dispatchMessage, 0, 0)
	if ok == 0 {
		d.queue = d.queue[:len(d.queue)-1]
		return fmt.Errorf("post browser UI task: %w", err)
	}
	return nil
}

func (d *Dispatcher) Invoke(ctx context.Context, fn func() error) error {
	if d == nil || fn == nil {
		return errors.New("invalid UI dispatcher task")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if windows.GetCurrentThreadId() == d.thread {
		d.mu.Lock()
		closed := d.closed
		d.mu.Unlock()
		if closed {
			return ErrClosed
		}
		return fn()
	}
	result := make(chan error, 1)
	if err := d.Post(func() {
		if err := ctx.Err(); err != nil {
			result <- err
		} else {
			result <- fn()
		}
	}); err != nil {
		return err
	}
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close must run before the host message loop exits, after all Hosts are closed.
func (d *Dispatcher) Close() error {
	if d == nil {
		return nil
	}
	return d.Invoke(context.Background(), func() error {
		d.mu.Lock()
		d.closed = true
		d.mu.Unlock()
		dispatchers.Delete(d.hwnd)
		destroyWindow.Call(d.hwnd)
		ole32.NewProc("CoUninitialize").Call()
		return nil
	})
}

func dispatcherWindowProc(hwnd uintptr, message uint32, wparam, lparam uintptr) uintptr {
	if message == dispatchMessage {
		if value, ok := dispatchers.Load(hwnd); ok {
			d := value.(*Dispatcher)
			d.mu.Lock()
			tasks := d.queue
			d.queue = nil
			d.mu.Unlock()
			for _, task := range tasks {
				task()
			}
		}
		return 0
	}
	result, _, _ := defWindowProc.Call(hwnd, uintptr(message), wparam, lparam)
	return result
}

// DPI returns the native parent DPI for CSS-to-physical surface conversion.
func DPI(hwnd uintptr) uint32 {
	dpi, _, _ := getDPIForWindow.Call(hwnd)
	if dpi == 0 {
		return 96
	}
	return uint32(dpi)
}
