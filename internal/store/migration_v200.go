package store

var specialistReadLimitsStatements = []string{
	`ALTER TABLE agent_nodes ADD COLUMN allowed_read_paths_json TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(allowed_read_paths_json))`,
	`ALTER TABLE agent_nodes ADD COLUMN tool_call_limit INTEGER NOT NULL DEFAULT 0 CHECK(tool_call_limit>=0)`,
	`ALTER TABLE agent_nodes ADD COLUMN cost_limit_micros INTEGER NOT NULL DEFAULT 0 CHECK(cost_limit_micros>=0)`,
	`ALTER TABLE specialist_tool_bindings ADD COLUMN allowed_read_paths_json TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(allowed_read_paths_json))`,
	`ALTER TABLE run_monetary_reservations ADD COLUMN agent_id TEXT NOT NULL DEFAULT ''`,
	`UPDATE run_monetary_reservations SET agent_id=COALESCE((SELECT a.agent_id FROM run_events e JOIN agent_attempts a ON a.id=json_extract(e.payload_json,'$.agent_attempt_id') WHERE e.run_id=run_monetary_reservations.run_id AND a.run_id=e.run_id AND e.source='specialist_model_gateway' AND e.type='model.started' AND json_extract(e.payload_json,'$.monetary_attempt_number')=run_monetary_reservations.attempt_number LIMIT 1),'') WHERE scope='specialist'`,
	`CREATE INDEX monetary_reservations_child ON run_monetary_reservations(run_id,agent_id)`,
}
