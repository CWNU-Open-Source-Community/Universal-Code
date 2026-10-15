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

type issuerFixture struct {
	server        *httptest.Server
	authURL       *url.URL
	refresh       atomic.Int32
	code          atomic.Int32
	rejectRefresh atomic.Bool
	mu            sync.Mutex
}

func newIssuer(t *testing.T) *issuerFixture {
	t.Helper()
	f := &issuerFixture{}
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource/mcp":
			json.NewEncoder(w).Encode(map[string]any{"resource": f.server.URL + "/mcp", "authorization_servers": []string{f.server.URL}, "scopes_supported": []string{"tools"}})
		case "/.well-known/oauth-authorization-server":
			json.NewEncoder(w).Encode(map[string]any{"issuer": f.server.URL, "authorization_endpoint": f.server.URL + "/authorize", "token_endpoint": f.server.URL + "/token", "code_challenge_methods_supported": []string{"S256"}, "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}, "token_endpoint_auth_methods_supported": []string{"none"}, "authorization_response_iss_parameter_supported": true})
		case "/token":
			r.ParseForm()
			if r.Form.Get("resource") != f.server.URL+"/mcp" || r.Form.Get("client_id") != "public-client" {
				t.Error("token audience/client mismatch")
				http.Error(w, "invalid", 400)
				return
			}
			if r.Form.Get("grant_type") == "refresh_token" {
				f.refresh.Add(1)
				if r.Form.Get("refresh_token") != "refresh-original" {
					t.Error("wrong refresh token")
				}
				if f.rejectRefresh.Load() {
					w.WriteHeader(400)
					io.WriteString(w, `{"error":"invalid_grant","error_description":"never disclose refresh-original"}`)
					return
				}
				io.WriteString(w, `{"access_token":"access-refreshed","refresh_token":"refresh-rotated","token_type":"Bearer","expires_in":3600}`)
				return
			}
			f.code.Add(1)
			f.mu.Lock()
			authorize := f.authURL
			f.mu.Unlock()
			digest := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if authorize == nil || authorize.Query().Get("code_challenge_method") != "S256" || base64.RawURLEncoding.EncodeToString(digest[:]) != authorize.Query().Get("code_challenge") || r.Form.Get("code") != "authorized-code" || r.Form.Get("redirect_uri") != authorize.Query().Get("redirect_uri") {
				t.Error("PKCE or callback binding missing")
				http.Error(w, "invalid", 400)
				return
			}
			io.WriteString(w, `{"access_token":"access-original","refresh_token":"refresh-original","token_type":"Bearer","expires_in":3600}`)
		case "/mcp":
			if r.Header.Get("Authorization") != "Bearer access-refreshed" {
				http.Error(w, "unauthorized", 401)
				return
			}
			io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"fixture MCP result"}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}
func (f *issuerFixture) start(t *testing.T, store *credential.MemoryStore) (*Flow, Binding) {
	t.Helper()
	b := Binding{Ref: "oauth-test", Fingerprint: strings.Repeat("a", 64), Target: f.server.URL + "/mcp"}
	flow, err := Start(t.Context(), b, f.server.URL, "public-client", "", f.server.Client(), func(ctx context.Context, raw string) error { return store.Put(ctx, b.Ref, raw) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(flow.Cancel)
	u, err := url.Parse(flow.Status().AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.authURL = u
	f.mu.Unlock()
	return flow, b
}
func (f *issuerFixture) callback(t *testing.T, changes url.Values) int {
	t.Helper()
	f.mu.Lock()
	u := f.authURL
	f.mu.Unlock()
	q := url.Values{"state": {u.Query().Get("state")}, "code": {"authorized-code"}, "iss": {f.server.URL}}
	for k, v := range changes {
		q[k] = v
	}
	response, err := http.Get(u.Query().Get("redirect_uri") + "?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	io.Copy(io.Discard, response.Body)
	return response.StatusCode
}
func awaitFlow(t *testing.T, f *Flow) {
	t.Helper()
	select {
	case <-f.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("flow did not finish")
	}
}
func TestOAuthPKCECallbackStoreRefreshAndMCP(t *testing.T) {
	f := newIssuer(t)
	store := credential.NewMemoryStore()
	flow, b := f.start(t, store)
	if got := f.callback(t, url.Values{"state": {"wrong-state"}}); got != 400 {
		t.Fatalf("bad state = %d", got)
	}
	if got := f.callback(t, url.Values{"iss": {"https://other.invalid"}}); got != 400 {
		t.Fatalf("bad issuer = %d", got)
	}
	if f.code.Load() != 0 {
		t.Fatal("invalid callback exchanged code")
	}
	if got := f.callback(t, nil); got != 200 {
		t.Fatalf("valid callback = %d", got)
	}
	awaitFlow(t, flow)
	if flow.Status().State != "authorized" {
		t.Fatal(flow.Status().State)
	}
	raw, _, _ := store.Get(t.Context(), b.Ref)
	revision := RevisionIdentity(raw)
	e, err := decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	e.Token.Expiry = time.Now().Add(-time.Minute)
	expired, _ := encode(e)
	store.Put(t.Context(), b.Ref, expired)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			bearer, err := Resolve(t.Context(), store, b, f.server.Client())
			if err != nil || bearer != "access-refreshed" {
				t.Errorf("refresh = %q, %v", bearer, err)
			}
		})
	}
	wg.Wait()
	if f.refresh.Load() != 1 {
		t.Fatalf("concurrent refresh count = %d", f.refresh.Load())
	}
	raw, _, _ = store.Get(t.Context(), b.Ref)
	if RevisionIdentity(raw) != revision {
		t.Fatal("refresh changed runtime revision")
	}
	bearer, err := Resolve(t.Context(), store, b, f.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, b.Target, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{}}}`))
	req.Header.Set("Authorization", "Bearer "+bearer)
	response, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	if _, err = Resolve(t.Context(), store, Binding{Ref: b.Ref, Fingerprint: strings.Repeat("b", 64), Target: b.Target}, f.server.Client()); err == nil {
		t.Fatal("changed binding accepted")
	}
	store.Delete(t.Context(), b.Ref)
	if _, err = Resolve(t.Context(), store, b, f.server.Client()); err == nil {
		t.Fatal("deleted credential accepted")
	}
}
func TestOAuthCancellationAndUntrustedIssuer(t *testing.T) {
	f := newIssuer(t)
	store := credential.NewMemoryStore()
	flow, b := f.start(t, store)
	flow.Cancel()
	awaitFlow(t, flow)
	if flow.Status().State != "cancelled" {
		t.Fatal(flow.Status().State)
	}
	if found, _ := store.Configured(t.Context(), b.Ref); found {
		t.Fatal("cancelled flow stored credentials")
	}
	if _, err := Start(t.Context(), b, "https://other.invalid", "public-client", "", f.server.Client(), func(context.Context, string) error { t.Fatal("unexpected persistence"); return nil }); err == nil {
		t.Fatal("unadvertised issuer accepted")
	}
}
func TestOAuthRejectedRefreshRequiresReauthorizationWithoutExposingSecret(t *testing.T) {
	f := newIssuer(t)
	store := credential.NewMemoryStore()
	flow, b := f.start(t, store)
	f.callback(t, nil)
	awaitFlow(t, flow)
	raw, _, _ := store.Get(t.Context(), b.Ref)
	e, _ := decode(raw)
	e.Token.Expiry = time.Now().Add(-time.Minute)
	raw, _ = encode(e)
	store.Put(t.Context(), b.Ref, raw)
	f.rejectRefresh.Store(true)
	_, err := Resolve(t.Context(), store, b, f.server.Client())
	if err != ErrOAuth {
		t.Fatalf("non-generic error: %v", err)
	}
	after, _, _ := store.Get(t.Context(), b.Ref)
	saved, decodeErr := decode(after)
	if decodeErr != nil || !saved.ReauthorizationRequired || saved.Token.RefreshToken != "refresh-original" {
		t.Fatal("rejected refresh did not preserve secret and mark reauthorization")
	}
	status, err := StoredStatus(after, b)
	if err != nil || status.State != "reauthorization_required" {
		t.Fatal("rejected grant status missing")
	}
	_, _ = Resolve(t.Context(), store, b, f.server.Client())
	if f.refresh.Load() != 1 {
		t.Fatal("rejected grant was retried")
	}

}
