package desktop

import (
	"context"
	"errors"
	"time"

	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/desktopbrowser"
)

type sharedBrowserLease struct {
	*desktopbrowser.ControlLease
	session            *sharedBrowserSession
	runID              string
	authoritySessionID string
}

func (l *sharedBrowserLease) Release(ctx context.Context) error {
	if err := l.ControlLease.Release(ctx); err != nil {
		return err
	}
	l.session.mu.Lock()
	if l.session.agentLease == l {
		l.session.agentLease = nil
		l.session.state.ControlOwner = "user"
	}
	l.session.mu.Unlock()
	return nil
}

func (s *SharedBrowserService) attachPage(ctx context.Context, request browserruntime.AgentBrowserStartRequest) (_ browserruntime.AgentBrowserAttachedPage, resultErr error) {
	// A provider failure before returning ownership must settle its own native
	// resources. The caller must never guess that a failed attachment is clean.
	var lease *sharedBrowserLease
	var connection desktopbrowser.JSONConnection
	defer func() {
		if resultErr == nil {
			return
		}
		cleanup := browserruntime.AgentBrowserCleanup{SessionID: request.Authority.SessionID, AgentDetached: true}
		if connection != nil {
			resultErr = errors.Join(resultErr, connection.Close())
		}
		if lease != nil {
			lease.Cancel()
			releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := lease.Release(releaseCtx); err != nil {
				resultErr = errors.Join(resultErr, err)
				cleanup.AgentDetached = false
				cleanup.CleanupPending = true
			}
		}
		resultErr = &browserruntime.AgentBrowserLaunchError{Err: resultErr, Cleanup: cleanup}
	}()
	if request.CheckAuthority == nil {
		return browserruntime.AgentBrowserAttachedPage{}, browserruntime.ErrBrowserRuntimeBoundary
	}
	if err := request.CheckAuthority(ctx, request.Authority); err != nil {
		return browserruntime.AgentBrowserAttachedPage{}, err
	}
	thread, err := s.store.GetThreadByRun(ctx, request.Authority.RunID)
	if err != nil {
		return browserruntime.AgentBrowserAttachedPage{}, err
	}
	workspaceID := thread.ProjectWorkspaceID
	if workspaceID == "" {
		workspaceID = thread.WorkspaceID
	}
	view, err := s.Open(ctx, SharedBrowserOpenRequest{WorkspaceID: workspaceID, ThreadID: thread.ID, RunID: request.Authority.RunID})
	if err != nil {
		return browserruntime.AgentBrowserAttachedPage{}, err
	}
	session, err := s.session(view.SessionID)
	if err != nil {
		return browserruntime.AgentBrowserAttachedPage{}, err
	}
	session.mu.Lock()
	page, ok := session.host.Tab(session.state.ActiveTabID)
	session.mu.Unlock()
	if !ok {
		return browserruntime.AgentBrowserAttachedPage{}, errors.New("shared browser has no active tab")
	}
	nativeLease, err := session.host.AcquireControl(ctx, page.ID())
	if err != nil {
		return browserruntime.AgentBrowserAttachedPage{}, err
	}
	lease = &sharedBrowserLease{ControlLease: nativeLease, session: session, runID: request.Authority.RunID, authoritySessionID: request.Authority.SessionID}
	session.mu.Lock()
	session.agentLease = lease
	session.state.ControlOwner = "agent"
	session.mu.Unlock()
	connection, err = page.OpenCDP(ctx, desktopbrowser.CDPOptions{Dispatch: lease.Dispatch, Events: []string{
		"Page.frameStartedLoading", "Page.frameStoppedLoading", "Page.lifecycleEvent", "Page.frameNavigated", "Page.navigatedWithinDocument", "DOM.documentUpdated", "Page.javascriptDialogOpening", "Page.javascriptDialogClosed", "Inspector.detached", "Inspector.targetCrashed", "Page.screencastFrame",
	}})
	if err != nil {
		return browserruntime.AgentBrowserAttachedPage{}, err
	}
	return browserruntime.AgentBrowserAttachedPage{PageID: page.ID(), SessionID: page.ID(), Connection: connection, Lease: lease, InstallNetworkPolicy: func(ctx context.Context, policy browserruntime.AgentBrowserAttachedNetworkPolicy) error {
		return lease.InstallNetworkPolicy(ctx, desktopbrowser.NetworkPolicy{Done: policy.Done, ReviewRequest: func(ctx context.Context, request desktopbrowser.NetworkRequest, send func() error) error {
			return policy.ReviewRequest(ctx, browserruntime.AgentBrowserAttachedNetworkRequest{ID: request.ID, FrameID: request.FrameID, Method: request.Method, URL: request.URL, Body: request.Body, BodyComplete: request.BodyComplete}, send)
		}})
	}}, nil
}
