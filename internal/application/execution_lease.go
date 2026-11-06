package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

func withRunExecutionLease(ctx context.Context, store RunExecutionLeaseStore,
	runID string, ownerID string, leasePolicy RunExecutionLeasePolicy,
	operation func(context.Context, domain.RunExecutionLease) error,
) error {
	if store == nil || operation == nil {
		return apperror.New(apperror.CodeFailedPrecondition,
			"run execution lease dependencies are required")
	}
	if err := leasePolicy.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeFailedPrecondition,
			"invalid run execution lease policy", err)
	}
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return apperror.New(apperror.CodeFailedPrecondition,
			"run execution lease owner is required")
	}
	acquired, err := store.AcquireRunExecutionLease(ctx,
		domain.AcquireRunExecutionLeaseRequest{
			RunID: strings.TrimSpace(runID), OwnerID: ownerID, TTL: leasePolicy.TTL,
		})
	if err != nil {
		return apperror.Normalize(err)
	}
	leaseCtx, cancelLease := context.WithCancel(ctx)
	stop := make(chan struct{})
	done := make(chan struct{})
	heartbeatErr := make(chan error, 1)
	expired := make(chan struct{})
	expiryErr := apperror.Wrap(apperror.CodeDeadlineExceeded,
		"run execution lease reached its confirmed expiry", context.DeadlineExceeded)
	expiry := time.AfterFunc(time.Until(acquired.Lease.ExpiresAt), func() {
		defer close(expired)
		select {
		case heartbeatErr <- expiryErr:
		default:
		}
		cancelLease()
	})
	go func() {
		defer close(done)
		renewRunExecutionLease(leaseCtx, store, acquired.Lease, leasePolicy,
			stop, heartbeatErr, cancelLease, expiry, expired)
	}()
	var operationErr error
	if !acquired.Lease.ActiveAt(time.Now()) {
		operationErr = expiryErr
	} else {
		operationErr = operation(leaseCtx, acquired.Lease)
	}
	close(stop)
	cancelLease()
	<-done
	if !expiry.Stop() {
		<-expired
	}
	var renewalErr error
	select {
	case renewalErr = <-heartbeatErr:
	default:
	}
	releaseCtx, cancelRelease := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	_, _, releaseErr := store.ReleaseRunExecutionLease(releaseCtx, acquired.Lease)
	cancelRelease()
	if renewalErr != nil {
		return errors.Join(apperror.Normalize(renewalErr), operationErr, releaseErr)
	}
	return errors.Join(operationErr, releaseErr)
}

func renewRunExecutionLease(ctx context.Context, store RunExecutionLeaseStore,
	lease domain.RunExecutionLease, policy RunExecutionLeasePolicy, stop <-chan struct{},
	heartbeatErr chan<- error, cancel context.CancelFunc, expiry *time.Timer, expired <-chan struct{},
) {
	ticker := time.NewTicker(policy.RenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			for {
				if ctx.Err() != nil {
					return
				}
				timeout := minDuration(runExecutionLeaseRenewalTimeout(policy), time.Until(lease.ExpiresAt))
				renewCtx, cancelRenew := context.WithTimeout(ctx, timeout)
				renewed, err := store.RenewRunExecutionLease(renewCtx, lease, policy.TTL)
				cancelRenew()
				if ctx.Err() != nil {
					return
				}
				if err == nil && !time.Now().Before(lease.ExpiresAt) {
					err = context.DeadlineExceeded
				}
				if err == nil {
					// Only the store's confirmed expiry extends our renewal budget.
					lease = renewed
					// A late reply cannot undo cancellation at the previous expiry.
					if !expiry.Stop() {
						<-expired
						return
					}
					expiry.Reset(time.Until(lease.ExpiresAt))
					break
				}
				if errors.Is(err, context.DeadlineExceeded) && time.Now().Before(lease.ExpiresAt) {
					pause := time.NewTimer(minDuration(250*time.Millisecond, time.Until(lease.ExpiresAt)))
					select {
					case <-ctx.Done():
						pause.Stop()
						return
					case <-stop:
						pause.Stop()
						return
					case <-pause.C:
					}
					continue
				}
				select {
				case heartbeatErr <- err:
				default:
				}
				cancel()
				return
			}
		}
	}
}

func runExecutionLeaseRenewalTimeout(policy RunExecutionLeasePolicy) time.Duration {
	return minDuration(2*time.Second, policy.TTL-policy.RenewInterval)
}

func minDuration(left time.Duration, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}
