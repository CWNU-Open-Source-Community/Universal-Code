package desktop

import (
	"context"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/workspace"
)

type projectlessDirectorySetter interface {
	SetProjectlessDirectory(context.Context, string) (workspace.ProjectlessDirectory, error)
}

type ProjectlessDirectorySelection struct {
	Status   string                          `json:"status"`
	Settings *workspace.ProjectlessDirectory `json:"settings"`
}

// ChooseProjectlessDirectory accepts no renderer path. It exposes the selected
// path only as operator settings after the native dialog has authorized it.
func (b *DesktopBridge) ChooseProjectlessDirectory() (ProjectlessDirectorySelection, error) {
	if b == nil || !b.bootstrap.ProjectlessWorkspacesEnabled || b.workspaceDirectoryPicker == nil {
		return ProjectlessDirectorySelection{}, apperror.New(apperror.CodeNotFound, "native chat directory selection is unavailable")
	}
	setter, ok := b.workspaceRegistrar.(projectlessDirectorySetter)
	if !ok {
		return ProjectlessDirectorySelection{}, apperror.New(apperror.CodeNotFound, "native chat directory settings are unavailable")
	}
	if !b.dialogActive.CompareAndSwap(false, true) {
		return ProjectlessDirectorySelection{}, apperror.New(apperror.CodeResourceExhausted, "a desktop file dialog is already active")
	}
	defer b.dialogActive.Store(false)
	ctx, err := b.lifecycleContext()
	if err != nil {
		return ProjectlessDirectorySelection{}, err
	}
	selected, err := b.workspaceDirectoryPicker.OpenWorkspaceDirectory(ctx)
	if err != nil {
		return ProjectlessDirectorySelection{}, apperror.New(apperror.CodeUnavailable, "native chat directory dialog failed")
	}
	if err := ctx.Err(); err != nil {
		return ProjectlessDirectorySelection{}, apperror.Normalize(err)
	}
	if selected == "" {
		return ProjectlessDirectorySelection{Status: "cancelled"}, nil
	}
	settings, err := setter.SetProjectlessDirectory(ctx, selected)
	if err != nil {
		return ProjectlessDirectorySelection{}, err
	}
	return ProjectlessDirectorySelection{Status: "selected", Settings: &settings}, nil
}

func (c *ControlPlane) SetProjectlessDirectory(ctx context.Context, selectedPath string) (workspace.ProjectlessDirectory, error) {
	if c == nil || c.stateStore == nil {
		return workspace.ProjectlessDirectory{}, apperror.New(apperror.CodeFailedPrecondition, "chat directory settings are unavailable")
	}
	return (workspace.ProjectlessManager{Store: c.stateStore}).SetDirectory(ctx, selectedPath)
}
