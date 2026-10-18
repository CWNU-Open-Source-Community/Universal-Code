package domain

import (
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
)

const ThreadContextCompactionRecoveryVersion = "thread_context_compaction_recovery.v1"
const ThreadContextCompactionOperationsVersion = "thread_context_compaction_operations.v1"

// The durable marker also fences late provider responses from installing a summary.
const ThreadContextCompactionDiscarded = "thread_context_discarded: operator stopped adopting this compaction; dispatched requests may still be charged"

type ThreadContextCompactionRecoveryRequest struct {
	Version       string `json:"version"`
	ThreadID      string `json:"-"`
	OperationID   string `json:"-"`
	ExpectedRunID string `json:"expected_run_id"`
	Action        string `json:"action"`
	RequestedBy   string `json:"-"`
}

func (r ThreadContextCompactionRecoveryRequest) Normalize() (ThreadContextCompactionRecoveryRequest, error) {
	r.ThreadID, r.OperationID = strings.TrimSpace(r.ThreadID), strings.TrimSpace(r.OperationID)
	r.ExpectedRunID, r.RequestedBy = strings.TrimSpace(r.ExpectedRunID), strings.TrimSpace(r.RequestedBy)
	if r.Version != ThreadContextCompactionRecoveryVersion || !ValidAgentID(r.ThreadID) ||
		!ValidAgentID(r.OperationID) || !ValidAgentID(r.ExpectedRunID) || !ValidAgentID(r.RequestedBy) ||
		(r.Action != "resume" && r.Action != "discard") {
		return r, apperror.New(apperror.CodeInvalidArgument, "Thread context recovery request is invalid")
	}
	return r, nil
}

// Deliberately excludes provider response text, request keys and source history.
type ThreadContextCompactionOperationView struct {
	OperationID  string                         `json:"operation_id"`
	RunID        string                         `json:"run_id"`
	SessionID    string                         `json:"session_id"`
	Status       string                         `json:"status"`
	SourceSHA256 string                         `json:"source_sha256"`
	Provider     string                         `json:"provider,omitempty"`
	Model        string                         `json:"model,omitempty"`
	CreatedAt    time.Time                      `json:"created_at"`
	StartedAt    *time.Time                     `json:"started_at,omitempty"`
	TerminalAt   *time.Time                     `json:"terminal_at,omitempty"`
	UsageKnown   bool                           `json:"usage_known"`
	ErrorMessage string                         `json:"error_message,omitempty"`
	Result       *ThreadContextCompactionResult `json:"result,omitempty"`
}

type ThreadContextCompactionOperations struct {
	Version    string                                 `json:"version"`
	ThreadID   string                                 `json:"thread_id"`
	Operations []ThreadContextCompactionOperationView `json:"operations"`
}

func (o ThreadContextCompactionOperation) View() ThreadContextCompactionOperationView {
	v := ThreadContextCompactionOperationView{OperationID: o.OperationID, RunID: o.RunID,
		SessionID: o.SessionID, Status: o.Status, SourceSHA256: o.SourceSHA256, Provider: o.Provider,
		Model: o.Model, CreatedAt: o.CreatedAt, StartedAt: o.StartedAt, TerminalAt: o.TerminalAt,
		UsageKnown: len(o.Usage) > 0 && string(o.Usage) != "null", ErrorMessage: o.ErrorMessage}
	if o.Status == "completed" || o.Status == "noop" {
		v.Result = &o.Result
	}
	return v
}
