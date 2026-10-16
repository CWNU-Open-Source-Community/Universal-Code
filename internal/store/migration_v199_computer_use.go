package store

// App grants use the existing approval and revocation ledger. This table only
// pins the application and source scope that a Computer Use grant may consume.
var computerUseAppGrantStatements = []string{
	`CREATE TABLE approval_computer_use_app_grant_scopes (
		grant_id TEXT PRIMARY KEY,
		source_approval_id TEXT NOT NULL,
		app_id TEXT NOT NULL,
		scope_fingerprint TEXT NOT NULL,
		FOREIGN KEY(grant_id) REFERENCES approval_session_grants(id) ON DELETE CASCADE,
		FOREIGN KEY(source_approval_id) REFERENCES tool_approvals(id) ON DELETE CASCADE,
		CHECK(length(app_id) BETWEEN 1 AND 65536 AND instr(app_id, char(0)) = 0),
		CHECK(length(scope_fingerprint) = 64 AND scope_fingerprint NOT GLOB '*[^0-9a-f]*')
	);`,
	`CREATE INDEX idx_computer_use_app_grant_scope ON approval_computer_use_app_grant_scopes(scope_fingerprint);`,
	`CREATE TRIGGER trg_computer_use_app_grant_scope_immutable
		BEFORE UPDATE ON approval_computer_use_app_grant_scopes BEGIN
		SELECT RAISE(ABORT, 'Computer Use app grant scope is immutable'); END;`,
}
