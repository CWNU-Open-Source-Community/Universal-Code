//go:build windows

package desktopbrowser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type cdpSubscription struct {
	receiver *comObject
	token    int64
	handler  *comHandler
}
type pageConnection struct {
	page          *nativePage
	options       CDPOptions
	mu            sync.Mutex
	closed        bool
	failure       error
	deadline      time.Time
	messages      [][]byte
	wake          chan struct{}
	done          chan struct{}
	subscriptions []cdpSubscription
}
type cdpRequest struct {
	ID        int64           `json:"id"`
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params"`
	SessionID string          `json:"sessionId"`
}

func (p *nativePage) OpenCDP(ctx context.Context, options CDPOptions) (JSONConnection, error) {
	if options.Dispatch == nil {
		return nil, errors.New("page CDP requires an atomic control-lease dispatcher")
	}
	c := &pageConnection{page: p, options: options, wake: make(chan struct{}, 1), done: make(chan struct{})}
	err := p.withUI(ctx, func() error {
		seen := map[string]bool{}
		for _, name := range options.Events {
			if seen[name] {
				continue
			}
			seen[name] = true
			if !validEventName(name) {
				c.closeOnUI()
				return errors.New("invalid page CDP event")
			}
			value, _ := windows.UTF16PtrFromString(name)
			var receiver *comObject
			if err := comCall(p.core, coreGetDevToolsReceiver, uintptr(unsafe.Pointer(value)), uintptr(unsafe.Pointer(&receiver))); err != nil {
				c.closeOnUI()
				return err
			}
			method := name
			handler := newHandler("{E2FDA4BE-5456-406C-A261-3D452138362C}", func(_, args uintptr) uintptr {
				params, err := getString((*comObject)(unsafe.Pointer(args)), 3)
				if err != nil {
					c.fail(err)
					return 0
				}
				c.enqueue(map[string]any{"method": method, "params": json.RawMessage(params), "sessionId": p.id})
				return 0
			})
			var token int64
			if err := comCall(receiver, 3, handler.pointer(), uintptr(unsafe.Pointer(&token))); err != nil {
				handler.release()
				receiver.release()
				c.closeOnUI()
				return err
			}
			c.subscriptions = append(c.subscriptions, cdpSubscription{receiver, token, handler})
		}
		p.mu.Lock()
		p.connections[c] = struct{}{}
		p.mu.Unlock()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}
func validEventName(name string) bool {
	if len(name) > 160 || strings.Count(name, ".") != 1 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

func (c *pageConnection) ReadJSON(value any) error {
	for {
		c.mu.Lock()
		if len(c.messages) > 0 {
			raw := c.messages[0]
			c.messages[0] = nil
			c.messages = c.messages[1:]
			c.mu.Unlock()
			return json.Unmarshal(raw, value)
		}
		if c.closed {
			err := c.failure
			if err == nil {
				err = io.EOF
			}
			c.mu.Unlock()
			return err
		}
		c.mu.Unlock()
		select {
		case <-c.wake:
		case <-c.done:
		}
	}
}
func (c *pageConnection) SetWriteDeadline(deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClosed
	}
	c.deadline = deadline
	return nil
}
func (c *pageConnection) WriteJSON(value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(raw) > 8<<20 {
		return errors.New("page CDP request exceeds 8 MiB")
	}
	var request cdpRequest
	if err = json.Unmarshal(raw, &request); err != nil {
		return err
	}
	if request.ID <= 0 || !validEventName(request.Method) {
		return errors.New("invalid page CDP request")
	}
	if request.SessionID != c.page.id {
		return errors.New("page CDP session does not match native controller")
	}
	// Browser/Target mutations would escape the exact controller and affect
	// sibling pages or the browser process. This is a page endpoint only.
	if strings.HasPrefix(request.Method, "Target.") || strings.HasPrefix(request.Method, "Browser.") {
		return fmt.Errorf("%s is unavailable on a native page connection", request.Method)
	}
	if len(request.Params) == 0 {
		request.Params = json.RawMessage(`{}`)
	}
	c.mu.Lock()
	closed, deadline := c.closed, c.deadline
	c.mu.Unlock()
	if closed {
		return ErrClosed
	}
	ctx := context.Background()
	var cancel context.CancelFunc
	if !deadline.IsZero() {
		ctx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	return c.page.withUI(ctx, func() error {
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if closed {
			return ErrClosed
		}
		return c.options.Dispatch(ctx, func() error {
			method, _ := windows.UTF16PtrFromString(request.Method)
			params, _ := windows.UTF16PtrFromString(string(request.Params))
			completed := newHandler("{5C4889F0-5EF6-4C5A-952C-D8F1B92D0574}", func(hr, result uintptr) uintptr {
				response := map[string]any{"id": request.ID, "sessionId": c.page.id}
				if err := hresult(request.Method, hr); err != nil {
					response["error"] = map[string]any{"code": -32000, "message": err.Error()}
				} else {
					text := "{}"
					if result != 0 {
						text = windows.UTF16PtrToString((*uint16)(unsafe.Pointer(result)))
					}
					if !json.Valid([]byte(text)) {
						response["error"] = map[string]any{"code": -32000, "message": "WebView2 returned invalid CDP JSON"}
					} else {
						response["result"] = json.RawMessage(text)
					}
				}
				c.enqueue(response)
				return 0
			})
			err := comCall(c.page.core, coreCallDevTools, uintptr(unsafe.Pointer(method)), uintptr(unsafe.Pointer(params)), completed.pointer())
			completed.release()
			return err
		})
	})
}

func (c *pageConnection) enqueue(value any) {
	raw, err := json.Marshal(value)
	if err != nil {
		c.fail(err)
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	if len(c.messages) >= 1024 || len(raw) > 16<<20 {
		c.mu.Unlock()
		c.fail(errors.New("native page CDP reader is not keeping up"))
		return
	}
	c.messages = append(c.messages, raw)
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}
func (c *pageConnection) fail(err error) {
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		c.failure = err
		close(c.done)
	}
	c.mu.Unlock()
}
func (c *pageConnection) Close() error {
	c.fail(nil)
	return c.page.host.options.Dispatcher.Post(func() { c.closeOnUI() })
}
func (c *pageConnection) closeOnUI() {
	c.fail(nil)
	for _, subscription := range c.subscriptions {
		_ = comCall(subscription.receiver, 4, uintptr(subscription.token))
		subscription.receiver.release()
		subscription.handler.release()
	}
	c.subscriptions = nil
	c.page.mu.Lock()
	delete(c.page.connections, c)
	c.page.mu.Unlock()
}
