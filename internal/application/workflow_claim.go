package application

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"cyberagent-workbench/internal/apperror"
)

const workflowClaimTTL = 30 * time.Second

// A short, renewed interpreter claim lets another host recover prepared work
// after a crash. The independent Run lease still owns actual worker execution.
func (s *WorkflowService) renewWorkflowClaim(ctx context.Context, id, owner string, claimStarted time.Time, stop <-chan struct{}, cancelTurn context.CancelFunc) error {
	const callBudget, retryDelay = 2 * time.Second, 100 * time.Millisecond
	validUntil := claimStarted.Add(workflowClaimTTL)
	var expired atomic.Bool
	expiry := time.AfterFunc(time.Until(validUntil), func() { expired.Store(true); cancelTurn() })
	defer expiry.Stop()
	cancelled := func() error {
		if expired.Load() {
			return fmt.Errorf("workflow execution claim reached its known expiry: %w", context.DeadlineExceeded)
		}
		return nil
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return cancelled()
		case <-stop:
			return cancelled()
		case <-ticker.C:
			for {
				if ctx.Err() != nil {
					return cancelled()
				}
				if !time.Now().Add(callBudget).Before(validUntil) {
					cancelTurn()
					return fmt.Errorf("workflow execution claim has no renewal budget before expiry: %w", context.DeadlineExceeded)
				}
				started := time.Now()
				renewCtx, cancel := context.WithTimeout(ctx, callBudget)
				renewed, err := s.store.RenewWorkflowExecutionClaim(renewCtx, id, owner)
				timedOut := errors.Is(err, context.DeadlineExceeded)
				cancel()
				if err == nil {
					if !renewed {
						cancelTurn()
						return apperror.New(apperror.CodeConflict, "workflow execution claim was lost")
					}
					if ctx.Err() != nil {
						return cancelled()
					}
					// SQL waiting consumes the TTL; only a confirmed renewal moves it.
					validUntil = started.Add(workflowClaimTTL)
					expiry.Reset(time.Until(validUntil))
					break
				}
				if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
					return cancelled()
				}
				if !timedOut || ctx.Err() != nil || !time.Now().Add(retryDelay+callBudget).Before(validUntil) {
					cancelTurn()
					return fmt.Errorf("renew workflow execution claim: %w", err)
				}
				pause := time.NewTimer(retryDelay)
				select {
				case <-ctx.Done():
					pause.Stop()
					return cancelled()
				case <-stop:
					pause.Stop()
					return cancelled()
				case <-pause.C:
				}
			}
		}
	}
}
