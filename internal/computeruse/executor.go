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
)

type Bounds struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type Window struct {
	WindowID    string `json:"window_id"`
	AppID       string `json:"app_id"`
	Title       string `json:"title"`
	PID         uint32 `json:"pid"`
	ProcessPath string `json:"process_path"`
	HWND        string `json:"hwnd"`
	Bounds      Bounds `json:"bounds"`
	Foreground  bool   `json:"foreground"`
}

type Observation struct {
	Window        Window `json:"window"`
	ObservationID string `json:"observation_id"`
	ImageWidth    int    `json:"image_width"`
	ImageHeight   int    `json:"image_height"`
	CaptureMethod string `json:"capture_method"`
	PNG           []byte `json:"-"`
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
	handle uintptr
	birth  uint64
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
	switch name {
	case "computer_observe":
	case "computer_activate":
		delete(e.observations, a.WindowID)
		e.backend.generation(true)
		if err = e.backend.activate(ctx, w); err != nil {
			return nil, nil, err
		}
		w, err = e.backend.inspect(w.handle)
		if err != nil || !sameWindow(old, w) {
			return nil, nil, errors.New("window changed during activation")
		}
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
		e.observations = map[string]observed{}
		e.backend.generation(true)
		if err = e.backend.input(ctx, w, name, a); err != nil {
			return nil, nil, err
		}
		select {
		case <-time.After(80 * time.Millisecond):
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	default:
		return nil, nil, errors.New("unknown computer tool")
	}
	current, err := e.backend.inspect(w.handle)
	if err != nil || !sameWindow(w, current) || current.Bounds != w.Bounds {
		return nil, nil, errors.New("window changed; observe it again")
	}
	current.WindowID = a.WindowID
	if !current.Foreground {
		return nil, nil, errors.New("target window is not foreground; activate it first")
	}
	data, err := e.backend.capture(ctx, current)
	if err != nil {
		return nil, nil, err
	}
	check, err := e.backend.inspect(w.handle)
	if err != nil || !sameWindow(current, check) || check.Bounds != current.Bounds || !check.Foreground {
		return nil, nil, errors.New("window changed during capture; observe again")
	}
	id := token()
	e.observations[a.WindowID] = observed{id, current.Bounds, time.Now(), e.backend.generation(false)}
	o := Observation{current.Window, id, current.Bounds.Width, current.Bounds.Height, "gdi-visible-desktop", data}
	return o, data, nil
}
func sameWindow(a, b nativeWindow) bool {
	return a.handle == b.handle && a.PID == b.PID && a.birth == b.birth && a.AppID == b.AppID
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
