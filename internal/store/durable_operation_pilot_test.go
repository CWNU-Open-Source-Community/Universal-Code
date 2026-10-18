package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/scheduler"
)

type durableOperationPilotClock struct{ now time.Time }

func (c durableOperationPilotClock) Now() time.Time { return c.now }
func (c durableOperationPilotClock) NewTimer(delay time.Duration) scheduler.Timer {
	return durableOperationPilotTimer{timer: time.NewTimer(delay)}
}

type durableOperationPilotTimer struct{ timer *time.Timer }

func (t durableOperationPilotTimer) C() <-chan time.Time            { return t.timer.C }
func (t durableOperationPilotTimer) Reset(delay time.Duration) bool { return t.timer.Reset(delay) }
func (t durableOperationPilotTimer) Stop() bool                     { return t.timer.Stop() }

func TestDurableOperationPilotsPreserveLegacyIdentityAcrossMigrationAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "durable-operation-pilots-v122.db")
	seedPath := filepath.Join(t.TempDir(), "seed.db")
	state := openHistoricalTestDatabase(t, seedPath, 177)

	workspace := WorkspaceRecord{
		ID: "workspace-controlled-create", Name: "controlled-create",
		RootPath: t.TempDir(), CreatedAt: time.Now().UTC(),
	}
	if err := state.SaveWorkspace(ctx, workspace); err != nil {
		_ = state.Close()
		t.Fatal(err)
	}
	runRequest := application.ControlledRunCreationRequest{
		Version: domain.RunCreationProtocolVersion, Goal: "Implement the parser",
		WorkspaceID: workspace.ID, Profile: "code", Surface: "code", Phase: "plan",
		OperationKey: "run-create-operation-0001", RequestedBy: "http_control",
	}
	createdRun, err := application.NewControlledRunCreationService(legacyControlledRunSeedStore{state}).Create(ctx,
		runRequest)
	if err != nil || createdRun.Replayed {
		_ = state.Close()
		t.Fatalf("create controlled Run: replayed=%t err=%v", createdRun.Replayed, err)
	}
	runKeyDigest := runmutation.RunCreationOperationDigest(runRequest.OperationKey)
	if runKeyDigest != "9b2ff570e61ef169aa472e8efa7681b161e11a03c72890feb35321d07f74e82d" {
		_ = state.Close()
		t.Fatalf("legacy Run creation key digest changed: %s", runKeyDigest)
	}
	runOperationBefore, found, err := state.GetRunCreationOperation(ctx, runKeyDigest)
	if err != nil || !found ||
		runOperationBefore.RequestFingerprint !=
			"dd5310aa4cfa7866278471cebd0f680e0586d01760e70932e46fb47e88fcef4e" {
		_ = state.Close()
		t.Fatalf("legacy Run creation operation=%#v found=%t err=%v",
			runOperationBefore, found, err)
	}
	// The legacy Run writer requires v177, but the current scheduling service
	// reads the latest schema. Upgrade only the donor; the migration under test
	// still starts from the real v122 database imported below.
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = Open(seedPath)
	if err != nil {
		t.Fatal(err)
	}
	currentSeed := state
	t.Cleanup(func() { _ = currentSeed.Close() })

	anchor := time.Now().UTC().Add(time.Second).Truncate(time.Millisecond)
	clock := durableOperationPilotClock{now: anchor}
	scheduledRequest := application.CreateScheduledJobRequest{
		Version: domain.ScheduledJobProtocolVersion,
		RunID:   createdRun.Run.ID, TargetRunID: createdRun.Run.ID,
		Schedule: domain.ScheduledJobSchedule{
			Kind: domain.ScheduledJobPeriodic, Timezone: "UTC", AnchorAt: anchor,
			IntervalSeconds: 60, MisfirePolicy: domain.ScheduledJobMisfireRunOnce,
		},
		DeadlineAt: anchor.Add(time.Hour), StopOnTargetTerminal: true,
		MaxRounds: 4, MaxModelCalls: 1, MaxElapsedSeconds: 3600,
		Retry: domain.ScheduledJobRetryPolicy{
			MaxAttempts: 3, InitialBackoffSeconds: 1, MaxBackoffSeconds: 10,
		},
		Notification:  domain.ScheduledJobNotifyFailure,
		ExecutionMode: domain.ScheduledJobReadOnly,
		OperationKey:  "scheduled-pilot-create-operation-0001", RequestedBy: "operator",
	}
	scheduledService := application.NewScheduledJobService(state).WithClock(clock)
	createdJob, err := scheduledService.Create(ctx, scheduledRequest)
	if err != nil || createdJob.Replayed {
		_ = state.Close()
		t.Fatalf("create scheduled job: replayed=%t err=%v", createdJob.Replayed, err)
	}
	pauseRequest := application.TransitionScheduledJobRequest{
		Version: domain.ScheduledJobControlProtocolVersion,
		RunID:   createdRun.Run.ID, JobID: createdJob.Job.ID,
		Action: domain.ScheduledJobPause, ExpectedRevision: createdJob.Job.Revision,
		OperationKey: "scheduled-pilot-pause-operation-0001", RequestedBy: "operator",
	}
	pausedJob, err := scheduledService.Transition(ctx, pauseRequest)
	if err != nil || pausedJob.Replayed || pausedJob.Job.Status != domain.ScheduledJobPaused {
		_ = state.Close()
		t.Fatalf("pause scheduled job: result=%#v err=%v", pausedJob, err)
	}
	createKeyDigest := runmutation.ScheduledJobOperationDigest(
		scheduledRequest.RunID, scheduledRequest.OperationKey)
	pauseKeyDigest := runmutation.ScheduledJobOperationDigest(
		pauseRequest.RunID, pauseRequest.OperationKey)
	createOperationBefore, found, err := state.GetScheduledJobOperation(ctx,
		createKeyDigest)
	if err != nil || !found {
		_ = state.Close()
		t.Fatalf("scheduled create operation=%#v found=%t err=%v",
			createOperationBefore, found, err)
	}
	pauseOperationBefore, found, err := state.GetScheduledJobOperation(ctx,
		pauseKeyDigest)
	if err != nil || !found {
		_ = state.Close()
		t.Fatalf("scheduled pause operation=%#v found=%t err=%v",
			pauseOperationBefore, found, err)
	}
	eventsBefore := durableOperationPilotEventCount(t, state, createdRun.Run.ID)

	state = historicalTestDatabaseFromSeed(t, state, path, 122)
	if version, err := state.SchemaVersion(ctx); err != nil || version != 122 {
		_ = state.Close()
		t.Fatalf("legacy schema version=%d want=122 err=%v", version, err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if version, err := upgraded.SchemaVersion(ctx); err != nil || version != LatestSchemaVersion {
		t.Fatalf("upgraded schema version=%d want=%d err=%v",
			version, LatestSchemaVersion, err)
	}
	replayedRun, err := application.NewControlledRunCreationService(upgraded).Create(ctx,
		runRequest)
	if err != nil || !replayedRun.Replayed || replayedRun.Run.ID != createdRun.Run.ID ||
		replayedRun.Mission.ID != createdRun.Mission.ID ||
		replayedRun.Session.ID != createdRun.Session.ID {
		t.Fatalf("replayed controlled Run=%#v err=%v", replayedRun, err)
	}
	upgradedScheduledService := application.NewScheduledJobService(upgraded).WithClock(clock)
	replayedCreate, err := upgradedScheduledService.Create(ctx, scheduledRequest)
	if err != nil || !replayedCreate.Replayed || replayedCreate.Job.ID != createdJob.Job.ID ||
		replayedCreate.Job.Status != domain.ScheduledJobPaused {
		t.Fatalf("replayed scheduled create=%#v err=%v", replayedCreate, err)
	}
	replayedPause, err := upgradedScheduledService.Transition(ctx, pauseRequest)
	if err != nil || !replayedPause.Replayed || replayedPause.Job.ID != createdJob.Job.ID ||
		replayedPause.Job.Status != domain.ScheduledJobPaused {
		t.Fatalf("replayed scheduled pause=%#v err=%v", replayedPause, err)
	}

	changedRun := runRequest
	changedRun.Goal = "Implement a different parser"
	if _, err := application.NewControlledRunCreationService(upgraded).Create(ctx,
		changedRun); apperror.CodeOf(err) != apperror.CodeConflict ||
		err.Error() != "Run creation idempotency key was already used for a different request" {
		t.Fatalf("changed Run request code=%s err=%v", apperror.CodeOf(err), err)
	}
	changedCreate := scheduledRequest
	changedCreate.MaxRounds++
	if _, err := upgradedScheduledService.Create(ctx,
		changedCreate); apperror.CodeOf(err) != apperror.CodeConflict ||
		err.Error() != "scheduled job operation key was already used for different intent" {
		t.Fatalf("changed scheduled create code=%s err=%v", apperror.CodeOf(err), err)
	}
	changedPause := pauseRequest
	changedPause.ExpectedRevision++
	if _, err := upgradedScheduledService.Transition(ctx,
		changedPause); apperror.CodeOf(err) != apperror.CodeConflict ||
		err.Error() != "scheduled job operation key was already used for different intent" {
		t.Fatalf("changed scheduled pause code=%s err=%v", apperror.CodeOf(err), err)
	}

	runOperationAfter, found, err := upgraded.GetRunCreationOperation(ctx, runKeyDigest)
	if err != nil || !found || !reflect.DeepEqual(runOperationAfter, runOperationBefore) {
		t.Fatalf("Run operation after upgrade=%#v want=%#v found=%t err=%v",
			runOperationAfter, runOperationBefore, found, err)
	}
	createOperationAfter, found, err := upgraded.GetScheduledJobOperation(ctx,
		createKeyDigest)
	if err != nil || !found || !reflect.DeepEqual(createOperationAfter, createOperationBefore) {
		t.Fatalf("scheduled create operation after upgrade=%#v want=%#v found=%t err=%v",
			createOperationAfter, createOperationBefore, found, err)
	}
	pauseOperationAfter, found, err := upgraded.GetScheduledJobOperation(ctx,
		pauseKeyDigest)
	if err != nil || !found || !reflect.DeepEqual(pauseOperationAfter, pauseOperationBefore) {
		t.Fatalf("scheduled pause operation after upgrade=%#v want=%#v found=%t err=%v",
			pauseOperationAfter, pauseOperationBefore, found, err)
	}
	if eventsAfter := durableOperationPilotEventCount(t, upgraded,
		createdRun.Run.ID); eventsAfter != eventsBefore {
		t.Fatalf("replay appended events: before=%d after=%d", eventsBefore, eventsAfter)
	}
	var runOperations, scheduledOperations int
	if err := upgraded.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM run_creation_operations`).Scan(&runOperations); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM scheduled_job_operations`).Scan(&scheduledOperations); err != nil {
		t.Fatal(err)
	}
	if runOperations != 1 || scheduledOperations != 2 {
		t.Fatalf("operation counts changed: Run=%d scheduled=%d",
			runOperations, scheduledOperations)
	}
	assertNoForeignKeyViolations(t, upgraded.db)
}

func durableOperationPilotEventCount(t testing.TB, state *SQLiteStore,
	runID string,
) int {
	t.Helper()
	var count int
	if err := state.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM run_events WHERE run_id = ?`, runID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
