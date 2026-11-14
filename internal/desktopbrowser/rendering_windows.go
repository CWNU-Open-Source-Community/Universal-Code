//go:build windows

package desktopbrowser

import (
	"errors"
	"fmt"
)

func (p *nativePage) agentRenderingNeededOnUI() bool {
	p.host.mu.Lock()
	lease := p.host.lease
	p.host.mu.Unlock()
	if lease == nil || lease.pageID != p.id {
		return false
	}
	lease.mu.Lock()
	ready := lease.policy != nil && !lease.cancelled
	lease.mu.Unlock()
	return ready
}

// WebView2 needs a rendered controller for first-page input and screenshots.
// An empty Win32 region keeps this exact background child invisible and out of
// native hit testing while allowing Chromium to produce frames. It never moves
// focus or the operator's pointer; the selected visible page is unaffected.
func (p *nativePage) beginMaskedRenderingOnUI() error {
	if p.maskedRendering {
		return nil
	}
	region, _, err := gdi32.NewProc("CreateRectRgn").Call(0, 0, 0, 0)
	if region == 0 {
		return fmt.Errorf("isolate background browser rendering: %w", err)
	}
	if ok, _, err := user32.NewProc("SetWindowRgn").Call(p.container, region, 0); ok == 0 {
		gdi32.NewProc("DeleteObject").Call(region)
		return fmt.Errorf("isolate background browser rendering: %w", err)
	}
	// SetWindowRgn owns region after success.
	p.maskedRendering = true
	if p.cursor != 0 {
		showWindow.Call(p.cursor, 0)
	}
	if err := comCall(p.controller, controllerPutIsVisible, 1); err != nil {
		return errors.Join(err, p.endMaskedRenderingOnUI())
	}
	showWindow.Call(p.container, 4) // SW_SHOWNOACTIVATE, with the empty region installed.
	return nil
}

func (p *nativePage) endMaskedRenderingOnUI() error {
	if !p.maskedRendering {
		return nil
	}
	// Hide before removing the region, including when selecting another tab or
	// releasing the Agent lease. A hidden page must never flash over HTML/UI.
	showWindow.Call(p.container, 0)
	if p.cursor != 0 {
		showWindow.Call(p.cursor, 0)
	}
	err := comCall(p.controller, controllerPutIsVisible, 0)
	if ok, _, regionErr := user32.NewProc("SetWindowRgn").Call(p.container, 0, 0); ok == 0 {
		return errors.Join(err, fmt.Errorf("restore background browser region: %w", regionErr))
	}
	p.maskedRendering = false
	return err
}
