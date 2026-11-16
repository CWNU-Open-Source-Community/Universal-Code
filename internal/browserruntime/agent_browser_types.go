package browserruntime

import (
	"context"
	"errors"
	"runtime"
	"time"
)

const AgentBrowserProtocolVersion = "agent-browser-runtime.v1"

var (
	ErrAgentBrowserStaleReference        = errors.New("browser reference is stale; obtain a new snapshot")
	ErrAgentBrowserClosed                = errors.New("agent browser session is closed")
	ErrAgentBrowserTargetChanged         = errors.New("browser target changed or is obscured; obtain a new snapshot")
	ErrAgentBrowserScreenshotUnavailable = errors.New("browser screenshot is unavailable; show the browser tab and retry, or use browser_snapshot for DOM inspection")
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

type AgentBrowserNetworkMode string

const (
	AgentBrowserNetworkOff        AgentBrowserNetworkMode = "off"
	AgentBrowserNetworkInternet   AgentBrowserNetworkMode = "internet"
	AgentBrowserNetworkRestricted AgentBrowserNetworkMode = "restricted"
)

type AgentBrowserStartRequest struct {
	HomePath        string
	Authority       AgentBrowserAuthority
	CheckAuthority  func(context.Context, AgentBrowserAuthority) error
	Product         BrowserProduct
	Headless        bool
	RuntimeDeadline time.Time
	// NetworkMode is granted independently from shell/filesystem permissions.
	// Internet permits network-accessible HTTP(S) destinations, including local
	// development sites; restricted confines requests to ScopedOrigins. Both
	// keep HTTP request-write review and grant no filesystem or command rights.
	// Off refuses launch. An absent mode preserves the legacy Full/scoped API.
	NetworkMode AgentBrowserNetworkMode
	// ScopedOrigins declares the operator-approved, exact origin set for a
	// restricted session, enforced for redirects and subresources as well.
	ScopedOrigins []string
	// ReviewNetworkWrite keeps one intercepted HTTP request paused until the
	// operator has approved its exact method, destination and complete body.
	// The callback must consume that decision once before calling dispatch.
	// WebSocket handshakes/messages are outside Fetch interception.
	ReviewNetworkWrite func(context.Context, AgentBrowserNetworkRequest, func() error) error
}

type AgentBrowserNetworkRequest struct {
	ID, RuntimeID, RequestID, FrameID, DocumentURL string
	DocumentEpoch                                  uint64
	Authority                                      AgentBrowserAuthority
	Method, URL, BodySHA256                        string
	BodyBytes                                      int
	ExpiresAt                                      time.Time
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
	Viewport      *AgentBrowserViewport `json:"viewport,omitempty"`
	ImageWidth    int                   `json:"image_width,omitempty"`
	ImageHeight   int                   `json:"image_height,omitempty"`
	SessionID     string                `json:"session_id"`
	CanonicalURL  string                `json:"canonical_url"`
	DocumentEpoch uint64                `json:"document_epoch"`
	MediaType     string                `json:"media_type"`
	SHA256        string                `json:"sha256"`
	Bytes         int                   `json:"bytes"`
	PNG           []byte                `json:"-"`
	CompletedAt   time.Time             `json:"completed_at"`
}

type AgentBrowserCleanup struct {
	AgentDetached        bool   `json:"agent_detached,omitempty"`
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

// Complete distinguishes releasing a borrowed page from destroying an owned
// browser. A detached native page deliberately retains its process and profile.
func (c AgentBrowserCleanup) Complete() bool {
	if c.AgentDetached {
		return !c.CleanupPending && !c.TreeReaped && !c.ProfileRemoved && c.ProcessScope == "" && !c.ProcessScopeVerified
	}
	return !c.CleanupPending && c.ProcessesStopped() && c.ProfileRemoved
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
