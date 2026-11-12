package desktop

import (
	"context"
	"time"

	"cyberagent-workbench/internal/apperror"
)

const SharedBrowserProtocolVersion = "desktop_shared_browser.v1"

// SharedBrowserController belongs to the desktop host. The browser content
// never receives these bindings or the application's connection tokens.
type SharedBrowserController interface {
	Open(context.Context, SharedBrowserOpenRequest) (SharedBrowserState, error)
	Get(context.Context, SharedBrowserGetRequest) (SharedBrowserState, error)
	Command(context.Context, SharedBrowserCommandRequest) (SharedBrowserState, error)
	SetSurface(context.Context, SharedBrowserSurfaceRequest) (SharedBrowserSurfaceResult, error)
}

type SharedBrowserOpenRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	WorkspaceID     string `json:"workspace_id"`
	ThreadID        string `json:"thread_id,omitempty"`
	RunID           string `json:"run_id,omitempty"`
	AdoptSessionID  string `json:"adopt_session_id,omitempty"`
}

type SharedBrowserGetRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	SessionID       string `json:"session_id"`
}

type SharedBrowserCommandRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	SessionID       string `json:"session_id"`
	Command         string `json:"command"`
	TabID           string `json:"tab_id,omitempty"`
	URL             string `json:"url,omitempty"`
	OperationKey    string `json:"operation_key"`
}

type SharedBrowserState struct {
	ProtocolVersion string                      `json:"protocol_version"`
	SessionID       string                      `json:"session_id"`
	WorkspaceID     string                      `json:"workspace_id"`
	ThreadID        string                      `json:"thread_id,omitempty"`
	RunID           string                      `json:"run_id,omitempty"`
	Revision        int64                       `json:"revision"`
	State           string                      `json:"state"`
	ControlOwner    string                      `json:"control_owner"`
	Fullscreen      bool                        `json:"fullscreen"`
	ActiveTabID     string                      `json:"active_tab_id"`
	Tabs            []SharedBrowserTab          `json:"tabs"`
	History         []SharedBrowserHistoryEntry `json:"history,omitempty"`
	Error           string                      `json:"error,omitempty"`
}

type SharedBrowserHistoryEntry struct {
	URL       string    `json:"url"`
	Title     string    `json:"title"`
	VisitedAt time.Time `json:"visited_at"`
}

type SharedBrowserTab struct {
	TabID        string `json:"tab_id"`
	URL          string `json:"url"`
	Title        string `json:"title"`
	Loading      bool   `json:"loading"`
	CanGoBack    bool   `json:"can_go_back"`
	CanGoForward bool   `json:"can_go_forward"`
}

type SharedBrowserBounds struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type SharedBrowserSurfaceRequest struct {
	ProtocolVersion   string              `json:"protocol_version"`
	SessionID         string              `json:"session_id"`
	TabID             string              `json:"tab_id"`
	Bounds            SharedBrowserBounds `json:"bounds"`
	Visible           bool                `json:"visible"`
	LayoutRevision    int64               `json:"layout_revision"`
	DeviceScaleFactor float64             `json:"device_scale_factor"`
}

type SharedBrowserSurfaceResult struct {
	ProtocolVersion string `json:"protocol_version"`
	SessionID       string `json:"session_id"`
	LayoutRevision  int64  `json:"layout_revision"`
	Visible         bool   `json:"visible"`
}

func (b *DesktopBridge) sharedBrowserContext(protocol string) (context.Context, error) {
	if b == nil || b.sharedBrowser == nil || b.contextProvider == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "native shared browser is unavailable")
	}
	if protocol != SharedBrowserProtocolVersion {
		return nil, apperror.New(apperror.CodeInvalidArgument, "unsupported shared browser protocol")
	}
	return b.lifecycleContext()
}

func (b *DesktopBridge) OpenSharedBrowser(request SharedBrowserOpenRequest) (SharedBrowserState, error) {
	ctx, err := b.sharedBrowserContext(request.ProtocolVersion)
	if err != nil {
		return SharedBrowserState{}, err
	}
	return b.sharedBrowser.Open(ctx, request)
}

func (b *DesktopBridge) GetSharedBrowser(request SharedBrowserGetRequest) (SharedBrowserState, error) {
	ctx, err := b.sharedBrowserContext(request.ProtocolVersion)
	if err != nil {
		return SharedBrowserState{}, err
	}
	return b.sharedBrowser.Get(ctx, request)
}

func (b *DesktopBridge) SharedBrowserCommand(request SharedBrowserCommandRequest) (SharedBrowserState, error) {
	ctx, err := b.sharedBrowserContext(request.ProtocolVersion)
	if err != nil {
		return SharedBrowserState{}, err
	}
	return b.sharedBrowser.Command(ctx, request)
}

func (b *DesktopBridge) SetSharedBrowserSurface(request SharedBrowserSurfaceRequest) (SharedBrowserSurfaceResult, error) {
	ctx, err := b.sharedBrowserContext(request.ProtocolVersion)
	if err != nil {
		return SharedBrowserSurfaceResult{}, err
	}
	return b.sharedBrowser.SetSurface(ctx, request)
}
