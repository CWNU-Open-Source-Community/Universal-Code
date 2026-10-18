package application

import (
	"context"
	"encoding/json"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolgateway"
)

func agentBrowserSlotKey(a toolgateway.AgentBrowserCallAuthority) string {
	if a.Role == domain.AgentRoleSpecialist {
		return a.RunID + "\x00" + a.AgentID
	}
	return a.RunID
}

func (s *AgentBrowserService) workerAuthority(ctx context.Context, ref domain.AgentAttemptRef, advertise bool) (toolgateway.AgentBrowserCallAuthority, error) {
	a, err := s.currentAuthority(ctx, ref.RunID)
	if err != nil {
		return a, err
	}
	worker, err := readWorkerExecutionContext(ctx, s.store, ref, s.drydocks)
	if err != nil {
		return a, err
	}
	if worker.Child.ParentID != a.RootAgentID {
		return a, agentBrowserUnavailable("browser worker parent changed")
	}
	a.Role, a.AgentID, a.AgentAttemptID = domain.AgentRoleSpecialist, ref.AgentID, ref.AttemptID
	a.SessionID = worker.Child.SessionID
	a.WorkspaceFingerprint = workerResourceFingerprint(worker.Binding)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authorityLocked(a, advertise)
}

// Slot ownership survives continue, while each advertised call authority still
// contains its current attempt and must pass check before dispatch.
func agentBrowserSameResource(a, b toolgateway.AgentBrowserCallAuthority) bool {
	if a.Role == domain.AgentRoleSpecialist && b.Role == domain.AgentRoleSpecialist {
		a.AgentAttemptID, b.AgentAttemptID = "", ""
		a.Generation, b.Generation = "", ""
	}
	return a == b
}

// The browser's process monitor runs between model turns. It checks resource
// ownership rather than reusing the opening attempt's tool-call authority.
func (s *AgentBrowserService) checkResource(ctx context.Context, a toolgateway.AgentBrowserCallAuthority) error {
	if a.Role != domain.AgentRoleSpecialist {
		return s.check(ctx, a)
	}
	current, err := s.currentAuthority(ctx, a.RunID)
	if err != nil {
		return err
	}
	worker, err := readWorkerResourceContext(ctx, s.store, domain.AgentAttemptRef{RunID: a.RunID, AgentID: a.AgentID, AttemptID: a.AgentAttemptID}, s.drydocks)
	if err != nil {
		return err
	}
	if worker.Child.ParentID != current.RootAgentID {
		return agentBrowserUnavailable("browser worker parent changed")
	}
	current.Role, current.AgentID, current.AgentAttemptID = a.Role, a.AgentID, a.AgentAttemptID
	current.SessionID = worker.Child.SessionID
	current.WorkspaceFingerprint = workerResourceFingerprint(worker.Binding)
	s.mu.Lock()
	defer s.mu.Unlock()
	slot := s.slots[agentBrowserSlotKey(a)]
	if slot == nil || slot.closed || s.closed {
		return agentBrowserUnavailable("browser worker resource closed")
	}
	current = agentBrowserAuthorityForSlot(current, slot)
	if !agentBrowserSameResource(current, a) || !agentBrowserSameResource(slot.authority, a) ||
		!s.options.Capabilities.RuntimeAuthority.AllowsRunAuthorizationFence(a.RunID, a.RunAuthorizationFence) {
		return agentBrowserUnavailable("browser worker resource authority changed")
	}
	return nil
}

func (s *AgentBrowserService) WithRunWorktree(drydocks *RunWorktreeService) *AgentBrowserService {
	if s != nil {
		s.drydocks = drydocks
	}
	return s
}

func (s *AgentBrowserService) WorkerToolAuthority(ctx context.Context, ref domain.AgentAttemptRef) (json.RawMessage, error) {
	a, err := s.workerAuthority(ctx, ref, true)
	if err != nil {
		return nil, err
	}
	return json.Marshal(a)
}

func (s *AgentBrowserService) CloseWorker(ctx context.Context, ref domain.AgentAttemptRef) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	slot := s.slots[agentBrowserSlotKey(toolgateway.AgentBrowserCallAuthority{RunID: ref.RunID, AgentID: ref.AgentID, AgentAttemptID: ref.AttemptID, Role: domain.AgentRoleSpecialist})]
	s.mu.Unlock()
	if slot == nil {
		return nil
	}
	_, err := s.Close(ctx, ref.RunID, slot.authority.BrowserSessionID)
	return err
}
