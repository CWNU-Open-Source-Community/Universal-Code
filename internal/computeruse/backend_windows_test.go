//go:build windows

package computeruse

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestAcceptedForegroundActivationWaitsAndCancels(t *testing.T) {
	reads := 0
	if err := waitForeground(context.Background(), 123, func() uintptr {
		reads++
		if reads < 3 {
			return 0
		}
		return 123
	}); err != nil || reads != 3 {
		t.Fatalf("accepted asynchronous activation did not settle: %d %v", reads, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := waitForeground(ctx, 123, func() uintptr { return 0 }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("activation did not respect cancellation: %v", err)
	}
}

func TestNativePointerOutputs(t *testing.T) {
	if _, err := mapGeneration(windows.InvalidHandle); err == nil {
		t.Fatal("invalid section handle mapped")
	}
	// Allocate an off-screen DIB only; no desktop capture or window is needed.
	info := bitmapInfo{Size: 40, Width: 2, Height: -2, Planes: 1, BitCount: 32}
	var pixels unsafe.Pointer
	bitmap, _, err := createDIBSection.Call(0, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&pixels)), 0, 0)
	if bitmap == 0 || pixels == nil {
		t.Fatalf("DIB pointer output unavailable: %v", err)
	}
	defer deleteObject.Call(bitmap)
	data := unsafe.Slice((*byte)(pixels), 16)
	data[0], data[15] = 17, 31
	if data[0] != 17 || data[15] != 31 {
		t.Fatal("DIB memory is not writable")
	}
}

func TestWindowsInputABI(t *testing.T) {
	want := uintptr(40)
	offset := uintptr(8)
	if unsafe.Sizeof(uintptr(0)) == 4 {
		want = 28
		offset = 4
	}
	if unsafe.Sizeof(inputEvent{}) != want || unsafe.Offsetof(inputEvent{}.Mouse) != offset {
		t.Fatal("incorrect SendInput ABI")
	}
	e := keyEvent(0x41, 0, 2)
	k := (*keyboardInput)(unsafe.Pointer(&e.Mouse))
	if e.Kind != 1 || k.VK != 0x41 || k.Flags != 2 {
		t.Fatal("incorrect keyboard union")
	}
}

func TestApplicationFrameRequiresUniqueHostedProcess(t *testing.T) {
	for _, pids := range [][]uint32{{42, 100}, {100, 42, 100}, {100}} {
		got, err := uniqueHostedPID(42, pids)
		if err != nil || got != 100 {
			t.Fatalf("unique app was not resolved: %v, %d, %v", pids, got, err)
		}
	}
	for _, pids := range [][]uint32{nil, {42}, {42, 0, 100}, {100, 101}, {42, 100, 101, 100}} {
		if _, err := uniqueHostedPID(42, pids); err == nil {
			t.Fatalf("ambiguous or unavailable host accepted: %v", pids)
		}
	}
}

func TestHostedWindowRevalidatesOwnerAndApplication(t *testing.T) {
	w := nativeWindow{Window: Window{PID: 100, AppID: "app", OwnerPID: 42}, handle: 99, birth: 10, ownerBirth: 20, ownerAppID: "frame-host"}
	for _, change := range []func(*nativeWindow){
		func(w *nativeWindow) { w.OwnerPID++ }, func(w *nativeWindow) { w.ownerBirth++ }, func(w *nativeWindow) { w.ownerAppID = "replacement host" },
		func(w *nativeWindow) { w.PID++ }, func(w *nativeWindow) { w.birth++ }, func(w *nativeWindow) { w.AppID = "another packaged app" },
	} {
		other := w
		change(&other)
		if sameWindow(w, other) {
			t.Fatalf("hosted window accepted changed identity: %#v", other)
		}
	}
}
func TestWindowsSelfExclusion(t *testing.T) {
	b := &winBackend{}
	b.loadExcluded()
	if !b.excluded[uint32(os.Getpid())] {
		t.Fatal("executor not excluded")
	}
	p, _, err := processIdentity(uint32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	if !b.excludedPaths[p] {
		t.Fatal("executor image not excluded")
	}
	id, err := applicationID(p)
	if err != nil {
		t.Fatal(err)
	}
	if parsed, err := ParseAppID(id); err != nil || parsed != p {
		t.Fatalf("identity roundtrip %q %v", parsed, err)
	}
}
func TestDesktopLockAndSharedGeneration(t *testing.T) {
	// This acquires only the coordination objects; it never observes or inputs
	// into any window and is safe on a developer's normal interactive desktop.
	a := newBackend()
	release, err := a.enter(context.Background())
	if err != nil {
		t.Skipf("desktop unavailable: %v", err)
	}
	start := a.generation(false)
	a.generation(true)
	result := make(chan error, 1)
	go func() {
		b := newBackend()
		unlock, err := b.enter(context.Background())
		if err == nil {
			unlock()
		}
		result <- err
	}()
	if err := <-result; err == nil {
		release()
		t.Fatal("concurrent desktop lock acquired")
	}
	release()
	b := newBackend()
	unlock, err := b.enter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if b.generation(false) != start+1 {
		t.Fatal("generation not shared between executors")
	}
}
