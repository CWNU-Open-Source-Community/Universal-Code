package store

// postToolFailureHookStatements admit the post_tool_failure lifecycle event
// into the hook audit ledger. The event CHECK constraint is part of the table
// definition, so the table is rebuilt in place: historical rows keep their
// rowids and every other constraint, index, and trigger is unchanged.
var postToolFailureHookStatements = []string{
	`PRAGMA legacy_alter_table=ON;`,
	`DROP TRIGGER trg_plugin_hook_diagnostics_bound;`,
	`ALTER TABLE plugin_hook_audits RENAME TO plugin_hook_audits_v225;`,
	`CREATE TABLE plugin_hook_audits_v226 (
		id TEXT PRIMARY KEY,
		plugin_id TEXT NOT NULL,
		hook_id TEXT NOT NULL,
		event TEXT NOT NULL,
		run_id TEXT NOT NULL DEFAULT '',
		workspace_id TEXT NOT NULL DEFAULT '',
		tool_name TEXT NOT NULL DEFAULT '',
		outcome TEXT NOT NULL,
		created_at TEXT NOT NULL,
		plugin_fingerprint TEXT NOT NULL DEFAULT '' CHECK(plugin_fingerprint = '' OR (length(plugin_fingerprint) = 64 AND plugin_fingerprint NOT GLOB '*[^0-9a-f]*')),
		declared_action TEXT NOT NULL DEFAULT '' CHECK(declared_action IN ('', 'deny', 'annotate', 'narrow', 'record', 'script')),
		rejected INTEGER CHECK(rejected IS NULL OR rejected IN (0, 1)),
		CHECK(event IN ('pre_tool', 'post_tool', 'post_tool_failure', 'run_started', 'run_completed',
			'session_opened', 'session_closed', 'user_prompt_submit', 'approval_requested', 'compaction',
			'subagent', 'checkpoint')),
		CHECK(outcome IN ('completed', 'failed_closed', 'failed_continue')),
		CHECK(length(plugin_id) BETWEEN 1 AND 256 AND length(hook_id) BETWEEN 1 AND 256),
		CHECK(length(run_id) <= 256 AND length(workspace_id) <= 256
			AND length(tool_name) <= 256),
		CHECK(julianday(created_at) IS NOT NULL)
	);`,
	`INSERT INTO plugin_hook_audits_v226 SELECT * FROM plugin_hook_audits_v225;`,
	`DROP TABLE plugin_hook_audits_v225;`,
	`ALTER TABLE plugin_hook_audits_v226 RENAME TO plugin_hook_audits;`,
	`CREATE TRIGGER trg_plugin_hook_audits_update_immutable
		BEFORE UPDATE ON plugin_hook_audits
		BEGIN SELECT RAISE(ABORT, 'plugin hook audit is immutable'); END;`,
	`CREATE TRIGGER trg_plugin_hook_audits_delete_immutable
		BEFORE DELETE ON plugin_hook_audits
		BEGIN SELECT RAISE(ABORT, 'plugin hook audit cannot be deleted'); END;`,
	`CREATE TRIGGER trg_plugin_hook_diagnostics_bound BEFORE INSERT ON plugin_hook_audits
	 WHEN (NEW.plugin_fingerprint != '' OR NEW.declared_action != '' OR NEW.rejected IS NOT NULL)
	 AND (NEW.plugin_fingerprint = '' OR NEW.declared_action = '' OR NEW.rejected IS NULL
	 OR NEW.rejected != CASE
		WHEN NEW.outcome = 'failed_closed' THEN 1
		WHEN NEW.outcome = 'failed_continue' THEN 0
		WHEN NEW.outcome = 'completed' AND NEW.declared_action = 'deny' THEN 1
		WHEN NEW.outcome = 'completed' AND NEW.declared_action = 'script' THEN NEW.rejected
		ELSE 0 END)
	 BEGIN SELECT RAISE(ABORT, 'Hook decision metadata must bind one observed outcome'); END;`,
	`CREATE INDEX idx_plugin_hook_audits_run_created
		ON plugin_hook_audits(run_id, created_at DESC, id DESC);`,
	`CREATE INDEX idx_plugin_hook_audits_workspace_created
		ON plugin_hook_audits(workspace_id, created_at DESC, id DESC);`,
	`PRAGMA legacy_alter_table=OFF;`,
}
