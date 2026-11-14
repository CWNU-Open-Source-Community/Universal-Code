package store

import "strings"

// Preserve an explicit process-free choice separately from an unselected
// initial Preview. All execution profile mutations remain created/paused,
// lease-free, immutable transitions. No historical rows or triggers are edited.
func explicitInitialPreviewStatements(previous []migration) []string {
	const trigger = "trg_run_execution_profile_snapshot_insert"
	var definition string
	for _, step := range previous {
		for _, statement := range step.Statements {
			if strings.HasPrefix(statement, "CREATE TRIGGER "+trigger+"\n") {
				definition = statement
			}
		}
	}
	const anchor = "previous.profile != NEW.profile"
	if strings.Count(definition, anchor) != 1 {
		panic("explicit Preview selection lost the execution profile transition")
	}
	definition = strings.Replace(definition, anchor,
		"(previous.profile != NEW.profile OR (previous.revision = 1 AND previous.profile = 'preview' AND NEW.profile = 'preview'))", 1)
	return []string{"DROP TRIGGER " + trigger + ";", definition}
}
