package modelregistry

import (
	"context"
	"encoding/json"
	"errors"

	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/llm"
)

const ModelDiscoveryVersion = "provider_model_discovery.v1"

type DiscoveredModel struct {
	ID               string `json:"id"`
	DisplayName      string `json:"display_name,omitempty"`
	InputTokenLimit  int    `json:"input_token_limit,omitempty"`
	OutputTokenLimit int    `json:"output_token_limit,omitempty"`
}

type ModelDiscoveryResult struct {
	Version   string            `json:"version"`
	Source    string            `json:"source"`
	Models    []DiscoveredModel `json:"models"`
	Truncated bool              `json:"truncated"`
}

type ModelDiscoveryOptions struct {
	ProviderID     string
	EndpointURL    string
	Transport      string
	AdvancedConfig json.RawMessage
	Secret         string
}

// ValidateModelDiscoveryDraft validates an empty-model draft independently of
// Provider definitions. Fetching a catalog neither saves nor qualifies a model.
func ValidateModelDiscoveryDraft(o ModelDiscoveryOptions) error {
	if !ValidCustomProviderID(o.ProviderID) || validateDefinitionURL(o.EndpointURL, false, maxProviderEndpointURLBytes) != nil {
		return errors.New("model discovery Provider identity or endpoint is invalid")
	}
	if o.Transport != llm.HarnessTransportOpenAIChatCompletions && o.Transport != llm.HarnessTransportOpenAIResponses && o.Transport != llm.HarnessTransportAnthropicMessages {
		return errors.New("model discovery transport is invalid")
	}
	_, err := ValidateAndNormalizeProviderAdvancedConfig(o.AdvancedConfig, o.ProviderID)
	return err
}

// DiscoverProviderModels sends only bounded, cancellable GET requests to the
// frozen draft origin. It never falls back to a preset after an upstream error.
func DiscoverProviderModels(ctx context.Context, o ModelDiscoveryOptions) (ModelDiscoveryResult, error) {
	result := ModelDiscoveryResult{Version: ModelDiscoveryVersion, Source: "provider_api", Models: []DiscoveredModel{}}
	if ctx == nil || ValidateModelDiscoveryDraft(o) != nil {
		return result, errors.New("model discovery draft is invalid")
	}
	normalized, _ := ValidateAndNormalizeProviderAdvancedConfig(o.AdvancedConfig, o.ProviderID)
	var advanced map[string]json.RawMessage
	_ = json.Unmarshal(normalized, &advanced)
	var headers map[string]any
	if raw, ok := advanced["request_headers"]; ok {
		_ = json.Unmarshal(raw, &headers)
	}
	policy := &providerRequestRuntime{providerID: o.ProviderID, endpoint: o.EndpointURL, headers: headers}
	if o.Secret == "" && !policy.AllowsKeylessEndpoint(o.EndpointURL) {
		return result, errors.New("model discovery requires an API key")
	}
	if o.Secret != "" && (!credential.ValidSecret(o.Secret) || len(o.Secret) < 8) {
		return result, errors.New("model discovery API key is invalid")
	}
	policy.credential = func(context.Context, string) (string, bool, error) {
		return o.Secret, o.Secret != "", nil
	}
	catalog, err := llm.FetchModelCatalog(ctx, llm.ModelCatalogRequest{
		Provider: o.ProviderID, EndpointURL: o.EndpointURL, Transport: o.Transport,
		Runtime: policy, MaxModels: 512,
	})
	if err != nil {
		return result, err
	}
	result.Truncated = catalog.Truncated
	for _, model := range catalog.Models {
		result.Models = append(result.Models, DiscoveredModel{
			ID: model.ID, DisplayName: model.DisplayName,
			InputTokenLimit: model.InputTokenLimit, OutputTokenLimit: model.OutputTokenLimit,
		})
	}
	return result, nil
}
