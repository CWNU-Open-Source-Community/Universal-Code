package application

import (
	"context"
	"encoding/base64"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/browserruntime"
)

const AgentBrowserScreencastVersion = "agent_browser_screencast.v1"

// AgentBrowserScreencastFrameView is one operator live-view frame. It never
// persists, never enters the evidence ledger, and is distinct from the hashed
// screenshot evidence that keeps serving verification.
type AgentBrowserScreencastFrameView struct {
	Version    string    `json:"version"`
	RunID      string    `json:"run_id"`
	SessionID  string    `json:"session_id"`
	Generation uint64    `json:"generation"`
	MediaType  string    `json:"media_type"`
	Width      int       `json:"width"`
	Height     int       `json:"height"`
	Data       string    `json:"data"`
	CapturedAt time.Time `json:"captured_at"`
}

type agentBrowserScreencastRuntime interface {
	StartScreencast(context.Context) (browserruntime.AgentBrowserScreencastStream, error)
}

// StreamScreencast forwards the run's active browser session frames to one
// operator viewer. It launches nothing, grants nothing, admits exactly one
// viewer, and ends when the session ends, the runtime exits, or the viewer
// disconnects.
func (s *AgentBrowserService) StreamScreencast(ctx context.Context, runID string,
	emit func(AgentBrowserScreencastFrameView) error,
) error {
	if s == nil || s.store == nil {
		return agentBrowserUnavailable("Agent browser unavailable")
	}
	if _, e := s.store.GetRun(ctx, runID); e != nil {
		return e
	}
	c, e := s.Capabilities(ctx, runID)
	if e != nil {
		return e
	}
	if !c.Available {
		return agentBrowserUnavailable("Agent browser session is not available")
	}
	s.mu.Lock()
	slot := s.slots[runID]
	if slot == nil || slot.runtime == nil || slot.closed {
		s.mu.Unlock()
		return agentBrowserUnavailable("Agent browser has no active session")
	}
	if slot.screencastViewer {
		s.mu.Unlock()
		return apperror.New(apperror.CodeConflict, "another operator view is already attached to this browser session")
	}
	slot.screencastViewer = true
	view := s.projectSlotLocked(slot)
	runtime := slot.runtime
	sessionID, generation := view.SessionID, view.Generation
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		slot.screencastViewer = false
		s.mu.Unlock()
	}()
	if sessionID == "" {
		return agentBrowserUnavailable("Agent browser session identity is not ready")
	}
	caster, ok := runtime.(agentBrowserScreencastRuntime)
	if !ok {
		return apperror.New(apperror.CodeFailedPrecondition, "Agent browser runtime does not expose a live view")
	}
	stream, err := caster.StartScreencast(ctx)
	if err != nil {
		return err
	}
	defer stream.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case frame, open := <-stream.Frames():
			if !open {
				return nil
			}
			out := AgentBrowserScreencastFrameView{Version: AgentBrowserScreencastVersion, RunID: runID,
				SessionID: sessionID, Generation: generation, MediaType: "image/jpeg",
				Width: frame.Width, Height: frame.Height,
				Data: base64.StdEncoding.EncodeToString(frame.JPEG), CapturedAt: frame.CapturedAt}
			if err := emit(out); err != nil {
				return err
			}
		}
	}
}
