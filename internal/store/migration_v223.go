package store

var browserNetworkAccessStatements = []string{
	`CREATE TABLE run_browser_network_preferences (
 run_id TEXT PRIMARY KEY REFERENCES runs(id), revision INTEGER NOT NULL CHECK(revision>0),
 mode TEXT NOT NULL CHECK(mode IN ('off','internet','restricted')),
 origins_json TEXT NOT NULL CHECK(json_valid(origins_json)), requested_by TEXT NOT NULL, updated_at TEXT NOT NULL
 );`,
	`CREATE TABLE workspace_browser_network_preferences (
 workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id), revision INTEGER NOT NULL CHECK(revision>0),
 mode TEXT NOT NULL CHECK(mode IN ('off','internet','restricted')),
 origins_json TEXT NOT NULL CHECK(json_valid(origins_json)), requested_by TEXT NOT NULL, updated_at TEXT NOT NULL
 );`,
	`CREATE TABLE browser_network_access_operations (
 key_digest TEXT PRIMARY KEY CHECK(length(key_digest)=64), fingerprint TEXT NOT NULL CHECK(length(fingerprint)=64),
 result_json TEXT NOT NULL CHECK(json_valid(result_json)), requested_by TEXT NOT NULL, created_at TEXT NOT NULL
 );`,
	`CREATE TRIGGER browser_network_access_operations_no_update BEFORE UPDATE ON browser_network_access_operations BEGIN SELECT RAISE(ABORT,'browser network operations are immutable'); END;`,
	`CREATE TRIGGER browser_network_access_operations_no_delete BEFORE DELETE ON browser_network_access_operations BEGIN SELECT RAISE(ABORT,'browser network operations are immutable'); END;`,
}
