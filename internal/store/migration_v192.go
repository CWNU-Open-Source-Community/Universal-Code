package store

import "strings"

func runWebAccessStatements() []string {
	statements := []string{strings.ReplaceAll(runNetworkAuthorityExpansionStatements[0], "run_network_authority_operations", "run_web_access_operations")}
	var trigger string
	for _, statement := range runNetworkAuthorityExpansionStatements {
		if strings.HasPrefix(statement, "CREATE TRIGGER trg_run_mode_snapshot_insert") {
			trigger = statement
			break
		}
	}
	marker := "(previous.phase = NEW.phase\n\t\t\t\t\t\t\t\t\t\tAND json_extract"
	webTransition := `(previous.phase = NEW.phase
		AND json_extract(previous.scope_json, '$.network_mode') IS json_extract(NEW.scope_json, '$.network_mode')
		AND COALESCE(json_extract(previous.scope_json, '$.allowed_targets'), '[]') = COALESCE(json_extract(NEW.scope_json, '$.allowed_targets'), '[]')
		AND EXISTS (SELECT 1 FROM run_web_access_operations operation
			WHERE operation.snapshot_id = NEW.id AND operation.run_id = NEW.run_id
				AND operation.expected_mode_revision = previous.revision
				AND operation.requested_by = NEW.requested_by AND operation.created_at = NEW.created_at))
		OR `
	if !strings.Contains(trigger, marker) {
		panic("Run mode trigger web transition marker missing")
	}
	trigger = strings.Replace(trigger, marker, webTransition+marker, 1)
	statements = append(statements, "DROP TRIGGER trg_run_mode_snapshot_insert;", trigger)
	// Reuse the frozen exact DNS-host rejection expression. Broad transport
	// authority remains derived in Go and can never be injected as a rule.
	start := strings.Index(trigger, "WHERE target.type != 'text'")
	if start < 0 {
		panic("exact target SQL validation expression missing")
	}
	closingTarget := "'instance-data.ec2.internal')"
	end := strings.Index(trigger[start:], closingTarget)
	if end < 0 {
		panic("exact target SQL validation expression missing")
	}
	invalidTarget := trigger[start : start+end+len(closingTarget)]
	statements = append(statements, `CREATE TRIGGER trg_run_web_access_scope_insert
		BEFORE INSERT ON run_mode_snapshots WHEN COALESCE((
		 (json_type(NEW.scope_json, '$.web_access_mode') IS NULL AND COALESCE(json_array_length(NEW.scope_json, '$.web_allowed_targets'),0)=0)
		 OR (json_extract(NEW.scope_json, '$.web_access_mode') IN ('disabled','rules','full')
		  AND (json_type(NEW.scope_json, '$.web_allowed_targets') IS NULL OR json_type(NEW.scope_json, '$.web_allowed_targets')='array')
		  AND COALESCE(json_array_length(NEW.scope_json, '$.web_allowed_targets'),0) BETWEEN 0 AND 256
		  AND (json_extract(NEW.scope_json, '$.web_access_mode')='rules' OR COALESCE(json_array_length(NEW.scope_json, '$.web_allowed_targets'),0)=0)
		  AND NOT EXISTS (SELECT 1 FROM json_each(NEW.scope_json, '$.web_allowed_targets') target `+invalidTarget+`)
		  AND NOT EXISTS (SELECT 1 FROM json_each(NEW.scope_json, '$.web_allowed_targets') earlier
		   JOIN json_each(NEW.scope_json, '$.web_allowed_targets') later ON CAST(earlier.key AS INTEGER)<CAST(later.key AS INTEGER) WHERE earlier.value>=later.value)
		 )
		),0)=0 OR (NEW.revision>1 AND EXISTS (SELECT 1 FROM run_mode_snapshots previous
		 WHERE previous.run_id=NEW.run_id AND previous.revision=NEW.revision-1
		 AND (json_extract(previous.scope_json,'$.web_access_mode') IS NOT json_extract(NEW.scope_json,'$.web_access_mode')
		 OR COALESCE(json_extract(previous.scope_json,'$.web_allowed_targets'),'[]') != COALESCE(json_extract(NEW.scope_json,'$.web_allowed_targets'),'[]')))
		 AND NOT EXISTS (SELECT 1 FROM run_web_access_operations operation WHERE operation.snapshot_id=NEW.id AND operation.run_id=NEW.run_id AND operation.expected_mode_revision=NEW.revision-1))
		BEGIN SELECT RAISE(ABORT,'Run web access scope or transition is invalid'); END;`)
	for _, statement := range runNetworkAuthorityExpansionStatements {
		if strings.Contains(statement, "CREATE TRIGGER trg_run_network_authority_operation_") {
			statements = append(statements, strings.ReplaceAll(strings.ReplaceAll(statement, "trg_run_network_authority_operation_", "trg_run_web_access_operation_"), "run_network_authority_operations", "run_web_access_operations"))
		}
	}
	return statements
}
