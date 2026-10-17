package store

import "strings"

var threadContextPinStatements = append([]string{
	`CREATE TABLE thread_context_pin_sets (thread_id TEXT PRIMARY KEY REFERENCES threads(id) ON DELETE CASCADE,revision INTEGER NOT NULL CHECK(revision>=0));`,
	`CREATE TABLE thread_context_pins (id TEXT PRIMARY KEY,thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,version INTEGER NOT NULL CHECK(version>0),status TEXT NOT NULL CHECK(status IN ('active','removed')),payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),UNIQUE(thread_id,id));`,
	`CREATE INDEX thread_context_pins_active ON thread_context_pins(thread_id,status,id);`,
	`CREATE TABLE thread_context_pin_revisions (pin_id TEXT NOT NULL REFERENCES thread_context_pins(id) ON DELETE CASCADE,version INTEGER NOT NULL CHECK(version>0),payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),PRIMARY KEY(pin_id,version));`,
	`CREATE TRIGGER thread_context_pin_revision_immutable BEFORE UPDATE ON thread_context_pin_revisions BEGIN SELECT RAISE(ABORT,'context pin revisions are immutable');END;`,
	`CREATE TABLE thread_context_pin_proposals (id TEXT PRIMARY KEY,thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,version INTEGER NOT NULL CHECK(version>0),status TEXT NOT NULL CHECK(status IN ('pending','resolved','dismissed')),operation_key_digest TEXT NOT NULL UNIQUE,request_fingerprint TEXT NOT NULL,invocation_id TEXT NOT NULL UNIQUE REFERENCES run_tool_calls(id),payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),created_at TEXT NOT NULL);`,
	`CREATE INDEX thread_context_pin_proposals_pending ON thread_context_pin_proposals(thread_id,status,created_at);`,
	`CREATE TABLE thread_context_pin_operations (thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,operation_key_digest TEXT NOT NULL,request_fingerprint TEXT NOT NULL,result_json TEXT NOT NULL CHECK(json_valid(result_json)),PRIMARY KEY(thread_id,operation_key_digest));`,
}, threadContextPinLedgerStatements()...)

func threadContextPinLedgerStatements() []string {
	// v202-v206 do not alter this ledger. Preserve the v201 rowids, call bytes,
	// attribution and authority guards; extend only the proposal name allowlist.
	previous := projectTestToolStatements()[:len(mcpToolSearchStatements)]
	result := make([]string, len(previous))
	for i, statement := range previous {
		statement = strings.ReplaceAll(statement, "run_supervisor_tool_calls_v201", "run_supervisor_tool_calls_v207")
		statement = strings.ReplaceAll(statement, "run_supervisor_tool_calls_v200", "run_supervisor_tool_calls_v206")
		result[i] = strings.ReplaceAll(statement, "'child_task_propose'", "'child_task_propose', 'context_pin_propose'")
	}
	return result
}
