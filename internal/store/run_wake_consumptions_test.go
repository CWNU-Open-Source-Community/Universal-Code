package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
)

func TestPreparedRunWakeConsumptionPreventsExpiredLeaseReclaim(t *testing.T) {
	ctx := context.Background()
	state := openWorkItemTestStore(t)
	run := createEligibleRunWakeTestRun(t, ctx, state)
	now := time.Now().UTC()
	intent, operation := newRunWakeTestSchedule(run, now, 3)
	if _, _, _, err := state.CreateRunWakeIntent(ctx, intent, operation); err != nil {
		t.Fatal(err)
	}
	leased, lease, acquired, err := state.AcquireRunWake(ctx, intent.ID,
		"wake-prepared-owner", "wake-prepared-lease", intent.NextWakeAt)
	if err != nil || !acquired {
		t.Fatalf("wake acquisition failed: acquired=%t err=%v", acquired, err)
	}
	operationKey := runmutation.RunWakeConsumptionOperationKey(intent.ID,
		lease.Generation)
	prepared, replayed, err := state.PrepareRunWakeConsumption(ctx,
		domain.RunWakeConsumption{
			ID:              idgen.New("wake-consume"),
			ProtocolVersion: domain.RunWakeConsumptionProtocolVersion,
			IntentID:        leased.ID, RunID: leased.RunID, SessionID: leased.SessionID,
			LeaseID: lease.ID, Generation: lease.Generation, OwnerID: lease.OwnerID,
			HandoffOperationKeyDigest: runmutation.RunExecutionHandoffOperationDigest(
				leased.RunID, operationKey),
			MaxSteps: 1, Status: domain.RunWakeConsumptionPrepared,
			CreatedAt: lease.AcquiredAt.Add(time.Millisecond),
		})
	if err != nil || replayed {
		t.Fatalf("prepare=%#v replayed=%t err=%v", prepared, replayed, err)
	}
	cancelAt := prepared.CreatedAt.Add(time.Millisecond)
	cancelKey := "wake-prepared-cancel-operation-0001"
	_, _, _, err = state.CancelRunWakeIntent(ctx, run.ID, cancelAt,
		domain.RunWakeOperation{
			ProtocolVersion: domain.RunWakeControlProtocolVersion,
			KeyDigest:       runmutation.RunWakeOperationDigest(run.ID, cancelKey),
			RequestFingerprint: runmutation.RunWakeCancelRequestFingerprint(run.ID,
				"operator"),
			Action: domain.RunWakeCancel, RunID: run.ID, RequestedBy: "operator",
			CreatedAt: cancelAt,
		})
	if apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("prepared Run wake cancellation code=%s err=%v",
			apperror.CodeOf(err), err)
	}
	tx, err := state.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancelEvent, err := appendRunWakeEventTx(ctx, tx, run.ID,
		events.RunWakeCancelledEvent, "run_wake_control", intent.ID,
		map[string]any{
			"attempt_count": lease.Generation, "execution_started": false,
			"model_called": false, "tool_called": false,
		}, cancelAt)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	_ = cancelEvent
	if _, err := tx.ExecContext(ctx, `UPDATE run_wake_intents SET status = 'cancelled',
		active_lease_id = NULL, updated_at = ?, cancelled_at = ? WHERE id = ?`,
		ts(cancelAt), ts(cancelAt), intent.ID); err == nil {
		_ = tx.Rollback()
		t.Fatal("SQLite trigger accepted cancellation of a prepared Run wake consumption")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	afterExpiry := lease.ExpiresAt.Add(time.Second)
	reconciled, replacement, reacquired, err := state.AcquireRunWake(ctx, intent.ID,
		"wake-second-owner", "wake-second-lease", afterExpiry)
	if err != nil || reacquired || replacement.ID != "" ||
		reconciled.Status != domain.RunWakeLeased ||
		reconciled.AttemptCount != lease.Generation ||
		reconciled.ActiveLeaseID != lease.ID {
		t.Fatalf("prepared generation was reclaimed: intent=%#v lease=%#v acquired=%t err=%v",
			reconciled, replacement, reacquired, err)
	}
	var leaseStatus string
	if err := state.db.QueryRowContext(ctx,
		`SELECT status FROM run_wake_leases WHERE id = ?`, lease.ID).Scan(&leaseStatus); err != nil || leaseStatus != string(domain.RunWakeLeaseActive) {
		t.Fatalf("prepared lease was mutated: status=%q err=%v", leaseStatus, err)
	}
	if _, err := application.NewRunService(state).Pause(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if due, err := state.ListDueRunWakeIntents(ctx, afterExpiry, 1); err != nil || len(due) != 0 {
		t.Fatalf("paused unresolved handoff remained runnable: due=%#v err=%v", due, err)
	}
	if _, err := application.NewRunService(state).Resume(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if due, err := state.ListDueRunWakeIntents(ctx, afterExpiry, 1); err != nil || len(due) != 1 || due[0].ID != intent.ID || due[0].AttemptCount != lease.Generation {
		t.Fatalf("resumed prepared generation was hidden or replaced: due=%#v err=%v", due, err)
	}
}

func TestRunWakeFailureEventUsesPersistedHandoffCallFacts(t *testing.T) {
	ctx := context.Background()
	state := openWorkItemTestStore(t)
	run := createEligibleRunWakeTestRun(t, ctx, state)
	now := time.Now().UTC()
	intent, wakeOperation := newRunWakeTestSchedule(run, now, 2)
	if _, _, _, err := state.CreateRunWakeIntent(ctx, intent, wakeOperation); err != nil {
		t.Fatal(err)
	}
	leased, wakeLease, acquired, err := state.AcquireRunWake(ctx, intent.ID,
		"wake-audit-owner", "wake-audit-lease", intent.NextWakeAt)
	if err != nil || !acquired {
		t.Fatalf("wake acquisition failed: acquired=%t err=%v", acquired, err)
	}
	operationKey := runmutation.RunWakeConsumptionOperationKey(intent.ID,
		wakeLease.Generation)
	consumption, _, err := state.PrepareRunWakeConsumption(ctx,
		domain.RunWakeConsumption{
			ID:              idgen.New("wake-consume"),
			ProtocolVersion: domain.RunWakeConsumptionProtocolVersion,
			IntentID:        leased.ID, RunID: leased.RunID, SessionID: leased.SessionID,
			LeaseID: wakeLease.ID, Generation: wakeLease.Generation,
			OwnerID: wakeLease.OwnerID,
			HandoffOperationKeyDigest: runmutation.RunExecutionHandoffOperationDigest(
				leased.RunID, operationKey),
			MaxSteps: 1, Status: domain.RunWakeConsumptionPrepared,
			CreatedAt: wakeLease.AcquiredAt.Add(time.Millisecond),
		})
	if err != nil {
		t.Fatal(err)
	}
	handoff, _, err := state.PrepareRunExecutionHandoff(ctx,
		domain.RunExecutionHandoffOperation{
			ID:              idgen.New("run-handoff"),
			ProtocolVersion: domain.RunExecutionHandoffProtocolVersion,
			KeyDigest:       consumption.HandoffOperationKeyDigest,
			RequestFingerprint: runmutation.RunExecutionHandoffRequestFingerprint(
				run.ID, "run_wake_consumer", 1),
			RunID: run.ID, SessionID: run.SessionID, RequestedBy: "run_wake_consumer",
			MaxSteps: 1, CreatedAt: now.Add(2 * time.Millisecond),
		})
	if err != nil || handoff.Result != nil || len(handoff.Items) != 1 {
		t.Fatalf("handoff=%#v err=%v", handoff, err)
	}
	if _, _, _, err := state.FailRunWakeConsumption(ctx, consumption.ID,
		handoff.Operation.ID, "pending_handoff", "conflict",
		now.Add(3*time.Millisecond)); err == nil {
		t.Fatal("Run wake failure accepted a handoff with an unknown result")
	}
	executionLease := acquireTestRunExecutionLease(t, ctx, state, run.ID)
	result, _, err := state.CompleteRunExecutionHandoff(ctx, handoff.Operation.ID,
		executionLease, domain.RunExecutionHandoffFailed, "model_failure", "internal",
		0, true, false)
	if err != nil || !result.ModelCalled || result.ToolCalled {
		t.Fatalf("handoff result=%#v err=%v", result, err)
	}
	failedAt := time.Now().UTC().Add(time.Second)
	tx, err := state.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	falseEvent, err := appendRunWakeEventTx(ctx, tx, run.ID,
		events.RunWakeRetriedEvent, "run_wake_coordinator", intent.ID,
		map[string]any{
			"generation": wakeLease.Generation, "attempt_count": wakeLease.Generation,
			"backoff_seconds": 5, "stop_reason": "model_failure",
			"error_code": "internal", "handoff_operation_id": handoff.Operation.ID,
			"execution_started": true, "model_called": false, "tool_called": false,
		}, failedAt)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_wake_consumptions
		SET status = 'failed', handoff_operation_id = ?, stop_reason = 'model_failure',
			error_code = 'internal', completion_event_sequence = ?, completed_at = ?
		WHERE id = ?`, handoff.Operation.ID, falseEvent.Sequence, ts(failedAt),
		consumption.ID); err == nil {
		_ = tx.Rollback()
		t.Fatal("SQLite trigger accepted false model-call facts for a failed handoff")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	failed, _, _, err := state.FailRunWakeConsumption(ctx, consumption.ID,
		handoff.Operation.ID, "model_failure", "internal", failedAt.Add(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	timeline, err := state.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	for _, event := range timeline {
		if event.Sequence == failed.CompletionEventSequence {
			if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if payload == nil || payload["model_called"] != true ||
		payload["tool_called"] != false {
		t.Fatalf("wake failure audit payload=%#v", payload)
	}
}

func TestRunWakeFailureRejectsUnrelatedHandoffAtServiceAndSQLBoundaries(t *testing.T) {
	ctx := context.Background()
	state := openWorkItemTestStore(t)
	run := createEligibleRunWakeTestRun(t, ctx, state)
	now := time.Now().UTC()
	intent, wakeOperation := newRunWakeTestSchedule(run, now, 2)
	if _, _, _, err := state.CreateRunWakeIntent(ctx, intent, wakeOperation); err != nil {
		t.Fatal(err)
	}
	leased, lease, acquired, err := state.AcquireRunWake(ctx, intent.ID,
		"wake-failure-owner", "wake-failure-lease", intent.NextWakeAt)
	if err != nil || !acquired {
		t.Fatalf("wake acquisition failed: acquired=%t err=%v", acquired, err)
	}
	operationKey := runmutation.RunWakeConsumptionOperationKey(intent.ID,
		lease.Generation)
	consumption, _, err := state.PrepareRunWakeConsumption(ctx,
		domain.RunWakeConsumption{
			ID:              idgen.New("wake-consume"),
			ProtocolVersion: domain.RunWakeConsumptionProtocolVersion,
			IntentID:        leased.ID, RunID: leased.RunID, SessionID: leased.SessionID,
			LeaseID: lease.ID, Generation: lease.Generation, OwnerID: lease.OwnerID,
			HandoffOperationKeyDigest: runmutation.RunExecutionHandoffOperationDigest(
				leased.RunID, operationKey),
			MaxSteps: 1, Status: domain.RunWakeConsumptionPrepared,
			CreatedAt: now.Add(time.Millisecond),
		})
	if err != nil {
		t.Fatal(err)
	}
	unrelatedKey := "unrelated-handoff-operation-0001"
	unrelated, _, err := state.PrepareRunExecutionHandoff(ctx,
		domain.RunExecutionHandoffOperation{
			ID:              idgen.New("run-handoff"),
			ProtocolVersion: domain.RunExecutionHandoffProtocolVersion,
			KeyDigest: runmutation.RunExecutionHandoffOperationDigest(run.ID,
				unrelatedKey),
			RequestFingerprint: runmutation.RunExecutionHandoffRequestFingerprint(
				run.ID, "unrelated_test", 1),
			RunID: run.ID, SessionID: run.SessionID, RequestedBy: "unrelated_test",
			MaxSteps: 1, CreatedAt: now.Add(2 * time.Millisecond),
		})
	if err != nil {
		t.Fatal(err)
	}
	failedAt := now.Add(3 * time.Millisecond)
	if _, _, _, err := state.FailRunWakeConsumption(ctx, consumption.ID,
		unrelated.Operation.ID, "conflict", "conflict", failedAt); err == nil {
		t.Fatal("service accepted an unrelated Run execution handoff")
	}
	tx, err := state.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	event, err := appendRunWakeEventTx(ctx, tx, run.ID,
		events.RunWakeRetriedEvent, "run_wake_coordinator", intent.ID,
		map[string]any{
			"generation": lease.Generation, "attempt_count": lease.Generation,
			"backoff_seconds": 5, "stop_reason": "conflict", "error_code": "conflict",
			"handoff_operation_id": unrelated.Operation.ID,
			"execution_started":    true, "model_called": false, "tool_called": false,
		}, failedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_wake_consumptions
		SET status = 'failed', handoff_operation_id = ?, stop_reason = 'conflict',
			error_code = 'conflict', completion_event_sequence = ?, completed_at = ?
		WHERE id = ?`, unrelated.Operation.ID, event.Sequence, ts(failedAt),
		consumption.ID); err == nil {
		t.Fatal("SQLite trigger accepted an unrelated Run execution handoff")
	}
}
