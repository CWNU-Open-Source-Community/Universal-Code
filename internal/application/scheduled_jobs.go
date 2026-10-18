package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/durableoperation"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/scheduler"
)

const (
	scheduledJobObservationEventLimit = 100
	scheduledJobExecutionTimeout      = 20 * time.Second
)

type ScheduledJobStore interface {
	GetRun(context.Context, string) (domain.Run, error)
	GetRootAgent(context.Context, string) (domain.AgentNode, bool, error)
	GetRunMode(context.Context, string) (domain.RunModeSnapshot, error)
	GetRunExecutionPermission(context.Context, string) (
		domain.RunExecutionPermissionSnapshot, error)
	LatestRunEventSequence(context.Context, string) (int64, error)
	ListRunEventsAfterSequence(context.Context, string, int64, int) ([]events.Event, error)

	GetScheduledJob(context.Context, string) (domain.ScheduledJob, error)
	ListScheduledJobs(context.Context, string, int) ([]domain.ScheduledJob, error)
	GetScheduledJobOperation(context.Context, string) (
		domain.ScheduledJobOperation, bool, error)
	GetScheduledJobAuthorization(context.Context, string) (
		domain.ScheduledJobAuthorization, bool, error)
	CreateScheduledJob(context.Context, domain.ScheduledJob,
		*domain.ScheduledJobAuthorization, domain.ScheduledJobOperation) (
		domain.ScheduledJob, bool, error)
	TransitionScheduledJob(context.Context, string, domain.ScheduledJobAction,
		int64, time.Time, domain.ScheduledJobOperation) (domain.ScheduledJob, bool, error)
	ListScheduledJobRounds(context.Context, string, int) ([]domain.ScheduledJobRound, error)
	ListScheduledJobNotifications(context.Context, string, int) (
		[]domain.ScheduledJobNotification, error)
	ClaimDueScheduledJob(context.Context, string, time.Time) (
		domain.ScheduledJob, domain.ScheduledJobLease, bool, error)
	CompleteScheduledJobRound(context.Context, domain.ScheduledJobLease,
		domain.ScheduledJobRoundOutcome, time.Time) (domain.ScheduledJob,
		domain.ScheduledJobRound, *domain.ScheduledJobNotification, error)
	FailScheduledJobRound(context.Context, domain.ScheduledJobLease,
		domain.ScheduledJobRoundFailure, time.Time) (domain.ScheduledJob,
		domain.ScheduledJobRound, *domain.ScheduledJobNotification, error)
	ReconcileScheduledJobs(context.Context, time.Time, int) (int, error)
}

type scheduledJobObservationStore interface {
	CreateScheduledJobWithObservationConsent(context.Context, domain.ScheduledJob,
		*domain.ScheduledJobAuthorization, domain.ScheduledJobObservationConsent,
		domain.ScheduledJobOperation) (domain.ScheduledJob, bool, error)
	EnableScheduledJobObservation(context.Context,
		domain.ScheduledJobObservationConsent, int64) (domain.ScheduledJob, bool, error)
	ClaimDueObservationScheduledJob(context.Context, string, time.Time) (
		domain.ScheduledJob, domain.ScheduledJobLease, bool, error)
	ReconcileObservationScheduledJobs(context.Context, time.Time, int) (int, error)
}

// ScheduledJobRoundExecutor is intentionally narrower than AgentRunner. It
// receives metadata-only observation facts and a stable operation key. The
// coordinator validates returned model/tool facts against the persisted mode.
type ScheduledJobRoundExecutor interface {
	ExecuteScheduledJobRound(context.Context, ScheduledJobExecutionRequest) (
		ScheduledJobExecutionResult, error)
}

type ScheduledJobExecutionRequest struct {
	Job                     domain.ScheduledJob
	Lease                   domain.ScheduledJobLease
	Authorization           *domain.ScheduledJobAuthorization
	ObservationSHA256       string
	EventSequence           int64
	RelevantEventCount      int
	ObservationWasTruncated bool
}

type ScheduledJobExecutionResult struct {
	ModelCalled bool
	ToolCalled  bool
}

type ScheduledJobService struct {
	store    ScheduledJobStore
	executor ScheduledJobRoundExecutor
	clock    scheduler.Clock
}

type CreateScheduledJobRequest struct {
	Version                   string
	RunID                     string
	TargetRunID               string
	Schedule                  domain.ScheduledJobSchedule
	DeadlineAt                time.Time
	StopOnTargetTerminal      bool
	MaxRounds                 int
	MaxModelCalls             int
	MaxElapsedSeconds         int64
	Retry                     domain.ScheduledJobRetryPolicy
	Notification              domain.ScheduledJobNotificationMode
	ExecutionMode             domain.ScheduledJobExecutionMode
	ConfirmRepair             bool
	ObservationConsentVersion int
	ExecutionConsentVersion   int
	ExecutionTarget           *domain.ScheduledJobExecutionTarget
	OperationKey              string
	RequestedBy               string
}

type EnableScheduledJobObservationRequest struct {
	RunID                     string
	JobID                     string
	ExpectedRevision          int64
	ObservationConsentVersion int
	OperationKey              string
	RequestedBy               string
}

type TransitionScheduledJobRequest struct {
	Version          string
	RunID            string
	JobID            string
	Action           domain.ScheduledJobAction
	ExpectedRevision int64
	OperationKey     string
	RequestedBy      string
}

type ScheduledJobControlResult struct {
	Job      domain.ScheduledJob `json:"job"`
	Replayed bool                `json:"replayed"`
}

type ScheduledJobSnapshot struct {
	Job           domain.ScheduledJob               `json:"job"`
	Authorization *domain.ScheduledJobAuthorization `json:"authorization,omitempty"`
	Rounds        []domain.ScheduledJobRound        `json:"rounds"`
	Notifications []domain.ScheduledJobNotification `json:"notifications"`
	Executions    []domain.ScheduledJobExecution    `json:"executions"`
}

func NewScheduledJobService(store ScheduledJobStore) *ScheduledJobService {
	return &ScheduledJobService{store: store, clock: scheduler.RealClock{}}
}

func (s *ScheduledJobService) WithRoundExecutor(
	executor ScheduledJobRoundExecutor,
) *ScheduledJobService {
	if s != nil {
		s.executor = executor
	}
	return s
}

func (s *ScheduledJobService) WithClock(clock scheduler.Clock) *ScheduledJobService {
	if s != nil && clock != nil {
		s.clock = clock
	}
	return s
}

func (s *ScheduledJobService) Create(ctx context.Context,
	request CreateScheduledJobRequest,
) (ScheduledJobControlResult, error) {
	if s == nil || s.store == nil || s.clock == nil {
		return ScheduledJobControlResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"scheduled job dependencies are required")
	}
	normalized, spec, err := normalizeCreateScheduledJobRequest(request)
	if err != nil {
		return ScheduledJobControlResult{}, err
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return ScheduledJobControlResult{}, apperror.Normalize(err)
	}
	keyDigest := runmutation.ScheduledJobOperationDigest(normalized.RunID,
		normalized.OperationKey)
	fingerprint := runmutation.ScheduledJobCreateRequestFingerprint(normalized.RunID,
		string(specJSON), normalized.RequestedBy, normalized.ConfirmRepair)
	if normalized.ObservationConsentVersion != 0 {
		fingerprint = runmutation.Fingerprint("scheduled_job_create_observation_consent.v1",
			fingerprint, strconv.Itoa(normalized.ObservationConsentVersion))
	}
	if replay, found, err := s.loadReplay(ctx, keyDigest, fingerprint,
		domain.ScheduledJobCreate, normalized.RunID, "", 0,
		normalized.RequestedBy); err != nil || found {
		return replay, err
	}
	run, err := s.store.GetRun(ctx, normalized.RunID)
	if err != nil {
		return ScheduledJobControlResult{}, apperror.Normalize(err)
	}
	if run.Terminal() || run.ID != spec.TargetRunID {
		return ScheduledJobControlResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"scheduled jobs require one explicit non-terminal owner Run target")
	}
	root, found, err := s.store.GetRootAgent(ctx, run.ID)
	if err != nil || !found || root.Role != domain.AgentRoleRoot || root.RunID != run.ID {
		if err == nil {
			err = apperror.New(apperror.CodeFailedPrecondition,
				"scheduled jobs require the current Run root Agent")
		}
		return ScheduledJobControlResult{}, apperror.Normalize(err)
	}
	mode, err := s.store.GetRunMode(ctx, run.ID)
	if err != nil {
		return ScheduledJobControlResult{}, apperror.Normalize(err)
	}
	permission, err := s.store.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		return ScheduledJobControlResult{}, apperror.Normalize(err)
	}
	now := s.clock.Now().UTC()
	if !now.Before(spec.DeadlineAt) {
		return ScheduledJobControlResult{}, apperror.New(apperror.CodeInvalidArgument,
			"scheduled job deadline must still be in the future")
	}
	jobID := idgen.New("scheduled-job")
	nextWake := spec.Schedule.AnchorAt
	job := domain.ScheduledJob{
		ID: jobID, Spec: spec, OwnerRunID: run.ID, OwnerRootAgentID: root.ID,
		Status: domain.ScheduledJobActive, Revision: 1, NextWakeAt: &nextWake,
		CreatedBy: normalized.RequestedBy, CreatedAt: now, UpdatedAt: now,
	}
	var authorization *domain.ScheduledJobAuthorization
	if spec.ExecutionMode == domain.ScheduledJobApprovedRepair {
		if !normalized.ConfirmRepair || mode.Surface != domain.ExecutionSurfaceCode ||
			mode.Phase != domain.ExecutionPhaseDeliver ||
			permission.Mode != domain.RunExecutionPermissionFull ||
			!permission.OperatorConfirmed {
			return ScheduledJobControlResult{}, apperror.New(apperror.CodeFailedPrecondition,
				"repair scheduling requires exact operator-confirmed Code/Deliver permission")
		}
		expires := spec.DeadlineAt
		elapsedExpiry := now.Add(time.Duration(spec.MaxElapsedSeconds) * time.Second)
		if elapsedExpiry.Before(expires) {
			expires = elapsedExpiry
		}
		authorization = &domain.ScheduledJobAuthorization{
			ProtocolVersion: domain.ScheduledJobAuthProtocolVersion,
			JobID:           job.ID, RunID: run.ID, ModeSnapshotID: mode.ID,
			ModeRevision: mode.Revision, PermissionSnapshotID: permission.ID,
			PermissionRevision: permission.Revision, AuthorizedBy: normalized.RequestedBy,
			AuthorizedAt: now, ExpiresAt: expires,
		}
	} else if normalized.ConfirmRepair {
		return ScheduledJobControlResult{}, apperror.New(apperror.CodeInvalidArgument,
			"read-only scheduled jobs cannot accept repair confirmation")
	}
	operation := domain.ScheduledJobOperation{
		ProtocolVersion: domain.ScheduledJobControlProtocolVersion,
		KeyDigest:       keyDigest, RequestFingerprint: fingerprint,
		Action: domain.ScheduledJobCreate, JobID: job.ID, RunID: run.ID,
		ExpectedRevision: 0, RequestedBy: normalized.RequestedBy, CreatedAt: now,
	}
	var stored domain.ScheduledJob
	var replayed bool
	if normalized.ObservationConsentVersion == 0 {
		stored, replayed, err = s.store.CreateScheduledJob(ctx, job, authorization, operation)
	} else {
		observationStore, ok := s.store.(scheduledJobObservationStore)
		if !ok {
			return ScheduledJobControlResult{}, apperror.New(apperror.CodeFailedPrecondition,
				"scheduled job observation store is required")
		}
		consent := domain.ScheduledJobObservationConsent{
			JobID: job.ID, RunID: run.ID,
			Version:     normalized.ObservationConsentVersion,
			ConfirmedBy: normalized.RequestedBy, ConfirmedAt: now,
			OperationKeySHA256: keyDigest, RequestFingerprint: fingerprint,
		}
		stored, replayed, err = observationStore.CreateScheduledJobWithObservationConsent(
			ctx, job, authorization, consent, operation)
	}
	return ScheduledJobControlResult{Job: stored, Replayed: replayed},
		apperror.Normalize(err)
}

// EnableObservation records durable consent for one exact existing read-only,
// zero-model job. The operation never changes the job status or revision.
func (s *ScheduledJobService) EnableObservation(ctx context.Context,
	request EnableScheduledJobObservationRequest,
) (ScheduledJobControlResult, error) {
	if s == nil || s.store == nil || s.clock == nil {
		return ScheduledJobControlResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"scheduled job dependencies are required")
	}
	normalized, err := normalizeEnableScheduledJobObservationRequest(request)
	if err != nil {
		return ScheduledJobControlResult{}, err
	}
	observationStore, ok := s.store.(scheduledJobObservationStore)
	if !ok {
		return ScheduledJobControlResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"scheduled job observation store is required")
	}
	keyDigest := runmutation.ScheduledJobOperationDigest(normalized.RunID,
		normalized.OperationKey)
	fingerprint := runmutation.Fingerprint("scheduled_job_observation_consent_request.v1",
		normalized.RunID, normalized.JobID,
		strconv.FormatInt(normalized.ExpectedRevision, 10),
		strconv.Itoa(normalized.ObservationConsentVersion), normalized.RequestedBy)
	now := s.clock.Now().UTC()
	consent := domain.ScheduledJobObservationConsent{
		JobID: normalized.JobID, RunID: normalized.RunID,
		Version:     normalized.ObservationConsentVersion,
		ConfirmedBy: normalized.RequestedBy, ConfirmedAt: now,
		OperationKeySHA256: keyDigest, RequestFingerprint: fingerprint,
	}
	job, replayed, err := observationStore.EnableScheduledJobObservation(ctx,
		consent, normalized.ExpectedRevision)
	return ScheduledJobControlResult{Job: job, Replayed: replayed},
		apperror.Normalize(err)
}

func (s *ScheduledJobService) Transition(ctx context.Context,
	request TransitionScheduledJobRequest,
) (ScheduledJobControlResult, error) {
	if s == nil || s.store == nil || s.clock == nil {
		return ScheduledJobControlResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"scheduled job dependencies are required")
	}
	normalized, err := normalizeTransitionScheduledJobRequest(request)
	if err != nil {
		return ScheduledJobControlResult{}, err
	}
	keyDigest := runmutation.ScheduledJobOperationDigest(normalized.RunID,
		normalized.OperationKey)
	fingerprint := runmutation.ScheduledJobTransitionRequestFingerprint(
		normalized.RunID, normalized.JobID, string(normalized.Action),
		normalized.ExpectedRevision, normalized.RequestedBy)
	if replay, found, err := s.loadReplay(ctx, keyDigest, fingerprint,
		normalized.Action, normalized.RunID, normalized.JobID,
		normalized.ExpectedRevision, normalized.RequestedBy); err != nil || found {
		return replay, err
	}
	job, err := s.store.GetScheduledJob(ctx, normalized.JobID)
	if err != nil {
		return ScheduledJobControlResult{}, apperror.Normalize(err)
	}
	if job.OwnerRunID != normalized.RunID {
		return ScheduledJobControlResult{}, apperror.New(apperror.CodeConflict,
			"scheduled job owner binding changed")
	}
	now := s.clock.Now().UTC()
	if now.Before(job.UpdatedAt) {
		now = job.UpdatedAt
	}
	operation := domain.ScheduledJobOperation{
		ProtocolVersion: domain.ScheduledJobControlProtocolVersion,
		KeyDigest:       keyDigest, RequestFingerprint: fingerprint,
		Action: normalized.Action, JobID: job.ID, RunID: job.OwnerRunID,
		ExpectedRevision: normalized.ExpectedRevision,
		RequestedBy:      normalized.RequestedBy, CreatedAt: now,
	}
	stored, replayed, err := s.store.TransitionScheduledJob(ctx, job.ID,
		normalized.Action, normalized.ExpectedRevision, now, operation)
	return ScheduledJobControlResult{Job: stored, Replayed: replayed},
		apperror.Normalize(err)
}

func (s *ScheduledJobService) Get(ctx context.Context, jobID string,
	roundLimit int, notificationLimit int,
) (ScheduledJobSnapshot, error) {
	if s == nil || s.store == nil {
		return ScheduledJobSnapshot{}, apperror.New(apperror.CodeFailedPrecondition,
			"scheduled job store is required")
	}
	if !validControlIdentity(jobID) || roundLimit < 1 || roundLimit > 100 ||
		notificationLimit < 1 || notificationLimit > 100 {
		return ScheduledJobSnapshot{}, apperror.New(apperror.CodeInvalidArgument,
			"scheduled job snapshot query is invalid")
	}
	job, err := s.store.GetScheduledJob(ctx, jobID)
	if err != nil {
		return ScheduledJobSnapshot{}, apperror.Normalize(err)
	}
	rounds, err := s.store.ListScheduledJobRounds(ctx, job.ID, roundLimit)
	if err != nil {
		return ScheduledJobSnapshot{}, apperror.Normalize(err)
	}
	notifications, err := s.store.ListScheduledJobNotifications(ctx, job.ID,
		notificationLimit)
	if err != nil {
		return ScheduledJobSnapshot{}, apperror.Normalize(err)
	}
	var authorization *domain.ScheduledJobAuthorization
	if value, found, err := s.store.GetScheduledJobAuthorization(ctx, job.ID); err != nil {
		return ScheduledJobSnapshot{}, apperror.Normalize(err)
	} else if found {
		authorization = &value
	}
	return ScheduledJobSnapshot{Job: job, Authorization: authorization,
		Rounds: rounds, Notifications: notifications}, nil
}

func (s *ScheduledJobService) List(ctx context.Context, runID string,
	limit int,
) ([]domain.ScheduledJob, error) {
	if s == nil || s.store == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"scheduled job store is required")
	}
	values, err := s.store.ListScheduledJobs(ctx, runID, limit)
	return values, apperror.Normalize(err)
}

// RunDue implements scheduler.DueRunner. It claims at most one occurrence,
// reads only event envelope metadata, skips model execution when nothing
// relevant changed, and persists every terminal/retry decision under fencing.
func (s *ScheduledJobService) RunDue(ctx context.Context, ownerID string,
	now time.Time,
) (bool, error) {
	return s.runDue(ctx, ownerID, now, false)
}

// RunDueObservation is the ordinary desktop runner. Both reconciliation and
// claim are restricted in SQLite before a candidate can be mutated.
func (s *ScheduledJobService) RunDueObservation(ctx context.Context,
	ownerID string, now time.Time,
) (bool, error) {
	return s.runDue(ctx, ownerID, now, true)
}

func (s *ScheduledJobService) runDue(ctx context.Context, ownerID string,
	now time.Time, observationOnly bool,
) (bool, error) {
	if s == nil || s.store == nil || s.clock == nil || ctx == nil {
		return false, apperror.New(apperror.CodeFailedPrecondition,
			"scheduled job worker dependencies are required")
	}
	now = now.UTC()
	if !validControlIdentity(ownerID) || now.IsZero() {
		return false, apperror.New(apperror.CodeInvalidArgument,
			"scheduled job worker owner or time is invalid")
	}
	var job domain.ScheduledJob
	var lease domain.ScheduledJobLease
	var claimed bool
	var err error
	if observationOnly {
		observationStore, ok := s.store.(scheduledJobObservationStore)
		if !ok {
			return false, apperror.New(apperror.CodeFailedPrecondition,
				"scheduled job observation store is required")
		}
		if _, err = observationStore.ReconcileObservationScheduledJobs(ctx, now, 64); err == nil {
			job, lease, claimed, err = observationStore.ClaimDueObservationScheduledJob(
				ctx, ownerID, now)
		}
	} else {
		if _, err = s.store.ReconcileScheduledJobs(ctx, now, 64); err == nil {
			job, lease, claimed, err = s.store.ClaimDueScheduledJob(ctx, ownerID, now)
		}
	}
	if err != nil {
		return false, apperror.Normalize(err)
	}
	if !claimed {
		return job.ID != "", nil
	}
	observation, err := s.observe(ctx, job)
	if err != nil {
		return true, s.failClaim(ctx, lease, ScheduledJobExecutionResult{}, err)
	}
	execution := ScheduledJobExecutionResult{}
	if observation.changed && job.Spec.ExecutionMode == domain.ScheduledJobApprovedRepair &&
		s.executor == nil {
		return true, s.failClaim(ctx, lease, execution, apperror.New(
			apperror.CodeUnavailable, "scheduled repair executor is unavailable"))
	}
	if observation.changed && s.executor != nil && job.ModelCalls < job.Spec.MaxModelCalls {
		executionAt := s.clock.Now().UTC()
		if !executionAt.Before(lease.ExpiresAt) {
			return true, s.failClaim(ctx, lease, execution,
				apperror.New(apperror.CodeConflict,
					"scheduled job lease expired before executor handoff"))
		}
		var authorization *domain.ScheduledJobAuthorization
		if job.Spec.ExecutionMode == domain.ScheduledJobApprovedRepair {
			value, lookupErr := s.currentRepairAuthorization(ctx, job, executionAt)
			if lookupErr != nil {
				return true, s.failClaim(ctx, lease, execution, lookupErr)
			}
			authorization = value
		}
		execCtx, cancel := context.WithTimeout(ctx, scheduledJobExecutionTimeout)
		execution, err = s.executor.ExecuteScheduledJobRound(execCtx,
			ScheduledJobExecutionRequest{
				Job: job, Lease: lease, Authorization: authorization,
				ObservationSHA256:       observation.digest,
				EventSequence:           observation.sequence,
				RelevantEventCount:      observation.relevant,
				ObservationWasTruncated: observation.truncated,
			})
		cancel()
		if execution.ToolCalled && job.Spec.ExecutionMode != domain.ScheduledJobApprovedRepair {
			err = apperror.New(apperror.CodePolicyDenied,
				"read-only scheduled monitor attempted to use a tool")
		}
		if execution.ToolCalled && !execution.ModelCalled {
			err = apperror.New(apperror.CodeConflict,
				"scheduled monitor tool fact is missing its model handoff")
		}
		if err != nil {
			return true, s.failClaim(ctx, lease, execution, err)
		}
	}
	finalRun, err := s.store.GetRun(ctx, job.Spec.TargetRunID)
	if err != nil {
		return true, s.failClaim(ctx, lease, execution, err)
	}
	result := scheduledJobObservationSummary(finalRun.Status, observation.relevant,
		observation.truncated, execution)
	outcome := domain.ScheduledJobRoundOutcome{
		// Advance only to the exact envelope that produced this observation.
		// Events appended while an optional executor is running must remain
		// visible to the next round instead of being skipped by a later read.
		EventSequence: observation.sequence, ObservationSHA256: observation.digest,
		TargetStatus: finalRun.Status, TargetTerminal: finalRun.Terminal(),
		Changed: observation.changed, ModelCalled: execution.ModelCalled,
		ToolCalled: execution.ToolCalled, Result: result,
	}
	completedAt := s.clock.Now().UTC()
	if completedAt.Before(now) {
		completedAt = now
	}
	completeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_, _, _, err = s.store.CompleteScheduledJobRound(completeCtx, lease, outcome,
		completedAt)
	return true, apperror.Normalize(err)
}

// currentRepairAuthorization closes the observation-to-executor window. The
// store performs the same comparison atomically while claiming, and every
// downstream repair sink still has to enforce its ordinary Policy/approval
// checks against the exact authorization passed in the request.
func (s *ScheduledJobService) currentRepairAuthorization(ctx context.Context,
	job domain.ScheduledJob, at time.Time,
) (*domain.ScheduledJobAuthorization, error) {
	authorization, found, err := s.store.GetScheduledJobAuthorization(ctx, job.ID)
	if err != nil || !found {
		if err == nil {
			err = apperror.New(apperror.CodeFailedPrecondition,
				"scheduled repair authorization was not found")
		}
		return nil, err
	}
	mode, err := s.store.GetRunMode(ctx, job.OwnerRunID)
	if err != nil {
		return nil, err
	}
	permission, err := s.store.GetRunExecutionPermission(ctx, job.OwnerRunID)
	if err != nil {
		return nil, err
	}
	if authorization.Validate() != nil || authorization.JobID != job.ID ||
		authorization.RunID != job.OwnerRunID ||
		mode.Surface != domain.ExecutionSurfaceCode ||
		mode.Phase != domain.ExecutionPhaseDeliver ||
		authorization.ModeSnapshotID != mode.ID ||
		authorization.ModeRevision != mode.Revision ||
		permission.Mode != domain.RunExecutionPermissionFull ||
		!permission.OperatorConfirmed ||
		authorization.PermissionSnapshotID != permission.ID ||
		authorization.PermissionRevision != permission.Revision ||
		!at.Before(authorization.ExpiresAt) {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"scheduled repair authorization changed before executor handoff")
	}
	return &authorization, nil
}

type scheduledJobObservation struct {
	digest    string
	sequence  int64
	relevant  int
	changed   bool
	truncated bool
}

func (s *ScheduledJobService) observe(ctx context.Context,
	job domain.ScheduledJob,
) (scheduledJobObservation, error) {
	run, err := s.store.GetRun(ctx, job.Spec.TargetRunID)
	if err != nil {
		return scheduledJobObservation{}, err
	}
	latest, err := s.store.LatestRunEventSequence(ctx, run.ID)
	if err != nil {
		return scheduledJobObservation{}, err
	}
	values, err := s.store.ListRunEventsAfterSequence(ctx, run.ID,
		job.LastEventSequence, scheduledJobObservationEventLimit)
	if err != nil {
		return scheduledJobObservation{}, err
	}
	// Advance only through the final envelope that this bounded observation
	// actually inspected. Using the pre-read global latest value here would
	// permanently skip the unread tail whenever the backlog exceeds the page
	// limit.
	observedSequence := job.LastEventSequence
	if len(values) > 0 {
		observedSequence = values[len(values)-1].Sequence
	}
	truncated := observedSequence < latest
	relevant := make([]events.Event, 0, len(values))
	kinds := make(map[string]struct{})
	for _, value := range values {
		if strings.HasPrefix(value.Type, "scheduled_job.") {
			continue
		}
		relevant = append(relevant, value)
		kinds[value.Type] = struct{}{}
	}
	typeNames := make([]string, 0, len(kinds))
	for kind := range kinds {
		typeNames = append(typeNames, kind)
	}
	sort.Strings(typeNames)
	parts := []string{"scheduled_job_observation.v1", run.ID, string(run.Status),
		strconv.FormatInt(observedSequence, 10), strconv.FormatBool(truncated),
		strings.Join(typeNames, ",")}
	for _, value := range relevant {
		parts = append(parts, strconv.FormatInt(value.Sequence, 10), value.Type,
			value.Source, value.SubjectID)
	}
	digest := runmutation.Fingerprint(parts...)
	changed := job.LastObservationSHA256 == "" || len(relevant) > 0
	return scheduledJobObservation{digest: digest, sequence: observedSequence,
		relevant: len(relevant), changed: changed, truncated: truncated}, nil
}

func (s *ScheduledJobService) failClaim(ctx context.Context,
	lease domain.ScheduledJobLease, execution ScheduledJobExecutionResult,
	cause error,
) error {
	code := scheduledJobFailureCode(cause)
	failure := domain.ScheduledJobRoundFailure{
		ErrorCode: code, Result: "Scheduled monitor round failed (" + code + ")",
		ModelCalled: execution.ModelCalled, ToolCalled: execution.ToolCalled,
	}
	now := s.clock.Now().UTC()
	if now.Before(lease.AcquiredAt) {
		now = lease.AcquiredAt
	}
	completeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_, _, _, failErr := s.store.FailScheduledJobRound(completeCtx, lease, failure, now)
	if failErr != nil {
		return apperror.Normalize(errors.Join(cause, failErr))
	}
	return apperror.Normalize(cause)
}

func (s *ScheduledJobService) loadReplay(ctx context.Context, keyDigest string,
	fingerprint string, action domain.ScheduledJobAction, runID string,
	jobID string, expectedRevision int64, requestedBy string,
) (ScheduledJobControlResult, bool, error) {
	operation, found, err := s.store.GetScheduledJobOperation(ctx, keyDigest)
	if err != nil || !found {
		return ScheduledJobControlResult{}, found, apperror.Normalize(err)
	}
	storedIdentity, storedErr := operation.ReplayIdentity()
	requestedIdentity, requestedErr := (domain.ScheduledJobOperation{
		ProtocolVersion: domain.ScheduledJobControlProtocolVersion,
		KeyDigest:       keyDigest, RequestFingerprint: fingerprint,
	}).ReplayIdentity()
	decision, decisionErr := durableoperation.Decide(storedIdentity, requestedIdentity)
	if operation.ProtocolVersion != domain.ScheduledJobControlProtocolVersion ||
		storedErr != nil || requestedErr != nil || decisionErr != nil ||
		decision != durableoperation.DecisionReplay || operation.Action != action ||
		operation.RunID != runID || operation.RequestedBy != requestedBy ||
		operation.ExpectedRevision != expectedRevision ||
		(jobID != "" && operation.JobID != jobID) {
		return ScheduledJobControlResult{}, true, apperror.New(apperror.CodeConflict,
			"scheduled job operation key was already used for different intent")
	}
	job, err := s.store.GetScheduledJob(ctx, operation.JobID)
	if err != nil {
		return ScheduledJobControlResult{}, true, apperror.Normalize(err)
	}
	return ScheduledJobControlResult{Job: job, Replayed: true}, true, nil
}

func normalizeCreateScheduledJobRequest(request CreateScheduledJobRequest) (
	CreateScheduledJobRequest, domain.ScheduledJobSpec, error,
) {
	request.RunID = strings.TrimSpace(request.RunID)
	request.TargetRunID = strings.TrimSpace(request.TargetRunID)
	request.RequestedBy = strings.TrimSpace(request.RequestedBy)
	if request.Version != domain.ScheduledJobProtocolVersion ||
		!validControlIdentity(request.RunID) || request.TargetRunID != request.RunID ||
		!validControlIdentity(request.RequestedBy) ||
		(request.ObservationConsentVersion != 0 &&
			request.ObservationConsentVersion != domain.ScheduledJobObservationConsentVersion) {
		return CreateScheduledJobRequest{}, domain.ScheduledJobSpec{},
			apperror.New(apperror.CodeInvalidArgument,
				"scheduled job version, owner, explicit target, or requester is invalid")
	}
	key, err := domain.NormalizeAgentOperationKey(request.OperationKey)
	if err != nil || key != request.OperationKey || containsSpaceOrControl(key) {
		return CreateScheduledJobRequest{}, domain.ScheduledJobSpec{},
			apperror.New(apperror.CodeInvalidArgument,
				"scheduled job idempotency key is invalid")
	}
	request.Schedule.AnchorAt = request.Schedule.AnchorAt.UTC()
	request.DeadlineAt = request.DeadlineAt.UTC()
	spec := domain.ScheduledJobSpec{
		Version: request.Version, Schedule: request.Schedule,
		TargetRunID: request.TargetRunID, DeadlineAt: request.DeadlineAt,
		StopOnTargetTerminal: request.StopOnTargetTerminal,
		MaxRounds:            request.MaxRounds, MaxModelCalls: request.MaxModelCalls,
		MaxElapsedSeconds: request.MaxElapsedSeconds, Retry: request.Retry,
		Notification: request.Notification, ExecutionMode: request.ExecutionMode,
	}
	if err := spec.Validate(); err != nil {
		return CreateScheduledJobRequest{}, domain.ScheduledJobSpec{},
			apperror.Wrap(apperror.CodeInvalidArgument,
				"scheduled job request is invalid", err)
	}
	request.OperationKey = key
	return request, spec, nil
}

func normalizeEnableScheduledJobObservationRequest(
	request EnableScheduledJobObservationRequest,
) (EnableScheduledJobObservationRequest, error) {
	request.RunID = strings.TrimSpace(request.RunID)
	request.JobID = strings.TrimSpace(request.JobID)
	request.RequestedBy = strings.TrimSpace(request.RequestedBy)
	if !validControlIdentity(request.RunID) || !validControlIdentity(request.JobID) ||
		!validControlIdentity(request.RequestedBy) || request.ExpectedRevision < 1 ||
		request.ObservationConsentVersion != domain.ScheduledJobObservationConsentVersion {
		return EnableScheduledJobObservationRequest{}, apperror.New(
			apperror.CodeInvalidArgument,
			"scheduled job observation consent request is invalid")
	}
	key, err := domain.NormalizeAgentOperationKey(request.OperationKey)
	if err != nil || key != request.OperationKey || containsSpaceOrControl(key) {
		return EnableScheduledJobObservationRequest{}, apperror.New(
			apperror.CodeInvalidArgument,
			"scheduled job observation consent idempotency key is invalid")
	}
	request.OperationKey = key
	return request, nil
}

func normalizeTransitionScheduledJobRequest(request TransitionScheduledJobRequest) (
	TransitionScheduledJobRequest, error,
) {
	request.RunID = strings.TrimSpace(request.RunID)
	request.JobID = strings.TrimSpace(request.JobID)
	request.RequestedBy = strings.TrimSpace(request.RequestedBy)
	if request.Version != domain.ScheduledJobControlProtocolVersion ||
		!validControlIdentity(request.RunID) || !validControlIdentity(request.JobID) ||
		!validControlIdentity(request.RequestedBy) || !request.Action.Valid() ||
		request.Action == domain.ScheduledJobCreate || request.ExpectedRevision < 1 {
		return TransitionScheduledJobRequest{}, apperror.New(
			apperror.CodeInvalidArgument, "scheduled job transition request is invalid")
	}
	key, err := domain.NormalizeAgentOperationKey(request.OperationKey)
	if err != nil || key != request.OperationKey || containsSpaceOrControl(key) {
		return TransitionScheduledJobRequest{}, apperror.New(
			apperror.CodeInvalidArgument, "scheduled job idempotency key is invalid")
	}
	request.OperationKey = key
	return request, nil
}

func scheduledJobObservationSummary(status domain.RunStatus, relevant int,
	truncated bool, execution ScheduledJobExecutionResult,
) string {
	return fmt.Sprintf("Observed target Run status=%s; relevant_events=%d; truncated=%t; model_called=%t; tool_called=%t",
		status, relevant, truncated, execution.ModelCalled, execution.ToolCalled)
}

func scheduledJobFailureCode(err error) string {
	code := strings.ToLower(string(apperror.CodeOf(apperror.Normalize(err))))
	if code == "" {
		code = "internal"
	}
	var normalized strings.Builder
	for _, current := range code {
		if (current >= 'a' && current <= 'z') || (current >= '0' && current <= '9') ||
			current == '_' || current == '-' || current == '.' {
			normalized.WriteRune(current)
		}
	}
	if normalized.Len() == 0 || normalized.Len() > 64 {
		return "internal"
	}
	return normalized.String()
}

var _ scheduler.DueRunner = (*ScheduledJobService)(nil)
