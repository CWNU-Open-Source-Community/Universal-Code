package browserruntime

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestAgentBrowserScopeAuthorizeResolvedAddresses(t *testing.T) {
	scope, err := NewAgentBrowserOriginScope([]string{"https://example.com", "http://localhost:3000"})
	if err != nil {
		t.Fatal(err)
	}
	public := Origin{Scheme: "https", Host: "example.com", Port: 443, HostClass: HostClassPublicName, ResolutionCheckRequired: true}
	loopback := Origin{Scheme: "http", Host: "localhost", Port: 3000, HostClass: HostClassLoopback}
	publicLiteral := Origin{Scheme: "https", Host: "93.184.216.34", Port: 443, HostClass: HostClassPublicIP}

	lookups := map[string][]string{
		"example.com":             {"93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"},
		"rebind.example.test":     {"93.184.216.34", "192.168.1.5"},
		"metadata.example.test":   {"169.254.169.254"},
		"loopback.example.test":   {"127.0.0.1"},
		"unresolved.example.test": nil,
	}
	lookup := func(_ context.Context, host string) ([]string, error) {
		if addresses, ok := lookups[host]; ok {
			return addresses, nil
		}
		return nil, errors.New("nxdomain")
	}

	if err := scope.authorizeResolved(context.Background(), public, lookup); err != nil {
		t.Fatalf("public addresses must pass: %v", err)
	}
	if err := scope.authorizeResolved(context.Background(), loopback, func(_ context.Context, _ string) ([]string, error) {
		return []string{"127.0.0.1"}, nil
	}); err != nil {
		t.Fatalf("loopback origin on loopback address must pass: %v", err)
	}
	err = scope.authorizeResolved(context.Background(), Origin{Scheme: "https", Host: "rebind.example.test", Port: 443, HostClass: HostClassPublicName, ResolutionCheckRequired: true}, lookup)
	var scopeErr *AgentBrowserScopeError
	if !errors.As(err, &scopeErr) || scopeErr.Code != AgentBrowserScopeResolvedOutOfScope {
		t.Fatalf("rebinding into a private range must be named: %v", err)
	}
	if !strings.Contains(scopeErr.Error(), "192.168.1.5") {
		t.Fatalf("refusal must name the forbidden address: %v", err)
	}
	err = scope.authorizeResolved(context.Background(), Origin{Scheme: "https", Host: "metadata.example.test", Port: 443, HostClass: HostClassPublicName, ResolutionCheckRequired: true}, lookup)
	if !errors.As(err, &scopeErr) || scopeErr.Code != AgentBrowserScopeResolvedOutOfScope {
		t.Fatalf("cloud metadata resolution must be refused: %v", err)
	}
	err = scope.authorizeResolved(context.Background(), Origin{Scheme: "https", Host: "unresolved.example.test", Port: 443, HostClass: HostClassPublicName, ResolutionCheckRequired: true}, lookup)
	if !errors.As(err, &scopeErr) || scopeErr.Code != AgentBrowserScopeResolutionFailed {
		t.Fatalf("unresolved name must fail closed with a named error: %v", err)
	}
	// A literal origin never consults the resolver; its declared address is
	// already exact, and a mismatching connection target is a boundary error.
	if err := scope.authorizeResolved(context.Background(), publicLiteral, func(_ context.Context, _ string) ([]string, error) {
		return []string{"93.184.216.34"}, nil
	}); err != nil {
		t.Fatalf("literal origin dispatch must not require resolution: %v", err)
	}
	if scope.authorizeResolvedAddress(publicLiteral, "93.184.216.33") {
		t.Fatal("mismatched literal address accepted")
	}
	if !scope.authorizeResolvedAddress(publicLiteral, "93.184.216.34") {
		t.Fatal("exact literal address refused")
	}
	if scope.authorizeResolvedAddress(loopback, "127.0.0.1") == false || scope.authorizeResolvedAddress(loopback, "10.0.0.1") {
		t.Fatal("loopback origin address classes inconsistent")
	}
}
