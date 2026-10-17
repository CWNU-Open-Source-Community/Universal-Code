package store

import "strings"

var projectHookStatements = func() []string {
	// Preserve native model-call uniqueness and historical rowids. A host-owned
	// hook round shares its real model source without fabricating a model attempt.
	create := strings.Replace(supervisorToolLoopStatements[0],
		"\t\tUNIQUE(run_id, turn, attempt_id, model_attempt),\n", "", 1)
	create = strings.Replace(create, "created_at TEXT NOT NULL,",
		"origin TEXT NOT NULL DEFAULT 'model' CHECK(origin IN ('model','project_hook')),\n\t\tcreated_at TEXT NOT NULL,", 1)
	create = strings.Replace(create, "round BETWEEN 1 AND 4", "round BETWEEN 1 AND 21", 1)
	statements := []string{
		`PRAGMA legacy_alter_table=ON;`,
		`DROP TRIGGER trg_supervisor_tool_round_active_attempt;`,
		`DROP TRIGGER trg_supervisor_tool_round_completed_model;`,
		`DROP TRIGGER trg_supervisor_tool_round_completion;`,
		`ALTER TABLE run_supervisor_tool_rounds RENAME TO run_supervisor_tool_rounds_v204;`,
		create,
		`INSERT INTO run_supervisor_tool_rounds(rowid,run_id,turn,attempt_id,round,model_attempt,created_at,completed_at)
		 SELECT rowid,run_id,turn,attempt_id,round,model_attempt,created_at,completed_at FROM run_supervisor_tool_rounds_v204;`,
		`DROP TABLE run_supervisor_tool_rounds_v204;`,
		`CREATE UNIQUE INDEX idx_supervisor_native_model_round ON run_supervisor_tool_rounds(run_id,turn,attempt_id,model_attempt) WHERE origin='model';`,
		`CREATE TRIGGER trg_supervisor_native_round_limit BEFORE INSERT ON run_supervisor_tool_rounds
		 WHEN NEW.origin='model' AND (SELECT COUNT(*) FROM run_supervisor_tool_rounds
		 WHERE run_id=NEW.run_id AND turn=NEW.turn AND attempt_id=NEW.attempt_id AND origin='model')>=4
		 BEGIN SELECT RAISE(ABORT,'native model tool round limit exceeded'); END;`,
		requireMigrationTrigger("trg_supervisor_tool_round_active_attempt", supervisorToolLoopStatements),
		requireMigrationTrigger("trg_supervisor_tool_round_completed_model", supervisorToolLoopStatements),
		requireMigrationTrigger("trg_supervisor_tool_round_completion", supervisorToolLoopStatements),
	}
	// Replay rows keep the physical execution sequence, which now includes host
	// hooks. Preserve their bytes, rowids, foreign keys and source checks.
	for _, table := range []struct {
		name, columns string
		source        []string
		create        int
		triggers      []string
	}{
		{"run_supervisor_provider_replay", "run_id,turn,attempt_id,round,model_attempt,provider,model,replay_blob,replay_sha256,created_at", supervisorProviderReplayStatements, 0,
			[]string{"trg_supervisor_provider_replay_source", "trg_supervisor_provider_replay_immutable"}},
		{"run_supervisor_context_recoveries", "run_id,turn,attempt_id,tool_round,protocol_repair,model_attempt,original_input_tokens,provider,model,created_at", supervisorProviderReplayStatements, 3,
			[]string{"trg_supervisor_context_recovery_source", "trg_supervisor_context_recovery_immutable"}},
		{"run_supervisor_assistant_replay", "run_id,turn,attempt_id,model_attempt,tool_round,provider,model,replay_blob,replay_sha256,created_at", supervisorAssistantReplayStatements, 0,
			[]string{"trg_supervisor_assistant_replay_source", "trg_supervisor_assistant_replay_immutable", "trg_supervisor_assistant_replay_delete"}},
	} {
		for _, trigger := range table.triggers {
			statements = append(statements, "DROP TRIGGER "+trigger+";")
		}
		create := strings.ReplaceAll(table.source[table.create], "AND 4)", "AND 21)")
		statements = append(statements,
			"ALTER TABLE "+table.name+" RENAME TO "+table.name+"_v204;", create,
			"INSERT INTO "+table.name+"(rowid,"+table.columns+") SELECT rowid,"+table.columns+" FROM "+table.name+"_v204;",
			"DROP TABLE "+table.name+"_v204;")
		for _, trigger := range table.triggers {
			definition := requireMigrationTrigger(trigger, table.source)
			if trigger == "trg_supervisor_provider_replay_source" {
				definition = strings.Replace(definition, "WHERE r.run_id=NEW.run_id", "WHERE r.origin='model' AND r.run_id=NEW.run_id", 1)
			}
			statements = append(statements, definition)
		}
	}
	statements = append(statements, `PRAGMA legacy_alter_table=OFF;`)
	return append(statements,
		`CREATE TABLE project_hook_trust (
		workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id),
		root_fingerprint TEXT NOT NULL CHECK(length(root_fingerprint)=64),
		fingerprint TEXT NOT NULL CHECK(length(fingerprint)=64),
		trusted INTEGER NOT NULL CHECK(trusted IN (0,1)),
		revision INTEGER NOT NULL CHECK(revision>0),
		updated_at TEXT NOT NULL
	);`,
		`CREATE TABLE project_hook_calls (
		run_id TEXT NOT NULL,
		turn INTEGER NOT NULL,
		attempt_id TEXT NOT NULL,
		call_id TEXT NOT NULL,
		trigger_key TEXT NOT NULL,
		slot TEXT NOT NULL CHECK(slot IN ('format','test')),
		target TEXT NOT NULL,
		config_fingerprint TEXT NOT NULL,
		workspace_version TEXT NOT NULL,
		PRIMARY KEY(run_id,turn,attempt_id,call_id),
		UNIQUE(run_id,trigger_key),
		FOREIGN KEY(run_id,turn,attempt_id,call_id) REFERENCES run_supervisor_tool_calls(run_id,turn,attempt_id,call_id)
	);`)
}()
