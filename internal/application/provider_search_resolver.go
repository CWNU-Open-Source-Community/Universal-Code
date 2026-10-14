package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/modelregistry"
	"cyberagent-workbench/internal/webevidence"
)

// ProviderSearchResolver maps the active Run's model route to an explicit
// search policy. A declared hosted adapter may execute the first query, while
// readiness requires an observed native tool result for the exact current
// Provider/model/configuration generation.
type ProviderSearchResolver struct {
	registry    *modelregistry.Registry
	definitions modelregistry.RouteSettingReader
	credentials credential.Store
	searxng     webevidence.SearchProvider
	web         webevidence.SearchProvider
	client      *webevidence.SafeHTTPClient
	nativeMu    sync.Mutex
	native      map[[sha256.Size]byte]webevidence.NativeSearchProvider
	nativeOrder [][sha256.Size]byte
}

const providerSearchNativeAdapterLimit = 64

const ProviderSearchReadinessProtocolVersion = "provider_search_readiness.v1"

const (
	ProviderSearchStateNetworkDisabled     = "network_disabled"
	ProviderSearchStateMissingAllowlist    = "missing_allowlist"
	ProviderSearchStateProviderUnqualified = "provider_unqualified"
	ProviderSearchStateProviderUnavailable = "provider_unavailable"
	ProviderSearchStateReady               = "ready"
)

const (
	ProviderSearchReasonRunNetworkDisabled       = "run_network_disabled"
	ProviderSearchReasonEndpointNotAllowlisted   = "search_endpoint_not_allowlisted"
	ProviderSearchReasonQualificationRequired    = "provider_native_qualification_required"
	ProviderSearchReasonQualificationFailed      = "provider_native_qualification_failed"
	ProviderSearchReasonNoActiveRun              = "no_active_run"
	ProviderSearchReasonModelProviderUnavailable = "model_provider_unavailable"
	ProviderSearchReasonPolicyDisabled           = "provider_search_policy_disabled"
	ProviderSearchReasonBackendNotConfigured     = "search_backend_not_configured"
	ProviderSearchReasonConfigurationInvalid     = "provider_search_configuration_invalid"
	ProviderSearchReasonBackendReady             = "search_backend_ready"
)

const (
	ProviderSearchRemediationEnableNetwork       = "enable_network_allowlist"
	ProviderSearchRemediationAddRequiredTarget   = "add_required_target"
	ProviderSearchRemediationQualifyProvider     = "qualify_provider_search"
	ProviderSearchRemediationCreateSuccessor     = "submit_to_create_successor"
	ProviderSearchRemediationConfigureProvider   = "configure_search_provider"
	ProviderSearchRemediationEnablePolicy        = "enable_provider_search"
	ProviderSearchRemediationRepairConfiguration = "repair_provider_configuration"
	ProviderSearchRemediationNone                = "none"
)

// ProviderSearchReadiness is a read-only, credential-free projection. State
// is deliberately fail-closed: declared_unverified can only become ready
// after the exact Provider/model/configuration generation has a positive
// observed qualification cached by the production adapter.
type ProviderSearchReadiness struct {
	ProtocolVersion string `json:"protocol_version"`
	ThreadID        string `json:"thread_id,omitempty"`
	RunID           string `json:"run_id,omitempty"`
	ModelRoute      string `json:"model_route,omitempty"`
	Provider        string `json:"provider,omitempty"`
	Model           string `json:"model,omitempty"`
	SearchPolicy    string `json:"search_policy,omitempty"`
	State           string `json:"state"`
	Reason          string `json:"reason"`
	Remediation     string `json:"remediation"`
	DetailCode      string `json:"detail_code,omitempty"`
	RequiredTarget  string `json:"required_target,omitempty"`
	NetworkMode     string `json:"network_mode"`
	ModeRevision    int64  `json:"mode_revision,omitempty"`
	RuntimeReady    bool   `json:"runtime_ready"`
	CapabilityGrant bool   `json:"capability_grant"`
}

type ProviderSearchReadinessStore interface {
	GetThread(context.Context, string) (domain.Thread, error)
	GetRun(context.Context, string) (domain.Run, error)
	GetRunMode(context.Context, string) (domain.RunModeSnapshot, error)
}

type ProviderSearchReadinessService struct {
	store        ProviderSearchReadinessStore
	resolver     *ProviderSearchResolver
	diagnosticMu sync.Mutex
}

func NewProviderSearchReadinessService(store ProviderSearchReadinessStore,
	resolver *ProviderSearchResolver,
) *ProviderSearchReadinessService {
	return &ProviderSearchReadinessService{store: store, resolver: resolver}
}

func (s *ProviderSearchReadinessService) Get(ctx context.Context,
	threadID string,
) (ProviderSearchReadiness, error) {
	if s == nil || s.store == nil || s.resolver == nil {
		return ProviderSearchReadiness{}, apperror.New(apperror.CodeFailedPrecondition,
			"Provider search readiness dependencies are required")
	}
	threadID = strings.TrimSpace(threadID)
	if !domain.ValidAgentID(threadID) {
		return ProviderSearchReadiness{}, apperror.New(apperror.CodeInvalidArgument,
			"Provider search readiness Thread id is invalid")
	}
	threadRecord, err := s.store.GetThread(ctx, threadID)
	if err != nil {
		return ProviderSearchReadiness{}, apperror.Normalize(err)
	}
	if threadRecord.ActiveRunID == "" {
		return ProviderSearchReadiness{ProtocolVersion: ProviderSearchReadinessProtocolVersion,
			ThreadID: threadID, State: ProviderSearchStateProviderUnavailable,
			Reason:      ProviderSearchReasonNoActiveRun,
			Remediation: ProviderSearchRemediationCreateSuccessor,
			NetworkMode: "disabled"}, nil
	}
	run, err := s.store.GetRun(ctx, threadRecord.ActiveRunID)
	if err != nil {
		return ProviderSearchReadiness{}, apperror.Normalize(err)
	}
	mode, err := s.store.GetRunMode(ctx, run.ID)
	if err != nil {
		return ProviderSearchReadiness{}, apperror.Normalize(err)
	}
	if mode.Scope.WebAccessMode == domain.WebAccessDisabled {
		ref, refErr := providerSearchModelRef(s.resolver.registry.Router(), run.Config.ModelRoute)
		if refErr != nil {
			return ProviderSearchReadiness{}, apperror.New(apperror.CodeFailedPrecondition, "Run model route is unavailable")
		}
		return ProviderSearchReadiness{ProtocolVersion: ProviderSearchReadinessProtocolVersion, ThreadID: threadID, RunID: run.ID, ModelRoute: run.Config.ModelRoute, Provider: ref.Provider, Model: ref.Model, SearchPolicy: webevidence.SearchPolicyDisabled, ModeRevision: mode.Revision, NetworkMode: "disabled", State: ProviderSearchStateNetworkDisabled, Reason: ProviderSearchReasonRunNetworkDisabled, Remediation: ProviderSearchRemediationEnableNetwork}, nil
	}
	authority := webSearchAuthority(mode.Scope)
	readiness := s.resolver.SearchReadiness(ctx,
		webevidence.SearchRoute{ModelRoute: run.Config.ModelRoute},
		authority)
	readiness.ThreadID = threadID
	readiness.RunID = run.ID
	readiness.ModeRevision = mode.Revision
	return readiness, nil
}

func NewProviderSearchResolver(registry *modelregistry.Registry,
	definitions modelregistry.RouteSettingReader, credentials credential.Store,
	searxng webevidence.SearchProvider, client *webevidence.SafeHTTPClient,
) (*ProviderSearchResolver, error) {
	if registry == nil || registry.Router() == nil || definitions == nil || credentials == nil {
		return nil, errors.New("Provider search resolver dependencies are required")
	}
	if client == nil {
		client = webevidence.NewProviderSearchHTTPClient()
	}
	return &ProviderSearchResolver{registry: registry, definitions: definitions,
		credentials: credentials, searxng: searxng, client: client,
		web:    webevidence.NewWebSearchProvider(client),
		native: make(map[[sha256.Size]byte]webevidence.NativeSearchProvider)}, nil
}

func (r *ProviderSearchResolver) ResolveSearch(ctx context.Context,
	route webevidence.SearchRoute, authority webevidence.NetworkAuthority,
) (webevidence.SearchSelection, error) {
	if r == nil || r.registry == nil || r.registry.Router() == nil || ctx == nil {
		return webevidence.SearchSelection{}, errors.New("model-routed Web search is unavailable")
	}
	if err := authority.Validate(); err != nil {
		return webevidence.SearchSelection{}, errors.New("Run Web search authority is invalid")
	}
	ref, err := providerSearchModelRef(r.registry.Router(), route.ModelRoute)
	if err != nil {
		return webevidence.SearchSelection{}, errors.New("Run model route is invalid")
	}
	availability, found := providerSearchAvailability(r.registry.Snapshot(), ref)
	if !found || availability.Status != modelregistry.ProviderAvailable ||
		(availability.Custom && !availability.Enabled) {
		return webevidence.SearchSelection{}, errors.New("Run model Provider is unavailable")
	}

	// Built-in model routes do not carry a durable search policy or the exact
	// endpoint/runtime binding required for Provider-native search. Preserve an
	// explicitly configured process SearXNG backend, but never silently turn an
	// unconfigured route into DuckDuckGo. Operators select DuckDuckGo through a
	// custom Provider's explicit web policy.
	if !availability.Custom {
		if r.searxng == nil {
			return webevidence.SearchSelection{}, errors.New(
				"Run model Provider search is not configured")
		}
		return r.searxngSelection(webevidence.SearchPolicySearXNG,
			"process_searxng_selected", providerSearchBinding(ref.Provider,
				ref.Model, "builtin", 0)), nil
	}

	definition, err := r.customDefinition(ctx, ref.Provider, availability.DefinitionRevision)
	if err != nil {
		return webevidence.SearchSelection{}, errors.New("custom Provider search definition is unavailable")
	}
	switch definition.SearchMode {
	case modelregistry.ProviderSearchModeDisabled:
		return webevidence.SearchSelection{}, errors.New("custom Provider search is disabled")
	case modelregistry.ProviderSearchModeWeb:
		return r.webSelection(webevidence.SearchPolicyWeb, "configured_web_selected",
			providerSearchBinding(definition.ID, ref.Model, definition.SearchMode, definition.Revision)), nil
	case modelregistry.ProviderSearchModeSearXNG:
		if r.searxng == nil {
			return webevidence.SearchSelection{}, errors.New("SearXNG search is not configured")
		}
		return r.searxngSelection(webevidence.SearchPolicySearXNG,
			"configured_searxng_selected", providerSearchBinding(definition.ID,
				ref.Model, definition.SearchMode, definition.Revision)), nil
	case modelregistry.ProviderSearchModeProviderNative:
		selection, err := r.declaredNativeSelection(ctx, authority, definition,
			ref.Model, webevidence.SearchPolicyProviderNative,
			"declared_provider_native")
		if err != nil {
			// provider_native is an exact choice. Invalid configuration, authority,
			// credential, and request-shape states fail closed and never fall back.
			// Runtime qualification is intentionally deferred to the first actual
			// Search call so a cold-process capability snapshot does not make a
			// hidden, potentially billable Provider request.
			return webevidence.SearchSelection{}, errors.New(
				"Provider-native Web search is unavailable")
		}
		return selection, nil
	case modelregistry.ProviderSearchModeAuto:
		if definition.NativeWebSearchCapability ==
			modelregistry.NativeWebSearchDeclaredUnverified &&
			nativeSearchTransportSupported(definition.Transport) &&
			!modelregistry.ProviderNativeWebSearchKnownUnsupported(definition) {
			// Without a configured fallback there is no backend choice for auto
			// to make. Treat the locally validated native declaration like the
			// exact provider_native policy and defer its bounded runtime probe to
			// the first real Search. When SearXNG exists, preserve the observed
			// qualification-before-selection contract below.
			if r.searxng == nil {
				selection, nativeErr := r.declaredNativeSelection(ctx, authority,
					definition, ref.Model, webevidence.SearchPolicyAuto,
					"auto_declared_provider_native")
				if nativeErr != nil {
					return webevidence.SearchSelection{}, errors.New(
						"automatic Web search has no eligible backend")
				}
				return selection, nil
			}
			selection, nativeErr := r.nativeSelection(ctx, authority, definition,
				ref.Model, webevidence.SearchPolicyAuto,
				"auto_qualified_provider_native")
			if nativeErr == nil {
				return selection, nil
			}
			if r.searxng != nil {
				return r.searxngSelection(webevidence.SearchPolicyAuto,
					"auto_native_unavailable_selected_searxng",
					providerSearchBinding(definition.ID, ref.Model,
						definition.SearchMode, definition.Revision)), nil
			}
			return webevidence.SearchSelection{}, errors.New(
				"automatic Web search has no qualified backend")
		}
		if r.searxng != nil {
			return r.searxngSelection(webevidence.SearchPolicyAuto,
				"auto_searxng_selected", providerSearchBinding(definition.ID,
					ref.Model, definition.SearchMode, definition.Revision)), nil
		}
		return webevidence.SearchSelection{}, errors.New(
			"automatic Web search has no configured backend")
	default:
		return webevidence.SearchSelection{}, errors.New("custom Provider search policy is invalid")
	}
}

func (r *ProviderSearchResolver) declaredNativeSelection(ctx context.Context,
	_ webevidence.NetworkAuthority, definition modelregistry.ProviderDefinition,
	model string, policy string, reason string,
) (webevidence.SearchSelection, error) {
	provider, runtime, providerAuthority, err := r.nativeSelectionCandidate(definition, model)
	if err != nil {
		return webevidence.SearchSelection{}, err
	}
	binding, err := provider.DeclaredCapabilityBinding(ctx, providerAuthority)
	if err != nil {
		return webevidence.SearchSelection{}, errors.New(
			"Provider-native search declaration is invalid")
	}
	return webevidence.SearchSelection{Policy: policy,
		Backend: provider.Name(), SelectionReason: reason,
		Binding: providerSearchBinding(definition.ID, model,
			definition.SearchMode+"\x00"+runtime.BindingDigest()+"\x00"+binding,
			definition.Revision), Provider: provider,
		ProviderAuthority: providerAuthority, ProviderAuthorityIndependent: true}, nil
}

// SearchReadiness inspects configuration, Run authority, and the adapter's
// existing qualification cache. It intentionally does not call Qualify and
// therefore performs no Provider request or capability grant.
func (r *ProviderSearchResolver) SearchReadiness(ctx context.Context,
	route webevidence.SearchRoute, authority webevidence.NetworkAuthority,
) ProviderSearchReadiness {
	readiness := ProviderSearchReadiness{ProtocolVersion: ProviderSearchReadinessProtocolVersion,
		ModelRoute: strings.TrimSpace(route.ModelRoute), NetworkMode: authority.Mode,
		State:       ProviderSearchStateProviderUnavailable,
		Reason:      ProviderSearchReasonConfigurationInvalid,
		Remediation: ProviderSearchRemediationRepairConfiguration}
	if r == nil || r.registry == nil || r.registry.Router() == nil || ctx == nil {
		return readiness
	}
	ref, err := providerSearchModelRef(r.registry.Router(), route.ModelRoute)
	if err != nil {
		return readiness
	}
	readiness.Provider, readiness.Model = ref.Provider, ref.Model
	availability, found := providerSearchAvailability(r.registry.Snapshot(), ref)
	if found {
		readiness.SearchPolicy = availability.SearchMode
	}
	if authority.Mode != "disabled" && authority.Mode != "allowlist" {
		readiness.State = ProviderSearchStateMissingAllowlist
		readiness.Reason = ProviderSearchReasonEndpointNotAllowlisted
		readiness.Remediation = ProviderSearchRemediationAddRequiredTarget
		return readiness
	}
	if !found || availability.Status != modelregistry.ProviderAvailable ||
		(availability.Custom && !availability.Enabled) {
		readiness.Reason = ProviderSearchReasonModelProviderUnavailable
		readiness.Remediation = ProviderSearchRemediationConfigureProvider
		return readiness
	}
	if !availability.Custom {
		if r.searxng == nil {
			readiness.Reason = ProviderSearchReasonBackendNotConfigured
			readiness.Remediation = ProviderSearchRemediationConfigureProvider
			return readiness
		}
		readiness.SearchPolicy = webevidence.SearchPolicySearXNG
		return providerSearchBackendReadiness(readiness, r.searxng, authority)
	}
	definition, err := r.customDefinition(ctx, ref.Provider,
		availability.DefinitionRevision)
	if err != nil {
		return readiness
	}
	readiness.SearchPolicy = definition.SearchMode
	switch definition.SearchMode {
	case modelregistry.ProviderSearchModeDisabled:
		readiness.Reason = ProviderSearchReasonPolicyDisabled
		readiness.Remediation = ProviderSearchRemediationEnablePolicy
		return readiness
	case modelregistry.ProviderSearchModeWeb:
		return providerSearchBackendReadiness(readiness, r.web, authority)
	case modelregistry.ProviderSearchModeSearXNG:
		return providerSearchBackendReadiness(readiness, r.searxng, authority)
	case modelregistry.ProviderSearchModeProviderNative:
		return r.nativeReadiness(ctx, readiness, authority, definition, ref.Model)
	case modelregistry.ProviderSearchModeAuto:
		native := readiness
		if definition.NativeWebSearchCapability ==
			modelregistry.NativeWebSearchDeclaredUnverified &&
			nativeSearchTransportSupported(definition.Transport) &&
			!modelregistry.ProviderNativeWebSearchKnownUnsupported(definition) {
			native = r.nativeReadiness(ctx, native, authority, definition, ref.Model)
			if r.searxng == nil {
				// Match ResolveSearch's existing exact declared-native choice;
				// do not advertise Web readiness for a different selected backend.
				return native
			}
		}
		fallback := providerSearchBackendReadiness(readiness, r.searxng, authority)
		if native.State == ProviderSearchStateReady {
			return native
		}
		if fallback.State == ProviderSearchStateReady {
			return fallback
		}
		if native.State == ProviderSearchStateNetworkDisabled {
			return native
		}
		// Provider-native search uses its own Provider authority. Report its
		// qualification state before a disabled direct-network fallback so the
		// read-only projection matches ResolveSearch's native-first order.
		if native.State == ProviderSearchStateProviderUnqualified {
			return native
		}
		if fallback.State == ProviderSearchStateNetworkDisabled {
			return fallback
		}
		if fallback.State == ProviderSearchStateMissingAllowlist {
			return fallback
		}
		if native.State == ProviderSearchStateMissingAllowlist {
			return native
		}
		return fallback
	default:
		return readiness
	}
}

func (r *ProviderSearchResolver) nativeReadiness(ctx context.Context,
	readiness ProviderSearchReadiness, _ webevidence.NetworkAuthority,
	definition modelregistry.ProviderDefinition, model string,
) ProviderSearchReadiness {
	if modelregistry.ProviderNativeWebSearchKnownUnsupported(definition) {
		readiness.State = ProviderSearchStateProviderUnavailable
		readiness.Reason = ProviderSearchReasonQualificationFailed
		readiness.Remediation = ProviderSearchRemediationConfigureProvider
		readiness.DetailCode = webevidence.NativeSearchReasonToolUnsupported
		return readiness
	}
	if definition.NativeWebSearchCapability !=
		modelregistry.NativeWebSearchDeclaredUnverified ||
		!nativeSearchTransportSupported(definition.Transport) ||
		!r.credentials.Available() {
		readiness.Reason = ProviderSearchReasonConfigurationInvalid
		readiness.Remediation = ProviderSearchRemediationRepairConfiguration
		return readiness
	}
	runtime, err := modelregistry.NewProviderRequestRuntime(definition, r.credentials)
	if err != nil {
		return readiness
	}
	provider, err := r.nativeProvider(definition, model, runtime)
	if err != nil {
		return readiness
	}
	providerAuthority, err := providerNativeSearchAuthority(provider.Endpoint())
	if err != nil {
		readiness.Reason = ProviderSearchReasonConfigurationInvalid
		readiness.Remediation = ProviderSearchRemediationRepairConfiguration
		return readiness
	}
	// Provider API egress is part of the configured model route, not the
	// operator's direct web_fetch allowlist. RequiredTarget therefore remains
	// empty and disabling direct Web access does not disable hosted search.
	readiness.RequiredTarget = ""
	snapshot := provider.QualificationSnapshot(ctx, providerAuthority)
	switch snapshot.Status {
	case webevidence.SearchQualificationReady:
		readiness.State = ProviderSearchStateReady
		readiness.Reason = ProviderSearchReasonBackendReady
		readiness.Remediation = ProviderSearchRemediationNone
		readiness.RuntimeReady = true
	case webevidence.SearchQualificationUnqualified:
		readiness.State = ProviderSearchStateProviderUnqualified
		readiness.Reason = ProviderSearchReasonQualificationRequired
		readiness.Remediation = ProviderSearchRemediationQualifyProvider
	case webevidence.SearchQualificationUnavailable:
		readiness.State = ProviderSearchStateProviderUnavailable
		readiness.Reason = ProviderSearchReasonQualificationFailed
		readiness.Remediation = ProviderSearchRemediationQualifyProvider
		readiness.DetailCode = snapshot.Reason
	default:
		readiness.Reason = ProviderSearchReasonConfigurationInvalid
	}
	return readiness
}

func providerSearchBackendReadiness(readiness ProviderSearchReadiness,
	provider webevidence.SearchProvider, authority webevidence.NetworkAuthority,
) ProviderSearchReadiness {
	if provider == nil || strings.TrimSpace(provider.Endpoint()) == "" {
		readiness.State = ProviderSearchStateProviderUnavailable
		readiness.Reason = ProviderSearchReasonBackendNotConfigured
		readiness.Remediation = ProviderSearchRemediationConfigureProvider
		return readiness
	}
	readiness.RequiredTarget = providerSearchEndpointTarget(provider.Endpoint())
	if authority.Mode == "disabled" {
		readiness.State = ProviderSearchStateNetworkDisabled
		readiness.Reason = ProviderSearchReasonRunNetworkDisabled
		readiness.Remediation = ProviderSearchRemediationEnableNetwork
		return readiness
	}
	if _, err := authority.Authorize(provider.Endpoint()); err != nil {
		readiness.State = ProviderSearchStateMissingAllowlist
		readiness.Reason = ProviderSearchReasonEndpointNotAllowlisted
		readiness.Remediation = ProviderSearchRemediationAddRequiredTarget
		return readiness
	}
	readiness.State = ProviderSearchStateReady
	readiness.Reason = ProviderSearchReasonBackendReady
	readiness.Remediation = ProviderSearchRemediationNone
	readiness.RuntimeReady = true
	return readiness
}

func providerSearchEndpointTarget(raw string) string {
	canonical, err := webevidence.CanonicalizePublicHTTPSURL(raw)
	if err != nil {
		return ""
	}
	parsed, err := url.Parse(canonical)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

func (r *ProviderSearchResolver) nativeSelection(ctx context.Context,
	_ webevidence.NetworkAuthority, definition modelregistry.ProviderDefinition,
	model string, policy string, reason string,
) (webevidence.SearchSelection, error) {
	provider, runtime, providerAuthority, err := r.nativeSelectionCandidate(definition, model)
	if err != nil {
		return webevidence.SearchSelection{}, err
	}
	binding, err := provider.Qualify(ctx, providerAuthority)
	if err != nil {
		return webevidence.SearchSelection{}, errors.New("Provider-native search qualification is unavailable")
	}
	return webevidence.SearchSelection{Policy: policy, Backend: provider.Name(),
		SelectionReason: reason,
		Binding: providerSearchBinding(definition.ID, model,
			definition.SearchMode+"\x00"+runtime.BindingDigest()+"\x00"+binding,
			definition.Revision), Provider: provider,
		ProviderAuthority: providerAuthority, ProviderAuthorityIndependent: true}, nil
}

func (r *ProviderSearchResolver) nativeSelectionCandidate(
	definition modelregistry.ProviderDefinition, model string,
) (webevidence.NativeSearchProvider, llm.HTTPProviderRuntime,
	webevidence.NetworkAuthority, error,
) {
	if modelregistry.ProviderNativeWebSearchKnownUnsupported(definition) {
		return nil, nil, webevidence.NetworkAuthority{}, errors.New("Provider does not implement hosted Web search")
	}
	if definition.NativeWebSearchCapability !=
		modelregistry.NativeWebSearchDeclaredUnverified ||
		!nativeSearchTransportSupported(definition.Transport) ||
		!r.credentials.Available() {
		return nil, nil, webevidence.NetworkAuthority{},
			errors.New("Provider-native search is not declared")
	}
	runtime, err := modelregistry.NewProviderRequestRuntime(definition, r.credentials)
	if err != nil {
		return nil, nil, webevidence.NetworkAuthority{},
			errors.New("Provider-native request policy is invalid")
	}
	provider, err := r.nativeProvider(definition, model, runtime)
	if err != nil {
		return nil, nil, webevidence.NetworkAuthority{},
			errors.New("Provider-native endpoint is invalid")
	}
	providerAuthority, err := providerNativeSearchAuthority(provider.Endpoint())
	if err != nil {
		return nil, nil, webevidence.NetworkAuthority{},
			errors.New("Provider-native endpoint authority is invalid")
	}
	return provider, runtime, providerAuthority, nil
}

func providerNativeSearchAuthority(endpoint string) (webevidence.NetworkAuthority, error) {
	target := providerSearchEndpointTarget(endpoint)
	authority := webevidence.NetworkAuthority{Mode: "allowlist",
		AllowedTargets: []string{target}}
	if target == "" {
		return webevidence.NetworkAuthority{}, errors.New("Provider endpoint target is invalid")
	}
	if _, err := authority.Authorize(endpoint); err != nil {
		return webevidence.NetworkAuthority{}, err
	}
	return authority, nil
}

// nativeProvider reuses the adapter across capability snapshot and execution.
// The adapter owns single-flight and credential-generation caches; rebuilding
// it for every Resolve would otherwise duplicate a potentially billable probe.
// This key is configuration-only. The adapter keeps its private credential
// digest solely inside its own non-printable in-process cache key.
func (r *ProviderSearchResolver) nativeProvider(
	definition modelregistry.ProviderDefinition, model string,
	runtime llm.HTTPProviderRuntime,
) (webevidence.NativeSearchProvider, error) {
	endpoint := providerSearchResponsesEndpoint(definition.EndpointURL)
	if definition.Transport == llm.HarnessTransportAnthropicMessages {
		endpoint = providerSearchAnthropicEndpoint(definition.EndpointURL)
	}
	key := sha256.Sum256([]byte(strings.Join([]string{
		"provider-search-adapter.v1", definition.ID, endpoint,
		model, definition.Transport, strconv.FormatUint(definition.Revision, 10),
		runtime.BindingDigest(),
	}, "\x00")))
	r.nativeMu.Lock()
	defer r.nativeMu.Unlock()
	if provider := r.native[key]; provider != nil {
		return provider, nil
	}
	var provider webevidence.NativeSearchProvider
	var err error
	switch definition.Transport {
	case llm.HarnessTransportOpenAIResponses:
		provider, err = webevidence.NewOpenAIResponsesSearchProvider(r.client, endpoint, definition.ID, model, runtime)
	case llm.HarnessTransportAnthropicMessages:
		provider, err = webevidence.NewAnthropicSearchProvider(r.client, endpoint, definition.ID, model, runtime)
	default:
		return nil, errors.New("Provider-native search transport is unsupported")
	}
	if err != nil {
		return nil, err
	}
	r.native[key] = provider
	r.nativeOrder = append(r.nativeOrder, key)
	for len(r.native) > providerSearchNativeAdapterLimit && len(r.nativeOrder) > 0 {
		oldest := r.nativeOrder[0]
		r.nativeOrder = r.nativeOrder[1:]
		delete(r.native, oldest)
	}
	return provider, nil
}

func providerSearchResponsesEndpoint(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(baseURL, "/v1/responses") ||
		strings.HasSuffix(baseURL, "/responses") {
		return baseURL
	}
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL + "/responses"
	}
	return baseURL + "/v1/responses"
}

func nativeSearchTransportSupported(transport string) bool {
	return transport == llm.HarnessTransportOpenAIResponses || transport == llm.HarnessTransportAnthropicMessages
}

func providerSearchAnthropicEndpoint(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(baseURL, "/v1/messages") {
		return baseURL
	}
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL + "/messages"
	}
	return baseURL + "/v1/messages"
}

func (r *ProviderSearchResolver) webSelection(policy string, reason string, binding string) webevidence.SearchSelection {
	return webevidence.SearchSelection{Policy: policy, Backend: r.web.Name(),
		SelectionReason: reason, Binding: binding, Provider: r.web}
}

func (r *ProviderSearchResolver) searxngSelection(policy string, reason string,
	binding string,
) webevidence.SearchSelection {
	return webevidence.SearchSelection{Policy: policy, Backend: r.searxng.Name(),
		SelectionReason: reason, Binding: binding, Provider: r.searxng}
}

func (r *ProviderSearchResolver) customDefinition(ctx context.Context,
	provider string, revision uint64,
) (modelregistry.ProviderDefinition, error) {
	collection, err := modelregistry.ReadProviderDefinitions(ctx, r.definitions)
	if err != nil {
		return modelregistry.ProviderDefinition{}, err
	}
	for _, definition := range collection.Providers {
		if definition.ID == provider && definition.Revision == revision &&
			definition.Enabled {
			return definition, nil
		}
	}
	return modelregistry.ProviderDefinition{}, errors.New("custom Provider definition is stale")
}

func providerSearchModelRef(router *llm.Router, route string) (llm.ModelRef, error) {
	route = strings.TrimSpace(route)
	if route == "" {
		return llm.ModelRef{}, errors.New("model route is required")
	}
	if strings.Contains(route, "/") {
		return llm.ParseModelRef(route)
	}
	return router.Resolve(route), nil
}

func providerSearchAvailability(snapshot modelregistry.Snapshot,
	ref llm.ModelRef,
) (modelregistry.ProviderAvailability, bool) {
	for _, provider := range snapshot.Providers {
		if provider.Name == ref.Provider && slices.Contains(provider.Models, ref.Model) {
			return provider, true
		}
	}
	return modelregistry.ProviderAvailability{}, false
}

func providerSearchBinding(provider string, model string, policy string,
	revision uint64,
) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("provider-search-binding.v1\x00%s\x00%s\x00%s\x00%s",
		provider, model, policy, strconv.FormatUint(revision, 10))))
	return hex.EncodeToString(sum[:])
}
