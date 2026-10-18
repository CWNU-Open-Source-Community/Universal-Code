package store

import "strings"

func scheduledJobExecutionStatements() []string {
	create := strings.Replace(scheduledJobStatements[0], "CREATE TABLE scheduled_jobs (", "CREATE TABLE scheduled_jobs_execution_upgrade (", 1)
	create = strings.Replace(create, "('read_only', 'approved_repair')", "('read_only', 'approved_repair', 'automatic')", 1)
	return []string{
		create,
		`INSERT INTO scheduled_jobs_execution_upgrade SELECT * FROM scheduled_jobs;`,
		`DROP TABLE scheduled_jobs;`,
		`PRAGMA legacy_alter_table = ON;`,
		`ALTER TABLE scheduled_jobs_execution_upgrade RENAME TO scheduled_jobs;`,
		`PRAGMA legacy_alter_table = OFF;`,
		scheduledJobStatements[1], scheduledJobStatements[2],
		`CREATE TABLE scheduled_job_execution_consents (
   job_id TEXT PRIMARY KEY REFERENCES scheduled_jobs(id) ON DELETE RESTRICT,
   version INTEGER NOT NULL CHECK(version=1),
   consent_json TEXT NOT NULL CHECK(json_valid(consent_json))
  );`,
		`CREATE TRIGGER trg_scheduled_job_execution_consent_insert BEFORE INSERT ON scheduled_job_execution_consents
   WHEN NOT EXISTS (SELECT 1 FROM scheduled_jobs WHERE id=NEW.job_id AND execution_mode='automatic'
    AND json_type(spec_json,'$.execution_target')='object')
   BEGIN SELECT RAISE(ABORT,'automatic execution requires an explicit target'); END;`,
		`CREATE TRIGGER trg_scheduled_job_execution_consent_update_immutable BEFORE UPDATE ON scheduled_job_execution_consents
   BEGIN SELECT RAISE(ABORT,'automatic execution consent is immutable'); END;`,
		`CREATE TRIGGER trg_scheduled_job_execution_consent_delete_immutable BEFORE DELETE ON scheduled_job_execution_consents
   BEGIN SELECT RAISE(ABORT,'automatic execution consent cannot be deleted'); END;`,
		`CREATE TABLE scheduled_job_executions (
   job_id TEXT NOT NULL REFERENCES scheduled_job_execution_consents(job_id) ON DELETE RESTRICT,
   occurrence_at TEXT NOT NULL,
   operation_key TEXT NOT NULL,
   receipt_json TEXT NOT NULL CHECK(json_valid(receipt_json)),
   PRIMARY KEY(job_id,occurrence_at),
   FOREIGN KEY(job_id,occurrence_at) REFERENCES scheduled_job_rounds(job_id,occurrence_at) ON DELETE RESTRICT
  ) WITHOUT ROWID;`,
	}
}
