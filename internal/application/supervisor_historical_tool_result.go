package application

import "cyberagent-workbench/internal/domain"

// A historical receipt is not a current snapshot. Keep the exact durable
// readback in the enclosing receipt without treating old pixels as text.
func supervisorHistoricalToolResult(call domain.SupervisorToolCall, result any) any {
	object, ok := result.(map[string]any)
	if !ok {
		return result
	}
	if call.ToolName == "mcp_tool_call" {
		projectMCPContent(object, nil, false, call.CallID)
		return object
	}
	if call.ToolName != "browser_snapshot" {
		return result
	}
	// Generic compaction would retain only three layout nodes while leaving
	// truncated=false, so omit the historical layout as a unit.
	out := make(map[string]any, len(object))
	for key, value := range object {
		if key != "layout" {
			out[key] = value
		}
	}
	if _, ok := object["layout"]; ok {
		out["layout_omitted"] = true
	}
	out["browser_references_historical"] = true
	return out
}
