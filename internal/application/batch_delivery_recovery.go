package application

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/session"
)

type CancelBatchDeliveryRequest struct {
	PlanID       string
	OperationKey string
	RequestedBy  string
	Reason       string
	Confirm      bool
}

type CancelBatchDeliveryResult struct {
	Snapshot             BatchDeliverySnapshot
	PreservedOrdinals    []int
	IntegrationPreserved bool
	Replayed             bool
}

// Cancel fences every child generation first in durable state and removes
// only clean, exactly bound worktrees. Dirty or identity-drifted directories
// are left in place and marked orphaned for explicit human recovery.
func (s *BatchDeliveryService) Cancel(ctx context.Context,
	request CancelBatchDeliveryRequest,
) (CancelBatchDeliveryResult, error) {
	var result CancelBatchDeliveryResult
	request.PlanID = strings.TrimSpace(request.PlanID)
	request.RequestedBy = strings.TrimSpace(request.RequestedBy)
	request.Reason = strings.TrimSpace(request.Reason)
	operationKey, err := domain.NormalizeAgentOperationKey(strings.TrimSpace(request.OperationKey))
	if s == nil || s.store == nil {
		return result, apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery store is unavailable")
	}
	if err != nil || request.PlanID == "" || request.RequestedBy == "" ||
		request.Reason == "" || len([]rune(request.Reason)) > domain.MaxBatchMailboxSummaryRunes-256 ||
		!request.Confirm {
		return result, apperror.New(apperror.CodeInvalidArgument,
			"batch delivery cancellation requires confirmation, requester, reason, and operation key")
	}
	plan, found, err := s.store.GetBatchDeliveryPlan(ctx, request.PlanID)
	if err != nil || !found {
		if err == nil {
			err = apperror.New(apperror.CodeNotFound, "batch delivery plan was not found")
		}
		return result, apperror.Normalize(err)
	}
	if plan.Status == domain.BatchDeliveryCompleted {
		return result, apperror.New(apperror.CodeFailedPrecondition,
			"completed batch delivery cannot be cancelled")
	}
	result.Replayed = plan.Status == domain.BatchDeliveryAborted
	source, err := s.batchDeliverySource(ctx, plan)
	if err != nil {
		return result, err
	}
	workspaces, err := s.store.ListBatchDeliveryWorkspaces(ctx, plan.ID)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	// Aborted is the durable no-new-execution fence. Cancellation can be
	// retried after unconfirmed process cleanup; nonterminal workspaces remain
	// in the ledger until their exact resources and directories are cleaned.
	s.validationMu.Lock()
	fresh, freshFound, fenceErr := s.store.GetBatchDeliveryPlan(ctx, plan.ID)
	if fenceErr == nil && (!freshFound || fresh.Status == domain.BatchDeliveryCompleted) {
		fenceErr = apperror.New(apperror.CodeConflict, "batch delivery cancellation plan changed")
	}
	if fenceErr == nil && fresh.Status != domain.BatchDeliveryAborted {
		fenceErr = s.store.SetBatchDeliveryPlanStatus(ctx, plan.ID, []domain.BatchDeliveryStatus{fresh.Status}, domain.BatchDeliveryAborted, s.now().UTC())
	}
	s.validationMu.Unlock()
	if fenceErr != nil {
		return result, fenceErr
	}
	if err := s.stopBatchValidationJobs(ctx, plan.ID); err != nil {
		return result, apperror.Wrap(apperror.CodeFailedPrecondition, "batch validation cleanup is unconfirmed; worktrees preserved", err)
	}
	if err := s.requireBatchValidationCleanupQuiescent(ctx, plan, workspaces); err != nil {
		return result, err
	}
	var joined error
	for _, workspace := range workspaces {
		if workspace.Status.Terminal() {
			if workspace.Status == domain.BatchWorkspaceOrphaned {
				result.PreservedOrdinals = append(result.PreservedOrdinals, workspace.Ordinal)
			}
			continue
		}
		finalStatus, head, summary := cleanupBatchChildWorktree(ctx, source.RootPath,
			workspace)
		if finalStatus == domain.BatchWorkspaceOrphaned {
			result.PreservedOrdinals = append(result.PreservedOrdinals, workspace.Ordinal)
		}
		now := s.now().UTC()
		message := batchDeliveryMessage(plan.ID, workspace.Ordinal, workspace.Generation,
			domain.BatchMailboxAborted, request.RequestedBy,
			request.Reason+"; "+summary, nil,
			operationKey+fmt.Sprintf("-abort-task-%d", workspace.Ordinal), now)
		if _, _, _, abortErr := s.store.AbortBatchDeliveryWorkspace(
			context.WithoutCancel(ctx), message, finalStatus, head); abortErr != nil {
			joined = errors.Join(joined, fmt.Errorf("cancel task %d: %w",
				workspace.Ordinal, abortErr))
		}
	}
	if queue, exists, queueErr := s.store.GetBatchDeliveryMergeQueueByPlan(ctx,
		plan.ID); queueErr != nil {
		joined = errors.Join(joined, queueErr)
	} else if exists && queue.Status != domain.BatchMergeQueueCompleted &&
		queue.Status != domain.BatchMergeQueueAborted {
		preserved, cleanupErr := cleanupBatchIntegrationWorktree(ctx, source.RootPath, queue)
		result.IntegrationPreserved = preserved
		if cleanupErr != nil {
			joined = errors.Join(joined, cleanupErr)
		}
		if abortErr := s.store.AbortBatchDeliveryMergeQueue(context.WithoutCancel(ctx),
			queue.ID, queue.Status, queue.IntegrationHead,
			request.Reason, s.now().UTC()); abortErr != nil {
			joined = errors.Join(joined, abortErr)
		}
	}
	if joined != nil {
		return result, apperror.Wrap(apperror.CodeFailedPrecondition,
			"batch delivery cancellation preserved unresolved state", joined)
	}
	fresh, found, err = s.store.GetBatchDeliveryPlan(ctx, plan.ID)
	if err != nil || !found {
		return result, apperror.Normalize(err)
	}
	if fresh.Status != domain.BatchDeliveryAborted {
		if err := s.store.SetBatchDeliveryPlanStatus(ctx, fresh.ID,
			[]domain.BatchDeliveryStatus{fresh.Status}, domain.BatchDeliveryAborted,
			s.now().UTC()); err != nil {
			return result, apperror.Normalize(err)
		}
	}
	result.Snapshot, err = s.Snapshot(ctx, plan.ID)
	return result, err
}

func cleanupBatchChildWorktree(ctx context.Context, sourceRoot string,
	workspace domain.BatchDeliveryWorkspace,
) (domain.BatchDeliveryWorkspaceStatus, string, string) {
	info, statErr := os.Lstat(workspace.WorktreeRoot)
	if errors.Is(statErr, os.ErrNotExist) {
		if err := repository.CleanupInterruptedWorktree(ctx, sourceRoot,
			workspace.WorktreeRoot, workspace.Branch, workspace.BaseCommit); err == nil {
			return domain.BatchWorkspaceCancelled, workspace.BaseCommit,
				"base-only branch and missing worktree reconciled"
		}
		head, err := repository.BranchFullHead(ctx, sourceRoot, workspace.Branch)
		if err == nil {
			if ancestor, ancestryErr := repository.IsAncestor(ctx, sourceRoot,
				workspace.BaseCommit, head); ancestryErr == nil && ancestor {
				return domain.BatchWorkspaceCancelled, head,
					"missing worktree reconciled; committed branch preserved"
			}
		}
		return domain.BatchWorkspaceOrphaned, "",
			"missing worktree or branch identity drifted; evidence preserved"
	}
	if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return domain.BatchWorkspaceOrphaned, "",
			"worktree identity is unsafe; path preserved"
	}
	branch, branchErr := repository.CurrentBranch(ctx, workspace.WorktreeRoot)
	head, headErr := repository.CurrentFullHead(ctx, workspace.WorktreeRoot)
	if branchErr != nil || headErr != nil || branch != workspace.Branch {
		return domain.BatchWorkspaceOrphaned, "",
			"worktree branch identity drifted; directory preserved"
	}
	ancestor, ancestryErr := repository.IsAncestor(ctx, workspace.WorktreeRoot,
		workspace.BaseCommit, head)
	if ancestryErr != nil || !ancestor {
		return domain.BatchWorkspaceOrphaned, head,
			"worktree ancestry drifted; directory preserved"
	}
	if err := repository.RemoveWorktreeKeepBranch(ctx, sourceRoot,
		workspace.WorktreeRoot, workspace.Branch, head); err != nil {
		return domain.BatchWorkspaceOrphaned, head,
			"dirty or unavailable worktree preserved for manual recovery"
	}
	if head == workspace.BaseCommit {
		_ = repository.CleanupInterruptedWorktree(context.WithoutCancel(ctx), sourceRoot,
			workspace.WorktreeRoot, workspace.Branch, workspace.BaseCommit)
		return domain.BatchWorkspaceCancelled, head,
			"clean base-only worktree and branch removed"
	}
	return domain.BatchWorkspaceCancelled, head,
		"clean worktree removed; committed branch preserved"
}

func cleanupBatchIntegrationWorktree(ctx context.Context, sourceRoot string,
	queue domain.BatchDeliveryMergeQueue,
) (bool, error) {
	info, err := os.Lstat(queue.IntegrationRoot)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return true, nil
	}
	head, headErr := repository.CurrentFullHead(ctx, queue.IntegrationRoot)
	branch, branchErr := repository.CurrentBranch(ctx, queue.IntegrationRoot)
	if headErr != nil || branchErr != nil || branch != queue.IntegrationBranch {
		return true, nil
	}
	if err := repository.RemoveWorktreeKeepBranch(ctx, sourceRoot,
		queue.IntegrationRoot, queue.IntegrationBranch, head); err != nil {
		return true, nil
	}
	return false, nil
}

func (s *BatchDeliveryService) batchDeliverySource(ctx context.Context,
	plan domain.BatchDeliveryPlan,
) (session.WorkspaceInfo, error) {
	var empty session.WorkspaceInfo
	run, err := s.store.GetRun(ctx, plan.RunID)
	if err != nil {
		return empty, apperror.Normalize(err)
	}
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return empty, apperror.Normalize(err)
	}
	source, err := s.store.GetWorkspaceInfo(ctx, mission.WorkspaceID)
	if err != nil || source.ID != plan.WorkspaceID {
		return empty, apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery source workspace binding changed")
	}
	return source, nil
}

type BatchDeliveryReconcileResult struct {
	PlanID                 string
	MaterializedWorktrees  int
	RecoveredWorktrees     int
	Expired                bool
	MergeResumed           bool
	MergeCompleted         bool
	NeedsOperatorAttention bool
}

// Reconcile converges durable worktree and merge-queue intents after restart.
// It does not rotate owner generations or mint replacement bearer tokens.
func (s *BatchDeliveryService) Reconcile(ctx context.Context,
	planID string,
) (BatchDeliveryReconcileResult, error) {
	result := BatchDeliveryReconcileResult{PlanID: strings.TrimSpace(planID)}
	if s == nil || s.store == nil || result.PlanID == "" {
		return result, apperror.New(apperror.CodeInvalidArgument,
			"batch delivery reconciliation plan is invalid")
	}
	plan, found, err := s.store.GetBatchDeliveryPlan(ctx, result.PlanID)
	if err != nil || !found {
		if err == nil {
			err = apperror.New(apperror.CodeNotFound, "batch delivery plan was not found")
		}
		return result, apperror.Normalize(err)
	}
	if plan.Status.Terminal() {
		return result, nil
	}
	run, err := s.store.GetRun(ctx, plan.RunID)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	if run.Status != domain.RunRunning {
		// Persisted queue/worktree facts survive restart, but lifecycle authority
		// does not. Leave uncertain resources untouched for an explicit operator
		// decision instead of reviving work after pause/cancellation/completion.
		result.NeedsOperatorAttention = true
		return result, nil
	}
	source, err := s.batchDeliverySource(ctx, plan)
	if err != nil {
		return result, err
	}
	workspaces, err := s.store.ListBatchDeliveryWorkspaces(ctx, plan.ID)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	now := s.now().UTC()
	for _, workspace := range workspaces {
		if batchWorkspaceLeaseActive(workspace.Status) && !now.Before(workspace.LeaseExpiresAt) {
			result.Expired = true
			cancelled, cancelErr := s.Cancel(context.WithoutCancel(ctx),
				CancelBatchDeliveryRequest{PlanID: plan.ID,
					OperationKey: "batch-reconcile-expired-" + plan.ID,
					RequestedBy:  "startup_reconciler", Reason: "child lease expired",
					Confirm: true})
			result.NeedsOperatorAttention = len(cancelled.PreservedOrdinals) != 0 ||
				cancelled.IntegrationPreserved
			return result, cancelErr
		}
	}
	if plan.Status == domain.BatchDeliveryPreparing || plan.Status == domain.BatchDeliveryBlocked {
		before := 0
		for _, workspace := range workspaces {
			if workspace.Status == domain.BatchWorkspacePreparing {
				before++
			}
		}
		if before > 0 {
			prepared := PrepareBatchDeliveryResult{Plan: plan, Workspaces: workspaces,
				Replayed: true}
			prepared, reconcileErr := s.reconcileMaterialization(ctx, source.RootPath, prepared)
			for _, workspace := range prepared.Workspaces {
				if workspace.Status == domain.BatchWorkspaceDispatched {
					result.MaterializedWorktrees++
				}
			}
			if result.MaterializedWorktrees > before {
				result.MaterializedWorktrees = before
			}
			if reconcileErr != nil {
				result.NeedsOperatorAttention = true
				return result, apperror.Normalize(reconcileErr)
			}
			plan = prepared.Plan
			workspaces = prepared.Workspaces
		}
	}
	for index, workspace := range workspaces {
		if workspace.Status == domain.BatchWorkspacePreparing || workspace.Status.Terminal() {
			continue
		}
		recovered, recoverErr := reconcileBatchChildWorktree(ctx, source.RootPath,
			workspace)
		if recoverErr != nil {
			result.NeedsOperatorAttention = true
			return result, apperror.Wrap(apperror.CodeFailedPrecondition,
				fmt.Sprintf("batch delivery task %d recovery failed", workspace.Ordinal),
				recoverErr)
		}
		if recovered.HeadCommit != workspace.HeadCommit && recovered.HeadCommit != "" {
			if err := s.store.SetBatchDeliveryWorkspaceStatus(ctx, plan.ID,
				workspace.Ordinal, workspace.Generation, workspace.Status, workspace.Status,
				recovered.HeadCommit, s.now().UTC()); err != nil {
				return result, apperror.Normalize(err)
			}
			workspaces[index].HeadCommit = recovered.HeadCommit
		}
		if recovered.Rematerialized {
			result.RecoveredWorktrees++
		}
	}
	queue, exists, err := s.store.GetBatchDeliveryMergeQueueByPlan(ctx, plan.ID)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	if !exists || (queue.Status != domain.BatchMergeQueuePrepared &&
		queue.Status != domain.BatchMergeQueueRunning) {
		return result, nil
	}
	result.MergeResumed = true
	mergeResult, err := s.resumeBatchMergeQueue(ctx, source.RootPath, plan,
		workspaces, queue)
	if mergeResult.Queue.Status == domain.BatchMergeQueueCompleted {
		result.MergeCompleted = true
	}
	if err != nil {
		result.NeedsOperatorAttention = true
	}
	return result, err
}

type reconciledBatchChild struct {
	HeadCommit     string
	Rematerialized bool
}

func reconcileBatchChildWorktree(ctx context.Context, sourceRoot string,
	workspace domain.BatchDeliveryWorkspace,
) (reconciledBatchChild, error) {
	var result reconciledBatchChild
	info, err := os.Lstat(workspace.WorktreeRoot)
	if errors.Is(err, os.ErrNotExist) {
		head, headErr := repository.BranchFullHead(ctx, sourceRoot, workspace.Branch)
		if headErr != nil {
			return result, headErr
		}
		ancestor, ancestryErr := repository.IsAncestor(ctx, sourceRoot,
			workspace.BaseCommit, head)
		if ancestryErr != nil || !ancestor {
			return result, errors.New("batch child branch no longer descends from its base")
		}
		if _, restoreErr := repository.RestoreExistingWorktree(ctx, sourceRoot,
			workspace.WorktreeRoot, workspace.Branch, head); restoreErr != nil {
			return result, restoreErr
		}
		return reconciledBatchChild{HeadCommit: head, Rematerialized: true}, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return result, errors.New("batch child worktree identity is unsafe")
	}
	branch, err := repository.CurrentBranch(ctx, workspace.WorktreeRoot)
	if err != nil || branch != workspace.Branch {
		return result, errors.New("batch child worktree branch drifted")
	}
	head, err := repository.CurrentFullHead(ctx, workspace.WorktreeRoot)
	if err != nil {
		return result, err
	}
	ancestor, err := repository.IsAncestor(ctx, workspace.WorktreeRoot,
		workspace.BaseCommit, head)
	if err != nil || !ancestor {
		return result, errors.New("batch child worktree ancestry drifted")
	}
	return reconciledBatchChild{HeadCommit: head}, nil
}

func batchWorkspaceLeaseActive(status domain.BatchDeliveryWorkspaceStatus) bool {
	switch status {
	case domain.BatchWorkspacePreparing, domain.BatchWorkspaceDispatched,
		domain.BatchWorkspaceAcknowledged, domain.BatchWorkspaceWorking,
		domain.BatchWorkspaceQuestion, domain.BatchWorkspaceChangesRequested:
		return true
	default:
		return false
	}
}

func (s *BatchDeliveryService) resumeBatchMergeQueue(ctx context.Context,
	sourceRoot string, plan domain.BatchDeliveryPlan,
	workspaces []domain.BatchDeliveryWorkspace, queue domain.BatchDeliveryMergeQueue,
) (MergeBatchDeliveryResult, error) {
	result := MergeBatchDeliveryResult{Queue: queue, Replayed: true,
		BaseDrifted: queue.LatestBaseCommit != plan.BaseCommit}
	result.Steps, _ = s.store.ListBatchDeliveryMergeSteps(ctx, queue.ID)
	sourceHead, err := repository.CurrentFullHead(ctx, sourceRoot)
	if err != nil || sourceHead != queue.LatestBaseCommit {
		if err == nil {
			err = errors.New("source HEAD changed while merge queue was interrupted")
		}
		return s.failBatchMergeWithoutStep(ctx, result, queue, "source_drift", err)
	}
	receipts, err := s.loadAcceptedBatchReceipts(ctx, plan, workspaces,
		queue.OrderedOrdinals)
	if err != nil {
		return result, err
	}
	if err := materializeBatchIntegration(ctx, sourceRoot, queue); err != nil {
		return s.failBatchMergeWithoutStep(ctx, result, queue,
			"integration_recovery_failed", err)
	}
	if queue.Status == domain.BatchMergeQueuePrepared {
		if err := s.store.MarkBatchDeliveryMergeQueueRunning(ctx, queue.ID,
			queue.LatestBaseCommit, s.now().UTC()); err != nil {
			return result, apperror.Normalize(err)
		}
		queue.Status, queue.IntegrationHead = domain.BatchMergeQueueRunning,
			queue.LatestBaseCommit
		result.Queue = queue
	}
	return s.runBatchMergeQueue(ctx, sourceRoot, plan, workspaces, receipts, result)
}

func (s *BatchDeliveryService) ReconcileStartup(ctx context.Context,
	limit int,
) ([]BatchDeliveryReconcileResult, error) {
	if s == nil || s.store == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery store is unavailable")
	}
	plans, err := s.store.ListRecoverableBatchDeliveryPlans(ctx, limit)
	if err != nil {
		return nil, apperror.Normalize(err)
	}
	results := make([]BatchDeliveryReconcileResult, 0, len(plans))
	var joined error
	for _, plan := range plans {
		result, reconcileErr := s.Reconcile(ctx, plan.ID)
		results = append(results, result)
		if reconcileErr != nil {
			joined = errors.Join(joined, fmt.Errorf("plan %s: %w", plan.ID, reconcileErr))
		}
	}
	return results, joined
}
