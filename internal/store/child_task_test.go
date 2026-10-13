package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
)

func TestChildTaskProposalReplaysDeduplicatesReviewsAndAdmits(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "child-task.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	_, run, err := application.NewRunService(st).Create(ctx, application.CreateRunRequest{
		Goal: "child task proposal test", Profile: "code", Budget: domain.Budget{MaxTurns: 10, MaxTokens: 100000},
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
	now := time.Now().UTC()
	spec := domain.ChildTaskProposalSpec{
		Version: domain.ChildTaskProposalVersion,
		Tasks: []domain.ChildTask{
			{Title: "first", Goal: "Read notes.txt and report its exact proof after a second review turn.",
				Skills: []string{"model.chat"}, SurfaceHint: domain.ChildTaskSurfaceHintCore,
				TurnLimit: 2, TokenLimit: 256, TimeoutMillis: 60000},
			{Title: "second", Goal: "review the first finding", Skills: []string{"model.chat"},
				SurfaceHint:        domain.ChildTaskSurfaceHintCore,
				DependencyOrdinals: []int{1}, TurnLimit: 2, TokenLimit: 256, TimeoutMillis: 60000},
		},
	}
	spec, err = domain.NormalizeChildTaskProposalSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	surface, tier, err := domain.ResolveChildTaskSurface(spec)
	if err != nil || surface != domain.ChildTaskSurfaceCore || tier != "" {
		t.Fatalf("surface resolution failed: %q %q %v", surface, tier, err)
	}
	proposal := domain.ChildTaskProposal{
		ID: idgen.New("childtask"), RunID: run.ID, RootAgentID: root.ID,
		SessionID: run.SessionID, WorkspaceID: run.MissionID,
		Status: domain.ChildTaskProposalProposed, Spec: spec, Surface: surface,
		RequestedBy: "run_supervisor", Version: 1, CreatedAt: now,
	}
	opKey := "child-task-op-0001-abcdefgh"
	operation := domain.ChildTaskOperation{
		KeyDigest:          runmutation.OperationKeyDigest("child_task_propose", run.ID, opKey),
		RequestFingerprint: runmutation.Fingerprint("child_task_request.v1", run.ID, spec.SpecJSONFingerprint()),
		ProposalID:         proposal.ID, RunID: run.ID, SessionID: run.SessionID,
		WorkspaceID: run.MissionID, RootAgentID: root.ID,
		LeaseID: idgen.New("lease"), LeaseGeneration: 1,
		RequestedBy: "run_supervisor", CreatedAt: now,
	}
	policyEvent, _ := events.New(run.ID, run.MissionID, events.PolicyDecisionEvent, "policy", idgen.New("inv"), map[string]any{"allowed": true})
	proposalEvent, _ := events.New(run.ID, run.MissionID, events.ChildTaskProposedEvent, "agent_coordinator", proposal.ID, map[string]any{"status": "proposed"})
	toolEvent, _ := events.New(run.ID, run.MissionID, events.ToolCompletedEvent, "agent_proposal_tool", idgen.New("inv2"), map[string]any{"tool_name": "child_task_propose"})
	stored, replayed, err := st.CreateChildTaskProposal(ctx, operation, proposal, policyEvent, proposalEvent, toolEvent)
	if err != nil || replayed || stored.ID != proposal.ID {
		t.Fatalf("create failed: %#v replayed=%t err=%v", stored, replayed, err)
	}
	// Same key replays; an equal spec under a different key dedupes.
	if _, replayed, err := st.CreateChildTaskProposal(ctx, operation, proposal, policyEvent, proposalEvent, toolEvent); err != nil || !replayed {
		t.Fatalf("replay failed: replayed=%t err=%v", replayed, err)
	}
	operation.KeyDigest = runmutation.OperationKeyDigest("child_task_propose", run.ID, "child-task-op-0002-abcdefgh")
	operation.RequestFingerprint = runmutation.Fingerprint("child_task_request.v1", run.ID, spec.SpecJSONFingerprint())
	operation.ProposalID = idgen.New("childtask")
	duplicate, replayed, err := st.CreateChildTaskProposal(ctx, operation, proposal, policyEvent, proposalEvent, toolEvent)
	if err != nil || !replayed || duplicate.ID != proposal.ID {
		t.Fatalf("dedup failed: %#v replayed=%t err=%v", duplicate, replayed, err)
	}
	// Review approve pins the operator tier ceiling.
	reviewed, replayed, err := st.ReviewChildTaskProposal(ctx, domain.ChildTaskReview{
		ProposalID: proposal.ID, Action: "approve", Reviewer: "operator", FanoutTier: domain.ReadOnlyFanoutTwo,
	}, "child-task-review-0001-abcdefgh")
	if err != nil || replayed || reviewed.Status != domain.ChildTaskProposalApproved {
		t.Fatalf("review failed: %#v replayed=%t err=%v", reviewed, replayed, err)
	}
	// Recover both possible interruptions after the child is created, even
	// when the operator retries with a new request key.
	var recoveredAgentID string
	for index, failure := range []struct {
		trigger      string
		messageCount int
	}{
		{trigger: `CREATE TRIGGER fail_child_task_admission BEFORE INSERT ON agent_messages
			BEGIN SELECT RAISE(ABORT, 'forced child task delivery failure'); END`, messageCount: 0},
		{trigger: `CREATE TRIGGER fail_child_task_admission BEFORE UPDATE OF status ON child_task_assignments
			WHEN NEW.status = 'admitted'
			BEGIN SELECT RAISE(ABORT, 'forced child task assignment failure'); END`, messageCount: 1},
	} {
		if _, err := st.db.ExecContext(ctx, failure.trigger); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.AdmitChildTaskProposal(ctx, proposal.ID,
			"child-task-admit-retry-abcdefgh-"+string(rune('a'+index))); err == nil ||
			!strings.Contains(err.Error(), "forced child task") {
			t.Fatalf("expected interrupted admission, got %v", err)
		}
		if _, err := st.db.ExecContext(ctx, `DROP TRIGGER fail_child_task_admission`); err != nil {
			t.Fatal(err)
		}
		nodes, err := st.ListAgentNodes(ctx, run.ID)
		if err != nil || len(nodes) != 2 {
			t.Fatalf("interrupted admission created duplicate children: %#v err=%v", nodes, err)
		}
		for _, node := range nodes {
			if node.Role != domain.AgentRoleSpecialist {
				continue
			}
			if recoveredAgentID != "" && node.ID != recoveredAgentID {
				t.Fatalf("retry replaced the child: got %q, want %q", node.ID, recoveredAgentID)
			}
			recoveredAgentID = node.ID
			messages, err := st.ListAgentMessages(ctx, node.ID, false, 10)
			if err != nil || len(messages) != failure.messageCount {
				t.Fatalf("interrupted delivery count is wrong: %#v err=%v", messages, err)
			}
		}
	}
	// Admission creates both core children and binds the declared dependency
	// onto the schema v101 wait ledger.
	admitted, assignments, err := st.AdmitChildTaskProposal(ctx, proposal.ID, "child-task-admit-0001-abcdefgh")
	if err != nil {
		t.Fatal(err)
	}
	if admitted.Status != domain.ChildTaskProposalApproved || len(assignments) != 2 {
		t.Fatalf("admission result is wrong: %#v %#v", admitted, assignments)
	}
	for _, assignment := range assignments {
		if assignment.Status != domain.ChildTaskAssignmentAdmitted || assignment.AdmittedAgentID == "" {
			t.Fatalf("assignment was not admitted: %#v", assignment)
		}
	}
	if assignments[0].AdmittedAgentID != recoveredAgentID {
		t.Fatal("admission did not recover the existing child")
	}
	if _, _, err := st.AdmitChildTaskProposal(ctx, proposal.ID, "child-task-admit-0001-abcdefgh"); err != nil {
		t.Fatalf("admission replay failed: %v", err)
	}
	nodes, err := st.ListAgentNodes(ctx, run.ID)
	if err != nil || len(nodes) != 3 {
		t.Fatalf("admission replay changed the child count: %#v err=%v", nodes, err)
	}
	for _, node := range nodes {
		if node.Role == domain.AgentRoleSpecialist && (len(node.Skills) != 1 || node.Skills[0] != "model.chat") {
			t.Fatalf("read-only child received extra capabilities: %#v", node)
		}
	}
	for _, assignment := range assignments {
		messages, err := st.ListAgentMessages(ctx, assignment.AdmittedAgentID, false, 10)
		if err != nil || len(messages) != 1 {
			t.Fatalf("child must receive its goal exactly once: %#v err=%v", messages, err)
		}
		message := messages[0]
		instruction, err := domain.DecodeAgentInstructionPayload(message.PayloadJSON)
		if err != nil || instruction.Instruction != spec.Tasks[assignment.Ordinal-1].Goal ||
			message.Kind != domain.AgentMessageInstruction || message.SenderAgentID != root.ID ||
			message.RecipientAgentID != assignment.AdmittedAgentID || message.Status != domain.AgentMessagePending {
			t.Fatalf("child goal was not delivered: %#v payload=%#v err=%v", message, instruction, err)
		}
	}
	edges, err := st.ListDependencyEdges(ctx, run.ID, 8)
	if err != nil || len(edges) != 1 || edges[0].State != domain.AgentDependencyWait {
		t.Fatalf("dependency binding failed: %#v err=%v", edges, err)
	}
}

func TestChildTaskProposalRejectsCycleAndThreeCoreTasks(t *testing.T) {
	if _, err := domain.DecodeChildTaskProposalSpec([]byte(
		`{"version":"child_task_proposal.v1","tasks":[
		{"title":"A","goal":"one","skills":["model.chat","replace_file"],"turn_limit":1,"token_limit":64,"timeout_millis":60000,"dependency_ordinals":[2]},
		{"title":"B","goal":"two","skills":["model.chat","replace_file"],"turn_limit":1,"token_limit":64,"timeout_millis":60000,"dependency_ordinals":[1]}]}`)); err == nil || apperror.CodeOf(err) == apperror.CodeInternal && err == nil {
		t.Fatal("dependency cycle was accepted")
	}
	three := domain.ChildTaskProposalSpec{Version: domain.ChildTaskProposalVersion, Tasks: []domain.ChildTask{
		{Title: "a", Goal: "one", Skills: []string{"model.chat", "replace_file"}, TurnLimit: 1, TokenLimit: 64, TimeoutMillis: 60000},
		{Title: "b", Goal: "two", Skills: []string{"model.chat", "replace_file"}, TurnLimit: 1, TokenLimit: 64, TimeoutMillis: 60000},
		{Title: "c", Goal: "three", Skills: []string{"model.chat", "replace_file"}, TurnLimit: 1, TokenLimit: 64, TimeoutMillis: 60000},
	}}
	if _, _, err := domain.ResolveChildTaskSurface(three); err == nil {
		t.Fatal("three core tasks were accepted")
	}
}
