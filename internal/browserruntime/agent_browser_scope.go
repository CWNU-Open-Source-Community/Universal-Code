package browserruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
)

const AgentBrowserOriginScopeProtocolVersion = "agent_browser_origin_scope.v1"

// MaxAgentBrowserScopeOrigins mirrors the builtin profile ceiling so a scoped
// agent browser session never declares a wider origin set than the safe-web
// products accept (ADR 0195).
const MaxAgentBrowserScopeOrigins = 8

// agentBrowserScopeProfile carries only the URL grammar the scope reuses from
// the safe-web target machinery. The agent browser is not a builtin registry
// profile: it is a disposable runtime whose scoped sessions serve local dev
// previews and public pages, so its grammar admits loopback and public names
// and leaves private network ranges denied. Name literals in the metadata and
// link-local families stay blocked through the shared classifier; resolved
// addresses of declared names are re-checked at dispatch (and per connection
// once the Fetch-domain extension lands). The grammar and limits are part of
// the scope's meaning: widening either requires bumping
// AgentBrowserOriginScopeProtocolVersion so stored scopes fail closed.
var agentBrowserScopeProfile = ProfileDescriptor{
	ID:     "agent-browser",
	Limits: ProfileLimits{MaxOrigins: MaxAgentBrowserScopeOrigins, MaxURLBytes: 4096},
	Network: NetworkControls{AllowPublicTargets: true, AllowLoopbackTargets: true,
		RequireExactOrigins: true, RevalidateRedirects: true, RevalidateResolvedIPs: true,
		BlockLinkLocal: true, BlockCloudMetadata: true, DefaultDeny: true},
}

// AgentBrowserOriginScope is the operator-declared, default-deny origin set of
// one agent browser session. Navigation and its landing URL must stay inside
// the declared set; the model can never add, widen, or re-declare origins.
// The operator surface that declares the set is a later phase of ADR 0195, so
// today only the trusted composition root constructs one.
type AgentBrowserOriginScope struct {
	ProtocolVersion string   `json:"protocol_version"`
	Origins         []Origin `json:"origins"`
	Fingerprint     string   `json:"fingerprint"`
}

// AgentBrowserScopeError is a named, deterministic scope rejection. It never
// marks an action outcome_unknown: the refusal happens before any effect, or
// names an observed escape after the page landed outside the declared set.
type AgentBrowserScopeError struct {
	Code   string
	Origin string
}

const (
	AgentBrowserScopeInvalidURL         = "invalid_navigation_url"
	AgentBrowserScopeOriginNotDeclared  = "origin_not_declared"
	AgentBrowserScopeLeftOrigins        = "navigation_left_declared_origins"
	AgentBrowserScopeInvalid            = "invalid_scope"
	AgentBrowserScopeResolutionFailed   = "resolution_failed"
	AgentBrowserScopeResolvedOutOfScope = "resolved_address_out_of_scope"
)

func (e *AgentBrowserScopeError) Error() string {
	switch e.Code {
	case AgentBrowserScopeOriginNotDeclared:
		return fmt.Sprintf("browser_origin_not_declared: %s is not declared for this agent browser session; ask the operator to declare it", e.Origin)
	case AgentBrowserScopeLeftOrigins:
		return fmt.Sprintf("browser_navigation_left_declared_origins: the page left the declared origins at %s; navigate back inside the declared set before further inputs", e.Origin)
	case AgentBrowserScopeInvalidURL:
		return "browser_navigation_url_invalid: the URL is not a navigable HTTP(S) origin inside the declared scope bounds"
	case AgentBrowserScopeResolutionFailed:
		return fmt.Sprintf("browser_origin_resolution_failed: %s could not be resolved; a scoped session refuses to navigate on an unresolved name", e.Origin)
	case AgentBrowserScopeResolvedOutOfScope:
		return fmt.Sprintf("browser_resolved_address_out_of_scope: %s resolves to a forbidden address; declare a different origin or ask the operator", e.Origin)
	default:
		return "browser_scope_invalid: the agent browser session scope failed validation"
	}
}

// NewAgentBrowserOriginScope builds a scope from operator-declared origin
// strings using the shared safe-web target grammar: exact scheme+host+port
// origins, canonicalized, deduplicated, sorted, and fingerprint-bound.
func NewAgentBrowserOriginScope(rawTargets []string) (AgentBrowserOriginScope, error) {
	if len(rawTargets) == 0 || len(rawTargets) > MaxAgentBrowserScopeOrigins {
		return AgentBrowserOriginScope{}, fmt.Errorf("agent browser scope requires between 1 and %d origins",
			MaxAgentBrowserScopeOrigins)
	}
	unique := make(map[string]Origin, len(rawTargets))
	for _, rawTarget := range rawTargets {
		origin, _, err := parseTarget(agentBrowserScopeProfile, agentBrowserScopeProfile.Limits, rawTarget)
		if err != nil {
			return AgentBrowserOriginScope{}, err
		}
		unique[origin.String()] = origin
	}
	origins := make([]Origin, 0, len(unique))
	for _, origin := range unique {
		origins = append(origins, origin)
	}
	sort.Slice(origins, func(left, right int) bool { return origins[left].String() < origins[right].String() })
	scope := AgentBrowserOriginScope{ProtocolVersion: AgentBrowserOriginScopeProtocolVersion, Origins: origins}
	fingerprint, err := agentBrowserScopeFingerprint(scope)
	if err != nil {
		return AgentBrowserOriginScope{}, err
	}
	scope.Fingerprint = fingerprint
	if err := scope.Validate(); err != nil {
		return AgentBrowserOriginScope{}, err
	}
	return scope, nil
}

func (scope AgentBrowserOriginScope) Validate() error {
	if scope.ProtocolVersion != AgentBrowserOriginScopeProtocolVersion {
		return errors.New("unsupported agent browser scope protocol")
	}
	if len(scope.Origins) == 0 || len(scope.Origins) > MaxAgentBrowserScopeOrigins {
		return errors.New("agent browser scope origin count is out of bounds")
	}
	last := ""
	for _, origin := range scope.Origins {
		rebuilt, _, err := parseTarget(agentBrowserScopeProfile, agentBrowserScopeProfile.Limits, origin.String())
		if err != nil || rebuilt != origin {
			return errors.New("agent browser scope contains a non-canonical origin")
		}
		current := origin.String()
		if last != "" && current <= last {
			return errors.New("agent browser scope origins must be unique and sorted")
		}
		last = current
	}
	expected, err := agentBrowserScopeFingerprint(scope)
	if err != nil || scope.Fingerprint != expected {
		return errors.New("agent browser scope fingerprint mismatch")
	}
	return nil
}

// AuthorizeNavigation admits exactly the declared origins. Any path on an
// allowed origin passes; the scheme, host, or port must match exactly. URLs
// beyond the scope's 4096-byte grammar bound fail here even though the raw
// runtime limit is 8192: the model path is bounded at 4096 upstream anyway.
// The context reaches the dispatch-time DNS lookup so cancellation and the
// caller's deadlines apply; no separate timeout is layered on top.
func (scope AgentBrowserOriginScope) AuthorizeNavigation(ctx context.Context, rawURL string) error {
	origin, err := scope.authorizeOrigin(rawURL)
	if err != nil {
		return err
	}
	if !origin.ResolutionCheckRequired {
		return nil
	}
	// Interim resolved-address enforcement (ADR 0195 Phase 1): a declared
	// public name must not resolve into a forbidden range. The per-connection
	// revalidation the safe-web product performs on every intercepted request
	// lands with the agent-browser Fetch-domain extension; until then this
	// dispatch-time check closes the obvious DNS-rebinding route, and the
	// window between this resolution and the browser's own connection is the
	// same accepted TOCTOU residual the composition already documents.
	return scope.authorizeResolved(ctx, origin, net.DefaultResolver.LookupHost)
}

func (scope AgentBrowserOriginScope) authorizeOrigin(rawURL string) (Origin, error) {
	if err := scope.Validate(); err != nil {
		return Origin{}, &AgentBrowserScopeError{Code: AgentBrowserScopeInvalid}
	}
	origin, _, err := parseTarget(agentBrowserScopeProfile, agentBrowserScopeProfile.Limits, rawURL)
	if err != nil {
		return Origin{}, &AgentBrowserScopeError{Code: AgentBrowserScopeInvalidURL, Origin: rawURL}
	}
	for _, allowed := range scope.Origins {
		if allowed == origin {
			return origin, nil
		}
	}
	return Origin{}, &AgentBrowserScopeError{Code: AgentBrowserScopeOriginNotDeclared, Origin: origin.String()}
}

// authorizeResolved refuses when any resolved address of the origin falls in a
// forbidden class. Rejecting on the first forbidden address is deliberately
// conservative: the browser will connect to one of the returned addresses and
// the scope cannot observe which.
func (scope AgentBrowserOriginScope) authorizeResolved(ctx context.Context, origin Origin, lookup func(context.Context, string) ([]string, error)) error {
	addresses, err := lookup(ctx, origin.Host)
	if err != nil || len(addresses) == 0 {
		return &AgentBrowserScopeError{Code: AgentBrowserScopeResolutionFailed, Origin: origin.String()}
	}
	for _, address := range addresses {
		if !scope.authorizeResolvedAddress(origin, address) {
			return &AgentBrowserScopeError{Code: AgentBrowserScopeResolvedOutOfScope, Origin: origin.String() + " resolved " + address}
		}
	}
	return nil
}

// authorizeResolvedAddress mirrors TargetScope.AuthorizeResolvedAddress's
// classification for a matched scope origin under the agent-browser grammar
// (private ranges denied): blocked classes never pass, a literal host must
// match exactly, a loopback origin stays on loopback, and public addresses
// pass. Keep aligned with the safe-web source of truth in scope.go.
func (scope AgentBrowserOriginScope) authorizeResolvedAddress(origin Origin, rawAddress string) bool {
	address, err := netip.ParseAddr(strings.TrimSpace(rawAddress))
	if err != nil || address.Zone() != "" {
		return false
	}
	address = address.Unmap()
	if class := classifyAddress(address); class == HostClassBlocked {
		return false
	}
	if literal, literalErr := netip.ParseAddr(origin.Host); literalErr == nil {
		return literal.Unmap() == address
	}
	class := classifyAddress(address)
	if origin.HostClass == HostClassLoopback && class != HostClassLoopback {
		return false
	}
	switch class {
	case HostClassPublicIP:
		return true
	case HostClassLoopback:
		return origin.HostClass == HostClassLoopback
	default:
		return false
	}
}

// agentBrowserScopeForRequest builds the session scope once at launch so the
// enforcement points never re-parse untrusted input per call.
func agentBrowserScopeForRequest(request AgentBrowserStartRequest) (*AgentBrowserOriginScope, error) {
	if len(request.ScopedOrigins) == 0 {
		return nil, nil
	}
	scope, err := NewAgentBrowserOriginScope(request.ScopedOrigins)
	if err != nil {
		return nil, ErrBrowserRuntimeBoundary
	}
	return &scope, nil
}

// MatchesOrigin reports whether the URL's origin is declared, without any
// resolution: composition boundaries that only need membership (readiness
// probes, display) use this, while navigation keeps the full authorization.
func (scope AgentBrowserOriginScope) MatchesOrigin(rawURL string) bool {
	origin, _, err := parseTarget(agentBrowserScopeProfile, agentBrowserScopeProfile.Limits, rawURL)
	if err != nil {
		return false
	}
	for _, allowed := range scope.Origins {
		if allowed == origin {
			return true
		}
	}
	return false
}

func agentBrowserScopeFingerprint(scope AgentBrowserOriginScope) (string, error) {
	copyValue := scope
	copyValue.Fingerprint = ""
	raw, err := json.Marshal(copyValue)
	if err != nil {
		return "", fmt.Errorf("encode agent browser scope: %w", err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

// authorizeSessionNavigation is the single runtime enforcement point: the
// dispatched URL and the page it lands on must both stay inside the declared
// set. Unscoped sessions admit every navigable URL, unchanged. Phase 1 scopes
// navigation and redirects only; once a landing escape is named, inputs on the
// escaped page are a Phase 2 boundary decision (the error already tells the
// model to navigate back before further inputs).
func (r *AgentBrowserRuntime) authorizeSessionNavigation(ctx context.Context, rawURL string) error {
	if r.scope == nil {
		return nil
	}
	return r.scope.AuthorizeNavigation(ctx, rawURL)
}
