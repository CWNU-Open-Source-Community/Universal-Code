package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/toolcontract"
	"github.com/gorilla/websocket"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type acceptedWebSocketTransport struct{ sdk.Connection }

func (t acceptedWebSocketTransport) Connect(context.Context) (sdk.Connection, error) {
	return t.Connection, nil
}

func remoteCompatibilityPeer(t *testing.T, kind TransportKind, calls *atomic.Int32) (*httptest.Server, string) {
	t.Helper()
	server := sdk.NewServer(&sdk.Implementation{Name: "compatible-peer", Version: "1"}, nil)
	server.AddTool(&sdk.Tool{Name: "lookup", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		calls.Add(1)
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "compatible-result"}}}, nil
	})
	var handler http.Handler = sdk.NewSSEHandler(func(*http.Request) *sdk.Server { return server }, nil)
	if kind == TransportWebSocket {
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			upgrader := websocket.Upgrader{}
			connection, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer connection.Close()
			session, err := server.Connect(r.Context(), acceptedWebSocketTransport{&webSocketClientConnection{connection: connection}}, nil)
			if err != nil {
				t.Error(err)
				return
			}
			_ = session.Wait()
		})
	}
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer transport-private-token" || r.Header.Get("X-Project") != "fixture" {
			t.Error("remote authentication or configured header missing")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	endpoint := peer.URL
	if kind == TransportWebSocket {
		endpoint = "wss" + strings.TrimPrefix(endpoint, "https")
	}
	return peer, endpoint
}

func TestAdditionalRemoteTransportsDiscoverCallAndGuard(t *testing.T) {
	for _, kind := range []TransportKind{TransportSSE, TransportWebSocket} {
		t.Run(string(kind), func(t *testing.T) {
			var calls atomic.Int32
			peer, endpoint := remoteCompatibilityPeer(t, kind, &calls)
			defer peer.Close()
			d := clientTransportDescriptor(kind, endpoint, nil)
			d.ProtocolVersion = ConfigClientProtocolVersion
			d.Headers = map[string]string{"X-Project": "fixture"}
			transport, err := newRemoteClientTransport(d, "transport-private-token", peer.Client())
			if err != nil {
				t.Fatal(err)
			}
			client := newClient(transport, d)
			defer client.Close()
			var disabled atomic.Bool
			client.remote.beforeSend = func(_ context.Context, frame *jsonrpc.Request, _ int64) error {
				if frame.Method == "tools/call" && disabled.Load() {
					return errors.New("operator disabled the server")
				}
				return nil
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			capabilities, err := client.Discover(ctx, time.Now())
			if err != nil || len(capabilities.Tools) != 1 {
				t.Fatalf("discovery=%#v err=%v", capabilities, err)
			}
			// Discover's handshake context has ended. SSE must keep its receive
			// stream open, and the subsequent RPC must use its own authority.
			result, err := client.CallTool(ctx, "lookup", json.RawMessage(`{}`), 4096)
			if err != nil || !strings.Contains(result.Content, "compatible-result") || calls.Load() != 1 {
				t.Fatalf("call=%#v err=%v count=%d", result, err, calls.Load())
			}
			disabled.Store(true)
			if _, err = client.CallTool(ctx, "lookup", json.RawMessage(`{}`), 4096); err == nil || calls.Load() != 1 {
				t.Fatal("disabled operation reached peer")
			}
		})
	}
}

func TestAdditionalRemoteTransportsResolvedProfiles(t *testing.T) {
	for _, kind := range []TransportKind{TransportSSE, TransportWebSocket} {
		t.Run(string(kind), func(t *testing.T) {
			var calls atomic.Int32
			peer, endpoint := remoteCompatibilityPeer(t, kind, &calls)
			defer peer.Close()
			declaration, host := testResolvedDeclaration(t)
			declaration.Transport, declaration.Stdio = toolcontract.Transport(kind), nil
			declaration.HTTP = &toolcontract.HTTPLaunch{Endpoint: endpoint, Headers: map[string]string{
				"Authorization": "Bearer transport-private-token", "X-Project": "fixture",
			}}
			host.ProtocolVersions = []string{preferredClientProtocolVersion, "2025-11-25", legacyClientProtocolVersion}
			launch, err := ResolveLaunch(declaration, host)
			if err != nil || len(launch.ProtocolVersions) != 2 || launch.ProtocolVersions[0] != "2025-11-25" {
				t.Fatalf("transport-compatible profiles=%v err=%v", launch.ProtocolVersions, err)
			}
			client := resolvedFixtureClient(t, launch, &resolvedGuardProbe{}, peer.Client())
			defer client.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			capabilities, err := client.DiscoverWithScope(ctx, testResolvedScope(t, launch, 3))
			if err != nil || capabilities.ProtocolVersion != "2025-11-25" || len(capabilities.Tools) != 1 {
				t.Fatalf("resolved discovery=%#v err=%v", capabilities, err)
			}
			result, native, receipt, err := client.CallToolWithReceipt(ctx, resolvedFixtureCall(t, client, launch, "{}"), "lookup", json.RawMessage(`{}`))
			if err != nil || receipt.State != toolcontract.ReceiptResultReceived || !strings.Contains(string(native), "compatible-result") || calls.Load() != 1 {
				t.Fatalf("resolved call=%#v receipt=%#v err=%v", result, receipt, err)
			}
			declaration.ProtocolVersions = []string{preferredClientProtocolVersion}
			if _, err := ResolveLaunch(declaration, host); err == nil {
				t.Fatal("unsupported explicit transport profile accepted")
			}
			launch.ProtocolVersions = []string{preferredClientProtocolVersion}
			if _, _, err := newLaunchClient(launch, peer.Client(), nil, nil); err == nil {
				t.Fatal("resolved transport accepted an unsupported profile")
			}
		})
	}
}

func TestAdditionalRemoteTransportsDoNotForwardCredentials(t *testing.T) {
	for _, kind := range []TransportKind{TransportSSE, TransportWebSocket} {
		t.Run(string(kind), func(t *testing.T) {
			var forwarded atomic.Int32
			other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1); w.WriteHeader(400) }))
			defer other.Close()
			peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if kind == TransportSSE {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", other.URL)
					w.(http.Flusher).Flush()
					<-r.Context().Done()
				} else {
					http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
				}
			}))
			defer peer.Close()
			endpoint := peer.URL
			if kind == TransportWebSocket {
				endpoint = "wss" + strings.TrimPrefix(endpoint, "https")
			}
			d := clientTransportDescriptor(kind, endpoint, nil)
			d.ProtocolVersion = ConfigClientProtocolVersion
			transport, err := newRemoteClientTransport(d, "transport-private-token", peer.Client())
			if err != nil {
				t.Fatal(err)
			}
			client := newClient(transport, d)
			defer client.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			if _, err := client.Discover(ctx, time.Now()); err == nil || forwarded.Load() != 0 {
				t.Fatal("cross-origin endpoint/redirect accepted")
			}
		})
	}
}

func TestAdditionalRemoteTransportDescriptorBoundaries(t *testing.T) {
	for _, kind := range []TransportKind{TransportSSE, TransportWebSocket} {
		endpoints := []string{"https://example.org/mcp", "http://127.0.0.1/mcp"}
		if kind == TransportWebSocket {
			endpoints = []string{"wss://example.org/mcp", "ws://127.0.0.1/mcp"}
		}
		for _, endpoint := range endpoints {
			d := clientTransportDescriptor(kind, endpoint, nil)
			if d.Validate() == nil {
				t.Fatal("new transport accepted by historical codec")
			}
			d.ProtocolVersion = ConfigClientProtocolVersion
			if err := d.Validate(); err != nil {
				t.Fatal(err)
			}
			launch, err := resolveManualLaunch(d, []string{legacyClientProtocolVersion}, nil)
			if err != nil || string(launch.Transport) != string(kind) {
				t.Fatalf("launch=%#v err=%v", launch, err)
			}
		}
		for _, endpoint := range []string{"http://example.org/mcp", "ws://example.org/mcp", "wss://name:password@example.org/mcp", "https://example.org/mcp?token=x"} {
			d := clientTransportDescriptor(kind, endpoint, nil)
			d.ProtocolVersion = ConfigClientProtocolVersion
			if d.Validate() == nil {
				t.Fatalf("unsafe endpoint accepted: %s", endpoint)
			}
		}
	}
}
