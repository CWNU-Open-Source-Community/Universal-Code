package store

import "strings"

// Preserve the existing ledger and its authority checks while admitting the
// new read-only image tool wherever existing AgentCode reads are admitted.
func workspaceImageSupervisorLedgerStatements() []string {
	previous := agentBrowserMoveSupervisorLedgerStatements()
	result := make([]string, len(previous))
	replaced := 0
	for i, statement := range previous {
		statement = strings.ReplaceAll(statement, "run_supervisor_tool_calls_v231", "run_supervisor_tool_calls_v232")
		statement = strings.ReplaceAll(statement, "run_supervisor_tool_calls_v230", "run_supervisor_tool_calls_v231")
		replaced += strings.Count(statement, "'workspace_list', 'workspace_read',")
		result[i] = strings.ReplaceAll(statement, "'workspace_list', 'workspace_read',", "'workspace_list', 'workspace_read', 'workspace_view_image',")
	}
	if replaced != 3 {
		panic("workspace image authority constraints unavailable")
	}
	return result
}
