package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const AnthropicVersion = "2023-06-01"

const (
	maxProviderBaseURLBytes = 2048
	maxProviderAPIKeyBytes  = 16 * 1024
)

type AnthropicCompatibleConfig struct {
	Name            string
	BaseURL         string
	APIKey          string
	DefaultModel    string
	DisableThinking bool
	HTTPClient      *http.Client
	Runtime         HTTPProviderRuntime
}

type AnthropicCompatibleProvider struct {
	name            string
	baseURL         string
	apiKey          string
	defaultModel    string
	disableThinking bool
	client          *http.Client
	runtime         HTTPProviderRuntime
}

func NewAnthropicCompatibleProvider(config AnthropicCompatibleConfig) (*AnthropicCompatibleProvider, error) {
	name := strings.TrimSpace(config.Name)
	if name == "" {
		name = "anthropic_compatible"
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
	client, err := providerHTTPClient(config.HTTPClient)
	if err != nil {
		return nil, err
	}
	defaultModel := strings.TrimSpace(config.DefaultModel)
	if defaultModel == "" {
		defaultModel = "claude-3-5-sonnet-latest"
	}
	return &AnthropicCompatibleProvider{
		name:            name,
		baseURL:         baseURL,
		apiKey:          config.APIKey,
		defaultModel:    defaultModel,
		disableThinking: config.DisableThinking,
		client:          client,
		runtime:         config.Runtime,
	}, nil
}

func normalizeProviderBaseURL(value string, provider string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if value == "" {
		return "", fmt.Errorf("base url is required for provider %s", provider)
	}
	if len([]byte(value)) > maxProviderBaseURLBytes {
		return "", fmt.Errorf("base url for provider %s exceeds %d bytes", provider,
			maxProviderBaseURLBytes)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", fmt.Errorf("base url for provider %s must be an absolute HTTP(S) URL", provider)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("base url for provider %s cannot contain credentials, query, or fragment", provider)
	}
	if parsed.Scheme == "http" && !providerLoopbackHost(parsed.Hostname()) {
		return "", fmt.Errorf("base url for provider %s must use HTTPS outside loopback", provider)
	}
	return value, nil
}

func providerLoopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	return ip != nil && ip.IsLoopback()
}

func validateProviderAPIKey(value string, provider string) error {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) ||
		len([]byte(value)) > maxProviderAPIKeyBytes {
		return fmt.Errorf("api key for provider %s must be normalized UTF-8 within %d bytes",
			provider, maxProviderAPIKeyBytes)
	}
	for _, current := range value {
		if unicode.IsControl(current) || unicode.IsSpace(current) {
			return fmt.Errorf("api key for provider %s cannot contain whitespace or control characters",
				provider)
		}
	}
	return nil
}

func (p *AnthropicCompatibleProvider) Name() string {
	return p.name
}

func (p *AnthropicCompatibleProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	secret, err := providerRequestCredential(ctx, p.name, p.apiKey, p.runtime, p.baseURL)
	if err != nil {
		return nil, NewProviderError(OutcomePermanent, p.name,
			"provider credential is unavailable", nil)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.modelsEndpoint(), nil)
	if err != nil {
		return nil, err
	}
	if err := p.addHeaders(req, secret, "application/json"); err != nil {
		return nil, NewProviderError(OutcomePermanent, p.name,
			"could not prepare model-list headers", nil)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return fallbackModelInfo(p.name, p.defaultModel), nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fallbackModelInfo(p.name, p.defaultModel), nil
	}
	var payload struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return fallbackModelInfo(p.name, p.defaultModel), nil
	}
	if len(payload.Data) == 0 {
		return fallbackModelInfo(p.name, p.defaultModel), nil
	}
	models := make([]ModelInfo, 0, len(payload.Data))
	for _, item := range payload.Data {
		if item.ID == "" {
			continue
		}
		display := item.DisplayName
		if display == "" {
			display = item.ID
		}
		models = append(models, ModelInfo{
			ID:           item.ID,
			DisplayName:  display,
			Provider:     p.name,
			Capabilities: []string{"chat", "tools"},
		})
	}
	if len(models) == 0 {
		return fallbackModelInfo(p.name, p.defaultModel), nil
	}
	return models, nil
}

func (p *AnthropicCompatibleProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	selectedModel := strings.TrimSpace(req.Model)
	if selectedModel == "" {
		selectedModel = p.defaultModel
	}
	wireModel, err := providerRequestModel(p.runtime, selectedModel)
	if err != nil {
		return nil, NewProviderError(OutcomePermanent, p.name, "could not map model", nil)
	}
	body, err := p.toRequest(wireModel, req)
	if err != nil {
		return nil, NewProviderError(OutcomePermanent, p.name, "could not prepare request", err)
	}
	secret, err := providerRequestCredential(ctx, p.name, p.apiKey, p.runtime, p.baseURL)
	if err != nil {
		return nil, NewProviderError(OutcomePermanent, p.name,
			"provider credential is unavailable", nil)
	}
	payload, err := providerRequestPayload(p.runtime, secret, body)
	if err != nil {
		return nil, NewProviderError(OutcomePermanent, p.name, "could not encode request", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint("/v1/messages"), bytes.NewReader(payload))
	if err != nil {
		return nil, NewProviderError(OutcomePermanent, p.name, "could not create request", err)
	}
	if err := p.addHeaders(httpReq, secret, "application/json"); err != nil {
		return nil, NewProviderError(OutcomePermanent, p.name,
			"could not prepare request headers", nil)
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, anthropicTransportError(ctx, p.name, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, anthropicTransportError(ctx, p.name, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, anthropicHTTPError(p.name, resp.StatusCode, resp.Header.Get("Retry-After"), raw)
	}
	var parsed anthropicMessageResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, NewProviderError(OutcomeInvalidResponse, p.name, "returned malformed JSON", err)
	}
	finishReason := anthropicFinishReason(parsed.StopReason)
	native := anthropicReplayBuilder{responseID: parsed.ID}
	var text strings.Builder
	var textParts []string
	for index, rawBlock := range parsed.Content {
		if native.startBlock(index, rawBlock) != nil || native.endBlock(index) != nil {
			return nil, NewProviderError(OutcomeInvalidResponse, p.name, "returned an unsupported native content block", nil)
		}
		if block := native.blocks[index].block; block.Type == "text" {
			text.WriteString(*block.Text)
			if strings.TrimSpace(*block.Text) != "" {
				textParts = append(textParts, *block.Text)
			}
		}
	}
	var toolCalls []ToolCall
	if CompletionError(p.name, finishReason) == nil {
		for _, pending := range native.blocks {
			block := pending.block
			if block.Type == "tool_use" {
				toolCalls = append(toolCalls, ToolCall{ID: block.ID, Name: block.Name, Arguments: append(json.RawMessage(nil), block.Input...)})
			}
		}
		toolCalls, err = NormalizeToolCalls(toolCalls)
		if err != nil {
			return nil, NewProviderError(OutcomeInvalidResponse, p.name, "returned invalid tool calls", err)
		}
		if err := validateAnthropicFinishReason(finishReason, strings.TrimSpace(text.String()) != "", len(toolCalls)); err != nil {
			return nil, NewProviderError(OutcomeInvalidResponse, p.name, "returned an incompatible stop reason", err)
		}
	}
	responseModel := p.responseModel(selectedModel, parsed.ModelOrDefault(selectedModel))
	var replay *ProviderReplay
	if len(toolCalls) > 0 && native.hasPrivate() {
		if responseModel != selectedModel {
			return nil, NewProviderError(OutcomeInvalidResponse, p.name, "native replay model does not match the requested source", nil)
		}
		replay, err = native.finish(p.name, selectedModel, p.replayBinding(selectedModel), toolCalls)
		if err != nil {
			return nil, NewProviderError(OutcomeInvalidResponse, p.name, "returned invalid native replay state", nil)
		}
	}
	publicText := strings.Join(textParts, "\n")
	if replay != nil {
		publicText = replay.AssistantText()
	}
	result := &ChatResponse{
		Text:      publicText,
		ToolCalls: toolCalls,
		Replay:    replay,
		// Keep the upstream response inside the adapter. Raw model payloads are
		// not part of the Supervisor contract and must not cross persistence or
		// activity boundaries.
		Raw:          nil,
		Model:        responseModel,
		Provider:     p.name,
		FinishReason: finishReason,
	}
	result.Usage, err = parsed.Usage.normalized()
	if err != nil {
		return nil, NewProviderError(OutcomeInvalidResponse, p.name, "returned invalid usage", err)
	}
	if completionErr := CompletionError(p.name, finishReason); completionErr != nil {
		return result, completionErr
	}
	return result, nil
}

func (p *AnthropicCompatibleProvider) responseModel(requested, returned string) string {
	if p.runtime != nil {
		// A custom definition may map its stable local route identity to a
		// different upstream model identifier. Do not let that wire detail escape
		// back into the Router/Supervisor contract.
		return requested
	}
	// The official DeepSeek Messages endpoint returns this shorter identity for
	// the documented v4-flash request (observed in both live probe responses).
	// Keep this exact alias inside the adapter; do not hide arbitrary model drift
	// or apply the exception to another provider, endpoint, or requested model.
	if p.hasDeepSeekFlashResponseAlias(requested) && returned == "deepseek-flash" {
		return requested
	}
	return returned
}

func (p *AnthropicCompatibleProvider) hasDeepSeekFlashResponseAlias(model string) bool {
	return p.runtime == nil && p.name == "deepseek" &&
		p.baseURL == "https://api.deepseek.com/anthropic" && model == "deepseek-v4-flash"
}

func anthropicHTTPError(provider string, statusCode int, retryAfterHeader string, raw []byte) *ProviderError {
	// The response body is deliberately ignored. Provider error text may be
	// persisted by the Supervisor, and a remote endpoint can echo prompts,
	// tool arguments, credentials, or arbitrary private content in that body.
	kind := OutcomePermanent
	reason := ProviderFailureProtocolIncompatible
	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		reason = ProviderFailureAuthentication
	case http.StatusNotFound:
		// A bare 404 can also mean that the configured Anthropic-compatible
		// endpoint does not implement /v1/messages. Without an exact safe wire
		// code, keep the conservative protocol classification.
		reason = ProviderFailureProtocolIncompatible
	case http.StatusTooManyRequests:
		kind = OutcomeRateLimited
		reason = ProviderFailureRateLimit
	case http.StatusServiceUnavailable, 529:
		kind = OutcomeRetryable
		reason = ProviderFailureCapacity
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusGatewayTimeout:
		kind = OutcomeRetryable
		reason = ProviderFailureNetwork
	}
	if statusCode == http.StatusBadRequest && anthropicContextLimit(raw) {
		kind, reason = OutcomePermanent, ProviderFailureContextLimit
	}
	message := fmt.Sprintf("returned HTTP %d", statusCode)
	err := NewProviderError(kind, provider, message, nil)
	err.StatusCode = statusCode
	err.RetryAfter = parseRetryAfter(retryAfterHeader, time.Now())
	err.Reason = reason
	return err
}

func anthropicContextLimit(raw []byte) bool {
	if !utf8.Valid(raw) {
		return false
	}
	var envelope struct {
		Error *struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Error == nil {
		return false
	}
	code := strings.ToLower(strings.TrimSpace(envelope.Error.Code))
	if code == "context_length_exceeded" || code == "model_context_window_exceeded" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(envelope.Error.Type), "invalid_request_error") &&
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(envelope.Error.Message)), "prompt is too long:")
}

func anthropicFinishReason(reason string) FinishReason {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "":
		return ""
	case "end_turn", "stop_sequence", "stop":
		return FinishReasonStop
	case "tool_use", "tool_calls":
		return FinishReasonToolCalls
	case "max_tokens", "max_output_tokens", "length":
		return FinishReasonLength
	case "pause_turn", "pause":
		return FinishReasonPause
	case "refusal", "content_filter":
		return FinishReasonRefusal
	case "model_context_window_exceeded", "context_length_exceeded", "context_limit":
		return FinishReasonContextLimit
	default:
		return FinishReasonUnknown
	}
}

func validateAnthropicFinishReason(reason FinishReason, hasText bool, toolCount int) error {
	switch reason {
	case FinishReasonStop:
		if !hasText || toolCount != 0 {
			return errors.New("stop reason did not contain text only")
		}
	case FinishReasonToolCalls:
		if toolCount == 0 {
			return errors.New("tool stop reason did not contain calls")
		}
	case "", FinishReasonUnknown:
		if !hasText && toolCount == 0 {
			return errors.New("legacy stop reason did not contain usable output")
		}
	default:
		return errors.New("stop reason is not successful")
	}
	return nil
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		const maxDuration = time.Duration(1<<63 - 1)
		if seconds > int64(maxDuration/time.Second) {
			return maxDuration
		}
		return time.Duration(seconds) * time.Second
	}
	when, err := http.ParseTime(value)
	if err != nil || !when.After(now) {
		return 0
	}
	return when.Sub(now)
}

func (p *AnthropicCompatibleProvider) StreamChat(ctx context.Context, req ChatRequest) (<-chan ChatChunk, error) {
	selectedModel := strings.TrimSpace(req.Model)
	if selectedModel == "" {
		selectedModel = p.defaultModel
	}
	wireModel, err := providerRequestModel(p.runtime, selectedModel)
	if err != nil {
		return nil, NewProviderError(OutcomePermanent, p.name, "could not map model", nil)
	}
	body, err := p.toRequest(wireModel, req)
	if err != nil {
		return nil, NewProviderError(OutcomePermanent, p.name, "could not prepare streaming request", err)
	}
	body.Stream = true
	secret, err := providerRequestCredential(ctx, p.name, p.apiKey, p.runtime, p.baseURL)
	if err != nil {
		return nil, NewProviderError(OutcomePermanent, p.name,
			"provider credential is unavailable", nil)
	}
	payload, err := providerRequestPayload(p.runtime, secret, body)
	if err != nil {
		return nil, NewProviderError(OutcomePermanent, p.name, "could not encode streaming request", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint("/v1/messages"), bytes.NewReader(payload))
	if err != nil {
		return nil, NewProviderError(OutcomePermanent, p.name, "could not create streaming request", err)
	}
	if err := p.addHeaders(httpReq, secret, "text/event-stream"); err != nil {
		return nil, NewProviderError(OutcomePermanent, p.name,
			"could not prepare streaming request headers", nil)
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, anthropicTransportError(ctx, p.name, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		if readErr != nil {
			return nil, anthropicTransportError(ctx, p.name, readErr)
		}
		return nil, anthropicHTTPError(p.name, resp.StatusCode, resp.Header.Get("Retry-After"), raw)
	}
	ch := make(chan ChatChunk, 8)
	go p.readStream(ctx, resp.Body, selectedModel, ch)
	return ch, nil
}

func anthropicTransportError(ctx context.Context, provider string, source error) *ProviderError {
	if err := providerContextError(ctx, provider); err != nil {
		return err
	}
	var typed *ProviderError
	if errors.As(source, &typed) {
		return NormalizeProviderError(provider, source)
	}
	var network net.Error
	if errors.As(source, &network) && network.Timeout() {
		err := NewProviderError(OutcomeRetryable, provider, "request failed", nil)
		err.Reason = ProviderFailureNetwork
		return err
	}
	return NormalizeProviderError(provider, source)
}

func (p *AnthropicCompatibleProvider) readStream(ctx context.Context, body io.ReadCloser, defaultModel string, chunks chan<- ChatChunk) {
	defer close(chunks)
	defer body.Close()
	state := anthropicStreamState{model: defaultModel,
		selectedModel: defaultModel, replayBinding: p.replayBinding(defaultModel),
		responseModel: func(returned string) string { return p.responseModel(defaultModel, returned) },
		events:        newProviderStreamEvents(p.name, defaultModel, "anthropic-response", StreamGranularityDelta)}
	sendError := func(err error) bool {
		return p.sendStreamChunk(ctx, chunks, state.events.failureChunk(err))
	}
	err := readProviderSSE(ctx, body, func(payload string) bool {
		if payload == "[DONE]" {
			if state.pendingToolErr != nil {
				_ = sendError(state.pendingToolErr)
				return false
			}
			_ = sendError(NewProviderError(OutcomeInvalidResponse, p.name,
				"stream ended without message_stop", io.ErrUnexpectedEOF))
			return false
		}
		chunk, done, err := state.consume([]byte(payload), p.name)
		if err != nil {
			_ = sendError(err)
			return false
		}
		if chunk != nil && !p.sendStreamChunk(ctx, chunks, *chunk) {
			return false
		}
		if done {
			return false
		}
		return true
	})
	switch err {
	case nil:
		return
	case errProviderSSEEventLimit:
		failure := NewProviderError(OutcomeInvalidResponse, p.name,
			"stream SSE event exceeds its accumulation limit", nil)
		failure.Reason = ProviderFailureProtocolIncompatible
		_ = sendError(failure)
		return
	case io.EOF:
		// The provider state below decides whether EOF is a valid terminal.
	default:
		_ = sendError(providerHTTPReadError(ctx, p.name, "stream read failed", err))
		return
	}
	if state.pendingToolErr != nil {
		_ = sendError(state.pendingToolErr)
		return
	}
	_ = sendError(NewProviderError(OutcomeInvalidResponse, p.name,
		"stream ended before message_stop", io.ErrUnexpectedEOF))
}

func (p *AnthropicCompatibleProvider) sendStreamChunk(ctx context.Context, chunks chan<- ChatChunk, chunk ChatChunk) bool {
	if chunk.Done && strings.TrimSpace(chunk.Provider) == "" {
		chunk.Provider = p.name
	}
	select {
	case <-ctx.Done():
		return false
	case chunks <- chunk:
		return true
	}
}

func (p *AnthropicCompatibleProvider) SupportsTools(model string) bool {
	return true
}

func (p *AnthropicCompatibleProvider) SupportsVision(model string) bool {
	return p.DescribeVision(model).State == VisionSupported
}
func (p *AnthropicCompatibleProvider) DescribeVision(model string) VisionCapability {
	return runtimeVision(p.runtime, model)
}

func (p *AnthropicCompatibleProvider) SupportsJSONMode(model string) bool {
	return false
}

func (p *AnthropicCompatibleProvider) DescribeModelHarness(model string) ModelHarness {
	model = strings.TrimSpace(model)
	if model == "" {
		model = p.defaultModel
	}
	binding := []string{p.name, p.baseURL, model, HarnessTransportAnthropicMessages,
		HarnessToolStrategyNative, HarnessJSONStrategyPrompt}
	if p.hasDeepSeekFlashResponseAlias(model) {
		binding = append(binding, "deepseek-flash-response-alias")
	}
	return ModelHarness{
		ProtocolVersion:     ModelHarnessProtocolVersion,
		TransportProtocol:   HarnessTransportAnthropicMessages,
		ToolStrategy:        HarnessToolStrategyNative,
		JSONStrategy:        HarnessJSONStrategyPrompt,
		QualificationStatus: HarnessQualificationRequired,
		BindingDigest:       providerHarnessBinding(p.runtime, binding...),
	}
}

func (p *AnthropicCompatibleProvider) toRequest(model string, req ChatRequest) (anthropicMessageRequest, error) {
	selectedModel := strings.TrimSpace(req.Model)
	if selectedModel == "" {
		selectedModel = p.defaultModel
	}
	if err := validateRequestImages(p, selectedModel, req.Messages); err != nil {
		return anthropicMessageRequest{}, err
	}
	for _, message := range req.Messages {
		for _, image := range message.Images {
			if image.Width > 8000 || image.Height > 8000 {
				return anthropicMessageRequest{}, errors.New("Anthropic image dimensions exceed 8000 pixels")
			}
		}
	}
	maxTokens := req.MaxTokens
	if maxTokens < 0 || maxTokens > 1_000_000 {
		return anthropicMessageRequest{}, errors.New("max tokens is outside the provider request limit")
	}
	if maxTokens == 0 {
		maxTokens = req.PlannedOutputTokens(p.ModelContextWindow(selectedModel))
	}
	out := anthropicMessageRequest{
		Model:     model,
		MaxTokens: maxTokens,
		Messages:  make([]anthropicMessage, 0, len(req.Messages)),
		Tools:     make([]anthropicTool, 0, len(req.Tools)),
	}
	if p.disableThinking {
		out.Thinking = &anthropicThinking{Type: "disabled"}
	}
	if req.Temperature > 0 {
		out.Temperature = &req.Temperature
	}
	var systemParts []string
	var replayCallIDs map[string]string
	for _, msg := range req.Messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		content := strings.TrimSpace(msg.Content)
		if msg.Replay != nil && role != "assistant" {
			return anthropicMessageRequest{}, errors.New("Anthropic replay requires an assistant message")
		}
		if content == "" && len(msg.ToolCalls) == 0 && len(msg.ToolResults) == 0 && len(msg.Images) == 0 && msg.Replay == nil {
			continue
		}
		if len(replayCallIDs) > 0 {
			if role == "assistant" || role == "system" {
				return anthropicMessageRequest{}, errors.New("Anthropic replay is missing its adjacent tool results")
			}
			var err error
			msg.ToolResults, err = anthropicReplayResults(msg.ToolResults, replayCallIDs)
			if err != nil {
				return anthropicMessageRequest{}, err
			}
			replayCallIDs = nil
		}
		switch role {
		case "system":
			if len(msg.ToolCalls) > 0 || len(msg.ToolResults) > 0 {
				return anthropicMessageRequest{}, errors.New("system messages cannot contain tool blocks")
			}
			systemParts = append(systemParts, content)
		case "assistant":
			if msg.Replay != nil {
				encoded, aliases, err := anthropicReplayMessage(msg, p.name, selectedModel, p.replayBinding(selectedModel))
				if err != nil {
					return anthropicMessageRequest{}, err
				}
				out.Messages = append(out.Messages, anthropicMessage{Role: "assistant", Content: encoded})
				replayCallIDs = aliases
				continue
			}
			encoded, err := anthropicMessageContent(msg, true)
			if err != nil {
				return anthropicMessageRequest{}, err
			}
			out.Messages = append(out.Messages, anthropicMessage{Role: "assistant", Content: encoded})
		default:
			encoded, err := anthropicMessageContent(msg, false)
			if err != nil {
				return anthropicMessageRequest{}, err
			}
			out.Messages = append(out.Messages, anthropicMessage{Role: "user", Content: encoded})
		}
	}
	if len(replayCallIDs) > 0 {
		return anthropicMessageRequest{}, errors.New("Anthropic replay has an unresolved native tool batch")
	}
	if len(req.Tools) > MaxProviderToolSpecs {
		return anthropicMessageRequest{}, errors.New("tool specification count exceeds the provider limit")
	}
	for index, spec := range req.Tools {
		name := strings.TrimSpace(spec.Name)
		description := strings.TrimSpace(spec.Description)
		parameters := append(json.RawMessage(nil), bytes.TrimSpace(spec.Parameters)...)
		if name == "" || len(parameters) == 0 || !json.Valid(parameters) {
			return anthropicMessageRequest{}, fmt.Errorf("invalid tool specification at index %d", index)
		}
		out.Tools = append(out.Tools, anthropicTool{Name: name, Description: description, InputSchema: parameters})
	}
	if len(systemParts) > 0 {
		out.System = strings.Join(systemParts, "\n\n")
	}
	if len(out.Messages) == 0 {
		out.Messages = append(out.Messages, anthropicMessage{Role: "user", Content: "Hello"})
	}
	return out, nil
}

func (p *AnthropicCompatibleProvider) modelsEndpoint() string {
	if strings.HasSuffix(p.baseURL, "/v1/messages") {
		return strings.TrimSuffix(p.baseURL, "/messages") + "/models"
	}
	return p.endpoint("/v1/models")
}

func (p *AnthropicCompatibleProvider) endpoint(path string) string {
	if strings.HasSuffix(p.baseURL, path) {
		return p.baseURL
	}
	if strings.HasSuffix(p.baseURL, "/v1") && strings.HasPrefix(path, "/v1/") {
		return p.baseURL + strings.TrimPrefix(path, "/v1")
	}
	return p.baseURL + path
}

func (p *AnthropicCompatibleProvider) addHeaders(req *http.Request, secret string,
	accept string,
) error {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", accept)
	req.Header.Set("anthropic-version", AnthropicVersion)
	if secret != "" {
		req.Header.Set("x-api-key", secret)
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	return applyProviderRequestHeaders(p.runtime, secret, req.Header)
}

type anthropicMessageRequest struct {
	Model       string             `json:"model"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	Thinking    *anthropicThinking `json:"thinking,omitempty"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature *float64           `json:"temperature,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
}

type anthropicThinking struct {
	Type string `json:"type"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicContentBlock struct {
	Type      string                `json:"type"`
	Text      string                `json:"text,omitempty"`
	ID        string                `json:"id,omitempty"`
	Name      string                `json:"name,omitempty"`
	Input     json.RawMessage       `json:"input,omitempty"`
	ToolUseID string                `json:"tool_use_id,omitempty"`
	Content   string                `json:"content,omitempty"`
	IsError   bool                  `json:"is_error,omitempty"`
	Source    *anthropicImageSource `json:"source,omitempty"`
}
type anthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

func anthropicMessageContent(message Message, assistant bool) (any, error) {
	if message.Replay != nil {
		return nil, errors.New("Anthropic replay requires provider source validation")
	}
	if err := ValidateMessageImages(message); err != nil {
		return nil, err
	}
	content := strings.TrimSpace(message.Content)
	if assistant && len(message.ToolResults) > 0 {
		return nil, errors.New("assistant messages cannot contain tool results")
	}
	if !assistant && len(message.ToolCalls) > 0 {
		return nil, errors.New("user messages cannot contain tool calls")
	}
	if len(message.ToolCalls) == 0 && len(message.ToolResults) == 0 && len(message.Images) == 0 {
		return content, nil
	}
	blocks := make([]anthropicContentBlock, 0, 1+len(message.ToolCalls)+len(message.ToolResults))
	if assistant {
		if content != "" {
			blocks = append(blocks, anthropicContentBlock{Type: "text", Text: content})
		}
		calls, err := NormalizeToolCalls(message.ToolCalls)
		if err != nil {
			return nil, err
		}
		for _, call := range calls {
			blocks = append(blocks, anthropicContentBlock{
				Type: "tool_use", ID: call.ID, Name: call.Name,
				Input: append(json.RawMessage(nil), call.Arguments...),
			})
		}
	} else {
		for _, result := range message.ToolResults {
			normalized, err := NormalizeToolResult(result)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, anthropicContentBlock{
				Type: "tool_result", ToolUseID: normalized.ToolCallID,
				Content: normalized.Content, IsError: normalized.IsError,
			})
		}
		// Anthropic Messages requires every tool_result block to precede any
		// accompanying text in the same user message.
		for _, image := range message.Images {
			blocks = append(blocks, anthropicContentBlock{Type: "image", Source: &anthropicImageSource{Type: "base64", MediaType: image.MediaType, Data: base64.StdEncoding.EncodeToString(image.Data)}})
		}
		if content != "" {
			blocks = append(blocks, anthropicContentBlock{Type: "text", Text: content})
		}
	}
	if len(blocks) == 0 {
		return nil, errors.New("structured Anthropic message has no content blocks")
	}
	return blocks, nil
}

type anthropicStreamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		ID    string         `json:"id"`
		Model string         `json:"model"`
		Usage anthropicUsage `json:"usage"`
	} `json:"message"`
	ContentBlock json.RawMessage `json:"content_block"`
	Delta        json.RawMessage `json:"delta"`
	Usage        anthropicUsage  `json:"usage"`
	Error        struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type anthropicStreamState struct {
	model          string
	usage          anthropicUsage
	toolBlocks     map[int]*anthropicStreamToolBlock
	textBlocks     map[int]bool
	privateBlocks  map[int]bool
	toolCalls      []ToolCall
	messageStarted bool
	messageStopped bool
	finishReason   FinishReason
	hasText        bool
	pendingToolErr error
	itemCount      int
	events         providerStreamEvents
	responseModel  func(string) string
	selectedModel  string
	replayBinding  string
	replay         anthropicReplayBuilder
}

type anthropicStreamToolBlock struct {
	id         string
	name       string
	input      json.RawMessage
	partial    strings.Builder
	hasPartial bool
}

func (s *anthropicStreamState) consume(payload []byte, provider string) (*ChatChunk, bool, error) {
	var event anthropicStreamEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, false, NewProviderError(OutcomeInvalidResponse, provider, "returned malformed stream event", err)
	}
	switch event.Type {
	case "message_start":
		if s.messageStarted || s.messageStopped {
			return nil, false, NewProviderError(OutcomeInvalidResponse, provider,
				"returned a duplicate message start", nil)
		}
		if event.Message.ID != "" && !replayIdentity(event.Message.ID) {
			return nil, false, NewProviderError(OutcomeInvalidResponse, provider, "returned an invalid native response identity", nil)
		}
		s.replay.responseID = event.Message.ID
		model := strings.TrimSpace(event.Message.Model)
		if model == "" || !utf8.ValidString(model) || len([]rune(model)) > MaxItemStreamIdentity ||
			strings.ContainsRune(model, 0) {
			return nil, false, NewProviderError(OutcomeInvalidResponse, provider,
				"returned an invalid model identity during the stream", nil)
		}
		if s.responseModel != nil {
			model = s.responseModel(model)
		}
		s.model = model
		s.events.model = model
		s.messageStarted = true
		// Compatible streams may omit the initial output count before any
		// output has been generated. Preserve this existing zero default.
		if event.Message.Usage.OutputTokens == nil {
			zero := 0
			event.Message.Usage.OutputTokens = &zero
		}
		if _, err := event.Message.Usage.normalized(); err != nil {
			return nil, false, NewProviderError(OutcomeInvalidResponse, provider, "returned invalid initial stream usage", nil)
		}
		s.usage = event.Message.Usage
		return &ChatChunk{Events: []StreamEvent{s.events.start()}}, false, nil
	case "content_block_start":
		if !s.messageStarted || s.messageStopped || event.Index < 0 || event.Index >= MaxProviderOutputItems ||
			s.blockExists(event.Index) {
			return nil, false, NewProviderError(OutcomeInvalidResponse, provider,
				"returned an invalid content block start", nil)
		}
		if s.replay.startBlock(event.Index, event.ContentBlock) != nil {
			return nil, false, NewProviderError(OutcomeInvalidResponse, provider, "returned an unsupported native content block", nil)
		}
		block := s.replay.blocks[event.Index].block
		itemID := fmt.Sprintf("block/%d", event.Index)
		if block.Type == "text" && *block.Text != "" {
			s.ensureBlockMaps()
			s.textBlocks[event.Index] = true
			s.itemCount++
			s.hasText = true
			events := []StreamEvent{s.events.emit(StreamEvent{
				Type: StreamOutputItemStarted, ItemID: itemID, ItemType: StreamItemMessage,
				ItemStatus: StreamItemInProgress,
			})}
			events = append(events, s.events.emit(StreamEvent{
				Type: StreamTextDelta, ItemID: itemID, ItemType: StreamItemMessage,
				ItemStatus: StreamItemInProgress, TextDelta: *block.Text,
			}))
			return &ChatChunk{Text: *block.Text, Events: events}, false, nil
		}
		if block.Type == "text" {
			s.ensureBlockMaps()
			s.textBlocks[event.Index] = true
			s.itemCount++
			return &ChatChunk{Events: []StreamEvent{s.events.emit(StreamEvent{
				Type: StreamOutputItemStarted, ItemID: itemID, ItemType: StreamItemMessage,
				ItemStatus: StreamItemInProgress,
			})}}, false, nil
		}
		if block.Type == "tool_use" {
			if len(s.toolBlocks)+len(s.toolCalls) >= MaxProviderToolCalls {
				return nil, false, NewProviderError(OutcomeInvalidResponse, provider,
					"returned too many tool blocks", nil)
			}
			id := block.ID
			name := block.Name
			if err := validateStreamIdentity(id, "provider call"); err != nil {
				return nil, false, NewProviderError(OutcomeInvalidResponse, provider,
					"returned an invalid tool block identity", err)
			}
			if err := validateToolName(name); err != nil {
				return nil, false, NewProviderError(OutcomeInvalidResponse, provider,
					"returned an invalid tool block name", err)
			}
			s.ensureBlockMaps()
			s.toolBlocks[event.Index] = &anthropicStreamToolBlock{
				id: id, name: name,
				input: append(json.RawMessage(nil), block.Input...),
			}
			s.itemCount++
			return &ChatChunk{Events: []StreamEvent{
				s.events.emit(StreamEvent{Type: StreamOutputItemStarted, ItemID: itemID,
					ItemType: StreamItemToolCall, ItemStatus: StreamItemInProgress}),
				s.events.emit(StreamEvent{Type: StreamToolCallStarted, ItemID: itemID, CallID: id,
					ItemType: StreamItemToolCall, ItemStatus: StreamItemInProgress, ToolName: name}),
			}}, false, nil
		}
		s.ensureBlockMaps()
		s.privateBlocks[event.Index] = true
	case "content_block_delta":
		if !s.messageStarted || s.messageStopped || event.Index < 0 ||
			event.Index >= MaxProviderOutputItems {
			return nil, false, NewProviderError(OutcomeInvalidResponse, provider,
				"returned a content delta outside an active message", nil)
		}
		if s.replay.appendDelta(event.Index, event.Delta) != nil {
			return nil, false, NewProviderError(OutcomeInvalidResponse, provider, "returned an invalid native content delta", nil)
		}
		var delta anthropicReplayDelta
		if json.Unmarshal(event.Delta, &delta) != nil {
			return nil, false, NewProviderError(OutcomeInvalidResponse, provider, "returned an invalid native content delta", nil)
		}
		itemID := fmt.Sprintf("block/%d", event.Index)
		if delta.Type == "text_delta" {
			if *delta.Text == "" || !s.textBlocks[event.Index] ||
				s.toolBlocks[event.Index] != nil || s.privateBlocks[event.Index] {
				return nil, false, NewProviderError(OutcomeInvalidResponse, provider,
					"returned text for an unknown content block", nil)
			}
			events := []StreamEvent{s.events.emit(StreamEvent{
				Type: StreamTextDelta, ItemID: itemID, ItemType: StreamItemMessage,
				ItemStatus: StreamItemInProgress, TextDelta: *delta.Text,
			})}
			s.hasText = true
			return &ChatChunk{Text: *delta.Text, Events: events}, false, nil
		}
		if delta.Type == "input_json_delta" {
			block, exists := s.toolBlocks[event.Index]
			if !exists {
				return nil, false, NewProviderError(OutcomeInvalidResponse, provider, "returned tool JSON for an unknown block", nil)
			}
			if block.partial.Len()+len(*delta.PartialJSON) > MaxProviderToolPayloadSize {
				return nil, false, NewProviderError(OutcomeInvalidResponse, provider, "streamed tool arguments exceed the size limit", nil)
			}
			block.hasPartial = true
			_, _ = block.partial.WriteString(*delta.PartialJSON)
			if *delta.PartialJSON != "" {
				return &ChatChunk{Events: []StreamEvent{s.events.emit(StreamEvent{
					Type: StreamToolArgumentDelta, ItemID: itemID, CallID: block.id,
					ItemType: StreamItemToolCall, ItemStatus: StreamItemInProgress,
					ToolName: block.name, ArgumentDelta: *delta.PartialJSON,
				})}}, false, nil
			}
			return nil, false, nil
		}
		if delta.Type == "citations_delta" && s.textBlocks[event.Index] {
			return nil, false, nil
		}
		if !s.privateBlocks[event.Index] {
			return nil, false, NewProviderError(OutcomeInvalidResponse, provider,
				"returned an unsupported content block delta", nil)
		}
	case "content_block_stop":
		if !s.messageStarted || s.messageStopped || event.Index < 0 ||
			event.Index >= MaxProviderOutputItems {
			return nil, false, NewProviderError(OutcomeInvalidResponse, provider,
				"returned a content stop outside an active message", nil)
		}
		if err := s.replay.endBlock(event.Index); err != nil {
			if s.toolBlocks[event.Index] != nil {
				s.pendingToolErr = NewProviderError(OutcomeInvalidResponse, provider, "returned invalid native tool input", nil)
				delete(s.toolBlocks, event.Index)
				return nil, false, nil
			}
			return nil, false, NewProviderError(OutcomeInvalidResponse, provider, "returned an invalid native content stop", nil)
		}
		itemID := fmt.Sprintf("block/%d", event.Index)
		if s.textBlocks[event.Index] {
			delete(s.textBlocks, event.Index)
			return &ChatChunk{Events: []StreamEvent{s.events.emit(StreamEvent{
				Type: StreamOutputItemCompleted, ItemID: itemID, ItemType: StreamItemMessage,
				ItemStatus: StreamItemCompleted,
			})}}, false, nil
		}
		if block, exists := s.toolBlocks[event.Index]; exists {
			arguments := append(json.RawMessage(nil), block.input...)
			if block.hasPartial {
				arguments = json.RawMessage(block.partial.String())
			}
			if len(bytes.TrimSpace(arguments)) == 0 {
				arguments = json.RawMessage(`{}`)
			}
			call, err := NormalizeToolCall(ToolCall{ID: block.id, Name: block.name, Arguments: arguments})
			if err != nil {
				toolErr := NewProviderError(OutcomeInvalidResponse, provider,
					"returned an invalid streamed tool call", err)
				s.pendingToolErr = toolErr
				delete(s.toolBlocks, event.Index)
				return nil, false, nil
			}
			s.toolCalls = append(s.toolCalls, call)
			delete(s.toolBlocks, event.Index)
			callCopy := call
			return &ChatChunk{Events: []StreamEvent{
				s.events.emit(StreamEvent{Type: StreamToolCallCompleted, ItemID: itemID,
					CallID: block.id, ItemType: StreamItemToolCall,
					ItemStatus: StreamItemReadyForValidation, ToolName: block.name,
					CompletedCall: &callCopy}),
				s.events.emit(StreamEvent{Type: StreamOutputItemCompleted, ItemID: itemID,
					CallID: block.id, ItemType: StreamItemToolCall,
					ItemStatus: StreamItemCompleted, ToolName: block.name}),
			}}, false, nil
		}
		if s.privateBlocks[event.Index] {
			delete(s.privateBlocks, event.Index)
			return nil, false, nil
		}
		return nil, false, NewProviderError(OutcomeInvalidResponse, provider,
			"returned a stop for an unknown content block", nil)
	case "message_delta":
		if !s.messageStarted || s.messageStopped {
			return nil, false, NewProviderError(OutcomeInvalidResponse, provider,
				"returned usage outside an active message", nil)
		}
		var delta struct {
			StopReason string `json:"stop_reason"`
		}
		if len(event.Delta) > 0 && json.Unmarshal(event.Delta, &delta) != nil {
			return nil, false, NewProviderError(OutcomeInvalidResponse, provider, "returned an invalid message delta", nil)
		}
		merged, err := s.usage.merge(event.Usage)
		if err != nil {
			return nil, false, NewProviderError(OutcomeInvalidResponse, provider, "returned invalid cumulative stream usage", nil)
		}
		s.usage = merged
		if delta.StopReason != "" {
			reason := anthropicFinishReason(delta.StopReason)
			if s.finishReason != "" && s.finishReason != reason {
				return nil, false, NewProviderError(OutcomeInvalidResponse, provider,
					"changed stop reason during the stream", nil)
			}
			s.finishReason = reason
		}
	case "message_stop":
		if !s.messageStarted || s.messageStopped || s.itemCount == 0 {
			return nil, false, NewProviderError(OutcomeInvalidResponse, provider, "message stopped outside an active response", nil)
		}
		s.messageStopped = true
		chunk := s.finalChunk()
		if err := chunk.Usage.Validate(); err != nil {
			return nil, false, NewProviderError(OutcomeInvalidResponse, provider, "returned invalid stream usage", err)
		}
		chunk.FinishReason = s.finishReason
		if completionErr := CompletionError(provider, s.finishReason); completionErr != nil {
			chunk.Done = false
			chunk.ToolCalls = nil
			chunk.Err = completionErr
			chunk.Events = []StreamEvent{s.events.terminalEvent(OutcomePermanent, chunk.Usage)}
			return &chunk, true, nil
		}
		if len(s.toolBlocks) != 0 || len(s.textBlocks) != 0 || len(s.privateBlocks) != 0 {
			return nil, false, NewProviderError(OutcomeInvalidResponse, provider,
				"message stopped with unfinished content blocks", nil)
		}
		if s.pendingToolErr != nil {
			return nil, false, s.pendingToolErr
		}
		calls, err := NormalizeToolCalls(s.toolCalls)
		if err != nil {
			return nil, false, NewProviderError(OutcomeInvalidResponse, provider, "returned invalid streamed tool calls", err)
		}
		if err := validateAnthropicFinishReason(s.finishReason, s.hasText, len(calls)); err != nil {
			return nil, false, NewProviderError(OutcomeInvalidResponse, provider,
				"returned an incompatible stop reason", err)
		}
		chunk.ToolCalls = calls
		if len(calls) > 0 && s.replay.hasPrivate() {
			if s.model != s.selectedModel {
				return nil, false, NewProviderError(OutcomeInvalidResponse, provider, "native replay model does not match the requested source", nil)
			}
			chunk.Replay, err = s.replay.finish(provider, s.selectedModel, s.replayBinding, calls)
			if err != nil {
				return nil, false, NewProviderError(OutcomeInvalidResponse, provider, "returned invalid native replay state", nil)
			}
		}
		chunk.Events = []StreamEvent{s.events.terminalEvent(OutcomeSuccess, chunk.Usage)}
		return &chunk, true, nil
	case "error":
		kind := OutcomePermanent
		switch event.Error.Type {
		case "rate_limit_error":
			kind = OutcomeRateLimited
		case "overloaded_error", "api_error":
			kind = OutcomeRetryable
		}
		// Error fields can echo prompt text or credentials. Only the known type
		// controls classification; no upstream error text leaves this adapter.
		return nil, false, NewProviderError(kind, provider, "returned a streaming error", nil)
	}
	return nil, false, nil
}

func (s *anthropicStreamState) ensureBlockMaps() {
	if s.toolBlocks == nil {
		s.toolBlocks = make(map[int]*anthropicStreamToolBlock)
	}
	if s.textBlocks == nil {
		s.textBlocks = make(map[int]bool)
	}
	if s.privateBlocks == nil {
		s.privateBlocks = make(map[int]bool)
	}
}

func (s *anthropicStreamState) blockExists(index int) bool {
	return s.toolBlocks[index] != nil || s.textBlocks[index] || s.privateBlocks[index]
}

func (s anthropicStreamState) finalChunk() ChatChunk {
	usage, err := s.usage.normalized()
	if err != nil {
		usage = Usage{TotalTokens: -1}
	}
	return ChatChunk{Done: true, ToolCalls: append([]ToolCall(nil), s.toolCalls...), Usage: &usage, Model: s.model}
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type anthropicMessageResponse struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Role       string            `json:"role"`
	Model      string            `json:"model"`
	StopReason string            `json:"stop_reason"`
	Content    []json.RawMessage `json:"content"`
	Usage      anthropicUsage    `json:"usage"`
}

func (r anthropicMessageResponse) ModelOrDefault(model string) string {
	if strings.TrimSpace(r.Model) == "" {
		return model
	}
	return r.Model
}

func fallbackModelInfo(provider string, model string) []ModelInfo {
	if model == "" {
		model = "claude-3-5-sonnet-latest"
	}
	return []ModelInfo{{
		ID:           model,
		DisplayName:  model,
		Provider:     provider,
		Capabilities: []string{"chat", "tools"},
	}}
}
