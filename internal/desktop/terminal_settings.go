package desktop

import (
	"context"
	"math"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	terminalruntime "cyberagent-workbench/internal/terminal"
)

type DesktopTerminalSettingsConfirmer interface {
	ConfirmTerminalSettings(context.Context, application.DesktopTerminalSettings) (bool, error)
}
type DesktopTerminalSettingsRequest struct {
	ProtocolVersion  string                              `json:"protocol_version"`
	ExpectedRevision int64                               `json:"expected_revision"`
	Settings         application.DesktopTerminalSettings `json:"settings"`
}
type DesktopTerminalSettingsView struct {
	ProtocolVersion string                              `json:"protocol_version"`
	Revision        int64                               `json:"revision"`
	Settings        application.DesktopTerminalSettings `json:"settings"`
	Shells          []terminalruntime.ShellInfo         `json:"shells"`
	CanSave         bool                                `json:"can_save"`
}
type DesktopTerminalSettingsResult struct {
	ProtocolVersion string                      `json:"protocol_version"`
	Status          string                      `json:"status"`
	Replayed        bool                        `json:"replayed"`
	View            DesktopTerminalSettingsView `json:"view"`
}

func (b *DesktopBridge) GetTerminalSettings() (DesktopTerminalSettingsView, error) {
	if b == nil {
		return DesktopTerminalSettingsView{}, apperror.New(apperror.CodeUnavailable, "terminal settings require the desktop app")
	}
	ctx, err := b.lifecycleContext()
	if err != nil {
		return DesktopTerminalSettingsView{}, err
	}
	if b.terminalSettings == nil {
		// Explicit granular CLI startup retains its existing manual grant policy.
		// Only ordinary product startup installs persisted preference controls.
		return b.terminalSettingsView(application.DesktopTerminalSettingsSnapshot{Revision: 1, Settings: application.DefaultDesktopTerminalSettings()}), nil
	}
	snapshot, err := b.terminalSettings.Load(ctx)
	if err != nil {
		return DesktopTerminalSettingsView{}, err
	}
	return b.terminalSettingsView(snapshot), nil
}
func (b *DesktopBridge) terminalSettingsView(snapshot application.DesktopTerminalSettingsSnapshot) DesktopTerminalSettingsView {
	return DesktopTerminalSettingsView{ProtocolVersion: application.DesktopTerminalSettingsVersion, Revision: snapshot.Revision, Settings: snapshot.Settings,
		Shells: terminalruntime.AvailableShells(), CanSave: b.terminalSettingsConfirmer != nil}
}

func (b *DesktopBridge) ConfirmTerminalSettings(request DesktopTerminalSettingsRequest) (DesktopTerminalSettingsResult, error) {
	if b == nil || b.terminalSettings == nil || b.terminalSettingsConfirmer == nil {
		return DesktopTerminalSettingsResult{}, apperror.New(apperror.CodeUnavailable, "terminal settings require native confirmation")
	}
	if request.ProtocolVersion != application.DesktopTerminalSettingsVersion || request.ExpectedRevision < 1 || request.Settings.Validate() != nil {
		return DesktopTerminalSettingsResult{}, apperror.New(apperror.CodeInvalidArgument, "terminal settings request is invalid")
	}
	available := request.Settings.DefaultShell == "auto"
	for _, shell := range terminalruntime.AvailableShells() {
		if shell.ID == request.Settings.DefaultShell && shell.Available {
			available = true
		}
	}
	if !available {
		return DesktopTerminalSettingsResult{}, apperror.New(apperror.CodeFailedPrecondition, "the selected shell is not installed on this computer")
	}
	if !b.riskRestartActive.CompareAndSwap(false, true) {
		return DesktopTerminalSettingsResult{}, apperror.New(apperror.CodeResourceExhausted, "another native settings confirmation is active")
	}
	defer b.riskRestartActive.Store(false)
	ctx, err := b.lifecycleContext()
	if err != nil {
		return DesktopTerminalSettingsResult{}, err
	}
	snapshot, err := b.terminalSettings.Load(ctx)
	if err != nil {
		return DesktopTerminalSettingsResult{}, err
	}
	result := func(status string, replayed bool) DesktopTerminalSettingsResult {
		return DesktopTerminalSettingsResult{ProtocolVersion: application.DesktopTerminalSettingsVersion, Status: status, Replayed: replayed, View: b.terminalSettingsView(snapshot)}
	}
	if snapshot.Revision != request.ExpectedRevision {
		if request.ExpectedRevision < math.MaxInt64 && snapshot.Revision == request.ExpectedRevision+1 && snapshot.Settings == request.Settings {
			return result("saved", true), nil
		}
		return DesktopTerminalSettingsResult{}, apperror.New(apperror.CodeConflict, "terminal settings changed; reload before saving")
	}
	confirmed, err := b.terminalSettingsConfirmer.ConfirmTerminalSettings(ctx, request.Settings)
	if err != nil {
		return DesktopTerminalSettingsResult{}, err
	}
	if !confirmed {
		return result("cancelled", false), nil
	}
	if err = ctx.Err(); err != nil {
		return DesktopTerminalSettingsResult{}, err
	}
	var replayed bool
	if controller, ok := b.debugTerminalAgentInput.(*application.TerminalPreferencesController); ok {
		snapshot, replayed, err = controller.SaveSettings(ctx, request.ExpectedRevision, request.Settings)
	} else {
		snapshot, replayed, err = b.terminalSettings.Save(ctx, request.ExpectedRevision, request.Settings)
	}
	if err != nil {
		return DesktopTerminalSettingsResult{}, err
	}
	return result("saved", replayed), nil
}
