package application

import (
	"encoding/json"
	"fmt"

	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspace"
)

// executeWorkspaceReadTool decodes an already authorized read and calls the
// shared workspace implementation. Each caller retains its own scope and lease
// checks before and after the observation.
func executeWorkspaceReadTool(root, workspaceID string, name toolgateway.ToolName,
	payload json.RawMessage, pathPolicy workspace.AgentCodePathPolicy,
) (any, map[string]string, error) {
	var value any
	metadata := map[string]string{"workspace_id": workspaceID}
	switch name {
	case toolgateway.WorkspaceListTool:
		var input toolgateway.WorkspaceListPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return nil, nil, err
		}
		result, err := workspace.AgentCodeListDirectory(root,
			workspaceID, input.Path, input.Cursor, input.Limit, false, pathPolicy)
		if err != nil {
			return nil, nil, err
		}
		value = result
		metadata["result_count"] = fmt.Sprint(len(result.Items))
		metadata["truncated"] = fmt.Sprint(result.Truncated)
	case toolgateway.WorkspaceReadTool:
		var input toolgateway.WorkspaceReadPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return nil, nil, err
		}
		result, err := workspace.AgentCodeReadFile(root,
			workspaceID, input.Path, input.StartLine, input.EndLine, false, pathPolicy)
		if err != nil {
			return nil, nil, err
		}
		value = result
		metadata["path"] = result.Path
		metadata["content_sha256"] = result.ContentSHA256
		metadata["truncated"] = fmt.Sprint(result.Truncated)
	case toolgateway.WorkspaceGlobTool:
		var input toolgateway.WorkspaceGlobPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return nil, nil, err
		}
		result, err := workspace.AgentCodeGlobFiles(root,
			workspaceID, input.Pattern, input.Cursor, input.Limit, false, pathPolicy)
		if err != nil {
			return nil, nil, err
		}
		value = result
		metadata["result_count"] = fmt.Sprint(len(result.Paths))
		metadata["truncated"] = fmt.Sprint(result.Truncated)
	case toolgateway.WorkspaceGrepTool:
		var input toolgateway.WorkspaceGrepPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return nil, nil, err
		}
		result, err := workspace.AgentCodeGrepFiles(root,
			workspaceID, input.Query, input.Pattern, input.Cursor, input.Limit,
			input.CaseSensitive, false, pathPolicy)
		if err != nil {
			return nil, nil, err
		}
		value = result
		metadata["result_count"] = fmt.Sprint(len(result.Matches))
		metadata["truncated"] = fmt.Sprint(result.Truncated)
	default:
		return nil, nil, fmt.Errorf("unsupported workspace read tool %q", name)
	}
	return value, metadata, nil
}
