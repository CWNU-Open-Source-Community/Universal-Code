package store

import "strings"

func specialistToolLoopStatements() []string {
	var schema string
	for _, statement := range specialistProtocolRepairStatements {
		if strings.HasPrefix(statement, "CREATE TABLE specialist_model_calls (") {
			schema = statement
			break
		}
	}
	if schema == "" {
		panic("Specialist tool migration lost model schema")
	}
	schema = strings.Replace(schema, "CREATE TABLE specialist_model_calls (", "CREATE TABLE specialist_model_calls_v190 (", 1)
	schema = strings.Replace(schema, "protocol_repair INTEGER NOT NULL DEFAULT 0,", "protocol_repair INTEGER NOT NULL DEFAULT 0,\n tool_round INTEGER NOT NULL DEFAULT 0 CHECK(tool_round BETWEEN 0 AND 16),", 1)
	schema = strings.Replace(schema, "action_kind IN ('continue', 'finish')", "action_kind IN ('continue', 'finish', 'tool_calls')", 1)
	schema = strings.Replace(schema, "action_kind = 'continue' AND report_outcome = ''", "action_kind IN ('continue', 'tool_calls') AND report_outcome = ''", 1)
	result := []string{`PRAGMA legacy_alter_table=ON;`, schema,
		`INSERT INTO specialist_model_calls_v190 SELECT *,0 FROM specialist_model_calls;`,
	}
	// tool_round is inserted beside protocol_repair, so name the existing columns
	// explicitly instead of relying on the old physical column order.
	result[2] = `INSERT INTO specialist_model_calls_v190
 (rowid,agent_attempt_id,run_id,agent_id,model_attempt_number,transport_attempt,max_attempts,protocol_repair,
 provider,model,input_fingerprint,action_fingerprint,status,outcome,error_text,retry_after_millis,retry_planned,
 elapsed_millis,stream_events,stream_bytes,input_tokens,output_tokens,total_tokens,usage_recorded,action_kind,
 report_outcome,policy_allowed,policy_needs_approval,policy_risk,policy_reason,user_message_id,assistant_message_id,started_at,finished_at)
 SELECT rowid,* FROM specialist_model_calls;`
	result = append(result, `DROP TABLE specialist_model_calls;`, `ALTER TABLE specialist_model_calls_v190 RENAME TO specialist_model_calls;`)
	result = append(result,
		`CREATE TABLE specialist_tool_bindings (
   agent_attempt_id TEXT PRIMARY KEY REFERENCES agent_attempts(id) ON DELETE CASCADE,
   run_id TEXT NOT NULL,agent_id TEXT NOT NULL,session_id TEXT NOT NULL REFERENCES sessions(id),
   workspace_id TEXT NOT NULL,root_fingerprint TEXT NOT NULL CHECK(length(root_fingerprint)=64),
   brief_fingerprint TEXT NOT NULL CHECK(length(brief_fingerprint)=64),mode TEXT NOT NULL CHECK(mode IN ('read','batch')),
   batch_plan_id TEXT NOT NULL DEFAULT '',batch_ordinal INTEGER NOT NULL DEFAULT 0,batch_generation INTEGER NOT NULL DEFAULT 0,
   created_at TEXT NOT NULL,
   FOREIGN KEY(run_id,agent_id) REFERENCES agent_nodes(run_id,id),
   CHECK((mode='read' AND batch_plan_id='' AND batch_ordinal=0 AND batch_generation=0)
    OR (mode='batch' AND length(batch_plan_id)>0 AND batch_ordinal>0 AND batch_generation>0))
  );`,
		`CREATE TRIGGER trg_specialist_tool_binding_insert BEFORE INSERT ON specialist_tool_bindings
   WHEN NOT EXISTS(SELECT 1 FROM agent_attempts a JOIN agent_nodes c ON c.id=a.agent_id
    JOIN specialist_task_briefs b ON b.agent_attempt_id=a.id JOIN run_execution_leases l ON l.run_id=a.run_id
    WHERE a.id=NEW.agent_attempt_id AND a.run_id=NEW.run_id AND a.agent_id=NEW.agent_id
     AND c.session_id=NEW.session_id AND c.role='specialist' AND c.status='running' AND c.active_attempt_id=a.id
     AND a.status='running' AND a.usage_recorded_at IS NULL AND b.fingerprint=NEW.brief_fingerprint
     AND l.lease_id=a.lease_id AND l.generation=a.lease_generation AND l.status='active' AND julianday(l.expires_at)>julianday('now'))
   BEGIN SELECT RAISE(ABORT,'Specialist tool binding requires its active prepared attempt'); END;`,
		`CREATE TRIGGER trg_specialist_tool_binding_immutable BEFORE UPDATE ON specialist_tool_bindings
   BEGIN SELECT RAISE(ABORT,'Specialist tool binding is immutable'); END;`,
		`CREATE TABLE specialist_tool_rounds (
   agent_attempt_id TEXT NOT NULL REFERENCES specialist_tool_bindings(agent_attempt_id) ON DELETE CASCADE,
   round INTEGER NOT NULL CHECK(round BETWEEN 1 AND 16),model_attempt INTEGER NOT NULL,
   assistant_text TEXT NOT NULL,provider_replay BLOB,created_at TEXT NOT NULL,
   PRIMARY KEY(agent_attempt_id,round),UNIQUE(agent_attempt_id,model_attempt),
   FOREIGN KEY(agent_attempt_id,model_attempt) REFERENCES specialist_model_calls(agent_attempt_id,model_attempt_number)
  );`,
		`CREATE TABLE specialist_tool_calls (
   agent_attempt_id TEXT NOT NULL,round INTEGER NOT NULL,position INTEGER NOT NULL CHECK(position BETWEEN 1 AND 4),
   call_id TEXT NOT NULL,name TEXT NOT NULL,payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),
   status TEXT NOT NULL CHECK(status IN ('pending','completed','denied','failed')),
   result_json TEXT NOT NULL DEFAULT '',error_code TEXT NOT NULL DEFAULT '',charge_id TEXT NOT NULL DEFAULT '',
   created_at TEXT NOT NULL,completed_at TEXT,
   PRIMARY KEY(agent_attempt_id,round,position),UNIQUE(agent_attempt_id,call_id),
   FOREIGN KEY(agent_attempt_id,round) REFERENCES specialist_tool_rounds(agent_attempt_id,round) ON DELETE CASCADE,
   CHECK((status='pending' AND result_json='' AND error_code='' AND completed_at IS NULL)
    OR (status='completed' AND json_valid(result_json) AND error_code='' AND completed_at IS NOT NULL)
    OR (status IN ('denied','failed') AND json_valid(result_json) AND length(error_code)>0 AND completed_at IS NOT NULL))
  );`,
		`CREATE TRIGGER trg_specialist_tool_round_immutable BEFORE UPDATE ON specialist_tool_rounds
   BEGIN SELECT RAISE(ABORT,'Specialist tool round is immutable'); END;`,
		`CREATE TRIGGER trg_specialist_tool_call_immutable BEFORE UPDATE ON specialist_tool_calls
   WHEN OLD.status<>'pending' OR NEW.agent_attempt_id<>OLD.agent_attempt_id OR NEW.round<>OLD.round
		    OR NEW.position<>OLD.position OR NEW.call_id<>OLD.call_id OR NEW.name<>OLD.name OR NEW.payload_json<>OLD.payload_json OR NEW.created_at<>OLD.created_at
		    OR (OLD.charge_id<>'' AND NEW.charge_id<>OLD.charge_id)
   BEGIN SELECT RAISE(ABORT,'Specialist tool receipt is immutable'); END;`,
		`CREATE TRIGGER trg_specialist_tool_attempt_terminal BEFORE UPDATE OF status ON agent_attempts
   WHEN NEW.status IN ('continued','finished') AND EXISTS(SELECT 1 FROM specialist_tool_calls WHERE agent_attempt_id=OLD.id AND status='pending')
   BEGIN SELECT RAISE(ABORT,'Specialist attempt still has pending tools'); END;`,
		`CREATE TRIGGER trg_specialist_tool_round_insert BEFORE INSERT ON specialist_tool_rounds
		 WHEN NEW.round<>(SELECT count(*)+1 FROM specialist_tool_rounds WHERE agent_attempt_id=NEW.agent_attempt_id)
		 OR EXISTS(SELECT 1 FROM specialist_tool_calls WHERE agent_attempt_id=NEW.agent_attempt_id AND status='pending')
		 OR NOT EXISTS(SELECT 1 FROM specialist_model_calls m JOIN agent_attempts a ON a.id=m.agent_attempt_id
		  JOIN agent_nodes c ON c.id=a.agent_id JOIN runs r ON r.id=a.run_id JOIN run_execution_leases l ON l.run_id=a.run_id
		  WHERE m.agent_attempt_id=NEW.agent_attempt_id AND m.model_attempt_number=NEW.model_attempt AND m.tool_round=NEW.round-1
		   AND m.status='completed' AND m.action_kind='tool_calls' AND a.status='running' AND r.status='running'
		   AND c.status='running' AND c.active_attempt_id=a.id AND l.lease_id=a.lease_id AND l.generation=a.lease_generation
		   AND l.status='active' AND julianday(l.expires_at)>julianday('now'))
		 BEGIN SELECT RAISE(ABORT,'child tool round requires its completed active leased model response');END;`,
		`CREATE TRIGGER trg_specialist_tool_call_terminal_lease BEFORE UPDATE OF status ON specialist_tool_calls
		 WHEN OLD.status='pending' AND NEW.status<>'pending' AND NOT EXISTS(
		 SELECT 1 FROM agent_attempts a JOIN agent_nodes c ON c.id=a.agent_id JOIN runs r ON r.id=a.run_id JOIN run_execution_leases l ON l.run_id=a.run_id
		 WHERE a.id=OLD.agent_attempt_id AND a.status='running' AND c.status='running' AND c.active_attempt_id=a.id AND r.status='running'
		 AND l.lease_id=a.lease_id AND l.generation=a.lease_generation AND l.status='active' AND julianday(l.expires_at)>julianday('now'))
		 BEGIN SELECT RAISE(ABORT,'child tool receipt requires its active leased attempt');END;`,
	)
	for _, statement := range specialistProtocolRepairStatements {
		if strings.HasPrefix(statement, "CREATE UNIQUE INDEX idx_specialist_model_one_started") || strings.HasPrefix(statement, "CREATE INDEX idx_specialist_model_agent_started") || strings.HasPrefix(statement, "CREATE TRIGGER trg_specialist_model_call_") {
			if strings.Contains(statement, "trg_specialist_model_call_phase_sequence") {
				statement = strings.Replace(statement, "AND protocol_repair = NEW.protocol_repair", "AND protocol_repair = NEW.protocol_repair AND tool_round = NEW.tool_round", 1)
			}
			if strings.Contains(statement, "trg_specialist_model_call_insert") {
				statement = strings.Replace(statement, "NEW.protocol_repair = 0 AND attempt.usage_recorded_at IS NULL", "NEW.protocol_repair = 0 AND (attempt.usage_recorded_at IS NULL OR EXISTS(SELECT 1 FROM specialist_tool_bindings tb WHERE tb.agent_attempt_id=attempt.id))", 1)
			}
			if strings.Contains(statement, "trg_specialist_model_call_identity_immutable") {
				statement = strings.Replace(statement, "transport_attempt, max_attempts, protocol_repair", "transport_attempt, max_attempts, protocol_repair, tool_round", 1)
			}
			result = append(result, statement)
		}
	}
	return append(result, `PRAGMA legacy_alter_table=OFF;`)
}
