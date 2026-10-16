package application

import (
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspace"
	"encoding/json"
)

func executeSpecialistScopedRead(root, workspaceID string, input toolgateway.SpecialistToolPayload, pathsJSON string, policy workspace.AgentCodePathPolicy) (any, error) {
	var paths []string
	if err := json.Unmarshal([]byte(pathsJSON), &paths); err != nil {
		return nil, err
	}
	scopes, err := workspace.AgentCodeReadScopes(root, paths, policy)
	if err != nil {
		return nil, err
	}
	switch toolgateway.ToolName(input.Tool) {
	case toolgateway.WorkspaceGlobTool:
		var p toolgateway.WorkspaceGlobPayload
		if err := json.Unmarshal(input.Arguments, &p); err != nil {
			return nil, err
		}
		return workspace.AgentCodeScopedGlobFiles(root, workspaceID, p.Pattern, p.Cursor, p.Limit, scopes, policy)
	case toolgateway.WorkspaceGrepTool:
		var p toolgateway.WorkspaceGrepPayload
		if err := json.Unmarshal(input.Arguments, &p); err != nil {
			return nil, err
		}
		return workspace.AgentCodeScopedGrepFiles(root, workspaceID, p.Query, p.Pattern, p.Cursor, p.Limit, p.CaseSensitive, scopes, policy)
	default:
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(input.Arguments, &p); err != nil {
			return nil, err
		}
		if err := workspace.AgentCodeCheckReadScope(p.Path, scopes); err != nil {
			return nil, err
		}
		value, _, err := executeWorkspaceReadTool(root, workspaceID, toolgateway.ToolName(input.Tool), input.Arguments, policy)
		return value, err
	}
}
