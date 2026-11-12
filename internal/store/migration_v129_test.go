package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/session"
)

func TestSchemaV129BackfillsThreadsAndPreservesRollbackBackup(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "thread-v128.db")
	legacy := openSchemaV128Store(t, path)
	restoreLegacyInputs := addCurrentInputColumnsForLegacySeed(t, legacy)
	_, created, err := newMigrationFixtureRunService(t, legacy).Create(ctx,
		application.CreateRunRequest{Goal: "preserve historical task", Profile: "review",
			Budget: domain.Budget{MaxTurns: 3}})
	if err != nil {
		t.Fatal(err)
	}
	runs := newMigrationFixtureRunService(t, legacy)
	running, err := runs.Start(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := runs.Fail(ctx, running.ID, "fixture terminal")
	if err != nil {
		t.Fatal(err)
	}
	restoreLegacyInputs()
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dir, "thread-v128.backup.db")
	copySQLiteFixture(t, path, backup)

	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	threadRecord, err := upgraded.GetThreadByRun(ctx, terminal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if threadRecord.ID != domain.InitialThreadID(terminal.ID) ||
		threadRecord.LastRunID != terminal.ID || threadRecord.ActiveRunID != "" ||
		threadRecord.Status != domain.ThreadActive || threadRecord.Version != 1 {
		t.Fatalf("unexpected v129 backfill: %#v", threadRecord)
	}
	bindings, err := upgraded.ListThreadRuns(ctx, threadRecord.ID)
	if err != nil || len(bindings) != 1 || bindings[0].RunID != terminal.ID ||
		bindings[0].SessionID != terminal.SessionID || bindings[0].Ordinal != 1 {
		t.Fatalf("unexpected v129 binding: %#v err=%v", bindings, err)
	}
	if err := assertNoThreadOrphans(ctx, upgraded.db); err != nil {
		t.Fatal(err)
	}
	assertNoForeignKeyViolations(t, upgraded.db)
	if err := upgraded.Close(); err != nil {
		t.Fatal(err)
	}

	// v129 is forward-only. Rollback restores the exact pre-migration backup;
	// verify the backup remains v128 and contains no future tables.
	rollbackDB, err := sql.Open("sqlite3", sqliteDSN(backup))
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackDB.Close()
	var version int
	if err := rollbackDB.QueryRowContext(ctx,
		`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 128 {
		t.Fatalf("rollback backup version=%d want=128 err=%v", version, err)
	}
	var threadTables int
	if err := rollbackDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name IN ('threads','thread_runs','thread_events')`).
		Scan(&threadTables); err != nil || threadTables != 0 {
		t.Fatalf("rollback backup contains future Thread schema: count=%d err=%v",
			threadTables, err)
	}
	var preservedRuns int
	if err := rollbackDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE id = ?`,
		terminal.ID).Scan(&preservedRuns); err != nil || preservedRuns != 1 {
		t.Fatalf("rollback backup lost Run: count=%d err=%v", preservedRuns, err)
	}
}

func TestSchemaV129RepairsLegacyRunWithoutSession(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "thread-v128-unbound.db")
	legacy := openSchemaV128Store(t, path)
	_, run, err := newMigrationFixtureRunService(t, legacy).Create(ctx,
		application.CreateRunRequest{Goal: "recover legacy unbound Run", Profile: "learn",
			Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.db.ExecContext(ctx, `UPDATE runs SET session_id = NULL WHERE id = ?`,
		run.ID); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	wantSessionID := "thread-session-" + run.ID
	repaired, err := upgraded.GetRun(ctx, run.ID)
	if err != nil || repaired.SessionID != wantSessionID {
		t.Fatalf("repaired Run=%#v err=%v want session=%s", repaired, err, wantSessionID)
	}
	threadRecord, err := upgraded.GetThreadByRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := upgraded.ListThreadRuns(ctx, threadRecord.ID)
	if err != nil || len(bindings) != 1 || bindings[0].SessionID != wantSessionID {
		t.Fatalf("recovery bindings=%#v err=%v", bindings, err)
	}
	if _, err := upgraded.GetSession(ctx, wantSessionID); err != nil {
		t.Fatalf("recovery Session missing: %v", err)
	}
	assertNoForeignKeyViolations(t, upgraded.db)
}

func TestThreadLifecycleAndExportNeverOrphanHistory(t *testing.T) {
	ctx := context.Background()
	st := openWorkItemTestStore(t)
	_, run := createWorkItemTestRun(t, ctx, st, "lifecycle export")
	threadRecord, err := st.GetThreadByRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.TransitionThreadWithOperationKey(ctx, threadRecord.ID,
		domain.ThreadArchive, 0, "test_operator",
		"thread-lifecycle-missing-version-0001", timeNowUTC()); err == nil {
		t.Fatal("Thread lifecycle accepted a missing optimistic version")
	}
	for index := 0; index < 1005; index++ {
		message := session.NewMessage(run.SessionID, "user", fmt.Sprintf("message-%04d", index))
		if _, err := st.SaveSessionMessage(ctx, message); err != nil {
			t.Fatalf("save message %d: %v", index, err)
		}
	}
	archiveAt := timeNowUTC()
	archived, err := st.TransitionThreadWithOperationKey(ctx, threadRecord.ID,
		domain.ThreadArchive, threadRecord.Version, "test_operator",
		"thread-lifecycle-archive-operation-0001", archiveAt)
	if err != nil || archived.Status != domain.ThreadArchived {
		t.Fatalf("archive=%#v err=%v", archived, err)
	}
	cancelled, err := st.GetRun(ctx, run.ID)
	if err != nil || cancelled.Status != domain.RunCancelled || archived.ActiveRunID != "" {
		t.Fatalf("archive did not safely cancel created Run: Run=%#v Thread=%#v err=%v",
			cancelled, archived, err)
	}
	replayed, err := st.TransitionThreadWithOperationKey(ctx, threadRecord.ID,
		domain.ThreadArchive, threadRecord.Version, "test_operator",
		"thread-lifecycle-archive-operation-0001", archiveAt.Add(time.Hour))
	if err != nil || replayed.Status != archived.Status || replayed.Version != archived.Version ||
		!replayed.UpdatedAt.Equal(archived.UpdatedAt) {
		t.Fatalf("archive replay=%#v err=%v original=%#v", replayed, err, archived)
	}
	noOp, err := st.TransitionThreadWithOperationKey(ctx, threadRecord.ID,
		domain.ThreadArchive, archived.Version, "test_operator",
		"thread-lifecycle-archive-noop-0001", archiveAt.Add(2*time.Hour))
	if err != nil || noOp.Version != archived.Version || !noOp.UpdatedAt.Equal(archived.UpdatedAt) {
		t.Fatalf("archive no-op=%#v err=%v original=%#v", noOp, err, archived)
	}
	if _, err := st.TransitionThreadWithOperationKey(ctx, threadRecord.ID,
		domain.ThreadRestore, archived.Version, "test_operator",
		"thread-lifecycle-archive-noop-0001", archiveAt.Add(3*time.Hour)); err == nil {
		t.Fatal("no-op lifecycle idempotency key was reusable for another request")
	}
	restored, err := st.TransitionThread(ctx, archived.ID, domain.ThreadRestore,
		archived.Version, "test_operator", timeNowUTC())
	if err != nil || restored.Status != domain.ThreadActive {
		t.Fatalf("restore=%#v err=%v", restored, err)
	}
	exported, err := st.ExportThread(ctx, restored.ID)
	if err != nil || len(exported.Runs) != 1 || len(exported.Sessions) != 1 ||
		len(exported.Bindings) != 1 || exported.Sessions[0].ID != run.SessionID ||
		len(exported.Messages) != maxThreadExportSection || !exported.MessagesTruncated ||
		len(exported.Events) < 3 ||
		len(exported.AuditEvents) == 0 || exported.Thread.ID != restored.ID {
		t.Fatalf("export=%#v err=%v", exported, err)
	}
	// The export's message section is bounded at the cap; the migrated
	// history itself stays whole through the uncapped page path.
	fullMessages, err := st.ListThreadMessagesPage(ctx, restored.ID, true, 0, 0)
	if err != nil || len(fullMessages) != 1005 {
		t.Fatalf("migrated message history orphaned: %d %v", len(fullMessages), err)
	}
	if exported.Messages[0].ProvenanceVersion != session.ContextProvenanceVersion ||
		exported.Messages[0].SourceKind != session.SourceOperatorMessage ||
		exported.Messages[0].ContentSHA256 == "" ||
		!exported.Messages[0].InstructionAuthorized {
		t.Fatalf("export message provenance=%#v", exported.Messages[0])
	}
	terminalThread, err := st.GetThread(ctx, restored.ID)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := st.TransitionThread(ctx, restored.ID, domain.ThreadDelete,
		terminalThread.Version, "test_operator", timeNowUTC())
	if err != nil || deleted.Status != domain.ThreadDeleted {
		t.Fatalf("delete=%#v err=%v", deleted, err)
	}
	afterDelete, err := st.ExportThread(ctx, deleted.ID)
	if err != nil || len(afterDelete.Runs) != 1 || len(afterDelete.Sessions) != 1 ||
		len(afterDelete.Bindings) != 1 ||
		len(afterDelete.Messages) != maxThreadExportSection || !afterDelete.MessagesTruncated ||
		len(afterDelete.AuditEvents) == 0 ||
		len(afterDelete.Events) < 4 {
		t.Fatalf("deleted export=%#v err=%v", afterDelete, err)
	}
	if err := assertNoThreadOrphans(ctx, st.db); err != nil {
		t.Fatal(err)
	}
}

func openSchemaV128Store(t testing.TB, path string) *SQLiteStore {
	t.Helper()
	state := openHistoricalTestDatabase(t, path, 128)
	if _, err := state.db.Exec(`PRAGMA busy_timeout = 5000;`); err != nil {
		t.Fatal(err)
	}
	return state
}

func copySQLiteFixture(t testing.TB, source, target string) {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertNoThreadOrphans(ctx context.Context, db *sql.DB) error {
	checks := []string{
		`SELECT COUNT(*) FROM thread_runs binding LEFT JOIN threads thread_record
			ON thread_record.id = binding.thread_id WHERE thread_record.id IS NULL`,
		`SELECT COUNT(*) FROM thread_runs binding LEFT JOIN runs run
			ON run.id = binding.run_id WHERE run.id IS NULL`,
		`SELECT COUNT(*) FROM thread_runs binding LEFT JOIN sessions session_record
			ON session_record.id = binding.session_id WHERE session_record.id IS NULL`,
		`SELECT COUNT(*) FROM session_messages message JOIN thread_runs binding
			ON binding.session_id = message.session_id LEFT JOIN threads thread_record
			ON thread_record.id = binding.thread_id WHERE thread_record.id IS NULL`,
		`SELECT COUNT(*) FROM thread_events event LEFT JOIN threads thread_record
			ON thread_record.id = event.thread_id WHERE thread_record.id IS NULL`,
	}
	for _, query := range checks {
		var count int
		if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("Thread orphan check returned %d for %s", count, query)
		}
	}
	return nil
}

func timeNowUTC() time.Time { return time.Now().UTC() }
