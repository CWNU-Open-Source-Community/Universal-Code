package store

func exactCommandWorkingDirectoryStatements() []string {
	v180 := commandOperationApprovalStatements()
	v185 := operatorCommandInvocationStatements()
	create := requireMigrationStatement("CREATE TABLE command_runtime_jobs_v180 (", v180)
	create = replaceCommandRuntimeMigrationFragment(create,
		"CREATE TABLE command_runtime_jobs_v180 (", "CREATE TABLE command_runtime_jobs_v234 (")
	create = replaceCommandRuntimeMigrationFragment(create,
		" AND working_directory = trim(working_directory)", "")
	create = replaceCommandRuntimeMigrationFragment(create,
		"\n\t\tFOREIGN KEY(run_id)",
		"\n\t\toperator_invocation INTEGER NOT NULL DEFAULT 0 CHECK(operator_invocation IN (0,1)),\n\t\tFOREIGN KEY(run_id)")
	const columns = "rowid, protocol_version, " + commandRuntimeV179Columns + ", run_authorization_fence, operator_invocation"
	// Leave external triggers and foreign keys referring to the canonical
	// table name throughout the copy. The migration checks the copied keys.
	return []string{
		"PRAGMA legacy_alter_table=ON;",
		create,
		"INSERT INTO command_runtime_jobs_v234 (" + columns + ") SELECT " + columns + " FROM command_runtime_jobs;",
		"DROP TABLE command_runtime_jobs;",
		"ALTER TABLE command_runtime_jobs_v234 RENAME TO command_runtime_jobs;",
		requireMigrationStatement("CREATE INDEX idx_command_runtime_jobs_run_created", v180),
		requireMigrationStatement("CREATE INDEX idx_command_runtime_jobs_active", v180),
		requireMigrationTrigger("trg_command_runtime_job_insert_scope", longProjectTestJobStatements()),
		requireMigrationTrigger("trg_command_runtime_job_insert_limit", v180),
		requireMigrationTrigger("trg_command_runtime_job_update_transition", v180),
		requireMigrationTrigger("trg_command_runtime_job_delete_immutable", v180),
		requireMigrationTrigger("trg_command_runtime_operator_invocation_immutable", v185),
		requireMigrationTrigger("trg_command_runtime_shared_service_policy", threadCommandServicePolicyStatements),
		"PRAGMA legacy_alter_table=OFF;",
	}
}
