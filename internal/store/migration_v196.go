package store

import "strings"

// The server descriptor and audit relationships stay unchanged. Only the
// accepted transport names expand; all historical rowids and bytes survive.
var mcpRemoteTransportStatements = func() []string {
	const next = "mcp_client_servers_v196"
	schema := requireMigrationStatement("CREATE TABLE mcp_client_servers (", extensionRuntimeStatements)
	schema = strings.Replace(schema, "mcp_client_servers (", next+" (", 1)
	const prior = "CHECK(transport IN ('stdio', 'streamable_http'))"
	if strings.Count(schema, prior) != 1 {
		panic("MCP server transport constraint unavailable")
	}
	schema = strings.Replace(schema, prior, "CHECK(transport IN ('stdio', 'streamable_http', 'sse', 'websocket'))", 1)
	statements := []string{`PRAGMA legacy_alter_table=ON;`, schema,
		`INSERT INTO ` + next + ` SELECT * FROM mcp_client_servers;`,
		`DROP TABLE mcp_client_servers;`, `ALTER TABLE ` + next + ` RENAME TO mcp_client_servers;`}
	// Copy rowids explicitly because activity and retained audit references can
	// rely on the original insertion identity even after descriptor deletion is denied.
	statements[2] = `INSERT INTO ` + next + `(rowid,id,protocol_version,name,transport,target,scope,run_id,workspace_id,
		descriptor_json,descriptor_fingerprint,state,capability_json,capability_fingerprint,approved_capability_fingerprint,
		health,health_message,discovery_lease_id,discovery_lease_expires_at,generation,reviewed_by,reviewed_at,created_at,updated_at)
		SELECT rowid,* FROM mcp_client_servers;`
	statements = append(statements, requireMigrationStatement("CREATE INDEX idx_mcp_client_servers_scope_state", extensionRuntimeStatements))
	for _, name := range []string{"trg_mcp_client_server_limit", "trg_mcp_client_server_descriptor_immutable", "trg_mcp_client_server_generation", "trg_mcp_client_servers_delete_immutable"} {
		statements = append(statements, requireMigrationTrigger(name, extensionRuntimeStatements))
	}
	return append(statements, `PRAGMA legacy_alter_table=OFF;`)
}()
