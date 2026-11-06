package store

import (
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
)

func TestWorkflowClaimRenewalPreservesCheckpointAndFencesOldOwner(t *testing.T) {
	st, run, definition, service := workflowDueFixture(t)
	e := parkWorkflow(t, st, run, definition, "workflow-claim-renewal", "running")
	e, claimed, err := st.ClaimWorkflowExecution(t.Context(), e.ID, "old-owner")
	if err != nil || !claimed {
		t.Fatalf("claim: claimed=%t err=%v", claimed, err)
	}
	readClaim := func() (string, time.Time) {
		t.Helper()
		var owner, expiry string
		if err := st.db.QueryRowContext(t.Context(), `SELECT claim_owner,claim_until FROM workflow_executions WHERE id=?`, e.ID).Scan(&owner, &expiry); err != nil {
			t.Fatal(err)
		}
		return owner, parseTS(expiry)
	}
	_, expiry := readClaim()
	if left := time.Until(expiry); left < 25*time.Second || left > 30*time.Second {
		t.Fatalf("claim does not use a 30-second lifetime: %s", left)
	}
	if _, err := st.db.ExecContext(t.Context(), `UPDATE workflow_executions SET claim_until=? WHERE id=?`, ts(time.Now().Add(5*time.Second)), e.ID); err != nil {
		t.Fatal(err)
	}
	if renewed, err := st.RenewWorkflowExecutionClaim(t.Context(), e.ID, "old-owner"); err != nil || !renewed {
		t.Fatalf("renewal: renewed=%t err=%v", renewed, err)
	}
	_, expiry = readClaim()
	current, err := st.GetWorkflowExecution(t.Context(), e.ID)
	if err != nil || current.Version != e.Version || !current.UpdatedAt.Equal(e.UpdatedAt) || time.Until(expiry) < 25*time.Second {
		t.Fatalf("renewal changed checkpoint/cursor or failed to extend: before=%+v after=%+v until=%s err=%v", e, current, time.Until(expiry), err)
	}
	// Pause may happen while an already admitted wave is draining. Keep its
	// ownership renewable, without allowing any new dispatch or implicit resume.
	e, err = service.Pause(t.Context(), e.ID, e.Version)
	if err != nil {
		t.Fatal(err)
	}
	if renewed, err := st.RenewWorkflowExecutionClaim(t.Context(), e.ID, "old-owner"); err != nil || !renewed {
		t.Fatalf("paused owner cannot drain: renewed=%t err=%v", renewed, err)
	}
	current, err = st.GetWorkflowExecution(t.Context(), e.ID)
	if err != nil || current.Status != "paused" || current.Version != e.Version || !current.UpdatedAt.Equal(e.UpdatedAt) {
		t.Fatalf("renewal changed paused checkpoint: %+v err=%v", current, err)
	}
	if _, claimed, err := st.ClaimWorkflowExecution(t.Context(), e.ID, "new-owner"); err != nil || claimed {
		t.Fatalf("renewal made paused workflow runnable: claimed=%t err=%v", claimed, err)
	}
	e, err = service.Resume(t.Context(), e.ID, e.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(t.Context(), `UPDATE workflow_executions SET claim_until=? WHERE id=?`, ts(time.Now().Add(-time.Second)), e.ID); err != nil {
		t.Fatal(err)
	}
	if renewed, err := st.RenewWorkflowExecutionClaim(t.Context(), e.ID, "old-owner"); err != nil || renewed {
		t.Fatalf("expired claim was revived: renewed=%t err=%v", renewed, err)
	}
	if _, claimed, err := st.ClaimWorkflowExecution(t.Context(), e.ID, "new-owner"); err != nil || !claimed {
		t.Fatalf("takeover: claimed=%t err=%v", claimed, err)
	}
	if err := st.ReleaseWorkflowExecution(t.Context(), e.ID, "old-owner"); err != nil {
		t.Fatal(err)
	}
	owner, _ := readClaim()
	if owner != "new-owner" {
		t.Fatalf("old owner released the new claim: owner=%s", owner)
	}
	if renewed, err := st.RenewWorkflowExecutionClaim(t.Context(), e.ID, "old-owner"); err != nil || renewed {
		t.Fatalf("old owner renewed new claim: renewed=%t err=%v", renewed, err)
	}
	if _, err := st.CheckpointWorkflowExecution(t.Context(), e, "old-owner"); err == nil {
		t.Fatal("old owner committed a checkpoint after takeover")
	}
	if renewed, err := st.RenewWorkflowExecutionClaim(t.Context(), e.ID, "new-owner"); err != nil || !renewed {
		t.Fatalf("current owner cannot renew: renewed=%t err=%v", renewed, err)
	}
	if current, err = st.GetWorkflowExecution(t.Context(), e.ID); err != nil || current.Version != e.Version || current.RunID != run.ID || current.Status != "running" {
		t.Fatalf("takeover changed workflow identity: %+v err=%v", current, err)
	}
	if run, err := st.GetRun(t.Context(), run.ID); err != nil || run.Status != domain.RunRunning {
		t.Fatalf("claim renewal changed Run: %+v err=%v", run, err)
	}
}
