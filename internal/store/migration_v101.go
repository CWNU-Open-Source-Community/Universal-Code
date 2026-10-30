package store

var dependencyWaitStatements = []string{
	`CREATE TABLE agent_dependency_edges (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		source_kind TEXT NOT NULL,
		source_id TEXT NOT NULL,
		target_kind TEXT NOT NULL,
		target_id TEXT NOT NULL,
		reason TEXT NOT NULL,
		state TEXT NOT NULL,
		failure_policy TEXT NOT NULL,
		generation INTEGER NOT NULL,
		deadline TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		resolved_at TEXT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(source_kind IN ('agent', 'tool', 'retriever', 'store', 'runner', 'model', 'external')),
		CHECK(target_kind IN ('agent', 'tool', 'retriever', 'store', 'runner', 'model', 'external')),
		CHECK(state IN ('wait', 'satisfied', 'failed', 'cancelled', 'expired')),
		CHECK(failure_policy IN ('fail', 'notify')),
		CHECK((state = 'wait') = (resolved_at IS NULL)),
		CHECK(generation >= 1),
		CHECK(id = trim(id) AND length(id) BETWEEN 1 AND 256 AND instr(id, char(0)) = 0),
		CHECK(run_id = trim(run_id) AND length(run_id) BETWEEN 1 AND 256 AND instr(run_id, char(0)) = 0),
		CHECK(source_id = trim(source_id) AND length(source_id) BETWEEN 1 AND 256 AND instr(source_id, char(0)) = 0),
		CHECK(target_id = trim(target_id) AND length(target_id) BETWEEN 1 AND 256 AND instr(target_id, char(0)) = 0),
		CHECK(reason = trim(reason) AND length(reason) BETWEEN 1 AND 2048 AND instr(reason, char(0)) = 0),
		CHECK(julianday(deadline) IS NOT NULL AND julianday(deadline) > julianday(created_at)),
		CHECK(julianday(created_at) IS NOT NULL AND julianday(updated_at) IS NOT NULL),
		CHECK(resolved_at IS NULL OR (julianday(resolved_at) IS NOT NULL AND julianday(resolved_at) >= julianday(created_at)))
	);`,
	`CREATE UNIQUE INDEX idx_agent_dependency_edges_open
		ON agent_dependency_edges(run_id, source_kind, source_id, target_kind, target_id, generation)
		WHERE state = 'wait';`,
	`CREATE INDEX idx_agent_dependency_edges_target
		ON agent_dependency_edges(run_id, target_kind, target_id) WHERE state = 'wait';`,
	`CREATE TABLE agent_dependency_wakes (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		edge_id TEXT NOT NULL UNIQUE,
		outcome TEXT NOT NULL,
		reason TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(edge_id) REFERENCES agent_dependency_edges(id) ON DELETE RESTRICT,
		CHECK(outcome IN ('satisfied', 'failed', 'cancelled', 'expired')),
		CHECK(id = trim(id) AND length(id) BETWEEN 1 AND 256 AND instr(id, char(0)) = 0),
		CHECK(edge_id = trim(edge_id) AND length(edge_id) BETWEEN 1 AND 256 AND instr(edge_id, char(0)) = 0),
		CHECK(reason = trim(reason) AND length(reason) BETWEEN 1 AND 2048 AND instr(reason, char(0)) = 0),
		CHECK(julianday(created_at) IS NOT NULL)
	) WITHOUT ROWID;`,
	`CREATE TABLE agent_dependency_edge_operations (
		operation_key_digest TEXT PRIMARY KEY,
		request_fingerprint TEXT NOT NULL,
		edge_id TEXT NOT NULL UNIQUE,
		created_at TEXT NOT NULL,
		FOREIGN KEY(edge_id) REFERENCES agent_dependency_edges(id) ON DELETE RESTRICT,
		CHECK(length(operation_key_digest) = 64 AND length(request_fingerprint) = 64),
		CHECK(julianday(created_at) IS NOT NULL)
	) WITHOUT ROWID;`,
}

