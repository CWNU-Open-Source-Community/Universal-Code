package browserruntime

import (
	"errors"
	"strings"
	"testing"
)

func TestAgentBrowserOriginScopeCanonicalizesAndDeduplicates(t *testing.T) {
	scope, err := NewAgentBrowserOriginScope([]string{
		"https://example.com/path", "http://localhost:3000", "HTTPS://EXAMPLE.COM:443/other",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(scope.Origins) != 2 {
		t.Fatalf("deduplicated origins: %+v", scope.Origins)
	}
	if scope.Origins[0].String() != "http://localhost:3000" || scope.Origins[1].String() != "https://example.com:443" {
		t.Fatalf("canonical sorted origins: %+v", scope.Origins)
	}
	if scope.ProtocolVersion != AgentBrowserOriginScopeProtocolVersion || len(scope.Fingerprint) != 64 {
		t.Fatalf("scope identity: %+v", scope)
	}
	sameSetDifferentOrder, err := NewAgentBrowserOriginScope([]string{"http://localhost:3000", "https://example.com"})
	if err != nil || sameSetDifferentOrder.Fingerprint != scope.Fingerprint {
		t.Fatalf("origin order must not change the fingerprint: %v %s", err, sameSetDifferentOrder.Fingerprint)
	}
	if err := scope.Validate(); err != nil {
		t.Fatalf("built scope must validate: %v", err)
	}
}

func TestAgentBrowserOriginScopeRejectsOutOfBoundsAndForbiddenTargets(t *testing.T) {
	if _, err := NewAgentBrowserOriginScope(nil); err == nil {
		t.Fatal("empty scope accepted")
	}
	tooMany := make([]string, 0, MaxAgentBrowserScopeOrigins+1)
	for index := 0; index <= MaxAgentBrowserScopeOrigins; index++ {
		tooMany = append(tooMany, "https://host-"+strings.Repeat("a", 8)+".test")
	}
	if _, err := NewAgentBrowserOriginScope(tooMany); err == nil {
		t.Fatal("origin count above the ceiling accepted")
	}
	for _, raw := range []string{
		"https://192.168.1.10", "http://10.0.0.5:8080", "http://metadata.google.internal",
		"file:///etc/passwd", "https://u:p@example.com", "https://example.com/#frag",
		"https://[fe80::1]", "http://169.254.169.254",
	} {
		if _, err := NewAgentBrowserOriginScope([]string{raw}); err == nil {
			t.Fatalf("forbidden scope target accepted: %s", raw)
		}
	}
}

func TestAgentBrowserOriginScopeValidateDetectsTampering(t *testing.T) {
	scope, err := NewAgentBrowserOriginScope([]string{"https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	tampered := scope
	tampered.Origins = append([]Origin(nil), scope.Origins...)
	tampered.Origins[0].Port = 8443
	if err := tampered.Validate(); err == nil || !strings.Contains(err.Error(), "fingerprint mismatch") && !strings.Contains(err.Error(), "non-canonical") {
		t.Fatalf("mutated origin must fail validation: %v", err)
	}
	// The fingerprint separates the declared set from a widened one even when
	// both are re-canonicalized from scratch.
	widenedFingerprint, err := agentBrowserScopeFingerprint(AgentBrowserOriginScope{
		ProtocolVersion: AgentBrowserOriginScopeProtocolVersion,
		Origins:         append(append([]Origin(nil), scope.Origins...), Origin{Scheme: "https", Host: "other.test", Port: 443}),
	})
	if err != nil || widenedFingerprint == scope.Fingerprint {
		t.Fatalf("widened origin set must fingerprint differently: %v %s", err, widenedFingerprint)
	}
	wrongProtocol := scope
	wrongProtocol.ProtocolVersion = "browser_target_scope.v1"
	if err := wrongProtocol.Validate(); err == nil {
		t.Fatal("foreign scope protocol accepted")
	}
}

func TestAgentBrowserScopeAuthorizeNavigationMatchesExactOrigins(t *testing.T) {
	scope, err := NewAgentBrowserOriginScope([]string{"https://example.com", "http://localhost:3000"})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"https://example.com/", "https://example.com/a/b?c=d", "http://localhost:3000/app",
		"HTTPS://EXAMPLE.COM", // canonicalization admits the same origin in any case form
	} {
		if err := scope.AuthorizeNavigation(raw); err != nil {
			t.Fatalf("in-scope navigation rejected: %s: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"https://example.com:8443/", "http://example.com/", "https://example.org/",
		"http://localhost:3001/app", "https://host.example.com.evil.test/",
	} {
		err := scope.AuthorizeNavigation(raw)
		var scopeErr *AgentBrowserScopeError
		if !errors.As(err, &scopeErr) || scopeErr.Code != AgentBrowserScopeOriginNotDeclared {
			t.Fatalf("out-of-scope navigation must name the missing origin: %s: %v", raw, err)
		}
		if !strings.Contains(scopeErr.Error(), "browser_origin_not_declared") {
			t.Fatalf("refusal is not a named origin error: %v", err)
		}
	}
	if err := scope.AuthorizeNavigation("https://u:p@example.com/"); err == nil {
		t.Fatal("credentialed URL accepted")
	}
}

func TestAgentBrowserScopeErrorRefusalsAreDeterministic(t *testing.T) {
	scopeErr := &AgentBrowserScopeError{Code: AgentBrowserScopeOriginNotDeclared, Origin: "https://example.com:8443"}
	if strings.Contains(scopeErr.Error(), "outcome_unknown") {
		t.Fatal("scope refusal must never read as an unknown-outcome action")
	}
	left := &AgentBrowserScopeError{Code: AgentBrowserScopeLeftOrigins, Origin: "https://evil.test/next"}
	if !strings.Contains(left.Error(), "browser_navigation_left_declared_origins") || !strings.Contains(left.Error(), "https://evil.test/next") {
		t.Fatalf("landing escape must name the page it left: %v", left)
	}
}
