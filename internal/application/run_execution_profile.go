package application

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
)

type RunExecutionProfileStore interface {
	GetRun(ctx context.Context, id string) (domain.Run, error)
	GetRunExecutionProfile(ctx context.Context,
		runID string) (domain.RunExecutionProfileSnapshot, error)
	GetRunExecutionProfileSnapshot(ctx context.Context,
		id string) (domain.RunExecutionProfileSnapshot, error)
	GetRunExecutionProfileOperation(ctx context.Context,
		keyDigest string) (domain.RunExecutionProfileOperation, bool, error)
	TransitionRunExecutionProfile(ctx context.Context,
		snapshot domain.RunExecutionProfileSnapshot,
		operation domain.RunExecutionProfileOperation,
		event events.Event) (domain.RunExecutionProfileSnapshot, bool, error)
}

type RunExecutionProfileService struct {
	store RunExecutionProfileStore
}

type ChangeRunExecutionProfileRequest struct {
	RunID        string
	Profile      string
	OperationKey string
	RequestedBy  string
	Reason       string
}

type ChangeRunExecutionProfileResult struct {
	Profile  domain.RunExecutionProfileSnapshot
	Replayed bool
}

func NewRunExecutionProfileService(
	store RunExecutionProfileStore,
) *RunExecutionProfileService {
	return &RunExecutionProfileService{store: store}
}

func (s *RunExecutionProfileService) Current(ctx context.Context,
	runID string,
) (domain.RunExecutionProfileSnapshot, error) {
	if s == nil || s.store == nil {
		return domain.RunExecutionProfileSnapshot{}, apperror.New(
			apperror.CodeFailedPrecondition, "Run execution profile store is required")
	}
	runID = strings.TrimSpace(runID)
	if !domain.ValidAgentID(runID) || strings.ContainsRune(runID, 0) {
		return domain.RunExecutionProfileSnapshot{}, apperror.New(
			apperror.CodeInvalidArgument, "Run execution profile Run id is invalid")
	}
	profile, err := s.store.GetRunExecutionProfile(ctx, runID)
	return profile, apperror.Normalize(err)
}

func (s *RunExecutionProfileService) Change(ctx context.Context,
	request ChangeRunExecutionProfileRequest,
) (ChangeRunExecutionProfileResult, error) {
	if s == nil || s.store == nil {
		return ChangeRunExecutionProfileResult{}, apperror.New(
			apperror.CodeFailedPrecondition, "Run execution profile store is required")
	}
	normalized, target, err := normalizeChangeRunExecutionProfileRequest(request)
	if err != nil {
		return ChangeRunExecutionProfileResult{}, apperror.Wrap(
			apperror.CodeInvalidArgument, err.Error(), err)
	}
	keyDigest := runmutation.Fingerprint("run_execution_profile_operation.v1",
		normalized.RunID, normalized.OperationKey)
	requestFingerprint := runmutation.Fingerprint(
		"run_execution_profile_change_request.v1", normalized.RunID,
		string(target), normalized.RequestedBy, normalized.Reason)
	if replay, found, err := s.loadReplay(ctx, keyDigest, requestFingerprint,
		normalized.RunID, normalized.RequestedBy, target); err != nil {
		return ChangeRunExecutionProfileResult{}, err
	} else if found {
		return replay, nil
	}
	run, err := s.store.GetRun(ctx, normalized.RunID)
	if err != nil {
		return ChangeRunExecutionProfileResult{}, apperror.Normalize(err)
	}
	if !domain.CanChangeRunExecutionProfile(run.Status) {
		return ChangeRunExecutionProfileResult{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"Run execution profile can only change while the Run is created or paused")
	}
	current, err := s.store.GetRunExecutionProfile(ctx, run.ID)
	if err != nil {
		return ChangeRunExecutionProfileResult{}, apperror.Normalize(err)
	}
	if current.Profile == target && !(current.IsDefaultPreview() && target == domain.RunExecutionProfilePreview) {
		if replay, found, err := s.loadReplay(ctx, keyDigest, requestFingerprint,
			normalized.RunID, normalized.RequestedBy, target); err != nil {
			return ChangeRunExecutionProfileResult{}, err
		} else if found {
			return replay, nil
		}
		return ChangeRunExecutionProfileResult{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"Run already uses the requested execution profile")
	}
	now := time.Now().UTC()
	if now.Before(current.CreatedAt) {
		now = current.CreatedAt
	}
	next, err := current.Next(idgen.New("run-exec-profile"), target,
		normalized.RequestedBy, normalized.Reason, now)
	if err != nil {
		return ChangeRunExecutionProfileResult{}, apperror.Wrap(
			apperror.CodeInvalidArgument, "Run execution profile transition is invalid", err)
	}
	operation := domain.RunExecutionProfileOperation{
		KeyDigest: keyDigest, RequestFingerprint: requestFingerprint,
		SnapshotID: next.ID, RunID: next.RunID, RequestedBy: next.RequestedBy,
		CreatedAt: next.CreatedAt,
	}
	event, err := events.New(next.RunID, next.MissionID,
		events.RunExecutionProfileSelectedEvent, "run_execution_profile", next.ID,
		map[string]any{
			"protocol": next.ProtocolVersion, "revision": next.Revision,
			"from": current.Profile, "to": next.Profile, "backend": next.Backend,
			"approval_policy":  next.ApprovalPolicy,
			"filesystem_scope": next.FilesystemScope, "network_scope": next.NetworkScope,
			"risk_tier": next.RiskTier, "required_gate": next.RequiredGate,
			"policy_version": next.PolicyVersion, "requested_by": next.RequestedBy,
			"reason": next.Reason, "process_enabled": false,
			"execution_authorized": false, "capability_grant": false,
		})
	if err != nil {
		return ChangeRunExecutionProfileResult{}, err
	}
	event.CreatedAt = next.CreatedAt
	stored, replayed, err := s.store.TransitionRunExecutionProfile(ctx, next,
		operation, event)
	return ChangeRunExecutionProfileResult{Profile: stored, Replayed: replayed},
		apperror.Normalize(err)
}

func (s *RunExecutionProfileService) loadReplay(ctx context.Context,
	keyDigest string, requestFingerprint string, runID string, requestedBy string,
	target domain.RunExecutionProfile,
) (ChangeRunExecutionProfileResult, bool, error) {
	existing, found, err := s.store.GetRunExecutionProfileOperation(ctx, keyDigest)
	if err != nil {
		return ChangeRunExecutionProfileResult{}, false, apperror.Normalize(err)
	}
	if !found {
		return ChangeRunExecutionProfileResult{}, false, nil
	}
	if existing.RequestFingerprint != requestFingerprint || existing.RunID != runID ||
		existing.RequestedBy != requestedBy {
		return ChangeRunExecutionProfileResult{}, true, apperror.New(
			apperror.CodeConflict,
			"Run execution profile operation key was already used for different intent")
	}
	stored, err := s.store.GetRunExecutionProfileSnapshot(ctx, existing.SnapshotID)
	if err != nil {
		return ChangeRunExecutionProfileResult{}, true, apperror.Normalize(err)
	}
	if stored.ID != existing.SnapshotID || stored.RunID != existing.RunID ||
		stored.RequestedBy != existing.RequestedBy ||
		!stored.CreatedAt.Equal(existing.CreatedAt) || stored.Profile != target ||
		stored.ProcessEnabled || stored.ExecutionAuthorized || stored.CapabilityGrant {
		return ChangeRunExecutionProfileResult{}, true, apperror.New(
			apperror.CodeInternal,
			"stored Run execution profile operation binding is invalid")
	}
	return ChangeRunExecutionProfileResult{Profile: stored, Replayed: true}, true, nil
}

func normalizeChangeRunExecutionProfileRequest(
	request ChangeRunExecutionProfileRequest,
) (ChangeRunExecutionProfileRequest, domain.RunExecutionProfile, error) {
	originalKey := request.OperationKey
	request.RunID = strings.TrimSpace(request.RunID)
	request.RequestedBy = strings.TrimSpace(redact.String(request.RequestedBy))
	request.Reason = strings.TrimSpace(redact.String(request.Reason))
	if request.RequestedBy == "" {
		request.RequestedBy = "cli_operator"
	}
	if request.Reason == "" {
		request.Reason = "operator selected execution profile"
	}
	if !domain.ValidAgentID(request.RunID) || strings.ContainsRune(request.RunID, 0) ||
		!domain.ValidAgentID(request.RequestedBy) || strings.ContainsRune(request.RequestedBy, 0) {
		return ChangeRunExecutionProfileRequest{}, "", errors.New(
			"bounded Run and operator identities are required")
	}
	if !utf8.ValidString(request.Reason) || strings.ContainsRune(request.Reason, 0) ||
		utf8.RuneCountInString(request.Reason) > domain.MaxRunExecutionProfileReasonRunes {
		return ChangeRunExecutionProfileRequest{}, "", errors.New(
			"run execution profile reason is invalid or too long")
	}
	if request.OperationKey != strings.TrimSpace(originalKey) ||
		!utf8.ValidString(request.OperationKey) {
		return ChangeRunExecutionProfileRequest{}, "", errors.New(
			"run execution profile operation key must be normalized UTF-8")
	}
	if _, err := domain.NormalizeAgentOperationKey(request.OperationKey); err != nil {
		return ChangeRunExecutionProfileRequest{}, "", err
	}
	for _, current := range request.OperationKey {
		if unicode.IsControl(current) || unicode.IsSpace(current) {
			return ChangeRunExecutionProfileRequest{}, "", errors.New(
				"run execution profile operation key cannot contain whitespace or control characters")
		}
	}
	target, err := domain.ParseRunExecutionProfile(request.Profile)
	if err != nil {
		return ChangeRunExecutionProfileRequest{}, "", err
	}
	request.Profile = string(target)
	return request, target, nil
}
