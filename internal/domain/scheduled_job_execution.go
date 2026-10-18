package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// The target is explicit instruction consent, never a replacement for the
// target Run's current permission and approval checks.
type ScheduledJobExecutionTarget struct {
	Kind                 string          `json:"kind"`
	ThreadID             string          `json:"thread_id,omitempty"`
	Prompt               string          `json:"prompt,omitempty"`
	WorkflowDefinitionID string          `json:"workflow_definition_id,omitempty"`
	Inputs               json.RawMessage `json:"inputs,omitempty"`
}

func (t ScheduledJobExecutionTarget) Validate() error {
	switch t.Kind {
	case "thread":
		if !ValidAgentID(t.ThreadID) || strings.ContainsRune(t.ThreadID, 0) || t.WorkflowDefinitionID != "" || len(t.Inputs) != 0 || strings.TrimSpace(t.Prompt) != t.Prompt || t.Prompt == "" || !utf8.ValidString(t.Prompt) || strings.ContainsRune(t.Prompt, 0) || len(t.Prompt) > 16000 {
			return errors.New("automatic Thread target requires a task and a prompt of at most 16000 bytes")
		}
	case "workflow":
		if !ValidAgentID(t.WorkflowDefinitionID) || strings.ContainsRune(t.WorkflowDefinitionID, 0) || t.ThreadID != "" || t.Prompt != "" || len(t.Inputs) > 16000 || (len(t.Inputs) > 0 && !json.Valid(t.Inputs)) {
			return errors.New("automatic workflow target requires a saved definition and valid bounded JSON inputs")
		}
	default:
		return errors.New("automatic target kind must be thread or workflow")
	}
	return nil
}

// Stored separately from observation consent; old observation receipts never
// satisfy the execution worker's selection or admission requirements.
type ScheduledJobExecutionConsent struct {
	JobID          string
	RunID          string
	Version        int
	ConfirmedBy    string
	ConfirmedAt    time.Time
	WorkspaceID    string
	RootPathSHA256 string
	Branch         string
}

func (c ScheduledJobExecutionConsent) Validate() error {
	if c.Version != ScheduledJobExecutionConsentVersion || c.ConfirmedAt.IsZero() || c.ConfirmedAt.Location() != time.UTC || !validLowerHexDigest(c.RootPathSHA256) {
		return errors.New("automatic execution consent version, time, or workspace is invalid")
	}
	for _, v := range []string{c.JobID, c.RunID, c.ConfirmedBy, c.WorkspaceID} {
		if !ValidAgentID(v) || strings.ContainsRune(v, 0) {
			return errors.New("automatic execution consent identity is invalid")
		}
	}
	return nil
}

// ScheduledJobExecution keeps the original submission identity across retries
// and restarts. Waiting receipts are observed instead of submitted again.
type ScheduledJobExecution struct {
	JobID               string    `json:"job_id"`
	OccurrenceAt        time.Time `json:"occurrence_at"`
	OperationKey        string    `json:"-"`
	RunID               string    `json:"run_id,omitempty"`
	MessageID           string    `json:"message_id,omitempty"`
	WorkflowExecutionID string    `json:"workflow_execution_id,omitempty"`
	State               string    `json:"state"`
	Result              string    `json:"result,omitempty"`
	ErrorCode           string    `json:"error_code,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

func (e ScheduledJobExecution) Validate() error {
	if !ValidAgentID(e.JobID) || !ValidAgentID(e.OperationKey) || e.OccurrenceAt.IsZero() || e.CreatedAt.IsZero() || e.UpdatedAt.Before(e.CreatedAt) {
		return errors.New("automatic execution receipt identity or time is invalid")
	}
	switch e.State {
	case "queued", "running", "waiting_approval", "completed", "failed", "blocked":
	default:
		return errors.New("automatic execution receipt state is invalid")
	}
	if err := validateScheduledJobSummary(e.Result, true); err != nil {
		return err
	}
	return validateScheduledJobCode(e.ErrorCode)
}
