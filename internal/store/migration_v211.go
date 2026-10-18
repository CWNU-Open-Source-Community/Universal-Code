package store

import "strings"

// Retain the complete v202 ownership/permission trigger. Only the two Plan
// registered-test branches admit a host-prepared background start; generic
// commands and operator Jobs retain their existing phase restrictions.
func longProjectTestJobStatements() []string {
	insert := requireMigrationTrigger("trg_command_runtime_job_insert_scope", specialistCommandRuntimeScopeStatements())
	insert = strings.ReplaceAll(insert, "NEW.timeout_milliseconds BETWEEN 1 AND 25000", "NEW.timeout_milliseconds BETWEEN 1 AND 1800000")
	insert = strings.ReplaceAll(insert, "json_extract(call.authority_json,'$.project_test_command.action')='run'", `((json_extract(call.authority_json,'$.project_test_command.action')='run'
	 AND NEW.timeout_milliseconds<=25000 AND COALESCE(json_extract(call.payload_json,'$.action'),'run')='run')
	 OR (json_extract(call.authority_json,'$.project_test_command.action')='start'
	 AND json_extract(call.payload_json,'$.action')='start'
	 AND length(json_extract(call.authority_json,'$.project_test_source.workspace_version'))=64
	 AND length(json_extract(call.authority_json,'$.project_test_source.test_fingerprint'))=64))`)
	return []string{`DROP TRIGGER trg_command_runtime_job_insert_scope;`, insert}
}
