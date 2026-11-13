package store

import "strings"

func exactFileEditApplyPathStatements() []string {
	var create, index string
	for _, statement := range agentCodeToolStatements {
		switch {
		case strings.HasPrefix(statement, "CREATE TABLE file_edit_apply_operations ("):
			create = strings.Replace(statement, "CHECK(path = trim(path) AND length(path)",
				"CHECK(length(path)", 1)
			if create == statement {
				panic("missing file edit apply path trim constraint")
			}
		case strings.HasPrefix(statement, "CREATE INDEX idx_file_edit_apply_operations_run_created"):
			index = statement
		}
	}
	if create == "" || index == "" {
		panic("missing file edit apply table or index")
	}
	// Leave references from apply results pointing to the original table name.
	// Reinstall the current write guards after copying the historical rows.
	return []string{
		"PRAGMA legacy_alter_table=ON;",
		"ALTER TABLE file_edit_apply_operations RENAME TO file_edit_apply_operations_v232;",
		create,
		"INSERT INTO file_edit_apply_operations SELECT * FROM file_edit_apply_operations_v232;",
		"DROP TABLE file_edit_apply_operations_v232;",
		index,
		requireMigrationTrigger("trg_file_edit_apply_operation_update_immutable", agentCodeToolStatements),
		requireMigrationTrigger("trg_file_edit_apply_operation_delete_immutable", agentCodeToolStatements),
		requireMigrationTrigger("trg_file_edit_auto_apply_insert", fileOperationApprovalStatements()),
		requireMigrationTrigger("trg_file_edit_apply_operation_insert", specialistFileEditStatements()),
		"PRAGMA legacy_alter_table=OFF;",
	}
}
