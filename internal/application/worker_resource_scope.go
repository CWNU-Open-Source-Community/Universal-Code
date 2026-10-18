package application

import (
	"context"
	"strconv"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runmutation"
)

// A resource may outlive a continued attempt. This check keeps its original
// attribution and is only for resource liveness, never for a new tool dispatch.
func readWorkerResourceContext(ctx context.Context, base any, origin domain.AgentAttemptRef, drydocks *RunWorktreeService) (workerExecutionContext, error) {
	return readWorkerContext(ctx, base, origin, drydocks, true)
}

func validateWorkerResourceAttempt(ctx context.Context, base any, origin domain.AgentAttemptRef, child domain.AgentNode) error {
	st, ok := base.(interface {
		GetAgentAttempt(context.Context, string) (domain.AgentAttempt, bool, error)
	})
	if !ok {
		return apperror.New(apperror.CodeFailedPrecondition, "worker resource attempt reader unavailable")
	}
	attempt, found, err := st.GetAgentAttempt(ctx, origin.AttemptID)
	if err != nil {
		return err
	}
	if !found || attempt.RunID != origin.RunID || attempt.AgentID != child.ID || attempt.ParentAgentID != child.ParentID ||
		(attempt.Status != domain.AgentAttemptContinued && (attempt.Status != domain.AgentAttemptRunning || child.ActiveAttemptID != attempt.ID)) {
		return apperror.New(apperror.CodePolicyDenied, "worker resource owner is no longer active")
	}
	return nil
}

// Host-issued workspace/profile bindings distinguish resources even when a
// Batch owner rotates while retaining its directory. Attempt IDs remain on
// individual calls; they deliberately do not partition a worker's resources.
func workerResourceFingerprint(b domain.SpecialistToolBinding) string {
	return runmutation.Fingerprint("worker_resource.v1", b.Ref.RunID, b.Ref.AgentID, b.SessionID,
		b.WorkspaceID, b.RootFingerprint, b.WorkerProfileJSON, b.BatchPlanID,
		strconv.Itoa(b.BatchOrdinal), strconv.FormatInt(b.BatchGeneration, 10))
}
