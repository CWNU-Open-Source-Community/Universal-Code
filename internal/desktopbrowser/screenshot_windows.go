//go:build windows

package desktopbrowser

import (
	"errors"
	"time"
)

// A hidden WebView2 controller does not answer Page.captureScreenshot. Render
// the same controller behind an empty native window region for this capture:
// no pixels or user input can reach its container, and focus is never moved.
// All fields and restoration run on the host UI thread.
type hiddenPageCapture struct {
	connection       *pageConnection
	id               int64
	timer            *time.Timer
	restoreRendering bool
}

func (c *pageConnection) beginHiddenCaptureOnUI(id int64) (*hiddenPageCapture, error) {
	p := c.page
	if p.hiddenCapture != nil {
		return nil, errors.New("native browser screenshot already in progress")
	}
	capture := &hiddenPageCapture{connection: c, id: id, restoreRendering: !getBool(p.controller, controllerGetIsVisible)}
	p.hiddenCapture = capture
	if capture.restoreRendering {
		if err := p.beginMaskedRenderingOnUI(); err != nil {
			_, restoreErr := capture.finishOnUI()
			return nil, errors.Join(err, restoreErr)
		}
	}
	capture.timer = time.AfterFunc(5*time.Second, func() {
		_ = p.host.options.Dispatcher.Post(func() {
			if p.hiddenCapture == capture {
				p.cancelHiddenCaptureOnUI("native browser cannot capture this hidden page; show its browser tab and retry, or use browser_snapshot for DOM inspection")
			}
		})
	})
	return capture, nil
}

func (c *hiddenPageCapture) finishOnUI() (bool, error) {
	p := c.connection.page
	if p.hiddenCapture != c {
		return false, nil
	}
	p.hiddenCapture = nil
	if c.timer != nil {
		c.timer.Stop()
	}
	if c.restoreRendering {
		return true, p.endMaskedRenderingOnUI()
	}
	return true, nil
}

func (p *nativePage) cancelHiddenCaptureOnUI(reason string) {
	if capture := p.hiddenCapture; capture != nil {
		_, err := capture.finishOnUI()
		if err != nil {
			reason = errors.Join(errors.New(reason), err).Error()
		}
		capture.connection.enqueue(map[string]any{"id": capture.id, "sessionId": p.id, "error": map[string]any{"code": -32000, "message": reason}})
	}
}
