package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runmutation"
)

func TestWorkflowCompletionCheckpointAndNotificationCommitTogether(t *testing.T) {
	st, run, definition, _ := workflowDueFixture(t)
	e := parkWorkflow(t, st, run, definition, "workflowrun-atomic-summary", "running")
	e, claimed, err := st.ClaimWorkflowExecution(t.Context(), e.ID, "checkpoint-owner")
	if err != nil || !claimed {
		t.Fatalf("claim=%t err=%v", claimed, err)
	}
	before := e
	e.Status = "completed"
	if _, err = st.db.Exec(`CREATE TRIGGER fail_notification BEFORE INSERT ON workflow_completion_notifications BEGIN SELECT RAISE(ABORT,'notification write failed'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err = st.CheckpointWorkflowExecution(t.Context(), e, "checkpoint-owner"); err == nil {
		t.Fatal("completion succeeded without its notification")
	}
	current, err := st.GetWorkflowExecution(t.Context(), e.ID)
	if err != nil || current.Status != before.Status || current.Version != before.Version {
		t.Fatalf("partial completion committed: %+v %v", current, err)
	}
	if _, err = st.db.Exec(`DROP TRIGGER fail_notification`); err != nil {
		t.Fatal(err)
	}
	completed, err := st.CheckpointWorkflowExecution(t.Context(), e, "checkpoint-owner")
	if err != nil || completed.Status != "completed" {
		t.Fatalf("completion: %+v %v", completed, err)
	}
	if _, err = st.CheckpointWorkflowExecution(t.Context(), e, "checkpoint-owner"); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeConflict {
		t.Fatalf("stale checkpoint accepted: %v", err)
	}
	candidates, err := st.ListWorkflowSummaryCandidates(t.Context(), time.Now().UTC(), 32)
	if err != nil || len(candidates) != 1 || candidates[0].ID != e.ID {
		t.Fatalf("missing or duplicate obligation: %+v %v", candidates, err)
	}
}

func TestWorkflowSummaryClaimsFenceOtherDatabaseAndExpiredOwner(t *testing.T) {
	st, run, definition, _ := workflowDueFixture(t)
	e := parkWorkflow(t, st, run, definition, "workflowrun-summary-claim", "completed")
	second, err := Open(filepath.Join(st.home, "workflow-due.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	now := time.Now().UTC()
	if _, claimed, err := st.ClaimWorkflowSummary(t.Context(), e.ID, "summary-owner-one", now); err != nil || !claimed {
		t.Fatalf("first claim=%t %v", claimed, err)
	}
	if _, claimed, err := second.ClaimWorkflowSummary(t.Context(), e.ID, "summary-owner-two", now); err != nil || claimed {
		t.Fatalf("second live owner=%t %v", claimed, err)
	}
	if err := second.FinishWorkflowSummary(t.Context(), e.ID, "summary-owner-two", "delivered", "", now); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeConflict {
		t.Fatalf("unclaimed owner settled: %v", err)
	}
	if _, err := st.db.Exec(`UPDATE workflow_completion_notifications SET claim_until=? WHERE execution_id=?`, ts(now.Add(-time.Second)), e.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishWorkflowSummary(t.Context(), e.ID, "summary-owner-one", "delivered", "", now); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeConflict {
		t.Fatalf("expired owner settled: %v", err)
	}
	if _, claimed, err := second.ClaimWorkflowSummary(t.Context(), e.ID, "summary-owner-two", now); err != nil || !claimed {
		t.Fatalf("claim recovery=%t %v", claimed, err)
	}
	if err := st.FinishWorkflowSummary(t.Context(), e.ID, "summary-owner-one", "delivered", "", now); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeConflict {
		t.Fatalf("superseded owner settled: %v", err)
	}
	if err := second.FinishWorkflowSummary(t.Context(), e.ID, "summary-owner-two", "delivered", "", now); err != nil {
		t.Fatal(err)
	}
	candidates, err := st.ListWorkflowSummaryCandidates(t.Context(), now.Add(24*time.Hour), 32)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("delivered obligation remained due: %+v %v", candidates, err)
	}
}

type workflowSummaryStoreHandoffFunc func(context.Context, application.ExecuteRunHandoffRequest) (application.ExecuteRunHandoffResult, error)

func (f workflowSummaryStoreHandoffFunc) Execute(ctx context.Context, request application.ExecuteRunHandoffRequest) (application.ExecuteRunHandoffResult, error) {
	return f(ctx, request)
}

func TestWorkflowSummaryWakeGenerationRequiresClaimAndNoActualHandoff(t *testing.T) {
	for _, persistOperation := range []bool{false, true} {
		name := "never-started"
		if persistOperation {
			name = "unknown-handoff-result"
		}
		t.Run(name, func(t *testing.T) {
			st, run, definition, _ := workflowDueFixture(t)
			e := parkWorkflow(t, st, run, definition, "workflowrun-wake-generation", "completed")
			if _, err := st.EnqueueOperatorSteering(t.Context(), domain.EnqueueOperatorSteeringRequest{RunID: run.ID, SessionID: run.SessionID, OperationKey: "summary-generation-notification", RequestedBy: "specialist_completion", Content: "saved workflow output"}); err != nil {
				t.Fatal(err)
			}
			wake, err := application.NewRunWakeControlService(st).Schedule(t.Context(), application.ScheduleRunWakeRequest{Version: domain.RunWakeControlProtocolVersion, RunID: run.ID, OperationKey: "summary-generation-wake", RequestedBy: "specialist_completion", MaxAttempts: 1, BaseBackoffSeconds: 5, MaxBackoffSeconds: 30, MaxElapsedSeconds: 120})
			if err != nil {
				t.Fatal(err)
			}
			failing := workflowSummaryStoreHandoffFunc(func(ctx context.Context, request application.ExecuteRunHandoffRequest) (application.ExecuteRunHandoffResult, error) {
				if persistOperation {
					_, _, err := st.PrepareRunExecutionHandoff(ctx, domain.RunExecutionHandoffOperation{ID: "unknown-workflow-handoff", ProtocolVersion: domain.RunExecutionHandoffProtocolVersion, KeyDigest: runmutation.RunExecutionHandoffOperationDigest(run.ID, request.OperationKey), RequestFingerprint: runmutation.RunExecutionHandoffRequestFingerprint(run.ID, request.RequestedBy, request.MaxSteps), RunID: run.ID, SessionID: run.SessionID, RequestedBy: request.RequestedBy, MaxSteps: request.MaxSteps, CreatedAt: time.Now().UTC()})
					if err != nil {
						t.Fatal(err)
					}
				}
				return application.ExecuteRunHandoffResult{}, apperror.New(apperror.CodeUnavailable, "handoff response unavailable")
			})
			result, err := application.NewForegroundRunWakeConsumer(st, failing).Consume(t.Context(), application.ConsumeRunWakeRequest{Version: domain.RunWakeConsumerProtocolVersion, RunID: run.ID, IntentID: wake.Intent.ID, OwnerID: "specialist_completion", MaxSteps: 1})
			if err == nil || result.Intent.Status != domain.RunWakeExhausted || result.Consumption.HandoffOperationID != "" {
				t.Fatalf("exhaustion setup: %+v %v", result, err)
			}
			generation, claimed, err := st.ClaimWorkflowSummary(t.Context(), e.ID, "generation-owner", time.Now().UTC())
			if err != nil || !claimed || generation != 0 {
				t.Fatalf("generation claim: %d %t %v", generation, claimed, err)
			}
			if advanced, err := st.AdvanceWorkflowSummaryWake(t.Context(), e.ID, "wrong-owner", wake.Intent.ID, generation); err != nil || advanced {
				t.Fatalf("unclaimed owner changed generation: %t %v", advanced, err)
			}
			if _, err := st.db.Exec(`UPDATE workflow_completion_notifications SET claim_until=? WHERE execution_id=?`, ts(time.Now().Add(-time.Second)), e.ID); err != nil {
				t.Fatal(err)
			}
			if advanced, err := st.AdvanceWorkflowSummaryWake(t.Context(), e.ID, "generation-owner", wake.Intent.ID, generation); err != nil || advanced {
				t.Fatalf("expired owner changed generation: %t %v", advanced, err)
			}
			generation, claimed, err = st.ClaimWorkflowSummary(t.Context(), e.ID, "replacement-owner", time.Now().UTC())
			if err != nil || !claimed {
				t.Fatalf("replacement claim: %t %v", claimed, err)
			}
			advanced, err := st.AdvanceWorkflowSummaryWake(t.Context(), e.ID, "replacement-owner", wake.Intent.ID, generation)
			if err != nil || advanced == persistOperation {
				t.Fatalf("unsafe generation change: actual_operation=%t advanced=%t err=%v", persistOperation, advanced, err)
			}
			if advanced, err := st.AdvanceWorkflowSummaryWake(t.Context(), e.ID, "replacement-owner", wake.Intent.ID, generation); err != nil || advanced {
				t.Fatalf("same generation advanced twice: %t %v", advanced, err)
			}
		})
	}
}
