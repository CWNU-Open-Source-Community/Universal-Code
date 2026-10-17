package store

import "strings"

// Keep the existing call and Job ledgers, immutable identities and authority
// fences. Plan admission is restricted to one pending, host-prepared run_tests
// call; ordinary command_runtime stays Deliver-only.
func projectTestToolStatements() []string {
	statements := make([]string, len(mcpToolSearchStatements))
	for i, statement := range mcpToolSearchStatements {
		statement = strings.ReplaceAll(statement, "run_supervisor_tool_calls_v194", "run_supervisor_tool_calls_v201")
		statement = strings.ReplaceAll(statement, "run_supervisor_tool_calls_v193", "run_supervisor_tool_calls_v200")
		statements[i] = strings.ReplaceAll(statement, "'command_runtime'", "'command_runtime', 'run_tests'")
	}
	insert := requireMigrationTrigger("trg_command_runtime_job_insert_scope", sbxCommandRuntimeScopeStatements())
	insert = replaceCommandRuntimeMigrationFragment(insert, "mode.phase = 'deliver'", `(mode.phase = 'deliver' OR (mode.phase = 'plan'
		AND NEW.operator_invocation = 0 AND NEW.profile = 'process'
		AND NEW.network = 'disabled' AND NEW.credentials = 'none'
		AND NEW.stdin_policy = 'closed' AND NEW.timeout_milliseconds BETWEEN 1 AND 25000
		AND permission.mode IN ('ask','auto','full')
		AND EXISTS (SELECT 1 FROM run_supervisor_tool_calls call
			JOIN run_supervisor_tool_call_agents actor ON actor.run_id=call.run_id AND actor.call_id=call.call_id
			WHERE call.run_id=run.id AND call.tool_name='run_tests' AND call.status='pending'
			AND actor.agent_id=root.id AND actor.agent_attempt_id=root.active_attempt_id
			AND json_extract(call.authority_json,'$.scope_fingerprint') IS NOT NULL
			AND json_extract(call.authority_json,'$.command_fingerprints[0]')=NEW.spec_fingerprint
			AND json_extract(call.authority_json,'$.permission_snapshot_id')=NEW.permission_snapshot_id
			AND json_extract(call.authority_json,'$.adapter.backend_identity')=NEW.adapter_backend_identity
			AND json_extract(call.authority_json,'$.project_test_command.action')='run'
			AND json_array_length(call.authority_json,'$.project_test_command.commands')=1
			AND json_extract(call.authority_json,'$.project_test_command.commands[0].purpose')=json_extract(NEW.intent_json,'$.purpose')
			AND json_extract(call.payload_json,'$.timeout_milliseconds')=NEW.timeout_milliseconds
			AND json_extract(call.payload_json,'$.target')=NEW.working_directory
		)))`)
	return append(statements, `DROP TRIGGER trg_command_runtime_job_insert_scope;`, insert)
}
