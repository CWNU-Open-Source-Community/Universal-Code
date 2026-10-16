package desktop

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
)

type verificationSettingsConfirmation struct {
	confirmed bool
	err       error
	calls     int
	settings  application.DesktopVerificationSettings
	during    func()
}

func (c *verificationSettingsConfirmation) ConfirmVerificationSettings(ctx context.Context, settings application.DesktopVerificationSettings) (bool, error) {
	c.calls++
	c.settings = settings
	if c.during != nil {
		c.during()
	}
	return c.confirmed, errors.Join(c.err, ctx.Err())
}

func verificationSettingsBridge(t *testing.T) (*DesktopBridge, *application.FileDesktopVerificationSettingsStore, *verificationSettingsConfirmation, *testRiskProfileRestarter) {
	t.Helper()
	store, err := application.NewFileDesktopVerificationSettingsStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	confirmer, restarter := &verificationSettingsConfirmation{}, &testRiskProfileRestarter{}
	bridge := newRiskRestartTestBridge(t, restarter)
	bridge.verificationSettings, bridge.verificationConfirmer = store, confirmer
	return bridge, store, confirmer, restarter
}

func verificationSettingsRequest() DesktopVerificationSettingsRequest {
	return DesktopVerificationSettingsRequest{ProtocolVersion: DesktopVerificationSettingsProtocolVersion, ExpectedRevision: 1,
		Settings: application.DesktopVerificationSettings{UIEvidenceEnabled: true}}
}

func TestVerificationSettingsRequireNativeConfirmationAndLeaveActiveServicesUnchanged(t *testing.T) {
	b, store, confirmer, restarter := verificationSettingsBridge(t)
	request := verificationSettingsRequest()
	for _, failure := range []error{nil, errors.New("native dialog failed")} {
		confirmer.err = failure
		result, err := b.ConfirmVerificationSettings(request)
		if failure == nil && (err != nil || result.Status != "cancelled") || failure != nil && apperror.CodeOf(err) != apperror.CodeUnavailable {
			t.Fatalf("native cancellation/failure=%+v err=%v", result, err)
		}
		current, loadErr := store.Load(t.Context())
		if loadErr != nil || current.Revision != 1 || current.Settings != (application.DesktopVerificationSettings{}) || restarter.calls != 0 {
			t.Fatalf("unconfirmed settings changed startup: %+v restart=%d err=%v", current, restarter.calls, loadErr)
		}
	}
	confirmer.confirmed, confirmer.err = true, nil
	result, err := b.ConfirmVerificationSettings(request)
	if err != nil || result.Status != "saved" || result.View.Settings != request.Settings || result.View.Revision != 2 ||
		!result.View.RestartRequired || !result.View.CanRestart || result.View.UIEvidence.Installed ||
		result.View.CapabilityGrant || result.View.ActiveSettings != (application.DesktopVerificationSettings{}) || restarter.calls != 0 || confirmer.settings != request.Settings {
		t.Fatalf("saved preferences changed active authority or restarted: %+v err=%v", result, err)
	}
	if !reflect.DeepEqual(result.View.UIEvidence.Remediation, []string{"restart_application"}) ||
		!reflect.DeepEqual(result.View.BatchValidation.Remediation, []string{"enable_and_restart"}) {
		t.Fatalf("Go startup guidance conflated the two independent preferences: %+v", result.View)
	}
	confirmationCalls := confirmer.calls
	retry, err := b.ConfirmVerificationSettings(request)
	if err != nil || !retry.Replayed || retry.View.Revision != 2 || confirmer.calls != confirmationCalls || restarter.calls != 0 {
		t.Fatalf("exact retry changed previously confirmed settings: %+v calls=%d err=%v", retry, confirmer.calls, err)
	}
}

type failingVerificationSettingsStore struct {
	application.DesktopVerificationSettingsStore
	err error
}

func (s *failingVerificationSettingsStore) Save(ctx context.Context, revision int64, settings application.DesktopVerificationSettings) (application.DesktopVerificationSettingsSnapshot, bool, error) {
	if s.err != nil {
		return application.DesktopVerificationSettingsSnapshot{}, false, s.err
	}
	return s.DesktopVerificationSettingsStore.Save(ctx, revision, settings)
}

func TestVerificationSettingsSaveFailureKeepsOriginalAndCanRetry(t *testing.T) {
	b, store, confirmer, restarter := verificationSettingsBridge(t)
	confirmer.confirmed = true
	failing := &failingVerificationSettingsStore{DesktopVerificationSettingsStore: store, err: errors.New("publication unavailable")}
	b.verificationSettings = failing
	if _, err := b.ConfirmVerificationSettings(verificationSettingsRequest()); err == nil {
		t.Fatal("failed save reported success")
	}
	current, err := store.Load(t.Context())
	if err != nil || current.Revision != 1 || restarter.calls != 0 || b.riskRestartActive.Load() {
		t.Fatalf("failed save changed settings or trapped retry: %+v err=%v", current, err)
	}
	failing.err = nil
	result, err := b.ConfirmVerificationSettings(verificationSettingsRequest())
	if err != nil || result.Status != "saved" || result.View.Revision != 2 {
		t.Fatalf("save retry failed: %+v %v", result, err)
	}
}

func TestVerificationSettingsRecheckRevisionAfterNativeDialogAndSerializeRestarts(t *testing.T) {
	b, store, confirmer, restarter := verificationSettingsBridge(t)
	entered, release := make(chan struct{}), make(chan struct{})
	confirmer.confirmed = true
	confirmer.during = func() { close(entered); <-release }
	result := make(chan error, 1)
	go func() { _, err := b.ConfirmVerificationSettings(verificationSettingsRequest()); result <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("native confirmation was not reached")
	}
	if _, err := b.RestartWithVerificationSettings(DesktopVerificationRestartRequest{ProtocolVersion: DesktopVerificationSettingsProtocolVersion}); apperror.CodeOf(err) != apperror.CodeResourceExhausted || restarter.calls != 0 {
		t.Fatalf("restart raced a pending native save: %v", err)
	}
	if _, err := b.ConfirmVerificationSettings(verificationSettingsRequest()); apperror.CodeOf(err) != apperror.CodeResourceExhausted {
		t.Fatalf("second confirmation was concurrent: %v", err)
	}
	newer := application.DesktopVerificationSettings{BatchValidationEnabled: true}
	if _, _, err := store.Save(t.Context(), 1, newer); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-result; apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale native selection overwrote newer preferences: %v", err)
	}
	current, err := store.Load(t.Context())
	if err != nil || current.Settings != newer || current.Revision != 2 {
		t.Fatalf("concurrent preference was lost: %+v err=%v", current, err)
	}
}

func TestVerificationSettingsRestartFailureRetainsPreferencesAndTerminalChoice(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		b, store, confirmer, restarter := verificationSettingsBridge(t)
		b.bootstrap.UserTerminalEnabled = terminal
		confirmer.confirmed = true
		if _, err := b.ConfirmVerificationSettings(verificationSettingsRequest()); err != nil {
			t.Fatal(err)
		}
		request := DesktopVerificationRestartRequest{ProtocolVersion: DesktopVerificationSettingsProtocolVersion}
		restarter.err = errors.New("self-exe ready handshake failed")
		if _, err := b.RestartWithVerificationSettings(request); apperror.CodeOf(err) != apperror.CodeUnavailable || b.riskRestartActive.Load() {
			t.Fatalf("failed restart trapped retry: %v", err)
		}
		view, err := b.GetVerificationSettings()
		if err != nil || !view.Settings.UIEvidenceEnabled || !view.RestartRequired || !view.CanRestart || view.UIEvidence.Installed || view.CapabilityGrant {
			t.Fatalf("restart failure lost preferences or changed current services: %+v err=%v", view, err)
		}
		restarter.err = nil
		cancelled, err := b.RestartWithVerificationSettings(request)
		if err != nil || cancelled.Status != DesktopRiskRestartCancelled || b.riskRestartActive.Load() {
			t.Fatalf("cancelled restart did not preserve process: %+v err=%v", cancelled, err)
		}
		restarter.restarting = true
		started, err := b.RestartWithVerificationSettings(request)
		want := DesktopRiskProfileVerification
		if terminal {
			want = DesktopRiskProfileVerificationDebug
		}
		if err != nil || started.Status != DesktopRiskRestartRestarting || restarter.profile != want || started.PersistentRuntimeGrant || started.ArbitraryArgumentsAccepted {
			t.Fatalf("restart widened authority or lost terminal choice: %+v profile=%s err=%v", started, restarter.profile, err)
		}
		if _, err := b.RestartWithSandboxSettings(DesktopSandboxRestartRequest{ProtocolVersion: DesktopSandboxRestartProtocolVersion}); apperror.CodeOf(err) != apperror.CodeResourceExhausted {
			t.Fatalf("sandbox restart raced accepted verification restart: %v", err)
		}
		current, _ := store.Load(t.Context())
		if current.Revision != 2 || !current.Settings.UIEvidenceEnabled {
			t.Fatal("restart changed saved preferences")
		}
	}
}
