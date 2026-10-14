package githubreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/idgen"
)

const (
	deviceCodePath  = "/login/device/code"
	accessTokenPath = "/login/oauth/access_token"
	maxOAuthBody    = 64 * 1024
)

type tokenBundle struct {
	ProtocolVersion  string    `json:"protocol_version"`
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token,omitempty"`
	TokenType        string    `json:"token_type"`
	Scope            string    `json:"scope,omitempty"`
	ExpiresAt        time.Time `json:"expires_at,omitempty"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at,omitempty"`
}

type tokenLease struct {
	value     string
	tokenType string
	scope     string
	expiresAt time.Time
}

type tokenResolver interface {
	resolve(context.Context, CredentialReference) (tokenLease, error)
	configured(context.Context, CredentialReference) (bool, error)
}

type DeviceAuthorization struct {
	ProtocolVersion string    `json:"protocol_version"`
	SessionID       string    `json:"session_id"`
	UserCode        string    `json:"user_code"`
	VerificationURI string    `json:"verification_uri"`
	ExpiresAt       time.Time `json:"expires_at"`
	PollIntervalMS  int64     `json:"poll_interval_ms"`
}

type DevicePollState string

const (
	DevicePending    DevicePollState = "pending"
	DeviceSlowDown   DevicePollState = "slow_down"
	DeviceAuthorized DevicePollState = "authorized"
	DeviceExpired    DevicePollState = "expired"
	DeviceDenied     DevicePollState = "denied"
)

type DevicePollResult struct {
	ProtocolVersion string              `json:"protocol_version"`
	SessionID       string              `json:"session_id"`
	State           DevicePollState     `json:"state"`
	Credential      CredentialReference `json:"credential"`
	Configured      bool                `json:"configured"`
	ExpiresAt       time.Time           `json:"expires_at,omitempty"`
	NextPollAt      time.Time           `json:"next_poll_at,omitempty"`
}

type CredentialStatus struct {
	ProtocolVersion  string              `json:"protocol_version"`
	Credential       CredentialReference `json:"credential"`
	StoreKind        string              `json:"store_kind"`
	StoreAvailable   bool                `json:"store_available"`
	Configured       bool                `json:"configured"`
	Refreshable      bool                `json:"refreshable"`
	ExpiresAt        time.Time           `json:"expires_at,omitempty"`
	RefreshExpiresAt time.Time           `json:"refresh_expires_at,omitempty"`
}

type deviceSession struct {
	id         string
	credential CredentialReference
	deviceCode string
	expiresAt  time.Time
	interval   time.Duration
	nextPollAt time.Time
	generation uint64
}

// AuthLifecycle orders credential replacement and disconnect across the
// managers for all connections using the same credential store.
type AuthLifecycle struct {
	store       credential.Store
	mu          sync.Mutex
	generations map[string]uint64
}

func NewAuthLifecycle(store credential.Store) (*AuthLifecycle, error) {
	if store == nil || !store.Available() {
		return nil, errors.New("system credential storage is unavailable")
	}
	return &AuthLifecycle{store: store, generations: make(map[string]uint64)}, nil
}

// credentialKey follows the store identity: Windows TargetName is case-insensitive.
// Other stores retain their case-sensitive names.
func (l *AuthLifecycle) credentialKey(name string) string {
	if l.store.Kind() == "windows_credential_manager" {
		return strings.ToUpper(name)
	}
	return name
}

func (l *AuthLifecycle) ImportToken(ctx context.Context, ref CredentialReference, token string) error {
	if ref.Validate() != nil || (ref.Kind != AuthFineGrainedPAT && ref.Kind != AuthOAuthUser) || !validOpaqueSecret(token) {
		return errors.New("GitHub token import request is invalid")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.store.Put(ctx, ref.Name, token); err != nil {
		return err
	}
	l.generations[l.credentialKey(ref.Name)]++
	return nil
}

type AuthManager struct {
	store      credential.Store
	clientID   string
	httpClient *http.Client
	oauthBase  string
	now        func() time.Time
	mu         *sync.Mutex
	lifecycle  *AuthLifecycle
	sessions   map[string]deviceSession
}

func NewAuthManager(store credential.Store, clientID string) (*AuthManager, error) {
	lifecycle, err := NewAuthLifecycle(store)
	if err != nil {
		return nil, err
	}
	return lifecycle.NewManager(clientID)
}

func (l *AuthLifecycle) NewManager(clientID string) (*AuthManager, error) {
	clientID = strings.TrimSpace(clientID)
	if clientID != "" && !validClientID(clientID) {
		return nil, errors.New("GitHub App public client id is invalid")
	}
	return &AuthManager{store: l.store, clientID: clientID, mu: &l.mu, lifecycle: l,
		httpClient: &http.Client{Timeout: 20 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			}}, oauthBase: "https://github.com", now: time.Now,
		sessions: make(map[string]deviceSession)}, nil
}

// NewAuthManagerForTest accepts only a loopback HTTP endpoint. It cannot be
// used to expand the production GitHub network boundary.
func NewAuthManagerForTest(store credential.Store, clientID string, oauthBase string,
	client *http.Client,
) (*AuthManager, error) {
	manager, err := NewAuthManager(store, clientID)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(strings.TrimSpace(oauthBase))
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" || parsed.Path != "" || !loopbackHostname(parsed.Hostname()) {
		return nil, errors.New("test OAuth endpoint must be a clean loopback HTTP origin")
	}
	manager.oauthBase = strings.TrimSuffix(oauthBase, "/")
	if client != nil {
		manager.httpClient = client
	}
	return manager, nil
}

func (m *AuthManager) BeginDeviceFlow(ctx context.Context,
	ref CredentialReference,
) (DeviceAuthorization, error) {
	if m == nil || m.store == nil {
		return DeviceAuthorization{}, errors.New("GitHub authentication manager is unavailable")
	}
	if ref.Kind != AuthGitHubAppDevice || ref.Validate() != nil {
		return DeviceAuthorization{}, errors.New("device flow requires a GitHub App device credential reference")
	}
	if !validClientID(m.clientID) {
		return DeviceAuthorization{}, errors.New("GitHub App public client id is not configured")
	}
	m.mu.Lock()
	generation := m.lifecycle.generations[m.lifecycle.credentialKey(ref.Name)]
	m.mu.Unlock()
	form := url.Values{"client_id": []string{m.clientID}}
	var response struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
	}
	if err := m.postOAuth(ctx, deviceCodePath, form, &response); err != nil {
		return DeviceAuthorization{}, err
	}
	if !validOpaqueSecret(response.DeviceCode) || !validUserCode(response.UserCode) ||
		response.VerificationURI != "https://github.com/login/device" ||
		response.ExpiresIn < 60 || response.ExpiresIn > 3600 ||
		response.Interval < 1 || response.Interval > 60 {
		return DeviceAuthorization{}, &Error{Code: FailureMalformed,
			Message: "GitHub device authorization response is invalid"}
	}
	now := m.now().UTC()
	session := deviceSession{id: idgen.New("github-device"), credential: ref,
		deviceCode: response.DeviceCode, expiresAt: now.Add(time.Duration(response.ExpiresIn) * time.Second),
		interval: time.Duration(response.Interval) * time.Second, nextPollAt: now,
		generation: generation}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lifecycle.generations[m.lifecycle.credentialKey(ref.Name)] != generation {
		return DeviceAuthorization{}, authLifecycleChanged()
	}
	m.pruneSessionsLocked(now)
	m.sessions[session.id] = session
	return DeviceAuthorization{ProtocolVersion: DeviceFlowProtocolVersion,
		SessionID: session.id, UserCode: response.UserCode,
		VerificationURI: response.VerificationURI, ExpiresAt: session.expiresAt,
		PollIntervalMS: session.interval.Milliseconds()}, nil
}

func (m *AuthManager) PollDeviceFlow(ctx context.Context, sessionID string) (DevicePollResult, error) {
	if m == nil {
		return DevicePollResult{}, errors.New("GitHub authentication manager is unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	m.mu.Lock()
	now := m.now().UTC()
	m.pruneSessionsLocked(now)
	session, found := m.sessions[sessionID]
	if !found {
		m.mu.Unlock()
		return DevicePollResult{}, &Error{Code: FailureNotFound,
			Message: "GitHub device authorization session was not found or expired"}
	}
	if now.Before(session.nextPollAt) {
		result := devicePollView(session, DevicePending, false)
		m.mu.Unlock()
		return result, nil
	}
	session.nextPollAt = now.Add(session.interval)
	m.sessions[sessionID] = session
	m.mu.Unlock()

	form := url.Values{"client_id": []string{m.clientID},
		"device_code": []string{session.deviceCode},
		"grant_type":  []string{"urn:ietf:params:oauth:grant-type:device_code"}}
	response, err := m.exchangeToken(ctx, form)
	if err != nil {
		return DevicePollResult{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lifecycle.generations[m.lifecycle.credentialKey(session.credential.Name)] != session.generation {
		delete(m.sessions, sessionID)
		return DevicePollResult{}, authLifecycleChanged()
	}
	if _, found := m.sessions[sessionID]; !found || !session.expiresAt.After(m.now().UTC()) {
		delete(m.sessions, sessionID)
		return DevicePollResult{}, authLifecycleChanged()
	}
	if response.Error != "" {
		switch response.Error {
		case "authorization_pending":
			return devicePollView(session, DevicePending, false), nil
		case "slow_down":
			session.interval += 5 * time.Second
			session.nextPollAt = now.Add(session.interval)
			m.sessions[sessionID] = session
			return devicePollView(session, DeviceSlowDown, false), nil
		case "expired_token":
			delete(m.sessions, sessionID)
			return devicePollView(session, DeviceExpired, false), nil
		case "access_denied":
			delete(m.sessions, sessionID)
			return devicePollView(session, DeviceDenied, false), nil
		default:
			return DevicePollResult{}, &Error{Code: FailureAuthentication,
				Message: "GitHub rejected device authorization"}
		}
	}
	bundle, err := response.bundle(now)
	if err != nil {
		return DevicePollResult{}, err
	}
	if err := m.storeBundle(ctx, session.credential, bundle); err != nil {
		return DevicePollResult{}, err
	}
	m.lifecycle.generations[m.lifecycle.credentialKey(session.credential.Name)]++
	delete(m.sessions, sessionID)
	result := devicePollView(session, DeviceAuthorized, true)
	result.ExpiresAt = bundle.ExpiresAt
	result.NextPollAt = time.Time{}
	return result, nil
}

func (m *AuthManager) Status(ctx context.Context, ref CredentialReference) (CredentialStatus, error) {
	if m == nil || m.store == nil || ref.Validate() != nil {
		return CredentialStatus{}, errors.New("GitHub credential status request is invalid")
	}
	status := CredentialStatus{ProtocolVersion: ProtocolVersion, Credential: ref,
		StoreKind: m.store.Kind(), StoreAvailable: m.store.Available()}
	raw, found, err := m.store.Get(ctx, ref.Name)
	if err != nil {
		return CredentialStatus{}, &Error{Code: FailureCredential,
			Message: "read GitHub credential status from the system store"}
	}
	status.Configured = found
	if !found || ref.Kind != AuthGitHubAppDevice {
		return status, nil
	}
	bundle, err := decodeTokenBundle(raw)
	if err != nil {
		return CredentialStatus{}, err
	}
	status.Refreshable = bundle.RefreshToken != "" &&
		(bundle.RefreshExpiresAt.IsZero() || bundle.RefreshExpiresAt.After(m.now().UTC()))
	status.ExpiresAt = bundle.ExpiresAt
	status.RefreshExpiresAt = bundle.RefreshExpiresAt
	return status, nil
}

func (m *AuthManager) Disconnect(ctx context.Context, ref CredentialReference) error {
	if m == nil || m.store == nil || ref.Validate() != nil {
		return errors.New("GitHub credential disconnect request is invalid")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.store.Delete(ctx, ref.Name); err != nil {
		return &Error{Code: FailureCredential,
			Message: "delete GitHub credential from the system store"}
	}
	m.lifecycle.generations[m.lifecycle.credentialKey(ref.Name)]++
	m.pruneSessionsLocked(m.now().UTC())
	return nil
}

func (m *AuthManager) configured(ctx context.Context, ref CredentialReference) (bool, error) {
	if m == nil || m.store == nil || ref.Validate() != nil {
		return false, errors.New("GitHub credential reference is invalid")
	}
	return m.store.Configured(ctx, ref.Name)
}

func (m *AuthManager) resolve(ctx context.Context, ref CredentialReference) (tokenLease, error) {
	if m == nil || m.store == nil || ref.Validate() != nil {
		return tokenLease{}, &Error{Code: FailureCredential,
			Message: "GitHub credential reference is invalid"}
	}
	m.mu.Lock()
	generation := m.lifecycle.generations[m.lifecycle.credentialKey(ref.Name)]
	raw, found, err := m.store.Get(ctx, ref.Name)
	m.mu.Unlock()
	if err != nil || !found {
		return tokenLease{}, &Error{Code: FailureCredential,
			Message: "GitHub credential is not configured in the system store"}
	}
	if ref.Kind != AuthGitHubAppDevice {
		if !validOpaqueSecret(raw) {
			return tokenLease{}, &Error{Code: FailureCredential,
				Message: "GitHub credential stored value is invalid"}
		}
		return tokenLease{value: raw, tokenType: "bearer"}, nil
	}
	bundle, err := decodeTokenBundle(raw)
	if err != nil {
		return tokenLease{}, err
	}
	now := m.now().UTC()
	if bundle.ExpiresAt.IsZero() || bundle.ExpiresAt.After(now.Add(2*time.Minute)) {
		return bundle.lease(), nil
	}
	if bundle.RefreshToken == "" || (!bundle.RefreshExpiresAt.IsZero() &&
		!bundle.RefreshExpiresAt.After(now)) {
		return tokenLease{}, &Error{Code: FailureAuthentication,
			Message: "GitHub user access token expired and cannot be refreshed"}
	}
	if !validClientID(m.clientID) {
		return tokenLease{}, &Error{Code: FailureCredential,
			Message: "GitHub App public client id is required to refresh the user access token"}
	}
	form := url.Values{"client_id": []string{m.clientID},
		"grant_type": []string{"refresh_token"}, "refresh_token": []string{bundle.RefreshToken}}
	response, err := m.exchangeToken(ctx, form)
	if err != nil {
		return tokenLease{}, err
	}
	if response.Error != "" {
		return tokenLease{}, &Error{Code: FailureAuthentication,
			Message: "GitHub rejected user access token refresh"}
	}
	refreshed, err := response.bundle(now)
	if err != nil {
		return tokenLease{}, err
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = bundle.RefreshToken
		refreshed.RefreshExpiresAt = bundle.RefreshExpiresAt
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lifecycle.generations[m.lifecycle.credentialKey(ref.Name)] != generation {
		return tokenLease{}, authLifecycleChanged()
	}
	current, found, err := m.store.Get(ctx, ref.Name)
	if err != nil {
		return tokenLease{}, &Error{Code: FailureCredential, Message: "read GitHub credential before token refresh commit"}
	}
	if !found || current != raw {
		return tokenLease{}, authLifecycleChanged()
	}
	if err := m.storeBundle(ctx, ref, refreshed); err != nil {
		return tokenLease{}, err
	}
	return refreshed.lease(), nil
}

type oauthTokenResponse struct {
	AccessToken      string `json:"access_token"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshToken     string `json:"refresh_token"`
	RefreshExpiresIn int    `json:"refresh_token_expires_in"`
	TokenType        string `json:"token_type"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	ErrorURI         string `json:"error_uri"`
	Interval         int    `json:"interval"`
}

func (r oauthTokenResponse) bundle(now time.Time) (tokenBundle, error) {
	if !validOpaqueSecret(r.AccessToken) ||
		(r.RefreshToken != "" && !validOpaqueSecret(r.RefreshToken)) ||
		(r.TokenType != "" && !strings.EqualFold(r.TokenType, "bearer")) ||
		r.ExpiresIn < 0 || r.RefreshExpiresIn < 0 {
		return tokenBundle{}, &Error{Code: FailureMalformed,
			Message: "GitHub OAuth token response is invalid"}
	}
	bundle := tokenBundle{ProtocolVersion: DeviceFlowProtocolVersion,
		AccessToken: r.AccessToken, RefreshToken: r.RefreshToken,
		TokenType: "bearer", Scope: sanitizeIdentity(r.Scope, 512)}
	if r.ExpiresIn > 0 {
		bundle.ExpiresAt = now.Add(time.Duration(r.ExpiresIn) * time.Second)
	}
	if r.RefreshExpiresIn > 0 {
		bundle.RefreshExpiresAt = now.Add(time.Duration(r.RefreshExpiresIn) * time.Second)
	}
	return bundle, nil
}

func (m *AuthManager) exchangeToken(ctx context.Context, form url.Values) (oauthTokenResponse, error) {
	var response oauthTokenResponse
	err := m.postOAuth(ctx, accessTokenPath, form, &response)
	return response, err
}

func (m *AuthManager) postOAuth(ctx context.Context, path string, form url.Values, output any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, m.oauthBase+path,
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("User-Agent", "Prayu-GitHub-Review/"+ProtocolVersion)
	if err := checkNativeWriteDispatch(ctx); err != nil {
		return err
	}
	response, err := m.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return &Error{Code: FailureCancelled, Message: "GitHub authentication was cancelled"}
		}
		return &Error{Code: FailureOffline, Message: "GitHub authentication endpoint is unreachable"}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return &Error{Code: FailureAuthentication,
			Message:    fmt.Sprintf("GitHub authentication returned HTTP %d", response.StatusCode),
			StatusCode: response.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxOAuthBody+1))
	if err != nil {
		return &Error{Code: FailureUnavailable,
			Message: "read GitHub authentication response"}
	}
	if len(data) > maxOAuthBody {
		return &Error{Code: FailureResponseBound,
			Message: "GitHub authentication response exceeded its byte bound"}
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return &Error{Code: FailureMalformed, Message: "GitHub authentication response is malformed"}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return &Error{Code: FailureMalformed, Message: "GitHub authentication response has trailing data"}
	}
	return nil
}

func (m *AuthManager) storeBundle(ctx context.Context, ref CredentialReference, bundle tokenBundle) error {
	encoded, err := json.Marshal(bundle)
	if err != nil || !credential.ValidSecret(string(encoded)) {
		return &Error{Code: FailureCredential,
			Message: "GitHub token bundle does not fit the system credential boundary"}
	}
	if err := m.store.Put(ctx, ref.Name, string(encoded)); err != nil {
		return &Error{Code: FailureCredential,
			Message: "store GitHub credential in the system credential store"}
	}
	return nil
}

func decodeTokenBundle(raw string) (tokenBundle, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var bundle tokenBundle
	if err := decoder.Decode(&bundle); err != nil || bundle.ProtocolVersion != DeviceFlowProtocolVersion ||
		!validOpaqueSecret(bundle.AccessToken) ||
		(bundle.RefreshToken != "" && !validOpaqueSecret(bundle.RefreshToken)) ||
		bundle.TokenType != "bearer" ||
		(!bundle.RefreshExpiresAt.IsZero() && bundle.ExpiresAt.After(bundle.RefreshExpiresAt)) {
		return tokenBundle{}, &Error{Code: FailureCredential,
			Message: "GitHub App credential bundle is invalid"}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return tokenBundle{}, &Error{Code: FailureCredential,
			Message: "GitHub App credential bundle has trailing data"}
	}
	return bundle, nil
}

func (b tokenBundle) lease() tokenLease {
	return tokenLease{value: b.AccessToken, tokenType: b.TokenType,
		scope: b.Scope, expiresAt: b.ExpiresAt}
}

func devicePollView(session deviceSession, state DevicePollState, configured bool) DevicePollResult {
	return DevicePollResult{ProtocolVersion: DeviceFlowProtocolVersion,
		SessionID: session.id, State: state, Credential: session.credential,
		Configured: configured, ExpiresAt: session.expiresAt,
		NextPollAt: session.nextPollAt}
}

func authLifecycleChanged() error {
	return &Error{Code: FailureCancelled, Message: "GitHub authorization changed while the request was in flight"}
}

func (m *AuthManager) pruneSessionsLocked(now time.Time) {
	for id, session := range m.sessions {
		if !session.expiresAt.After(now) || session.generation != m.lifecycle.generations[m.lifecycle.credentialKey(session.credential.Name)] {
			delete(m.sessions, id)
		}
	}
}

func validOpaqueSecret(value string) bool {
	return credential.ValidSecret(value) && len(value) >= 8
}

func validUserCode(value string) bool {
	return validIdentity(value) && len(value) <= 32
}

func loopbackHostname(value string) bool {
	if strings.EqualFold(value, "localhost") {
		return true
	}
	ip := net.ParseIP(value)
	return ip != nil && ip.IsLoopback()
}

func parseSecondsHeader(value string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds <= 0 || seconds > 24*60*60 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}
