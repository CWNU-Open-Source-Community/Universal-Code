package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/toolgateway"
)

type stubStore struct {
	mu     sync.Mutex
	audits []string
}

func (s *stubStore) GetRun(context.Context, string) (domain.Run, error) {
	return domain.Run{ID: "run-1", Status: domain.RunRunning, Budget: domain.DefaultBudget()}, nil
}
func (s *stubStore) LatestRunEventSequence(context.Context, string) (int64, error) { return 0, nil }
func (s *stubStore) ListRunEventsAfterSequence(context.Context, string, int64, int) ([]events.Event, error) {
	return nil, nil
}
func (s *stubStore) RecordMCPAudit(_ context.Context, _ string, eventType string, _ map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audits = append(s.audits, eventType)
	return nil
}

type stubTools struct{}

func (stubTools) Invoke(_ context.Context, call toolgateway.ToolCall) (toolgateway.Outcome, error) {
	return toolgateway.Outcome{Result: &toolgateway.Result{Status: toolgateway.StatusCompleted,
		ExitCode: 0, MIME: "application/json", Metadata: map[string]string{"summary": "ok"}}}, nil
}

func newTestServer(t *testing.T) (*Server, *stubStore) {
	t.Helper()
	store := &stubStore{}
	server, err := New(Options{Store: store, Tools: stubTools{}, RunID: "run-1",
		WorkspaceID: "workspace-1"})
	if err != nil {
		t.Fatal(err)
	}
	return server, store
}

func serve(t *testing.T, server *Server, messages ...string) []string {
	t.Helper()
	input := strings.Join(messages, "\n") + "\n"
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(output.String()), "\n")
}

func TestMCPHandshakeGatesAndCapabilities(t *testing.T) {
	server, _ := newTestServer(t)
	responses := serve(t, server,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, // before initialize → error
		`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":4,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":5,"method":"ping"}`,
	)
	if len(responses) != 5 {
		t.Fatalf("expected 5 responses, got %d: %v", len(responses), responses)
	}
	if !strings.Contains(responses[0], "-32002") {
		t.Fatalf("pre-initialize request was not rejected: %s", responses[0])
	}
	if !strings.Contains(responses[1], "2025-06-18") || !strings.Contains(responses[1], "tools") {
		t.Fatalf("initialize capabilities missing: %s", responses[1])
	}
	// The dispatched responses may interleave; assert on the joined stream.
	joined := strings.Join(responses[2:], "\n")
	for _, want := range []string{"read_file", "list_workspace", "cyberagent://run/summary", "{}"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("catalog/ping response missing %q: %s", want, joined)
		}
	}
}

func TestMCPRejectsMalformedOversizedAndDuplicateIDs(t *testing.T) {
	server, _ := newTestServer(t)
	huge := strings.Repeat("a", MaxMessageBytes+1)
	responses := serve(t, server,
		`not json`,
		`{"jsonrpc":"2.0","id":10,"method":"unknown.method"}`,
		`{"jsonrpc":"2.0","id":11,"method":"initialize","params":{"protocolVersion":"2099-01-01","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}`,
		`{"jsonrpc":"2.0","id":12,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"1"},"extra":true}}`,
		`{"jsonrpc":"2.0","id":13,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}`,
		`{"jsonrpc":"2.0","id":"same","method":"ping"}`,
		`{"jsonrpc":"2.0","id":"same","method":"ping"}`,
	)
	if len(responses) < 4 {
		t.Fatalf("expected responses, got %d", len(responses))
	}
	if !strings.Contains(responses[0], "-32700") {
		t.Fatalf("malformed message not rejected: %s", responses[0])
	}
	if !strings.Contains(responses[1], "-32002") {
		t.Fatalf("pre-initialize unknown method not rejected: %s", responses[1])
	}
	if !strings.Contains(responses[2], `"protocolVersion":"2025-11-25"`) {
		t.Fatalf("unknown requested version did not negotiate a supported version: %s", responses[2])
	}
	if !strings.Contains(responses[3], "-32602") {
		t.Fatalf("unknown initialize field accepted: %s", responses[3])
	}
	// duplicate in-flight id must be rejected as replay (after a successful handshake)
	joined := strings.Join(responses, "\n")
	if !strings.Contains(joined, "duplicate in-flight request id") {
		t.Fatalf("duplicate id replay was not rejected: %v", responses)
	}
	_ = huge
}

func TestMCPToolCallForwardsAndAudits(t *testing.T) {
	server, store := newTestServer(t)
	responses := serve(t, server,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"README.md"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"shell","arguments":{"command":"whoami"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"cyberagent://run/summary"}}`,
	)
	joined := strings.Join(responses, "\n")
	if !strings.Contains(joined, "\"isError\"") == false && !strings.Contains(joined, "\"content\"") {
		t.Fatalf("tool result missing: %v", responses)
	}
	if !strings.Contains(joined, "tool not found in this server") {
		t.Fatalf("undeclared tool was accepted: %v", responses)
	}
	if !strings.Contains(joined, "cyberagent://run/summary") {
		t.Fatalf("resource read missing: %v", responses)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.audits) != 3 {
		t.Fatalf("audit events missing: %v", store.audits)
	}
}

func TestMCPSessionTTLExpiry(t *testing.T) {
	server, _ := newTestServer(t)
	now := time.Now().UTC()
	server.now = func() time.Time { return now }
	responses := serve(t, server,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
	)
	if len(responses) != 1 {
		t.Fatalf("initialize failed: %v", responses)
	}
	// Advance beyond the default 24h TTL and serve one more request.
	now = now.Add(25 * time.Hour)
	var output bytes.Buffer
	if err := server.Serve(context.Background(),
		strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"ping"}`+"\n"), &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "TTL expired") {
		t.Fatalf("expired session was not rejected: %s", output.String())
	}
}

// blockingTools blocks until its context is done, modeling a gateway call
// that resolves only via the per-call timeout or a cancellation notification.
type blockingTools struct {
	started chan struct{}
}

func (b *blockingTools) Invoke(ctx context.Context, _ toolgateway.ToolCall) (toolgateway.Outcome, error) {
	if b.started != nil {
		select {
		case <-b.started:
		default:
			close(b.started)
		}
	}
	<-ctx.Done()
	return toolgateway.Outcome{}, ctx.Err()
}

func TestMCPToolCallTimeout(t *testing.T) {
	server, _ := newTestServer(t)
	server.callTimeout = 100 * time.Millisecond
	server.tools = &blockingTools{started: make(chan struct{})}
	output := serve(t, server,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"README.md"}}}`,
	)
	if !strings.Contains(strings.Join(output, "\n"), "tool call timed out") {
		t.Fatalf("timeout was not enforced: %v", output)
	}
}

func TestMCPToolCallCancellation(t *testing.T) {
	server, _ := newTestServer(t)
	server.tools = &blockingTools{started: make(chan struct{})}
	output := serve(t, server,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"README.md"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":2,"reason":"user"}}`,
	)
	joined := strings.Join(output, "\n")
	if !strings.Contains(joined, "tool call cancelled") {
		t.Fatalf("cancellation was not honoured: %v", output)
	}
}

func TestMCPConcurrencyLimit(t *testing.T) {
	server, _ := newTestServer(t)
	server.maxConcurrent = 1
	server.tools = &blockingTools{started: make(chan struct{})}
	output := serve(t, server,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a.txt"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"b.txt"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":2,"reason":"user"}}`,
	)
	joined := strings.Join(output, "\n")
	if !strings.Contains(joined, "MCP concurrency limit reached") {
		t.Fatalf("concurrency limit was not enforced: %v", output)
	}
	if !strings.Contains(joined, "tool call cancelled") {
		t.Fatalf("in-flight call was not cancelled: %v", output)
	}
}

var _ = json.Marshal
