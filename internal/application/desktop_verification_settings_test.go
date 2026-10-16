package application

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/apperror"
)

func TestDesktopVerificationPreferencesPersistIndependentChoicesAndExactRetry(t *testing.T) {
	home := t.TempDir()
	store, err := NewFileDesktopVerificationSettingsStore(home)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := store.Load(t.Context())
	if err != nil || initial.Revision != 1 || initial.Settings != (DesktopVerificationSettings{}) {
		t.Fatalf("initial preferences=%+v err=%v", initial, err)
	}
	first := DesktopVerificationSettings{UIEvidenceEnabled: true}
	saved, replayed, err := store.Save(t.Context(), 1, first)
	if err != nil || replayed || saved.Revision != 2 || saved.Settings != first {
		t.Fatalf("independent UI preference=%+v replay=%t err=%v", saved, replayed, err)
	}
	reopened, err := NewFileDesktopVerificationSettingsStore(home)
	if err != nil {
		t.Fatal(err)
	}
	retry, replayed, err := reopened.Save(t.Context(), 1, first)
	if err != nil || !replayed || retry != saved {
		t.Fatalf("lost response retry=%+v replay=%t err=%v", retry, replayed, err)
	}
	second := DesktopVerificationSettings{BatchValidationEnabled: true}
	changed, _, err := reopened.Save(t.Context(), 2, second)
	if err != nil || changed.Revision != 3 || changed.Settings != second {
		t.Fatalf("disable UI and enable batch=%+v err=%v", changed, err)
	}
	if _, _, err := store.Save(t.Context(), 2, first); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale confirmation overwrote newer preferences: %v", err)
	}
	current, err := store.Load(t.Context())
	if err != nil || current != changed {
		t.Fatalf("reopened preference=%+v err=%v", current, err)
	}
	if _, err := os.Stat(filepath.Join(home, sandboxEnvironmentSettingsFile)); !os.IsNotExist(err) {
		t.Fatalf("verification preferences wrote sandbox settings: %v", err)
	}
}

func TestDesktopVerificationPreferencesSharePublicationLockAndHonorCancellation(t *testing.T) {
	store, err := NewFileDesktopVerificationSettingsStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, err := store.home.openHome(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	lock, err := lockSandboxEnvironmentSettings(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Save(t.Context(), 1, DesktopVerificationSettings{UIEvidenceEnabled: true}); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("publication lock was bypassed: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := store.Save(ctx, 1, DesktopVerificationSettings{BatchValidationEnabled: true}); err == nil {
		t.Fatal("cancelled save changed preferences")
	}
	current, err := store.Load(t.Context())
	if err != nil || current.Revision != 1 || current.Settings != (DesktopVerificationSettings{}) {
		t.Fatalf("failed save changed durable preferences: %+v %v", current, err)
	}
}

func TestDesktopVerificationPreferencesRejectIncompleteOrAuthorizingStorage(t *testing.T) {
	for _, raw := range []string{
		`{"version":"desktop_verification_settings.v1","revision":1,"settings":{"ui_evidence_enabled":true}}`,
		`{"version":"desktop_verification_settings.v1","revision":1,"settings":{"ui_evidence_enabled":true,"batch_validation_enabled":null}}`,
		`{"version":"desktop_verification_settings.v1","revision":1,"settings":{"ui_evidence_enabled":true,"ui_evidence_enabled":false,"batch_validation_enabled":false}}`,
		`{"version":"desktop_verification_settings.v1","revision":1,"settings":{"ui_evidence_enabled":true,"batch_validation_enabled":false,"runtime_grant":true}}`,
	} {
		store, err := NewFileDesktopVerificationSettingsStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(store.home.home, desktopVerificationSettingsFile)
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load(t.Context()); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
			t.Fatalf("invalid saved preference loaded: %s err=%v", raw, err)
		}
		if _, _, err := store.Save(t.Context(), 1, DesktopVerificationSettings{}); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
			t.Fatalf("invalid storage was overwritten: %v", err)
		}
		retained, err := os.ReadFile(path)
		if err != nil || string(retained) != raw {
			t.Fatalf("invalid original was lost: %q err=%v", retained, err)
		}
	}
}
