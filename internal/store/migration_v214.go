package store

import "strings"

// Rebuild the existing fenced tool ledger without changing stored call bytes,
// rowids or actor bindings. Thread messages carry no execution authority.
func threadPeerToolLedgerStatements() []string {
	previous := threadContextPinLedgerStatements()
	result := make([]string, len(previous))
	replaced := 0
	for i, statement := range previous {
		statement = strings.ReplaceAll(statement, "run_supervisor_tool_calls_v207", "run_supervisor_tool_calls_v214")
		statement = strings.ReplaceAll(statement, "run_supervisor_tool_calls_v206", "run_supervisor_tool_calls_v213")
		replaced += strings.Count(statement, "'context_pin_propose'")
		result[i] = strings.ReplaceAll(statement, "'context_pin_propose'", "'context_pin_propose', 'thread_list', 'thread_send_message'")
	}
	if replaced != 1 {
		panic("thread messaging Supervisor tool allowlist unavailable")
	}
	return result
}
