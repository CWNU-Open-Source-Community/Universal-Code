package runner

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
)

func TestCommandRuntimeOwnershipFailureInterruptsProcess(t *testing.T) {
	for _, tc := range []struct {
		phase   string
		discard bool
	}{{"authority", false}, {"owner_renew", false}, {"authority", true}, {"owner_renew", true}} {
		name := tc.phase
		if tc.discard {
			name += "/digest_only"
		}
		t.Run(name, func(t *testing.T) {
			state := newCommandRuntimeMemoryStore()
			starter := &commandRuntimeFakeStarter{}
			manager, err := NewCommandRuntimeManager(state, starter, "ownership-failure-fixture")
			if err != nil {
				t.Fatal(err)
			}
			manager.ownerRenewEvery = 10 * time.Millisecond
			manager.ownerRenewTimeout = 100 * time.Millisecond
			var revoked atomic.Bool
			request := commandRuntimeTestRequest(manager, 2000)
			request.DiscardOutput = tc.discard
			request.DispatchCheck = func(context.Context, CommandRuntimeResolvedSpec) error {
				if revoked.Load() {
					return apperror.Wrap(apperror.CodePolicyDenied, "authorization revoked", errors.New("token=secret-value-1234567890"))
				}
				return nil
			}
			request.OwnershipCheck = func(ctx context.Context) error { return request.DispatchCheck(ctx, request.Spec) }
			job, _, err := manager.Start(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if tc.phase == "authority" {
				revoked.Store(true)
			} else {
				state.mu.Lock()
				state.failActive = true
				state.mu.Unlock()
			}
			terminal, page := waitCommandRuntimeTerminal(t, manager, job.ID)
			if terminal.State != CommandRuntimeJobInterrupted || terminal.ExitCode == nil || *terminal.ExitCode != 125 {
				t.Fatalf("did not stop revoked process: %+v", terminal)
			}
			stored, err := state.GetCommandRuntimeJob(t.Context(), job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.discard {
				if stored.Stderr != "" || len(page.Frames) != 0 || stored.OutputFramesJSON != "[]" {
					t.Fatalf("digest-only interruption retained diagnostic: %+v", stored)
				}
				return
			}
			want := "command runtime owner renewal failed: injected owner-renewal persistence failure"
			if tc.phase == "authority" {
				want = "command runtime authority recheck failed: authorization revoked"
				if !strings.Contains(stored.Stderr, "[REDACTED:secret]") {
					t.Fatalf("authority diagnostic was not redacted: %q", stored.Stderr)
				}
			}
			if !strings.Contains(stored.Stderr, want) || strings.Contains(stored.Stderr, "secret-value") ||
				stored.StderrSHA256 != commandRuntimeStringSHA256(stored.Stderr) || stored.StderrObservedBytes < int64(len(stored.Stderr)) {
				t.Fatalf("interruption diagnostic missing from durable output: %q", stored.Stderr)
			}
		})
	}
}
