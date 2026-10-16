//go:build windows

package computeruse

import (
	"context"
	"os"
	"testing"
	"unsafe"
)

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
