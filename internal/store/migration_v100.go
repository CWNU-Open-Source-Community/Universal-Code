package store

var monetaryBudgetStatements = []string{
	`CREATE TABLE provider_price_snapshots (
		id TEXT PRIMARY KEY,
		protocol_version TEXT NOT NULL,
		source TEXT NOT NULL,
		currency TEXT NOT NULL,
		imported_by TEXT NOT NULL,
		imported_at TEXT NOT NULL,
		valid_from TEXT NOT NULL,
		valid_until TEXT NOT NULL,
		fingerprint TEXT NOT NULL UNIQUE,
		entries_json TEXT NOT NULL,
		active INTEGER NOT NULL DEFAULT 0,
		CHECK(protocol_version = 'price_snapshot.v1'),
		CHECK(source = 'operator_import'),
		CHECK(currency = 'USD'),
		CHECK(active IN (0, 1)),
		CHECK(id = trim(id) AND length(id) BETWEEN 1 AND 128 AND instr(id, char(0)) = 0),
		CHECK(imported_by = trim(imported_by) AND length(imported_by) BETWEEN 1 AND 256
			AND instr(imported_by, char(0)) = 0),
		CHECK(length(fingerprint) = 64 AND fingerprint = lower(fingerprint)
			AND fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(json_valid(entries_json) AND length(entries_json) BETWEEN 1 AND 65536),
		CHECK(julianday(valid_from) IS NOT NULL AND julianday(valid_until) IS NOT NULL
			AND julianday(valid_until) > julianday(valid_from))
	);`,
	`CREATE UNIQUE INDEX idx_provider_price_snapshots_active
		ON provider_price_snapshots(active) WHERE active = 1;`,
	`CREATE TABLE run_monetary_usage (
		run_id TEXT PRIMARY KEY,
		reserved_micros INTEGER NOT NULL DEFAULT 0,
		settled_micros INTEGER NOT NULL DEFAULT 0,
		released_micros INTEGER NOT NULL DEFAULT 0,
		updated_at TEXT NOT NULL,
		exhausted_at TEXT,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(reserved_micros >= 0 AND settled_micros >= 0 AND released_micros >= 0),
		CHECK(reserved_micros >= settled_micros + released_micros),
		CHECK(julianday(updated_at) IS NOT NULL)
	);`,
	`CREATE TABLE run_monetary_reservations (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		scope TEXT NOT NULL,
		provider TEXT NOT NULL,
		model TEXT NOT NULL,
		attempt_number INTEGER NOT NULL,
		reserved_micros INTEGER NOT NULL,
		settled_micros INTEGER NOT NULL DEFAULT 0,
		released_micros INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL,
		estimate_source TEXT NOT NULL,
		price_fingerprint TEXT NOT NULL,
		created_at TEXT NOT NULL,
		settled_at TEXT,
		released_at TEXT,
		UNIQUE(run_id, scope, attempt_number),
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(scope IN ('root', 'specialist', 'readonly_fanout')),
		CHECK(attempt_number >= 1),
		CHECK(reserved_micros >= 1 AND settled_micros >= 0 AND released_micros >= 0),
		CHECK(settled_micros + released_micros <= reserved_micros),
		CHECK(status IN ('reserved', 'settled', 'released')),
		CHECK((status = 'settled') = (settled_at IS NOT NULL)),
		CHECK((status = 'released') = (released_at IS NOT NULL)),
		CHECK(status = 'reserved' OR settled_micros + released_micros = reserved_micros),
		CHECK(id = trim(id) AND length(id) BETWEEN 1 AND 256 AND instr(id, char(0)) = 0),
		CHECK(run_id = trim(run_id) AND length(run_id) BETWEEN 1 AND 256
			AND instr(run_id, char(0)) = 0),
		CHECK(provider = trim(provider) AND length(provider) BETWEEN 1 AND 256
			AND instr(provider, char(0)) = 0),
		CHECK(model = trim(model) AND length(model) BETWEEN 1 AND 256
			AND instr(model, char(0)) = 0),
		CHECK(estimate_source = trim(estimate_source) AND length(estimate_source) BETWEEN 1 AND 256
			AND instr(estimate_source, char(0)) = 0),
		CHECK(length(price_fingerprint) = 64 AND price_fingerprint = lower(price_fingerprint)
			AND price_fingerprint NOT GLOB '*[^0-9a-f]*'),
		CHECK(julianday(created_at) IS NOT NULL)
	) WITHOUT ROWID;`,
}

