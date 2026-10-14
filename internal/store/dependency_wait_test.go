package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/coordinator"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/waitgraph"
)

func newDependencyTestFixture(t *testing.T) (*SQLiteStore, domain.Run, domain.AgentNode, []domain.AgentNode) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "dependency.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	_, run, err := application.NewRunService(st).Create(ctx, application.CreateRunRequest{
		Goal: "dependency wait test", Profile: "code", Budget: domain.Budget{MaxTurns: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunService(st).Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	root, _, err := st.RegisterRootAgent(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	coord, err := coordinator.NewWithSpecialistAdmission(st,
		coordinator.SpecialistAdmissionPolicy{MaxChildren: 2, MaxTurnsPerChild: 2,
			MaxTokensPerChild: 32})
	if err != nil {
		t.Fatal(err)
	}
	children := make([]domain.AgentNode, 0, 2)
	for index := 1; index <= 2; index++ {
		admitted, err := coord.AdmitSpecialist(ctx, coordinator.AdmitSpecialistRequest{
			RunID: run.ID, ParentAgentID: root.ID,
			Title: "dependency child", Skills: []string{"model.chat"},
			TurnLimit: 2, TokenLimit: 32,
			IdempotencyKey: idgen.New("dep-admit-") + string(rune('0'+index)),
		})
		if err != nil {
			t.Fatal(err)
		}
		children = append(children, admitted.Agent)
	}
	return st, run, root, children
}

func newDependencyEdge(run domain.Run, source, target domain.AgentNode,
	generation int64,
) domain.DependencyEdge {
	now := time.Now().UTC()
	return domain.DependencyEdge{
		ID: idgen.New("depedge"), RunID: run.ID,
		SourceKind: waitgraph.KindAgent, SourceID: source.ID,
		TargetKind: waitgraph.KindAgent, TargetID: target.ID,
		Reason: "bounded dependency reason", State: domain.AgentDependencyWait,
		FailurePolicy: domain.DependencyPolicyFail, Generation: generation,
		Deadline: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
}

func TestRecordDependencyWaitReplaysAndSettlesOnce(t *testing.T) {
	st, run, root, children := newDependencyTestFixture(t)
	ctx := context.Background()
	edge := newDependencyEdge(run, root, children[0], 1)
	stored, replayed, err := st.RecordDependencyWait(ctx, edge, "dep-record-0001-abcdefgh")
	if err != nil || replayed || stored.ID != edge.ID || stored.State != domain.AgentDependencyWait {
		t.Fatalf("record failed: %#v replayed=%t err=%v", stored, replayed, err)
	}
	stored, replayed, err = st.RecordDependencyWait(ctx, edge, "dep-record-0001-abcdefgh")
	if err != nil || !replayed || stored.ID != edge.ID {
		t.Fatalf("replay failed: %#v replayed=%t err=%v", stored, replayed, err)
	}
	tampered := edge
	tampered.Reason = "different intent"
	if _, _, err := st.RecordDependencyWait(ctx, tampered, "dep-record-0001-abcdefgh"); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("reused key with different intent was not rejected: %v", err)
	}
	wakes, err := st.SettleDependencyTarget(ctx, run.ID, waitgraph.KindAgent,
		children[0].ID, domain.AgentDependencySatisfied, "target completed")
	if err != nil || len(wakes) != 1 || wakes[0].Outcome != domain.AgentDependencySatisfied {
		t.Fatalf("settle failed: %#v err=%v", wakes, err)
	}
	messages, err := st.ListAgentMessages(ctx, root.ID, false, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Semantic != domain.AgentMessageSemanticDependency ||
		messages[0].SenderAgentID != children[0].ID {
		t.Fatalf("source did not receive the dependency notification: %#v", messages)
	}
	wakes, err = st.SettleDependencyTarget(ctx, run.ID, waitgraph.KindAgent,
		children[0].ID, domain.AgentDependencySatisfied, "target completed")
	if err != nil || len(wakes) != 0 {
		t.Fatalf("replayed settle woke again: %#v err=%v", wakes, err)
	}
	edges, err := st.ListDependencyEdges(ctx, run.ID, 8)
	if err != nil || len(edges) != 1 || edges[0].State != domain.AgentDependencySatisfied ||
		edges[0].ResolvedAt == nil {
		t.Fatalf("edge did not reach satisfied: %#v err=%v", edges, err)
	}
}

func TestDependencyWaitRequiresEveryTargetBeforeWakingSpecialist(t *testing.T) {
	st, run, root, children := newDependencyTestFixture(t)
	ctx := t.Context()
	for index, target := range []domain.AgentNode{root, children[1]} {
		if _, _, err := st.RecordDependencyWait(ctx, newDependencyEdge(run, children[0], target, 1),
			"dependency-all-targets-"+string(rune('a'+index))); err != nil {
			t.Fatal(err)
		}
	}
	for index, target := range []domain.AgentNode{root, children[1]} {
		if _, err := st.SettleDependencyTarget(ctx, run.ID, waitgraph.KindAgent, target.ID,
			domain.AgentDependencySatisfied, "target result available"); err != nil {
			t.Fatal(err)
		}
		child, err := st.GetAgentNode(ctx, children[0].ID)
		want := domain.AgentWaiting
		if index == 1 {
			want = domain.AgentReady
		}
		if err != nil || child.Status != want || child.TurnsUsed != 0 {
			t.Fatalf("child woke before all dependencies settled: %#v err=%v", child, err)
		}
	}
}

func TestDependencyFailPolicyPreventsExecutionAfterCancellationOrTimeout(t *testing.T) {
	for _, outcome := range []domain.AgentDependencyState{domain.AgentDependencyCancelled, domain.AgentDependencyExpired} {
		t.Run(string(outcome), func(t *testing.T) {
			st, run, _, children := newDependencyTestFixture(t)
			ctx := t.Context()
			if _, _, err := st.RecordDependencyWait(ctx, newDependencyEdge(run, children[1], children[0], 1),
				"dependency-failed-target"); err != nil {
				t.Fatal(err)
			}
			if _, err := st.SettleDependencyTarget(ctx, run.ID, waitgraph.KindAgent, children[0].ID,
				outcome, "target result unavailable"); err != nil {
				t.Fatal(err)
			}
			child, err := st.GetAgentNode(ctx, children[1].ID)
			if err != nil || child.Status != domain.AgentFailed || child.TurnsUsed != 0 {
				t.Fatalf("unavailable dependency did not fail child without execution: %#v err=%v", child, err)
			}
		})
	}
}

func TestDependencyWaitRejectsCyclesAndForeignEndpoints(t *testing.T) {
	st, run, root, children := newDependencyTestFixture(t)
	ctx := context.Background()
	first := newDependencyEdge(run, root, children[0], 1)
	if _, _, err := st.RecordDependencyWait(ctx, first, "dep-cycle-0001-abcdefgh"); err != nil {
		t.Fatal(err)
	}
	second := newDependencyEdge(run, children[0], children[1], 1)
	if _, _, err := st.RecordDependencyWait(ctx, second, "dep-cycle-0002-abcdefgh"); err != nil {
		t.Fatal(err)
	}
	cycle := newDependencyEdge(run, children[1], root, 1)
	if _, _, err := st.RecordDependencyWait(ctx, cycle, "dep-cycle-0003-abcdefgh"); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("multi-node cycle was not rejected: %v", err)
	}
	selfLoop := newDependencyEdge(run, root, root, 1)
	if _, _, err := st.RecordDependencyWait(ctx, selfLoop, "dep-cycle-0004-abcdefgh"); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("self-loop was not rejected: %v", err)
	}
	_, otherRun, err := application.NewRunService(st).Create(ctx, application.CreateRunRequest{
		Goal: "other mission", Profile: "code", Budget: domain.Budget{MaxTurns: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	foreign := newDependencyEdge(run, root, children[0], 2)
	foreign.RunID = otherRun.ID
	if _, _, err := st.RecordDependencyWait(ctx, foreign, "dep-cycle-0005-abcdefgh"); apperror.CodeOf(err) != apperror.CodeNotFound {
		t.Fatalf("cross-run endpoint was not rejected: %v", err)
	}
	toolEdge := newDependencyEdge(run, root, children[0], 2)
	toolEdge.SourceKind = waitgraph.KindTool
	if _, _, err := st.RecordDependencyWait(ctx, toolEdge, "dep-cycle-0006-abcdefgh"); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("non-agent durable endpoint was not rejected: %v", err)
	}
}

func TestDependencyFailurePolicyPropagation(t *testing.T) {
	t.Run("fail", func(t *testing.T) {
		st, run, root, children := newDependencyTestFixture(t)
		ctx := context.Background()
		if _, err := st.db.ExecContext(ctx, `UPDATE agent_nodes SET status = 'waiting' WHERE id = ?`,
			root.ID); err != nil {
			t.Fatal(err)
		}
		edge := newDependencyEdge(run, root, children[0], 1)
		if _, _, err := st.RecordDependencyWait(ctx, edge, "dep-policy-fail-0001-abcdefgh"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.SettleDependencyTarget(ctx, run.ID, waitgraph.KindAgent,
			children[0].ID, domain.AgentDependencyFailed, "target failed"); err != nil {
			t.Fatal(err)
		}
		node, err := st.GetAgentNode(ctx, root.ID)
		if err != nil || node.Status != domain.AgentFailed {
			t.Fatalf("fail policy did not fail the waiting source: %#v err=%v", node, err)
		}
	})
	t.Run("notify", func(t *testing.T) {
		st, run, root, children := newDependencyTestFixture(t)
		ctx := context.Background()
		if _, err := st.db.ExecContext(ctx, `UPDATE agent_nodes SET status = 'waiting' WHERE id = ?`,
			root.ID); err != nil {
			t.Fatal(err)
		}
		edge := newDependencyEdge(run, root, children[0], 1)
		edge.FailurePolicy = domain.DependencyPolicyNotify
		if _, _, err := st.RecordDependencyWait(ctx, edge, "dep-policy-notify-0001-abcdefgh"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.SettleDependencyTarget(ctx, run.ID, waitgraph.KindAgent,
			children[0].ID, domain.AgentDependencyFailed, "target failed"); err != nil {
			t.Fatal(err)
		}
		node, err := st.GetAgentNode(ctx, root.ID)
		if err != nil || node.Status != domain.AgentReady {
			t.Fatalf("notify policy did not wake the waiting source: %#v err=%v", node, err)
		}
	})
}

func TestDependencyCancelAndExpire(t *testing.T) {
	st, run, root, children := newDependencyTestFixture(t)
	ctx := context.Background()
	edge := newDependencyEdge(run, root, children[0], 1)
	if _, _, err := st.RecordDependencyWait(ctx, edge, "dep-cancel-0001-abcdefgh"); err != nil {
		t.Fatal(err)
	}
	wakes, err := st.CancelDependencySource(ctx, run.ID, waitgraph.KindAgent, root.ID,
		"parent cancelled")
	if err != nil || len(wakes) != 1 || wakes[0].Outcome != domain.AgentDependencyCancelled {
		t.Fatalf("cancel fan-down failed: %#v err=%v", wakes, err)
	}
	overdue := newDependencyEdge(run, root, children[1], 2)
	overdue.Deadline = time.Now().UTC().Add(-time.Minute)
	overdue.CreatedAt = time.Now().UTC().Add(-2 * time.Minute)
	overdue.UpdatedAt = overdue.CreatedAt
	if _, _, err := st.RecordDependencyWait(ctx, overdue, "dep-expire-0001-abcdefgh"); err != nil {
		t.Fatal(err)
	}
	wakes, err = st.ExpireOverdueDependencyEdges(ctx, run.ID, time.Now().UTC())
	if err != nil || len(wakes) != 1 || wakes[0].Outcome != domain.AgentDependencyExpired {
		t.Fatalf("deadline expiry failed: %#v err=%v", wakes, err)
	}
	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range timeline {
		if event.Type == "dependency.deadlock_detected" {
			found = true
		}
	}
	if !found {
		t.Fatal("no-progress deadline did not emit the deadlock diagnosis")
	}
}

func TestDependencyReconcileRecoversAcrossRestart(t *testing.T) {
	st, run, root, children := newDependencyTestFixture(t)
	ctx := context.Background()
	// Edge written, then the worker crashes before settling.
	edge := newDependencyEdge(run, children[0], root, 1)
	if _, _, err := st.RecordDependencyWait(ctx, edge, "dep-reconcile-0001-abcdefgh"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE agent_nodes SET status = 'completed', finished_at = ? WHERE id = ?`,
		ts(time.Now().UTC()), root.ID); err != nil {
		t.Fatal(err)
	}
	wakes, err := st.ReconcileDependencyEdges(ctx, run.ID)
	if err != nil || len(wakes) != 1 || wakes[0].Outcome != domain.AgentDependencySatisfied {
		t.Fatalf("reconcile did not settle the completed target: %#v err=%v", wakes, err)
	}
	// The second recovery pass must not wake or settle again.
	wakes, err = st.ReconcileDependencyEdges(ctx, run.ID)
	if err != nil || len(wakes) != 0 {
		t.Fatalf("reconcile replayed instead of recovering: %#v err=%v", wakes, err)
	}
	// A run that reaches a terminal state cancels its open waits.
	late := newDependencyEdge(run, root, children[1], 2)
	if _, _, err := st.RecordDependencyWait(ctx, late, "dep-reconcile-0002-abcdefgh"); err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunService(st).Complete(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	// The run-terminal hook already fanned the cancellation down; the recovery
	// pass must be idempotent and wake nothing again.
	edges, err := st.ListDependencyEdges(ctx, run.ID, 8)
	if err != nil {
		t.Fatal(err)
	}
	var cancelled *domain.DependencyEdge
	for index := range edges {
		if edges[index].ID == late.ID {
			cancelled = &edges[index]
		}
	}
	if cancelled == nil || cancelled.State != domain.AgentDependencyCancelled ||
		cancelled.ResolvedAt == nil {
		t.Fatalf("terminal run did not fan down the open wait: %#v", edges)
	}
	wakes, err = st.ReconcileDependencyEdges(ctx, run.ID)
	if err != nil || len(wakes) != 0 {
		t.Fatalf("reconcile after terminal fan-down was not idempotent: %#v err=%v", wakes, err)
	}
}

func TestDependencyPollingLivelockDiagnosis(t *testing.T) {
	st, run, root, children := newDependencyTestFixture(t)
	ctx := context.Background()
	generation := int64(1)
	var livelockErr error
	for generation <= domain.DependencyPollingLivelockLimit+1 {
		edge := newDependencyEdge(run, root, children[0], generation)
		stored, _, err := st.RecordDependencyWait(ctx, edge, idgen.New("dep-poll"))
		if err != nil {
			livelockErr = err
			break
		}
		if _, err := st.SettleDependencyTarget(ctx, run.ID, waitgraph.KindAgent,
			children[0].ID, domain.AgentDependencySatisfied, "polling wake"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.ConsumeAgentMessages(ctx, root.ID, 16); err != nil {
			t.Fatal(err)
		}
		_ = stored
		generation++
	}
	if apperror.CodeOf(livelockErr) != apperror.CodeLivelock {
		t.Fatalf("polling livelock was not diagnosed: %v", livelockErr)
	}
	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range timeline {
		if event.Type == "dependency.livelock_detected" {
			found = true
		}
	}
	if !found {
		t.Fatal("polling livelock did not emit the diagnosis event")
	}
}

func TestDependencyStallDetectionIsReadOnly(t *testing.T) {
	st, run, root, children := newDependencyTestFixture(t)
	ctx := context.Background()
	overdue := newDependencyEdge(run, root, children[0], 1)
	overdue.Deadline = time.Now().UTC().Add(-time.Minute)
	overdue.CreatedAt = time.Now().UTC().Add(-2 * time.Minute)
	overdue.UpdatedAt = overdue.CreatedAt
	if _, _, err := st.RecordDependencyWait(ctx, overdue, "dep-detect-0001-abcdefgh"); err != nil {
		t.Fatal(err)
	}
	diagnosis, err := st.DetectDependencyStalls(ctx, run.ID, time.Now().UTC())
	if err != nil || len(diagnosis.DeadlockedEdgeIDs) != 1 ||
		diagnosis.DeadlockedEdgeIDs[0] != overdue.ID {
		t.Fatalf("stall diagnosis is wrong: %#v err=%v", diagnosis, err)
	}
	edges, err := st.ListDependencyEdges(ctx, run.ID, 8)
	if err != nil || len(edges) != 1 || edges[0].State != domain.AgentDependencyWait {
		t.Fatalf("detection mutated the ledger: %#v err=%v", edges, err)
	}
}
