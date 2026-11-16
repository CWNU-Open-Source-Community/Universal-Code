package browserruntime

import (
	"context"
	"errors"
	"io"
	"testing"
)

type failingAgentBrowserConnection struct {
	*attachedTestConnection
	failure error
	cancel  context.CancelFunc
}

func (c *failingAgentBrowserConnection) WriteJSON(value any) error {
	if c.failure != nil {
		if c.cancel != nil {
			c.cancel()
		}
		return c.failure
	}
	return c.attachedTestConnection.WriteJSON(value)
}

type failingNativeAgentBrowserConnection struct {
	*failingAgentBrowserConnection
}

func (c *failingNativeAgentBrowserConnection) WriteJSONContext(_ context.Context, value any) error {
	return c.WriteJSON(value)
}

type dispatchedAgentBrowserWriteError struct{ error }

func (dispatchedAgentBrowserWriteError) WriteDispatched() bool { return true }
func (e dispatchedAgentBrowserWriteError) Unwrap() error       { return e.error }

func TestAgentBrowserCDPWriteFailure(t *testing.T) {
	for _, operation := range []string{"call", "send"} {
		t.Run(operation, func(t *testing.T) {
			for _, test := range []struct {
				name    string
				native  bool
				err     error
				cancel  bool
				closed  bool
				unknown bool
			}{
				{name: "native cancellation", native: true, err: context.Canceled},
				{name: "native deadline", native: true, err: context.DeadlineExceeded},
				{name: "native cancellation after dispatch", native: true, err: dispatchedAgentBrowserWriteError{context.Canceled}, unknown: true},
				{name: "native broken connection", native: true, err: io.ErrClosedPipe, closed: true},
				{name: "native broken connection during cancellation", native: true, err: io.ErrClosedPipe, cancel: true, closed: true},
				{name: "stream cancellation", err: context.Canceled, closed: true},
				{name: "stream deadline", err: context.DeadlineExceeded, closed: true},
			} {
				t.Run(test.name, func(t *testing.T) {
					_, _, wire, _, _ := attachedFixture(t)
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					failing := &failingAgentBrowserConnection{attachedTestConnection: wire, failure: test.err}
					if test.cancel {
						failing.cancel = cancel
					}
					var connection AgentBrowserConnection = failing
					if test.native {
						connection = &failingNativeAgentBrowserConnection{failing}
					}
					cdp := newAgentBrowserCDP(connection, func(cdpWireMessage) {})
					defer cdp.close()
					var err error
					if operation == "call" {
						err = cdp.call(ctx, wire.session, "DOM.getDocument", map[string]any{}, nil)
					} else {
						var wait func() error
						wait, err = cdp.send(ctx, wire.session, "DOM.getDocument", map[string]any{})
						if wait != nil {
							t.Fatal("failed send returned a waiter")
						}
					}
					if !errors.Is(err, test.err) {
						t.Fatalf("write error = %v, want %v", err, test.err)
					}
					if test.native && !test.closed {
						var action *AgentBrowserActionError
						if !errors.As(err, &action) || action.Dispatched != test.unknown || action.OutcomeUnknown != test.unknown {
							t.Fatalf("native write lost dispatch state: %v", err)
						}
					}
					cdp.mu.Lock()
					pending := len(cdp.pending)
					cdp.mu.Unlock()
					if pending != 0 {
						t.Fatal("failed write retained a pending reply")
					}
					select {
					case <-wire.closed:
						if !test.closed {
							t.Fatal("native cancellation closed the connection")
						}
					default:
						if test.closed {
							t.Fatal("write failure left the connection open")
						}
					}
					failing.failure = nil
					err = cdp.call(t.Context(), wire.session, "DOM.getDocument", map[string]any{}, nil)
					if test.closed {
						if !errors.Is(err, ErrAgentBrowserClosed) {
							t.Fatalf("call after broken write = %v", err)
						}
					} else if err != nil {
						t.Fatalf("observation after cancelled native write: %v", err)
					}
				})
			}
		})
	}
}
