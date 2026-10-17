package store

import "testing"

// Current queue writers/readers and Supervisor context queries need v168's four
// message columns and two receipt tables, plus v171's steering mode identity.
// Current Web fetch readers also need v192's empty web access selection ledger.
// Current context readers use empty v207/v208 ledgers only during this seed.
// This fixture-only compatibility does not record a migration or change
// v165/v166's handoff and authorization guards. Its restore rejects revisions,
// edited identities, attachment evidence, committed corrections and web
// selections, then verifies the exact original schema, migration ledger,
// and foreign keys.
func addV166FixtureQueueCompatibility(t testing.TB, state *SQLiteStore) func() {
	t.Helper()
	restoreContext := addEmptyCurrentContextForLegacySeed(t, state)
	setup := append(append([]string{}, operatorSteeringRevisionStatements...), midTurnSteeringStatements[:3]...)
	setup = append(setup, runWebAccessStatements()[0])
	restore := append([]string{}, removeCurrentSteeringForLegacySeedStatements()...)
	restore = append(restore,
		`CREATE TEMP TABLE legacy_fixture_empty_queue_history (n INTEGER CHECK(n=0));`,
		`INSERT INTO legacy_fixture_empty_queue_history SELECT count(*) FROM operator_steering_revisions;`,
		`INSERT INTO legacy_fixture_empty_queue_history SELECT count(*) FROM operator_message_attachment_evidence;`,
		`INSERT INTO legacy_fixture_empty_queue_history SELECT count(*) FROM run_web_access_operations;`,
		`INSERT INTO legacy_fixture_empty_queue_history SELECT count(*) FROM operator_steering_messages
			WHERE revision<>0 OR edited_at IS NOT NULL OR original_content<>content
				OR original_content_sha256<>content_sha256;`,
		`DROP TABLE legacy_fixture_empty_queue_history;`,
		`DROP TABLE run_web_access_operations;`,
		`DROP TRIGGER trg_operator_steering_update_monotonic;`,
		`DROP TABLE operator_steering_revisions;`,
		`DROP TABLE operator_message_attachment_evidence;`,
		`ALTER TABLE operator_steering_messages DROP COLUMN revision;`,
		`ALTER TABLE operator_steering_messages DROP COLUMN original_content;`,
		`ALTER TABLE operator_steering_messages DROP COLUMN original_content_sha256;`,
		`ALTER TABLE operator_steering_messages DROP COLUMN edited_at;`,
		migrationTriggerBeforeForTest("trg_operator_steering_update_monotonic", 168))
	restoreQueue := withLegacySeedSchema(t, state, setup, restore)
	return func() {
		restoreQueue()
		restoreContext()
	}
}
