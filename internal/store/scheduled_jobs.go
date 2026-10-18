package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/durableoperation"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/scheduler"
)

const scheduledJobSelect = `SELECT id, spec_json, owner_run_id,
	owner_root_agent_id, status, revision, next_wake_at, pending_occurrence_at,
	rounds_completed, model_calls, consecutive_unchanged, last_event_sequence,
	last_observation_sha256, last_result, last_error_code, stop_reason,
	active_lease_generation, active_lease_expires_at, created_by, created_at,
	updated_at, completed_at, active_lease_owner_sha256, active_fence_token_sha256
	, COALESCE((SELECT consent.version FROM scheduled_job_observation_consents consent
		WHERE consent.job_id = scheduled_jobs.id), 0)
	, COALESCE((SELECT consent.version FROM scheduled_job_execution_consents consent
		WHERE consent.job_id = scheduled_jobs.id), 0)
	FROM scheduled_jobs `

type scheduledJobRecord struct {
	Job              domain.ScheduledJob
	LeaseOwnerSHA256 string
	FenceTokenSHA256 string
}

func (s *SQLiteStore) GetScheduledJob(ctx context.Context,
	id string,
) (domain.ScheduledJob, error) {
	id = strings.TrimSpace(id)
	if !domain.ValidAgentID(id) || strings.ContainsRune(id, 0) {
		return domain.ScheduledJob{}, apperror.New(apperror.CodeInvalidArgument,
			"scheduled job id is invalid")
	}
	record, err := getScheduledJob(ctx, s.db, id)
	return record.Job, err
}

func (s *SQLiteStore) ListScheduledJobs(ctx context.Context, runID string,
	limit int,
) ([]domain.ScheduledJob, error) {
	runID = strings.TrimSpace(runID)
	if runID != "" && (!domain.ValidAgentID(runID) || strings.ContainsRune(runID, 0)) {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"scheduled job Run filter is invalid")
	}
	if limit < 1 || limit > 100 {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"scheduled job list limit must be between 1 and 100")
	}
	query := scheduledJobSelect
	args := []any{}
	if runID != "" {
		query += `WHERE owner_run_id = ? `
		args = append(args, runID)
	}
	query += `ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.ScheduledJob, 0, limit)
	for rows.Next() {
		record, err := scanScheduledJob(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, record.Job)
	}
	return result, rows.Err()
}

func (s *SQLiteStore) GetScheduledJobOperation(ctx context.Context,
	keyDigest string,
) (domain.ScheduledJobOperation, bool, error) {
	keyDigest = strings.TrimSpace(keyDigest)
	if !validStoreDigest(keyDigest) {
		return domain.ScheduledJobOperation{}, false, apperror.New(
			apperror.CodeInvalidArgument, "scheduled job operation digest is invalid")
	}
	return getScheduledJobOperation(ctx, s.db, keyDigest)
}

func (s *SQLiteStore) GetScheduledJobAuthorization(ctx context.Context,
	jobID string,
) (domain.ScheduledJobAuthorization, bool, error) {
	jobID = strings.TrimSpace(jobID)
	if !domain.ValidAgentID(jobID) || strings.ContainsRune(jobID, 0) {
		return domain.ScheduledJobAuthorization{}, false, apperror.New(
			apperror.CodeInvalidArgument, "scheduled job id is invalid")
	}
	return getScheduledJobAuthorization(ctx, s.db, jobID)
}

func (s *SQLiteStore) CreateScheduledJob(ctx context.Context, job domain.ScheduledJob,
	authorization *domain.ScheduledJobAuthorization, operation domain.ScheduledJobOperation,
) (domain.ScheduledJob, bool, error) {
	return s.createScheduledJob(ctx, job, authorization, nil, nil, operation)
}

func (s *SQLiteStore) CreateScheduledJobWithObservationConsent(ctx context.Context,
	job domain.ScheduledJob, authorization *domain.ScheduledJobAuthorization,
	consent domain.ScheduledJobObservationConsent, operation domain.ScheduledJobOperation,
) (domain.ScheduledJob, bool, error) {
	return s.createScheduledJob(ctx, job, authorization, &consent, nil, operation)
}

func (s *SQLiteStore) createScheduledJob(ctx context.Context, job domain.ScheduledJob,
	authorization *domain.ScheduledJobAuthorization,
	consent *domain.ScheduledJobObservationConsent,
	executionConsent *domain.ScheduledJobExecutionConsent,
	operation domain.ScheduledJobOperation,
) (domain.ScheduledJob, bool, error) {
	if err := validateScheduledJobCreate(job, authorization, consent, operation); err != nil {
		return domain.ScheduledJob{}, false, err
	}
	if (job.Spec.ExecutionMode == domain.ScheduledJobAutomatic) != (executionConsent != nil) {
		return domain.ScheduledJob{}, false, apperror.New(apperror.CodeInvalidArgument, "automatic execution requires separate explicit consent")
	}
	if executionConsent != nil && (executionConsent.Validate() != nil || executionConsent.JobID != job.ID || executionConsent.RunID != job.OwnerRunID || executionConsent.ConfirmedBy != job.CreatedBy || !executionConsent.ConfirmedAt.Equal(job.CreatedAt)) {
		return domain.ScheduledJob{}, false, apperror.New(apperror.CodeInvalidArgument, "automatic execution consent does not match creation")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.ScheduledJob{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if existing, found, err := getScheduledJobOperation(ctx, tx,
		operation.KeyDigest); err != nil {
		return domain.ScheduledJob{}, false, err
	} else if found {
		if err := requireScheduledJobOperationReplay(existing, operation); err != nil {
			return domain.ScheduledJob{}, false, err
		}
		stored, err := getScheduledJob(ctx, tx, existing.JobID)
		if err != nil {
			return domain.ScheduledJob{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.ScheduledJob{}, false, err
		}
		return stored.Job, true, nil
	}
	if _, found, err := getScheduledJobObservationConsentByOperation(ctx, tx,
		operation.KeyDigest); err != nil {
		return domain.ScheduledJob{}, false, err
	} else if found {
		return domain.ScheduledJob{}, false, apperror.New(apperror.CodeConflict,
			"scheduled job operation key was already used for observation consent")
	}
	run, mission, err := getCoordinatorRunTx(ctx, tx, job.OwnerRunID)
	if err != nil {
		return domain.ScheduledJob{}, false, err
	}
	if run.Terminal() && job.Spec.ExecutionMode != domain.ScheduledJobAutomatic {
		return domain.ScheduledJob{}, false, apperror.New(
			apperror.CodeFailedPrecondition, "scheduled jobs require a non-terminal Run")
	}
	root, found, err := getRootAgentTx(ctx, tx, run.ID)
	if err != nil || !found {
		if err == nil {
			err = apperror.New(apperror.CodeFailedPrecondition,
				"scheduled jobs require the Run root Agent")
		}
		return domain.ScheduledJob{}, false, err
	}
	if root.ID != job.OwnerRootAgentID || root.Role != domain.AgentRoleRoot ||
		root.RunID != run.ID || run.MissionID != mission.ID {
		return domain.ScheduledJob{}, false, apperror.New(
			apperror.CodeConflict, "scheduled job owner binding changed")
	}
	mode, err := getCurrentRunModeSnapshot(ctx, tx, run.ID)
	if err != nil {
		return domain.ScheduledJob{}, false, err
	}
	permission, err := getCurrentRunExecutionPermissionSnapshot(ctx, tx, run.ID)
	if err != nil {
		return domain.ScheduledJob{}, false, err
	}
	if err := validateScheduledJobAuthority(job, authorization, mode, permission,
		job.CreatedAt); err != nil {
		return domain.ScheduledJob{}, false, err
	}
	if err := insertScheduledJobTx(ctx, tx, job); err != nil {
		return domain.ScheduledJob{}, false, normalizeScheduledJobWriteError(err)
	}
	if authorization != nil {
		if err := insertScheduledJobAuthorizationTx(ctx, tx, *authorization); err != nil {
			return domain.ScheduledJob{}, false, err
		}
	}
	if consent != nil {
		if err := insertScheduledJobObservationConsentTx(ctx, tx, *consent); err != nil {
			return domain.ScheduledJob{}, false, normalizeScheduledJobWriteError(err)
		}
		job.ObservationConsentVersion = consent.Version
	}
	if executionConsent != nil {
		encoded, err := json.Marshal(executionConsent)
		if err != nil {
			return domain.ScheduledJob{}, false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO scheduled_job_execution_consents(job_id,version,consent_json) VALUES(?,?,?)`, job.ID, executionConsent.Version, string(encoded)); err != nil {
			return domain.ScheduledJob{}, false, err
		}
		job.ExecutionConsentVersion = executionConsent.Version
	}
	if err := insertScheduledJobOperationTx(ctx, tx, operation); err != nil {
		return domain.ScheduledJob{}, false, normalizeScheduledJobWriteError(err)
	}
	if err := appendScheduledJobEventTx(ctx, tx, run, events.ScheduledJobCreatedEvent,
		"scheduled_job_control", job.ID, map[string]any{
			"protocol": job.Spec.Version, "kind": job.Spec.Schedule.Kind,
			"timezone":                job.Spec.Schedule.Timezone,
			"misfire_policy":          job.Spec.Schedule.MisfirePolicy,
			"execution_mode":          job.Spec.ExecutionMode,
			"stop_on_target_terminal": job.Spec.StopOnTargetTerminal,
			"max_rounds":              job.Spec.MaxRounds, "max_model_calls": job.Spec.MaxModelCalls,
			"operator_confirmed_repair": authorization != nil,
			"execution_bypass":          false, "network_bypass": false,
			"approval_bypass": false,
		}, job.CreatedAt); err != nil {
		return domain.ScheduledJob{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ScheduledJob{}, false, err
	}
	return job, false, nil
}

// EnableScheduledJobObservation inserts one immutable receipt for an exact
// owner and revision. Replays return the current job projection and never
// change status, revision, scheduling, retry, or lease state.
func (s *SQLiteStore) EnableScheduledJobObservation(ctx context.Context,
	consent domain.ScheduledJobObservationConsent, expectedRevision int64,
) (domain.ScheduledJob, bool, error) {
	if err := consent.Validate(); err != nil {
		return domain.ScheduledJob{}, false, apperror.Wrap(apperror.CodeInvalidArgument,
			"scheduled job observation consent is invalid", err)
	}
	if expectedRevision < 1 {
		return domain.ScheduledJob{}, false, apperror.New(apperror.CodeInvalidArgument,
			"scheduled job observation consent revision is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.ScheduledJob{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if stored, found, err := getScheduledJobObservationConsentByOperation(ctx, tx,
		consent.OperationKeySHA256); err != nil {
		return domain.ScheduledJob{}, false, err
	} else if found {
		if stored.JobID != consent.JobID || stored.RunID != consent.RunID ||
			stored.Version != consent.Version || stored.ConfirmedBy != consent.ConfirmedBy ||
			stored.RequestFingerprint != consent.RequestFingerprint {
			return domain.ScheduledJob{}, false, apperror.New(apperror.CodeConflict,
				"scheduled job observation operation key was already used for different intent")
		}
		job, err := getScheduledJob(ctx, tx, stored.JobID)
		if err != nil {
			return domain.ScheduledJob{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.ScheduledJob{}, false, err
		}
		return job.Job, true, nil
	}
	if _, found, err := getScheduledJobOperation(ctx, tx, consent.OperationKeySHA256); err != nil {
		return domain.ScheduledJob{}, false, err
	} else if found {
		return domain.ScheduledJob{}, false, apperror.New(apperror.CodeConflict,
			"scheduled job operation key was already used for different intent")
	}
	record, err := getScheduledJob(ctx, tx, consent.JobID)
	if err != nil {
		return domain.ScheduledJob{}, false, err
	}
	job := record.Job
	if job.OwnerRunID != consent.RunID || job.Revision != expectedRevision {
		return domain.ScheduledJob{}, false, apperror.New(apperror.CodeConflict,
			"scheduled job changed before observation consent")
	}
	if job.Status.Terminal() || job.Spec.ExecutionMode != domain.ScheduledJobReadOnly ||
		job.Spec.MaxModelCalls != 0 {
		return domain.ScheduledJob{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"observation consent requires a non-terminal read-only zero-model job")
	}
	if job.ObservationConsentVersion != 0 {
		return domain.ScheduledJob{}, false, apperror.New(apperror.CodeConflict,
			"scheduled job observation consent already exists")
	}
	if err := insertScheduledJobObservationConsentTx(ctx, tx, consent); err != nil {
		return domain.ScheduledJob{}, false, normalizeScheduledJobWriteError(err)
	}
	job.ObservationConsentVersion = consent.Version
	if err := tx.Commit(); err != nil {
		return domain.ScheduledJob{}, false, err
	}
	return job, false, nil
}

func (s *SQLiteStore) TransitionScheduledJob(ctx context.Context, jobID string,
	action domain.ScheduledJobAction, expectedRevision int64, at time.Time,
	operation domain.ScheduledJobOperation,
) (domain.ScheduledJob, bool, error) {
	jobID = strings.TrimSpace(jobID)
	at = at.UTC()
	if !domain.ValidAgentID(jobID) || !action.Valid() || action == domain.ScheduledJobCreate ||
		expectedRevision < 1 || at.IsZero() || operation.JobID != jobID ||
		operation.Action != action || operation.ExpectedRevision != expectedRevision ||
		operation.CreatedAt != at {
		return domain.ScheduledJob{}, false, apperror.New(apperror.CodeInvalidArgument,
			"scheduled job transition is invalid")
	}
	if err := operation.Validate(); err != nil {
		return domain.ScheduledJob{}, false, apperror.Wrap(
			apperror.CodeInvalidArgument, "scheduled job operation is invalid", err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.ScheduledJob{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if existing, found, err := getScheduledJobOperation(ctx, tx,
		operation.KeyDigest); err != nil {
		return domain.ScheduledJob{}, false, err
	} else if found {
		if err := requireScheduledJobOperationReplay(existing, operation); err != nil {
			return domain.ScheduledJob{}, false, err
		}
		stored, err := getScheduledJob(ctx, tx, existing.JobID)
		if err != nil {
			return domain.ScheduledJob{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.ScheduledJob{}, false, err
		}
		return stored.Job, true, nil
	}
	if _, found, err := getScheduledJobObservationConsentByOperation(ctx, tx,
		operation.KeyDigest); err != nil {
		return domain.ScheduledJob{}, false, err
	} else if found {
		return domain.ScheduledJob{}, false, apperror.New(apperror.CodeConflict,
			"scheduled job operation key was already used for observation consent")
	}
	record, err := getScheduledJob(ctx, tx, jobID)
	if err != nil {
		return domain.ScheduledJob{}, false, err
	}
	job := record.Job
	if job.OwnerRunID != operation.RunID || job.Revision != expectedRevision ||
		at.Before(job.UpdatedAt) {
		return domain.ScheduledJob{}, false, apperror.New(apperror.CodeConflict,
			"scheduled job changed before the requested transition")
	}
	eventType := ""
	switch action {
	case domain.ScheduledJobPause:
		if job.Status != domain.ScheduledJobActive {
			return domain.ScheduledJob{}, false, apperror.New(
				apperror.CodeFailedPrecondition, "only active scheduled jobs can be paused")
		}
		if err := revokeScheduledJobLeaseTx(ctx, tx, record, at, "paused"); err != nil {
			return domain.ScheduledJob{}, false, err
		}
		job.Status = domain.ScheduledJobPaused
		job.NextWakeAt = nil
		job.ActiveLeaseGeneration = 0
		job.ActiveLeaseExpiresAt = nil
		eventType = events.ScheduledJobPausedEvent
	case domain.ScheduledJobResume:
		if job.Status != domain.ScheduledJobPaused {
			return domain.ScheduledJob{}, false, apperror.New(
				apperror.CodeFailedPrecondition, "only paused scheduled jobs can be resumed")
		}
		job.Status = domain.ScheduledJobActive
		next := at
		job.NextWakeAt = &next
		eventType = events.ScheduledJobResumedEvent
	case domain.ScheduledJobCancel:
		if job.Status.Terminal() {
			return domain.ScheduledJob{}, false, apperror.New(
				apperror.CodeFailedPrecondition, "terminal scheduled jobs cannot be cancelled")
		}
		if err := revokeScheduledJobLeaseTx(ctx, tx, record, at, "cancelled"); err != nil {
			return domain.ScheduledJob{}, false, err
		}
		job.Status = domain.ScheduledJobCancelled
		job.StopReason = domain.ScheduledJobStopCancelled
		job.NextWakeAt = nil
		job.PendingOccurrenceAt = nil
		job.ActiveLeaseGeneration = 0
		job.ActiveLeaseExpiresAt = nil
		job.CompletedAt = &at
		eventType = events.ScheduledJobCancelledEvent
	}
	job.Revision++
	job.UpdatedAt = at
	if err := updateScheduledJobTx(ctx, tx, job, expectedRevision, record, true); err != nil {
		return domain.ScheduledJob{}, false, err
	}
	if err := insertScheduledJobOperationTx(ctx, tx, operation); err != nil {
		return domain.ScheduledJob{}, false, normalizeScheduledJobWriteError(err)
	}
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id, status,
		config_json, budget_json, started_at, finished_at, created_at, updated_at
		FROM runs WHERE id = ?`, job.OwnerRunID))
	if err != nil {
		return domain.ScheduledJob{}, false, err
	}
	if err := appendScheduledJobEventTx(ctx, tx, run, eventType,
		"scheduled_job_control", job.ID, map[string]any{
			"action": action, "revision": job.Revision, "requested_by": operation.RequestedBy,
			"execution_granted": false, "network_granted": false,
		}, at); err != nil {
		return domain.ScheduledJob{}, false, err
	}
	if action == domain.ScheduledJobCancel {
		if _, err := maybeRecordScheduledJobNotificationTx(ctx, tx, run, job,
			false, false, false, at); err != nil {
			return domain.ScheduledJob{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.ScheduledJob{}, false, err
	}
	return job, false, nil
}

func (s *SQLiteStore) ListScheduledJobRounds(ctx context.Context, jobID string,
	limit int,
) ([]domain.ScheduledJobRound, error) {
	jobID = strings.TrimSpace(jobID)
	if !domain.ValidAgentID(jobID) || limit < 1 || limit > 100 {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"scheduled job round query is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT protocol_version, job_id,
		occurrence_at, ordinal, attempt, claim_generation, status, event_sequence,
		observation_sha256, changed, model_called, tool_called, result, error_code,
		started_at, completed_at FROM scheduled_job_rounds WHERE job_id = ?
		ORDER BY ordinal DESC LIMIT ?`, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.ScheduledJobRound, 0, limit)
	for rows.Next() {
		round, err := scanScheduledJobRound(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, round)
	}
	return result, rows.Err()
}

func (s *SQLiteStore) ListScheduledJobNotifications(ctx context.Context,
	jobID string, limit int,
) ([]domain.ScheduledJobNotification, error) {
	jobID = strings.TrimSpace(jobID)
	if !domain.ValidAgentID(jobID) || limit < 1 || limit > 100 {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"scheduled job notification query is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, job_id, kind, summary, created_at
		FROM scheduled_job_notifications WHERE job_id = ?
		ORDER BY created_at DESC, id DESC LIMIT ?`, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.ScheduledJobNotification, 0, limit)
	for rows.Next() {
		var value domain.ScheduledJobNotification
		var createdAt string
		if err := rows.Scan(&value.ID, &value.JobID, &value.Kind, &value.Summary,
			&createdAt); err != nil {
			return nil, err
		}
		value.CreatedAt = parseTS(createdAt)
		if err := value.Validate(); err != nil {
			return nil, fmt.Errorf("stored scheduled job notification is invalid: %w", err)
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

// ClaimDueScheduledJob atomically chooses at most one due job. The returned
// plaintext fence token exists only in process memory; SQLite stores its digest.
func (s *SQLiteStore) ClaimDueScheduledJob(ctx context.Context, ownerID string,
	now time.Time,
) (domain.ScheduledJob, domain.ScheduledJobLease, bool, error) {
	return s.claimDueScheduledJob(ctx, ownerID, now, 0)
}

func (s *SQLiteStore) ClaimDueObservationScheduledJob(ctx context.Context,
	ownerID string, now time.Time,
) (domain.ScheduledJob, domain.ScheduledJobLease, bool, error) {
	return s.claimDueScheduledJob(ctx, ownerID, now, 1)
}

func (s *SQLiteStore) claimDueScheduledJob(ctx context.Context, ownerID string,
	now time.Time, scope int,
) (domain.ScheduledJob, domain.ScheduledJobLease, bool, error) {
	ownerID = strings.TrimSpace(ownerID)
	now = now.UTC()
	if !domain.ValidAgentID(ownerID) || now.IsZero() {
		return domain.ScheduledJob{}, domain.ScheduledJobLease{}, false,
			apperror.New(apperror.CodeInvalidArgument,
				"scheduled job claim owner or time is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.ScheduledJob{}, domain.ScheduledJobLease{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	where := `WHERE status = 'active' AND julianday(next_wake_at) <= julianday(?)
		AND (active_lease_generation=0 OR julianday(active_lease_expires_at)<=julianday(?)) `
	where += scheduledJobSelection(scope)
	record, err := scanScheduledJob(tx.QueryRowContext(ctx, scheduledJobSelect+
		where+`ORDER BY next_wake_at, id LIMIT 1`, ts(now), ts(now)))
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return domain.ScheduledJob{}, domain.ScheduledJobLease{}, false, err
		}
		return domain.ScheduledJob{}, domain.ScheduledJobLease{}, false, nil
	}
	if err != nil {
		return domain.ScheduledJob{}, domain.ScheduledJobLease{}, false, err
	}
	job := record.Job
	if stop := scheduledJobPreclaimStop(job, now); stop != domain.ScheduledJobStopNone {
		job, err = stopScheduledJobTx(ctx, tx, record, domain.ScheduledJobExhausted,
			stop, now, "budget or deadline reached before claim")
		if err != nil {
			return domain.ScheduledJob{}, domain.ScheduledJobLease{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.ScheduledJob{}, domain.ScheduledJobLease{}, false, err
		}
		return job, domain.ScheduledJobLease{}, false, nil
	}
	target, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id, status,
		config_json, budget_json, started_at, finished_at, created_at, updated_at
		FROM runs WHERE id = ?`, job.Spec.TargetRunID))
	if err != nil {
		return domain.ScheduledJob{}, domain.ScheduledJobLease{}, false, err
	}
	if job.Spec.StopOnTargetTerminal && target.Terminal() {
		job, err = stopScheduledJobTx(ctx, tx, record, domain.ScheduledJobCompleted,
			domain.ScheduledJobStopTargetTerminal, now, "target Run is terminal")
		if err != nil {
			return domain.ScheduledJob{}, domain.ScheduledJobLease{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.ScheduledJob{}, domain.ScheduledJobLease{}, false, err
		}
		return job, domain.ScheduledJobLease{}, false, nil
	}
	if err := requireCurrentScheduledJobAuthorization(ctx, tx, job, now); err != nil {
		job, stopErr := stopScheduledJobTx(ctx, tx, record, domain.ScheduledJobFailed,
			domain.ScheduledJobStopAuthorization, now, "scheduled job authority is stale")
		if stopErr != nil {
			return domain.ScheduledJob{}, domain.ScheduledJobLease{}, false, stopErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return domain.ScheduledJob{}, domain.ScheduledJobLease{}, false, commitErr
		}
		return job, domain.ScheduledJobLease{}, false, nil
	}
	if job.PendingOccurrenceAt == nil &&
		job.Spec.Schedule.MisfirePolicy == domain.ScheduledJobMisfireSkip &&
		scheduler.MissedPeriodicOccurrence(job.Spec.Schedule, *job.NextWakeAt, now) {
		job, err = skipScheduledJobOccurrenceTx(ctx, tx, record, now)
		if err != nil {
			return domain.ScheduledJob{}, domain.ScheduledJobLease{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.ScheduledJob{}, domain.ScheduledJobLease{}, false, err
		}
		return job, domain.ScheduledJobLease{}, false, nil
	}
	lease, job, err := claimScheduledJobTx(ctx, tx, record, ownerID, now)
	if err != nil {
		return domain.ScheduledJob{}, domain.ScheduledJobLease{}, false, err
	}
	if lease.JobID == "" {
		if err := tx.Commit(); err != nil {
			return domain.ScheduledJob{}, domain.ScheduledJobLease{}, false, err
		}
		return job, domain.ScheduledJobLease{}, false, nil
	}
	if err := tx.Commit(); err != nil {
		return domain.ScheduledJob{}, domain.ScheduledJobLease{}, false, err
	}
	return job, lease, true, nil
}

func (s *SQLiteStore) CompleteScheduledJobRound(ctx context.Context,
	lease domain.ScheduledJobLease, outcome domain.ScheduledJobRoundOutcome,
	now time.Time,
) (domain.ScheduledJob, domain.ScheduledJobRound, *domain.ScheduledJobNotification, error) {
	if err := lease.Validate(); err != nil {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil,
			apperror.Wrap(apperror.CodeInvalidArgument, "scheduled job lease is invalid", err)
	}
	if err := outcome.Validate(); err != nil {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil,
			apperror.Wrap(apperror.CodeInvalidArgument, "scheduled job outcome is invalid", err)
	}
	now = now.UTC()
	if now.IsZero() {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil,
			apperror.New(apperror.CodeInvalidArgument, "scheduled job completion time is required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	record, _, err := requireScheduledJobLeaseTx(ctx, tx, lease, now)
	if err != nil {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil, err
	}
	job := record.Job
	if outcome.ToolCalled && job.Spec.ExecutionMode != domain.ScheduledJobApprovedRepair {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil,
			apperror.New(apperror.CodePolicyDenied,
				"read-only scheduled jobs cannot report tool execution")
	}
	if outcome.ModelCalled && (job.Spec.MaxModelCalls == 0 ||
		job.ModelCalls >= job.Spec.MaxModelCalls) {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil,
			apperror.New(apperror.CodeFailedPrecondition,
				"scheduled job model-call budget is exhausted")
	}
	safeResult := boundedScheduledJobText(outcome.Result)
	status := domain.ScheduledJobRoundCompleted
	if !outcome.Changed {
		status = domain.ScheduledJobRoundUnchanged
	}
	result, err := tx.ExecContext(ctx, `UPDATE scheduled_job_rounds SET
		status = ?, event_sequence = ?, observation_sha256 = ?, changed = ?,
		model_called = ?, tool_called = ?, result = ?, error_code = '', completed_at = ?
		WHERE job_id = ? AND occurrence_at = ? AND status = 'claimed'
		AND claim_generation = ? AND fence_token_sha256 = ?`, status,
		outcome.EventSequence, outcome.ObservationSHA256, boolInt(outcome.Changed),
		boolInt(outcome.ModelCalled), boolInt(outcome.ToolCalled), safeResult, ts(now),
		lease.JobID, ts(lease.OccurrenceAt), lease.Generation,
		lease.FenceTokenSHA256)
	if err != nil {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil, err
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil,
			apperror.New(apperror.CodeConflict, "scheduled job round changed before completion")
	}
	job.RoundsCompleted++
	if outcome.ModelCalled {
		job.ModelCalls++
	}
	if outcome.Changed {
		job.ConsecutiveUnchanged = 0
	} else {
		job.ConsecutiveUnchanged++
	}
	job.LastEventSequence = outcome.EventSequence
	job.LastObservationSHA256 = outcome.ObservationSHA256
	job.LastResult = safeResult
	job.LastErrorCode = ""
	job.PendingOccurrenceAt = nil
	job.ActiveLeaseGeneration = 0
	job.ActiveLeaseExpiresAt = nil
	job.UpdatedAt = now
	job.Revision++
	terminalReason := domain.ScheduledJobStopNone
	terminalStatus := domain.ScheduledJobCompleted
	switch {
	case job.Spec.StopOnTargetTerminal && outcome.TargetTerminal:
		terminalReason = domain.ScheduledJobStopTargetTerminal
	case job.Spec.Schedule.Kind == domain.ScheduledJobOnce:
		terminalReason = domain.ScheduledJobStopOnceCompleted
	case job.RoundsCompleted >= job.Spec.MaxRounds:
		terminalReason = domain.ScheduledJobStopRoundBudget
		terminalStatus = domain.ScheduledJobExhausted
	case job.Spec.MaxModelCalls > 0 && job.ModelCalls >= job.Spec.MaxModelCalls:
		terminalReason = domain.ScheduledJobStopModelBudget
		terminalStatus = domain.ScheduledJobExhausted
	}
	if terminalReason == domain.ScheduledJobStopNone {
		next, nextErr := scheduler.NextOccurrence(job.Spec.Schedule, now)
		if nextErr != nil {
			return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil, nextErr
		}
		if !outcome.Changed {
			idle := scheduler.IdleBackoff(
				time.Duration(job.Spec.Schedule.IntervalSeconds)*time.Second,
				job.ConsecutiveUnchanged)
			candidate := now.Add(idle)
			if candidate.After(next) {
				next = candidate
			}
		}
		if next.IsZero() || !next.Before(job.Spec.DeadlineAt) {
			terminalReason = domain.ScheduledJobStopDeadline
			terminalStatus = domain.ScheduledJobExhausted
		} else {
			job.NextWakeAt = &next
		}
	}
	if terminalReason != domain.ScheduledJobStopNone {
		job.Status = terminalStatus
		job.StopReason = terminalReason
		job.NextWakeAt = nil
		job.CompletedAt = &now
	}
	if err := updateScheduledJobTx(ctx, tx, job, record.Job.Revision, record,
		false); err != nil {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil, err
	}
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id, status,
		config_json, budget_json, started_at, finished_at, created_at, updated_at
		FROM runs WHERE id = ?`, job.OwnerRunID))
	if err != nil {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil, err
	}
	if err := appendScheduledJobEventTx(ctx, tx, run,
		events.ScheduledJobRoundCompletedEvent, "scheduled_job_worker", job.ID,
		map[string]any{
			"occurrence_at": lease.OccurrenceAt, "round": lease.RoundOrdinal,
			"attempt": lease.Attempt, "claim_generation": lease.Generation,
			"event_sequence": outcome.EventSequence, "changed": outcome.Changed,
			"model_called": outcome.ModelCalled, "tool_called": outcome.ToolCalled,
			"execution_mode": job.Spec.ExecutionMode,
			"stop_reason":    terminalReason,
		}, now); err != nil {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil, err
	}
	if terminalReason != domain.ScheduledJobStopNone {
		if err := appendScheduledJobEventTx(ctx, tx, run,
			events.ScheduledJobStoppedEvent, "scheduled_job_worker", job.ID,
			map[string]any{"status": job.Status, "stop_reason": terminalReason,
				"rounds_completed": job.RoundsCompleted, "model_calls": job.ModelCalls},
			now); err != nil {
			return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil, err
		}
	}
	notification, err := maybeRecordScheduledJobNotificationTx(ctx, tx, run, job,
		outcome.Changed, false, record.Job.LastErrorCode != "", now)
	if err != nil {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil, err
	}
	storedRound, err := getScheduledJobRoundTx(ctx, tx, job.ID, lease.OccurrenceAt)
	if err != nil {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil, err
	}
	return job, storedRound, notification, nil
}

func (s *SQLiteStore) FailScheduledJobRound(ctx context.Context,
	lease domain.ScheduledJobLease, failure domain.ScheduledJobRoundFailure, now time.Time,
) (domain.ScheduledJob, domain.ScheduledJobRound, *domain.ScheduledJobNotification, error) {
	if err := lease.Validate(); err != nil {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil,
			apperror.Wrap(apperror.CodeInvalidArgument, "scheduled job lease is invalid", err)
	}
	failure.ErrorCode = strings.ToLower(strings.TrimSpace(failure.ErrorCode))
	failure.Result = boundedScheduledJobText(failure.Result)
	now = now.UTC()
	if err := failure.Validate(); err != nil || now.IsZero() {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil,
			apperror.New(apperror.CodeInvalidArgument, "scheduled job failure is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	record, round, err := requireScheduledJobLeaseTx(ctx, tx, lease, now)
	if err != nil {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil, err
	}
	job := record.Job
	retryAt := now.Add(scheduler.RetryBackoff(job.Spec.Retry, round.Attempt))
	if failure.ToolCalled && job.Spec.ExecutionMode != domain.ScheduledJobApprovedRepair {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil,
			apperror.New(apperror.CodePolicyDenied,
				"read-only scheduled jobs cannot report tool execution")
	}
	if failure.ModelCalled && (job.Spec.MaxModelCalls == 0 ||
		job.ModelCalls >= job.Spec.MaxModelCalls) {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil,
			apperror.New(apperror.CodeFailedPrecondition,
				"scheduled job model-call budget is exhausted")
	}
	if failure.ModelCalled {
		job.ModelCalls++
	}
	elapsedDeadline := job.CreatedAt.Add(
		time.Duration(job.Spec.MaxElapsedSeconds) * time.Second)
	retry := round.Attempt < job.Spec.Retry.MaxAttempts && retryAt.Before(job.Spec.DeadlineAt) &&
		retryAt.Before(elapsedDeadline)
	if job.Spec.ExecutionMode == domain.ScheduledJobAutomatic {
		if receipt, found, err := getScheduledExecution(ctx, tx, job.ID, lease.OccurrenceAt); err != nil {
			return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil, err
		} else if found && receipt.State == "failed" {
			retry = false
		}
	}
	if failure.ModelCalled && job.ModelCalls >= job.Spec.MaxModelCalls {
		retry = false
	}
	terminalStatus := domain.ScheduledJobFailed
	terminalReason := domain.ScheduledJobStopRetryExhausted
	if !retry {
		switch {
		case !retryAt.Before(job.Spec.DeadlineAt):
			terminalStatus = domain.ScheduledJobExhausted
			terminalReason = domain.ScheduledJobStopDeadline
		case !retryAt.Before(elapsedDeadline):
			terminalStatus = domain.ScheduledJobExhausted
			terminalReason = domain.ScheduledJobStopElapsedBudget
		case failure.ModelCalled && job.ModelCalls >= job.Spec.MaxModelCalls:
			terminalStatus = domain.ScheduledJobExhausted
			terminalReason = domain.ScheduledJobStopModelBudget
		}
	}
	roundStatus := domain.ScheduledJobRoundFailed
	completedAt := any(ts(now))
	if retry {
		roundStatus = domain.ScheduledJobRoundRetryWait
		completedAt = nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE scheduled_job_rounds SET status = ?,
		model_called = ?, tool_called = ?, result = ?, error_code = ?, completed_at = ?
		WHERE job_id = ? AND occurrence_at = ?
		AND status = 'claimed' AND claim_generation = ? AND fence_token_sha256 = ?`,
		roundStatus, boolInt(failure.ModelCalled), boolInt(failure.ToolCalled),
		failure.Result, failure.ErrorCode, completedAt, lease.JobID, ts(lease.OccurrenceAt),
		lease.Generation, lease.FenceTokenSHA256)
	if err != nil {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil, err
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil,
			apperror.New(apperror.CodeConflict, "scheduled job round changed before failure")
	}
	job.LastResult = failure.Result
	job.LastErrorCode = failure.ErrorCode
	job.ActiveLeaseGeneration = 0
	job.ActiveLeaseExpiresAt = nil
	job.UpdatedAt = now
	job.Revision++
	if retry {
		job.NextWakeAt = &retryAt
	} else {
		job.RoundsCompleted++
		job.Status = terminalStatus
		job.StopReason = terminalReason
		job.NextWakeAt = nil
		job.PendingOccurrenceAt = nil
		job.CompletedAt = &now
	}
	if err := updateScheduledJobTx(ctx, tx, job, record.Job.Revision, record,
		false); err != nil {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil, err
	}
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id, status,
		config_json, budget_json, started_at, finished_at, created_at, updated_at
		FROM runs WHERE id = ?`, job.OwnerRunID))
	if err != nil {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil, err
	}
	eventType := events.ScheduledJobStoppedEvent
	if retry {
		eventType = events.ScheduledJobRoundRetriedEvent
	}
	if err := appendScheduledJobEventTx(ctx, tx, run, eventType,
		"scheduled_job_worker", job.ID, map[string]any{
			"occurrence_at": lease.OccurrenceAt, "round": lease.RoundOrdinal,
			"attempt": lease.Attempt, "claim_generation": lease.Generation,
			"retry": retry, "retry_at": nullableScheduledTime(job.NextWakeAt),
			"error_code": failure.ErrorCode, "model_called": failure.ModelCalled,
			"tool_called": failure.ToolCalled,
		}, now); err != nil {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil, err
	}
	notification, err := maybeRecordScheduledJobNotificationTx(ctx, tx, run, job,
		false, true, false, now)
	if err != nil {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil, err
	}
	storedRound, err := getScheduledJobRoundTx(ctx, tx, job.ID, lease.OccurrenceAt)
	if err != nil {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ScheduledJob{}, domain.ScheduledJobRound{}, nil, err
	}
	return job, storedRound, notification, nil
}

// ReconcileScheduledJobs makes expired claims reclaimable after crash or sleep.
// It never executes a round, calls a model, starts a process, or restores repair
// authority; approved repair is revalidated again at the next claim.
func (s *SQLiteStore) ReconcileScheduledJobs(ctx context.Context, now time.Time,
	limit int,
) (int, error) {
	return s.reconcileScheduledJobs(ctx, now, limit, 0)
}

func (s *SQLiteStore) ReconcileObservationScheduledJobs(ctx context.Context,
	now time.Time, limit int,
) (int, error) {
	return s.reconcileScheduledJobs(ctx, now, limit, 1)
}

func (s *SQLiteStore) reconcileScheduledJobs(ctx context.Context, now time.Time,
	limit int, scope int,
) (int, error) {
	now = now.UTC()
	if now.IsZero() || limit < 1 || limit > 1024 {
		return 0, apperror.New(apperror.CodeInvalidArgument,
			"scheduled job reconciliation bounds are invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	query := `SELECT id FROM scheduled_jobs
		WHERE status = 'active' AND active_lease_generation > 0
		AND julianday(active_lease_expires_at) <= julianday(?) `
	query += scheduledJobSelection(scope)
	query += `ORDER BY active_lease_expires_at, id LIMIT ?`
	rows, err := tx.QueryContext(ctx, query, ts(now), limit)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		record, err := getScheduledJob(ctx, tx, id)
		if err != nil {
			return 0, err
		}
		if record.Job.PendingOccurrenceAt == nil {
			return 0, apperror.New(apperror.CodeConflict,
				"expired scheduled job lease lost its occurrence binding")
		}
		roundUpdate, err := tx.ExecContext(ctx, `UPDATE scheduled_job_rounds
			SET status = 'retry_wait' WHERE job_id = ? AND occurrence_at = ?
			AND status = 'claimed' AND claim_generation = ?`, id,
			ts(*record.Job.PendingOccurrenceAt), record.Job.ActiveLeaseGeneration)
		if err != nil {
			return 0, err
		}
		if affected, err := roundUpdate.RowsAffected(); err != nil || affected != 1 {
			return 0, apperror.New(apperror.CodeConflict,
				"scheduled job expired round changed during reconciliation")
		}
		jobUpdate, err := tx.ExecContext(ctx, `UPDATE scheduled_jobs SET revision = revision + 1,
			next_wake_at = ?, active_lease_generation = 0,
			active_lease_owner_sha256 = '', active_fence_token_sha256 = '',
			active_lease_expires_at = NULL, updated_at = ? WHERE id = ? AND revision = ?`,
			ts(now), ts(now), id, record.Job.Revision)
		if err != nil {
			return 0, err
		}
		if affected, err := jobUpdate.RowsAffected(); err != nil || affected != 1 {
			return 0, apperror.New(apperror.CodeConflict,
				"scheduled job changed during reconciliation")
		}
		run, err := getRunControlRunTx(ctx, tx, record.Job.OwnerRunID)
		if err != nil {
			return 0, err
		}
		if err := appendScheduledJobEventTx(ctx, tx, run,
			events.ScheduledJobRoundRetriedEvent, "scheduled_job_reconciler", id,
			map[string]any{"occurrence_at": *record.Job.PendingOccurrenceAt,
				"claim_generation": record.Job.ActiveLeaseGeneration,
				"reason":           "lease_expired", "execution_granted": false}, now); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(ids), nil
}

func claimScheduledJobTx(ctx context.Context, tx *sql.Tx,
	record scheduledJobRecord, ownerID string, now time.Time,
) (domain.ScheduledJobLease, domain.ScheduledJob, error) {
	job := record.Job
	occurrence := *job.NextWakeAt
	ordinal := job.RoundsCompleted + 1
	attempt := 1
	if job.PendingOccurrenceAt != nil {
		occurrence = *job.PendingOccurrenceAt
		var status, errorCode string
		var existingAttempt int
		err := tx.QueryRowContext(ctx, `SELECT status, attempt, error_code FROM scheduled_job_rounds
			WHERE job_id = ? AND occurrence_at = ?`, job.ID, ts(occurrence)).
			Scan(&status, &existingAttempt, &errorCode)
		if err != nil {
			return domain.ScheduledJobLease{}, domain.ScheduledJob{}, err
		}
		if status == string(domain.ScheduledJobRoundClaimed) &&
			job.ActiveLeaseExpiresAt != nil && now.Before(*job.ActiveLeaseExpiresAt) {
			return domain.ScheduledJobLease{}, job, nil
		}
		if status != string(domain.ScheduledJobRoundClaimed) &&
			status != string(domain.ScheduledJobRoundRetryWait) {
			return domain.ScheduledJobLease{}, domain.ScheduledJob{}, apperror.New(
				apperror.CodeConflict, "scheduled job pending occurrence is already terminal")
		}
		attempt = existingAttempt + 1
		// Once an execution intent exists, recovery observes or replays that
		// exact input. Approval polling must not consume retry attempts.
		if job.Spec.ExecutionMode == domain.ScheduledJobAutomatic {
			receipt, found, err := getScheduledExecution(ctx, tx, job.ID, occurrence)
			if err != nil {
				return domain.ScheduledJobLease{}, domain.ScheduledJob{}, err
			}
			if found && (status == string(domain.ScheduledJobRoundClaimed) || errorCode == "lease_revoked" || receipt.MessageID != "" || receipt.WorkflowExecutionID != "") {
				attempt = existingAttempt
			}
		}
		if attempt > job.Spec.Retry.MaxAttempts {
			stopped, err := stopScheduledJobTx(ctx, tx, record,
				domain.ScheduledJobFailed, domain.ScheduledJobStopRetryExhausted,
				now, "retry attempts exhausted during recovery")
			return domain.ScheduledJobLease{}, stopped, err
		}
	}
	// Lease generations must remain monotonic even after the public active
	// projection is cleared; the round stores the previous high-water.
	var highWater int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(claim_generation), 0)
		FROM scheduled_job_rounds WHERE job_id = ?`, job.ID).Scan(&highWater); err != nil {
		return domain.ScheduledJobLease{}, domain.ScheduledJob{}, err
	}
	generation := highWater + 1
	fenceToken := idgen.New("scheduled-fence")
	fenceDigest := scheduledJobDigest(fenceToken)
	ownerDigest := scheduledJobDigest(ownerID)
	expiresAt := now.Add(domain.ScheduledJobLeaseSeconds * time.Second)
	operationKey := "scheduled-round-" + scheduledJobDigest(job.ID+"\x00"+
		occurrence.Format(time.RFC3339Nano))
	lease := domain.ScheduledJobLease{
		JobID: job.ID, OccurrenceAt: occurrence, RoundOrdinal: ordinal,
		Attempt: attempt, Generation: generation, OwnerID: ownerID,
		FenceToken: fenceToken, FenceTokenSHA256: fenceDigest,
		AcquiredAt: now, ExpiresAt: expiresAt, OperationKey: operationKey,
		ExecutionMode: job.Spec.ExecutionMode,
	}
	if err := lease.Validate(); err != nil {
		return domain.ScheduledJobLease{}, domain.ScheduledJob{}, err
	}
	if job.PendingOccurrenceAt == nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO scheduled_job_rounds
			(job_id, occurrence_at, protocol_version, ordinal, attempt, claim_generation,
			fence_token_sha256, owner_id_sha256, status, event_sequence,
			observation_sha256, changed, model_called, tool_called, result, error_code,
			started_at, completed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'claimed', 0,
			'', 0, 0, 0, '', '', ?, NULL)`, job.ID, ts(occurrence),
			domain.ScheduledJobRoundProtocolVersion, ordinal, attempt, generation,
			fenceDigest, ownerDigest, ts(now)); err != nil {
			return domain.ScheduledJobLease{}, domain.ScheduledJob{},
				normalizeScheduledJobWriteError(err)
		}
		job.PendingOccurrenceAt = &occurrence
	} else {
		result, err := tx.ExecContext(ctx, `UPDATE scheduled_job_rounds SET
			attempt = ?, claim_generation = ?, fence_token_sha256 = ?,
			owner_id_sha256 = ?, status = 'claimed', result = '', error_code = '',
			started_at = ?, completed_at = NULL WHERE job_id = ? AND occurrence_at = ?
			AND status IN ('claimed', 'retry_wait')`, attempt, generation, fenceDigest,
			ownerDigest, ts(now), job.ID, ts(occurrence))
		if err != nil {
			return domain.ScheduledJobLease{}, domain.ScheduledJob{}, err
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			return domain.ScheduledJobLease{}, domain.ScheduledJob{}, apperror.New(
				apperror.CodeConflict, "scheduled job occurrence changed before reclaim")
		}
	}
	previousRevision := job.Revision
	job.Revision++
	job.ActiveLeaseGeneration = generation
	job.ActiveLeaseExpiresAt = &expiresAt
	job.UpdatedAt = now
	result, err := tx.ExecContext(ctx, `UPDATE scheduled_jobs SET revision = ?,
		pending_occurrence_at = ?, active_lease_generation = ?,
		active_lease_owner_sha256 = ?, active_fence_token_sha256 = ?,
		active_lease_expires_at = ?, updated_at = ? WHERE id = ? AND status = 'active'
		AND revision = ?`, job.Revision, ts(occurrence), generation, ownerDigest,
		fenceDigest, ts(expiresAt), ts(now), job.ID, previousRevision)
	if err != nil {
		return domain.ScheduledJobLease{}, domain.ScheduledJob{}, err
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return domain.ScheduledJobLease{}, domain.ScheduledJob{}, apperror.New(
			apperror.CodeConflict, "scheduled job changed before claim")
	}
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id, status,
		config_json, budget_json, started_at, finished_at, created_at, updated_at
		FROM runs WHERE id = ?`, job.OwnerRunID))
	if err != nil {
		return domain.ScheduledJobLease{}, domain.ScheduledJob{}, err
	}
	if err := appendScheduledJobEventTx(ctx, tx, run, events.ScheduledJobClaimedEvent,
		"scheduled_job_worker", job.ID, map[string]any{
			"occurrence_at": occurrence, "round": ordinal, "attempt": attempt,
			"claim_generation": generation, "lease_seconds": domain.ScheduledJobLeaseSeconds,
			"operation_key_sha256": scheduledJobDigest(operationKey),
			"execution_granted":    false, "network_granted": false,
		}, now); err != nil {
		return domain.ScheduledJobLease{}, domain.ScheduledJob{}, err
	}
	return lease, job, nil
}

func requireScheduledJobLeaseTx(ctx context.Context, tx *sql.Tx,
	lease domain.ScheduledJobLease, now time.Time,
) (scheduledJobRecord, domain.ScheduledJobRound, error) {
	record, err := getScheduledJob(ctx, tx, lease.JobID)
	if err != nil {
		return scheduledJobRecord{}, domain.ScheduledJobRound{}, err
	}
	job := record.Job
	if job.Status != domain.ScheduledJobActive || job.PendingOccurrenceAt == nil ||
		!job.PendingOccurrenceAt.Equal(lease.OccurrenceAt) ||
		job.ActiveLeaseGeneration != lease.Generation ||
		job.ActiveLeaseExpiresAt == nil || !now.Before(*job.ActiveLeaseExpiresAt) ||
		record.LeaseOwnerSHA256 != scheduledJobDigest(lease.OwnerID) ||
		record.FenceTokenSHA256 != lease.FenceTokenSHA256 ||
		lease.FenceTokenSHA256 != scheduledJobDigest(lease.FenceToken) {
		return scheduledJobRecord{}, domain.ScheduledJobRound{}, apperror.New(
			apperror.CodeConflict, "scheduled job lease is stale or no longer authoritative")
	}
	round, err := getScheduledJobRoundTx(ctx, tx, job.ID, lease.OccurrenceAt)
	if err != nil {
		return scheduledJobRecord{}, domain.ScheduledJobRound{}, err
	}
	if round.Status != domain.ScheduledJobRoundClaimed ||
		round.ClaimGeneration != lease.Generation || round.Attempt != lease.Attempt ||
		round.Ordinal != lease.RoundOrdinal {
		return scheduledJobRecord{}, domain.ScheduledJobRound{}, apperror.New(
			apperror.CodeConflict, "scheduled job round claim is stale")
	}
	return record, round, nil
}

func skipScheduledJobOccurrenceTx(ctx context.Context, tx *sql.Tx,
	record scheduledJobRecord, now time.Time,
) (domain.ScheduledJob, error) {
	job := record.Job
	occurrence := *job.NextWakeAt
	ordinal := job.RoundsCompleted + 1
	digest := scheduledJobDigest(job.ID + "\x00skip\x00" + occurrence.Format(time.RFC3339Nano))
	if _, err := tx.ExecContext(ctx, `INSERT INTO scheduled_job_rounds
		(job_id, occurrence_at, protocol_version, ordinal, attempt, claim_generation,
		fence_token_sha256, owner_id_sha256, status, event_sequence,
		observation_sha256, changed, model_called, tool_called, result, error_code,
		started_at, completed_at) VALUES (?, ?, ?, ?, 1, 1, ?, ?, 'skipped', 0,
		'', 0, 0, 0, 'misfire skipped after sleep or restart', '', ?, ?)`,
		job.ID, ts(occurrence), domain.ScheduledJobRoundProtocolVersion, ordinal,
		digest, digest, ts(now), ts(now)); err != nil {
		return domain.ScheduledJob{}, normalizeScheduledJobWriteError(err)
	}
	job.RoundsCompleted++
	job.LastResult = "misfire skipped after sleep or restart"
	job.UpdatedAt = now
	job.Revision++
	next, err := scheduler.NextOccurrence(job.Spec.Schedule, now)
	if err != nil {
		return domain.ScheduledJob{}, err
	}
	if job.RoundsCompleted >= job.Spec.MaxRounds {
		job.Status = domain.ScheduledJobExhausted
		job.StopReason = domain.ScheduledJobStopRoundBudget
		job.NextWakeAt = nil
		job.CompletedAt = &now
	} else if next.IsZero() || !next.Before(job.Spec.DeadlineAt) {
		job.Status = domain.ScheduledJobExhausted
		job.StopReason = domain.ScheduledJobStopDeadline
		job.NextWakeAt = nil
		job.CompletedAt = &now
	} else {
		job.NextWakeAt = &next
	}
	if err := updateScheduledJobTx(ctx, tx, job, record.Job.Revision, record,
		false); err != nil {
		return domain.ScheduledJob{}, err
	}
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id, status,
		config_json, budget_json, started_at, finished_at, created_at, updated_at
		FROM runs WHERE id = ?`, job.OwnerRunID))
	if err != nil {
		return domain.ScheduledJob{}, err
	}
	if err := appendScheduledJobEventTx(ctx, tx, run,
		events.ScheduledJobRoundSkippedEvent, "scheduled_job_worker", job.ID,
		map[string]any{"occurrence_at": occurrence, "round": ordinal,
			"misfire_policy":   job.Spec.Schedule.MisfirePolicy,
			"collapsed_replay": true, "model_called": false, "tool_called": false},
		now); err != nil {
		return domain.ScheduledJob{}, err
	}
	if _, err := maybeRecordScheduledJobNotificationTx(ctx, tx, run, job,
		false, false, false, now); err != nil {
		return domain.ScheduledJob{}, err
	}
	return job, nil
}

func stopScheduledJobTx(ctx context.Context, tx *sql.Tx, record scheduledJobRecord,
	status domain.ScheduledJobStatus, reason domain.ScheduledJobStopReason,
	now time.Time, summary string,
) (domain.ScheduledJob, error) {
	job := record.Job
	if !status.Terminal() || reason == domain.ScheduledJobStopNone {
		return domain.ScheduledJob{}, errors.New("scheduled job terminal transition is invalid")
	}
	if err := revokeScheduledJobLeaseTx(ctx, tx, record, now, string(reason)); err != nil {
		return domain.ScheduledJob{}, err
	}
	job.Status = status
	job.StopReason = reason
	job.NextWakeAt = nil
	job.PendingOccurrenceAt = nil
	job.ActiveLeaseGeneration = 0
	job.ActiveLeaseExpiresAt = nil
	job.LastResult = boundedScheduledJobText(summary)
	job.CompletedAt = &now
	job.UpdatedAt = now
	job.Revision++
	if err := updateScheduledJobTx(ctx, tx, job, record.Job.Revision, record,
		false); err != nil {
		return domain.ScheduledJob{}, err
	}
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id, status,
		config_json, budget_json, started_at, finished_at, created_at, updated_at
		FROM runs WHERE id = ?`, job.OwnerRunID))
	if err != nil {
		return domain.ScheduledJob{}, err
	}
	if err := appendScheduledJobEventTx(ctx, tx, run,
		events.ScheduledJobStoppedEvent, "scheduled_job_worker", job.ID,
		map[string]any{"status": status, "stop_reason": reason,
			"rounds_completed": job.RoundsCompleted, "model_calls": job.ModelCalls,
			"model_called": false, "tool_called": false}, now); err != nil {
		return domain.ScheduledJob{}, err
	}
	if _, err := maybeRecordScheduledJobNotificationTx(ctx, tx, run, job,
		false, status == domain.ScheduledJobFailed, false, now); err != nil {
		return domain.ScheduledJob{}, err
	}
	return job, nil
}

func revokeScheduledJobLeaseTx(ctx context.Context, tx *sql.Tx,
	record scheduledJobRecord, at time.Time, reason string,
) error {
	job := record.Job
	if job.ActiveLeaseGeneration == 0 || job.PendingOccurrenceAt == nil {
		return nil
	}
	status := domain.ScheduledJobRoundRetryWait
	completed := any(nil)
	if reason != "paused" {
		status = domain.ScheduledJobRoundFailed
		completed = ts(at)
	}
	result, err := tx.ExecContext(ctx, `UPDATE scheduled_job_rounds SET status = ?,
		result = ?, error_code = ?, completed_at = ? WHERE job_id = ? AND occurrence_at = ?
		AND status = 'claimed' AND claim_generation = ? AND fence_token_sha256 = ?`,
		status, boundedScheduledJobText("lease revoked: "+reason), "lease_revoked",
		completed, job.ID, ts(*job.PendingOccurrenceAt), job.ActiveLeaseGeneration,
		record.FenceTokenSHA256)
	if err != nil {
		return err
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return apperror.New(apperror.CodeConflict,
			"scheduled job active lease could not be revoked")
	}
	return nil
}

func scheduledJobPreclaimStop(job domain.ScheduledJob,
	now time.Time,
) domain.ScheduledJobStopReason {
	if !now.Before(job.Spec.DeadlineAt) {
		return domain.ScheduledJobStopDeadline
	}
	if !now.Before(job.CreatedAt.Add(time.Duration(job.Spec.MaxElapsedSeconds) * time.Second)) {
		return domain.ScheduledJobStopElapsedBudget
	}
	if job.RoundsCompleted >= job.Spec.MaxRounds {
		return domain.ScheduledJobStopRoundBudget
	}
	if job.Spec.MaxModelCalls > 0 && job.ModelCalls >= job.Spec.MaxModelCalls {
		return domain.ScheduledJobStopModelBudget
	}
	return domain.ScheduledJobStopNone
}

func validateScheduledJobCreate(job domain.ScheduledJob,
	authorization *domain.ScheduledJobAuthorization,
	consent *domain.ScheduledJobObservationConsent,
	operation domain.ScheduledJobOperation,
) error {
	if err := job.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"scheduled job is invalid", err)
	}
	if job.Status != domain.ScheduledJobActive || job.Revision != 1 ||
		job.RoundsCompleted != 0 || job.ModelCalls != 0 ||
		job.ConsecutiveUnchanged != 0 || job.LastEventSequence != 0 ||
		job.LastObservationSHA256 != "" || job.LastResult != "" ||
		job.LastErrorCode != "" || job.PendingOccurrenceAt != nil ||
		job.ActiveLeaseGeneration != 0 || job.CompletedAt != nil {
		return apperror.New(apperror.CodeInvalidArgument,
			"new scheduled job contains runtime state")
	}
	if err := operation.Validate(); err != nil || operation.Action != domain.ScheduledJobCreate ||
		operation.JobID != job.ID || operation.RunID != job.OwnerRunID ||
		operation.ExpectedRevision != 0 || operation.RequestedBy != job.CreatedBy ||
		operation.CreatedAt != job.CreatedAt {
		return apperror.New(apperror.CodeInvalidArgument,
			"scheduled job create operation is invalid")
	}
	if job.Spec.ExecutionMode == domain.ScheduledJobReadOnly && authorization != nil {
		return apperror.New(apperror.CodeInvalidArgument,
			"read-only scheduled jobs cannot persist repair authorization")
	}
	if job.Spec.ExecutionMode == domain.ScheduledJobApprovedRepair {
		if authorization == nil || authorization.Validate() != nil ||
			authorization.JobID != job.ID || authorization.RunID != job.OwnerRunID ||
			authorization.AuthorizedBy != job.CreatedBy ||
			authorization.AuthorizedAt != job.CreatedAt ||
			authorization.ExpiresAt.After(job.Spec.DeadlineAt) {
			return apperror.New(apperror.CodeInvalidArgument,
				"approved repair scheduled job requires an exact bounded authorization")
		}
	}
	if consent != nil {
		if err := consent.Validate(); err != nil || consent.JobID != job.ID ||
			consent.RunID != job.OwnerRunID || consent.ConfirmedBy != job.CreatedBy ||
			consent.ConfirmedAt != job.CreatedAt ||
			consent.OperationKeySHA256 != operation.KeyDigest ||
			consent.RequestFingerprint != operation.RequestFingerprint ||
			job.Spec.ExecutionMode != domain.ScheduledJobReadOnly ||
			job.Spec.MaxModelCalls != 0 {
			return apperror.New(apperror.CodeInvalidArgument,
				"scheduled job observation consent requires an exact read-only zero-model create")
		}
	}
	return nil
}

func validateScheduledJobAuthority(job domain.ScheduledJob,
	authorization *domain.ScheduledJobAuthorization, mode domain.RunModeSnapshot,
	permission domain.RunExecutionPermissionSnapshot, at time.Time,
) error {
	if job.Spec.ExecutionMode == domain.ScheduledJobAutomatic {
		return nil
	}
	if job.Spec.ExecutionMode == domain.ScheduledJobReadOnly {
		if mode.Phase != domain.ExecutionPhasePlan {
			return apperror.New(apperror.CodeFailedPrecondition,
				"read-only loop-monitor creation requires Plan root mode")
		}
		return nil
	}
	if authorization == nil || mode.Surface != domain.ExecutionSurfaceCode ||
		mode.Phase != domain.ExecutionPhaseDeliver ||
		permission.Mode != domain.RunExecutionPermissionFull ||
		!permission.OperatorConfirmed ||
		authorization.ModeSnapshotID != mode.ID ||
		authorization.ModeRevision != mode.Revision ||
		authorization.PermissionSnapshotID != permission.ID ||
		authorization.PermissionRevision != permission.Revision ||
		!at.Before(authorization.ExpiresAt) {
		return apperror.New(apperror.CodeFailedPrecondition,
			"repair loop-monitor requires exact current Code/Deliver permission authorization")
	}
	return nil
}

func requireCurrentScheduledJobAuthorization(ctx context.Context, tx *sql.Tx,
	job domain.ScheduledJob, now time.Time,
) error {
	var authorization *domain.ScheduledJobAuthorization
	if job.Spec.ExecutionMode == domain.ScheduledJobApprovedRepair {
		stored, found, err := getScheduledJobAuthorization(ctx, tx, job.ID)
		if err != nil || !found {
			if err == nil {
				err = errors.New("scheduled job repair authorization is missing")
			}
			return err
		}
		authorization = &stored
	}
	mode, err := getCurrentRunModeSnapshot(ctx, tx, job.OwnerRunID)
	if err != nil {
		return err
	}
	permission, err := getCurrentRunExecutionPermissionSnapshot(ctx, tx,
		job.OwnerRunID)
	if err != nil {
		return err
	}
	return validateScheduledJobAuthority(job, authorization, mode, permission, now)
}

func insertScheduledJobTx(ctx context.Context, tx *sql.Tx,
	job domain.ScheduledJob,
) error {
	specJSON, err := json.Marshal(job.Spec)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO scheduled_jobs
		(id, protocol_version, spec_json, target_run_id, owner_run_id,
		owner_root_agent_id, execution_mode, status, revision, next_wake_at,
		pending_occurrence_at, rounds_completed, model_calls, consecutive_unchanged,
		last_event_sequence, last_observation_sha256, last_result, last_error_code,
		stop_reason, active_lease_generation, active_lease_owner_sha256,
		active_fence_token_sha256, active_lease_expires_at, created_by, created_at,
		updated_at, completed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL,
		0, 0, 0, 0, '', '', '', '', 0, '', '', NULL, ?, ?, ?, NULL)`,
		job.ID, job.Spec.Version, string(specJSON), job.Spec.TargetRunID,
		job.OwnerRunID, job.OwnerRootAgentID, job.Spec.ExecutionMode, job.Status,
		job.Revision, nullableScheduledTime(job.NextWakeAt), job.CreatedBy,
		ts(job.CreatedAt), ts(job.UpdatedAt))
	return err
}

func updateScheduledJobTx(ctx context.Context, tx *sql.Tx, job domain.ScheduledJob,
	expectedRevision int64, previous scheduledJobRecord, transitionClearedLease bool,
) error {
	if err := job.Validate(); err != nil {
		return fmt.Errorf("scheduled job update is invalid: %w", err)
	}
	ownerDigest := previous.LeaseOwnerSHA256
	fenceDigest := previous.FenceTokenSHA256
	if job.ActiveLeaseGeneration == 0 {
		ownerDigest = ""
		fenceDigest = ""
	}
	if transitionClearedLease || job.ActiveLeaseGeneration == 0 {
		ownerDigest, fenceDigest = "", ""
	}
	result, err := tx.ExecContext(ctx, `UPDATE scheduled_jobs SET status = ?, revision = ?,
		next_wake_at = ?, pending_occurrence_at = ?, rounds_completed = ?, model_calls = ?,
		consecutive_unchanged = ?, last_event_sequence = ?, last_observation_sha256 = ?,
		last_result = ?, last_error_code = ?, stop_reason = ?, active_lease_generation = ?,
		active_lease_owner_sha256 = ?, active_fence_token_sha256 = ?,
		active_lease_expires_at = ?, updated_at = ?, completed_at = ?
		WHERE id = ? AND revision = ?`, job.Status, job.Revision,
		nullableScheduledTime(job.NextWakeAt), nullableScheduledTime(job.PendingOccurrenceAt),
		job.RoundsCompleted, job.ModelCalls, job.ConsecutiveUnchanged,
		job.LastEventSequence, job.LastObservationSHA256, job.LastResult,
		job.LastErrorCode, job.StopReason, job.ActiveLeaseGeneration, ownerDigest,
		fenceDigest, nullableScheduledTime(job.ActiveLeaseExpiresAt), ts(job.UpdatedAt),
		nullableScheduledTime(job.CompletedAt), job.ID, expectedRevision)
	if err != nil {
		return err
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return apperror.New(apperror.CodeConflict,
			"scheduled job changed before update")
	}
	return nil
}

func insertScheduledJobAuthorizationTx(ctx context.Context, tx *sql.Tx,
	authorization domain.ScheduledJobAuthorization,
) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO scheduled_job_authorizations
		(job_id, protocol_version, run_id, mode_snapshot_id, mode_revision,
		permission_snapshot_id, permission_revision, authorized_by, authorized_at,
		expires_at, execution_bypass, network_bypass, approval_bypass)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 0)`, authorization.JobID,
		authorization.ProtocolVersion, authorization.RunID,
		authorization.ModeSnapshotID, authorization.ModeRevision,
		authorization.PermissionSnapshotID, authorization.PermissionRevision,
		authorization.AuthorizedBy, ts(authorization.AuthorizedAt),
		ts(authorization.ExpiresAt))
	return err
}

func insertScheduledJobOperationTx(ctx context.Context, tx *sql.Tx,
	operation domain.ScheduledJobOperation,
) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO scheduled_job_operations
		(operation_key_sha256, request_fingerprint, protocol_version, action,
		job_id, run_id, expected_revision, requested_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, operation.KeyDigest,
		operation.RequestFingerprint, operation.ProtocolVersion, operation.Action,
		operation.JobID, operation.RunID, operation.ExpectedRevision,
		operation.RequestedBy, ts(operation.CreatedAt))
	return err
}

func insertScheduledJobObservationConsentTx(ctx context.Context, tx *sql.Tx,
	consent domain.ScheduledJobObservationConsent,
) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO scheduled_job_observation_consents
		(job_id, run_id, version, confirmed_by, confirmed_at,
		 operation_key_sha256, request_fingerprint)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, consent.JobID, consent.RunID,
		consent.Version, consent.ConfirmedBy, ts(consent.ConfirmedAt),
		consent.OperationKeySHA256, consent.RequestFingerprint)
	return err
}

func getScheduledJob(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (scheduledJobRecord, error) {
	return scanScheduledJob(queryer.QueryRowContext(ctx,
		scheduledJobSelect+`WHERE id = ?`, id))
}

func scanScheduledJob(row scanner) (scheduledJobRecord, error) {
	var record scheduledJobRecord
	var specJSON, status, stopReason string
	var nextWake, occurrence, leaseExpires, completed sql.NullString
	var createdAt, updatedAt string
	err := row.Scan(&record.Job.ID, &specJSON, &record.Job.OwnerRunID,
		&record.Job.OwnerRootAgentID, &status, &record.Job.Revision, &nextWake,
		&occurrence, &record.Job.RoundsCompleted, &record.Job.ModelCalls,
		&record.Job.ConsecutiveUnchanged, &record.Job.LastEventSequence,
		&record.Job.LastObservationSHA256, &record.Job.LastResult,
		&record.Job.LastErrorCode, &stopReason, &record.Job.ActiveLeaseGeneration,
		&leaseExpires, &record.Job.CreatedBy, &createdAt, &updatedAt, &completed,
		&record.LeaseOwnerSHA256, &record.FenceTokenSHA256,
		&record.Job.ObservationConsentVersion, &record.Job.ExecutionConsentVersion)
	if err != nil {
		return scheduledJobRecord{}, err
	}
	if err := json.Unmarshal([]byte(specJSON), &record.Job.Spec); err != nil {
		return scheduledJobRecord{}, fmt.Errorf("decode scheduled job spec: %w", err)
	}
	record.Job.Status = domain.ScheduledJobStatus(status)
	record.Job.StopReason = domain.ScheduledJobStopReason(stopReason)
	record.Job.NextWakeAt = parseNullableTS(nextWake)
	record.Job.PendingOccurrenceAt = parseNullableTS(occurrence)
	record.Job.ActiveLeaseExpiresAt = parseNullableTS(leaseExpires)
	record.Job.CreatedAt = parseTS(createdAt)
	record.Job.UpdatedAt = parseTS(updatedAt)
	record.Job.CompletedAt = parseNullableTS(completed)
	if err := record.Job.Validate(); err != nil {
		return scheduledJobRecord{}, fmt.Errorf("stored scheduled job is invalid: %w", err)
	}
	if record.Job.ActiveLeaseGeneration == 0 &&
		(record.LeaseOwnerSHA256 != "" || record.FenceTokenSHA256 != "") {
		return scheduledJobRecord{}, errors.New("stored scheduled job retained stale lease digests")
	}
	if record.Job.ActiveLeaseGeneration > 0 &&
		(!validStoreDigest(record.LeaseOwnerSHA256) ||
			!validStoreDigest(record.FenceTokenSHA256)) {
		return scheduledJobRecord{}, errors.New("stored scheduled job lease digests are invalid")
	}
	return record, nil
}

func getScheduledJobObservationConsentByOperation(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, keyDigest string) (domain.ScheduledJobObservationConsent, bool, error) {
	var value domain.ScheduledJobObservationConsent
	var confirmedAt string
	err := queryer.QueryRowContext(ctx, `SELECT job_id, run_id, version,
		confirmed_by, confirmed_at, operation_key_sha256, request_fingerprint
		FROM scheduled_job_observation_consents WHERE operation_key_sha256 = ?`,
		keyDigest).Scan(&value.JobID, &value.RunID, &value.Version,
		&value.ConfirmedBy, &confirmedAt, &value.OperationKeySHA256,
		&value.RequestFingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ScheduledJobObservationConsent{}, false, nil
	}
	if err != nil {
		return domain.ScheduledJobObservationConsent{}, false, err
	}
	value.ConfirmedAt = parseTS(confirmedAt)
	return value, true, value.Validate()
}

func getScheduledJobOperation(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, keyDigest string) (domain.ScheduledJobOperation, bool, error) {
	var operation domain.ScheduledJobOperation
	var action, createdAt string
	err := queryer.QueryRowContext(ctx, `SELECT operation_key_sha256,
		request_fingerprint, protocol_version, action, job_id, run_id,
		expected_revision, requested_by, created_at FROM scheduled_job_operations
		WHERE operation_key_sha256 = ?`, keyDigest).Scan(&operation.KeyDigest,
		&operation.RequestFingerprint, &operation.ProtocolVersion, &action,
		&operation.JobID, &operation.RunID, &operation.ExpectedRevision,
		&operation.RequestedBy, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ScheduledJobOperation{}, false, nil
	}
	if err != nil {
		return domain.ScheduledJobOperation{}, false, err
	}
	operation.Action = domain.ScheduledJobAction(action)
	operation.CreatedAt = parseTS(createdAt)
	return operation, true, operation.Validate()
}

func getScheduledJobAuthorization(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, jobID string) (domain.ScheduledJobAuthorization, bool, error) {
	var value domain.ScheduledJobAuthorization
	var authorizedAt, expiresAt string
	err := queryer.QueryRowContext(ctx, `SELECT protocol_version, job_id, run_id,
		mode_snapshot_id, mode_revision, permission_snapshot_id, permission_revision,
		authorized_by, authorized_at, expires_at, execution_bypass, network_bypass,
		approval_bypass FROM scheduled_job_authorizations WHERE job_id = ?`, jobID).
		Scan(&value.ProtocolVersion, &value.JobID, &value.RunID,
			&value.ModeSnapshotID, &value.ModeRevision, &value.PermissionSnapshotID,
			&value.PermissionRevision, &value.AuthorizedBy, &authorizedAt, &expiresAt,
			&value.ExecutionBypass, &value.NetworkBypass, &value.ApprovalBypass)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ScheduledJobAuthorization{}, false, nil
	}
	if err != nil {
		return domain.ScheduledJobAuthorization{}, false, err
	}
	value.AuthorizedAt = parseTS(authorizedAt)
	value.ExpiresAt = parseTS(expiresAt)
	return value, true, value.Validate()
}

func getScheduledJobRoundTx(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, jobID string, occurrence time.Time) (domain.ScheduledJobRound, error) {
	return scanScheduledJobRound(queryer.QueryRowContext(ctx, `SELECT protocol_version,
		job_id, occurrence_at, ordinal, attempt, claim_generation, status,
		event_sequence, observation_sha256, changed, model_called, tool_called,
		result, error_code, started_at, completed_at FROM scheduled_job_rounds
		WHERE job_id = ? AND occurrence_at = ?`, jobID, ts(occurrence)))
}

func scanScheduledJobRound(row scanner) (domain.ScheduledJobRound, error) {
	var value domain.ScheduledJobRound
	var occurrence, status, startedAt string
	var completedAt sql.NullString
	err := row.Scan(&value.ProtocolVersion, &value.JobID, &occurrence,
		&value.Ordinal, &value.Attempt, &value.ClaimGeneration, &status,
		&value.EventSequence, &value.ObservationSHA256, &value.Changed,
		&value.ModelCalled, &value.ToolCalled, &value.Result, &value.ErrorCode,
		&startedAt, &completedAt)
	if err != nil {
		return domain.ScheduledJobRound{}, err
	}
	value.OccurrenceAt = parseTS(occurrence)
	value.Status = domain.ScheduledJobRoundStatus(status)
	value.StartedAt = parseTS(startedAt)
	value.CompletedAt = parseNullableTS(completedAt)
	if err := value.Validate(); err != nil {
		return domain.ScheduledJobRound{}, fmt.Errorf("stored scheduled job round is invalid: %w", err)
	}
	return value, nil
}

func requireScheduledJobOperationReplay(stored domain.ScheduledJobOperation,
	requested domain.ScheduledJobOperation,
) error {
	storedIdentity, storedErr := stored.ReplayIdentity()
	requestedIdentity, requestedErr := requested.ReplayIdentity()
	decision, decisionErr := durableoperation.Decide(storedIdentity, requestedIdentity)
	jobIdentityMatches := stored.JobID == requested.JobID
	// A create assigns the Job ID as an output after the caller has supplied
	// its semantic intent. Concurrent creators therefore generate different
	// candidate IDs; the durable operation must replay the first committed
	// output when every request-bound field and fingerprint still match.
	if stored.Action == domain.ScheduledJobCreate &&
		requested.Action == domain.ScheduledJobCreate {
		jobIdentityMatches = true
	}
	if stored.ProtocolVersion != requested.ProtocolVersion ||
		storedErr != nil || requestedErr != nil || decisionErr != nil ||
		decision != durableoperation.DecisionReplay ||
		stored.Action != requested.Action || !jobIdentityMatches ||
		stored.RunID != requested.RunID ||
		stored.ExpectedRevision != requested.ExpectedRevision ||
		stored.RequestedBy != requested.RequestedBy {
		return apperror.New(apperror.CodeConflict,
			"scheduled job operation key was already used for different intent")
	}
	return nil
}

func appendScheduledJobEventTx(ctx context.Context, tx *sql.Tx, run domain.Run,
	eventType string, source string, subjectID string, payload any, at time.Time,
) error {
	event, err := events.New(run.ID, run.MissionID, eventType, source, subjectID, payload)
	if err != nil {
		return err
	}
	event.CreatedAt = at.UTC()
	_, err = insertRunEventTx(ctx, tx, event)
	return err
}

func maybeRecordScheduledJobNotificationTx(ctx context.Context, tx *sql.Tx,
	run domain.Run, job domain.ScheduledJob, changed bool, failed bool,
	recovered bool, now time.Time,
) (*domain.ScheduledJobNotification, error) {
	mode := job.Spec.Notification
	kind := ""
	summary := ""
	switch {
	case failed && (mode == domain.ScheduledJobNotifyFailure || mode == domain.ScheduledJobNotifyAll):
		kind, summary = "failure", "Scheduled monitor round failed: "+job.LastErrorCode
	case recovered && (mode == domain.ScheduledJobNotifyFailure || mode == domain.ScheduledJobNotifyAll):
		kind, summary = "recovery", "Scheduled monitor recovered after an earlier failure"
	case changed && (mode == domain.ScheduledJobNotifyChange || mode == domain.ScheduledJobNotifyAll):
		kind, summary = "change", job.LastResult
	case mode == domain.ScheduledJobNotifyAll && !job.Status.Terminal():
		kind, summary = "completed", "Scheduled monitor round completed without target changes"
	case job.Status.Terminal() && mode == domain.ScheduledJobNotifyAll:
		kind, summary = "completed", "Scheduled monitor stopped: "+string(job.StopReason)
	default:
		return nil, nil
	}
	summary = boundedScheduledJobText(summary)
	dedup := scheduledJobDigest(job.ID + "\x00" + kind + "\x00" +
		fmt.Sprintf("%d", job.LastEventSequence) + "\x00" + job.LastObservationSHA256 +
		"\x00" + job.LastErrorCode + "\x00" + string(job.StopReason))
	value := domain.ScheduledJobNotification{ID: idgen.New("scheduled-notification"),
		JobID: job.ID, Kind: kind, Summary: summary, CreatedAt: now}
	if err := value.Validate(); err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO scheduled_job_notifications
		(id, job_id, dedup_key_sha256, kind, summary, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, value.ID, value.JobID, dedup, value.Kind,
		value.Summary, ts(value.CreatedAt))
	if err != nil {
		return nil, err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows == 0 {
		return nil, err
	}
	if err := appendScheduledJobEventTx(ctx, tx, run,
		events.ScheduledJobNotificationEvent, "scheduled_job_worker", value.ID,
		map[string]any{"job_id": job.ID, "kind": kind,
			"deduplicated": false, "content_included": false}, now); err != nil {
		return nil, err
	}
	return &value, nil
}

func normalizeScheduledJobWriteError(err error) error {
	if err == nil {
		return nil
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "unique constraint") || strings.Contains(lower, "constraint failed") {
		return apperror.Wrap(apperror.CodeConflict,
			"scheduled job state conflicts with an existing operation", err)
	}
	return err
}

func scheduledJobDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func boundedScheduledJobText(value string) string {
	value = strings.TrimSpace(redact.String(value))
	if value == "" {
		value = "no additional details"
	}
	runes := []rune(value)
	if len(runes) > domain.MaxScheduledJobSummaryRunes {
		value = string(runes[:domain.MaxScheduledJobSummaryRunes])
	}
	if !utf8.ValidString(value) {
		return "invalid UTF-8 details were omitted"
	}
	return value
}

func nullableScheduledTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return ts(value.UTC())
}
