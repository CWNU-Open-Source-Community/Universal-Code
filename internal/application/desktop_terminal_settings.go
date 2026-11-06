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

const DesktopTerminalSettingsVersion = "desktop_terminal_settings.v1"

const desktopTerminalSettingsFile = "desktop-terminal.json"

// Native preferences select the shell and consent policy. Run authority and leases remain process-local.
type DesktopTerminalSettings struct {
	AgentControl     string `json:"agent_control"`
	ManualTTLSeconds int    `json:"manual_ttl_seconds"`
	DefaultShell     string `json:"default_shell"`
}

func DefaultDesktopTerminalSettings() DesktopTerminalSettings {
	return DesktopTerminalSettings{AgentControl: "manual", ManualTTLSeconds: 300, DefaultShell: "auto"}
}

func (settings DesktopTerminalSettings) Validate() error {
	if settings.AgentControl != "disabled" && settings.AgentControl != "manual" && settings.AgentControl != "full" {
		return apperror.New(apperror.CodeInvalidArgument, "unknown terminal Agent-control preference")
	}
	if settings.ManualTTLSeconds < 15 || settings.ManualTTLSeconds > 900 {
		return apperror.New(apperror.CodeInvalidArgument, "terminal manual authorization must be between 15 and 900 seconds")
	}
	switch settings.DefaultShell {
	case "auto", "pwsh", "powershell", "cmd", "bash":
	default:
		return apperror.New(apperror.CodeInvalidArgument, "unknown terminal shell preference")
	}
	return nil
}

type DesktopTerminalSettingsSnapshot struct {
	Revision int64
	Settings DesktopTerminalSettings
}

type DesktopTerminalSettingsReader interface {
	Load(context.Context) (DesktopTerminalSettingsSnapshot, error)
}

type DesktopTerminalSettingsStore interface {
	DesktopTerminalSettingsReader
	Save(context.Context, int64, DesktopTerminalSettings) (DesktopTerminalSettingsSnapshot, bool, error)
}

type FileDesktopTerminalSettingsStore struct {
	home *FileSandboxEnvironmentSettingsStore
}

// The native composition root selects appHome. No HTTP controller exposes
// this store; Desktop must obtain native confirmation before calling Save.
func NewFileDesktopTerminalSettingsStore(appHome string) (*FileDesktopTerminalSettingsStore, error) {
	home, err := NewFileSandboxEnvironmentSettingsStore(appHome)
	if err != nil {
		return nil, err
	}
	return &FileDesktopTerminalSettingsStore{home: home}, nil
}

func (store *FileDesktopTerminalSettingsStore) Load(ctx context.Context) (DesktopTerminalSettingsSnapshot, error) {
	store.home.mu.Lock()
	defer store.home.mu.Unlock()
	root, err := store.home.openHome(ctx)
	if err != nil {
		return DesktopTerminalSettingsSnapshot{}, err
	}
	defer root.Close()
	return loadDesktopTerminalSettings(root)
}

func (store *FileDesktopTerminalSettingsStore) Save(ctx context.Context, expectedRevision int64, settings DesktopTerminalSettings) (DesktopTerminalSettingsSnapshot, bool, error) {
	if err := settings.Validate(); err != nil {
		return DesktopTerminalSettingsSnapshot{}, false, err
	}
	if expectedRevision < 1 {
		return DesktopTerminalSettingsSnapshot{}, false, apperror.New(apperror.CodeInvalidArgument, "terminal settings require the current positive revision")
	}
	store.home.mu.Lock()
	defer store.home.mu.Unlock()
	root, err := store.home.openHome(ctx)
	if err != nil {
		return DesktopTerminalSettingsSnapshot{}, false, err
	}
	defer root.Close()
	// Share the existing per-home settings publication lock. Independent
	// native processes cannot race the revision check and atomic replacement.
	lock, err := lockSandboxEnvironmentSettings(root)
	if err != nil {
		return DesktopTerminalSettingsSnapshot{}, false, err
	}
	defer lock.Close()
	current, err := loadDesktopTerminalSettings(root)
	if err != nil {
		return DesktopTerminalSettingsSnapshot{}, false, err
	}
	if current.Revision != expectedRevision {
		if expectedRevision < math.MaxInt64 && current.Revision == expectedRevision+1 && current.Settings == settings {
			return current, true, nil
		}
		return DesktopTerminalSettingsSnapshot{}, false, apperror.New(apperror.CodeConflict, "terminal settings changed; read the current revision before saving")
	}
	if current.Settings == settings {
		return current, false, nil
	}
	if current.Revision == math.MaxInt64 {
		return DesktopTerminalSettingsSnapshot{}, false, apperror.New(apperror.CodeResourceExhausted, "terminal settings revision is exhausted")
	}
	next := DesktopTerminalSettingsSnapshot{Revision: current.Revision + 1, Settings: settings}
	raw, err := json.Marshal(desktopTerminalSettingsRecord{Version: DesktopTerminalSettingsVersion, Revision: next.Revision, Settings: settings})
	if err != nil {
		return DesktopTerminalSettingsSnapshot{}, false, err
	}
	if err := persistDesktopSettingsFile(ctx, root, desktopTerminalSettingsFile, append(raw, '\n')); err != nil {
		return DesktopTerminalSettingsSnapshot{}, false, err
	}
	return next, false, nil
}

type desktopTerminalSettingsRecord struct {
	Version  string                  `json:"version"`
	Revision int64                   `json:"revision"`
	Settings DesktopTerminalSettings `json:"settings"`
}

func loadDesktopTerminalSettings(root *os.Root) (DesktopTerminalSettingsSnapshot, error) {
	info, err := root.Lstat(desktopTerminalSettingsFile)
	if errors.Is(err, os.ErrNotExist) {
		return DesktopTerminalSettingsSnapshot{Revision: 1, Settings: DefaultDesktopTerminalSettings()}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxSandboxEnvironmentSettingsBytes {
		return DesktopTerminalSettingsSnapshot{}, apperror.New(apperror.CodeFailedPrecondition, "saved terminal settings are unavailable or invalid")
	}
	file, err := root.Open(desktopTerminalSettingsFile)
	if err != nil {
		return DesktopTerminalSettingsSnapshot{}, apperror.New(apperror.CodeUnavailable, "saved terminal settings could not be read")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return DesktopTerminalSettingsSnapshot{}, apperror.New(apperror.CodeConflict, "saved terminal settings changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxSandboxEnvironmentSettingsBytes+1))
	if err != nil || len(raw) > maxSandboxEnvironmentSettingsBytes {
		return DesktopTerminalSettingsSnapshot{}, apperror.New(apperror.CodeFailedPrecondition, "saved terminal settings exceed their bound")
	}
	finished, err := file.Stat()
	if err != nil || !os.SameFile(opened, finished) || opened.Size() != finished.Size() || !opened.ModTime().Equal(finished.ModTime()) {
		return DesktopTerminalSettingsSnapshot{}, apperror.New(apperror.CodeConflict, "saved terminal settings changed while reading")
	}
	fields, valid := sandboxEnvironmentJSONObject(raw, "version", "revision", "settings")
	if !valid {
		return DesktopTerminalSettingsSnapshot{}, apperror.New(apperror.CodeFailedPrecondition, "saved terminal settings are incomplete or invalid")
	}
	if _, valid := sandboxEnvironmentJSONObject(fields["settings"], "agent_control", "manual_ttl_seconds", "default_shell"); !valid {
		return DesktopTerminalSettingsSnapshot{}, apperror.New(apperror.CodeFailedPrecondition, "saved terminal preferences are incomplete or invalid")
	}
	var record desktopTerminalSettingsRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&record) != nil || decoder.Decode(new(any)) != io.EOF || record.Version != DesktopTerminalSettingsVersion || record.Revision < 1 || record.Settings.Validate() != nil {
		return DesktopTerminalSettingsSnapshot{}, apperror.New(apperror.CodeFailedPrecondition, "saved terminal settings are invalid")
	}
	return DesktopTerminalSettingsSnapshot{Revision: record.Revision, Settings: record.Settings}, nil
}
