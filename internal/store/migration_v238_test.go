package store

import (
	"maps"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
)

func TestSchemaV238PreservesCommandJobsAndExactWorkingDirectories(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "command-v237.db")
	state := openHistoricalTestDatabase(t, path, 177)
	job := commandRuntimeMigrationJob(t, state, domain.RunExecutionPermissionFullAccess,
		commandruntimeadapter.HostUnsandboxed(strings.Repeat("a", 64)))
	insertV162CommandRuntimeJob(t, state, job, 73)
	for _, item := range migrationPlan()[177:237] {
		if err := state.applyMigration(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	before, err := state.GetCommandRuntimeJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	actors := legacyFixtureRows(t, state, "command_runtime_job_agents")
	beforeSchema := commandScopeV189Schema(t, state)
	// Verify v238's exact isolated schema change before Open applies subsequent migrations.
	if err := state.applyMigration(ctx, migrationPlan()[237]); err != nil {
		t.Fatal(err)
	}
	afterV238Schema := commandScopeV189Schema(t, state)
	delete(beforeSchema, "table:command_runtime_jobs")
	delete(afterV238Schema, "table:command_runtime_jobs")
	if !maps.Equal(beforeSchema, afterV238Schema) {
		t.Fatal("migration changed unrelated schema, including command authorization triggers")
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	after, err := state.GetCommandRuntimeJob(ctx, job.ID)
	if err != nil || !reflect.DeepEqual(before, after) ||
		!reflect.DeepEqual(actors, legacyFixtureRows(t, state, "command_runtime_job_agents")) {
		t.Fatalf("historical command or actor changed: %v", err)
	}
	var rowID int
	if err := state.db.QueryRowContext(ctx, "SELECT rowid FROM command_runtime_jobs WHERE id=?", job.ID).Scan(&rowID); err != nil || rowID != 73 {
		t.Fatalf("historical rowid=%d: %v", rowID, err)
	}
	const exact = " checks "
	if err := cloneCommandScopeV189(t, state, job.ID, 238, map[string]any{
		"working_directory": exact,
	}); err != nil {
		t.Fatal(err)
	}
	stored, err := state.GetCommandRuntimeJob(ctx, "forged-v189-238")
	if err != nil || stored.WorkingDirectory != exact {
		t.Fatalf("working directory=%q: %v", stored.WorkingDirectory, err)
	}
	if _, err := state.db.ExecContext(ctx, "UPDATE command_runtime_jobs SET working_directory='checks' WHERE id=?", stored.ID); err == nil {
		t.Fatal("saved working directory could be changed")
	}
	assertNoForeignKeyViolations(t, state.db)
	assertLatestMigrationLedger(t, state, migrationPlan())
}
