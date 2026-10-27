package browserruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// The operator live view is visibility only. It adds no agent-callable CDP
// power, never persists frames, never reaches tool results, and is capped
// below the page's repaint rate so it cannot become a bulk channel.
const (
	AgentBrowserScreencastMaxFPS        = 10
	AgentBrowserScreencastJPEGQuality   = 60
	AgentBrowserScreencastMaxWidth      = 1280
	AgentBrowserScreencastMaxHeight     = 900
	AgentBrowserScreencastMaxFrameBytes = 512 * 1024
	// everyNthFrame bounds Chrome's capture side; admit() below additionally
	// enforces the wall-clock ceiling so a fast-repainting page cannot exceed it.
	agentBrowserScreencastEveryNthFrame = 6
	agentBrowserScreencastFrameInterval = time.Second / AgentBrowserScreencastMaxFPS
)

var ErrAgentBrowserScreencastActive = errors.New("agent browser screencast already has a viewer")

type AgentBrowserScreencastFrame struct {
	JPEG       []byte
	Width      int
	Height     int
	CapturedAt time.Time
}

// AgentBrowserScreencastStream is the single-viewer handle for one session's
// live view. Frames is closed when the stream stops or the session ends.
type AgentBrowserScreencastStream struct {
	frames <-chan AgentBrowserScreencastFrame
	stop   func()
}

func (s AgentBrowserScreencastStream) Frames() <-chan AgentBrowserScreencastFrame {
	return s.frames
}

func (s AgentBrowserScreencastStream) Stop() {
	if s.stop != nil {
		s.stop()
	}
}

// agentBrowserScreencastPump owns one viewer's flow: Chrome pushes frames, the
// pump rate-limits, drops stale frames for a slow viewer, and always acks so
// Chromium's own flow control stays healthy.
type agentBrowserScreencastPump struct {
	frames   chan AgentBrowserScreencastFrame
	acks     chan string
	stopCh   chan struct{}
	stopOnce sync.Once
	last     time.Time
	now      func() time.Time
}

func (p *agentBrowserScreencastPump) admit(now time.Time) bool {
	if now.Sub(p.last) < agentBrowserScreencastFrameInterval {
		return false
	}
	p.last = now
	return true
}

func (p *agentBrowserScreencastPump) drainAcks(r *AgentBrowserRuntime) {
	for sessionID := range p.acks {
		if sessionID == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = r.call(ctx, "Page.screencastFrameAck", map[string]any{"sessionId": sessionID}, nil)
		cancel()
	}
}

// StartScreencast opens the renderer's frame tap for exactly one viewer. The
// frames channel closes on Stop or when the session ends. A second concurrent
// viewer is rejected instead of silently sharing or preempting the first.
func (r *AgentBrowserRuntime) StartScreencast(ctx context.Context) (AgentBrowserScreencastStream, error) {
	r.mu.Lock()
	if r.screencast != nil {
		r.mu.Unlock()
		return AgentBrowserScreencastStream{}, ErrAgentBrowserScreencastActive
	}
	if r.session == "" {
		r.mu.Unlock()
		return AgentBrowserScreencastStream{}, ErrAgentBrowserClosed
	}
	pump := &agentBrowserScreencastPump{frames: make(chan AgentBrowserScreencastFrame, 2),
		acks: make(chan string, 8), stopCh: make(chan struct{}), now: time.Now}
	r.screencast = pump
	r.mu.Unlock()

	params := map[string]any{"format": "jpeg", "quality": AgentBrowserScreencastJPEGQuality,
		"maxWidth": AgentBrowserScreencastMaxWidth, "maxHeight": AgentBrowserScreencastMaxHeight,
		"everyNthFrame": agentBrowserScreencastEveryNthFrame}
	if err := r.call(ctx, "Page.startScreencast", params, nil); err != nil {
		r.mu.Lock()
		r.screencast = nil
		r.mu.Unlock()
		return AgentBrowserScreencastStream{}, err
	}
	go pump.drainAcks(r)
	go func() {
		select {
		case <-r.done:
		case <-pump.stopCh:
		}
		r.stopScreencastPump(false)
	}()
	return AgentBrowserScreencastStream{frames: pump.frames, stop: func() {
		r.stopScreencastPump(true)
	}}, nil
}

// stopScreencastPump tears the pump down exactly once. callCDP is false when
// the session itself already ended; the browser-side tap dies with it.
func (r *AgentBrowserRuntime) stopScreencastPump(callCDP bool) {
	r.mu.Lock()
	pump := r.screencast
	r.screencast = nil
	r.mu.Unlock()
	if pump == nil {
		return
	}
	pump.stopOnce.Do(func() {
		close(pump.stopCh)
		close(pump.acks)
		close(pump.frames)
	})
	if callCDP {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = r.call(ctx, "Page.stopScreencast", map[string]any{}, nil)
	}
}

// deliverScreencastLocked runs on the CDP reader while r.mu is held. It must
// never block: forwarding drops stale frames, and acks are queued to a worker
// so a stalled browser call cannot stall every other CDP event.
func (r *AgentBrowserRuntime) deliverScreencastLocked(event cdpWireMessage) {
	pump := r.screencast
	if pump == nil {
		return
	}
	var frame struct {
		SessionID string `json:"sessionId"`
		Data      string `json:"data"`
		Metadata  struct {
			DeviceWidth  int `json:"deviceWidth"`
			DeviceHeight int `json:"deviceHeight"`
		} `json:"metadata"`
	}
	if json.Unmarshal(event.Params, &frame) != nil || frame.SessionID == "" {
		return
	}
	if now := pump.now(); pump.admit(now) {
		if raw, err := base64.StdEncoding.DecodeString(frame.Data); err == nil &&
			len(raw) > 0 && len(raw) <= AgentBrowserScreencastMaxFrameBytes {
			out := AgentBrowserScreencastFrame{JPEG: raw, Width: frame.Metadata.DeviceWidth,
				Height: frame.Metadata.DeviceHeight, CapturedAt: now.UTC()}
			select {
			case pump.frames <- out:
			default:
				// Drop-oldest: a live view prefers freshness over completeness.
				select {
				case <-pump.frames:
				default:
				}
				select {
				case pump.frames <- out:
				default:
				}
			}
		}
	}
	select {
	case pump.acks <- frame.SessionID:
	default:
	}
}
