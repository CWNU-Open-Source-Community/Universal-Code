package store

import "strings"

// Reuse the current ledger rebuild, preserving rowids, payloads, authority,
// actor references and immutable call identities. Only the MCP tool allowlists
// change; search is subject to the same durable authority requirement.
var mcpToolSearchStatements = func() []string {
	statements := make([]string, len(builtinSkillReadStatements))
	replaced := 0
	for i, statement := range builtinSkillReadStatements {
		statement = strings.ReplaceAll(statement, "run_supervisor_tool_calls_v173", "run_supervisor_tool_calls_v194")
		statement = strings.ReplaceAll(statement, "run_supervisor_tool_calls_v172", "run_supervisor_tool_calls_v193")
		replaced += strings.Count(statement, "'mcp_tool_call'")
		statements[i] = strings.ReplaceAll(statement, "'mcp_tool_call'", "'mcp_tool_call', 'mcp_tool_search'")
	}
	if replaced != 3 {
		panic("MCP Supervisor tool allowlist or authority constraints unavailable")
	}
	return statements
}()
