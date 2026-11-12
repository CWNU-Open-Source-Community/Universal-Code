package application

import (
	"context"
	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolgateway"
	"errors"
	"time"
)

func (s *AgentBrowserService) startRuntime(ctx context.Context, a toolgateway.AgentBrowserCallAuthority, slot *agentBrowserSlot) (agentBrowserRuntime, error) {
	runtimeAuthority := browserruntime.AgentBrowserAuthority{RunID: a.RunID, ManagerBootID: a.ManagerBootID, SessionID: a.BrowserSessionID, Generation: a.SessionGeneration, PermissionSnapshotID: a.PermissionSnapshotID, PermissionRevision: a.PermissionRevision, PermissionActivation: slot.activation, RunAuthorizationFence: a.RunAuthorizationFence, PermissionMode: string(a.PermissionMode)}
	request := browserruntime.AgentBrowserStartRequest{HomePath: s.options.HomePath, Authority: runtimeAuthority, Product: s.options.Product, Headless: s.options.Headless, RuntimeDeadline: time.Now().Add(s.options.SessionLifetime), NetworkMode: browserruntime.AgentBrowserNetworkMode(a.BrowserNetworkAccess), ScopedOrigins: append([]string(nil), a.DeclaredOrigins...), ReviewNetworkWrite: s.reviewNetworkWrite(a), CheckAuthority: func(c context.Context, received browserruntime.AgentBrowserAuthority) error {
		if received != runtimeAuthority {
			return agentBrowserUnavailable("runtime activation does not match its private slot")
		}
		return s.checkResource(c, a)
	}}
	if s.options.SharedPage != nil && a.Role == domain.AgentRoleRoot {
		page, err := s.options.SharedPage(ctx, request)
		if err != nil {
			return nil, err
		}
		return browserruntime.AttachAgentBrowser(ctx, request, page)
	}
	return s.launch(ctx, request)
}

// AttachSharedPage is an operator-only composition entry point. It grants no
// durable tool call and dispatches no navigation, input, snapshot or screenshot.
// All model operations still require their exact started call and live lease.
func (s *AgentBrowserService) AttachSharedPage(ctx context.Context, runID string) (AgentBrowserView, error) {
	if s == nil || s.options.SharedPage == nil {
		return AgentBrowserView{}, agentBrowserUnavailable("shared browser host is unavailable")
	}
	a, err := s.authorityForAdvertisement(ctx, runID)
	if err != nil {
		return AgentBrowserView{}, err
	}
	s.mu.Lock()
	slot := s.slots[agentBrowserSlotKey(a)]
	s.mu.Unlock()
	if slot == nil {
		return AgentBrowserView{}, agentBrowserUnavailable("browser session is unavailable")
	}
	slot.action.Lock()
	defer slot.action.Unlock()
	if err = s.check(ctx, a); err != nil {
		return AgentBrowserView{}, err
	}
	s.mu.Lock()
	if slot.runtime != nil {
		s.mu.Unlock()
		return s.GetStatus(ctx, runID)
	}
	slot.launchAttempted = true
	slot.view.State = "starting"
	s.mu.Unlock()
	runtime, err := s.startRuntime(ctx, a, slot)
	if err != nil {
		if runtime != nil {
			runtime.Cancel()
		}
		s.mu.Lock()
		slot.closed = true
		slot.view.State = "failed"
		slot.view.FailureReason = err.Error()
		var failure *browserruntime.AgentBrowserLaunchError
		if errors.As(err, &failure) && failure.Cleanup.SessionID == a.BrowserSessionID {
			receipt := failure.Cleanup
			slot.view.Cleanup = &receipt
		}
		view := s.projectSlotLocked(slot)
		s.mu.Unlock()
		return view, err
	}
	s.mu.Lock()
	retired := s.closed || slot.closed || s.slots[agentBrowserSlotKey(a)] != slot
	slot.runtime = runtime
	s.mu.Unlock()
	go s.observeRuntimeDone(slot, runtime)
	if retired {
		runtime.Cancel()
		return AgentBrowserView{}, agentBrowserUnavailable("shared page retired during attachment")
	}
	if err = s.check(ctx, a); err != nil {
		runtime.Cancel()
		return AgentBrowserView{}, err
	}
	return s.GetStatus(ctx, runID)
}

// DetachForUser revokes the agent slot before waiting for an in-flight action.
// The native adapter releases only agent control; its page and login survive.
func (s *AgentBrowserService) DetachForUser(ctx context.Context, runID, sessionID string) (AgentBrowserView, error) {
	if s == nil || s.options.SharedPage == nil {
		return AgentBrowserView{}, agentBrowserUnavailable("shared browser host is unavailable")
	}
	return s.Close(ctx, runID, sessionID)
}
