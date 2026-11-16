package desktop

import (
	"context"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/rootfs"
)

const (
	WorkspaceLauncherListProtocolVersion = "desktop_workspace_launcher_list.v1"
	WorkspaceOpenProtocolVersion         = "desktop_workspace_open.v1"

	maxWorkspaceIdentityBytes = 256
	maxWorkspaceNameBytes     = 128
	maxWorkspaceLaunchers     = 12
	maxLauncherLabelBytes     = 80
)

type WorkspaceLauncherKind string

const (
	WorkspaceLauncherFolder   WorkspaceLauncherKind = "folder"
	WorkspaceLauncherTerminal WorkspaceLauncherKind = "terminal"
	WorkspaceLauncherEditor   WorkspaceLauncherKind = "editor"
)

type WorkspaceOpenStatus string

const (
	WorkspaceOpenCancelled WorkspaceOpenStatus = "cancelled"
	WorkspaceOpenStarted   WorkspaceOpenStatus = "started"
)

// WorkspaceOpenTarget remains inside Go. RootPath must never cross the Wails
// binding into renderer JavaScript.
type WorkspaceOpenTarget struct {
	ID       string
	Name     string
	RootPath string
}

type WorkspaceLauncherDescriptor struct {
	ID    string                `json:"id"`
	Label string                `json:"label"`
	Kind  WorkspaceLauncherKind `json:"kind"`
}

type WorkspaceLauncherList struct {
	ProtocolVersion            string                        `json:"protocol_version"`
	WorkspaceID                string                        `json:"workspace_id"`
	Launchers                  []WorkspaceLauncherDescriptor `json:"launchers"`
	RootPathExposed            bool                          `json:"root_path_exposed"`
	RendererPathInputSupported bool                          `json:"renderer_path_input_supported"`
	ArbitraryArgumentsAccepted bool                          `json:"arbitrary_arguments_accepted"`
	AgentAuthorityGranted      bool                          `json:"agent_authority_granted"`
}

type WorkspaceOpenRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	WorkspaceID     string `json:"workspace_id"`
	LauncherID      string `json:"launcher_id"`
}

type WorkspaceOpenResult struct {
	ProtocolVersion            string              `json:"protocol_version"`
	WorkspaceID                string              `json:"workspace_id"`
	LauncherID                 string              `json:"launcher_id"`
	Status                     WorkspaceOpenStatus `json:"status"`
	OperatorConfirmed          bool                `json:"operator_confirmed"`
	ExternalProcessStarted     bool                `json:"external_process_started"`
	ArbitraryArgumentsAccepted bool                `json:"arbitrary_arguments_accepted"`
	CommandExecuted            bool                `json:"command_executed"`
	RootPathExposed            bool                `json:"root_path_exposed"`
	AgentAuthorityGranted      bool                `json:"agent_authority_granted"`
}

type NativeWorkspaceOpenResult struct {
	Status                 WorkspaceOpenStatus
	OperatorConfirmed      bool
	ExternalProcessStarted bool
}

type WorkspaceResolver interface {
	ResolveWorkspace(context.Context, string) (WorkspaceOpenTarget, error)
}

type NativeWorkspaceLauncher interface {
	List(context.Context) ([]WorkspaceLauncherDescriptor, error)
	Open(context.Context, WorkspaceOpenTarget, string) (NativeWorkspaceOpenResult, error)
}

// WorkspaceLaunchers returns a pathless allowlist for a registered Workspace.
func (b *DesktopBridge) WorkspaceLaunchers(workspaceID string) (WorkspaceLauncherList, error) {
	if b == nil || b.workspaceResolver == nil || b.workspaceLauncher == nil ||
		!b.bootstrap.WorkspaceOpenEnabled {
		return WorkspaceLauncherList{}, apperror.New(apperror.CodeNotFound,
			"desktop workspace opening is disabled")
	}
	if !validWorkspaceIdentity(workspaceID) {
		return WorkspaceLauncherList{}, apperror.New(apperror.CodeInvalidArgument,
			"desktop workspace launcher request is invalid")
	}
	ctx, err := b.lifecycleContext()
	if err != nil {
		return WorkspaceLauncherList{}, err
	}
	if _, err := b.resolveWorkspace(ctx, workspaceID); err != nil {
		return WorkspaceLauncherList{}, err
	}
	launchers, err := b.workspaceLauncher.List(ctx)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return WorkspaceLauncherList{}, apperror.Normalize(ctxErr)
		}
		return WorkspaceLauncherList{}, apperror.New(apperror.CodeUnavailable,
			"native workspace launcher discovery failed")
	}
	if err := validateWorkspaceLaunchers(launchers); err != nil {
		return WorkspaceLauncherList{}, err
	}
	pathless := make([]WorkspaceLauncherDescriptor, len(launchers))
	copy(pathless, launchers)
	return WorkspaceLauncherList{
		ProtocolVersion: WorkspaceLauncherListProtocolVersion,
		WorkspaceID:     workspaceID,
		Launchers:       pathless,
	}, nil
}

// OpenWorkspace accepts no path, command, environment, or argument vector.
// The native implementation must show an operator confirmation before it may
// start one allowlisted external application.
func (b *DesktopBridge) OpenWorkspace(request WorkspaceOpenRequest) (WorkspaceOpenResult, error) {
	if b == nil || b.workspaceResolver == nil || b.workspaceLauncher == nil ||
		!b.bootstrap.WorkspaceOpenEnabled {
		return WorkspaceOpenResult{}, apperror.New(apperror.CodeNotFound,
			"desktop workspace opening is disabled")
	}
	if request.ProtocolVersion != WorkspaceOpenProtocolVersion ||
		!validWorkspaceIdentity(request.WorkspaceID) || !validLauncherIdentity(request.LauncherID) {
		return WorkspaceOpenResult{}, apperror.New(apperror.CodeInvalidArgument,
			"desktop workspace open request is invalid")
	}
	if !b.workspaceOpenActive.CompareAndSwap(false, true) {
		return WorkspaceOpenResult{}, apperror.New(apperror.CodeResourceExhausted,
			"desktop workspace open confirmation is already active")
	}
	defer b.workspaceOpenActive.Store(false)

	ctx, err := b.lifecycleContext()
	if err != nil {
		return WorkspaceOpenResult{}, err
	}
	target, err := b.resolveWorkspace(ctx, request.WorkspaceID)
	if err != nil {
		return WorkspaceOpenResult{}, err
	}
	launchers, err := b.workspaceLauncher.List(ctx)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return WorkspaceOpenResult{}, apperror.Normalize(ctxErr)
		}
		return WorkspaceOpenResult{}, apperror.New(apperror.CodeUnavailable,
			"native workspace launcher discovery failed")
	}
	if err := validateWorkspaceLaunchers(launchers); err != nil {
		return WorkspaceOpenResult{}, err
	}
	found := false
	for _, launcher := range launchers {
		if launcher.ID == request.LauncherID {
			found = true
			break
		}
	}
	if !found {
		return WorkspaceOpenResult{}, apperror.New(apperror.CodeNotFound,
			"desktop workspace launcher was not found")
	}
	native, err := b.workspaceLauncher.Open(ctx, target, request.LauncherID)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return WorkspaceOpenResult{}, apperror.Normalize(ctxErr)
		}
		return WorkspaceOpenResult{}, apperror.New(apperror.CodeUnavailable,
			"native workspace open failed")
	}
	if !validNativeWorkspaceOpenResult(native) {
		return WorkspaceOpenResult{}, apperror.New(apperror.CodeInternal,
			"native workspace open result violated its confirmation contract")
	}
	return WorkspaceOpenResult{
		ProtocolVersion:        WorkspaceOpenProtocolVersion,
		WorkspaceID:            request.WorkspaceID,
		LauncherID:             request.LauncherID,
		Status:                 native.Status,
		OperatorConfirmed:      native.OperatorConfirmed,
		ExternalProcessStarted: native.ExternalProcessStarted,
	}, nil
}

func (b *DesktopBridge) resolveWorkspace(ctx context.Context,
	workspaceID string) (WorkspaceOpenTarget, error) {
	target, err := b.workspaceResolver.ResolveWorkspace(ctx, workspaceID)
	if err != nil {
		code := apperror.CodeOf(apperror.Normalize(err))
		if code == apperror.CodeNotFound {
			return WorkspaceOpenTarget{}, apperror.New(apperror.CodeNotFound,
				"desktop workspace was not found")
		}
		return WorkspaceOpenTarget{}, apperror.New(apperror.CodeUnavailable,
			"desktop workspace resolution failed")
	}
	if !validWorkspaceOpenTarget(target, workspaceID) {
		return WorkspaceOpenTarget{}, apperror.New(apperror.CodeInternal,
			"registered desktop workspace is invalid")
	}
	return target, nil
}

func validateWorkspaceLaunchers(launchers []WorkspaceLauncherDescriptor) error {
	if len(launchers) > maxWorkspaceLaunchers {
		return apperror.New(apperror.CodeInternal,
			"native workspace launcher list exceeded its bound")
	}
	seen := make(map[string]struct{}, len(launchers))
	for _, launcher := range launchers {
		if !validLauncherIdentity(launcher.ID) ||
			!validNormalizedText(launcher.Label, maxLauncherLabelBytes) ||
			(launcher.Kind != WorkspaceLauncherFolder &&
				launcher.Kind != WorkspaceLauncherTerminal &&
				launcher.Kind != WorkspaceLauncherEditor) {
			return apperror.New(apperror.CodeInternal,
				"native workspace launcher descriptor is invalid")
		}
		if _, exists := seen[launcher.ID]; exists {
			return apperror.New(apperror.CodeInternal,
				"native workspace launcher list contains duplicate identifiers")
		}
		seen[launcher.ID] = struct{}{}
	}
	return nil
}

func validWorkspaceOpenTarget(target WorkspaceOpenTarget, expectedID string) bool {
	return target.ID == expectedID && validWorkspaceIdentity(target.ID) &&
		validNormalizedText(target.Name, maxWorkspaceNameBytes) &&
		utf8.ValidString(target.RootPath) && !strings.ContainsRune(target.RootPath, 0) &&
		filepath.IsAbs(target.RootPath) &&
		target.RootPath == filepath.Clean(target.RootPath) && rootfs.CheckExternalDirectory(target.RootPath) == nil
}

func validNativeWorkspaceOpenResult(result NativeWorkspaceOpenResult) bool {
	if result.Status == WorkspaceOpenStarted {
		return result.OperatorConfirmed && result.ExternalProcessStarted
	}
	return result.Status == WorkspaceOpenCancelled && !result.OperatorConfirmed &&
		!result.ExternalProcessStarted
}

func validWorkspaceIdentity(value string) bool {
	if !validNormalizedText(value, maxWorkspaceIdentityBytes) {
		return false
	}
	for _, current := range value {
		if unicode.IsSpace(current) {
			return false
		}
	}
	return true
}

func validLauncherIdentity(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for index, current := range value {
		if (current >= 'a' && current <= 'z') || (current >= '0' && current <= '9') ||
			(current == '-' && index > 0) {
			continue
		}
		return false
	}
	return true
}

func validNormalizedText(value string, maximumBytes int) bool {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) ||
		len([]byte(value)) > maximumBytes || strings.ContainsRune(value, 0) {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) {
			return false
		}
	}
	return true
}
