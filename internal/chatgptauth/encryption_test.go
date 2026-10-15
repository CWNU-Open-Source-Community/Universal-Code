package chatgptauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/credential"
)

func TestEncryptedSessionRoundTrip(t *testing.T) {
	s := testStore(t)
	a := testAuth()
	a.AccessToken = strings.Repeat("private-access-token-", 300)
	a.RefreshToken = "private-refresh-token"
	a.IDToken = "private-id-token"
	a.PendingRefresh = &pendingRefresh{Response: tokenResponse{Access: "pending-access", Refresh: "pending-refresh", ID: "pending-id"}, ReceivedAt: time.Now()}
	saveTest(t, s, a)
	path := filepath.Join(s.Dir, "accounts.enc")
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{a.AccessToken, a.RefreshToken, a.IDToken, a.Email, "pending-access", "pending-refresh", "pending-id"} {
		if bytes.Contains(first, []byte(secret)) {
			t.Fatal("session file contains plaintext credentials or identity")
		}
	}
	name, err := s.keyName()
	if err != nil {
		t.Fatal(err)
	}
	key, found, err := s.KeyStore.Get(context.Background(), name)
	if err != nil || !found || len(key) > credential.MaxSecretBytes {
		t.Fatal("invalid key-store entry", err)
	}
	decoded, err := base64.RawStdEncoding.DecodeString(key)
	if err != nil || len(decoded) != 32 {
		t.Fatal("not an AES-256 key", err)
	}
	restarted := Store{Dir: s.Dir, KeyStore: s.KeyStore}
	got, err := restarted.Selected(a.ClientID)
	if err != nil || got.AccessToken != a.AccessToken || got.PendingRefresh.Response.Refresh != "pending-refresh" {
		t.Fatal("encrypted restart recovery failed", err)
	}
	if err := restarted.Select(context.Background(), a.ClientID); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil || bytes.Equal(first, second) {
		t.Fatal("encryption nonce was reused", err)
	}
}

func TestEncryptedSessionKeyFailuresPreserveFile(t *testing.T) {
	for _, scenario := range []string{"missing", "malformed", "wrong", "tampered", "unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			s := testStore(t)
			saveTest(t, s, testAuth())
			name, _ := s.keyName()
			path := filepath.Join(s.Dir, "accounts.enc")
			if scenario == "missing" {
				_ = s.KeyStore.Delete(context.Background(), name)
			}
			if scenario == "malformed" {
				_ = s.KeyStore.Put(context.Background(), name, "not-a-key")
			}
			if scenario == "wrong" {
				_ = s.KeyStore.Put(context.Background(), name, base64.RawStdEncoding.EncodeToString(make([]byte, 32)))
			}
			if scenario == "unavailable" {
				s.KeyStore = nil
			}
			if scenario == "tampered" {
				b, _ := os.ReadFile(path)
				var e encryptedDatabase
				if err := json.Unmarshal(b, &e); err != nil {
					t.Fatal(err)
				}
				e.Ciphertext[0] ^= 1
				b, _ = json.Marshal(e)
				if err := os.WriteFile(path, b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(path)
			if _, err := s.Snapshot(); err == nil {
				t.Fatal("unreadable encrypted session accepted")
			}
			if err := s.Clear(context.Background(), ""); err == nil {
				t.Fatal("clear overwrote unreadable session")
			}
			ready := false
			if _, err := NewClient().Login(context.Background(), s, "", true, func(string) { ready = true }); err == nil || ready {
				t.Fatal("login continued without readable durable storage", err)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("key failure changed existing session")
			}
			if scenario == "missing" {
				if _, found, _ := s.KeyStore.Get(context.Background(), name); found {
					t.Fatal("missing key was regenerated")
				}
			}
		})
	}
}

type failingKeyStore struct{ credential.Store }

func (s failingKeyStore) Put(context.Context, string, string) error {
	return errors.New("key persistence unavailable")
}

func TestLoginRequiresKeyPersistenceBeforeURL(t *testing.T) {
	s := testStore(t)
	s.KeyStore = failingKeyStore{s.KeyStore}
	ready := false
	if _, err := NewClient().Login(context.Background(), s, "", true, func(string) { ready = true }); err == nil || ready {
		t.Fatal("login started before key persistence", err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "accounts.enc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed key persistence created a session", err)
	}
}

func TestSaveModelsRejectsStaleAccount(t *testing.T) {
	for _, scenario := range []string{"valid", "logout", "revision", "subject", "issuer", "account"} {
		t.Run(scenario, func(t *testing.T) {
			s := testStore(t)
			a := testAuth()
			saveTest(t, s, a)
			models := []Model{{Slug: "fixture-model", DisplayName: "Fixture model", Visibility: "list"}}
			switch scenario {
			case "logout":
				if err := s.Clear(context.Background(), a.ClientID); err != nil {
					t.Fatal(err)
				}
			case "revision":
				a.Revision++
			case "subject":
				a.Subject = "different"
			case "issuer":
				a.Issuer = "different"
			case "account":
				a.ClientID = "different"
			}
			err := s.SaveModels(context.Background(), &a, models)
			if (err == nil) != (scenario == "valid") {
				t.Fatal("unexpected discovery commit", err)
			}
			got, err := s.Selected("")
			if err != nil {
				t.Fatal(err)
			}
			if (len(got.Models) == 1) != (scenario == "valid") {
				t.Fatal("stale catalog was persisted")
			}
		})
	}
}
