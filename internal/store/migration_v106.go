package store

var gitMutationStatements = []string{
	`CREATE TABLE git_mutation_operations (
		id TEXT PRIMARY KEY,
		protocol_version TEXT NOT NULL,
		operation_key_digest TEXT NOT NULL UNIQUE,
		request_fingerprint TEXT NOT NULL,
		run_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL,
		operation TEXT NOT NULL,
		spec_json TEXT NOT NULL,
		pre_head TEXT NOT NULL,
		post_head TEXT NOT NULL DEFAULT '',
		branch TEXT NOT NULL DEFAULT '',
		commit_id TEXT NOT NULL DEFAULT '',
		conflicted INTEGER NOT NULL DEFAULT 0 CHECK(conflicted IN (0, 1)),
		clean INTEGER NOT NULL DEFAULT 0 CHECK(clean IN (0, 1)),
		stderr_prefix TEXT NOT NULL DEFAULT '',
		completed_at TEXT,
		created_at TEXT NOT NULL,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(protocol_version = 'repository_mutation.v1'),
		CHECK(operation IN ('stage', 'unstage', 'commit', 'create_branch', 'switch_branch')),
		CHECK(length(id) BETWEEN 1 AND 256 AND instr(id, char(0)) = 0),
		CHECK(length(operation_key_digest) = 64 AND length(request_fingerprint) = 64),
		CHECK(json_valid(spec_json) AND length(spec_json) BETWEEN 1 AND 32768),
		CHECK(julianday(created_at) IS NOT NULL)
	);`,
}