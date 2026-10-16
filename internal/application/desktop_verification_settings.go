package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"

	"cyberagent-workbench/internal/apperror"
)

const DesktopVerificationSettingsVersion = "desktop_verification_settings.v1"

const desktopVerificationSettingsFile = "desktop-verification.json"

// These preferences install Desktop services on a later product startup.
// They contain no Run identity, permission, lease or runtime authorization.
type DesktopVerificationSettings struct {
	UIEvidenceEnabled      bool `json:"ui_evidence_enabled"`
	BatchValidationEnabled bool `json:"batch_validation_enabled"`
}

type DesktopVerificationSettingsSnapshot struct {
	Revision int64
	Settings DesktopVerificationSettings
}

type DesktopVerificationSettingsReader interface {
	Load(context.Context) (DesktopVerificationSettingsSnapshot, error)
}

type DesktopVerificationSettingsStore interface {
	DesktopVerificationSettingsReader
	Save(context.Context, int64, DesktopVerificationSettings) (DesktopVerificationSettingsSnapshot, bool, error)
}

// This is startup guidance, not an execution decision for a Run. HTTP may
// project these facts but cannot invoke the native preference confirmation.
type VerificationFeatureReadiness struct {
	Installed   bool     `json:"installed"`
	BlockedBy   []string `json:"blocked_by"`
	Remediation []string `json:"remediation"`
}

func BuildVerificationFeatureReadiness(installed, savedEnabled, nativeControl bool) VerificationFeatureReadiness {
	value := VerificationFeatureReadiness{Installed: installed, BlockedBy: []string{}, Remediation: []string{}}
	if installed {
		if !savedEnabled && nativeControl {
			value.Remediation = append(value.Remediation, "restart_application")
		}
		return value
	}
	value.BlockedBy = append(value.BlockedBy, "startup_gate_closed")
	if !nativeControl {
		value.BlockedBy = append(value.BlockedBy, "native_control_unavailable")
		value.Remediation = append(value.Remediation, "open_desktop_settings")
	} else if savedEnabled {
		value.Remediation = append(value.Remediation, "restart_application")
	} else {
		value.Remediation = append(value.Remediation, "enable_and_restart")
	}
	return value
}

type FileDesktopVerificationSettingsStore struct {
	home *FileSandboxEnvironmentSettingsStore
}

// The native composition root selects appHome. No HTTP controller exposes
// this store; Desktop must obtain native confirmation before calling Save.
func NewFileDesktopVerificationSettingsStore(appHome string) (*FileDesktopVerificationSettingsStore, error) {
	home, err := NewFileSandboxEnvironmentSettingsStore(appHome)
	if err != nil {
		return nil, err
	}
	return &FileDesktopVerificationSettingsStore{home: home}, nil
}

func (store *FileDesktopVerificationSettingsStore) Load(ctx context.Context) (DesktopVerificationSettingsSnapshot, error) {
	store.home.mu.Lock()
	defer store.home.mu.Unlock()
	root, err := store.home.openHome(ctx)
	if err != nil {
		return DesktopVerificationSettingsSnapshot{}, err
	}
	defer root.Close()
	return loadDesktopVerificationSettings(root)
}

func (store *FileDesktopVerificationSettingsStore) Save(ctx context.Context, expectedRevision int64, settings DesktopVerificationSettings) (DesktopVerificationSettingsSnapshot, bool, error) {
	if expectedRevision < 1 {
		return DesktopVerificationSettingsSnapshot{}, false, apperror.New(apperror.CodeInvalidArgument, "verification settings require the current positive revision")
	}
	store.home.mu.Lock()
	defer store.home.mu.Unlock()
	root, err := store.home.openHome(ctx)
	if err != nil {
		return DesktopVerificationSettingsSnapshot{}, false, err
	}
	defer root.Close()
	// Share the existing per-home settings publication lock. Independent
	// native processes cannot race the revision check and atomic replacement.
	lock, err := lockSandboxEnvironmentSettings(root)
	if err != nil {
		return DesktopVerificationSettingsSnapshot{}, false, err
	}
	defer lock.Close()
	current, err := loadDesktopVerificationSettings(root)
	if err != nil {
		return DesktopVerificationSettingsSnapshot{}, false, err
	}
	if current.Revision != expectedRevision {
		if expectedRevision < math.MaxInt64 && current.Revision == expectedRevision+1 && current.Settings == settings {
			return current, true, nil
		}
		return DesktopVerificationSettingsSnapshot{}, false, apperror.New(apperror.CodeConflict, "verification settings changed; read the current revision before saving")
	}
	if current.Settings == settings {
		return current, false, nil
	}
	if current.Revision == math.MaxInt64 {
		return DesktopVerificationSettingsSnapshot{}, false, apperror.New(apperror.CodeResourceExhausted, "verification settings revision is exhausted")
	}
	next := DesktopVerificationSettingsSnapshot{Revision: current.Revision + 1, Settings: settings}
	raw, err := json.Marshal(desktopVerificationSettingsRecord{Version: DesktopVerificationSettingsVersion, Revision: next.Revision, Settings: settings})
	if err != nil {
		return DesktopVerificationSettingsSnapshot{}, false, err
	}
	if err := persistDesktopSettingsFile(ctx, root, desktopVerificationSettingsFile, append(raw, '\n')); err != nil {
		return DesktopVerificationSettingsSnapshot{}, false, err
	}
	return next, false, nil
}

type desktopVerificationSettingsRecord struct {
	Version  string                      `json:"version"`
	Revision int64                       `json:"revision"`
	Settings DesktopVerificationSettings `json:"settings"`
}

func loadDesktopVerificationSettings(root *os.Root) (DesktopVerificationSettingsSnapshot, error) {
	info, err := root.Lstat(desktopVerificationSettingsFile)
	if errors.Is(err, os.ErrNotExist) {
		return DesktopVerificationSettingsSnapshot{Revision: 1}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxSandboxEnvironmentSettingsBytes {
		return DesktopVerificationSettingsSnapshot{}, apperror.New(apperror.CodeFailedPrecondition, "saved verification settings are unavailable or invalid")
	}
	file, err := root.Open(desktopVerificationSettingsFile)
	if err != nil {
		return DesktopVerificationSettingsSnapshot{}, apperror.New(apperror.CodeUnavailable, "saved verification settings could not be read")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return DesktopVerificationSettingsSnapshot{}, apperror.New(apperror.CodeConflict, "saved verification settings changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxSandboxEnvironmentSettingsBytes+1))
	if err != nil || len(raw) > maxSandboxEnvironmentSettingsBytes {
		return DesktopVerificationSettingsSnapshot{}, apperror.New(apperror.CodeFailedPrecondition, "saved verification settings exceed their bound")
	}
	finished, err := file.Stat()
	if err != nil || !os.SameFile(opened, finished) || opened.Size() != finished.Size() || !opened.ModTime().Equal(finished.ModTime()) {
		return DesktopVerificationSettingsSnapshot{}, apperror.New(apperror.CodeConflict, "saved verification settings changed while reading")
	}
	fields, valid := sandboxEnvironmentJSONObject(raw, "version", "revision", "settings")
	if !valid {
		return DesktopVerificationSettingsSnapshot{}, apperror.New(apperror.CodeFailedPrecondition, "saved verification settings are incomplete or invalid")
	}
	if _, valid := sandboxEnvironmentJSONObject(fields["settings"], "ui_evidence_enabled", "batch_validation_enabled"); !valid {
		return DesktopVerificationSettingsSnapshot{}, apperror.New(apperror.CodeFailedPrecondition, "saved verification preferences are incomplete or invalid")
	}
	var record desktopVerificationSettingsRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&record) != nil || decoder.Decode(new(any)) != io.EOF || record.Version != DesktopVerificationSettingsVersion || record.Revision < 1 {
		return DesktopVerificationSettingsSnapshot{}, apperror.New(apperror.CodeFailedPrecondition, "saved verification settings are invalid")
	}
	return DesktopVerificationSettingsSnapshot{Revision: record.Revision, Settings: record.Settings}, nil
}
