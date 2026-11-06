package store

import (
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

func TestRunWakeDueSkipsUnavailableRunsBeforeLimit(t *testing.T) {
	for _, unavailable := range []string{"terminal", "no_pending", "live_lease"} {
		t.Run(unavailable, func(t *testing.T) {
			ctx := t.Context()
			state := openWorkItemTestStore(t)
			blocked := createEligibleRunWakeTestRun(t, ctx, state)
			healthy := createEligibleRunWakeTestRun(t, ctx, state)
			now := time.Now().UTC()
			blockedWake, operation := newRunWakeTestSchedule(blocked, now, 3)
			if _, _, _, err := state.CreateRunWakeIntent(ctx, blockedWake, operation); err != nil {
				t.Fatal(err)
			}
			healthyWake, operation := newRunWakeTestSchedule(healthy, now.Add(time.Millisecond), 3)
			if _, _, _, err := state.CreateRunWakeIntent(ctx, healthyWake, operation); err != nil {
				t.Fatal(err)
			}
			var lease domain.RunExecutionLease
			switch unavailable {
			case "terminal":
				if _, err := application.NewRunService(state).Cancel(ctx, blocked.ID); err != nil {
					t.Fatal(err)
				}
			case "no_pending":
				messages, err := state.ListOperatorSteering(ctx, blocked.ID, 1)
				if err != nil || len(messages) != 1 {
					t.Fatalf("messages=%#v err=%v", messages, err)
				}
				if _, err := state.CancelOperatorSteering(ctx, domain.CancelOperatorSteeringRequest{
					MessageID: messages[0].ID, OperationKey: "cancel-last-message",
					RequestedBy: "operator", Reason: "instruction withdrawn",
				}); err != nil {
					t.Fatal(err)
				}
			case "live_lease":
				lease = acquireTestRunExecutionLease(t, ctx, state, blocked.ID)
			}
			dueAt := now.Add(2 * time.Second)
			due, err := state.ListDueRunWakeIntents(ctx, dueAt, 1)
			if err != nil || len(due) != 1 || due[0].ID != healthyWake.ID {
				t.Fatalf("unavailable Run hid healthy wake: due=%#v err=%v", due, err)
			}
			stored, err := state.GetRunWakeIntent(ctx, blockedWake.ID)
			if err != nil || stored.Status != domain.RunWakeQueued || stored.AttemptCount != 0 {
				t.Fatalf("candidate read changed blocked wake: %#v err=%v", stored, err)
			}
			if unavailable == "live_lease" {
				if _, _, err := state.ReleaseRunExecutionLease(ctx, lease); err != nil {
					t.Fatal(err)
				}
				due, err := state.ListDueRunWakeIntents(ctx, dueAt, 1)
				if err != nil || len(due) != 1 || due[0].ID != blockedWake.ID {
					t.Fatalf("released lease did not unblock original wake: due=%#v err=%v", due, err)
				}
			}
		})
	}
}

func TestRunWakeDueRetainsExpiredPausedWakeForExhaustion(t *testing.T) {
	for _, status := range []domain.RunWakeStatus{domain.RunWakeQueued, domain.RunWakeLeased} {
		t.Run(string(status), func(t *testing.T) {
			ctx := t.Context()
			state := openWorkItemTestStore(t)
			run := createEligibleRunWakeTestRun(t, ctx, state)
			intent, operation := newRunWakeTestSchedule(run, time.Now().UTC(), 3)
			if _, _, _, err := state.CreateRunWakeIntent(ctx, intent, operation); err != nil {
				t.Fatal(err)
			}
			attempts := 0
			if status == domain.RunWakeLeased {
				if _, _, acquired, err := state.AcquireRunWake(ctx, intent.ID,
					"old-worker", "old-lease", intent.NextWakeAt); err != nil || !acquired {
					t.Fatalf("old lease acquired=%t err=%v", acquired, err)
				}
				attempts = 1
			}
			if _, err := application.NewRunService(state).Pause(ctx, run.ID); err != nil {
				t.Fatal(err)
			}
			due, err := state.ListDueRunWakeIntents(ctx, intent.DeadlineAt, 1)
			if err != nil || len(due) != 1 || due[0].ID != intent.ID {
				t.Fatalf("expired paused wake hidden from cleanup: due=%#v err=%v", due, err)
			}
			settled, _, acquired, err := state.AcquireRunWake(ctx, intent.ID,
				"expiry-worker", "expiry-lease", intent.DeadlineAt)
			if err != nil || acquired || settled.Status != domain.RunWakeExhausted || settled.AttemptCount != attempts {
				t.Fatalf("expired wake did not settle without execution: %#v acquired=%t err=%v", settled, acquired, err)
			}
			due, err = state.ListDueRunWakeIntents(ctx, intent.DeadlineAt, 1)
			if err != nil || len(due) != 0 {
				t.Fatalf("settled wake remained due: due=%#v err=%v", due, err)
			}
		})
	}
}
