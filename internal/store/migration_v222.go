package store

var browserNetworkRequestStatements = []string{
	`CREATE TABLE browser_network_requests (
 id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id),
 session_id TEXT NOT NULL, workspace_id TEXT NOT NULL,
 manager_boot_id TEXT NOT NULL, runtime_id TEXT NOT NULL,
 fingerprint TEXT NOT NULL CHECK(length(fingerprint)=64), authority_json TEXT NOT NULL CHECK(json_valid(authority_json)),
 method TEXT NOT NULL, display_url TEXT NOT NULL,
 body_sha256 TEXT NOT NULL CHECK(length(body_sha256)=64), body_bytes INTEGER NOT NULL CHECK(body_bytes BETWEEN 0 AND 1048576),
 expires_at TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('paused','consumed','dispatched','failed','invalidated'))
 );`,
	`CREATE INDEX browser_network_requests_live ON browser_network_requests(status,manager_boot_id);`,
}
