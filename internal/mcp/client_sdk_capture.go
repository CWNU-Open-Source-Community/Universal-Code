package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Observe already framed SDK messages in memory; never log remote payloads.
// This preserves native extensions and arbitrary JSON numeric precision.
type sdkRawResponseKey struct{}
type sdkRawResponse struct {
	mu       sync.Mutex
	raw      json.RawMessage
	rpcError *jsonrpc.Error
}

func (r *sdkRawResponse) result() (json.RawMessage, *jsonrpc.Error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return bytes.Clone(r.raw), r.rpcError
}

type sdkPendingResponse struct {
	method string
	target *sdkRawResponse
}
type sdkResponseTap struct {
	mu             sync.Mutex
	pending        map[jsonrpc.ID]sdkPendingResponse
	negotiated     func(string)
	acceptProtocol func(string) error
	receiveInput   func(*jsonrpc.Request)
	prepareReply   func(context.Context, *jsonrpc.Response) (context.Context, error)
}

func (t *sdkResponseTap) forget(target *sdkRawResponse) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, value := range t.pending {
		if value.target == target {
			delete(t.pending, id)
		}
	}
}

type sdkObservingTransport struct {
	sdk.Transport
	tap *sdkResponseTap
}

func (t *sdkObservingTransport) Connect(ctx context.Context) (sdk.Connection, error) {
	conn, err := t.Transport.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &sdkObservingConnection{Connection: conn, tap: t.tap}, nil
}

type sdkObservingConnection struct {
	sdk.Connection
	tap *sdkResponseTap
}

func (c *sdkObservingConnection) Write(ctx context.Context, msg jsonrpc.Message) error {
	if response, ok := msg.(*jsonrpc.Response); ok && c.tap.prepareReply != nil {
		var err error
		ctx, err = c.tap.prepareReply(ctx, response)
		if err != nil {
			return err
		}
	}
	if request, ok := msg.(*jsonrpc.Request); ok && request.IsCall() {
		target, _ := ctx.Value(sdkRawResponseKey{}).(*sdkRawResponse)
		c.tap.mu.Lock()
		c.tap.pending[request.ID] = sdkPendingResponse{method: request.Method, target: target}
		c.tap.mu.Unlock()
	}
	return c.Connection.Write(ctx, msg)
}
func (c *sdkObservingConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	msg, err := c.Connection.Read(ctx)
	if request, ok := msg.(*jsonrpc.Request); ok && c.tap.receiveInput != nil {
		c.tap.receiveInput(request)
	}
	if response, ok := msg.(*jsonrpc.Response); ok {
		c.tap.mu.Lock()
		pending, found := c.tap.pending[response.ID]
		delete(c.tap.pending, response.ID)
		c.tap.mu.Unlock()
		if found {
			if pending.target != nil {
				pending.target.mu.Lock()
				if response.Error == nil {
					pending.target.raw = bytes.Clone(response.Result)
				} else if rpcError, ok := response.Error.(*jsonrpc.Error); ok {
					// Only a wire error observed by the SDK transport proves a
					// remote response. Local SDK call errors can have the same type.
					copy := *rpcError
					pending.target.rpcError = &copy
				}
				pending.target.mu.Unlock()
			}
			if response.Error == nil && pending.method == "initialize" {
				var result struct {
					ProtocolVersion string `json:"protocolVersion"`
				}
				if json.Unmarshal(response.Result, &result) == nil {
					if c.tap.acceptProtocol != nil {
						if err := c.tap.acceptProtocol(result.ProtocolVersion); err != nil {
							return nil, err
						}
					}
					if c.tap.negotiated != nil && supportedClientProtocol(result.ProtocolVersion) {
						c.tap.negotiated(result.ProtocolVersion)
					}
				}
			}
		}
	}
	return msg, err
}
