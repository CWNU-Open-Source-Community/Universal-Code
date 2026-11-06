package application

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

type runLeaseBoundaryStore struct {
	acquired domain.RunExecutionLease
	acquire  func()
	renew    func(context.Context, domain.RunExecutionLease, time.Duration) (domain.RunExecutionLease, error)
	calls    atomic.Int32
}

func (s *runLeaseBoundaryStore) AcquireRunExecutionLease(context.Context, domain.AcquireRunExecutionLeaseRequest) (domain.RunExecutionLeaseAcquisition, error) {
	if s.acquire != nil {
		s.acquire()
	}
	return domain.RunExecutionLeaseAcquisition{Lease: s.acquired}, nil
}

func (s *runLeaseBoundaryStore) RenewRunExecutionLease(ctx context.Context, lease domain.RunExecutionLease, ttl time.Duration) (domain.RunExecutionLease, error) {
	s.calls.Add(1)
	return s.renew(ctx, lease, ttl)
}

func (s *runLeaseBoundaryStore) ReleaseRunExecutionLease(context.Context, domain.RunExecutionLease) (domain.RunExecutionLease, bool, error) {
	return s.acquired, true, nil
}

func (s *runLeaseBoundaryStore) GetRunExecutionLease(context.Context, string) (domain.RunExecutionLease, bool, error) {
	return s.acquired, true, nil
}

func runBoundaryLease(expiry time.Time) domain.RunExecutionLease {
	return domain.RunExecutionLease{RunID: "run-boundary", LeaseID: "lease-boundary", OwnerID: "worker-boundary",
		Generation: 1, Status: domain.RunExecutionLeaseActive, AcquiredAt: expiry.Add(-time.Second),
		RenewedAt: expiry.Add(-time.Second), ExpiresAt: expiry}
}

func TestRunLeaseExpiredAcquisitionDoesNotEnterOperation(t *testing.T) {
	// An acquisition may commit before its caller is scheduled to read it.
	st := &runLeaseBoundaryStore{acquired: runBoundaryLease(time.Now().Add(100 * time.Millisecond))}
	st.acquire = func() {
		timer := time.NewTimer(time.Until(st.acquired.ExpiresAt) + 25*time.Millisecond)
		defer timer.Stop()
		<-timer.C
	}
	var entered atomic.Bool
	done := make(chan error, 1)
	go func() {
		done <- withRunExecutionLease(t.Context(), st, st.acquired.RunID, st.acquired.OwnerID,
			RunExecutionLeasePolicy{TTL: time.Second, RenewInterval: 100 * time.Millisecond},
			func(context.Context, domain.RunExecutionLease) error { entered.Store(true); return nil })
	}()
	select {
	case err := <-done:
		if entered.Load() || !errors.Is(err, context.DeadlineExceeded) || st.calls.Load() != 0 {
			t.Fatalf("expired acquisition ran work: entered=%t renewals=%d err=%v", entered.Load(), st.calls.Load(), err)
		}
	case <-time.After(time.Second):
		t.Fatal("expired acquisition did not return")
	}
}

func TestRunLeaseExpiryCancelsBeforeLateRenewalReturns(t *testing.T) {
	st := &runLeaseBoundaryStore{acquired: runBoundaryLease(time.Now().Add(300 * time.Millisecond))}
	entered, release, cancelled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	st.renew = func(_ context.Context, lease domain.RunExecutionLease, ttl time.Duration) (domain.RunExecutionLease, error) {
		close(entered)
		// Intentionally ignore the SQL context: expiry must cancel work even
		// while a backend has not returned, and a late success cannot revive it.
		<-release
		lease.RenewedAt = time.Now()
		lease.ExpiresAt = lease.RenewedAt.Add(ttl)
		return lease, nil
	}
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		done <- withRunExecutionLease(t.Context(), st, st.acquired.RunID, st.acquired.OwnerID,
			RunExecutionLeasePolicy{TTL: time.Second, RenewInterval: 25 * time.Millisecond},
			func(ctx context.Context, _ domain.RunExecutionLease) error {
				<-ctx.Done()
				close(cancelled)
				return ctx.Err()
			})
	}()
	defer unblock()
	t.Cleanup(func() {
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("late renewal did not drain during cleanup")
		}
	})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("renewal did not enter backend")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("operation continued past confirmed expiry while renewal was blocked")
	}
	// Release through a separate gate so cleanup also runs if an assertion fails.
	unblock()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) || st.calls.Load() != 1 {
			t.Fatalf("late success revived expired lease: calls=%d err=%v", st.calls.Load(), err)
		}
	case <-time.After(time.Second):
		t.Fatal("lease did not drain after late renewal returned")
	}
}

func TestRunLeaseDefinitiveRenewalFailureDoesNotRetry(t *testing.T) {
	for _, code := range []apperror.Code{apperror.CodeConflict, apperror.CodePolicyDenied} {
		t.Run(string(code), func(t *testing.T) {
			failure := apperror.New(code, "renewal rejected")
			st := &runLeaseBoundaryStore{acquired: runBoundaryLease(time.Now().Add(time.Second))}
			st.renew = func(context.Context, domain.RunExecutionLease, time.Duration) (domain.RunExecutionLease, error) {
				return domain.RunExecutionLease{}, failure
			}
			err := withRunExecutionLease(t.Context(), st, st.acquired.RunID, st.acquired.OwnerID,
				RunExecutionLeasePolicy{TTL: time.Second, RenewInterval: 25 * time.Millisecond},
				func(ctx context.Context, _ domain.RunExecutionLease) error { <-ctx.Done(); return ctx.Err() })
			if !errors.Is(err, failure) || st.calls.Load() != 1 {
				t.Fatalf("definitive failure retried: calls=%d err=%v", st.calls.Load(), err)
			}
		})
	}
}
