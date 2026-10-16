package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/coordinator"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/tools"
)

func TestSpecialistDelegationApplicationCreatesTwoBoundedChildren(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "delegation-application.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	run, proposal, review := createApprovedDelegationApplicationFixture(t, ctx, st, 2, true)
	service, err := application.NewDefaultSpecialistDelegationApplicationService(
		st, policy.NewDefaultChecker())
	if err != nil {
		t.Fatal(err)
	}
	request := application.ApplySpecialistDelegationRequest{
		ProposalID: proposal.ID, OperationKey: "delegation-application-happy-0001",
		RequestedBy: review.ReviewedBy,
	}
	result, err := service.Apply(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Application.Status != domain.SpecialistDelegationApplied || result.Replayed ||
		result.Recovered || len(result.Application.Assignments) != 2 {
		t.Fatalf("unexpected application result: %#v", result)
	}
	for _, assignment := range result.Application.Assignments {
		node, e := st.GetAgentNode(ctx, assignment.AgentID)
		proposed := proposal.Spec.Assignments[assignment.Ordinal-1]
		if e != nil || strings.Join(node.AllowedReadPaths, ",") != strings.Join(proposed.AllowedReadPaths, ",") || node.ToolCallLimit != proposed.ToolCallLimit || node.CostLimitMicros != proposed.CostLimitMicros {
			t.Fatalf("approved constraints lost: %+v %v", node, e)
		}
		if assignment.Status != domain.SpecialistDelegationAssignmentInstructed ||
			assignment.AgentID == "" || assignment.MessageID == "" {
			t.Fatalf("assignment was not fully applied: %#v", assignment)
		}
		messages, err := st.ListAgentMessages(ctx, assignment.AgentID, true, 10)
		if err != nil || len(messages) != 1 || messages[0].ID != assignment.MessageID {
			t.Fatalf("instruction was not delivered exactly once: %#v err=%v", messages, err)
		}
		payload, err := domain.DecodeAgentInstructionPayload(messages[0].PayloadJSON)
		if err != nil || payload.Instruction != proposal.Spec.Assignments[assignment.Ordinal-1].Goal {
			t.Fatalf("instruction payload drifted: %#v err=%v", payload, err)
		}
	}
	nodes, err := st.ListAgentNodes(ctx, run.ID)
	if err != nil || len(nodes) != 3 {
		t.Fatalf("application did not create exactly two children: %#v err=%v", nodes, err)
	}
	for _, node := range nodes {
		if node.Role == domain.AgentRoleSpecialist && node.Status != domain.AgentReady {
			t.Fatalf("application unexpectedly scheduled a child: %#v", node)
		}
	}
	var attempts, schedules int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_attempts
		WHERE run_id = ?`, run.ID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM specialist_schedules
		WHERE run_id = ?`, run.ID).Scan(&schedules); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 || schedules != 0 {
		t.Fatalf("application started execution: attempts=%d schedules=%d", attempts, schedules)
	}
	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil || countRunEventType(timeline, events.AgentDelegationApplicationStartedEvent) != 1 ||
		countRunEventType(timeline, events.AgentDelegationAssignmentAdmittedEvent) != 2 ||
		countRunEventType(timeline, events.AgentDelegationInstructionDeliveredEvent) != 2 ||
		countRunEventType(timeline, events.AgentDelegationAppliedEvent) != 1 {
		t.Fatalf("application audit stream is incomplete: %#v err=%v", timeline, err)
	}
	for _, event := range timeline {
		if strings.HasPrefix(event.Type, "agent.delegation_") {
			for _, assignment := range proposal.Spec.Assignments {
				if strings.Contains(event.PayloadJSON, assignment.Goal) ||
					strings.Contains(event.PayloadJSON, assignment.Title) {
					t.Fatalf("application event leaked assignment content: %s", event.PayloadJSON)
				}
			}
		}
	}
	replayed, err := service.Apply(ctx, request)
	if err != nil || !replayed.Replayed || replayed.Application.ID != result.Application.ID {
		t.Fatalf("application replay failed: %#v err=%v", replayed, err)
	}
	changed := request
	changed.OperationKey = "delegation-application-second-key-0002"
	if _, err := service.Apply(ctx, changed); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("second application key did not conflict: code=%s err=%v", apperror.CodeOf(err), err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE specialist_delegation_application_assignments
		SET agent_id = ? WHERE application_id = ? AND ordinal = 1`, "agent-tampered",
		result.Application.ID); err == nil {
		t.Fatal("direct application assignment mutation succeeded")
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM specialist_delegation_applications
		WHERE id = ?`, result.Application.ID); err == nil {
		t.Fatal("direct application deletion succeeded")
	}
	var rawKeyCount int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM specialist_delegation_application_operations
		WHERE operation_key_digest = ? OR request_fingerprint = ?`, request.OperationKey,
		request.OperationKey).Scan(&rawKeyCount); err != nil {
		t.Fatal(err)
	}
	if rawKeyCount != 0 {
		t.Fatal("raw application operation key was persisted")
	}
}

func TestSpecialistDelegationApplicationConvergesAcrossStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegation-application-concurrency.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ctx := context.Background()
	_, proposal, review := createApprovedDelegationApplicationFixture(t, ctx, st, 2)
	services := make([]*application.SpecialistDelegationApplicationService, 2)
	services[0], err = application.NewDefaultSpecialistDelegationApplicationService(
		st, policy.NewDefaultChecker())
	if err != nil {
		t.Fatal(err)
	}
	services[1], err = application.NewDefaultSpecialistDelegationApplicationService(
		other, policy.NewDefaultChecker())
	if err != nil {
		t.Fatal(err)
	}
	request := application.ApplySpecialistDelegationRequest{
		ProposalID: proposal.ID, OperationKey: "delegation-application-concurrent",
		RequestedBy: review.ReviewedBy,
	}
	const workers = 8
	type applyResult struct {
		result application.ApplySpecialistDelegationResult
		err    error
	}
	results := make(chan applyResult, workers)
	var wg sync.WaitGroup
	for index := range workers {
		wg.Add(1)
		go func(service *application.SpecialistDelegationApplicationService) {
			defer wg.Done()
			result, err := service.Apply(ctx, request)
			results <- applyResult{result: result, err: err}
		}(services[index%len(services)])
	}
	wg.Wait()
	close(results)
	applicationIDs := map[string]bool{}
	for current := range results {
		if current.err != nil {
			t.Fatal(current.err)
		}
		applicationIDs[current.result.Application.ID] = true
	}
	if len(applicationIDs) != 1 {
		t.Fatalf("concurrent application IDs diverged: %#v", applicationIDs)
	}
	nodes, err := st.ListAgentNodes(ctx, proposal.RunID)
	if err != nil || len(nodes) != 3 {
		t.Fatalf("concurrent application duplicated children: %#v err=%v", nodes, err)
	}
	applicationState, found, err := st.GetSpecialistDelegationApplicationByProposal(ctx,
		proposal.ID)
	if err != nil || !found || applicationState.Status != domain.SpecialistDelegationApplied {
		t.Fatalf("concurrent application did not converge: %#v err=%v", applicationState, err)
	}
	for _, assignment := range applicationState.Assignments {
		messages, err := st.ListAgentMessages(ctx, assignment.AgentID, true, 10)
		if err != nil || len(messages) != 1 {
			t.Fatalf("concurrent application duplicated instruction: %#v err=%v", messages, err)
		}
	}
	timeline, err := st.ListRunEvents(ctx, proposal.RunID)
	if err != nil || countRunEventType(timeline, events.AgentDelegationApplicationStartedEvent) != 1 ||
		countRunEventType(timeline, events.AgentDelegationAppliedEvent) != 1 {
		t.Fatalf("concurrent lifecycle events diverged: %#v err=%v", timeline, err)
	}
}

func TestSpecialistDelegationApplicationRecoversAfterAdmissionCommit(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "delegation-application-admission-recovery.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	_, proposal, review := createApprovedDelegationApplicationFixture(t, ctx, st, 1)
	failing := &failOnceDelegationApplicationStore{SQLiteStore: st, failAdmittedMark: true}
	service, err := application.NewDefaultSpecialistDelegationApplicationService(
		failing, policy.NewDefaultChecker())
	if err != nil {
		t.Fatal(err)
	}
	request := application.ApplySpecialistDelegationRequest{
		ProposalID: proposal.ID, OperationKey: "delegation-application-recover-admit",
		RequestedBy: review.ReviewedBy,
	}
	if _, err := service.Apply(ctx, request); err == nil {
		t.Fatal("injected admitted-mark failure did not stop application")
	}
	nodes, err := st.ListAgentNodes(ctx, proposal.RunID)
	if err != nil || len(nodes) != 2 {
		t.Fatalf("admission was not committed before injected failure: %#v err=%v", nodes, err)
	}
	pending, found, err := st.GetSpecialistDelegationApplicationByProposal(ctx, proposal.ID)
	if err != nil || !found || pending.Assignments[0].Status != domain.SpecialistDelegationAssignmentPending {
		t.Fatalf("application did not preserve its recoverable pending state: %#v err=%v", pending, err)
	}
	recovered, err := service.Apply(ctx, request)
	if err != nil || !recovered.Recovered || recovered.Application.Status != domain.SpecialistDelegationApplied {
		t.Fatalf("application did not recover: %#v err=%v", recovered, err)
	}
	nodes, err = st.ListAgentNodes(ctx, proposal.RunID)
	if err != nil || len(nodes) != 2 {
		t.Fatalf("recovery duplicated child: %#v err=%v", nodes, err)
	}
}

func TestSpecialistDelegationApplicationRecoversAfterInstructionCommit(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "delegation-application-instruction-recovery.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	_, proposal, review := createApprovedDelegationApplicationFixture(t, ctx, st, 1)
	failing := &failOnceDelegationApplicationStore{SQLiteStore: st, failInstructionMark: true}
	service, err := application.NewDefaultSpecialistDelegationApplicationService(
		failing, policy.NewDefaultChecker())
	if err != nil {
		t.Fatal(err)
	}
	request := application.ApplySpecialistDelegationRequest{
		ProposalID: proposal.ID, OperationKey: "delegation-application-recover-message",
		RequestedBy: review.ReviewedBy,
	}
	if _, err := service.Apply(ctx, request); err == nil {
		t.Fatal("injected instruction-mark failure did not stop application")
	}
	pending, found, err := st.GetSpecialistDelegationApplicationByProposal(ctx, proposal.ID)
	if err != nil || !found || pending.Assignments[0].Status != domain.SpecialistDelegationAssignmentAdmitted {
		t.Fatalf("application did not preserve admitted state: %#v err=%v", pending, err)
	}
	messages, err := st.ListAgentMessages(ctx, pending.Assignments[0].AgentID, true, 10)
	if err != nil || len(messages) != 1 {
		t.Fatalf("instruction was not committed before injected failure: %#v err=%v", messages, err)
	}
	lease, err := st.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{
		RunID: proposal.RunID, OwnerID: "blocked-child-scheduler", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.BeginSpecialistAttempt(ctx, domain.AgentAttemptStart{
		AttemptID: idgen.New("agent-attempt"), RunID: proposal.RunID,
		AgentID: pending.Assignments[0].AgentID, ParentAgentID: proposal.RootAgentID,
		Lease: lease.Lease, StartedAt: time.Now().UTC(),
	}, "blocked-attempt-during-application"); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("child scheduling bypassed application reservation: code=%s err=%v",
			apperror.CodeOf(err), err)
	}
	usage, err := st.GetRunAgentUsage(ctx, proposal.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.StartSpecialistSchedule(ctx, domain.SpecialistScheduleStart{
		ID: "blocked-delegation-application-schedule", RunID: proposal.RunID,
		AgentIDs: []string{pending.Assignments[0].AgentID}, MaxRounds: 1,
		Lease: lease.Lease, UsageBefore: usage, StartedAt: time.Now().UTC(),
	}); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("Specialist schedule bypassed application reservation: code=%s err=%v",
			apperror.CodeOf(err), err)
	}
	if _, _, err := st.ReleaseRunExecutionLease(ctx, lease.Lease); err != nil {
		t.Fatal(err)
	}
	recovered, err := service.Apply(ctx, request)
	if err != nil || !recovered.Recovered || recovered.Application.Status != domain.SpecialistDelegationApplied {
		t.Fatalf("instruction application did not recover: %#v err=%v", recovered, err)
	}
	messages, err = st.ListAgentMessages(ctx, pending.Assignments[0].AgentID, true, 10)
	if err != nil || len(messages) != 1 {
		t.Fatalf("recovery duplicated instruction: %#v err=%v", messages, err)
	}
}

func TestSpecialistDelegationApplicationReservesRootAndAbortsOnTerminalRun(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "delegation-application-abort.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	run, proposal, review := createApprovedDelegationApplicationFixture(t, ctx, st, 1)
	failing := &failOnceAdmissionStore{SQLiteStore: st, fail: true}
	service, err := application.NewDefaultSpecialistDelegationApplicationService(
		failing, policy.NewDefaultChecker())
	if err != nil {
		t.Fatal(err)
	}
	request := application.ApplySpecialistDelegationRequest{
		ProposalID: proposal.ID, OperationKey: "delegation-application-reservation",
		RequestedBy: review.ReviewedBy,
	}
	if _, err := service.Apply(ctx, request); err == nil {
		t.Fatal("injected admission failure did not leave applying state")
	}
	lease, err := st.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{
		RunID: run.ID, OwnerID: "blocked-supervisor", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginSupervisorTurn(ctx, lease.Lease, "must wait for application"); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("root turn bypassed application reservation: code=%s err=%v", apperror.CodeOf(err), err)
	}
	if _, _, err := st.ReleaseRunExecutionLease(ctx, lease.Lease); err != nil {
		t.Fatal(err)
	}
	unrelated, err := coordinator.NewWithSpecialistAdmission(st,
		coordinator.SpecialistAdmissionPolicy{
			MaxChildren: 2, MaxTurnsPerChild: 8, MaxTokensPerChild: 1024,
		})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unrelated.AdmitSpecialist(ctx, coordinator.AdmitSpecialistRequest{
		RunID: run.ID, ParentAgentID: proposal.RootAgentID, Title: "unrelated child",
		Skills: []string{"model.chat"}, TurnLimit: 1, TokenLimit: 32,
		IdempotencyKey: "unrelated-admission-during-application",
	}); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("unrelated admission bypassed reservation: code=%s err=%v", apperror.CodeOf(err), err)
	}
	if _, err := application.NewRunService(st).Cancel(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	aborted, found, err := st.GetSpecialistDelegationApplicationByProposal(ctx, proposal.ID)
	if err != nil || !found || aborted.Status != domain.SpecialistDelegationAborted ||
		aborted.StopCode != "run_cancelled" {
		t.Fatalf("terminal Run did not abort application: %#v err=%v", aborted, err)
	}
	if _, err := service.Apply(ctx, request); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("aborted application resumed: code=%s err=%v", apperror.CodeOf(err), err)
	}
	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil || countRunEventType(timeline, events.AgentDelegationApplicationAbortedEvent) != 1 {
		t.Fatalf("application abort event is missing: %#v err=%v", timeline, err)
	}
}

func TestSpecialistDelegationApplicationPolicyDenialCreatesNoState(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "delegation-application-policy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	_, proposal, review := createApprovedDelegationApplicationFixture(t, ctx, st, 1)
	service, err := application.NewDefaultSpecialistDelegationApplicationService(st,
		denyingDelegationApplicationChecker{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Apply(ctx, application.ApplySpecialistDelegationRequest{
		ProposalID: proposal.ID, OperationKey: "delegation-application-policy-denial",
		RequestedBy: review.ReviewedBy,
	})
	if apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("Policy denial was not enforced: code=%s err=%v", apperror.CodeOf(err), err)
	}
	if _, found, err := st.GetSpecialistDelegationApplicationByProposal(ctx, proposal.ID); err != nil || found {
		t.Fatalf("Policy denial persisted application state: found=%t err=%v", found, err)
	}
	nodes, err := st.ListAgentNodes(ctx, proposal.RunID)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("Policy denial created a child: %#v err=%v", nodes, err)
	}
	timeline, err := st.ListRunEvents(ctx, proposal.RunID)
	if err != nil {
		t.Fatal(err)
	}
	denials := 0
	for _, event := range timeline {
		if event.Type == events.PolicyDecisionEvent &&
			strings.Contains(event.PayloadJSON, "specialist_delegation_application") &&
			strings.Contains(event.PayloadJSON, `"allowed":false`) {
			denials++
		}
	}
	if denials != 1 {
		t.Fatalf("Policy denial audit count is %d", denials)
	}
}

func TestSchemaV31ReviewSurvivesApplicationMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegation-application-upgrade.db")
	st := openHistoricalTestDatabase(t, filepath.Join(t.TempDir(), "seed.db"), 177)

	ctx := context.Background()
	_, proposal, review := createApprovedDelegationApplicationFixture(t, ctx, st, 1)
	st = historicalTestDatabaseFromSeed(t, st, path, 31)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if version, err := st.SchemaVersion(ctx); err != nil || version != LatestSchemaVersion {
		t.Fatalf("schema v31 did not upgrade: version=%d err=%v", version, err)
	}
	loadedReview, found, err := st.GetSpecialistDelegationReviewByProposal(ctx, proposal.ID)
	if err != nil || !found || loadedReview.ID != review.ID {
		t.Fatalf("review was not preserved: %#v found=%t err=%v", loadedReview, found, err)
	}
	if _, found, err := st.GetSpecialistDelegationApplicationByProposal(ctx, proposal.ID); err != nil || found {
		t.Fatalf("migration synthesized an application: found=%t err=%v", found, err)
	}
}

type failOnceDelegationApplicationStore struct {
	*SQLiteStore
	mu                  sync.Mutex
	failAdmittedMark    bool
	failInstructionMark bool
}

func (s *failOnceDelegationApplicationStore) MarkSpecialistDelegationAssignmentAdmitted(
	ctx context.Context, applicationID string, ordinal int, agentID string,
) (domain.SpecialistDelegationApplicationAssignment, bool, error) {
	s.mu.Lock()
	if s.failAdmittedMark {
		s.failAdmittedMark = false
		s.mu.Unlock()
		return domain.SpecialistDelegationApplicationAssignment{}, false,
			errors.New("injected admitted-mark failure")
	}
	s.mu.Unlock()
	return s.SQLiteStore.MarkSpecialistDelegationAssignmentAdmitted(ctx, applicationID,
		ordinal, agentID)
}

func (s *failOnceDelegationApplicationStore) MarkSpecialistDelegationAssignmentInstructed(
	ctx context.Context, applicationID string, ordinal int, agentID string, messageID string,
) (domain.SpecialistDelegationApplicationAssignment, bool, error) {
	s.mu.Lock()
	if s.failInstructionMark {
		s.failInstructionMark = false
		s.mu.Unlock()
		return domain.SpecialistDelegationApplicationAssignment{}, false,
			errors.New("injected instruction-mark failure")
	}
	s.mu.Unlock()
	return s.SQLiteStore.MarkSpecialistDelegationAssignmentInstructed(ctx, applicationID,
		ordinal, agentID, messageID)
}

type failOnceAdmissionStore struct {
	*SQLiteStore
	mu   sync.Mutex
	fail bool
}

func (s *failOnceAdmissionStore) AdmitSpecialist(ctx context.Context,
	admission domain.SpecialistAdmission, operationKey string,
) (domain.AgentNode, bool, error) {
	s.mu.Lock()
	if s.fail {
		s.fail = false
		s.mu.Unlock()
		return domain.AgentNode{}, false, errors.New("injected admission failure")
	}
	s.mu.Unlock()
	return s.SQLiteStore.AdmitSpecialist(ctx, admission, operationKey)
}

type denyingDelegationApplicationChecker struct{}

func (denyingDelegationApplicationChecker) CheckText(string, string) policy.Decision {
	return policy.Decision{Allowed: false, Risk: "high", Reason: "denied by application test Policy"}
}

func (denyingDelegationApplicationChecker) CheckToolCall(tools.Call) policy.Decision {
	return policy.Decision{Allowed: false, Risk: "high", Reason: "denied by application test Policy"}
}

func createApprovedDelegationApplicationFixture(t *testing.T, ctx context.Context,
	st *SQLiteStore, assignmentCount int, scoped ...bool,
) (domain.Run, domain.SpecialistDelegationProposal, domain.SpecialistDelegationReview) {
	t.Helper()
	run, turn, lease := beginDelegationTestTurn(t, ctx, st, "apply approved delegation")
	assignments := make([]domain.SpecialistDelegationAssignment, assignmentCount)
	for index := range assignments {
		assignments[index] = domain.SpecialistDelegationAssignment{
			Title:  "Bounded Specialist " + string(rune('A'+index)),
			Goal:   "Inspect parser boundary " + string(rune('A'+index)),
			Skills: []string{"model.chat"}, TurnLimit: 2, TokenLimit: 512,
		}
		if len(scoped) > 0 && scoped[0] {
			assignments[index].AllowedReadPaths = []string{"auth/" + string(rune('A'+index))}
			assignments[index].ToolCallLimit = int64(index + 1)
			assignments[index].CostLimitMicros = int64((index + 1) * 1000)
		}
	}
	outcome, err := newDelegationTestGateway(st).Invoke(ctx, toolgateway.ToolCall{
		Name: toolgateway.SpecialistDelegationProposeTool,
		Payload: delegationTestPayload(t, domain.SpecialistDelegationSpec{
			Version: domain.SpecialistDelegationVersion, Assignments: assignments,
		}),
		OperationKey: "delegation-proposal-for-application", RunID: run.ID,
		AgentID: turn.Agent.ID, SessionID: run.SessionID, WorkspaceID: "ws-delegation",
		RequestedBy: "run_supervisor", LeaseID: lease.LeaseID,
		LeaseGeneration: lease.Generation,
	})
	if err != nil || outcome.Result == nil {
		t.Fatalf("create application proposal: outcome=%#v err=%v", outcome, err)
	}
	proposal, err := st.GetSpecialistDelegationProposal(ctx,
		outcome.Result.Metadata["proposal_id"])
	if err != nil {
		t.Fatal(err)
	}
	reviewed, err := application.NewSpecialistDelegationReviewService(st).Review(ctx,
		application.ReviewSpecialistDelegationRequest{
			ProposalID: proposal.ID, OperationKey: "delegation-review-for-application",
			Decision: domain.SpecialistDelegationApproved, ReviewedBy: "cli_operator",
		})
	if err != nil {
		t.Fatal(err)
	}
	modelAttempt := llm.ModelAttempt{
		Number: 1, TransportAttempt: 1, MaxAttempts: 1,
		Provider: "test", Model: "test-model",
	}
	if inserted, err := st.RecordSupervisorModelStarted(ctx, turn.Checkpoint,
		modelAttempt); err != nil || !inserted {
		t.Fatalf("record model start: inserted=%t err=%v", inserted, err)
	}
	modelAttempt.Outcome = llm.OutcomeSuccess
	response := llm.ChatResponse{
		Text: "delegation proposal recorded", Provider: "test", Model: "test-model",
		Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	}
	checkpoint, err := st.RecordSupervisorModelCompleted(ctx, turn.Checkpoint,
		modelAttempt, response)
	if err != nil {
		t.Fatal(err)
	}
	updatedRun, _, _, err := st.CompleteSupervisorTurn(ctx, checkpoint, response,
		domain.RootAction{
			Version: domain.RootLifecycleVersion, Kind: domain.RootActionContinue,
			Message: "delegation proposal recorded",
		}, policy.Decision{Allowed: true, Reason: "allowed by test Policy"}, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ReleaseRunExecutionLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	return updatedRun, proposal, reviewed.Review
}
