package store

import "strings"

// commandRuntimeAdapterStatements rebuilds the v116 Job ledger around an
// explicit adapter identity. Historical rows receive legacy_unbound and remain
// readable, but inserts, active-job accounting, and process ownership never
// treat that projection as executable authority. The Supervisor call ledger is
// rebuilt at the same migration so every newly advertised command_runtime call
// carries the exact Go-issued adapter generation that execution must match.
var commandRuntimeAdapterStatements = func() []string {
	legacyJobProjectionColumns := strings.Join([]string{
		"id", "protocol_version", "operation_digest", "request_fingerprint",
		"invocation_id", "run_id", "mission_id", "session_id", "workspace_id",
		"root_agent_id", "workspace_root_sha256", "mode_snapshot_id", "mode_revision",
		"profile_snapshot_id", "profile_revision", "permission_snapshot_id",
		"permission_revision", "permission_mode", "lease_id", "lease_generation",
		"lease_owner_id", "owner_id", "owner_generation", "owner_renewed_at",
		"owner_expires_at", "intent_json", "spec_fingerprint", "profile",
		"executable_path", "executable_sha256", "environment_sha256",
		"working_directory", "stdin_policy", "network", "credentials",
		"timeout_milliseconds", "inline_limit_bytes", "artifact_limit_bytes", "state",
		"pid", "process_group", "stdout", "stderr", "stdout_observed_bytes",
		"stderr_observed_bytes", "output_cursor", "output_base_cursor",
		"output_frames_json", "stdout_sha256", "stderr_sha256", "truncation_reason",
		"exit_code", "timed_out", "cancelled", "killed", "tree_reaped",
		"job_assigned_at_creation", "stdin_closed", "stdin_write_count", "version",
		"created_at", "started_at", "completed_at", "updated_at",
	}, ", ")
	createJobs := requireMigrationStatement("CREATE TABLE command_runtime_jobs (",
		commandRuntimeStatements)
	createJobs = replaceCommandRuntimeMigrationFragment(createJobs,
		"CREATE TABLE command_runtime_jobs (",
		"CREATE TABLE command_runtime_jobs_v131 (")
	createJobs = replaceCommandRuntimeMigrationFragment(createJobs,
		"\t\tupdated_at TEXT NOT NULL,\n\t\tFOREIGN KEY(run_id)",
		"\t\tupdated_at TEXT NOT NULL,\n"+
			"\t\tadapter_kind TEXT NOT NULL,\n"+
			"\t\tadapter_backend TEXT NOT NULL,\n"+
			"\t\tadapter_backend_identity TEXT NOT NULL,\n"+
			"\t\tadapter_generation TEXT NOT NULL,\n"+
			"\t\tadapter_isolation_grade TEXT NOT NULL,\n"+
			"\t\tadapter_network_policy TEXT NOT NULL,\n"+
			"\t\tadapter_credential_policy TEXT NOT NULL,\n"+
			"\t\tFOREIGN KEY(run_id)")
	createJobs = replaceCommandRuntimeMigrationFragment(createJobs,
		"\t\tCHECK(permission_mode = 'full_access'),",
		"\t\tCHECK((adapter_kind = 'sandboxed_workspace' AND permission_mode = 'workspace_access'\n"+
			"\t\t\t\tAND adapter_isolation_grade = 'workspace_sandbox'\n"+
			"\t\t\t\tAND adapter_network_policy = 'denied' AND adapter_credential_policy = 'none')\n"+
			"\t\t\tOR (adapter_kind = 'host_unsandboxed' AND permission_mode = 'full_access'\n"+
			"\t\t\t\tAND adapter_isolation_grade = 'host_unsandboxed'\n"+
			"\t\t\t\tAND adapter_network_policy = 'host_available'\n"+
			"\t\t\t\tAND adapter_credential_policy = 'host_available')\n"+
			"\t\t\tOR (adapter_kind = 'legacy_unbound' AND permission_mode = 'full_access'\n"+
			"\t\t\t\tAND adapter_backend = 'legacy_unbound'\n"+
			"\t\t\t\tAND adapter_backend_identity = 'legacy_unbound'\n"+
			"\t\t\t\tAND adapter_generation = 'legacy_unbound'\n"+
			"\t\t\t\tAND adapter_isolation_grade = 'legacy_unknown'\n"+
			"\t\t\t\tAND adapter_network_policy = 'legacy_unknown'\n"+
			"\t\t\t\tAND adapter_credential_policy = 'legacy_unknown')),\n"+
			"\t\tCHECK(length(adapter_kind) BETWEEN 1 AND 256\n"+
			"\t\t\tAND length(adapter_backend) BETWEEN 1 AND 256\n"+
			"\t\t\tAND length(adapter_backend_identity) BETWEEN 1 AND 256\n"+
			"\t\t\tAND length(adapter_generation) BETWEEN 1 AND 256),")
	createJobs = replaceCommandRuntimeMigrationFragment(createJobs,
		"OR (state IN ('running', 'stopping') AND pid > 0 AND process_group > 0\n"+
			"\t\t\t\tAND job_assigned_at_creation = 1 AND started_at IS NOT NULL",
		"OR (state IN ('running', 'stopping')\n"+
			"\t\t\t\tAND ((adapter_kind = 'sandboxed_workspace' AND pid = 0 AND process_group = 0)\n"+
			"\t\t\t\t\tOR (adapter_kind IN ('host_unsandboxed', 'legacy_unbound')\n"+
			"\t\t\t\t\t\tAND pid > 0 AND process_group > 0))\n"+
			"\t\t\t\tAND job_assigned_at_creation = 1 AND started_at IS NOT NULL")

	insertScope := requireMigrationTrigger("trg_command_runtime_job_insert_scope",
		commandRuntimeStatements)
	insertScope = replaceCommandRuntimeMigrationFragment(insertScope,
		"AND profile.revision = NEW.profile_revision AND profile.profile = 'local'",
		"AND profile.revision = NEW.profile_revision\n"+
			"\t\t\t\tAND ((NEW.adapter_kind = 'host_unsandboxed' AND profile.profile = 'local')\n"+
			"\t\t\t\t\tOR (NEW.adapter_kind = 'sandboxed_workspace'\n"+
			"\t\t\t\t\t\tAND ((NEW.adapter_backend = 'local_windows_lpac' AND profile.profile = 'local')\n"+
			"\t\t\t\t\t\t\tOR (NEW.adapter_backend = 'docker_standard_code' AND profile.profile = 'docker'))))")
	insertScope = replaceCommandRuntimeMigrationFragment(insertScope,
		"AND permission.mode = NEW.permission_mode AND permission.mode = 'full_access'",
		"AND permission.mode = NEW.permission_mode\n"+
			"\t\t\t\tAND ((NEW.adapter_kind = 'host_unsandboxed' AND permission.mode = 'full_access')\n"+
			"\t\t\t\t\tOR (NEW.adapter_kind = 'sandboxed_workspace'\n"+
			"\t\t\t\t\t\tAND permission.mode = 'workspace_access'))")

	insertLimit := requireMigrationTrigger("trg_command_runtime_job_insert_limit",
		commandRuntimeStatements)
	insertLimit = replaceCommandRuntimeMigrationFragment(insertLimit,
		"WHERE run_id = NEW.run_id AND state IN ('prepared', 'running', 'stopping')",
		"WHERE run_id = NEW.run_id AND adapter_kind <> 'legacy_unbound'\n"+
			"\t\t\tAND state IN ('prepared', 'running', 'stopping')")

	transition := requireMigrationTrigger("trg_command_runtime_job_update_transition",
		commandRuntimeStatements)
	transition = replaceCommandRuntimeMigrationFragment(transition,
		"OR NEW.lease_owner_id != OLD.lease_owner_id OR NEW.owner_id != OLD.owner_id",
		"OR NEW.lease_owner_id != OLD.lease_owner_id\n"+
			"\t\t\tOR NEW.adapter_kind != OLD.adapter_kind\n"+
			"\t\t\tOR NEW.adapter_backend != OLD.adapter_backend\n"+
			"\t\t\tOR NEW.adapter_backend_identity != OLD.adapter_backend_identity\n"+
			"\t\t\tOR NEW.adapter_generation != OLD.adapter_generation\n"+
			"\t\t\tOR NEW.adapter_isolation_grade != OLD.adapter_isolation_grade\n"+
			"\t\t\tOR NEW.adapter_network_policy != OLD.adapter_network_policy\n"+
			"\t\t\tOR NEW.adapter_credential_policy != OLD.adapter_credential_policy\n"+
			"\t\t\tOR NEW.owner_id != OLD.owner_id")

	createCalls := requireMigrationStatement("CREATE TABLE run_supervisor_tool_calls (",
		githubReviewStatements)
	createCalls = replaceCommandRuntimeMigrationFragment(createCalls,
		"CREATE TABLE run_supervisor_tool_calls (",
		"CREATE TABLE run_supervisor_tool_calls_v131 (")
	createCalls = replaceCommandRuntimeMigrationFragment(createCalls,
		"\t\tcompleted_at TEXT,\n\t\tPRIMARY KEY",
		"\t\tcompleted_at TEXT,\n"+
			"\t\tstream_response_id TEXT NOT NULL DEFAULT '',\n"+
			"\t\tstream_item_id TEXT NOT NULL DEFAULT '',\n"+
			"\t\tstream_call_id TEXT NOT NULL DEFAULT '',\n"+
			"\t\tPRIMARY KEY")
	createCalls = strings.ReplaceAll(createCalls,
		"'github_review_evidence_list', 'github_review_evidence_read')",
		"'github_review_evidence_list', 'github_review_evidence_read', 'command_runtime')")
	if strings.Count(createCalls, "'command_runtime')") < 2 {
		panic("command runtime Supervisor authority constraint is unavailable")
	}

	createCandidates := requireMigrationStatement(
		"CREATE TABLE sandbox_execution_candidates (", sandboxExecutionCandidateStatements)
	createCandidates = replaceCommandRuntimeMigrationFragment(createCandidates,
		"CREATE TABLE sandbox_execution_candidates (",
		"CREATE TABLE sandbox_execution_candidates_v131 (")
	createCandidates = replaceCommandRuntimeMigrationFragment(createCandidates,
		"\t\tlease_quiescent INTEGER NOT NULL,\n",
		"\t\tlease_quiescent INTEGER NOT NULL,\n"+
			"\t\trun_lease_id TEXT NOT NULL DEFAULT '',\n"+
			"\t\trun_lease_generation INTEGER NOT NULL DEFAULT 0,\n"+
			"\t\trun_lease_owner_id TEXT NOT NULL DEFAULT '',\n")
	createCandidates = replaceCommandRuntimeMigrationFragment(createCandidates,
		"\t\tCHECK(protocol_version = 'sandbox_execution_candidate.v1'),",
		"\t\tCHECK(protocol_version IN ('sandbox_execution_candidate.v1',\n"+
			"\t\t\t'sandbox_execution_candidate.v2')),")
	createCandidates = replaceCommandRuntimeMigrationFragment(createCandidates,
		"\t\tCHECK(budget_checked = 1 AND lease_quiescent = 1),",
		"\t\tCHECK(budget_checked = 1),\n"+
			"\t\tCHECK((protocol_version = 'sandbox_execution_candidate.v1'\n"+
			"\t\t\t\tAND lease_quiescent = 1 AND run_lease_id = ''\n"+
			"\t\t\t\tAND run_lease_generation = 0 AND run_lease_owner_id = '')\n"+
			"\t\t\tOR (protocol_version = 'sandbox_execution_candidate.v2' AND (\n"+
			"\t\t\t\t(lease_quiescent = 1 AND run_lease_id = ''\n"+
			"\t\t\t\t\tAND run_lease_generation = 0 AND run_lease_owner_id = '')\n"+
			"\t\t\t\tOR (lease_quiescent = 0 AND length(run_lease_id) BETWEEN 1 AND 256\n"+
			"\t\t\t\t\tAND run_lease_generation > 0\n"+
			"\t\t\t\t\tAND length(run_lease_owner_id) BETWEEN 1 AND 256)))),")
	createCandidates = replaceCommandRuntimeMigrationFragment(createCandidates,
		"\t\tCHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256\n"+
			"\t\t\tAND instr(requested_by, char(0)) = 0)",
		"\t\tCHECK(run_lease_id = trim(run_lease_id) AND instr(run_lease_id, char(0)) = 0),\n"+
			"\t\tCHECK(run_lease_owner_id = trim(run_lease_owner_id)\n"+
			"\t\t\tAND instr(run_lease_owner_id, char(0)) = 0),\n"+
			"\t\tCHECK(requested_by = trim(requested_by) AND length(requested_by) BETWEEN 1 AND 256\n"+
			"\t\t\tAND instr(requested_by, char(0)) = 0)")

	quiescentCandidateGuard := "\t\t\t\tAND NOT EXISTS (SELECT 1 FROM run_execution_leases lease\n" +
		"\t\t\t\t\tWHERE lease.run_id = NEW.run_id AND lease.status = 'active'\n" +
		"\t\t\t\t\t\tAND julianday(lease.expires_at) > julianday('now'))"
	leaseBoundCandidateGuard := "\t\t\t\tAND ((NEW.lease_quiescent = 1\n" +
		"\t\t\t\t\t\tAND NOT EXISTS (SELECT 1 FROM run_execution_leases lease\n" +
		"\t\t\t\t\t\t\tWHERE lease.run_id = NEW.run_id AND lease.status = 'active'\n" +
		"\t\t\t\t\t\t\t\tAND julianday(lease.expires_at) > julianday('now')))\n" +
		"\t\t\t\t\tOR (NEW.lease_quiescent = 0\n" +
		"\t\t\t\t\t\tAND EXISTS (SELECT 1 FROM run_execution_leases lease\n" +
		"\t\t\t\t\t\t\tWHERE lease.run_id = NEW.run_id AND lease.status = 'active'\n" +
		"\t\t\t\t\t\t\t\tAND lease.lease_id = NEW.run_lease_id\n" +
		"\t\t\t\t\t\t\t\tAND lease.generation = NEW.run_lease_generation\n" +
		"\t\t\t\t\t\t\t\tAND lease.owner_id = NEW.run_lease_owner_id\n" +
		"\t\t\t\t\t\t\t\tAND julianday(lease.expires_at) > julianday('now'))))"
	candidateInsert := requireMigrationTrigger(
		"trg_sandbox_execution_candidate_insert", sandboxExecutionCandidateStatements)
	candidateInsert = replaceCommandRuntimeMigrationFragment(candidateInsert,
		quiescentCandidateGuard, leaseBoundCandidateGuard)

	quiescentStageGuard := "\t\t\t\tAND NOT EXISTS (SELECT 1 FROM run_execution_leases run_lease\n" +
		"\t\t\t\t\tWHERE run_lease.run_id = NEW.run_id AND run_lease.status = 'active'\n" +
		"\t\t\t\t\t\tAND julianday(run_lease.expires_at) > julianday('now'))"
	leaseBoundStageGuard := "\t\t\t\tAND ((candidate.lease_quiescent = 1\n" +
		"\t\t\t\t\t\tAND NOT EXISTS (SELECT 1 FROM run_execution_leases run_lease\n" +
		"\t\t\t\t\t\t\tWHERE run_lease.run_id = NEW.run_id AND run_lease.status = 'active'\n" +
		"\t\t\t\t\t\t\t\tAND julianday(run_lease.expires_at) > julianday('now')))\n" +
		"\t\t\t\t\tOR (candidate.lease_quiescent = 0\n" +
		"\t\t\t\t\t\tAND EXISTS (SELECT 1 FROM run_execution_leases run_lease\n" +
		"\t\t\t\t\t\t\tWHERE run_lease.run_id = NEW.run_id AND run_lease.status = 'active'\n" +
		"\t\t\t\t\t\t\t\tAND run_lease.lease_id = candidate.run_lease_id\n" +
		"\t\t\t\t\t\t\t\tAND run_lease.generation = candidate.run_lease_generation\n" +
		"\t\t\t\t\t\t\t\tAND run_lease.owner_id = candidate.run_lease_owner_id\n" +
		"\t\t\t\t\t\t\t\tAND julianday(run_lease.expires_at) > julianday('now'))))"
	leaseBoundTrigger := func(name string, statements []string) string {
		value := requireMigrationTrigger(name, statements)
		return replaceCommandRuntimeMigrationFragment(value, quiescentStageGuard,
			leaseBoundStageGuard)
	}
	leaseBoundExecutionGuard := strings.ReplaceAll(leaseBoundStageGuard,
		"run_execution_leases run_lease", "run_execution_leases lease")
	leaseBoundExecutionGuard = strings.ReplaceAll(leaseBoundExecutionGuard,
		"run_lease.", "lease.")
	disabledExecutionInsert := requireMigrationTrigger(
		"trg_sandbox_disabled_execution_insert", sandboxLifecycleStatements)
	disabledExecutionInsert = replaceCommandRuntimeMigrationFragment(
		disabledExecutionInsert, quiescentCandidateGuard, leaseBoundExecutionGuard)
	disabledPreflightInsert := leaseBoundTrigger(
		"trg_sandbox_disabled_preflight_insert", sandboxPreflightStatements)
	backendEvidenceInsert := leaseBoundTrigger(
		"trg_sandbox_backend_evidence_insert", sandboxBackendEvidenceStatements)
	outputSimulationInsert := leaseBoundTrigger(
		"trg_sandbox_output_simulation_insert", sandboxBackendEvidenceStatements)
	dockerObservationInsert := leaseBoundTrigger(
		"trg_sandbox_docker_observation_insert", sandboxDockerObservationStatements)
	dockerPlanInsert := leaseBoundTrigger(
		"trg_sandbox_docker_container_plan_insert", sandboxDockerContainerPlanStatements)
	dockerAdmissionInsert := requireMigrationTrigger(
		"trg_sandbox_docker_product_admission_insert",
		sandboxDockerProductAdmissionStatements)
	dockerAdmissionInsert = replaceCommandRuntimeMigrationFragment(
		dockerAdmissionInsert, quiescentCandidateGuard, leaseBoundExecutionGuard)

	statements := []string{
		`DROP TRIGGER trg_sandbox_execution_candidate_insert;`,
		`DROP TRIGGER trg_sandbox_execution_candidate_operation_insert;`,
		`DROP TRIGGER trg_sandbox_execution_candidate_update_immutable;`,
		`DROP TRIGGER trg_sandbox_execution_candidate_delete_immutable;`,
		`DROP TRIGGER trg_sandbox_disabled_execution_insert;`,
		`DROP TRIGGER trg_sandbox_disabled_preflight_insert;`,
		`DROP TRIGGER trg_sandbox_backend_evidence_insert;`,
		`DROP TRIGGER trg_sandbox_output_simulation_insert;`,
		`DROP TRIGGER trg_sandbox_docker_observation_insert;`,
		`DROP TRIGGER trg_sandbox_docker_container_plan_insert;`,
		`DROP TRIGGER trg_sandbox_docker_product_admission_insert;`,
		createCandidates,
		`INSERT INTO sandbox_execution_candidates_v131
			(id, preparation_id, run_id, mission_id, workspace_id, protocol_version,
			 manifest_fingerprint, authorization_fingerprint, workspace_fingerprint,
			 scope_fingerprint, policy_fingerprint, mount_binding_fingerprint,
			 approval_id, approval_status, mount_count, regular_file_mount_count,
			 directory_mount_count, tokens_used, execution_millis_used, tool_calls_used,
			 budget_checked, lease_quiescent, run_lease_id, run_lease_generation,
			 run_lease_owner_id, backend_enabled, execution_authorized, requested_by, validated_at)
		SELECT id, preparation_id, run_id, mission_id, workspace_id, protocol_version,
			 manifest_fingerprint, authorization_fingerprint, workspace_fingerprint,
			 scope_fingerprint, policy_fingerprint, mount_binding_fingerprint,
			 approval_id, approval_status, mount_count, regular_file_mount_count,
			 directory_mount_count, tokens_used, execution_millis_used, tool_calls_used,
			 budget_checked, lease_quiescent, '', 0, '', backend_enabled,
			 execution_authorized, requested_by, validated_at
		FROM sandbox_execution_candidates;`,
		`DROP TABLE sandbox_execution_candidates;`,
		`ALTER TABLE sandbox_execution_candidates_v131 RENAME TO sandbox_execution_candidates;`,
		requireMigrationStatement(
			"CREATE INDEX idx_sandbox_execution_candidates_run_validated",
			sandboxExecutionCandidateStatements),
		candidateInsert,
		requireMigrationTrigger("trg_sandbox_execution_candidate_operation_insert",
			sandboxExecutionCandidateStatements),
		requireMigrationTrigger("trg_sandbox_execution_candidate_update_immutable",
			sandboxExecutionCandidateStatements),
		requireMigrationTrigger("trg_sandbox_execution_candidate_delete_immutable",
			sandboxExecutionCandidateStatements),
		disabledExecutionInsert,
		disabledPreflightInsert,
		backendEvidenceInsert,
		outputSimulationInsert,
		dockerObservationInsert,
		dockerPlanInsert,
		dockerAdmissionInsert,
	}

	return append(statements, []string{
		`DROP TRIGGER trg_command_runtime_job_insert_scope;`,
		`DROP TRIGGER trg_command_runtime_job_insert_limit;`,
		`DROP TRIGGER trg_command_runtime_job_update_transition;`,
		`DROP TRIGGER trg_command_runtime_job_delete_immutable;`,
		`DROP INDEX idx_command_runtime_jobs_run_created;`,
		`DROP INDEX idx_command_runtime_jobs_active;`,
		`ALTER TABLE command_runtime_jobs RENAME TO command_runtime_jobs_v130;`,
		createJobs,
		`INSERT INTO command_runtime_jobs_v131 SELECT ` + legacyJobProjectionColumns + `,
			'legacy_unbound', 'legacy_unbound', 'legacy_unbound', 'legacy_unbound',
			'legacy_unknown', 'legacy_unknown', 'legacy_unknown'
			FROM command_runtime_jobs_v130;`,
		`DROP TABLE command_runtime_jobs_v130;`,
		`ALTER TABLE command_runtime_jobs_v131 RENAME TO command_runtime_jobs;`,
		requireMigrationStatement("CREATE INDEX idx_command_runtime_jobs_run_created",
			commandRuntimeStatements),
		requireMigrationStatement("CREATE INDEX idx_command_runtime_jobs_active",
			commandRuntimeStatements),
		insertScope,
		insertLimit,
		transition,
		requireMigrationTrigger("trg_command_runtime_job_delete_immutable",
			commandRuntimeStatements),

		`DROP TRIGGER trg_supervisor_tool_call_model_attempt;`,
		`DROP TRIGGER trg_supervisor_tool_round_completion;`,
		`DROP TRIGGER trg_supervisor_tool_stream_identity_immutable;`,
		`DROP TRIGGER trg_supervisor_tool_stream_identity_insert;`,
		`DROP INDEX idx_run_supervisor_tool_calls_pending;`,
		`DROP INDEX idx_supervisor_tool_stream_call_identity;`,
		`DROP INDEX idx_supervisor_tool_stream_item_identity;`,
		`ALTER TABLE run_supervisor_tool_calls RENAME TO run_supervisor_tool_calls_v130;`,
		createCalls,
		// v116-v130 command calls carried no adapter authority. Preserve their
		// audit fields with a readable history marker that DecodeAuthority
		// rejects, rather than manufacturing a binding to an executable adapter.
		`INSERT INTO run_supervisor_tool_calls_v131
			(run_id, turn, attempt_id, round, position, model_attempt, call_id,
			 tool_name, payload_json, authority_json, status, result_json, error_code,
			 created_at, completed_at, stream_response_id, stream_item_id, stream_call_id)
		SELECT run_id, turn, attempt_id, round, position, model_attempt, call_id,
			tool_name, payload_json,
			CASE WHEN tool_name = 'command_runtime' AND authority_json = ''
				THEN '{"protocol_version":"command-runtime-legacy-unbound.v1","authority_json":""}'
				ELSE authority_json END,
			status, result_json, error_code, created_at, completed_at,
			stream_response_id, stream_item_id, stream_call_id
			FROM run_supervisor_tool_calls_v130;`,
		`DROP TABLE run_supervisor_tool_calls_v130;`,
		`ALTER TABLE run_supervisor_tool_calls_v131 RENAME TO run_supervisor_tool_calls;`,
		requireMigrationStatement("CREATE INDEX idx_run_supervisor_tool_calls_pending",
			githubReviewStatements),
		requireMigrationStatement("CREATE UNIQUE INDEX idx_supervisor_tool_stream_item_identity",
			itemStreamToolIdentityStatements),
		requireMigrationStatement("CREATE UNIQUE INDEX idx_supervisor_tool_stream_call_identity",
			itemStreamToolIdentityStatements),
		requireMigrationTrigger("trg_supervisor_tool_call_model_attempt",
			githubReviewStatements),
		requireMigrationTrigger("trg_supervisor_tool_round_completion",
			githubReviewStatements),
		requireMigrationTrigger("trg_supervisor_tool_stream_identity_insert",
			itemStreamToolIdentityStatements),
		requireMigrationTrigger("trg_supervisor_tool_stream_identity_immutable",
			itemStreamToolIdentityStatements),
	}...)
}()

func replaceCommandRuntimeMigrationFragment(statement, old, replacement string) string {
	if !strings.Contains(statement, old) {
		panic("command runtime adapter migration fragment is unavailable: " + old)
	}
	return strings.Replace(statement, old, replacement, 1)
}
