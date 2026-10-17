package store

import "strings"

func specialistWorkerStatements() []string {
	statements := specialistWorkerStatementsWithoutFileEdits()
	statements = append(statements, specialistCommandRuntimeScopeStatements()...)
	return append(statements, specialistFileEditStatements()...)
}

func specialistWorkerStatementsWithoutFileEdits() []string {
	legacy := specialistToolLoopStatements()
	schema := requireMigrationStatement("CREATE TABLE specialist_tool_bindings (", legacy)
	schema = strings.Replace(schema, "CREATE TABLE specialist_tool_bindings (", "CREATE TABLE specialist_tool_bindings_v202 (", 1)
	schema = strings.Replace(schema, "CHECK(mode IN ('read','batch'))", "CHECK(mode IN ('read','batch','worker'))", 1)
	schema = strings.Replace(schema, "created_at TEXT NOT NULL,", "created_at TEXT NOT NULL,allowed_read_paths_json TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(allowed_read_paths_json)),worker_profile_json TEXT NOT NULL DEFAULT 'null' CHECK(json_valid(worker_profile_json)),", 1)
	schema = strings.Replace(schema, "OR (mode='batch' AND length(batch_plan_id)>0 AND batch_ordinal>0 AND batch_generation>0))", "OR (mode='batch' AND length(batch_plan_id)>0 AND batch_ordinal>0 AND batch_generation>0) OR (mode='worker' AND ((batch_plan_id='' AND batch_ordinal=0 AND batch_generation=0) OR (length(batch_plan_id)>0 AND batch_ordinal>0 AND batch_generation>0))))", 1)
	immutable := requireMigrationTrigger("trg_specialist_tool_call_immutable", legacy)
	immutable = strings.Replace(immutable, "OR (OLD.charge_id<>'' AND NEW.charge_id<>OLD.charge_id)", `OR (OLD.charge_id<>'' AND NEW.charge_id<>OLD.charge_id)
    OR (OLD.authority_json<>'' AND NEW.authority_json<>OLD.authority_json)
    OR (OLD.approval_source_id<>'' AND NEW.approval_source_id<>OLD.approval_source_id)
    OR (OLD.approval_id<>'' AND NEW.approval_id<>OLD.approval_id)
    OR (OLD.started_at IS NOT NULL AND NEW.started_at IS NOT OLD.started_at)`, 1)
	statements := []string{
		`ALTER TABLE agent_nodes ADD COLUMN worker_profile_json TEXT NOT NULL DEFAULT 'null' CHECK(json_valid(worker_profile_json))`,
		`ALTER TABLE specialist_delegation_assignments ADD COLUMN allowed_read_paths_json TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(allowed_read_paths_json))`,
		`ALTER TABLE specialist_delegation_assignments ADD COLUMN tool_call_limit INTEGER NOT NULL DEFAULT 0 CHECK(tool_call_limit>=0)`,
		`ALTER TABLE specialist_delegation_assignments ADD COLUMN cost_limit_micros INTEGER NOT NULL DEFAULT 0 CHECK(cost_limit_micros>=0)`,
		`ALTER TABLE specialist_delegation_assignments ADD COLUMN worker_profile_json TEXT NOT NULL DEFAULT 'null' CHECK(json_valid(worker_profile_json))`,
		`PRAGMA legacy_alter_table=ON;`, schema,
		`INSERT INTO specialist_tool_bindings_v202 (agent_attempt_id,run_id,agent_id,session_id,workspace_id,root_fingerprint,brief_fingerprint,mode,batch_plan_id,batch_ordinal,batch_generation,created_at,allowed_read_paths_json) SELECT agent_attempt_id,run_id,agent_id,session_id,workspace_id,root_fingerprint,brief_fingerprint,mode,batch_plan_id,batch_ordinal,batch_generation,created_at,allowed_read_paths_json FROM specialist_tool_bindings`,
		`DROP TABLE specialist_tool_bindings`,
		`ALTER TABLE specialist_tool_bindings_v202 RENAME TO specialist_tool_bindings`,
		requireMigrationTrigger("trg_specialist_tool_binding_insert", legacy),
		requireMigrationTrigger("trg_specialist_tool_binding_immutable", legacy),
		`ALTER TABLE specialist_tool_calls ADD COLUMN authority_json TEXT NOT NULL DEFAULT '' CHECK(authority_json='' OR json_valid(authority_json))`,
		`ALTER TABLE specialist_tool_calls ADD COLUMN approval_source_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE specialist_tool_calls ADD COLUMN approval_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE specialist_tool_calls ADD COLUMN started_at TEXT`,
		`CREATE UNIQUE INDEX specialist_tool_approval_source ON specialist_tool_calls(approval_source_id) WHERE approval_source_id<>''`,
		`DROP TRIGGER trg_specialist_tool_call_immutable`, immutable,
		`CREATE TRIGGER trg_specialist_tool_start BEFORE UPDATE OF started_at ON specialist_tool_calls
 WHEN OLD.started_at IS NULL AND NEW.started_at IS NOT NULL AND
 (NEW.status<>'pending' OR NEW.charge_id='' OR NEW.authority_json='' OR
  (NEW.approval_id<>'' AND NOT EXISTS(SELECT 1 FROM tool_approvals p WHERE p.id=NEW.approval_id AND p.proposal_id=NEW.approval_source_id AND p.status='approved')))
 BEGIN SELECT RAISE(ABORT,'child tool start requires charged authority and approved review');END`,
		`DROP TRIGGER trg_agent_attempt_identity_immutable`,
		`CREATE TRIGGER trg_agent_attempt_identity_immutable BEFORE UPDATE OF id,run_id,agent_id,parent_agent_id,lease_id,lease_generation,turn_number,started_at ON agent_attempts
 WHEN NEW.id<>OLD.id OR NEW.run_id<>OLD.run_id OR NEW.agent_id<>OLD.agent_id OR NEW.parent_agent_id<>OLD.parent_agent_id OR NEW.turn_number<>OLD.turn_number OR NEW.started_at<>OLD.started_at
 OR ((NEW.lease_id<>OLD.lease_id OR NEW.lease_generation<>OLD.lease_generation) AND NOT
  (OLD.status='running' AND NEW.status='running' AND NEW.lease_generation>OLD.lease_generation
   AND EXISTS(SELECT 1 FROM specialist_tool_calls c JOIN tool_approvals p ON p.id=c.approval_id
    WHERE c.agent_attempt_id=OLD.id AND c.status='pending' AND c.started_at IS NULL AND p.status IN ('approved','denied') AND p.proposal_id=c.approval_source_id)
   AND EXISTS(SELECT 1 FROM run_execution_leases l WHERE l.run_id=NEW.run_id AND l.lease_id=NEW.lease_id AND l.generation=NEW.lease_generation AND l.status='active' AND julianday(l.expires_at)>julianday('now'))))
 BEGIN SELECT RAISE(ABORT,'Agent attempt identity is immutable outside approval resume');END`,
		`PRAGMA legacy_alter_table=OFF;`,
	}
	return statements
}
