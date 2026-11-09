package store

import (
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
)

func TestWorkflowCompletedCheckpointHonorsRunControl(t *testing.T) {
	for _, action := range []string{"cancel", "pause"} {
		t.Run(action, func(t *testing.T) {
			st, run, definition, _ := workflowDueFixture(t)
			e := parkWorkflow(t, st, run, definition, "workflow-completion-control", "running")
			e, claimed, err := st.ClaimWorkflowExecution(t.Context(), e.ID, "old-worker")
			if err != nil || !claimed {
				t.Fatalf("claim: acquired=%t err=%v", claimed, err)
			}
			runs := application.NewRunService(st)
			nextStatus := "failed"
			if action == "pause" {
				_, err = runs.Pause(t.Context(), run.ID)
				nextStatus = "waiting"
			} else {
				_, err = runs.Cancel(t.Context(), run.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			e.Status = "completed"
			if _, err := st.CheckpointWorkflowExecution(t.Context(), e, "old-worker"); apperror.CodeOf(err) != apperror.CodeConflict {
				t.Fatalf("completion after Run %s did not conflict: %v", action, err)
			}
			current, err := st.GetWorkflowExecution(t.Context(), e.ID)
			if err != nil || current.Status != "running" || current.Version != e.Version {
				t.Fatalf("rejected completion changed checkpoint: %+v err=%v", current, err)
			}
			var notifications int
			if err := st.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM workflow_completion_notifications WHERE execution_id=?`, e.ID).Scan(&notifications); err != nil || notifications != 0 {
				t.Fatalf("rejected completion published outbox: count=%d err=%v", notifications, err)
			}
			e.Status = nextStatus
			if current, err = st.CheckpointWorkflowExecution(t.Context(), e, "old-worker"); err != nil || current.Status != nextStatus {
				t.Fatalf("Run %s blocked its %s checkpoint: %+v err=%v", action, nextStatus, current, err)
			}
		})
	}
}
