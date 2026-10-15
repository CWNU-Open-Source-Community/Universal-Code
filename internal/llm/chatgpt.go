package llm

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

const (
	ChatGPTResponsesEndpoint = "https://api.openai.com/v1/responses"
	ChatGPTToolNamespace     = "universal_code"
)

// ChatGPTProviderConfig has no endpoint or static API-key override. Its runtime
// belongs to one verified registration and resolves that account at dispatch.
type ChatGPTProviderConfig struct {
	Name         string
	DefaultModel string
	Models       []ModelInfo
	Runtime      EndpointBoundHTTPProviderRuntime
	HTTPClient   *http.Client
}

type ChatGPTProvider struct {
	*OpenAIResponsesProvider
	models []ModelInfo
}

func NewChatGPTProvider(config ChatGPTProviderConfig) (*ChatGPTProvider, error) {
	if config.Runtime == nil || !config.Runtime.AllowsCredentialEndpoint(ChatGPTResponsesEndpoint) {
		return nil, errors.New("ChatGPT account runtime is required")
	}
	client, err := providerHTTPClient(config.HTTPClient)
	if err != nil {
		return nil, err
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client.Transport = chatGPTResponsesTransport{next: transport}
	responses, err := NewOpenAIResponsesProvider(OpenAIResponsesConfig{
		Name: config.Name, BaseURL: ChatGPTResponsesEndpoint,
		DefaultModel: config.DefaultModel, Runtime: config.Runtime, HTTPClient: client,
	})
	if err != nil {
		return nil, err
	}
	responses.toolNamespace = ChatGPTToolNamespace
	return &ChatGPTProvider{OpenAIResponsesProvider: responses,
		models: cloneChatGPTModels(config.Models)}, nil
}

func cloneChatGPTModels(models []ModelInfo) []ModelInfo {
	copy := append([]ModelInfo(nil), models...)
	for i := range copy {
		copy[i].Capabilities = append([]string(nil), copy[i].Capabilities...)
	}
	return copy
}

// ListModels returns the account catalog already discovered by the login
// service. Check the account first so an old captured provider cannot list a
// signed-out or replaced registration's models.
func (p *ChatGPTProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	if ctx == nil {
		return nil, openAILocalError(p.name, "ChatGPT request context is required")
	}
	if _, err := providerRequestCredential(ctx, p.name, "", p.runtime, p.baseURL); err != nil {
		return nil, openAILocalError(p.name, "ChatGPT account is unavailable")
	}
	return cloneChatGPTModels(p.models), nil
}

// Chat uses the same SSE transport as StreamChat. Subscription inference must
// stream on the wire even for callers that need a completed buffered response.
func (p *ChatGPTProvider) Chat(ctx context.Context, request ChatRequest) (*ChatResponse, error) {
	if ctx == nil {
		return nil, openAILocalError(p.name, "ChatGPT request context is required")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	chunks, err := p.StreamChat(ctx, request)
	if err != nil {
		return nil, err
	}
	model := strings.TrimSpace(request.Model)
	if model == "" {
		model = p.defaultModel
	}
	var accumulator *ItemStreamAccumulator
	var text strings.Builder
	var result *ChatResponse
	for chunk := range chunks {
		if chunk.Err != nil {
			return nil, chunk.Err
		}
		if accumulator == nil {
			if len(chunk.Events) == 0 {
				return nil, openAIProtocolError(p.name, "ChatGPT stream omitted its response start")
			}
			accumulator, err = NewItemStreamAccumulator(chunk.Events[0].ResponseID, p.name, model)
			if err != nil {
				return nil, openAIProtocolError(p.name, "ChatGPT stream identity is invalid")
			}
		}
		chunk, _, err = accumulator.Consume(chunk)
		if err != nil || text.Len()+len(chunk.Text) > MaxModelOutputBytes {
			return nil, openAIProtocolError(p.name, "ChatGPT stream output is invalid")
		}
		text.WriteString(chunk.Text)
		if chunk.Done {
			if chunk.Usage == nil {
				return nil, openAIProtocolError(p.name, "ChatGPT completion omitted usage")
			}
			result = &ChatResponse{ResponseID: accumulator.ResponseID(), Text: text.String(),
				ToolCalls: chunk.ToolCalls, Items: accumulator.Items(), Usage: *chunk.Usage,
				Provider: chunk.Provider, Model: chunk.Model, FinishReason: chunk.FinishReason,
				Replay: chunk.Replay}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, openAITransportError(ctx, p.name)
	}
	if result == nil {
		return nil, openAIProtocolError(p.name, "ChatGPT stream ended before completion")
	}
	return result, nil
}

type chatGPTResponsesTransport struct{ next http.RoundTripper }

func (t chatGPTResponsesTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.String() != ChatGPTResponsesEndpoint || request.Method != http.MethodPost ||
		(request.Host != "" && request.Host != "api.openai.com") {
		return nil, errors.New("ChatGPT request endpoint is invalid")
	}
	for name := range request.Header {
		switch http.CanonicalHeaderKey(name) {
		case "Authorization", "Content-Type", "Accept", "User-Agent":
		default:
			return nil, errors.New("ChatGPT request contains an unsupported header")
		}
	}
	return t.next.RoundTrip(request)
}
