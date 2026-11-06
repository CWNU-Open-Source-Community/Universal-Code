package application_test

import (
	"context"
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/store"
)

func TestRunWakeControlSchedulesReplaysAndCancelsWithoutExecution(t *testing.T) {
	ctx := context.Background()
	state, err := store.Open(filepath.Join(t.TempDir(), "wake-control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	_, created, err := application.NewRunService(state).Create(ctx,
		application.CreateRunRequest{Goal: "wake control", Profile: "code",
			Budget: domain.Budget{MaxTurns: 4}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := application.NewRunService(state).Start(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.EnqueueOperatorSteering(ctx, domain.EnqueueOperatorSteeringRequest{
		RunID: run.ID, SessionID: run.SessionID, Content: "queued wake work",
		OperationKey: "wake-control-steering-operation-0001", RequestedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	service := application.NewRunWakeControlService(state)
	request := application.ScheduleRunWakeRequest{
		Version: domain.RunWakeControlProtocolVersion, RunID: run.ID,
		OperationKey: "wake-control-schedule-operation-0001", RequestedBy: "operator",
		MaxAttempts: 3, InitialDelaySeconds: 0, BaseBackoffSeconds: 5,
		MaxBackoffSeconds: 30, MaxElapsedSeconds: 120,
	}
	invalidTimeline := request
	invalidTimeline.OperationKey = "wake-control-invalid-timeline-0001"
	invalidTimeline.InitialDelaySeconds = 121
	if _, err := service.Schedule(ctx, invalidTimeline); apperror.CodeOf(err) !=
		apperror.CodeInvalidArgument {
		t.Fatalf("invalid wake timeline code=%s err=%v", apperror.CodeOf(err), err)
	}
	result, err := service.Schedule(ctx, request)
	if err != nil || result.Replayed || result.Intent.Status != domain.RunWakeQueued ||
		result.Intent.ExecutionEnabled || result.Intent.BackgroundLoopEnabled {
		t.Fatalf("schedule result=%#v err=%v", result, err)
	}
	replay, err := service.Schedule(ctx, request)
	if err != nil || !replay.Replayed || replay.Intent.ID != result.Intent.ID {
		t.Fatalf("schedule replay=%#v err=%v", replay, err)
	}
	changed := request
	changed.MaxAttempts = 4
	if _, err := service.Schedule(ctx, changed); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("changed same-key schedule code=%s err=%v", apperror.CodeOf(err), err)
	}
	cancelled, err := service.Cancel(ctx, application.CancelRunWakeRequest{
		Version: domain.RunWakeControlProtocolVersion, RunID: run.ID,
		OperationKey: "wake-control-cancel-operation-0001", RequestedBy: "operator",
	})
	if err != nil || cancelled.Replayed ||
		cancelled.Intent.Status != domain.RunWakeCancelled {
		t.Fatalf("cancel result=%#v err=%v", cancelled, err)
	}
	loaded, found, err := service.Get(ctx, run.ID)
	if err != nil || !found || loaded.Status != domain.RunWakeCancelled ||
		loaded.ExecutionEnabled || loaded.BackgroundLoopEnabled {
		t.Fatalf("loaded intent=%#v found=%t err=%v", loaded, found, err)
	}
}

func TestRunWakeCoordinatorClaimsMetadataOnlyOwnership(t *testing.T) {
	ctx := context.Background()
	state, err := store.Open(filepath.Join(t.TempDir(), "wake-coordinator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	_, created, err := application.NewRunService(state).Create(ctx,
		application.CreateRunRequest{Goal: "wake owner", Profile: "code",
			Budget: domain.Budget{MaxTurns: 4}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := application.NewRunService(state).Start(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.EnqueueOperatorSteering(ctx, domain.EnqueueOperatorSteeringRequest{
		RunID: run.ID, SessionID: run.SessionID, Content: "claim wake work",
		OperationKey: "wake-owner-steering-operation-0001", RequestedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	control := application.NewRunWakeControlService(state)
	scheduled, err := control.Schedule(ctx, application.ScheduleRunWakeRequest{
		Version: domain.RunWakeControlProtocolVersion, RunID: run.ID,
		OperationKey: "wake-owner-schedule-operation-0001", RequestedBy: "operator",
		MaxAttempts: 2, InitialDelaySeconds: 0, BaseBackoffSeconds: 5,
		MaxBackoffSeconds: 10, MaxElapsedSeconds: 120,
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator := application.NewRunWakeCoordinator(state)
	due, err := coordinator.Due(ctx, 8, 1)
	if err != nil || len(due) != 1 || due[0].ID != scheduled.Intent.ID {
		t.Fatalf("due intents=%#v err=%v", due, err)
	}
	intent, lease, acquired, err := coordinator.Claim(ctx, scheduled.Intent.ID,
		"scheduler-owner")
	if err != nil || !acquired || intent.Status != domain.RunWakeLeased ||
		lease.Status != domain.RunWakeLeaseActive || intent.ExecutionEnabled ||
		intent.BackgroundLoopEnabled {
		t.Fatalf("claim intent=%#v lease=%#v acquired=%t err=%v",
			intent, lease, acquired, err)
	}
	if _, _, acquired, err := coordinator.Claim(ctx, scheduled.Intent.ID,
		"other-owner"); err != nil || acquired {
		t.Fatalf("second claim acquired=%t err=%v", acquired, err)
	}
}
