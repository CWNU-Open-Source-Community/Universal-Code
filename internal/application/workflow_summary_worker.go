package application

import (
	"context"
	"time"

	"cyberagent-workbench/internal/idgen"
)

// Process at most one notification per tick, then let the interpreter advance.
// Backoff orders failed deliveries behind other ready completions. A service
// without a conversation handoff leaves its obligations for a capable host.
func (s *WorkflowService) runDueWorkflowSummary(ctx context.Context, now time.Time) (bool, error) {
	if s.workers.summaryHandoff == nil {
		return false, nil
	}
	candidates, err := s.store.ListWorkflowSummaryCandidates(ctx, now, 32)
	if err != nil {
		return false, err
	}
	for _, execution := range candidates {
		attempted, err := s.deliverWorkflowSummary(ctx, execution, idgen.New("workflowsummary"), now)
		if attempted || err != nil {
			return attempted, err
		}
	}
	return false, nil
}
