package store

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/policy"
)

func TestWorkflowHistoryDoesNotBlockLaterReviewedDelegation(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "workflow-delegation-history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := t.Context()
	run, proposal, review := createApprovedDelegationApplicationFixture(t, ctx, st, 2)
	nodes, err := st.ListAgentNodes(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	root := nodes[0]
	for index := range 2 {
		child, _, err := st.AdmitSpecialist(ctx, domain.SpecialistAdmission{AgentID: fmt.Sprintf("agent-history-%d", index), SessionID: fmt.Sprintf("sess-history-%d", index), RunID: run.ID, ParentAgentID: root.ID, Title: "finished old job", Skills: []string{"model.chat"}, TurnLimit: 5, TokenLimit: 1500, MaxChildren: 2, CreatedAt: time.Now().UTC()}, fmt.Sprintf("workflow-history-admit-%d", index))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.Exec(`UPDATE agent_nodes SET status='cancelled',finished_at=?,updated_at=?,version=version+1 WHERE id=?`, ts(time.Now()), ts(time.Now()), child.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.SnapshotAgentGraph(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = validateSpecialistDelegationCapacityTx(ctx, tx, run, root, proposal.Spec)
	_ = tx.Rollback()
	if err != nil {
		t.Fatalf("proposal counted terminal children as active: %v", err)
	}
	service, err := application.NewDefaultSpecialistDelegationApplicationService(st, policy.NewDefaultChecker())
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(ctx, application.ApplySpecialistDelegationRequest{ProposalID: proposal.ID, OperationKey: "workflow-later-delegation", RequestedBy: review.ReviewedBy})
	if err != nil {
		t.Fatalf("reviewed delegation retained old reservations: %v", err)
	}
	if len(result.Application.Assignments) != 2 {
		t.Fatalf("assignments: %#v", result.Application.Assignments)
	}
	nodes, err = st.ListAgentNodes(ctx, run.ID)
	if err != nil || len(nodes) != 5 {
		t.Fatalf("history was not retained: %d %v", len(nodes), err)
	}
}
