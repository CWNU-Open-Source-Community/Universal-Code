//go:build windows

package desktopbrowser

import (
	"context"
	"time"
	"unsafe"
)

type nativePopup struct {
	opener         *nativePage
	args, deferral *comObject
	lease          *ControlLease
	url            string
	finished       bool // UI thread only
}

func (p *nativePage) openPopup(args *comObject) {
	// Handled is set before any asynchronous work: failed/denied popups must
	// never fall back to a browser window outside this native Host.
	if err := comCall(args, 6, 1); err != nil {
		return
	}
	url, err := getString(args, 3)
	if err != nil || validURL(url) != nil {
		return
	}
	var deferral *comObject
	if err = comCall(args, 9, uintptr(unsafe.Pointer(&deferral))); err != nil {
		return
	}
	args.addRef()
	p.host.mu.Lock()
	lease := p.host.lease
	p.host.mu.Unlock()
	popup := &nativePopup{opener: p, args: args, deferral: deferral, lease: lease, url: url}
	p.host.popups[popup] = struct{}{}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		// Do not Navigate: assigning this pristine, same-environment WebView to
		// NewWindow lets Chromium perform the original window.open navigation
		// and preserves the WindowProxy, opener, named window and POST data.
		page, err := p.host.CreateTab(ctx, TabOptions{})
		_ = p.host.options.Dispatcher.Post(func() {
			if err != nil {
				popup.finishOnUI(nil)
				return
			}
			popup.finishOnUI(page.(*nativePage))
		})
	}()
}

func (r *nativePopup) finishOnUI(child *nativePage) {
	if r.finished {
		if child != nil {
			child.closeOnUI()
		}
		return
	}
	r.finished = true
	delete(r.opener.host.popups, r)
	accept := false
	completed := false
	if child != nil {
		r.opener.mu.RLock()
		closed := r.opener.closed
		r.opener.mu.RUnlock()
		if !closed {
			assign := func() error {
				if err := comCall(r.args, 4, uintptr(unsafe.Pointer(child.core))); err != nil {
					return err
				}
				completed = true
				return comCall(r.deferral, 3)
			}
			var err error
			if r.lease != nil {
				err = r.lease.Dispatch(context.Background(), assign)
			} else {
				err = assign()
			}
			accept = err == nil
		}
	}
	// Complete and release on the same UI STA as the NewWindowRequested event.
	if !completed {
		_ = comCall(r.deferral, 3)
	}
	r.deferral.release()
	r.args.release()
	if !accept {
		if child != nil {
			child.closeOnUI()
		}
		return
	}
	r.opener.host.emit(Event{Kind: "popup", State: child.State(), URL: r.url, OpenerID: r.opener.ID()})
}
