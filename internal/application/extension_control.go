package application

import (
	"context"
	"errors"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/plugins"
	"cyberagent-workbench/internal/session"
)

const ExtensionInventoryProtocolVersion = "extension-inventory.v1"

type ExtensionControlStore interface {
	GetRun(context.Context, string) (domain.Run, error)
	GetMission(context.Context, string) (domain.Mission, error)
	ListMCPClientServers(context.Context, string, string, int) ([]mcp.ServerRecord, error)
	ListMCPClientCalls(context.Context, string, int) ([]mcp.CallAudit, error)
	ListPluginInstallations(context.Context, string, int) ([]plugins.Installation, error)
}

type ExtensionInventory struct {
	ProtocolVersion string                 `json:"protocol_version"`
	RunID           string                 `json:"run_id,omitempty"`
	WorkspaceID     string                 `json:"workspace_id,omitempty"`
	MCPServers      []mcp.ServerRecord     `json:"mcp_servers"`
	MCPCalls        []mcp.CallAudit        `json:"mcp_calls"`
	Plugins         []plugins.Installation `json:"plugins"`
}

type ExtensionControlService struct {
	store       ExtensionControlStore
	mcp         *mcp.Manager
	plugins     *plugins.Service
	credentials *MCPCredentialService
}

func NewExtensionControlService(store ExtensionControlStore, manager *mcp.Manager,
	pluginService *plugins.Service,
) (*ExtensionControlService, error) {
	if store == nil || manager == nil || pluginService == nil {
		return nil, errors.New("extension control dependencies are required")
	}
	return &ExtensionControlService{store: store, mcp: manager, plugins: pluginService}, nil
}

func (s *ExtensionControlService) Inventory(ctx context.Context, runID string) (
	ExtensionInventory, error,
) {
	return s.InventoryForScope(ctx, runID, "")
}

// InventoryForScope also reads Workspace-scoped registrations before a Run
// exists. A supplied Run always fixes its Workspace and owns the call receipts.
func (s *ExtensionControlService) InventoryForScope(ctx context.Context, runID, workspaceID string) (
	ExtensionInventory, error,
) {
	result := ExtensionInventory{ProtocolVersion: ExtensionInventoryProtocolVersion,
		MCPServers: []mcp.ServerRecord{}, MCPCalls: []mcp.CallAudit{},
		Plugins: []plugins.Installation{}}
	var err error
	result.Plugins, err = s.store.ListPluginInstallations(ctx, "", 1_000)
	if err != nil {
		return ExtensionInventory{}, err
	}
	runID = strings.TrimSpace(runID)
	workspaceID = strings.TrimSpace(workspaceID)
	if runID == "" {
		if workspaceID == "" {
			return result, nil
		}
		workspaces, ok := s.store.(interface {
			GetWorkspaceInfo(context.Context, string) (session.WorkspaceInfo, error)
		})
		if !ok {
			return ExtensionInventory{}, apperror.New(apperror.CodeFailedPrecondition,
				"Workspace extension inventory is unavailable")
		}
		if _, err := workspaces.GetWorkspaceInfo(ctx, workspaceID); err != nil {
			return ExtensionInventory{}, err
		}
		result.WorkspaceID = workspaceID
		if err := s.mcp.EnsureComputerUse(ctx, workspaceID); err != nil {
			return ExtensionInventory{}, err
		}
		result.MCPServers, err = s.store.ListMCPClientServers(ctx, "", workspaceID,
			mcp.MaxClientServers)
		if err != nil {
			return ExtensionInventory{}, err
		}
		return result, nil
	}
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return ExtensionInventory{}, err
	}
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return ExtensionInventory{}, err
	}
	if workspaceID != "" && workspaceID != mission.WorkspaceID {
		return ExtensionInventory{}, apperror.New(apperror.CodeConflict,
			"extension Run and Workspace scopes do not match")
	}
	result.RunID, result.WorkspaceID = run.ID, mission.WorkspaceID
	if err := s.mcp.EnsureComputerUse(ctx, mission.WorkspaceID); err != nil {
		return ExtensionInventory{}, err
	}
	result.MCPServers, err = s.store.ListMCPClientServers(ctx, run.ID,
		mission.WorkspaceID, mcp.MaxClientServers)
	if err != nil {
		return ExtensionInventory{}, err
	}
	result.MCPCalls, err = s.store.ListMCPClientCalls(ctx, run.ID, 200)
	if err != nil {
		return ExtensionInventory{}, err
	}
	return result, nil
}

func (s *ExtensionControlService) ReviewMCP(ctx context.Context, serverID string,
	request mcp.ReviewRequest,
) (mcp.ServerRecord, error) {
	return s.mcp.Review(ctx, serverID, request)
}

func (s *ExtensionControlService) RefreshMCP(ctx context.Context,
	serverID string,
) (mcp.ServerRecord, error) {
	return s.mcp.Refresh(ctx, serverID)
}

func (s *ExtensionControlService) ReviewPlugin(ctx context.Context,
	installationID string, request plugins.ReviewRequest,
) (plugins.Installation, error) {
	return s.plugins.Review(ctx, installationID, request)
}
