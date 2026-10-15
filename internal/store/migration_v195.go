package store

// Each row is a child of the existing immutable Thread message intent. A start
// is durable before any server connection, so restart never repeats a read.
var threadMCPReferenceStatements = []string{
	`CREATE TABLE thread_mcp_references (
		intent_digest TEXT NOT NULL REFERENCES thread_message_intents(operation_key_digest) ON DELETE RESTRICT,
		position INTEGER NOT NULL CHECK(position BETWEEN 1 AND 4),
		operation_id TEXT NOT NULL UNIQUE,
		reference_json TEXT NOT NULL CHECK(json_valid(reference_json) AND length(CAST(reference_json AS BLOB))<=20480),
		requested_by TEXT NOT NULL,
		state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','started','completed')),
		receipt TEXT NOT NULL DEFAULT '' CHECK(receipt IN ('','not_dispatched','result_received','outcome_unknown')),
		result_json TEXT NOT NULL DEFAULT '' CHECK(result_json='' OR (json_valid(result_json) AND length(CAST(result_json AS BLOB))<=393216)),
		evidence_message_id INTEGER REFERENCES session_messages(id) ON DELETE RESTRICT,
		PRIMARY KEY(intent_digest,position),
		CHECK((state IN ('pending','started') AND receipt='' AND result_json='' AND evidence_message_id IS NULL)
		 OR (state='completed' AND receipt!='' AND result_json!='' AND evidence_message_id IS NOT NULL))
	) WITHOUT ROWID;`,
	`CREATE TRIGGER trg_thread_mcp_reference_insert BEFORE INSERT ON thread_mcp_references
	 WHEN NEW.state!='pending' OR NEW.receipt!='' OR NEW.result_json!='' OR NEW.evidence_message_id IS NOT NULL
	 BEGIN SELECT RAISE(ABORT,'MCP reference must begin as an unconsumed message selection'); END;`,
	`CREATE TRIGGER trg_thread_mcp_reference_update BEFORE UPDATE ON thread_mcp_references
	 WHEN NEW.intent_digest!=OLD.intent_digest OR NEW.position!=OLD.position OR NEW.operation_id!=OLD.operation_id
	 OR NEW.reference_json!=OLD.reference_json OR NEW.requested_by!=OLD.requested_by
	 OR NOT ((OLD.state='pending' AND NEW.state='started') OR (OLD.state='started' AND NEW.state='completed'))
	 OR (NEW.state='completed' AND NOT EXISTS (SELECT 1 FROM session_messages evidence
	  JOIN thread_message_intents intent ON intent.operation_key_digest=NEW.intent_digest
	  JOIN runs run ON run.id=intent.run_id
	  WHERE evidence.id=NEW.evidence_message_id AND evidence.session_id=run.session_id
	   AND evidence.source_kind='tool_result' AND evidence.source_ref=NEW.operation_id AND evidence.instruction_authorized=0))
	 BEGIN SELECT RAISE(ABORT,'MCP reference selection and completed result are immutable'); END;`,
	`CREATE TRIGGER trg_thread_mcp_reference_delete BEFORE DELETE ON thread_mcp_references
	 BEGIN SELECT RAISE(ABORT,'MCP reference receipts are retained'); END;`,
}
