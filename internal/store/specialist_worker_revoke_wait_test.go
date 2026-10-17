package store

import (
	"encoding/json"
	"testing"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/session"
)

func TestWorkerRevokeTerminatesApprovalWait(t *testing.T) {
	for _, state := range []string{"pending", "approved", "previously_revoked"} {
		t.Run(state, func(t *testing.T) {
			st := openWorkItemTestStore(t)
			run, child, a, b, c, lease := workerToolFixture(t, st, &domain.WorkerProfile{})
			ctx, ref := t.Context(), attemptRef(a)
			a, _, err := st.GetAgentAttempt(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			permission, err := st.GetRunExecutionPermission(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(map[string]any{"permission_snapshot_id": permission.ID, "permission_revision": permission.Revision, "permission_mode": permission.Mode})
			c, err = st.BindSpecialistToolAuthority(ctx, ref, c.CallID, string(raw))
			if err != nil {
				t.Fatal(err)
			}
			p, err := st.EnsureApproval(ctx, approval.Proposal{IdempotencyKey: "revoke-wait-approval", ProposalID: c.ApprovalSourceID, SessionID: child.SessionID, WorkspaceID: b.WorkspaceID, ToolName: c.Name, ActionClass: "command_process", Mode: "per_call", Status: approval.StatusPending, RequestFingerprint: domain.SpecialistToolApprovalFingerprint(c), RequestedBy: "specialist_runner"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = st.SetSpecialistToolApproval(ctx, ref, c.CallID, p.ID); err != nil {
				t.Fatal(err)
			}
			if _, _, err = st.ReleaseRunExecutionLease(ctx, lease); err != nil {
				t.Fatal(err)
			}
			if state == "approved" {
				if _, err = st.DecideApproval(ctx, approval.DecisionRequest{ProposalID: c.ApprovalSourceID, IdempotencyKey: "revoke-wait-decision", Action: approval.ActionApprove, ReviewedBy: "operator"}); err != nil {
					t.Fatal(err)
				}
			}
			if state == "previously_revoked" {
				// A database written before this fix may already contain this state.
				if _, err = st.db.ExecContext(ctx, `UPDATE agent_nodes SET worker_profile_json='null' WHERE id=?`, child.ID); err != nil {
					t.Fatal(err)
				}
				newLease := acquireTestRunExecutionLease(t, ctx, st, run.ID)
				recovered, err := st.RecoverSpecialistAttempts(ctx, newLease)
				if err != nil || len(recovered) != 1 || recovered[0].Status != domain.AgentAttemptCrashed {
					t.Fatalf("revoked stale wait was skipped: %+v %v", recovered, err)
				}
				node, err := st.GetAgentNode(ctx, child.ID)
				if err != nil || node.Status == domain.AgentRunning || node.ActiveAttemptID != "" || node.Worker != nil {
					t.Fatalf("revoked source remained live: %+v %v", node, err)
				}
				if _, err = st.ResumeSpecialistToolAttempt(ctx, ref, newLease); err == nil {
					t.Fatal("historical revoked approval resumed")
				}
				return
			}
			node, err := st.RevokeSpecialistReadTools(ctx, run.ID, child.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !node.Terminal() || node.ActiveAttemptID != "" || node.Worker != nil {
				t.Fatalf("revocation left suspended child: %+v", node)
			}
			attempt, _, err := st.GetAgentAttempt(ctx, a.ID)
			if err != nil || attempt.Status != domain.AgentAttemptInterrupted || attempt.Usage != a.Usage {
				t.Fatalf("attempt not safely terminated: %+v %v", attempt, err)
			}
			linked, err := st.GetSession(ctx, child.SessionID)
			if err != nil || linked.Status != session.StatusArchived {
				t.Fatalf("child session still active: %+v %v", linked, err)
			}
			turns, tokens, err := specialistReservationsTx(ctx, st.db, run.ID, child.ParentID)
			if err != nil || turns != 1 || tokens != a.Usage.TotalTokens {
				t.Fatalf("terminal child kept unused reservations: %d %d %v", turns, tokens, err)
			}
			call, err := st.GetSpecialistToolCall(ctx, ref, c.CallID)
			if err != nil || call.StartedAt != nil {
				t.Fatalf("revoked intent dispatched: %+v %v", call, err)
			}
			newLease := acquireTestRunExecutionLease(t, ctx, st, run.ID)
			if _, err = st.ResumeSpecialistToolAttempt(ctx, ref, newLease); err == nil {
				t.Fatal("revoked approval resumed")
			}
			if recovered, err := st.RecoverSpecialistAttempts(ctx, newLease); err != nil || len(recovered) != 0 {
				t.Fatalf("terminal attempt recovered: %+v %v", recovered, err)
			}
		})
	}
}
