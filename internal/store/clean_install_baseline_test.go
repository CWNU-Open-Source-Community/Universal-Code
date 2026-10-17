package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/domain"
)

func TestCleanInstallBaselineRequiresAProvablyEmptyMainSchema(t *testing.T) {
	plan := migrationPlan()
	tests := []struct {
		name      string
		prepare   []string
		wantApply bool
	}{
		{name: "empty", wantApply: true},
		{name: "migration ledger already exists", prepare: []string{`CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL,
			applied_at TEXT NOT NULL)`}},
		{name: "legacy user table", prepare: []string{`CREATE TABLE workspaces (
			id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, root_path TEXT NOT NULL,
			created_at TEXT NOT NULL)`, `INSERT INTO workspaces VALUES
			('legacy-workspace', 'legacy', 'C:\legacy', '2026-08-26T00:00:00Z')`}},
		{name: "unrelated user object", prepare: []string{
			`CREATE TABLE unrelated (id INTEGER PRIMARY KEY)`,
			`CREATE VIEW unrelated_view AS SELECT id FROM unrelated`,
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := openUnmigratedSQLiteStore(t, filepath.Join(t.TempDir(), "state.db"))
			defer state.Close()
			for _, statement := range test.prepare {
				if _, err := state.db.ExecContext(t.Context(), statement); err != nil {
					t.Fatal(err)
				}
			}
			used, err := state.tryCleanInstallBaseline(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			if used != test.wantApply {
				t.Fatalf("baseline used=%t want=%t", used, test.wantApply)
			}
			if test.wantApply {
				assertLatestMigrationLedger(t, state, plan)
				return
			}
			for _, statement := range test.prepare {
				if strings.HasPrefix(strings.TrimSpace(statement), "INSERT") {
					var count int
					if err := state.db.QueryRowContext(t.Context(),
						`SELECT COUNT(*) FROM workspaces WHERE id = 'legacy-workspace'`).Scan(&count); err != nil || count != 1 {
						t.Fatalf("baseline touched legacy data: count=%d err=%v", count, err)
					}
				}
			}
		})
	}
}

func TestCleanInstallBaselineMissingProofFallsBackWithoutMutation(t *testing.T) {
	state := openUnmigratedSQLiteStore(t, filepath.Join(t.TempDir(), "state.db"))
	defer state.Close()
	artifact := currentCleanInstallBaselineArtifactForTest()
	artifact.MigrationPlanSHA256 = strings.Repeat("0", 64)
	used, err := state.tryCleanInstallBaselineArtifact(t.Context(), migrationPlan(), artifact)
	if err != nil || used {
		t.Fatalf("stale artifact used=%t err=%v", used, err)
	}
	empty, err := sqliteMainSchemaEmpty(t.Context(), state.db)
	if err != nil || !empty {
		t.Fatalf("stale artifact mutated empty database: empty=%t err=%v", empty, err)
	}
	if err := state.applyMigrations(t.Context(), migrationPlan()); err != nil {
		t.Fatalf("historical fallback failed: %v", err)
	}
	assertLatestMigrationLedger(t, state, migrationPlan())
}

func TestCleanInstallBaselineFailureRollsBackAndRestartRecovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	state := openUnmigratedSQLiteStore(t, path)
	artifact := currentCleanInstallBaselineArtifactForTest()
	artifact.SQL = `CREATE TABLE baseline_partial_write (id INTEGER PRIMARY KEY);` +
		cleanInstallBaselineStatementSeparator + artifact.SQL +
		cleanInstallBaselineStatementSeparator + `THIS IS NOT VALID SQL;`
	sum := sha256.Sum256([]byte(artifact.SQL))
	artifact.SQLSHA256 = fmt.Sprintf("%x", sum)
	used, err := state.tryCleanInstallBaselineArtifact(t.Context(), migrationPlan(), artifact)
	if err == nil || used {
		t.Fatalf("broken baseline used=%t err=%v", used, err)
	}
	empty, emptyErr := sqliteMainSchemaEmpty(t.Context(), state.db)
	if emptyErr != nil || !empty {
		t.Fatalf("failed baseline left a partial schema: empty=%t err=%v", empty, emptyErr)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := Open(path)
	if err != nil {
		t.Fatalf("restart after rolled-back baseline failed: %v", err)
	}
	defer restarted.Close()
	assertLatestMigrationLedger(t, restarted, migrationPlan())
	var partial int
	if err := restarted.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM sqlite_schema
		WHERE name = 'baseline_partial_write'`).Scan(&partial); err != nil || partial != 0 {
		t.Fatalf("partial baseline object survived restart: count=%d err=%v", partial, err)
	}
}

func TestCleanInstallBaselineDiskFullRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk-full.db")
	state := openUnmigratedSQLiteStore(t, path)
	if _, err := state.db.ExecContext(t.Context(), `PRAGMA page_size = 512;`); err != nil {
		t.Fatal(err)
	}
	if _, err := state.db.ExecContext(t.Context(), `PRAGMA max_page_count = 2;`); err != nil {
		t.Fatal(err)
	}
	used, err := state.tryCleanInstallBaseline(t.Context(), migrationPlan())
	if err == nil || used {
		t.Fatalf("bounded database unexpectedly accepted baseline: used=%t err=%v", used, err)
	}
	empty, emptyErr := sqliteMainSchemaEmpty(t.Context(), state.db)
	if emptyErr != nil || !empty {
		t.Fatalf("disk error left a partial schema: empty=%t err=%v", empty, emptyErr)
	}
	if _, err := state.db.ExecContext(t.Context(), `PRAGMA max_page_count = 1073741823;`); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(path)
	if err != nil {
		t.Fatalf("restart after simulated disk recovery failed: %v", err)
	}
	defer restarted.Close()
	assertLatestMigrationLedger(t, restarted, migrationPlan())
}

func TestCleanInstallBaselineMatchesRepresentativeLegacyUpgrades(t *testing.T) {
	ctx := t.Context()
	plan := migrationPlan()
	baseline := openUnmigratedSQLiteStore(t, filepath.Join(t.TempDir(), "baseline.db"))
	used, err := baseline.tryCleanInstallBaseline(ctx, plan)
	if err != nil || !used {
		t.Fatalf("clean-install baseline used=%t err=%v", used, err)
	}
	defer baseline.Close()
	wantDigest, err := sqliteSchemaDigest(ctx, baseline.db)
	if err != nil {
		t.Fatal(err)
	}
	if wantDigest != cleanInstallBaselineSchemaSHA256 {
		t.Fatalf("baseline schema digest=%s want=%s", wantDigest, cleanInstallBaselineSchemaSHA256)
	}
	assertLatestMigrationLedger(t, baseline, plan)
	if err := verifySQLiteForeignKeys(ctx, baseline.db); err != nil {
		t.Fatal(err)
	}

	for _, version := range []int{1, 97, 128, 132, 171, 192, 198, 203} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), fmt.Sprintf("legacy-v%d.db", version))
			legacy := openUnmigratedSQLiteStore(t, path)
			if err := applyMigrationPrefixForTest(ctx, legacy, plan, version); err != nil {
				t.Fatalf("create v%d fixture: %v", version, err)
			}
			if err := legacy.Close(); err != nil {
				t.Fatal(err)
			}
			upgraded, err := Open(path)
			if err != nil {
				t.Fatalf("upgrade v%d fixture: %v", version, err)
			}
			defer upgraded.Close()
			gotDigest, err := sqliteSchemaDigest(ctx, upgraded.db)
			if err != nil {
				t.Fatal(err)
			}
			if gotDigest != wantDigest {
				t.Fatalf("v%d schema digest=%s want=%s", version, gotDigest, wantDigest)
			}
			assertLatestMigrationLedger(t, upgraded, plan)
			if err := verifySQLiteForeignKeys(ctx, upgraded.db); err != nil {
				t.Fatalf("v%d foreign keys: %v", version, err)
			}
		})
	}
}

func TestLegacyV132AuditAuthorityReceiptRecoveryCleanupAndReplaySurviveUpgrade(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "legacy-v132-durable.db")
	state := openUnmigratedSQLiteStore(t, path)
	if err := applyMigrationPrefixForTest(ctx, state, migrationPlan(), 132); err != nil {
		t.Fatal(err)
	}
	sessionPlan, identity, acceptance, ownership := legacyV132BrowserLaunchStoreFixture(t, state)
	authorityBefore, err := state.GetRunExecutionPermission(ctx, sessionPlan.RunID)
	if err != nil || authorityBefore.Mode != domain.RunExecutionPermissionConservative ||
		authorityBefore.ExecutionAuthorized || authorityBefore.CapabilityGrant {
		t.Fatalf("legacy authority snapshot=%+v err=%v", authorityBefore, err)
	}
	attempt, lease, replayed, err := state.PrepareBrowserLaunch(ctx, sessionPlan,
		identity, acceptance, ownership, "baseline-legacy-browser-operation", "legacy-worker")
	if err != nil || replayed {
		t.Fatalf("legacy attempt=%+v replayed=%t err=%v", attempt, replayed, err)
	}
	replayedAttempt, replayedLease, replayed, err := state.PrepareBrowserLaunch(ctx,
		sessionPlan, identity, acceptance, ownership,
		"baseline-legacy-browser-operation", "legacy-worker")
	if err != nil || !replayed || replayedAttempt.ID != attempt.ID || replayedLease.ID != lease.ID {
		t.Fatalf("legacy exact replay diverged: attempt=%+v replayed=%t err=%v",
			replayedAttempt, replayed, err)
	}

	successCheckpoints := successfulBrowserRuntimeCheckpointFixture(t, attempt, ownership,
		"baseline-success-runtime", time.Now().UTC().Round(time.Millisecond))
	for _, checkpoint := range successCheckpoints {
		if err := state.RecordBrowserRuntimeCheckpoint(ctx, checkpoint); err != nil {
			t.Fatal(err)
		}
	}
	successReceipt := successfulBrowserRuntimeReceiptFixture(t,
		successCheckpoints[len(successCheckpoints)-1])
	if err := state.RecordBrowserRuntimeReceipt(ctx, successReceipt); err != nil {
		t.Fatal(err)
	}
	if err := state.RecordBrowserRuntimeReceipt(ctx, successReceipt); err != nil {
		t.Fatalf("legacy receipt exact replay failed: %v", err)
	}

	recoveryInitial := initialBrowserRuntimeCheckpointFixture(t, attempt, ownership,
		"baseline-recovery-runtime", successReceipt.CompletedAt.Add(time.Second))
	if err := state.RecordBrowserRuntimeCheckpoint(ctx, recoveryInitial); err != nil {
		t.Fatal(err)
	}
	recoveryFailed := recoveryInitial
	recoveryFailed.ID = recoveryInitial.RuntimeID + "-checkpoint-2"
	recoveryFailed.PreviousCheckpointFingerprint = recoveryInitial.Fingerprint
	recoveryFailed.Generation = 2
	recoveryFailed.Stage = browserruntime.BrowserRuntimeStageFailed
	recoveryFailed.RecoveryRequired = true
	recoveryFailed.FailureCode = "network_cleanup_unverified"
	recoveryFailed.RecordedAt = recoveryInitial.RecordedAt.Add(time.Millisecond)
	recoveryFailed.Fingerprint = browserRuntimeLifecycleStoreFixtureFingerprint(t, recoveryFailed)
	if err := state.RecordBrowserRuntimeCheckpoint(ctx, recoveryFailed); err != nil {
		t.Fatal(err)
	}
	recoveryReceipt := browserruntime.BrowserRuntimeReceipt{
		ProtocolVersion: browserruntime.BrowserRuntimeReceiptProtocolVersion,
		ID:              recoveryFailed.RuntimeID + "-receipt", RuntimeID: recoveryFailed.RuntimeID,
		RunID: recoveryFailed.RunID, AttemptFingerprint: recoveryFailed.AttemptFingerprint,
		AuthorizationFingerprint:   recoveryFailed.AuthorizationFingerprint,
		FinalCheckpointFingerprint: recoveryFailed.Fingerprint,
		RestrictedCDPClosed:        true, Succeeded: false, RecoveryRequired: true,
		FailureCode: recoveryFailed.FailureCode, StartedAt: recoveryInitial.RecordedAt,
		CompletedAt: recoveryFailed.RecordedAt.Add(time.Millisecond),
	}
	recoveryReceipt.Fingerprint = browserRuntimeLifecycleStoreFixtureFingerprint(t, recoveryReceipt)
	if err := state.RecordBrowserRuntimeReceipt(ctx, recoveryReceipt); err != nil {
		t.Fatal(err)
	}
	eventsBefore, err := state.ListRunEvents(ctx, sessionPlan.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(path)
	if err != nil {
		t.Fatalf("upgrade durable v132 fixture: %v", err)
	}
	defer upgraded.Close()
	assertLatestMigrationLedger(t, upgraded, migrationPlan())
	authorityAfter, err := upgraded.GetRunExecutionPermission(ctx, sessionPlan.RunID)
	if err != nil || !reflect.DeepEqual(authorityAfter, authorityBefore) {
		t.Fatalf("authority snapshot changed: before=%+v after=%+v err=%v",
			authorityBefore, authorityAfter, err)
	}
	storedAttempt, storedLease, replayed, err := upgraded.PrepareBrowserLaunch(ctx,
		sessionPlan, identity, acceptance, ownership,
		"baseline-legacy-browser-operation", "legacy-worker")
	if err != nil || !replayed || storedAttempt.ID != attempt.ID || storedLease.ID != lease.ID {
		t.Fatalf("exactly-once operation changed after upgrade: replayed=%t err=%v", replayed, err)
	}
	storedReceipt, found, err := upgraded.LoadBrowserRuntimeReceipt(ctx, successReceipt.RuntimeID)
	if err != nil || !found || !reflect.DeepEqual(storedReceipt, successReceipt) ||
		!storedReceipt.ProcessTreeQuiescent || !storedReceipt.NetworkCleanupVerified ||
		!storedReceipt.ProfileReleased || !storedReceipt.ProfileCleaned {
		t.Fatalf("cleanup receipt changed: found=%t receipt=%+v err=%v", found, storedReceipt, err)
	}
	if err := upgraded.RecordBrowserRuntimeReceipt(ctx, successReceipt); err != nil {
		t.Fatalf("receipt replay changed after upgrade: %v", err)
	}
	recoverable, err := upgraded.ListRecoverableBrowserRuntimeCheckpoints(ctx, 10)
	if err != nil || len(recoverable) != 1 ||
		recoverable[0].Fingerprint != recoveryFailed.Fingerprint {
		t.Fatalf("recovery state changed: checkpoints=%+v err=%v", recoverable, err)
	}
	eventsAfter, err := upgraded.ListRunEvents(ctx, sessionPlan.RunID)
	if err != nil || !reflect.DeepEqual(eventsAfter, eventsBefore) {
		t.Fatalf("audit history changed: before=%d after=%d err=%v",
			len(eventsBefore), len(eventsAfter), err)
	}
	if err := verifySQLiteForeignKeys(ctx, upgraded.db); err != nil {
		t.Fatal(err)
	}
}

// Reuse the existing metadata-only browser identity fixture in a separate
// current-schema database. Only identity/acceptance values cross into this
// historical fixture; its Run and five-mode permission are seeded directly
// under the genuine v132 constraints without relaxing a trigger.
func legacyV132BrowserLaunchStoreFixture(t *testing.T, state *SQLiteStore) (
	browserruntime.SessionPlan, browserruntime.BrowserExecutableIdentity,
	browserruntime.BrowserAcceptanceCandidate, browserruntime.ProfileOwnershipPlan,
) {
	t.Helper()
	metadata, err := Open(filepath.Join(t.TempDir(), "browser-identity-fixture.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	_, identity, acceptance, _ := browserLaunchStoreFixture(t, metadata)
	workspace := WorkspaceRecord{ID: "workspace-browser-launch-store", Name: "browser-launch-store", RootPath: t.TempDir()}
	if err := state.SaveWorkspace(t.Context(), workspace); err != nil {
		t.Fatal(err)
	}
	run := seedLegacyStructuredToolRun(t, state, workspace.ID, domain.ExecutionPhaseDeliver, domain.RunExecutionPermissionConservative)
	plan, err := browserruntime.BuildSessionPlan(browserruntime.NewSessionPlanRequest{
		SessionID: run.SessionID, RunID: run.ID, WorkspaceID: workspace.ID,
		ProfileID: browserruntime.ProfileSafeWeb, Targets: []string{"https://example.test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ownership, err := browserruntime.BuildProfileOwnershipPlan(plan, identity,
		filepath.Join(t.TempDir(), browserruntime.ProfileRuntimeRootName))
	if err != nil {
		t.Fatal(err)
	}
	return plan, identity, acceptance, ownership
}

func BenchmarkSQLiteCleanInstallCreationPaths(b *testing.B) {
	plan := migrationPlan()
	for _, benchmark := range []struct {
		name string
		run  func(context.Context, *SQLiteStore) error
	}{
		{name: "consolidated-baseline", run: func(ctx context.Context, state *SQLiteStore) error {
			used, err := state.tryCleanInstallBaseline(ctx, plan)
			if err == nil && !used {
				return fmt.Errorf("baseline was not used")
			}
			return err
		}},
		{name: "historical-v1-to-latest", run: func(ctx context.Context, state *SQLiteStore) error {
			return state.applyMigrations(ctx, plan)
		}},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			root := b.TempDir()
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				state := openUnmigratedSQLiteStore(b,
					filepath.Join(root, fmt.Sprintf("iteration-%d.db", index)))
				if err := benchmark.run(context.Background(), state); err != nil {
					b.Fatal(err)
				}
				if err := state.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func currentCleanInstallBaselineArtifactForTest() cleanInstallBaselineArtifact {
	return cleanInstallBaselineArtifact{
		SchemaVersion:       cleanInstallBaselineSchemaVersion,
		SQL:                 cleanInstallBaselineSQL,
		SQLSHA256:           cleanInstallBaselineSQLSHA256,
		SchemaSHA256:        cleanInstallBaselineSchemaSHA256,
		MigrationPlanSHA256: cleanInstallBaselineMigrationPlanSHA256,
	}
}

func openUnmigratedSQLiteStore(t testing.TB, path string) *SQLiteStore {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", sqliteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.ExecContext(context.Background(), `PRAGMA foreign_keys = ON;`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	absolute, err := filepath.Abs(path)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return &SQLiteStore{db: db, home: filepath.Dir(absolute)}
}

func applyMigrationPrefixForTest(ctx context.Context, state *SQLiteStore,
	plan []migration, version int,
) error {
	if version < 1 || version > len(plan) {
		return fmt.Errorf("invalid migration fixture version %d", version)
	}
	if _, err := state.db.ExecContext(ctx, `CREATE TABLE schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		checksum TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	for _, item := range plan[:version] {
		if err := state.applyMigration(ctx, item); err != nil {
			return fmt.Errorf("apply migration %d: %w", item.Version, err)
		}
	}
	return nil
}

func assertLatestMigrationLedger(t *testing.T, state *SQLiteStore, plan []migration) {
	t.Helper()
	applied, err := state.loadAppliedMigrations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := validateMigrationPlan(plan, applied); err != nil {
		t.Fatal(err)
	}
	if len(applied) != LatestSchemaVersion {
		t.Fatalf("migration ledger entries=%d want=%d", len(applied), LatestSchemaVersion)
	}
}
