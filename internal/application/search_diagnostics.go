package application

import (
	"context"
	"errors"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/modelregistry"
	"cyberagent-workbench/internal/webevidence"
)

const SearchDiagnosticsProtocolVersion = "search_diagnostics.v1"

type SearchDiagnostics struct {
	ProtocolVersion         string    `json:"protocol_version"`
	ThreadID                string    `json:"thread_id"`
	RunID                   string    `json:"run_id"`
	ModelRoute              string    `json:"model_route"`
	Provider                string    `json:"provider"`
	Model                   string    `json:"model"`
	SearchPolicy            string    `json:"search_policy"`
	Backend                 string    `json:"backend"`
	ModeRevision            int64     `json:"mode_revision"`
	CheckedAt               time.Time `json:"checked_at"`
	State                   string    `json:"state"`
	Code                    string    `json:"code"`
	ResultCount             int       `json:"result_count"`
	NetworkRequestAttempted bool      `json:"network_request_attempted"`
	HTTPStatus              int       `json:"http_status,omitempty"`
	RetryAfter              string    `json:"retry_after,omitempty"`
	RateLimitReset          string    `json:"rate_limit_reset,omitempty"`
	RequiredTarget          string    `json:"required_target,omitempty"`
}

// Check performs one explicit, bounded connectivity/search probe. It does not
// create a Run, consume its messages, approve tools, or repeat a user's task.
// GET readiness deliberately remains a local-only projection.
func (s *ProviderSearchReadinessService) Check(ctx context.Context, threadID string) (SearchDiagnostics, error) {
	if s == nil || s.resolver == nil || s.resolver.registry == nil {
		return SearchDiagnostics{}, apperror.New(apperror.CodeUnavailable, "Search diagnostics are unavailable")
	}
	if !s.diagnosticMu.TryLock() {
		return SearchDiagnostics{}, apperror.New(apperror.CodeConflict, "A search connection check is already in progress")
	}
	defer s.diagnosticMu.Unlock()
	generation := s.resolver.registry.Generation()
	ready, err := s.Get(ctx, threadID)
	if err != nil {
		return SearchDiagnostics{}, err
	}
	result := SearchDiagnostics{ProtocolVersion: SearchDiagnosticsProtocolVersion, ThreadID: threadID, RunID: ready.RunID,
		ModelRoute: ready.ModelRoute, Provider: ready.Provider, Model: ready.Model, SearchPolicy: ready.SearchPolicy,
		ModeRevision: ready.ModeRevision, CheckedAt: time.Now().UTC(), State: "failed", Code: "not_configured", RequiredTarget: ready.RequiredTarget}
	if ready.State == ProviderSearchStateNetworkDisabled || ready.State == ProviderSearchStateMissingAllowlist {
		result.Code = "not_authorized"
		return result, nil
	}
	if ready.RunID == "" {
		return result, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	// The ordinary Run lease serializes the probe with web-mode transitions.
	// A busy Run can be checked once its current execution has stopped.
	if leases, ok := s.store.(interface {
		AcquireRunExecutionLease(context.Context, domain.AcquireRunExecutionLeaseRequest) (domain.RunExecutionLeaseAcquisition, error)
		ReleaseRunExecutionLease(context.Context, domain.RunExecutionLease) (domain.RunExecutionLease, bool, error)
	}); ok {
		acquired, err := leases.AcquireRunExecutionLease(probeCtx, domain.AcquireRunExecutionLeaseRequest{RunID: ready.RunID, OwnerID: idgen.New("search-check"), TTL: 30 * time.Second})
		if err != nil {
			return result, apperror.Normalize(err)
		}
		defer func() { _, _, _ = leases.ReleaseRunExecutionLease(context.WithoutCancel(ctx), acquired.Lease) }()
	}
	run, err := s.store.GetRun(probeCtx, ready.RunID)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	mode, err := s.store.GetRunMode(probeCtx, run.ID)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	if mode.Scope.WebAccessMode == domain.WebAccessDisabled || mode.Revision != ready.ModeRevision || run.Config.ModelRoute != ready.ModelRoute {
		result.Code = "configuration_changed"
		return result, nil
	}
	authority := webevidence.NetworkAuthority{Mode: "allowlist",
		AllowedTargets: []string{webevidence.PublicHTTPSTarget}}
	selection, err := s.resolver.diagnosticSelection(probeCtx, webevidence.SearchRoute{ModelRoute: run.Config.ModelRoute}, authority)
	if err != nil {
		result.Code = searchDiagnosticFailureCode(err)
		return result, nil
	}
	result.Backend = selection.Backend
	result.SearchPolicy = selection.Policy
	if s.resolver.registry.Generation() != generation || mode.Revision != ready.ModeRevision || run.Config.ModelRoute != ready.ModelRoute {
		result.Code = "configuration_changed"
		return result, nil
	}
	if selection.ProviderAuthorityIndependent {
		authority = selection.ProviderAuthority
	}
	if selection.Provider == nil {
		return result, nil
	}
	if _, err := authority.Authorize(selection.Provider.Endpoint()); err != nil {
		result.Code = "not_authorized"
		return result, nil
	}
	result.NetworkRequestAttempted = true
	var items []webevidence.ProviderResult
	var probeErr error
	if native, ok := selection.Provider.(webevidence.NativeSearchProvider); ok {
		items, probeErr = native.CheckSearchConnection(probeCtx, "Traverse Board", 1, authority)
	} else {
		items, probeErr = selection.Provider.Search(probeCtx, "Traverse Board", 1, authority)
	}
	result.CheckedAt = time.Now().UTC()
	if probeErr != nil {
		result.Code = searchDiagnosticFailureCode(probeErr)
		var diagnostic *webevidence.SearchDiagnosticError
		if errors.As(probeErr, &diagnostic) {
			result.NetworkRequestAttempted = !diagnostic.RequestNotAttempted
			result.HTTPStatus = diagnostic.HTTPStatus
			result.RetryAfter = diagnostic.RetryAfter
			result.RateLimitReset = diagnostic.RateLimitReset
		}
	} else if len(items) == 0 {
		result.Code = "no_usable_results"
	} else {
		result.State, result.Code, result.ResultCount = "succeeded", "none", len(items)
	}
	// Never present the observation as current after a concurrent Thread route,
	// web policy or Provider configuration change.
	current, currentErr := s.Get(ctx, threadID)
	currentSelection, selectionErr := s.resolver.diagnosticSelection(ctx, webevidence.SearchRoute{ModelRoute: run.Config.ModelRoute}, authority)
	if currentErr != nil || current.RunID != ready.RunID || current.ModelRoute != ready.ModelRoute ||
		current.Provider != ready.Provider || current.Model != ready.Model || current.SearchPolicy != ready.SearchPolicy ||
		current.ModeRevision != ready.ModeRevision ||
		s.resolver.registry.Generation() != generation || selectionErr != nil || currentSelection.Binding != selection.Binding {
		result.State, result.Code, result.ResultCount = "failed", "configuration_changed", 0
		result.RetryAfter, result.RateLimitReset = "", ""
	}
	return result, nil
}

func searchDiagnosticFailureCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var diagnostic *webevidence.SearchDiagnosticError
	if errors.As(err, &diagnostic) {
		return diagnostic.Code
	}
	var native *webevidence.NativeSearchQualificationError
	if errors.As(err, &native) {
		switch native.Reason {
		case webevidence.NativeSearchReasonToolUnsupported:
			return "tool_unsupported"
		case webevidence.NativeSearchReasonCredentialUnavailable:
			return "authentication"
		case webevidence.NativeSearchReasonTransportUnavailable:
			return "network"
		case webevidence.NativeSearchReasonProviderRejected:
			return "provider_rejected"
		default:
			return "invalid_response"
		}
	}
	switch webevidence.ClassifySearchProviderFailure(err) {
	case webevidence.SearchFailureReasonUnreachable:
		return "network"
	case webevidence.SearchFailureReasonNoUsableResults:
		return "no_usable_results"
	default:
		return "not_configured"
	}
}

// diagnosticSelection is local-only. In auto mode it inspects the same cached
// candidate as readiness; it never qualifies native search or falls back after
// a failed request. An exact native selection remains probeable after failure.
func (r *ProviderSearchResolver) diagnosticSelection(ctx context.Context, route webevidence.SearchRoute, authority webevidence.NetworkAuthority) (webevidence.SearchSelection, error) {
	ref, err := providerSearchModelRef(r.registry.Router(), route.ModelRoute)
	if err != nil {
		return webevidence.SearchSelection{}, err
	}
	availability, found := providerSearchAvailability(r.registry.Snapshot(), ref)
	if !found || availability.Status != modelregistry.ProviderAvailable || (availability.Custom && !availability.Enabled) {
		return webevidence.SearchSelection{}, errors.New("model Provider is unavailable")
	}
	if !availability.Custom {
		if r.searxng == nil {
			return webevidence.SearchSelection{}, errors.New("search is not configured")
		}
		return r.searxngSelection(webevidence.SearchPolicySearXNG, "diagnostic_process_searxng", providerSearchBinding(ref.Provider, ref.Model, "builtin", 0)), nil
	}
	definition, err := r.customDefinition(ctx, ref.Provider, availability.DefinitionRevision)
	if err != nil {
		return webevidence.SearchSelection{}, err
	}
	binding := providerSearchBinding(definition.ID, ref.Model, definition.SearchMode, definition.Revision)
	switch definition.SearchMode {
	case modelregistry.ProviderSearchModeWeb:
		return r.webSelection(webevidence.SearchPolicyWeb, "diagnostic_web", binding), nil
	case modelregistry.ProviderSearchModeSearXNG:
		if r.searxng != nil {
			return r.searxngSelection(webevidence.SearchPolicySearXNG, "diagnostic_searxng", binding), nil
		}
	case modelregistry.ProviderSearchModeProviderNative:
		return r.declaredNativeSelection(ctx, authority, definition, ref.Model, webevidence.SearchPolicyProviderNative, "diagnostic_native")
	case modelregistry.ProviderSearchModeAuto:
		candidate, nativeErr := r.declaredNativeSelection(ctx, authority, definition, ref.Model, webevidence.SearchPolicyAuto, "diagnostic_auto_native")
		if nativeErr == nil {
			native, ok := candidate.Provider.(webevidence.NativeSearchProvider)
			if !ok {
				return webevidence.SearchSelection{}, errors.New("native search adapter does not implement diagnostics")
			}
			fallback := providerSearchBackendReadiness(ProviderSearchReadiness{}, r.searxng, authority)
			if native.QualificationSnapshot(ctx, candidate.ProviderAuthority).Status == webevidence.SearchQualificationReady || fallback.State != ProviderSearchStateReady {
				return candidate, nil
			}
		}
		if r.searxng != nil {
			return r.searxngSelection(webevidence.SearchPolicyAuto, "diagnostic_auto_searxng", binding), nil
		}
	}
	return webevidence.SearchSelection{}, errors.New("search is not configured")
}
