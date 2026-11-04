package browserruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/credential"
)

const SessionPlanProtocolVersion = "browser_session_plan.v1"

type ProxyMode string

const (
	ProxyModeDirect ProxyMode = "direct"
	ProxyModeHTTP   ProxyMode = "http"
	ProxyModeHTTPS  ProxyMode = "https"
	ProxyModeSOCKS5 ProxyMode = "socks5"
)

type ProxyAuthority struct {
	CredentialRead    bool `json:"credential_read"`
	NetworkConnection bool `json:"network_connection"`
}

// ProxyConfig stores only a normalized endpoint and an optional system
// credential name. It never contains proxy userinfo or a plaintext secret.
type ProxyConfig struct {
	Mode                    ProxyMode      `json:"mode"`
	Host                    string         `json:"host,omitempty"`
	Port                    uint16         `json:"port,omitempty"`
	CredentialRef           string         `json:"credential_ref,omitempty"`
	ResolutionCheckRequired bool           `json:"resolution_check_required"`
	Authority               ProxyAuthority `json:"authority"`
}

type FeatureRequest struct {
	InterceptRequests       bool
	ModifyRequests          bool
	ReplayRequests          bool
	EditCookies             bool
	RelaxOriginPolicy       bool
	AllowInsecureContent    bool
	IgnoreCertificateErrors bool
}

type PlannedFeatures struct {
	DOMInspection           bool `json:"dom_inspection"`
	Screenshots             bool `json:"screenshots"`
	RequestCapture          bool `json:"request_capture"`
	RequestInterception     bool `json:"request_interception"`
	RequestMutation         bool `json:"request_mutation"`
	RequestReplay           bool `json:"request_replay"`
	CookieEditing           bool `json:"cookie_editing"`
	RelaxOriginPolicy       bool `json:"relax_origin_policy"`
	AllowInsecureContent    bool `json:"allow_insecure_content"`
	IgnoreCertificateErrors bool `json:"ignore_certificate_errors"`
}

type SessionIsolation struct {
	EphemeralProfile      bool `json:"ephemeral_profile"`
	ClearStorageOnClose   bool `json:"clear_storage_on_close"`
	SharedProfile         bool `json:"shared_profile"`
	PersonalProfile       bool `json:"personal_profile"`
	ExtensionsEnabled     bool `json:"extensions_enabled"`
	PasswordStoreEnabled  bool `json:"password_store_enabled"`
	HostFilesystemEnabled bool `json:"host_filesystem_enabled"`
	DownloadsQuarantined  bool `json:"downloads_quarantined"`
	ModelOwnsCleanup      bool `json:"model_owns_cleanup"`
}

type SessionPlan struct {
	ProtocolVersion    string           `json:"protocol_version"`
	SessionID          string           `json:"session_id"`
	RunID              string           `json:"run_id"`
	WorkspaceID        string           `json:"workspace_id"`
	ProfileID          ProfileID        `json:"profile_id"`
	ProfileFingerprint string           `json:"profile_fingerprint"`
	ProfileToken       string           `json:"profile_token"`
	Scope              TargetScope      `json:"scope"`
	Proxy              ProxyConfig      `json:"proxy"`
	Features           PlannedFeatures  `json:"features"`
	Isolation          SessionIsolation `json:"isolation"`
	Limits             ProfileLimits    `json:"limits"`
	// LimitsOverride is the operator-narrowed ceiling from the pinned project
	// config, when one was supplied. It is part of the fingerprint; the
	// verifier recomputes the narrowed limits from it and the profile. The
	// fingerprint is an unkeyed digest, so a tamperer who rewrites both the
	// override and the limits can re-sign them — but only back to the builtin
	// profile ceiling: narrowing-only validation never accepts a wider value,
	// and the plan is validated when it is built, not read back from
	// untrusted storage.
	LimitsOverride               *ProfileLimits   `json:"limits_override,omitempty"`
	EvidenceClass                string           `json:"evidence_class"`
	ApprovalRequired             bool             `json:"approval_required"`
	InstrumentedRiskAcknowledged bool             `json:"instrumented_risk_acknowledged"`
	RequiredBackend              string           `json:"required_backend"`
	StartBlocked                 bool             `json:"start_blocked"`
	BlockingGates                []string         `json:"blocking_gates"`
	Authority                    RuntimeAuthority `json:"authority"`
	Fingerprint                  string           `json:"fingerprint"`
}

type NewSessionPlanRequest struct {
	SessionID                    string
	RunID                        string
	WorkspaceID                  string
	ProfileID                    ProfileID
	Targets                      []string
	ProxyURL                     string
	ProxyCredentialRef           string
	Features                     FeatureRequest
	InstrumentedRiskAcknowledged bool
	LimitsOverride               *ProfileLimits
}

func NewProxyConfig(rawURL string, credentialRef string) (ProxyConfig, error) {
	rawURL = strings.TrimSpace(rawURL)
	credentialRef = strings.TrimSpace(credentialRef)
	if rawURL == "" {
		if credentialRef != "" {
			return ProxyConfig{}, errors.New("proxy credential reference requires a proxy endpoint")
		}
		return ProxyConfig{Mode: ProxyModeDirect}, nil
	}
	if len(rawURL) > 2048 || !utf8.ValidString(rawURL) || containsControl(rawURL) ||
		strings.Contains(rawURL, "\\") {
		return ProxyConfig{}, errors.New("proxy endpoint is malformed")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Opaque != "" || parsed.Host == "" || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ProxyConfig{}, errors.New("proxy endpoint must not contain credentials, path, query, or fragment")
	}
	mode := ProxyMode(strings.ToLower(parsed.Scheme))
	switch mode {
	case ProxyModeHTTP, ProxyModeHTTPS, ProxyModeSOCKS5:
	default:
		return ProxyConfig{}, fmt.Errorf("unsupported proxy scheme %q", parsed.Scheme)
	}
	host, err := normalizeHost(parsed.Hostname())
	if err != nil {
		return ProxyConfig{}, err
	}
	class, resolutionRequired := classifyHost(host)
	if class == HostClassBlocked {
		return ProxyConfig{}, errors.New("proxy endpoint uses an always-blocked host class")
	}
	port, err := proxyPort(mode, parsed.Port())
	if err != nil {
		return ProxyConfig{}, err
	}
	if credentialRef != "" && !credential.ValidName(credentialRef) {
		return ProxyConfig{}, errors.New("proxy credential reference is invalid")
	}
	config := ProxyConfig{Mode: mode, Host: host, Port: port,
		CredentialRef: credentialRef, ResolutionCheckRequired: resolutionRequired}
	if err := config.Validate(); err != nil {
		return ProxyConfig{}, err
	}
	return config, nil
}

func (config ProxyConfig) Validate() error {
	if config.Authority != (ProxyAuthority{}) {
		return errors.New("proxy configuration cannot grant credential or network authority")
	}
	if config.Mode == ProxyModeDirect {
		if config.Host != "" || config.Port != 0 || config.CredentialRef != "" ||
			config.ResolutionCheckRequired {
			return errors.New("direct proxy configuration contains endpoint state")
		}
		return nil
	}
	switch config.Mode {
	case ProxyModeHTTP, ProxyModeHTTPS, ProxyModeSOCKS5:
	default:
		return fmt.Errorf("unsupported proxy mode %q", config.Mode)
	}
	host, err := normalizeHost(config.Host)
	if err != nil || host != config.Host || config.Port == 0 {
		return errors.New("proxy configuration is not canonical")
	}
	class, resolutionRequired := classifyHost(host)
	if class == HostClassBlocked || resolutionRequired != config.ResolutionCheckRequired {
		return errors.New("proxy configuration host class is invalid")
	}
	if config.CredentialRef != "" && !credential.ValidName(config.CredentialRef) {
		return errors.New("proxy credential reference is invalid")
	}
	return nil
}

func (config ProxyConfig) AuthorizeResolvedAddress(rawAddress string) AddressDecision {
	if err := config.Validate(); err != nil || config.Mode == ProxyModeDirect {
		return AddressDecision{Code: "invalid_proxy", Class: HostClassBlocked}
	}
	address, err := netip.ParseAddr(strings.TrimSpace(rawAddress))
	if err != nil || address.Zone() != "" {
		return AddressDecision{Code: "invalid_address", Class: HostClassBlocked}
	}
	address = address.Unmap()
	class := classifyAddress(address)
	if class == HostClassBlocked {
		return AddressDecision{Code: "blocked_address", Class: class}
	}
	if literal, literalErr := netip.ParseAddr(config.Host); literalErr == nil && literal.Unmap() != address {
		return AddressDecision{Code: "literal_address_mismatch", Class: class}
	}
	return AddressDecision{Allowed: true, Code: "allowed", Class: class}
}

func BuildSessionPlan(request NewSessionPlanRequest) (SessionPlan, error) {
	request.SessionID = strings.TrimSpace(request.SessionID)
	request.RunID = strings.TrimSpace(request.RunID)
	request.WorkspaceID = strings.TrimSpace(request.WorkspaceID)
	for label, value := range map[string]string{
		"session id": request.SessionID, "Run id": request.RunID, "Workspace id": request.WorkspaceID,
	} {
		if !validPlanIdentity(value) {
			return SessionPlan{}, fmt.Errorf("browser %s is invalid", label)
		}
	}
	profile, ok := BuiltinRegistry().Lookup(request.ProfileID)
	if !ok {
		return SessionPlan{}, fmt.Errorf("unsupported browser profile %q", request.ProfileID)
	}
	if request.InstrumentedRiskAcknowledged != (request.ProfileID == ProfileCTFInstrumented) {
		return SessionPlan{}, errors.New("ctf-instrumented requires an explicit, profile-specific risk acknowledgement")
	}
	scope, err := NewTargetScope(request.ProfileID, request.Targets)
	if err != nil {
		return SessionPlan{}, err
	}
	proxy, err := NewProxyConfig(request.ProxyURL, request.ProxyCredentialRef)
	if err != nil {
		return SessionPlan{}, err
	}
	if proxy.Mode != ProxyModeDirect && !profile.Network.AllowProxy {
		return SessionPlan{}, errors.New("browser profile does not allow a proxy")
	}
	features, err := planFeatures(profile, request.Features)
	if err != nil {
		return SessionPlan{}, err
	}
	limits := profile.Limits
	if request.LimitsOverride != nil {
		if err := validateLimitsOverride(profile.Limits, *request.LimitsOverride); err != nil {
			return SessionPlan{}, err
		}
		limits = narrowProfileLimits(profile.Limits, *request.LimitsOverride)
	}
	profileFingerprint, err := ProfileFingerprint(profile)
	if err != nil {
		return SessionPlan{}, err
	}
	requiredBackend := "isolated_browser_worker"
	if profile.Isolation.ContainerRequired {
		requiredBackend = "containerized_browser_worker"
	}
	plan := SessionPlan{
		ProtocolVersion: SessionPlanProtocolVersion, SessionID: request.SessionID,
		RunID: request.RunID, WorkspaceID: request.WorkspaceID, ProfileID: profile.ID,
		ProfileFingerprint: profileFingerprint, Scope: scope, Proxy: proxy, Features: features,
		Isolation: fixedSessionIsolation(), Limits: limits,
		LimitsOverride: request.LimitsOverride,
		EvidenceClass:  profile.EvidenceClass, ApprovalRequired: profile.ApprovalRequired,
		InstrumentedRiskAcknowledged: request.InstrumentedRiskAcknowledged,
		RequiredBackend:              requiredBackend, StartBlocked: true,
		BlockingGates: blockingGates(profile),
	}
	plan.ProfileToken = profileToken(plan)
	plan.Fingerprint, err = sessionPlanFingerprint(plan)
	if err != nil {
		return SessionPlan{}, err
	}
	if err := plan.Validate(); err != nil {
		return SessionPlan{}, err
	}
	return plan, nil
}

func (plan SessionPlan) Validate() error {
	if plan.ProtocolVersion != SessionPlanProtocolVersion {
		return fmt.Errorf("unsupported browser session plan protocol %q", plan.ProtocolVersion)
	}
	for label, value := range map[string]string{
		"session id": plan.SessionID, "Run id": plan.RunID, "Workspace id": plan.WorkspaceID,
	} {
		if !validPlanIdentity(value) {
			return fmt.Errorf("browser %s is invalid", label)
		}
	}
	profile, ok := BuiltinRegistry().Lookup(plan.ProfileID)
	if !ok {
		return fmt.Errorf("unsupported browser profile %q", plan.ProfileID)
	}
	profileFingerprint, err := ProfileFingerprint(profile)
	if err != nil || plan.ProfileFingerprint != profileFingerprint {
		return errors.New("browser session profile fingerprint mismatch")
	}
	if err := plan.Scope.Validate(); err != nil || plan.Scope.ProfileID != plan.ProfileID ||
		plan.Scope.ProfileFingerprint != plan.ProfileFingerprint {
		return errors.New("browser session target scope does not match its profile")
	}
	if err := plan.Proxy.Validate(); err != nil {
		return err
	}
	if err := validatePlannedFeatures(profile, plan.Features); err != nil {
		return err
	}
	if plan.Isolation != fixedSessionIsolation() {
		return errors.New("browser session isolation controls were changed")
	}
	expectedBackend := "isolated_browser_worker"
	if profile.Isolation.ContainerRequired {
		expectedBackend = "containerized_browser_worker"
	}
	expectedLimits := profile.Limits
	if plan.LimitsOverride != nil {
		if err := validateLimitsOverride(profile.Limits, *plan.LimitsOverride); err != nil {
			return err
		}
		expectedLimits = narrowProfileLimits(profile.Limits, *plan.LimitsOverride)
	}
	if plan.Limits != expectedLimits || plan.EvidenceClass != profile.EvidenceClass ||
		plan.ApprovalRequired != profile.ApprovalRequired ||
		plan.InstrumentedRiskAcknowledged != (plan.ProfileID == ProfileCTFInstrumented) ||
		plan.RequiredBackend != expectedBackend {
		return errors.New("browser session controls do not match the selected profile")
	}
	if !plan.StartBlocked || plan.Authority != (RuntimeAuthority{}) ||
		!reflect.DeepEqual(plan.BlockingGates, blockingGates(profile)) {
		return errors.New("browser session plan cannot grant launch authority")
	}
	if plan.ProfileToken != profileToken(plan) {
		return errors.New("browser session disposable-profile token mismatch")
	}
	expectedFingerprint, err := sessionPlanFingerprint(plan)
	if err != nil || plan.Fingerprint != expectedFingerprint {
		return errors.New("browser session plan fingerprint mismatch")
	}
	return nil
}

func planFeatures(profile ProfileDescriptor, request FeatureRequest) (PlannedFeatures, error) {
	features := PlannedFeatures{
		DOMInspection: true, Screenshots: true, RequestCapture: true,
		RequestInterception: request.InterceptRequests || request.ModifyRequests ||
			request.ReplayRequests || request.EditCookies,
		RequestMutation: request.ModifyRequests, RequestReplay: request.ReplayRequests,
		CookieEditing: request.EditCookies, RelaxOriginPolicy: request.RelaxOriginPolicy,
		AllowInsecureContent:    request.AllowInsecureContent,
		IgnoreCertificateErrors: request.IgnoreCertificateErrors,
	}
	if err := validatePlannedFeatures(profile, features); err != nil {
		return PlannedFeatures{}, err
	}
	return features, nil
}

func validatePlannedFeatures(profile ProfileDescriptor, features PlannedFeatures) error {
	if !features.DOMInspection || !features.Screenshots || !features.RequestCapture {
		return errors.New("browser session lost baseline inspection features")
	}
	if features.RequestInterception && !profile.Tools.RequestInterception ||
		features.RequestMutation && !profile.Tools.RequestMutation ||
		features.RequestReplay && !profile.Tools.RequestReplay ||
		features.CookieEditing && !profile.Tools.CookieEditing {
		return errors.New("browser session requests a tool feature outside its profile")
	}
	if (features.RequestMutation || features.RequestReplay || features.CookieEditing) &&
		!features.RequestInterception {
		return errors.New("browser mutation, replay, and cookie editing require interception")
	}
	if features.RelaxOriginPolicy && !profile.Security.MayRelaxOriginPolicy ||
		features.AllowInsecureContent && !profile.Security.MayRelaxMixedContent ||
		features.IgnoreCertificateErrors && !profile.Security.MayRelaxCertificateErrors {
		return errors.New("browser session requests a security relaxation outside its profile")
	}
	return nil
}

func fixedSessionIsolation() SessionIsolation {
	return SessionIsolation{
		EphemeralProfile: true, ClearStorageOnClose: true,
		DownloadsQuarantined: true,
	}
}

func blockingGates(profile ProfileDescriptor) []string {
	gates := []string{
		"append_only_browser_audit", "browser_executable_identity",
		"browser_process_tree_lifecycle", "browser_runtime_sandbox",
		"bounded_artifact_handoff", "exact_network_scope_enforcement",
	}
	if profile.ApprovalRequired {
		gates = append(gates, "operator_scoped_approval")
	}
	if profile.Isolation.ContainerRequired {
		gates = append(gates, "container_runtime_isolation")
	}
	sort.Strings(gates)
	return gates
}

// narrowProfileLimits applies operator narrowing to the builtin profile
// limits: every positive override field takes min(base, override); zero
// fields keep the profile value. Narrowing-only is enforced by
// validateLimitsOverride before this runs.
func narrowProfileLimits(base, override ProfileLimits) ProfileLimits {
	narrowed := base
	if override.MaxOrigins > 0 && override.MaxOrigins < narrowed.MaxOrigins {
		narrowed.MaxOrigins = override.MaxOrigins
	}
	if override.MaxURLBytes > 0 && override.MaxURLBytes < narrowed.MaxURLBytes {
		narrowed.MaxURLBytes = override.MaxURLBytes
	}
	if override.MaxRequests > 0 && override.MaxRequests < narrowed.MaxRequests {
		narrowed.MaxRequests = override.MaxRequests
	}
	if override.MaxResponseBytes > 0 && override.MaxResponseBytes < narrowed.MaxResponseBytes {
		narrowed.MaxResponseBytes = override.MaxResponseBytes
	}
	if override.MaxDownloadBytes > 0 && override.MaxDownloadBytes < narrowed.MaxDownloadBytes {
		narrowed.MaxDownloadBytes = override.MaxDownloadBytes
	}
	if override.TimeoutMS > 0 && override.TimeoutMS < narrowed.TimeoutMS {
		narrowed.TimeoutMS = override.TimeoutMS
	}
	return narrowed
}

// validateLimitsOverride rejects negative fields and any field that would
// widen the builtin profile ceiling; zero fields mean "keep the profile
// value". A value exactly equal to the ceiling is accepted as a no-op —
// unlike budget.max_turns, which requires a strictly lower bound — because
// the browser ceiling is the profile itself and equal budgets neither widen
// nor narrow anything.
func validateLimitsOverride(base, override ProfileLimits) error {
	fields := []struct {
		name           string
		override, base int
	}{
		{"max_origins", override.MaxOrigins, base.MaxOrigins},
		{"max_url_bytes", override.MaxURLBytes, base.MaxURLBytes},
		{"max_requests", override.MaxRequests, base.MaxRequests},
		{"max_response_bytes", override.MaxResponseBytes, base.MaxResponseBytes},
		{"max_download_bytes", override.MaxDownloadBytes, base.MaxDownloadBytes},
		{"timeout_ms", override.TimeoutMS, base.TimeoutMS},
	}
	for _, field := range fields {
		if field.override < 0 {
			return fmt.Errorf("browser limits override %s cannot be negative", field.name)
		}
		if field.override > field.base {
			return fmt.Errorf("browser limits override %s must narrow the profile ceiling (%d)", field.name, field.base)
		}
	}
	return nil
}

func profileToken(plan SessionPlan) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		"browser-profile-token.v1", plan.SessionID, plan.RunID, plan.WorkspaceID,
		plan.ProfileFingerprint, plan.Scope.Fingerprint,
	}, "\x00")))
	return hex.EncodeToString(digest[:])
}

func sessionPlanFingerprint(plan SessionPlan) (string, error) {
	copyValue := plan
	copyValue.Fingerprint = ""
	raw, err := json.Marshal(copyValue)
	if err != nil {
		return "", fmt.Errorf("encode browser session plan: %w", err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func proxyPort(mode ProxyMode, rawPort string) (uint16, error) {
	if rawPort == "" {
		switch mode {
		case ProxyModeHTTP:
			return 80, nil
		case ProxyModeHTTPS:
			return 443, nil
		case ProxyModeSOCKS5:
			return 1080, nil
		}
	}
	value, err := strconv.Atoi(rawPort)
	if err != nil || value <= 0 || value > 65535 {
		return 0, errors.New("proxy port is invalid")
	}
	return uint16(value), nil
}

func validPlanIdentity(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) ||
		utf8.RuneCountInString(value) > 128 || strings.ContainsRune(value, 0) {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) {
			return false
		}
	}
	return true
}
