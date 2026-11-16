package store

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

func TestIdleThreadFullReactivationSchemaV233PreservesPermissionsAndFences(t *testing.T) {
	ctx := t.Context()
	st := openHistoricalTestDatabase(t, filepath.Join(t.TempDir(), "idle-full-v232.db"), 232)
	defer st.Close()
	_, run, err := application.NewRunService(st).Create(ctx, application.CreateRunRequest{
		Goal: "reactivate an idle Full chat", Profile: "code", Interactive: true, Budget: domain.Budget{MaxTurns: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	capabilities := domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
		RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
	selected, err := application.NewThreadExecutionPermissionService(st, capabilities).Change(ctx, application.ChangeThreadExecutionPermissionRequest{
		ThreadID: domain.InitialThreadID(run.ID), Mode: "full", ConfirmFull: true, OperationKey: "v232-initial-full",
		RequestedBy: "test_operator", Reason: "prepare an existing Full preference",
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := st.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = application.NewRunService(st).Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	next, err := before.Next("v233-full-reconfirmation", domain.RunExecutionPermissionFull, true,
		"test_operator", "explicit Full reconfirmation", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	insert := func(snapshot domain.RunExecutionPermissionSnapshot) error {
		tx, err := st.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := insertRunExecutionPermissionSnapshotTx(ctx, tx, snapshot); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err := insert(next); err == nil {
		t.Fatal("v232 unexpectedly allowed Full reconfirmation for a running chat")
	}
	if err := st.applyMigration(ctx, migrationPlan()[232]); err != nil {
		t.Fatal(err)
	}
	afterMigration, err := st.GetRunExecutionPermission(ctx, run.ID)
	threadAfterMigration, threadErr := st.GetThreadExecutionPermission(ctx, selected.Permission.ThreadID)
	if err != nil || threadErr != nil || !reflect.DeepEqual(afterMigration, before) || !reflect.DeepEqual(threadAfterMigration, selected.Permission) {
		t.Fatalf("v233 changed existing permission rows: run=%+v thread=%+v err=%v/%v", afterMigration, threadAfterMigration, err, threadErr)
	}
	lease, err := st.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "v233-concurrent-owner", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := insert(next); err == nil {
		t.Fatal("v233 allowed a running Full revision under an active execution lease")
	}
	if _, _, err := st.ReleaseRunExecutionLease(ctx, lease.Lease); err != nil {
		t.Fatal(err)
	}
	if err := insert(next); err != nil {
		t.Fatalf("v233 still rejects quiescent same-Full reconfirmation: %v", err)
	}
	_, askRun, err := application.NewRunService(st).Create(ctx, application.CreateRunRequest{
		Goal: "preserve running Ask upgrade rejection", Profile: "code", Budget: domain.Budget{MaxTurns: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	ask, err := st.GetRunExecutionPermission(ctx, askRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = application.NewRunService(st).Start(ctx, askRun.ID); err != nil {
		t.Fatal(err)
	}
	upgrade, err := ask.Next("v233-blocked-ask-upgrade", domain.RunExecutionPermissionFull, true,
		"test_operator", "a real running upgrade must remain blocked", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := insert(upgrade); err == nil {
		t.Fatal("v233 widened the running Ask-to-Full upgrade boundary")
	}
	if version, err := st.SchemaVersion(ctx); err != nil || version != 233 {
		t.Fatalf("schema version=%d err=%v", version, err)
	}
	assertNoForeignKeyViolations(t, st.db)
}
