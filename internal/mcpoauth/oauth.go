// Package mcpoauth owns operator-initiated OAuth sessions. It never dispatches MCP calls.
package mcpoauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/credential"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

const prefix = "uc-mcp-oauth-v1:"

// OAuth bounds are independent of the small OS keyring entry. MaxBundleBytes
// includes the version prefix and base64 encoding; each access/refresh token is
// separately bounded. Client secrets retain their existing 1024-byte limit.
const (
	MaxBundleBytes = 64 * 1024
	MaxTokenBytes  = 16 * 1024
)

const (
	ClientIDPreRegistered = "pre_registered"
	ClientIDMetadataURL   = "metadata_url"
	ClientIDAuto          = "auto"
	ClientIDDynamic       = "dynamic"
)

var ErrOAuth = errors.New("MCP OAuth could not be verified; refresh status or authorize again")

// CredentialLock serializes local refresh, login completion and credential mutation.
// The number of entries is bounded by the host's registered credentials.
var credentialLocks sync.Map

func CredentialLock(ref string) func() {
	v, _ := credentialLocks.LoadOrStore(ref, &sync.Mutex{})
	m := v.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}
func IsEnvelope(value string) bool {
	return strings.HasPrefix(value, prefix) || strings.HasPrefix(value, storedPrefix)
}

type Binding struct{ Ref, Fingerprint, Target string }
type OAuthStatus struct {
	FlowID           string     `json:"flow_id,omitempty"`
	State            string     `json:"state"`
	Issuers          []string   `json:"issuers,omitempty"`
	Issuer           string     `json:"issuer,omitempty"`
	AuthorizationURL string     `json:"authorization_url,omitempty"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
}
type envelope struct {
	ReauthorizationRequired bool             `json:"reauthorization_required,omitempty"`
	Session                 string           `json:"session"`
	Fingerprint             string           `json:"fingerprint"`
	Target                  string           `json:"target"`
	Resource                string           `json:"resource"`
	Issuer                  string           `json:"issuer"`
	ClientID                string           `json:"client_id"`
	ClientSecret            string           `json:"client_secret,omitempty"`
	AuthStyle               oauth2.AuthStyle `json:"auth_style,omitempty"`
	TokenEndpoint           string           `json:"token_endpoint"`
	Token                   *oauth2.Token    `json:"token"`
}

func decode(raw string) (*envelope, error) {
	if !strings.HasPrefix(raw, prefix) || len(raw) > MaxBundleBytes {
		return nil, ErrOAuth
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(raw, prefix))
	if err != nil {
		return nil, ErrOAuth
	}
	var e envelope
	if json.Unmarshal(data, &e) != nil || !validEnvelope(&e) {
		return nil, ErrOAuth
	}
	return &e, nil
}
func encode(e *envelope) (string, error) {
	if !validEnvelope(e) {
		return "", ErrOAuth
	}
	b, err := json.Marshal(e)
	if err != nil {
		return "", ErrOAuth
	}
	raw := prefix + base64.RawURLEncoding.EncodeToString(b)
	if len(raw) > MaxBundleBytes {
		return "", ErrOAuth
	}
	return raw, nil
}

func validToken(value string) bool {
	if value == "" || len(value) > MaxTokenBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func validEnvelope(e *envelope) bool {
	return e != nil && e.Session != "" && e.Token != nil && validToken(e.Token.AccessToken) &&
		(e.Token.RefreshToken == "" || validToken(e.Token.RefreshToken)) && strings.EqualFold(e.Token.TokenType, "Bearer") &&
		(e.ClientSecret == "" || (credential.ValidSecret(e.ClientSecret) && len(e.ClientSecret) <= 1024)) &&
		validHTTPS(e.Target) && validHTTPS(e.Issuer) && validHTTPS(e.TokenEndpoint) && validHTTPS(e.Resource) && sameOrigin(e.Target, e.Resource)
}

// RevisionIdentity is stable across refresh, but a new login has a new identity.
func RevisionIdentity(raw string) string {
	if IsEnvelope(raw) {
		e, err := decode(raw)
		if err != nil {
			return "invalid-oauth"
		}
		if e.ReauthorizationRequired {
			return prefix + e.Session + ":reauthorization-required"
		}
		return prefix + e.Session + ":" + e.Fingerprint + ":" + e.Target + ":" + e.Issuer + ":" + e.Resource + ":" + e.ClientID + ":" + e.TokenEndpoint
	}
	return raw
}
func StoredStatus(raw string, b Binding) (OAuthStatus, error) {
	e, err := decode(raw)
	if err != nil || e.Target != b.Target || e.Fingerprint != b.Fingerprint {
		return OAuthStatus{}, ErrOAuth
	}
	state := "authorized"
	if e.ReauthorizationRequired {
		return OAuthStatus{State: "reauthorization_required", Issuer: e.Issuer}, nil
	}
	if !e.Token.Valid() {
		state = "expired"
		if e.Token.RefreshToken != "" {
			state = "refresh_required"
		}
	}
	status := OAuthStatus{State: state, Issuer: e.Issuer}
	if !e.Token.Expiry.IsZero() {
		expiry := e.Token.Expiry
		status.ExpiresAt = &expiry
	}
	return status, nil
}

// LocalBearer only reads the envelope, for local redaction preparation. Network
// refresh is deferred until the runtime has passed its execution authority.
func LocalBearer(raw string, b Binding) (string, error) {
	e, err := decode(raw)
	if err != nil || e.Target != b.Target || e.Fingerprint != b.Fingerprint {
		return "", ErrOAuth
	}
	return e.Token.AccessToken, nil
}

func validHTTPS(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Opaque == "" && len(raw) <= 4096
}
func sameOrigin(a, b string) bool {
	x, e := url.Parse(a)
	y, f := url.Parse(b)
	return e == nil && f == nil && x.Scheme == y.Scheme && strings.EqualFold(x.Host, y.Host)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func clientFor(base *http.Client, allowed func(*http.Request) bool) *http.Client {
	return clientForAuth(base, allowed, nil)
}
func clientForAuth(base *http.Client, allowed, allowAuth func(*http.Request) bool) *http.Client {
	rt := http.DefaultTransport.(*http.Transport).Clone()
	rt.Proxy = nil
	var transport http.RoundTripper = rt
	if base != nil && base.Transport != nil {
		transport = base.Transport
	}
	return &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrOAuth }, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if !allowed(r) || r.URL.User != nil || r.URL.Fragment != "" || (r.Header.Get("Authorization") != "" && (allowAuth == nil || !allowAuth(r))) {
			return nil, ErrOAuth
		}
		response, err := transport.RoundTrip(r)
		if err != nil {
			return nil, ErrOAuth
		}
		response.Body = &boundedBody{ReadCloser: response.Body, remaining: 128 * 1024}
		return response, nil
	})}
}

// The SDK selects a server-wide confidential-client auth method even for public
// preregistrations. Normalize only its matching, empty-secret identity to public
// client form authentication. The endpoint gate still rejects all auth headers.
func publicTokenRequest(r *http.Request, endpoint, clientID string) (*http.Request, error) {
	if r.Body != nil {
		defer r.Body.Close()
	}
	if r.Method != http.MethodPost || r.URL.String() != endpoint || r.Body == nil || len(r.Header.Values("Authorization")) > 1 {
		return nil, ErrOAuth
	}
	hasBasic := r.Header.Get("Authorization") != ""
	if hasBasic {
		name, secret, ok := r.BasicAuth()
		decoded, err := url.QueryUnescape(name)
		if !ok || err != nil || decoded != clientID || secret != "" {
			return nil, ErrOAuth
		}
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 64*1024+1))
	if err != nil || len(raw) > 64*1024 {
		return nil, ErrOAuth
	}
	form, err := url.ParseQuery(string(raw))
	if err != nil || len(form["client_id"]) > 1 || len(form["client_secret"]) > 1 || form.Get("client_secret") != "" ||
		(form.Get("client_id") != clientID && (form.Get("client_id") != "" || !hasBasic)) {
		return nil, ErrOAuth
	}
	form.Set("client_id", clientID)
	form.Del("client_secret")
	encoded := form.Encode()
	r = r.Clone(r.Context())
	r.Header.Del("Authorization")
	r.Body = io.NopCloser(strings.NewReader(encoded))
	r.ContentLength = int64(len(encoded))
	r.GetBody = nil
	return r, nil
}

type boundedBody struct {
	io.ReadCloser
	remaining int64
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, ErrOAuth
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, e := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, e
}

type discovery struct {
	resource    *oauthex.ProtectedResourceMetadata
	metadataURL string
	snapshots   map[string][]byte
}

func discover(ctx context.Context, target string, base *http.Client) (*discovery, error) {
	if !validHTTPS(target) {
		return nil, ErrOAuth
	}
	u, _ := url.Parse(target)
	path := u.Path
	u.Path = "/.well-known/oauth-protected-resource/" + strings.TrimLeft(path, "/")
	u.RawPath = ""
	candidates := []struct{ metadata, resource string }{{u.String(), target}}
	u.Path = "/.well-known/oauth-protected-resource"
	root := *u
	root.Path = ""
	candidates = append(candidates, struct{ metadata, resource string }{u.String(), root.String()})
	c := clientFor(base, func(r *http.Request) bool { return r.Method == http.MethodGet && sameOrigin(target, r.URL.String()) })
	for _, candidate := range candidates {
		prm, err := oauthex.GetProtectedResourceMetadata(ctx, candidate.metadata, candidate.resource, c)
		if err != nil || prm == nil {
			continue
		}
		if len(prm.AuthorizationServers) == 0 || len(prm.AuthorizationServers) > 16 {
			return nil, ErrOAuth
		}
		for _, issuer := range prm.AuthorizationServers {
			if !validHTTPS(issuer) {
				return nil, ErrOAuth
			}
		}
		raw, _ := json.Marshal(prm)
		return &discovery{prm, candidate.metadata, map[string][]byte{candidate.metadata: raw}}, nil
	}
	return nil, ErrOAuth
}
func Discover(ctx context.Context, target string, base *http.Client) (OAuthStatus, error) {
	d, e := discover(ctx, target, base)
	if e != nil {
		return OAuthStatus{}, e
	}
	return OAuthStatus{State: "discovered", Issuers: d.resource.AuthorizationServers}, nil
}

// Flow holds transient verifier/state indirectly in the SDK until completion.
type Flow struct {
	id      string
	binding Binding
	mu      sync.Mutex
	status  OAuthStatus
	cancel  context.CancelFunc
	done    chan struct{}
	result  *envelope
}

func (f *Flow) MatchesBinding(b Binding) bool { return f.binding == b }

func (f *Flow) Status() OAuthStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	value := f.status
	value.FlowID = f.id
	return value
}
func (f *Flow) Cancel() {
	f.mu.Lock()
	if f.status.State == "authorizing" || f.status.State == "starting" {
		f.status = OAuthStatus{State: "cancelled"}
		f.cancel()
	}
	f.mu.Unlock()
}
func (f *Flow) Done() <-chan struct{} { return f.done }

// Options apply to one operator-initiated login.
type Options struct {
	ClientIDMode string
	ClientSecret string
	CallbackPort int
}

func Start(ctx context.Context, b Binding, issuer, clientID, clientIDMode string, base *http.Client, persist func(context.Context, string) error) (*Flow, error) {
	return StartWithOptions(ctx, b, issuer, clientID, Options{ClientIDMode: clientIDMode}, base, persist)
}

// StartWithOptions returns once an operator URL is ready. persist must recheck
// the complete descriptor/reference binding while holding its mutation lock.
func StartWithOptions(ctx context.Context, b Binding, issuer, clientID string, options Options, base *http.Client, persist func(context.Context, string) error) (*Flow, error) {
	clientIDMode, clientSecret := options.ClientIDMode, options.ClientSecret
	if clientIDMode == ClientIDAuto {
		if clientID != "" {
			clientIDMode = ClientIDPreRegistered
		} else {
			clientIDMode = ClientIDDynamic
		}
	}
	if !validHTTPS(issuer) || options.CallbackPort < 0 || options.CallbackPort > 65535 ||
		(clientSecret != "" && (!credential.ValidSecret(clientSecret) || len(clientSecret) > 1024)) {
		return nil, ErrOAuth
	}
	switch clientIDMode {
	case "", ClientIDPreRegistered, ClientIDMetadataURL:
		if !validClientID(clientID) {
			return nil, ErrOAuth
		}
	case ClientIDDynamic:
		if clientID != "" || clientSecret != "" {
			return nil, ErrOAuth
		}
	default:
		return nil, ErrOAuth
	}
	if clientIDMode == ClientIDMetadataURL {
		u, err := url.Parse(clientID)
		if err != nil || !validHTTPS(clientID) || u.Path == "" || u.Path == "/" || clientSecret != "" {
			return nil, ErrOAuth
		}
	}

	d, err := discover(ctx, b.Target, base)
	if err != nil || !slices.Contains(d.resource.AuthorizationServers, issuer) {
		return nil, ErrOAuth
	}
	discoveryClient := clientFor(base, func(r *http.Request) bool { return r.Method == http.MethodGet && sameOrigin(issuer, r.URL.String()) })
	underlying := discoveryClient.Transport
	discoveryClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		response, e := underlying.RoundTrip(r)
		if e != nil {
			return nil, e
		}
		raw, e := io.ReadAll(response.Body)
		response.Body.Close()
		if e != nil {
			return nil, ErrOAuth
		}
		response.Body = io.NopCloser(bytes.NewReader(raw))
		if response.StatusCode == 200 {
			d.snapshots[r.URL.String()] = raw
		}
		return response, nil
	})
	asm, err := auth.GetAuthServerMetadata(ctx, issuer, discoveryClient)
	if err != nil || asm == nil || asm.Issuer != issuer || !validHTTPS(asm.AuthorizationEndpoint) || !validHTTPS(asm.TokenEndpoint) || !slices.Contains(asm.CodeChallengeMethodsSupported, "S256") {
		return nil, ErrOAuth
	}
	if clientIDMode == ClientIDMetadataURL && !asm.ClientIDMetadataDocumentSupported {
		return nil, ErrOAuth
	}
	// Freeze metadata after operator confirmation. Later discovery cannot substitute
	// a different issuer or endpoint between browser navigation and code exchange.
	d.resource.AuthorizationServers = []string{issuer}
	d.snapshots[d.metadataURL], _ = json.Marshal(d.resource)
	listener, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(options.CallbackPort))
	if err != nil {
		return nil, ErrOAuth
	}
	callback := "http://" + listener.Addr().String() + "/oauth/callback"
	// Registration is sent only to the endpoint in the frozen, confirmed issuer metadata.
	if clientIDMode == ClientIDDynamic {
		if !validHTTPS(asm.RegistrationEndpoint) {
			listener.Close()
			return nil, ErrOAuth
		}
		registrationClient := clientFor(base, func(r *http.Request) bool {
			return r.Method == http.MethodPost && r.URL.String() == asm.RegistrationEndpoint
		})
		registered, err := oauthex.RegisterClient(ctx, asm.RegistrationEndpoint, &oauthex.ClientRegistrationMetadata{
			ClientName: "Universal Code", ApplicationType: "native", RedirectURIs: []string{callback}, TokenEndpointAuthMethod: "none",
			GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"},
		}, registrationClient)
		if err != nil || registered == nil || !validClientID(registered.ClientID) || registered.ClientSecret != "" || registered.TokenEndpointAuthMethod != "none" || !slices.Contains(registered.RedirectURIs, callback) {
			listener.Close()
			return nil, ErrOAuth
		}
		clientID = registered.ClientID
	}
	authStyle := oauth2.AuthStyleInParams
	if clientSecret != "" {
		switch {
		case slices.Contains(asm.TokenEndpointAuthMethodsSupported, "client_secret_post"):
		case len(asm.TokenEndpointAuthMethodsSupported) == 0 || slices.Contains(asm.TokenEndpointAuthMethodsSupported, "client_secret_basic"):
			authStyle = oauth2.AuthStyleInHeader
		default:
			listener.Close()
			return nil, ErrOAuth
		}
	}
	tokenClient := tokenHTTPClient(base, asm.TokenEndpoint, clientID, clientSecret, authStyle)
	tokenTransport := tokenClient.Transport
	tokenClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			if raw, ok := d.snapshots[r.URL.String()]; ok {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(raw)), Request: r}, nil
			}
			return nil, ErrOAuth
		}
		return tokenTransport.RoundTrip(r)
	})
	flowCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	f := &Flow{id: rand.Text(), binding: b, status: OAuthStatus{State: "starting", Issuer: issuer}, cancel: cancel, done: make(chan struct{})}
	ready := make(chan struct{})
	var readyOnce sync.Once
	result := make(chan *auth.AuthorizationResult, 1)
	var expectedState string
	var callbackMu sync.Mutex
	consumed := false
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 8192}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		callbackMu.Lock()
		defer callbackMu.Unlock()
		q, parseErr := url.ParseQuery(r.URL.RawQuery)
		valid := parseErr == nil && len(r.URL.RawQuery) <= 8192 && r.Method == http.MethodGet && r.ContentLength == 0 && len(r.TransferEncoding) == 0 && r.Host == listener.Addr().String() && r.URL.Path == "/oauth/callback" && flowCtx.Err() == nil && !consumed && expectedState != "" && subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(expectedState)) == 1
		for key, values := range q {
			if len(values) != 1 || (key != "state" && key != "code" && key != "iss" && key != "error" && key != "error_description" && key != "error_uri") {
				valid = false
			}
		}
		if !valid {
			http.Error(w, "Invalid authorization callback", http.StatusBadRequest)
			return
		}
		if (asm.AuthorizationResponseIssParameterSupported && q.Get("iss") == "") || (q.Get("iss") != "" && q.Get("iss") != issuer) {
			http.Error(w, "Invalid authorization issuer", http.StatusBadRequest)
			return
		}
		consumed = true
		if q.Get("error") != "" || q.Get("code") == "" {
			f.mu.Lock()
			f.status = OAuthStatus{State: "denied", Issuer: issuer}
			f.mu.Unlock()
			cancel()
			http.Error(w, "Authorization was not completed", http.StatusBadRequest)
			return
		}
		result <- &auth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}
		_, _ = io.WriteString(w, "Authorization received. Return to Universal Code to check the result.")
	})
	go func() { _ = server.Serve(listener) }()
	config := &auth.AuthorizationCodeHandlerConfig{RedirectURL: callback, Client: tokenClient, RequestRefreshToken: true, AcceptUnadvertisedIss: true,
		AuthorizationCodeFetcher: func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			u, e := url.Parse(args.URL)
			if e != nil || !sameOrigin(asm.AuthorizationEndpoint, args.URL) {
				return nil, ErrOAuth
			}
			callbackMu.Lock()
			expectedState = u.Query().Get("state")
			callbackMu.Unlock()
			expiry, _ := flowCtx.Deadline()
			f.mu.Lock()
			f.status = OAuthStatus{State: "authorizing", Issuer: issuer, AuthorizationURL: args.URL, ExpiresAt: &expiry}
			f.mu.Unlock()
			readyOnce.Do(func() { close(ready) })
			select {
			case value := <-result:
				return value, nil
			case <-ctx.Done():
				return nil, ErrOAuth
			}
		}, NewTokenSource: func(_ context.Context, cfg *oauth2.Config, token *oauth2.Token) (oauth2.TokenSource, error) {
			if token == nil || !validToken(token.AccessToken) || (token.RefreshToken != "" && !validToken(token.RefreshToken)) || !token.Valid() || !strings.EqualFold(token.TokenType, "Bearer") {
				return nil, ErrOAuth
			}
			f.result = &envelope{Session: rand.Text(), Fingerprint: b.Fingerprint, Target: b.Target, Resource: d.resource.Resource, Issuer: issuer, ClientID: clientID, ClientSecret: clientSecret, AuthStyle: authStyle, TokenEndpoint: cfg.Endpoint.TokenURL, Token: token}
			return oauth2.StaticTokenSource(token), nil
		}}
	if clientIDMode == ClientIDMetadataURL {
		config.ClientIDMetadataDocumentConfig = &auth.ClientIDMetadataDocumentConfig{URL: clientID}
	} else {
		config.PreregisteredClient = &oauthex.ClientCredentials{ClientID: clientID, Issuer: issuer}
		if clientSecret != "" {
			config.PreregisteredClient.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: clientSecret}
		}
	}
	handler, err := auth.NewAuthorizationCodeHandler(config)
	if err != nil {
		cancel()
		server.Close()
		return nil, ErrOAuth
	}
	go func() {
		defer close(f.done)
		defer func() { f.result = nil }()
		defer cancel()
		defer server.Close()
		defer readyOnce.Do(func() { close(ready) })
		req, _ := http.NewRequestWithContext(flowCtx, http.MethodGet, b.Target, nil)
		response := &http.Response{StatusCode: 401, Header: http.Header{"Www-Authenticate": []string{`Bearer resource_metadata="` + d.metadataURL + `"`}}, Body: io.NopCloser(strings.NewReader(""))}
		err := handler.Authorize(flowCtx, req, response)
		if flowCtx.Err() != nil {
			f.mu.Lock()
			if f.status.State != "cancelled" && f.status.State != "denied" {
				f.status = OAuthStatus{State: "expired", Issuer: issuer}
			}
			f.mu.Unlock()
			return
		}
		if err == nil && f.result != nil {
			var raw string
			raw, err = encode(f.result)
			if err == nil {
				err = persist(flowCtx, raw)
			}
		}
		f.result = nil
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.status.State == "cancelled" && err != nil {
			return
		}
		if err != nil {
			f.status = OAuthStatus{State: "failed", Issuer: issuer}
		} else {
			f.status = OAuthStatus{State: "authorized", Issuer: issuer}
		}
	}()
	select {
	case <-ready:
		if f.Status().State != "authorizing" {
			return nil, ErrOAuth
		}
		return f, nil
	case <-ctx.Done():
		f.Cancel()
		return nil, ErrOAuth
	}
}

// Resolve returns a bearer for exactly the stored descriptor. Callers still own
// MCP authorization and must recheck it at actual dispatch.
func Resolve(ctx context.Context, store credential.Store, b Binding, base *http.Client) (string, error) {
	return ResolveRevision(ctx, store, b, base, "")
}

// ResolveRevision also rejects a replacement occurring between runtime revision
// validation and credential resolution.
func ResolveRevision(ctx context.Context, store credential.Store, b Binding, base *http.Client, expectedIdentity string) (string, error) {
	unlock := CredentialLock(b.Ref)
	defer unlock()
	raw, found, err := store.Get(ctx, b.Ref)
	if err != nil || !found {
		return "", ErrOAuth
	}
	if expectedIdentity != "" && RevisionIdentity(raw) != expectedIdentity {
		return "", ErrOAuth
	}
	if !IsEnvelope(raw) {
		return raw, nil
	}
	e, err := decode(raw)
	if err != nil || e.Fingerprint != b.Fingerprint || e.Target != b.Target {
		return "", ErrOAuth
	}
	if e.ReauthorizationRequired {
		return "", ErrOAuth
	}
	if e.Token.Valid() {
		return e.Token.AccessToken, nil
	}
	if e.Token.RefreshToken == "" {
		return "", ErrOAuth
	}
	c := tokenHTTPClient(base, e.TokenEndpoint, e.ClientID, e.ClientSecret, e.AuthStyle)
	underlying := c.Transport
	c.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024+1))
		r.Body.Close()
		if err != nil || len(body) > 64*1024 {
			return nil, ErrOAuth
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			return nil, ErrOAuth
		}
		values.Set("resource", e.Resource)
		encoded := values.Encode()
		r = r.Clone(r.Context())
		r.Body = io.NopCloser(strings.NewReader(encoded))
		r.ContentLength = int64(len(encoded))
		return underlying.RoundTrip(r)
	})
	style := e.AuthStyle
	if style == oauth2.AuthStyleAutoDetect {
		style = oauth2.AuthStyleInParams
	}
	cfg := oauth2.Config{ClientID: e.ClientID, ClientSecret: e.ClientSecret, Endpoint: oauth2.Endpoint{TokenURL: e.TokenEndpoint, AuthStyle: style}}
	token, err := cfg.TokenSource(context.WithValue(ctx, oauth2.HTTPClient, c), e.Token).Token()
	var rejected *oauth2.RetrieveError
	if errors.As(err, &rejected) && rejected.ErrorCode == "invalid_grant" {
		// A rejected refresh grant is a durable local reauthorization condition;
		// subsequent calls must not keep retrying the rejected refresh token.
		e.ReauthorizationRequired = true
		if marked, encodeErr := encode(e); encodeErr == nil {
			_ = Replace(ctx, store, b.Ref, raw, true, marked)
		}
		return "", ErrOAuth
	}
	if err != nil || token == nil || !validToken(token.AccessToken) || (token.RefreshToken != "" && !validToken(token.RefreshToken)) || !token.Valid() || !strings.EqualFold(token.TokenType, "Bearer") {
		return "", ErrOAuth
	}
	e.Token = token
	updated, err := encode(e)
	if err != nil {
		return "", ErrOAuth
	}
	if err = Replace(ctx, store, b.Ref, raw, true, updated); err != nil {
		return "", ErrOAuth
	}
	return token.AccessToken, nil
}

func validClientID(id string) bool {
	return len(id) > 0 && len(id) <= 256 && !strings.ContainsAny(id, "\r\n\x00")
}

// Only this exact token endpoint may receive this client's authentication header.
func tokenHTTPClient(base *http.Client, endpoint, id, secret string, style oauth2.AuthStyle) *http.Client {
	c := clientForAuth(base, func(r *http.Request) bool { return r.Method == http.MethodPost && r.URL.String() == endpoint }, func(r *http.Request) bool {
		name, password, ok := r.BasicAuth()
		name, e1 := url.QueryUnescape(name)
		password, e2 := url.QueryUnescape(password)
		return secret != "" && style == oauth2.AuthStyleInHeader && len(r.Header.Values("Authorization")) == 1 && ok && e1 == nil && e2 == nil && name == id && password == secret
	})
	if secret == "" {
		underlying := c.Transport
		c.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			r, err := publicTokenRequest(r, endpoint, id)
			if err != nil {
				return nil, err
			}
			return underlying.RoundTrip(r)
		})
	}
	return c
}
