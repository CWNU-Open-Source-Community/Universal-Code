package store

import "strings"

// Rebuild the existing fenced tool ledger without changing stored call bytes,
// rowids or actor bindings. The workspace upload is an authority-bound Agent
// browser action in the same family as scroll and key: its durable records
// require the same authority_json checks as the other browser tools.
func agentBrowserUploadSupervisorLedgerStatements() []string {
	previous := threadPeerToolLedgerStatements()
	result := make([]string, len(previous))
	replaced := 0
	for i, statement := range previous {
		statement = strings.ReplaceAll(statement, "run_supervisor_tool_calls_v214", "run_supervisor_tool_calls_v220")
		statement = strings.ReplaceAll(statement, "run_supervisor_tool_calls_v213", "run_supervisor_tool_calls_v219")
		replaced += strings.Count(statement, "'browser_scroll', 'browser_key')")
		result[i] = strings.ReplaceAll(statement, "'browser_scroll', 'browser_key')", "'browser_scroll', 'browser_key', 'browser_upload')")
	}
	if replaced != 3 {
		panic("Agent browser Supervisor authority constraints unavailable")
	}
	return result
}
