package desktop

import (
	"context"

	"cyberagent-workbench/internal/apperror"
)

const DesktopRiskRestartProtocolVersion = "desktop_risk_restart.v1"

type DesktopRiskProfile string

const (
	DesktopRiskProfileFullAccess        DesktopRiskProfile = "full_access"
	DesktopRiskProfileDebug             DesktopRiskProfile = "debug"
	DesktopRiskProfileSandbox           DesktopRiskProfile = "sandbox_settings"
	DesktopRiskProfileSandboxDebug      DesktopRiskProfile = "sandbox_settings_debug"
	DesktopRiskProfileVerification      DesktopRiskProfile = "verification_settings"
	DesktopRiskProfileVerificationDebug DesktopRiskProfile = "verification_settings_debug"
)

func (p DesktopRiskProfile) Valid() bool {
	return p == DesktopRiskProfileDebug || p == DesktopRiskProfileSandbox || p == DesktopRiskProfileSandboxDebug ||
		p == DesktopRiskProfileVerification || p == DesktopRiskProfileVerificationDebug
}

type DesktopRiskRestartStatus string

const (
	DesktopRiskRestartCancelled  DesktopRiskRestartStatus = "cancelled"
	DesktopRiskRestartRestarting DesktopRiskRestartStatus = "restarting"
)

// DesktopRiskRestartRequest accepts only a fixed product risk profile. In
// particular, the renderer cannot provide an executable, argv, environment,
// working directory, parent PID, or process-launch option.
type DesktopRiskRestartRequest struct {
	ProtocolVersion string             `json:"protocol_version"`
	Profile         DesktopRiskProfile `json:"profile"`
}

type DesktopRiskRestartResult struct {
	ProtocolVersion            string                   `json:"protocol_version"`
	Profile                    DesktopRiskProfile       `json:"profile"`
	Status                     DesktopRiskRestartStatus `json:"status"`
	RestartRequired            bool                     `json:"restart_required"`
	ArbitraryArgumentsAccepted bool                     `json:"arbitrary_arguments_accepted"`
	PersistentRuntimeGrant     bool                     `json:"persistent_runtime_grant"`
}

// DesktopRiskProfileRestarter is implemented by the native Wails shell. It
// owns the final OS dialog, exact self-launch, and quit sequencing. Returning
// false with no error means that the operator cancelled the native dialog.
type DesktopRiskProfileRestarter interface {
	ConfirmAndRestart(context.Context, DesktopRiskProfile) (bool, error)
}

func (b *DesktopBridge) RestartWithRiskProfile(
	request DesktopRiskRestartRequest,
) (DesktopRiskRestartResult, error) {
	if b == nil || !b.bootstrap.RiskProfileRestartEnabled || b.riskProfileRestarter == nil {
		return DesktopRiskRestartResult{}, apperror.New(apperror.CodeNotFound,
			"desktop risk-profile restart is disabled")
	}
	if request.ProtocolVersion != DesktopRiskRestartProtocolVersion || request.Profile != DesktopRiskProfileDebug {
		return DesktopRiskRestartResult{}, apperror.New(apperror.CodeInvalidArgument,
			"desktop risk-profile restart request is invalid")
	}
	if request.Profile == DesktopRiskProfileDebug && b.bootstrap.UserTerminalEnabled {
		return DesktopRiskRestartResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"desktop risk profile is already active")
	}
	if !b.riskRestartActive.CompareAndSwap(false, true) {
		return DesktopRiskRestartResult{}, apperror.New(apperror.CodeResourceExhausted,
			"desktop risk-profile restart is already active")
	}
	ctx, err := b.lifecycleContext()
	if err != nil {
		b.riskRestartActive.Store(false)
		return DesktopRiskRestartResult{}, err
	}
	restarting, err := b.riskProfileRestarter.ConfirmAndRestart(ctx, request.Profile)
	if err != nil {
		b.riskRestartActive.Store(false)
		return DesktopRiskRestartResult{}, apperror.Wrap(apperror.CodeUnavailable,
			"desktop risk-profile restart could not be prepared", err)
	}
	status := DesktopRiskRestartCancelled
	if restarting {
		// Keep the guard set until this process exits. This closes the small
		// interval between returning the result and the scheduled native quit.
		status = DesktopRiskRestartRestarting
	} else {
		b.riskRestartActive.Store(false)
	}
	return DesktopRiskRestartResult{
		ProtocolVersion:            DesktopRiskRestartProtocolVersion,
		Profile:                    request.Profile,
		Status:                     status,
		RestartRequired:            true,
		ArbitraryArgumentsAccepted: false,
		PersistentRuntimeGrant:     false,
	}, nil
}
