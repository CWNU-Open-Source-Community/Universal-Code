package mcp

import (
	"context"
	"errors"
	"testing"
)

func TestWorkerCatalogDefersIdleNotificationWithoutRestoringAuthority(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	current := false
	stale := errors.New("attempt already continued")
	s := &catalogSession{lifetime: ctx, stop: stop, check: func(context.Context) error { return stale }, workspace: &InvocationWorkspace{CheckLifetime: func(context.Context) error {
		if current {
			return errors.New("worker ended")
		}
		return nil
	}}}
	m := &Manager{}
	if err := m.refreshTaskCatalog(ctx, catalogSessionKey{}, s); !errors.Is(err, stale) {
		t.Fatalf("idle refresh: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("continued worker lost its transport on a notification")
	}
	if err := s.boundTaskCheck()(ctx); !errors.Is(err, stale) {
		t.Fatalf("old task authority was restored: %v", err)
	}
	current = true
	if err := m.refreshTaskCatalog(ctx, catalogSessionKey{}, s); !errors.Is(err, stale) {
		t.Fatalf("closed worker refresh: %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("ended worker retained its transport")
	}
}
