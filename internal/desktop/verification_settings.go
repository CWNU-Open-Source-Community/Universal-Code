package desktop

import (
	"context"
	"math"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
)

const DesktopVerificationSettingsProtocolVersion = application.DesktopVerificationSettingsVersion

// The native shell shows the exact desired preferences. A renderer boolean
// is never accepted as evidence that this confirmation happened.
type DesktopVerificationSettingsConfirmer interface {
	ConfirmVerificationSettings(context.Context, application.DesktopVerificationSettings) (bool, error)
}

type DesktopVerificationSettingsRequest struct {
	ProtocolVersion  string                                  `json:"protocol_version"`
	ExpectedRevision int64                                   `json:"expected_revision"`
	Settings         application.DesktopVerificationSettings `json:"settings"`
}

type DesktopVerificationSettingsView struct {
	ProtocolVersion string                                   `json:"protocol_version"`
	Revision        int64                                    `json:"revision"`
	Settings        application.DesktopVerificationSettings  `json:"settings"`
	ActiveSettings  application.DesktopVerificationSettings  `json:"active_settings"`
	RestartRequired bool                                     `json:"restart_required"`
	CanSave         bool                                     `json:"can_save"`
	CanRestart      bool                                     `json:"can_restart"`
	UIEvidence      application.VerificationFeatureReadiness `json:"ui_evidence"`
	BatchValidation application.VerificationFeatureReadiness `json:"batch_validation"`
	CapabilityGrant bool                                     `json:"capability_grant"`
}

type DesktopVerificationSettingsResult struct {
	ProtocolVersion string                          `json:"protocol_version"`
	Status          string                          `json:"status"`
	Replayed        bool                            `json:"replayed"`
	View            DesktopVerificationSettingsView `json:"view"`
}

type DesktopVerificationRestartRequest struct {
	ProtocolVersion string `json:"protocol_version"`
}

type DesktopVerificationRestartResult struct {
	ProtocolVersion            string                   `json:"protocol_version"`
	Status                     DesktopRiskRestartStatus `json:"status"`
	RestartRequired            bool                     `json:"restart_required"`
	ArbitraryArgumentsAccepted bool                     `json:"arbitrary_arguments_accepted"`
	PersistentRuntimeGrant     bool                     `json:"persistent_runtime_grant"`
}

func (b *DesktopBridge) GetVerificationSettings() (DesktopVerificationSettingsView, error) {
	if err := b.verificationSettingsAvailable(); err != nil {
		return DesktopVerificationSettingsView{}, err
	}
	ctx, err := b.lifecycleContext()
	if err != nil {
		return DesktopVerificationSettingsView{}, err
	}
	snapshot, err := b.verificationSettings.Load(ctx)
	if err != nil {
		return DesktopVerificationSettingsView{}, err
	}
	return b.verificationSettingsView(snapshot), nil
}

func (b *DesktopBridge) ConfirmVerificationSettings(request DesktopVerificationSettingsRequest) (DesktopVerificationSettingsResult, error) {
	if err := b.verificationSettingsAvailable(); err != nil {
		return DesktopVerificationSettingsResult{}, err
	}
	if request.ProtocolVersion != DesktopVerificationSettingsProtocolVersion || request.ExpectedRevision < 1 {
		return DesktopVerificationSettingsResult{}, apperror.New(apperror.CodeInvalidArgument, "desktop verification settings request is invalid")
	}
	if !b.riskRestartActive.CompareAndSwap(false, true) {
		return DesktopVerificationSettingsResult{}, apperror.New(apperror.CodeResourceExhausted, "desktop settings confirmation or restart is already active")
	}
	defer b.riskRestartActive.Store(false)
	ctx, err := b.lifecycleContext()
	if err != nil {
		return DesktopVerificationSettingsResult{}, err
	}
	current, err := b.verificationSettings.Load(ctx)
	if err != nil {
		return DesktopVerificationSettingsResult{}, err
	}
	if current.Revision != request.ExpectedRevision {
		// An exact lost-response retry only observes the preference previously
		// saved after native confirmation. It performs no new write or restart.
		if request.ExpectedRevision < math.MaxInt64 && current.Revision == request.ExpectedRevision+1 && current.Settings == request.Settings {
			return b.verificationSettingsResult("saved", current, true), nil
		}
		return DesktopVerificationSettingsResult{}, apperror.New(apperror.CodeConflict, "verification settings changed; read the current revision before saving")
	}
	confirmed, err := b.verificationConfirmer.ConfirmVerificationSettings(ctx, request.Settings)
	if err != nil {
		return DesktopVerificationSettingsResult{}, apperror.Wrap(apperror.CodeUnavailable, "native verification settings confirmation failed", err)
	}
	if !confirmed {
		current, err = b.verificationSettings.Load(ctx)
		if err != nil {
			return DesktopVerificationSettingsResult{}, err
		}
		return b.verificationSettingsResult("cancelled", current, false), nil
	}
	if err := ctx.Err(); err != nil {
		return DesktopVerificationSettingsResult{}, err
	}
	// Recheck the revision after the native dialog. Another native process
	// must not be overwritten with the selection from an older confirmation.
	saved, replayed, err := b.verificationSettings.Save(ctx, request.ExpectedRevision, request.Settings)
	if err != nil {
		return DesktopVerificationSettingsResult{}, err
	}
	return b.verificationSettingsResult("saved", saved, replayed), nil
}

func (b *DesktopBridge) RestartWithVerificationSettings(request DesktopVerificationRestartRequest) (DesktopVerificationRestartResult, error) {
	if err := b.verificationSettingsAvailable(); err != nil {
		return DesktopVerificationRestartResult{}, err
	}
	if request.ProtocolVersion != DesktopVerificationSettingsProtocolVersion {
		return DesktopVerificationRestartResult{}, apperror.New(apperror.CodeInvalidArgument, "desktop verification restart request is invalid")
	}
	if !b.riskRestartActive.CompareAndSwap(false, true) {
		return DesktopVerificationRestartResult{}, apperror.New(apperror.CodeResourceExhausted, "desktop settings confirmation or restart is already active")
	}
	restarting := false
	defer func() {
		if !restarting {
			b.riskRestartActive.Store(false)
		}
	}()
	ctx, err := b.lifecycleContext()
	if err != nil {
		return DesktopVerificationRestartResult{}, err
	}
	snapshot, err := b.verificationSettings.Load(ctx)
	if err != nil {
		return DesktopVerificationRestartResult{}, err
	}
	if !b.verificationSettingsView(snapshot).RestartRequired {
		return DesktopVerificationRestartResult{}, apperror.New(apperror.CodeFailedPrecondition, "saved verification settings are already active")
	}
	profile := DesktopRiskProfileVerification
	if b.bootstrap.UserTerminalEnabled {
		profile = DesktopRiskProfileVerificationDebug
	}
	restarting, err = b.riskProfileRestarter.ConfirmAndRestart(ctx, profile)
	if err != nil {
		restarting = false
		return DesktopVerificationRestartResult{}, apperror.Wrap(apperror.CodeUnavailable, "desktop verification restart could not be prepared", err)
	}
	status := DesktopRiskRestartCancelled
	if restarting {
		status = DesktopRiskRestartRestarting
	}
	return DesktopVerificationRestartResult{ProtocolVersion: DesktopVerificationSettingsProtocolVersion, Status: status, RestartRequired: true}, nil
}

func (b *DesktopBridge) verificationSettingsAvailable() error {
	if b == nil || !b.bootstrap.RiskProfileRestartEnabled || b.verificationSettings == nil || b.verificationConfirmer == nil || b.riskProfileRestarter == nil {
		return apperror.New(apperror.CodeNotFound, "desktop native verification settings are unavailable")
	}
	return nil
}

func (b *DesktopBridge) verificationSettingsView(snapshot application.DesktopVerificationSettingsSnapshot) DesktopVerificationSettingsView {
	active := application.DesktopVerificationSettings{UIEvidenceEnabled: b.bootstrap.UIEvidenceControlEnabled, BatchValidationEnabled: b.bootstrap.BatchDeliveryHostValidationEnabled}
	restart := active != snapshot.Settings
	return DesktopVerificationSettingsView{
		ProtocolVersion: DesktopVerificationSettingsProtocolVersion, Revision: snapshot.Revision,
		Settings: snapshot.Settings, ActiveSettings: active, RestartRequired: restart,
		CanSave: true, CanRestart: restart,
		UIEvidence:      application.BuildVerificationFeatureReadiness(active.UIEvidenceEnabled, snapshot.Settings.UIEvidenceEnabled, true),
		BatchValidation: application.BuildVerificationFeatureReadiness(active.BatchValidationEnabled, snapshot.Settings.BatchValidationEnabled, true),
	}
}

func (b *DesktopBridge) verificationSettingsResult(status string, snapshot application.DesktopVerificationSettingsSnapshot, replayed bool) DesktopVerificationSettingsResult {
	return DesktopVerificationSettingsResult{ProtocolVersion: DesktopVerificationSettingsProtocolVersion, Status: status, Replayed: replayed, View: b.verificationSettingsView(snapshot)}
}
