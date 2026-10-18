package mcpoauth

import (
	"context"
	"net/http"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
)

// This opt-in check makes only anonymous GET requests to Sentry's documented
// public MCP endpoint and metadata. It does not register a client, open a login
// or access an account. A proxy is injected for this test only.
func TestOAuthPublicMetadataInterop(t *testing.T) {
	if os.Getenv("CYBERAGENT_MCP_PUBLIC_METADATA_TEST") != "1" {
		t.Skip("set CYBERAGENT_MCP_PUBLIC_METADATA_TEST=1 for anonymous public metadata acceptance")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	defer transport.CloseIdleConnections()
	base := &http.Client{Transport: transport}
	const target, issuer = "https://mcp.sentry.dev/mcp", "https://mcp.sentry.dev"
	status, err := Discover(ctx, target, base)
	if err != nil || status.State != "discovered" || !slices.Contains(status.Issuers, issuer) {
		t.Fatal("Sentry protected-resource metadata could not be verified")
	}
	client := clientFor(base, func(r *http.Request) bool {
		return r.Method == http.MethodGet && sameOrigin(issuer, r.URL.String())
	})
	metadata, err := auth.GetAuthServerMetadata(ctx, issuer, client)
	if err != nil || metadata == nil || metadata.Issuer != issuer || !validHTTPS(metadata.AuthorizationEndpoint) || !validHTTPS(metadata.TokenEndpoint) ||
		!slices.Contains(metadata.CodeChallengeMethodsSupported, "S256") || !validHTTPS(metadata.RegistrationEndpoint) {
		t.Fatal("Sentry authorization-server metadata could not be verified")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("anonymous MCP request failed")
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatal("expected unauthenticated MCP connection to require authorization")
	}
	t.Log("Sentry public resource and issuer metadata verified; anonymous MCP request returned 401; no account login attempted")
}
