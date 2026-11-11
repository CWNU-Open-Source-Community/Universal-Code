package store

// Post-occurrence hook boundaries (post_tool, run_completed, session_closed)
// cap observation output instead of failing, and the boundary fire that
// tripped the cap records the drop as the outcome "output_capped". SQLite
// cannot widen the v121 outcome CHECK in place, so preserve the v222 rows
// while rebuilding the audit ledger.
var hookAuditOutcomeCapStatements = []string{
	`DROP INDEX idx_plugin_hook_audits_run_created;`,
	`DROP INDEX idx_plugin_hook_audits_workspace_created;`,
	`DROP TRIGGER trg_plugin_hook_diagnostics_bound;`,
	`DROP TRIGGER trg_plugin_hook_audits_update_immutable;`,
	`DROP TRIGGER trg_plugin_hook_audits_delete_immutable;`,
	`ALTER TABLE plugin_hook_audits RENAME TO plugin_hook_audits_v222;`,
	`CREATE TABLE plugin_hook_audits (
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
		declared_action TEXT NOT NULL DEFAULT '' CHECK(declared_action IN ('', 'deny', 'annotate', 'narrow', 'record')),
		rejected INTEGER CHECK(rejected IS NULL OR rejected IN (0, 1)),
		CHECK(event IN ('pre_tool', 'post_tool', 'run_started', 'run_completed',
			'session_opened', 'session_closed', 'compaction', 'subagent', 'checkpoint')),
		CHECK(outcome IN ('completed', 'failed_closed', 'failed_continue', 'output_capped')),
		CHECK(length(plugin_id) BETWEEN 1 AND 256 AND length(hook_id) BETWEEN 1 AND 256),
		CHECK(length(run_id) <= 256 AND length(workspace_id) <= 256
			AND length(tool_name) <= 256),
		CHECK(julianday(created_at) IS NOT NULL)
	);`,
	`INSERT INTO plugin_hook_audits
		(id, plugin_id, hook_id, event, run_id, workspace_id, tool_name, outcome,
		 created_at, plugin_fingerprint, declared_action, rejected)
	SELECT id, plugin_id, hook_id, event, run_id, workspace_id, tool_name, outcome,
		 created_at, plugin_fingerprint, declared_action, rejected
		FROM plugin_hook_audits_v222;`,
	`DROP TABLE plugin_hook_audits_v222;`,
	`CREATE INDEX idx_plugin_hook_audits_run_created
		ON plugin_hook_audits(run_id, created_at DESC, id DESC);`,
	`CREATE INDEX idx_plugin_hook_audits_workspace_created
		ON plugin_hook_audits(workspace_id, created_at DESC, id DESC);`,
	`CREATE TRIGGER trg_plugin_hook_audits_update_immutable
		BEFORE UPDATE ON plugin_hook_audits
		BEGIN SELECT RAISE(ABORT, 'plugin hook audit is immutable'); END;`,
	`CREATE TRIGGER trg_plugin_hook_audits_delete_immutable
		BEFORE DELETE ON plugin_hook_audits
		BEGIN SELECT RAISE(ABORT, 'plugin hook audit is immutable'); END;`,
	`CREATE TRIGGER trg_plugin_hook_diagnostics_bound BEFORE INSERT ON plugin_hook_audits
		 WHEN (NEW.plugin_fingerprint != '' OR NEW.declared_action != '' OR NEW.rejected IS NOT NULL)
		 AND (NEW.plugin_fingerprint = '' OR NEW.declared_action = '' OR NEW.rejected IS NULL
		 OR NEW.rejected != CASE WHEN NEW.outcome = 'failed_closed' OR (NEW.declared_action = 'deny' AND NEW.outcome = 'completed') THEN 1 ELSE 0 END)
		 BEGIN SELECT RAISE(ABORT, 'Hook decision metadata must bind one observed outcome'); END;`,
}
