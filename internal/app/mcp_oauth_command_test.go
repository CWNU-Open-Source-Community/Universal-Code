package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/store"
)

type oauthCLIOutput chan []byte

func (o oauthCLIOutput) Write(p []byte) (int, error) { o <- bytes.Clone(p); return len(p), nil }

func TestMCPOAuthCLIConfidentialClientAndFixedPort(t *testing.T) {
	const secret = "cli-private-client-secret"
	t.Setenv("MCP_TEST_CLIENT_SECRET", secret)
	var issuer *httptest.Server
	issuer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource/mcp":
			json.NewEncoder(w).Encode(map[string]any{"resource": issuer.URL + "/mcp", "authorization_servers": []string{issuer.URL}})
		case "/.well-known/oauth-authorization-server":
			json.NewEncoder(w).Encode(map[string]any{"issuer": issuer.URL, "authorization_endpoint": issuer.URL + "/authorize", "token_endpoint": issuer.URL + "/token", "code_challenge_methods_supported": []string{"S256"}, "response_types_supported": []string{"code"}, "token_endpoint_auth_methods_supported": []string{"client_secret_post"}})
		case "/token":
			r.ParseForm()
			if r.Form.Get("client_secret") != secret {
				t.Error("CLI did not supply secret from environment")
			}
			io.WriteString(w, `{"access_token":"cli-private-access-token","token_type":"Bearer","expires_in":3600}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuer.Close()
	prior := http.DefaultTransport
	http.DefaultTransport = issuer.Client().Transport
	defer func() { http.DefaultTransport = prior }()
	state, err := store.Open(filepath.Join(t.TempDir(), "oauth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err := state.SaveWorkspace(t.Context(), store.WorkspaceRecord{ID: "workspace-cli", Name: "oauth-cli", RootPath: t.TempDir(), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	owned := credential.NewMemoryStore()
	manager, err := mcp.NewClientManager(state, owned, mcp.ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	d := mcp.ServerDescriptor{ProtocolVersion: mcp.ClientProtocolVersion, ID: "cli-oauth", Name: "cli-oauth", Transport: mcp.TransportStreamableHTTP, Target: issuer.URL + "/mcp", CredentialRef: "cli-oauth", Scope: mcp.ScopeWorkspace, WorkspaceID: "workspace-cli", Source: mcp.Source{Kind: "manual", URI: "cli"}, DeclaredCapabilities: []mcp.CapabilityKind{mcp.CapabilityTools}, CallTimeoutMillis: 3000, MaxResultBytes: 8192}
	if _, _, err = manager.Stage(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	listener.Close()
	output := make(oauthCLIOutput, 4)
	app := &App{store: state, credentials: owned, out: output, errOut: io.Discard}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- app.mcpOAuthCommand(ctx, []string{"oauth-login", d.ID, "--issuer", issuer.URL, "--client-id", "cli-client", "--client-secret-env", "MCP_TEST_CLIENT_SECRET", "--callback-port", port, "--confirm"})
	}()
	var status application.MCPCredentialStatus
	select {
	case raw := <-output:
		if strings.Contains(string(raw), secret) {
			t.Fatal("CLI printed client secret")
		}
		if err = json.Unmarshal(raw, &status); err != nil {
			t.Fatal(err)
		}
	case err := <-done:
		t.Fatalf("login exited before handoff: %v", err)
	case <-ctx.Done():
		t.Fatal("no handoff")
	}
	if status.OAuth == nil {
		t.Fatal("no OAuth status")
	}
	authorize, _ := url.Parse(status.OAuth.AuthorizationURL)
	redirect := authorize.Query().Get("redirect_uri")
	if redirect != "http://127.0.0.1:"+port+"/oauth/callback" {
		t.Fatal("CLI callback port ignored")
	}
	response, err := http.Get(redirect + "?" + url.Values{"state": {authorize.Query().Get("state")}, "code": {"fixture-code"}, "iss": {issuer.URL}}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("login did not complete")
	}
	final := <-output
	if json.Unmarshal(final, &status) != nil || status.OAuth == nil || status.OAuth.State != "authorized" || strings.Contains(string(final), secret) {
		t.Fatal("invalid final CLI status")
	}
	if raw, found, err := owned.Get(t.Context(), d.CredentialRef); err != nil || !found || !strings.HasPrefix(raw, "uc-mcp-oauth-v1:") {
		t.Fatal("CLI did not persist OAuth credential")
	}
}
