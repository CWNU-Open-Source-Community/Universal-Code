package store

import "strings"

func specialistFileEditStatements() []string {
	auto := requireMigrationTrigger("trg_file_edit_auto_authorization_insert", fileOperationApprovalStatements())
	worker := `EXISTS(SELECT 1 FROM agent_nodes child JOIN agent_attempts attempt ON attempt.id=child.active_attempt_id
 JOIN specialist_tool_bindings b ON b.agent_attempt_id=attempt.id JOIN specialist_tool_calls call ON call.agent_attempt_id=attempt.id
 WHERE child.run_id=run.id AND child.id=NEW.agent_id AND child.session_id=NEW.session_id AND child.role='specialist'
 AND child.status='running' AND attempt.status='running' AND attempt.agent_id=child.id AND attempt.run_id=run.id
 AND attempt.lease_id=NEW.lease_id AND attempt.lease_generation=NEW.lease_generation
 AND b.mode='worker' AND b.batch_plan_id='' AND b.session_id=child.session_id AND b.workspace_id=mission.workspace_id
 AND b.worker_profile_json=child.worker_profile_json AND child.worker_profile_json<>'null'
 AND (json_type(child.worker_profile_json,'$.tools')='null' OR EXISTS(SELECT 1 FROM json_each(child.worker_profile_json,'$.tools') tool WHERE tool.value=call.name))
 AND call.name IN ('workspace_change','workspace_delete') AND (call.name<>'workspace_delete' OR json_extract(call.payload_json,'$.action')='propose')
 AND call.status='pending' AND call.started_at IS NOT NULL AND call.charge_id<>''
 AND json_extract(call.payload_json,'$.path')=NEW.path AND json_extract(call.payload_json,'$.expected_sha256')=NEW.original_hash
 AND json_extract(call.authority_json,'$.worker.agent_id')=child.id AND json_extract(call.authority_json,'$.worker.attempt_id')=attempt.id
 AND json_extract(call.authority_json,'$.permission_snapshot_id')=NEW.permission_snapshot_id
 AND json_extract(call.authority_json,'$.permission_revision')=NEW.permission_revision)`
	auto = replaceCommandRuntimeMigrationFragment(auto, "run.id=NEW.run_id AND run.session_id=NEW.session_id", "run.id=NEW.run_id AND (run.session_id=NEW.session_id OR ("+worker+"))")
	auto = replaceCommandRuntimeMigrationFragment(auto, "session_record.id=run.session_id", "session_record.id=NEW.session_id")
	auto = replaceCommandRuntimeMigrationFragment(auto, "agent.session_id=run.session_id AND agent.role='root'\n\t\t\t\tAND agent.parent_id IS NULL AND agent.depth=0", "((agent.session_id=run.session_id AND agent.role='root' AND agent.parent_id IS NULL AND agent.depth=0) OR (agent.session_id=NEW.session_id AND agent.role='specialist' AND ("+worker+")))")
	apply := requireMigrationTrigger("trg_file_edit_apply_operation_insert", threadDrydockRuntimeScopeStatements)
	applyWorker := `EXISTS(SELECT 1 FROM agent_nodes child JOIN agent_attempts attempt ON attempt.id=child.active_attempt_id
 JOIN specialist_tool_bindings b ON b.agent_attempt_id=attempt.id JOIN specialist_tool_calls call ON call.agent_attempt_id=attempt.id
 JOIN run_execution_leases lease ON lease.run_id=child.run_id
 WHERE child.run_id=run.id AND child.id=NEW.applied_by AND child.session_id=NEW.session_id AND child.role='specialist'
 AND child.status='running' AND attempt.status='running' AND attempt.agent_id=child.id AND attempt.run_id=run.id
 AND attempt.lease_id=lease.lease_id AND attempt.lease_generation=lease.generation AND lease.status='active' AND julianday(lease.expires_at)>julianday('now')
 AND b.mode='worker' AND b.batch_plan_id='' AND b.session_id=child.session_id AND b.workspace_id=mission.workspace_id
 AND b.worker_profile_json=child.worker_profile_json AND child.worker_profile_json<>'null'
 AND call.name IN ('workspace_apply','workspace_delete') AND call.status='pending' AND call.started_at IS NOT NULL AND call.charge_id<>''
 AND (json_type(child.worker_profile_json,'$.tools')='null' OR EXISTS(SELECT 1 FROM json_each(child.worker_profile_json,'$.tools') tool WHERE tool.value=call.name))
 AND json_extract(call.payload_json,'$.edit_id')=NEW.edit_id
 AND json_extract(call.authority_json,'$.worker.agent_id')=child.id AND json_extract(call.authority_json,'$.worker.attempt_id')=attempt.id)`
	apply = replaceCommandRuntimeMigrationFragment(apply, "session_record.id = run.session_id", "session_record.id = NEW.session_id")
	apply = replaceCommandRuntimeMigrationFragment(apply, "run.id = NEW.run_id AND run.session_id = NEW.session_id", "run.id = NEW.run_id AND (run.session_id = NEW.session_id OR ("+applyWorker+"))")
	base := specialistWorkerStatementsWithoutFileEdits()
	start := requireMigrationTrigger("trg_specialist_tool_start", base)
	start = strings.Replace(start, "p.proposal_id=NEW.approval_source_id", `(p.proposal_id=NEW.approval_source_id OR (NEW.name IN ('workspace_apply','workspace_delete') AND p.proposal_id=json_extract(NEW.payload_json,'$.edit_id') AND p.action_class='workspace_write'))`, 1)
	resume := requireMigrationTrigger("trg_agent_attempt_identity_immutable", base)
	resume = strings.Replace(resume, "p.proposal_id=c.approval_source_id", `(p.proposal_id=c.approval_source_id OR (c.name IN ('workspace_apply','workspace_delete') AND p.proposal_id=json_extract(c.payload_json,'$.edit_id') AND p.action_class='workspace_write'))`, 1)
	return []string{`DROP TRIGGER trg_file_edit_auto_authorization_insert`, auto, `DROP TRIGGER trg_file_edit_apply_operation_insert`, apply, `DROP TRIGGER trg_specialist_tool_start`, start, `DROP TRIGGER trg_agent_attempt_identity_immutable`, resume}
}
