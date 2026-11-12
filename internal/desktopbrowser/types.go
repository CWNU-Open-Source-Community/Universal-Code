// Package desktopbrowser hosts native browser pages in the desktop window.
// External web pages never receive the Wails renderer's bindings or asset server.
package desktopbrowser

import (
	"context"
	"errors"
	"time"
)

var (
	ErrUnsupported = errors.New("native shared browser is supported only on Windows")
	ErrClosed      = errors.New("native browser is closed")
)

// HostOptions describes one browser session. Give each isolated session its own
// persistent absolute UserDataDir. ParentHWND must belong to Dispatcher.ThreadID.
type HostOptions struct {
	ParentHWND  uintptr
	Dispatcher  *Dispatcher
	UserDataDir string
	OnEvent     func(Event)
}

type TabOptions struct{ ID, URL string }

// Bounds are physical pixels in the parent window's client area. The desktop
// bridge converts renderer CSS pixels using the current monitor's DPI.
type Bounds struct{ X, Y, Width, Height int32 }

type State struct {
	ID           string `json:"tab_id"`
	URL          string `json:"url"`
	Title        string `json:"title"`
	Loading      bool   `json:"loading"`
	CanGoBack    bool   `json:"can_go_back"`
	CanGoForward bool   `json:"can_go_forward"`
	Closed       bool   `json:"closed"`
	Error        string `json:"error,omitempty"`
}

// Event callbacks run serially outside the UI thread. State always contains a
// complete snapshot. Kinds are state, closed, popup, fullscreen, and user_input.
type Event struct {
	Kind       string
	State      State
	URL        string
	Fullscreen bool
}

type CDPOptions struct {
	Events []string
	// Dispatch runs on the UI thread immediately before the COM call. The
	// application's lease must hold its control lock across the supplied call,
	// so cancellation cannot race with a queued native mutation.
	Dispatch func(context.Context, func() error) error
}

type NetworkRequest struct {
	ID, FrameID, Method, URL string
	Body                     []byte
	BodyComplete             bool
}

// NetworkPolicy covers every page/worker request in a Host while an explicit
// control lease is active. ReviewRequest must call dispatch once to approve;
// returning nil without calling it still blocks the request.
type NetworkPolicy struct {
	ReviewRequest func(context.Context, NetworkRequest, func() error) error
	Done          <-chan struct{}
}

// JSONConnection is a page-scoped CDP wire connection. Close only detaches this
// connection; the user's page, cookies, and native controller remain alive.
type JSONConnection interface {
	ReadJSON(any) error
	WriteJSON(any) error
	SetWriteDeadline(time.Time) error
	Close() error
}

// Page is the stable native page identity used by the desktop/application bridge.
// Its logical CDP session ID is ID(); it never points at the Wails renderer.
type Page interface {
	ID() string
	ProfileID() string
	State() State
	Navigate(context.Context, string) error
	Back(context.Context) error
	Forward(context.Context) error
	Reload(context.Context) error
	Stop(context.Context) error
	Focus(context.Context) error
	SetSurface(context.Context, Bounds, bool) error
	SetUserInputEnabled(context.Context, bool) error
	OpenCDP(context.Context, CDPOptions) (JSONConnection, error)
	Close(context.Context) error
}
