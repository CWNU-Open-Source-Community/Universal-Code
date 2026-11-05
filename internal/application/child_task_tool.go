package application

import (
	"context"
	"encoding/json"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/toolgateway"
)

// ChildTaskMutationStore is the bounded persistence surface for model-
// proposed child task sets.
type ChildTaskMutationStore interface {
	GetRun(ctx context.Context, id string) (domain.Run, error)
	CreateChildTaskProposal(ctx context.Context, operation domain.ChildTaskOperation,
		proposal domain.ChildTaskProposal, policyEvent, proposalEvent, toolEvent events.Event,
	) (domain.ChildTaskProposal, bool, error)
}

// ChildTaskToolExecutor receives the model's proposal, resolves the surface,
// and persists exactly one review-required proposal.
type ChildTaskToolExecutor struct {
	store ChildTaskMutationStore
}

func NewChildTaskToolExecutor(store ChildTaskMutationStore) *ChildTaskToolExecutor {
	return &ChildTaskToolExecutor{store: store}
}

func (e *ChildTaskToolExecutor) ProposeChildTasks(ctx context.Context,
	scope toolgateway.ChildTaskProposalContext,
	spec domain.ChildTaskProposalSpec,
) (toolgateway.ChildTaskProposalResult, error) {
	if e == nil || e.store == nil {
		return toolgateway.ChildTaskProposalResult{}, apperror.New(
			apperror.CodeFailedPrecondition, "child task mutation store is required")
	}
	if err := scope.Validate(); err != nil {
		return toolgateway.ChildTaskProposalResult{}, apperror.Wrap(
			apperror.CodeInvalidArgument, err.Error(), err)
	}
	normalized, err := domain.NormalizeChildTaskProposalSpec(spec)
	if err != nil {
		return toolgateway.ChildTaskProposalResult{}, apperror.Wrap(
			apperror.CodeInvalidArgument, "child task specification is invalid", err)
	}
	surface, tier, err := domain.ResolveChildTaskSurface(normalized)
	if err != nil {
		return toolgateway.ChildTaskProposalResult{}, apperror.Wrap(
			apperror.CodeInvalidArgument, "child task surface cannot be resolved", err)
	}
	run, err := e.store.GetRun(ctx, scope.RunID)
	if err != nil {
		return toolgateway.ChildTaskProposalResult{}, apperror.Normalize(err)
	}
	now := time.Now().UTC()
	proposal := domain.ChildTaskProposal{
		ID: idgen.New("childtask"), RunID: scope.RunID, RootAgentID: scope.RootAgentID,
		SessionID: scope.SessionID, WorkspaceID: scope.WorkspaceID,
		Status: domain.ChildTaskProposalProposed, Spec: normalized,
		Surface: surface, FanoutTier: tier, RequestedBy: scope.RequestedBy,
		Version: 1, CreatedAt: now,
	}
	fingerprint := runmutation.Fingerprint("child_task_request.v1", scope.RunID,
		scope.RootAgentID, scope.SessionID, scope.WorkspaceID, scope.RequestedBy,
		normalized.SpecJSONFingerprint())
	operation := domain.ChildTaskOperation{
		KeyDigest: runmutation.OperationKeyDigest(
			string(toolgateway.ChildTaskProposeTool), scope.RunID, scope.OperationKey),
		RequestFingerprint: fingerprint, ProposalID: proposal.ID, RunID: scope.RunID,
		SessionID: scope.SessionID, WorkspaceID: scope.WorkspaceID,
		RootAgentID: scope.RootAgentID, LeaseID: scope.LeaseID,
		LeaseGeneration: scope.LeaseGeneration, RequestedBy: scope.RequestedBy,
		CreatedAt: now,
	}
	policyEvent, proposalEvent, toolEvent, err := childTaskEvents(run, scope, proposal)
	if err != nil {
		return toolgateway.ChildTaskProposalResult{}, err
	}
	stored, replayed, err := e.store.CreateChildTaskProposal(ctx, operation, proposal,
		policyEvent, proposalEvent, toolEvent)
	if err != nil {
		return toolgateway.ChildTaskProposalResult{}, apperror.Normalize(err)
	}
	return toolgateway.ChildTaskProposalResult{
		ProposalID: stored.ID, Status: stored.Status, Surface: stored.Surface,
		FanoutTier: stored.FanoutTier, TaskCount: len(stored.Spec.Tasks),
		Replayed: replayed,
	}, nil
}

func childTaskEvents(run domain.Run, scope toolgateway.ChildTaskProposalContext,
	proposal domain.ChildTaskProposal,
) (events.Event, events.Event, events.Event, error) {
	policyEvent, err := events.New(run.ID, run.MissionID, events.PolicyDecisionEvent,
		"policy", scope.InvocationID, map[string]any{
			"context": "tool_run." + string(toolgateway.ChildTaskProposeTool),
			"allowed": true, "needs_approval": false,
			"risk": scope.PolicyDecision.Risk, "reason": scope.PolicyDecision.Reason,
			"agent_id": scope.RootAgentID, "operator_review_required": true,
			"admission_authorized": false,
		})
	if err != nil {
		return events.Event{}, events.Event{}, events.Event{}, err
	}
	var totalTurns, totalTokens int64
	for _, task := range proposal.Spec.Tasks {
		totalTurns += task.TurnLimit
		totalTokens += task.TokenLimit
	}
	proposalEvent, err := events.New(run.ID, run.MissionID, events.ChildTaskProposedEvent,
		"agent_coordinator", proposal.ID, map[string]any{
			"proposal_id": proposal.ID, "root_agent_id": proposal.RootAgentID,
			"protocol_version": proposal.Spec.Version, "status": proposal.Status,
			"surface": string(proposal.Surface), "fanout_tier": string(proposal.FanoutTier),
			"task_count": len(proposal.Spec.Tasks),
			"suggested_turns": totalTurns, "suggested_tokens": totalTokens,
			"operator_review_required": true, "admission_authorized": false,
		})
	if err != nil {
		return events.Event{}, events.Event{}, events.Event{}, err
	}
	toolEvent, err := events.New(run.ID, run.MissionID, events.ToolCompletedEvent,
		"agent_proposal_tool", scope.InvocationID, map[string]any{
			"invocation_id": scope.InvocationID,
			"tool_name": toolgateway.ChildTaskProposeTool,
			"target_id": proposal.ID, "agent_id": scope.RootAgentID,
			"execution_backend": "agent_proposal", "admission_authorized": false,
		})
	if err != nil {
		return events.Event{}, events.Event{}, events.Event{}, err
	}
	return policyEvent, proposalEvent, toolEvent, nil
}

var _ = json.Marshal

