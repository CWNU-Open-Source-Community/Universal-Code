package application

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
)

type ThreadExecutionPermissionStore interface {
	GetThread(context.Context, string) (domain.Thread, error)
	GetThreadExecutionPermission(context.Context,
		string) (domain.ThreadExecutionPermissionSnapshot, error)
	GetRunExecutionPermission(context.Context,
		string) (domain.RunExecutionPermissionSnapshot, error)
	GetThreadExecutionPermissionSnapshot(context.Context,
		string) (domain.ThreadExecutionPermissionSnapshot, error)
	GetThreadExecutionPermissionOperation(context.Context,
		string) (domain.ThreadExecutionPermissionOperation, bool, error)
	TransitionThreadExecutionPermission(context.Context,
		domain.ThreadExecutionPermissionSnapshot,
		domain.ThreadExecutionPermissionOperation) (
		domain.ThreadExecutionPermissionSnapshot,
		domain.ThreadExecutionPermissionOperation, bool, error)
}

type ThreadExecutionPermissionService struct {
	store        ThreadExecutionPermissionStore
	capabilities domain.ExecutionPermissionRuntimeCapabilities
}

type ChangeThreadExecutionPermissionRequest struct {
	ThreadID                string
	Mode                    string
	OperationKey            string
	RequestedBy             string
	Reason                  string
	ConfirmFull             bool
	ConfirmWorkspaceAccess  bool
	ConfirmUserApproval     bool
	ConfirmDangerFullAccess bool
	ConfirmDebugAccess      bool
}

type ChangeThreadExecutionPermissionResult struct {
	Permission       domain.ThreadExecutionPermissionSnapshot
	CurrentRunID     string
	CurrentRunEffect domain.ThreadExecutionPermissionCurrentRunEffect
	Replayed         bool
}

type CurrentThreadExecutionPermissionResult struct {
	Permission             domain.ThreadExecutionPermissionSnapshot
	CurrentRunID           string
	CurrentRunMode         domain.RunExecutionPermissionMode
	CurrentRunPermission   domain.RunExecutionPermissionSnapshot
	CurrentRunSynchronized bool
}

func NewThreadExecutionPermissionService(store ThreadExecutionPermissionStore,
	capabilities domain.ExecutionPermissionRuntimeCapabilities,
) *ThreadExecutionPermissionService {
	return &ThreadExecutionPermissionService{store: store, capabilities: capabilities}
}

func (s *ThreadExecutionPermissionService) Current(ctx context.Context,
	threadID string,
) (domain.ThreadExecutionPermissionSnapshot, error) {
	if s == nil || s.store == nil {
		return domain.ThreadExecutionPermissionSnapshot{}, apperror.New(
			apperror.CodeFailedPrecondition, "Thread execution permission store is required")
	}
	threadID = strings.TrimSpace(threadID)
	if !domain.ValidAgentID(threadID) || strings.ContainsRune(threadID, 0) {
		return domain.ThreadExecutionPermissionSnapshot{}, apperror.New(
			apperror.CodeInvalidArgument, "Thread execution permission Thread id is invalid")
	}
	permission, err := s.store.GetThreadExecutionPermission(ctx, threadID)
	return permission, apperror.Normalize(err)
}

func (s *ThreadExecutionPermissionService) Inspect(ctx context.Context,
	threadID string,
) (CurrentThreadExecutionPermissionResult, error) {
	permission, err := s.Current(ctx, threadID)
	if err != nil {
		return CurrentThreadExecutionPermissionResult{}, err
	}
	threadRecord, err := s.store.GetThread(ctx, permission.ThreadID)
	if err != nil {
		return CurrentThreadExecutionPermissionResult{}, apperror.Normalize(err)
	}
	result := CurrentThreadExecutionPermissionResult{Permission: permission,
		CurrentRunID: threadRecord.ActiveRunID}
	if threadRecord.ActiveRunID == "" {
		return result, nil
	}
	runPermission, err := s.store.GetRunExecutionPermission(ctx, threadRecord.ActiveRunID)
	if err != nil {
		return CurrentThreadExecutionPermissionResult{}, apperror.Normalize(err)
	}
	result.CurrentRunMode = runPermission.Mode
	result.CurrentRunPermission = runPermission
	result.CurrentRunSynchronized = runPermission.Mode == permission.Mode &&
		runPermission.PolicyVersion == permission.PolicyVersion &&
		runPermission.ApprovalPolicy == permission.ApprovalPolicy &&
		runPermission.CommandScope == permission.CommandScope &&
		runPermission.FilesystemScope == permission.FilesystemScope &&
		runPermission.NetworkScope == permission.NetworkScope &&
		runPermission.PersistentTerminal == permission.PersistentTerminal &&
		runPermission.BackgroundProcess == permission.BackgroundProcess &&
		runPermission.AgentTerminalInput == permission.AgentTerminalInput &&
		runPermission.RiskTier == permission.RiskTier &&
		runPermission.RequiredGate == permission.RequiredGate &&
		!runPermission.ProcessEnabled && !runPermission.ExecutionAuthorized &&
		!runPermission.CapabilityGrant
	return result, nil
}

func (s *ThreadExecutionPermissionService) Change(ctx context.Context,
	request ChangeThreadExecutionPermissionRequest,
) (ChangeThreadExecutionPermissionResult, error) {
	if s == nil || s.store == nil {
		return ChangeThreadExecutionPermissionResult{}, apperror.New(
			apperror.CodeFailedPrecondition, "Thread execution permission store is required")
	}
	if err := s.capabilities.Validate(); err != nil {
		return ChangeThreadExecutionPermissionResult{}, apperror.Wrap(
			apperror.CodeFailedPrecondition,
			"Thread execution permission runtime capabilities are invalid", err)
	}
	normalized, target, confirmed, err :=
		normalizeChangeThreadExecutionPermissionRequest(request)
	if err != nil {
		return ChangeThreadExecutionPermissionResult{}, apperror.Wrap(
			apperror.CodeInvalidArgument, err.Error(), err)
	}
	if !s.capabilities.Allows(target) {
		return ChangeThreadExecutionPermissionResult{}, apperror.New(
			apperror.CodePolicyDenied,
			fmt.Sprintf(
				"Thread execution permission %s is unavailable because this process lacks gate %s",
				target, requiredExecutionPermissionGate(target)))
	}
	threadRecord, err := s.store.GetThread(ctx, normalized.ThreadID)
	if err != nil {
		return ChangeThreadExecutionPermissionResult{}, apperror.Normalize(err)
	}
	current, err := s.store.GetThreadExecutionPermission(ctx, threadRecord.ID)
	if err != nil {
		return ChangeThreadExecutionPermissionResult{}, apperror.Normalize(err)
	}
	var currentRunPermission *domain.RunExecutionPermissionSnapshot
	if threadRecord.ActiveRunID != "" {
		permission, err := s.store.GetRunExecutionPermission(ctx, threadRecord.ActiveRunID)
		if err != nil {
			return ChangeThreadExecutionPermissionResult{}, apperror.Normalize(err)
		}
		currentRunPermission = &permission
	}
	keyDigest := runmutation.Fingerprint(
		"thread_execution_permission_operation.v1", normalized.ThreadID,
		normalized.OperationKey)
	requestFingerprint := runmutation.Fingerprint(
		"thread_execution_permission_change_request.v1", normalized.ThreadID,
		string(target), fmt.Sprintf("%t", confirmed), normalized.RequestedBy,
		normalized.Reason)
	if replay, found, err := s.loadReplay(ctx, keyDigest, requestFingerprint,
		normalized.ThreadID, normalized.RequestedBy, target); err != nil {
		return ChangeThreadExecutionPermissionResult{}, err
	} else if found {
		// An exact Full replay is idempotent. In particular, do not revoke and
		// immediately recreate its live generation: an already-authorized job
		// must not be spuriously fenced merely because the client retried a
		// successful request. A cold process has no live grant, so
		// activateFullAccess may safely restore this exact current snapshot.
		if err := s.activateFullAccess(ctx, replay); err != nil {
			return ChangeThreadExecutionPermissionResult{}, err
		}
		return replay, nil
	}
	preRevokedRuntime := false
	if s.capabilities.RuntimeAuthority != nil &&
		threadPermissionRequiresImmediateRuntimeRevocation(
			current, currentRunPermission, target) {
		// A high-risk downgrade is fail-closed before its durable write. A
		// non-revoking change is fenced only after the Store confirms that it
		// actually applied to the current Run; a deferred preference must not
		// disturb an in-flight Run.
		if current.Mode.IsFullPreference() && !target.IsFullPreference() {
			s.capabilities.RuntimeAuthority.RevokeThread(normalized.ThreadID)
		}
		if threadRecord.ActiveRunID != "" {
			s.capabilities.RuntimeAuthority.RevokeRun(threadRecord.ActiveRunID)
		}
		preRevokedRuntime = true
	}
	if threadRecord.Status != domain.ThreadActive {
		return ChangeThreadExecutionPermissionResult{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"Thread execution permission can only change while the Thread is active")
	}
	now := time.Now().UTC()
	if now.Before(current.CreatedAt) {
		now = current.CreatedAt
	}
	next, err := current.Next(idgen.New("thread-exec-permission"), target, confirmed,
		normalized.RequestedBy, normalized.Reason, now)
	if err != nil {
		return ChangeThreadExecutionPermissionResult{}, apperror.Wrap(
			apperror.CodeInvalidArgument,
			"Thread execution permission transition is invalid", err)
	}
	operation := domain.ThreadExecutionPermissionOperation{
		KeyDigest: keyDigest, RequestFingerprint: requestFingerprint,
		SnapshotID: next.ID, ThreadID: next.ThreadID, RequestedBy: next.RequestedBy,
		CreatedAt: next.CreatedAt,
	}
	stored, storedOperation, replayed, err := s.store.TransitionThreadExecutionPermission(
		ctx, next, operation)
	if err != nil {
		return ChangeThreadExecutionPermissionResult{}, apperror.Normalize(err)
	}
	result := ChangeThreadExecutionPermissionResult{
		Permission: stored, CurrentRunID: storedOperation.CurrentRunID,
		CurrentRunEffect: storedOperation.CurrentRunEffect, Replayed: replayed,
	}
	currentRunPermissionChanged := currentRunPermission == nil ||
		storedOperation.CurrentRunPermissionSnapshotID != currentRunPermission.ID
	if s.capabilities.RuntimeAuthority != nil && !preRevokedRuntime &&
		result.CurrentRunEffect.AppliesToCurrentRun() && currentRunPermissionChanged {
		// A fresh operation may reaffirm a synchronized non-Full Thread
		// preference. The Store binds that operation to the existing immutable
		// Run snapshot; do not revoke live child fences when no Run permission
		// generation actually changed.
		if !target.IsFullPreference() {
			s.capabilities.RuntimeAuthority.RevokeThread(normalized.ThreadID)
		}
		if result.CurrentRunID != "" {
			s.capabilities.RuntimeAuthority.RevokeRun(result.CurrentRunID)
		}
	}
	if err := s.activateFullAccess(ctx, result); err != nil {
		return ChangeThreadExecutionPermissionResult{}, err
	}
	return result, nil
}

func threadPermissionRequiresImmediateRuntimeRevocation(
	current domain.ThreadExecutionPermissionSnapshot,
	currentRun *domain.RunExecutionPermissionSnapshot,
	target domain.RunExecutionPermissionMode,
) bool {
	if domain.PermissionTransitionRevokes(current.Mode, target) {
		return true
	}
	if currentRun == nil {
		// A same-mode Full re-confirmation with no active Run rotates the
		// process-local Thread grant immediately.
		return current.Mode.IsFullPreference() && target.IsFullPreference()
	}
	return domain.PermissionTransitionRevokes(currentRun.Mode, target)
}

func (s *ThreadExecutionPermissionService) activateFullAccess(ctx context.Context,
	result ChangeThreadExecutionPermissionResult,
) error {
	if result.Permission.Mode != domain.RunExecutionPermissionFull {
		return nil
	}
	if s.capabilities.RuntimeAuthority == nil {
		return apperror.New(apperror.CodeFailedPrecondition,
			"dynamic Full Access runtime authority is unavailable")
	}
	currentThreadPermission, err := s.store.GetThreadExecutionPermission(
		ctx, result.Permission.ThreadID)
	if err != nil {
		return apperror.Normalize(err)
	}
	if currentThreadPermission.ID != result.Permission.ID ||
		currentThreadPermission.Revision != result.Permission.Revision ||
		currentThreadPermission.Mode != result.Permission.Mode {
		// Never let replaying an older Full operation recreate a live Thread
		// grant after a newer durable downgrade.
		return nil
	}
	if result.CurrentRunID != "" &&
		result.CurrentRunEffect == domain.ThreadExecutionPermissionDeferred {
		// Keep the current Run's permission and fences unchanged. The explicit
		// confirmation must still authorize the future Thread preference, or a
		// successor could materialize Full but never bind a live runtime grant.
		if s.capabilities.RuntimeAuthority.AllowsThreadFullAccess(currentThreadPermission, nil) {
			return nil
		}
		if _, err := s.capabilities.RuntimeAuthority.ActivateThreadFullPreference(currentThreadPermission); err != nil {
			return apperror.Wrap(apperror.CodeFailedPrecondition,
				"dynamic Thread Full Access preference could not be activated", err)
		}
		return nil
	}
	threadRecord, err := s.store.GetThread(ctx, result.Permission.ThreadID)
	if err != nil {
		return apperror.Normalize(err)
	}
	var runPermission *domain.RunExecutionPermissionSnapshot
	if threadRecord.ActiveRunID != "" {
		current, err := s.store.GetRunExecutionPermission(ctx, threadRecord.ActiveRunID)
		if err != nil {
			return apperror.Normalize(err)
		}
		runPermission = &current
	}
	if s.capabilities.RuntimeAuthority.AllowsThreadFullAccess(
		currentThreadPermission, runPermission) {
		return nil
	}
	if _, err := s.capabilities.RuntimeAuthority.ActivateThreadFullAccess(
		result.Permission, runPermission); err != nil {
		return apperror.Wrap(apperror.CodeFailedPrecondition,
			"dynamic Thread Full Access could not be activated", err)
	}
	return nil
}

func (s *ThreadExecutionPermissionService) loadReplay(ctx context.Context,
	keyDigest, requestFingerprint, threadID, requestedBy string,
	target domain.RunExecutionPermissionMode,
) (ChangeThreadExecutionPermissionResult, bool, error) {
	existing, found, err := s.store.GetThreadExecutionPermissionOperation(ctx, keyDigest)
	if err != nil {
		return ChangeThreadExecutionPermissionResult{}, false, apperror.Normalize(err)
	}
	if !found {
		return ChangeThreadExecutionPermissionResult{}, false, nil
	}
	if existing.RequestFingerprint != requestFingerprint ||
		existing.ThreadID != threadID || existing.RequestedBy != requestedBy {
		return ChangeThreadExecutionPermissionResult{}, true, apperror.New(
			apperror.CodeConflict,
			"Thread execution permission operation key was already used for different intent")
	}
	stored, err := s.store.GetThreadExecutionPermissionSnapshot(ctx, existing.SnapshotID)
	if err != nil {
		return ChangeThreadExecutionPermissionResult{}, true, apperror.Normalize(err)
	}
	if stored.ID != existing.SnapshotID || stored.ThreadID != existing.ThreadID ||
		stored.RequestedBy != existing.RequestedBy ||
		!stored.CreatedAt.Equal(existing.CreatedAt) || stored.Mode != target ||
		stored.ProcessEnabled || stored.ExecutionAuthorized || stored.CapabilityGrant {
		return ChangeThreadExecutionPermissionResult{}, true, apperror.New(
			apperror.CodeInternal,
			"stored Thread execution permission operation binding is invalid")
	}
	return ChangeThreadExecutionPermissionResult{
		Permission: stored, CurrentRunID: existing.CurrentRunID,
		CurrentRunEffect: existing.CurrentRunEffect, Replayed: true,
	}, true, nil
}

func normalizeChangeThreadExecutionPermissionRequest(
	request ChangeThreadExecutionPermissionRequest,
) (ChangeThreadExecutionPermissionRequest, domain.RunExecutionPermissionMode, bool, error) {
	// Reuse the Run selector's exact actor, reason, operation-key, confirmation,
	// and mode validation. Thread preferences intentionally have the same risk
	// acknowledgement and process-local runtime gates as Run selections.
	normalized, mode, confirmed, err := normalizeChangeRunExecutionPermissionRequest(
		ChangeRunExecutionPermissionRequest{
			RunID: request.ThreadID, Mode: request.Mode,
			OperationKey: request.OperationKey, RequestedBy: request.RequestedBy,
			Reason:                  request.Reason,
			ConfirmFull:             request.ConfirmFull,
			ConfirmWorkspaceAccess:  request.ConfirmWorkspaceAccess,
			ConfirmUserApproval:     request.ConfirmUserApproval,
			ConfirmDangerFullAccess: request.ConfirmDangerFullAccess,
			ConfirmDebugAccess:      request.ConfirmDebugAccess,
		})
	if err != nil {
		return ChangeThreadExecutionPermissionRequest{}, "", false, err
	}
	request.ThreadID = normalized.RunID
	request.Mode = normalized.Mode
	request.OperationKey = normalized.OperationKey
	request.RequestedBy = normalized.RequestedBy
	request.Reason = normalized.Reason
	return request, mode, confirmed, nil
}
