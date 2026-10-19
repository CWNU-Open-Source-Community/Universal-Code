package store

import "strings"

func pnpmPreparationReceiptStatements() []string {
	var admission, update string
	for _, statement := range projectPreparationStatements {
		if strings.HasPrefix(statement, "CREATE TRIGGER trg_project_preparation_insert ") {
			admission = statement
		}
		if strings.HasPrefix(statement, "CREATE TRIGGER trg_project_preparation_update ") {
			update = statement
		}
	}
	if admission == "" || update == "" {
		panic("project preparation receipt triggers are unavailable")
	}
	admission = strings.Replace(admission, "WHEN NEW.state!='pending'", `WHEN NEW.fetch_job_id!=''
	 OR ((NEW.workspace_root='' OR NEW.seed_directory='') AND (NEW.workspace_root!='' OR NEW.seed_directory!=''))
	 OR (NEW.workspace_root!='' AND (NEW.backend!='docker_standard_code'
	  OR (NEW.workspace_root!='.' AND NEW.target!=NEW.workspace_root AND substr(NEW.target,1,length(NEW.workspace_root)+1)!=NEW.workspace_root||'/')))
	 OR NEW.state!='pending'`, 1)
	update = strings.Replace(update, "WHEN NEW.id!=OLD.id", `WHEN NEW.workspace_root!=OLD.workspace_root OR NEW.seed_directory!=OLD.seed_directory
	 OR (OLD.fetch_job_id!='' AND NEW.fetch_job_id!=OLD.fetch_job_id)
	 OR (NEW.fetch_job_id!='' AND (NEW.seed_directory='' OR NOT EXISTS (
	  SELECT 1 FROM command_runtime_jobs job WHERE job.id=NEW.fetch_job_id
	   AND job.run_id=NEW.run_id AND job.adapter_backend=NEW.backend
	   AND job.working_directory=NEW.seed_directory AND job.permission_revision=NEW.permission_revision
	   AND json_extract(job.intent_json,'$.purpose')='Populate pnpm dependency store '||NEW.request_fingerprint)))
	 OR (NEW.state='ready' AND NEW.seed_directory!='' AND NOT EXISTS (
	  SELECT 1 FROM command_runtime_jobs job WHERE job.id=NEW.fetch_job_id
	   AND job.state='completed' AND job.exit_code=0 AND job.tree_reaped=1))
	 OR NEW.id!=OLD.id`, 1)
	if strings.Count(update, "job.working_directory=NEW.target") != 2 {
		panic("project preparation directory bindings changed")
	}
	update = strings.ReplaceAll(update, "job.working_directory=NEW.target", "job.working_directory=COALESCE(NULLIF(NEW.workspace_root,''),NEW.target)")
	return []string{
		`ALTER TABLE project_preparations ADD COLUMN workspace_root TEXT NOT NULL DEFAULT '' CHECK(length(workspace_root)<=4096);`,
		`ALTER TABLE project_preparations ADD COLUMN seed_directory TEXT NOT NULL DEFAULT '' CHECK(length(seed_directory)<=4096);`,
		`ALTER TABLE project_preparations ADD COLUMN fetch_job_id TEXT NOT NULL DEFAULT '' CHECK(length(fetch_job_id)<=128);`,
		`DROP TRIGGER trg_project_preparation_insert;`,
		`DROP TRIGGER trg_project_preparation_update;`,
		admission,
		update,
	}
}
