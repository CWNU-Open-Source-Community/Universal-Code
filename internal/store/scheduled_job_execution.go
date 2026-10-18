package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

const scheduledObservationSelection = `(execution_mode='read_only'
 AND CAST(json_extract(spec_json,'$.max_model_calls') AS INTEGER)=0
 AND EXISTS(SELECT 1 FROM scheduled_job_observation_consents consent WHERE consent.job_id=scheduled_jobs.id AND consent.version=1))`
const scheduledExecutionSelection = `(execution_mode='automatic'
 AND EXISTS(SELECT 1 FROM scheduled_job_execution_consents consent WHERE consent.job_id=scheduled_jobs.id AND consent.version=1))`

func scheduledJobSelection(scope int) string {
	switch scope {
	case 1:
		return " AND " + scheduledObservationSelection + " "
	case 2:
		return " AND (" + scheduledObservationSelection + " OR " + scheduledExecutionSelection + ") "
	}
	return ""
}

func (s *SQLiteStore) CreateScheduledJobWithExecutionConsent(ctx context.Context, job domain.ScheduledJob, consent domain.ScheduledJobExecutionConsent, operation domain.ScheduledJobOperation) (domain.ScheduledJob, bool, error) {
	return s.createScheduledJob(ctx, job, nil, nil, &consent, operation)
}
func (s *SQLiteStore) ClaimDueConfirmedScheduledJob(ctx context.Context, owner string, now time.Time) (domain.ScheduledJob, domain.ScheduledJobLease, bool, error) {
	return s.claimDueScheduledJob(ctx, owner, now, 2)
}
func (s *SQLiteStore) ReconcileConfirmedScheduledJobs(ctx context.Context, now time.Time, limit int) (int, error) {
	return s.reconcileScheduledJobs(ctx, now, limit, 2)
}

func (s *SQLiteStore) GetScheduledJobExecutionConsent(ctx context.Context, jobID string) (domain.ScheduledJobExecutionConsent, error) {
	var raw string
	var c domain.ScheduledJobExecutionConsent
	if err := s.db.QueryRowContext(ctx, `SELECT consent_json FROM scheduled_job_execution_consents WHERE job_id=?`, jobID).Scan(&raw); err != nil {
		return c, apperror.Normalize(err)
	}
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}

func (s *SQLiteStore) ListScheduledJobExecutions(ctx context.Context, jobID string, limit int) ([]domain.ScheduledJobExecution, error) {
	if limit < 1 || limit > 100 {
		return nil, apperror.New(apperror.CodeInvalidArgument, "execution receipt list limit is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT operation_key,receipt_json FROM scheduled_job_executions WHERE job_id=? ORDER BY occurrence_at DESC LIMIT ?`, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.ScheduledJobExecution{}
	for rows.Next() {
		var e domain.ScheduledJobExecution
		var raw string
		if err := rows.Scan(&e.OperationKey, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			return nil, err
		}
		if err := e.Validate(); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

func (s *SQLiteStore) GetScheduledJobExecution(ctx context.Context, jobID string, occurrence time.Time) (domain.ScheduledJobExecution, bool, error) {
	return getScheduledExecution(ctx, s.db, jobID, occurrence)
}
func getScheduledExecution(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, jobID string, occurrence time.Time) (domain.ScheduledJobExecution, bool, error) {
	var e domain.ScheduledJobExecution
	var raw string
	err := q.QueryRowContext(ctx, `SELECT operation_key,receipt_json FROM scheduled_job_executions WHERE job_id=? AND occurrence_at=?`, jobID, ts(occurrence)).Scan(&e.OperationKey, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return e, false, nil
	}
	if err != nil {
		return e, false, err
	}
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		return e, false, err
	}
	return e, true, e.Validate()
}

// SaveScheduledJobExecution stores intent before calling the target executor.
// Every later write requires the current schedule lease and the original key.
func (s *SQLiteStore) SaveScheduledJobExecution(ctx context.Context, lease domain.ScheduledJobLease, e domain.ScheduledJobExecution, now time.Time) (domain.ScheduledJobExecution, error) {
	if err := e.Validate(); err != nil {
		return e, err
	}
	if e.JobID != lease.JobID || !e.OccurrenceAt.Equal(lease.OccurrenceAt) || e.OperationKey != lease.OperationKey {
		return e, apperror.New(apperror.CodeConflict, "execution receipt does not match its occurrence")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return e, err
	}
	defer tx.Rollback()
	record, _, err := requireScheduledJobLeaseTx(ctx, tx, lease, now)
	if err != nil {
		return e, err
	}
	if record.Job.Spec.ExecutionMode != domain.ScheduledJobAutomatic || record.Job.ExecutionConsentVersion != 1 {
		return e, apperror.New(apperror.CodePolicyDenied, "execution consent is required")
	}
	previous, found, err := getScheduledExecution(ctx, tx, e.JobID, e.OccurrenceAt)
	if err != nil {
		return e, err
	}
	if found && (previous.OperationKey != e.OperationKey || !previous.CreatedAt.Equal(e.CreatedAt) || previous.RunID != "" && previous.RunID != e.RunID || previous.MessageID != "" && previous.MessageID != e.MessageID || previous.WorkflowExecutionID != "" && previous.WorkflowExecutionID != e.WorkflowExecutionID) {
		return e, apperror.New(apperror.CodeConflict, "original scheduled submission cannot be replaced")
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return e, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO scheduled_job_executions(job_id,occurrence_at,operation_key,receipt_json) VALUES(?,?,?,?) ON CONFLICT(job_id,occurrence_at) DO UPDATE SET receipt_json=excluded.receipt_json`, e.JobID, ts(e.OccurrenceAt), e.OperationKey, string(raw))
	if err != nil {
		return e, err
	}
	return e, tx.Commit()
}

func (s *SQLiteStore) RenewScheduledJobLease(ctx context.Context, lease domain.ScheduledJobLease, now time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	record, _, err := requireScheduledJobLeaseTx(ctx, tx, lease, now)
	if err != nil {
		return false, err
	}
	expiry := now.Add(domain.ScheduledJobLeaseSeconds * time.Second)
	if expiry.After(record.Job.Spec.DeadlineAt) {
		expiry = record.Job.Spec.DeadlineAt
	}
	_, err = tx.ExecContext(ctx, `UPDATE scheduled_jobs SET active_lease_expires_at=? WHERE id=? AND active_lease_generation=? AND active_fence_token_sha256=?`, ts(expiry), lease.JobID, lease.Generation, lease.FenceTokenSHA256)
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// Yield preserves the same round while its target is running or awaiting an
// existing approval. It releases the schedule lease, not the target execution.
func (s *SQLiteStore) YieldScheduledJobExecution(ctx context.Context, lease domain.ScheduledJobLease, summary string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	record, _, err := requireScheduledJobLeaseTx(ctx, tx, lease, now)
	if err != nil {
		return err
	}
	e, found, err := getScheduledExecution(ctx, tx, lease.JobID, lease.OccurrenceAt)
	if err != nil {
		return err
	}
	if !found {
		return apperror.New(apperror.CodeFailedPrecondition, "execution receipt is required before waiting")
	}
	job := record.Job
	job.ActiveLeaseGeneration = 0
	job.ActiveLeaseExpiresAt = nil
	next := now.Add(5 * time.Second)
	if next.After(job.Spec.DeadlineAt) {
		next = job.Spec.DeadlineAt
	}
	job.NextWakeAt = &next
	job.UpdatedAt = now
	job.Revision++
	job.LastResult = boundedScheduledJobText(summary)
	if err := updateScheduledJobTx(ctx, tx, job, record.Job.Revision, record, false); err != nil {
		return err
	}
	// The existing notification deduplication includes the result, so an
	// unchanged approval wait does not produce another notification.
	if e.State == "waiting_approval" || e.State == "blocked" {
		run, err := getRunControlRunTx(ctx, tx, job.OwnerRunID)
		if err != nil {
			return err
		}
		if _, err := maybeRecordScheduledJobNotificationTx(ctx, tx, run, job, job.LastResult != record.Job.LastResult, false, false, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}
