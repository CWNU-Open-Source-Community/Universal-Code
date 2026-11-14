package browserruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
)

// AgentBrowserConnection carries only the exact owned page's CDP messages.
// Native adapters map SessionID to their own top-level page and reject all
// other target IDs. Close detaches the connection; it must not close the page.
type AgentBrowserConnection interface {
	ReadJSON(any) error
	WriteJSON(any) error
	SetWriteDeadline(time.Time) error
	Close() error
}

// AgentBrowserControlLease is issued by the trusted native host for agent dispatch.
// Ordinary human input does not revoke it. Cancel fences queued protocol writes. A
// revoked lease is never reactivated: another attachment gets a fresh lease.
type AgentBrowserControlLease interface {
	Check(context.Context) error
	Done() <-chan struct{}
	Cancel()
	Release(context.Context) error
}

type AgentBrowserAttachedNetworkRequest struct {
	ID, FrameID, Method, URL string
	Body                     []byte
	BodyComplete             bool
}

// Install before permitting agent control. Every request, including workers
// and subresources, must cross ReviewRequest. dispatch must synchronously reach
// the actual native send and recheck its supplied context there. An error aborts
// any not-yet-dispatched request; a successful send cannot be undone by a later
// review error. The host must deny downloads during the agent lease.
// An agent's queued/paused requests must never migrate to operator ownership.
type AgentBrowserAttachedNetworkPolicy struct {
	ReviewRequest func(context.Context, AgentBrowserAttachedNetworkRequest, func(context.Context) error) error
	Done          <-chan struct{}
}

type AgentBrowserAttachedPage struct {
	PageID               string
	SessionID            string
	Connection           AgentBrowserConnection
	Lease                AgentBrowserControlLease
	InstallNetworkPolicy func(context.Context, AgentBrowserAttachedNetworkPolicy) error
}

// AgentBrowserAttachedEvents is the page event subscription required by an
// exact-page transport. Missing navigation/resize events can leave stale refs
// usable; an adapter must deliver these on the attachment's logical SessionID.
func AgentBrowserAttachedEvents() []string {
	return []string{
		"Page.frameStartedLoading", "Page.frameStoppedLoading", "Page.lifecycleEvent",
		"Page.frameNavigated", "Page.navigatedWithinDocument", "Page.frameResized",
		"DOM.documentUpdated", "Page.javascriptDialogOpening", "Page.javascriptDialogClosed",
		"Inspector.detached", "Inspector.targetCrashed", "Page.screencastFrame",
	}
}

// AttachAgentBrowser controls the exact native page the user already sees.
// It does not create a BrowserContext, target, process or profile. Network and
// control gates must be installed by the native host; no unsupported gate is
// silently replaced with unrestricted browsing.
func AttachAgentBrowser(ctx context.Context, request AgentBrowserStartRequest, page AgentBrowserAttachedPage) (_ *AgentBrowserRuntime, err error) {
	adopted := false
	defer func() {
		if err == nil || adopted {
			return
		}
		cleanup := AgentBrowserCleanup{SessionID: request.Authority.SessionID, CleanupPending: true}
		var cleanupErr error
		if page.Lease != nil {
			page.Lease.Cancel()
			cleanupCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			cleanupErr = page.Lease.Release(cleanupCtx)
			stop()
			cleanup.AgentDetached = cleanupErr == nil
		}
		if page.Connection != nil {
			cleanupErr = errors.Join(cleanupErr, page.Connection.Close())
		}
		cleanup.CleanupPending = cleanupErr != nil || !cleanup.AgentDetached
		err = &AgentBrowserLaunchError{Err: errors.Join(err, cleanupErr), Cleanup: cleanup}
	}()
	if ctx == nil {
		return nil, ErrBrowserRuntimeBoundary
	}
	if err := validateAgentBrowserRequest(request); err != nil {
		return nil, err
	}
	if !validRestrictedCDPToken(page.PageID) || !validRestrictedCDPToken(page.SessionID) || page.Connection == nil || page.Lease == nil || page.Lease.Done() == nil || page.InstallNetworkPolicy == nil {
		return nil, ErrBrowserRuntimeBoundary
	}
	if err := request.CheckAuthority(ctx, request.Authority); err != nil {
		return nil, err
	}
	scope, err := agentBrowserScopeForRequest(request)
	if err != nil {
		return nil, err
	}
	life, cancel := context.WithDeadline(context.Background(), request.RuntimeDeadline)
	r := &AgentBrowserRuntime{request: request, product: request.Product, life: life, cancel: cancel, scope: scope, done: make(chan struct{}), operation: make(chan struct{}, 1), refs: map[string]agentBrowserRef{}, state: "starting", epoch: 1, session: page.SessionID, attached: &page, networkRuntimeID: agentBrowserToken(), networkPending: map[string]context.CancelFunc{}}
	adopted = true
	r.cancel = func() { page.Lease.Cancel(); cancel() }
	r.operation <- struct{}{}
	defer func() {
		if err != nil {
			cleanupCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			cleanup, cleanupErr := r.Close(cleanupCtx)
			err = &AgentBrowserLaunchError{Err: errors.Join(err, cleanupErr), Cleanup: cleanup}
		}
	}()
	originalCheck := request.CheckAuthority
	r.request.CheckAuthority = func(c context.Context, a AgentBrowserAuthority) error {
		if err := page.Lease.Check(c); err != nil {
			return err
		}
		return originalCheck(c, a)
	}
	if err = r.request.CheckAuthority(ctx, request.Authority); err != nil {
		return nil, err
	}
	if err = page.InstallNetworkPolicy(ctx, AgentBrowserAttachedNetworkPolicy{ReviewRequest: r.reviewAttachedRequest, Done: r.life.Done()}); err != nil {
		return nil, err
	}
	r.cdp = newAgentBrowserCDP(page.Connection, r.onEvent)
	for _, method := range []string{"Page.enable", "DOM.enable", "Runtime.enable", "Accessibility.enable"} {
		if err = r.call(ctx, method, map[string]any{}, nil); err != nil {
			return nil, err
		}
	}
	if err = r.call(ctx, "Page.setLifecycleEventsEnabled", map[string]any{"enabled": true}, nil); err != nil {
		return nil, err
	}
	var doc agentBrowserDocument
	if doc, err = r.document(ctx); err != nil {
		return nil, err
	}
	if doc.url != "about:blank" {
		if err = r.authorizeSessionNavigation(ctx, doc.url); err != nil {
			return nil, err
		}
	}
	r.mu.Lock()
	r.state = "ready"
	r.mu.Unlock()
	go r.reapOnInvalidation()
	go r.monitorAttached()
	return r, nil
}

func (r *AgentBrowserRuntime) monitorAttached() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-r.life.Done():
			return
		case <-r.attached.Lease.Done():
			r.Cancel()
			return
		case <-r.cdp.done:
			r.Cancel()
			return
		case <-ticker.C:
			ctx, stop := context.WithTimeout(r.life, time.Second)
			err := r.request.CheckAuthority(ctx, r.request.Authority)
			stop()
			if err != nil {
				r.Cancel()
				return
			}
		}
	}
}

// AttachmentDetached proves agent control was released, without claiming that
// the user's browser process stopped or their persistent profile was removed.
func (r *AgentBrowserRuntime) AttachmentDetached() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attached != nil && r.attachmentDetached
}

func (r *AgentBrowserRuntime) reviewAttachedRequest(ctx context.Context, in AgentBrowserAttachedNetworkRequest, dispatch func(context.Context) error) error {
	if dispatch == nil || in.ID == "" || in.Method == "" || in.Method != strings.ToUpper(in.Method) || strings.ContainsAny(in.Method, " \t\r\n\x00") {
		return ErrBrowserRuntimeBoundary
	}
	if err := r.request.CheckAuthority(ctx, r.request.Authority); err != nil {
		return err
	}
	if err := r.authorizeSessionNavigation(ctx, in.URL); err != nil {
		return err
	}
	op, stop := context.WithTimeout(ctx, 2*time.Minute)
	defer stop()
	stopLife := context.AfterFunc(r.life, stop)
	defer stopLife()
	r.mu.Lock()
	id := agentBrowserToken()
	epoch := r.networkEpoch
	r.networkPending[id] = stop
	request := AgentBrowserNetworkRequest{ID: id, RuntimeID: r.networkRuntimeID, RequestID: in.ID, FrameID: in.FrameID, DocumentURL: r.currentURL, DocumentEpoch: epoch, Authority: r.request.Authority, Method: in.Method, URL: in.URL, ExpiresAt: time.Now().Add(2 * time.Minute)}
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.networkPending, id); r.mu.Unlock() }()
	var once sync.Mutex
	consumed := false
	send := func() error {
		once.Lock()
		defer once.Unlock()
		if consumed {
			return errors.New("browser network request already consumed")
		}
		if err := op.Err(); err != nil {
			return err
		}
		if err := r.request.CheckAuthority(op, r.request.Authority); err != nil {
			return err
		}
		r.mu.Lock()
		stale := r.life.Err() != nil || r.networkEpoch != epoch
		r.mu.Unlock()
		if stale {
			return ErrAgentBrowserStaleReference
		}
		consumed = true
		// Keep op registered until the host completes the actual native send.
		// Document invalidation synchronously cancels op, including while its
		// UI dispatch is queued. No database lookup runs on the UI thread.
		return dispatch(op)
	}
	switch in.Method {
	case "GET", "HEAD", "OPTIONS":
		return send()
	}
	if !in.BodyComplete || len(in.Body) > 1024*1024 || r.request.ReviewNetworkWrite == nil {
		return ErrBrowserRuntimeBoundary
	}
	sum := sha256.Sum256(in.Body)
	request.BodySHA256 = hex.EncodeToString(sum[:])
	request.BodyBytes = len(in.Body)
	return r.request.ReviewNetworkWrite(op, request, send)
}
