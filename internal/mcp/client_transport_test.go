package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type blockingMCPWriteCloser struct {
	closed   chan struct{}
	entered  chan struct{}
	returned chan struct{}
}

func (w *blockingMCPWriteCloser) Write(context.Context, jsonrpc.Message) error {
	close(w.entered)
	defer close(w.returned)
	<-w.closed
	return io.ErrClosedPipe
}

func (w *blockingMCPWriteCloser) Read(ctx context.Context) (jsonrpc.Message, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-w.closed:
		return nil, io.EOF
	}
}
func (*blockingMCPWriteCloser) SessionID() string { return "" }

func (w *blockingMCPWriteCloser) Close() error {
	select {
	case <-w.closed:
	default:
		close(w.closed)
	}
	return nil
}

func TestRemoteClientTransportPerformsTLSHandshakeDiscoveryAndCall(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer fixture-token" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		var envelope Envelope
		if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
			http.Error(writer, "invalid", http.StatusBadRequest)
			return
		}
		if len(envelope.ID) == 0 {
			writer.WriteHeader(http.StatusAccepted)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Mcp-Session-Id", "session-one")
		_, _ = writer.Write(clientFixtureResponse(envelope))
	}))
	defer server.Close()
	descriptor := clientTransportDescriptor(TransportStreamableHTTP, server.URL, nil)
	transport, err := newRemoteClientTransport(descriptor, "fixture-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	client := newClient(transport, descriptor)
	defer client.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	capabilities, err := client.Discover(ctx, time.Now())
	if err != nil || len(capabilities.Tools) != 1 || capabilities.Tools[0].Name != "lookup" {
		t.Fatalf("remote discovery=%#v err=%v", capabilities, err)
	}
	result, err := client.CallTool(ctx, "lookup", json.RawMessage(`{"query":"one"}`), 4096)
	if err != nil || !strings.Contains(result.Content, "remote-result") || requests < 4 {
		t.Fatalf("remote call=%#v requests=%d err=%v", result, requests, err)
	}
}

func TestStdioClientTransportRunsApprovedAbsoluteExecutable(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	descriptor := clientTransportDescriptor(TransportStdio, executable,
		[]string{"-test.run=^TestMCPStdioHelperProcess$", "--", "mcp-stdio-helper"})
	transport, err := newStdioClientTransport(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	client := newClient(transport, descriptor)
	defer client.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	capabilities, err := client.Discover(ctx, time.Now())
	if err != nil || len(capabilities.Tools) != 1 {
		t.Fatalf("stdio discovery=%#v err=%v", capabilities, err)
	}
	result, err := client.CallTool(ctx, "lookup", json.RawMessage(`{"query":"two"}`), 4096)
	if err != nil || !strings.Contains(result.Content, "stdio-result") {
		t.Fatalf("stdio call=%#v err=%v", result, err)
	}
}

func TestStdioClientTransportCancelsBlockedWrite(t *testing.T) {
	writer := &blockingMCPWriteCloser{closed: make(chan struct{}), entered: make(chan struct{}), returned: make(chan struct{})}
	transport := &guardedSDKConnection{Connection: writer, writeGate: make(chan struct{}, 1)}
	defer transport.Close()
	deadline, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	ctx, cancel := context.WithCancel(deadline)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- transport.Write(ctx, &jsonrpc.Request{Method: "notifications/initialized"}) }()
	select {
	case <-writer.entered:
	case <-deadline.Done():
		t.Fatal("stdio write was not entered")
	}
	// The SDK Write is still blocked. Cancellation must fail the connection
	// closed, since a partly written frame cannot be shared with another call.
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked stdio write error=%v, want cancellation", err)
		}
	case <-deadline.Done():
		t.Fatal("blocked stdio write was not cancelled")
	}
	select {
	case <-writer.returned:
	case <-deadline.Done():
		t.Fatal("cancelled write did not close the pipe and release its writer")
	}
}

// Close can interrupt I/O before its writer finishes unwinding. A queued SDK
// notification must not enter the same writer during that interval.
type delayedExitMCPWriter struct {
	*blockingMCPWriteCloser
	exit   chan struct{}
	second chan struct{}
	writes atomic.Int32
}

func (w *delayedExitMCPWriter) Write(context.Context, jsonrpc.Message) error {
	if w.writes.Add(1) != 1 {
		close(w.second)
		return io.ErrClosedPipe
	}
	close(w.entered)
	defer close(w.returned)
	<-w.closed
	<-w.exit
	return io.ErrClosedPipe
}

func TestGuardedClientTransportCancellationWaitsForWriterExit(t *testing.T) {
	writer := &delayedExitMCPWriter{blockingMCPWriteCloser: &blockingMCPWriteCloser{
		closed: make(chan struct{}), entered: make(chan struct{}), returned: make(chan struct{})},
		exit: make(chan struct{}), second: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(writer.exit) })
	transport := &guardedSDKConnection{Connection: writer, writeGate: make(chan struct{}, 1)}
	defer transport.Close()
	deadline, stop := context.WithTimeout(t.Context(), 3*time.Second)
	defer stop()
	ctx, cancel := context.WithCancel(deadline)
	defer cancel()
	firstDone := make(chan error, 1)
	go func() { firstDone <- transport.Write(ctx, &jsonrpc.Request{Method: "notifications/initialized"}) }()
	select {
	case <-writer.entered:
	case <-deadline.Done():
		t.Fatal("first writer did not enter")
	}
	cancel()
	select {
	case <-writer.closed:
	case <-deadline.Done():
		t.Fatal("cancel did not close connection")
	}
	secondDone := make(chan error, 1)
	go func() { secondDone <- transport.Write(deadline, &jsonrpc.Request{Method: "notifications/initialized"}) }()
	select {
	case <-writer.second:
		t.Fatal("queued write overlapped the cancelled writer")
	case <-firstDone:
		t.Fatal("cancelled write returned before its writer exited")
	case <-time.After(50 * time.Millisecond):
	}
	release.Do(func() { close(writer.exit) })
	select {
	case err := <-firstDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("first write: %v", err)
		}
	case <-deadline.Done():
		t.Fatal("first write did not finish")
	}
	select {
	case err := <-secondDone:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("queued write: %v", err)
		}
	case <-deadline.Done():
		t.Fatal("queued write did not finish")
	}
}

// This observer sits outside the real stdio adapter. Peer progress alone does
// not prove that the local SDK Write has returned or that its gate was released.
type completedMCPWriteTransport struct {
	sdk.Transport
	toolWritten chan struct{}
}

func (t *completedMCPWriteTransport) Connect(ctx context.Context) (sdk.Connection, error) {
	connection, err := t.Transport.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &completedMCPWriteConnection{Connection: connection, toolWritten: t.toolWritten}, nil
}

type completedMCPWriteConnection struct {
	sdk.Connection
	toolWritten chan struct{}
	once        sync.Once
}

func (c *completedMCPWriteConnection) Write(ctx context.Context, message jsonrpc.Message) error {
	err := c.Connection.Write(ctx, message)
	if request, ok := message.(*jsonrpc.Request); err == nil && ok && request.Method == "tools/call" {
		c.once.Do(func() { close(c.toolWritten) })
	}
	return err
}

func TestMCPStdioHelperProcess(t *testing.T) {
	if !slices.Contains(os.Args, "mcp-stdio-helper") {
		t.Skip("helper subprocess only")
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), MaxMessageBytes)
	for scanner.Scan() {
		envelope, err := DecodeEnvelope(scanner.Bytes())
		if err != nil {
			return
		}
		if len(envelope.ID) == 0 {
			continue
		}
		response := clientFixtureResponseWithText(envelope, "stdio-result")
		_, _ = fmt.Fprintln(os.Stdout, string(response))
	}
}

func clientTransportDescriptor(kind TransportKind, target string,
	arguments []string,
) ServerDescriptor {
	return ServerDescriptor{ProtocolVersion: ClientProtocolVersion, ID: "fixture",
		Name: "Fixture", Transport: kind, Target: target, Arguments: arguments,
		DeclaredCapabilities: []CapabilityKind{CapabilityTools}, Scope: ScopeWorkspace,
		WorkspaceID: "workspace-1", Source: Source{Kind: "manual", URI: "test://fixture"},
		CallTimeoutMillis: 5_000, MaxResultBytes: 4096}
}

func clientFixtureResponse(request Envelope) []byte {
	return clientFixtureResponseWithText(request, "remote-result")
}

func clientFixtureResponseWithText(request Envelope, text string) []byte {
	var result any
	switch request.Method {
	case "initialize":
		result = map[string]any{"protocolVersion": ProtocolVersion,
			"capabilities": map[string]any{"tools": map[string]any{}},
			"serverInfo":   map[string]string{"name": "fixture", "version": "1.0.0"}}
	case "tools/list":
		result = map[string]any{"tools": []map[string]any{{"name": "lookup",
			"description": "Look up a fixture.",
			"inputSchema": json.RawMessage(`{"type":"object","additionalProperties":false,"required":["query"],"properties":{"query":{"type":"string"}}}`)}}}
	case "tools/call":
		result = map[string]any{"content": []map[string]string{{"type": "text", "text": text}}}
	default:
		result = map[string]any{}
	}
	resultRaw, _ := json.Marshal(result)
	responseRaw, _ := json.Marshal(Envelope{JSONRPC: "2.0",
		ID: append(json.RawMessage(nil), request.ID...), Result: resultRaw})
	return responseRaw
}
