package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/operatoraction"
)

func TestOperatorActionProjectionAggregatesOnlyCurrentRunFacts(t *testing.T) {
	ctx := t.Context()
	state := openWorkItemTestStore(t)
	root := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	workspace := WorkspaceRecord{ID: "workspace-actions", Name: "actions",
		RootPath: root, CreatedAt: time.Now().UTC()}
	if err := state.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	_, created, err := application.NewRunService(state).Create(ctx,
		application.CreateRunRequest{Goal: "aggregate operator actions", Profile: "code",
			WorkspaceID: workspace.ID, Budget: domain.Budget{MaxTurns: 8}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := application.NewRunService(state).Start(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.EnqueueOperatorSteering(ctx, domain.EnqueueOperatorSteeringRequest{
		RunID: run.ID, SessionID: run.SessionID, Content: "PRIVATE operator instruction",
		OperationKey: "operator-action-steering-operation-0001", RequestedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	digest := strings.Repeat("a", 64)
	for _, edit := range []struct{ id, status string }{
		{"edit-review-action", "proposed"}, {"edit-apply-action", "approved"},
	} {
		if _, err := state.db.ExecContext(ctx, `INSERT INTO file_edits
			(id, session_id, workspace_id, path, status, original_text, proposed_text,
			diff_text, original_hash, proposed_hash, reason, secrets_redacted,
			created_at, updated_at) VALUES (?, ?, ?, ?, ?, '', 'safe', 'diff',
			'missing', ?, '', 0, ?, ?)`, edit.id, run.SessionID, workspace.ID,
			edit.id+".txt", edit.status, digest, ts(now), ts(now)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := state.db.ExecContext(ctx, `INSERT INTO tool_approvals
		(id, idempotency_key, proposal_id, run_id, session_id, workspace_id,
		tool_name, action_class, mode, status, request_fingerprint, decision_reason,
		requested_by, reviewed_by, version, created_at, updated_at, decided_at)
		VALUES ('approval-action', 'approval-action-key', 'approval-action-proposal',
		?, ?, ?, 'shell', 'shell', 'per_call', 'pending', ?, '', 'model', '', 1, ?, ?, NULL)`,
		run.ID, run.SessionID, workspace.ID, digest, ts(now), ts(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunWakeControlService(state).Schedule(ctx,
		application.ScheduleRunWakeRequest{Version: domain.RunWakeControlProtocolVersion,
			RunID: run.ID, OperationKey: "operator-action-wake-operation-0001",
			RequestedBy: "operator", MaxAttempts: 2, InitialDelaySeconds: 0,
			BaseBackoffSeconds: 5, MaxBackoffSeconds: 30, MaxElapsedSeconds: 120}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.db.ExecContext(ctx, `INSERT INTO child_task_proposals
		(id, run_id, root_agent_id, session_id, workspace_id, status, spec_json,
		surface, fanout_tier, dedup_fingerprint, requested_by, version, created_at)
		VALUES ('child-task-review-action', ?, 'root-actions', ?, ?, 'proposed',
		'{}', 'core', '', ?, 'model', 1, ?)`, run.ID, run.SessionID, workspace.ID,
		digest, ts(now)); err != nil {
		t.Fatal(err)
	}
	records, err := state.ListOperatorActionRecords(ctx, run.ID, run.SessionID,
		workspace.ID, time.Now().UTC().Add(time.Second), operatoraction.MaxItems+1)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 6 {
		t.Fatalf("unexpected action projection: %#v", records)
	}
	want := []operatoraction.Kind{operatoraction.KindWakeDue,
		operatoraction.KindApprovalPending, operatoraction.KindFileEditReview,
		operatoraction.KindFileEditApply, operatoraction.KindChildTaskReview,
		operatoraction.KindSteeringPending}
	for index, kind := range want {
		if records[index].Kind != kind || records[index].RunID != run.ID {
			t.Fatalf("action %d drifted: %#v", index, records[index])
		}
	}
	if _, err := state.ListOperatorActionRecords(ctx, run.ID, run.SessionID,
		workspace.ID, time.Now().UTC(), operatoraction.MaxItems+2); err == nil {
		t.Fatal("unbounded operator action query was accepted")
	}
}
