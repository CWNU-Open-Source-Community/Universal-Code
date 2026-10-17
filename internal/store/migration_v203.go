package store

import "strings"

var workflowStatements = append([]string{
	`CREATE TABLE agent_graph_snapshots_v203 (id TEXT PRIMARY KEY,run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,version INTEGER NOT NULL CHECK(version>0),protocol_version TEXT NOT NULL CHECK(protocol_version='agent_graph.v1'),root_agent_id TEXT NOT NULL,node_count INTEGER NOT NULL CHECK(node_count BETWEEN 1 AND 257),pending_message_count INTEGER NOT NULL CHECK(pending_message_count BETWEEN 0 AND 32896),state_json TEXT NOT NULL,created_at TEXT NOT NULL,FOREIGN KEY(run_id,root_agent_id) REFERENCES agent_nodes(run_id,id) ON DELETE CASCADE,UNIQUE(run_id,version));`,
	`INSERT INTO agent_graph_snapshots_v203 SELECT * FROM agent_graph_snapshots;`,
	`DROP TABLE agent_graph_snapshots;`,
	`ALTER TABLE agent_graph_snapshots_v203 RENAME TO agent_graph_snapshots;`,
	`CREATE INDEX idx_agent_graph_snapshots_latest ON agent_graph_snapshots(run_id,version DESC);`,
	`DROP TRIGGER trg_agent_child_depth;`,
	`CREATE TRIGGER trg_agent_child_depth BEFORE INSERT ON agent_nodes WHEN NEW.parent_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM agent_nodes parent WHERE parent.run_id=NEW.run_id AND parent.id=NEW.parent_id AND NEW.depth=parent.depth+1 AND parent.child_limit>(SELECT COUNT(*) FROM agent_nodes child WHERE child.run_id=NEW.run_id AND child.parent_id=parent.id AND child.status NOT IN ('completed','failed','cancelled'))) BEGIN SELECT RAISE(ABORT, 'agent child depth or active limit is invalid'); END;`,
	`DROP TRIGGER trg_agent_node_limit;`,
	`CREATE TRIGGER trg_agent_node_limit BEFORE INSERT ON agent_nodes WHEN (SELECT COUNT(*) FROM agent_nodes WHERE run_id=NEW.run_id)>=257 BEGIN SELECT RAISE(ABORT, 'agent history limit reached'); END;`,
	`CREATE TABLE workflow_definitions (id TEXT PRIMARY KEY, digest TEXT NOT NULL UNIQUE, spec_json TEXT NOT NULL CHECK(json_valid(spec_json)), created_at TEXT NOT NULL);`,
	`CREATE TRIGGER workflow_definition_immutable BEFORE UPDATE ON workflow_definitions BEGIN SELECT RAISE(ABORT, 'workflow definitions are immutable'); END;`,
	`CREATE TABLE workflow_executions (id TEXT PRIMARY KEY, definition_id TEXT NOT NULL REFERENCES workflow_definitions(id), run_id TEXT NOT NULL REFERENCES runs(id), inputs_json TEXT NOT NULL CHECK(json_valid(inputs_json)), status TEXT NOT NULL CHECK(status IN ('running','waiting','waiting_approval','paused','completed','failed')), reason TEXT NOT NULL DEFAULT '', state_json TEXT NOT NULL CHECK(json_valid(state_json)), usage_before_json TEXT NOT NULL CHECK(json_valid(usage_before_json)), version INTEGER NOT NULL DEFAULT 1, claim_owner TEXT NOT NULL DEFAULT '', claim_until TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL);`,
	`CREATE INDEX workflow_run ON workflow_executions(run_id, created_at);`,
	`ALTER TABLE workflow_executions ADD COLUMN turns_before INTEGER NOT NULL DEFAULT 0;`,
	`CREATE TABLE workflow_steps (id TEXT PRIMARY KEY, execution_id TEXT NOT NULL REFERENCES workflow_executions(id), step_id TEXT NOT NULL, agent_id TEXT NOT NULL, session_id TEXT NOT NULL, inputs_json TEXT NOT NULL CHECK(json_valid(inputs_json)), status TEXT NOT NULL CHECK(status IN ('pending','completed','failed')), output_json TEXT NOT NULL DEFAULT 'null' CHECK(json_valid(output_json)), reason TEXT NOT NULL DEFAULT '');`,
	`CREATE INDEX workflow_step_execution ON workflow_steps(execution_id);`,
}, workflowDelegationCapacityStatements()...)

// Preserve the existing authorization predicates while separating live reservations
// from immutable job history in both legacy delegation triggers.
func workflowDelegationCapacityStatements() []string {
	proposal := requireMigrationTrigger("trg_specialist_delegation_operation_insert", specialistDelegationProposalStatements)
	proposal = strings.Replace(proposal, "child.parent_id = root.id)", "child.parent_id = root.id AND child.status NOT IN ('completed','failed','cancelled'))", 1)
	application := requireMigrationTrigger("trg_specialist_delegation_application_insert", specialistDelegationApplicationStatements)
	application = strings.Replace(application, "child.parent_id = NEW.root_agent_id)", "child.parent_id = NEW.root_agent_id AND child.status NOT IN ('completed','failed','cancelled'))", 1)
	application = strings.Replace(application, "SUM(child.turn_limit)", "SUM(CASE WHEN child.status IN ('completed','failed','cancelled') THEN child.turns_used ELSE MAX(child.turn_limit,child.turns_used) END)", 1)
	application = strings.Replace(application, "SUM(child.token_limit)", "SUM(CASE WHEN child.status IN ('completed','failed','cancelled') THEN child.tokens_used ELSE MAX(child.token_limit,child.tokens_used) END)", 1)
	return []string{"DROP TRIGGER trg_specialist_delegation_operation_insert", proposal, "DROP TRIGGER trg_specialist_delegation_application_insert", application}
}
