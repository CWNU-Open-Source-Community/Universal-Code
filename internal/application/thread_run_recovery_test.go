package application_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/store"
)

func TestThreadRunRecoveryFailsOldRunThenExplicitSubmitMaterializesSuccessor(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "thread-run-recovery.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	runs := application.NewRunService(st)
	_, created, err := runs.Create(ctx, application.CreateRunRequest{
		Goal: "recover a failed durable Thread turn", Profile: "code",
		Budget: domain.Budget{MaxTurns: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	running, err := runs.Start(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	threadRecord, err := st.GetThreadByRun(ctx, running.ID)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := st.EnqueueOperatorSteering(ctx, domain.EnqueueOperatorSteeringRequest{
		RunID: running.ID, SessionID: running.SessionID,
		Content:      "message that must remain failed history",
		OperationKey: "thread-recovery-message-0001", RequestedBy: "recovery_test_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	const handoffKey = "thread-recovery-handoff-0001"
	operation := domain.RunExecutionHandoffOperation{
		ID: idgen.New("run-handoff"), ProtocolVersion: domain.RunExecutionHandoffProtocolVersion,
		KeyDigest: runmutation.RunExecutionHandoffOperationDigest(running.ID, handoffKey),
		RequestFingerprint: runmutation.RunExecutionHandoffRequestFingerprint(
			running.ID, "recovery_test_operator", 1),
		RunID: running.ID, SessionID: running.SessionID,
		RequestedBy: "recovery_test_operator", MaxSteps: 1, CreatedAt: time.Now().UTC(),
	}
	handoff, _, err := st.PrepareRunExecutionHandoff(ctx, operation)
	if err != nil || len(handoff.Items) != 1 || handoff.Items[0].MessageID != queued.Message.ID {
		t.Fatalf("prepare handoff=%+v err=%v", handoff, err)
	}
	acquired, err := st.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{
		RunID: running.ID, OwnerID: "recovery-test-worker", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CompleteRunExecutionHandoff(ctx, operation.ID, acquired.Lease,
		domain.RunExecutionHandoffFailed, "failed_precondition", "failed_precondition",
		0, false, false); err != nil {
		t.Fatal(err)
	}
	recoveryService := application.NewThreadRunRecoveryService(st)
	if _, err := recoveryService.Recover(ctx, application.RecoverThreadRunRequest{
		Version: domain.ThreadRunRecoveryProtocolVersion, ThreadID: threadRecord.ID,
		RunID: running.ID, HandoffOperationID: operation.ID,
		OperationKey: "thread-recovery-control-0001",
		RequestedBy:  "recovery_test_operator",
	}); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("active lease recovery code=%s err=%v", apperror.CodeOf(err), err)
	}
	if _, _, err := st.ReleaseRunExecutionLease(ctx, acquired.Lease); err != nil {
		t.Fatal(err)
	}
	recovery, found, err := recoveryService.Get(ctx, threadRecord.ID)
	if err != nil || !found || !recovery.Quiescent || recovery.RunID != running.ID ||
		recovery.HandoffOperationID != operation.ID ||
		recovery.Disposition != domain.ThreadRunFailureRequiresSuccessor ||
		recovery.Detail != "" {
		t.Fatalf("recovery projection=%+v found=%t err=%v", recovery, found, err)
	}
	// Recovery is already durably fenced before lifecycle extensions run. A
	// denying completion hook therefore cannot leave the stale Run marked live.
	engine := hooks.NewEngine(st)
	digest := sha256.Sum256([]byte("deny-thread-recovery-completion"))
	if err := engine.Replace([]hooks.Registration{{
		PluginID: "thread-recovery-test-plugin", PluginFingerprint: hex.EncodeToString(digest[:]),
		Declaration: hooks.Declaration{ProtocolVersion: hooks.ProtocolVersion,
			ID: "deny-thread-recovery-completion", Event: hooks.RunCompleted,
			Action: hooks.ActionDeny, FailurePolicy: hooks.FailureContinue,
			TimeoutMillis: 100, Message: "operator policy"},
	}}); err != nil {
		t.Fatal(err)
	}
	recoveryService.WithLifecycleHooks(engine)

	registry := newMutableThreadModelRouteRegistry()
	if _, err := application.NewThreadModelRouteService(st, registry).Change(ctx,
		application.ChangeThreadModelRouteRequest{
			Version: domain.ThreadModelRouteControlProtocolVersion, ThreadID: threadRecord.ID,
			Action: domain.ThreadModelRouteSelect, Provider: "selected-provider",
			Model: "selected-model", OperationKey: "thread-recovery-route-0001",
			RequestedBy: "recovery_test_operator",
		}); err != nil {
		t.Fatal(err)
	}
	permissionChange, err := application.NewThreadExecutionPermissionService(st,
		domain.ExecutionPermissionRuntimeCapabilities{}).Change(ctx,
		application.ChangeThreadExecutionPermissionRequest{
			ThreadID:     threadRecord.ID,
			Mode:         string(domain.RunExecutionPermissionAuto),
			OperationKey: "thread-recovery-permission-0001", RequestedBy: "recovery_test_operator",
			Reason: "use the Auto preference on the successor Run",
		})
	if err != nil || permissionChange.CurrentRunEffect != domain.ThreadExecutionPermissionDeferred {
		t.Fatalf("successor permission preference=%+v err=%v", permissionChange, err)
	}
	recovered, err := recoveryService.Recover(ctx, application.RecoverThreadRunRequest{
		Version: domain.ThreadRunRecoveryProtocolVersion, ThreadID: threadRecord.ID,
		RunID: running.ID, HandoffOperationID: operation.ID,
		OperationKey: "thread-recovery-control-0001",
		RequestedBy:  "recovery_test_operator",
	})
	if err != nil || recovered.Replayed || recovered.FailedRun.Status != domain.RunFailed ||
		recovered.Thread.ActiveRunID != "" {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	storedMessage, err := st.GetOperatorSteering(ctx, queued.Message.ID)
	if err != nil || storedMessage.Status != domain.OperatorSteeringCancelled {
		t.Fatalf("old pending steering was not cancelled: %+v err=%v", storedMessage, err)
	}
	replayed, err := recoveryService.Recover(ctx, application.RecoverThreadRunRequest{
		Version: domain.ThreadRunRecoveryProtocolVersion, ThreadID: threadRecord.ID,
		RunID: running.ID, HandoffOperationID: operation.ID,
		OperationKey: "thread-recovery-control-0001",
		RequestedBy:  "recovery_test_operator",
	})
	if err != nil || !replayed.Replayed {
		t.Fatalf("recovery replay=%+v err=%v", replayed, err)
	}
	if _, err := recoveryService.Recover(ctx, application.RecoverThreadRunRequest{
		Version: domain.ThreadRunRecoveryProtocolVersion, ThreadID: threadRecord.ID,
		RunID: running.ID, HandoffOperationID: "run-handoff-different-intent",
		OperationKey: "thread-recovery-control-0001",
		RequestedBy:  "recovery_test_operator",
	}); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("changed recovery intent code=%s err=%v", apperror.CodeOf(err), err)
	}
	if _, err := recoveryService.Recover(ctx, application.RecoverThreadRunRequest{
		Version: domain.ThreadRunRecoveryProtocolVersion, ThreadID: threadRecord.ID,
		RunID: running.ID, HandoffOperationID: operation.ID,
		OperationKey: "thread-recovery-other-key-0001",
		RequestedBy:  "recovery_test_operator",
	}); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("different recovery key code=%s err=%v", apperror.CodeOf(err), err)
	}
	successor, err := application.NewThreadService(st).WithModelRouteRegistry(registry).
		Submit(ctx, application.SubmitThreadMessageRequest{
			Version: domain.ThreadMessageProtocolVersion, ThreadID: threadRecord.ID,
			Content:      "retry explicitly on the selected successor model",
			OperationKey: "thread-recovery-successor-0001",
			RequestedBy:  "recovery_test_operator",
		})
	if err != nil || !successor.SuccessorCreated ||
		successor.Run.Config.ModelRoute != "selected-provider/selected-model" ||
		successor.Session.Route != "selected-provider/selected-model" {
		t.Fatalf("successor=%+v err=%v", successor, err)
	}
	successorPermission, err := st.GetRunExecutionPermission(ctx, successor.Run.ID)
	if err != nil || successorPermission.Mode != domain.RunExecutionPermissionAuto {
		t.Fatalf("successor permission=%+v err=%v", successorPermission, err)
	}
	bindings, err := st.ListThreadRuns(ctx, threadRecord.ID)
	if err != nil || len(bindings) != 2 || bindings[1].PredecessorRunID != running.ID {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
}

func TestThreadRunRecoveryKeepsTransientFailedTurnOnCurrentRun(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "thread-run-retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	runs := application.NewRunService(st)
	_, created, err := runs.Create(ctx, application.CreateRunRequest{
		Goal: "retry a transient failed turn", Profile: "code",
		Budget: domain.Budget{MaxTurns: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	running, err := runs.Start(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	threadRecord, err := st.GetThreadByRun(ctx, running.ID)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := st.EnqueueOperatorSteering(ctx, domain.EnqueueOperatorSteeringRequest{
		RunID: running.ID, SessionID: running.SessionID,
		Content: "retry this transient model failure", OperationKey: "thread-retry-message-0001",
		RequestedBy: "recovery_retry_test_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	const handoffKey = "thread-retry-handoff-0001"
	operation := domain.RunExecutionHandoffOperation{
		ID: idgen.New("run-handoff"), ProtocolVersion: domain.RunExecutionHandoffProtocolVersion,
		KeyDigest: runmutation.RunExecutionHandoffOperationDigest(running.ID, handoffKey),
		RequestFingerprint: runmutation.RunExecutionHandoffRequestFingerprint(
			running.ID, "recovery_retry_test_operator", 1),
		RunID: running.ID, SessionID: running.SessionID,
		RequestedBy: "recovery_retry_test_operator", MaxSteps: 1, CreatedAt: time.Now().UTC(),
	}
	handoff, _, err := st.PrepareRunExecutionHandoff(ctx, operation)
	if err != nil || len(handoff.Items) != 1 || handoff.Items[0].MessageID != queued.Message.ID {
		t.Fatalf("prepare handoff=%+v err=%v", handoff, err)
	}
	lease, err := st.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{
		RunID: running.ID, OwnerID: "recovery-retry-worker", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CompleteRunExecutionHandoff(ctx, operation.ID, lease.Lease,
		domain.RunExecutionHandoffFailed, "unavailable", "unavailable", 0, false,
		false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ReleaseRunExecutionLease(ctx, lease.Lease); err != nil {
		t.Fatal(err)
	}
	raw, found, err := st.GetThreadRunRecovery(ctx, threadRecord.ID)
	if err != nil || !found || raw.Disposition != domain.ThreadRunFailureRetrySameTurn ||
		!raw.Quiescent {
		t.Fatalf("raw retry disposition=%+v found=%t err=%v", raw, found, err)
	}
	service := application.NewThreadRunRecoveryService(st)
	if visible, visibleFound, err := service.Get(ctx, threadRecord.ID); err != nil ||
		visibleFound || visible != (domain.ThreadRunRecovery{}) {
		t.Fatalf("transient failure surfaced as recovery=%+v found=%t err=%v",
			visible, visibleFound, err)
	}
	if _, err := service.Recover(ctx, application.RecoverThreadRunRequest{
		Version: domain.ThreadRunRecoveryProtocolVersion, ThreadID: threadRecord.ID,
		RunID: running.ID, HandoffOperationID: operation.ID,
		OperationKey: "thread-retry-recovery-0001",
		RequestedBy:  "recovery_retry_test_operator",
	}); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("transient recovery code=%s err=%v", apperror.CodeOf(err), err)
	}
	if _, _, _, err := st.RecoverThreadRunFromFailedHandoff(ctx, threadRecord.ID,
		running.ID, operation.ID, "recovery_retry_test_operator",
		"thread-retry-store-recovery-0001"); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("store accepted transient terminal recovery code=%s err=%v",
			apperror.CodeOf(err), err)
	}
	storedRun, err := st.GetRun(ctx, running.ID)
	if err != nil || storedRun.Status != domain.RunRunning {
		t.Fatalf("transient failure terminalized Run=%+v err=%v", storedRun, err)
	}
	storedMessage, err := st.GetOperatorSteering(ctx, queued.Message.ID)
	if err != nil || storedMessage.Status != domain.OperatorSteeringPending {
		t.Fatalf("transient retry lost pending input=%+v err=%v", storedMessage, err)
	}
	automatic, err := service.RecoverForNextTurn(ctx, application.RecoverThreadRunRequest{
		Version: domain.ThreadRunRecoveryProtocolVersion, ThreadID: threadRecord.ID,
		RunID: running.ID, HandoffOperationID: operation.ID,
		OperationKey: "thread-retry-next-turn-0001",
		RequestedBy:  "recovery_retry_test_operator",
	})
	if err != nil || automatic.Replayed || automatic.FailedRun.Status != domain.RunFailed ||
		automatic.Thread.ActiveRunID != "" {
		t.Fatalf("explicit next turn did not abandon transient failure=%+v err=%v",
			automatic, err)
	}
	storedMessage, err = st.GetOperatorSteering(ctx, queued.Message.ID)
	if err != nil || storedMessage.Status != domain.OperatorSteeringCancelled {
		t.Fatalf("explicit next turn retained failed input=%+v err=%v", storedMessage, err)
	}
}

func TestThreadRunRecoveryRevokesRuntimeOnlyAfterDurableTerminalFence(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	authority := domain.NewExecutionPermissionRuntimeAuthority()
	run := domain.Run{ID: "run-recovery-order", MissionID: "mission-recovery-order",
		SessionID: "session-recovery-order", Status: domain.RunRunning,
		Config: domain.RunConfig{ModelRoute: "fixture/model"}, Budget: domain.DefaultBudget(),
		CreatedAt: now, UpdatedAt: now}
	fence, err := authority.IssueRunAuthorizationFence(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &orderedThreadRunRecoveryStore{authority: authority, fence: fence,
		run: run, mission: domain.Mission{ID: run.MissionID, Goal: "ordering",
			Profile: domain.ProfileCode, WorkspaceID: "workspace-recovery-order",
			Scope: domain.DefaultScope("workspace-recovery-order"), CreatedAt: now,
			UpdatedAt: now},
		thread: domain.Thread{ID: "thread-recovery-order", MissionID: run.MissionID,
			Status: domain.ThreadActive, ActiveRunID: run.ID, LastRunID: run.ID,
			CreatedAt: now, UpdatedAt: now},
		recovery: domain.ThreadRunRecovery{
			ProtocolVersion: domain.ThreadRunRecoveryProtocolVersion,
			ThreadID:        "thread-recovery-order", RunID: run.ID,
			HandoffOperationID: "run-handoff-recovery-order",
			Disposition:        domain.ThreadRunFailureRequiresSuccessor,
			ErrorCode:          "failed_precondition", StopReason: "failed_precondition",
			Quiescent: true, FailedAt: now,
		},
	}
	result, err := application.NewThreadRunRecoveryService(fixture).
		WithExecutionPermissionRuntimeAuthority(authority).
		Recover(ctx, application.RecoverThreadRunRequest{
			Version:  domain.ThreadRunRecoveryProtocolVersion,
			ThreadID: fixture.thread.ID, RunID: run.ID,
			HandoffOperationID: fixture.recovery.HandoffOperationID,
			OperationKey:       "thread-recovery-order-operation-0001",
			RequestedBy:        "recovery_order_test_operator",
		})
	if err != nil || result.FailedRun.Status != domain.RunFailed {
		t.Fatalf("ordered recovery=%+v err=%v", result, err)
	}
	if !fixture.fenceLiveDuringStore {
		t.Fatal("runtime authority was revoked before the durable terminal transaction")
	}
	if authority.AllowsRunAuthorizationFence(run.ID, fence) {
		t.Fatal("terminal recovery retained process-local runtime authority")
	}
}

type orderedThreadRunRecoveryStore struct {
	authority            *domain.ExecutionPermissionRuntimeAuthority
	fence                uint64
	run                  domain.Run
	mission              domain.Mission
	thread               domain.Thread
	recovery             domain.ThreadRunRecovery
	fenceLiveDuringStore bool
}

func (s *orderedThreadRunRecoveryStore) GetThreadRunRecovery(
	context.Context, string,
) (domain.ThreadRunRecovery, bool, error) {
	return s.recovery, true, nil
}

func (s *orderedThreadRunRecoveryStore) GetRun(context.Context, string) (domain.Run, error) {
	return s.run, nil
}

func (s *orderedThreadRunRecoveryStore) GetMission(
	context.Context, string,
) (domain.Mission, error) {
	return s.mission, nil
}

func (s *orderedThreadRunRecoveryStore) RecoverThreadRunFromFailedHandoff(
	context.Context, string, string, string, string, string,
) (domain.Thread, domain.Run, bool, error) {
	s.fenceLiveDuringStore = s.authority.AllowsRunAuthorizationFence(s.run.ID, s.fence)
	failed := s.run
	failed.Status = domain.RunFailed
	failed.UpdatedAt = time.Now().UTC()
	failed.FinishedAt = &failed.UpdatedAt
	threadRecord := s.thread
	threadRecord.ActiveRunID = ""
	threadRecord.UpdatedAt = failed.UpdatedAt
	return threadRecord, failed, false, nil
}
