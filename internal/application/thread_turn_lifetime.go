package application

import (
	"context"

	"cyberagent-workbench/internal/apperror"
)

type acceptedThreadTurnContextKey struct{}

// WithHostLifetime is configured before serving requests. Only explicitly
// submitted ordinary turns transfer to this lifetime, after durable acceptance.
// Automatic/scheduled executions continue to use their caller's context.
func (s *ThreadTurnService) WithHostLifetime(parent context.Context) *ThreadTurnService {
	if s != nil {
		s.hostContext, s.hostCancel = context.WithCancel(parent)
		s.hostDrained = make(chan struct{})
		close(s.hostDrained)
	}
	return s
}

// Select the host context only for an input whose journal transaction committed.
// Preparation stays request-scoped; Stop still cancels the same active owner.
func acceptedThreadTurnContext(caller context.Context) context.Context {
	if host, ok := caller.Value(acceptedThreadTurnContextKey{}).(context.Context); ok {
		return host
	}
	return caller
}

// Register the complete mutating call, including its deferred rejection/receipt
// writes. This short lock never covers SQLite work or the live turn stop lock.
func (s *ThreadTurnService) beginHostOperation(caller context.Context) (context.Context, func(), error) {
	if s.hostContext == nil {
		return caller, func() {}, nil
	}
	s.hostMu.Lock()
	if s.hostContext.Err() != nil {
		s.hostMu.Unlock()
		return caller, nil, apperror.New(apperror.CodeUnavailable, "Thread execution host is shutting down")
	}
	if s.hostOperations == 0 {
		s.hostDrained = make(chan struct{})
	}
	s.hostOperations++
	s.hostMu.Unlock()
	linked, cancel := context.WithCancel(caller)
	stopHostCancellation := context.AfterFunc(s.hostContext, cancel)
	return linked, func() {
		stopHostCancellation()
		cancel()
		s.hostMu.Lock()
		s.hostOperations--
		if s.hostOperations == 0 {
			close(s.hostDrained)
		}
		s.hostMu.Unlock()
	}, nil
}

// Shutdown rejects new turns and cancels live execution in this host. A
// timeout leaves the store borrowed until a later Shutdown confirms the drain.
func (s *ThreadTurnService) Shutdown(ctx context.Context) error {
	if s == nil || s.hostCancel == nil {
		return nil
	}
	s.hostCancel()
	s.hostMu.Lock()
	drained := s.hostDrained
	s.hostMu.Unlock()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
