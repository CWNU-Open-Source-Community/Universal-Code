package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/durableoperation"
)

const (
	ScheduledJobProtocolVersion        = "scheduled-job.v1"
	ScheduledJobControlProtocolVersion = "scheduled-job-control.v1"
	ScheduledJobRoundProtocolVersion   = "scheduled-job-round.v1"
	ScheduledJobAuthProtocolVersion    = "scheduled-job-authorization.v1"
	scheduledJobReplayDomain           = "scheduled_job_operation.v1"

	MinScheduledJobIntervalSeconds        = 1
	MaxScheduledJobIntervalSeconds        = 30 * 24 * 60 * 60
	MaxScheduledJobElapsedSeconds         = 90 * 24 * 60 * 60
	MaxScheduledJobRounds                 = 10_000
	MaxScheduledJobModelCalls             = 10_000
	MaxScheduledJobAttempts               = 8
	MaxScheduledJobBackoffSeconds         = 6 * 60 * 60
	ScheduledJobLeaseSeconds              = 30
	MaxScheduledJobSummaryRunes           = 512
	MaxScheduledJobTimezoneBytes          = 128
	ScheduledJobObservationConsentVersion = 1
	ScheduledJobExecutionConsentVersion   = 1
)

type ScheduledJobKind string

const (
	ScheduledJobOnce     ScheduledJobKind = "once"
	ScheduledJobPeriodic ScheduledJobKind = "periodic"
)

func (k ScheduledJobKind) Valid() bool {
	return k == ScheduledJobOnce || k == ScheduledJobPeriodic
}

type ScheduledJobMisfirePolicy string

const (
	// ScheduledJobMisfireRunOnce collapses all missed intervals into one catch-up
	// occurrence. It never replays every interval after a long sleep.
	ScheduledJobMisfireRunOnce ScheduledJobMisfirePolicy = "run_once"
	// ScheduledJobMisfireSkip records one bounded skip receipt and advances to
	// the first future occurrence without executing a round.
	ScheduledJobMisfireSkip ScheduledJobMisfirePolicy = "skip"
)

func (p ScheduledJobMisfirePolicy) Valid() bool {
	return p == ScheduledJobMisfireRunOnce || p == ScheduledJobMisfireSkip
}

type ScheduledJobNotificationMode string

const (
	ScheduledJobNotifySilent  ScheduledJobNotificationMode = "silent"
	ScheduledJobNotifyChange  ScheduledJobNotificationMode = "on_change"
	ScheduledJobNotifyFailure ScheduledJobNotificationMode = "on_failure"
	ScheduledJobNotifyAll     ScheduledJobNotificationMode = "all"
)

func (m ScheduledJobNotificationMode) Valid() bool {
	switch m {
	case ScheduledJobNotifySilent, ScheduledJobNotifyChange,
		ScheduledJobNotifyFailure, ScheduledJobNotifyAll:
		return true
	default:
		return false
	}
}

type ScheduledJobExecutionMode string

const (
	ScheduledJobReadOnly       ScheduledJobExecutionMode = "read_only"
	ScheduledJobApprovedRepair ScheduledJobExecutionMode = "approved_repair"
	ScheduledJobAutomatic      ScheduledJobExecutionMode = "automatic"
)

func (m ScheduledJobExecutionMode) Valid() bool {
	return m == ScheduledJobReadOnly || m == ScheduledJobApprovedRepair || m == ScheduledJobAutomatic
}

type ScheduledJobStatus string

const (
	ScheduledJobActive    ScheduledJobStatus = "active"
	ScheduledJobPaused    ScheduledJobStatus = "paused"
	ScheduledJobCompleted ScheduledJobStatus = "completed"
	ScheduledJobFailed    ScheduledJobStatus = "failed"
	ScheduledJobCancelled ScheduledJobStatus = "cancelled"
	ScheduledJobExhausted ScheduledJobStatus = "exhausted"
)

func (s ScheduledJobStatus) Valid() bool {
	switch s {
	case ScheduledJobActive, ScheduledJobPaused, ScheduledJobCompleted,
		ScheduledJobFailed, ScheduledJobCancelled, ScheduledJobExhausted:
		return true
	default:
		return false
	}
}

func (s ScheduledJobStatus) Terminal() bool {
	return s.Valid() && s != ScheduledJobActive && s != ScheduledJobPaused
}

type ScheduledJobStopReason string

const (
	ScheduledJobStopNone           ScheduledJobStopReason = ""
	ScheduledJobStopOnceCompleted  ScheduledJobStopReason = "once_completed"
	ScheduledJobStopTargetTerminal ScheduledJobStopReason = "target_terminal"
	ScheduledJobStopDeadline       ScheduledJobStopReason = "deadline"
	ScheduledJobStopRoundBudget    ScheduledJobStopReason = "round_budget"
	ScheduledJobStopModelBudget    ScheduledJobStopReason = "model_budget"
	ScheduledJobStopElapsedBudget  ScheduledJobStopReason = "elapsed_budget"
	ScheduledJobStopRetryExhausted ScheduledJobStopReason = "retry_exhausted"
	ScheduledJobStopAuthorization  ScheduledJobStopReason = "authorization_stale"
	ScheduledJobStopCancelled      ScheduledJobStopReason = "cancelled"
)

func (r ScheduledJobStopReason) Valid() bool {
	switch r {
	case ScheduledJobStopNone, ScheduledJobStopOnceCompleted,
		ScheduledJobStopTargetTerminal, ScheduledJobStopDeadline,
		ScheduledJobStopRoundBudget, ScheduledJobStopModelBudget,
		ScheduledJobStopElapsedBudget, ScheduledJobStopRetryExhausted,
		ScheduledJobStopAuthorization, ScheduledJobStopCancelled:
		return true
	default:
		return false
	}
}

type ScheduledJobSchedule struct {
	Kind            ScheduledJobKind          `json:"kind"`
	Timezone        string                    `json:"timezone"`
	AnchorAt        time.Time                 `json:"anchor_at"`
	IntervalSeconds int64                     `json:"interval_seconds,omitempty"`
	MisfirePolicy   ScheduledJobMisfirePolicy `json:"misfire_policy"`
}

func (s ScheduledJobSchedule) Validate() error {
	if !s.Kind.Valid() || !s.MisfirePolicy.Valid() || s.AnchorAt.IsZero() {
		return errors.New("scheduled job schedule kind, misfire policy, and anchor are required")
	}
	if s.AnchorAt.Location() != time.UTC {
		return errors.New("scheduled job anchor must be normalized to UTC")
	}
	if s.Timezone == "" || s.Timezone != strings.TrimSpace(s.Timezone) ||
		len(s.Timezone) > MaxScheduledJobTimezoneBytes || !utf8.ValidString(s.Timezone) ||
		strings.ContainsRune(s.Timezone, 0) {
		return errors.New("scheduled job timezone is invalid")
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		return fmt.Errorf("scheduled job timezone is not an IANA location: %w", err)
	}
	if s.Kind == ScheduledJobOnce && s.IntervalSeconds != 0 {
		return errors.New("one-time scheduled jobs cannot have an interval")
	}
	if s.Kind == ScheduledJobPeriodic && (s.IntervalSeconds < MinScheduledJobIntervalSeconds ||
		s.IntervalSeconds > MaxScheduledJobIntervalSeconds) {
		return fmt.Errorf("periodic interval must be between %d and %d seconds",
			MinScheduledJobIntervalSeconds, MaxScheduledJobIntervalSeconds)
	}
	return nil
}

type ScheduledJobRetryPolicy struct {
	MaxAttempts           int `json:"max_attempts"`
	InitialBackoffSeconds int `json:"initial_backoff_seconds"`
	MaxBackoffSeconds     int `json:"max_backoff_seconds"`
}

func (p ScheduledJobRetryPolicy) Validate() error {
	if p.MaxAttempts < 1 || p.MaxAttempts > MaxScheduledJobAttempts ||
		p.InitialBackoffSeconds < 1 || p.InitialBackoffSeconds > MaxScheduledJobBackoffSeconds ||
		p.MaxBackoffSeconds < p.InitialBackoffSeconds ||
		p.MaxBackoffSeconds > MaxScheduledJobBackoffSeconds {
		return errors.New("scheduled job retry policy is outside its hard bounds")
	}
	return nil
}

// ScheduledJobSpec is the immutable, non-authorizing policy selected at
// creation. Time arrival only permits observation and a bounded recheck.
type ScheduledJobSpec struct {
	Version              string                       `json:"version"`
	Schedule             ScheduledJobSchedule         `json:"schedule"`
	TargetRunID          string                       `json:"target_run_id"`
	DeadlineAt           time.Time                    `json:"deadline_at"`
	StopOnTargetTerminal bool                         `json:"stop_on_target_terminal"`
	MaxRounds            int                          `json:"max_rounds"`
	MaxModelCalls        int                          `json:"max_model_calls"`
	MaxElapsedSeconds    int64                        `json:"max_elapsed_seconds"`
	Retry                ScheduledJobRetryPolicy      `json:"retry"`
	Notification         ScheduledJobNotificationMode `json:"notification"`
	ExecutionMode        ScheduledJobExecutionMode    `json:"execution_mode"`
	ExecutionTarget      *ScheduledJobExecutionTarget `json:"execution_target,omitempty"`
}

func (s ScheduledJobSpec) Validate() error {
	if s.Version != ScheduledJobProtocolVersion {
		return fmt.Errorf("unsupported scheduled job protocol %q", s.Version)
	}
	if err := s.Schedule.Validate(); err != nil {
		return err
	}
	if !ValidAgentID(s.TargetRunID) || strings.ContainsRune(s.TargetRunID, 0) {
		return errors.New("scheduled job target Run identity is invalid")
	}
	if s.DeadlineAt.IsZero() || s.DeadlineAt.Location() != time.UTC ||
		!s.DeadlineAt.After(s.Schedule.AnchorAt) {
		return errors.New("scheduled job deadline must be a UTC instant after its anchor")
	}
	if s.MaxRounds < 1 || s.MaxRounds > MaxScheduledJobRounds ||
		s.MaxModelCalls < 0 || s.MaxModelCalls > MaxScheduledJobModelCalls ||
		s.MaxModelCalls > s.MaxRounds || s.MaxElapsedSeconds < 1 ||
		s.MaxElapsedSeconds > MaxScheduledJobElapsedSeconds {
		return errors.New("scheduled job round, model, or elapsed budget is outside its hard bounds")
	}
	if err := s.Retry.Validate(); err != nil {
		return err
	}
	if !s.Notification.Valid() || !s.ExecutionMode.Valid() {
		return errors.New("scheduled job notification or execution mode is invalid")
	}
	if s.ExecutionMode == ScheduledJobApprovedRepair && s.MaxModelCalls == 0 {
		return errors.New("approved repair scheduled jobs require a positive model-call budget")
	}
	if s.ExecutionMode == ScheduledJobAutomatic {
		if s.ExecutionTarget == nil || s.MaxModelCalls != 0 || s.StopOnTargetTerminal {
			return errors.New("automatic scheduled jobs require an execution target, Run-owned model budget, and no owner-terminal stop")
		}
		return s.ExecutionTarget.Validate()
	}
	if s.ExecutionTarget != nil {
		return errors.New("execution targets require automatic execution mode")
	}
	return nil
}

type ScheduledJob struct {
	ID                        string                 `json:"id"`
	Spec                      ScheduledJobSpec       `json:"spec"`
	OwnerRunID                string                 `json:"owner_run_id"`
	OwnerRootAgentID          string                 `json:"owner_root_agent_id"`
	Status                    ScheduledJobStatus     `json:"status"`
	Revision                  int64                  `json:"revision"`
	NextWakeAt                *time.Time             `json:"next_wake_at,omitempty"`
	PendingOccurrenceAt       *time.Time             `json:"pending_occurrence_at,omitempty"`
	RoundsCompleted           int                    `json:"rounds_completed"`
	ModelCalls                int                    `json:"model_calls"`
	ConsecutiveUnchanged      int                    `json:"consecutive_unchanged"`
	LastEventSequence         int64                  `json:"last_event_sequence"`
	LastObservationSHA256     string                 `json:"last_observation_sha256,omitempty"`
	LastResult                string                 `json:"last_result,omitempty"`
	LastErrorCode             string                 `json:"last_error_code,omitempty"`
	StopReason                ScheduledJobStopReason `json:"stop_reason,omitempty"`
	ActiveLeaseGeneration     int64                  `json:"active_lease_generation"`
	ActiveLeaseExpiresAt      *time.Time             `json:"active_lease_expires_at,omitempty"`
	CreatedBy                 string                 `json:"created_by"`
	CreatedAt                 time.Time              `json:"created_at"`
	UpdatedAt                 time.Time              `json:"updated_at"`
	CompletedAt               *time.Time             `json:"completed_at,omitempty"`
	ObservationConsentVersion int                    `json:"observation_consent_version"`
	ExecutionConsentVersion   int                    `json:"execution_consent_version"`
}

func (j ScheduledJob) Validate() error {
	for label, value := range map[string]string{
		"id": j.ID, "owner Run id": j.OwnerRunID,
		"owner root Agent id": j.OwnerRootAgentID, "creator": j.CreatedBy,
	} {
		if !ValidAgentID(value) || strings.ContainsRune(value, 0) {
			return fmt.Errorf("scheduled job %s is invalid", label)
		}
	}
	if err := j.Spec.Validate(); err != nil {
		return err
	}
	if j.Spec.TargetRunID != j.OwnerRunID {
		return errors.New("scheduled job v1 owner and explicit target Run must match")
	}
	if !j.Status.Valid() || j.Revision < 1 || j.RoundsCompleted < 0 ||
		j.RoundsCompleted > j.Spec.MaxRounds || j.ModelCalls < 0 ||
		j.ModelCalls > j.Spec.MaxModelCalls || j.ConsecutiveUnchanged < 0 ||
		j.LastEventSequence < 0 || j.ActiveLeaseGeneration < 0 ||
		j.CreatedAt.IsZero() || j.UpdatedAt.Before(j.CreatedAt) ||
		(j.ObservationConsentVersion != 0 &&
			j.ObservationConsentVersion != ScheduledJobObservationConsentVersion) ||
		(j.ExecutionConsentVersion != 0 && j.ExecutionConsentVersion != ScheduledJobExecutionConsentVersion) {
		return errors.New("scheduled job mutable state is invalid")
	}
	if j.LastObservationSHA256 != "" && !validLowerHexDigest(j.LastObservationSHA256) {
		return errors.New("scheduled job observation digest is invalid")
	}
	if err := validateScheduledJobSummary(j.LastResult, true); err != nil {
		return err
	}
	if err := validateScheduledJobCode(j.LastErrorCode); err != nil {
		return err
	}
	if !j.StopReason.Valid() {
		return errors.New("scheduled job stop reason is invalid")
	}
	if (j.NextWakeAt == nil) != (j.Status != ScheduledJobActive) {
		return errors.New("only active scheduled jobs require a next wake")
	}
	if j.NextWakeAt != nil && (j.NextWakeAt.IsZero() || j.NextWakeAt.Location() != time.UTC) {
		return errors.New("scheduled job next wake must be normalized to UTC")
	}
	if j.PendingOccurrenceAt != nil &&
		(j.PendingOccurrenceAt.IsZero() || j.PendingOccurrenceAt.Location() != time.UTC) {
		return errors.New("scheduled job pending occurrence must be normalized to UTC")
	}
	if (j.ActiveLeaseExpiresAt == nil) != (j.ActiveLeaseGeneration == 0) {
		return errors.New("scheduled job active lease projection is inconsistent")
	}
	if j.ActiveLeaseExpiresAt != nil && j.PendingOccurrenceAt == nil {
		return errors.New("scheduled job lease requires a pending occurrence")
	}
	if j.Status.Terminal() {
		if j.CompletedAt == nil || j.CompletedAt.Before(j.CreatedAt) ||
			j.StopReason == ScheduledJobStopNone || j.ActiveLeaseGeneration != 0 {
			return errors.New("terminal scheduled job requires completion and stop metadata")
		}
	} else if j.CompletedAt != nil || j.StopReason != ScheduledJobStopNone {
		return errors.New("non-terminal scheduled job cannot contain terminal metadata")
	}
	return nil
}

// ScheduledJobObservationConsent is an immutable per-job receipt. It records
// only consent to the versioned read-only, zero-model observation semantics;
// it does not grant model, tool, network, filesystem, or process authority.
type ScheduledJobObservationConsent struct {
	JobID              string    `json:"job_id"`
	RunID              string    `json:"run_id"`
	Version            int       `json:"version"`
	ConfirmedBy        string    `json:"confirmed_by"`
	ConfirmedAt        time.Time `json:"confirmed_at"`
	OperationKeySHA256 string    `json:"-"`
	RequestFingerprint string    `json:"-"`
}

func (c ScheduledJobObservationConsent) Validate() error {
	if c.Version != ScheduledJobObservationConsentVersion || c.ConfirmedAt.IsZero() ||
		c.ConfirmedAt.Location() != time.UTC || !validLowerHexDigest(c.OperationKeySHA256) ||
		!validLowerHexDigest(c.RequestFingerprint) {
		return errors.New("scheduled job observation consent version, time, or digest is invalid")
	}
	for _, value := range []string{c.JobID, c.RunID, c.ConfirmedBy} {
		if !ValidAgentID(value) || strings.ContainsRune(value, 0) {
			return errors.New("scheduled job observation consent identity is invalid")
		}
	}
	return nil
}

type ScheduledJobAuthorization struct {
	ProtocolVersion      string    `json:"protocol_version"`
	JobID                string    `json:"job_id"`
	RunID                string    `json:"run_id"`
	ModeSnapshotID       string    `json:"mode_snapshot_id"`
	ModeRevision         int64     `json:"mode_revision"`
	PermissionSnapshotID string    `json:"permission_snapshot_id"`
	PermissionRevision   int64     `json:"permission_revision"`
	AuthorizedBy         string    `json:"authorized_by"`
	AuthorizedAt         time.Time `json:"authorized_at"`
	ExpiresAt            time.Time `json:"expires_at"`
	ExecutionBypass      bool      `json:"execution_bypass"`
	NetworkBypass        bool      `json:"network_bypass"`
	ApprovalBypass       bool      `json:"approval_bypass"`
}

func (a ScheduledJobAuthorization) Validate() error {
	if a.ProtocolVersion != ScheduledJobAuthProtocolVersion || a.ModeRevision < 1 ||
		a.PermissionRevision < 1 || a.AuthorizedAt.IsZero() ||
		!a.ExpiresAt.After(a.AuthorizedAt) || a.ExecutionBypass || a.NetworkBypass ||
		a.ApprovalBypass {
		return errors.New("scheduled job repair authorization contract is invalid")
	}
	for _, value := range []string{a.JobID, a.RunID, a.ModeSnapshotID,
		a.PermissionSnapshotID, a.AuthorizedBy} {
		if !ValidAgentID(value) || strings.ContainsRune(value, 0) {
			return errors.New("scheduled job repair authorization identity is invalid")
		}
	}
	return nil
}

type ScheduledJobAction string

const (
	ScheduledJobCreate ScheduledJobAction = "create"
	ScheduledJobPause  ScheduledJobAction = "pause"
	ScheduledJobResume ScheduledJobAction = "resume"
	ScheduledJobCancel ScheduledJobAction = "cancel"
)

func (a ScheduledJobAction) Valid() bool {
	return a == ScheduledJobCreate || a == ScheduledJobPause ||
		a == ScheduledJobResume || a == ScheduledJobCancel
}

type ScheduledJobOperation struct {
	ProtocolVersion    string             `json:"protocol_version"`
	KeyDigest          string             `json:"-"`
	RequestFingerprint string             `json:"-"`
	Action             ScheduledJobAction `json:"action"`
	JobID              string             `json:"job_id"`
	RunID              string             `json:"run_id"`
	ExpectedRevision   int64              `json:"expected_revision"`
	RequestedBy        string             `json:"requested_by"`
	CreatedAt          time.Time          `json:"created_at"`
}

func (o ScheduledJobOperation) Validate() error {
	if o.ProtocolVersion != ScheduledJobControlProtocolVersion || !o.Action.Valid() ||
		o.ExpectedRevision < 0 || o.CreatedAt.IsZero() {
		return errors.New("scheduled job operation protocol, action, digest, revision, or time is invalid")
	}
	if _, err := o.ReplayIdentity(); err != nil {
		return errors.New("scheduled job operation protocol, action, digest, revision, or time is invalid")
	}
	for _, value := range []string{o.JobID, o.RunID, o.RequestedBy} {
		if !ValidAgentID(value) || strings.ContainsRune(value, 0) {
			return errors.New("scheduled job operation identity is invalid")
		}
	}
	return nil
}

// ReplayIdentity returns only the identity subset shared by the pilot. Job
// ownership, revision, authorization, scheduling, leases, rounds, and events
// remain domain-owned.
func (o ScheduledJobOperation) ReplayIdentity() (durableoperation.Identity, error) {
	if o.ProtocolVersion != ScheduledJobControlProtocolVersion {
		return durableoperation.Identity{}, errors.New("scheduled job operation protocol is invalid")
	}
	return durableoperation.NewIdentity(scheduledJobReplayDomain, o.KeyDigest,
		o.RequestFingerprint)
}

type ScheduledJobRoundStatus string

const (
	ScheduledJobRoundClaimed   ScheduledJobRoundStatus = "claimed"
	ScheduledJobRoundRetryWait ScheduledJobRoundStatus = "retry_wait"
	ScheduledJobRoundUnchanged ScheduledJobRoundStatus = "unchanged"
	ScheduledJobRoundCompleted ScheduledJobRoundStatus = "completed"
	ScheduledJobRoundFailed    ScheduledJobRoundStatus = "failed"
	ScheduledJobRoundSkipped   ScheduledJobRoundStatus = "skipped"
)

func (s ScheduledJobRoundStatus) Valid() bool {
	switch s {
	case ScheduledJobRoundClaimed, ScheduledJobRoundRetryWait,
		ScheduledJobRoundUnchanged, ScheduledJobRoundCompleted,
		ScheduledJobRoundFailed, ScheduledJobRoundSkipped:
		return true
	default:
		return false
	}

}

type ScheduledJobLease struct {
	JobID            string                    `json:"job_id"`
	OccurrenceAt     time.Time                 `json:"occurrence_at"`
	RoundOrdinal     int                       `json:"round_ordinal"`
	Attempt          int                       `json:"attempt"`
	Generation       int64                     `json:"generation"`
	OwnerID          string                    `json:"owner_id"`
	FenceToken       string                    `json:"-"`
	FenceTokenSHA256 string                    `json:"-"`
	AcquiredAt       time.Time                 `json:"acquired_at"`
	ExpiresAt        time.Time                 `json:"expires_at"`
	OperationKey     string                    `json:"operation_key"`
	ExecutionMode    ScheduledJobExecutionMode `json:"execution_mode"`
}

func (l ScheduledJobLease) Validate() error {
	for _, value := range []string{l.JobID, l.OwnerID, l.FenceToken, l.OperationKey} {
		if !ValidAgentID(value) || strings.ContainsRune(value, 0) {
			return errors.New("scheduled job lease identity is invalid")
		}
	}
	if !validLowerHexDigest(l.FenceTokenSHA256) || l.RoundOrdinal < 1 ||
		l.RoundOrdinal > MaxScheduledJobRounds || l.Attempt < 1 ||
		l.Attempt > MaxScheduledJobAttempts || l.Generation < 1 ||
		l.OccurrenceAt.IsZero() || l.OccurrenceAt.Location() != time.UTC ||
		l.AcquiredAt.IsZero() || !l.ExpiresAt.After(l.AcquiredAt) ||
		l.ExpiresAt.After(l.AcquiredAt.Add(ScheduledJobLeaseSeconds*time.Second)) ||
		!l.ExecutionMode.Valid() {
		return errors.New("scheduled job lease bounds are invalid")
	}
	return nil
}

type ScheduledJobRound struct {
	ProtocolVersion   string                  `json:"protocol_version"`
	JobID             string                  `json:"job_id"`
	OccurrenceAt      time.Time               `json:"occurrence_at"`
	Ordinal           int                     `json:"ordinal"`
	Attempt           int                     `json:"attempt"`
	ClaimGeneration   int64                   `json:"claim_generation"`
	Status            ScheduledJobRoundStatus `json:"status"`
	EventSequence     int64                   `json:"event_sequence"`
	ObservationSHA256 string                  `json:"observation_sha256,omitempty"`
	Changed           bool                    `json:"changed"`
	ModelCalled       bool                    `json:"model_called"`
	ToolCalled        bool                    `json:"tool_called"`
	Result            string                  `json:"result,omitempty"`
	ErrorCode         string                  `json:"error_code,omitempty"`
	StartedAt         time.Time               `json:"started_at"`
	CompletedAt       *time.Time              `json:"completed_at,omitempty"`
}

// ScheduledJobRoundOutcome contains only bounded, already-redacted observation
// metadata. Raw event payloads, prompts, terminal input, command arguments, and
// secrets never cross this storage contract.
type ScheduledJobRoundOutcome struct {
	EventSequence     int64
	ObservationSHA256 string
	TargetStatus      RunStatus
	TargetTerminal    bool
	Changed           bool
	ModelCalled       bool
	ToolCalled        bool
	Result            string
}

// ScheduledJobRoundFailure accounts for work that happened before an attempt
// failed. Model/tool facts are still charged even when the occurrence retries.
type ScheduledJobRoundFailure struct {
	ErrorCode   string
	Result      string
	ModelCalled bool
	ToolCalled  bool
}

func (f ScheduledJobRoundFailure) Validate() error {
	if err := validateScheduledJobCode(f.ErrorCode); err != nil || f.ErrorCode == "" {
		return errors.New("scheduled job failure code is invalid")
	}
	if err := validateScheduledJobSummary(f.Result, false); err != nil {
		return err
	}
	if f.ToolCalled && !f.ModelCalled {
		return errors.New("scheduled job failed tool usage requires a model handoff")
	}
	return nil
}

func (o ScheduledJobRoundOutcome) Validate() error {
	terminal := o.TargetStatus == RunCompleted || o.TargetStatus == RunFailed ||
		o.TargetStatus == RunCancelled
	if o.EventSequence < 0 || !validLowerHexDigest(o.ObservationSHA256) ||
		!ValidRunStatus(o.TargetStatus) || o.TargetTerminal != terminal ||
		(o.ToolCalled && !o.ModelCalled) {
		return errors.New("scheduled job observation outcome is invalid")
	}
	if !o.Changed && (o.ModelCalled || o.ToolCalled) {
		return errors.New("unchanged scheduled job observation cannot call a model or tool")
	}
	return validateScheduledJobSummary(o.Result, false)
}

func (r ScheduledJobRound) Validate() error {
	if r.ProtocolVersion != ScheduledJobRoundProtocolVersion ||
		!ValidAgentID(r.JobID) || !r.Status.Valid() || r.Ordinal < 1 ||
		r.Ordinal > MaxScheduledJobRounds || r.Attempt < 1 ||
		r.Attempt > MaxScheduledJobAttempts || r.ClaimGeneration < 1 ||
		r.EventSequence < 0 || r.OccurrenceAt.IsZero() ||
		r.OccurrenceAt.Location() != time.UTC || r.StartedAt.IsZero() {
		return errors.New("scheduled job round contract is invalid")
	}
	if r.ObservationSHA256 != "" && !validLowerHexDigest(r.ObservationSHA256) {
		return errors.New("scheduled job round observation digest is invalid")
	}
	if err := validateScheduledJobSummary(r.Result, true); err != nil {
		return err
	}
	if err := validateScheduledJobCode(r.ErrorCode); err != nil {
		return err
	}
	terminal := r.Status != ScheduledJobRoundClaimed && r.Status != ScheduledJobRoundRetryWait
	if terminal != (r.CompletedAt != nil) {
		return errors.New("scheduled job round completion metadata is inconsistent")
	}
	if r.Status == ScheduledJobRoundUnchanged && (r.Changed || r.ModelCalled || r.ToolCalled) {
		return errors.New("unchanged scheduled job round cannot call a model or tool")
	}
	if r.ToolCalled && !r.ModelCalled {
		return errors.New("scheduled job tool usage requires a model handoff")
	}
	return nil
}

type ScheduledJobNotification struct {
	ID        string    `json:"id"`
	JobID     string    `json:"job_id"`
	Kind      string    `json:"kind"`
	Summary   string    `json:"summary"`
	CreatedAt time.Time `json:"created_at"`
}

func (n ScheduledJobNotification) Validate() error {
	if !ValidAgentID(n.ID) || !ValidAgentID(n.JobID) || n.CreatedAt.IsZero() ||
		(n.Kind != "change" && n.Kind != "failure" && n.Kind != "recovery" &&
			n.Kind != "completed") {
		return errors.New("scheduled job notification identity, kind, or time is invalid")
	}
	return validateScheduledJobSummary(n.Summary, false)
}

func validateScheduledJobSummary(value string, optional bool) error {
	if value == "" && optional {
		return nil
	}
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) ||
		utf8.RuneCountInString(value) > MaxScheduledJobSummaryRunes ||
		strings.ContainsRune(value, 0) {
		return fmt.Errorf("scheduled job summary must contain between 1 and %d normalized characters",
			MaxScheduledJobSummaryRunes)
	}
	return nil
}

func validateScheduledJobCode(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 64 || value != strings.TrimSpace(value) || !utf8.ValidString(value) ||
		strings.ContainsRune(value, 0) {
		return errors.New("scheduled job error code is invalid")
	}
	for _, current := range []byte(value) {
		if (current >= 'a' && current <= 'z') || (current >= '0' && current <= '9') ||
			current == '_' || current == '-' || current == '.' {
			continue
		}
		return errors.New("scheduled job error code is invalid")
	}
	return nil
}
