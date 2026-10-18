package application

import (
	"context"
	"fmt"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

const sharedCommandServiceCheckpointReason = "a shared command service may write concurrently; checkpoint changes cannot be attributed to one operation"

type commandRuntimeJobLister interface {
	ListCommandRuntimeJobs(context.Context, runner.CommandRuntimeListFilter) ([]runner.CommandRuntimeJob, error)
}

// Use the physical root, including other Runs that share it. Terminal state
// alone is insufficient: an unreaped process can still write to the workspace.
func sharedCommandServices(ctx context.Context, store any, root string) ([]runner.CommandRuntimeJob, error) {
	reader, ok := store.(commandRuntimeJobLister)
	if !ok {
		return nil, nil
	}
	rootSHA, err := runner.CommandRuntimeWorkspaceRootSHA256(root)
	if err != nil {
		return nil, err
	}
	jobs, err := reader.ListCommandRuntimeJobs(ctx, runner.CommandRuntimeListFilter{
		WorkspaceRootSHA256: rootSHA, UnreapedOnly: true, Limit: 500,
	})
	if err != nil {
		return nil, err
	}
	if len(jobs) == 500 {
		return nil, apperror.New(apperror.CodeResourceExhausted, "shared service lookup reached its safe scan limit")
	}
	var services []runner.CommandRuntimeJob
	for _, job := range jobs {
		if job.WorkspaceRootSHA256 == rootSHA && (!job.State.Terminal() || !job.TreeReaped) &&
			runner.CommandRuntimeJobWorkspaceMode(job) == runner.CommandRuntimeWorkspaceSharedService {
			services = append(services, job)
		}
	}
	return services, nil
}

func requireSharedCommandServicesStopped(ctx context.Context, store any, root string) error {
	services, err := sharedCommandServices(ctx, store, root)
	if err != nil {
		return err
	}
	if len(services) != 0 {
		return apperror.New(apperror.CodeConflict, fmt.Sprintf(
			"workspace restore requires stopping shared service Job %q and waiting for its process tree to be reaped", services[0].ID))
	}
	return nil
}

func sharedCommandServiceCheckpointReasons(ctx context.Context, store any, root string) ([]string, error) {
	services, err := sharedCommandServices(ctx, store, root)
	if err != nil {
		return nil, err
	}
	if len(services) != 0 {
		return []string{sharedCommandServiceCheckpointReason}, nil
	}
	return nil, nil
}

// Replaying an already launched service returns its immutable Job, without
// replaying the launch checkpoint over later edits in the same workspace.
func (s *CommandRuntimeService) sealedSharedServiceBoundary(ctx context.Context, jobID, operationDigest string,
	spec runner.CommandRuntimeResolvedSpec,
) (bool, error) {
	if s.checkpoints == nil || workerCommandRuntimeFromContext(ctx) != nil ||
		spec.Spec.WorkspaceMode != runner.CommandRuntimeWorkspaceSharedService {
		return false, nil
	}
	job, err := s.store.GetCommandRuntimeJob(ctx, jobID)
	if apperror.CodeOf(apperror.Normalize(err)) == apperror.CodeNotFound || err == runner.ErrCommandRuntimeJobNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if job.OperationDigest != operationDigest || job.SpecFingerprint != runner.CommandRuntimeSpecFingerprint(spec) ||
		runner.CommandRuntimeJobWorkspaceMode(job) != runner.CommandRuntimeWorkspaceSharedService {
		return false, apperror.New(apperror.CodeConflict, "shared service operation key was reused for different launch intent")
	}
	digest := workspaceBoundaryOperationDigest(job.RunID, workspacecheckpoint.TransactionCommandBatch, operationDigest)
	transaction, found, err := s.checkpoints.store.GetWorkspaceCheckpointTransactionByOperation(ctx, digest)
	return found && transaction.Status.Terminal(), err
}
