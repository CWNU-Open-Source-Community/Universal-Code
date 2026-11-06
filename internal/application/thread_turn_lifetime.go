package application

import "context"

// WithHostLifetime is configured before serving requests. Only explicitly
// submitted ordinary turns transfer to this lifetime, after durable acceptance.
// Automatic/scheduled executions continue to use their caller's context.
func (s *ThreadTurnService) WithHostLifetime(parent context.Context) *ThreadTurnService {
	if s != nil {
		s.hostContext, s.hostCancel = context.WithCancel(parent)
	}
	return s
}

// acceptedTurnContext is called only after the message transaction commits.
// Preparation stays request-scoped; Stop still cancels the same active owner.
func (s *ThreadTurnService) acceptedTurnContext(caller context.Context, threadID string) context.Context {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	if active := s.activeTurns[threadID]; active != nil && active.hostContext != nil {
		return active.hostContext
	}
	return caller
}

// Shutdown rejects new turns and cancels live execution in this host. A
// timeout leaves the store borrowed until a later Shutdown confirms the drain.
func (s *ThreadTurnService) Shutdown(ctx context.Context) error {
	if s == nil || s.hostCancel == nil {
		return nil
	}
	s.turnMu.Lock()
	s.hostCancel()
	var pending []<-chan struct{}
	for _, active := range s.activeTurns {
		active.stopping = true
		active.cancel()
		if active.done != nil {
			pending = append(pending, active.done)
		}
	}
	s.turnMu.Unlock()
	for _, done := range pending {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
