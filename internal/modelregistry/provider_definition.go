package modelregistry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/redact"
)

const (
	ProviderDefinitionVersion           = "provider_definition.v1"
	ProviderDefinitionCollectionVersion = "provider_definition_collection.v1"
	ProviderDefinitionsSettingKey       = "provider_definitions.v1"
)

const (
	ProviderTransportOpenAIChatCompletions = llm.HarnessTransportOpenAIChatCompletions
	ProviderTransportOpenAIResponses       = llm.HarnessTransportOpenAIResponses
	ProviderTransportAnthropicMessages     = llm.HarnessTransportAnthropicMessages
)

const (
	ProviderSearchModeDisabled       = "disabled"
	ProviderSearchModeAuto           = "auto"
	ProviderSearchModeWeb            = "web"
	ProviderSearchModeSearXNG        = "searxng"
	ProviderSearchModeProviderNative = "provider_native"
)

const (
	NativeWebSearchUnsupported        = "unsupported"
	NativeWebSearchDeclaredUnverified = "declared_unverified"
)

const (
	MaxCustomProviderDefinitions  = 64
	maxProviderDefinitions        = MaxCustomProviderDefinitions
	maxProviderDefinitionBytes    = 1 << 20
	maxProviderDisplayNameBytes   = 128
	maxProviderNoteBytes          = 2048
	maxProviderWebsiteURLBytes    = 2048
	maxProviderEndpointURLBytes   = 2048
	maxProviderDefinitionModels   = 128
	MaxProviderDefinitionRevision = uint64(1<<63 - 1)
	maxProviderCollectionRevision = MaxProviderDefinitionRevision
)

var reservedProviderIDs = map[string]struct{}{
	"anthropic": {},
	"chatgpt":   {},
	"deepseek":  {},
	"mimo":      {},
	"mock":      {},
	"ollama":    {},
	"openai":    {},
}

// ProviderDefinition is the durable, credential-free description of a custom
// model Provider. AdvancedConfig may carry provider-specific, credential-free
// JSON; actual secrets remain in the OS credential store under ID.
//
// SearchMode and NativeWebSearchCapability are operator declarations only.
// They do not grant a hosted search tool or any network authority.
type ProviderDefinition struct {
	Version                   string          `json:"version"`
	ID                        string          `json:"id"`
	DisplayName               string          `json:"display_name"`
	Note                      string          `json:"note"`
	WebsiteURL                string          `json:"website_url"`
	EndpointURL               string          `json:"endpoint_url"`
	DefaultModel              string          `json:"default_model"`
	Models                    []string        `json:"models"`
	Transport                 string          `json:"transport"`
	SearchMode                string          `json:"search_mode"`
	NativeWebSearchCapability string          `json:"native_web_search_capability"`
	AdvancedConfig            json.RawMessage `json:"advanced_config"`
	Enabled                   bool            `json:"enabled"`
	Revision                  uint64          `json:"revision"`
}

type ProviderDefinitionCollection struct {
	Version   string               `json:"version"`
	Revision  uint64               `json:"revision"`
	Providers []ProviderDefinition `json:"providers"`
}

func EmptyProviderDefinitionCollection() ProviderDefinitionCollection {
	return ProviderDefinitionCollection{Version: ProviderDefinitionCollectionVersion,
		Providers: []ProviderDefinition{}}
}

// Validate accepts Revision zero for a create draft. A persisted collection
// additionally requires every contained definition to have a positive
// revision.
func (definition ProviderDefinition) Validate() error {
	if definition.Version != ProviderDefinitionVersion {
		return errors.New("custom Provider definition version is invalid")
	}
	if !validCustomProviderID(definition.ID) {
		return errors.New("custom Provider id is invalid or reserved")
	}
	if !validPublicProviderText(definition.DisplayName, maxProviderDisplayNameBytes, true) {
		return errors.New("custom Provider display name is invalid")
	}
	if !validPublicProviderText(definition.Note, maxProviderNoteBytes, false) {
		return errors.New("custom Provider note is invalid or contains credential material")
	}
	if definition.WebsiteURL != "" {
		if err := validateDefinitionURL(definition.WebsiteURL, true, maxProviderWebsiteURLBytes); err != nil {
			return fmt.Errorf("custom Provider website URL is invalid: %w", err)
		}
	}
	if err := validateDefinitionURL(definition.EndpointURL, false, maxProviderEndpointURLBytes); err != nil {
		return fmt.Errorf("custom Provider endpoint URL is invalid: %w", err)
	}
	if len(definition.Models) == 0 || len(definition.Models) > maxProviderDefinitionModels {
		return fmt.Errorf("custom Provider models must contain between 1 and %d entries",
			maxProviderDefinitionModels)
	}
	if !validAvailabilityIdentifier(definition.DefaultModel, maxPublicModelNameBytes) {
		return errors.New("custom Provider default model is invalid")
	}
	seen := make(map[string]struct{}, len(definition.Models))
	defaultFound := false
	for _, model := range definition.Models {
		if !validAvailabilityIdentifier(model, maxPublicModelNameBytes) {
			return errors.New("custom Provider model identifier is invalid")
		}
		if _, duplicate := seen[model]; duplicate {
			return errors.New("custom Provider model identifiers must be unique")
		}
		seen[model] = struct{}{}
		defaultFound = defaultFound || model == definition.DefaultModel
	}
	if !defaultFound {
		return errors.New("custom Provider default model must be present in models")
	}
	switch definition.Transport {
	case ProviderTransportOpenAIChatCompletions, ProviderTransportOpenAIResponses,
		ProviderTransportAnthropicMessages:
	default:
		return errors.New("custom Provider transport is unsupported")
	}
	switch definition.SearchMode {
	case ProviderSearchModeDisabled, ProviderSearchModeAuto, ProviderSearchModeWeb,
		ProviderSearchModeSearXNG, ProviderSearchModeProviderNative:
	default:
		return errors.New("custom Provider search mode is unsupported")
	}
	switch definition.NativeWebSearchCapability {
	case NativeWebSearchUnsupported, NativeWebSearchDeclaredUnverified:
	default:
		return errors.New("custom Provider native web-search capability is unsupported")
	}
	if definition.SearchMode == ProviderSearchModeProviderNative &&
		definition.NativeWebSearchCapability != NativeWebSearchDeclaredUnverified {
		return errors.New("provider-native search mode requires an explicit unverified capability declaration")
	}
	normalized, err := ValidateAndNormalizeProviderAdvancedConfig(definition.AdvancedConfig, definition.ID)
	if err != nil {
		return err
	}
	if err := validateDefinedVisionModels(normalized, definition.Models); err != nil {
		return err
	}
	if definition.Revision > maxProviderCollectionRevision {
		return errors.New("custom Provider definition revision is out of range")
	}
	return nil
}

func (collection ProviderDefinitionCollection) Validate() error {
	if collection.Version != ProviderDefinitionCollectionVersion {
		return errors.New("custom Provider definition collection version is invalid")
	}
	if collection.Revision > maxProviderCollectionRevision ||
		len(collection.Providers) > maxProviderDefinitions {
		return errors.New("custom Provider definition collection is out of range")
	}
	if collection.Revision == 0 && len(collection.Providers) != 0 {
		return errors.New("unrevisioned custom Provider collection must be empty")
	}
	seen := make(map[string]struct{}, len(collection.Providers))
	previous := ""
	for _, definition := range collection.Providers {
		if err := definition.Validate(); err != nil {
			return err
		}
		if definition.Revision == 0 {
			return errors.New("persisted custom Provider definition revision is required")
		}
		if _, duplicate := seen[definition.ID]; duplicate {
			return errors.New("custom Provider definition ids must be unique")
		}
		if previous != "" && definition.ID <= previous {
			return errors.New("custom Provider definitions must be sorted by id")
		}
		seen[definition.ID] = struct{}{}
		previous = definition.ID
	}
	return nil
}

func EncodeProviderDefinitionCollection(collection ProviderDefinitionCollection) (string, error) {
	if collection.Providers == nil {
		collection.Providers = []ProviderDefinition{}
	}
	for index := range collection.Providers {
		normalized, err := NormalizeProviderDefinition(collection.Providers[index])
		if err != nil {
			return "", err
		}
		collection.Providers[index] = normalized
	}
	sort.Slice(collection.Providers, func(i, j int) bool {
		return collection.Providers[i].ID < collection.Providers[j].ID
	})
	if err := collection.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(collection)
	if err != nil {
		return "", err
	}
	if len(encoded) > maxProviderDefinitionBytes {
		return "", errors.New("custom Provider definition collection is too large")
	}
	return string(encoded), nil
}

func NormalizeProviderDefinition(definition ProviderDefinition) (ProviderDefinition, error) {
	advanced, err := ValidateAndNormalizeProviderAdvancedConfig(definition.AdvancedConfig,
		definition.ID)
	if err != nil {
		return ProviderDefinition{}, err
	}
	advanced, err = providerCompatibilityDefaults(definition, advanced)
	if err != nil {
		return ProviderDefinition{}, err
	}
	definition.AdvancedConfig = advanced
	definition.Models = append([]string(nil), definition.Models...)
	return definition, nil
}

// providerCompatibilityDefaults keeps an existing saved DeepSeek Responses
// definition usable with a multi-round tool loop. DeepSeek's Responses API is
// stateless and enables private reasoning by default; a continuation that omits
// the private reasoning item is rejected after tool results. Traverse does not
// persist or expose chain-of-thought, so the safe compatibility default is
// explicit non-thinking mode. An operator-owned request_body.reasoning value is
// never overwritten and remains visible/editable in Advanced JSON.
func providerCompatibilityDefaults(definition ProviderDefinition,
	advanced json.RawMessage,
) (json.RawMessage, error) {
	if definition.Transport != ProviderTransportOpenAIResponses {
		return advanced, nil
	}
	endpoint, err := url.Parse(definition.EndpointURL)
	if err != nil || !strings.EqualFold(endpoint.Hostname(), "api.deepseek.com") {
		return advanced, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(advanced))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil || root == nil {
		return nil, errors.New("DeepSeek Responses compatibility config is invalid")
	}
	requestBody, found := root["request_body"]
	if !found {
		requestBody = map[string]any{}
		root["request_body"] = requestBody
	}
	body, ok := requestBody.(map[string]any)
	if !ok {
		return nil, errors.New("DeepSeek Responses request body config is invalid")
	}
	if _, explicitlyConfigured := body["reasoning"]; explicitlyConfigured {
		return advanced, nil
	}
	body["reasoning"] = map[string]any{"effort": "none"}
	encoded, err := json.Marshal(root)
	if err != nil {
		return nil, errors.New("DeepSeek Responses compatibility config could not be encoded")
	}
	return ValidateAndNormalizeProviderAdvancedConfig(encoded, definition.ID)
}

func DecodeProviderDefinitionCollection(value string) (ProviderDefinitionCollection, error) {
	if value == "" || len([]byte(value)) > maxProviderDefinitionBytes || !utf8.ValidString(value) {
		return ProviderDefinitionCollection{}, errors.New("custom Provider definition collection is invalid")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(value))
	decoder.DisallowUnknownFields()
	var collection ProviderDefinitionCollection
	if err := decoder.Decode(&collection); err != nil {
		return ProviderDefinitionCollection{}, errors.New("custom Provider definition collection is malformed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ProviderDefinitionCollection{}, errors.New("custom Provider definition collection contains trailing data")
	}
	if collection.Providers == nil {
		return ProviderDefinitionCollection{}, errors.New("custom Provider definition providers must be an array")
	}
	for index := range collection.Providers {
		normalized, err := NormalizeProviderDefinition(collection.Providers[index])
		if err != nil {
			return ProviderDefinitionCollection{}, err
		}
		collection.Providers[index] = normalized
	}
	if err := collection.Validate(); err != nil {
		return ProviderDefinitionCollection{}, err
	}
	return collection, nil
}

func ReadProviderDefinitions(ctx context.Context,
	reader RouteSettingReader,
) (ProviderDefinitionCollection, error) {
	if ctx == nil || reader == nil {
		return ProviderDefinitionCollection{}, errors.New("custom Provider definition reader is required")
	}
	value, found, err := reader.GetProviderSetting(ctx, ProviderDefinitionsSettingKey)
	if err != nil {
		return ProviderDefinitionCollection{}, err
	}
	if !found {
		return EmptyProviderDefinitionCollection(), nil
	}
	return DecodeProviderDefinitionCollection(value)
}

func validCustomProviderID(value string) bool {
	if !credential.ValidName(value) || value != strings.ToLower(value) {
		return false
	}
	if _, reserved := reservedProviderIDs[value]; reserved {
		return false
	}
	if strings.HasPrefix(value, "chatgpt-") {
		return false
	}
	for index, current := range value {
		if index == 0 && !(current >= 'a' && current <= 'z') {
			return false
		}
		if !(current >= 'a' && current <= 'z') && !(current >= '0' && current <= '9') &&
			current != '-' && current != '_' {
			return false
		}
	}
	return true
}

func ValidCustomProviderID(value string) bool {
	return validCustomProviderID(value)
}

func validPublicProviderText(value string, maximumBytes int, required bool) bool {
	if value != strings.TrimSpace(value) || !utf8.ValidString(value) ||
		len([]byte(value)) > maximumBytes || redact.String(value) != value {
		return false
	}
	if required && value == "" {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) && current != '\n' && current != '\t' {
			return false
		}
	}
	return true
}

func validateDefinitionURL(value string, website bool, maximumBytes int) error {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) ||
		len([]byte(value)) > maximumBytes || redact.String(value) != value {
		return errors.New("URL is missing, unbounded, or contains credential material")
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed == nil || parsed.IsAbs() == false || parsed.Host == "" ||
		parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("URL must be an absolute credential-free URL without query or fragment")
	}
	if website {
		if parsed.Scheme != "https" {
			return errors.New("website URL must use HTTPS")
		}
	} else if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return errors.New("endpoint URL must use HTTP(S)")
	}
	hostname := parsed.Hostname()
	if hostname == "" || strings.ContainsAny(hostname, " \\/?#@") {
		return errors.New("URL hostname is invalid")
	}
	for _, current := range hostname {
		if current > unicode.MaxASCII || unicode.IsControl(current) || unicode.IsSpace(current) {
			return errors.New("URL hostname must be normalized ASCII")
		}
	}
	if parsed.Scheme == "http" && !definitionLoopbackHost(hostname) {
		return errors.New("endpoint URL must use HTTPS outside loopback")
	}
	if strings.Contains(parsed.Host, ":") {
		if port := parsed.Port(); port == "" && !strings.HasSuffix(parsed.Host, "]") {
			return errors.New("URL port is invalid")
		}
	}
	return nil
}

func definitionLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
