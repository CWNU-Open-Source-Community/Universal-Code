package mcpoauth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/privatefile"
)

const storedPrefix = "uc-mcp-oauth-file-v1:"
const encryptedVersion = "universal-code.mcp-oauth.v1\x00"

var errStorage = errors.New("MCP encrypted credential storage could not be verified")

// Store is an MCP-only view of the system store. Static credentials and legacy
// OAuth envelopes remain readable there. New OAuth writes keep a fresh AES key
// and opaque file ID in that same OS entry, and only ciphertext on disk.
// Use the original system store for provider credentials and other features.
type Store struct {
	Dir      string
	KeyStore credential.Store
}

func (s Store) Kind() string {
	if s.KeyStore == nil {
		return "unsupported"
	}
	// This identifies the OS store that protects the encryption keys.
	return s.KeyStore.Kind()
}
func (s Store) Available() bool {
	return s.Dir != "" && s.KeyStore != nil && s.KeyStore.Available()
}

func (s Store) Get(ctx context.Context, name string) (string, bool, error) {
	if !s.Available() || !credential.ValidName(name) || ctx.Err() != nil {
		return "", false, errStorage
	}
	raw, found, err := s.KeyStore.Get(ctx, name)
	if err != nil {
		return "", false, errStorage
	}
	if !found || !strings.HasPrefix(raw, storedPrefix) {
		return raw, found, nil
	}
	value, err := s.readFile(name, raw)
	if err != nil {
		return "", false, errStorage
	}
	return value, true, nil
}

func (s Store) Configured(ctx context.Context, name string) (bool, error) {
	_, found, err := s.Get(ctx, name)
	return found, err
}

func (s Store) withLock(ctx context.Context, name string, fn func() error) error {
	if !s.Available() || !credential.ValidName(name) || ctx.Err() != nil {
		return errStorage
	}
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		return errStorage
	}
	if err := os.Chmod(s.Dir, 0700); err != nil {
		return errStorage
	}
	// One short commit lock also covers credential-name aliases in OS stores.
	path := filepath.Join(s.Dir, "credentials.lock")
	if err := privatefile.WithLock(ctx, path, fn); err != nil {
		return errStorage
	}
	return nil
}

func (s Store) Put(ctx context.Context, name, value string) error {
	return s.withLock(ctx, name, func() error {
		prior, _, err := s.KeyStore.Get(ctx, name)
		if err != nil {
			return errStorage
		}
		return s.put(ctx, name, value, prior)
	})
}

// CompareAndSwap performs the final comparison and write under one file lock.
// Cooperating CLI/Desktop processes cannot overwrite an intervening login,
// refresh, static replacement or deletion. OS writers outside this view still
// cannot participate in the lock; exact readback detects observed conflicts.
func (s Store) CompareAndSwap(ctx context.Context, name, prior string, priorFound bool, value string) error {
	return s.withLock(ctx, name, func() error {
		physical, found, err := s.KeyStore.Get(ctx, name)
		if err != nil || found != priorFound {
			return errStorage
		}
		current := physical
		if found && strings.HasPrefix(physical, storedPrefix) {
			current, err = s.readFile(name, physical)
		}
		if err != nil || current != prior {
			return errStorage
		}
		return s.put(ctx, name, value, physical)
	})
}

// put stages and verifies an immutable ciphertext file before publishing its
// key/reference in the OS entry. Failure before publication preserves prior.
// An unverified OS write is never rolled back or automatically retried.
func (s Store) put(ctx context.Context, name, value, prior string) error {
	physical := value
	if strings.HasPrefix(value, prefix) {
		if _, err := decode(value); err != nil {
			return errStorage
		}
		var err error
		physical, err = s.writeFile(name, value)
		if err != nil {
			return errStorage
		}
	} else if IsEnvelope(value) || !credential.ValidSecret(value) {
		return errStorage
	}
	if err := s.KeyStore.Put(ctx, name, physical); err != nil {
		// Put may have committed despite an error. Retain its ciphertext so an
		// operator can inspect status without losing a possibly active entry.
		return errStorage
	}
	current, found, err := s.KeyStore.Get(ctx, name)
	if err != nil || !found || current != physical {
		return errStorage
	}
	if strings.HasPrefix(physical, storedPrefix) {
		verified, err := s.readFile(name, physical)
		if err != nil || verified != value {
			return errStorage
		}
	}
	if prior != physical {
		return s.removeFile(prior)
	}
	return nil
}

func (s Store) Delete(ctx context.Context, name string) error {
	return s.withLock(ctx, name, func() error {
		prior, _, err := s.KeyStore.Get(ctx, name)
		if err != nil {
			return errStorage
		}
		if err := s.KeyStore.Delete(ctx, name); err != nil {
			return errStorage
		}
		if _, found, err := s.KeyStore.Get(ctx, name); err != nil || found {
			return errStorage
		}
		return s.removeFile(prior)
	})
}

func storedParts(raw string) (string, []byte, error) {
	if !strings.HasPrefix(raw, storedPrefix) {
		return "", nil, errStorage
	}
	id, encoded, ok := strings.Cut(strings.TrimPrefix(raw, storedPrefix), ":")
	decodedID, err := hex.DecodeString(id)
	if !ok || err != nil || len(decodedID) != 16 || hex.EncodeToString(decodedID) != id {
		return "", nil, errStorage
	}
	key, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return "", nil, errStorage
	}
	return id, key, nil
}

func bundleAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errStorage
	}
	return cipher.NewGCM(block)
}

func (s Store) writeFile(name, value string) (string, error) {
	key, idBytes := make([]byte, 32), make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return "", errStorage
	}
	if _, err := rand.Read(idBytes); err != nil {
		return "", errStorage
	}
	id := hex.EncodeToString(idBytes)
	aead, err := bundleAEAD(key)
	if err != nil {
		return "", errStorage
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", errStorage
	}
	data := append([]byte(encryptedVersion), nonce...)
	data = aead.Seal(data, nonce, []byte(value), []byte(encryptedVersion+name+"\x00"+id))
	path := filepath.Join(s.Dir, id+".enc")
	if err := privatefile.Write(path, data, nil); err != nil {
		return "", errStorage
	}
	physical := storedPrefix + id + ":" + base64.RawURLEncoding.EncodeToString(key)
	verified, err := s.readFile(name, physical)
	if err != nil || verified != value {
		_ = os.Remove(path) // The OS entry has not been touched yet.
		return "", errStorage
	}
	return physical, nil
}

func (s Store) readFile(name, raw string) (string, error) {
	id, key, err := storedParts(raw)
	if err != nil {
		return "", errStorage
	}
	aead, err := bundleAEAD(key)
	if err != nil {
		return "", errStorage
	}
	f, err := os.Open(filepath.Join(s.Dir, id+".enc"))
	if err != nil {
		return "", errStorage
	}
	defer f.Close()
	header := len(encryptedVersion) + aead.NonceSize()
	max := header + MaxBundleBytes + aead.Overhead()
	data, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if err != nil || len(data) > max || len(data) < header+aead.Overhead() || !strings.HasPrefix(string(data), encryptedVersion) {
		return "", errStorage
	}
	plain, err := aead.Open(nil, data[len(encryptedVersion):header], data[header:], []byte(encryptedVersion+name+"\x00"+id))
	if err != nil {
		return "", errStorage
	}
	value := string(plain)
	if _, err := decode(value); err != nil {
		return "", errStorage
	}
	return value, nil
}

func (s Store) removeFile(raw string) error {
	if !strings.HasPrefix(raw, storedPrefix) {
		return nil
	}
	id, _, err := storedParts(raw)
	if err != nil {
		return errStorage
	}
	if err := os.Remove(filepath.Join(s.Dir, id+".enc")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errStorage
	}
	return nil
}

// Replace retains support for injected and legacy stores. Callers hold the
// credential's in-process lock; the encrypted Store adds cross-process CAS.
func Replace(ctx context.Context, store credential.Store, name, prior string, priorFound bool, value string) error {
	if atomic, ok := store.(interface {
		CompareAndSwap(context.Context, string, string, bool, string) error
	}); ok {
		return atomic.CompareAndSwap(ctx, name, prior, priorFound, value)
	}
	current, found, err := store.Get(ctx, name)
	if err != nil || found != priorFound || current != prior || ctx.Err() != nil {
		return ErrOAuth
	}
	if err := store.Put(ctx, name, value); err != nil {
		return ErrOAuth
	}
	current, found, err = store.Get(ctx, name)
	if err != nil || !found || current != value {
		return ErrOAuth
	}
	return nil
}
