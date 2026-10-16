//go:build desktop

package main

import (
	"context"

	"cyberagent-workbench/internal/application"
)

// Product startup and each fixed native restart read the same preferences.
// Explicit granular flags keep their original startup gates and never inherit
// persisted preferences or a native settings-write capability.
func loadDesktopVerificationPreferences(ctx context.Context, config desktopOptions, home string) (desktopOptions, application.DesktopVerificationSettingsStore, error) {
	if !config.riskProfileRestart {
		return config, nil, nil
	}
	store, err := application.NewFileDesktopVerificationSettingsStore(home)
	if err != nil {
		return config, nil, err
	}
	snapshot, err := store.Load(ctx)
	if err != nil {
		return config, nil, err
	}
	config.uiEvidence = snapshot.Settings.UIEvidenceEnabled
	config.batchValidation = snapshot.Settings.BatchValidationEnabled
	return config, store, nil
}
