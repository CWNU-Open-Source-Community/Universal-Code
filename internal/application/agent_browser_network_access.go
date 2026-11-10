package application

import (
	"context"
	"errors"
)

// InvalidateNetworkAccess closes only obsolete browser sessions, including
// worker-owned slots. Command Jobs keep their separate authorization fence.
func (s *AgentBrowserService) InvalidateNetworkAccess(ctx context.Context, runID string) error {
	if s == nil {
		return nil
	}
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	var sessions []string
	var runtimes []agentBrowserRuntime
	for id, slot := range s.sessions {
		if slot.authority.RunID == runID && slot.authority.BrowserNetworkRevision < run.Config.EffectiveBrowserNetworkRevision() {
			sessions = append(sessions, id)
			if slot.runtime != nil {
				runtimes = append(runtimes, slot.runtime)
			}
		}
	}
	s.mu.Unlock()
	for _, r := range runtimes {
		r.Cancel()
	}
	for _, id := range sessions {
		_, closeErr := s.Close(ctx, runID, id)
		err = errors.Join(err, closeErr)
	}
	return err
}
