package application

import (
	"context"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/workspace"
)

type MCPWorkspaceRootStore interface {
	RunFileWorkspaceStore
	GetRun(context.Context, string) (domain.Run, error)
	GetMission(context.Context, string) (domain.Mission, error)
}

// The host supplies exactly its selected Workspace view. Ordinary Runs keep
// their registered root; owned Runs use the same inspected Drydock as file
// tools. Neither process cwd nor ambient user directories enter MCP roots.
func NewMCPWorkspaceRootProvider(store MCPWorkspaceRootStore,
	drydock func(context.Context, string) (*RunWorktreeService, error),
) mcp.WorkspaceRootProvider {
	return func(ctx context.Context, runID, workspaceID string) (mcp.WorkspaceRoot, error) {
		if store == nil || !domain.ValidAgentID(workspaceID) {
			return mcp.WorkspaceRoot{}, apperror.New(apperror.CodeFailedPrecondition, "MCP Workspace root is unavailable")
		}
		var selected session.WorkspaceInfo
		if runID == "" {
			var err error
			selected, err = store.GetWorkspaceInfo(ctx, workspaceID)
			if err != nil {
				return mcp.WorkspaceRoot{}, err
			}
			if selected.ID != workspaceID {
				return mcp.WorkspaceRoot{}, apperror.New(apperror.CodeConflict, "MCP Workspace identity changed")
			}
		} else {
			run, err := store.GetRun(ctx, runID)
			if err != nil {
				return mcp.WorkspaceRoot{}, err
			}
			mission, err := store.GetMission(ctx, run.MissionID)
			if err != nil {
				return mcp.WorkspaceRoot{}, err
			}
			if run.ID != runID || mission.WorkspaceID != workspaceID {
				return mcp.WorkspaceRoot{}, apperror.New(apperror.CodeConflict, "MCP Workspace does not belong to this Run")
			}
			var service *RunWorktreeService
			owned, err := runHasOwnedFileWorkspace(ctx, store, runID)
			if err != nil {
				return mcp.WorkspaceRoot{}, err
			}
			if owned && drydock != nil {
				service, err = drydock(ctx, runID)
				if err != nil {
					return mcp.WorkspaceRoot{}, err
				}
			}
			files, err := ResolveRunFileWorkspace(ctx, store, run, mission, service)
			if err != nil {
				return mcp.WorkspaceRoot{}, err
			}
			if files.Drydock != nil {
				if err := requireCurrentRunFileDrydock(ctx, store, runID, *files.Drydock); err != nil {
					return mcp.WorkspaceRoot{}, err
				}
			}
			selected = files.Workspace
		}
		fingerprint, err := workspace.AgentCodeRootFingerprint(selected.RootPath)
		if err != nil {
			return mcp.WorkspaceRoot{}, err
		}
		return mcp.WorkspaceRoot{Path: selected.RootPath, Fingerprint: fingerprint}, nil
	}
}
