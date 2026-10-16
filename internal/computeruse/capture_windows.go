//go:build windows

package computeruse

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"unsafe"
)

var gdi32 = windowsDLL("gdi32.dll")
var getDC = user32.NewProc("GetDC")
var releaseDC = user32.NewProc("ReleaseDC")
var createCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
var deleteDC = gdi32.NewProc("DeleteDC")
var createDIBSection = gdi32.NewProc("CreateDIBSection")
var selectObject = gdi32.NewProc("SelectObject")
var deleteObject = gdi32.NewProc("DeleteObject")
var bitBlt = gdi32.NewProc("BitBlt")

type bitmapInfo struct {
	Size                         uint32
	Width, Height                int32
	Planes, BitCount             uint16
	Compression, SizeImage       uint32
	XPelsPerMeter, YPelsPerMeter int32
	ClrUsed, ClrImportant        uint32
}

func (b *winBackend) capture(ctx context.Context, w nativeWindow) ([]byte, error) {
	if err := b.guard(ctx, w); err != nil {
		return nil, err
	}
	if err := captureBoundsAllowed(w.Bounds); err != nil {
		return nil, err
	}
	if !screenContains(w.Bounds) {
		return nil, errors.New("window extends outside the visible desktop; move it fully on screen")
	}
	dc, _, _ := getDC.Call(0)
	if dc == 0 {
		return nil, errors.New("desktop capture unavailable")
	}
	defer releaseDC.Call(0, dc)
	mem, _, _ := createCompatibleDC.Call(dc)
	if mem == 0 {
		return nil, errors.New("cannot create capture DC")
	}
	defer deleteDC.Call(mem)
	info := bitmapInfo{Size: 40, Width: int32(w.Bounds.Width), Height: -int32(w.Bounds.Height), Planes: 1, BitCount: 32}
	var pixels unsafe.Pointer
	bitmap, _, _ := createDIBSection.Call(dc, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&pixels)), 0, 0)
	if bitmap == 0 || pixels == nil {
		return nil, errors.New("cannot create capture bitmap")
	}
	defer deleteObject.Call(bitmap)
	old, _, _ := selectObject.Call(mem, bitmap)
	if old == 0 || old == ^uintptr(0) {
		return nil, errors.New("cannot select capture bitmap")
	}
	defer selectObject.Call(mem, old)
	ok, _, _ := bitBlt.Call(mem, 0, 0, uintptr(w.Bounds.Width), uintptr(w.Bounds.Height), dc, uintptr(w.Bounds.X), uintptr(w.Bounds.Y), 0x40cc0020) // SRCCOPY | CAPTUREBLT
	if ok == 0 {
		return nil, errors.New("GDI desktop capture failed")
	}
	img := image.NewRGBA(image.Rect(0, 0, w.Bounds.Width, w.Bounds.Height))
	raw := unsafe.Slice((*byte)(pixels), len(img.Pix))
	for y := 0; y < w.Bounds.Height; y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for x := 0; x < w.Bounds.Width; x++ {
			i := (y*w.Bounds.Width + x) * 4
			img.Pix[i] = raw[i+2]
			img.Pix[i+1] = raw[i+1]
			img.Pix[i+2] = raw[i]
			img.Pix[i+3] = 255
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
