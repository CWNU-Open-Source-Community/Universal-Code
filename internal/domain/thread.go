package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	ThreadProtocolVersion          = "thread.v1"
	ThreadCreationProtocolVersion  = "thread_creation.v1"
	ThreadMessageProtocolVersion   = "thread_message_submission.v1"
	ThreadLifecycleProtocolVersion = "thread_lifecycle.v1"
	ThreadExportProtocolVersion    = "thread_export.v1"
)

type ThreadStatus string

const (
	ThreadActive   ThreadStatus = "active"
	ThreadArchived ThreadStatus = "archived"
	ThreadDeleted  ThreadStatus = "deleted"
)

type ThreadLifecycleAction string

const (
	ThreadArchive ThreadLifecycleAction = "archive"
	ThreadRestore ThreadLifecycleAction = "restore"
	ThreadDelete  ThreadLifecycleAction = "delete"
)

func ValidThreadStatus(status ThreadStatus) bool {
	switch status {
	case ThreadActive, ThreadArchived, ThreadDeleted:
		return true
	default:
		return false
	}
}

// Thread is the stable, user-facing task identity. A Run is one finite
// execution attempt within a Thread; a Session is the Run-local conversation
// and authority boundary. ActiveRunID is empty between a terminal Run and the
// atomically-created successor.
type Thread struct {
	ProjectWorkspaceID       string       `json:"project_workspace_id"`
	ProjectAssignmentVersion int64        `json:"project_assignment_version"`
	ID                       string       `json:"id"`
	ProtocolVersion          string       `json:"protocol_version"`
	WorkspaceID              string       `json:"workspace_id,omitempty"`
	MissionID                string       `json:"mission_id"`
	Title                    string       `json:"title"`
	Pinned                   bool         `json:"pinned,omitempty"`
	Status                   ThreadStatus `json:"status"`
	ActiveRunID              string       `json:"active_run_id,omitempty"`
	LastRunID                string       `json:"last_run_id"`
	Version                  int64        `json:"version"`
	CreatedAt                time.Time    `json:"created_at"`
	UpdatedAt                time.Time    `json:"updated_at"`
	ArchivedAt               *time.Time   `json:"archived_at,omitempty"`
	DeletedAt                *time.Time   `json:"deleted_at,omitempty"`
}

func (t Thread) Validate() error {
	for label, value := range map[string]string{
		"id": t.ID, "mission id": t.MissionID, "title": t.Title,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("thread %s is required", label)
		}
	}
	if t.ProtocolVersion != ThreadProtocolVersion {
		return fmt.Errorf("unsupported thread protocol %q", t.ProtocolVersion)
	}
	if !ValidThreadStatus(t.Status) {
		return fmt.Errorf("invalid thread status %q", t.Status)
	}
	if strings.TrimSpace(t.LastRunID) == "" {
		return errors.New("thread last Run id is required")
	}
	if t.Version <= 0 {
		return errors.New("thread version must be positive")
	}
	if t.CreatedAt.IsZero() || t.UpdatedAt.IsZero() {
		return errors.New("thread timestamps are required")
	}
	if t.Status == ThreadArchived && t.ArchivedAt == nil {
		return errors.New("archived thread requires archived_at")
	}
	if t.Status == ThreadDeleted && t.DeletedAt == nil {
		return errors.New("deleted thread requires deleted_at")
	}
	return nil
}

func (t Thread) CanAcceptMessages() bool {
	return t.Status == ThreadActive
}

// InitialThreadID is deterministic so upgraded data and newly-created data use
// the same projection without an auxiliary identity allocation protocol.
func InitialThreadID(runID string) string {
	return "thread-" + strings.TrimSpace(runID)
}

type ThreadRun struct {
	ThreadID         string    `json:"thread_id"`
	RunID            string    `json:"run_id"`
	SessionID        string    `json:"session_id"`
	Ordinal          int64     `json:"ordinal"`
	PredecessorRunID string    `json:"predecessor_run_id,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

func (binding ThreadRun) Validate() error {
	if strings.TrimSpace(binding.ThreadID) == "" || strings.TrimSpace(binding.RunID) == "" ||
		strings.TrimSpace(binding.SessionID) == "" {
		return errors.New("thread Run identities are required")
	}
	if binding.Ordinal <= 0 {
		return errors.New("thread Run ordinal must be positive")
	}
	if binding.Ordinal == 1 && binding.PredecessorRunID != "" {
		return errors.New("initial thread Run cannot have a predecessor")
	}
	if binding.Ordinal > 1 && strings.TrimSpace(binding.PredecessorRunID) == "" {
		return errors.New("successor thread Run requires a predecessor")
	}
	if binding.CreatedAt.IsZero() {
		return errors.New("thread Run created_at is required")
	}
	return nil
}

type ThreadEvent struct {
	ID          int64     `json:"id"`
	ThreadID    string    `json:"thread_id"`
	RunID       string    `json:"run_id,omitempty"`
	Type        string    `json:"type"`
	Source      string    `json:"source"`
	PayloadJSON string    `json:"payload_json"`
	CreatedAt   time.Time `json:"created_at"`
}

type ThreadMessage struct {
	ID                    string    `json:"id"`
	ThreadID              string    `json:"thread_id"`
	RunID                 string    `json:"run_id"`
	SessionID             string    `json:"session_id"`
	Role                  string    `json:"role"`
	Content               string    `json:"content"`
	ProvenanceVersion     string    `json:"provenance_version"`
	SourceKind            string    `json:"source_kind"`
	SourceRef             string    `json:"source_ref,omitempty"`
	ContentSHA256         string    `json:"content_sha256"`
	InstructionAuthorized bool      `json:"instruction_authorized"`
	Status                string    `json:"status"`
	TokenEstimate         int       `json:"token_estimate"`
	Compacted             bool      `json:"compacted"`
	CreatedAt             time.Time `json:"created_at"`
}

// ThreadSession mirrors the durable Run-local Session identity in an export
// without introducing a domain-to-session package dependency.
type ThreadSession struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id,omitempty"`
	Title       string    `json:"title"`
	Route       string    `json:"route"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ThreadRunAuditEvent is the lossless Run audit projection included in a
// Thread export. It deliberately mirrors durable run_events without importing
// the events package back into domain.
type ThreadRunAuditEvent struct {
	EventID     string    `json:"event_id"`
	Version     string    `json:"version"`
	RunID       string    `json:"run_id"`
	MissionID   string    `json:"mission_id"`
	Sequence    int64     `json:"sequence"`
	Type        string    `json:"type"`
	Source      string    `json:"source"`
	SubjectID   string    `json:"subject_id,omitempty"`
	PayloadJSON string    `json:"payload_json"`
	CreatedAt   time.Time `json:"created_at"`
}

type ThreadFilter struct {
	Status         ThreadStatus
	Pinned         *bool
	IncludeDeleted bool
	TitleQuery     string
	Limit          int
}

const MaxThreadTitleQueryRunes = 256

// NormalizeThreadTitleQuery defines literal title-substring matching. SQLite's
// lower() folds ASCII; other Unicode characters retain their exact spelling.
func NormalizeThreadTitleQuery(query string) (string, error) {
	if !utf8.ValidString(query) {
		return "", errors.New("Thread title query must be valid UTF-8")
	}
	query = strings.TrimSpace(query)
	if utf8.RuneCountInString(query) > MaxThreadTitleQueryRunes {
		return "", fmt.Errorf("Thread title query must be at most %d characters", MaxThreadTitleQueryRunes)
	}
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, query), nil
}

// ThreadExecutionFacts is a private read projection. An unsettled durable
// boundary prevents a process-local observer from claiming execution is idle.
type ThreadExecutionFacts struct {
	RunID           string
	RunStatus       RunStatus
	PendingApproval bool
	Unsettled       bool
}

type ThreadExport struct {
	Missions        []Mission             `json:"missions"`
	ProtocolVersion string                `json:"protocol_version"`
	ExportedAt      time.Time             `json:"exported_at"`
	Thread          Thread                `json:"thread"`
	Mission         Mission               `json:"mission"`
	Bindings        []ThreadRun           `json:"bindings"`
	Runs            []Run                 `json:"runs"`
	Sessions        []ThreadSession       `json:"sessions"`
	Messages        []ThreadMessage       `json:"messages"`
	Events          []ThreadEvent         `json:"events"`
	AuditEvents     []ThreadRunAuditEvent `json:"audit_events"`
}
