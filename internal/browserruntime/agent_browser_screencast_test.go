package browserruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// screencastTestConn is a scripted agentBrowserConnection: it records every
// client write and feeds scripted server messages back, so the pump can be
// driven deterministically without a browser.
type screencastTestConn struct {
	mu     sync.Mutex
	writes []cdpWireMessage
	in     chan cdpWireMessage
	notify chan struct{}
	closed chan struct{}
	once   sync.Once
}

func newScreencastTestConn() *screencastTestConn {
	return &screencastTestConn{in: make(chan cdpWireMessage, 64), notify: make(chan struct{}, 64),
		closed: make(chan struct{})}
}

func (c *screencastTestConn) ReadJSON(v any) error {
	message, ok := <-c.in
	if !ok {
		return io.EOF
	}
	*(v.(*cdpWireMessage)) = message
	return nil
}

func (c *screencastTestConn) WriteJSON(v any) error {
	select {
	case <-c.closed:
		return errors.New("connection closed")
	default:
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var message cdpWireMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		return err
	}
	c.mu.Lock()
	c.writes = append(c.writes, message)
	c.mu.Unlock()
	// Auto-reply to every command so the caller never waits on a scripted
	// acknowledgement; late replies are discarded by the pending map.
	if message.ID != 0 {
		c.in <- cdpWireMessage{ID: message.ID, SessionID: message.SessionID, Result: json.RawMessage(`{}`)}
	}
	select {
	case c.notify <- struct{}{}:
	default:
	}
	return nil
}

func (c *screencastTestConn) SetWriteDeadline(time.Time) error { return nil }
func (c *screencastTestConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *screencastTestConn) wrote() []cdpWireMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]cdpWireMessage(nil), c.writes...)
}

func (c *screencastTestConn) frame(sequence int, jpegBytes int) {
	payload := make([]byte, jpegBytes)
	for index := range payload {
		payload[index] = byte(sequence)
	}
	params, err := json.Marshal(map[string]any{"sessionId": "owned-session",
		"data":     base64.StdEncoding.EncodeToString(payload),
		"metadata": map[string]any{"deviceWidth": 1280, "deviceHeight": 900}})
	if err != nil {
		return
	}
	c.in <- cdpWireMessage{Method: "Page.screencastFrame", SessionID: "owned-session",
		Params: params}
}

func (c *screencastTestConn) waitFor(t *testing.T, predicate func([]cdpWireMessage) bool,
	description string,
) []cdpWireMessage {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		writes := c.wrote()
		if predicate(writes) {
			return writes
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; writes so far: %d", description, len(writes))
		}
		<-c.notify
	}
}

func newScreencastTestRuntime(t *testing.T) (*AgentBrowserRuntime, *screencastTestConn) {
	t.Helper()
	conn := newScreencastTestConn()
	life, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runtime := &AgentBrowserRuntime{life: life, cancel: cancel, done: make(chan struct{}),
		session: "owned-session", frame: "main"}
	runtime.cdp = newAgentBrowserCDP(conn, runtime.onEvent)
	t.Cleanup(runtime.cdp.close)
	return runtime, conn
}

func TestAgentBrowserScreencastForwardsAckAndStops(t *testing.T) {
	runtime, conn := newScreencastTestRuntime(t)
	stream, err := runtime.StartScreencast(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	writes := conn.waitFor(t, func(writes []cdpWireMessage) bool {
		return len(writes) >= 1 && writes[0].Method == "Page.startScreencast"
	}, "Page.startScreencast")
	if writes[0].SessionID != "owned-session" {
		t.Fatalf("start screencast was not session-scoped: %+v", writes[0])
	}
	var params struct {
		Format        string `json:"format"`
		EveryNthFrame int    `json:"everyNthFrame"`
		MaxWidth      int    `json:"maxWidth"`
		MaxHeight     int    `json:"maxHeight"`
	}
	if json.Unmarshal(writes[0].Params, &params) != nil || params.Format != "jpeg" ||
		params.MaxWidth != AgentBrowserScreencastMaxWidth || params.MaxHeight != AgentBrowserScreencastMaxHeight ||
		params.EveryNthFrame != agentBrowserScreencastEveryNthFrame {
		t.Fatalf("unexpected screencast params: %s", writes[0].Params)
	}

	// A deterministic clock: each frame lands one full rate interval after the
	// previous one, so every frame is admitted and forwarded.
	runtime.mu.Lock()
	pump := runtime.screencast
	base := time.Unix(0, 0)
	steps := 0
	pump.now = func() time.Time { steps++; return base.Add(time.Duration(steps) * agentBrowserScreencastFrameInterval) }
	runtime.mu.Unlock()

	const frames = 6
	received := 0
	for sequence := 0; sequence < frames; sequence++ {
		// Push and receive one frame at a time: the two-frame buffer with
		// drop-oldest may otherwise legally discard an unread frame, which is
		// the live view's freshness-over-completeness contract.
		conn.frame(sequence, 64)
		select {
		case frame := <-stream.Frames():
			received++
			if len(frame.JPEG) != 64 || frame.JPEG[0] != byte(sequence) ||
				frame.Width != 1280 || frame.Height != 900 {
				t.Fatalf("frame %d arrived mangled: %d bytes %dx%d first=%d", received,
					len(frame.JPEG), frame.Width, frame.Height, frame.JPEG[0])
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d of %d frames arrived", received, frames)
		}
	}
	conn.waitFor(t, func(writes []cdpWireMessage) bool {
		acks := 0
		for _, write := range writes {
			if write.Method == "Page.screencastFrameAck" {
				acks++
			}
		}
		return acks == frames
	}, "one ack per delivered frame")

	if _, err := runtime.StartScreencast(context.Background()); !errors.Is(err, ErrAgentBrowserScreencastActive) {
		t.Fatalf("second viewer was not rejected: %v", err)
	}

	stream.Stop()
	writes = conn.waitFor(t, func(writes []cdpWireMessage) bool {
		for _, write := range writes {
			if write.Method == "Page.stopScreencast" {
				return true
			}
		}
		return false
	}, "Page.stopScreencast")
	_ = writes
	if _, open := <-stream.Frames(); open {
		t.Fatal("frames channel stayed open after stop")
	}

	second, err := runtime.StartScreencast(context.Background())
	if err != nil {
		t.Fatalf("restart after stop failed: %v", err)
	}
	second.Stop()
}

func TestAgentBrowserScreencastStaleWatcherCannotCloseItsReplacement(t *testing.T) {
	runtime, _ := newScreencastTestRuntime(t)
	first, err := runtime.StartScreencast(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	stalePump := runtime.screencast
	runtime.mu.Unlock()
	first.Stop()

	second, err := runtime.StartScreencast(context.Background())
	if err != nil {
		t.Fatalf("restart after stop failed: %v", err)
	}
	// The stopped viewer's watcher can unwind after the replacement attached;
	// its cleanup must leave the new pump registered and open.
	runtime.stopScreencastPump(stalePump, false)
	runtime.mu.Lock()
	current := runtime.screencast
	runtime.mu.Unlock()
	if current == nil || current == stalePump {
		t.Fatal("a stale watcher tore down the replacement live view")
	}
	select {
	case _, open := <-second.Frames():
		if !open {
			t.Fatal("replacement frame channel was closed by a stale watcher")
		}
	case <-time.After(50 * time.Millisecond):
	}
	second.Stop()
}

func TestAgentBrowserScreencastDropsOversizedAndStaleFramesButAlwaysAcks(t *testing.T) {
	runtime, conn := newScreencastTestRuntime(t)
	stream, err := runtime.StartScreencast(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	conn.waitFor(t, func(writes []cdpWireMessage) bool {
		return len(writes) >= 1 && writes[0].Method == "Page.startScreencast"
	}, "Page.startScreencast")

	// Frozen clock: nothing is admitted after the first frame, so the rest are
	// stale for the rate limiter; the oversized frame additionally fails the
	// byte bound. Every single frame must still be acked.
	runtime.mu.Lock()
	pump := runtime.screencast
	base := time.Unix(0, 0)
	pump.now = func() time.Time { return base }
	runtime.mu.Unlock()

	conn.frame(1, 64)
	first := <-stream.Frames()
	if len(first.JPEG) != 64 {
		t.Fatalf("first frame lost: %d bytes", len(first.JPEG))
	}
	const stale = 4
	for sequence := 0; sequence < stale; sequence++ {
		conn.frame(sequence, 64)
	}
	conn.frame(9, AgentBrowserScreencastMaxFrameBytes+1)

	total := stale + 2
	conn.waitFor(t, func(writes []cdpWireMessage) bool {
		acks := 0
		for _, write := range writes {
			if write.Method == "Page.screencastFrameAck" {
				acks++
			}
		}
		return acks == total
	}, "one ack per received frame, forwarded or not")
	extra := 0
	for {
		select {
		case <-stream.Frames():
			extra++
		case <-time.After(200 * time.Millisecond):
		}
		if extra == 0 {
			break
		}
	}
	if extra != 0 {
		t.Fatalf("stale or oversized frames leaked through: %d", extra)
	}
	stream.Stop()
}

func TestAgentBrowserScreencastRejectsViewerWithoutSession(t *testing.T) {
	conn := newScreencastTestConn()
	life, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime := &AgentBrowserRuntime{life: life, cancel: cancel, done: make(chan struct{})}
	runtime.cdp = newAgentBrowserCDP(conn, runtime.onEvent)
	defer runtime.cdp.close()
	if _, err := runtime.StartScreencast(context.Background()); !errors.Is(err, ErrAgentBrowserClosed) {
		t.Fatalf("viewer started without an owned session: %v", err)
	}
}
