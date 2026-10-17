package store

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/toolgateway"
)

func TestSpecialistDelegationReviewConvergesWithoutAuthorizingAdmission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegation-review-concurrency.db")
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
	run, proposal := createDelegationReviewProposal(t, ctx, st, "concurrent review")
	request := application.ReviewSpecialistDelegationRequest{
		ProposalID: proposal.ID, OperationKey: "delegation-review-concurrent-0001",
		Decision: domain.SpecialistDelegationApproved, Reason: "bounded and in scope",
		ReviewedBy: "cli_operator",
	}
	services := []*application.SpecialistDelegationReviewService{
		application.NewSpecialistDelegationReviewService(st),
		application.NewSpecialistDelegationReviewService(other),
	}
	const workers = 8
	type result struct {
		review application.ReviewSpecialistDelegationResult
		err    error
	}
	results := make(chan result, workers)
	var wg sync.WaitGroup
	for index := range workers {
		wg.Add(1)
		go func(service *application.SpecialistDelegationReviewService) {
			defer wg.Done()
			review, err := service.Review(ctx, request)
			results <- result{review: review, err: err}
		}(services[index%len(services)])
	}
	wg.Wait()
	close(results)
	ids := map[string]bool{}
	created := 0
	for current := range results {
		if current.err != nil {
			t.Fatal(current.err)
		}
		ids[current.review.Review.ID] = true
		if !current.review.Replayed {
			created++
		}
	}
	if len(ids) != 1 || created != 1 {
		t.Fatalf("review did not converge: ids=%#v created=%d", ids, created)
	}
	stored, found, err := st.GetSpecialistDelegationReviewByProposal(ctx, proposal.ID)
	if err != nil || !found || stored.Decision != domain.SpecialistDelegationApproved ||
		stored.Reason != "bounded and in scope" {
		t.Fatalf("unexpected durable review: %#v found=%t err=%v", stored, found, err)
	}
	nodes, err := st.ListAgentNodes(ctx, run.ID)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("review created or removed an Agent: %#v err=%v", nodes, err)
	}
	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil || countRunEventType(timeline, events.AgentDelegationReviewedEvent) != 1 {
		t.Fatalf("review event is inconsistent: %#v err=%v", timeline, err)
	}
	for _, event := range timeline {
		if event.Type != events.AgentDelegationReviewedEvent {
			continue
		}
		if strings.Contains(event.PayloadJSON, stored.Reason) ||
			strings.Contains(event.PayloadJSON, request.OperationKey) ||
			!strings.Contains(event.PayloadJSON, `"admission_authorized":false`) ||
			!strings.Contains(event.PayloadJSON, `"application_required":true`) {
			t.Fatalf("review event leaked content or claimed authority: %s", event.PayloadJSON)
		}
	}
	var rawKeys int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM specialist_delegation_review_operations
		WHERE operation_key_digest = ? OR request_fingerprint = ?`, request.OperationKey,
		request.OperationKey).Scan(&rawKeys); err != nil {
		t.Fatal(err)
	}
	if rawKeys != 0 {
		t.Fatal("raw Specialist delegation review operation key was persisted")
	}
	var unsafeColumns int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM pragma_table_info('specialist_delegation_review_operations')
		WHERE name IN ('reason', 'lease_id', 'lease_generation')`).Scan(&unsafeColumns); err != nil {
		t.Fatal(err)
	}
	if unsafeColumns != 0 {
		t.Fatal("review operation ledger contains content or lease columns")
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE specialist_delegation_reviews
		SET decision = 'rejected' WHERE id = ?`, stored.ID); err == nil {
		t.Fatal("direct review mutation succeeded")
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM specialist_delegation_reviews
		WHERE id = ?`, stored.ID); err == nil {
		t.Fatal("direct review deletion succeeded")
	}
	changed := request
	changed.Reason = "different intent"
	if _, err := services[0].Review(ctx, changed); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("changed-intent replay was not rejected: code=%s err=%v", apperror.CodeOf(err), err)
	}
	secondKey := request
	secondKey.OperationKey = "delegation-review-second-key-0002"
	if _, err := services[0].Review(ctx, secondKey); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("second decision was not rejected: code=%s err=%v", apperror.CodeOf(err), err)
	}
}

func TestSpecialistDelegationReviewRedactsReasonAndRejectsTerminalApproval(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "delegation-review-boundary.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	run, proposal := createDelegationReviewProposal(t, ctx, st, "terminal review")
	service := application.NewSpecialistDelegationReviewService(st)
	if _, err := service.Review(ctx, application.ReviewSpecialistDelegationRequest{
		ProposalID: proposal.ID, OperationKey: "delegation-review-empty-reason-01",
		Decision: domain.SpecialistDelegationRejected, ReviewedBy: "cli_operator",
	}); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("empty rejection reason was accepted: code=%s err=%v", apperror.CodeOf(err), err)
	}
	if _, err := application.NewRunService(st).Cancel(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Review(ctx, application.ReviewSpecialistDelegationRequest{
		ProposalID: proposal.ID, OperationKey: "delegation-review-terminal-approve-01",
		Decision: domain.SpecialistDelegationApproved, ReviewedBy: "cli_operator",
	}); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("terminal Run approval was accepted: code=%s err=%v", apperror.CodeOf(err), err)
	}
	secret := "Authorization: Bearer " + strings.Repeat("z", 32)
	result, err := service.Review(ctx, application.ReviewSpecialistDelegationRequest{
		ProposalID: proposal.ID, OperationKey: "delegation-review-terminal-reject-01",
		Decision: domain.SpecialistDelegationRejected,
		Reason:   "cancelled Run; " + secret, ReviewedBy: "cli_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Review.Reason, strings.Repeat("z", 16)) ||
		!strings.Contains(result.Review.Reason, "[REDACTED:bearer-token]") {
		t.Fatalf("review reason was not redacted: %q", result.Review.Reason)
	}
	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range timeline {
		if strings.Contains(event.PayloadJSON, strings.Repeat("z", 16)) ||
			strings.Contains(event.PayloadJSON, result.Review.Reason) {
			t.Fatalf("review reason entered event stream: %s", event.PayloadJSON)
		}
	}
	secondDecision := application.ReviewSpecialistDelegationRequest{
		ProposalID: proposal.ID, OperationKey: "delegation-review-terminal-second-01",
		Decision: domain.SpecialistDelegationApproved, ReviewedBy: "cli_operator",
	}
	if _, err := service.Review(ctx, secondDecision); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("terminal second decision did not remain a conflict: code=%s err=%v",
			apperror.CodeOf(err), err)
	}
}

func TestSpecialistDelegationReviewEventRequiresExplicitNonAuthorization(t *testing.T) {
	review := domain.SpecialistDelegationReview{
		ID: "delegation-review-required-field", ProposalID: "delegation-required-field",
		RunID: "run-required-field", RootAgentID: "agent-required-field",
		Decision: domain.SpecialistDelegationApproved, ReviewedBy: "cli_operator",
		Version: 1, CreatedAt: time.Now().UTC(),
	}
	event, err := events.New(review.RunID, "mission-required-field",
		events.AgentDelegationReviewedEvent, "operator", review.ID, map[string]any{
			"review_id": review.ID, "proposal_id": review.ProposalID,
			"root_agent_id": review.RootAgentID, "decision": review.Decision,
			"reviewed_by": review.ReviewedBy, "review_version": review.Version,
			"application_required": true,
		})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateSpecialistDelegationReviewEvent(event, review); err == nil {
		t.Fatal("review event without explicit admission_authorized=false was accepted")
	}
}

func TestSchemaV30ProposalSurvivesReviewMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegation-review-upgrade.db")
	st := openCurrentTestDatabase(t, filepath.Join(t.TempDir(), "seed.db"))

	ctx := context.Background()
	_, proposal := createDelegationReviewProposal(t, ctx, st, "v30 review migration")
	st = historicalTestDatabaseFromSeed(t, st, path, 30)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if version, err := st.SchemaVersion(ctx); err != nil || version != LatestSchemaVersion {
		t.Fatalf("schema v30 did not upgrade: version=%d err=%v", version, err)
	}
	loaded, err := st.GetSpecialistDelegationProposal(ctx, proposal.ID)
	if err != nil || loaded.ID != proposal.ID || len(loaded.Spec.Assignments) != 1 {
		t.Fatalf("proposal was not preserved: %#v err=%v", loaded, err)
	}
	if _, found, err := st.GetSpecialistDelegationReviewByProposal(ctx, proposal.ID); err != nil || found {
		t.Fatalf("migration synthesized a review: found=%t err=%v", found, err)
	}
}

func createDelegationReviewProposal(t *testing.T, ctx context.Context,
	st *SQLiteStore, goal string,
) (domain.Run, domain.SpecialistDelegationProposal) {
	t.Helper()
	run, turn, lease := beginDelegationTestTurn(t, ctx, st, goal)
	outcome, err := newDelegationTestGateway(st).Invoke(ctx, toolgateway.ToolCall{
		Name: toolgateway.SpecialistDelegationProposeTool,
		Payload: delegationTestPayload(t, domain.SpecialistDelegationSpec{
			Version: domain.SpecialistDelegationVersion,
			Assignments: []domain.SpecialistDelegationAssignment{{
				Title: "Review bounded parser", Goal: "Inspect parser boundaries",
				Skills: []string{"model.chat"}, TurnLimit: 1, TokenLimit: 64,
			}},
		}),
		OperationKey: "delegation-proposal-for-review", RunID: run.ID,
		AgentID: turn.Agent.ID, SessionID: run.SessionID, WorkspaceID: "ws-delegation",
		RequestedBy: "run_supervisor", LeaseID: lease.LeaseID,
		LeaseGeneration: lease.Generation,
	})
	if err != nil || outcome.Result == nil {
		t.Fatalf("create review proposal: outcome=%#v err=%v", outcome, err)
	}
	proposal, err := st.GetSpecialistDelegationProposal(ctx,
		outcome.Result.Metadata["proposal_id"])
	if err != nil {
		t.Fatal(err)
	}
	return run, proposal
}
