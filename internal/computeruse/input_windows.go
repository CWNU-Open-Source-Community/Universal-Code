//go:build windows

package computeruse

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"unicode/utf16"
	"unsafe"
)

func windowsDLL(name string) *windows.LazyDLL { return windows.NewLazySystemDLL(name) }

var sendInput = user32.NewProc("SendInput")
var setCursorPos = user32.NewProc("SetCursorPos")
var getAsyncKeyState = user32.NewProc("GetAsyncKeyState")

type mouseInput struct {
	DX, DY            int32
	Data, Flags, Time uint32
	Extra             uintptr
}
type keyboardInput struct {
	VK, Scan    uint16
	Flags, Time uint32
	Extra       uintptr
}
type inputEvent struct {
	Kind  uint32
	Mouse mouseInput
}

func keyEvent(vk, scan uint16, flags uint32) inputEvent {
	e := inputEvent{Kind: 1}
	*(*keyboardInput)(unsafe.Pointer(&e.Mouse)) = keyboardInput{VK: vk, Scan: scan, Flags: flags}
	return e
}
func mouseEvent(flags, data uint32) inputEvent {
	return inputEvent{Mouse: mouseInput{Flags: flags, Data: data}}
}
func submit(events []inputEvent) error {
	if len(events) == 0 {
		return nil
	}
	n, _, err := sendInput.Call(uintptr(len(events)), uintptr(unsafe.Pointer(&events[0])), unsafe.Sizeof(events[0]))
	if n != uintptr(len(events)) {
		return fmt.Errorf("SendInput accepted %d of %d events (Windows may block elevated targets): %v", n, len(events), err)
	}
	return nil
}

// Release pairs are attempted even when SendInput partially succeeds. Cleanup
// does not depend on a cancelled context or the target still being foreground.
func submitPaired(events, release []inputEvent) error {
	if err := submit(events); err != nil {
		cleanup := submit(release)
		return errors.Join(err, cleanup)
	}
	return nil
}
func modifiersClear() error {
	for _, vk := range []uintptr{0x10, 0x11, 0x12, 0x5b, 0x5c, 1, 2, 4} {
		state, _, _ := getAsyncKeyState.Call(vk)
		if state&0x8000 != 0 {
			return errors.New("a physical modifier or mouse button is held; release it before computer input")
		}
	}
	return nil
}
func extended(vk uint16) uint32 {
	if (vk >= 0x21 && vk <= 0x2e) || vk == 0x5b {
		return 1
	}
	return 0
}
func pointOwner(x, y int) uintptr {
	var h uintptr
	if unsafe.Sizeof(uintptr(0)) == 8 {
		h, _, _ = windowFromPoint.Call(uintptr(uint64(uint32(x)) | uint64(uint32(y))<<32))
	} else {
		h, _, _ = windowFromPoint.Call(uintptr(uint32(x)), uintptr(uint32(y)))
	}
	r, _, _ := getAncestor.Call(h, 2)
	return r
}
func (b *winBackend) moveTo(ctx context.Context, w nativeWindow, x, y int) error {
	if err := b.guard(ctx, w); err != nil {
		return err
	}
	if !screenContains(w.Bounds) {
		return errors.New("window extends outside visible desktop")
	}
	x += w.Bounds.X
	y += w.Bounds.Y
	if pointOwner(x, y) != w.handle {
		return errors.New("input point is covered by another window")
	}
	if ok, _, _ := setCursorPos.Call(uintptr(x), uintptr(y)); ok == 0 {
		return errors.New("cannot position desktop cursor")
	}
	if err := b.guard(ctx, w); err != nil {
		return err
	}
	if pointOwner(x, y) != w.handle {
		return errors.New("input point became covered by another window")
	}
	return nil
}
func (b *winBackend) input(ctx context.Context, w nativeWindow, name string, a Arguments) error {
	if err := b.guard(ctx, w); err != nil {
		return err
	}
	if err := modifiersClear(); err != nil {
		return err
	}
	switch name {
	case "computer_click":
		if err := b.moveTo(ctx, w, a.X, a.Y); err != nil {
			return err
		}
		down, up := uint32(2), uint32(4)
		if a.Button == "right" {
			down, up = 8, 16
		}
		if a.Button == "middle" {
			down, up = 32, 64
		}
		return submitPaired([]inputEvent{mouseEvent(down, 0), mouseEvent(up, 0)}, []inputEvent{mouseEvent(up, 0)})
	case "computer_scroll":
		if err := b.moveTo(ctx, w, w.Bounds.Width/2, w.Bounds.Height/2); err != nil {
			return err
		}
		events := []inputEvent{}
		if a.DY != 0 {
			events = append(events, mouseEvent(0x800, uint32(int32(a.DY*120))))
		}
		if a.DX != 0 {
			events = append(events, mouseEvent(0x1000, uint32(int32(a.DX*120))))
		}
		return submit(events)
	case "computer_type":
		for _, u := range utf16.Encode([]rune(a.Text)) {
			if err := b.guard(ctx, w); err != nil {
				return err
			}
			if err := submitPaired([]inputEvent{keyEvent(0, u, 4), keyEvent(0, u, 6)}, []inputEvent{keyEvent(0, u, 6)}); err != nil {
				return err
			}
		}
		return nil
	case "computer_key":
		keys, err := parseChord(a.Key)
		if err != nil {
			return err
		}
		events := []inputEvent{}
		release := []inputEvent{}
		for _, k := range keys {
			state, _, _ := getAsyncKeyState.Call(uintptr(k))
			if state&0x8000 != 0 {
				return errors.New("a requested key is already physically held")
			}
			events = append(events, keyEvent(k, 0, extended(k)))
		}
		for i := len(keys) - 1; i >= 0; i-- {
			release = append(release, keyEvent(keys[i], 0, extended(keys[i])|2))
		}
		events = append(events, release...)
		if err := b.guard(ctx, w); err != nil {
			return err
		}
		return submitPaired(events, release)
	}
	return errors.New("unsupported input operation")
}
