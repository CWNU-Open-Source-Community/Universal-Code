package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/mcpoauth"
)

// Optional browser fixture uses the production API and application service with
// an isolated SQLite database, synthetic issuer and encrypted credential files.
func TestMCPOAuthHTTPAndBrowserFixture(t *testing.T) {
	for _, mode := range []string{"metadata_url", "auto", "confidential"} {
		t.Run(mode, func(t *testing.T) { testMCPOAuthHTTPAndBrowserFixture(t, mode) })
	}
}
func testMCPOAuthHTTPAndBrowserFixture(t *testing.T, mode string) {
	access := strings.Repeat("http-fixture-private-access-", 128)
	refresh := strings.Repeat("http-fixture-private-refresh-", 128)
	var mcpRequests atomic.Int32
	var issuer *httptest.Server
	issuer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource/mcp":
			json.NewEncoder(w).Encode(map[string]any{"resource": issuer.URL + "/mcp", "authorization_servers": []string{issuer.URL}})
		case "/.well-known/oauth-authorization-server":
			json.NewEncoder(w).Encode(map[string]any{"issuer": issuer.URL, "authorization_endpoint": issuer.URL + "/authorize", "token_endpoint": issuer.URL + "/token", "code_challenge_methods_supported": []string{"S256"}, "response_types_supported": []string{"code"}, "authorization_response_iss_parameter_supported": true, "client_id_metadata_document_supported": true, "registration_endpoint": issuer.URL + "/register", "token_endpoint_auth_methods_supported": []string{"client_secret_post", "none"}})
		case "/register":
			var metadata map[string]any
			if json.NewDecoder(r.Body).Decode(&metadata) != nil {
				t.Error("bad DCR metadata")
			}
			metadata["client_id"] = "dynamic-fixture"
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(metadata)
		case "/authorize":
			query := url.Values{"state": {r.URL.Query().Get("state")}, "code": {"local-fixture-code"}, "iss": {issuer.URL}}
			callback := r.URL.Query().Get("redirect_uri") + "?" + query.Encode()
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<!doctype html><title>Local OAuth issuer</title><h1>Local TLS OAuth issuer</h1><p>Synthetic account: fixture only</p><a href="`+html.EscapeString(callback)+`">Approve fixture access</a>`)
		case "/mcp":
			mcpRequests.Add(1)
			if r.Header.Get("Authorization") != "Bearer "+access {
				http.Error(w, "unauthorized", 401)
				return
			}
			var frame mcp.Envelope
			if json.NewDecoder(r.Body).Decode(&frame) != nil {
				http.Error(w, "invalid request", 400)
				return
			}
			if frame.Method == "notifications/initialized" {
				w.WriteHeader(202)
				return
			}
			var result any
			switch frame.Method {
			case "initialize":
				result = map[string]any{"protocolVersion": mcp.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "oauth-fixture", "version": "1"}}
			case "tools/list":
				result = map[string]any{"tools": []map[string]any{{"name": "echo", "description": access, "inputSchema": map[string]any{"type": "object"}}}}
			default:
				http.Error(w, "unexpected MCP method", 400)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": frame.ID, "result": result})
		case "/token":
			r.ParseForm()
			if mode == "confidential" && r.Form.Get("client_secret") != "http-fixture-private-client-secret" {
				t.Error("missing confidential client secret")
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": refresh, "token_type": "Bearer", "expires_in": 3600})
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuer.Close()
	// All clients in this non-parallel test trust only the fixture's TLS roots.
	prior := http.DefaultTransport
	http.DefaultTransport = issuer.Client().Transport
	defer func() { http.DefaultTransport = prior }()
	f, controller, _, _ := newExtensionOnboardingFixture(t)
	var keys credential.Store = credential.NewMemoryStore()
	if os.Getenv("CYBERAGENT_OAUTH_BROWSER_SYSTEM_STORE") == "1" {
		keys = credential.NewSystemStore()
		if !keys.Available() {
			t.Fatal("system credential store unavailable")
		}
	}
	owned := mcpoauth.Store{Dir: t.TempDir(), KeyStore: keys}
	controller.WithCredentials(owned)
	registration := extensionRegistrationRequest(f)
	registration.Descriptor.Target = issuer.URL + "/mcp"
	registration.Descriptor.CredentialRef = "oauth-http-fixture-" + strings.ToLower(rand.Text())
	t.Cleanup(func() {
		if err := owned.Delete(context.Background(), registration.Descriptor.CredentialRef); err != nil {
			t.Error("could not remove fixture credential")
		}
	})
	var registered ExtensionMCPRegistrationView
	decodeDataStatus(t, extensionOnboardingRequest(t, f.api, ExtensionMCPRegistrationPath, registration), http.StatusAccepted, &registered)
	d := registered.Server
	binding := MCPCredentialBindingView{ServerID: d.ID, WorkspaceID: d.WorkspaceID, RunID: d.RunID, ExpectedDescriptorFingerprint: d.DescriptorFingerprint, Target: d.Target, CredentialRef: d.CredentialRef}
	if output := os.Getenv("CYBERAGENT_OAUTH_BROWSER_FIXTURE"); output != "" {
		api := httptest.NewServer(f.api)
		defer api.Close()
		raw, _ := json.Marshal(map[string]any{"api_url": api.URL, "issuer": issuer.URL, "server": registered.Server, "read_token": testAccessToken, "control_token": testControlToken})
		if err := os.WriteFile(output, raw, 0600); err != nil {
			t.Fatal(err)
		}
		t.Log("OAuth browser fixture ready")
		timeout := time.NewTimer(15 * time.Minute)
		defer timeout.Stop()
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-timeout.C:
				t.Fatal("browser fixture expired")
			case <-ticker.C:
				if _, err := os.Stat(output + ".stop"); err == nil {
					return
				}
			}
		}
	}
	var status MCPCredentialStatusView
	decodeData(t, f.get(t, mcpCredentialStatusPath(binding)), &status)
	request := MCPCredentialRequestView{Version: application.MCPCredentialProtocolVersion, Binding: binding, Action: "oauth-start", Confirm: true, ExpectedReferenceFingerprint: status.ReferenceFingerprint, OAuthIssuer: issuer.URL, OAuthClientID: "https://client.example/metadata.json", OAuthClientIDMode: "metadata_url"}
	if mode == "auto" {
		request.OAuthClientID = ""
		request.OAuthClientIDMode = "auto"
	}
	if mode == "confidential" {
		request.OAuthClientID = "private-fixture"
		request.OAuthClientIDMode = "pre_registered"
		request.OAuthClientSecret = "http-fixture-private-client-secret"
	}
	path := "/api/v1/extensions/mcp/" + binding.ServerID + "/credential"
	// The new actions retain the existing control-token gate.
	unauthorized := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(extensionJSON(t, request))))
	unauthorized.Header.Set("Authorization", "Bearer "+testAccessToken)
	unauthorized.Header.Set("Content-Type", "application/json")
	denied := httptest.NewRecorder()
	f.api.ServeHTTP(denied, unauthorized)
	if denied.Code < 400 {
		t.Fatal("read token started OAuth")
	}
	decodeData(t, extensionOnboardingRequest(t, f.api, path, request), &status)
	if status.OAuth == nil || status.OAuth.AuthorizationURL == "" || status.OAuth.FlowID == "" {
		t.Fatal("missing control handoff")
	}
	var polled MCPCredentialStatusView
	decodeData(t, f.get(t, mcpCredentialStatusPath(binding)), &polled)
	if polled.OAuth.AuthorizationURL != "" {
		t.Fatal("read token received OAuth state URL")
	}
	authURL, _ := url.Parse(status.OAuth.AuthorizationURL)
	callback := authURL.Query().Get("redirect_uri") + "?" + url.Values{"state": {authURL.Query().Get("state")}, "code": {"fixture-code"}, "iss": {issuer.URL}}.Encode()
	response, err := http.Get(callback)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		decodeData(t, f.get(t, mcpCredentialStatusPath(binding)), &polled)
		if polled.Configured {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("callback did not store credentials")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if polled.OAuth.State != "authorized" {
		t.Fatal(polled.OAuth.State)
	}
	raw, _ := json.Marshal(polled)
	if strings.Contains(string(raw), "private-access") || strings.Contains(string(raw), "private-refresh") || strings.Contains(string(raw), "private-client-secret") {
		t.Fatal("secret escaped HTTP")
	}
	if mcpRequests.Load() != 0 {
		t.Fatal("OAuth login dispatched MCP without review")
	}
	manager, err := mcp.NewClientManager(f.store, owned, mcp.ManagerOptions{HTTPClient: issuer.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Refresh(t.Context(), binding.ServerID); err == nil {
		t.Fatal("OAuth bypassed discovery review")
	}
	if mcpRequests.Load() != 0 {
		t.Fatal("unreviewed MCP reached network")
	}
	if _, err = manager.Review(t.Context(), binding.ServerID, mcp.ReviewRequest{Action: mcp.ReviewApproveDiscovery, ExpectedDescriptorFingerprint: binding.ExpectedDescriptorFingerprint, ReviewedBy: "operator-fixture"}); err != nil {
		t.Fatal(err)
	}
	discovered, err := manager.Refresh(t.Context(), binding.ServerID)
	if err != nil || len(discovered.Capabilities.Tools) != 1 || strings.Contains(discovered.Capabilities.Tools[0].Description, "http-fixture-private-access") {
		t.Fatalf("OAuth MCP discovery or redaction failed: %v", err)
	}
	request.Action = "delete"
	request.OAuthIssuer = ""
	request.OAuthClientID = ""
	request.OAuthClientIDMode = ""
	request.OAuthClientSecret = ""
	decodeData(t, extensionOnboardingRequest(t, f.api, path, request), &polled)
	if polled.Configured {
		t.Fatal("OAuth credential removal failed")
	}
}
