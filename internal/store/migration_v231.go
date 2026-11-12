package store

import "strings"

// Extend the fenced tool ledger with same-page pointer movement, preserving
// historical call bytes, rowids, actor bindings and the authority checks.
func agentBrowserMoveSupervisorLedgerStatements() []string {
	previous := agentBrowserUploadSupervisorLedgerStatements()
	result := make([]string, len(previous))
	replaced := 0
	for i, statement := range previous {
		statement = strings.ReplaceAll(statement, "run_supervisor_tool_calls_v220", "run_supervisor_tool_calls_v231")
		statement = strings.ReplaceAll(statement, "run_supervisor_tool_calls_v219", "run_supervisor_tool_calls_v230")
		replaced += strings.Count(statement, "'browser_key', 'browser_upload')")
		result[i] = strings.ReplaceAll(statement, "'browser_key', 'browser_upload')", "'browser_key', 'browser_upload', 'browser_move')")
	}
	if replaced != 3 {
		panic("Agent browser pointer authority constraints unavailable")
	}
	return result
}
