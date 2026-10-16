package application

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
)

const sandboxEnvironmentSettingsFile = "sandbox-environment.json"
const maxSandboxEnvironmentSettingsBytes = 8 * 1024

type FileSandboxEnvironmentSettingsStore struct {
	mu       sync.Mutex
	home     string
	homeInfo os.FileInfo
}

type sandboxEnvironmentSettingsRecord struct {
	Version  string                     `json:"version"`
	Revision int64                      `json:"revision"`
	Settings SandboxEnvironmentSettings `json:"settings"`
}

// appHome is selected by Go at startup, never by an HTTP request or model.
func NewFileSandboxEnvironmentSettingsStore(appHome string) (*FileSandboxEnvironmentSettingsStore, error) {
	if appHome == "" {
		return nil, apperror.New(apperror.CodeInvalidArgument, "application settings home is required")
	}
	absolute, err := filepath.Abs(appHome)
	if err != nil {
		return nil, apperror.New(apperror.CodeInvalidArgument, "application settings home is invalid")
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, apperror.New(apperror.CodeUnavailable, "application settings home could not be opened")
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, apperror.New(apperror.CodeUnavailable, "application settings home could not be resolved")
	}
	info, err := os.Lstat(canonical)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "application settings home is unavailable")
	}
	directory, err := os.Open(canonical)
	if err != nil {
		return nil, apperror.New(apperror.CodeUnavailable, "application settings home could not be opened")
	}
	bound, statErr := directory.Stat()
	_ = directory.Close()
	if statErr != nil || !bound.IsDir() || !os.SameFile(info, bound) {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "application settings home changed while binding")
	}
	// File.Stat eagerly captures Windows file identity. Lstat alone may defer
	// reading the identity until SameFile, after the path has been replaced.
	return &FileSandboxEnvironmentSettingsStore{home: canonical, homeInfo: bound}, nil
}

func (store *FileSandboxEnvironmentSettingsStore) Load(ctx context.Context) (SandboxEnvironmentSettingsSnapshot, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	root, err := store.openHome(ctx)
	if err != nil {
		return SandboxEnvironmentSettingsSnapshot{}, err
	}
	defer root.Close()
	return loadSandboxEnvironmentSettings(root)
}

func (store *FileSandboxEnvironmentSettingsStore) Save(ctx context.Context, expectedRevision int64,
	settings SandboxEnvironmentSettings,
) (SandboxEnvironmentSettingsSnapshot, bool, error) {
	if expectedRevision < 1 {
		return SandboxEnvironmentSettingsSnapshot{}, false, apperror.New(apperror.CodeInvalidArgument, "sandbox settings require the current positive revision")
	}
	if err := settings.Validate(); err != nil {
		return SandboxEnvironmentSettingsSnapshot{}, false, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	root, err := store.openHome(ctx)
	if err != nil {
		return SandboxEnvironmentSettingsSnapshot{}, false, err
	}
	defer root.Close()
	lock, err := lockSandboxEnvironmentSettings(root)
	if err != nil {
		return SandboxEnvironmentSettingsSnapshot{}, false, err
	}
	defer lock.Close()
	current, err := loadSandboxEnvironmentSettings(root)
	if err != nil {
		return SandboxEnvironmentSettingsSnapshot{}, false, err
	}
	if current.Revision != expectedRevision {
		if expectedRevision < math.MaxInt64 && current.Revision == expectedRevision+1 && current.Settings == settings {
			return current, true, nil
		}
		return SandboxEnvironmentSettingsSnapshot{}, false, apperror.New(apperror.CodeConflict, "sandbox settings changed; read the current revision before saving")
	}
	if current.Settings == settings {
		return current, false, nil
	}
	if current.Revision == math.MaxInt64 {
		return SandboxEnvironmentSettingsSnapshot{}, false, apperror.New(apperror.CodeResourceExhausted, "sandbox settings revision is exhausted")
	}
	next := SandboxEnvironmentSettingsSnapshot{Revision: current.Revision + 1, Settings: settings}
	raw, err := json.Marshal(sandboxEnvironmentSettingsRecord{Version: SandboxEnvironmentVersion, Revision: next.Revision, Settings: next.Settings})
	if err != nil {
		return SandboxEnvironmentSettingsSnapshot{}, false, apperror.New(apperror.CodeInternal, "sandbox settings could not be encoded")
	}
	if err := persistSandboxEnvironmentSettings(ctx, root, append(raw, '\n')); err != nil {
		return SandboxEnvironmentSettingsSnapshot{}, false, err
	}
	return next, false, nil
}

func (store *FileSandboxEnvironmentSettingsStore) openHome(ctx context.Context) (*os.Root, error) {
	if ctx == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument, "sandbox settings context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Lstat(store.home)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !os.SameFile(store.homeInfo, info) {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "application settings home changed; reopen the application")
	}
	root, err := os.OpenRoot(store.home)
	if err != nil {
		return nil, apperror.New(apperror.CodeUnavailable, "application settings home could not be opened")
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(store.homeInfo, opened) {
		_ = root.Close()
		return nil, apperror.New(apperror.CodeFailedPrecondition, "application settings home changed while opening")
	}
	return root, nil
}

func loadSandboxEnvironmentSettings(root *os.Root) (SandboxEnvironmentSettingsSnapshot, error) {
	info, err := root.Lstat(sandboxEnvironmentSettingsFile)
	if errors.Is(err, os.ErrNotExist) {
		return SandboxEnvironmentSettingsSnapshot{Revision: 1, Settings: DefaultSandboxEnvironmentSettings()}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxSandboxEnvironmentSettingsBytes {
		return SandboxEnvironmentSettingsSnapshot{}, apperror.New(apperror.CodeFailedPrecondition, "saved sandbox settings are unavailable or invalid")
	}
	file, err := root.Open(sandboxEnvironmentSettingsFile)
	if err != nil {
		return SandboxEnvironmentSettingsSnapshot{}, apperror.New(apperror.CodeUnavailable, "saved sandbox settings could not be read")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return SandboxEnvironmentSettingsSnapshot{}, apperror.New(apperror.CodeConflict, "saved sandbox settings changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxSandboxEnvironmentSettingsBytes+1))
	if err != nil || len(raw) > maxSandboxEnvironmentSettingsBytes {
		return SandboxEnvironmentSettingsSnapshot{}, apperror.New(apperror.CodeFailedPrecondition, "saved sandbox settings exceed their bound")
	}
	finished, err := file.Stat()
	if err != nil || !os.SameFile(opened, finished) || opened.Size() != finished.Size() || !opened.ModTime().Equal(finished.ModTime()) {
		return SandboxEnvironmentSettingsSnapshot{}, apperror.New(apperror.CodeConflict, "saved sandbox settings changed while reading")
	}
	fields, valid := sandboxEnvironmentJSONObject(raw, "version", "revision", "settings")
	if !valid {
		return SandboxEnvironmentSettingsSnapshot{}, apperror.New(apperror.CodeFailedPrecondition, "saved sandbox settings are invalid; inspect the application settings file")
	}
	if _, valid := sandboxEnvironmentJSONObject(fields["settings"], "default_backend", "docker_enabled", "docker_image_digest", "sbx_enabled", "sbx_template"); !valid {
		return SandboxEnvironmentSettingsSnapshot{}, apperror.New(apperror.CodeFailedPrecondition, "saved sandbox settings are incomplete or invalid")
	}
	var record sandboxEnvironmentSettingsRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&record) != nil || decoder.Decode(new(any)) != io.EOF || record.Version != SandboxEnvironmentVersion || record.Revision < 1 || record.Settings.Validate() != nil {
		return SandboxEnvironmentSettingsSnapshot{}, apperror.New(apperror.CodeFailedPrecondition, "saved sandbox settings are invalid; inspect the application settings file")
	}
	return SandboxEnvironmentSettingsSnapshot{Revision: record.Revision, Settings: record.Settings}, nil
}

// The on-disk format has no optional fields. Decode objects by token so missing,
// case-variant and duplicate names cannot silently become zero-valued settings.
func sandboxEnvironmentJSONObject(raw []byte, names ...string) (map[string]json.RawMessage, bool) {
	if !utf8.Valid(raw) {
		return nil, false
	}
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		allowed[name] = true
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, false
	}
	fields := make(map[string]json.RawMessage, len(names))
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || !allowed[name] || fields[name] != nil {
			return nil, false
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, false
		}
		fields[name] = value
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || decoder.Decode(new(any)) != io.EOF || len(fields) != len(names) {
		return nil, false
	}
	return fields, true
}

func lockSandboxEnvironmentSettings(root *os.Root) (*os.File, error) {
	name := sandboxEnvironmentSettingsFile + ".lock"
	info, err := root.Lstat(name)
	var file *os.File
	if errors.Is(err, os.ErrNotExist) {
		file, err = root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	} else if err == nil && info.Mode().IsRegular() && info.Size() == 0 {
		file, err = root.OpenFile(name, os.O_RDWR, 0)
	} else {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "sandbox settings publication lock is invalid")
	}
	if err != nil {
		return nil, apperror.New(apperror.CodeConflict, "sandbox settings publication is busy; retry the same request")
	}
	opened, statErr := file.Stat()
	current, pathErr := root.Lstat(name)
	if statErr != nil || pathErr != nil || !opened.Mode().IsRegular() || opened.Size() != 0 || !current.Mode().IsRegular() ||
		!os.SameFile(opened, current) || (info != nil && !os.SameFile(info, opened)) {
		_ = file.Close()
		return nil, apperror.New(apperror.CodeFailedPrecondition, "sandbox settings publication lock changed")
	}
	// Reuse the platform-specific OS lock primitive; this file and lock are
	// independent from managed LSP configuration and release on process exit.
	if err := lockCodeIntelConfigurationFile(file); err != nil {
		_ = file.Close()
		return nil, apperror.New(apperror.CodeConflict, "sandbox settings publication is busy; retry the same request")
	}
	return file, nil
}

func persistSandboxEnvironmentSettings(ctx context.Context, root *os.Root, raw []byte) error {
	return persistDesktopSettingsFile(ctx, root, sandboxEnvironmentSettingsFile, raw)
}

// Both callers supply a fixed Go-owned settings filename. No renderer path
// crosses this atomic publication boundary.
func persistDesktopSettingsFile(ctx context.Context, root *os.Root, name string, raw []byte) error {
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return apperror.New(apperror.CodeUnavailable, "sandbox settings publication could not be prepared")
	}
	temporary := "." + strings.TrimSuffix(name, ".json") + "-" + hex.EncodeToString(entropy[:])
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return apperror.New(apperror.CodeUnavailable, "sandbox settings could not be written")
	}
	defer root.Remove(temporary)
	_, err = file.Write(raw)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = root.Rename(temporary, name)
	}
	if err != nil {
		return apperror.New(apperror.CodeUnavailable, "sandbox settings publication failed; retry the same request to inspect its result")
	}
	if runtime.GOOS != "windows" {
		directory, err := root.Open(".")
		if err != nil {
			return apperror.New(apperror.CodeUnavailable, "sandbox settings directory synchronization failed; retry the same request")
		}
		err = directory.Sync()
		_ = directory.Close()
		if err != nil {
			return apperror.New(apperror.CodeUnavailable, "sandbox settings directory synchronization failed; retry the same request")
		}
	}
	return nil
}
