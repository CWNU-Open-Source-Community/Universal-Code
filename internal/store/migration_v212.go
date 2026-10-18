package store

var threadCommandServicePolicyStatements = []string{
	`CREATE TABLE thread_command_service_policies (
      thread_id TEXT PRIMARY KEY REFERENCES threads(id) ON DELETE RESTRICT,
      policy TEXT NOT NULL CHECK(policy IN ('auto','exclusive')),
      revision INTEGER NOT NULL CHECK(revision >= 2),
      updated_at TEXT NOT NULL
    );`,
	`CREATE TRIGGER trg_command_runtime_shared_service_policy
      BEFORE INSERT ON command_runtime_jobs
      WHEN json_extract(NEW.intent_json,'$.workspace_mode')='shared_service'
       AND EXISTS (SELECT 1 FROM thread_runs binding
         JOIN thread_command_service_policies preference ON preference.thread_id=binding.thread_id
         WHERE binding.run_id=NEW.run_id AND preference.policy='exclusive')
      BEGIN SELECT RAISE(ABORT,'Thread requires exclusive command execution'); END;`,
}
