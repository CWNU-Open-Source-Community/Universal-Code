package domain

import (
	"fmt"
	"strings"
	"time"
)

const (
	PRFollowupActive    = "active"
	PRFollowupPaused    = "paused"
	PRFollowupBlocked   = "blocked"
	PRFollowupReady     = "ready"
	PRFollowupCompleted = "completed"
	PRFollowupCancelled = "cancelled"
	PRFollowupExhausted = "exhausted"

	PRFollowupTurnQueued          = "queued"
	PRFollowupTurnRunning         = "running"
	PRFollowupTurnWaitingApproval = "waiting_approval"
	PRFollowupTurnCompleted       = "completed"
	PRFollowupTurnFailed          = "failed"
	PRFollowupTurnUnknown         = "unknown"
)

// PRFollowup is the public projection of one bounded subscription on a Thread.
type PRFollowup struct {
	ID                      string     `json:"id"`
	ThreadID                string     `json:"thread_id"`
	ConnectionID            string     `json:"connection_id"`
	Repository              string     `json:"repository"`
	WorkspaceID             string     `json:"workspace_id"`
	RootPathSHA256          string     `json:"root_path_sha256"`
	Branch                  string     `json:"branch"`
	PullRequest             int64      `json:"pull_request"`
	IntervalSeconds         int64      `json:"interval_seconds"`
	DeadlineAt              time.Time  `json:"deadline_at"`
	MaxTurns                int        `json:"max_turns"`
	IncludeExistingComments bool       `json:"include_existing_comments"`
	Status                  string     `json:"status"`
	Revision                int64      `json:"revision"`
	TurnsStarted            int        `json:"turns_started"`
	NextCheckAt             time.Time  `json:"next_check_at"`
	CreatedAt               time.Time  `json:"created_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
	LastCheckedAt           *time.Time `json:"last_checked_at,omitempty"`
	LastHeadSHA             string     `json:"last_head_sha,omitempty"`
	LastResult              string     `json:"last_result,omitempty"`
	LastError               string     `json:"last_error,omitempty"`
}

func (p PRFollowup) Terminal() bool {
	return p.Status == PRFollowupCompleted || p.Status == PRFollowupCancelled || p.Status == PRFollowupExhausted
}

func (p PRFollowup) Validate() error {
	for name, value := range map[string]string{
		"id": p.ID, "thread id": p.ThreadID, "connection id": p.ConnectionID,
		"repository": p.Repository, "workspace id": p.WorkspaceID,
		"root path SHA256": p.RootPathSHA256, "branch": p.Branch,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("PR followup %s is required", name)
		}
	}
	switch p.Status {
	case PRFollowupActive, PRFollowupPaused, PRFollowupBlocked, PRFollowupReady,
		PRFollowupCompleted, PRFollowupCancelled, PRFollowupExhausted:
	default:
		return fmt.Errorf("invalid PR followup status %q", p.Status)
	}
	if p.PullRequest < 1 || p.IntervalSeconds < 1 || p.MaxTurns < 1 || p.Revision < 1 ||
		p.TurnsStarted < 0 || p.TurnsStarted > p.MaxTurns {
		return fmt.Errorf("invalid PR followup limits or revision")
	}
	if p.CreatedAt.IsZero() || p.UpdatedAt.Before(p.CreatedAt) || p.NextCheckAt.IsZero() ||
		!p.DeadlineAt.After(p.CreatedAt) {
		return fmt.Errorf("invalid PR followup timestamps")
	}
	return nil
}

// PRFollowupTurn preserves the exact Thread submission across worker restarts.
// It is internal state; HTTP responses expose PRFollowup only.
type PRFollowupTurn struct {
	ID           string    `json:"id"`
	OperationKey string    `json:"operation_key"`
	Content      string    `json:"content"`
	RunID        string    `json:"run_id,omitempty"`
	MessageID    string    `json:"message_id,omitempty"`
	State        string    `json:"state"`
	Error        string    `json:"error,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Internal observations, turn bodies and lease credentials are never serialized
// with the public record. The store persists them separately.
type PRFollowupState struct {
	Record         PRFollowup        `json:"record"`
	Observed       map[string]string `json:"-"`
	Initialized    bool              `json:"-"`
	Pending        *PRFollowupTurn   `json:"-"`
	ReviewedTurnID string            `json:"-"`
	LeaseOwner     string            `json:"-"`
	LeaseToken     string            `json:"-"`
	LeaseUntil     time.Time         `json:"-"`
}
