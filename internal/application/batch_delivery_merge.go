package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/runmutation"
)

type MergeBatchDeliveryRequest struct {
	PlanID          string
	OrderedOrdinals []int
	ConfirmReplay   bool
	OperationKey    string
	RequestedBy     string
	WorktreeParent  string // internal/test override; not part of the HTTP DTO
	Confirm         bool
}

type MergeBatchDeliveryResult struct {
	Queue       domain.BatchDeliveryMergeQueue
	Steps       []domain.BatchDeliveryMergeStep
	BaseDrifted bool
	Replayed    bool
}

func (s *BatchDeliveryService) merge(ctx context.Context,
	request MergeBatchDeliveryRequest,
) (MergeBatchDeliveryResult, error) {
	var result MergeBatchDeliveryResult
	if s == nil || s.store == nil {
		return result, apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery store is unavailable")
	}
	request.PlanID = strings.TrimSpace(request.PlanID)
	request.RequestedBy = strings.TrimSpace(request.RequestedBy)
	operationKey, err := domain.NormalizeAgentOperationKey(strings.TrimSpace(request.OperationKey))
	if err != nil || request.PlanID == "" || request.RequestedBy == "" || !request.Confirm {
		return result, apperror.New(apperror.CodeInvalidArgument,
			"batch delivery merge requires confirmation, requester, and operation key")
	}
	plan, found, err := s.store.GetBatchDeliveryPlan(ctx, request.PlanID)
	if err != nil || !found {
		if err == nil {
			err = apperror.New(apperror.CodeNotFound, "batch delivery plan was not found")
		}
		return result, apperror.Normalize(err)
	}
	order, err := normalizeBatchMergeOrder(plan.Spec, request.OrderedOrdinals)
	if err != nil {
		return result, err
	}
	orderJSON, _ := json.Marshal(order)
	operationDigest := runmutation.OperationKeyDigest(domain.BatchDeliveryMergeQueueVersion,
		plan.ID, operationKey)
	queueID := "batchmerge-" + operationDigest[:24]
	queue, queueExists, err := s.store.GetBatchDeliveryMergeQueue(ctx, queueID)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	if queueExists {
		fingerprint := runmutation.Fingerprint("batch-delivery-merge-request.v1", plan.ID,
			plan.BaseCommit, queue.LatestBaseCommit, string(orderJSON),
			fmt.Sprint(request.ConfirmReplay), request.RequestedBy)
		if queue.OperationDigest != operationDigest || queue.RequestFingerprint != fingerprint ||
			!slices.Equal(queue.OrderedOrdinals, order) {
			return result, apperror.New(apperror.CodeConflict,
				"batch delivery merge operation key was reused for different intent")
		}
		result.Queue, result.Replayed = queue, true
		result.BaseDrifted = queue.LatestBaseCommit != plan.BaseCommit
		result.Steps, _ = s.store.ListBatchDeliveryMergeSteps(ctx, queue.ID)
		if queue.Status == domain.BatchMergeQueueCompleted {
			return result, nil
		}
		if queue.Status == domain.BatchMergeQueueBlocked || queue.Status == domain.BatchMergeQueueAborted {
			return result, apperror.New(apperror.CodeFailedPrecondition,
				"batch delivery merge queue is blocked; start a new reviewed replay operation")
		}
	} else if plan.Status != domain.BatchDeliveryReviewing &&
		plan.Status != domain.BatchDeliveryBlocked {
		return result, apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery merge requires reviewed or recoverable blocked deliveries")
	}
	workspaces, err := s.store.ListBatchDeliveryWorkspaces(ctx, plan.ID)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	receipts, err := s.loadAcceptedBatchReceipts(ctx, plan, workspaces, order)
	if err != nil {
		return result, err
	}
	if overlap := batchDeliveryChangedFileOverlap(receipts); overlap != "" {
		s.blockBatchPlan(ctx, plan, "ownership_overlap")
		return result, apperror.New(apperror.CodeConflict,
			"batch delivery merge blocked by changed-file overlap at "+overlap)
	}
	run, err := s.store.GetRun(ctx, plan.RunID)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	if run.Status != domain.RunRunning {
		return result, apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery merge requires a running Run")
	}
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	source, err := s.store.GetWorkspaceInfo(ctx, mission.WorkspaceID)
	if err != nil || source.ID != plan.WorkspaceID {
		return result, apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery source workspace binding changed")
	}
	sourceState, err := repository.Inspect(ctx, source.RootPath, source.ID)
	if err != nil || !sourceState.Available || !sourceState.Clean || sourceState.Detached ||
		sourceState.Branch != plan.SourceBranch {
		return result, apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery merge requires the clean bound source branch")
	}
	latestBase, err := repository.CurrentFullHead(ctx, source.RootPath)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	if queueExists {
		if latestBase != queue.LatestBaseCommit {
			if queue.Status == domain.BatchMergeQueuePrepared ||
				queue.Status == domain.BatchMergeQueueRunning {
				_ = s.store.BlockBatchDeliveryMergeQueue(context.WithoutCancel(ctx), queue.ID,
					queue.Status, queue.IntegrationHead, "source_drift",
					"source HEAD changed after merge queue preparation", s.now().UTC())
			}
			return result, apperror.New(apperror.CodeConflict,
				"batch delivery source changed after merge queue preparation")
		}
		latestBase = queue.LatestBaseCommit
	} else {
		result.BaseDrifted = latestBase != plan.BaseCommit
	}
	if !queueExists && result.BaseDrifted {
		ancestor, ancestryErr := repository.IsAncestor(ctx, source.RootPath,
			plan.BaseCommit, latestBase)
		if ancestryErr != nil || !ancestor {
			s.blockBatchPlan(ctx, plan, "non_linear_base_drift")
			return result, apperror.New(apperror.CodeConflict,
				"batch delivery base drift is non-linear and cannot be replayed")
		}
		if !request.ConfirmReplay {
			s.blockBatchPlan(ctx, plan, "base_drift_confirmation_required")
			return result, apperror.New(apperror.CodeConflict,
				"batch delivery source advanced; confirm latest-base replay")
		}
	}
	if !queueExists {
		requestFingerprint := runmutation.Fingerprint("batch-delivery-merge-request.v1", plan.ID,
			plan.BaseCommit, latestBase, string(orderJSON), fmt.Sprint(request.ConfirmReplay),
			request.RequestedBy)
		parent, parentErr := prepareBatchWorktreeParent(source.RootPath,
			request.WorktreeParent, operationDigest)
		if parentErr != nil {
			return result, apperror.Normalize(parentErr)
		}
		branch := fmt.Sprintf("codex/batch-%s/merge-%s", plan.OperationDigest[:12],
			operationDigest[:10])
		integrationRoot, normalizeErr := repository.NormalizeWorktreeDestination(source.RootPath,
			filepath.Join(parent, "integration"))
		if normalizeErr != nil {
			return result, apperror.Normalize(normalizeErr)
		}
		now := s.now().UTC()
		queue = domain.BatchDeliveryMergeQueue{ID: queueID,
			PlanID: plan.ID, ProtocolVersion: domain.BatchDeliveryMergeQueueVersion,
			Status: domain.BatchMergeQueuePrepared, BaseCommit: plan.BaseCommit,
			LatestBaseCommit: latestBase, IntegrationBranch: branch,
			IntegrationRoot: integrationRoot, OrderedOrdinals: order,
			OperationDigest: operationDigest, RequestFingerprint: requestFingerprint,
			CreatedBy: request.RequestedBy, CreatedAt: now, UpdatedAt: now}
		queue, result.Replayed, err = s.store.CreateBatchDeliveryMergeQueue(ctx, queue)
		if err != nil {
			return result, apperror.Normalize(err)
		}
		result.Queue = queue
	}
	if queue.Status == domain.BatchMergeQueuePrepared {
		if err := materializeBatchIntegration(ctx, source.RootPath, queue); err != nil {
			_ = s.store.BlockBatchDeliveryMergeQueue(context.WithoutCancel(ctx), queue.ID,
				domain.BatchMergeQueuePrepared, "", "integration_materialization_failed",
				err.Error(), s.now().UTC())
			return result, apperror.Wrap(apperror.CodeFailedPrecondition,
				"batch integration worktree could not be materialized", err)
		}
		if err := s.store.MarkBatchDeliveryMergeQueueRunning(ctx, queue.ID,
			queue.LatestBaseCommit, s.now().UTC()); err != nil {
			return result, apperror.Normalize(err)
		}
		queue.Status, queue.IntegrationHead = domain.BatchMergeQueueRunning,
			queue.LatestBaseCommit
		result.Queue = queue
	} else if err := materializeBatchIntegration(ctx, source.RootPath, queue); err != nil {
		_ = s.store.BlockBatchDeliveryMergeQueue(context.WithoutCancel(ctx), queue.ID,
			domain.BatchMergeQueueRunning, queue.IntegrationHead,
			"integration_recovery_failed", err.Error(), s.now().UTC())
		return result, apperror.Wrap(apperror.CodeFailedPrecondition,
			"batch integration worktree could not be recovered", err)
	}
	result, err = s.runBatchMergeQueue(ctx, source.RootPath, plan, workspaces,
		receipts, result)
	return result, apperror.Normalize(err)
}

func normalizeBatchMergeOrder(spec domain.BatchDeliverySpec, requested []int) ([]int, error) {
	order := append([]int(nil), requested...)
	if len(order) == 0 {
		order = make([]int, len(spec.Tasks))
		for index := range order {
			order[index] = index + 1
		}
	}
	if len(order) != len(spec.Tasks) {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"batch merge order must include every task exactly once")
	}
	positions := make(map[int]int, len(order))
	for index, ordinal := range order {
		if ordinal < 1 || ordinal > len(spec.Tasks) {
			return nil, apperror.New(apperror.CodeInvalidArgument,
				"batch merge order contains an invalid ordinal")
		}
		if _, duplicate := positions[ordinal]; duplicate {
			return nil, apperror.New(apperror.CodeInvalidArgument,
				"batch merge order contains a duplicate ordinal")
		}
		positions[ordinal] = index
	}
	for _, task := range spec.Tasks {
		for _, dependency := range task.DependencyOrdinals {
			if positions[dependency] >= positions[task.Ordinal] {
				return nil, apperror.New(apperror.CodeConflict,
					"batch merge order violates the durable task DAG")
			}
		}
	}
	return order, nil
}

func (s *BatchDeliveryService) loadAcceptedBatchReceipts(ctx context.Context,
	plan domain.BatchDeliveryPlan, workspaces []domain.BatchDeliveryWorkspace,
	order []int,
) (map[int]domain.BatchDeliveryReceipt, error) {
	if len(workspaces) != len(order) {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery workspace set is incomplete")
	}
	receipts := make(map[int]domain.BatchDeliveryReceipt, len(order))
	for _, ordinal := range order {
		workspace := workspaces[ordinal-1]
		if workspace.Status != domain.BatchWorkspaceAccepted {
			return nil, apperror.New(apperror.CodeFailedPrecondition,
				fmt.Sprintf("batch delivery task %d is not accepted", ordinal))
		}
		receipt, found, err := s.store.GetBatchDeliveryReceipt(ctx, plan.ID, ordinal,
			workspace.Generation)
		if err != nil || !found {
			if err == nil {
				err = apperror.New(apperror.CodeNotFound,
					"accepted batch delivery receipt was not found")
			}
			return nil, apperror.Normalize(err)
		}
		review, reviewed, err := s.store.GetBatchDeliveryReview(ctx, receipt.ID)
		if err != nil || !reviewed || review.Verdict != domain.BatchReviewAccepted ||
			review.HeadCommit != receipt.HeadCommit || review.DiffSHA256 != receipt.DiffSHA256 ||
			!review.FullDiffReviewed || !review.CallChainReviewed || !review.TestsReviewed {
			return nil, apperror.New(apperror.CodeFailedPrecondition,
				"batch delivery merge requires an independent accepted full-diff review")
		}
		receipts[ordinal] = receipt
	}
	return receipts, nil
}

func batchDeliveryChangedFileOverlap(receipts map[int]domain.BatchDeliveryReceipt) string {
	owners := make(map[string]int)
	ordinals := make([]int, 0, len(receipts))
	for ordinal := range receipts {
		ordinals = append(ordinals, ordinal)
	}
	slices.Sort(ordinals)
	for _, ordinal := range ordinals {
		for _, changed := range receipts[ordinal].ChangedFiles {
			if existing, overlap := owners[changed]; overlap && existing != ordinal {
				return changed
			}
			owners[changed] = ordinal
		}
	}
	return ""
}

// batchMergeValidationRequirements returns the cumulative declared checks for
// the integration state after the supplied merge prefix. A later delivery can
// invalidate an earlier task's contract, so running only the current task's
// checks would make prior evidence stale.
func batchMergeValidationRequirements(plan domain.BatchDeliveryPlan,
	ordinals []int,
) []domain.BatchDeliveryValidationRequirement {
	total := 0
	for _, ordinal := range ordinals {
		if ordinal >= 1 && ordinal <= len(plan.Spec.Tasks) {
			total += len(plan.Spec.Tasks[ordinal-1].Validations)
		}
	}
	result := make([]domain.BatchDeliveryValidationRequirement, 0, total)
	for _, ordinal := range ordinals {
		if ordinal >= 1 && ordinal <= len(plan.Spec.Tasks) {
			result = append(result, plan.Spec.Tasks[ordinal-1].Validations...)
		}
	}
	return result
}

func (s *BatchDeliveryService) blockBatchPlan(ctx context.Context,
	plan domain.BatchDeliveryPlan, _ string,
) {
	if plan.Status == domain.BatchDeliveryReviewing || plan.Status == domain.BatchDeliveryActive {
		_ = s.store.SetBatchDeliveryPlanStatus(context.WithoutCancel(ctx), plan.ID,
			[]domain.BatchDeliveryStatus{plan.Status}, domain.BatchDeliveryBlocked,
			s.now().UTC())
	}
}

func materializeBatchIntegration(ctx context.Context, sourceRoot string,
	queue domain.BatchDeliveryMergeQueue,
) error {
	expectedHead := queue.LatestBaseCommit
	if queue.Status == domain.BatchMergeQueueRunning && queue.IntegrationHead != "" {
		expectedHead = queue.IntegrationHead
	}
	info, err := os.Lstat(queue.IntegrationRoot)
	switch {
	case err == nil:
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("batch integration destination is not a real directory")
		}
		if queue.Status == domain.BatchMergeQueueRunning {
			// Git may have completed the next merge before its receipt was
			// persisted. Check the repository identity here; runBatchMergeQueue
			// verifies that an advanced HEAD is exactly that pending merge.
			expectedHead, err = repository.CurrentFullHead(ctx, queue.IntegrationRoot)
			if err != nil {
				return err
			}
		}
		return repository.VerifyBatchWorktreeBinding(ctx, sourceRoot, queue.IntegrationRoot,
			queue.IntegrationBranch, expectedHead, true)
	case errors.Is(err, os.ErrNotExist):
		if queue.Status == domain.BatchMergeQueueRunning {
			_, err = repository.RestoreExistingWorktree(ctx, sourceRoot,
				queue.IntegrationRoot, queue.IntegrationBranch, expectedHead)
			return err
		}
		if err := repository.CleanupInterruptedWorktree(ctx, sourceRoot,
			queue.IntegrationRoot, queue.IntegrationBranch, queue.LatestBaseCommit); err != nil {
			return err
		}
		_, err = repository.CreateWorktree(ctx, sourceRoot, queue.IntegrationRoot,
			queue.IntegrationBranch, queue.LatestBaseCommit)
		return err
	default:
		return err
	}
}

func (s *BatchDeliveryService) runBatchMergeQueue(ctx context.Context,
	sourceRoot string, plan domain.BatchDeliveryPlan,
	workspaces []domain.BatchDeliveryWorkspace,
	receipts map[int]domain.BatchDeliveryReceipt,
	result MergeBatchDeliveryResult,
) (MergeBatchDeliveryResult, error) {
	queue := result.Queue
	if queue.Status != domain.BatchMergeQueueRunning {
		return result, apperror.New(apperror.CodeFailedPrecondition,
			"batch merge queue is not running")
	}
	for index := queue.NextIndex; index < len(queue.OrderedOrdinals); index++ {
		ordinal := queue.OrderedOrdinals[index]
		receipt := receipts[ordinal]
		workspace := workspaces[ordinal-1]
		if _, err := s.activeBatchRun(ctx, plan.RunID); err != nil {
			return s.failBatchMergeWithoutStep(ctx, result, queue,
				"run_inactive", err)
		}
		if err := verifyBatchChildUnchanged(ctx, plan, workspace, receipt); err != nil {
			return s.failBatchMergeWithoutStep(ctx, result, queue,
				"child_head_drift", err)
		}
		if err := verifyBatchSourceUnchanged(ctx, sourceRoot, plan,
			queue.LatestBaseCommit); err != nil {
			return s.failBatchMergeWithoutStep(ctx, result, queue,
				"source_drift", err)
		}
		preHead := queue.IntegrationHead
		actualHead, err := repository.CurrentFullHead(ctx, queue.IntegrationRoot)
		if err != nil {
			return s.failBatchMergeWithoutStep(ctx, result, queue,
				"integration_readback_failed", err)
		}
		postHead := ""
		recovered := false
		if actualHead != preHead {
			if bindingErr := repository.VerifyBatchWorktreeBinding(ctx, sourceRoot,
				queue.IntegrationRoot, queue.IntegrationBranch, actualHead, true); bindingErr != nil {
				return s.failBatchMergeWithoutStep(ctx, result, queue,
					"integration_drift", bindingErr)
			}
			if recoveryErr := repository.VerifyBatchMergeCommit(ctx, queue.IntegrationRoot,
				queue.IntegrationBranch, preHead, receipt.HeadCommit, actualHead,
				ordinal); recoveryErr != nil {
				return s.failBatchMergeWithoutStep(ctx, result, queue,
					"integration_drift", recoveryErr)
			}
			postHead, recovered = actualHead, true
		} else {
			var conflicted bool
			postHead, conflicted, err = repository.MergeBatchDeliveryStep(ctx,
				queue.IntegrationRoot, queue.IntegrationBranch, preHead,
				receipt.HeadCommit, ordinal)
			if err != nil {
				code := "merge_failed"
				if conflicted {
					code = "text_conflict"
				}
				return s.failBatchMergeStep(ctx, result, queue, ordinal, receipt.HeadCommit,
					preHead, preHead, code, err, nil)
			}
		}
		if err := repository.VerifyBatchWorktreeBinding(ctx, sourceRoot,
			queue.IntegrationRoot, queue.IntegrationBranch, postHead, true); err != nil {
			return s.failBatchMergeWithoutStep(ctx, result, queue,
				"integration_drift", err)
		}
		if err := repository.VerifyBatchMergeCommit(ctx, queue.IntegrationRoot,
			queue.IntegrationBranch, preHead, receipt.HeadCommit, postHead,
			ordinal); err != nil {
			return s.failBatchMergeWithoutStep(ctx, result, queue,
				"integration_drift", err)
		}
		validation, validationErr := s.runBatchDeliveryValidations(ctx, plan.RunID,
			queue.IntegrationRoot, queue.LatestBaseCommit,
			batchMergeValidationRequirements(plan, queue.OrderedOrdinals[:index+1]),
			batchValidationTargetRef{PlanID: plan.ID, QueueID: queue.ID, OperationKey: queue.ID + "-validation-" + fmt.Sprint(index)})
		if err := verifyBatchIntegrationAndChildren(ctx, sourceRoot, plan, queue,
			workspaces, receipts, preHead, receipt.HeadCommit, postHead, ordinal); err != nil {
			return s.failBatchMergeWithoutStep(ctx, result, queue,
				"validation_state_drift", err)
		}
		if _, err := s.activeBatchRun(ctx, plan.RunID); err != nil {
			return s.failBatchMergeWithoutStep(ctx, result, queue,
				"run_inactive", err)
		}
		if validationErr != nil {
			if rollbackErr := repository.RollbackBatchIntegration(context.WithoutCancel(ctx),
				queue.IntegrationRoot, queue.IntegrationBranch, postHead, preHead); rollbackErr != nil {
				validationErr = errors.Join(validationErr, rollbackErr)
			}
			return s.failBatchMergeStep(ctx, result, queue, ordinal, receipt.HeadCommit,
				preHead, preHead, "semantic_validation_failed", validationErr, validation)
		}
		if sourceErr := verifyBatchSourceUnchanged(ctx, sourceRoot, plan,
			queue.LatestBaseCommit); sourceErr != nil {
			if rollbackErr := repository.RollbackBatchIntegration(context.WithoutCancel(ctx),
				queue.IntegrationRoot, queue.IntegrationBranch, postHead, preHead); rollbackErr != nil {
				sourceErr = errors.Join(sourceErr, rollbackErr)
			}
			return s.failBatchMergeStep(ctx, result, queue, ordinal, receipt.HeadCommit,
				preHead, preHead, "source_drift", sourceErr, validation)
		}
		if _, err := s.activeBatchRun(ctx, plan.RunID); err != nil {
			return s.failBatchMergeWithoutStep(ctx, result, queue,
				"run_inactive", err)
		}
		validationJSON, _ := json.Marshal(map[string]any{
			"recovered": recovered, "receipts": validation,
		})
		completedAt := s.now().UTC()
		queueStatus := domain.BatchMergeQueueRunning
		if index+1 == len(queue.OrderedOrdinals) {
			queueStatus = domain.BatchMergeQueueCompleted
		}
		step := domain.BatchDeliveryMergeStep{QueueID: queue.ID, StepIndex: index,
			Ordinal: ordinal, InputHead: receipt.HeadCommit, PreMergeHead: preHead,
			PostMergeHead: postHead, Status: domain.BatchMergeQueueCompleted,
			ValidationJSON: string(validationJSON), CreatedAt: completedAt,
			CompletedAt: &completedAt}
		if err := s.store.CompleteBatchDeliveryMergeStep(ctx, step, index+1, postHead,
			queueStatus, "", ""); err != nil {
			return result, apperror.Normalize(err)
		}
		result.Steps = append(result.Steps, step)
		queue.NextIndex, queue.IntegrationHead, queue.Status = index+1, postHead, queueStatus
		queue.UpdatedAt = completedAt
		result.Queue = queue
	}
	for ordinal, receipt := range receipts {
		if err := verifyBatchChildUnchanged(ctx, plan, workspaces[ordinal-1],
			receipt); err != nil {
			return result, apperror.New(apperror.CodeConflict,
				"batch merge completed but a child worktree was polluted")
		}
	}
	return result, nil
}

func verifyBatchSourceUnchanged(ctx context.Context, sourceRoot string,
	plan domain.BatchDeliveryPlan, expectedHead string,
) error {
	state, err := repository.Inspect(ctx, sourceRoot, plan.WorkspaceID)
	if err != nil {
		return err
	}
	if !state.Available || !state.Clean || state.Detached ||
		state.Branch != plan.SourceBranch || state.FullHead != expectedHead {
		return errors.New("batch source repository identity or clean state changed")
	}
	return nil
}

func verifyBatchIntegrationAndChildren(ctx context.Context, sourceRoot string,
	plan domain.BatchDeliveryPlan, queue domain.BatchDeliveryMergeQueue,
	workspaces []domain.BatchDeliveryWorkspace,
	receipts map[int]domain.BatchDeliveryReceipt, preHead, childHead, postHead string,
	ordinal int,
) error {
	if err := verifyBatchSourceUnchanged(ctx, sourceRoot, plan,
		queue.LatestBaseCommit); err != nil {
		return err
	}
	if err := repository.VerifyBatchWorktreeBinding(ctx, sourceRoot,
		queue.IntegrationRoot, queue.IntegrationBranch, postHead, true); err != nil {
		return err
	}
	if err := repository.VerifyBatchMergeCommit(ctx, queue.IntegrationRoot,
		queue.IntegrationBranch, preHead, childHead, postHead, ordinal); err != nil {
		return err
	}
	for childOrdinal, childReceipt := range receipts {
		if childOrdinal < 1 || childOrdinal > len(workspaces) {
			return errors.New("batch child receipt ordinal is invalid")
		}
		if err := verifyBatchChildUnchanged(ctx, plan,
			workspaces[childOrdinal-1], childReceipt); err != nil {
			return err
		}
	}
	return nil
}

func verifyBatchChildUnchanged(ctx context.Context, plan domain.BatchDeliveryPlan,
	workspace domain.BatchDeliveryWorkspace, receipt domain.BatchDeliveryReceipt,
) error {
	inspection, err := repository.InspectBatchDelivery(ctx, workspace.WorktreeRoot,
		workspace.Branch, workspace.BaseCommit, plan.Spec.Contract.MaxChangedFiles,
		plan.Spec.Contract.MaxDiffBytes)
	if err != nil {
		return err
	}
	if inspection.HeadCommit != receipt.HeadCommit ||
		inspection.DiffSHA256 != receipt.DiffSHA256 ||
		inspection.CallChainSHA256 != receipt.CallChainSHA256 ||
		!slices.Equal(inspection.ChangedFiles, receipt.ChangedFiles) {
		return errors.New("accepted child delivery changed after review")
	}
	return nil
}

func (s *BatchDeliveryService) failBatchMergeWithoutStep(ctx context.Context,
	result MergeBatchDeliveryResult, queue domain.BatchDeliveryMergeQueue,
	code string, cause error,
) (MergeBatchDeliveryResult, error) {
	summary := batchMergeFailureSummary(code)
	_ = s.store.BlockBatchDeliveryMergeQueue(context.WithoutCancel(ctx), queue.ID,
		domain.BatchMergeQueueRunning, queue.IntegrationHead, code, summary,
		s.now().UTC())
	result.Queue.Status, result.Queue.FailureCode = domain.BatchMergeQueueBlocked, code
	result.Queue.FailureSummary = summary
	return result, apperror.Wrap(apperror.CodeConflict,
		"batch delivery merge queue blocked", cause)
}

func (s *BatchDeliveryService) failBatchMergeStep(ctx context.Context,
	result MergeBatchDeliveryResult, queue domain.BatchDeliveryMergeQueue,
	ordinal int, inputHead, preHead, postHead, code string, cause error,
	validation []domain.BatchDeliveryTestReceipt,
) (MergeBatchDeliveryResult, error) {
	summary := batchMergeFailureSummary(code)
	validationJSON, _ := json.Marshal(map[string]any{"receipts": validation})
	completedAt := s.now().UTC()
	step := domain.BatchDeliveryMergeStep{QueueID: queue.ID, StepIndex: queue.NextIndex,
		Ordinal: ordinal, InputHead: inputHead, PreMergeHead: preHead,
		PostMergeHead: postHead, Status: domain.BatchMergeQueueBlocked,
		ValidationJSON: string(validationJSON), FailureCode: code,
		CreatedAt: completedAt, CompletedAt: &completedAt}
	if err := s.store.CompleteBatchDeliveryMergeStep(context.WithoutCancel(ctx), step,
		queue.NextIndex, postHead, domain.BatchMergeQueueBlocked, code, summary); err != nil {
		cause = errors.Join(cause, err)
	}
	result.Steps = append(result.Steps, step)
	result.Queue.Status, result.Queue.FailureCode = domain.BatchMergeQueueBlocked, code
	result.Queue.FailureSummary, result.Queue.IntegrationHead = summary, postHead
	return result, apperror.Wrap(apperror.CodeConflict,
		"batch delivery merge step blocked and rolled back", cause)
}

func batchMergeFailureSummary(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		code = "unknown"
	}
	return "batch merge blocked; inspect structured failure code " + code +
		" and validation output digests"
}
