package store

var terminalSessionStatements = []string{
	`CREATE TABLE terminal_sessions (
		id TEXT PRIMARY KEY,
		protocol_version TEXT NOT NULL,
		run_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL,
		state TEXT NOT NULL,
		cwd TEXT NOT NULL DEFAULT '',
		columns INTEGER NOT NULL,
		rows INTEGER NOT NULL,
		process_pid INTEGER NOT NULL DEFAULT 0,
		agent_input_active INTEGER NOT NULL DEFAULT 0 CHECK(agent_input_active IN (0, 1)),
		created_at TEXT NOT NULL,
		closed_at TEXT,
		last_activity_at TEXT NOT NULL,
		FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE RESTRICT,
		CHECK(protocol_version = 'user_terminal_session.v1'),
		CHECK(state IN ('starting', 'running', 'exited', 'closed', 'failed')),
		CHECK(length(id) BETWEEN 1 AND 256 AND instr(id, char(0)) = 0),
		CHECK(columns BETWEEN 20 AND 300 AND rows BETWEEN 5 AND 120),
		CHECK(length(cwd) <= 4096 AND instr(cwd, char(0)) = 0),
		CHECK(julianday(created_at) IS NOT NULL AND julianday(last_activity_at) IS NOT NULL)
	);`,
}