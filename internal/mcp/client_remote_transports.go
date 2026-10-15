package mcp

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Legacy SSE keeps its initial GET open for the entire session. The handshake
// context limits connection setup; individual RPC contexts govern later calls.
type persistentSSETransport struct{ transport *sdk.SSEClientTransport }

func (t *persistentSSETransport) Connect(ctx context.Context) (sdk.Connection, error) {
	lifetime, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, cancel)
	connection, err := t.transport.Connect(lifetime)
	stop()
	if err != nil || ctx.Err() != nil {
		cancel()
		if connection != nil {
			_ = connection.Close()
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return &cancelClientConnection{Connection: connection, cancel: cancel}, nil
}

type cancelClientConnection struct {
	sdk.Connection
	cancel context.CancelFunc
}

func (c *cancelClientConnection) Close() error {
	c.cancel()
	return c.Connection.Close()
}

type webSocketClientTransport struct {
	endpoint string
	remote   *mcpHTTPTransport
}

func (t *webSocketClientTransport) Connect(ctx context.Context) (sdk.Connection, error) {
	remote := t.remote
	if remote.beforeConnect != nil {
		if err := remote.beforeConnect(ctx); err != nil {
			return nil, err
		}
	}
	headers := make(http.Header, len(remote.headers)+1)
	for name, values := range remote.headers {
		if !hostControlledMCPHeader(name) && !isWebSocketHeader(name) {
			headers[name] = append([]string(nil), values...)
		}
	}
	bearer := remote.bearer
	if remote.resolveBearer != nil {
		var err error
		bearer, err = remote.resolveBearer(ctx)
		if err != nil {
			return nil, err
		}
	}
	if bearer != "" {
		headers.Set("Authorization", "Bearer "+bearer)
	}
	dialer := websocket.Dialer{HandshakeTimeout: 30 * time.Second}
	// Reuse the host's TLS trust and explicit proxy/dial policy. WebSocket
	// handshakes do not follow redirects and never forward credentials elsewhere.
	if base, ok := remote.base.(*http.Transport); ok {
		dialer.Proxy, dialer.NetDialContext = base.Proxy, base.DialContext
		if base.TLSClientConfig != nil {
			dialer.TLSClientConfig = base.TLSClientConfig.Clone()
		}
	} else {
		return nil, errors.New("MCP WebSocket requires an HTTP transport with explicit TLS and dial settings")
	}
	connection, response, err := dialer.DialContext(ctx, t.endpoint, headers)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	connection.SetReadLimit(MaxMessageBytes)
	return &guardedSDKConnection{Connection: &webSocketClientConnection{connection: connection},
		writeGate: make(chan struct{}, 1), beforeSend: remote.beforeSend}, nil
}

func isWebSocketHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Sec-Websocket-Key", "Sec-Websocket-Version", "Sec-Websocket-Extensions", "Sec-Websocket-Protocol", "Sec-Websocket-Accept":
		return true
	}
	return false
}

type webSocketClientConnection struct {
	connection *websocket.Conn
	closeOnce  sync.Once
	closeErr   error
}

func (c *webSocketClientConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	kind, data, err := c.connection.ReadMessage()
	if err != nil {
		return nil, err
	}
	if kind != websocket.TextMessage {
		return nil, errors.New("MCP WebSocket requires JSON text messages")
	}
	return jsonrpc.DecodeMessage(data)
}

func (c *webSocketClientConnection) Write(ctx context.Context, message jsonrpc.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := jsonrpc.EncodeMessage(message)
	if err != nil {
		return err
	}
	if len(raw) > MaxMessageBytes {
		return errors.New("MCP WebSocket request exceeds its size bound")
	}
	return c.connection.WriteMessage(websocket.TextMessage, raw)
}

func (c *webSocketClientConnection) SessionID() string { return "" }
func (c *webSocketClientConnection) Close() error {
	c.closeOnce.Do(func() { c.closeErr = c.connection.Close() })
	return c.closeErr
}
