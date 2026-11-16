//go:build windows

package desktopbrowser

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeCDPWriteCancellationReportsStartedDispatch(t *testing.T) {
	for _, started := range []bool{false, true} {
		name := "queued"
		if started {
			name = "started"
		}
		t.Run(name, func(t *testing.T) {
			ui := startProbeUI(t)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			blocked, resume := make(chan struct{}), make(chan struct{})
			var resumeOnce sync.Once
			release := func() { resumeOnce.Do(func() { close(resume) }) }
			defer release()
			var dispatches atomic.Int32
			connection := &pageConnection{
				page:    &nativePage{id: "page", core: &comObject{}, host: &Host{options: HostOptions{Dispatcher: ui.dispatcher}}},
				methods: map[string]bool{"DOM.getDocument": true},
				options: CDPOptions{Dispatch: func(context.Context, func() error) error {
					dispatches.Add(1)
					close(blocked)
					<-resume // Stay inside dispatch after cancellation reaches the caller.
					return nil
				}},
			}
			if !started {
				if err := ui.dispatcher.Post(func() { close(blocked); <-resume }); err != nil {
					t.Fatal(err)
				}
				select {
				case <-blocked:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			result := make(chan error, 1)
			go func() {
				result <- connection.WriteJSONContext(ctx, map[string]any{"id": 1, "sessionId": "page", "method": "DOM.getDocument", "params": map[string]any{}})
			}()
			if started {
				select {
				case <-blocked:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			} else {
				waitProbe(t, ctx, func() bool {
					ui.dispatcher.mu.Lock()
					defer ui.dispatcher.mu.Unlock()
					return len(ui.dispatcher.queue) > 0
				})
			}
			cancel()
			select {
			case err := <-result:
				var state interface{ WriteDispatched() bool }
				if !errors.Is(err, context.Canceled) || (errors.As(err, &state) && state.WriteDispatched()) != started {
					t.Fatalf("cancelled %s write lost dispatch state: %v", name, err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled write waited for the blocked UI task")
			}
			release()
			barrier, stop := context.WithTimeout(t.Context(), time.Second)
			defer stop()
			if err := ui.dispatcher.Invoke(barrier, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			want := int32(0)
			if started {
				want = 1
			}
			if got := dispatches.Load(); got != want {
				t.Fatalf("native dispatches = %d, want %d", got, want)
			}
		})
	}
}
