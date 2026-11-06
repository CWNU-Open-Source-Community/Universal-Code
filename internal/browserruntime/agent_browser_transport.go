package browserruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// One reader routes both events and replies. A late reply to a cancelled call
// is discarded by ID; it can never satisfy a subsequent action.
type agentBrowserCDP struct {
	conn    agentBrowserConnection
	mu      sync.Mutex
	writeMu sync.Mutex
	nextID  int64
	pending map[int64]agentBrowserPending
	done    chan struct{}
	onEvent func(cdpWireMessage)
	once    sync.Once
}

// The only dial remains in the audited restricted_cdp_transport adapter; this
// dispatcher receives an already validated owned-loopback connection.
type agentBrowserConnection interface {
	ReadJSON(any) error
	WriteJSON(any) error
	SetWriteDeadline(time.Time) error
	Close() error
}
type agentBrowserPending struct {
	session string
	reply   chan cdpWireMessage
}

func newAgentBrowserCDP(conn agentBrowserConnection, onEvent func(cdpWireMessage)) *agentBrowserCDP {
	c := &agentBrowserCDP{conn: conn, pending: map[int64]agentBrowserPending{}, done: make(chan struct{}), onEvent: onEvent}
	go c.read()
	return c
}
func (c *agentBrowserCDP) close() { c.once.Do(func() { close(c.done); _ = c.conn.Close() }) }
func (c *agentBrowserCDP) read() {
	defer c.close()
	for {
		var message cdpWireMessage
		if err := c.conn.ReadJSON(&message); err != nil {
			return
		}
		if message.ID != 0 {
			c.mu.Lock()
			p, ok := c.pending[message.ID]
			if ok {
				delete(c.pending, message.ID)
			}
			c.mu.Unlock()
			if ok {
				if p.session != message.SessionID {
					return
				}
				p.reply <- message
			}
		} else if message.Method != "" {
			c.onEvent(message)
		}
	}
}

var agentBrowserCDPMethods = map[string]bool{
	"Target.createBrowserContext": false, "Target.createTarget": false, "Target.attachToTarget": false,
	"Browser.setDownloadBehavior": false,
	// Quarantine capture only: cancels one GUID-named download that exceeded
	// the byte budget. It reads nothing and touches no other download.
	"Browser.cancelDownload": false,
	"Page.enable":            true, "DOM.enable": true, "Runtime.enable": true, "Accessibility.enable": true,
	"Network.enable": true, "Network.setBlockedURLs": true, "Network.setBypassServiceWorker": true,
	"Fetch.enable": true, "Fetch.continueRequest": true, "Fetch.failRequest": true,
	"Page.getFrameTree": true, "Page.createIsolatedWorld": true, "Page.navigate": true, "Page.captureScreenshot": true,
	"Page.setLifecycleEventsEnabled": true,
	"DOM.getDocument":                true, "DOM.resolveNode": true, "DOM.scrollIntoViewIfNeeded": true, "DOM.focus": true,
	// Upload sets file paths on one snapshot-resolved file input; the
	// application layer proves the path is a real file inside the workspace
	// before this method is ever reachable.
	"DOM.setFileInputFiles":  true,
	"Runtime.callFunctionOn": true, "Runtime.releaseObjectGroup": true, "Accessibility.getFullAXTree": true,
	"Input.dispatchMouseEvent": true, "Input.dispatchKeyEvent": true, "Input.insertText": true,
	"Emulation.setDeviceMetricsOverride": true,
	// Operator live view only: start/stop toggle the renderer's frame tap and
	// the ack is pure flow control. None of them navigates, evaluates, or reads
	// anything the agent could not already capture as hashed screenshot evidence.
	"Page.startScreencast": true, "Page.stopScreencast": true, "Page.screencastFrameAck": true,
}

// send is used under the document invalidation fence. Replies are discarded
// by ID so the reader can process a navigation without waiting on that fence.
func (c *agentBrowserCDP) send(ctx context.Context, session, method string, params any) error {
	if target, ok := agentBrowserCDPMethods[method]; !ok || !target || session == "" {
		return ErrBrowserRuntimeBoundary
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	err := c.conn.WriteJSON(map[string]any{"id": id, "sessionId": session, "method": method, "params": params})
	if err != nil {
		c.close()
	}
	return err
}

func (c *agentBrowserCDP) call(ctx context.Context, session, method string, params any, out any) error {
	target, ok := agentBrowserCDPMethods[method]
	if !ok || (target && session == "") || (!target && session != "") {
		return ErrBrowserRuntimeBoundary
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-c.done:
		return ErrAgentBrowserClosed
	default:
	}
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	reply := make(chan cdpWireMessage, 1)
	c.pending[id] = agentBrowserPending{session, reply}
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	c.writeMu.Lock()
	if err := ctx.Err(); err != nil {
		c.writeMu.Unlock()
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.conn.SetWriteDeadline(deadline)
	err := c.conn.WriteJSON(struct {
		ID        int64  `json:"id"`
		SessionID string `json:"sessionId,omitempty"`
		Method    string `json:"method"`
		Params    any    `json:"params"`
	}{id, session, method, params})
	c.writeMu.Unlock()
	if err != nil {
		c.close()
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return ErrAgentBrowserClosed
	case message := <-reply:
		if message.Error != nil {
			return fmt.Errorf("browser protocol %s failed (%d)", method, message.Error.Code)
		}
		if len(message.Result) == 0 {
			return errors.New("browser protocol reply has no result")
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(message.Result, out)
	}
}
