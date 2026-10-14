package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/runactivity"
	"cyberagent-workbench/internal/toolgateway"
)

// Store is the bounded read surface the server may touch. There is no
// direct SQLite, Runner, or Policy bypass anywhere in this package.
type Store interface {
	GetRun(context.Context, string) (domain.Run, error)
	LatestRunEventSequence(context.Context, string) (int64, error)
	ListRunEventsAfterSequence(context.Context, string, int64, int) ([]events.Event, error)
	RecordMCPAudit(context.Context, string, string, map[string]any) error
}

// ToolRunner forwards typed actions through the existing Tool Gateway so
// Policy, Approval, budgets, and redaction stay authoritative.
type ToolRunner interface {
	Invoke(context.Context, toolgateway.ToolCall) (toolgateway.Outcome, error)
}

// Server is one MCP session. The stdio transport is the only transport;
// the server never listens on a socket.
type Server struct {
	store         Store
	tools         ToolRunner
	runID         string
	workspaceID   string
	sessionTTL    time.Duration
	callTimeout   time.Duration
	maxConcurrent int
	clientName    string
	clientVersion string
	initialized   bool
	initializedAt time.Time
	mu            sync.Mutex
	writeMu       sync.Mutex
	inFlight      map[string]context.CancelFunc
	seenIDs       map[string]struct{}
	now           func() time.Time
}

type Options struct {
	Store         Store
	Tools         ToolRunner
	RunID         string
	WorkspaceID   string
	SessionTTL    time.Duration
	CallTimeout   time.Duration
	MaxConcurrent int
}

func New(options Options) (*Server, error) {
	if options.Store == nil || options.Tools == nil {
		return nil, errors.New("MCP server requires a Store and a Tool Gateway")
	}
	if strings.TrimSpace(options.RunID) == "" || strings.TrimSpace(options.WorkspaceID) == "" {
		return nil, errors.New("MCP server requires a Run and Workspace scope")
	}
	if options.SessionTTL <= 0 || options.SessionTTL > 24*time.Hour {
		options.SessionTTL = 24 * time.Hour
	}
	if options.CallTimeout <= 0 || options.CallTimeout > 5*time.Minute {
		options.CallTimeout = 30 * time.Second
	}
	if options.MaxConcurrent < 1 || options.MaxConcurrent > 16 {
		options.MaxConcurrent = 8
	}
	return &Server{store: options.Store, tools: options.Tools, runID: strings.TrimSpace(options.RunID),
		workspaceID: strings.TrimSpace(options.WorkspaceID), sessionTTL: options.SessionTTL,
		callTimeout: options.CallTimeout, maxConcurrent: options.MaxConcurrent,
		inFlight: make(map[string]context.CancelFunc), seenIDs: make(map[string]struct{}),
		now: func() time.Time { return time.Now().UTC() }}, nil
}

// Serve runs the stdio loop until stdin closes. Requests are dispatched to
// worker goroutines bounded by maxConcurrent, so an in-flight call does not
// block the read loop: timeouts fire and notifications/cancelled reach the
// call it names. All responses are written as newline-delimited JSON;
// logging and errors stay on stderr only.
func (s *Server) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), MaxMessageBytes)
	var wg sync.WaitGroup
	defer wg.Wait()
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		raw := append([]byte(nil), scanner.Bytes()...)
		envelope, err := DecodeEnvelope(raw)
		if err != nil {
			_ = s.write(output, Envelope{JSONRPC: "2.0", ID: json.RawMessage("null"),
				Error: &RPCError{Code: CodeParseError, Message: err.Error()}})
			continue
		}
		request, isRequest, err := DecodeRequest(envelope)
		if err != nil {
			_ = s.write(output, Envelope{JSONRPC: "2.0", ID: envelope.ID,
				Error: &RPCError{Code: CodeInvalidRequest, Message: err.Error()}})
			continue
		}
		if !isRequest {
			s.handleNotification(ctx, envelope, output)
			continue
		}
		// initialize stays synchronous so the handshake gate is deterministic:
		// nothing else is dispatched until the capability response is written.
		if request.Method == "initialize" {
			s.mu.Lock()
			_, duplicate := s.seenIDs[string(request.ID)]
			if !duplicate {
				s.seenIDs[string(request.ID)] = struct{}{}
			}
			s.mu.Unlock()
			if duplicate {
				_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID,
					Error: &RPCError{Code: CodeInvalidRequest, Message: "duplicate in-flight request id (replay rejected)"}})
				continue
			}
			callCtx, cancel := context.WithTimeout(ctx, s.callTimeout)
			s.handleRequest(callCtx, request, output)
			cancel()
			continue
		}
		// Pre-initialize requests are rejected inline so the gate cannot race
		// a concurrent initialize handshake.
		s.mu.Lock()
		initialized := s.initialized
		s.mu.Unlock()
		if !initialized {
			_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID,
				Error: &RPCError{Code: CodeNotInitialized, Message: "server requires initialize before other requests"}})
			continue
		}
		// Register the request before the next line is read, so a
		// notifications/cancelled for it deterministically finds it in-flight.
		key := string(request.ID)
		s.mu.Lock()
		if _, exists := s.seenIDs[key]; exists {
			s.mu.Unlock()
			_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID,
				Error: &RPCError{Code: CodeInvalidRequest, Message: "duplicate in-flight request id (replay rejected)"}})
			continue
		}
		if len(s.inFlight) >= s.maxConcurrent {
			s.mu.Unlock()
			_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID,
				Error: &RPCError{Code: CodeInternalError, Message: "MCP concurrency limit reached"}})
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, s.callTimeout)
		s.inFlight[key] = cancel
		s.seenIDs[key] = struct{}{}
		s.mu.Unlock()
		wg.Add(1)
		go func(request Request) {
			defer wg.Done()
			defer func() {
				s.mu.Lock()
				delete(s.inFlight, key)
				s.mu.Unlock()
				cancel()
			}()
			s.handleRequest(callCtx, request, output)
		}(request)
	}
	return scanner.Err()
}

// handleRequest runs one accepted request inside its call context; the read
// loop already registered it as in-flight and enforces the concurrency limit.
func (s *Server) handleRequest(ctx context.Context, request Request, output io.Writer) {
	s.mu.Lock()
	initialized := s.initialized
	initializedAt := s.initializedAt
	s.mu.Unlock()
	if request.Method != "initialize" && !initialized {
		_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID,
			Error: &RPCError{Code: CodeNotInitialized, Message: "server requires initialize before other requests"}})
		return
	}
	if initialized && s.now().Sub(initializedAt) > s.sessionTTL {
		_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID,
			Error: &RPCError{Code: CodeNotInitialized, Message: "MCP session capability TTL expired; re-initialize"}})
		return
	}
	switch request.Method {
	case "initialize":
		s.serveInitialize(ctx, request, output)
	case "tools/list":
		s.serveToolsList(ctx, request, output)
	case "tools/call":
		s.serveToolsCall(ctx, request, output)
	case "resources/list":
		s.serveResourcesList(ctx, request, output)
	case "resources/read":
		s.serveResourcesRead(ctx, request, output)
	case "resources/templates/list":
		_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID, Result: mustJSON(map[string]any{"resourceTemplates": []any{}})})
	case "ping":
		_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage("{}")})
	default:
		_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID,
			Error: &RPCError{Code: CodeMethodNotFound, Message: "method not found"}})
	}
}

func (s *Server) handleNotification(_ context.Context, envelope Envelope, output io.Writer) {
	switch envelope.Method {
	case "notifications/cancelled":
		var params CancelledNotification
		if envelope.Params == nil || json.Unmarshal(envelope.Params, &params) != nil {
			return
		}
		s.mu.Lock()
		cancel, ok := s.inFlight[string(params.RequestID)]
		s.mu.Unlock()
		if ok {
			cancel()
		}
	case "notifications/initialized":
		return // accepted no-op; the initialize response already carries state
	default:
		return // unknown notifications are ignored per JSON-RPC
	}
	_ = output
}

func (s *Server) serveInitialize(ctx context.Context, request Request, output io.Writer) {
	params, err := DecodeInitializeParams(request.Params)
	if err != nil {
		_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID,
			Error: &RPCError{Code: CodeInvalidParams, Message: err.Error()}})
		return
	}
	if _, err := s.store.GetRun(ctx, s.runID); err != nil {
		_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID,
			Error: &RPCError{Code: CodeInvalidParams, Message: "MCP Run scope is unavailable"}})
		return
	}
	s.mu.Lock()
	s.initialized = true
	s.initializedAt = s.now()
	s.clientName = params.ClientInfo.Name
	s.clientVersion = params.ClientInfo.Version
	s.mu.Unlock()
	result := InitializeResult{ProtocolVersion: ProtocolVersion,
		Capabilities: ServerCapabilities{Resources: &struct{}{}, Tools: &struct{}{}},
		ServerInfo:   ClientInfo{Name: ServerName, Version: ServerVersion}}
	_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID, Result: mustJSON(result)})
	_ = s.store.RecordMCPAudit(ctx, s.runID, "mcp.initialized", map[string]any{
		"client_name": s.clientName, "client_version": s.clientVersion,
	})
}

func (s *Server) write(output io.Writer, envelope Envelope) error {
	raw, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := output.Write(append(raw, '\n')); err != nil {
		return err
	}
	return nil
}

func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage("{}")
	}
	return raw
}

func (s *Server) activityResource(ctx context.Context) (runactivity.Projection, error) {
	latest, err := s.store.LatestRunEventSequence(ctx, s.runID)
	if err != nil {
		return runactivity.Projection{}, apperror.Normalize(err)
	}
	after := latest - runactivity.MaxSourceEvents
	if after < 0 {
		after = 0
	}
	source, err := s.store.ListRunEventsAfterSequence(ctx, s.runID, after, runactivity.MaxSourceEvents)
	if err != nil {
		return runactivity.Projection{}, apperror.Normalize(err)
	}
	return runactivity.Build(s.runID, source, after > 0)
}

var _ = fmt.Sprintf
