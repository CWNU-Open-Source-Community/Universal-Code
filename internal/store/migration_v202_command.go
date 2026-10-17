package store

// Preserve every existing adapter, permission, resource and lease predicate.
// The additional Session branch accepts only one charged, started worker
// intent whose exact native command fingerprint is being prepared.
func specialistCommandRuntimeScopeStatements() []string {
	insert := requireMigrationTrigger("trg_command_runtime_job_insert_scope", projectTestToolStatements())
	worker := `EXISTS (SELECT 1 FROM agent_nodes child
		JOIN agent_attempts attempt ON attempt.agent_id=child.id AND attempt.run_id=child.run_id
		JOIN specialist_tool_bindings b ON b.agent_attempt_id=attempt.id AND b.agent_id=child.id AND b.run_id=child.run_id
		JOIN specialist_tool_calls call ON call.agent_attempt_id=attempt.id
		JOIN specialist_task_briefs brief ON brief.agent_attempt_id=attempt.id
		JOIN sessions child_session ON child_session.id=child.session_id
		WHERE child.run_id=run.id AND child.parent_id=root.id AND child.role='specialist'
		AND child.session_id=NEW.session_id AND child_session.status='active'
		AND child_session.workspace_id=mission.workspace_id AND child.status='running'
		AND child.active_attempt_id=attempt.id AND attempt.status='running'
		AND attempt.lease_id=NEW.lease_id AND attempt.lease_generation=NEW.lease_generation
		AND b.session_id=child.session_id AND b.workspace_id=NEW.workspace_id AND b.mode='worker'
		AND b.worker_profile_json=child.worker_profile_json AND b.brief_fingerprint=brief.fingerprint
		AND (json_type(child.worker_profile_json,'$.tools')='null' OR EXISTS
		 (SELECT 1 FROM json_each(child.worker_profile_json,'$.tools') tool WHERE tool.value=call.name))
		AND call.name IN ('command_runtime','run_tests') AND call.status='pending'
		AND call.started_at IS NOT NULL AND call.charge_id=NEW.invocation_id AND call.authority_json<>''
		AND json_extract(call.authority_json,'$.protocol_version')='command-runtime-adapter-authority.v2'
		AND json_extract(call.authority_json,'$.run_id')=NEW.run_id
		AND json_extract(call.authority_json,'$.worker.agent_id')=child.id
		AND json_extract(call.authority_json,'$.worker.attempt_id')=attempt.id
		AND json_extract(call.authority_json,'$.worker.session_id')=child.session_id
		AND json_extract(call.authority_json,'$.scope_fingerprint') IS NOT NULL
		AND json_extract(call.authority_json,'$.permission_snapshot_id')=NEW.permission_snapshot_id
		AND json_extract(call.authority_json,'$.permission_revision')=NEW.permission_revision
		AND json_extract(call.authority_json,'$.permission_mode')=NEW.permission_mode
		AND json_extract(call.authority_json,'$.adapter.backend_identity')=NEW.adapter_backend_identity
		AND json_extract(call.authority_json,'$.adapter.generation')=NEW.adapter_generation
		AND EXISTS(SELECT 1 FROM json_each(call.authority_json,'$.command_fingerprints') pin WHERE pin.value=NEW.spec_fingerprint))`
	insert = replaceCommandRuntimeMigrationFragment(insert,
		"run.session_id = NEW.session_id AND mission.workspace_id = NEW.workspace_id",
		"(run.session_id = NEW.session_id OR ("+worker+")) AND mission.workspace_id = NEW.workspace_id")
	// Project tests retain their fixed closed-stdin, offline, process-only shape
	// in Plan. The extra child branch is fenced by the worker intent above.
	insert = replaceCommandRuntimeMigrationFragment(insert,
		"mode.phase = 'deliver' OR (mode.phase = 'plan'",
		`mode.phase = 'deliver' OR (mode.phase='plan' AND NEW.operator_invocation=0
		AND NEW.profile='process' AND NEW.network='disabled' AND NEW.credentials='none'
		AND NEW.stdin_policy='closed' AND NEW.timeout_milliseconds BETWEEN 1 AND 25000
		AND permission.mode IN ('ask','auto','full') AND (`+worker+`)
		AND EXISTS(SELECT 1 FROM specialist_tool_calls call JOIN agent_attempts attempt ON attempt.id=call.agent_attempt_id
		 WHERE attempt.run_id=run.id AND call.charge_id=NEW.invocation_id AND call.name='run_tests'
		 AND json_extract(call.authority_json,'$.project_test_command.action')='run'
		 AND json_array_length(call.authority_json,'$.project_test_command.commands')=1
		 AND json_extract(call.authority_json,'$.project_test_command.commands[0].purpose')=json_extract(NEW.intent_json,'$.purpose')
		 AND json_extract(call.payload_json,'$.timeout_milliseconds')=NEW.timeout_milliseconds
		 AND json_extract(call.payload_json,'$.target')=NEW.working_directory)) OR (mode.phase = 'plan'`)
	return []string{`DROP TRIGGER trg_command_runtime_job_insert_scope;`, insert}
}
