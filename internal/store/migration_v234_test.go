package store

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

func TestExplicitPreviewSchemaV234PreservesHistoryAndRecordsIntent(t *testing.T) {
	ctx := t.Context()
	st := openHistoricalTestDatabase(t, filepath.Join(t.TempDir(), "explicit-preview-v233.db"), 233)
	defer st.Close()
	_, run, err := application.NewRunService(st).Create(ctx, application.CreateRunRequest{
		Goal: "preserve an explicitly selected Preview", Profile: "code", Budget: domain.Budget{MaxTurns: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := st.GetRunExecutionProfile(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	next, err := initial.Next("explicit-preview-v234", domain.RunExecutionProfilePreview, "test_operator", "explicitly disable command execution", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertRunExecutionProfileSnapshotTx(ctx, tx, next); err == nil {
		t.Fatal("v233 unexpectedly recorded same-profile Preview intent")
	}
	_ = tx.Rollback()
	if err := st.applyMigration(ctx, migrationPlan()[233]); err != nil {
		t.Fatal(err)
	}
	retained, err := st.GetRunExecutionProfile(ctx, run.ID)
	if err != nil || !reflect.DeepEqual(retained, initial) {
		t.Fatalf("migration rewrote the old profile: %+v err=%v", retained, err)
	}
	request := application.ChangeRunExecutionProfileRequest{RunID: run.ID, Profile: "preview", OperationKey: "select-explicit-preview-v234",
		RequestedBy: "test_operator", Reason: "explicitly keep Preview in advanced settings"}
	selected, err := application.NewRunExecutionProfileService(st).Change(ctx, request)
	if err != nil || selected.Profile.Revision != 2 || selected.Profile.IsDefaultPreview() {
		t.Fatalf("explicit Preview remained indistinguishable from the initial default: %+v err=%v", selected, err)
	}
	replayed, err := application.NewRunExecutionProfileService(st).Change(ctx, request)
	if err != nil || !replayed.Replayed || replayed.Profile.ID != selected.Profile.ID {
		t.Fatalf("Preview selection was not idempotent: %+v err=%v", replayed, err)
	}
	if _, err := selected.Profile.Next("repeated-preview", domain.RunExecutionProfilePreview, "test_operator", "duplicate no-op", time.Now().UTC()); err == nil {
		t.Fatal("same-profile transition widened beyond the unselected initial Preview")
	}
	assertNoForeignKeyViolations(t, st.db)
}
