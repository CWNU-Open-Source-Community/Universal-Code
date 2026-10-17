package store

import "strings"

func threadContextCompactionStatements() []string {
	// Keep the existing reservation constraints and child attribution intact.
	create := requireMigrationStatement("CREATE TABLE run_monetary_reservations (", monetaryBudgetStatements)
	create = strings.Replace(create, "CREATE TABLE run_monetary_reservations (", "CREATE TABLE run_monetary_reservations_v208 (", 1)
	create = strings.Replace(create, "'readonly_fanout'))", "'readonly_fanout', 'context_compaction'))", 1)
	create = strings.Replace(create, "id TEXT PRIMARY KEY,", "id TEXT PRIMARY KEY, agent_id TEXT NOT NULL DEFAULT '',", 1)
	return []string{
		`CREATE TABLE thread_context_compactions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			operation_id TEXT NOT NULL UNIQUE,
			thread_id TEXT NOT NULL REFERENCES threads(id),
			run_id TEXT NOT NULL REFERENCES runs(id),
			session_id TEXT NOT NULL REFERENCES sessions(id),
			key_digest TEXT NOT NULL,
			request_fingerprint TEXT NOT NULL,
			source_sha256 TEXT NOT NULL,
            input_fingerprint TEXT NOT NULL DEFAULT '',
            input_messages_omitted INTEGER NOT NULL DEFAULT 0 CHECK(input_messages_omitted>=0),
			status TEXT NOT NULL CHECK(status IN ('prepared','started','generated','completed','noop','failed')),
			provider TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '',
			response_text TEXT NOT NULL DEFAULT '', usage_json TEXT NOT NULL DEFAULT 'null' CHECK(json_valid(usage_json)),
			reserved_tokens INTEGER NOT NULL DEFAULT 0 CHECK(reserved_tokens>=0),
			elapsed_millis INTEGER NOT NULL DEFAULT 0 CHECK(elapsed_millis>=0),
			completion_sequence INTEGER NOT NULL DEFAULT 0,
			result_json TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(result_json)),
			error_code TEXT NOT NULL DEFAULT '', error_message TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL, started_at TEXT, terminal_at TEXT,
			UNIQUE(thread_id,key_digest))`,
		`CREATE UNIQUE INDEX thread_context_compaction_active ON thread_context_compactions(thread_id)
			WHERE status IN ('prepared','started','generated')`,
		create,
		`INSERT INTO run_monetary_reservations_v208 (id,run_id,scope,provider,model,attempt_number,reserved_micros,settled_micros,released_micros,status,estimate_source,price_fingerprint,created_at,settled_at,released_at,agent_id)
			SELECT id,run_id,scope,provider,model,attempt_number,reserved_micros,settled_micros,released_micros,status,estimate_source,price_fingerprint,created_at,settled_at,released_at,agent_id FROM run_monetary_reservations`,
		`DROP TABLE run_monetary_reservations`,
		`ALTER TABLE run_monetary_reservations_v208 RENAME TO run_monetary_reservations`,
		`CREATE INDEX monetary_reservations_child ON run_monetary_reservations(run_id,agent_id)`,
	}
}
