package browserruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"
)

type agentBrowserRef struct {
	backend             int64
	epoch               uint64
	snapshot, signature string
	name, role          string
}

type AgentBrowserRuntime struct {
	request                                   AgentBrowserStartRequest
	profile                                   agentBrowserProfile
	product                                   BrowserProduct
	process                                   agentBrowserOwnedProcess
	cdp                                       *agentBrowserCDP
	life                                      context.Context
	cancel                                    context.CancelFunc
	done                                      chan struct{}
	doneOnce                                  sync.Once
	closeMu                                   sync.Mutex
	operation                                 chan struct{}
	mu                                        sync.Mutex
	state, session, frame, loader, currentURL string
	loaded                                    string
	loading                                   bool
	epoch                                     uint64
	refs                                      map[string]agentBrowserRef
	screencast                                *agentBrowserScreencastPump
	browserContextID                          string
	downloadPath                              string
	downloads                                 map[string]*AgentBrowserDownloadReceipt
	downloadOrder                             []string
}

func (r *AgentBrowserRuntime) initialize(ctx context.Context) error {
	var bc struct {
		ID string `json:"browserContextId"`
	}
	if err := r.cdp.call(ctx, "", "Target.createBrowserContext", map[string]any{"disposeOnDetach": true}, &bc); err != nil {
		return err
	}
	if !validRestrictedCDPToken(bc.ID) {
		return ErrBrowserRuntimeBoundary
	}
	r.mu.Lock()
	r.browserContextID = bc.ID
	r.downloads = map[string]*AgentBrowserDownloadReceipt{}
	r.mu.Unlock()
	if err := r.startDownloadQuarantine(ctx); err != nil {
		return err
	}
	var target struct {
		ID string `json:"targetId"`
	}
	if err := r.cdp.call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank", "browserContextId": bc.ID}, &target); err != nil {
		return err
	}
	if !validRestrictedCDPToken(target.ID) {
		return ErrBrowserRuntimeBoundary
	}
	var attached struct {
		ID string `json:"sessionId"`
	}
	if err := r.cdp.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": target.ID, "flatten": true}, &attached); err != nil {
		return err
	}
	if !validRestrictedCDPToken(attached.ID) {
		return ErrBrowserRuntimeBoundary
	}
	r.mu.Lock()
	r.session = attached.ID
	r.mu.Unlock()
	for _, method := range []string{"Page.enable", "DOM.enable", "Runtime.enable", "Accessibility.enable"} {
		if err := r.call(ctx, method, map[string]any{}, nil); err != nil {
			return err
		}
	}
	if err := r.call(ctx, "Page.setLifecycleEventsEnabled", map[string]any{"enabled": true}, nil); err != nil {
		return err
	}
	_, err := r.document(ctx)
	return err
}

func (r *AgentBrowserRuntime) call(ctx context.Context, method string, params any, out any) error {
	r.mu.Lock()
	session := r.session
	r.mu.Unlock()
	return r.cdp.call(ctx, session, method, params, out)
}

func (r *AgentBrowserRuntime) invalidateLocked() { r.epoch++; r.refs = map[string]agentBrowserRef{} }
func (r *AgentBrowserRuntime) onEvent(event cdpWireMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if event.Method == "Target.detachedFromTarget" {
		var detached struct{ SessionID string }
		if json.Unmarshal(event.Params, &detached) == nil && detached.SessionID == r.session && r.session != "" {
			r.cancel()
		}
		return
	}
	// Download events are pushed on the browser-level session that enabled
	// them; Chromium may also deliver them on the owning target session.
	// Either way they are consumed here and never reach the target switch.
	if event.Method == "Browser.downloadWillBegin" || event.Method == "Browser.downloadProgress" {
		if event.SessionID == "" || event.SessionID == r.session {
			r.handleDownloadEventLocked(event)
		}
		return
	}
	if event.SessionID != r.session || r.session == "" {
		return
	}
	switch event.Method {
	case "Page.frameStartedLoading":
		var e struct{ FrameID string }
		if json.Unmarshal(event.Params, &e) == nil && e.FrameID == r.frame {
			r.loading = true
			r.invalidateLocked()
		}
	case "Page.frameStoppedLoading":
		var e struct{ FrameID string }
		if json.Unmarshal(event.Params, &e) == nil && e.FrameID == r.frame {
			r.loading = false
		}
	case "Page.lifecycleEvent":
		var e struct{ FrameID, LoaderID, Name string }
		if json.Unmarshal(event.Params, &e) == nil && e.FrameID == r.frame && (e.Name == "DOMContentLoaded" || e.Name == "load") {
			r.loaded = e.LoaderID
			r.loading = false
		}
	case "Page.frameNavigated":
		var e struct {
			Frame struct{ ID, ParentID, LoaderID, URL string }
		}
		if json.Unmarshal(event.Params, &e) != nil || e.Frame.ParentID != "" {
			return
		}
		r.frame = e.Frame.ID
		r.loader = e.Frame.LoaderID
		r.currentURL = e.Frame.URL
		r.invalidateLocked()
		if e.Frame.URL != "about:blank" && !validAgentBrowserURL(e.Frame.URL) {
			r.cancel()
		}
	case "Page.navigatedWithinDocument":
		var e struct{ FrameID, URL string }
		if json.Unmarshal(event.Params, &e) == nil && e.FrameID == r.frame {
			r.currentURL = e.URL
			r.loading = false
			r.invalidateLocked()
		}
	case "DOM.documentUpdated":
		r.invalidateLocked()
	case "Page.screencastFrame":
		r.deliverScreencastLocked(event)
		return
	case "Page.javascriptDialogOpening":
		r.state = "waiting_user"
		r.invalidateLocked()
	case "Page.javascriptDialogClosed":
		if r.life.Err() == nil {
			r.state = "ready"
		}
	case "Inspector.detached", "Inspector.targetCrashed":
		r.cancel()
	}
}

func (r *AgentBrowserRuntime) monitor() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-r.life.Done():
			return
		case <-r.process.Done():
			r.cancel()
			return
		case <-r.cdp.done:
			r.cancel()
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(r.life, time.Second)
			err := r.request.CheckAuthority(ctx, r.request.Authority)
			cancel()
			if err != nil {
				r.cancel()
				return
			}
		}
	}
}

// Cleanup must not wait behind a slow application authority lookup. The
// trusted callback receives a deadline, while cancellation has its own reaper.
func (r *AgentBrowserRuntime) reapOnInvalidation() {
	<-r.life.Done()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = r.Close(ctx)
}

// begin serializes actions, but never replays an action after a timeout.
func (r *AgentBrowserRuntime) begin(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, ErrBrowserRuntimeBoundary
	}
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-r.life.Done():
		return nil, nil, ErrAgentBrowserClosed
	case <-r.operation:
	}
	release := func() { r.operation <- struct{}{} }
	if r.life.Err() != nil {
		release()
		return nil, nil, ErrAgentBrowserClosed
	}
	if err := r.request.CheckAuthority(ctx, r.request.Authority); err != nil {
		r.Cancel()
		release()
		return nil, nil, err
	}
	r.mu.Lock()
	if r.state == "waiting_user" {
		r.mu.Unlock()
		release()
		return nil, nil, errors.New("browser is waiting for user interaction")
	}
	r.state = "busy"
	r.mu.Unlock()
	op, cancel := context.WithTimeout(ctx, 30*time.Second)
	stop := context.AfterFunc(r.life, cancel)
	return op, func() {
		stop()
		cancel()
		r.mu.Lock()
		if r.life.Err() == nil && r.state == "busy" {
			r.state = "ready"
		}
		r.mu.Unlock()
		release()
	}, nil
}

func (r *AgentBrowserRuntime) check(ctx context.Context, epoch uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.life.Err() != nil {
		return ErrAgentBrowserClosed
	}
	if err := r.request.CheckAuthority(ctx, r.request.Authority); err != nil {
		r.Cancel()
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.epoch != epoch {
		return ErrAgentBrowserStaleReference
	}
	return nil
}
func (r *AgentBrowserRuntime) Status() AgentBrowserStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.state
	if state == "ready" && r.loading {
		state = "loading"
	}
	if r.life.Err() != nil && state != "closed" && state != "cleanup_pending" {
		state = "closing"
	}
	return AgentBrowserStatus{Version: AgentBrowserProtocolVersion, SessionID: r.request.Authority.SessionID, Generation: r.request.Authority.Generation, Product: r.product, State: state, CanonicalURL: r.currentURL, DocumentEpoch: r.epoch, SupportedActions: []string{"navigate", "snapshot", "click", "type", "screenshot", "scroll", "key"}, Downloads: r.downloadReceiptsLocked()}
}

// Done signals invalidation and the end of the first cleanup attempt. Inspect
// Close's result for cleanup completion; a failed cleanup remains retryable.
func (r *AgentBrowserRuntime) Done() <-chan struct{} { return r.done }
func (r *AgentBrowserRuntime) Cancel()               { r.cancel() }
func (r *AgentBrowserRuntime) Close(ctx context.Context) (AgentBrowserCleanup, error) {
	r.cancel()
	r.closeMu.Lock()
	defer r.closeMu.Unlock()
	defer r.doneOnce.Do(func() { close(r.done) })
	result := AgentBrowserCleanup{SessionID: r.request.Authority.SessionID}
	r.mu.Lock()
	r.state = "closing"
	r.invalidateLocked()
	r.mu.Unlock()
	if r.cdp != nil {
		r.cdp.close()
	}
	var err error
	if r.process == nil {
		result = AgentBrowserNoProcessCleanup(result.SessionID)
		result.ProfileRemoved = false
	} else {
		err = r.process.Stop(ctx)
		exit, ok := r.process.Exit()
		result.TreeReaped = ok && exit.TreeReaped
		if ok {
			result.ProcessScope, result.ProcessScopeVerified = exit.ProcessScope, exit.ProcessScopeVerified
		}
	}
	if result.ProcessesStopped() {
		cleanupErr := r.profile.cleanup()
		result.ProfileRemoved = cleanupErr == nil
		err = errors.Join(err, cleanupErr)
	}
	result.CleanupPending = !result.ProcessesStopped() || !result.ProfileRemoved
	if result.CleanupPending && err == nil {
		err = errors.New("agent browser cleanup is pending")
	}
	r.mu.Lock()
	if result.CleanupPending {
		r.state = "cleanup_pending"
	} else {
		r.state = "closed"
	}
	r.mu.Unlock()
	return result, err
}

func agentBrowserURL(raw string) (string, error) {
	if len(raw) > 8192 || strings.ContainsAny(raw, "\x00\r\n\t") {
		return "", ErrBrowserRuntimeBoundary
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", ErrBrowserRuntimeBoundary
	}
	return u.String(), nil
}

type agentBrowserDocument struct {
	epoch   uint64
	backend int64
	url     string
}

func (r *AgentBrowserRuntime) document(ctx context.Context) (agentBrowserDocument, error) {
	// Await the typed document-ready event, without replaying navigation or
	// waiting for unrelated network activity to stop.
	for {
		r.mu.Lock()
		loading := r.loading
		r.mu.Unlock()
		if !loading {
			break
		}
		select {
		case <-ctx.Done():
			return agentBrowserDocument{}, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	r.mu.Lock()
	startedEpoch := r.epoch
	r.mu.Unlock()
	var tree struct {
		FrameTree struct {
			Frame struct{ ID, LoaderID, URL string }
		}
	}
	if err := r.call(ctx, "Page.getFrameTree", map[string]any{}, &tree); err != nil {
		return agentBrowserDocument{}, err
	}
	f := tree.FrameTree.Frame
	if f.ID == "" || (f.URL != "about:blank" && !validAgentBrowserURL(f.URL)) {
		return agentBrowserDocument{}, ErrBrowserRuntimeBoundary
	}
	r.mu.Lock()
	if r.epoch != startedEpoch {
		r.mu.Unlock()
		return agentBrowserDocument{}, ErrAgentBrowserStaleReference
	}
	if r.frame != f.ID || r.loader != f.LoaderID || r.currentURL != f.URL {
		r.frame = f.ID
		r.loader = f.LoaderID
		r.currentURL = f.URL
		r.invalidateLocked()
	}
	epoch := r.epoch
	r.mu.Unlock()
	var dom struct {
		Root struct {
			Backend int64 `json:"backendNodeId"`
		}
	}
	if err := r.call(ctx, "DOM.getDocument", map[string]any{"depth": 0, "pierce": false}, &dom); err != nil {
		return agentBrowserDocument{}, err
	}
	if err := r.check(ctx, epoch); err != nil {
		return agentBrowserDocument{}, err
	}
	return agentBrowserDocument{epoch, dom.Root.Backend, f.URL}, nil
}
func validAgentBrowserURL(raw string) bool { _, err := agentBrowserURL(raw); return err == nil }
