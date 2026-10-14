// Package mcp implements the Go-owned MCP Server v1: a stdio-only, local
// adapter over the existing application services. It is not a trust
// boundary and never grants authority the CLI/HTTP control planes lack.
package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

const (
	// ProtocolVersion is the historical default for persisted server integrations.
	ProtocolVersion = "2025-06-18"
	ServerName      = "cyberagent-mcp"
	ServerVersion   = "1.0.0"

	MaxMessageBytes     = 4 * 1024 * 1024
	MaxToolsPerRequest  = 64
	MaxResourcesPerRead = 32
)

// Envelope is the JSON-RPC 2.0 message envelope shared by every MCP
// message on the stdio transport (newline-delimited JSON).
type Envelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
	CodeNotInitialized = -32002
)

// DecodeEnvelope reads one bounded UTF-8 newline-delimited JSON-RPC
// message. Trailing bytes after the object are rejected.
func DecodeEnvelope(raw []byte) (Envelope, error) {
	if len(raw) == 0 || len(raw) > MaxMessageBytes || !utf8.Valid(raw) {
		return Envelope{}, fmt.Errorf("MCP message must be valid UTF-8 within %d bytes", MaxMessageBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var envelope Envelope
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, fmt.Errorf("MCP message is not a valid JSON-RPC envelope: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Envelope{}, errors.New("MCP message contains trailing data")
	}
	if envelope.JSONRPC != "2.0" {
		return Envelope{}, errors.New("MCP message must declare jsonrpc 2.0")
	}
	return envelope, nil
}

// Request represents one decoded client request.
type Request struct {
	ID         json.RawMessage
	Method     string
	Params     json.RawMessage
	modern     bool
	clientName string
}

// DecodeRequest validates the request shape: exactly one of request
// (id+method), response (id+result/error), or notification (method only).
func DecodeRequest(envelope Envelope) (Request, bool, error) {
	hasID := len(envelope.ID) > 0 && !bytes.Equal(envelope.ID, []byte("null"))
	hasMethod := envelope.Method != ""
	hasResult := envelope.Result != nil || envelope.Error != nil
	if hasID && hasMethod && !hasResult {
		return Request{ID: envelope.ID, Method: envelope.Method, Params: envelope.Params}, true, nil
	}
	if hasID && !hasMethod && hasResult {
		return Request{}, false, nil // response to a server request; v1 never issues requests
	}
	if !hasID && hasMethod {
		return Request{}, false, nil // notification
	}
	return Request{}, false, errors.New("MCP message shape is invalid")
}
