package store

import "strings"

// npm workspace installation uses the shared root without pnpm's fetch seed.
// Keep v215's immutable directory, permission and exact Job-purpose checks.
func npmWorkspacePreparationStatements() []string {
	var admission string
	for _, statement := range pnpmPreparationReceiptStatements() {
		if strings.HasPrefix(statement, "CREATE TRIGGER trg_project_preparation_insert ") {
			admission = statement
		}
	}
	old := `((NEW.workspace_root='' OR NEW.seed_directory='') AND (NEW.workspace_root!='' OR NEW.seed_directory!=''))`
	replacement := `(NEW.seed_directory!='' AND NEW.workspace_root='')`
	if strings.Count(admission, old) != 1 {
		panic("project preparation workspace admission changed")
	}
	admission = strings.Replace(admission, old, replacement, 1)
	old = `NEW.backend!='docker_standard_code'`
	replacement = `NEW.backend NOT IN ('docker_standard_code','local_windows_lpac','docker_sandboxes')
	  OR (NEW.seed_directory!='' AND NEW.backend!='docker_standard_code')`
	if strings.Count(admission, old) != 1 {
		panic("project preparation backend admission changed")
	}
	return []string{`DROP TRIGGER trg_project_preparation_insert;`, strings.Replace(admission, old, replacement, 1)}
}
