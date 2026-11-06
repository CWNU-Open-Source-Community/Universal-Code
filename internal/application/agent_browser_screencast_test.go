package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/toolgateway"
)

func TestAgentBrowserStreamScreencastBindsOneViewerToTheRun(t *testing.T) {
	service, st, runtime, supervisor, turn := newAgentBrowserFixture(t)
	if err := service.StreamScreencast(t.Context(), "run-missing",
		func(AgentBrowserScreencastFrameView) error { return nil }); err == nil {
		t.Fatal("live view accepted a Run the store does not have")
	}
	call := agentBrowserFixtureCall(t, service, st, toolgateway.BrowserNavigateTool,
		`{"version":"browser_navigate.v2","url":"https://example.org"}`)
	if _, err := runAgentBrowserFixtureCall(t, supervisor, turn, call); err != nil {
		t.Fatal(err)
	}
	runtime.screencastStarted = make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() {
		first <- service.StreamScreencast(ctx, st.base.run.ID,
			func(AgentBrowserScreencastFrameView) error { return nil })
	}()
	select {
	case <-runtime.screencastStarted:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("live view never attached")
	}
	if err := service.StreamScreencast(t.Context(), st.base.run.ID,
		func(AgentBrowserScreencastFrameView) error { return nil }); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("second viewer was not refused: %v", err)
	}
	cancel()
	select {
	case err := <-first:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("live view outlived its request: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("live view did not stop after its request was cancelled")
	}
	service.mu.Lock()
	attached := service.slots[st.base.run.ID] != nil && service.slots[st.base.run.ID].screencastViewer
	service.mu.Unlock()
	if attached {
		t.Fatal("viewer slot was not released after the live view ended")
	}
}
