package application

import (
	"context"
	"encoding/json"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspace"
)

type workspaceImageResult struct {
	Version         string                `json:"version"`
	Path            string                `json:"path"`
	FileWorkspaceID string                `json:"file_workspace_id"`
	RootFingerprint string                `json:"root_fingerprint"`
	Image           domain.WorkspaceImage `json:"image"`
}

func (e *AgentCodeToolExecutor) viewWorkspaceImage(ctx context.Context, scope toolgateway.AgentCodeExecutionScope, payload json.RawMessage, policy workspace.AgentCodePathPolicy) (workspaceImageResult, error) {
	var input toolgateway.WorkspaceViewImagePayload
	canonical, err := toolgateway.NormalizeAgentCodePayload(toolgateway.WorkspaceViewImageTool, payload)
	if err != nil {
		return workspaceImageResult{}, err
	}
	if err := json.Unmarshal(canonical, &input); err != nil {
		return workspaceImageResult{}, err
	}
	metadata, data, err := workspace.AgentCodeReadImage(scope.WorkspaceRoot, input.Path, policy)
	if err != nil {
		return workspaceImageResult{}, err
	}
	st, ok := e.store.(interface {
		SaveWorkspaceImage(context.Context, string, string, string, string, []byte) (domain.WorkspaceImage, error)
	})
	if !ok {
		return workspaceImageResult{}, apperror.New(apperror.CodeFailedPrecondition, "workspace image persistence is unavailable")
	}
	key := runmutation.Fingerprint("workspace_view_image.v1", scope.RunID, scope.InvocationID, scope.OperationKey, string(canonical))
	image, err := st.SaveWorkspaceImage(ctx, scope.ControlWorkspaceID(), key, metadata.MIMEType, metadata.Name, data)
	if err != nil {
		return workspaceImageResult{}, err
	}
	return workspaceImageResult{Version: "workspace_image_result.v1", Path: input.Path, FileWorkspaceID: scope.WorkspaceID, RootFingerprint: scope.RootFingerprint, Image: image}, nil
}
