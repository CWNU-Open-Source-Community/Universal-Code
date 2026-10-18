package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
)

// WithBatchDelivery shares the operator workbench's current owner tokens and
// recovery controls. Saving or reading a workflow never mints Batch authority.
func (s *WorkflowService) WithBatchDelivery(batch *BatchDeliveryWorkbenchService) *WorkflowService {
	s.batch = batch
	return s
}

type workflowBatchOutput struct {
	PlanID            string   `json:"plan_id"`
	Outcome           string   `json:"outcome"`
	ReceiptIDs        []string `json:"receipt_ids"`
	ReviewIDs         []string `json:"review_ids"`
	IntegrationBranch string   `json:"integration_branch"`
	IntegrationHead   string   `json:"integration_head"`
	SourceUpdated     bool     `json:"source_updated"`
}

// A Batch step drives an explicitly selected, already approved delivery. Review
// remains the existing independent operation. Completed output describes the
// integration branch; subsequent shared workers still use their Run workspace.
func (s *WorkflowService) advanceWorkflowBatch(ctx context.Context, e domain.WorkflowExecution, step domain.WorkflowStep, limits domain.WorkflowLimits, usage domain.RunAgentUsage, usedTurns int64) (json.RawMessage, int, string, error) {
	if s.batch == nil || step.BatchPlan == nil {
		return nil, 0, "", errors.New("workflow isolated delivery is unavailable")
	}
	ctx, cancel := context.WithDeadline(ctx, e.CreatedAt.Add(time.Duration(limits.TimeoutMillis)*time.Millisecond))
	defer cancel()
	value, err := workflowValue(e, *step.BatchPlan)
	if err != nil {
		return nil, 0, "", err
	}
	var planID string
	if json.Unmarshal(value, &planID) != nil || !domain.ValidAgentID(planID) {
		return nil, 0, "", errors.New("batch_plan must resolve to an approved delivery plan ID")
	}
	snapshot, err := s.batch.WorkbenchSnapshot(ctx, planID)
	if err != nil {
		return nil, 0, "", err
	}
	plan := snapshot.Snapshot.Plan
	if plan.RunID != e.RunID {
		return nil, 0, "", errors.New("isolated delivery belongs to another Run")
	}
	jobs := len(plan.Spec.Tasks)
	if jobs == 0 || max(e.State.AllocatedJobs, e.State.Jobs)+jobs > limits.MaxJobs {
		return nil, 0, "", errors.New("workflow job bound cannot include this isolated delivery")
	}
	if plan.Status == domain.BatchDeliveryAborted {
		return nil, 0, "", errors.New("isolated delivery was aborted")
	}
	if queue := snapshot.Snapshot.MergeQueue; queue != nil && queue.Status == domain.BatchMergeQueueCompleted {
		// Integration may have committed immediately before a host interruption.
		// Retire its workers on replay as well, before admitting a later step.
		if err := s.batch.closeBatchWorkers(ctx, plan.ID); err != nil {
			return nil, 0, "", err
		}
		output := workflowBatchOutput{PlanID: plan.ID, Outcome: "succeeded", IntegrationBranch: queue.IntegrationBranch, IntegrationHead: queue.IntegrationHead,
			ReceiptIDs: []string{}, ReviewIDs: []string{}}
		for _, receipt := range snapshot.Snapshot.Receipts {
			included := false
			for _, merged := range snapshot.Snapshot.MergeSteps {
				if merged.Status == domain.BatchMergeQueueCompleted && merged.Ordinal == receipt.Ordinal && merged.InputHead == receipt.HeadCommit {
					included = true
					break
				}
			}
			if !included {
				continue
			}
			for _, review := range snapshot.Snapshot.Reviews {
				if review.ReceiptID == receipt.ID && review.Verdict == domain.BatchReviewAccepted {
					output.ReceiptIDs = append(output.ReceiptIDs, receipt.ID)
					output.ReviewIDs = append(output.ReviewIDs, review.ID)
				}
			}
		}
		if len(output.ReceiptIDs) != jobs || len(output.ReviewIDs) != jobs {
			return nil, 0, "", errors.New("completed integration is missing accepted delivery receipts")
		}
		raw, err := json.Marshal(output)
		return raw, jobs, "", err
	}
	if usage.TotalTokens-e.UsageBefore.TotalTokens >= limits.MaxTokens || usedTurns >= limits.MaxTurns {
		return nil, 0, "", errors.New("workflow budget exhausted before isolated delivery completed")
	}
	if plan.Status == domain.BatchDeliveryBlocked || plan.Status == domain.BatchDeliveryMerging {
		return nil, 0, "isolated delivery needs inspection or integration recovery: " + plan.ID, nil
	}
	waiting := ""
	accepted := map[int]bool{}
	for _, workspace := range snapshot.Snapshot.Workspaces {
		accepted[workspace.Ordinal] = workspace.Status == domain.BatchWorkspaceAccepted || workspace.Status == domain.BatchWorkspaceMerged
	}
	for index, workspace := range snapshot.Snapshot.Workspaces {
		if accepted[workspace.Ordinal] {
			continue
		}
		switch workspace.Status {
		case domain.BatchWorkspaceReadyForReview:
			waiting = "isolated delivery awaits independent review: " + plan.ID
			continue
		case domain.BatchWorkspaceChangesRequested, domain.BatchWorkspaceQuestion, domain.BatchWorkspaceOrphaned, domain.BatchWorkspaceCancelled, domain.BatchWorkspaceFailed, domain.BatchWorkspacePreparing:
			waiting = "isolated delivery needs review feedback or owner recovery: " + plan.ID
			continue
		}
		if index >= len(snapshot.Children) || !snapshot.Children[index].OwnerAvailable || snapshot.Children[index].OutcomeUnresolved {
			waiting = "isolated delivery owner or execution outcome needs recovery: " + plan.ID
			continue
		}
		if snapshot.Children[index].Executing {
			waiting = "isolated worker is running: " + plan.ID
			continue
		}
		task := plan.Spec.Tasks[workspace.Ordinal-1]
		ready := true
		for _, dependency := range task.DependencyOrdinals {
			ready = ready && accepted[dependency]
		}
		if !ready {
			waiting = "isolated worker awaits an accepted dependency: " + plan.ID
			continue
		}
		child, err := s.store.GetAgentNode(ctx, workspace.AgentID)
		if err != nil {
			return nil, 0, "", err
		}
		_, pendingApproval, err := batchWorkerApprovalWait(ctx, s.store, child)
		if err != nil {
			return nil, 0, "", err
		}
		if pendingApproval {
			waiting = "isolated worker awaits tool approval: " + plan.ID
			continue
		}
		if child.TokenLimit-child.TokensUsed > limits.MaxTokens-(usage.TotalTokens-e.UsageBefore.TotalTokens) || child.TurnLimit-child.TurnsUsed > limits.MaxTurns-usedTurns {
			return nil, 0, "", errors.New("workflow remaining budget cannot cover the isolated worker's admission")
		}
		updated, err := s.batch.ExecuteWorkbench(ctx, ExecuteBatchDeliveryWorkbenchRequest{PlanID: plan.ID, Ordinal: workspace.Ordinal, ExpectedGeneration: workspace.Generation, Confirm: true,
			OperationKey: "workflow-batch-" + runmutation.Fingerprint(plan.ID, strconv.Itoa(workspace.Ordinal), strconv.FormatInt(workspace.Generation, 10))})
		if err != nil {
			return nil, 0, "isolated execution stopped; inspect its persisted outcome: " + plan.ID + ": " + redact.String(err.Error()), nil
		}
		for _, current := range updated.Children {
			if current.Ordinal == int64(workspace.Ordinal) && current.WaitingApproval {
				return nil, 0, "isolated worker awaits tool approval: " + plan.ID, nil
			}
		}
		return nil, 0, "", nil
	}
	if waiting != "" {
		return nil, 0, waiting, nil
	}
	if len(accepted) != jobs {
		return nil, 0, "", errors.New("isolated delivery workspace set changed")
	}
	_, err = s.batch.Merge(ctx, MergeBatchDeliveryRequest{PlanID: plan.ID, Confirm: true, RequestedBy: "workflow_host",
		OperationKey: "workflow-integrate-" + runmutation.Fingerprint(plan.ID)})
	if err != nil {
		return nil, 0, fmt.Sprintf("isolated integration requires inspection: %s: %s", plan.ID, redact.String(err.Error())), nil
	}
	return nil, 0, "", nil
}
