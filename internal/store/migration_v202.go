package store

// This is the next migration after main's v201. If another migration lands
// first, rebase and renumber this new migration before merging.
var prFollowupStatements = []string{
	`CREATE TABLE pr_followups (
		id TEXT PRIMARY KEY,
		thread_id TEXT NOT NULL,
		request_fingerprint TEXT NOT NULL,
		status TEXT NOT NULL CHECK(status IN ('active','paused','blocked','ready','completed','cancelled','exhausted')),
		revision INTEGER NOT NULL CHECK(revision >= 1),
		next_check_at INTEGER NOT NULL,
		deadline_at INTEGER NOT NULL,
		record_json TEXT NOT NULL CHECK(json_valid(record_json)),
		state_json TEXT NOT NULL CHECK(json_valid(state_json)),
		lease_owner TEXT NOT NULL DEFAULT '',
		lease_token TEXT NOT NULL DEFAULT '',
		lease_until INTEGER NOT NULL DEFAULT 0,
		FOREIGN KEY(thread_id) REFERENCES threads(id) ON DELETE RESTRICT,
		CHECK(json_extract(record_json, '$.id') = id),
		CHECK(json_extract(record_json, '$.thread_id') = thread_id),
		CHECK(json_extract(record_json, '$.status') = status),
		CHECK(json_extract(record_json, '$.revision') = revision),
		CHECK((lease_owner = '' AND lease_token = '' AND lease_until = 0)
			OR (lease_owner <> '' AND lease_token <> '' AND lease_until > 0))
	);`,
	`CREATE UNIQUE INDEX idx_pr_followups_open_thread ON pr_followups(thread_id)
		WHERE status IN ('active','paused','blocked','ready');`,
	`CREATE INDEX idx_pr_followups_due ON pr_followups(status,next_check_at,lease_until);`,
}
