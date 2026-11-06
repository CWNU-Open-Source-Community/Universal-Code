package application

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	terminalruntime "cyberagent-workbench/internal/terminal"
)

func terminalPreferencesFixture(t *testing.T, mode string) (debugTerminalAgentFixture, *TerminalPreferencesController) {
	t.Helper()
	f := newDebugTerminalAgentFixture(t, false)
	store, err := NewFileDesktopTerminalSettingsStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settings := DefaultDesktopTerminalSettings()
	settings.AgentControl = mode
	settings.ManualTTLSeconds = 45
	if _, _, err = store.Save(t.Context(), 1, settings); err != nil {
		t.Fatal(err)
	}
	return f, NewTerminalPreferencesController(f.service, store, f.manager)
}

func TestTerminalPreferencesManualDurationAndDisabledDoNotBlockUserInput(t *testing.T) {
	f, p := terminalPreferencesFixture(t, "manual")
	if _, found, err := p.Active(t.Context(), f.run.ID); err != nil || found {
		t.Fatalf("manual mode auto-granted: %v", err)
	}
	binding, err := p.Grant(t.Context(), GrantDebugTerminalAgentInputRequest{ProtocolVersion: DebugTerminalAgentInputProtocolVersion, RunID: f.run.ID, TerminalSessionID: f.sessionID, RequestedBy: "test_operator", ConfirmFullAccess: true, ConfirmAgentTerminalInput: true, TTL: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if binding.ExpiresAt.Sub(binding.IssuedAt) != 45*time.Second {
		t.Fatalf("ignored configured duration: %+v", binding)
	}
	snapshot, _ := p.settings.Load(t.Context())
	settings := snapshot.Settings
	settings.AgentControl = "disabled"
	if _, _, err = p.SaveSettings(t.Context(), snapshot.Revision, settings); err != nil {
		t.Fatal(err)
	}
	if _, found, err := f.service.Active(t.Context(), f.run.ID); err != nil || found {
		t.Fatalf("save left old lease active: %v", err)
	}
	if _, err = p.Write(t.Context(), WriteDebugTerminalAgentInputRequest{ProtocolVersion: DebugTerminalAgentInputProtocolVersion, BindingID: binding.ID, OperationKey: "disabled-write", Data: []byte("go version\r")}); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("disabled write: %v", err)
	}
	if _, err = p.Read(t.Context(), ReadDebugTerminalAgentOutputRequest{ProtocolVersion: DebugTerminalAgentInputProtocolVersion, BindingID: binding.ID, MaxBytes: 64}); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("disabled read: %v", err)
	}
	if _, err = f.manager.WriteUser(t.Context(), terminalruntime.UserInputRequest{SessionID: f.sessionID, RequestedBy: "test_operator", UserConfirmed: true, Data: []byte("go version\r")}); err != nil {
		t.Fatalf("manual typing blocked: %v", err)
	}
}

func TestTerminalPreferencesFullRenewsAndExplicitRevokeStaysStopped(t *testing.T) {
	f, p := terminalPreferencesFixture(t, "full")
	binding, found, err := p.Active(t.Context(), f.run.ID)
	if err != nil || !found {
		t.Fatalf("full grant: %+v %v", binding, err)
	}
	for i := 0; i < 205; i++ {
		current, ok, err := p.Active(t.Context(), f.run.ID)
		if err != nil || !ok {
			t.Fatalf("active %d: %v", i, err)
		}
		if _, err = p.Write(t.Context(), WriteDebugTerminalAgentInputRequest{ProtocolVersion: DebugTerminalAgentInputProtocolVersion, BindingID: current.ID, OperationKey: fmt.Sprintf("terminal-preferences-write-%04d", i), Data: []byte("go version\r")}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	rotated, _, err := p.Active(t.Context(), f.run.ID)
	if err != nil || rotated.ID == binding.ID {
		t.Fatalf("operation budget did not renew: %v", err)
	}
	// A button rendered just before renewal still stops this same terminal.
	if err = p.Revoke(t.Context(), RevokeDebugTerminalAgentInputRequest{ProtocolVersion: DebugTerminalAgentInputProtocolVersion, BindingID: binding.ID, RequestedBy: "test_operator", OperatorConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	p.Reconcile(t.Context())
	if _, found, err := p.Active(t.Context(), f.run.ID); err != nil || found {
		t.Fatalf("revoked session auto-resumed: %v", err)
	}
	resumed, err := p.Grant(t.Context(), GrantDebugTerminalAgentInputRequest{ProtocolVersion: DebugTerminalAgentInputProtocolVersion, RunID: f.run.ID, TerminalSessionID: f.sessionID, RequestedBy: "test_operator", ConfirmFullAccess: true, ConfirmAgentTerminalInput: true})
	if err != nil {
		t.Fatal(err)
	}
	p.now = func() time.Time { return resumed.ExpiresAt.Add(-20 * time.Second) }
	next, found, err := p.Active(t.Context(), f.run.ID)
	if err != nil || !found || next.ID == resumed.ID {
		t.Fatalf("time renewal failed: %+v %v", next, err)
	}
	p.Shutdown(t.Context())
	if _, _, err = p.Active(t.Context(), f.run.ID); err == nil {
		t.Fatal("shutdown regranted authority")
	}
}

func TestTerminalPreferencesFullCannotRestoreRevokedTaskPermission(t *testing.T) {
	f, p := terminalPreferencesFixture(t, "full")
	binding, found, err := p.Active(t.Context(), f.run.ID)
	if err != nil || !found {
		t.Fatal(err)
	}
	f.service.capabilities.RuntimeAuthority.RevokeRun(f.run.ID)
	p.Reconcile(t.Context())
	if _, found, err = p.Active(t.Context(), f.run.ID); found {
		t.Fatalf("restored cold Full: %v", err)
	}
	if _, err = p.Write(t.Context(), WriteDebugTerminalAgentInputRequest{ProtocolVersion: DebugTerminalAgentInputProtocolVersion, BindingID: binding.ID, OperationKey: "after-revocation", Data: []byte("go version\r")}); err == nil {
		t.Fatal("wrote after permission revocation")
	}
	if _, writes := f.process.snapshot(); writes != 0 {
		t.Fatalf("unexpected writes: %d", writes)
	}
}

func TestDesktopTerminalPreferencesPersistAndRejectStaleOrCorruptSettings(t *testing.T) {
	home := t.TempDir()
	store, err := NewFileDesktopTerminalSettingsStore(home)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := store.Load(t.Context())
	if err != nil || initial.Settings != DefaultDesktopTerminalSettings() {
		t.Fatal(initial, err)
	}
	prefs := initial.Settings
	prefs.AgentControl = "full"
	prefs.DefaultShell = "cmd"
	saved, _, err := store.Save(t.Context(), initial.Revision, prefs)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFileDesktopTerminalSettingsStore(home)
	if err != nil {
		t.Fatal(err)
	}
	retry, replayed, err := reopened.Save(t.Context(), initial.Revision, prefs)
	if err != nil || !replayed || retry != saved {
		t.Fatalf("retry: %+v %v", retry, err)
	}
	prefs.AgentControl = "disabled"
	if _, _, err = reopened.Save(t.Context(), initial.Revision, prefs); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale save: %v", err)
	}
	for _, raw := range []string{
		`{"version":"desktop_terminal_settings.v1","revision":2,"settings":{"agent_control":"full","manual_ttl_seconds":300}}`,
		`{"version":"desktop_terminal_settings.v1","revision":2,"settings":{"agent_control":"full","manual_ttl_seconds":0,"default_shell":"auto"}}`,
		`{"version":"desktop_terminal_settings.v1","revision":2,"settings":{"agent_control":"manual","agent_control":"full","manual_ttl_seconds":300,"default_shell":"auto"}}`,
	} {
		if err = os.WriteFile(filepath.Join(home, desktopTerminalSettingsFile), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = store.Load(t.Context()); err == nil {
			t.Fatal("accepted incomplete/corrupt preferences")
		}
		if _, _, err = store.Save(t.Context(), 2, prefs); err == nil {
			t.Fatal("overwrote corrupt preferences")
		}
	}
}
