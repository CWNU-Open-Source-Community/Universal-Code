package application

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/mcp"
)

func TestMCPOAuthServiceBindingCancellationAndSecretFreeStatus(t *testing.T) {
	var issuer *httptest.Server
	issuer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource/mcp":
			json.NewEncoder(w).Encode(map[string]any{"resource": issuer.URL + "/mcp", "authorization_servers": []string{issuer.URL}})
		case "/.well-known/oauth-authorization-server":
			json.NewEncoder(w).Encode(map[string]any{"issuer": issuer.URL, "authorization_endpoint": issuer.URL + "/authorize", "token_endpoint": issuer.URL + "/token", "code_challenge_methods_supported": []string{"S256"}, "response_types_supported": []string{"code"}})
		case "/token":
			io.WriteString(w, `{"access_token":"private-access-token","refresh_token":"private-refresh-token","token_type":"Bearer","expires_in":3600}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuer.Close()
	record := mcpCredentialRecord("oauth-server")
	record.Descriptor.Target = issuer.URL + "/mcp"
	record.DescriptorFingerprint = record.Descriptor.Fingerprint()
	registry := &mcpCredentialRegistryFake{[]mcp.ServerRecord{record}}
	owned := credential.NewMemoryStore()
	service := NewMCPCredentialService(registry, owned)
	service.oauthHTTP = issuer.Client()
	binding := mcpCredentialBinding(record)
	request := mcpCredentialChange(t, service, binding)
	request.Secret = ""
	request.Action = "oauth-discover"
	status, err := service.Change(t.Context(), request)
	if err != nil || status.OAuth == nil || len(status.OAuth.Issuers) != 1 {
		t.Fatalf("discovery: %+v %v", status, err)
	}
	request.Action = "oauth-start"
	request.OAuthIssuer = issuer.URL
	request.OAuthClientID = "public-client"
	start, err := service.Change(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer service.oauth[binding.CredentialRef].Cancel()
	if _, err = service.Change(t.Context(), request); err == nil {
		t.Fatal("concurrent login accepted")
	}
	status, err = service.Status(t.Context(), binding)
	if err != nil || status.OAuth.AuthorizationURL != "" || status.Configured {
		t.Fatal("read-only status disclosed handoff or claimed token")
	}
	authURL, _ := url.Parse(start.OAuth.AuthorizationURL)
	query := url.Values{"state": {authURL.Query().Get("state")}, "code": {"fixture-code"}, "iss": {issuer.URL}}
	response, err := http.Get(authURL.Query().Get("redirect_uri") + "?" + query.Encode())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	select {
	case <-service.oauth[binding.CredentialRef].Done():
	case <-time.After(5 * time.Second):
		t.Fatal("login timed out")
	}
	status, err = service.Status(t.Context(), binding)
	if err != nil || !status.Configured || status.OAuth.State != "authorized" {
		t.Fatalf("stored status = %+v %v", status, err)
	}
	raw, _ := json.Marshal(status)
	if strings.Contains(string(raw), "private-") || strings.Contains(string(raw), "code_verifier") || strings.Contains(string(raw), "authorization_url") {
		t.Fatal("secret in public status")
	}
	if registry.records[0].State != mcp.TrustStaged {
		t.Fatal("login changed MCP execution trust")
	}
	// Replacing with a static token cancels an in-flight OAuth transaction and
	// prevents its late completion from overwriting the explicit operator change.
	start, err = service.Change(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	pending := service.oauth[binding.CredentialRef]
	replacement := mcpCredentialChange(t, service, binding)
	replacement.Secret = "replacement-static-token"
	if _, err = service.Change(t.Context(), replacement); err != nil {
		t.Fatal(err)
	}
	<-pending.Done()
	stored, _, _ := owned.Get(t.Context(), binding.CredentialRef)
	if stored != replacement.Secret {
		t.Fatal("cancelled login replaced static token")
	}
	// A revoked registration can cancel pending work; its callback cannot save.
	request.Action = "oauth-start"
	_, err = service.Change(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	pending = service.oauth[binding.CredentialRef]
	// A different descriptor cannot borrow a pending flow's ID to cancel it.
	registry.records[0].Descriptor.Target += "/changed"
	registry.records[0].DescriptorFingerprint = registry.records[0].Descriptor.Fingerprint()
	changed := mcpCredentialChange(t, service, mcpCredentialBinding(registry.records[0]))
	changed.Action, changed.Secret, changed.OAuthFlowID = "oauth-cancel", "", pending.Status().FlowID
	if _, err = service.Change(t.Context(), changed); err == nil {
		t.Fatal("changed descriptor cancelled another binding's flow")
	}
	registry.records[0] = record
	registry.records[0].State = mcp.TrustRevoked
	request.OAuthFlowID = pending.Status().FlowID
	request.Action = "oauth-cancel"
	request.OAuthIssuer = ""
	request.OAuthClientID = ""
	if _, err = service.Change(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	<-pending.Done()
}
