package application

import (
	"context"
	"encoding/json"

	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"
)

// Reuse only Go-recorded checks from an immutable child attempt bound to this
// exact Batch generation and final clean committed checkout. Legacy workers
// and missing/stale receipts retain the existing submission validation path.
func (s *BatchDeliveryService) reuseSpecialistBatchVerification(ctx context.Context, plan domain.BatchDeliveryPlan, child domain.BatchDeliveryWorkspace, task domain.BatchDeliveryTaskSpec, head string) ([]domain.BatchDeliveryTestReceipt, bool, error) {
	st, ok := s.store.(interface {
		ListBatchSpecialistVerificationResults(context.Context, string, int, int64, string) ([]string, error)
	})
	if !ok {
		return nil, false, nil
	}
	candidates, err := st.ListBatchSpecialistVerificationResults(ctx, plan.ID, child.Ordinal, child.Generation, child.AgentID)
	if err != nil {
		return nil, false, err
	}
	for _, raw := range candidates {
		var envelope struct {
			Result struct {
				Passed     bool                              `json:"passed"`
				HeadCommit string                            `json:"head_commit"`
				Receipts   []domain.BatchDeliveryTestReceipt `json:"receipts"`
			} `json:"result"`
		}
		if json.Unmarshal([]byte(raw), &envelope) != nil || !envelope.Result.Passed || envelope.Result.HeadCommit != head || len(envelope.Result.Receipts) != len(task.Validations) {
			continue
		}
		valid := true
		for i, receipt := range envelope.Result.Receipts {
			requirement := task.Validations[i]
			if receipt.RequirementID != requirement.ID || receipt.Kind != requirement.Kind || receipt.Scope != requirement.Scope || receipt.ExitCode != 0 || receipt.CompletedAt.IsZero() || len(receipt.OutputSHA256) != 64 {
				valid = false
				break
			}
			if requirement.Kind != domain.BatchValidationGitDiffCheck {
				var adapter commandruntimeadapter.Identity
				if s.validationRuntime != nil {
					selected, err := s.validationRuntime.projectCommandService(ctx, plan.RunID)
					if err != nil {
						return nil, false, err
					}
					adapter = selected.adapter
				} else {
					if err := s.requireBatchLocalHostValidation(ctx, plan.RunID); err != nil {
						return nil, false, err
					}
					adapter = commandruntimeadapter.HostUnsandboxed(runner.OnceExecutionProtocolVersion)
				}
				rootSHA, err := runner.CommandRuntimeWorkspaceRootSHA256(child.WorktreeRoot)
				execution := receipt.Execution
				if err != nil || execution == nil || execution.Backend != adapter.Backend || execution.BackendIdentity != adapter.BackendIdentity ||
					execution.BackendGeneration != adapter.Generation || execution.IsolationGrade != string(adapter.IsolationGrade) ||
					execution.NetworkPolicy != string(adapter.NetworkPolicy) || execution.CredentialPolicy != string(adapter.CredentialPolicy) ||
					execution.WorkspaceSHA256 != rootSHA || execution.ReadinessCheckedAt.IsZero() || !execution.TreeReaped || !execution.StdinClosed ||
					(adapter.Kind == commandruntimeadapter.KindSandboxedWorkspace && (execution.Readiness != "ready" || execution.JobID == "")) {
					valid = false
					break
				}
			}
		}
		if valid {
			return envelope.Result.Receipts, true, nil
		}
	}
	return nil, false, nil
}
