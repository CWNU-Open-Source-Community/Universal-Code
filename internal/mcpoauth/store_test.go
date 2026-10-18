package mcpoauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/credential"
	"golang.org/x/oauth2"
)

func storedFixture(t *testing.T, large bool) (*envelope, string) {
	t.Helper()
	e := &envelope{Session: "test-session", Fingerprint: strings.Repeat("a", 64), Target: "https://mcp.example/mcp", Resource: "https://mcp.example/mcp",
		Issuer: "https://issuer.example", ClientID: "test-client", ClientSecret: "private-client-secret", TokenEndpoint: "https://issuer.example/token",
		Token: &oauth2.Token{AccessToken: "private-access", RefreshToken: "private-refresh", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}}
	if large {
		e.Token.AccessToken = strings.Repeat("private-access-", 600)
		e.Token.RefreshToken = strings.Repeat("private-refresh-", 600)
	}
	raw, err := encode(e)
	if err != nil {
		t.Fatal(err)
	}
	return e, raw
}

func ciphertextPath(t *testing.T, s Store, name string) string {
	t.Helper()
	raw, found, err := s.KeyStore.Get(t.Context(), name)
	if err != nil || !found {
		t.Fatal("missing OS reference")
	}
	id, _, err := storedParts(raw)
	if err != nil {
		t.Fatal("invalid OS reference")
	}
	return filepath.Join(s.Dir, id+".enc")
}

func TestEncryptedStoreLargeBundleRestartReplacementAndDeletion(t *testing.T) {
	e, raw := storedFixture(t, true)
	if len(raw) <= credential.MaxSecretBytes {
		t.Fatal("fixture does not exceed OS limit")
	}
	keys := credential.NewMemoryStore()
	s := Store{Dir: t.TempDir(), KeyStore: keys}
	if err := s.Put(t.Context(), "oauth-test", raw); err != nil {
		t.Fatal(err)
	}
	physical, _, _ := keys.Get(t.Context(), "oauth-test")
	if !credential.ValidSecret(physical) || !strings.HasPrefix(physical, storedPrefix) {
		t.Fatal("OS store did not receive a bounded key/reference")
	}
	path := ciphertextPath(t, s, "oauth-test")
	ciphertext, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{raw, e.Token.AccessToken, e.Token.RefreshToken, e.ClientSecret, physical} {
		if bytes.Contains(ciphertext, []byte(secret)) || (secret != physical && strings.Contains(physical, secret)) {
			t.Fatal("plaintext credential was persisted outside its owner")
		}
	}
	restarted := Store{Dir: s.Dir, KeyStore: keys}
	read, found, err := restarted.Get(t.Context(), "oauth-test")
	if err != nil || !found || read != raw {
		t.Fatal("restart could not read the large bundle")
	}
	b := Binding{Ref: "oauth-test", Fingerprint: e.Fingerprint, Target: e.Target}
	if status, err := StoredStatus(read, b); err != nil || status.State != "authorized" {
		t.Fatal("large-bundle status failed")
	}
	if bearer, err := LocalBearer(read, b); err != nil || bearer != e.Token.AccessToken {
		t.Fatal("large bearer was not resolved")
	}
	e.Token.AccessToken = strings.Repeat("rotated-access-", 600)
	updated, err := encode(e)
	if err != nil || RevisionIdentity(raw) != RevisionIdentity(updated) {
		t.Fatal("refresh changed session identity")
	}
	if err := Replace(t.Context(), restarted, b.Ref, raw, true, updated); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("superseded ciphertext remained")
	}
	path = ciphertextPath(t, s, b.Ref)
	if err := restarted.Put(t.Context(), b.Ref, "replacement-static-token"); err != nil {
		t.Fatal(err)
	}
	if read, _, err := keys.Get(t.Context(), b.Ref); err != nil || read != "replacement-static-token" {
		t.Fatal("static token did not stay in the OS store")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("static replacement retained the old ciphertext")
	}
	if err := restarted.Put(t.Context(), b.Ref, updated); err != nil {
		t.Fatal(err)
	}
	path = ciphertextPath(t, s, b.Ref)
	if err := restarted.Delete(t.Context(), b.Ref); err != nil {
		t.Fatal(err)
	}
	if found, err := restarted.Configured(t.Context(), b.Ref); err != nil || found {
		t.Fatal("deleted credential remains configured")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("delete retained the active ciphertext")
	}
	if err := restarted.Put(t.Context(), b.Ref, strings.Repeat("s", credential.MaxSecretBytes+1)); err == nil {
		t.Fatal("static credential limit was enlarged")
	}
}

type failingBundleKeys struct {
	credential.Store
	failWrite bool
	drift     bool
	writes    int
}

func (s *failingBundleKeys) Put(ctx context.Context, name, value string) error {
	s.writes++
	if s.failWrite {
		return errors.New("private-backend-error-with-token")
	}
	return s.Store.Put(ctx, name, value)
}
func (s *failingBundleKeys) Get(ctx context.Context, name string) (string, bool, error) {
	if s.drift && s.writes > 0 {
		return "", false, errors.New("private-backend-error-with-token")
	}
	return s.Store.Get(ctx, name)
}

func TestEncryptedStoreLegacyMigrationAndPrecommitFailures(t *testing.T) {
	_, legacy := storedFixture(t, false)
	_, large := storedFixture(t, true)
	for _, scenario := range []string{"success", "OS-write", "directory", "oversized"} {
		t.Run(scenario, func(t *testing.T) {
			keys := credential.NewMemoryStore()
			if err := keys.Put(t.Context(), "oauth-test", legacy); err != nil {
				t.Fatal(err)
			}
			backend := &failingBundleKeys{Store: keys, failWrite: scenario == "OS-write"}
			s := Store{Dir: filepath.Join(t.TempDir(), "bundles"), KeyStore: backend}
			read, found, err := s.Get(t.Context(), "oauth-test")
			if err != nil || !found || read != legacy {
				t.Fatal("legacy bundle became unreadable")
			}
			if physical, _, _ := keys.Get(t.Context(), "oauth-test"); physical != legacy {
				t.Fatal("read migrated the legacy entry")
			}
			if scenario == "directory" {
				if err := os.WriteFile(s.Dir, []byte("blocked"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			next := large
			if scenario == "oversized" {
				next = prefix + strings.Repeat("a", MaxBundleBytes)
			}
			err = Replace(t.Context(), s, "oauth-test", legacy, true, next)
			if scenario == "success" {
				if err != nil {
					t.Fatal(err)
				}
				if read, _, err := s.Get(t.Context(), "oauth-test"); err != nil || read != large {
					t.Fatal("migration did not preserve the logical bundle")
				}
				return
			}
			if err == nil || strings.Contains(err.Error(), "private-backend") {
				t.Fatal("failure was hidden or disclosed a backend secret")
			}
			if read, _, err := keys.Get(t.Context(), "oauth-test"); err != nil || read != legacy {
				t.Fatal("precommit failure changed the prior credential")
			}
		})
	}
}

func TestEncryptedStoreUnverifiedCommitRetainsRecoverableFile(t *testing.T) {
	_, raw := storedFixture(t, true)
	keys := &failingBundleKeys{Store: credential.NewMemoryStore(), drift: true}
	s := Store{Dir: t.TempDir(), KeyStore: keys}
	if err := s.Put(t.Context(), "oauth-test", raw); err == nil || strings.Contains(err.Error(), "private-backend") {
		t.Fatal("unverified commit was reported as success or exposed backend data")
	}
	keys.drift = false
	if got, found, err := s.Get(t.Context(), "oauth-test"); err != nil || !found || got != raw || keys.writes != 1 {
		t.Fatal("unverified commit was rolled back, deleted or retried")
	}
}

func TestEncryptedStoreTamperMissingKeyAndReferenceIsolation(t *testing.T) {
	_, raw := storedFixture(t, true)
	for _, scenario := range []string{"ciphertext", "missing-file", "wrong-key", "missing-key", "wrong-ref", "oversized-file"} {
		t.Run(scenario, func(t *testing.T) {
			keys := credential.NewMemoryStore()
			s := Store{Dir: t.TempDir(), KeyStore: keys}
			if err := s.Put(t.Context(), "oauth-test", raw); err != nil {
				t.Fatal(err)
			}
			physical, _, _ := keys.Get(t.Context(), "oauth-test")
			path := ciphertextPath(t, s, "oauth-test")
			name := "oauth-test"
			switch scenario {
			case "ciphertext":
				data, _ := os.ReadFile(path)
				data[len(data)-1] ^= 1
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-file":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "wrong-key":
				id, _, _ := storedParts(physical)
				_ = keys.Put(t.Context(), name, storedPrefix+id+":"+base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
			case "missing-key":
				_ = keys.Delete(t.Context(), name)
			case "wrong-ref":
				name = "other-ref"
				_ = keys.Put(t.Context(), name, physical)
			case "oversized-file":
				if err := os.WriteFile(path, bytes.Repeat([]byte{'x'}, MaxBundleBytes+1024), 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, found, err := s.Get(t.Context(), name)
			if scenario == "missing-key" {
				if err != nil || found {
					t.Fatal("missing OS entry recovered a token from disk")
				}
			} else if err == nil || found {
				t.Fatal("unverified ciphertext was accepted")
			}
		})
	}
}

func TestEncryptedStoreCASRejectsInterveningChanges(t *testing.T) {
	_, prior := storedFixture(t, true)
	s := Store{Dir: t.TempDir(), KeyStore: credential.NewMemoryStore()}
	if err := s.Put(t.Context(), "oauth-test", prior); err != nil {
		t.Fatal(err)
	}
	other := Store{Dir: s.Dir, KeyStore: s.KeyStore}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, value := range []string{"static-first", "static-second"} {
		wg.Go(func() {
			<-start
			results <- Replace(t.Context(), other, "oauth-test", prior, true, value)
		})
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatal("CAS did not preserve the winning replacement")
	}
	if err := other.Delete(t.Context(), "oauth-test"); err != nil {
		t.Fatal(err)
	}
	if err := Replace(t.Context(), s, "oauth-test", prior, true, prior); err == nil {
		t.Fatal("stale OAuth result resurrected a deleted entry")
	}
	if err := s.Put(t.Context(), "oauth-test", "new-static-token"); err != nil {
		t.Fatal(err)
	}
	if err := Replace(t.Context(), s, "oauth-test", "", false, prior); err == nil {
		t.Fatal("login from an absent entry overwrote a new static token")
	}
}

func TestOAuthBundleAndTokenBounds(t *testing.T) {
	e, _ := storedFixture(t, false)
	e.Token.AccessToken = strings.Repeat("a", MaxTokenBytes)
	e.Token.RefreshToken = strings.Repeat("r", MaxTokenBytes)
	if raw, err := encode(e); err != nil || len(raw) > MaxBundleBytes {
		t.Fatal("bounded large tokens were rejected")
	}
	e.Token.AccessToken += "a"
	if _, err := encode(e); err == nil {
		t.Fatal("oversized access token was accepted")
	}
	e.Token.AccessToken = "access"
	e.Token.RefreshToken += "r"
	if _, err := encode(e); err == nil {
		t.Fatal("oversized refresh token was accepted")
	}
	e.Token.RefreshToken = "refresh"
	e.Session = strings.Repeat("s", MaxBundleBytes)
	if _, err := encode(e); err == nil {
		t.Fatal("oversized bundle was accepted")
	}
	if _, err := decode(prefix + strings.Repeat("a", MaxBundleBytes)); err == nil {
		t.Fatal("oversized stored bundle was decoded")
	}
	for _, value := range []string{"token with-space", "token\r\nheader", "token\x00", "\xff"} {
		if validToken(value) {
			t.Fatal("unsafe token was accepted")
		}
	}
}

// Explicit opt-in writes synthetic credentials to the platform OS store. No
// user account or external authorization service is involved.
func TestOAuthSystemStoreLifecycle(t *testing.T) {
	if os.Getenv("CYBERAGENT_MCP_OS_STORE_TEST") != "1" {
		t.Skip("set CYBERAGENT_MCP_OS_STORE_TEST=1 for synthetic OS-store acceptance")
	}
	keys := credential.NewSystemStore()
	if !keys.Available() {
		t.Fatal("system store unavailable")
	}
	s := Store{Dir: t.TempDir(), KeyStore: keys}
	ref := "mcp-oauth-test-" + strings.ToLower(rand.Text())
	t.Cleanup(func() {
		if err := s.Delete(context.Background(), ref); err != nil {
			t.Error("could not remove synthetic OS credential")
		}
	})
	testLargeOAuthLifecycle(t, s, ref)
}
