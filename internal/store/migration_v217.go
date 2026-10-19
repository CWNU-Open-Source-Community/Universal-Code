package store

var projectlessWorkspaceStatements = []string{
	`CREATE TABLE projectless_workspace_allocations (
		operation_key_digest TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL UNIQUE,
		root_path TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`,
	`DROP TRIGGER trg_thread_initial_project`,
	`CREATE TRIGGER trg_thread_initial_project AFTER INSERT ON threads BEGIN
	 INSERT INTO thread_project_assignments(thread_id,project_workspace_id,version)
	 VALUES(NEW.id, CASE WHEN EXISTS(SELECT 1 FROM projectless_workspace_allocations WHERE workspace_id=NEW.workspace_id) THEN '' ELSE NEW.workspace_id END, 1); END`,
}
