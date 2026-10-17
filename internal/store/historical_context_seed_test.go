package store

import "testing"

// Current runners and budget readers inspect context ledgers that did not exist
// in historical fixtures. Keep those ledgers empty while seeding, then restore
// the exact original schema and migration ledger before testing the upgrade.
func addEmptyCurrentContextForLegacySeed(t testing.TB, state *SQLiteStore) func() {
	t.Helper()
	version, err := state.SchemaVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if version >= 206 {
		return func() {}
	}
	tables := []struct {
		name, statement string
	}{
		{"thread_context_compactions", threadContextCompactionStatements()[0]},
	}
	if version < 205 {
		for _, name := range []string{"thread_context_pin_sets", "thread_context_pins", "thread_context_pin_proposals"} {
			tables = append(tables, struct{ name, statement string }{
				name, requireMigrationStatement("CREATE TABLE "+name+" (", threadContextPinStatements),
			})
		}
	}
	var setup, restore []string
	for _, table := range tables {
		setup = append(setup, table.statement,
			"CREATE TRIGGER legacy_fixture_empty_"+table.name+" BEFORE INSERT ON "+table.name+
				" BEGIN SELECT RAISE(ABORT, 'historical fixture cannot contain current context'); END;")
	}
	restore = append(restore, `CREATE TEMP TABLE legacy_fixture_empty_context (n INTEGER CHECK(n=0));`)
	for _, table := range tables {
		restore = append(restore, "INSERT INTO legacy_fixture_empty_context SELECT count(*) FROM "+table.name+";")
	}
	restore = append(restore, `DROP TABLE legacy_fixture_empty_context;`)
	for i := len(tables) - 1; i >= 0; i-- {
		restore = append(restore, "DROP TABLE "+tables[i].name+";")
	}
	return withLegacySeedSchema(t, state, setup, restore)
}
