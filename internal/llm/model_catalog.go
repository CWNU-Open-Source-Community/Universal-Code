package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/redact"
)

// CatalogModel describes remote catalog data, not qualified model capabilities.
type CatalogModel struct {
	ID               string
	DisplayName      string
	InputTokenLimit  int
	OutputTokenLimit int
}

type ModelCatalog struct {
	Models    []CatalogModel
	Truncated bool
}

// ModelCatalogRequest is shared by configured providers and transient drafts.
// Runtime supplies validated headers and resolves credentials for each page.
type ModelCatalogRequest struct {
	Provider, EndpointURL, Transport, APIKey string
	Runtime                                  HTTPProviderRuntime
	HTTPClient                               *http.Client
	MaxModels                                int
}

// FetchModelCatalog owns the catalog wire protocol for both discovery and CLI
// listing. Errors never become configured default models or upstream text.
func FetchModelCatalog(ctx context.Context, o ModelCatalogRequest) (ModelCatalog, error) {
	result := ModelCatalog{Models: []CatalogModel{}}
	if ctx == nil {
		return result, errors.New("model catalog context is required")
	}
	endpoint, google, anthropic, err := modelCatalogEndpoint(o.EndpointURL, o.Transport)
	if err != nil {
		return result, NewProviderError(OutcomePermanent, o.Provider, "model catalog endpoint is invalid", nil)
	}
	client, err := providerHTTPClient(o.HTTPClient)
	if err != nil {
		return result, err
	}
	limit := o.MaxModels
	if limit <= 0 || limit > 4096 {
		limit = 512
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	seenModels, seenCursors := map[string]bool{}, map[string]bool{}
	cursor := ""
	for page := 0; page < 8; page++ {
		secret, err := providerRequestCredential(ctx, o.Provider, o.APIKey, o.Runtime, o.EndpointURL)
		if err != nil {
			return result, NewProviderError(OutcomePermanent, o.Provider, "model catalog credential is unavailable", nil)
		}
		q := url.Values{}
		switch {
		case google:
			q.Set("pageSize", "100")
			if cursor != "" {
				q.Set("pageToken", cursor)
			}
		case anthropic:
			q.Set("limit", "100")
			if cursor != "" {
				q.Set("after_id", cursor)
			}
		case cursor != "":
			q.Set("after", cursor)
		}
		endpoint.RawQuery = q.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return result, NewProviderError(OutcomePermanent, o.Provider, "model catalog request is invalid", nil)
		}
		req.Header.Set("Accept", "application/json")
		if secret != "" {
			switch {
			case google:
				req.Header.Set("x-goog-api-key", secret)
			case anthropic:
				req.Header.Set("x-api-key", secret)
				req.Header.Set("Authorization", "Bearer "+secret)
			default:
				req.Header.Set("Authorization", "Bearer "+secret)
			}
		}
		if anthropic {
			req.Header.Set("anthropic-version", AnthropicVersion)
		}
		if applyProviderRequestHeaders(o.Runtime, secret, req.Header) != nil {
			return result, NewProviderError(OutcomePermanent, o.Provider, "model catalog headers are invalid", nil)
		}
		response, err := client.Do(req)
		if err != nil {
			return result, openAITransportError(ctx, o.Provider)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		_ = response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			failure := openAIHTTPError(o.Provider, response.StatusCode, response.Header.Get("Retry-After"), nil)
			failure.Message = fmt.Sprintf("model catalog returned HTTP %d", response.StatusCode)
			return result, failure
		}
		if readErr != nil {
			return result, providerHTTPReadError(ctx, o.Provider, "could not read model catalog", readErr)
		}
		if len(body) > 1<<20 {
			return result, openAIProtocolError(o.Provider, "model catalog response exceeds its byte limit")
		}
		models, next, more, err := parseModelCatalogPage(body, google, secret)
		if err != nil {
			return result, openAIProtocolError(o.Provider, "returned an invalid model catalog")
		}
		for _, model := range models {
			if seenModels[model.ID] {
				continue
			}
			if len(result.Models) == limit {
				result.Truncated = true
				return result, nil
			}
			seenModels[model.ID] = true
			result.Models = append(result.Models, model)
		}
		if !more {
			return result, nil
		}
		if !safeCatalogText(next, 2048, secret, true) || next == "" || seenCursors[next] {
			return result, openAIProtocolError(o.Provider, "model catalog pagination is invalid")
		}
		seenCursors[next], cursor = true, next
	}
	result.Truncated = true
	return result, nil
}

func modelCatalogEndpoint(baseURL, transport string) (*url.URL, bool, bool, error) {
	switch transport {
	case HarnessTransportOpenAIChatCompletions, HarnessTransportOpenAIResponses, HarnessTransportAnthropicMessages:
	default:
		return nil, false, false, errors.New("unsupported catalog transport")
	}
	normalized, err := normalizeProviderBaseURL(baseURL, "catalog")
	if err != nil {
		return nil, false, false, err
	}
	u, _ := url.Parse(normalized)
	official := u.Scheme == "https" && (u.Port() == "" || u.Port() == "443")
	host := strings.TrimSuffix(u.Hostname(), ".")
	google := official && strings.EqualFold(host, "generativelanguage.googleapis.com")
	deepseek := official && strings.EqualFold(host, "api.deepseek.com")
	anthropic := transport == HarnessTransportAnthropicMessages && !google && !deepseek
	path := strings.TrimRight(u.Path, "/")
	switch {
	case deepseek:
		u.Path = "/models"
	case google && (path == "/v1beta" || strings.HasPrefix(path, "/v1beta/")):
		u.Path = "/v1beta/models"
	default:
		explicitEndpoint := false
		for _, suffix := range []string{"/chat/completions", "/responses", "/messages"} {
			if strings.HasSuffix(path, suffix) {
				path = strings.TrimSuffix(path, suffix)
				explicitEndpoint = true
				break
			}
		}
		if !explicitEndpoint && !strings.HasSuffix(path, "/v1") {
			path += "/v1"
		}
		u.Path = path + "/models"
	}
	u.RawPath = ""
	return u, google, anthropic, nil
}

func listProviderCatalog(ctx context.Context, o ModelCatalogRequest) ([]ModelInfo, error) {
	o.MaxModels = 4096
	catalog, err := FetchModelCatalog(ctx, o)
	if err != nil {
		return nil, err
	}
	if catalog.Truncated {
		return nil, openAIProtocolError(o.Provider, "model catalog exceeds its listing limit")
	}
	models := make([]ModelInfo, 0, len(catalog.Models))
	for _, model := range catalog.Models {
		display := model.DisplayName
		if display == "" {
			display = model.ID
		}
		models = append(models, ModelInfo{ID: model.ID, DisplayName: display,
			Provider: o.Provider, Capabilities: []string{}})
	}
	return models, nil
}

func parseModelCatalogPage(body []byte, google bool, secret string) ([]CatalogModel, string, bool, error) {
	var page struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			MaxInput    int    `json:"max_input_tokens"`
			MaxOutput   int    `json:"max_tokens"`
		} `json:"data"`
		Models []struct {
			Name        string   `json:"name"`
			DisplayName string   `json:"displayName"`
			Input       int      `json:"inputTokenLimit"`
			Output      int      `json:"outputTokenLimit"`
			Methods     []string `json:"supportedGenerationMethods"`
		} `json:"models"`
		Error   json.RawMessage `json:"error"`
		HasMore bool            `json:"has_more"`
		LastID  string          `json:"last_id"`
		Next    string          `json:"nextPageToken"`
	}
	if !utf8.Valid(body) || json.Unmarshal(body, &page) != nil ||
		(len(page.Error) > 0 && string(page.Error) != "null") ||
		(google && page.Models == nil) || (!google && page.Data == nil) {
		return nil, "", false, errors.New("invalid catalog")
	}
	models := []CatalogModel{}
	add := func(id, display string, input, output int) {
		if id == "" || !safeCatalogText(id, 256, secret, true) {
			return
		}
		model := CatalogModel{ID: id}
		if safeCatalogText(display, 256, secret, false) {
			model.DisplayName = display
		}
		if input > 0 && input <= 2*1024*1024 {
			model.InputTokenLimit = input
		}
		if output > 0 && output <= 1_000_000 {
			model.OutputTokenLimit = output
		}
		models = append(models, model)
	}
	if google {
		for _, model := range page.Models {
			for _, method := range model.Methods {
				if method == "generateContent" {
					add(strings.TrimPrefix(model.Name, "models/"), model.DisplayName, model.Input, model.Output)
					break
				}
			}
		}
		return models, page.Next, page.Next != "", nil
	}
	for _, model := range page.Data {
		add(model.ID, model.DisplayName, model.MaxInput, model.MaxOutput)
	}
	return models, page.LastID, page.HasMore, nil
}

func safeCatalogText(value string, maximum int, secret string, cursor bool) bool {
	if !utf8.ValidString(value) || len(value) > maximum || strings.TrimSpace(value) != value ||
		redact.String(value) != value || (secret != "" && strings.Contains(value, secret)) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || (cursor && unicode.IsSpace(r)) {
			return false
		}
	}
	return true
}
