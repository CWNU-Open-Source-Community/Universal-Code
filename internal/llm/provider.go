package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// MaxProviderToolSpecs bounds the tools advertised to a model. It is
	// deliberately separate from MaxProviderToolCalls: a Supervisor may expose
	// many narrowly scoped tools while still accepting only a small parallel
	// call batch from one model response.
	MaxProviderToolSpecs       = 128
	MaxProviderToolCalls       = 16
	MaxProviderOutputItems     = MaxProviderToolCalls * 4
	MaxProviderToolIdentity    = 128
	MaxProviderToolPayloadSize = 256 * 1024
	MaxProviderToolResultSize  = 64 * 1024
)

type Provider interface {
	Name() string
	ListModels(ctx context.Context) ([]ModelInfo, error)
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	StreamChat(ctx context.Context, req ChatRequest) (<-chan ChatChunk, error)
	SupportsTools(model string) bool
	SupportsVision(model string) bool
	SupportsJSONMode(model string) bool
}

type ModelInfo struct {
	ID           string
	DisplayName  string
	Provider     string
	Capabilities []string
}

type Message struct {
	Role        string          `json:"role"`
	Content     string          `json:"content,omitempty"`
	Images      []ImagePart     `json:"-"`
	ToolCalls   []ToolCall      `json:"tool_calls,omitempty"`
	ToolResults []ToolResult    `json:"tool_results,omitempty"`
	Replay      *ProviderReplay `json:"-"`
	// Go-only marker for already leaf-redacted compaction JSON. It cannot be
	// set through provider/model JSON and becomes invalid if Content changes.
	contextCompactionContentSHA256 string
}

type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type ChatRequest struct {
	Model         string
	Messages      []Message
	Tools         []ToolSpec
	Temperature   float64
	MaxTokens     int
	JSONMode      bool
	Metadata      map[string]string
	preparedModel *preparedModelRequest
}

type ChatResponse struct {
	ResponseID   string
	Text         string
	ToolCalls    []ToolCall
	Items        []OutputItem
	Usage        Usage
	Raw          json.RawMessage
	Model        string
	Provider     string
	FinishReason FinishReason
	Replay       *ProviderReplay `json:"-"`
	// Application-issued evidence for a rejected native request. Never returned
	// to the provider, interpreted as a tool call, or included in public history.
	ToolRequestRejection *ToolRequestRejection `json:"-"`
}

type ChatChunk struct {
	Text         string
	Done         bool
	ToolCalls    []ToolCall
	Events       []StreamEvent
	Usage        *Usage
	Model        string
	Provider     string
	FinishReason FinishReason
	Replay       *ProviderReplay `json:"-"`
	Err          error
}

func FinalChatChunk(response *ChatResponse) ChatChunk {
	if response == nil {
		return ChatChunk{Done: true}
	}
	usage := response.Usage
	completionErr := CompletionError(response.Provider, response.FinishReason)
	var terminalErr error
	toolCalls := response.ToolCalls
	if completionErr != nil {
		terminalErr = completionErr
		toolCalls = nil
	}
	return ChatChunk{
		Done: completionErr == nil, ToolCalls: toolCalls, Usage: &usage, Model: response.Model, Provider: response.Provider,
		FinishReason: response.FinishReason, Replay: response.Replay,
		Err: terminalErr,
	}
}

type FinishReason string

const (
	FinishReasonStop         FinishReason = "stop"
	FinishReasonToolCalls    FinishReason = "tool_calls"
	FinishReasonLength       FinishReason = "length"
	FinishReasonPause        FinishReason = "pause"
	FinishReasonRefusal      FinishReason = "refusal"
	FinishReasonContextLimit FinishReason = "context_limit"
	FinishReasonUnknown      FinishReason = "unknown"
)

func (r FinishReason) Valid() bool {
	switch r {
	case "", FinishReasonStop, FinishReasonToolCalls, FinishReasonLength, FinishReasonPause,
		FinishReasonRefusal, FinishReasonContextLimit, FinishReasonUnknown:
		return true
	default:
		return false
	}
}

// CompletionError maps a provider's explicit non-success terminal reason to a
// stable, non-retryable error while preserving the response and its usage.
// Empty and unknown reasons remain compatible with legacy provider fixtures.
func CompletionError(provider string, reason FinishReason) *ProviderError {
	return reason.Error(provider)
}

func (r FinishReason) Error(provider string) *ProviderError {
	var failure ProviderFailureReason
	var message string
	switch r {
	case FinishReasonLength:
		failure, message = ProviderFailureOutputLimit, "stopped at the output limit"
	case FinishReasonPause:
		failure, message = ProviderFailurePaused, "paused before completion"
	case FinishReasonRefusal:
		failure, message = ProviderFailureRefusal, "refused the request"
	case FinishReasonContextLimit:
		failure, message = ProviderFailureContextLimit, "exceeded the context limit"
	default:
		return nil
	}
	err := NewProviderError(OutcomePermanent, provider, message, nil)
	err.Reason = failure
	return err
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	// Authority is Go-issued, never provider-supplied, and omitted from wire
	// payloads. Supervisor persistence uses it to replay a pending call against
	// the exact capability snapshot that exposed the tool.
	Authority json.RawMessage `json:"-"`
	// Stream identities are application-owned reconciliation metadata. They
	// never come from provider authority and are never sent back on the wire.
	StreamResponseID string `json:"-"`
	StreamItemID     string `json:"-"`
	StreamCallID     string `json:"-"`
}

func NormalizeToolCall(call ToolCall) (ToolCall, error) {
	call.ID = strings.TrimSpace(call.ID)
	call.Name = strings.TrimSpace(call.Name)
	call.Arguments = append(json.RawMessage(nil), bytes.TrimSpace(call.Arguments)...)
	call.Authority = append(json.RawMessage(nil), bytes.TrimSpace(call.Authority)...)
	call.StreamResponseID = strings.TrimSpace(call.StreamResponseID)
	call.StreamItemID = strings.TrimSpace(call.StreamItemID)
	call.StreamCallID = strings.TrimSpace(call.StreamCallID)
	if call.ID == "" || call.Name == "" || !utf8.ValidString(call.ID) || !utf8.ValidString(call.Name) ||
		len([]rune(call.ID)) > MaxProviderToolIdentity || len([]rune(call.Name)) > MaxProviderToolIdentity ||
		strings.ContainsRune(call.ID, 0) || strings.ContainsRune(call.Name, 0) {
		return ToolCall{}, errors.New("provider tool call id and name must be bounded normalized UTF-8")
	}
	for _, current := range call.Name {
		if (current < 'a' || current > 'z') && (current < '0' || current > '9') && current != '_' {
			return ToolCall{}, errors.New("provider tool call name must use lowercase letters, digits, or underscore")
		}
	}
	if len(call.Arguments) == 0 || len(call.Arguments) > MaxProviderToolPayloadSize ||
		!utf8.Valid(call.Arguments) || !json.Valid(call.Arguments) {
		return ToolCall{}, fmt.Errorf("provider tool call arguments must be valid UTF-8 JSON up to %d bytes",
			MaxProviderToolPayloadSize)
	}
	streamIDs := []string{call.StreamResponseID, call.StreamItemID, call.StreamCallID}
	streamCount := 0
	for _, value := range streamIDs {
		if value != "" {
			streamCount++
			if err := validateStreamIdentity(value, "tool call"); err != nil {
				return ToolCall{}, err
			}
		}
	}
	if streamCount != 0 && streamCount != len(streamIDs) {
		return ToolCall{}, errors.New("provider tool call stream identities must be all present or all absent")
	}
	return call, nil
}

func NormalizeToolCalls(calls []ToolCall) ([]ToolCall, error) {
	if len(calls) > MaxProviderToolCalls {
		return nil, fmt.Errorf("provider tool call list exceeds %d items", MaxProviderToolCalls)
	}
	out := make([]ToolCall, len(calls))
	seen := make(map[string]struct{}, len(calls))
	seenStreamItems := make(map[string]struct{}, len(calls))
	seenStreamCalls := make(map[string]struct{}, len(calls))
	streamResponseID := ""
	streamMetadata := false
	for index, call := range calls {
		normalized, err := NormalizeToolCall(call)
		if err != nil {
			return nil, fmt.Errorf("invalid provider tool call at index %d: %w", index, err)
		}
		if _, exists := seen[normalized.ID]; exists {
			return nil, errors.New("provider tool call ids must be unique")
		}
		seen[normalized.ID] = struct{}{}
		hasStreamMetadata := normalized.StreamResponseID != ""
		if index == 0 {
			streamMetadata = hasStreamMetadata
		} else if hasStreamMetadata != streamMetadata {
			return nil, errors.New("provider tool calls must consistently carry stream identities")
		}
		if hasStreamMetadata {
			if streamResponseID == "" {
				streamResponseID = normalized.StreamResponseID
			} else if normalized.StreamResponseID != streamResponseID {
				return nil, errors.New("provider tool calls changed their stream response identity")
			}
			if _, exists := seenStreamItems[normalized.StreamItemID]; exists {
				return nil, errors.New("provider tool call stream item ids must be unique")
			}
			if _, exists := seenStreamCalls[normalized.StreamCallID]; exists {
				return nil, errors.New("provider tool call stream call ids must be unique")
			}
			seenStreamItems[normalized.StreamItemID] = struct{}{}
			seenStreamCalls[normalized.StreamCallID] = struct{}{}
		}
		out[index] = normalized
	}
	return out, nil
}

type ToolResult struct {
	ToolCallID          string `json:"tool_call_id"`
	Content             string `json:"content"`
	IsError             bool   `json:"is_error,omitempty"`
	storedHistoryDigest [32]byte
}

func NormalizeToolResult(result ToolResult) (ToolResult, error) {
	result.ToolCallID = strings.TrimSpace(result.ToolCallID)
	result.Content = strings.TrimSpace(result.Content)
	if result.ToolCallID == "" || !utf8.ValidString(result.ToolCallID) ||
		len([]rune(result.ToolCallID)) > MaxProviderToolIdentity || strings.ContainsRune(result.ToolCallID, 0) {
		return ToolResult{}, errors.New("provider tool result call id must be bounded normalized UTF-8")
	}
	if result.Content == "" || !utf8.ValidString(result.Content) || len(result.Content) > MaxProviderToolResultSize {
		return ToolResult{}, fmt.Errorf("provider tool result content must be valid UTF-8 up to %d bytes",
			MaxProviderToolResultSize)
	}
	return result, nil
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
	// InputTokens includes every logical input token. Cache categories are
	// disjoint subsets, not additional tokens to add to this total.
	CacheReadTokens       int `json:"cache_read_tokens,omitempty"`
	CacheCreationTokens   int `json:"cache_creation_tokens,omitempty"`
	CacheCreation5mTokens int `json:"cache_creation_5m_tokens,omitempty"`
	CacheCreation1hTokens int `json:"cache_creation_1h_tokens,omitempty"`
	// Aggregate-only creation leaves the TTL unknown. CacheUsageReported
	// distinguishes omitted legacy fields from explicitly reported zeros.
	CacheCreationBreakdownKnown bool `json:"cache_creation_breakdown_known,omitempty"`
	CacheUsageReported          bool `json:"cache_usage_reported,omitempty"`
}
