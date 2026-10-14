package application

import (
	"context"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
)

const RunWebAccessControlProtocolVersion = "run_web_access_control.v1"

type RunWebAccessStore interface {
	GetRun(context.Context, string) (domain.Run, error)
	GetRunMode(context.Context, string) (domain.RunModeSnapshot, error)
	GetRunModeSnapshot(context.Context, string) (domain.RunModeSnapshot, error)
	GetRunWebAccessOperation(context.Context, string) (domain.RunWebAccessOperation, bool, error)
	TransitionRunWebAccess(context.Context, domain.RunModeSnapshot, domain.RunWebAccessOperation, events.Event) (domain.RunModeSnapshot, bool, error)
}

type RunWebAccessService struct {
	store            RunWebAccessStore
	runtimeAuthority *domain.ExecutionPermissionRuntimeAuthority
}

type ChangeRunWebAccessRequest struct {
	Version              string
	RunID                string
	ExpectedModeRevision int64
	WebAccessMode        string
	WebAllowedTargets    []string
	OperationKey         string
	RequestedBy          string
	Reason               string
}

type ChangeRunWebAccessResult struct {
	Mode     domain.RunModeSnapshot
	Replayed bool
}

func NewRunWebAccessService(store RunWebAccessStore) *RunWebAccessService {
	return &RunWebAccessService{store: store}
}
func (s *RunWebAccessService) WithRuntimeAuthority(authority *domain.ExecutionPermissionRuntimeAuthority) *RunWebAccessService {
	s.runtimeAuthority = authority
	return s
}

func (s *RunWebAccessService) Change(ctx context.Context, request ChangeRunWebAccessRequest) (ChangeRunWebAccessResult, error) {
	if s == nil || s.store == nil {
		return ChangeRunWebAccessResult{}, apperror.New(apperror.CodeFailedPrecondition, "Run web access store is required")
	}
	if request.Version != RunWebAccessControlProtocolVersion || request.ExpectedModeRevision <= 0 {
		return ChangeRunWebAccessResult{}, apperror.New(apperror.CodeInvalidArgument, "web access version and positive expected mode revision are required")
	}
	if request.Reason == "" {
		request.Reason = "operator selected web access"
	}
	identity, _, err := normalizeChangeRunPhaseRequest(ChangeRunPhaseRequest{RunID: request.RunID, Phase: "deliver", OperationKey: request.OperationKey, RequestedBy: request.RequestedBy, Reason: request.Reason})
	if err != nil {
		return ChangeRunWebAccessResult{}, apperror.Wrap(apperror.CodeInvalidArgument, "web access operation identity is invalid", err)
	}
	mode, targets, err := normalizeWebAccess(request.WebAccessMode, request.WebAllowedTargets, false)
	if err != nil {
		return ChangeRunWebAccessResult{}, err
	}
	key := runmutation.Fingerprint("run_web_access_operation.v1", identity.RunID, identity.OperationKey)
	fingerprint := domain.RunWebAccessFingerprint(identity.RunID, request.ExpectedModeRevision, mode, targets, identity.RequestedBy, identity.Reason)
	if replay, found, err := s.loadWebAccessReplay(ctx, key, fingerprint, identity.RunID, request.ExpectedModeRevision); err != nil {
		return ChangeRunWebAccessResult{}, err
	} else if found {
		return replay, nil
	}
	run, err := s.store.GetRun(ctx, identity.RunID)
	if err != nil {
		return ChangeRunWebAccessResult{}, apperror.Normalize(err)
	}
	if !domain.CanChangeRunPhase(run.Status) {
		return ChangeRunWebAccessResult{}, apperror.New(apperror.CodeConflict, "web access can change only while the Run is created or paused")
	}
	current, err := s.store.GetRunMode(ctx, run.ID)
	if err != nil {
		return ChangeRunWebAccessResult{}, apperror.Normalize(err)
	}
	if current.Revision != request.ExpectedModeRevision {
		if replay, found, err := s.loadWebAccessReplay(ctx, key, fingerprint, identity.RunID, request.ExpectedModeRevision); err != nil {
			return ChangeRunWebAccessResult{}, err
		} else if found {
			return replay, nil
		}

		return ChangeRunWebAccessResult{}, apperror.New(apperror.CodeConflict, "Run web access changed concurrently")
	}
	if s.runtimeAuthority != nil {
		if _, err := s.runtimeAuthority.RotateRunAuthorizationFence(run.ID); err != nil {
			return ChangeRunWebAccessResult{}, apperror.Wrap(apperror.CodeFailedPrecondition, "previous tool generation could not be fenced", err)
		}
	}
	now := time.Now().UTC()
	if now.Before(current.CreatedAt) {
		now = current.CreatedAt
	}
	next := current
	next.Scope = domain.CloneScope(current.Scope)
	next.ID = idgen.New("run-web-access")
	next.Revision++
	next.Scope.WebAccessMode = mode
	next.Scope.WebAllowedTargets = targets
	next.RequestedBy = identity.RequestedBy
	next.Reason = identity.Reason
	next.CreatedAt = now
	if err := next.Validate(); err != nil {
		return ChangeRunWebAccessResult{}, apperror.Wrap(apperror.CodeInvalidArgument, "web access snapshot is invalid", err)
	}
	operation := domain.RunWebAccessOperation{KeyDigest: key, RequestFingerprint: fingerprint, SnapshotID: next.ID, RunID: next.RunID, ExpectedModeRevision: request.ExpectedModeRevision, RequestedBy: next.RequestedBy, CreatedAt: now}
	event, err := events.New(next.RunID, next.MissionID, events.RunWebAccessChangedEvent, "run_web_access", next.ID, map[string]any{"protocol": RunWebAccessControlProtocolVersion, "revision": next.Revision, "expected_mode_revision": current.Revision, "from": current.Scope.EffectiveWebAccessMode(), "to": mode, "allowed_target_count": len(targets), "requested_by": next.RequestedBy, "reason": next.Reason})
	if err != nil {
		return ChangeRunWebAccessResult{}, err
	}
	event.CreatedAt = now
	stored, replayed, err := s.store.TransitionRunWebAccess(ctx, next, operation, event)
	if err != nil {
		return ChangeRunWebAccessResult{}, apperror.Normalize(err)
	}
	if !replayed && s.runtimeAuthority != nil {
		if _, err := s.runtimeAuthority.RotateRunAuthorizationFence(run.ID); err != nil {
			return ChangeRunWebAccessResult{}, apperror.Wrap(apperror.CodeInternal, "web access changed but its runtime fence could not rotate", err)
		}
	}
	return ChangeRunWebAccessResult{Mode: stored, Replayed: replayed}, nil
}

func (s *RunWebAccessService) loadWebAccessReplay(ctx context.Context, key, fingerprint, runID string, revision int64) (ChangeRunWebAccessResult, bool, error) {
	if operation, found, err := s.store.GetRunWebAccessOperation(ctx, key); err != nil {
		return ChangeRunWebAccessResult{}, false, apperror.Normalize(err)
	} else if found {
		if operation.RequestFingerprint != fingerprint || operation.RunID != runID || operation.ExpectedModeRevision != revision {
			return ChangeRunWebAccessResult{}, true, apperror.New(apperror.CodeConflict, "web access key was already used for different intent")
		}
		stored, err := s.store.GetRunModeSnapshot(ctx, operation.SnapshotID)
		if err == nil && (stored.ID != operation.SnapshotID || stored.RunID != operation.RunID || stored.Revision != operation.ExpectedModeRevision+1 || stored.RequestedBy != operation.RequestedBy || !stored.CreatedAt.Equal(operation.CreatedAt) || domain.RunWebAccessFingerprint(stored.RunID, operation.ExpectedModeRevision, stored.Scope.WebAccessMode, stored.Scope.WebAllowedTargets, stored.RequestedBy, stored.Reason) != operation.RequestFingerprint) {
			return ChangeRunWebAccessResult{}, true, apperror.New(apperror.CodeInternal, "stored web access operation binding is invalid")
		}
		return ChangeRunWebAccessResult{Mode: stored, Replayed: true}, true, apperror.Normalize(err)
	}
	return ChangeRunWebAccessResult{}, false, nil
}
