package store

import "strings"

func exactWorkspaceSnapshotPathStatements() []string {
	// Foreign keys are disabled during the rebuild. Legacy rename keeps references
	// in other tables and triggers pointing to the original table names.
	statements := []string{`PRAGMA legacy_alter_table=ON;`}
	var indexesAndTriggers []string
	for _, table := range []struct {
		name, pathColumn string
		schema           []string
	}{
		{"readonly_fanout_plans", "scope_path", readOnlyFanoutPlanStatements},
		{"readonly_fanout_files", "relative_path", readOnlyFanoutPlanStatements},
		{"readonly_fanout_findings", "relative_path", readOnlyFanoutExecutionStatements},
		{"findings", "relative_path", findingReportStatements},
		{"finding_evidence", "relative_path", findingReportStatements},
		{"workspace_checkpoint_entries", "path", workspaceCheckpointStatements},
	} {
		var create string
		for _, statement := range table.schema {
			if strings.HasPrefix(statement, "CREATE TABLE "+table.name+" (") {
				trimCheck := "CHECK(" + table.pathColumn + " = trim(" + table.pathColumn + ")"
				create = strings.Replace(statement, trimCheck+" AND ", "CHECK(", 1)
				create = strings.Replace(create, trimCheck+"\n\t\t\tAND ", "CHECK(", 1)
				create = strings.Replace(create, " AND "+table.pathColumn+" = trim("+table.pathColumn+")", "", 1)
				if create == statement {
					panic("missing path trim constraint on " + table.name)
				}
			}
			fields := strings.Fields(statement)
			if (len(fields) >= 5 && fields[1] == "INDEX" && fields[3] == "ON" &&
				strings.HasPrefix(fields[4], table.name+"(")) ||
				(len(fields) >= 7 && fields[1] == "TRIGGER" && fields[5] == "ON" && fields[6] == table.name) {
				indexesAndTriggers = append(indexesAndTriggers, statement)
			}
		}
		if create == "" {
			panic("missing workspace snapshot table " + table.name)
		}
		old := table.name + "_v231"
		statements = append(statements,
			"ALTER TABLE "+table.name+" RENAME TO "+old+";", create,
			"INSERT INTO "+table.name+" SELECT * FROM "+old+";",
			"DROP TABLE "+old+";",
		)
	}
	// Restore write guards after copying so they don't reject historical rows.
	statements = append(statements, indexesAndTriggers...)
	return append(statements, `PRAGMA legacy_alter_table=OFF;`)
}
