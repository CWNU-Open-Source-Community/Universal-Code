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
	if scope.RequestedBy != "run_supervisor" || scope.SupervisorToolCallID == "" {
		return workspaceImageResult{}, apperror.New(apperror.CodePolicyDenied, "workspace image requires its exact root Supervisor call")
	}
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
	key := workspaceImageSaveKey(scope.RunID, scope.SupervisorToolCallID, scope.OperationKey, canonical)
	image, err := st.SaveWorkspaceImage(ctx, scope.ControlWorkspaceID(), key, metadata.MIMEType, metadata.Name, data)
	if err != nil {
		return workspaceImageResult{}, err
	}
	return workspaceImageResult{Version: "workspace_image_result.v1", Path: input.Path, FileWorkspaceID: scope.WorkspaceID, RootFingerprint: scope.RootFingerprint, Image: image}, nil
}

func supervisorWorkspaceImageOperationKey(call domain.SupervisorToolCall) string {
	return runmutation.SupervisorToolExecutionKey(call.RunID, call.Turn, call.ToolName, call.PayloadJSON, call.CallID)
}

func workspaceImageSaveKey(runID, callID, operationKey string, payload json.RawMessage) string {
	return runmutation.Fingerprint("workspace_view_image.v1", runID, callID, operationKey, string(payload))
}
