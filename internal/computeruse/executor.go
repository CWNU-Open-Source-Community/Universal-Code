// Package computeruse provides the built-in Windows desktop MCP executor.
// Host-side authorization must precede calls; this package binds each call to
// a live native window and serializes access to the interactive desktop.
package computeruse

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"cyberagent-workbench/internal/imageattachment"
)

type Bounds struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type Window struct {
	WindowID         string `json:"window_id"`
	AppID            string `json:"app_id"`
	Title            string `json:"title"`
	PID              uint32 `json:"pid"`
	ProcessPath      string `json:"process_path"`
	HWND             string `json:"hwnd"`
	Bounds           Bounds `json:"bounds"`
	Foreground       bool   `json:"foreground"`
	OwnerPID         uint32 `json:"window_owner_pid"`
	OwnerProcessPath string `json:"window_owner_process_path"`
}

type Observation struct {
	Window        Window `json:"window"`
	ObservationID string `json:"observation_id"`
	ImageWidth    int    `json:"image_width"`
	ImageHeight   int    `json:"image_height"`
	CaptureMethod string `json:"capture_method"`
	PNG           []byte `json:"-"`
	Action        string `json:"action,omitempty"`
	ActionSent    bool   `json:"action_sent,omitempty"`
}

// ObservationUnavailable preserves what happened when an input was sent but
// cannot be followed by a usable image, including a window that just closed.
// ActionStatus is sent, unknown (input may be partial), or not_sent.
type ObservationUnavailable struct {
	LastKnownWindow      Window `json:"last_known_window"`
	Action               string `json:"action,omitempty"`
	ActionSent           bool   `json:"action_sent"`
	ActionStatus         string `json:"action_status"`
	ObservationAvailable bool   `json:"observation_available"`
	Reason               string `json:"reason"`
	NextStep             string `json:"next_step"`
}

func unavailableObservation(w Window, action, status string, err error) ObservationUnavailable {
	return ObservationUnavailable{LastKnownWindow: w, Action: action, ActionSent: status == "sent", ActionStatus: status, Reason: err.Error(), NextStep: "List windows or obtain a new observation before further input. Do not automatically replay this action; no observation ID or usable image was delivered."}
}

type Arguments struct {
	WindowID      string `json:"window_id"`
	AppID         string `json:"app_id"`
	ObservationID string `json:"observation_id"`
	X             int    `json:"x"`
	Y             int    `json:"y"`
	Button        string `json:"button"`
	Text          string `json:"text"`
	Key           string `json:"key"`
	DX            int    `json:"dx"`
	DY            int    `json:"dy"`
}

type nativeWindow struct {
	Window
	handle     uintptr
	birth      uint64
	ownerBirth uint64
	ownerAppID string
}
type backend interface {
	enter(context.Context) (func(), error)
	generation(bool) uint64
	list(context.Context) ([]nativeWindow, error)
	inspect(uintptr) (nativeWindow, error)
	capture(context.Context, nativeWindow) ([]byte, error)
	activate(context.Context, nativeWindow) error
	input(context.Context, nativeWindow, string, Arguments) error
}
type observed struct {
	id         string
	bounds     Bounds
	at         time.Time
	generation uint64
}
type executor struct {
	backend      backend
	lock         chan struct{}
	windows      map[string]nativeWindow
	observations map[string]observed
}

func newExecutor(b backend) *executor {
	return &executor{b, make(chan struct{}, 1), map[string]nativeWindow{}, map[string]observed{}}
}
func token() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func (e *executor) run(ctx context.Context, name string, a Arguments) (any, []byte, error) {
	select {
	case e.lock <- struct{}{}:
		defer func() { <-e.lock }()
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	release, err := e.backend.enter(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer release()
	if err = ctx.Err(); err != nil {
		return nil, nil, err
	}
	if name == "computer_list_windows" {
		ws, err := e.backend.list(ctx)
		if err != nil {
			return nil, nil, err
		}
		live := make(map[string]nativeWindow)
		out := make([]Window, 0, len(ws))
		for _, w := range ws {
			for id, old := range e.windows {
				if sameWindow(w, old) {
					w.WindowID = id
					break
				}
			}
			if w.WindowID == "" {
				w.WindowID = token()
			}
			live[w.WindowID] = w
			out = append(out, w.Window)
		}
		e.windows = live
		for id := range e.observations {
			if _, ok := live[id]; !ok {
				delete(e.observations, id)
			}
		}
		return struct {
			Windows []Window `json:"windows"`
		}{out}, nil, nil
	}
	old, ok := e.windows[a.WindowID]
	if !ok || a.AppID == "" || old.AppID != a.AppID {
		return nil, nil, errors.New("unknown window or application identity; list windows again")
	}
	w, err := e.backend.inspect(old.handle)
	if err != nil || !sameWindow(old, w) {
		delete(e.windows, a.WindowID)
		delete(e.observations, a.WindowID)
		return nil, nil, errors.New("window identity changed; list windows again")
	}
	w.WindowID = a.WindowID
	action, status := "", "not_sent"
	unavailable := func(err error) (any, []byte, error) {
		delete(e.observations, a.WindowID)
		return unavailableObservation(w.Window, action, status, err), nil, nil
	}
	switch name {
	case "computer_observe":
	case "computer_activate":
		action = name
		delete(e.observations, a.WindowID)
		e.backend.generation(true)
		if err = e.backend.activate(ctx, w); err != nil {
			status = "unknown"
			return unavailable(fmt.Errorf("activation may have changed focus: %w", err))
		}
		status = "sent"
		activated, inspectErr := e.backend.inspect(w.handle)
		if inspectErr != nil || !sameWindow(old, activated) {
			return unavailable(errors.New("activation completed, but the window changed before observation"))
		}
		w = activated
		w.WindowID = a.WindowID
	case "computer_click", "computer_type", "computer_key", "computer_scroll":
		o, ok := e.observations[a.WindowID]
		if !ok || a.ObservationID == "" || o.id != a.ObservationID || time.Since(o.at) > 60*time.Second || o.bounds != w.Bounds || o.generation != e.backend.generation(false) {
			return nil, nil, errors.New("stale observation; observe the window again")
		}
		if !w.Foreground {
			return nil, nil, errors.New("target window is not foreground; activate and observe it first")
		}
		if err = validateInput(name, a, w.Bounds); err != nil {
			return nil, nil, err
		}
		// Consume before input, including partially completed or failed input.
		action = name
		e.observations = map[string]observed{}
		e.backend.generation(true)
		if err = e.backend.input(ctx, w, name, a); err != nil {
			status = "unknown"
			return unavailable(fmt.Errorf("input may have been partially sent: %w", err))
		}
		status = "sent"
		select {
		case <-time.After(80 * time.Millisecond):
		case <-ctx.Done():
			return unavailable(fmt.Errorf("input was sent; fresh observation cancelled: %w", ctx.Err()))
		}
	default:
		return nil, nil, errors.New("unknown computer tool")
	}
	current, err := e.backend.inspect(w.handle)
	if err != nil || !sameWindow(w, current) || current.Bounds != w.Bounds {
		return unavailable(errors.New("window closed, moved, or changed before a fresh observation could be captured"))
	}
	current.WindowID = a.WindowID
	if !current.Foreground {
		return unavailable(errors.New("target window is no longer foreground"))
	}
	if err := captureBoundsAllowed(current.Bounds); err != nil {
		return unavailable(err)
	}
	data, err := e.backend.capture(ctx, current)
	if err != nil {
		return unavailable(err)
	}
	check, err := e.backend.inspect(w.handle)
	if err != nil || !sameWindow(current, check) || check.Bounds != current.Bounds || !check.Foreground {
		return unavailable(errors.New("window changed during capture"))
	}
	id := token()
	o := Observation{Window: current.Window, ObservationID: id, ImageWidth: current.Bounds.Width, ImageHeight: current.Bounds.Height, CaptureMethod: "gdi-visible-desktop", PNG: data, Action: action, ActionSent: status == "sent"}
	if fits, err := resultFits(o, data); err != nil {
		return unavailable(err)
	} else if !fits {
		return unavailable(errors.New("screenshot exceeds the host's 4 MiB MCP message limit at its original dimensions; no image or observation ID was delivered"))
	}
	e.observations[a.WindowID] = observed{id, current.Bounds, time.Now(), e.backend.generation(false)}
	return o, data, nil
}

func captureBoundsAllowed(b Bounds) error {
	if b.Width < 1 || b.Height < 1 || b.Width > imageattachment.MaxDimension || b.Height > imageattachment.MaxDimension || int64(b.Width)*int64(b.Height) > imageattachment.MaxPixels {
		return errors.New("screenshot exceeds the host image limits of 8192 pixels per side or 16 MiPixels; resize the window before observing again; no image or observation ID was delivered")
	}
	return nil
}
func sameWindow(a, b nativeWindow) bool {
	return a.handle == b.handle && a.PID == b.PID && a.birth == b.birth && a.AppID == b.AppID &&
		a.OwnerPID == b.OwnerPID && a.ownerBirth == b.ownerBirth && a.ownerAppID == b.ownerAppID
}
func validateInput(name string, a Arguments, b Bounds) error {
	switch name {
	case "computer_click":
		if a.X < 0 || a.Y < 0 || a.X >= b.Width || a.Y >= b.Height {
			return errors.New("click outside observed image")
		}
		if a.Button != "" && a.Button != "left" && a.Button != "right" && a.Button != "middle" {
			return errors.New("unsupported mouse button")
		}
	case "computer_type":
		if len(a.Text) == 0 || len(a.Text) > 8192 {
			return errors.New("text must contain 1 to 8192 UTF-8 bytes")
		}
	case "computer_key":
		if _, err := parseChord(a.Key); err != nil {
			return err
		}
	case "computer_scroll":
		if (a.DX == 0 && a.DY == 0) || a.DX < -10 || a.DX > 10 || a.DY < -10 || a.DY > 10 {
			return fmt.Errorf("scroll dx/dy must be between -10 and 10 wheel detents, with at least one nonzero")
		}
	}
	return nil
}
