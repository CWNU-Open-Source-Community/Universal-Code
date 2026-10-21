package browserruntime

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestAgentBrowserConnectStartupDeadlineAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		budget     time.Duration
		parent     time.Duration
		cancelLife time.Duration
		want       time.Duration
		wantErr    error
	}{
		{"windows_budget", 30 * time.Second, 5 * time.Minute, 0, 30 * time.Second, context.DeadlineExceeded},
		{"macos_budget", 3 * time.Minute, 5 * time.Minute, 0, 3 * time.Minute, context.DeadlineExceeded},
		{"shorter_parent", 3 * time.Minute, 45 * time.Second, 0, 45 * time.Second, context.DeadlineExceeded},
		{"runtime_cancel", 3 * time.Minute, 5 * time.Minute, 2 * time.Second, 2 * time.Second, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// No endpoint is published: exercise the real discovery wait with
			// virtual time rather than sleeping for the native startup budget.
			profile := t.TempDir()
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), tc.parent)
				defer cancel()
				life, cancelLife := context.WithCancel(t.Context())
				defer cancelLife()
				if tc.cancelLife > 0 {
					time.AfterFunc(tc.cancelLife, cancelLife)
				}
				r := &AgentBrowserRuntime{
					life:    life,
					process: &retryableAgentBrowserTestProcess{done: make(chan struct{})},
				}
				started := time.Now()
				err := r.connect(ctx, profile, tc.budget)
				if !errors.Is(err, tc.wantErr) || time.Since(started) != tc.want {
					t.Fatalf("endpoint discovery ended after %s with %v; want %s and %v", time.Since(started), err, tc.want, tc.wantErr)
				}
			})
		})
	}
}
