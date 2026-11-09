package application

import (
	"context"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
)

type RunWakeControlStore interface {
	GetRun(context.Context, string) (domain.Run, error)
	GetRunExecutionLease(context.Context, string) (domain.RunExecutionLease, bool, error)
	GetOperatorSteeringQueueSummary(context.Context,
		string) (domain.OperatorSteeringQueueSummary, error)
	GetRunWakeOperation(context.Context, string) (domain.RunWakeOperation, bool, error)
	GetLatestRunWakeIntent(context.Context, string) (domain.RunWakeIntent, bool, error)
	GetRunWakeIntent(context.Context, string) (domain.RunWakeIntent, error)
	CreateRunWakeIntent(context.Context, domain.RunWakeIntent, domain.RunWakeOperation) (
		domain.RunWakeIntent, domain.RunWakeOperation, bool, error)
	CancelRunWakeIntent(context.Context, string, time.Time, domain.RunWakeOperation) (
		domain.RunWakeIntent, domain.RunWakeOperation, bool, error)
}

type RunWakeControlService struct {
	store RunWakeControlStore
	now   func() time.Time
}

type ScheduleRunWakeRequest struct {
	Version             string
	RunID               string
	OperationKey        string
	RequestedBy         string
	MaxAttempts         int
	InitialDelaySeconds int
	BaseBackoffSeconds  int
	MaxBackoffSeconds   int
	MaxElapsedSeconds   int
}

type CancelRunWakeRequest struct {
	Version      string
	RunID        string
	OperationKey string
	RequestedBy  string
}

type RunWakeControlResult struct {
	Intent    domain.RunWakeIntent
	Operation domain.RunWakeOperation
	Replayed  bool
}

func NewRunWakeControlService(store RunWakeControlStore) *RunWakeControlService {
	return &RunWakeControlService{store: store, now: func() time.Time { return time.Now().UTC() }}
}

func (s *RunWakeControlService) Schedule(ctx context.Context,
	request ScheduleRunWakeRequest,
) (RunWakeControlResult, error) {
	if s == nil || s.store == nil || s.now == nil {
		return RunWakeControlResult{}, apperror.New(
			apperror.CodeFailedPrecondition, "Run wake control store is required")
	}
	normalized, err := normalizeScheduleRunWakeRequest(request)
	if err != nil {
		return RunWakeControlResult{}, err
	}
	keyDigest := runmutation.RunWakeOperationDigest(normalized.RunID,
		normalized.OperationKey)
	fingerprint := runmutation.RunWakeScheduleRequestFingerprint(normalized.RunID,
		normalized.RequestedBy, normalized.MaxAttempts, normalized.InitialDelaySeconds,
		normalized.BaseBackoffSeconds, normalized.MaxBackoffSeconds,
		normalized.MaxElapsedSeconds)
	if replay, found, err := s.loadReplay(ctx, keyDigest, fingerprint,
		domain.RunWakeSchedule, normalized.RunID, normalized.RequestedBy); err != nil || found {
		return replay, err
	}
	run, err := s.requireEligibleRun(ctx, normalized.RunID)
	if err != nil {
		return RunWakeControlResult{}, err
	}
	if existing, found, err := s.store.GetLatestRunWakeIntent(ctx, run.ID); err != nil {
		return RunWakeControlResult{}, apperror.Normalize(err)
	} else if found && (existing.Status == domain.RunWakeQueued ||
		existing.Status == domain.RunWakeLeased) {
		return RunWakeControlResult{}, apperror.New(apperror.CodeConflict,
			"Run already has an active wake intent")
	}
	now := s.now().UTC()
	intent := domain.RunWakeIntent{
		ID: idgen.New("wake"), ProtocolVersion: domain.RunWakeIntentProtocolVersion,
		RunID: run.ID, SessionID: run.SessionID, Status: domain.RunWakeQueued,
		MaxAttempts:         normalized.MaxAttempts,
		InitialDelaySeconds: normalized.InitialDelaySeconds,
		BaseBackoffSeconds:  normalized.BaseBackoffSeconds,
		MaxBackoffSeconds:   normalized.MaxBackoffSeconds,
		MaxElapsedSeconds:   normalized.MaxElapsedSeconds,
		NextWakeAt:          now.Add(time.Duration(normalized.InitialDelaySeconds) * time.Second),
		DeadlineAt:          now.Add(time.Duration(normalized.MaxElapsedSeconds) * time.Second),
		CreatedAt:           now, UpdatedAt: now,
	}
	operation := domain.RunWakeOperation{
		ProtocolVersion: domain.RunWakeControlProtocolVersion,
		KeyDigest:       keyDigest, RequestFingerprint: fingerprint,
		Action: domain.RunWakeSchedule, IntentID: intent.ID, RunID: run.ID,
		RequestedBy: normalized.RequestedBy, CreatedAt: now,
	}
	stored, storedOperation, replayed, err := s.store.CreateRunWakeIntent(ctx,
		intent, operation)
	return RunWakeControlResult{Intent: stored, Operation: storedOperation,
		Replayed: replayed}, apperror.Normalize(err)
}

func (s *RunWakeControlService) Cancel(ctx context.Context,
	request CancelRunWakeRequest,
) (RunWakeControlResult, error) {
	if s == nil || s.store == nil || s.now == nil {
		return RunWakeControlResult{}, apperror.New(
			apperror.CodeFailedPrecondition, "Run wake control store is required")
	}
	normalized, err := normalizeCancelRunWakeRequest(request)
	if err != nil {
		return RunWakeControlResult{}, err
	}
	keyDigest := runmutation.RunWakeOperationDigest(normalized.RunID,
		normalized.OperationKey)
	fingerprint := runmutation.RunWakeCancelRequestFingerprint(normalized.RunID,
		normalized.RequestedBy)
	if replay, found, err := s.loadReplay(ctx, keyDigest, fingerprint,
		domain.RunWakeCancel, normalized.RunID, normalized.RequestedBy); err != nil || found {
		return replay, err
	}
	if _, err := s.store.GetRun(ctx, normalized.RunID); err != nil {
		return RunWakeControlResult{}, apperror.Normalize(err)
	}
	intent, found, err := s.store.GetLatestRunWakeIntent(ctx, normalized.RunID)
	if err != nil {
		return RunWakeControlResult{}, apperror.Normalize(err)
	}
	if !found || (intent.Status != domain.RunWakeQueued &&
		intent.Status != domain.RunWakeLeased) {
		return RunWakeControlResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"Run has no active wake intent")
	}
	now := s.now().UTC()
	operation := domain.RunWakeOperation{
		ProtocolVersion: domain.RunWakeControlProtocolVersion,
		KeyDigest:       keyDigest, RequestFingerprint: fingerprint,
		Action: domain.RunWakeCancel, RunID: normalized.RunID,
		RequestedBy: normalized.RequestedBy, CreatedAt: now,
	}
	stored, storedOperation, replayed, err := s.store.CancelRunWakeIntent(ctx,
		normalized.RunID, now, operation)
	return RunWakeControlResult{Intent: stored, Operation: storedOperation,
		Replayed: replayed}, apperror.Normalize(err)
}

func (s *RunWakeControlService) Get(ctx context.Context,
	runID string,
) (domain.RunWakeIntent, bool, error) {
	if s == nil || s.store == nil {
		return domain.RunWakeIntent{}, false, apperror.New(
			apperror.CodeFailedPrecondition, "Run wake control store is required")
	}
	if !validControlIdentity(runID) {
		return domain.RunWakeIntent{}, false, apperror.New(
			apperror.CodeInvalidArgument, "Run wake Run id is invalid")
	}
	if _, err := s.store.GetRun(ctx, runID); err != nil {
		return domain.RunWakeIntent{}, false, apperror.Normalize(err)
	}
	intent, found, err := s.store.GetLatestRunWakeIntent(ctx, runID)
	return intent, found, apperror.Normalize(err)
}

func (s *RunWakeControlService) loadReplay(ctx context.Context, keyDigest string,
	fingerprint string, action domain.RunWakeAction, runID string,
	requestedBy string,
) (RunWakeControlResult, bool, error) {
	operation, found, err := s.store.GetRunWakeOperation(ctx, keyDigest)
	if err != nil || !found {
		return RunWakeControlResult{}, found, apperror.Normalize(err)
	}
	if operation.ProtocolVersion != domain.RunWakeControlProtocolVersion ||
		operation.RequestFingerprint != fingerprint || operation.Action != action ||
		operation.RunID != runID || operation.RequestedBy != requestedBy {
		return RunWakeControlResult{}, true, apperror.New(apperror.CodeConflict,
			"Run wake operation key was already used for different intent")
	}
	intent, err := s.store.GetRunWakeIntent(ctx, operation.IntentID)
	if err != nil {
		return RunWakeControlResult{}, true, apperror.Normalize(err)
	}
	return RunWakeControlResult{Intent: intent, Operation: operation, Replayed: true},
		true, nil
}

func (s *RunWakeControlService) requireEligibleRun(ctx context.Context,
	runID string,
) (domain.Run, error) {
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return domain.Run{}, apperror.Normalize(err)
	}
	if run.Status != domain.RunRunning || run.SessionID == "" {
		return domain.Run{}, apperror.New(apperror.CodeFailedPrecondition,
			"Run must be running with an attached Session before scheduling a wake")
	}
	lease, found, err := s.store.GetRunExecutionLease(ctx, run.ID)
	if err != nil {
		return domain.Run{}, apperror.Normalize(err)
	}
	if found && lease.ActiveAt(s.now().UTC()) {
		return domain.Run{}, apperror.New(apperror.CodeConflict,
			"Run has an active execution lease")
	}
	queue, err := s.store.GetOperatorSteeringQueueSummary(ctx, run.ID)
	if err != nil {
		return domain.Run{}, apperror.Normalize(err)
	}
	if queue.Pending == 0 {
		return domain.Run{}, apperror.New(apperror.CodeFailedPrecondition,
			"Run wake scheduling requires pending operator work")
	}
	return run, nil
}

func normalizeScheduleRunWakeRequest(request ScheduleRunWakeRequest) (
	ScheduleRunWakeRequest, error,
) {
	if request.Version != domain.RunWakeControlProtocolVersion ||
		!validControlIdentity(request.RunID) || !validControlIdentity(request.RequestedBy) ||
		request.MaxAttempts < 1 || request.MaxAttempts > domain.MaxRunWakeAttempts ||
		request.InitialDelaySeconds < 0 ||
		request.InitialDelaySeconds > domain.MaxRunWakeInitialDelaySeconds ||
		request.BaseBackoffSeconds < domain.MinRunWakeBackoffSeconds ||
		request.BaseBackoffSeconds > domain.MaxRunWakeBackoffSeconds ||
		request.MaxBackoffSeconds < request.BaseBackoffSeconds ||
		request.MaxBackoffSeconds > domain.MaxRunWakeBackoffSeconds ||
		request.MaxElapsedSeconds < domain.MinRunWakeElapsedSeconds ||
		request.MaxElapsedSeconds > domain.MaxRunWakeElapsedSeconds ||
		request.InitialDelaySeconds > request.MaxElapsedSeconds {
		return ScheduleRunWakeRequest{}, apperror.New(apperror.CodeInvalidArgument,
			"Run wake schedule request is invalid")
	}
	key, err := domain.NormalizeAgentOperationKey(request.OperationKey)
	if err != nil || key != request.OperationKey || containsSpaceOrControl(key) {
		return ScheduleRunWakeRequest{}, apperror.New(apperror.CodeInvalidArgument,
			"Run wake idempotency key is invalid")
	}
	request.OperationKey = key
	return request, nil
}

func normalizeCancelRunWakeRequest(request CancelRunWakeRequest) (
	CancelRunWakeRequest, error,
) {
	if request.Version != domain.RunWakeControlProtocolVersion ||
		!validControlIdentity(request.RunID) || !validControlIdentity(request.RequestedBy) {
		return CancelRunWakeRequest{}, apperror.New(apperror.CodeInvalidArgument,
			"Run wake cancellation request is invalid")
	}
	key, err := domain.NormalizeAgentOperationKey(request.OperationKey)
	if err != nil || key != request.OperationKey || containsSpaceOrControl(key) {
		return CancelRunWakeRequest{}, apperror.New(apperror.CodeInvalidArgument,
			"Run wake idempotency key is invalid")
	}
	request.OperationKey = key
	return request, nil
}

type RunWakeCoordinatorStore interface {
	ListDueRunWakeIntents(context.Context, time.Time, int, int) ([]domain.RunWakeIntent, error)
	AcquireRunWake(context.Context, string, string, string, time.Time) (
		domain.RunWakeIntent, domain.RunWakeLease, bool, error)
	ReleaseRunWakeForRetry(context.Context, domain.RunWakeLease, time.Time) (
		domain.RunWakeIntent, error)
}

// RunWakeCoordinator exposes ownership primitives only. It deliberately has no
// AgentRunner, Router, Tool Gateway, or background goroutine dependency.
type RunWakeCoordinator struct {
	store RunWakeCoordinatorStore
	now   func() time.Time
}

func NewRunWakeCoordinator(store RunWakeCoordinatorStore) *RunWakeCoordinator {
	return &RunWakeCoordinator{store: store, now: func() time.Time { return time.Now().UTC() }}
}

func (c *RunWakeCoordinator) Due(ctx context.Context,
	limit, maxSteps int,
) ([]domain.RunWakeIntent, error) {
	if c == nil || c.store == nil || c.now == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"Run wake coordinator store is required")
	}
	if limit < 1 || limit > 64 {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"Run wake due limit must be between 1 and 64")
	}
	if maxSteps < 1 || maxSteps > domain.MaxRunExecutionHandoffSteps {
		return nil, apperror.New(apperror.CodeInvalidArgument, "Run wake due step limit is invalid")
	}
	values, err := c.store.ListDueRunWakeIntents(ctx, c.now().UTC(), limit, maxSteps)
	return values, apperror.Normalize(err)
}

func (c *RunWakeCoordinator) Claim(ctx context.Context, intentID string,
	ownerID string,
) (domain.RunWakeIntent, domain.RunWakeLease, bool, error) {
	if c == nil || c.store == nil || c.now == nil || !validControlIdentity(intentID) ||
		!validControlIdentity(ownerID) {
		return domain.RunWakeIntent{}, domain.RunWakeLease{}, false,
			apperror.New(apperror.CodeInvalidArgument, "Run wake claim request is invalid")
	}
	intent, lease, acquired, err := c.store.AcquireRunWake(ctx, intentID, ownerID,
		idgen.New("wake-lease"), c.now().UTC())
	return intent, lease, acquired, apperror.Normalize(err)
}

func (c *RunWakeCoordinator) Retry(ctx context.Context,
	lease domain.RunWakeLease,
) (domain.RunWakeIntent, error) {
	if c == nil || c.store == nil || c.now == nil {
		return domain.RunWakeIntent{}, apperror.New(apperror.CodeFailedPrecondition,
			"Run wake coordinator store is required")
	}
	intent, err := c.store.ReleaseRunWakeForRetry(ctx, lease, c.now().UTC())
	return intent, apperror.Normalize(err)
}
