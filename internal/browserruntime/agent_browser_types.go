package browserruntime

import (
	"context"
	"errors"
	"runtime"
	"time"
)

const AgentBrowserProtocolVersion = "agent-browser-runtime.v1"

var (
	ErrAgentBrowserStaleReference = errors.New("browser reference is stale; obtain a new snapshot")
	ErrAgentBrowserClosed         = errors.New("agent browser session is closed")
	ErrAgentBrowserTargetChanged  = errors.New("browser target changed or is obscured; obtain a new snapshot")
)

// AgentBrowserAuthority is supplied by the trusted application composition root,
// never by a renderer or model. CheckAuthority must validate its live generation
// and fence against the current application authority, including during actions.
type AgentBrowserAuthority struct {
	RunID                 string
	ManagerBootID         string
	SessionID             string
	Generation            uint64
	PermissionSnapshotID  string
	PermissionRevision    int64
	PermissionActivation  uint64
	RunAuthorizationFence uint64
	PermissionMode        string
}

type AgentBrowserStartRequest struct {
	HomePath        string
	Authority       AgentBrowserAuthority
	CheckAuthority  func(context.Context, AgentBrowserAuthority) error
	Product         BrowserProduct
	Headless        bool
	RuntimeDeadline time.Time
}

type AgentBrowserStatus struct {
	Version       string         `json:"version"`
	SessionID     string         `json:"session_id"`
	Generation    uint64         `json:"generation"`
	Product       BrowserProduct `json:"product"`
	State         string         `json:"state"`
	CanonicalURL  string         `json:"canonical_url"`
	DocumentEpoch uint64         `json:"document_epoch"`
	// Unsupported features are explicit; adapters must not advertise them.
	SupportedActions []string `json:"supported_actions"`
	// Downloads are bounded quarantine receipts for bytes the page pushed at
	// the browser. They never carry a path and never reach tool results.
	Downloads []AgentBrowserDownloadReceipt `json:"downloads,omitempty"`
}

type AgentBrowserNavigation struct {
	SessionID     string                `json:"session_id"`
	CanonicalURL  string                `json:"canonical_url"`
	DocumentEpoch uint64                `json:"document_epoch"`
	CompletedAt   time.Time             `json:"completed_at"`
	Viewport      *AgentBrowserViewport `json:"viewport,omitempty"`
}

type AgentBrowserElement struct {
	Ref      string `json:"ref"`
	Role     string `json:"role"`
	Name     string `json:"name"`
	Tag      string `json:"tag"`
	Type     string `json:"type,omitempty"`
	Disabled bool   `json:"disabled"`
}

type AgentBrowserSnapshot struct {
	Version           string                `json:"version"`
	SessionID         string                `json:"session_id"`
	SnapshotID        string                `json:"snapshot_id"`
	CanonicalURL      string                `json:"canonical_url"`
	Title             string                `json:"title"`
	DocumentEpoch     uint64                `json:"document_epoch"`
	Text              string                `json:"text"`
	Elements          []AgentBrowserElement `json:"elements"`
	Layout            *AgentBrowserLayout   `json:"layout,omitempty"`
	Truncated         bool                  `json:"truncated"`
	UntrustedEvidence bool                  `json:"untrusted_evidence"`
	FramesSupported   bool                  `json:"frames_supported"`
	CompletedAt       time.Time             `json:"completed_at"`
}

type AgentBrowserInteraction struct {
	SessionID     string `json:"session_id"`
	CanonicalURL  string `json:"canonical_url"`
	DocumentEpoch uint64 `json:"document_epoch"`
	Action        string `json:"action"`
	Dispatched    bool   `json:"dispatched"`
	// Dispatched means the browser accepted input, not that an external server
	// committed a transaction or that a resulting navigation has finished.
	CompletedAt time.Time `json:"completed_at"`
}

type AgentBrowserScreenshot struct {
	SessionID     string    `json:"session_id"`
	CanonicalURL  string    `json:"canonical_url"`
	DocumentEpoch uint64    `json:"document_epoch"`
	MediaType     string    `json:"media_type"`
	SHA256        string    `json:"sha256"`
	Bytes         int       `json:"bytes"`
	PNG           []byte    `json:"-"`
	CompletedAt   time.Time `json:"completed_at"`
}

type AgentBrowserCleanup struct {
	SessionID            string `json:"session_id"`
	TreeReaped           bool   `json:"tree_reaped"`
	ProfileRemoved       bool   `json:"profile_removed"`
	CleanupPending       bool   `json:"cleanup_pending"`
	ProcessScope         string `json:"process_scope,omitempty"`
	ProcessScopeVerified bool   `json:"process_scope_verified,omitempty"`
}

const DarwinAgentBrowserProcessScope = "darwin_group_and_scoped_children.v1"
const agentBrowserNoProcessScope = "no_owned_process_resources"

// AgentBrowserNoProcessCleanup requires no live or unreaped process resources.
// It must never substitute for missing evidence after a failed termination.
func AgentBrowserNoProcessCleanup(sessionID string) AgentBrowserCleanup {
	if runtime.GOOS == "darwin" {
		return AgentBrowserCleanup{SessionID: sessionID, ProfileRemoved: true, ProcessScope: agentBrowserNoProcessScope, ProcessScopeVerified: true}
	}
	return AgentBrowserCleanup{SessionID: sessionID, TreeReaped: true, ProfileRemoved: true}
}

// ProcessesStopped checks the actual adapter's scope. Darwin never supplies a
// Windows Job/tree claim; its receipt covers the group and attributed children.
func (c AgentBrowserCleanup) ProcessesStopped() bool {
	switch c.ProcessScope {
	case "":
		return c.TreeReaped && !c.ProcessScopeVerified
	case DarwinAgentBrowserProcessScope, agentBrowserNoProcessScope:
		return !c.TreeReaped && c.ProcessScopeVerified
	default:
		return false
	}
}

type agentBrowserOwnedProcess interface {
	PID() int
	Done() <-chan struct{}
	Exit() (BrowserProcessExit, bool)
	Stop(context.Context) error
}

type AgentBrowserActionError struct {
	Err            error
	Dispatched     bool
	OutcomeUnknown bool
}

func (e *AgentBrowserActionError) Error() string { return e.Err.Error() }
func (e *AgentBrowserActionError) Unwrap() error { return e.Err }

// Runtime API: Navigate, Snapshot, Click, Type, Screenshot, Scroll, Key,
// Status, Cancel, Close, Done. Cancel permanently invalidates this slot and
// initiates owned cleanup; it is never a pause/reusable action cancellation.
// Sensitive external effects require application-level confirmation before
// Click/Type/Key. This runtime does not infer a webpage's business semantics.

// AgentBrowserLaunchError preserves cleanup evidence when no live handle can
// be returned. Callers must bind Cleanup.SessionID to the attempted session.
type AgentBrowserLaunchError struct {
	Err     error
	Cleanup AgentBrowserCleanup
}

func (e *AgentBrowserLaunchError) Error() string { return e.Err.Error() }
func (e *AgentBrowserLaunchError) Unwrap() error { return e.Err }
