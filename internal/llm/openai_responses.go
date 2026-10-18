package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

// OpenAIResponsesConfig configures an OpenAI Responses compatible endpoint.
// It intentionally installs no hosted web-search tool; native search is a
// separately qualified capability with its own execution authority.
type OpenAIResponsesConfig struct {
	Name         string
	BaseURL      string
	APIKey       string
	DefaultModel string
	HTTPClient   *http.Client
	Runtime      HTTPProviderRuntime
}

type OpenAIResponsesProvider struct {
	name          string
	baseURL       string
	apiKey        string
	defaultModel  string
	client        *http.Client
	runtime       HTTPProviderRuntime
	toolNamespace string
}

func NewOpenAIResponsesProvider(config OpenAIResponsesConfig) (*OpenAIResponsesProvider, error) {
	name := strings.TrimSpace(config.Name)
	if name == "" {
		name = "openai_responses"
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
	if _, err := normalizeOpenAIModel(model); err != nil {
		return nil, fmt.Errorf("default model for provider %s is invalid", name)
	}
	client, err := providerHTTPClient(config.HTTPClient)
	if err != nil {
		return nil, err
	}
	return &OpenAIResponsesProvider{
		name: name, baseURL: baseURL, apiKey: config.APIKey,
		defaultModel: model, client: client,
		runtime: config.Runtime,
	}, nil
}

func (p *OpenAIResponsesProvider) Name() string { return p.name }

func (p *OpenAIResponsesProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	return listProviderCatalog(ctx, ModelCatalogRequest{
		Provider: p.name, EndpointURL: p.baseURL, Transport: HarnessTransportOpenAIResponses,
		APIKey: p.apiKey, Runtime: p.runtime, HTTPClient: p.client,
	})
}

func (p *OpenAIResponsesProvider) Chat(ctx context.Context,
	request ChatRequest,
) (*ChatResponse, error) {
	selectedModel, wire, err := p.prepareRequest(request, false)
	if err != nil {
		return nil, openAILocalError(p.name, "could not prepare Responses request")
	}
	secret, err := providerRequestCredential(ctx, p.name, p.apiKey, p.runtime, p.baseURL)
	if err != nil {
		return nil, openAILocalError(p.name, "provider credential is unavailable")
	}
	payload, err := providerRequestPayload(p.runtime, secret, wire)
	if err != nil {
		return nil, openAILocalError(p.name, "could not encode Responses request")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.responsesEndpoint(), bytes.NewReader(payload))
	if err != nil {
		return nil, openAILocalError(p.name, "could not create Responses request")
	}
	if err := applyOpenAIRequestHeaders(httpRequest, false, secret, p.runtime); err != nil {
		return nil, openAILocalError(p.name, "could not prepare Responses request headers")
	}
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return nil, openAITransportError(ctx, p.name)
	}
	defer response.Body.Close()
	raw, err := readOpenAIBody(response.Body)
	if err != nil {
		return nil, openAIReadError(ctx, p.name, "could not read Responses response", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, openAIHTTPError(p.name, response.StatusCode,
			response.Header.Get("Retry-After"), raw)
	}
	if !utf8.Valid(raw) {
		return nil, openAIProtocolError(p.name, "returned non-UTF-8 Responses JSON")
	}
	var parsed openAIResponsesResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, openAIProtocolError(p.name, "returned malformed Responses JSON")
	}
	return p.normalizeResponse(selectedModel, parsed)
}

func (p *OpenAIResponsesProvider) StreamChat(ctx context.Context,
	request ChatRequest,
) (<-chan ChatChunk, error) {
	selectedModel, wire, err := p.prepareRequest(request, true)
	if err != nil {
		return nil, openAILocalError(p.name, "could not prepare streaming Responses request")
	}
	secret, err := providerRequestCredential(ctx, p.name, p.apiKey, p.runtime, p.baseURL)
	if err != nil {
		return nil, openAILocalError(p.name, "provider credential is unavailable")
	}
	payload, err := providerRequestPayload(p.runtime, secret, wire)
	if err != nil {
		return nil, openAILocalError(p.name, "could not encode streaming Responses request")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.responsesEndpoint(), bytes.NewReader(payload))
	if err != nil {
		return nil, openAILocalError(p.name, "could not create streaming Responses request")
	}
	if err := applyOpenAIRequestHeaders(httpRequest, true, secret, p.runtime); err != nil {
		return nil, openAILocalError(p.name, "could not prepare streaming Responses headers")
	}
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return nil, openAITransportError(ctx, p.name)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		defer response.Body.Close()
		raw, readErr := readOpenAIBody(response.Body)
		if readErr != nil {
			return nil, openAIReadError(ctx, p.name, "could not read Responses error", readErr)
		}
		return nil, openAIHTTPError(p.name, response.StatusCode,
			response.Header.Get("Retry-After"), raw)
	}
	chunks := make(chan ChatChunk, 8)
	go p.readStream(ctx, response.Body, selectedModel, chunks)
	return chunks, nil
}

func (p *OpenAIResponsesProvider) SupportsTools(model string) bool {
	_, err := normalizeOpenAIModel(strings.TrimSpace(model))
	return err == nil
}

func (p *OpenAIResponsesProvider) SupportsVision(model string) bool {
	return p.DescribeVision(model).State == VisionSupported
}
func (p *OpenAIResponsesProvider) DescribeVision(model string) VisionCapability {
	return runtimeVision(p.runtime, model)
}

func (p *OpenAIResponsesProvider) SupportsJSONMode(model string) bool {
	return p.SupportsTools(model)
}

func (p *OpenAIResponsesProvider) DescribeModelHarness(model string) ModelHarness {
	model = strings.TrimSpace(model)
	if model == "" {
		model = p.defaultModel
	}
	return ModelHarness{
		ProtocolVersion:   ModelHarnessProtocolVersion,
		TransportProtocol: HarnessTransportOpenAIResponses,
		ToolStrategy:      HarnessToolStrategyNative, JSONStrategy: HarnessJSONStrategyNative,
		QualificationStatus: HarnessQualificationRequired,
		BindingDigest: providerHarnessBinding(p.runtime, p.name, p.baseURL, model,
			HarnessTransportOpenAIResponses, HarnessToolStrategyNative,
			HarnessJSONStrategyNative),
	}
}

func (p *OpenAIResponsesProvider) prepareRequest(request ChatRequest,
	stream bool,
) (string, openAIResponsesRequest, error) {
	selectedModel := strings.TrimSpace(request.Model)
	if selectedModel == "" {
		selectedModel = p.defaultModel
	}
	var err error
	selectedModel, err = normalizeOpenAIModel(selectedModel)
	if err != nil {
		return "", openAIResponsesRequest{}, err
	}
	wireModel, err := providerRequestModel(p.runtime, selectedModel)
	if err != nil {
		return "", openAIResponsesRequest{}, err
	}
	wireModel, err = normalizeOpenAIModel(wireModel)
	if err != nil {
		return "", openAIResponsesRequest{}, err
	}
	if math.IsNaN(request.Temperature) || math.IsInf(request.Temperature, 0) ||
		request.Temperature < 0 || request.Temperature > 2 {
		return "", openAIResponsesRequest{}, errors.New("temperature is outside the supported range")
	}
	maxTokens := request.MaxTokens
	if maxTokens < 0 || maxTokens > 1_000_000 {
		return "", openAIResponsesRequest{}, errors.New("max output tokens exceeds the provider request limit")
	}
	wire := openAIResponsesRequest{Model: wireModel, MaxOutputTokens: maxTokens,
		Store: false, Stream: stream}
	if err := validateRequestImages(p, selectedModel, request.Messages); err != nil {
		return "", openAIResponsesRequest{}, err
	}
	if request.Temperature > 0 {
		wire.Temperature = &request.Temperature
	}
	binding := p.DescribeModelHarness(selectedModel).BindingDigest
	var replayCallIDs map[string]string
	for index, message := range request.Messages {
		var items []any
		if strings.EqualFold(strings.TrimSpace(message.Role), "assistant") && message.Replay != nil &&
			message.Replay.matchesSource(p.name, selectedModel, HarnessTransportOpenAIResponses, binding) {
			items, replayCallIDs, err = openAIResponsesReplayInput(message, message.Replay)
		} else {
			resultIDs := map[string]string(nil)
			if strings.EqualFold(strings.TrimSpace(message.Role), "user") && len(message.ToolResults) > 0 {
				resultIDs = replayCallIDs
			}
			items, err = openAIResponsesInput(message, resultIDs)
			replayCallIDs = nil
		}
		if err != nil {
			return "", openAIResponsesRequest{}, fmt.Errorf("invalid message at index %d", index)
		}
		wire.Input = append(wire.Input, items...)
	}
	if len(wire.Input) == 0 {
		wire.Input = append(wire.Input, map[string]any{"role": "user", "content": "Hello"})
	}
	if len(request.Tools) > MaxProviderToolSpecs {
		return "", openAIResponsesRequest{}, errors.New("tool specification count exceeds the provider limit")
	}
	for index, spec := range request.Tools {
		name := strings.TrimSpace(spec.Name)
		parameters := append(json.RawMessage(nil), bytes.TrimSpace(spec.Parameters)...)
		if err := validateOpenAIToolName(name); err != nil || len(parameters) == 0 ||
			len(parameters) > MaxProviderToolPayloadSize || !utf8.Valid(parameters) ||
			!json.Valid(parameters) {
			return "", openAIResponsesRequest{}, fmt.Errorf("invalid tool specification at index %d", index)
		}
		wire.Tools = append(wire.Tools, openAIResponsesTool{Type: "function", Name: name,
			Description: strings.TrimSpace(spec.Description), Parameters: parameters})
	}
	if request.JSONMode && !(len(wire.Tools) > 0 && p.usesDeepSeekNativeToolFormat()) {
		wire.Text = &openAIResponsesText{Format: openAIResponsesTextFormat{Type: "json_object"}}
	}
	if p.toolNamespace != "" {
		wire.MaxOutputTokens = 0
		wire.Temperature = nil
		wire.Stream = true
		wire.Include = []string{"reasoning.encrypted_content"}
		input := make([]any, 0, len(wire.Input))
		for _, raw := range wire.Input {
			item, ok := raw.(map[string]any)
			if !ok {
				return "", openAIResponsesRequest{}, errors.New("ChatGPT input item is invalid")
			}
			if item["role"] == "system" {
				if wire.Instructions != "" {
					wire.Instructions += "\n\n"
				}
				wire.Instructions += item["content"].(string)
				continue
			}
			if item["type"] == "function_call" {
				item["namespace"] = p.toolNamespace
			}
			input = append(input, item)
		}
		wire.Input = input
		if len(wire.Input) == 0 {
			wire.Input = []any{map[string]any{"role": "user", "content": "Hello"}}
		}
		if len(wire.Tools) != 0 {
			wire.Tools = []openAIResponsesTool{{Type: "namespace", Name: p.toolNamespace,
				Tools: wire.Tools}}
		}
	}
	return selectedModel, wire, nil
}

func (p *OpenAIResponsesProvider) usesDeepSeekNativeToolFormat() bool {
	endpoint, err := url.Parse(p.baseURL)
	// Observed on the official Responses endpoint: forcing json_object while
	// offering functions can turn native calls into ordinary DSML text. Keep
	// native functions available without that text-format constraint. Tool-free
	// JSON requests and the caller's strict root-response validation remain.
	// Provider/model names and third-party proxy endpoints do not opt into this.
	return err == nil && endpoint.Scheme == "https" &&
		strings.EqualFold(strings.TrimSuffix(endpoint.Hostname(), "."), "api.deepseek.com") &&
		(endpoint.Port() == "" || endpoint.Port() == "443")
}

func openAIResponsesInput(message Message, replayCallIDs map[string]string) ([]any, error) {
	if err := ValidateMessageImages(message); err != nil {
		return nil, err
	}
	role := strings.ToLower(strings.TrimSpace(message.Role))
	content := strings.TrimSpace(message.Content)
	if content == "" && len(message.ToolCalls) == 0 && len(message.ToolResults) == 0 && len(message.Images) == 0 {
		return nil, nil
	}
	switch role {
	case "system", "developer":
		if content == "" || len(message.ToolCalls) != 0 || len(message.ToolResults) != 0 {
			return nil, errors.New("system message has invalid structured content")
		}
		return []any{map[string]any{"role": role, "content": content}}, nil
	case "assistant":
		if len(message.ToolResults) != 0 {
			return nil, errors.New("assistant message cannot contain tool results")
		}
		calls, err := NormalizeToolCalls(message.ToolCalls)
		if err != nil {
			return nil, err
		}
		items := make([]any, 0, len(calls)+1)
		if content != "" {
			items = append(items, map[string]any{"role": "assistant", "content": content})
		}
		for _, call := range calls {
			items = append(items, map[string]any{"type": "function_call",
				"call_id": call.ID, "name": call.Name, "arguments": string(call.Arguments)})
		}
		return items, nil
	case "user":
		if len(message.ToolCalls) != 0 {
			return nil, errors.New("user message cannot contain tool calls")
		}
		items := make([]any, 0, len(message.ToolResults)+1)
		for _, result := range message.ToolResults {
			normalized, err := NormalizeToolResult(result)
			if err != nil {
				return nil, err
			}
			callID := normalized.ToolCallID
			if wireID := replayCallIDs[callID]; wireID != "" {
				callID = wireID
			}
			items = append(items, map[string]any{"type": "function_call_output",
				"call_id": callID, "output": normalized.Content})
		}
		if len(message.Images) > 0 {
			parts := make([]any, 0, len(message.Images)+1)
			if content != "" {
				parts = append(parts, map[string]any{"type": "input_text", "text": content})
			}
			for _, image := range message.Images {
				parts = append(parts, map[string]any{"type": "input_image", "image_url": imageDataURL(image), "detail": "auto"})
			}
			items = append(items, map[string]any{"role": "user", "content": parts})
		} else if content != "" {
			items = append(items, map[string]any{"role": "user", "content": content})
		}
		if len(items) == 0 {
			return nil, errors.New("user message has no content")
		}
		return items, nil
	default:
		return nil, errors.New("message role is unsupported")
	}
}

func openAIResponsesReplayInput(message Message, replay *ProviderReplay) ([]any, map[string]string, error) {
	if replay == nil || message.Content != replay.AssistantText() {
		return nil, nil, errors.New("Responses replay text changed")
	}
	calls, err := NormalizeToolCalls(message.ToolCalls)
	if err != nil || replay.ValidateToolCalls(calls) != nil {
		return nil, nil, errors.New("Responses replay tool batch changed")
	}
	items := make([]any, 0, len(replay.parts))
	callIDs := make(map[string]string, len(replay.calls))
	for _, part := range replay.parts {
		switch part.Kind {
		case "reasoning", "compaction":
			var item map[string]any
			if json.Unmarshal(part.Opaque, &item) != nil {
				return nil, nil, errors.New("Responses replay private item is invalid")
			}
			items = append(items, item)
		case "text":
			item := map[string]any{"id": part.ID, "type": "message", "status": "completed",
				"role": "assistant", "content": []map[string]any{{"type": "output_text", "text": part.Text}}}
			if part.Phase != "" {
				item["phase"] = part.Phase
			}
			items = append(items, item)
		case "tool":
			if part.CallIndex < 0 || part.CallIndex >= len(calls) || part.CallIndex >= len(replay.calls) {
				return nil, nil, errors.New("Responses replay tool position is invalid")
			}
			call, bound := calls[part.CallIndex], replay.calls[part.CallIndex]
			items = append(items, map[string]any{"id": part.ID, "type": "function_call",
				"status": "completed", "call_id": bound.WireID, "name": call.Name,
				"arguments": string(call.Arguments)})
			callIDs[bound.DurableID] = bound.WireID
		default:
			return nil, nil, errors.New("Responses replay part is unsupported")
		}
	}
	return items, callIDs, nil
}

func (p *OpenAIResponsesProvider) normalizeResponse(selectedModel string,
	response openAIResponsesResponse,
) (*ChatResponse, error) {
	if response.Object != "response" || validateStreamIdentity(response.ID, "Responses response") != nil {
		return nil, openAIProtocolError(p.name, "returned an invalid Responses envelope")
	}
	if _, err := normalizeOpenAIModel(response.Model); err != nil {
		return nil, openAIProtocolError(p.name, "returned an invalid Responses model")
	}
	if response.Error != nil {
		return nil, openAIWireError(p.name, *response.Error)
	}
	usage, err := normalizeResponsesUsage(response.Usage)
	if err != nil {
		return nil, openAIProtocolError(p.name, "returned invalid Responses usage")
	}
	if response.Status == "incomplete" {
		finishReason := responsesIncompleteFinishReason(response.IncompleteDetails)
		result := &ChatResponse{ResponseID: response.ID, Usage: usage, Model: selectedModel,
			Provider: p.name, FinishReason: finishReason}
		if completionErr := CompletionError(p.name, finishReason); completionErr != nil {
			return result, completionErr
		}
		return nil, openAIProtocolError(p.name, "returned an incomplete response without a supported reason")
	}
	if response.Status != "completed" {
		return nil, openAIProtocolError(p.name, "returned an invalid Responses status")
	}
	if len(response.Output) == 0 || len(response.Output) > MaxProviderOutputItems {
		return nil, openAIProtocolError(p.name, "returned an invalid Responses output list")
	}
	var text strings.Builder
	calls := make([]ToolCall, 0)
	refused := false
	itemIDs := make(map[string]struct{}, len(response.Output))
	for _, item := range response.Output {
		if validateStreamIdentity(item.ID, "Responses output item") != nil {
			return nil, openAIProtocolError(p.name, "returned an invalid Responses output item identity")
		}
		if _, duplicate := itemIDs[item.ID]; duplicate {
			return nil, openAIProtocolError(p.name, "reused a Responses output item identity")
		}
		itemIDs[item.ID] = struct{}{}
		switch item.Type {
		case "message":
			if item.Status != "completed" || (item.Role != "" && item.Role != "assistant") {
				return nil, openAIProtocolError(p.name, "returned an invalid Responses message")
			}
			for _, part := range item.Content {
				switch part.Type {
				case "output_text":
					if !utf8.ValidString(part.Text) || text.Len()+len(part.Text) > MaxModelOutputBytes {
						return nil, openAIProtocolError(p.name, "returned invalid Responses text")
					}
					_, _ = text.WriteString(part.Text)
				case "refusal":
					if part.Refusal == "" || !utf8.ValidString(part.Refusal) ||
						len(part.Refusal) > MaxModelOutputBytes {
						return nil, openAIProtocolError(p.name, "returned invalid Responses refusal")
					}
					refused = true
				default:
					return nil, openAIProtocolError(p.name, "returned unsupported Responses message content")
				}
			}
		case "function_call":
			if item.Status != "completed" {
				return nil, openAIProtocolError(p.name, "returned an incomplete Responses function call")
			}
			arguments := item.Arguments
			if strings.TrimSpace(arguments) == "" {
				arguments = `{}`
			}
			calls = append(calls, ToolCall{ID: item.CallID, Name: item.Name,
				Arguments: json.RawMessage(arguments)})
		case "reasoning", "compaction":
			// Private reasoning is deliberately not projected into the Harness.
		default:
			return nil, openAIProtocolError(p.name, "returned an unsupported Responses output item")
		}
	}
	normalizedCalls, err := NormalizeToolCalls(calls)
	if err != nil {
		return nil, openAIProtocolError(p.name, "returned invalid Responses function calls")
	}
	if refused {
		if text.Len() != 0 || len(normalizedCalls) != 0 {
			return nil, openAIProtocolError(p.name, "mixed a Responses refusal with executable output")
		}
		result := &ChatResponse{ResponseID: response.ID, Usage: usage, Model: selectedModel,
			Provider: p.name, FinishReason: FinishReasonRefusal}
		return result, CompletionError(p.name, FinishReasonRefusal)
	}
	if text.Len() == 0 && len(normalizedCalls) == 0 {
		return nil, openAIProtocolError(p.name, "returned no usable Responses output")
	}
	finishReason := FinishReasonStop
	if len(normalizedCalls) > 0 {
		finishReason = FinishReasonToolCalls
	}
	var replay *ProviderReplay
	if len(normalizedCalls) > 0 {
		replay, err = p.responsesReplay(selectedModel, response.Output, normalizedCalls)
		if err != nil {
			return nil, openAIProtocolError(p.name, "returned invalid replayable Responses output")
		}
	}
	return &ChatResponse{ResponseID: response.ID, Text: text.String(), ToolCalls: normalizedCalls,
		Usage: usage, Model: selectedModel, Provider: p.name, FinishReason: finishReason,
		Replay: replay}, nil
}

func responsesIncompleteFinishReason(details *openAIResponsesIncompleteDetails) FinishReason {
	if details == nil {
		return FinishReasonUnknown
	}
	switch strings.ToLower(strings.TrimSpace(details.Reason)) {
	case "max_output_tokens", "max_tokens", "length":
		return FinishReasonLength
	case "content_filter", "refusal":
		return FinishReasonRefusal
	case "context_length_exceeded", "model_context_window_exceeded":
		return FinishReasonContextLimit
	case "pause", "pause_turn":
		return FinishReasonPause
	default:
		return FinishReasonUnknown
	}
}

func (p *OpenAIResponsesProvider) responsesReplay(selectedModel string,
	output []openAIResponsesOutputItem, calls []ToolCall,
) (*ProviderReplay, error) {
	parts, err := responsesReplayParts(output)
	if err != nil {
		return nil, err
	}
	return newProviderReplay(p.name, selectedModel, HarnessTransportOpenAIResponses,
		p.DescribeModelHarness(selectedModel).BindingDigest, parts, calls)
}

func responsesReplayParts(output []openAIResponsesOutputItem) ([]providerReplayPart, error) {
	parts := make([]providerReplayPart, 0, len(output))
	callIndex := 0
	for _, item := range output {
		switch item.Type {
		case "reasoning", "compaction":
			opaque := map[string]any{"id": item.ID, "type": item.Type}
			if item.Status != "" {
				opaque["status"] = item.Status
			}
			if item.EncryptedContent != "" {
				opaque["encrypted_content"] = item.EncryptedContent
			}
			if item.Type == "reasoning" && len(item.Summary) > 0 {
				var summary any
				if json.Unmarshal(item.Summary, &summary) != nil {
					return nil, errors.New("Responses reasoning summary is invalid")
				}
				opaque["summary"] = summary
			}
			raw, err := json.Marshal(opaque)
			if err != nil {
				return nil, err
			}
			parts = append(parts, providerReplayPart{Kind: item.Type, ID: item.ID, Opaque: raw})
		case "message":
			var text strings.Builder
			for _, content := range item.Content {
				if content.Type != "output_text" {
					return nil, errors.New("Responses replay text content is invalid")
				}
				text.WriteString(content.Text)
			}
			parts = append(parts, providerReplayPart{Kind: "text", ID: item.ID,
				Phase: item.Phase, Text: text.String()})
		case "function_call":
			parts = append(parts, providerReplayPart{Kind: "tool", ID: item.ID, CallIndex: callIndex})
			callIndex++
		default:
			return nil, errors.New("Responses replay output item is unsupported")
		}
	}
	return parts, nil
}

func normalizeResponsesUsage(wire *openAIResponsesUsage) (Usage, error) {
	if wire == nil || wire.InputTokens == nil || wire.OutputTokens == nil ||
		wire.TotalTokens == nil {
		return Usage{}, errors.New("Responses token count is missing")
	}
	usage := Usage{InputTokens: *wire.InputTokens, OutputTokens: *wire.OutputTokens}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || *wire.TotalTokens < 0 {
		return Usage{}, errors.New("Responses token count is negative")
	}
	maxInt := int(^uint(0) >> 1)
	if usage.InputTokens > maxInt-usage.OutputTokens {
		return Usage{}, errors.New("Responses token count overflow")
	}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	if usage.TotalTokens != *wire.TotalTokens {
		return Usage{}, errors.New("Responses token count is inconsistent")
	}
	return applyOpenAICacheUsage(usage, wire.InputTokensDetails)
}

func (p *OpenAIResponsesProvider) responsesEndpoint() string {
	if strings.HasSuffix(p.baseURL, "/v1/responses") || strings.HasSuffix(p.baseURL, "/responses") {
		return p.baseURL
	}
	if strings.HasSuffix(p.baseURL, "/v1") {
		return p.baseURL + "/responses"
	}
	return p.baseURL + "/v1/responses"
}

type openAIResponsesRequest struct {
	Model           string                `json:"model"`
	Instructions    string                `json:"instructions,omitempty"`
	Include         []string              `json:"include,omitempty"`
	Input           []any                 `json:"input"`
	Tools           []openAIResponsesTool `json:"tools,omitempty"`
	Text            *openAIResponsesText  `json:"text,omitempty"`
	Temperature     *float64              `json:"temperature,omitempty"`
	MaxOutputTokens int                   `json:"max_output_tokens,omitempty"`
	Store           bool                  `json:"store"`
	Stream          bool                  `json:"stream"`
}

type openAIResponsesTool struct {
	Type        string                `json:"type"`
	Name        string                `json:"name"`
	Description string                `json:"description,omitempty"`
	Parameters  json.RawMessage       `json:"parameters,omitempty"`
	Tools       []openAIResponsesTool `json:"tools,omitempty"`
}

type openAIResponsesText struct {
	Format openAIResponsesTextFormat `json:"format"`
}

type openAIResponsesTextFormat struct {
	Type string `json:"type"`
}

type openAIResponsesUsage struct {
	InputTokens        *int                     `json:"input_tokens"`
	OutputTokens       *int                     `json:"output_tokens"`
	TotalTokens        *int                     `json:"total_tokens"`
	InputTokensDetails *openAIInputTokenDetails `json:"input_tokens_details"`
}

type openAIResponsesContent struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

type openAIResponsesOutputItem struct {
	ID               string                   `json:"id"`
	Type             string                   `json:"type"`
	Status           string                   `json:"status"`
	Role             string                   `json:"role"`
	Phase            string                   `json:"phase,omitempty"`
	Content          []openAIResponsesContent `json:"content"`
	CallID           string                   `json:"call_id"`
	Name             string                   `json:"name"`
	Namespace        string                   `json:"namespace,omitempty"`
	Arguments        string                   `json:"arguments"`
	EncryptedContent string                   `json:"encrypted_content,omitempty"`
	Summary          json.RawMessage          `json:"summary,omitempty"`
	statusRaw        json.RawMessage
}

func (item *openAIResponsesOutputItem) UnmarshalJSON(raw []byte) error {
	type wireItem openAIResponsesOutputItem
	var wire struct {
		wireItem
		Status json.RawMessage `json:"status"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	if len(wire.Status) != 0 {
		if err := json.Unmarshal(wire.Status, &wire.wireItem.Status); err != nil {
			return err
		}
	}
	*item = openAIResponsesOutputItem(wire.wireItem)
	item.statusRaw = append(json.RawMessage(nil), wire.Status...)
	return nil
}

// Reasoning status is optional (and nullable) in the official item schema.
// Keep wire data unchanged and distinguish omission from an explicit empty string.
func (item openAIResponsesOutputItem) reasoningStatusOmitted() bool {
	return item.Type == "reasoning" && item.Status == "" &&
		(len(item.statusRaw) == 0 || bytes.Equal(bytes.TrimSpace(item.statusRaw), []byte("null")))
}

func (item openAIResponsesOutputItem) completedStatus() bool {
	return item.Status == "completed" || item.reasoningStatusOmitted()
}

// A done reasoning item and its terminal snapshot can differ only in whether
// the optional completed status is present. Compare copies, preserving replay data.
func responsesCompletedOutputEqual(done, terminal []openAIResponsesOutputItem) bool {
	if len(done) != len(terminal) {
		return false
	}
	for index, item := range done {
		snapshot := terminal[index]
		if item.Type == "reasoning" && snapshot.Type == "reasoning" {
			if !item.completedStatus() || !snapshot.completedStatus() {
				return false
			}
			item.Status, snapshot.Status = "", ""
		}
		left, leftErr := json.Marshal(item)
		right, rightErr := json.Marshal(snapshot)
		if leftErr != nil || rightErr != nil || !bytes.Equal(left, right) {
			return false
		}
	}
	return true
}

type openAIResponsesIncompleteDetails struct {
	Reason string `json:"reason"`
}

type openAIResponsesResponse struct {
	ID                string                            `json:"id"`
	Object            string                            `json:"object"`
	Status            string                            `json:"status"`
	Model             string                            `json:"model"`
	Output            []openAIResponsesOutputItem       `json:"output"`
	Usage             *openAIResponsesUsage             `json:"usage"`
	Error             *openAIError                      `json:"error"`
	IncompleteDetails *openAIResponsesIncompleteDetails `json:"incomplete_details"`
}

type openAIResponsesStreamEvent struct {
	Type      string                    `json:"type"`
	Response  openAIResponsesResponse   `json:"response"`
	Item      openAIResponsesOutputItem `json:"item"`
	ItemID    string                    `json:"item_id"`
	Delta     string                    `json:"delta"`
	Refusal   string                    `json:"refusal"`
	Arguments string                    `json:"arguments"`
	Name      string                    `json:"name"`
	Error     json.RawMessage           `json:"error"`
	Code      json.RawMessage           `json:"code"`
	Message   json.RawMessage           `json:"message"`
	Param     json.RawMessage           `json:"param"`
}

func (e openAIResponsesStreamEvent) genericError() (openAIError, bool) {
	// Responses generic error events use flat code/message/param fields.
	// Keep the legacy nested envelope, but never choose between mixed shapes.
	if len(e.Error) != 0 {
		if len(e.Code) != 0 || len(e.Message) != 0 || len(e.Param) != 0 {
			return openAIError{}, false
		}
		var wire *openAIError
		if json.Unmarshal(e.Error, &wire) != nil || wire == nil {
			return openAIError{}, false
		}
		return *wire, true
	}
	var message *string
	if !utf8.Valid(e.Message) || json.Unmarshal(e.Message, &message) != nil ||
		message == nil {
		return openAIError{}, false
	}
	for _, field := range []json.RawMessage{e.Code, e.Param} {
		if len(field) == 0 {
			continue
		}
		var value *string
		if !utf8.Valid(field) || json.Unmarshal(field, &value) != nil {
			return openAIError{}, false
		}
	}
	// Only the existing code allowlist classifies the error. Discard upstream
	// message and param so neither can become a diagnostic, cause or event.
	return openAIError{Code: e.Code}, true
}

type responsesStreamItem struct {
	typeName       StreamItemType
	wireType       string
	private        bool
	privateBytes   int
	callID         string
	name           string
	arguments      strings.Builder
	finalArguments string
	text           strings.Builder
	callCompleted  bool
	callInvalid    bool
	completed      bool
	final          *openAIResponsesOutputItem
}

type responsesStreamState struct {
	provider       string
	toolNamespace  string
	selectedModel  string
	responseID     string
	events         providerStreamEvents
	items          map[string]*responsesStreamItem
	itemOrder      []string
	toolCalls      []ToolCall
	binding        string
	started        bool
	terminal       bool
	publicItems    int
	wireEvents     int
	pendingToolErr error
	refused        bool
	refusalBytes   int
}

func (s *responsesStreamState) consume(payload []byte) (*ChatChunk, bool, error) {
	s.wireEvents++
	if s.wireEvents > MaxItemStreamEvents || len(payload) > maxOpenAIStreamEventBytes {
		return nil, false, openAIProtocolError(s.provider, "Responses stream exceeded its event limit")
	}
	var event openAIResponsesStreamEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, false, openAIProtocolError(s.provider, "returned malformed Responses stream event")
	}
	switch event.Type {
	case "response.created":
		if s.started || event.Response.Object != "response" || event.Response.Status != "in_progress" ||
			validateStreamIdentity(event.Response.ID, "Responses stream response") != nil {
			return nil, false, openAIProtocolError(s.provider, "returned an invalid Responses stream start")
		}
		if _, err := normalizeOpenAIModel(event.Response.Model); err != nil {
			return nil, false, openAIProtocolError(s.provider, "returned an invalid Responses stream model")
		}
		s.started = true
		s.responseID = event.Response.ID
		s.events = newProviderStreamEvents(s.provider, s.selectedModel,
			s.responseID, StreamGranularityDelta)
		return &ChatChunk{Events: []StreamEvent{s.events.start()}}, false, nil
	case "response.in_progress", "response.content_part.added", "response.content_part.done",
		"response.output_text.done":
		if !s.started || s.terminal {
			return nil, false, openAIProtocolError(s.provider, "returned a Responses event outside an active response")
		}
		return nil, false, nil
	case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done",
		"response.reasoning_summary_text.done", "response.reasoning_text.done":
		item := s.items[event.ItemID]
		if !s.started || s.terminal || item == nil || !item.private || item.completed {
			return nil, false, openAIProtocolError(s.provider, "returned reasoning summary outside a private item")
		}
		return nil, false, nil
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		item := s.items[event.ItemID]
		if !s.started || s.terminal || item == nil || !item.private || item.completed ||
			event.Delta == "" || !utf8.ValidString(event.Delta) ||
			item.privateBytes+len(event.Delta) > MaxModelOutputBytes {
			return nil, false, openAIProtocolError(s.provider, "returned invalid private reasoning summary")
		}
		item.privateBytes += len(event.Delta)
		s.events.markGeneration()
		return nil, false, nil
	case "response.output_item.added":
		return s.startItem(event.Item)
	case "response.output_text.delta":
		item, err := s.activeItem(event.ItemID, StreamItemMessage)
		if err != nil || event.Delta == "" || !utf8.ValidString(event.Delta) ||
			item.text.Len()+len(event.Delta) > MaxModelOutputBytes {
			return nil, false, openAIProtocolError(s.provider, "returned an invalid Responses text delta")
		}
		_, _ = item.text.WriteString(event.Delta)
		return &ChatChunk{Text: event.Delta, Events: []StreamEvent{s.events.emit(StreamEvent{
			Type: StreamTextDelta, ItemID: event.ItemID, ItemType: StreamItemMessage,
			ItemStatus: StreamItemInProgress, TextDelta: event.Delta,
		})}}, false, nil
	case "response.refusal.delta":
		_, err := s.activeItem(event.ItemID, StreamItemMessage)
		if err != nil || event.Delta == "" || !utf8.ValidString(event.Delta) ||
			s.refusalBytes+len(event.Delta) > MaxModelOutputBytes {
			return nil, false, openAIProtocolError(s.provider, "returned an invalid Responses refusal delta")
		}
		s.refused = true
		s.refusalBytes += len(event.Delta)
		return nil, false, nil
	case "response.refusal.done":
		if _, err := s.activeItem(event.ItemID, StreamItemMessage); err != nil ||
			event.Refusal == "" || !utf8.ValidString(event.Refusal) ||
			len(event.Refusal) > MaxModelOutputBytes {
			return nil, false, openAIProtocolError(s.provider, "returned an invalid Responses refusal completion")
		}
		s.refused = true
		return nil, false, nil
	case "response.function_call_arguments.delta":
		item, err := s.activeItem(event.ItemID, StreamItemToolCall)
		if err != nil || event.Delta == "" || !utf8.ValidString(event.Delta) ||
			item.arguments.Len()+len(event.Delta) > MaxProviderToolPayloadSize {
			return nil, false, openAIProtocolError(s.provider, "returned an invalid Responses function delta")
		}
		_, _ = item.arguments.WriteString(event.Delta)
		return &ChatChunk{Events: []StreamEvent{s.events.emit(StreamEvent{
			Type: StreamToolArgumentDelta, ItemID: event.ItemID, CallID: item.callID,
			ItemType: StreamItemToolCall, ItemStatus: StreamItemInProgress,
			ToolName: item.name, ArgumentDelta: event.Delta,
		})}}, false, nil
	case "response.function_call_arguments.done":
		item, err := s.activeItem(event.ItemID, StreamItemToolCall)
		if err != nil || item.callCompleted || (event.Name != "" && event.Name != item.name) {
			return nil, false, openAIProtocolError(s.provider, "returned an invalid Responses function completion")
		}
		return s.completeTool(event.ItemID, item, event.Arguments)
	case "response.output_item.done":
		return s.completeItem(event.Item)
	case "response.completed":
		if !s.started || s.terminal || event.Response.ID != s.responseID ||
			event.Response.Object != "response" || event.Response.Status != "completed" ||
			event.Response.Error != nil || len(s.items) == 0 || s.publicItems == 0 {
			return nil, false, openAIProtocolError(s.provider, "returned an invalid Responses completion")
		}
		if _, err := normalizeOpenAIModel(event.Response.Model); err != nil {
			return nil, false, openAIProtocolError(s.provider, "returned an invalid Responses completion model")
		}
		for _, item := range s.items {
			if !item.completed || item.final == nil || !item.final.completedStatus() {
				return nil, false, openAIProtocolError(s.provider, "completed Responses stream with unfinished items")
			}
		}
		usage, err := normalizeResponsesUsage(event.Response.Usage)
		if err != nil {
			return nil, false, openAIProtocolError(s.provider, "returned invalid Responses stream usage")
		}
		calls, err := NormalizeToolCalls(s.toolCalls)
		if err != nil {
			return nil, false, openAIProtocolError(s.provider, "returned invalid Responses stream calls")
		}
		if s.pendingToolErr != nil {
			return nil, false, s.pendingToolErr
		}
		if s.refused {
			if len(calls) != 0 || s.hasResponseText() {
				return nil, false, openAIProtocolError(s.provider,
					"mixed a Responses refusal with executable output")
			}
			s.terminal = true
			completionErr := CompletionError(s.provider, FinishReasonRefusal)
			return &ChatChunk{Done: false, Usage: &usage, Model: s.selectedModel, Provider: s.provider,
				FinishReason: FinishReasonRefusal, Err: completionErr,
				Events: []StreamEvent{s.events.terminalEvent(OutcomePermanent, &usage)}}, true, nil
		}
		output, err := s.completedOutput()
		if err != nil {
			return nil, false, openAIProtocolError(s.provider, "returned invalid completed Responses items")
		}
		if len(event.Response.Output) > 0 {
			if !responsesCompletedOutputEqual(output, event.Response.Output) {
				return nil, false, openAIProtocolError(s.provider,
					"Responses terminal output changed after item completion")
			}
			output = event.Response.Output
		}
		var replay *ProviderReplay
		if len(calls) > 0 {
			parts, err := responsesReplayParts(output)
			if err != nil {
				return nil, false, openAIProtocolError(s.provider, "returned invalid replayable Responses stream output")
			}
			replay, err = newProviderReplay(s.provider, s.selectedModel,
				HarnessTransportOpenAIResponses, s.binding, parts, calls)
			if err != nil {
				return nil, false, openAIProtocolError(s.provider, "returned invalid replayable Responses stream output")
			}
		}
		finishReason := FinishReasonStop
		if len(calls) > 0 {
			finishReason = FinishReasonToolCalls
		}
		s.terminal = true
		return &ChatChunk{Done: true, ToolCalls: calls, Usage: &usage,
			Model: s.selectedModel, Provider: s.provider,
			FinishReason: finishReason, Replay: replay,
			Events: []StreamEvent{s.events.terminalEvent(OutcomeSuccess, &usage)}}, true, nil
	case "response.incomplete":
		if !s.started || s.terminal || event.Response.ID != s.responseID ||
			event.Response.Object != "response" || event.Response.Status != "incomplete" {
			return nil, false, openAIProtocolError(s.provider, "returned an invalid incomplete Responses terminal")
		}
		usage, err := normalizeResponsesUsage(event.Response.Usage)
		if err != nil {
			return nil, false, openAIProtocolError(s.provider, "returned invalid incomplete Responses usage")
		}
		finishReason := responsesIncompleteFinishReason(event.Response.IncompleteDetails)
		completionErr := CompletionError(s.provider, finishReason)
		if completionErr == nil {
			return nil, false, openAIProtocolError(s.provider,
				"returned an incomplete response without a supported reason")
		}
		s.terminal = true
		return &ChatChunk{Done: false, Usage: &usage, Model: s.selectedModel, Provider: s.provider,
			FinishReason: finishReason, Err: completionErr,
			Events: []StreamEvent{s.events.terminalEvent(OutcomePermanent, &usage)}}, true, nil
	case "response.failed":
		if event.Response.Error == nil {
			return nil, false, openAIProtocolError(s.provider, "Responses stream failed without a provider error")
		}
		providerErr := openAIWireError(s.provider, *event.Response.Error)
		chunk := &ChatChunk{Done: false, Model: s.selectedModel, Provider: s.provider,
			FinishReason: FinishReasonUnknown, Err: providerErr}
		if usage, err := normalizeResponsesUsage(event.Response.Usage); err == nil {
			chunk.Usage = &usage
			chunk.Events = []StreamEvent{s.events.terminalEvent(providerErr.Kind, &usage)}
		} else {
			chunk.Events = []StreamEvent{s.events.terminalEvent(providerErr.Kind, nil)}
		}
		s.terminal = true
		return chunk, true, nil
	case "error":
		wire, valid := event.genericError()
		if !valid {
			return nil, false, openAIProtocolError(s.provider, "Responses stream returned an invalid error event")
		}
		return nil, false, openAIWireError(s.provider, wire)
	default:
		return nil, false, openAIProtocolError(s.provider, "returned an unsupported Responses stream event")
	}
}

func (s *responsesStreamState) startItem(item openAIResponsesOutputItem) (*ChatChunk, bool, error) {
	if !s.started || s.terminal || len(s.items) >= MaxProviderOutputItems ||
		validateStreamIdentity(item.ID, "Responses output item") != nil || s.items[item.ID] != nil {
		return nil, false, openAIProtocolError(s.provider, "returned an invalid Responses output item start")
	}
	if item.Status != "in_progress" && !item.reasoningStatusOmitted() {
		return nil, false, openAIProtocolError(s.provider, "returned an invalid Responses output item status")
	}
	state := &responsesStreamItem{wireType: item.Type}
	events := make([]StreamEvent, 0, 2)
	switch item.Type {
	case "message":
		state.typeName = StreamItemMessage
		s.publicItems++
		events = append(events, s.events.emit(StreamEvent{Type: StreamOutputItemStarted,
			ItemID: item.ID, ItemType: StreamItemMessage, ItemStatus: StreamItemInProgress}))
	case "function_call":
		callID := strings.TrimSpace(item.CallID)
		name := strings.TrimSpace(item.Name)
		if validateStreamIdentity(callID, "Responses function call") != nil ||
			(s.toolNamespace != "" && item.Namespace != s.toolNamespace) ||
			validateToolName(name) != nil || len(s.toolCalls) >= MaxProviderToolCalls {
			return nil, false, openAIProtocolError(s.provider, "returned an invalid Responses function start")
		}
		for _, existing := range s.items {
			if existing.callID == callID {
				return nil, false, openAIProtocolError(s.provider, "reused a Responses function call id")
			}
		}
		state.typeName, state.callID, state.name = StreamItemToolCall, callID, name
		s.publicItems++
		events = append(events,
			s.events.emit(StreamEvent{Type: StreamOutputItemStarted, ItemID: item.ID,
				ItemType: StreamItemToolCall, ItemStatus: StreamItemInProgress}),
			s.events.emit(StreamEvent{Type: StreamToolCallStarted, ItemID: item.ID,
				CallID: callID, ItemType: StreamItemToolCall,
				ItemStatus: StreamItemInProgress, ToolName: name}))
	case "reasoning", "compaction":
		state.private = true
	default:
		return nil, false, openAIProtocolError(s.provider, "returned an unsupported Responses output item start")
	}
	s.items[item.ID] = state
	s.itemOrder = append(s.itemOrder, item.ID)
	return &ChatChunk{Events: events}, false, nil
}

func (s *responsesStreamState) activeItem(id string,
	typeName StreamItemType,
) (*responsesStreamItem, error) {
	if !s.started || s.terminal {
		return nil, errors.New("Responses stream is not active")
	}
	item := s.items[id]
	if item == nil || item.typeName != typeName || item.completed {
		return nil, errors.New("Responses output item is not active")
	}
	return item, nil
}

func (s *responsesStreamState) completeTool(id string, item *responsesStreamItem,
	arguments string,
) (*ChatChunk, bool, error) {
	if item.arguments.Len() != 0 {
		if arguments == "" || strings.TrimSpace(arguments) != strings.TrimSpace(item.arguments.String()) {
			return nil, false, openAIProtocolError(s.provider, "Responses function arguments changed at completion")
		}
	} else if arguments == "" {
		arguments = `{}`
	}
	call, err := NormalizeToolCall(ToolCall{ID: item.callID, Name: item.name,
		Arguments: json.RawMessage(arguments)})
	if err != nil {
		var value any
		var syntaxErr *json.SyntaxError
		message := "returned an invalid completed Responses function"
		if parseErr := json.Unmarshal([]byte(arguments), &value); errors.As(parseErr, &syntaxErr) &&
			syntaxErr.Error() == "unexpected end of JSON input" {
			message = "returned incomplete Responses function arguments; check the model output-token limit"
		}
		toolErr := openAIProtocolError(s.provider, message)
		s.pendingToolErr = toolErr
		item.callCompleted = true
		item.callInvalid = true
		item.finalArguments = arguments
		return &ChatChunk{}, false, nil
	}
	item.callCompleted = true
	item.finalArguments = string(call.Arguments)
	s.toolCalls = append(s.toolCalls, call)
	callCopy := call
	return &ChatChunk{Events: []StreamEvent{s.events.emit(StreamEvent{
		Type: StreamToolCallCompleted, ItemID: id, CallID: item.callID,
		ItemType: StreamItemToolCall, ItemStatus: StreamItemReadyForValidation,
		ToolName: item.name, CompletedCall: &callCopy,
	})}}, false, nil
}

func (s *responsesStreamState) completeItem(wire openAIResponsesOutputItem) (*ChatChunk, bool, error) {
	item := s.items[wire.ID]
	if item == nil || item.completed || wire.Type == "" || wire.Type != item.wireType ||
		(wire.Status != "completed" && wire.Status != "incomplete" && !wire.reasoningStatusOmitted()) {
		return nil, false, openAIProtocolError(s.provider, "returned an invalid Responses output item completion")
	}
	incomplete := wire.Status == "incomplete"
	chunk := &ChatChunk{}
	if item.private {
		item.completed = true
		copy := wire
		copy.Content = append([]openAIResponsesContent(nil), wire.Content...)
		copy.Summary = append(json.RawMessage(nil), wire.Summary...)
		item.final = &copy
		return chunk, false, nil
	}
	if item.typeName == StreamItemMessage {
		var completedText strings.Builder
		for _, part := range wire.Content {
			switch part.Type {
			case "output_text":
				if !utf8.ValidString(part.Text) || completedText.Len()+len(part.Text) > MaxModelOutputBytes {
					return nil, false, openAIProtocolError(s.provider, "returned invalid completed Responses text")
				}
				_, _ = completedText.WriteString(part.Text)
			case "refusal":
				if part.Refusal == "" || !utf8.ValidString(part.Refusal) ||
					len(part.Refusal) > MaxModelOutputBytes {
					return nil, false, openAIProtocolError(s.provider, "returned invalid completed Responses refusal")
				}
				s.refused = true
			default:
				return nil, false, openAIProtocolError(s.provider, "returned unsupported Responses message content")
			}
		}
		if item.text.Len() == 0 && completedText.Len() > 0 {
			text := completedText.String()
			_, _ = item.text.WriteString(text)
			chunk.Text = text
			chunk.Events = append(chunk.Events, s.events.emit(StreamEvent{Type: StreamTextDelta,
				ItemID: wire.ID, ItemType: StreamItemMessage, ItemStatus: StreamItemInProgress,
				TextDelta: text}))
		} else if completedText.String() != item.text.String() {
			return nil, false, openAIProtocolError(s.provider, "Responses text changed at item completion")
		}
		if !incomplete {
			chunk.Events = append(chunk.Events, s.events.emit(StreamEvent{Type: StreamOutputItemCompleted,
				ItemID: wire.ID, ItemType: StreamItemMessage, ItemStatus: StreamItemCompleted}))
		}
	} else {
		if wire.CallID != item.callID || wire.Name != item.name ||
			(s.toolNamespace != "" && wire.Namespace != s.toolNamespace) {
			return nil, false, openAIProtocolError(s.provider, "Responses function identity changed at item completion")
		}
		if incomplete {
			if item.arguments.Len() != 0 && wire.Arguments != "" &&
				strings.TrimSpace(wire.Arguments) != strings.TrimSpace(item.arguments.String()) {
				return nil, false, openAIProtocolError(s.provider,
					"Responses function arguments changed at incomplete item completion")
			}
		} else if !item.callCompleted {
			completed, _, err := s.completeTool(wire.ID, item, wire.Arguments)
			if err != nil {
				return nil, false, err
			}
			chunk.Events = append(chunk.Events, completed.Events...)
		} else if strings.TrimSpace(wire.Arguments) != strings.TrimSpace(item.finalArguments) {
			return nil, false, openAIProtocolError(s.provider,
				"Responses function arguments changed at item completion")
		}
		if !incomplete && !item.callInvalid {
			chunk.Events = append(chunk.Events, s.events.emit(StreamEvent{Type: StreamOutputItemCompleted,
				ItemID: wire.ID, CallID: item.callID, ItemType: StreamItemToolCall,
				ItemStatus: StreamItemCompleted, ToolName: item.name}))
		}
	}
	item.completed = true
	copy := wire
	copy.Content = append([]openAIResponsesContent(nil), wire.Content...)
	copy.Summary = append(json.RawMessage(nil), wire.Summary...)
	item.final = &copy
	return chunk, false, nil
}

func (s *responsesStreamState) hasResponseText() bool {
	for _, item := range s.items {
		if item != nil && item.typeName == StreamItemMessage && item.text.Len() != 0 {
			return true
		}
	}
	return false
}

func (s *responsesStreamState) completedOutput() ([]openAIResponsesOutputItem, error) {
	output := make([]openAIResponsesOutputItem, 0, len(s.itemOrder))
	for _, id := range s.itemOrder {
		item := s.items[id]
		if item == nil || !item.completed || item.final == nil || !item.final.completedStatus() {
			return nil, errors.New("Responses output item is unfinished")
		}
		output = append(output, *item.final)
	}
	return output, nil
}

func (p *OpenAIResponsesProvider) readStream(ctx context.Context, body io.ReadCloser,
	selectedModel string, chunks chan<- ChatChunk,
) {
	defer close(chunks)
	defer body.Close()
	state := responsesStreamState{provider: p.name, selectedModel: selectedModel,
		toolNamespace: p.toolNamespace,
		binding:       p.DescribeModelHarness(selectedModel).BindingDigest,
		items:         make(map[string]*responsesStreamItem)}
	send := func(chunk ChatChunk) bool {
		select {
		case <-ctx.Done():
			return false
		case chunks <- chunk:
			return true
		}
	}
	sendFailure := func(err error) bool {
		if state.events.provider == "" {
			state.events = newProviderStreamEvents(p.name, selectedModel,
				"responses-unstarted", StreamGranularityDelta)
		}
		return send(state.events.failureChunk(err))
	}
	err := readProviderSSE(ctx, body, func(payload string) bool {
		if payload == "[DONE]" {
			if state.terminal {
				return false
			}
			if state.pendingToolErr != nil {
				_ = sendFailure(state.pendingToolErr)
				return false
			}
			_ = sendFailure(openAIProtocolError(p.name, "Responses stream ended before completion"))
			return false
		}
		chunk, done, err := state.consume([]byte(payload))
		if err != nil {
			_ = sendFailure(err)
			return false
		}
		if chunk != nil && !send(*chunk) {
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
		_ = sendFailure(openAIProtocolError(p.name, "Responses stream event exceeds its limit"))
		return
	case io.EOF:
		// The provider state below decides whether EOF is a valid terminal.
	default:
		_ = sendFailure(openAIReadError(ctx, p.name, "could not read Responses stream", err))
		return
	}
	if state.pendingToolErr != nil {
		_ = sendFailure(state.pendingToolErr)
		return
	}
	_ = sendFailure(openAIProtocolError(p.name, "Responses stream ended before completion"))
}
