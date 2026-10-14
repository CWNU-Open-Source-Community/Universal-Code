package mcpoauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/credential"
)

func TestOAuthPublicClientInteroperabilityTLS(t *testing.T) {
	for _, mode := range []string{ClientIDPreRegistered, ClientIDMetadataURL} {
		t.Run(mode, func(t *testing.T) {
			var issuer, endpoints *httptest.Server
			var challenge, redirect string
			var mu sync.Mutex
			var exchanges atomic.Int32
			var cimdSupported atomic.Bool
			cimdSupported.Store(true)
			clientID := "public-client"
			endpoints = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/authorize":
					q := r.URL.Query()
					if q.Get("client_id") != clientID || q.Get("resource") != issuer.URL+"/mcp" || q.Get("code_challenge_method") != "S256" {
						t.Error("authorization lost client/resource/PKCE binding")
					}
					mu.Lock()
					challenge, redirect = q.Get("code_challenge"), q.Get("redirect_uri")
					mu.Unlock()
					callback := url.Values{"state": {q.Get("state")}, "iss": {issuer.URL}, "code": {"fixture-code"}}
					http.Redirect(w, r, q.Get("redirect_uri")+"?"+callback.Encode(), http.StatusFound)
				case "/token":
					exchanges.Add(1)
					if r.ParseForm() != nil || r.Header.Get("Authorization") != "" || r.Form.Get("client_secret") != "" || r.Form.Get("client_id") != clientID || r.Form.Get("resource") != issuer.URL+"/mcp" {
						t.Error("public token request leaked credentials or lost binding")
					}
					if r.Form.Get("grant_type") == "authorization_code" {
						mu.Lock()
						digest := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
						if base64.RawURLEncoding.EncodeToString(digest[:]) != challenge || r.Form.Get("redirect_uri") != redirect {
							t.Error("code exchange lost PKCE/redirect binding")
						}
						mu.Unlock()
					}
					// Unknown expiry is valid both on initial exchange and refresh.
					io.WriteString(w, `{"access_token":"interop-access","refresh_token":"interop-refresh","token_type":"Bearer"}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer endpoints.Close()
			issuer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/.well-known/oauth-protected-resource/mcp":
					json.NewEncoder(w).Encode(map[string]any{"resource": issuer.URL + "/mcp", "authorization_servers": []string{issuer.URL}})
				case "/.well-known/oauth-authorization-server":
					json.NewEncoder(w).Encode(map[string]any{"issuer": issuer.URL, "authorization_endpoint": endpoints.URL + "/authorize", "token_endpoint": endpoints.URL + "/token", "code_challenge_methods_supported": []string{"S256"}, "response_types_supported": []string{"code"}, "token_endpoint_auth_methods_supported": []string{"none", "client_secret_basic"}, "client_id_metadata_document_supported": cimdSupported.Load()})
				default:
					http.NotFound(w, r)
				}
			}))
			defer issuer.Close()
			if mode == ClientIDMetadataURL {
				clientID = "https://client.example/metadata.json"
			}
			b := Binding{Ref: "interop", Fingerprint: strings.Repeat("a", 64), Target: issuer.URL + "/mcp"}
			owned := credential.NewMemoryStore()
			persist := func(ctx context.Context, raw string) error { return owned.Put(ctx, b.Ref, raw) }
			flow, err := Start(t.Context(), b, issuer.URL, clientID, mode, issuer.Client(), persist)
			if err != nil {
				t.Fatal(err)
			}
			defer flow.Cancel()
			response, err := endpoints.Client().Get(flow.Status().AuthorizationURL)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			awaitFlow(t, flow)
			if flow.Status().State != "authorized" {
				t.Fatal(flow.Status().State)
			}
			raw, _, _ := owned.Get(t.Context(), b.Ref)
			status, err := StoredStatus(raw, b)
			if err != nil || status.State != "authorized" || status.ExpiresAt != nil {
				t.Fatal("unknown expiry was rejected or fabricated")
			}
			revision := RevisionIdentity(raw)
			entry, _ := decode(raw)
			entry.Token.Expiry = time.Now().Add(-time.Minute)
			expired, _ := encode(entry)
			owned.Put(t.Context(), b.Ref, expired)
			if _, err = Resolve(t.Context(), owned, b, issuer.Client()); err != nil {
				t.Fatal(err)
			}
			after, _, _ := owned.Get(t.Context(), b.Ref)
			if RevisionIdentity(after) != revision || exchanges.Load() != 2 {
				t.Fatal("refresh changed binding or repeated exchange")
			}
			if mode == ClientIDMetadataURL {
				cimdSupported.Store(false)
				if _, err = Start(t.Context(), b, issuer.URL, clientID, mode, issuer.Client(), persist); err == nil {
					t.Fatal("CIMD used without advertised support")
				}
			}
		})
	}
}

func TestPublicTokenRequestRejectsDifferentIdentityOrSecretBeforeTLS(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	clientID := "https://client.example/metadata.json"
	for _, kind := range []string{"wrong-basic-id", "basic-secret", "body-secret", "wrong-body-id", "duplicate-id", "other-endpoint"} {
		t.Run(kind, func(t *testing.T) {
			body := url.Values{"client_id": {clientID}, "grant_type": {"authorization_code"}}
			if kind == "body-secret" {
				body.Set("client_secret", "secret")
			}
			if kind == "wrong-body-id" {
				body.Set("client_id", "other")
			}
			if kind == "duplicate-id" {
				body.Add("client_id", clientID)
			}
			r, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/token", strings.NewReader(body.Encode()))
			if kind == "wrong-basic-id" {
				r.SetBasicAuth("other", "")
			}
			if kind == "basic-secret" {
				r.SetBasicAuth(url.QueryEscape(clientID), "secret")
			}
			if kind == "other-endpoint" {
				r.URL.Path = "/other"
			}
			request, err := publicTokenRequest(r, server.URL+"/token", clientID)
			if err == nil {
				response, requestErr := server.Client().Do(request)
				if response != nil {
					response.Body.Close()
				}
				t.Fatalf("invalid identity accepted: %v", requestErr)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatal("invalid credentials reached token endpoint")
	}
}
