package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/redact"
)

const (
	defaultOpenAIModel        = "gpt-4.1-mini"
	maxOpenAIResponseBytes    = 2 << 20
	maxOpenAIStreamLineBytes  = 1 << 20
	maxOpenAIStreamEventBytes = 1 << 20
	maxOpenAIModelBytes       = 512
)

// OpenAICompatibleConfig configures an OpenAI Chat Completions compatible
// endpoint. Credentials remain in memory and are never copied into requests,
// responses, errors, or Harness metadata.
type OpenAICompatibleConfig struct {
	Name         string
	BaseURL      string
	APIKey       string
	DefaultModel string
	HTTPClient   *http.Client
	Runtime      HTTPProviderRuntime
}

// OpenAICompatibleProvider implements the protocol-neutral Provider contract
// using the OpenAI Chat Completions wire protocol.
type OpenAICompatibleProvider struct {
	name         string
	baseURL      string
	apiKey       string
	defaultModel string
	client       *http.Client
	runtime      HTTPProviderRuntime
}

func NewOpenAICompatibleProvider(config OpenAICompatibleConfig) (*OpenAICompatibleProvider, error) {
	name := strings.TrimSpace(config.Name)
	if name == "" {
		name = "openai_compatible"
	}
	baseURL, err := normalizeProviderBaseURL(config.BaseURL, name)
	if err != nil {
		return nil, err
	}
	if config.Runtime == nil {
		if err := validateProviderAPIKey(config.APIKey, name); err != nil {
			return nil, err
		}
	} else if err := validateHTTPProviderRuntime(config.Runtime); err != nil {
		return nil, err
	}
	model := strings.TrimSpace(config.DefaultModel)
	if model == "" {
		model = defaultOpenAIModel
	}
	model, err = normalizeOpenAIModel(model)
	if err != nil {
		return nil, fmt.Errorf("default model for provider %s is invalid", name)
	}
	client, err := providerHTTPClient(config.HTTPClient)
	if err != nil {
		return nil, err
	}
	return &OpenAICompatibleProvider{
		name: name, baseURL: baseURL, apiKey: config.APIKey,
		defaultModel: model, client: client,
		runtime: config.Runtime,
	}, nil
}

func (p *OpenAICompatibleProvider) Name() string { return p.name }

func (p *OpenAICompatibleProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	return listProviderCatalog(ctx, ModelCatalogRequest{
		Provider: p.name, EndpointURL: p.baseURL, Transport: HarnessTransportOpenAIChatCompletions,
		APIKey: p.apiKey, Runtime: p.runtime, HTTPClient: p.client,
	})
}

func (p *OpenAICompatibleProvider) Chat(ctx context.Context, request ChatRequest) (*ChatResponse, error) {
	model, wire, err := p.prepareRequest(request, false)
	if err != nil {
		return nil, openAILocalError(p.name, "could not prepare request")
	}
	secret, err := providerRequestCredential(ctx, p.name, p.apiKey, p.runtime, p.baseURL)
	if err != nil {
		return nil, openAILocalError(p.name, "provider credential is unavailable")
	}
	payload, err := providerRequestPayload(p.runtime, secret, wire)
	if err != nil {
		return nil, openAILocalError(p.name, "could not encode request")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.endpoint("/v1/chat/completions"), bytes.NewReader(payload))
	if err != nil {
		return nil, openAILocalError(p.name, "could not create request")
	}
	if err := applyOpenAIRequestHeaders(httpReq, false, secret, p.runtime); err != nil {
		return nil, openAILocalError(p.name, "could not prepare request headers")
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, openAITransportError(ctx, p.name)
	}
	defer resp.Body.Close()
	raw, err := readOpenAIBody(resp.Body)
	if err != nil {
		return nil, openAIReadError(ctx, p.name, "could not read response", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, openAIHTTPError(p.name, resp.StatusCode, resp.Header.Get("Retry-After"), raw)
	}
	if !utf8.Valid(raw) {
		return nil, openAIProtocolError(p.name, "returned non-UTF-8 JSON")
	}
	if (GeminiThoughtSignatureScope(p.baseURL, wire.Model) || KimiReasoningScope(p.baseURL, wire.Model)) && validateGeminiWireJSON(raw) != nil {
		return nil, openAIProtocolError(p.name, "returned ambiguous native metadata")
	}
	var parsed openAIChatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, openAIProtocolError(p.name, "returned malformed JSON")
	}
	if parsed.Error != nil {
		return nil, openAIWireError(p.name, *parsed.Error)
	}
	result, err := p.normalizeResponse(model, parsed)
	if err != nil {
		return result, err
	}
	if GeminiThoughtSignatureScope(p.baseURL, wire.Model) {
		if err := p.captureGeminiResponse(model, wire.Model, parsed, result); err != nil {
			return nil, openAIProtocolError(p.name, "returned invalid Gemini signature replay")
		}
	}
	if KimiReasoningScope(p.baseURL, wire.Model) {
		if err := p.captureKimiResponse(model, wire.Model, parsed, result); err != nil {
			// A rejected replay cannot publish native state. Its already
			// validated usage remains available for failure accounting.
			result.Replay = nil
			return result, openAIProtocolError(p.name, "returned invalid Kimi private replay")
		}
	}
	return result, nil
}

func (p *OpenAICompatibleProvider) StreamChat(ctx context.Context, request ChatRequest) (<-chan ChatChunk, error) {
	model, wire, err := p.prepareRequest(request, true)
	if err != nil {
		return nil, openAILocalError(p.name, "could not prepare streaming request")
	}
	secret, err := providerRequestCredential(ctx, p.name, p.apiKey, p.runtime, p.baseURL)
	if err != nil {
		return nil, openAILocalError(p.name, "provider credential is unavailable")
	}
	payload, err := providerRequestPayload(p.runtime, secret, wire)
	if err != nil {
		return nil, openAILocalError(p.name, "could not encode streaming request")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.endpoint("/v1/chat/completions"), bytes.NewReader(payload))
	if err != nil {
		return nil, openAILocalError(p.name, "could not create streaming request")
	}
	if err := applyOpenAIRequestHeaders(httpReq, true, secret, p.runtime); err != nil {
		return nil, openAILocalError(p.name, "could not prepare streaming request headers")
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, openAITransportError(ctx, p.name)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		defer resp.Body.Close()
		raw, readErr := readOpenAIBody(resp.Body)
		if readErr != nil {
			return nil, openAIReadError(ctx, p.name, "could not read error response", readErr)
		}
		return nil, openAIHTTPError(p.name, resp.StatusCode, resp.Header.Get("Retry-After"), raw)
	}
	chunks := make(chan ChatChunk, 8)
	go p.readStream(ctx, resp.Body, model, chunks, wire.Model)
	return chunks, nil
}

func (p *OpenAICompatibleProvider) SupportsTools(model string) bool {
	return strings.TrimSpace(model) == p.defaultModel
}

func (p *OpenAICompatibleProvider) SupportsVision(model string) bool {
	return p.DescribeVision(model).State == VisionSupported
}
func (p *OpenAICompatibleProvider) DescribeVision(model string) VisionCapability {
	return runtimeVision(p.runtime, model)
}

func (p *OpenAICompatibleProvider) SupportsJSONMode(model string) bool {
	return strings.TrimSpace(model) == p.defaultModel
}

func (p *OpenAICompatibleProvider) DescribeModelHarness(model string) ModelHarness {
	model = strings.TrimSpace(model)
	if model == "" {
		model = p.defaultModel
	}
	binding := providerHarnessBinding(p.runtime, p.name, p.baseURL, model,
		HarnessTransportOpenAIChatCompletions, HarnessToolStrategyNative, HarnessJSONStrategyNative)
	if mappedModel, err := providerRequestModel(p.runtime, model); err == nil {
		if wireModel, err := normalizeOpenAIModel(mappedModel); err == nil && GeminiThoughtSignatureScope(p.baseURL, wireModel) {
			// One-round qualifications predate native sequential signature replay.
			binding = providerHarnessBinding(nil, binding, wireModel, "gemini-sequential-signature-probe-v1")
		}
		if wireModel, err := normalizeOpenAIModel(mappedModel); err == nil && KimiReasoningScope(p.baseURL, wireModel) {
			binding = providerHarnessBinding(nil, binding, wireModel, "kimi-preserved-history-probe-v4")
		}
	}
	return ModelHarness{
		ProtocolVersion:   ModelHarnessProtocolVersion,
		TransportProtocol: HarnessTransportOpenAIChatCompletions,
		ToolStrategy:      HarnessToolStrategyNative, JSONStrategy: HarnessJSONStrategyNative,
		QualificationStatus: HarnessQualificationRequired,
		BindingDigest:       binding,
	}
}

func (p *OpenAICompatibleProvider) prepareRequest(request ChatRequest, stream bool) (string, openAIChatRequest, error) {
	selectedModel := strings.TrimSpace(request.Model)
	if selectedModel == "" {
		selectedModel = p.defaultModel
	}
	var err error
	selectedModel, err = normalizeOpenAIModel(selectedModel)
	if err != nil {
		return "", openAIChatRequest{}, err
	}
	wireModel, err := providerRequestModel(p.runtime, selectedModel)
	if err != nil {
		return "", openAIChatRequest{}, err
	}
	wireModel, err = normalizeOpenAIModel(wireModel)
	if err != nil {
		return "", openAIChatRequest{}, err
	}
	if math.IsNaN(request.Temperature) || math.IsInf(request.Temperature, 0) ||
		request.Temperature < 0 || request.Temperature > 2 {
		return "", openAIChatRequest{}, errors.New("temperature is outside the supported range")
	}
	maxTokens := request.MaxTokens
	if maxTokens < 0 || maxTokens > 1_000_000 {
		return "", openAIChatRequest{}, errors.New("max tokens exceeds the provider request limit")
	}
	wire := openAIChatRequest{Model: wireModel, MaxTokens: maxTokens, Stream: stream}
	if err := validateRequestImages(p, selectedModel, request.Messages); err != nil {
		return "", openAIChatRequest{}, err
	}
	if request.Temperature > 0 {
		wire.Temperature = &request.Temperature
	}
	if stream {
		wire.StreamOptions = &openAIStreamOptions{IncludeUsage: true}
	}
	if request.JSONMode {
		wire.ResponseFormat = &openAIResponseFormat{Type: "json_object"}
	}
	if GeminiThoughtSignatureScope(p.baseURL, wireModel) {
		wire.Messages, err = p.geminiMessages(request.Messages, selectedModel, wireModel)
		if err != nil {
			return "", openAIChatRequest{}, err
		}
	} else if KimiReasoningScope(p.baseURL, wireModel) {
		wire.Messages, err = p.kimiMessages(request.Messages, selectedModel, wireModel)
		if err != nil {
			return "", openAIChatRequest{}, err
		}
	} else {
		for index, message := range request.Messages {
			if message.Replay != nil {
				return "", openAIChatRequest{}, errors.New("private replay is unsupported for this Chat provider scope")
			}
			mapped, err := openAIMessages(message)
			if err != nil {
				return "", openAIChatRequest{}, fmt.Errorf("invalid message at index %d", index)
			}
			wire.Messages = append(wire.Messages, mapped...)
		}
	}
	if len(wire.Messages) == 0 {
		content := "Hello"
		wire.Messages = append(wire.Messages, openAIMessage{Role: "user", Content: &content})
	}
	if len(request.Tools) > MaxProviderToolSpecs {
		return "", openAIChatRequest{}, errors.New("tool specification count exceeds the provider limit")
	}
	for index, spec := range request.Tools {
		name := strings.TrimSpace(spec.Name)
		parameters := append(json.RawMessage(nil), bytes.TrimSpace(spec.Parameters)...)
		if err := validateOpenAIToolName(name); err != nil || len(parameters) == 0 ||
			len(parameters) > MaxProviderToolPayloadSize || !utf8.Valid(parameters) || !json.Valid(parameters) {
			return "", openAIChatRequest{}, fmt.Errorf("invalid tool specification at index %d", index)
		}
		wire.Tools = append(wire.Tools, openAITool{Type: "function", Function: openAIFunctionSpec{
			Name: name, Description: strings.TrimSpace(spec.Description), Parameters: parameters,
		}})
	}
	return selectedModel, wire, nil
}

func openAIMessages(message Message) ([]openAIMessage, error) {
	if err := ValidateMessageImages(message); err != nil {
		return nil, err
	}
	role := strings.ToLower(strings.TrimSpace(message.Role))
	content := strings.TrimSpace(message.Content)
	if content == "" && len(message.ToolCalls) == 0 && len(message.ToolResults) == 0 && len(message.Images) == 0 {
		return nil, nil
	}
	switch role {
	case "system":
		if len(message.ToolCalls) != 0 || len(message.ToolResults) != 0 || content == "" {
			return nil, errors.New("system message has invalid structured content")
		}
		return []openAIMessage{{Role: "system", Content: &content}}, nil
	case "assistant":
		if len(message.ToolResults) != 0 {
			return nil, errors.New("assistant message cannot contain tool results")
		}
		calls, err := NormalizeToolCalls(message.ToolCalls)
		if err != nil {
			return nil, err
		}
		mapped := openAIMessage{Role: "assistant"}
		if content != "" {
			mapped.Content = &content
		}
		for _, call := range calls {
			mapped.ToolCalls = append(mapped.ToolCalls, openAIToolCall{
				ID: call.ID, Type: "function", Function: openAIFunctionCall{
					Name: call.Name, Arguments: string(call.Arguments),
				},
			})
		}
		return []openAIMessage{mapped}, nil
	case "user":
		if len(message.ToolCalls) != 0 {
			return nil, errors.New("user message cannot contain tool calls")
		}
		mapped := make([]openAIMessage, 0, len(message.ToolResults)+1)
		for _, result := range message.ToolResults {
			normalized, err := NormalizeToolResult(result)
			if err != nil {
				return nil, err
			}
			resultContent := normalized.Content
			mapped = append(mapped, openAIMessage{
				Role: "tool", Content: &resultContent, ToolCallID: normalized.ToolCallID,
			})
		}
		if len(message.Images) > 0 {
			parts := make([]openAIContentPart, 0, len(message.Images)+1)
			if content != "" {
				parts = append(parts, openAIContentPart{Type: "text", Text: content})
			}
			for _, image := range message.Images {
				parts = append(parts, openAIContentPart{Type: "image_url", ImageURL: &openAIImageURL{URL: imageDataURL(image), Detail: "auto"}})
			}
			mapped = append(mapped, openAIMessage{Role: "user", Parts: parts})
		} else if content != "" {
			mapped = append(mapped, openAIMessage{Role: "user", Content: &content})
		}
		if len(mapped) == 0 {
			return nil, errors.New("user message has no content")
		}
		return mapped, nil
	default:
		return nil, errors.New("message role is unsupported")
	}
}

func (p *OpenAICompatibleProvider) normalizeResponse(defaultModel string, response openAIChatResponse) (*ChatResponse, error) {
	if len(response.Choices) != 1 || response.Choices[0].Index == nil || *response.Choices[0].Index != 0 {
		return nil, openAIProtocolError(p.name, "returned an invalid choice list")
	}
	choice := response.Choices[0]
	if choice.Message.Role != "" && choice.Message.Role != "assistant" {
		return nil, openAIProtocolError(p.name, "returned an invalid message role")
	}
	text := ""
	if choice.Message.Content != nil {
		text = *choice.Message.Content
	}
	if !utf8.ValidString(text) || len(text) > MaxModelOutputBytes {
		return nil, openAIProtocolError(p.name, "returned invalid response text")
	}
	if response.Usage == nil {
		return nil, openAIProtocolError(p.name, "omitted token usage")
	}
	usage, err := normalizeOpenAIUsage(*response.Usage)
	if err != nil {
		return nil, openAIProtocolError(p.name, "returned invalid token usage")
	}
	if _, err := normalizeOpenAIModel(response.Model); err != nil {
		return nil, openAIProtocolError(p.name, "returned an invalid model identity")
	}
	finishReason := openAIFinishReason(choice.FinishReason)
	refusal, err := decodeOpenAIRefusal(choice.Message.Refusal)
	if err != nil {
		return &ChatResponse{Usage: usage, Model: defaultModel, Provider: p.name},
			openAIProtocolError(p.name, "returned invalid refusal")
	}
	if refusal != "" {
		if text != "" || len(choice.Message.ToolCalls) != 0 ||
			(finishReason != FinishReasonStop && finishReason != FinishReasonRefusal) {
			return &ChatResponse{Usage: usage, Model: defaultModel, Provider: p.name},
				openAIProtocolError(p.name, "mixed a refusal with incompatible output")
		}
		finishReason = FinishReasonRefusal
	}
	var calls []ToolCall
	if CompletionError(p.name, finishReason) == nil {
		calls, err = normalizeOpenAIToolCalls(choice.Message.ToolCalls)
		if err != nil {
			return nil, openAIProtocolError(p.name, "returned invalid tool calls")
		}
		if err := validateOpenAIFinishReason(finishReason, text != "", len(calls)); err != nil {
			return nil, openAIProtocolError(p.name, "returned an incompatible finish reason")
		}
	}
	result := &ChatResponse{
		Text: text, ToolCalls: calls, Usage: usage, Raw: nil,
		// OpenAI may resolve a stable alias to a dated snapshot in the wire
		// response. Keep the selected Router identity stable while still
		// requiring a valid upstream identity above.
		Model: defaultModel, Provider: p.name, FinishReason: finishReason,
	}
	if completionErr := CompletionError(p.name, finishReason); completionErr != nil {
		return result, completionErr
	}
	return result, nil
}

func normalizeOpenAIToolCalls(wire []openAIToolCall) ([]ToolCall, error) {
	calls := make([]ToolCall, 0, len(wire))
	for _, item := range wire {
		if item.Type != "" && item.Type != "function" {
			return nil, errors.New("tool call type is unsupported")
		}
		arguments := item.Function.Arguments
		if strings.TrimSpace(arguments) == "" {
			arguments = `{}`
		}
		calls = append(calls, ToolCall{ID: item.ID, Name: item.Function.Name,
			Arguments: json.RawMessage(arguments)})
	}
	return NormalizeToolCalls(calls)
}

func normalizeOpenAIUsage(wire openAIUsage) (Usage, error) {
	if wire.PromptTokens == nil || wire.CompletionTokens == nil || wire.TotalTokens == nil {
		return Usage{}, errors.New("token count is missing")
	}
	usage := Usage{InputTokens: *wire.PromptTokens, OutputTokens: *wire.CompletionTokens}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || *wire.TotalTokens < 0 {
		return Usage{}, errors.New("negative token count")
	}
	maxInt := int(^uint(0) >> 1)
	if usage.InputTokens > maxInt-usage.OutputTokens {
		return Usage{}, errors.New("token count overflow")
	}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	if *wire.TotalTokens != usage.TotalTokens {
		return Usage{}, errors.New("inconsistent total token count")
	}
	usage, err := applyOpenAICacheUsage(usage, wire.PromptTokensDetails)
	if err != nil {
		return Usage{}, err
	}
	// DeepSeek's older usage form reports the same cache hit count at the
	// top level. Accept it without adding it to the logical input total.
	if wire.PromptCacheHitTokens != nil {
		if wire.PromptTokensDetails != nil && wire.PromptTokensDetails.CachedTokens != nil &&
			usage.CacheReadTokens != *wire.PromptCacheHitTokens {
			return Usage{}, errors.New("conflicting cache hit token counts")
		}
		usage.CacheReadTokens = *wire.PromptCacheHitTokens
		usage.CacheUsageReported = true
	}
	if wire.PromptCacheMissTokens != nil {
		if !usage.CacheUsageReported || *wire.PromptCacheMissTokens < 0 || usage.CacheReadTokens < 0 ||
			usage.CacheReadTokens > usage.InputTokens || *wire.PromptCacheMissTokens != usage.InputTokens-usage.CacheReadTokens {
			return Usage{}, errors.New("inconsistent cache miss token count")
		}
	}
	return usage, usage.Validate()
}

func openAIFinishReason(reason string) FinishReason {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "":
		return ""
	case "stop", "end_turn":
		return FinishReasonStop
	case "tool_calls", "tool_use":
		return FinishReasonToolCalls
	case "length", "max_tokens", "max_output_tokens":
		return FinishReasonLength
	case "pause", "pause_turn":
		return FinishReasonPause
	case "refusal", "content_filter":
		return FinishReasonRefusal
	case "context_limit", "context_length_exceeded", "model_context_window_exceeded":
		return FinishReasonContextLimit
	default:
		return FinishReasonUnknown
	}
}

func validateOpenAIFinishReason(reason FinishReason, hasText bool, toolCount int) error {
	switch reason {
	case FinishReasonStop:
		if toolCount != 0 || !hasText {
			return errors.New("stop finish did not contain text")
		}
	case FinishReasonToolCalls:
		if toolCount == 0 {
			return errors.New("tool finish did not contain calls")
		}
	case "", FinishReasonUnknown:
		if !hasText && toolCount == 0 {
			return errors.New("legacy finish did not contain usable output")
		}
	default:
		return errors.New("finish reason is not successful")
	}
	return nil
}

func normalizeOpenAIModel(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) || len([]byte(value)) > maxOpenAIModelBytes ||
		strings.ContainsRune(value, 0) || redact.String(value) != value {
		return "", errors.New("model identifier is invalid")
	}
	for _, current := range value {
		if unicode.IsControl(current) || unicode.IsSpace(current) {
			return "", errors.New("model identifier is invalid")
		}
	}
	return value, nil
}

func validateOpenAIToolName(name string) error {
	_, err := NormalizeToolCall(ToolCall{ID: "spec", Name: name, Arguments: json.RawMessage(`{}`)})
	return err
}

func (p *OpenAICompatibleProvider) endpoint(path string) string {
	if path == "/v1/chat/completions" {
		if endpoint, ok := geminiOpenAIEndpoint(p.baseURL); ok {
			return endpoint
		}
	}
	if strings.HasSuffix(p.baseURL, path) {
		return p.baseURL
	}
	if strings.HasSuffix(p.baseURL, "/v1") && strings.HasPrefix(path, "/v1/") {
		return p.baseURL + strings.TrimPrefix(path, "/v1")
	}
	return p.baseURL + path
}

func readOpenAIBody(reader io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, maxOpenAIResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxOpenAIResponseBytes {
		return nil, errors.New("response exceeds size limit")
	}
	return raw, nil
}

func openAILocalError(provider string, message string) *ProviderError {
	err := NewProviderError(OutcomePermanent, provider, message, nil)
	err.Reason = ProviderFailureProtocolIncompatible
	return err
}

func openAIProtocolError(provider string, message string) *ProviderError {
	err := NewProviderError(OutcomeInvalidResponse, provider, message, nil)
	err.Reason = ProviderFailureProtocolIncompatible
	return err
}

func openAITransportError(ctx context.Context, provider string) *ProviderError {
	if err := providerContextError(ctx, provider); err != nil {
		return err
	}
	err := NewProviderError(OutcomeRetryable, provider, "request failed", nil)
	err.Reason = ProviderFailureNetwork
	return err
}

func openAIReadError(ctx context.Context, provider string, message string, source error) *ProviderError {
	return providerHTTPReadError(ctx, provider, message, source)
}

func openAIHTTPError(provider string, statusCode int, retryAfter string, raw []byte) *ProviderError {
	kind := OutcomePermanent
	reason := ProviderFailureProtocolIncompatible
	switch statusCode {
	case http.StatusPaymentRequired:
		// Billing failures need an account change, not a protocol repair or
		// automatic retry. Keep the existing capacity classification contract.
		reason = ProviderFailureCapacity
	case http.StatusUnauthorized, http.StatusForbidden:
		reason = ProviderFailureAuthentication
	case http.StatusTooManyRequests:
		kind, reason = OutcomeRateLimited, ProviderFailureRateLimit
	case http.StatusServiceUnavailable, 529:
		kind, reason = OutcomeRetryable, ProviderFailureCapacity
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusGatewayTimeout:
		kind, reason = OutcomeRetryable, ProviderFailureNetwork
	}
	var envelope struct {
		Error *openAIError `json:"error"`
	}
	// The HTTP status is authoritative for standard status classes. Some
	// compatible endpoints reuse broad error types such as server_error even
	// for authentication and throttling responses.
	wireReason := ProviderFailureNone
	if utf8.Valid(raw) && json.Unmarshal(raw, &envelope) == nil && envelope.Error != nil {
		if statusCode == http.StatusBadRequest && isOpenAIContextLimit(*envelope.Error) {
			wireReason = ProviderFailureContextLimit
		} else {
			wireReason = classifyOpenAIWireError(*envelope.Error)
		}
	}
	if statusCode == http.StatusNotFound {
		// A bare 404 normally means the configured Chat Completions path is
		// incompatible. Only a canonical, content-free error code/type can
		// prove that the requested model itself was not found.
		if wireReason == ProviderFailureModelNotFound {
			reason = wireReason
		}
	} else if reason == ProviderFailureProtocolIncompatible && wireReason != ProviderFailureNone {
		reason = wireReason
		switch wireReason {
		case ProviderFailureRateLimit:
			kind = OutcomeRateLimited
		case ProviderFailureCapacity, ProviderFailureNetwork:
			kind = OutcomeRetryable
		default:
			kind = OutcomePermanent
		}
	}
	err := NewProviderError(kind, provider, fmt.Sprintf("returned HTTP %d", statusCode), nil)
	err.StatusCode = statusCode
	err.RetryAfter = parseRetryAfter(retryAfter, time.Now())
	err.Reason = reason
	return err
}

func openAIWireError(provider string, wire openAIError) *ProviderError {
	reason := classifyOpenAIWireError(wire)
	kind := OutcomePermanent
	switch reason {
	case ProviderFailureRateLimit:
		kind = OutcomeRateLimited
	case ProviderFailureCapacity, ProviderFailureNetwork:
		kind = OutcomeRetryable
	case ProviderFailureNone:
		reason = ProviderFailureProtocolIncompatible
	}
	err := NewProviderError(kind, provider, "returned a provider error", nil)
	err.Reason = reason
	return err
}

func classifyOpenAIWireError(wire openAIError) ProviderFailureReason {
	code := ""
	if len(wire.Code) > 0 {
		_ = json.Unmarshal(wire.Code, &code)
	}
	// Prefer the specific code over the broader error type. Both are treated as
	// untrusted wire values and must match a closed canonical allowlist; prefix
	// or substring matches could turn attacker-controlled near-misses into false
	// authentication, capacity, or model-not-found facts.
	for _, signal := range []string{code, wire.Type} {
		switch strings.ToLower(strings.TrimSpace(signal)) {
		case "rate_limit", "rate_limit_error", "rate_limit_exceeded",
			"insufficient_quota", "quota_exceeded", "subscription_sharing_usage_limit_exceeded":
			return ProviderFailureRateLimit
		case "server_error", "overloaded", "overloaded_error",
			"capacity_exceeded", "subscription_sharing_usage_unavailable", "subscription_sharing_user_unavailable":
			return ProviderFailureCapacity
		case "model_not_found", "model_not_exist", "model_not_exists":
			return ProviderFailureModelNotFound
		case "authentication_error", "invalid_api_key", "incorrect_api_key",
			"api_key_invalid", "permission_denied", "insufficient_permissions",
			"subscription_sharing_invalid_user", "subscription_sharing_user_not_eligible",
			"chatpass_v2_scope_not_authorized", "chatpass_v2_invalid_authorization_context":
			return ProviderFailureAuthentication
		case "request_timeout", "timeout_error":
			return ProviderFailureNetwork
		}
	}
	return ProviderFailureNone
}

func isOpenAIContextLimit(wire openAIError) bool {
	var code string
	return len(wire.Code) > 0 && json.Unmarshal(wire.Code, &code) == nil &&
		strings.EqualFold(strings.TrimSpace(code), "context_length_exceeded")
}

type openAIChatRequest struct {
	Model          string                `json:"model"`
	Messages       []openAIMessage       `json:"messages"`
	Tools          []openAITool          `json:"tools,omitempty"`
	Temperature    *float64              `json:"temperature,omitempty"`
	MaxTokens      int                   `json:"max_tokens,omitempty"`
	ResponseFormat *openAIResponseFormat `json:"response_format,omitempty"`
	Stream         bool                  `json:"stream,omitempty"`
	StreamOptions  *openAIStreamOptions  `json:"stream_options,omitempty"`
}

type openAIMessage struct {
	Role             string              `json:"role"`
	Content          *string             `json:"content,omitempty"`
	ToolCalls        []openAIToolCall    `json:"tool_calls,omitempty"`
	ToolCallID       string              `json:"tool_call_id,omitempty"`
	Parts            []openAIContentPart `json:"-"`
	ExtraContent     json.RawMessage     `json:"extra_content,omitempty"`
	ReasoningContent json.RawMessage     `json:"reasoning_content,omitempty"`
	Refusal          json.RawMessage     `json:"refusal,omitempty"`
	ContentRaw       json.RawMessage     `json:"-"`
}

type openAIImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail"`
}
type openAIContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *openAIImageURL `json:"image_url,omitempty"`
}

func (message openAIMessage) MarshalJSON() ([]byte, error) {
	type plain openAIMessage
	if len(message.ContentRaw) != 0 {
		return json.Marshal(struct {
			plain
			Content json.RawMessage `json:"content"`
		}{plain: plain(message), Content: message.ContentRaw})
	}
	if len(message.Parts) == 0 {
		return json.Marshal(plain(message))
	}
	return json.Marshal(struct {
		plain
		Content []openAIContentPart `json:"content"`
	}{plain: plain(message), Content: message.Parts})
}

func (message *openAIMessage) UnmarshalJSON(raw []byte) error {
	type plain openAIMessage
	var value plain
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	*message = openAIMessage(value)
	message.ContentRaw = append(json.RawMessage(nil), fields["content"]...)
	// Private native fields require exact wire spelling. encoding/json's
	// case-insensitive fallback must not promote an unknown extension.
	message.ReasoningContent = append(json.RawMessage(nil), fields["reasoning_content"]...)
	message.Refusal = append(json.RawMessage(nil), fields["refusal"]...)
	return nil
}

type openAITool struct {
	Type     string             `json:"type"`
	Function openAIFunctionSpec `json:"function"`
}

type openAIFunctionSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type openAIToolCall struct {
	ID           string             `json:"id"`
	Type         string             `json:"type"`
	Function     openAIFunctionCall `json:"function"`
	ExtraContent json.RawMessage    `json:"extra_content,omitempty"`
}

type openAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIResponseFormat struct {
	Type string `json:"type"`
}

type openAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIUsage struct {
	PromptTokens          *int                     `json:"prompt_tokens"`
	CompletionTokens      *int                     `json:"completion_tokens"`
	TotalTokens           *int                     `json:"total_tokens"`
	PromptTokensDetails   *openAIInputTokenDetails `json:"prompt_tokens_details"`
	PromptCacheHitTokens  *int                     `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens *int                     `json:"prompt_cache_miss_tokens"`
}

type openAIError struct {
	Message string          `json:"message"`
	Type    string          `json:"type"`
	Code    json.RawMessage `json:"code"`
}

type openAIChatResponse struct {
	ID      string         `json:"id"`
	Model   string         `json:"model"`
	Choices []openAIChoice `json:"choices"`
	Usage   *openAIUsage   `json:"usage"`
	Error   *openAIError   `json:"error,omitempty"`
}

type openAIChoice struct {
	Index        *int              `json:"index"`
	Message      openAIMessage     `json:"message"`
	Delta        openAIStreamDelta `json:"delta"`
	FinishReason string            `json:"finish_reason"`
}

type openAIStreamDelta struct {
	Role             string                 `json:"role"`
	Content          *string                `json:"content"`
	ToolCalls        []openAIStreamToolCall `json:"tool_calls"`
	ExtraContent     json.RawMessage        `json:"extra_content,omitempty"`
	ReasoningContent json.RawMessage        `json:"reasoning_content,omitempty"`
	Refusal          json.RawMessage        `json:"refusal,omitempty"`
}

func (delta *openAIStreamDelta) UnmarshalJSON(raw []byte) error {
	type plain openAIStreamDelta
	var value plain
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	*delta = openAIStreamDelta(value)
	delta.ReasoningContent = append(json.RawMessage(nil), fields["reasoning_content"]...)
	delta.Refusal = append(json.RawMessage(nil), fields["refusal"]...)
	return nil
}

type openAIStreamToolCall struct {
	Index        *int               `json:"index"`
	ID           string             `json:"id"`
	Type         string             `json:"type"`
	Function     openAIFunctionCall `json:"function"`
	ExtraContent json.RawMessage    `json:"extra_content,omitempty"`
}

type openAIStreamResponse struct {
	ID      string         `json:"id"`
	Model   string         `json:"model"`
	Choices []openAIChoice `json:"choices"`
	Usage   *openAIUsage   `json:"usage"`
	Error   *openAIError   `json:"error,omitempty"`
}

type openAIStreamToolState struct {
	id        string
	name      string
	idSeen    bool
	nameSeen  bool
	itemID    string
	itemSeen  bool
	callSeen  bool
	arguments strings.Builder
	pending   []string
}

type openAIStreamState struct {
	provider       string
	model          string
	upstreamModel  string
	responseModel  bool
	finished       bool
	finishReason   string
	usage          *Usage
	textBytes      int
	refusalBytes   int
	hasTextDelta   bool
	textItemSeen   bool
	tools          map[int]*openAIStreamToolState
	events         providerStreamEvents
	gemini         *geminiReplayBuilder
	geminiProvider *OpenAICompatibleProvider
	kimi           *kimiReplayBuilder
	kimiProvider   *OpenAICompatibleProvider
	wireModel      string
}

func (s *openAIStreamState) consume(payload []byte) (*ChatChunk, error) {
	if !utf8.Valid(payload) {
		return nil, openAIProtocolError(s.provider, "returned a non-UTF-8 stream event")
	}
	if (s.gemini != nil || s.kimi != nil) && validateGeminiWireJSON(payload) != nil {
		return nil, openAIProtocolError(s.provider, "returned ambiguous native metadata")
	}
	var event openAIStreamResponse
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, openAIProtocolError(s.provider, "returned a malformed stream event")
	}
	if event.Error != nil {
		return nil, openAIWireError(s.provider, *event.Error)
	}
	if strings.TrimSpace(event.Model) != "" {
		model, err := normalizeOpenAIModel(event.Model)
		if err != nil {
			return nil, openAIProtocolError(s.provider, "returned an invalid stream model")
		}
		if s.responseModel && model != s.upstreamModel {
			return nil, openAIProtocolError(s.provider, "changed model identity during the stream")
		}
		s.upstreamModel, s.responseModel = model, true
	}
	if s.gemini != nil && s.gemini.observeID(event.ID) != nil {
		return nil, openAIProtocolError(s.provider, "stream returned invalid Gemini response identity")
	}
	if s.kimi != nil && s.kimi.observeID(event.ID) != nil {
		return nil, openAIProtocolError(s.provider, "stream returned invalid Kimi response identity")
	}
	if len(event.Choices) == 0 {
		if event.Usage == nil || s.usage != nil || !s.finished || !s.responseModel ||
			strings.TrimSpace(event.Model) == "" {
			return nil, openAIProtocolError(s.provider, "returned an invalid stream choice")
		}
		usage, err := normalizeOpenAIUsage(*event.Usage)
		if err != nil {
			return nil, openAIProtocolError(s.provider, "returned invalid stream usage")
		}
		s.usage = &usage
		events := s.ensureStreamStarted()
		if len(events) == 0 {
			return nil, nil
		}
		return &ChatChunk{Events: events}, nil
	}
	if len(event.Choices) != 1 || event.Choices[0].Index == nil || *event.Choices[0].Index != 0 || s.finished {
		return nil, openAIProtocolError(s.provider, "returned an invalid stream choice")
	}
	choice := event.Choices[0]
	var reasoningDelta string
	if json.Unmarshal(choice.Delta.ReasoningContent, &reasoningDelta) == nil && reasoningDelta != "" {
		s.events.markGeneration()
	}
	if choice.Delta.Role != "" && choice.Delta.Role != "assistant" {
		return nil, openAIProtocolError(s.provider, "returned an invalid stream role")
	}
	refusal, err := decodeOpenAIRefusal(choice.Delta.Refusal)
	if err != nil || s.refusalBytes > MaxModelOutputBytes-len(refusal) {
		return nil, openAIProtocolError(s.provider, "returned invalid streamed refusal")
	}
	s.refusalBytes += len(refusal)
	if s.gemini != nil && s.gemini.capture(choice.Delta.ExtraContent, nil) != nil {
		return nil, openAIProtocolError(s.provider, "stream returned invalid Gemini message signature")
	}
	if s.kimi != nil {
		if s.kimi.capture(choice.Delta.ReasoningContent, true) != nil {
			return nil, openAIProtocolError(s.provider, "stream returned invalid Kimi private reasoning")
		}
		s.kimi.observeContent(choice.Delta.Content)
	}
	streamEvents := s.ensureStreamStarted()
	chunk := &ChatChunk{}
	if choice.Delta.Content != nil && *choice.Delta.Content != "" {
		text := *choice.Delta.Content
		if s.textBytes > MaxModelOutputBytes-len(text) {
			return nil, openAIProtocolError(s.provider, "streamed text exceeds the output limit")
		}
		s.textBytes += len(text)
		s.hasTextDelta = true
		if s.gemini != nil {
			s.gemini.text.WriteString(text)
		}
		if s.kimi != nil {
			s.kimi.text.WriteString(text)
		}
		if !s.textItemSeen {
			s.textItemSeen = true
			streamEvents = append(streamEvents, s.events.emit(StreamEvent{
				Type: StreamOutputItemStarted, ItemID: "message", ItemType: StreamItemMessage,
				ItemStatus: StreamItemInProgress,
			}))
		}
		streamEvents = append(streamEvents, s.events.emit(StreamEvent{
			Type: StreamTextDelta, ItemID: "message", ItemType: StreamItemMessage,
			ItemStatus: StreamItemInProgress, TextDelta: text,
		}))
		chunk.Text = text
	}
	for _, delta := range choice.Delta.ToolCalls {
		events, err := s.consumeToolDelta(delta)
		if err != nil {
			return nil, openAIProtocolError(s.provider, "returned an invalid tool-call delta")
		}
		if s.gemini != nil && s.gemini.capture(delta.ExtraContent, delta.Index) != nil {
			return nil, openAIProtocolError(s.provider, "stream returned invalid Gemini tool signature")
		}
		streamEvents = append(streamEvents, events...)
	}
	if choice.FinishReason != "" {
		s.finished = true
		s.finishReason = strings.TrimSpace(choice.FinishReason)
	}
	if event.Usage != nil {
		// OpenAI emits a final empty-choices usage event when include_usage is
		// enabled. Some compatible endpoints attach the same exact usage object
		// to the choice that carries finish_reason. Accept both terminal forms,
		// while rejecting early, duplicate, or structurally incomplete usage.
		if s.usage != nil || !s.finished || !s.responseModel ||
			strings.TrimSpace(event.Model) == "" {
			return nil, openAIProtocolError(s.provider, "returned usage before the final stream event")
		}
		usage, err := normalizeOpenAIUsage(*event.Usage)
		if err != nil {
			return nil, openAIProtocolError(s.provider, "returned invalid stream usage")
		}
		s.usage = &usage
	}
	chunk.Events = streamEvents
	if chunk.Text == "" && len(chunk.Events) == 0 {
		return nil, nil
	}
	return chunk, nil
}

func (s *openAIStreamState) consumeToolDelta(delta openAIStreamToolCall) ([]StreamEvent, error) {
	events := make([]StreamEvent, 0, 4)
	if delta.Index == nil || *delta.Index < 0 || *delta.Index >= MaxProviderToolCalls {
		return nil, errors.New("tool-call index is invalid")
	}
	if delta.Type != "" && delta.Type != "function" {
		return nil, errors.New("tool-call type is unsupported")
	}
	if s.tools == nil {
		s.tools = make(map[int]*openAIStreamToolState)
	}
	state, exists := s.tools[*delta.Index]
	if !exists {
		if len(s.tools) >= MaxProviderToolCalls {
			return nil, errors.New("too many tool calls")
		}
		state = &openAIStreamToolState{itemID: fmt.Sprintf("tool/%d", *delta.Index)}
		s.tools[*delta.Index] = state
	}
	if !state.itemSeen {
		state.itemSeen = true
		events = append(events, s.events.emit(StreamEvent{
			Type: StreamOutputItemStarted, ItemID: state.itemID, ItemType: StreamItemToolCall,
			ItemStatus: StreamItemInProgress,
		}))
	}
	if delta.ID != "" {
		if state.idSeen && delta.ID != state.id {
			return nil, errors.New("tool-call id changed")
		}
		state.id, state.idSeen = delta.ID, true
	}
	if delta.Function.Name != "" {
		if state.nameSeen && delta.Function.Name != state.name {
			return nil, errors.New("tool-call name changed")
		}
		state.name, state.nameSeen = delta.Function.Name, true
	}
	if !utf8.ValidString(state.id) || !utf8.ValidString(state.name) ||
		len([]rune(state.id)) > MaxProviderToolIdentity || len([]rune(state.name)) > MaxProviderToolIdentity {
		return nil, errors.New("tool-call identity exceeds the limit")
	}
	if state.arguments.Len() > MaxProviderToolPayloadSize-len(delta.Function.Arguments) {
		return nil, errors.New("tool-call arguments exceed the limit")
	}
	_, _ = state.arguments.WriteString(delta.Function.Arguments)
	if delta.Function.Arguments != "" {
		state.pending = append(state.pending, delta.Function.Arguments)
	}
	if !state.callSeen && state.idSeen && state.nameSeen {
		if err := validateToolName(state.name); err != nil {
			return nil, err
		}
		state.callSeen = true
		events = append(events, s.events.emit(StreamEvent{
			Type: StreamToolCallStarted, ItemID: state.itemID, CallID: state.id,
			ItemType: StreamItemToolCall, ItemStatus: StreamItemInProgress, ToolName: state.name,
		}))
		for _, pending := range state.pending {
			events = append(events, s.events.emit(StreamEvent{
				Type: StreamToolArgumentDelta, ItemID: state.itemID, CallID: state.id,
				ItemType: StreamItemToolCall, ItemStatus: StreamItemInProgress,
				ToolName: state.name, ArgumentDelta: pending,
			}))
		}
		state.pending = nil
	} else if state.callSeen && delta.Function.Arguments != "" {
		pending := state.pending[len(state.pending)-1]
		state.pending = state.pending[:len(state.pending)-1]
		events = append(events, s.events.emit(StreamEvent{
			Type: StreamToolArgumentDelta, ItemID: state.itemID, CallID: state.id,
			ItemType: StreamItemToolCall, ItemStatus: StreamItemInProgress,
			ToolName: state.name, ArgumentDelta: pending,
		}))
	}
	return events, nil
}

func (s *openAIStreamState) finalChunk() (ChatChunk, error) {
	if !s.finished {
		return ChatChunk{}, openAIProtocolError(s.provider, "stream ended without a finish reason")
	}
	if s.usage == nil {
		return ChatChunk{}, openAIProtocolError(s.provider, "stream omitted token usage")
	}
	if !s.responseModel {
		return ChatChunk{}, openAIProtocolError(s.provider, "stream omitted its model identity")
	}
	finishReason := openAIFinishReason(s.finishReason)
	usage := *s.usage
	if s.refusalBytes > 0 {
		if s.hasTextDelta || len(s.tools) != 0 ||
			(finishReason != FinishReasonStop && finishReason != FinishReasonRefusal) {
			return ChatChunk{}, openAIProtocolError(s.provider, "mixed a refusal with incompatible output")
		}
		finishReason = FinishReasonRefusal
	}
	if completionErr := CompletionError(s.provider, finishReason); completionErr != nil {
		return ChatChunk{Done: false, Usage: &usage, Model: s.model, Provider: s.provider,
			FinishReason: finishReason, Err: completionErr,
			Events: []StreamEvent{s.events.terminalEvent(OutcomePermanent, &usage)}}, nil
	}
	indices := make([]int, 0, len(s.tools))
	for index := range s.tools {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	calls := make([]ToolCall, 0, len(indices))
	for expected, index := range indices {
		if index != expected {
			return ChatChunk{}, openAIProtocolError(s.provider, "stream returned sparse tool-call indices")
		}
		state := s.tools[index]
		arguments := state.arguments.String()
		if strings.TrimSpace(arguments) == "" {
			arguments = `{}`
		}
		calls = append(calls, ToolCall{ID: state.id, Name: state.name,
			Arguments: json.RawMessage(arguments)})
	}
	var err error
	calls, err = NormalizeToolCalls(calls)
	if err != nil {
		return ChatChunk{}, openAIProtocolError(s.provider, "stream returned invalid tool calls")
	}
	if err := validateOpenAIFinishReason(finishReason, s.hasTextDelta, len(calls)); err != nil {
		return ChatChunk{}, openAIProtocolError(s.provider, "stream returned an incompatible finish reason")
	}
	var replay *ProviderReplay
	if s.gemini != nil {
		replay, err = s.gemini.replay(s.geminiProvider, s.model, s.wireModel, s.upstreamModel, s.gemini.text.String(), calls)
		if err != nil {
			return ChatChunk{}, openAIProtocolError(s.provider, "stream returned invalid Gemini signature replay")
		}
	}
	if s.kimi != nil {
		if (len(calls) == 0 && finishReason != FinishReasonStop) ||
			(len(calls) != 0 && finishReason != FinishReasonToolCalls) {
			return ChatChunk{}, openAIProtocolError(s.provider, "stream did not complete its Kimi native response")
		}
		for position, index := range indices {
			if !replayIdentity(s.tools[index].id) || s.tools[index].id != calls[position].ID || s.tools[index].name != calls[position].Name {
				return ChatChunk{}, openAIProtocolError(s.provider, "stream changed its Kimi native tool identity during acceptance")
			}
		}
		replay, err = s.kimi.replay(s.kimiProvider, s.model, s.wireModel, s.upstreamModel, s.kimi.text.String(), calls)
		if err != nil {
			return ChatChunk{}, openAIProtocolError(s.provider, "stream returned invalid Kimi private replay")
		}
	}
	events := make([]StreamEvent, 0, 3+len(calls)*2)
	if s.textItemSeen {
		events = append(events, s.events.emit(StreamEvent{
			Type: StreamOutputItemCompleted, ItemID: "message", ItemType: StreamItemMessage,
			ItemStatus: StreamItemCompleted,
		}))
	}
	for _, index := range indices {
		state := s.tools[index]
		if !state.callSeen || len(state.pending) != 0 {
			return ChatChunk{}, openAIProtocolError(s.provider, "stream returned an unfinished tool call")
		}
		var call ToolCall
		for _, current := range calls {
			if current.ID == state.id {
				call = current
				break
			}
		}
		events = append(events, s.events.emit(StreamEvent{
			Type: StreamToolCallCompleted, ItemID: state.itemID, CallID: state.id,
			ItemType: StreamItemToolCall, ItemStatus: StreamItemReadyForValidation,
			ToolName: state.name, CompletedCall: &call,
		}))
		events = append(events, s.events.emit(StreamEvent{
			Type: StreamOutputItemCompleted, ItemID: state.itemID, CallID: state.id,
			ItemType: StreamItemToolCall, ItemStatus: StreamItemCompleted, ToolName: state.name,
		}))
	}
	events = append(events, s.events.terminalEvent(OutcomeSuccess, &usage))
	return ChatChunk{Done: true, ToolCalls: calls, Replay: replay, Events: events, Usage: &usage,
		Model: s.model, Provider: s.provider, FinishReason: finishReason}, nil
}

func (s *openAIStreamState) ensureStreamStarted() []StreamEvent {
	if s.events.started {
		return nil
	}
	return []StreamEvent{s.events.start()}
}

func (p *OpenAICompatibleProvider) readStream(ctx context.Context, body io.ReadCloser,
	model string, chunks chan<- ChatChunk, wireModels ...string,
) {
	defer close(chunks)
	defer body.Close()
	state := openAIStreamState{provider: p.name, model: model,
		events: newProviderStreamEvents(p.name, model, "openai-response", StreamGranularityDelta)}
	wireModel := model
	if len(wireModels) != 0 {
		wireModel = wireModels[0]
	}
	if GeminiThoughtSignatureScope(p.baseURL, wireModel) {
		state.gemini, state.geminiProvider, state.wireModel = &geminiReplayBuilder{}, p, wireModel
	}
	if KimiReasoningScope(p.baseURL, wireModel) {
		state.kimi, state.kimiProvider, state.wireModel = &kimiReplayBuilder{}, p, wireModel
	}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), maxOpenAIStreamLineBytes)
	lines := providerStreamLines{}
	scanner.Split(lines.split)
	dataLines := make([]string, 0, 1)
	eventBytes := 0
	stopped := false
	sendError := func(err error) bool {
		chunk := state.events.failureChunk(err)
		if state.usage != nil {
			usage := *state.usage
			chunk.Usage = &usage
			for index := range chunk.Events {
				if chunk.Events[index].Type == StreamResponseFailed || chunk.Events[index].Type == StreamResponseCancelled {
					chunk.Events[index].Usage = &usage
				}
			}
		}
		return p.sendStreamChunk(ctx, chunks, chunk)
	}
	flush := func() bool {
		if len(dataLines) == 0 {
			return true
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		eventBytes = 0
		if strings.TrimSpace(payload) == "[DONE]" {
			final, err := state.finalChunk()
			if err != nil {
				_ = sendError(err)
				return false
			}
			stopped = true
			return p.sendStreamChunk(ctx, chunks, final)
		}
		chunk, err := state.consume([]byte(payload))
		if err != nil {
			_ = sendError(err)
			return false
		}
		return chunk == nil || p.sendStreamChunk(ctx, chunks, *chunk)
	}
	for scanner.Scan() {
		if err := scanner.Err(); err != nil && !lines.terminated {
			_ = sendError(openAIReadError(ctx, p.name, "stream read failed", err))
			return
		}
		line := scanner.Text()
		if line == "" {
			if !flush() || stopped {
				return
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if !found || field != "data" {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		if !utf8.ValidString(value) || eventBytes > maxOpenAIStreamEventBytes-len(value)-1 {
			_ = sendError(openAIProtocolError(p.name, "stream event exceeds its UTF-8 size limit"))
			return
		}
		eventBytes += len(value) + 1
		dataLines = append(dataLines, value)
	}
	if stopped || ctx.Err() != nil {
		return
	}
	if scanner.Err() != nil {
		_ = sendError(openAIReadError(ctx, p.name, "stream read failed", scanner.Err()))
		return
	}
	if len(dataLines) != 0 {
		if !flush() || stopped {
			return
		}
	}
	_ = sendError(openAIProtocolError(p.name, "stream ended before [DONE]"))
}

func (p *OpenAICompatibleProvider) sendStreamChunk(ctx context.Context,
	chunks chan<- ChatChunk, chunk ChatChunk,
) bool {
	select {
	case <-ctx.Done():
		return false
	case chunks <- chunk:
		return true
	}
}
