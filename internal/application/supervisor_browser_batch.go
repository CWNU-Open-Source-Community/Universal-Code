package application

import (
	"fmt"

	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/toolgateway"
)

// Validate only newly prepared calls, after their schemas and current authority
// have passed. A successful v2 input or snapshot replaces the refs available to
// the next model response. A later click/type in this same response therefore
// has an unresolved dependency, even if the earlier operation could fail. Do
// not reinterpret persisted pending calls or replay through this preflight.
func validateSupervisorBrowserBatch(calls []llm.ToolCall) error {
	var invalidator toolgateway.ToolName
	for _, call := range calls {
		name := toolgateway.ToolName(call.Name)
		if !toolgateway.IsBrowserActionTool(name) || !toolgateway.IsAgentBrowserPayload(call.Arguments) {
			continue
		}
		if (name == toolgateway.BrowserClickTool || name == toolgateway.BrowserTypeTool || name == toolgateway.BrowserUploadTool || name == toolgateway.BrowserMoveTool) && invalidator != "" {
			return fmt.Errorf("unresolved_snapshot_dependency: %s cannot use current snapshot refs or screenshot coordinates after %s in the same batch; send one input with current refs, then browser_snapshot (optionally browser_screenshot), and use the returned snapshot_id and refs in the next response", name, invalidator)
		}
		switch name {
		case toolgateway.BrowserNavigateTool, toolgateway.BrowserSnapshotTool,
			toolgateway.BrowserClickTool, toolgateway.BrowserTypeTool, toolgateway.BrowserUploadTool,
			toolgateway.BrowserKeyTool, toolgateway.BrowserScrollTool, toolgateway.BrowserMoveTool:
			invalidator = name
		}
	}
	return nil
}
