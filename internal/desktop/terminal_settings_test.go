package desktop

import (
	"context"
	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"testing"
)

type terminalConfirmation struct {
	confirmed bool
	calls     int
	during    func()
}

func (c *terminalConfirmation) ConfirmTerminalSettings(ctx context.Context, settings application.DesktopTerminalSettings) (bool, error) {
	c.calls++
	if c.during != nil {
		c.during()
	}
	return c.confirmed, ctx.Err()
}

func TestTerminalSettingsNativeConfirmationRevisionAndRetry(t *testing.T) {
	store, err := application.NewFileDesktopTerminalSettingsStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bridge := newRiskRestartTestBridge(t, &testRiskProfileRestarter{})
	confirm := &terminalConfirmation{}
	bridge.terminalSettings = store
	bridge.terminalSettingsConfirmer = confirm
	request := DesktopTerminalSettingsRequest{ProtocolVersion: application.DesktopTerminalSettingsVersion, ExpectedRevision: 1, Settings: application.DefaultDesktopTerminalSettings()}
	request.Settings.AgentControl = "full"
	result, err := bridge.ConfirmTerminalSettings(request)
	if err != nil || result.Status != "cancelled" {
		t.Fatal(result, err)
	}
	current, _ := store.Load(t.Context())
	if current.Revision != 1 {
		t.Fatal("cancelled save changed policy")
	}
	confirm.confirmed = true
	result, err = bridge.ConfirmTerminalSettings(request)
	if err != nil || result.Status != "saved" || result.View.Settings.AgentControl != "full" {
		t.Fatal(result, err)
	}
	calls := confirm.calls
	result, err = bridge.ConfirmTerminalSettings(request)
	if err != nil || !result.Replayed || confirm.calls != calls {
		t.Fatal("retry opened confirmation", result, err)
	}
	request.ExpectedRevision = 2
	request.Settings.AgentControl = "disabled"
	confirm.during = func() {
		prefs := application.DefaultDesktopTerminalSettings()
		prefs.ManualTTLSeconds = 60
		if _, _, err := store.Save(t.Context(), 2, prefs); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = bridge.ConfirmTerminalSettings(request); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale native dialog overwrote preferences: %v", err)
	}
	bridge.terminalSettingsConfirmer = nil
	if _, err = bridge.ConfirmTerminalSettings(request); err == nil {
		t.Fatal("saved without native consent")
	}
}

func TestTerminalSettingsGranularStartupKeepsManualGrantPolicy(t *testing.T) {
	bridge := newRiskRestartTestBridge(t, &testRiskProfileRestarter{})
	view, err := bridge.GetTerminalSettings()
	if err != nil || view.CanSave || view.Settings != application.DefaultDesktopTerminalSettings() {
		t.Fatalf("granular startup must expose readonly manual defaults: %+v %v", view, err)
	}
}
