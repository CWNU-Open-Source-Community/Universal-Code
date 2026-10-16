//go:build desktop

package main

import (
	"reflect"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/desktop"
)

func TestDesktopVerificationPreferencesLoadOnlyForProductAndEveryFixedRestart(t *testing.T) {
	home := t.TempDir()
	store, err := application.NewFileDesktopVerificationSettingsStore(home)
	if err != nil {
		t.Fatal(err)
	}
	settings := application.DesktopVerificationSettings{UIEvidenceEnabled: true, BatchValidationEnabled: true}
	if _, _, err := store.Save(t.Context(), 1, settings); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"--safe-view"}, {"--operator-preview"}, {"--enable-profile-control"}} {
		before, err := parseDesktopOptions(args)
		if err != nil {
			t.Fatal(err)
		}
		after, preferences, err := loadDesktopVerificationPreferences(t.Context(), before, home)
		if err != nil {
			t.Fatal(err)
		}
		if len(args) == 0 {
			before.uiEvidence, before.batchValidation = true, true
			if preferences == nil {
				t.Fatal("product startup lost its native preferences")
			}
		} else if preferences != nil {
			t.Fatalf("explicit launch inherited native preferences: %v", args)
		}
		if before != after {
			t.Fatalf("preferences changed unrelated startup behavior for %v", args)
		}
	}
	for _, profile := range []desktop.DesktopRiskProfile{desktop.DesktopRiskProfileDebug, desktop.DesktopRiskProfileSandbox,
		desktop.DesktopRiskProfileSandboxDebug, desktop.DesktopRiskProfileVerification, desktop.DesktopRiskProfileVerificationDebug} {
		before, err := desktopOptionsForRiskProfile(profile)
		if err != nil {
			t.Fatal(err)
		}
		terminal := before.userTerminal
		after, preferences, err := loadDesktopVerificationPreferences(t.Context(), before, home)
		if err != nil || preferences == nil || !after.uiEvidence || !after.batchValidation || after.userTerminal != terminal {
			t.Fatalf("fixed restart %s lost preferences/terminal: %+v err=%v", profile, after, err)
		}
		before.uiEvidence, before.batchValidation = true, true
		if before != after {
			t.Fatalf("preferences widened fixed restart %s", profile)
		}
	}
	if _, _, err := store.Save(t.Context(), 2, application.DesktopVerificationSettings{}); err != nil {
		t.Fatal(err)
	}
	config, _ := parseDesktopOptions(nil)
	disabled, _, err := loadDesktopVerificationPreferences(t.Context(), config, home)
	if err != nil || disabled.uiEvidence || disabled.batchValidation || disabled.userTerminal {
		t.Fatalf("later product startup ignored disabled preferences: %+v %v", disabled, err)
	}
}

func TestDesktopVerificationNativeDialogNamesExactSavedChoices(t *testing.T) {
	options := desktopVerificationSettingsDialogOptions(application.DesktopVerificationSettings{UIEvidenceEnabled: true})
	if !strings.Contains(options.Message, "浏览器验证：启用") || !strings.Contains(options.Message, "批量验证：关闭") ||
		!strings.Contains(options.Message, "保存后当前应用继续运行") || !strings.Contains(options.Message, "不会恢复旧执行权") ||
		!reflect.DeepEqual(options.Buttons, []string{"确认保存", "取消"}) || options.DefaultButton != "取消" || options.CancelButton != "取消" {
		t.Fatalf("native confirmation lost exact choices or safe defaults: %+v", options)
	}
	ready, err := newRiskRestartReadyChannel()
	if err != nil {
		t.Fatal(err)
	}
	defer ready.close()
	for _, profile := range []desktop.DesktopRiskProfile{desktop.DesktopRiskProfileVerification, desktop.DesktopRiskProfileVerificationDebug} {
		arguments, err := riskRestartHelperArguments(profile, 123, ready.descriptor(), ready.token)
		if err != nil {
			t.Fatal(err)
		}
		parsed, matched, err := parseRiskRestartHelperOptions(arguments)
		if err != nil || !matched || parsed.profile != profile || len(arguments) != 5 {
			t.Fatalf("verification restart escaped exact helper arguments: %+v %v", parsed, err)
		}
		options, err := desktopRiskRestartDialogOptions(profile)
		if err != nil || !strings.Contains(options.Message, "已保存的浏览器验证和批量验证偏好") || options.DefaultButton != "取消" {
			t.Fatalf("fixed restart lacks native confirmation: %+v %v", options, err)
		}
	}
}
