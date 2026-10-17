package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/standardcode"
	"cyberagent-workbench/internal/workspace"
)

const workerCommandDockerPrefix = "native-worker-command/v1/"

func workerCommandDockerRequester(proof *workerCommandRuntimeProof) string {
	return workerCommandDockerPrefix + domain.SpecialistToolApprovalID(proof.binding.worker.Ref, proof.scope.SpecialistToolCallID)
}

func isWorkerCommandDockerRequester(value string) bool {
	return value == "native-worker-command" || strings.HasPrefix(value, "native-worker-command/")
}

func workerCommandDockerContext(ctx context.Context, scope runner.CommandRuntimeScope,
	spec runner.CommandRuntimeResolvedSpec, command standardcode.Command,
) (context.Context, error) {
	proof := workerCommandRuntimeFromContext(ctx)
	if proof == nil {
		return ctx, nil
	}
	if batchValidationRuntimeFromContext(ctx) != nil || gitBisectRuntimeFromContext(ctx) != nil {
		return nil, runner.ErrCommandRuntimeBoundary
	}
	if err := proof.check(ctx, scope, spec.WorkspaceRoot); err != nil {
		return nil, err
	}
	copy := *proof
	copy.nativeScope = scope
	var err error
	copy.commandSHA256, err = command.Fingerprint()
	if err != nil {
		return nil, err
	}
	return context.WithValue(ctx, workerCommandRuntimeKey{}, &copy), nil
}

func workerCommandDockerWorkspace(ctx context.Context, binding sandbox.DockerStandardCodeRunnerBinding,
	owned sandbox.WorkspaceBinding,
) (sandbox.WorkspaceBinding, error) {
	proof := workerCommandRuntimeFromContext(ctx)
	if proof == nil {
		return owned, nil
	}
	if proof.commandSHA256 == "" || proof.commandSHA256 != binding.CommandSHA256 ||
		proof.scope.RunID != binding.RunID || proof.binding.run.SessionID != binding.SessionID {
		return sandbox.WorkspaceBinding{}, fmt.Errorf("%w: worker Docker command fingerprint or Run resource binding changed", runner.ErrCommandRuntimeBoundary)
	}
	if err := proof.check(ctx, proof.nativeScope, proof.binding.rootPath); err != nil {
		return sandbox.WorkspaceBinding{}, err
	}
	owned.RootPath = proof.binding.rootPath
	return owned, nil
}

// Cleanup can read the immutable original target after Stop or lease loss.
// It cannot recreate a worker proof or authorize another container launch.
func (s *DockerSandboxService) workerCommandCleanupWorkspace(ctx context.Context,
	admission domain.DockerSandboxAdmission, owned sandbox.WorkspaceBinding,
) (sandbox.WorkspaceBinding, error) {
	if !validDockerSandboxIdentity(admission.RequestedBy) || !strings.HasPrefix(admission.RequestedBy, workerCommandDockerPrefix) {
		return sandbox.WorkspaceBinding{}, errors.New("worker Docker cleanup source is invalid")
	}
	sourceID := strings.TrimPrefix(admission.RequestedBy, workerCommandDockerPrefix)
	st, ok := s.store.(interface {
		GetHistoricalSpecialistApprovalCall(context.Context, string, string) (domain.SpecialistToolCall, bool, error)
		GetSpecialistToolBinding(context.Context, domain.AgentAttemptRef) (domain.SpecialistToolBinding, error)
	})
	if !ok {
		return sandbox.WorkspaceBinding{}, errors.New("worker Docker cleanup ledger unavailable")
	}
	call, started, err := st.GetHistoricalSpecialistApprovalCall(ctx, admission.RunID, sourceID)
	if err != nil || !started || call.ApprovalSourceID != sourceID || sourceID != domain.SpecialistToolApprovalID(call.Ref, call.CallID) {
		return sandbox.WorkspaceBinding{}, errors.New("worker Docker cleanup intent changed")
	}
	a, err := commandruntimeadapter.DecodeAuthority(json.RawMessage(call.AuthorityJSON))
	if err != nil || a.Worker == nil || a.Worker.AgentID != call.Ref.AgentID || a.Worker.AttemptID != call.Ref.AttemptID || a.RunID != admission.RunID {
		return sandbox.WorkspaceBinding{}, errors.New("worker Docker cleanup actor changed")
	}
	b, err := st.GetSpecialistToolBinding(ctx, call.Ref)
	if err != nil || b.Mode != "worker" || b.WorkspaceID != admission.WorkspaceID || b.SessionID != a.Worker.SessionID {
		return sandbox.WorkspaceBinding{}, errors.New("worker Docker cleanup workspace binding changed")
	}
	root := owned.RootPath
	if b.BatchPlanID != "" {
		batches, ok := s.store.(interface {
			GetBatchDeliveryWorkspace(context.Context, string, int) (domain.BatchDeliveryWorkspace, bool, error)
		})
		if !ok {
			return sandbox.WorkspaceBinding{}, errors.New("worker Docker cleanup Batch reader unavailable")
		}
		child, found, err := batches.GetBatchDeliveryWorkspace(ctx, b.BatchPlanID, b.BatchOrdinal)
		if err != nil || !found || child.AgentID != call.Ref.AgentID || child.Generation != b.BatchGeneration {
			return sandbox.WorkspaceBinding{}, errors.New("worker Docker cleanup Batch generation changed")
		}
		root = child.WorktreeRoot
	}
	actual, err := workspace.AgentCodeRootFingerprint(root)
	if err != nil || actual != b.RootFingerprint {
		return sandbox.WorkspaceBinding{}, errors.New("worker Docker cleanup physical workspace changed")
	}
	owned.RootPath = root
	return owned, nil
}
