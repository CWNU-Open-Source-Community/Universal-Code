package application

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/readonlyfanout"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
)

type ReadOnlyFanoutPlanStore interface {
	policy.DecisionRecorder
	GetMission(ctx context.Context, id string) (domain.Mission, error)
	GetRun(ctx context.Context, id string) (domain.Run, error)
	GetRunMode(ctx context.Context, runID string) (domain.RunModeSnapshot, error)
	GetSession(ctx context.Context, id string) (session.Session, error)
	GetWorkspaceInfo(ctx context.Context, id string) (session.WorkspaceInfo, error)
	GetReadOnlyFanoutOperation(ctx context.Context,
		keyDigest string) (domain.ReadOnlyFanoutOperation, bool, error)
	GetReadOnlyFanoutPlan(ctx context.Context, id string) (domain.ReadOnlyFanoutPlan, error)
	CreateReadOnlyFanoutPlan(ctx context.Context, plan domain.ReadOnlyFanoutPlan,
		operation domain.ReadOnlyFanoutOperation, policyEvent events.Event,
		plannedEvent events.Event) (domain.ReadOnlyFanoutPlan, bool, error)
}

type ReadOnlyFanoutPlanService struct {
	store   ReadOnlyFanoutPlanStore
	checker policy.Checker
}

func NewReadOnlyFanoutPlanService(store ReadOnlyFanoutPlanStore,
	checker policy.Checker,
) *ReadOnlyFanoutPlanService {
	return &ReadOnlyFanoutPlanService{store: store, checker: checker}
}

type CreateReadOnlyFanoutPlanRequest struct {
	RunID        string
	Goal         string
	ScopePath    string
	Tier         string
	OperationKey string
	RequestedBy  string
}

type CreateReadOnlyFanoutPlanResult struct {
	Plan     domain.ReadOnlyFanoutPlan
	Replayed bool
}

func (s *ReadOnlyFanoutPlanService) Create(ctx context.Context,
	request CreateReadOnlyFanoutPlanRequest,
) (CreateReadOnlyFanoutPlanResult, error) {
	if s == nil || s.store == nil || s.checker == nil {
		return CreateReadOnlyFanoutPlanResult{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"read-only fan-out planning store and Policy checker are required")
	}
	normalized, tier, err := normalizeReadOnlyFanoutPlanRequest(request)
	if err != nil {
		return CreateReadOnlyFanoutPlanResult{}, apperror.Wrap(
			apperror.CodeInvalidArgument, "read-only fan-out planning request is invalid", err)
	}
	run, err := s.store.GetRun(ctx, normalized.RunID)
	if err != nil {
		return CreateReadOnlyFanoutPlanResult{}, apperror.Normalize(err)
	}
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return CreateReadOnlyFanoutPlanResult{}, apperror.Normalize(err)
	}
	mode, err := s.store.GetRunMode(ctx, run.ID)
	if err != nil {
		return CreateReadOnlyFanoutPlanResult{}, apperror.Normalize(err)
	}
	if run.Status != domain.RunRunning || mission.WorkspaceID == "" ||
		mode.Scope.WorkspaceID != mission.WorkspaceID ||
		mode.Scope.NetworkMode != "disabled" {
		return CreateReadOnlyFanoutPlanResult{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"read-only fan-out planning requires a running local-workspace Run with network disabled")
	}
	linkedSession, err := s.store.GetSession(ctx, run.SessionID)
	if err != nil {
		return CreateReadOnlyFanoutPlanResult{}, apperror.Normalize(err)
	}
	if linkedSession.Status != session.StatusActive ||
		linkedSession.WorkspaceID != mission.WorkspaceID {
		return CreateReadOnlyFanoutPlanResult{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"read-only fan-out planning requires the active Run Session and workspace")
	}
	workspace, err := s.store.GetWorkspaceInfo(ctx, mission.WorkspaceID)
	if err != nil {
		return CreateReadOnlyFanoutPlanResult{}, apperror.Normalize(err)
	}
	keyDigest := runmutation.OperationKeyDigest("readonly_fanout_plan", run.ID,
		normalized.OperationKey)
	requestFingerprint := runmutation.Fingerprint("readonly_fanout_plan_request.v1",
		run.ID, workspace.ID, normalized.ScopePath, normalized.Goal, string(tier),
		normalized.RequestedBy)
	if existing, found, err := s.store.GetReadOnlyFanoutOperation(ctx, keyDigest); err != nil {
		return CreateReadOnlyFanoutPlanResult{}, apperror.Normalize(err)
	} else if found {
		if existing.RequestFingerprint != requestFingerprint || existing.RunID != run.ID ||
			existing.WorkspaceID != workspace.ID || existing.RequestedBy != normalized.RequestedBy {
			return CreateReadOnlyFanoutPlanResult{}, apperror.New(
				apperror.CodeConflict,
				"read-only fan-out operation key was already used for different intent")
		}
		plan, err := s.store.GetReadOnlyFanoutPlan(ctx, existing.PlanID)
		return CreateReadOnlyFanoutPlanResult{Plan: plan, Replayed: true},
			apperror.Normalize(err)
	}
	planID := idgen.New("fanout-plan")
	decision := s.checker.CheckText("readonly_fanout_plan",
		normalized.Goal+"\nworkspace_scope="+normalized.ScopePath)
	decision.Risk = strings.TrimSpace(redact.String(decision.Risk))
	decision.Reason = strings.TrimSpace(redact.String(decision.Reason))
	if !decision.Allowed || decision.NeedsApproval {
		if err := s.store.RecordPolicyDecision(ctx, policy.DecisionRecord{
			SessionID: run.SessionID, SubjectID: planID, Context: "readonly_fanout_plan",
			Decision: decision,
		}); err != nil {
			return CreateReadOnlyFanoutPlanResult{}, apperror.Normalize(err)
		}
		reason := decision.Reason
		if decision.NeedsApproval && decision.Allowed {
			reason = "read-only fan-out Policy requires an approval path that is not enabled"
		}
		return CreateReadOnlyFanoutPlanResult{}, apperror.New(
			apperror.CodePolicyDenied, reason)
	}
	plan, err := readonlyfanout.BuildPlan(ctx, readonlyfanout.BuildPlanRequest{
		PlanID: planID, RunID: run.ID, WorkspaceID: workspace.ID,
		WorkspaceRoot: workspace.RootPath, ScopePath: normalized.ScopePath,
		Goal: normalized.Goal, Tier: tier, RequestedBy: normalized.RequestedBy,
	})
	if err != nil {
		return CreateReadOnlyFanoutPlanResult{}, apperror.Wrap(
			apperror.CodeInvalidArgument, "read-only fan-out snapshot could not be built", err)
	}
	operation := domain.ReadOnlyFanoutOperation{
		KeyDigest: keyDigest, RequestFingerprint: requestFingerprint,
		PlanID: plan.ID, RunID: run.ID, WorkspaceID: workspace.ID,
		RequestedBy: normalized.RequestedBy, CreatedAt: plan.CreatedAt,
	}
	policyEvent, err := events.New(run.ID, mission.ID, events.PolicyDecisionEvent,
		"readonly_fanout", plan.ID, map[string]any{
			"context": "readonly_fanout_plan", "allowed": true,
			"needs_approval": false, "risk": decision.Risk, "reason": decision.Reason,
			"capability": "workspace_readonly", "execution_authorized": false,
		})
	if err != nil {
		return CreateReadOnlyFanoutPlanResult{}, err
	}
	plannedEvent, err := events.New(run.ID, mission.ID, events.ReadOnlyFanoutPlannedEvent,
		"readonly_fanout", plan.ID, map[string]any{
			"plan_id": plan.ID, "protocol": plan.ProtocolVersion,
			"requested_tier":        plan.RequestedTier,
			"effective_parallelism": plan.EffectiveParallelism,
			"file_count":            plan.FileCount, "total_bytes": plan.TotalBytes,
			"excluded_count": plan.ExcludedCount, "shard_count": plan.ShardCount,
			"snapshot_digest":        plan.SnapshotDigest,
			"capability_fingerprint": plan.CapabilityFingerprint,
			"shell":                  false, "file_write": false, "network": false,
			"child_spawn": false, "execution_authorized": false,
		})
	if err != nil {
		return CreateReadOnlyFanoutPlanResult{}, err
	}
	policyEvent.CreatedAt = plan.CreatedAt
	plannedEvent.CreatedAt = plan.CreatedAt
	stored, replayed, err := s.store.CreateReadOnlyFanoutPlan(ctx, plan, operation,
		policyEvent, plannedEvent)
	return CreateReadOnlyFanoutPlanResult{Plan: stored, Replayed: replayed},
		apperror.Normalize(err)
}

func normalizeReadOnlyFanoutPlanRequest(request CreateReadOnlyFanoutPlanRequest,
) (CreateReadOnlyFanoutPlanRequest, domain.ReadOnlyFanoutTier, error) {
	originalKey := request.OperationKey
	request.RunID = strings.TrimSpace(request.RunID)
	request.Goal = strings.TrimSpace(redact.String(request.Goal))
	request.RequestedBy = strings.TrimSpace(redact.String(request.RequestedBy))
	if request.ScopePath == "" {
		request.ScopePath = "."
	}
	if request.Tier == "" {
		request.Tier = string(domain.ReadOnlyFanoutAuto)
	}
	tier, err := domain.ParseReadOnlyFanoutTier(request.Tier)
	if err != nil {
		return CreateReadOnlyFanoutPlanRequest{}, "", err
	}
	if !domain.ValidAgentID(request.RunID) || !domain.ValidAgentID(request.RequestedBy) ||
		request.Goal == "" || utf8.RuneCountInString(request.Goal) >
		domain.MaxReadOnlyFanoutGoalRunes || strings.ContainsRune(request.Goal, 0) {
		return CreateReadOnlyFanoutPlanRequest{}, "",
			errors.New("run, bounded goal, and operator identities are required")
	}
	if request.OperationKey != strings.TrimSpace(originalKey) ||
		!utf8.ValidString(request.OperationKey) {
		return CreateReadOnlyFanoutPlanRequest{}, "",
			errors.New("read-only fan-out operation key must be normalized UTF-8")
	}
	if _, err := domain.NormalizeAgentOperationKey(request.OperationKey); err != nil {
		return CreateReadOnlyFanoutPlanRequest{}, "", err
	}
	for _, current := range request.OperationKey {
		if unicode.IsControl(current) || unicode.IsSpace(current) {
			return CreateReadOnlyFanoutPlanRequest{}, "",
				errors.New("read-only fan-out operation key cannot contain whitespace or control characters")
		}
	}
	return request, tier, nil
}
