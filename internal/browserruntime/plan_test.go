package browserruntime

import (
	"strings"
	"testing"
)

func TestSafeWebSessionPlanIsIsolatedAndNonAuthorizing(t *testing.T) {
	plan, err := BuildSessionPlan(NewSessionPlanRequest{
		SessionID: "browser-session-one", RunID: "run-one", WorkspaceID: "ws-demo",
		ProfileID: ProfileSafeWeb, Targets: []string{"https://example.com", "http://localhost:5173"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	if plan.Authority != (RuntimeAuthority{}) || !plan.StartBlocked ||
		!plan.Isolation.EphemeralProfile || !plan.Isolation.ClearStorageOnClose ||
		plan.Isolation.PersonalProfile || plan.Isolation.ExtensionsEnabled ||
		plan.Isolation.PasswordStoreEnabled || plan.Isolation.HostFilesystemEnabled ||
		!plan.Isolation.DownloadsQuarantined || plan.Isolation.ModelOwnsCleanup {
		t.Fatalf("unsafe session isolation or authority: %#v", plan)
	}
	if !plan.Features.DOMInspection || !plan.Features.Screenshots || !plan.Features.RequestCapture ||
		plan.Features.RequestMutation || plan.Features.RelaxOriginPolicy {
		t.Fatalf("unexpected safe-web features: %#v", plan.Features)
	}
	if plan.Proxy.Mode != ProxyModeDirect || plan.ApprovalRequired ||
		plan.RequiredBackend != "isolated_browser_worker" || len(plan.Fingerprint) != 64 ||
		len(plan.ProfileToken) != 64 {
		t.Fatalf("unexpected safe-web plan: %#v", plan)
	}
}

func TestCTFLabPlanSupportsBoundedInterceptionAndProxyReference(t *testing.T) {
	plan, err := BuildSessionPlan(NewSessionPlanRequest{
		SessionID: "browser-session-two", RunID: "run-two", WorkspaceID: "ws-ctf",
		ProfileID: ProfileCTFLab, Targets: []string{"http://192.168.56.20:8080"},
		ProxyURL: "socks5://127.0.0.1:9050", ProxyCredentialRef: "ctf-proxy",
		Features: FeatureRequest{ModifyRequests: true, ReplayRequests: true, EditCookies: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	if !plan.ApprovalRequired || !plan.Features.RequestInterception ||
		!plan.Features.RequestMutation || !plan.Features.RequestReplay || !plan.Features.CookieEditing {
		t.Fatalf("ctf-lab feature plan is incomplete: %#v", plan)
	}
	if plan.Proxy.Mode != ProxyModeSOCKS5 || plan.Proxy.Host != "127.0.0.1" ||
		plan.Proxy.Port != 9050 || plan.Proxy.CredentialRef != "ctf-proxy" ||
		plan.Proxy.Authority != (ProxyAuthority{}) {
		t.Fatalf("unexpected proxy projection: %#v", plan.Proxy)
	}
	if decision := plan.Proxy.AuthorizeResolvedAddress("127.0.0.1"); !decision.Allowed {
		t.Fatalf("exact proxy address rejected: %#v", decision)
	}
}

func TestInstrumentedPlanRequiresExplicitAcknowledgementAndContainer(t *testing.T) {
	request := NewSessionPlanRequest{
		SessionID: "browser-session-three", RunID: "run-three", WorkspaceID: "ws-ctf",
		ProfileID: ProfileCTFInstrumented, Targets: []string{"http://10.10.10.10"},
		Features: FeatureRequest{
			ModifyRequests: true, RelaxOriginPolicy: true,
			AllowInsecureContent: true, IgnoreCertificateErrors: true,
		},
	}
	if _, err := BuildSessionPlan(request); err == nil {
		t.Fatal("instrumented plan passed without acknowledgement")
	}
	request.InstrumentedRiskAcknowledged = true
	plan, err := BuildSessionPlan(request)
	if err != nil {
		t.Fatal(err)
	}
	if plan.EvidenceClass != "instrumented" || !plan.InstrumentedRiskAcknowledged ||
		plan.RequiredBackend != "containerized_browser_worker" ||
		!plan.Features.RelaxOriginPolicy || !plan.Features.AllowInsecureContent ||
		!plan.Features.IgnoreCertificateErrors || !containsGate(plan.BlockingGates, "container_runtime_isolation") {
		t.Fatalf("instrumented controls are incomplete: %#v", plan)
	}
}

func TestSafeWebRejectsCTFToolingAndSecurityRelaxations(t *testing.T) {
	requests := []FeatureRequest{
		{ModifyRequests: true}, {ReplayRequests: true}, {EditCookies: true},
		{RelaxOriginPolicy: true}, {AllowInsecureContent: true}, {IgnoreCertificateErrors: true},
	}
	for index, features := range requests {
		_, err := BuildSessionPlan(NewSessionPlanRequest{
			SessionID: "browser-safe-denial", RunID: "run-safe", WorkspaceID: "ws-safe",
			ProfileID: ProfileSafeWeb, Targets: []string{"https://example.com"}, Features: features,
		})
		if err == nil {
			t.Fatalf("unsafe feature request %d passed safe-web", index)
		}
	}
}

func TestProxyConfigurationRejectsSecretsAndBlockedDestinations(t *testing.T) {
	blocked := []struct {
		endpoint      string
		credentialRef string
	}{
		{"http://user:secret@127.0.0.1:8080", ""},
		{"http://169.254.169.254:8080", ""},
		{"http://metadata.google.internal:8080", ""},
		{"socks5://127.0.0.1:9050/path", ""},
		{"ftp://127.0.0.1:21", ""},
		{"", "orphan-credential"},
		{"http://127.0.0.1:8080", "not valid"},
	}
	for _, candidate := range blocked {
		if _, err := NewProxyConfig(candidate.endpoint, candidate.credentialRef); err == nil {
			t.Fatalf("unsafe proxy %q passed", candidate.endpoint)
		}
	}
}

func TestSessionPlanTamperingFailsClosed(t *testing.T) {
	base, err := BuildSessionPlan(NewSessionPlanRequest{
		SessionID: "browser-tamper", RunID: "run-tamper", WorkspaceID: "ws-tamper",
		ProfileID: ProfileCTFLab, Targets: []string{"http://127.0.0.1:8080"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mutations := []func(*SessionPlan){
		func(value *SessionPlan) { value.Authority.NetworkAccess = true },
		func(value *SessionPlan) { value.StartBlocked = false },
		func(value *SessionPlan) { value.Isolation.PersonalProfile = true },
		func(value *SessionPlan) { value.BlockingGates = value.BlockingGates[1:] },
		func(value *SessionPlan) { value.ProfileToken = "00" },
		func(value *SessionPlan) { value.Scope.Authority.NetworkAccess = true },
		func(value *SessionPlan) { value.Fingerprint = "00" },
	}
	for index, mutate := range mutations {
		candidate := base
		candidate.Scope.Origins = append([]Origin(nil), base.Scope.Origins...)
		candidate.BlockingGates = append([]string(nil), base.BlockingGates...)
		mutate(&candidate)
		if err := candidate.Validate(); err == nil {
			t.Fatalf("mutation %d unexpectedly passed", index)
		}
	}
}

func containsGate(gates []string, expected string) bool {
	for _, gate := range gates {
		if gate == expected {
			return true
		}
	}
	return false
}

func FuzzProxyConfiguration(f *testing.F) {
	for _, seed := range []string{
		"", "http://127.0.0.1:8080", "socks5://localhost:9050",
		"http://user:secret@proxy.invalid", "http://169.254.169.254:80",
		"file:///tmp/socket", "http://[::1]:8080", "\x00",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, rawEndpoint string) {
		config, err := NewProxyConfig(rawEndpoint, "")
		if err != nil {
			return
		}
		if err := config.Validate(); err != nil {
			t.Fatalf("accepted proxy failed reconstruction: %v", err)
		}
	})
}

func TestSessionPlanLimitsOverrideNarrowsAndEntersFingerprint(t *testing.T) {
	base, err := BuildSessionPlan(NewSessionPlanRequest{
		SessionID: "browser-session-one", RunID: "run-one", WorkspaceID: "ws-demo",
		ProfileID: ProfileSafeWeb, Targets: []string{"https://example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	narrowed, err := BuildSessionPlan(NewSessionPlanRequest{
		SessionID: "browser-session-one", RunID: "run-one", WorkspaceID: "ws-demo",
		ProfileID: ProfileSafeWeb, Targets: []string{"https://example.com"},
		LimitsOverride: &ProfileLimits{MaxRequests: 200, TimeoutMS: 180000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := narrowed.Validate(); err != nil {
		t.Fatal(err)
	}
	if narrowed.Limits.MaxRequests != 200 || narrowed.Limits.TimeoutMS != 180000 {
		t.Fatalf("narrowed fields not applied: %+v", narrowed.Limits)
	}
	if narrowed.Limits.MaxOrigins != base.Limits.MaxOrigins ||
		narrowed.Limits.MaxResponseBytes != base.Limits.MaxResponseBytes ||
		narrowed.Limits.MaxDownloadBytes != base.Limits.MaxDownloadBytes ||
		narrowed.Limits.MaxURLBytes != base.Limits.MaxURLBytes {
		t.Fatalf("unset override fields must keep profile values: %+v", narrowed.Limits)
	}
	// The fingerprint covers the whole plan (limits and override). Request
	// count and timeout overrides preserve the scope and profile token.
	if narrowed.Fingerprint == base.Fingerprint {
		t.Fatal("override must change the plan fingerprint")
	}
	if narrowed.ProfileToken != base.ProfileToken {
		t.Fatal("profile token must not depend on the budget override")
	}
	if narrowed.LimitsOverride == nil || narrowed.LimitsOverride.MaxRequests != 200 {
		t.Fatalf("override not carried on the plan: %+v", narrowed.LimitsOverride)
	}

	// Zero-field overrides keep every profile value.
	noOp, err := BuildSessionPlan(NewSessionPlanRequest{
		SessionID: "browser-session-one", RunID: "run-one", WorkspaceID: "ws-demo",
		ProfileID: ProfileSafeWeb, Targets: []string{"https://example.com"},
		LimitsOverride: &ProfileLimits{},
	})
	if err != nil || noOp.Limits != base.Limits {
		t.Fatalf("zero-field override: %v %+v", err, noOp.Limits)
	}

	// An override exactly equal to the profile ceiling is a legal no-op: unlike
	// budget.max_turns, the browser ceiling IS the profile itself, so equality
	// neither widens nor narrows.
	equal, err := BuildSessionPlan(NewSessionPlanRequest{
		SessionID: "browser-session-one", RunID: "run-one", WorkspaceID: "ws-demo",
		ProfileID: ProfileSafeWeb, Targets: []string{"https://example.com"},
		LimitsOverride: &ProfileLimits{MaxRequests: base.Limits.MaxRequests, TimeoutMS: base.Limits.TimeoutMS},
	})
	if err != nil || equal.Limits != base.Limits || equal.LimitsOverride == nil ||
		equal.LimitsOverride.MaxRequests != base.Limits.MaxRequests {
		t.Fatalf("ceiling-equal override: %v %+v", err, equal.Limits)
	}
	if equal.Fingerprint == base.Fingerprint {
		t.Fatal("ceiling-equal override must still enter the fingerprint")
	}
}

func TestSessionPlanLimitsOverrideRejectsWideningAndNegative(t *testing.T) {
	base := BuiltinRegistry()
	profile, ok := base.Lookup(ProfileSafeWeb)
	if !ok {
		t.Fatal("safe-web profile missing")
	}
	cases := []struct {
		name    string
		version ProfileLimits
		wantErr string
	}{
		{"widen requests", ProfileLimits{MaxRequests: profile.Limits.MaxRequests + 1}, "must narrow the profile ceiling"},
		{"widen timeout", ProfileLimits{TimeoutMS: profile.Limits.TimeoutMS + 1}, "must narrow the profile ceiling"},
		{"negative origins", ProfileLimits{MaxOrigins: -1}, "cannot be negative"},
	}
	for _, testCase := range cases {
		_, err := BuildSessionPlan(NewSessionPlanRequest{
			SessionID: "browser-session-one", RunID: "run-one", WorkspaceID: "ws-demo",
			ProfileID: ProfileSafeWeb, Targets: []string{"https://example.com"},
			LimitsOverride: &testCase.version,
		})
		if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
			t.Fatalf("%s: err=%v want %q", testCase.name, err, testCase.wantErr)
		}
	}
}

func TestSessionPlanLimitsOverrideTamperFailsClosed(t *testing.T) {
	narrowed, err := BuildSessionPlan(NewSessionPlanRequest{
		SessionID: "browser-session-one", RunID: "run-one", WorkspaceID: "ws-demo",
		ProfileID: ProfileSafeWeb, Targets: []string{"https://example.com"},
		LimitsOverride: &ProfileLimits{MaxRequests: 200},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, ok := BuiltinRegistry().Lookup(ProfileSafeWeb)
	if !ok {
		t.Fatal("safe-web profile missing")
	}
	// A tamperer widens the limits back to the profile values and re-signs the
	// fingerprint; the verifier must still catch the mismatch against the
	// narrowing the override authorizes.
	tampered := narrowed
	tampered.Limits = profile.Limits
	tampered.Fingerprint, err = sessionPlanFingerprint(tampered)
	if err != nil {
		t.Fatal(err)
	}
	if err := tampered.Validate(); err == nil ||
		!strings.Contains(err.Error(), "browser session controls do not match") {
		t.Fatalf("widened tamper accepted: %v", err)
	}
}
