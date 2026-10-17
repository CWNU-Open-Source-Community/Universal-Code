package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
)

func TestSpecialistWaitingScheduleReplaysThenResumesOriginalAttempt(t *testing.T) {
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
	review, err := st.EnsureApproval(ctx, approval.Proposal{IdempotencyKey: "waiting-schedule-approval", ProposalID: call.ApprovalSourceID, SessionID: child.SessionID, WorkspaceID: binding.WorkspaceID, ToolName: call.Name, ActionClass: "command_process", Mode: "per_call", Status: approval.StatusPending, RequestFingerprint: domain.SpecialistToolApprovalFingerprint(call), RequestedBy: "specialist_runner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.SetSpecialistToolApproval(ctx, call.Ref, call.CallID, review.ID); err != nil {
		t.Fatal(err)
	}
	usage, err := st.GetRunAgentUsage(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("d", 64)
	start := domain.SpecialistScheduleStart{ID: idgen.New("schedule"), RunID: run.ID, AgentIDs: []string{child.ID}, MaxRounds: 4, ContinuationDigest: digest, Lease: lease, UsageBefore: usage, StartedAt: time.Now().UTC()}
	first, err := st.StartSpecialistSchedule(ctx, start)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.FinishSpecialistSchedule(ctx, domain.SpecialistScheduleFinish{ID: first.Schedule.ID, Lease: lease, Status: domain.SpecialistScheduleCompleted, StopReason: "waiting_approval", RoundsCompleted: 1, TurnsStarted: 1, UsageAfter: usage, FinishedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.ReleaseRunExecutionLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	waiting, pending, err := st.SpecialistToolApprovalState(ctx, call.Ref)
	if err != nil || !waiting || !pending {
		t.Fatalf("released approval wait was lost: waiting=%v pending=%v err=%v", waiting, pending, err)
	}
	start.Lease = acquireTestRunExecutionLease(t, ctx, st, run.ID)
	start.ID, start.StartedAt = idgen.New("schedule"), time.Now().UTC()
	replay, err := st.StartSpecialistSchedule(ctx, start)
	if err != nil || replay.Schedule.ID != first.Schedule.ID || replay.Schedule.StopReason != "waiting_approval" {
		t.Fatalf("pending review created another scheduling pass: %+v %v", replay, err)
	}
	if _, err = st.DecideApproval(ctx, approval.DecisionRequest{ProposalID: call.ApprovalSourceID, IdempotencyKey: "waiting-schedule-decision", Action: approval.ActionApprove, ReviewedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	second, err := st.StartSpecialistSchedule(ctx, start)
	if err != nil || second.Schedule.ID == first.Schedule.ID || second.Schedule.Status != domain.SpecialistScheduleRunning {
		t.Fatalf("decided review failed to open next pass: %+v %v", second, err)
	}
	resumed, err := st.ResumeSpecialistToolAttempt(ctx, call.Ref, start.Lease)
	if err != nil || resumed.ID != attempt.ID || resumed.Turn != attempt.Turn || resumed.Usage.TotalTokens != 4 {
		t.Fatalf("schedule resumed another attempt or reset usage: %+v %v", resumed, err)
	}
	stored, err := st.GetSpecialistToolCall(ctx, call.Ref, call.CallID)
	if err != nil || stored.AuthorityJSON != call.AuthorityJSON || stored.StartedAt != nil || stored.ChargeID != "" {
		t.Fatalf("approval resume changed original intent: %+v %v", stored, err)
	}
	ids, err := st.ListSpecialistContinuationAttemptIDs(ctx, digest)
	if err != nil || len(ids) != 1 || ids[0] != attempt.ID {
		t.Fatalf("continuation lost original attempt identity: %v %v", ids, err)
	}
}
