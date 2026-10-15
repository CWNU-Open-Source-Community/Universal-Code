package modelregistry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/redact"
)

const ProtocolVersion = "model_availability.v2"

const ReloadProtocolVersion = "provider_registry_reload.v1"

const (
	DiagnosticProtocolVersion   = "provider_diagnostic.v1"
	RouteControlProtocolVersion = "model_route_control.v1"
	DiagnosticTimeout           = 15 * time.Second
	diagnosticMaxTokens         = 128
)

const (
	ProviderAvailable            = "available"
	ProviderNotConfigured        = "not_configured"
	ProviderInvalidConfiguration = "invalid_configuration"
)

const (
	ProviderKindLocal               = "local"
	ProviderKindAnthropicCompatible = "anthropic_compatible"
	ProviderKindOpenAICompatible    = "openai_compatible"
	ProviderKindOllama              = "ollama"
)

const (
	DiagnosticReachable   = "reachable"
	DiagnosticUnreachable = "unreachable"
)

const (
	defaultMimoBaseURL     = "https://token-plan-cn.xiaomimimo.com/anthropic"
	DefaultMimoModel       = "mimo-v2.5-pro"
	defaultDeepSeekBaseURL = "https://api.deepseek.com/anthropic"
	DefaultDeepSeekModel   = "deepseek-v4-flash"
	defaultAnthropicURL    = "https://api.anthropic.com"
	DefaultAnthropicModel  = "claude-3-5-sonnet-latest"
	defaultOpenAIBaseURL   = "https://api.openai.com"
	DefaultOpenAIModel     = "gpt-4.1-mini"
	DefaultOpenAITimeout   = llm.DefaultProviderRequestTimeout
	defaultOllamaBaseURL   = llm.OllamaDefaultBaseURL
	ollamaProbeTimeout     = 5 * time.Second
)

var routeNames = []string{"code", "ctf", "learn", "review", "script"}

const (
	maxPublicProviderNameBytes = 128
	maxPublicModelNameBytes    = 256
)

type ProviderAvailability struct {
	Name                          string
	DisplayName                   string
	Kind                          string
	Status                        string
	Models                        []string
	Harnesses                     []HarnessAvailability
	CredentialSource              string
	NetworkRequired               bool
	ConfigurationError            bool
	Custom                        bool
	Enabled                       bool
	DefinitionRevision            uint64
	Transport                     string
	SearchMode                    string
	NativeWebSearchCapability     string
	NativeWebSearchRuntimeEnabled bool
}

type RouteAvailability struct {
	Name         string
	Provider     string
	Model        string
	Available    bool
	HarnessReady bool
}

type Snapshot struct {
	ProtocolVersion string
	Generation      uint64
	Providers       []ProviderAvailability
	Routes          []RouteAvailability
}

type ReloadResult struct {
	ProtocolVersion string
	Generation      uint64
	Reloaded        bool
}

// DiagnosticResult is intentionally content-free. A diagnostic may make one
// minimal model request, but neither model text nor a raw Provider error crosses
// this boundary.
type DiagnosticResult struct {
	ProtocolVersion         string
	Provider                string
	Model                   string
	Status                  string
	Outcome                 string
	FailureReason           llm.ProviderFailureReason
	Retryable               bool
	NetworkRequestAttempted bool
	ModelCalled             bool
	ToolCalled              bool
	ResponseContentReturned bool
	QualificationStatus     string
	DurationMillis          int64
}

type RouteSettingReader interface {
	GetProviderSetting(ctx context.Context, key string) (string, bool, error)
}

type RouteSettingWriter interface {
	SetProviderSetting(ctx context.Context, key string, value string) error
}

type RouteSettingStore interface {
	RouteSettingReader
	RouteSettingWriter
}

type EnvironmentLookup func(string) (string, bool)

type CredentialReader interface {
	Get(context.Context, string) (string, bool, error)
}

type Registry struct {
	mu                      sync.RWMutex
	routeMu                 sync.Mutex
	qualificationMu         sync.Mutex
	router                  *llm.Router
	providers               []ProviderAvailability
	available               map[string]struct{}
	lookup                  EnvironmentLookup
	credentials             credentialLookup
	generation              uint64
	ollama                  *llm.OllamaProvider
	strictCredentialReads   bool
	customDefinitionsLoaded bool
	chatGPT                 *chatGPTSource
	chatGPTLoaded           bool
	qualificationStatuses   map[string]persistedQualificationStatus
	credentialRevisions     map[string]uint64
	geminiSequentialProbes  map[llm.ModelRef]bool
	kimiHistoryProbes       map[llm.ModelRef]bool
}

type anthropicEnvironment struct {
	name            string
	apiKeyEnv       string
	baseURLEnv      string
	modelEnv        string
	timeoutEnv      string
	defaultBaseURL  string
	defaultModel    string
	disableThinking bool
}

type openAIEnvironment struct {
	name           string
	apiKeyEnv      string
	baseURLEnv     string
	modelEnv       string
	timeoutEnv     string
	defaultBaseURL string
	defaultModel   string
}

type ollamaEnvironment struct {
	name           string
	baseURLEnv     string
	modelEnv       string
	timeoutEnv     string
	defaultBaseURL string
}

func NewFromEnvironment() *Registry {
	return New(os.LookupEnv)
}

// NewFromEnvironmentWithCredentials preserves environment-variable priority
// while allowing the Go control plane to bootstrap secrets from an OS-owned
// credential store. No credential is copied into Registry snapshots.
func NewFromEnvironmentWithCredentials(reader CredentialReader) (*Registry, error) {
	return newRegistry(os.LookupEnv, func(ctx context.Context, provider string) (string, bool, error) {
		if reader == nil {
			return "", false, nil
		}
		return reader.Get(ctx, provider)
	})
}

func New(lookup EnvironmentLookup) *Registry {
	registry, _ := newRegistry(lookup, nil)
	return registry
}

type credentialLookup func(context.Context, string) (string, bool, error)

func newRegistry(lookup EnvironmentLookup,
	credentials credentialLookup,
) (*Registry, error) {
	return buildRegistry(context.Background(), lookup, credentials, false)
}

func buildRegistry(ctx context.Context, lookup EnvironmentLookup,
	credentials credentialLookup, strictCredentialReads bool,
) (*Registry, error) {
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}
	if ctx == nil {
		return nil, errors.New("model registry context is required")
	}
	router := llm.NewDefaultRouter()
	registry := &Registry{
		router: router,
		providers: []ProviderAvailability{{
			Name: "mock", Kind: ProviderKindLocal, Status: ProviderAvailable,
			Models:           []string{"mock-code", "mock-cyber-agent", "mock-fast"},
			CredentialSource: "none", NetworkRequired: false,
		}},
		available: map[string]struct{}{"mock": {}}, lookup: lookup,
		credentials: credentials, generation: 1,
		strictCredentialReads:  strictCredentialReads,
		qualificationStatuses:  make(map[string]persistedQualificationStatus),
		credentialRevisions:    make(map[string]uint64),
		geminiSequentialProbes: make(map[llm.ModelRef]bool),
		kimiHistoryProbes:      make(map[llm.ModelRef]bool),
	}
	configs := []anthropicEnvironment{
		{name: "mimo", apiKeyEnv: "MIMO_API_KEY", baseURLEnv: "MIMO_BASE_URL",
			modelEnv: "MIMO_MODEL", timeoutEnv: "MIMO_TIMEOUT_SECONDS", defaultBaseURL: defaultMimoBaseURL,
			defaultModel: DefaultMimoModel},
		{name: "deepseek", apiKeyEnv: "DEEPSEEK_API_KEY", baseURLEnv: "DEEPSEEK_BASE_URL",
			modelEnv: "DEEPSEEK_MODEL", timeoutEnv: "DEEPSEEK_TIMEOUT_SECONDS", defaultBaseURL: defaultDeepSeekBaseURL,
			defaultModel: DefaultDeepSeekModel, disableThinking: true},
		{name: "anthropic", apiKeyEnv: "CYBERAGENT_ANTHROPIC_API_KEY",
			baseURLEnv: "CYBERAGENT_ANTHROPIC_BASE_URL", modelEnv: "CYBERAGENT_ANTHROPIC_MODEL",
			timeoutEnv:     "CYBERAGENT_ANTHROPIC_TIMEOUT_SECONDS",
			defaultBaseURL: defaultAnthropicURL, defaultModel: DefaultAnthropicModel},
	}
	for _, config := range configs {
		if err := registry.registerAnthropicEnvironment(ctx, config, lookup,
			credentials, strictCredentialReads); err != nil {
			return nil, err
		}
	}
	if err := registry.registerOpenAIEnvironment(ctx, openAIEnvironment{
		name: "openai", apiKeyEnv: "CYBERAGENT_OPENAI_API_KEY",
		baseURLEnv: "CYBERAGENT_OPENAI_BASE_URL", modelEnv: "CYBERAGENT_OPENAI_MODEL",
		timeoutEnv:     "CYBERAGENT_OPENAI_TIMEOUT_SECONDS",
		defaultBaseURL: defaultOpenAIBaseURL, defaultModel: DefaultOpenAIModel,
	}, lookup, credentials, strictCredentialReads); err != nil {
		return nil, err
	}
	if err := registry.registerOllamaEnvironment(ctx, ollamaEnvironment{
		name: "ollama", baseURLEnv: "CYBERAGENT_OLLAMA_BASE_URL",
		modelEnv: "CYBERAGENT_OLLAMA_MODEL", defaultBaseURL: defaultOllamaBaseURL,
		timeoutEnv: "CYBERAGENT_OLLAMA_TIMEOUT_SECONDS",
	}, lookup); err != nil {
		return nil, err
	}
	sort.Slice(registry.providers, func(i, j int) bool {
		return registry.providers[i].Name < registry.providers[j].Name
	})
	return registry, nil
}

// Reload builds a complete candidate generation before taking the public
// snapshot lock. The final Router replacement is atomic; Provider values
// already captured by active calls remain valid and are never cancelled.
func (r *Registry) Reload(ctx context.Context, reader RouteSettingReader) (ReloadResult, error) {
	if r == nil || r.router == nil {
		return ReloadResult{}, errors.New("model registry is unavailable")
	}
	if ctx == nil || reader == nil {
		return ReloadResult{}, errors.New("model registry reload dependencies are required")
	}
	if err := ctx.Err(); err != nil {
		return ReloadResult{}, err
	}
	r.qualificationMu.Lock()
	defer r.qualificationMu.Unlock()
	r.routeMu.Lock()
	defer r.routeMu.Unlock()
	return r.reloadLocked(ctx, reader)
}

// MutateProviderDefinitions keeps definition persistence and credential cleanup
// serialized with credential changes, route selection, and Registry reload.
// Attempt reload even after an error to reflect the durable state when
// credential cleanup or definition compensation failed.
func (r *Registry) MutateProviderDefinitions(ctx context.Context, reader RouteSettingReader,
	mutation func() error,
) (ReloadResult, error) {
	if r == nil || r.router == nil || ctx == nil || reader == nil || mutation == nil {
		return ReloadResult{}, errors.New("Provider definition mutation dependencies are required")
	}
	if err := ctx.Err(); err != nil {
		return ReloadResult{}, err
	}
	r.qualificationMu.Lock()
	defer r.qualificationMu.Unlock()
	r.routeMu.Lock()
	defer r.routeMu.Unlock()
	mutationErr := mutation()
	reloadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	result, reloadErr := r.reloadLocked(reloadCtx, reader)
	return result, errors.Join(mutationErr, reloadErr)
}

func (r *Registry) reloadLocked(ctx context.Context, reader RouteSettingReader) (ReloadResult, error) {
	candidate, err := buildRegistry(ctx, r.lookup, r.credentials, true)
	if err != nil {
		return ReloadResult{}, err
	}
	candidate.chatGPT = r.chatGPT
	if err := candidate.LoadRouteSettings(ctx, reader); err != nil {
		return ReloadResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ReloadResult{}, err
	}

	providers := make([]ProviderAvailability, len(candidate.providers))
	copy(providers, candidate.providers)
	for index := range providers {
		providers[index].Models = append([]string(nil), candidate.providers[index].Models...)
		providers[index].Harnesses = append([]HarnessAvailability(nil),
			candidate.providers[index].Harnesses...)
	}
	available := make(map[string]struct{}, len(candidate.available))
	for name := range candidate.available {
		available[name] = struct{}{}
	}
	qualificationStatuses := make(map[string]persistedQualificationStatus,
		len(candidate.qualificationStatuses))
	for key, status := range candidate.qualificationStatuses {
		qualificationStatuses[key] = status
	}
	credentialRevisions := make(map[string]uint64, len(candidate.credentialRevisions))
	for key, revision := range candidate.credentialRevisions {
		credentialRevisions[key] = revision
	}
	r.mu.Lock()
	if err := r.router.ReplaceConfiguration(candidate.router); err != nil {
		r.mu.Unlock()
		return ReloadResult{}, err
	}
	r.providers = providers
	r.available = available
	r.qualificationStatuses = qualificationStatuses
	r.credentialRevisions = credentialRevisions
	r.geminiSequentialProbes = candidate.geminiSequentialProbes
	r.kimiHistoryProbes = candidate.kimiHistoryProbes
	r.ollama = candidate.ollama
	r.customDefinitionsLoaded = candidate.customDefinitionsLoaded
	r.chatGPTLoaded = candidate.chatGPTLoaded
	r.strictCredentialReads = candidate.strictCredentialReads
	r.generation++
	generation := r.generation
	r.mu.Unlock()
	return ReloadResult{ProtocolVersion: ReloadProtocolVersion,
		Generation: generation, Reloaded: true}, nil
}

func (r *Registry) Generation() uint64 {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.generation
}

func (r *Registry) Router() *llm.Router {
	if r == nil {
		return nil
	}
	return r.router
}

func (r *Registry) LoadRouteSettings(ctx context.Context, reader RouteSettingReader) error {
	if r == nil || r.router == nil {
		return errors.New("model registry is unavailable")
	}
	if reader == nil {
		return errors.New("model route setting reader is required")
	}
	if !r.chatGPTLoaded {
		if err := r.loadChatGPTProviders(ctx); err != nil {
			return err
		}
		r.chatGPTLoaded = true
	}
	if !r.customDefinitionsLoaded {
		if err := r.loadCustomProviderDefinitions(ctx, reader); err != nil {
			return err
		}
		r.customDefinitionsLoaded = true
		sort.Slice(r.providers, func(i, j int) bool {
			return r.providers[i].Name < r.providers[j].Name
		})
	}
	for _, route := range routeNames {
		value, found, err := reader.GetProviderSetting(ctx, "route."+route)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		ref, err := llm.ParseModelRef(value)
		if err != nil || strings.TrimSpace(ref.Provider) != ref.Provider ||
			strings.TrimSpace(ref.Model) != ref.Model ||
			!validAvailabilityIdentifier(ref.Provider, maxPublicProviderNameBytes) ||
			!validAvailabilityIdentifier(ref.Model, maxPublicModelNameBytes) {
			continue
		}
		r.router.SetRoute(route, ref)
	}
	credentialRevisions, err := r.loadCredentialRevisions(ctx, reader)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.credentialRevisions = credentialRevisions
	r.mu.Unlock()
	if err := r.loadHarnessQualifications(ctx, reader); err != nil {
		return err
	}
	statuses := r.loadQualificationStatuses(ctx, reader)
	r.mu.Lock()
	r.qualificationStatuses = statuses
	r.mu.Unlock()
	return nil
}

func (r *Registry) SelectRoute(ctx context.Context, writer RouteSettingWriter,
	route string, provider string, model string,
) (RouteAvailability, error) {
	if r == nil || r.router == nil {
		return RouteAvailability{}, errors.New("model registry is unavailable")
	}
	if writer == nil {
		return RouteAvailability{}, errors.New("model route setting writer is required")
	}
	r.routeMu.Lock()
	defer r.routeMu.Unlock()
	route = strings.TrimSpace(route)
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if !containsRoute(route) {
		return RouteAvailability{}, errors.New("model route is not supported")
	}
	if !validAvailabilityIdentifier(provider, maxPublicProviderNameBytes) ||
		!validAvailabilityIdentifier(model, maxPublicModelNameBytes) ||
		!r.providerModelAvailable(provider, model) {
		return RouteAvailability{}, errors.New("selected Provider model is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return RouteAvailability{}, err
	}
	r.probeOllamaCapabilities(ctx, provider, model)
	value := provider + "/" + model
	if err := writer.SetProviderSetting(ctx, "route."+route, value); err != nil {
		return RouteAvailability{}, fmt.Errorf("persist model route: %w", err)
	}
	// SetRoute cannot fail after the exact Provider/model validation above. If
	// the process exits between persistence and this update, startup reloads the
	// durable setting before serving requests.
	r.router.SetRoute(route, llm.ModelRef{Provider: provider, Model: model})
	profile, _ := r.router.HarnessProfile(llm.ModelRef{Provider: provider, Model: model})
	return RouteAvailability{Name: route, Provider: provider, Model: model, Available: true,
		HarnessReady: rootHarnessReady(profile)}, nil
}

func (r *Registry) Diagnose(ctx context.Context, provider string,
	model string,
) (DiagnosticResult, error) {
	if r == nil || r.router == nil {
		return DiagnosticResult{}, errors.New("model registry is unavailable")
	}
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if !validAvailabilityIdentifier(provider, maxPublicProviderNameBytes) ||
		!validAvailabilityIdentifier(model, maxPublicModelNameBytes) {
		return DiagnosticResult{}, errors.New("diagnostic Provider model is unavailable")
	}
	r.probeOllamaCapabilities(ctx, provider, model)
	providerStatus, _, known := r.providerModelStatus(provider, model)
	if !known {
		return DiagnosticResult{}, errors.New("diagnostic Provider model is unavailable")
	}
	if providerStatus == ProviderNotConfigured {
		return DiagnosticResult{
			ProtocolVersion: DiagnosticProtocolVersion,
			Provider:        provider, Model: model, Status: DiagnosticUnreachable,
			Outcome:             string(llm.OutcomePermanent),
			FailureReason:       llm.ProviderFailureNotConfigured,
			QualificationStatus: QualificationStatusNotConfigured,
		}, nil
	}
	if providerStatus != ProviderAvailable {
		return DiagnosticResult{}, errors.New("diagnostic Provider model is unavailable")
	}
	networkRequired := r.providerNetworkRequired(provider)
	diagnosticCtx, cancel := context.WithTimeout(ctx, DiagnosticTimeout)
	defer cancel()
	started := time.Now()
	result := DiagnosticResult{
		ProtocolVersion: DiagnosticProtocolVersion,
		Provider:        provider, Model: model, Status: DiagnosticUnreachable,
		FailureReason:           llm.ProviderFailureNone,
		NetworkRequestAttempted: networkRequired, ModelCalled: true,
		ToolCalled: false, ResponseContentReturned: false,
	}
	response, err := r.router.ChatModelRef(diagnosticCtx,
		llm.ModelRef{Provider: provider, Model: model}, llm.ChatRequest{
			Model: model,
			Messages: []llm.Message{{Role: "user",
				Content: "Reply with one short acknowledgement for a connectivity diagnostic."}},
			Temperature: 0, MaxTokens: diagnosticMaxTokens,
			Metadata: map[string]string{"purpose": "connectivity_diagnostic"},
		})
	result.DurationMillis = time.Since(started).Milliseconds()
	if result.DurationMillis < 0 {
		result.DurationMillis = 0
	}
	if err != nil {
		providerErr := llm.NormalizeProviderError(provider, err)
		outcome := llm.ProviderErrorKind(providerErr)
		if !outcome.Valid() || outcome == llm.OutcomeSuccess {
			outcome = llm.OutcomePermanent
		}
		result.Outcome = string(outcome)
		result.FailureReason = providerErr.Reason
		result.Retryable = outcome.Retryable()
		result.QualificationStatus = QualificationStatusFor(outcome, providerErr.Reason)
		return result, nil
	}
	if response == nil || strings.TrimSpace(response.Provider) != provider {
		result.Outcome = string(llm.OutcomeInvalidResponse)
		result.FailureReason = llm.ProviderFailureProtocolIncompatible
		result.QualificationStatus = QualificationStatusProtocolMismatch
		return result, nil
	}
	result.Status = DiagnosticReachable
	result.Outcome = string(llm.OutcomeSuccess)
	result.FailureReason = llm.ProviderFailureNone
	result.QualificationStatus = QualificationStatusAvailable
	return result, nil
}

// DiagnoseAndRecord serializes the provider call and its durable projection
// with credential changes, Harness qualification, and Registry reload. This
// prevents a slow response from an older credential or definition generation
// from being published as the status of the current provider binding.
func (r *Registry) DiagnoseAndRecord(ctx context.Context, writer RouteSettingWriter,
	provider string, model string,
) (DiagnosticResult, error) {
	if r == nil || writer == nil {
		return DiagnosticResult{}, errors.New("diagnostic persistence dependencies are required")
	}
	r.qualificationMu.Lock()
	defer r.qualificationMu.Unlock()
	result, err := r.Diagnose(ctx, provider, model)
	if err != nil {
		return DiagnosticResult{}, err
	}
	if result.QualificationStatus == "" {
		return result, nil
	}
	if err := r.persistQualificationStatus(ctx, writer, provider, model,
		result.QualificationStatus, qualificationStatusSourceDiagnostic); err != nil {
		return DiagnosticResult{}, fmt.Errorf("persist Provider diagnostic status: %w", err)
	}
	return result, nil
}

func (r *Registry) Snapshot() Snapshot {
	if r == nil || r.router == nil {
		return Snapshot{ProtocolVersion: ProtocolVersion, Providers: []ProviderAvailability{},
			Routes: []RouteAvailability{}}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	providers := make([]ProviderAvailability, len(r.providers))
	for index, provider := range r.providers {
		providers[index] = provider
		if !provider.Custom {
			if providers[index].DisplayName == "" {
				providers[index].DisplayName = provider.Name
			}
			providers[index].Enabled = true
			providers[index].SearchMode = ProviderSearchModeDisabled
			providers[index].NativeWebSearchCapability = NativeWebSearchUnsupported
			switch provider.Kind {
			case ProviderKindAnthropicCompatible:
				providers[index].Transport = llm.HarnessTransportAnthropicMessages
			case ProviderKindOpenAICompatible:
				if provider.Transport == "" {
					providers[index].Transport = llm.HarnessTransportOpenAIChatCompletions
				}
			case ProviderKindOllama:
				providers[index].Transport = llm.HarnessTransportOllamaChat
			case ProviderKindLocal:
				providers[index].Transport = llm.HarnessTransportMock
			}
		}
		providers[index].Models = make([]string, len(provider.Models))
		configuredHarnesses := append([]HarnessAvailability(nil), provider.Harnesses...)
		providers[index].Harnesses = make([]HarnessAvailability, 0, len(provider.Models))
		for modelIndex, model := range provider.Models {
			providers[index].Models[modelIndex] = availabilityIdentifier(model,
				maxPublicModelNameBytes)
			if profile, err := r.router.HarnessProfile(llm.ModelRef{
				Provider: provider.Name, Model: model,
			}); err == nil {
				providers[index].Harnesses = append(providers[index].Harnesses,
					harnessAvailability(model, profile))
			} else if fallback, found := harnessForModel(configuredHarnesses, model); found {
				providers[index].Harnesses = append(providers[index].Harnesses, fallback)
			}
		}
		for harnessIndex := range providers[index].Harnesses {
			key := provider.Name + "." + providers[index].Harnesses[harnessIndex].Model
			if status, ok := r.qualificationStatuses[key]; ok {
				providers[index].Harnesses[harnessIndex].LatestQualificationStatus = status.Status
				providers[index].Harnesses[harnessIndex].QualificationCheckedAt = status.CheckedAt
				providers[index].Harnesses[harnessIndex].QualificationSource =
					normalizeQualificationStatusSource(status.Source)
			} else if provider.Status == ProviderNotConfigured {
				providers[index].Harnesses[harnessIndex].LatestQualificationStatus =
					QualificationStatusNotConfigured
				providers[index].Harnesses[harnessIndex].QualificationSource =
					qualificationStatusSourceAvailability
			}
		}
	}
	available := make(map[string]struct{}, len(r.available))
	for name := range r.available {
		available[name] = struct{}{}
	}
	routes := r.router.Routes()
	outRoutes := make([]RouteAvailability, 0, len(routeNames))
	for _, name := range routeNames {
		ref := routes[name]
		_, registered := available[ref.Provider]
		provider := availabilityIdentifier(ref.Provider, maxPublicProviderNameBytes)
		model := availabilityIdentifier(ref.Model, maxPublicModelNameBytes)
		profile, profileErr := r.router.HarnessProfile(ref)
		outRoutes = append(outRoutes, RouteAvailability{
			Name: name, Provider: provider, Model: model,
			Available:    registered && provider == ref.Provider && model == ref.Model,
			HarnessReady: profileErr == nil && rootHarnessReady(profile),
		})
	}
	return Snapshot{ProtocolVersion: ProtocolVersion, Generation: r.generation,
		Providers: providers, Routes: outRoutes}
}

func (r *Registry) providerModelAvailable(provider string, model string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, ok := r.available[provider]; !ok {
		return false
	}
	for _, current := range r.providers {
		if current.Name != provider || current.Status != ProviderAvailable {
			continue
		}
		for _, candidate := range current.Models {
			if candidate == model {
				return true
			}
		}
	}
	return false
}

func (r *Registry) providerNetworkRequired(provider string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, current := range r.providers {
		if current.Name == provider {
			return current.NetworkRequired
		}
	}
	return false
}

func (r *Registry) providerModelStatus(provider string, model string) (
	string, HarnessAvailability, bool,
) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, current := range r.providers {
		if current.Name != provider {
			continue
		}
		for _, candidate := range current.Models {
			if candidate == model {
				harness, _ := harnessForModel(current.Harnesses, model)
				return current.Status, harness, true
			}
		}
	}
	return "", HarnessAvailability{}, false
}

func containsRoute(route string) bool {
	for _, current := range routeNames {
		if current == route {
			return true
		}
	}
	return false
}

// SupportedRouteNames returns a copy for mutation services that must prevent a
// custom Provider from being removed while a durable route still references
// it.
func SupportedRouteNames() []string {
	return append([]string(nil), routeNames...)
}

func (r *Registry) loadCustomProviderDefinitions(ctx context.Context,
	reader RouteSettingReader,
) error {
	collection, err := ReadProviderDefinitions(ctx, reader)
	if err != nil {
		return fmt.Errorf("load custom Provider definitions: %w", err)
	}
	for _, definition := range collection.Providers {
		if err := r.registerCustomProvider(ctx, definition); err != nil {
			return err
		}
	}
	return nil
}

func (r *Registry) registerCustomProvider(ctx context.Context,
	definition ProviderDefinition,
) error {
	kind := ProviderKindOpenAICompatible
	jsonStrategy := llm.HarnessJSONStrategyNative
	if definition.Transport == ProviderTransportAnthropicMessages {
		kind = ProviderKindAnthropicCompatible
		jsonStrategy = llm.HarnessJSONStrategyPrompt
	}
	harnesses := make([]HarnessAvailability, 0, len(definition.Models))
	for _, model := range definition.Models {
		harnesses = append(harnesses, unqualifiedHarnessAvailability(model,
			definition.Transport, jsonStrategy))
	}
	availability := ProviderAvailability{
		Name: definition.ID, DisplayName: definition.DisplayName,
		Kind: kind, Status: ProviderNotConfigured,
		Models: append([]string(nil), definition.Models...), Harnesses: harnesses,
		CredentialSource: "system", NetworkRequired: true,
		Custom: true, Enabled: definition.Enabled,
		DefinitionRevision: definition.Revision, Transport: definition.Transport,
		SearchMode:                definition.SearchMode,
		NativeWebSearchCapability: definition.NativeWebSearchCapability,
		// Hosted search is deliberately not installed by a definition. A future
		// runtime qualification must set this from observed behavior, never from
		// the user-writable declaration.
		NativeWebSearchRuntimeEnabled: false,
	}
	if ProviderNativeWebSearchKnownUnsupported(definition) {
		availability.NativeWebSearchCapability = NativeWebSearchUnsupported
	}
	if !definition.Enabled {
		r.providers = append(r.providers, availability)
		return nil
	}
	key := ""
	present := false
	if r.credentials != nil {
		var err error
		key, present, err = r.credentials(ctx, definition.ID)
		if err != nil {
			if r.strictCredentialReads {
				return fmt.Errorf("read system credential for custom Provider %s: %w",
					definition.ID, err)
			}
			availability.Status = ProviderInvalidConfiguration
			availability.ConfigurationError = true
			r.providers = append(r.providers, availability)
			return nil
		}
	}
	runtime, runtimeErr := newProviderRequestRuntime(definition, r.credentials)
	if runtimeErr != nil {
		availability.Status = ProviderInvalidConfiguration
		availability.ConfigurationError = true
		r.providers = append(r.providers, availability)
		return nil
	}
	if !present {
		if !runtime.AllowsKeylessEndpoint(definition.EndpointURL) {
			r.providers = append(r.providers, availability)
			return nil
		}
		availability.CredentialSource = "none"
	} else if key == "" || key != strings.TrimSpace(key) {
		availability.Status = ProviderInvalidConfiguration
		availability.ConfigurationError = true
		r.providers = append(r.providers, availability)
		return nil
	}
	var provider llm.Provider
	var err error
	switch definition.Transport {
	case ProviderTransportOpenAIChatCompletions:
		provider, err = llm.NewOpenAICompatibleProvider(llm.OpenAICompatibleConfig{
			Name: definition.ID, BaseURL: definition.EndpointURL,
			DefaultModel: definition.DefaultModel,
			HTTPClient:   &http.Client{Timeout: runtime.requestTimeout}, Runtime: runtime,
		})
	case ProviderTransportOpenAIResponses:
		provider, err = llm.NewOpenAIResponsesProvider(llm.OpenAIResponsesConfig{
			Name: definition.ID, BaseURL: definition.EndpointURL,
			DefaultModel: definition.DefaultModel,
			HTTPClient:   &http.Client{Timeout: runtime.requestTimeout}, Runtime: runtime,
		})
	case ProviderTransportAnthropicMessages:
		provider, err = llm.NewAnthropicCompatibleProvider(llm.AnthropicCompatibleConfig{
			Name: definition.ID, BaseURL: definition.EndpointURL,
			DefaultModel: definition.DefaultModel,
			HTTPClient:   &http.Client{Timeout: runtime.requestTimeout}, Runtime: runtime,
		})
	default:
		err = errors.New("unsupported custom Provider transport")
	}
	if err != nil {
		availability.Status = ProviderInvalidConfiguration
		availability.ConfigurationError = true
	} else {
		availability.Status = ProviderAvailable
		r.router.RegisterProvider(provider)
		r.available[definition.ID] = struct{}{}
		if definition.Transport == ProviderTransportOpenAIChatCompletions {
			r.freezeGeminiProbe(definition.ID, definition.EndpointURL, definition.Models, runtime)
			r.freezeKimiProbe(definition.ID, definition.EndpointURL, definition.Models, runtime)
		}
	}
	r.providers = append(r.providers, availability)
	return nil
}

func (r *Registry) registerAnthropicEnvironment(ctx context.Context, config anthropicEnvironment,
	lookup EnvironmentLookup, credentials credentialLookup, strictCredentialReads bool,
) error {
	requestTimeout, timeoutErr := environmentRequestTimeout(lookup, config.timeoutEnv)
	key, present := lookup(config.apiKeyEnv)
	credentialSource := "none"
	if present {
		credentialSource = "environment"
	}
	if !present && credentials != nil {
		var err error
		key, present, err = credentials(ctx, config.name)
		if err != nil {
			if strictCredentialReads {
				return fmt.Errorf("read system credential for %s: %w", config.name, err)
			}
			r.providers = append(r.providers, ProviderAvailability{
				Name: config.name, Kind: ProviderKindAnthropicCompatible,
				Status: ProviderInvalidConfiguration, Models: []string{},
				CredentialSource: "system", NetworkRequired: true,
				ConfigurationError: true,
			})
			return nil
		}
		if present {
			credentialSource = "system"
		}
	}
	status := ProviderNotConfigured
	model := environmentValue(lookup, config.modelEnv, config.defaultModel)
	models := []string{}
	harnesses := []HarnessAvailability{}
	if validAvailabilityIdentifier(model, maxPublicModelNameBytes) {
		model = strings.TrimSpace(model)
		models = []string{model}
		harnesses = []HarnessAvailability{unqualifiedHarnessAvailability(model,
			llm.HarnessTransportAnthropicMessages, llm.HarnessJSONStrategyPrompt)}
	}
	configurationError := timeoutErr != nil
	if configurationError {
		status = ProviderInvalidConfiguration
	} else if present && key != "" {
		baseURL := environmentValue(lookup, config.baseURLEnv, config.defaultBaseURL)
		if len(models) == 0 {
			status = ProviderInvalidConfiguration
			configurationError = true
			r.providers = append(r.providers, ProviderAvailability{
				Name: config.name, Kind: ProviderKindAnthropicCompatible, Status: status,
				Models: models, Harnesses: harnesses,
				CredentialSource: credentialSource, NetworkRequired: true,
				ConfigurationError: configurationError,
			})
			return nil
		}
		provider, err := llm.NewAnthropicCompatibleProvider(llm.AnthropicCompatibleConfig{
			Name: config.name, BaseURL: baseURL, APIKey: key, DefaultModel: model,
			DisableThinking: config.disableThinking,
			HTTPClient:      &http.Client{Timeout: requestTimeout},
		})
		if err != nil {
			status = ProviderInvalidConfiguration
			configurationError = true
		} else {
			status = ProviderAvailable
			r.router.RegisterProvider(provider)
			r.available[config.name] = struct{}{}
		}
	} else if present && key != strings.TrimSpace(key) {
		status = ProviderInvalidConfiguration
		configurationError = true
	}
	r.providers = append(r.providers, ProviderAvailability{
		Name: config.name, Kind: ProviderKindAnthropicCompatible, Status: status,
		Models: models, Harnesses: harnesses,
		CredentialSource: credentialSource, NetworkRequired: true,
		ConfigurationError: configurationError,
	})
	return nil
}

func (r *Registry) registerOpenAIEnvironment(ctx context.Context, config openAIEnvironment,
	lookup EnvironmentLookup, credentials credentialLookup, strictCredentialReads bool,
) error {
	requestTimeout, timeoutErr := environmentRequestTimeout(lookup, config.timeoutEnv)
	key, present := lookup(config.apiKeyEnv)
	credentialSource := "none"
	if present {
		credentialSource = "environment"
	}
	if !present && credentials != nil {
		var err error
		key, present, err = credentials(ctx, config.name)
		if err != nil {
			if strictCredentialReads {
				return fmt.Errorf("read system credential for %s: %w", config.name, err)
			}
			r.providers = append(r.providers, ProviderAvailability{
				Name: config.name, Kind: ProviderKindOpenAICompatible,
				Status: ProviderInvalidConfiguration, Models: []string{}, Harnesses: []HarnessAvailability{},
				CredentialSource: "system", NetworkRequired: true, ConfigurationError: true,
			})
			return nil
		}
		if present {
			credentialSource = "system"
		}
	}
	model := environmentValue(lookup, config.modelEnv, config.defaultModel)
	models := []string{}
	harnesses := []HarnessAvailability{}
	if validAvailabilityIdentifier(model, maxPublicModelNameBytes) {
		model = strings.TrimSpace(model)
		models = []string{model}
		harnesses = []HarnessAvailability{unqualifiedHarnessAvailability(model,
			llm.HarnessTransportOpenAIChatCompletions, llm.HarnessJSONStrategyNative)}
	}
	status := ProviderNotConfigured
	configurationError := timeoutErr != nil
	if configurationError {
		status = ProviderInvalidConfiguration
	} else if present && key != "" {
		if len(models) == 0 {
			status = ProviderInvalidConfiguration
			configurationError = true
		} else {
			baseURL := environmentValue(lookup, config.baseURLEnv, config.defaultBaseURL)
			provider, err := llm.NewOpenAICompatibleProvider(llm.OpenAICompatibleConfig{
				Name:    config.name,
				BaseURL: baseURL,
				APIKey:  key, DefaultModel: model,
				HTTPClient: &http.Client{Timeout: requestTimeout},
			})
			if err != nil {
				status = ProviderInvalidConfiguration
				configurationError = true
			} else {
				status = ProviderAvailable
				r.router.RegisterProvider(provider)
				r.available[config.name] = struct{}{}
				r.freezeGeminiProbe(config.name, baseURL, models, nil)
				r.freezeKimiProbe(config.name, baseURL, models, nil)
			}
		}
	} else if present && key != strings.TrimSpace(key) {
		status = ProviderInvalidConfiguration
		configurationError = true
	}
	r.providers = append(r.providers, ProviderAvailability{
		Name: config.name, Kind: ProviderKindOpenAICompatible, Status: status,
		Models: models, Harnesses: harnesses,
		CredentialSource: credentialSource, NetworkRequired: true,
		ConfigurationError: configurationError,
	})
	return nil
}

// registerOllamaEnvironment enables the keyless loopback provider only when
// the operator explicitly configures CYBERAGENT_OLLAMA_BASE_URL and a default
// model. Without the explicit endpoint the provider stays not_configured; the
// registry never scans the LAN or probes a default endpoint on its own.
func (r *Registry) registerOllamaEnvironment(ctx context.Context, config ollamaEnvironment,
	lookup EnvironmentLookup,
) error {
	_ = ctx
	requestTimeout, timeoutErr := environmentRequestTimeout(lookup, config.timeoutEnv)
	baseURL, endpointSet := lookup(config.baseURLEnv)
	baseURL = strings.TrimSpace(baseURL)
	model := environmentValue(lookup, config.modelEnv, "")
	models := []string{}
	if validAvailabilityIdentifier(model, maxPublicModelNameBytes) {
		models = []string{strings.TrimSpace(model)}
	}
	status := ProviderNotConfigured
	configurationError := timeoutErr != nil
	if configurationError {
		status = ProviderInvalidConfiguration
	} else if endpointSet && baseURL != "" {
		provider, err := llm.NewOllamaProvider(llm.OllamaConfig{
			Name: config.name, BaseURL: baseURL,
			HTTPClient: &http.Client{Timeout: requestTimeout},
		})
		if err != nil || len(models) == 0 {
			status = ProviderInvalidConfiguration
			configurationError = true
		} else {
			status = ProviderAvailable
			r.router.RegisterProvider(provider)
			r.available[config.name] = struct{}{}
			r.mu.Lock()
			r.ollama = provider
			r.mu.Unlock()
		}
	}
	harnesses := make([]HarnessAvailability, 0, len(models))
	for _, current := range models {
		harnesses = append(harnesses, unqualifiedHarnessAvailability(current,
			llm.HarnessTransportOllamaChat, llm.HarnessJSONStrategyNative))
	}
	r.providers = append(r.providers, ProviderAvailability{
		Name: config.name, Kind: ProviderKindOllama, Status: status,
		Models: models, Harnesses: harnesses,
		CredentialSource: "none", NetworkRequired: true,
		ConfigurationError: configurationError,
	})
	return nil
}

// probeOllamaCapabilities performs a bounded best-effort /api/show probe so
// tools/vision/JSON strategies and the context window reflect real daemon
// metadata. Probe failures never block route selection, qualification, or
// diagnostics: the capability cache simply stays unknown and every
// capability fails closed until a later probe succeeds.
func (r *Registry) probeOllamaCapabilities(ctx context.Context, provider string, model string) {
	if r == nil || r.router == nil || provider != ProviderKindOllama {
		return
	}
	r.mu.RLock()
	ollama := r.ollama
	r.mu.RUnlock()
	if ollama == nil || !validAvailabilityIdentifier(model, maxPublicModelNameBytes) {
		return
	}
	probeCtx, cancel := context.WithTimeout(ctx, ollamaProbeTimeout)
	defer cancel()
	probe, err := ollama.ProbeModel(probeCtx, model)
	if err != nil || !probe.Known || probe.ContextLength < 4096 ||
		probe.ContextLength > llm.MaxContextWindowTokens {
		return
	}
	window := llm.DefaultContextWindow()
	window.WindowTokens = probe.ContextLength
	window.Source = "ollama_probe"
	_ = r.router.SetContextWindow(llm.ModelRef{Provider: provider, Model: model}, window)
}

func unqualifiedHarnessAvailability(model string, transport string,
	jsonStrategy string,
) HarnessAvailability {
	return HarnessAvailability{
		ProtocolVersion: llm.ModelHarnessProtocolVersion,
		Model:           model, TransportProtocol: transport,
		ToolStrategy: llm.HarnessToolStrategyNative, JSONStrategy: jsonStrategy,
		QualificationStatus: llm.HarnessQualificationRequired,
	}
}

func harnessForModel(harnesses []HarnessAvailability, model string) (HarnessAvailability, bool) {
	for _, harness := range harnesses {
		if harness.Model == model {
			return harness, true
		}
	}
	return HarnessAvailability{}, false
}

func environmentValue(lookup EnvironmentLookup, name string, fallback string) string {
	value, found := lookup(name)
	if !found || strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func validAvailabilityIdentifier(value string, maximumBytes int) bool {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) ||
		len([]byte(value)) > maximumBytes || redact.String(value) != value {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) || unicode.IsSpace(current) {
			return false
		}
	}
	return true
}

func availabilityIdentifier(value string, maximumBytes int) string {
	if validAvailabilityIdentifier(value, maximumBytes) {
		return value
	}
	if redact.String(value) != value {
		return "redacted"
	}
	return "invalid"
}
