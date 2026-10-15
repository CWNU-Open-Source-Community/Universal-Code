package store

import "strings"

// Native Skills retain exact component pins and optional author versions in the
// existing v2 ledger. Copy every old row and rowid unchanged, including binding
// JSON, so existing selection fingerprints and operation receipts remain valid.
func nativeSkillSelectionStatements() []string {
	var table string
	var statements, recreate []string
	for _, statement := range pluginSkillSelectionStatements() {
		if strings.HasPrefix(statement, "CREATE TABLE run_external_skill_selection_items_v184 (") {
			table = strings.Replace(statement, "_v184 (", "_v197 (", 1)
		}
		if (strings.HasPrefix(statement, "CREATE TRIGGER ") || strings.HasPrefix(statement, "CREATE INDEX ")) &&
			strings.Contains(statement, "run_external_skill_selection") {
			fields := strings.Fields(statement)
			statements = append(statements, "DROP "+fields[1]+" "+fields[2]+";")
			if strings.HasPrefix(statement, "CREATE TRIGGER trg_run_external_skill_selection_plugin_item_insert") {
				statement = replacePluginSelectionAnchor(statement, "WHEN NEW.plugin_binding_json != '{}' AND NOT EXISTS (",
					"WHEN NEW.plugin_binding_json != '{}' AND COALESCE(json_extract(NEW.plugin_binding_json, '$.native'), 0) = 0 AND NOT EXISTS (")
			}
			recreate = append(recreate, statement)
		}
	}
	if table == "" {
		panic("v197 requires the immutable v184 selection items table")
	}
	table = replacePluginSelectionAnchor(table, "\t\tUNIQUE(selection_id, installation_id),\n", "")
	table = replacePluginSelectionAnchor(table, "\t\tUNIQUE(selection_id, name, version),\n", "")
	table = replacePluginSelectionAnchor(table, `CHECK(name = trim(name) AND length(name) BETWEEN 1 AND 64
			AND name NOT GLOB '*[^a-z0-9-]*' AND name GLOB '[a-z]*' AND substr(name, -1) != '-'),`,
		`CHECK(COALESCE(json_extract(plugin_binding_json, '$.native'), 0) = 1 OR
   (name = trim(name) AND length(name) BETWEEN 1 AND 64
    AND name NOT GLOB '*[^a-z0-9-]*' AND name GLOB '[a-z]*' AND substr(name, -1) != '-')),
  CHECK(COALESCE(json_extract(plugin_binding_json, '$.native'), 0) = 0 OR
   (plugin_installation_id = installation_id AND length(name) BETWEEN 1 AND 64
    AND instr(name, char(0)) = 0)),
  CHECK(json_type(plugin_binding_json, '$.native') IS NULL OR json_type(plugin_binding_json, '$.native') = 'true'),`)
	table = replacePluginSelectionAnchor(table, `CHECK(version = trim(version) AND length(version) BETWEEN 5 AND 29
			AND version NOT GLOB '*[^0-9.]*'),`,
		`CHECK((COALESCE(json_extract(plugin_binding_json, '$.native'), 0) = 0
   AND version = trim(version) AND length(version) BETWEEN 5 AND 29 AND version NOT GLOB '*[^0-9.]*')
   OR (json_extract(plugin_binding_json, '$.native') = 1 AND length(version) <= 262144 AND instr(version, char(0)) = 0)),`)
	table = replacePluginSelectionAnchor(table, "CHECK(archive_bytes BETWEEN 1 AND 65536),",
		"CHECK(archive_bytes BETWEEN 1 AND CASE WHEN json_extract(plugin_binding_json, '$.native') = 1 THEN 4194304 ELSE 65536 END),")
	for _, view := range externalSkillProjectionStatements {
		statements = append(statements, "DROP VIEW "+strings.Fields(view)[2]+";")
		recreate = append(recreate, view)
	}
	statements = append(statements, table,
		`INSERT INTO run_external_skill_selection_items_v197 (rowid, selection_id, ordinal, installation_id, installation_fingerprint, install_result_fingerprint, name, version, surface, content_sha256, content_bytes, token_upper_bound, archive_sha256, archive_bytes, package_fingerprint, object_key, trust_class, tool_dependency_count, specialist_eligible, plugin_binding_json, legacy_installation_id, plugin_installation_id)
   SELECT rowid, * FROM run_external_skill_selection_items;`,
		`DROP TABLE run_external_skill_selection_items;`,
		`ALTER TABLE run_external_skill_selection_items_v197 RENAME TO run_external_skill_selection_items;`)
	statements = append(statements, recreate...)
	return append(statements,
		`CREATE UNIQUE INDEX idx_external_skill_legacy_installation ON run_external_skill_selection_items(selection_id, installation_id)
   WHERE COALESCE(json_extract(plugin_binding_json, '$.native'), 0) = 0;`,
		`CREATE UNIQUE INDEX idx_external_skill_legacy_ref ON run_external_skill_selection_items(selection_id, name, version)
   WHERE COALESCE(json_extract(plugin_binding_json, '$.native'), 0) = 0;`,
		`CREATE UNIQUE INDEX idx_external_skill_native_component ON run_external_skill_selection_items(selection_id, installation_id, json_extract(plugin_binding_json, '$.component_id'))
   WHERE json_extract(plugin_binding_json, '$.native') = 1;`,
		`CREATE TRIGGER trg_run_external_skill_selection_native_item_insert
 BEFORE INSERT ON run_external_skill_selection_items
 WHEN json_extract(NEW.plugin_binding_json, '$.native') = 1 AND NOT EXISTS (
  SELECT 1 FROM run_external_skill_selections selection
  JOIN plugin_installations installation ON installation.id = NEW.plugin_installation_id
  JOIN plugin_objects object ON object.archive_sha256 = installation.archive_sha256
   AND object.package_fingerprint = installation.package_fingerprint
  JOIN json_each(installation.manifest_json, '$.skills') skill
  JOIN json_each(installation.manifest_json, '$.inventory') entry
  WHERE selection.id = NEW.selection_id AND selection.protocol_version = 'external_skill_selection.v2'
   AND NEW.ordinal = 1 + (SELECT COUNT(*) FROM run_external_skill_selection_items existing WHERE existing.selection_id = NEW.selection_id)
   AND installation.protocol_version = 'plugin-installation.v2' AND installation.state = 'enabled'
   AND json_extract(installation.source_json, '$.surface') = selection.surface AND NEW.surface = selection.surface
   AND EXISTS (SELECT 1 FROM json_each(installation.enabled_capabilities_json) WHERE value = 'skills')
   AND installation.plugin_id = json_extract(NEW.plugin_binding_json, '$.package_id')
   AND installation.generation = json_extract(NEW.plugin_binding_json, '$.generation')
   AND installation.archive_sha256 = NEW.archive_sha256 AND installation.archive_bytes = NEW.archive_bytes
   AND installation.package_fingerprint = NEW.package_fingerprint
   AND json_extract(installation.manifest_json, '$.format') IN ('agent-skills', 'agent-plugins')
   AND json_type(installation.manifest_json, '$.legacy') IS NULL
   AND json_extract(skill.value, '$.instructions.Component.PackageID') = installation.plugin_id
   AND json_extract(skill.value, '$.instructions.Component.ComponentID') = json_extract(NEW.plugin_binding_json, '$.component_id')
   AND json_extract(skill.value, '$.instructions.SHA256') = NEW.content_sha256
   AND json_extract(skill.value, '$.name') = NEW.name
   AND COALESCE(json_extract(installation.manifest_json, '$.author_version'), '') = NEW.version
   AND json_extract(entry.value, '$.path') = json_extract(skill.value, '$.instructions.Path')
   AND json_extract(entry.value, '$.kind') = 'file'
   AND json_extract(entry.value, '$.sha256') = NEW.content_sha256
   AND json_extract(entry.value, '$.bytes') = NEW.content_bytes
   AND NEW.content_bytes = NEW.token_upper_bound AND NEW.tool_dependency_count = 0)
 BEGIN SELECT RAISE(ABORT, 'external native Skill selection component binding is invalid'); END;`)
}
