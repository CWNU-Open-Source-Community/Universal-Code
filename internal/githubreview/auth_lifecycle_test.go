package githubreview

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/credential"
)

type lifecycleRoundTrip func(*http.Request) (*http.Response, error)

func (f lifecycleRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

const lifecycleDeviceResponse = `{"device_code":"synthetic-device","user_code":"ABCD-EFGH","verification_uri":"https://github.com/login/device","expires_in":900,"interval":1}`
const lifecycleTokenResponse = `{"access_token":"synthetic-renewed","refresh_token":"synthetic-rotated","token_type":"bearer","expires_in":3600}`

func lifecycleHTTPResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestAuthDisconnectInvalidatesInflightResponsesAcrossManagers(t *testing.T) {
	for _, kind := range []string{"refresh", "begin", "poll", "slow_down"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			store := credential.NewMemoryStore()
			lifecycle, err := NewAuthLifecycle(store)
			if err != nil {
				t.Fatal(err)
			}
			manager, err := lifecycle.NewManager("Iv1.client")
			if err != nil {
				t.Fatal(err)
			}
			// Represents another repository connection or a replacement cache entry.
			alias, err := lifecycle.NewManager("Iv1.other-client")
			if err != nil {
				t.Fatal(err)
			}
			ref := CredentialReference{Name: "shared-credential", Kind: AuthGitHubAppDevice}
			if err := manager.storeBundle(ctx, ref, tokenBundle{ProtocolVersion: DeviceFlowProtocolVersion,
				AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", TokenType: "bearer", ExpiresAt: time.Now().Add(-time.Hour)}); err != nil {
				t.Fatal(err)
			}
			manager.httpClient = &http.Client{Transport: lifecycleRoundTrip(func(*http.Request) (*http.Response, error) {
				return lifecycleHTTPResponse(lifecycleDeviceResponse), nil
			})}
			var sessionID string
			if kind == "poll" || kind == "slow_down" {
				authorization, err := manager.BeginDeviceFlow(ctx, ref)
				if err != nil {
					t.Fatal(err)
				}
				sessionID = authorization.SessionID
			}
			entered, release := make(chan struct{}), make(chan struct{})
			manager.httpClient = &http.Client{Transport: lifecycleRoundTrip(func(request *http.Request) (*http.Response, error) {
				close(entered)
				select {
				case <-release:
				case <-request.Context().Done():
					return nil, request.Context().Err()
				}
				body := lifecycleTokenResponse
				if kind == "begin" {
					body = lifecycleDeviceResponse
				}
				if kind == "slow_down" {
					body = `{"error":"slow_down"}`
				}
				return lifecycleHTTPResponse(body), nil
			})}
			done := make(chan error, 1)
			go func() {
				var err error
				switch kind {
				case "refresh":
					_, err = manager.resolve(ctx, ref)
				case "begin":
					_, err = manager.BeginDeviceFlow(ctx, ref)
				default:
					_, err = manager.PollDeviceFlow(ctx, sessionID)
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if err := alias.Disconnect(ctx, ref); err != nil {
				t.Fatal(err)
			}
			close(release)
			var responseErr error
			select {
			case responseErr = <-done:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			var failure *Error
			if !errors.As(responseErr, &failure) || failure.Code != FailureCancelled {
				t.Fatalf("stale %s accepted: %v", kind, responseErr)
			}
			if configured, err := store.Configured(ctx, ref.Name); err != nil || configured {
				t.Fatalf("credential resurrected: configured=%t err=%v", configured, err)
			}
			if len(manager.sessions) != 0 {
				t.Fatal("stale device session survived")
			}
			// A new explicit login remains usable after invalidating the old work.
			manager.httpClient = &http.Client{Transport: lifecycleRoundTrip(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path == deviceCodePath {
					return lifecycleHTTPResponse(lifecycleDeviceResponse), nil
				}
				return lifecycleHTTPResponse(lifecycleTokenResponse), nil
			})}
			login, err := manager.BeginDeviceFlow(ctx, ref)
			if err != nil {
				t.Fatal(err)
			}
			result, err := manager.PollDeviceFlow(ctx, login.SessionID)
			if err != nil || !result.Configured {
				t.Fatalf("reconnect=%+v err=%v", result, err)
			}
		})
	}
}

func TestAuthDeviceLoginCannotOverwriteImportedToken(t *testing.T) {
	store := credential.NewMemoryStore()
	lifecycle, _ := NewAuthLifecycle(store)
	manager, _ := lifecycle.NewManager("Iv1.client")
	manager.httpClient = &http.Client{Transport: lifecycleRoundTrip(func(*http.Request) (*http.Response, error) {
		return lifecycleHTTPResponse(lifecycleDeviceResponse), nil
	})}
	ref := CredentialReference{Name: "shared-credential", Kind: AuthGitHubAppDevice}
	login, err := manager.BeginDeviceFlow(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	pat := CredentialReference{Name: ref.Name, Kind: AuthFineGrainedPAT}
	if err := lifecycle.ImportToken(t.Context(), pat, "synthetic-imported-pat"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PollDeviceFlow(t.Context(), login.SessionID); err == nil {
		t.Fatal("invalidated device login survived import")
	}
	lease, err := manager.resolve(t.Context(), pat)
	if err != nil || lease.value != "synthetic-imported-pat" {
		t.Fatalf("imported credential changed: %v", err)
	}
}

func TestAuthRefreshDoesNotOverwriteReplacedBundle(t *testing.T) {
	store := credential.NewMemoryStore()
	manager, _ := NewAuthManager(store, "Iv1.client")
	ref := CredentialReference{Name: "rotated", Kind: AuthGitHubAppDevice}
	old := tokenBundle{ProtocolVersion: DeviceFlowProtocolVersion, AccessToken: "synthetic-old", RefreshToken: "synthetic-refresh", TokenType: "bearer", ExpiresAt: time.Now().Add(-time.Hour)}
	if err := manager.storeBundle(t.Context(), ref, old); err != nil {
		t.Fatal(err)
	}
	manager.httpClient = &http.Client{Transport: lifecycleRoundTrip(func(request *http.Request) (*http.Response, error) {
		rotated := old
		rotated.AccessToken, rotated.ExpiresAt = "synthetic-current", time.Now().Add(time.Hour)
		if err := manager.storeBundle(request.Context(), ref, rotated); err != nil {
			return nil, err
		}
		return lifecycleHTTPResponse(lifecycleTokenResponse), nil
	})}
	if _, err := manager.resolve(t.Context(), ref); err == nil {
		t.Fatal("stale refresh overwrote newer bundle")
	}
	lease, err := manager.resolve(t.Context(), ref)
	if err != nil || lease.value != "synthetic-current" {
		t.Fatalf("newer bundle lost: %v", err)
	}
}

// Windows Credential Manager treats differently cased target names as aliases.
type caseInsensitiveCredentialStore struct{ *credential.MemoryStore }

func (caseInsensitiveCredentialStore) Kind() string { return "windows_credential_manager" }
func (s caseInsensitiveCredentialStore) Put(ctx context.Context, name, value string) error {
	return s.MemoryStore.Put(ctx, strings.ToLower(name), value)
}
func (s caseInsensitiveCredentialStore) Get(ctx context.Context, name string) (string, bool, error) {
	return s.MemoryStore.Get(ctx, strings.ToLower(name))
}
func (s caseInsensitiveCredentialStore) Delete(ctx context.Context, name string) error {
	return s.MemoryStore.Delete(ctx, strings.ToLower(name))
}
func (s caseInsensitiveCredentialStore) Configured(ctx context.Context, name string) (bool, error) {
	return s.MemoryStore.Configured(ctx, strings.ToLower(name))
}

func TestAuthCredentialCaseAliasesInvalidateInflightPoll(t *testing.T) {
	for _, action := range []string{"disconnect", "import"} {
		t.Run(action, func(t *testing.T) {
			store := caseInsensitiveCredentialStore{credential.NewMemoryStore()}
			lifecycle, _ := NewAuthLifecycle(store)
			manager, _ := lifecycle.NewManager("Iv1.client")
			alias, _ := lifecycle.NewManager("Iv1.other-client")
			ref := CredentialReference{Name: "Shared-Credential", Kind: AuthGitHubAppDevice}
			aliasRef := CredentialReference{Name: "shared-credential", Kind: AuthGitHubAppDevice}
			manager.httpClient = &http.Client{Transport: lifecycleRoundTrip(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path == deviceCodePath {
					return lifecycleHTTPResponse(lifecycleDeviceResponse), nil
				}
				if action == "disconnect" {
					if err := alias.Disconnect(request.Context(), aliasRef); err != nil {
						return nil, err
					}
				} else {
					aliasRef.Kind = AuthFineGrainedPAT
					if err := lifecycle.ImportToken(request.Context(), aliasRef, "synthetic-replacement"); err != nil {
						return nil, err
					}
				}
				return lifecycleHTTPResponse(lifecycleTokenResponse), nil
			})}
			login, err := manager.BeginDeviceFlow(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.PollDeviceFlow(t.Context(), login.SessionID); err == nil {
				t.Fatal("stale poll accepted after credential alias changed")
			}
			value, found, err := store.Get(t.Context(), ref.Name)
			if err != nil || (action == "disconnect" && found) || (action == "import" && (!found || value != "synthetic-replacement")) {
				t.Fatalf("credential alias change was overwritten: found=%t err=%v", found, err)
			}
		})
	}
}
