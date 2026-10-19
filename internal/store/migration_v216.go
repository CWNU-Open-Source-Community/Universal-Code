package store

func threadProjectMoveStatements() []string {
	return []string{
		`CREATE TABLE thread_project_assignments (thread_id TEXT PRIMARY KEY REFERENCES threads(id), project_workspace_id TEXT NOT NULL, version INTEGER NOT NULL CHECK(version>0));`,
		`INSERT INTO thread_project_assignments SELECT id,workspace_id,1 FROM threads;`,
		`CREATE TRIGGER trg_thread_initial_project AFTER INSERT ON threads BEGIN INSERT INTO thread_project_assignments VALUES(NEW.id,NEW.workspace_id,1); END;`,
		`CREATE TABLE thread_project_moves (operation_key TEXT PRIMARY KEY, fingerprint TEXT NOT NULL, thread_id TEXT NOT NULL REFERENCES threads(id), predecessor_run_id TEXT NOT NULL REFERENCES runs(id), successor_run_id TEXT REFERENCES runs(id), project_workspace_id TEXT NOT NULL, assignment_version INTEGER NOT NULL, created_at TEXT NOT NULL);`,
		`CREATE TRIGGER trg_thread_project_move_immutable BEFORE UPDATE ON thread_project_moves BEGIN SELECT RAISE(ABORT,'Thread project move receipt is immutable'); END;`,
		`CREATE TRIGGER trg_thread_project_move_nodelete BEFORE DELETE ON thread_project_moves BEGIN SELECT RAISE(ABORT,'Thread project move receipt is immutable'); END;`,
		`DROP TRIGGER trg_threads_binding_immutable;`,
		`CREATE TRIGGER trg_threads_binding_immutable BEFORE UPDATE OF protocol_version,created_at ON threads BEGIN SELECT RAISE(ABORT,'Thread identity is immutable'); END;`,
		`CREATE TRIGGER trg_thread_workspace_transition BEFORE UPDATE OF workspace_id,mission_id ON threads WHEN NOT EXISTS (
 SELECT 1 FROM thread_project_moves move JOIN runs r ON r.id=move.successor_run_id JOIN missions m ON m.id=r.mission_id
 JOIN sessions s ON s.id=r.session_id JOIN runs predecessor ON predecessor.id=move.predecessor_run_id
 WHERE move.thread_id=OLD.id AND move.predecessor_run_id=OLD.last_run_id AND predecessor.status IN ('completed','failed','cancelled')
 AND r.status='created' AND r.mission_id=NEW.mission_id AND m.workspace_id=NEW.workspace_id AND s.workspace_id=NEW.workspace_id
 AND NOT EXISTS(SELECT 1 FROM thread_runs used WHERE used.run_id=r.id))
 BEGIN SELECT RAISE(ABORT,'Thread workspace change requires a fresh Run and move receipt'); END;`,
	}
}
