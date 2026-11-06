package browserruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

const TargetScopeProtocolVersion = "browser_target_scope.v1"

type HostClass string

const (
	HostClassPublicName HostClass = "public_name"
	HostClassPublicIP   HostClass = "public_ip"
	HostClassLoopback   HostClass = "loopback"
	HostClassPrivate    HostClass = "private"
	HostClassBlocked    HostClass = "blocked"
)

type Origin struct {
	Scheme                  string    `json:"scheme"`
	Host                    string    `json:"host"`
	Port                    uint16    `json:"port"`
	HostClass               HostClass `json:"host_class"`
	ResolutionCheckRequired bool      `json:"resolution_check_required"`
}

func (origin Origin) String() string {
	return origin.Scheme + "://" + net.JoinHostPort(origin.Host, strconv.Itoa(int(origin.Port)))
}

type ScopeAuthority struct {
	NetworkAccess bool `json:"network_access"`
}

type TargetScope struct {
	ProtocolVersion             string         `json:"protocol_version"`
	ProfileID                   ProfileID      `json:"profile_id"`
	ProfileFingerprint          string         `json:"profile_fingerprint"`
	Origins                     []Origin       `json:"origins"`
	DefaultDeny                 bool           `json:"default_deny"`
	RedirectRevalidation        bool           `json:"redirect_revalidation"`
	ResolvedAddressRevalidation bool           `json:"resolved_address_revalidation"`
	Authority                   ScopeAuthority `json:"authority"`
	Fingerprint                 string         `json:"fingerprint"`
	// Zero keeps the builtin ceiling. Omission preserves existing v1 scope
	// fingerprints; narrowed bounds travel with the scope to CDP request checks.
	MaxOrigins  int `json:"max_origins,omitempty"`
	MaxURLBytes int `json:"max_url_bytes,omitempty"`
}

type NavigationDecision struct {
	Allowed                 bool
	Code                    string
	CanonicalURL            string
	Origin                  Origin
	ResolutionCheckRequired bool
}

type AddressDecision struct {
	Allowed bool
	Code    string
	Class   HostClass
}

func NewTargetScope(profileID ProfileID, rawTargets []string) (TargetScope, error) {
	profile, ok := BuiltinRegistry().Lookup(profileID)
	if !ok {
		return TargetScope{}, fmt.Errorf("unsupported browser profile %q", profileID)
	}
	return newTargetScope(profile, profile.Limits, rawTargets)
}

func newTargetScope(profile ProfileDescriptor, limits ProfileLimits, rawTargets []string) (TargetScope, error) {
	if len(rawTargets) == 0 || len(rawTargets) > limits.MaxOrigins {
		return TargetScope{}, fmt.Errorf("browser target scope requires between 1 and %d targets",
			limits.MaxOrigins)
	}

	unique := make(map[string]Origin, len(rawTargets))
	for _, rawTarget := range rawTargets {
		origin, _, err := parseTarget(profile, limits, rawTarget)
		if err != nil {
			return TargetScope{}, err
		}
		unique[origin.String()] = origin
	}
	origins := make([]Origin, 0, len(unique))
	for _, origin := range unique {
		origins = append(origins, origin)
	}
	sort.Slice(origins, func(left, right int) bool { return origins[left].String() < origins[right].String() })

	profileFingerprint, err := ProfileFingerprint(profile)
	if err != nil {
		return TargetScope{}, err
	}
	scope := TargetScope{
		ProtocolVersion: TargetScopeProtocolVersion, ProfileID: profile.ID,
		ProfileFingerprint: profileFingerprint, Origins: origins, DefaultDeny: true,
		RedirectRevalidation: true, ResolvedAddressRevalidation: true,
	}
	if limits.MaxOrigins != profile.Limits.MaxOrigins {
		scope.MaxOrigins = limits.MaxOrigins
	}
	if limits.MaxURLBytes != profile.Limits.MaxURLBytes {
		scope.MaxURLBytes = limits.MaxURLBytes
	}
	scope.Fingerprint, err = targetScopeFingerprint(scope)
	if err != nil {
		return TargetScope{}, err
	}
	if err := scope.Validate(); err != nil {
		return TargetScope{}, err
	}
	return scope, nil
}

func (scope TargetScope) Validate() error {
	if scope.ProtocolVersion != TargetScopeProtocolVersion {
		return fmt.Errorf("unsupported browser target scope protocol %q", scope.ProtocolVersion)
	}
	profile, ok := BuiltinRegistry().Lookup(scope.ProfileID)
	if !ok {
		return fmt.Errorf("unsupported browser profile %q", scope.ProfileID)
	}
	profileFingerprint, err := ProfileFingerprint(profile)
	if err != nil || scope.ProfileFingerprint != profileFingerprint {
		return errors.New("browser target scope profile fingerprint mismatch")
	}
	limits, err := scope.effectiveLimits(profile)
	if err != nil {
		return err
	}
	if len(scope.Origins) == 0 || len(scope.Origins) > limits.MaxOrigins {
		return errors.New("browser target scope origin count is out of bounds")
	}
	if !scope.DefaultDeny || !scope.RedirectRevalidation || !scope.ResolvedAddressRevalidation {
		return errors.New("browser target scope lost a mandatory revalidation boundary")
	}
	if scope.Authority != (ScopeAuthority{}) {
		return errors.New("browser target scope cannot grant network authority")
	}
	last := ""
	for _, origin := range scope.Origins {
		rebuilt, _, parseErr := parseTarget(profile, limits, origin.String())
		if parseErr != nil || rebuilt != origin {
			return errors.New("browser target scope contains a non-canonical origin")
		}
		current := origin.String()
		if last != "" && current <= last {
			return errors.New("browser target scope origins must be unique and sorted")
		}
		last = current
	}
	expected, err := targetScopeFingerprint(scope)
	if err != nil || scope.Fingerprint != expected {
		return errors.New("browser target scope fingerprint mismatch")
	}
	return nil
}

func (scope TargetScope) effectiveLimits(profile ProfileDescriptor) (ProfileLimits, error) {
	override := ProfileLimits{MaxOrigins: scope.MaxOrigins, MaxURLBytes: scope.MaxURLBytes}
	if err := validateLimitsOverride(profile.Limits, override); err != nil {
		return ProfileLimits{}, err
	}
	return narrowProfileLimits(profile.Limits, override), nil
}

func (scope TargetScope) AuthorizeNavigation(rawURL string) NavigationDecision {
	if err := scope.Validate(); err != nil {
		return NavigationDecision{Code: "invalid_scope"}
	}
	profile, _ := BuiltinRegistry().Lookup(scope.ProfileID)
	limits, _ := scope.effectiveLimits(profile) // Validate checked the narrowed bounds.
	origin, canonicalURL, err := parseTarget(profile, limits, rawURL)
	if err != nil {
		return NavigationDecision{Code: "invalid_or_forbidden_url"}
	}
	for _, allowed := range scope.Origins {
		if origin == allowed {
			return NavigationDecision{
				Allowed: true, Code: "allowed", CanonicalURL: canonicalURL,
				Origin: origin, ResolutionCheckRequired: origin.ResolutionCheckRequired,
			}
		}
	}
	return NavigationDecision{Code: "origin_not_allowed"}
}

func (scope TargetScope) AuthorizeResolvedAddress(origin Origin, rawAddress string) AddressDecision {
	if err := scope.Validate(); err != nil {
		return AddressDecision{Code: "invalid_scope", Class: HostClassBlocked}
	}
	found := false
	for _, allowed := range scope.Origins {
		if allowed == origin {
			found = true
			break
		}
	}
	if !found {
		return AddressDecision{Code: "origin_not_allowed", Class: HostClassBlocked}
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
	profile, _ := BuiltinRegistry().Lookup(scope.ProfileID)

	if literal, literalErr := netip.ParseAddr(origin.Host); literalErr == nil {
		if literal.Unmap() != address {
			return AddressDecision{Code: "literal_address_mismatch", Class: class}
		}
		return AddressDecision{Allowed: true, Code: "allowed", Class: class}
	}
	if origin.HostClass == HostClassLoopback && class != HostClassLoopback {
		return AddressDecision{Code: "localhost_resolution_mismatch", Class: class}
	}
	switch class {
	case HostClassPublicIP:
		return AddressDecision{Allowed: true, Code: "allowed", Class: class}
	case HostClassPrivate:
		if profile.Network.AllowPrivateTargets {
			return AddressDecision{Allowed: true, Code: "allowed", Class: class}
		}
	case HostClassLoopback:
		if origin.HostClass == HostClassLoopback || profile.Network.AllowPrivateTargets {
			return AddressDecision{Allowed: true, Code: "allowed", Class: class}
		}
	}
	return AddressDecision{Code: "resolved_address_out_of_scope", Class: class}
}

func parseTarget(profile ProfileDescriptor, limits ProfileLimits, rawTarget string) (Origin, string, error) {
	rawTarget = strings.TrimSpace(rawTarget)
	if rawTarget == "" || len(rawTarget) > limits.MaxURLBytes ||
		!utf8.ValidString(rawTarget) || containsControl(rawTarget) || strings.Contains(rawTarget, "\\") {
		return Origin{}, "", errors.New("browser target URL is empty, oversized, or malformed")
	}
	parsed, err := url.Parse(rawTarget)
	if err != nil || parsed.Opaque != "" || parsed.Host == "" || parsed.User != nil ||
		parsed.Fragment != "" {
		return Origin{}, "", errors.New("browser target must be an absolute URL without credentials or a fragment")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return Origin{}, "", fmt.Errorf("browser target scheme %q is not allowed", parsed.Scheme)
	}
	host, err := normalizeHost(parsed.Hostname())
	if err != nil {
		return Origin{}, "", err
	}
	port, err := normalizePort(scheme, parsed.Port())
	if err != nil {
		return Origin{}, "", err
	}
	class, resolutionRequired := classifyHost(host)
	if class == HostClassBlocked {
		return Origin{}, "", errors.New("browser target resolves to an always-blocked host class")
	}
	if class == HostClassLoopback && !profile.Network.AllowLoopbackTargets {
		return Origin{}, "", errors.New("browser profile does not allow loopback targets")
	}
	if class == HostClassPrivate && !profile.Network.AllowPrivateTargets {
		return Origin{}, "", errors.New("browser profile does not allow private targets")
	}
	if (class == HostClassPublicIP || class == HostClassPublicName) &&
		!profile.Network.AllowPublicTargets {
		return Origin{}, "", errors.New("browser profile does not allow public targets")
	}
	origin := Origin{Scheme: scheme, Host: host, Port: port, HostClass: class,
		ResolutionCheckRequired: resolutionRequired}
	parsed.Scheme = scheme
	parsed.Host = net.JoinHostPort(host, strconv.Itoa(int(port)))
	parsed.User = nil
	canonicalURL := parsed.String()
	if len(canonicalURL) > limits.MaxURLBytes {
		return Origin{}, "", errors.New("canonical browser target URL exceeds its byte limit")
	}
	return origin, canonicalURL, nil
}

func normalizeHost(rawHost string) (string, error) {
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(rawHost)), ".")
	if host == "" || len(host) > 253 || strings.ContainsAny(host, "%*[]/\\") || containsControl(host) {
		return "", errors.New("browser target host is malformed")
	}
	if address, err := netip.ParseAddr(host); err == nil {
		return address.Unmap().String(), nil
	}
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil || ascii == "" || len(ascii) > 253 || strings.Contains(ascii, "..") {
		return "", errors.New("browser target host is not a valid DNS name")
	}
	return strings.ToLower(ascii), nil
}

func normalizePort(scheme string, rawPort string) (uint16, error) {
	if rawPort == "" {
		if scheme == "https" {
			return 443, nil
		}
		return 80, nil
	}
	value, err := strconv.Atoi(rawPort)
	if err != nil || value <= 0 || value > 65535 {
		return 0, errors.New("browser target port is invalid")
	}
	return uint16(value), nil
}

func classifyHost(host string) (HostClass, bool) {
	if isMetadataHostname(host) {
		return HostClassBlocked, true
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return HostClassLoopback, true
	}
	if address, err := netip.ParseAddr(host); err == nil {
		return classifyAddress(address.Unmap()), false
	}
	return HostClassPublicName, true
}

func classifyAddress(address netip.Addr) HostClass {
	if !address.IsValid() || address.IsUnspecified() || address.IsMulticast() ||
		address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || isMetadataAddress(address) ||
		reservedAddress(address) {
		return HostClassBlocked
	}
	if address.IsLoopback() {
		return HostClassLoopback
	}
	if address.IsPrivate() || sharedAddress(address) {
		return HostClassPrivate
	}
	return HostClassPublicIP
}

func isMetadataHostname(host string) bool {
	switch host {
	case "metadata.google.internal", "metadata.azure.internal", "instance-data.ec2.internal":
		return true
	default:
		return false
	}
}

func isMetadataAddress(address netip.Addr) bool {
	for _, raw := range []string{"169.254.169.254", "169.254.170.2", "100.100.100.200", "168.63.129.16", "fd00:ec2::254"} {
		if address == netip.MustParseAddr(raw) {
			return true
		}
	}
	return false
}

func sharedAddress(address netip.Addr) bool {
	return netip.MustParsePrefix("100.64.0.0/10").Contains(address) ||
		netip.MustParsePrefix("198.18.0.0/15").Contains(address)
}

func reservedAddress(address netip.Addr) bool {
	if !address.Is4() {
		return false
	}
	return netip.MustParsePrefix("0.0.0.0/8").Contains(address) ||
		netip.MustParsePrefix("240.0.0.0/4").Contains(address)
}

func containsControl(value string) bool {
	for _, current := range value {
		if unicode.IsControl(current) {
			return true
		}
	}
	return false
}

func targetScopeFingerprint(scope TargetScope) (string, error) {
	copyValue := scope
	copyValue.Fingerprint = ""
	raw, err := json.Marshal(copyValue)
	if err != nil {
		return "", fmt.Errorf("encode browser target scope: %w", err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}
