package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Client negotiation is independent of the native server ProtocolVersion,
// and of persisted descriptor format versions.
const preferredClientProtocolVersion = "2026-07-28"

const legacyClientProtocolVersion = "2025-06-18"

func supportedClientProtocol(version string) bool {
	switch version {
	case "2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25", preferredClientProtocolVersion:
		return true
	default:
		return false
	}
}

func newSDKClient(transport sdk.Transport) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	tap := &sdkResponseTap{pending: make(map[jsonrpc.ID]sdkPendingResponse)}
	return &Client{transport: &sdkObservingTransport{Transport: transport, tap: tap}, tap: tap,
		lifetime: ctx, stop: cancel, protocolVersion: preferredClientProtocolVersion}
}
func (t *Client) connect(ctx context.Context) (*sdk.ClientSession, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if t.lifetime.Err() != nil {
		return nil, errors.New("MCP client is closed")
	}
	if t.connected {
		return t.session, t.connectErr
	}
	t.connected = true
	if t.beforeConnect != nil {
		if err := t.beforeConnect(ctx); err != nil {
			t.connectErr = err
			return nil, err
		}
	}
	// After host preparation, before SDK Connect can start the process or
	// make any discovery/initialize request. This does not grant authority.
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(t.lifetime, cancel)
	defer stop()
	defer cancel()
	options := &sdk.ClientOptions{
		Capabilities:   &sdk.ClientCapabilities{},
		MultiRoundTrip: &sdk.MultiRoundTripOptions{Disabled: true},
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProgressNotificationHandler: func(ctx context.Context, req *sdk.ProgressNotificationClientRequest) {
			t.progressMu.RLock()
			handler := t.progress
			t.progressMu.RUnlock()
			if handler != nil {
				handler(ctx, req.Params)
			}
		},
	}
	if t.inputHandler != nil {
		options.Capabilities.Elicitation = &sdk.ElicitationCapabilities{Form: &sdk.FormElicitationCapabilities{}, URL: &sdk.URLElicitationCapabilities{}}
		options.ElicitationHandler = t.handleLegacyInput
		t.tap.receiveInput = t.receiveLegacyInput
		t.tap.prepareReply = t.prepareLegacyReply
	}
	client := sdk.NewClient(&sdk.Implementation{Name: ClientName, Version: ClientVersion}, options)
	client.AddSendingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, request sdk.Request) (sdk.Result, error) {
			capture, _ := ctx.Value(sdkRawResponseKey{}).(*sdkRawResponse)
			if capture == nil {
				capture = &sdkRawResponse{}
			}
			ctx = context.WithValue(ctx, sdkRawResponseKey{}, capture)
			defer t.tap.forget(capture)
			result, err := next(ctx, method, request)
			raw, _ := capture.result()
			if len(raw) > MaxMessageBytes {
				return nil, errors.New("MCP response exceeds the transport limit")
			}
			if catalog, _ := ctx.Value(resolvedCatalogKey{}).(*resolvedCatalog); catalog != nil && len(raw) > 0 && (method == "tools/list" || method == "resources/list" || method == "resources/templates/list" || method == "prompts/list") {
				if len(raw) > MaxMessageBytes-catalog.bytes {
					return nil, errors.New("MCP native discovery exceeds its aggregate result limit")
				}
				catalog.bytes += len(raw)
				catalog.results = append(catalog.results, raw)
			}
			// Discovery is refreshed under host authority. Disable only the
			// SDK's decoded catalog cache before it stores this result; the
			// original native response and its TTL remain available to the host.
			switch result := result.(type) {
			case *sdk.ListToolsResult:
				// SDK's schema is any; retain exact JSON numbers for the stored
				// projection as well as the native catalog fingerprint.
				var page struct {
					Tools []struct {
						InputSchema json.RawMessage `json:"inputSchema"`
					} `json:"tools"`
				}
				if len(raw) > 0 && json.Unmarshal(raw, &page) == nil && len(page.Tools) == len(result.Tools) {
					for i, tool := range result.Tools {
						if tool != nil {
							tool.InputSchema = page.Tools[i].InputSchema
						}
					}
				}
				result.TTLMs = 0
			case *sdk.ListResourcesResult:
				result.TTLMs = 0
			case *sdk.ListResourceTemplatesResult:
				result.TTLMs = 0
			case *sdk.ListPromptsResult:
				result.TTLMs = 0
			}
			return result, err
		}
	})
	t.session, t.connectErr = client.Connect(ctx, t.transport, &sdk.ClientSessionOptions{ProtocolVersion: t.protocolVersion})
	if t.session != nil && len(t.allowedVersions) > 0 && !slices.Contains(t.allowedVersions, t.session.InitializeResult().ProtocolVersion) {
		_ = t.session.Close()
		t.session = nil
		t.connectErr = errors.New("MCP negotiated a protocol outside the authorized profile")
	}
	if t.session != nil && t.tap.negotiated != nil {
		t.tap.negotiated(t.session.InitializeResult().ProtocolVersion)
	}
	return t.session, t.connectErr
}

// callTool is the single typed SDK call path for legacy projections and native
// receipts. The tap observes native bytes; it does not dispatch another request.
func (c *Client) callTool(ctx context.Context, params *sdk.CallToolParams) (*sdk.CallToolResult, json.RawMessage, error) {
	session, err := c.connect(ctx)
	if err != nil {
		return nil, nil, err
	}
	attempt, _ := ctx.Value(sdkCallAttemptKey{}).(*sdkCallAttempt)
	if attempt == nil {
		attempt = &sdkCallAttempt{guard: c.beforeCall}
	}
	capture, _ := ctx.Value(sdkRawResponseKey{}).(*sdkRawResponse)
	if capture == nil {
		capture = &sdkRawResponse{}
	}
	ctx = context.WithValue(ctx, sdkCallAttemptKey{}, attempt)
	ctx = context.WithValue(ctx, sdkRawResponseKey{}, capture)
	result, err := session.CallTool(ctx, params)
	raw, wireError := capture.result()
	if wireError != nil {
		return nil, raw, wireError
	}
	if len(raw) > 0 {
		var shape struct {
			ResultType string          `json:"resultType"`
			Content    json.RawMessage `json:"content"`
		}
		if json.Unmarshal(raw, &shape) != nil || len(raw) > MaxMessageBytes {
			return nil, raw, errors.New("MCP tool result is invalid or oversized")
		}
		if shape.ResultType == "input_required" {
			return nil, raw, &sdkInputRequiredError{raw: raw}
		}
		if len(bytes.TrimSpace(shape.Content)) == 0 || bytes.TrimSpace(shape.Content)[0] != '[' {
			return nil, raw, errors.New("MCP tool result content is missing or invalid")
		}
		return result, raw, err
	}
	if err != nil && attempt.dispatched.Load() {
		return nil, nil, &sdkOutcomeUnknownError{cause: err}
	}
	attempt.mu.Lock()
	rejection := attempt.rejection
	attempt.mu.Unlock()
	if rejection != nil {
		return nil, nil, rejection
	}
	if err == nil {
		err = errors.New("MCP tool response is missing")
	}
	return nil, nil, err
}

func (c *Client) sdkError(err error) error {
	if err == nil {
		return nil
	}
	message := "MCP request failed"
	if c.resolved == nil {
		var remote *jsonrpc.Error
		if errors.As(err, &remote) {
			message = fmt.Sprintf("MCP request failed with remote code %d", remote.Code)
		} else {
			message = c.sanitizeText(err.Error())
		}
	}
	return &sanitizedClientError{message: message, cause: err}
}

func (t *Client) Close() error {
	if t == nil || t.closed.Swap(true) {
		return nil
	}
	t.closeOnce.Do(func() {
		t.stop()
		t.mu.Lock()
		session := t.session
		t.mu.Unlock()
		if session != nil {
			t.closeErr = session.Close()
		}
	})
	return t.closeErr
}

type sdkCallAttemptKey struct{}
type sdkCallAttempt struct {
	guard      func(context.Context) error
	dispatched atomic.Bool
	mu         sync.Mutex
	rejection  error
	wireID     json.RawMessage
	cancelSent atomic.Bool
}

func (a *sdkCallAttempt) reject(err error) error {
	a.mu.Lock()
	a.rejection = err
	a.mu.Unlock()
	return err
}

func beforeSDKDispatch(ctx context.Context, method string) error {
	if !isClientInvocationMethod(method) {
		return ctx.Err()
	}
	call, _ := ctx.Value(sdkCallAttemptKey{}).(*sdkCallAttempt)
	if call == nil {
		return errors.New("MCP tool dispatch is missing its host call context")
	}
	if err := ctx.Err(); err != nil {
		return call.reject(err)
	}
	if call.guard != nil {
		if err := call.guard(ctx); err != nil {
			return call.reject(err)
		}
	}
	if err := ctx.Err(); err != nil {
		return call.reject(err)
	}
	if !call.dispatched.CompareAndSwap(false, true) {
		return errors.New("automatic MCP tool call replay is forbidden")
	}
	return nil
}

type sdkOutcomeUnknownError struct{ cause error }

func (e *sdkOutcomeUnknownError) Error() string {
	return "MCP tool outcome is unknown after dispatch; do not automatically repeat the action: " + e.cause.Error()
}
func (e *sdkOutcomeUnknownError) Unwrap() error { return e.cause }

type sdkInputRequiredError struct{ raw json.RawMessage }

func (*sdkInputRequiredError) Error() string {
	return "MCP tool requires additional input; host mediation is required before any retry"
}

// A cancelled response wait stays request-scoped. A blocked frame write closes
// the connection because a partially written frame cannot be reused safely.
type guardedSDKConnection struct {
	sdk.Connection
	writeGate  chan struct{}
	beforeSend func(context.Context, *jsonrpc.Request, int64) error
}

func (c *guardedSDKConnection) Write(ctx context.Context, msg jsonrpc.Message) error {
	select {
	case c.writeGate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-c.writeGate }()
	if response, ok := msg.(*jsonrpc.Response); ok && ctx.Value(legacyReplyKey{}) != nil {
		if err := authorizeLegacyReply(ctx, response, true); err != nil {
			return err
		}
	}
	if request, ok := msg.(*jsonrpc.Request); ok {
		if c.beforeSend != nil {
			raw, err := jsonrpc.EncodeMessage(msg)
			if err != nil {
				return err
			}
			if err := c.beforeSend(ctx, request, int64(len(raw)+1)); err != nil {
				return err
			}
		}
		// Final dispatch guard: after acquiring the writer, before SDK framing.
		if err := beforeSDKDispatch(ctx, request.Method); err != nil {
			return err
		}
	}
	done := make(chan error, 1)
	go func() { done <- c.Connection.Write(ctx, msg) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = c.Connection.Close()
		// Close interrupts the socket/pipe, but its writer may still be unwinding.
		// Keep the gate until it exits so another SDK frame cannot overlap it.
		<-done
		return ctx.Err()
	}
}

// Preserve errors.Is/errors.As without exposing unsanitized diagnostics.
type sanitizedClientError struct {
	message string
	cause   error
}

func (e *sanitizedClientError) Error() string { return e.message }
func (e *sanitizedClientError) Unwrap() error { return e.cause }
