package store

import (
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

func newChildTaskDependencyProposal(t *testing.T) (*SQLiteStore, domain.Run, domain.ChildTaskProposal) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "child-dependencies.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	_, run, err := application.NewRunService(st).Create(ctx, application.CreateRunRequest{
		Goal: "review dependent child tasks", Profile: "code", Budget: domain.Budget{MaxTurns: 10},
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
	spec, err := domain.NormalizeChildTaskProposalSpec(domain.ChildTaskProposalSpec{
		Version: domain.ChildTaskProposalVersion, Tasks: []domain.ChildTask{
			{Title: "first", Goal: "produce the finding", Skills: []string{"model.chat"},
				SurfaceHint: domain.ChildTaskSurfaceHintCore, TurnLimit: 2, TokenLimit: 256, TimeoutMillis: 60000},
			{Title: "second", Goal: "review the first finding", Skills: []string{"model.chat"},
				SurfaceHint: domain.ChildTaskSurfaceHintCore, TurnLimit: 2, TokenLimit: 256, TimeoutMillis: 60000,
				DependencyOrdinals: []int{1}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	proposal := domain.ChildTaskProposal{ID: idgen.New("childtask"), RunID: run.ID,
		RootAgentID: root.ID, SessionID: run.SessionID, WorkspaceID: run.MissionID,
		Status: domain.ChildTaskProposalProposed, Spec: spec, Surface: domain.ChildTaskSurfaceCore,
		RequestedBy: "run_supervisor", Version: 1, CreatedAt: now}
	operation := domain.ChildTaskOperation{KeyDigest: runmutation.Fingerprint("child-dependency-proposal", proposal.ID),
		RequestFingerprint: runmutation.Fingerprint("child-dependency-request", spec.SpecJSONFingerprint()), ProposalID: proposal.ID, RunID: run.ID,
		SessionID: run.SessionID, WorkspaceID: run.MissionID, RootAgentID: root.ID,
		LeaseID: idgen.New("lease"), LeaseGeneration: 1, RequestedBy: "run_supervisor", CreatedAt: now}
	policyEvent, _ := events.New(run.ID, run.MissionID, events.PolicyDecisionEvent, "policy", idgen.New("inv"), map[string]any{"allowed": true})
	proposalEvent, _ := events.New(run.ID, run.MissionID, events.ChildTaskProposedEvent, "agent_coordinator", proposal.ID, map[string]any{"status": "proposed"})
	toolEvent, _ := events.New(run.ID, run.MissionID, events.ToolCompletedEvent, "agent_proposal_tool", idgen.New("inv"), map[string]any{"tool_name": "child_task_propose"})
	if _, _, err := st.CreateChildTaskProposal(ctx, operation, proposal, policyEvent, proposalEvent, toolEvent); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ReviewChildTaskProposal(ctx, domain.ChildTaskReview{
		ProposalID: proposal.ID, Action: "approve", Reviewer: "operator", FanoutTier: domain.ReadOnlyFanoutTwo,
	}, "child-dependency-review"); err != nil {
		t.Fatal(err)
	}
	return st, run, proposal
}

func TestChildTaskAdmissionRecoversDependencyCommitAndDelayedReplay(t *testing.T) {
	st, run, proposal := newChildTaskDependencyProposal(t)
	ctx := t.Context()
	key := "child-dependency-admit"
	if _, err := st.db.ExecContext(ctx, `CREATE TRIGGER interrupt_child_instruction
		BEFORE INSERT ON agent_messages WHEN NEW.kind = 'instruction' AND EXISTS (
			SELECT 1 FROM child_task_assignments WHERE ordinal = 2 AND admitted_agent_id = NEW.recipient_agent_id)
		BEGIN SELECT RAISE(ABORT, 'interrupted child instruction'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.AdmitChildTaskProposal(ctx, proposal.ID, key); err == nil ||
		!strings.Contains(err.Error(), "interrupted child instruction") {
		t.Fatalf("expected interrupted instruction, got %v", err)
	}
	assignments, err := st.ListChildTaskAssignments(ctx, proposal.ID)
	if err != nil || len(assignments) != 2 || assignments[1].AdmittedAgentID == "" ||
		assignments[1].Status != domain.ChildTaskAssignmentProposed {
		t.Fatalf("child creation did not persist its assignment binding: %#v err=%v", assignments, err)
	}
	childID := assignments[1].AdmittedAgentID
	lease := acquireTestRunExecutionLease(t, ctx, st, run.ID)
	if _, _, err := st.SendAgentMessage(ctx, domain.AgentMessage{
		ID: idgen.New("agentmsg"), RunID: run.ID, RecipientAgentID: childID,
		Kind: domain.AgentMessageControl, Semantic: domain.AgentMessageSemanticWake,
		PayloadJSON: `{"reason":"retry interrupted admission"}`,
	}, "child-dependency-interrupted-wake"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.BeginSpecialistAttempt(ctx, domain.AgentAttemptStart{
		AttemptID: idgen.New("attempt"), RunID: run.ID, AgentID: childID,
		ParentAgentID: proposal.RootAgentID, Lease: lease,
	}, "child-dependency-interrupted-start"); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("missing dependency edge allowed execution after wake: %v", err)
	}
	if _, err := st.ReconcileDependencyEdges(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ReleaseRunExecutionLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `DROP TRIGGER interrupt_child_instruction`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `CREATE TRIGGER interrupt_dependency_binding
		BEFORE INSERT ON agent_dependency_edges
		BEGIN SELECT RAISE(ABORT, 'interrupted dependency binding'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.AdmitChildTaskProposal(ctx, proposal.ID, key); err == nil ||
		!strings.Contains(err.Error(), "interrupted dependency binding") {
		t.Fatalf("expected interrupted dependency binding, got %v", err)
	}
	assignments, err = st.ListChildTaskAssignments(ctx, proposal.ID)
	if err != nil || len(assignments) != 2 {
		t.Fatalf("interrupted admission lost assignments: %#v err=%v", assignments, err)
	}
	child, err := st.GetAgentNode(ctx, assignments[1].AdmittedAgentID)
	if err != nil || child.Status != domain.AgentWaiting || child.TurnsUsed != 0 {
		t.Fatalf("child became runnable before dependency binding: %#v err=%v", child, err)
	}
	if _, err := st.db.ExecContext(ctx, `DROP TRIGGER interrupt_dependency_binding`); err != nil {
		t.Fatal(err)
	}
	// Interrupt after all children and the dependency have committed, before
	// the admission acknowledgement is durable.
	if _, err := st.db.ExecContext(ctx, `CREATE TRIGGER interrupt_child_admission
		BEFORE INSERT ON run_events WHEN NEW.type = 'agent.child_task_admitted'
		BEGIN SELECT RAISE(ABORT, 'interrupted after dependency commit'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.AdmitChildTaskProposal(ctx, proposal.ID, key); err == nil ||
		!strings.Contains(err.Error(), "interrupted after dependency commit") {
		t.Fatalf("expected interrupted acknowledgement, got %v", err)
	}
	edges, err := st.ListDependencyEdges(ctx, run.ID, 8)
	if err != nil || len(edges) != 1 {
		t.Fatalf("dependency did not commit before interruption: %#v err=%v", edges, err)
	}
	original := edges[0]
	if _, err := st.db.ExecContext(ctx, `DROP TRIGGER interrupt_child_admission`); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	for _, retryKey := range []string{key, "child-dependency-admit-new-request", key} {
		_, assignments, err := st.AdmitChildTaskProposal(ctx, proposal.ID, retryKey)
		if err != nil || len(assignments) != 2 {
			t.Fatalf("admission replay failed: %#v err=%v", assignments, err)
		}
		edges, err = st.ListDependencyEdges(ctx, run.ID, 8)
		if err != nil || len(edges) != 1 || edges[0].ID != original.ID ||
			!edges[0].Deadline.Equal(original.Deadline) || edges[0].State != domain.AgentDependencyWait {
			t.Fatalf("replay changed dependency identity/deadline: %#v err=%v", edges, err)
		}
		nodes, err := st.ListAgentNodes(ctx, run.ID)
		if err != nil || len(nodes) != 3 {
			t.Fatalf("replay duplicated children: %#v err=%v", nodes, err)
		}
	}
}

func TestChildTaskDependenciesFenceExecutionAndPropagateTerminalOutcomes(t *testing.T) {
	for _, outcome := range []domain.AgentStatus{domain.AgentCompleted, domain.AgentFailed} {
		t.Run(string(outcome), func(t *testing.T) {
			st, run, proposal := newChildTaskDependencyProposal(t)
			ctx := t.Context()
			_, assignments, err := st.AdmitChildTaskProposal(ctx, proposal.ID, "child-dependency-admit")
			if err != nil {
				t.Fatal(err)
			}
			targetID, sourceID := assignments[0].AdmittedAgentID, assignments[1].AdmittedAgentID
			source, err := st.GetAgentNode(ctx, sourceID)
			if err != nil || source.Status != domain.AgentWaiting || source.TurnsUsed != 0 {
				t.Fatalf("dependent child was not admitted waiting: %#v err=%v", source, err)
			}
			lease := acquireTestRunExecutionLease(t, ctx, st, run.ID)
			// A manual wake must not bypass the dependency check in the start transaction.
			if _, _, err := st.SendAgentMessage(ctx, domain.AgentMessage{
				ID: idgen.New("agentmsg"), RunID: run.ID, RecipientAgentID: sourceID,
				Kind: domain.AgentMessageControl, Semantic: domain.AgentMessageSemanticWake,
				PayloadJSON: `{"reason":"operator retry"}`,
			}, "child-dependency-manual-wake"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := st.BeginSpecialistAttempt(ctx, domain.AgentAttemptStart{
				AttemptID: idgen.New("attempt"), RunID: run.ID, AgentID: sourceID,
				ParentAgentID: proposal.RootAgentID, Lease: lease,
			}, "child-dependency-blocked-start"); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
				t.Fatalf("unsatisfied dependency allowed execution: %v", err)
			}
			source, err = st.GetAgentNode(ctx, sourceID)
			if err != nil || source.TurnsUsed != 0 || source.ActiveAttemptID != "" {
				t.Fatalf("blocked execution consumed a turn: %#v err=%v", source, err)
			}
			attempt, _, err := st.BeginSpecialistAttempt(ctx, domain.AgentAttemptStart{
				AttemptID: idgen.New("attempt"), RunID: run.ID, AgentID: targetID,
				ParentAgentID: proposal.RootAgentID, Lease: lease,
			}, "child-dependency-target-start")
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := st.RecordSpecialistAttemptUsage(ctx, domain.AgentAttemptRef{
				RunID: run.ID, AgentID: targetID, AttemptID: attempt.ID,
			}, domain.AgentAttemptUsage{InputTokens: 255, OutputTokens: 1, TotalTokens: 256}, "child-dependency-target-usage"); err != nil {
				t.Fatal(err)
			}
			if outcome == domain.AgentCompleted {
				if _, _, err := st.FinishSpecialist(ctx, newCompletionTestValue(run.ID, proposal.RootAgentID,
					targetID, attempt.ID, domain.CompletionSucceeded, nil, nil, "dependency target finished"), "child-dependency-target-finish"); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, _, err := st.CrashSpecialistAttempt(ctx, domain.AgentAttemptFailureRequest{
					Ref:                   domain.AgentAttemptRef{RunID: run.ID, AgentID: targetID, AttemptID: attempt.ID},
					Failure:               domain.AgentAttemptFailure{Code: "provider_error", Reason: "target could not finish"},
					NotificationMessageID: idgen.New("agentmsg"), FailedAt: time.Now().UTC(),
				}, "child-dependency-target-fail"); err != nil {
					t.Fatal(err)
				}
			}
			wakes, err := st.ReconcileDependencyEdges(ctx, run.ID)
			if err != nil || len(wakes) != 1 {
				t.Fatalf("dependency was not settled: %#v err=%v", wakes, err)
			}
			source, err = st.GetAgentNode(ctx, sourceID)
			want := domain.AgentReady
			if outcome == domain.AgentFailed {
				want = domain.AgentFailed
			}
			if err != nil || source.Status != want || source.TurnsUsed != 0 {
				t.Fatalf("dependency outcome did not propagate: %#v err=%v", source, err)
			}
			_, _, err = st.BeginSpecialistAttempt(ctx, domain.AgentAttemptStart{
				AttemptID: idgen.New("attempt"), RunID: run.ID, AgentID: sourceID,
				ParentAgentID: proposal.RootAgentID, Lease: lease,
			}, "child-dependency-after-settle")
			if outcome == domain.AgentCompleted && err != nil ||
				outcome == domain.AgentFailed && apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
				t.Fatalf("settled dependency allowed wrong execution: %v", err)
			}
			if _, _, err := st.AdmitChildTaskProposal(ctx, proposal.ID, "child-dependency-after-settle-replay"); err != nil {
				t.Fatalf("settled dependency admission replay failed: %v", err)
			}
			if wakes, err := st.ReconcileDependencyEdges(ctx, run.ID); err != nil || len(wakes) != 0 {
				t.Fatalf("settled dependency woke twice: %#v err=%v", wakes, err)
			}
		})
	}
}
