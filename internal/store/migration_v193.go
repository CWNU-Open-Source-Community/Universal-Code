package store

var projectPreparationStatements = []string{
	`CREATE TABLE project_preparations (
		id TEXT PRIMARY KEY CHECK(length(id) BETWEEN 1 AND 128),
		run_id TEXT NOT NULL REFERENCES runs(id),
		key_digest TEXT NOT NULL UNIQUE CHECK(length(key_digest)=64 AND key_digest NOT GLOB '*[^0-9a-f]*'),
		request_fingerprint TEXT NOT NULL CHECK(length(request_fingerprint)=64 AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
		target TEXT NOT NULL CHECK(length(target) BETWEEN 1 AND 4096),
		plan_fingerprint TEXT NOT NULL CHECK(length(plan_fingerprint)=64 AND plan_fingerprint NOT GLOB '*[^0-9a-f]*'),
		backend TEXT NOT NULL CHECK(backend IN ('local_windows_lpac','docker_standard_code','docker_sandboxes')),
		requested_by TEXT NOT NULL CHECK(length(requested_by) BETWEEN 1 AND 128),
		request_json TEXT NOT NULL CHECK(json_valid(request_json) AND length(CAST(request_json AS BLOB))<=65536),
		owner_id TEXT NOT NULL CHECK(length(owner_id) BETWEEN 1 AND 128),
		permission_revision INTEGER NOT NULL CHECK(permission_revision>0),
		state TEXT NOT NULL CHECK(state IN ('pending','downloading','installing','ready','failed','cancelled','interrupted')),
		completed_packages INTEGER NOT NULL CHECK(completed_packages BETWEEN 0 AND total_packages),
		total_packages INTEGER NOT NULL CHECK(total_packages BETWEEN 0 AND 6000),
		cache_hits INTEGER NOT NULL CHECK(cache_hits BETWEEN 0 AND completed_packages),
		downloaded_bytes INTEGER NOT NULL CHECK(downloaded_bytes BETWEEN 0 AND 536870912),
		job_id TEXT NOT NULL DEFAULT '', script_job_id TEXT NOT NULL DEFAULT '',
		error TEXT NOT NULL CHECK(length(CAST(error AS BLOB))<=4096),
		cancel_requested INTEGER NOT NULL CHECK(cancel_requested IN (0,1)),
		revision INTEGER NOT NULL CHECK(revision>0),
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL CHECK(updated_at>=created_at)
	);`,
	`CREATE INDEX idx_project_preparations_run ON project_preparations(run_id,target,created_at DESC);`,
	`CREATE UNIQUE INDEX idx_project_preparation_active ON project_preparations(run_id) WHERE state IN ('pending','downloading','installing');`,
	`CREATE TRIGGER trg_project_preparation_insert BEFORE INSERT ON project_preparations
	 WHEN NEW.state!='pending' OR NEW.revision!=1 OR NEW.completed_packages!=0 OR NEW.cache_hits!=0
	 OR NEW.downloaded_bytes!=0 OR NEW.job_id!='' OR NEW.script_job_id!='' OR NEW.cancel_requested!=0 OR NEW.error!=''
	 OR NEW.created_at!=NEW.updated_at OR NOT EXISTS (SELECT 1 FROM runs WHERE id=NEW.run_id AND status IN ('created','paused','running'))
	 BEGIN SELECT RAISE(ABORT,'invalid preparation admission'); END;`,
	`CREATE TRIGGER trg_project_preparation_update BEFORE UPDATE ON project_preparations
	 WHEN NEW.id!=OLD.id OR NEW.run_id!=OLD.run_id OR NEW.key_digest!=OLD.key_digest
	 OR NEW.request_fingerprint!=OLD.request_fingerprint OR NEW.target!=OLD.target OR NEW.plan_fingerprint!=OLD.plan_fingerprint
	 OR NEW.backend!=OLD.backend OR NEW.requested_by!=OLD.requested_by OR NEW.request_json!=OLD.request_json
	 OR NEW.owner_id!=OLD.owner_id OR NEW.permission_revision!=OLD.permission_revision OR NEW.total_packages!=OLD.total_packages
	 OR NEW.created_at!=OLD.created_at OR NEW.revision!=OLD.revision+1 OR NEW.updated_at<OLD.updated_at
	 OR NEW.completed_packages<OLD.completed_packages OR NEW.cache_hits<OLD.cache_hits OR NEW.downloaded_bytes<OLD.downloaded_bytes
	 OR NEW.cancel_requested<OLD.cancel_requested OR (OLD.job_id!='' AND NEW.job_id!=OLD.job_id)
	 OR (OLD.script_job_id!='' AND NEW.script_job_id!=OLD.script_job_id)
	 OR (NEW.job_id!='' AND NOT EXISTS (SELECT 1 FROM command_runtime_jobs job
	  WHERE job.id=NEW.job_id AND job.run_id=NEW.run_id AND job.adapter_backend=NEW.backend
	   AND job.working_directory=NEW.target AND job.permission_revision=NEW.permission_revision
	   AND json_extract(job.intent_json,'$.purpose')='Prepare project dependencies '||NEW.request_fingerprint))
	 OR (NEW.script_job_id!='' AND (COALESCE(json_extract(NEW.request_json,'$.run_install_scripts'),0)!=1
	  OR NOT EXISTS (SELECT 1 FROM command_runtime_jobs job
	   WHERE job.id=NEW.script_job_id AND job.run_id=NEW.run_id AND job.adapter_backend=NEW.backend
	    AND job.working_directory=NEW.target AND job.permission_revision=NEW.permission_revision
	    AND json_extract(job.intent_json,'$.purpose')='Run dependency installation scripts '||NEW.request_fingerprint)))
	 OR (NEW.state='ready' AND (NEW.cancel_requested!=0 OR NEW.completed_packages!=NEW.total_packages OR NEW.error!=''
	  OR NOT EXISTS (SELECT 1 FROM command_runtime_jobs job WHERE job.id=NEW.job_id
	   AND job.state='completed' AND job.exit_code=0 AND job.tree_reaped=1)
	  OR (COALESCE(json_extract(NEW.request_json,'$.run_install_scripts'),0)=1 AND NOT EXISTS (
	   SELECT 1 FROM command_runtime_jobs job WHERE job.id=NEW.script_job_id
	    AND job.state='completed' AND job.exit_code=0 AND job.tree_reaped=1))))
	 OR OLD.state IN ('ready','failed','cancelled','interrupted')
	 OR (NEW.state NOT IN ('failed','cancelled','interrupted') AND NOT (
	  NEW.state=OLD.state OR (OLD.state='pending' AND NEW.state='downloading')
	  OR (OLD.state='downloading' AND NEW.state='installing') OR (OLD.state='installing' AND NEW.state='ready')))
	 BEGIN SELECT RAISE(ABORT,'invalid preparation transition'); END;`,
	`CREATE TRIGGER trg_project_preparation_delete BEFORE DELETE ON project_preparations BEGIN SELECT RAISE(ABORT,'preparation receipts are retained'); END;`,
}
