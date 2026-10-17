package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolbudget"
)

func TestHistoricalWorkerToolSourceSurvivesPermissionChangeWithoutReviewAuthority(t *testing.T) {
	ctx := t.Context()
	st := openWorkItemTestStore(t)
	run, child, attempt, binding, call, lease := workerToolFixture(t, st, &domain.WorkerProfile{})
	permission, err := st.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	authority, _ := json.Marshal(map[string]any{"permission_snapshot_id": permission.ID, "permission_revision": permission.Revision, "permission_mode": permission.Mode})
	call, err = st.BindSpecialistToolAuthority(ctx, call.Ref, call.CallID, string(authority))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.ChargeToolCall(ctx, toolbudget.ChargeRequest{RunID: run.ID, SessionID: child.SessionID, WorkspaceID: binding.WorkspaceID, AgentID: child.ID, AgentAttemptID: attempt.ID, ToolCallID: call.CallID, ToolName: call.Name, ActionClass: "command_process", RequestedBy: "specialist_runner", LeaseID: lease.LeaseID, LeaseGeneration: lease.Generation}); err != nil {
		t.Fatal(err)
	}
	if _, err = st.MarkSpecialistToolStarted(ctx, call.Ref, call.CallID, call.AuthorityJSON); err != nil {
		t.Fatal(err)
	}
	if _, err = application.NewRunService(st).Pause(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = application.NewRunExecutionPermissionService(st, domain.ExecutionPermissionRuntimeCapabilities{WorkspaceSandboxEnabled: true, OperatorApprovalEnabled: true}).Change(ctx, application.ChangeRunExecutionPermissionRequest{RunID: run.ID, Mode: "auto", OperationKey: "history-source-permission-change", RequestedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.GetSpecialistApprovalCall(ctx, run.ID, call.ApprovalSourceID); err == nil {
		t.Fatal("old authority became a current review source after a permission change")
	}
	historical, started, err := st.GetHistoricalSpecialistApprovalCall(ctx, run.ID, call.ApprovalSourceID)
	if err != nil || !started || historical.Ref != call.Ref || historical.CallID != call.CallID || historical.AuthorityJSON != call.AuthorityJSON || historical.ChargeID == "" {
		t.Fatalf("historical cleanup source changed: %+v started=%v err=%v", historical, started, err)
	}
	if _, _, err = st.GetHistoricalSpecialistApprovalCall(ctx, "another-run", call.ApprovalSourceID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("historical source crossed its Run: %v", err)
	}
	if _, err = st.MarkSpecialistToolStarted(ctx, call.Ref, call.CallID, call.AuthorityJSON); err == nil {
		t.Fatal("historical receipt restored dispatch authority to a paused Run")
	}
}
