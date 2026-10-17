package application

import (
	"context"
	"encoding/json"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolgateway"
)

func agentBrowserSlotKey(a toolgateway.AgentBrowserCallAuthority) string {
	if a.Role == domain.AgentRoleSpecialist {
		return a.RunID + "\x00" + a.AgentID + "\x00" + a.AgentAttemptID
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
	a.WorkspaceFingerprint = worker.Binding.RootFingerprint
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authorityLocked(a, advertise)
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
