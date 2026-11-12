//go:build windows

package desktopbrowser

import (
	"context"
	"errors"
	"sync"
	"unsafe"

	"github.com/google/uuid"
	"golang.org/x/sys/windows"
)

// ControlLease is an explicit, revocable exclusive agent lease over a Host's
// profile. It implements the application's lease structurally. It fences agent
// commands only; native user input remains available throughout the lease.
type ControlLease struct {
	host      *Host
	pageID    string
	mu        sync.Mutex
	cancelled bool
	done      chan struct{}
	policy    *NetworkPolicy
	released  bool
}

func (h *Host) AcquireControl(ctx context.Context, pageID string) (*ControlLease, error) {
	lease := &ControlLease{host: h, pageID: pageID, done: make(chan struct{})}
	err := h.options.Dispatcher.Invoke(ctx, func() error {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.closed {
			return ErrClosed
		}
		if h.lease != nil {
			return errors.New("native browser profile already has an agent control lease")
		}
		if _, ok := h.tabs[pageID]; !ok {
			return ErrClosed
		}
		for _, page := range h.tabs {
			if !page.networkCapable {
				return errors.New("native browser agent control requires WebView2 request-source-kinds support")
			}
		}
		h.lease = lease
		return nil
	})
	if err != nil {
		return nil, err
	}
	return lease, nil
}

func (l *ControlLease) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cancelled {
		return ErrClosed
	}
	return nil
}
func (l *ControlLease) Done() <-chan struct{} { return l.done }
func (l *ControlLease) Cancel() {
	l.mu.Lock()
	if !l.cancelled {
		l.cancelled = true
		close(l.done)
	}
	l.mu.Unlock()
}
func (l *ControlLease) Dispatch(ctx context.Context, send func() error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.cancelled {
		return ErrClosed
	}
	return send()
}
func (l *ControlLease) InstallNetworkPolicy(ctx context.Context, policy NetworkPolicy) error {
	if policy.ReviewRequest == nil || policy.Done == nil {
		return errors.New("native browser network policy is incomplete")
	}
	if err := l.Dispatch(ctx, func() error {
		if l.policy != nil {
			return errors.New("native browser network policy already installed")
		}
		l.policy = &policy
		return nil
	}); err != nil {
		return err
	}
	go func() {
		select {
		case <-policy.Done:
			l.Cancel()
		case <-l.done:
		}
	}()
	return nil
}
func (l *ControlLease) Release(ctx context.Context) error {
	l.Cancel()
	return l.host.options.Dispatcher.Invoke(ctx, func() error {
		l.mu.Lock()
		if l.released {
			l.mu.Unlock()
			return nil
		}
		l.released = true
		l.mu.Unlock()
		h := l.host
		// An approved but paused request belongs to this lease and cannot
		// migrate to a later agent or to unrestricted manual browsing.
		for pending := range h.pending {
			if pending.lease == l {
				pending.finishOnUI(false)
			}
		}
		h.mu.Lock()
		if h.lease == l {
			h.lease = nil
		}
		h.mu.Unlock()
		return nil
	})
}

type nativeRequest struct {
	host           *Host
	page           *nativePage
	lease          *ControlLease
	args, deferral *comObject
	finished       bool // UI-thread only
}

func (p *nativePage) installNetwork() error {
	// Base filter semantics do not cover all shared/service worker requests.
	// Manual browsing works on old runtimes; acquiring agent control fails.
	v22, err := p.core.query("{DB75DFC7-A857-4632-A398-6969DDE26C0A}")
	if err != nil {
		return nil
	}
	defer v22.release()
	all, _ := windows.UTF16PtrFromString("*")
	if err = comCall(v22, 123, uintptr(unsafe.Pointer(all)), 0, 7); err != nil {
		return err
	}
	handler := newHandler("{AB00B74C-15F1-4646-80E8-E76341D25D71}", func(_, args uintptr) uintptr { p.interceptRequest((*comObject)(unsafe.Pointer(args))); return 0 })
	var token int64
	if err = comCall(p.core, 55, handler.pointer(), uintptr(unsafe.Pointer(&token))); err != nil {
		handler.release()
		return err
	}
	p.handlers = append(p.handlers, handler)
	v4, err := p.core.query("{20D02D59-6DF2-42DC-BD06-F98A694B1302}")
	if err != nil {
		return nil
	}
	defer v4.release()
	download := newHandler("{EFEDC989-C396-41CA-83F7-07F845A55724}", func(_, args uintptr) uintptr {
		p.host.mu.Lock()
		leased := p.host.lease != nil
		p.host.mu.Unlock()
		if leased {
			a := (*comObject)(unsafe.Pointer(args))
			_ = comCall(a, 5, 1)
			_ = comCall(a, 9, 1)
		}
		return 0
	})
	if err = comCall(v4, 75, download.pointer(), uintptr(unsafe.Pointer(&token))); err != nil {
		download.release()
		return err
	}
	p.handlers = append(p.handlers, download)
	p.networkCapable = true
	return nil
}

func (p *nativePage) interceptRequest(args *comObject) {
	h := p.host
	h.mu.Lock()
	lease := h.lease
	h.mu.Unlock()
	if lease == nil {
		return
	}
	var deferral *comObject
	if err := comCall(args, 6, uintptr(unsafe.Pointer(&deferral))); err != nil {
		p.blockRequest(args)
		return
	}
	args.addRef()
	pending := &nativeRequest{host: h, page: p, lease: lease, args: args, deferral: deferral}
	h.pending[pending] = struct{}{}
	var request *comObject
	if err := comCall(args, 3, uintptr(unsafe.Pointer(&request))); err != nil {
		pending.finishOnUI(false)
		return
	}
	defer request.release()
	url, urlErr := getString(request, 3)
	method, methodErr := getString(request, 5)
	if urlErr != nil || methodErr != nil {
		pending.finishOnUI(false)
		return
	}
	body, complete := requestBody(request)
	input := NetworkRequest{ID: uuid.NewString(), Method: method, URL: url, Body: body, BodyComplete: complete}
	lease.mu.Lock()
	policy := lease.policy
	cancelled := lease.cancelled
	lease.mu.Unlock()
	if policy == nil || cancelled {
		pending.finishOnUI(false)
		return
	}
	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			select {
			case <-lease.done:
				cancel()
			case <-policy.Done:
				cancel()
			case <-ctx.Done():
			}
		}()
		approved := false
		var decision sync.Mutex
		send := func() error {
			decision.Lock()
			defer decision.Unlock()
			if approved {
				return errors.New("native browser request already approved")
			}
			return lease.Dispatch(ctx, func() error { approved = true; return nil })
		}
		err := policy.ReviewRequest(ctx, input, send)
		decision.Lock()
		allow := err == nil && approved
		decision.Unlock()
		if postErr := h.options.Dispatcher.Post(func() {
			defer cancel()
			// Hold the same revocation lock across completing the native
			// deferral, the point at which Chromium can send the request.
			if !allow || lease.Dispatch(ctx, func() error { pending.finishOnUI(true); return nil }) != nil {
				pending.finishOnUI(false)
			}
		}); postErr != nil {
			cancel()
		}
	}()
}

func (p *nativePage) blockRequest(args *comObject) {
	reason := windows.StringToUTF16Ptr("Blocked")
	headers := windows.StringToUTF16Ptr("Content-Type: text/plain\r\nCache-Control: no-store\r\n")
	var response *comObject
	if err := comCall(p.host.env, 4, 0, 403, uintptr(unsafe.Pointer(reason)), uintptr(unsafe.Pointer(headers)), uintptr(unsafe.Pointer(&response))); err == nil {
		_ = comCall(args, 5, uintptr(unsafe.Pointer(response)))
		response.release()
	}
}
func (r *nativeRequest) finishOnUI(allow bool) {
	if r.finished {
		return
	}
	r.finished = true
	delete(r.host.pending, r)
	if !allow {
		r.page.blockRequest(r.args)
	}
	_ = comCall(r.deferral, 3)
	r.deferral.release()
	r.args.release()
}

func requestBody(request *comObject) ([]byte, bool) {
	var stream *comObject
	if err := comCall(request, 7, uintptr(unsafe.Pointer(&stream))); err != nil {
		return nil, false
	}
	if stream == nil {
		return nil, true
	}
	defer stream.release()
	var clone *comObject
	if err := comCall(stream, 13, uintptr(unsafe.Pointer(&clone))); err != nil {
		return nil, false
	}
	defer clone.release()
	if err := comCall(clone, 5, 0, 0, 0); err != nil {
		return nil, false
	}
	const limit = 1 << 20
	body := make([]byte, 0, 4096)
	for len(body) <= limit {
		buffer := make([]byte, 4096)
		var count uint32
		err := comCall(clone, 3, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), uintptr(unsafe.Pointer(&count)))
		if count > uint32(len(buffer)) {
			return nil, false
		}
		body = append(body, buffer[:count]...)
		if err != nil {
			return body, false
		}
		if count == 0 {
			return body, true
		}
	}
	return body[:limit], false
}
