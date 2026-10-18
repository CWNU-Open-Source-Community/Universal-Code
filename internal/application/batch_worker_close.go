package application

import (
	"context"
	"errors"

	"cyberagent-workbench/internal/domain"
)

type batchWorkerResourceCloser interface {
	CloseBatchWorkerResources(domain.AgentAttemptRef) error
}

func (s *BatchDeliveryService) Merge(ctx context.Context, request MergeBatchDeliveryRequest) (MergeBatchDeliveryResult, error) {
	result, err := s.merge(ctx, request)
	if err != nil || result.Queue.Status != domain.BatchMergeQueueCompleted {
		return result, err
	}
	return result, s.closeBatchWorkers(ctx, result.Queue.PlanID)
}

// Terminal Batch plans retain their receipts, while every entry point retires
// the same worker nodes and retries any resource cleanup not yet confirmed.
func (s *BatchDeliveryService) closeBatchWorkers(ctx context.Context, planID string) error {
	nodes, err := s.store.CloseTerminalBatchWorkers(ctx, planID)
	if err != nil {
		return err
	}
	s.validationMu.Lock()
	runtime := s.workerResources
	s.validationMu.Unlock()
	var closeErr error
	if runtime != nil {
		for _, node := range nodes {
			closeErr = errors.Join(closeErr, runtime.CloseBatchWorkerResources(domain.AgentAttemptRef{RunID: node.RunID, AgentID: node.ID}))
		}
	}
	return closeErr
}

func (w *BatchDeliveryModelWorker) CloseBatchWorkerResources(ref domain.AgentAttemptRef) error {
	if w.workerRuntime != nil {
		return w.workerRuntime.closeWorkerResources(ref)
	}
	return nil
}
