package application

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/workspace"
)

type workerExecutionStore interface {
	RunFileWorkspaceStore
	GetRun(context.Context, string) (domain.Run, error)
	GetMission(context.Context, string) (domain.Mission, error)
	GetAgentNode(context.Context, string) (domain.AgentNode, error)
	GetSpecialistToolBinding(context.Context, domain.AgentAttemptRef) (domain.SpecialistToolBinding, error)
}

type workerToolSourceStore interface {
	GetSpecialistToolCall(context.Context, domain.AgentAttemptRef, string) (domain.SpecialistToolCall, error)
	GetSpecialistApprovalCall(context.Context, string, string) (domain.SpecialistToolCall, bool, error)
	ValidateSpecialistToolSource(context.Context, domain.SpecialistToolSource) (domain.SpecialistToolCall, error)
}

type workerExecutionContext struct {
	Run            domain.Run
	Mission        domain.Mission
	Child          domain.AgentNode
	Binding        domain.SpecialistToolBinding
	Files          RunFileWorkspace
	BatchReadHints []domain.BatchDeliveryOwnershipHint
}

// Resolve the host-issued binding on each operation. Workspace paths never
// come from a model payload, and a Batch generation cannot fall back to its source.
func readWorkerExecutionContext(ctx context.Context, base any, ref domain.AgentAttemptRef, drydocks *RunWorktreeService) (workerExecutionContext, error) {
	return readWorkerContext(ctx, base, ref, drydocks, false)
}

func readWorkerContext(ctx context.Context, base any, ref domain.AgentAttemptRef, drydocks *RunWorktreeService, resourceLifetime bool) (workerExecutionContext, error) {
	var value workerExecutionContext
	st, ok := base.(workerExecutionStore)
	if !ok || ref.Validate() != nil {
		return value, apperror.New(apperror.CodeFailedPrecondition, "worker execution binding is unavailable")
	}
	var err error
	value.Child, err = st.GetAgentNode(ctx, ref.AgentID)
	if err != nil {
		return value, err
	}
	child := value.Child
	if child.RunID != ref.RunID || child.Role != domain.AgentRoleSpecialist || child.Worker == nil || child.Terminal() {
		return value, apperror.New(apperror.CodePolicyDenied, "worker actor or active attempt changed")
	}
	if resourceLifetime {
		if err := validateWorkerResourceAttempt(ctx, base, ref, child); err != nil {
			return value, err
		}
	} else if child.Status != domain.AgentRunning || child.ActiveAttemptID != ref.AttemptID {
		return value, apperror.New(apperror.CodePolicyDenied, "worker actor or active attempt changed")
	}
	parent, err := st.GetAgentNode(ctx, child.ParentID)
	if err != nil {
		return value, err
	}
	if parent.RunID != ref.RunID || parent.Role != domain.AgentRoleRoot {
		return value, errors.New("worker parent identity changed")
	}
	value.Binding, err = st.GetSpecialistToolBinding(ctx, ref)
	if err != nil {
		return value, err
	}
	b := value.Binding
	profile, err := json.Marshal(child.Worker)
	if err != nil || b.Mode != "worker" || b.SessionID != child.SessionID || b.WorkerProfileJSON != string(profile) {
		return value, apperror.New(apperror.CodePolicyDenied, "worker capability profile changed")
	}
	value.Run, err = st.GetRun(ctx, ref.RunID)
	if err != nil {
		return value, err
	}
	if value.Run.Terminal() || value.Run.Status == domain.RunPaused {
		return value, apperror.New(apperror.CodePolicyDenied, "worker Run is no longer active")
	}
	value.Mission, err = st.GetMission(ctx, value.Run.MissionID)
	if err != nil {
		return value, err
	}
	if b.WorkspaceID != value.Mission.WorkspaceID {
		return value, errors.New("worker source workspace changed")
	}
	actorSession, err := st.GetSession(ctx, child.SessionID)
	if err != nil || actorSession.Status != session.StatusActive || actorSession.WorkspaceID != b.WorkspaceID {
		return value, apperror.New(apperror.CodeConflict, "worker Session is no longer active in this workspace")
	}
	if b.BatchPlanID != "" {
		batches, ok := base.(interface {
			GetBatchDeliveryWorkspace(context.Context, string, int) (domain.BatchDeliveryWorkspace, bool, error)
			GetBatchDeliveryPlan(context.Context, string) (domain.BatchDeliveryPlan, bool, error)
		})
		if !ok {
			return value, errors.New("worker Batch workspace reader unavailable")
		}
		owned, found, err := batches.GetBatchDeliveryWorkspace(ctx, b.BatchPlanID, b.BatchOrdinal)
		if err != nil {
			return value, err
		}
		if !found || owned.AgentID != child.ID || owned.Generation != b.BatchGeneration || owned.WorktreeRoot == "" ||
			(owned.Status != domain.BatchWorkspaceAcknowledged && owned.Status != domain.BatchWorkspaceWorking && owned.Status != domain.BatchWorkspaceQuestion) || !owned.LeaseExpiresAt.After(time.Now()) ||
			owned.ToolProfile.Validate() != nil || owned.ToolProfileFingerprint != owned.ToolProfile.Fingerprint() {
			return value, apperror.New(apperror.CodeConflict, "worker Batch workspace generation changed")
		}
		plan, found, err := batches.GetBatchDeliveryPlan(ctx, b.BatchPlanID)
		if err != nil {
			return value, err
		}
		if !found || plan.RunID != ref.RunID || plan.WorkspaceID != b.WorkspaceID || plan.RootAgentID != child.ParentID || plan.Status.Terminal() || plan.Status == domain.BatchDeliveryMerging || b.BatchOrdinal < 1 || b.BatchOrdinal > len(plan.Spec.Tasks) {
			return value, apperror.New(apperror.CodeConflict, "worker Batch plan changed")
		}
		for _, ordinal := range plan.Spec.Tasks[b.BatchOrdinal-1].DependencyOrdinals {
			dependency, found, err := batches.GetBatchDeliveryWorkspace(ctx, plan.ID, ordinal)
			if err != nil {
				return value, err
			}
			if !found || (dependency.Status != domain.BatchWorkspaceAccepted && dependency.Status != domain.BatchWorkspaceMerged) {
				return value, apperror.New(apperror.CodeFailedPrecondition, "worker Batch dependency is not accepted")
			}
		}
		value.BatchReadHints = append([]domain.BatchDeliveryOwnershipHint{}, plan.Spec.Tasks[b.BatchOrdinal-1].OwnershipHints...)
		value.Files.Source, err = st.GetWorkspaceInfo(ctx, b.WorkspaceID)
		if err != nil {
			return value, err
		}
		value.Files.Workspace = session.WorkspaceInfo{ID: b.WorkspaceID, RootPath: owned.WorktreeRoot}
	} else {
		value.Files, err = resolveRunFileWorkspaceControl(ctx, st, value.Run, value.Mission, drydocks)
		if err != nil {
			return value, err
		}
	}
	fingerprint, err := workspace.AgentCodeRootFingerprint(value.Files.Workspace.RootPath)
	if err != nil || fingerprint != b.RootFingerprint {
		return value, apperror.New(apperror.CodeConflict, "worker physical workspace changed")
	}
	return value, nil
}
