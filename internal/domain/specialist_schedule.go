package domain

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const MaxSpecialistScheduleRounds = 32

type SpecialistScheduleStatus string

const (
	SpecialistScheduleRunning   SpecialistScheduleStatus = "running"
	SpecialistScheduleCompleted SpecialistScheduleStatus = "completed"
	SpecialistScheduleFailed    SpecialistScheduleStatus = "failed"
	SpecialistScheduleCancelled SpecialistScheduleStatus = "cancelled"
	SpecialistScheduleAbandoned SpecialistScheduleStatus = "abandoned"
)

func (s SpecialistScheduleStatus) Valid() bool {
	switch s {
	case SpecialistScheduleRunning, SpecialistScheduleCompleted, SpecialistScheduleFailed,
		SpecialistScheduleCancelled, SpecialistScheduleAbandoned:
		return true
	default:
		return false
	}
}

func (s SpecialistScheduleStatus) Terminal() bool {
	return s.Valid() && s != SpecialistScheduleRunning
}

type SpecialistScheduleStart struct {
	ID                 string
	RunID              string
	AgentIDs           []string
	MaxRounds          int
	OperatorRequestID  string
	ContinuationDigest string
	Lease              RunExecutionLease
	UsageBefore        RunAgentUsage
	StartedAt          time.Time
}

func (s SpecialistScheduleStart) Normalize() (SpecialistScheduleStart, error) {
	s.ID = strings.TrimSpace(s.ID)
	s.RunID = strings.TrimSpace(s.RunID)
	if err := validateSpecialistScheduleIdentity(s.ID, "id"); err != nil {
		return SpecialistScheduleStart{}, err
	}
	if err := validateSpecialistScheduleIdentity(s.RunID, "Run id"); err != nil {
		return SpecialistScheduleStart{}, err
	}
	agentIDs, err := normalizeSpecialistScheduleAgentIDs(s.AgentIDs)
	if err != nil {
		return SpecialistScheduleStart{}, err
	}
	s.AgentIDs = agentIDs
	s.OperatorRequestID = strings.TrimSpace(s.OperatorRequestID)
	if s.ContinuationDigest != "" && (len(s.ContinuationDigest) != 64 || strings.Trim(s.ContinuationDigest, "0123456789abcdef") != "") {
		return SpecialistScheduleStart{}, errors.New("specialist continuation digest is invalid")
	}
	if s.ContinuationDigest != "" && s.OperatorRequestID != "" {
		return SpecialistScheduleStart{}, errors.New("specialist schedule cannot combine delegation and continuation request identities")
	}
	if s.OperatorRequestID != "" {
		if err := validateSpecialistScheduleIdentity(s.OperatorRequestID,
			"operator request id"); err != nil {
			return SpecialistScheduleStart{}, err
		}
	}
	if s.MaxRounds <= 0 || s.MaxRounds > MaxSpecialistScheduleRounds {
		return SpecialistScheduleStart{}, fmt.Errorf("specialist schedule rounds must be between 1 and %d",
			MaxSpecialistScheduleRounds)
	}
	if err := s.Lease.Validate(); err != nil || s.Lease.Status != RunExecutionLeaseActive ||
		s.Lease.RunID != s.RunID {
		return SpecialistScheduleStart{}, errors.New("specialist schedule requires its active Run execution lease")
	}
	if err := s.UsageBefore.Validate(); err != nil || s.UsageBefore.RunID != s.RunID {
		return SpecialistScheduleStart{}, errors.New("specialist schedule usage snapshot is invalid")
	}
	if s.StartedAt.IsZero() {
		s.StartedAt = time.Now().UTC()
	} else {
		s.StartedAt = s.StartedAt.UTC()
	}
	return s, nil
}

type SpecialistScheduleFinish struct {
	ID                string
	Lease             RunExecutionLease
	Status            SpecialistScheduleStatus
	StopReason        string
	RoundsCompleted   int
	TurnsStarted      int
	RecoveredAttempts int
	UsageAfter        RunAgentUsage
	ErrorCode         string
	FinishedAt        time.Time
}

func (f SpecialistScheduleFinish) Normalize() (SpecialistScheduleFinish, error) {
	f.ID = strings.TrimSpace(f.ID)
	f.StopReason = strings.TrimSpace(f.StopReason)
	f.ErrorCode = strings.TrimSpace(f.ErrorCode)
	if err := validateSpecialistScheduleIdentity(f.ID, "id"); err != nil {
		return SpecialistScheduleFinish{}, err
	}
	if !f.Status.Terminal() {
		return SpecialistScheduleFinish{}, errors.New("specialist schedule finish requires a terminal status")
	}
	if err := validateModelCancellationText(f.StopReason, 64, false); err != nil {
		return SpecialistScheduleFinish{}, fmt.Errorf("specialist schedule stop reason is invalid: %w", err)
	}
	if f.ErrorCode != "" {
		if err := validateModelCancellationText(f.ErrorCode, 64, false); err != nil {
			return SpecialistScheduleFinish{}, fmt.Errorf("specialist schedule error code is invalid: %w", err)
		}
	}
	if f.RoundsCompleted < 0 || f.RoundsCompleted > MaxSpecialistScheduleRounds ||
		f.TurnsStarted < 0 || f.RecoveredAttempts < 0 {
		return SpecialistScheduleFinish{}, errors.New("specialist schedule counters are invalid")
	}
	if err := f.Lease.Validate(); err != nil || f.Lease.Status != RunExecutionLeaseActive ||
		f.Lease.RunID != f.UsageAfter.RunID {
		return SpecialistScheduleFinish{}, errors.New("specialist schedule finish requires its active Run lease")
	}
	if err := f.UsageAfter.Validate(); err != nil {
		return SpecialistScheduleFinish{}, errors.New("specialist schedule final usage snapshot is invalid")
	}
	if f.FinishedAt.IsZero() {
		f.FinishedAt = time.Now().UTC()
	} else {
		f.FinishedAt = f.FinishedAt.UTC()
	}
	return f, nil
}

type SpecialistSchedule struct {
	ID                 string
	ContinuationDigest string
	RunID              string
	AgentIDs           []string
	MaxRounds          int
	Status             SpecialistScheduleStatus
	StopReason         string
	RoundsCompleted    int
	TurnsStarted       int
	RecoveredAttempts  int
	UsageBefore        RunAgentUsage
	UsageAfter         RunAgentUsage
	ErrorCode          string
	StartedAt          time.Time
	FinishedAt         *time.Time
}

func (s SpecialistSchedule) Validate() error {
	if err := validateSpecialistScheduleIdentity(s.ID, "id"); err != nil {
		return err
	}
	if err := validateSpecialistScheduleIdentity(s.RunID, "Run id"); err != nil {
		return err
	}
	normalized, err := normalizeSpecialistScheduleAgentIDs(s.AgentIDs)
	if err != nil {
		return err
	}
	if len(normalized) != len(s.AgentIDs) {
		return errors.New("specialist schedule Agent identities are invalid")
	}
	for index := range normalized {
		if normalized[index] != s.AgentIDs[index] {
			return errors.New("specialist schedule Agent identities must be sorted")
		}
	}
	if s.MaxRounds <= 0 || s.MaxRounds > MaxSpecialistScheduleRounds || !s.Status.Valid() ||
		s.StartedAt.IsZero() || s.RoundsCompleted < 0 || s.RoundsCompleted > s.MaxRounds ||
		s.TurnsStarted < 0 || s.RecoveredAttempts < 0 {
		return errors.New("specialist schedule metadata is invalid")
	}
	if err := s.UsageBefore.Validate(); err != nil || s.UsageBefore.RunID != s.RunID {
		return errors.New("specialist schedule initial usage is invalid")
	}
	if err := s.UsageAfter.Validate(); err != nil || s.UsageAfter.RunID != s.RunID {
		return errors.New("specialist schedule final usage is invalid")
	}
	if s.Status == SpecialistScheduleRunning {
		if s.FinishedAt != nil || s.StopReason != "" || s.ErrorCode != "" ||
			s.RoundsCompleted != 0 || s.TurnsStarted != 0 || s.RecoveredAttempts != 0 ||
			s.UsageAfter != s.UsageBefore {
			return errors.New("running Specialist schedule contains terminal summary data")
		}
		return nil
	}
	if s.FinishedAt == nil || s.FinishedAt.IsZero() || s.FinishedAt.Before(s.StartedAt) {
		return errors.New("terminal Specialist schedule requires a consistent finish time")
	}
	if err := validateModelCancellationText(s.StopReason, 64, false); err != nil {
		return errors.New("terminal Specialist schedule requires a bounded stop reason")
	}
	if s.ErrorCode != "" {
		if err := validateModelCancellationText(s.ErrorCode, 64, false); err != nil {
			return errors.New("specialist schedule error code is invalid")
		}
	}
	return nil
}

type SpecialistScheduleStartResult struct {
	Schedule          SpecialistSchedule
	RecoveredSchedule bool
}

func normalizeSpecialistScheduleAgentIDs(values []string) ([]string, error) {
	if len(values) == 0 || len(values) > MaxAgentChildren {
		return nil, fmt.Errorf("specialist schedule requires between 1 and %d child Agents", MaxAgentChildren)
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if err := validateSpecialistScheduleIdentity(value, "Agent id"); err != nil {
			return nil, err
		}
		if _, exists := seen[value]; exists {
			return nil, errors.New("specialist schedule Agent ids must be unique")
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func validateSpecialistScheduleIdentity(value string, label string) error {
	if err := validateModelCancellationText(value, MaxModelCancellationIdentityRunes, false); err != nil {
		return fmt.Errorf("specialist schedule %s is invalid: %w", label, err)
	}
	return nil
}
