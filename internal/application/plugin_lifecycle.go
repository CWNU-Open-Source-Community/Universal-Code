package application

import (
	"context"
	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/plugins"
	"slices"
)

func (s *ExtensionControlService) PluginHistory(ctx context.Context, id string) (plugins.History, error) {
	return s.plugins.History(ctx, id)
}
func (s *ExtensionControlService) RollbackPlugin(ctx context.Context, currentID, targetID string, request plugins.RollbackRequest) (plugins.Installation, plugins.Installation, error) {
	return s.plugins.Rollback(ctx, currentID, targetID, request)
}
func (s *ExtensionControlService) RevokePluginPublisher(ctx context.Context, id, fingerprint string, generation int64, actor string) (plugins.PublisherTrust, error) {
	return s.plugins.RevokeInstallationPublisher(ctx, id, fingerprint, generation, actor)
}

func (s *ExtensionControlService) TrustPluginPublisher(ctx context.Context, id, fingerprint string, generation int64, actor string) (plugins.PublisherTrust, error) {
	return s.plugins.TrustInstallationPublisher(ctx, id, fingerprint, generation, actor)
}

type HookDeclaration struct {
	InstallationID     string
	PluginID           string
	PackageFingerprint string
	InstallationState  plugins.State
	Active             bool
	Declaration        hooks.Declaration
}
type HookDiagnostics struct {
	RunID               string
	WorkspaceID         string
	Declarations        []HookDeclaration
	OmittedDeclarations int
	Observations        []hooks.AuditRecord
}

func (s *ExtensionControlService) HookDiagnostics(ctx context.Context, runID, workspaceID string) (HookDiagnostics, error) {
	reader, ok := s.store.(interface {
		ListHookAudits(context.Context, string, string, int) ([]hooks.AuditRecord, error)
	})
	if !ok {
		return HookDiagnostics{}, apperror.New(apperror.CodeUnavailable, "observed hook diagnostics are unavailable")
	}
	// Reuse production Run/Workspace binding and existence checks.
	inventory, err := s.InventoryForScope(ctx, runID, workspaceID)
	if err != nil {
		return HookDiagnostics{}, err
	}
	result := HookDiagnostics{RunID: inventory.RunID, WorkspaceID: inventory.WorkspaceID, Declarations: []HookDeclaration{}}
	for _, installation := range inventory.Plugins {
		for _, declaration := range installation.Manifest.Hooks {
			if len(result.Declarations) == 1000 {
				result.OmittedDeclarations++
				continue
			}
			result.Declarations = append(result.Declarations, HookDeclaration{InstallationID: installation.ID, PluginID: installation.PackageID(), PackageFingerprint: installation.PackageFingerprint, InstallationState: installation.State, Active: installation.State == plugins.StateEnabled && slices.Contains(installation.EnabledCapabilities, plugins.CapabilityHooks), Declaration: declaration})
		}
	}
	result.Observations, err = reader.ListHookAudits(ctx, result.RunID, result.WorkspaceID, 200)
	return result, err
}
