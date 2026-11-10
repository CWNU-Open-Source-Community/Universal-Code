package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cyberagent-workbench/internal/apperror"
)

// A short, renewed interpreter claim lets another host recover prepared work
// after a crash. The independent Run lease still owns actual worker execution.
func (s *WorkflowService) renewWorkflowClaim(ctx context.Context, id, owner string, stop <-chan struct{}, cancelTurn context.CancelFunc) error {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-stop:
			return nil
		case <-ticker.C:
			renewCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			renewed, err := s.store.RenewWorkflowExecutionClaim(renewCtx, id, owner)
			cancel()
			if err != nil {
				if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
					return nil
				}
				cancelTurn()
				return fmt.Errorf("renew workflow execution claim: %w", err)
			}
			if !renewed {
				cancelTurn()
				return apperror.New(apperror.CodeConflict, "workflow execution claim was lost")
			}
		}
	}
}
