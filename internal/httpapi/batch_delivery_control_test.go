package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

type batchDeliveryControllerStub struct {
	snapshot application.BatchDeliverySnapshot
	review   application.ReviewBatchDeliveryRequest
}

func (s *batchDeliveryControllerStub) Prepare(context.Context,
	application.PrepareBatchDeliveryRequest,
) (application.PrepareBatchDeliveryResult, error) {
	return application.PrepareBatchDeliveryResult{}, nil
}

func (s *batchDeliveryControllerStub) List(context.Context, string, int) (
	[]domain.BatchDeliveryPlan, error,
) {
	return []domain.BatchDeliveryPlan{s.snapshot.Plan}, nil
}

func (s *batchDeliveryControllerStub) Snapshot(context.Context, string) (
	application.BatchDeliverySnapshot, error,
) {
	return s.snapshot, nil
}

func (s *batchDeliveryControllerStub) Review(_ context.Context,
	request application.ReviewBatchDeliveryRequest,
) (domain.BatchDeliveryReview, bool, error) {
	s.review = request
	return domain.BatchDeliveryReview{ID: "batch-review-http-0001", PlanID: request.PlanID,
		Ordinal: request.Ordinal, Generation: request.Generation,
		ProtocolVersion: domain.BatchDeliveryReviewVersion,
		ReceiptID:       "batch-receipt-http-0001", Reviewer: request.Reviewer,
		Verdict: request.Verdict, Summary: request.Summary,
		BaseCommit: strings.Repeat("a", 40), HeadCommit: strings.Repeat("b", 40),
		DiffSHA256: strings.Repeat("c", 64), CallChainSHA256: strings.Repeat("d", 64),
		FullDiffReviewed:  request.FullDiffReviewed,
		CallChainReviewed: request.CallChainReviewed, TestsReviewed: request.TestsReviewed,
		CreatedAt: time.Now().UTC()}, false, nil
}

func (s *batchDeliveryControllerStub) RenewOwner(context.Context,
	application.RenewBatchDeliveryOwnerRequest,
) (domain.BatchDeliveryWorkspace, application.BatchDeliveryAuthority, error) {
	return domain.BatchDeliveryWorkspace{}, application.BatchDeliveryAuthority{}, nil
}

func (s *batchDeliveryControllerStub) Merge(context.Context,
	application.MergeBatchDeliveryRequest,
) (application.MergeBatchDeliveryResult, error) {
	return application.MergeBatchDeliveryResult{}, nil
}

func (s *batchDeliveryControllerStub) Cancel(context.Context,
	application.CancelBatchDeliveryRequest,
) (application.CancelBatchDeliveryResult, error) {
	return application.CancelBatchDeliveryResult{}, nil
}

func (s *batchDeliveryControllerStub) Reconcile(context.Context, string) (
	application.BatchDeliveryReconcileResult, error,
) {
	return application.BatchDeliveryReconcileResult{}, nil
}

func TestBatchDeliveryHTTPProjectionOmitsPrivateGitAndOwnerState(t *testing.T) {
	fixture := newAPIFixture(t)
	now := time.Now().UTC()
	spec, err := domain.NormalizeBatchDeliverySpec(domain.BatchDeliverySpec{
		Version: domain.BatchDeliveryProtocolVersion,
		Tasks: []domain.BatchDeliveryTaskSpec{{Ordinal: 1,
			OwnershipHints: []domain.BatchDeliveryOwnershipHint{{
				Path: "internal/owned", Kind: domain.BatchDeliveryOwnershipDirectory}},
			Budget: domain.BatchDeliveryBudget{TurnLimit: 2, TokenLimit: 128,
				TimeoutMillis: 60_000},
			Validations: []domain.BatchDeliveryValidationRequirement{{
				ID: "diff", Kind: domain.BatchValidationGitDiffCheck, Scope: "."}},
			ExpectedArtifacts: []domain.ChildTaskExpectedArtifact{}}},
		Contract: domain.BatchDeliveryContract{RequireClean: true,
			RequireIndependentReview: true, RequireAllValidations: true,
			MaxChangedFiles: 16, MaxDiffBytes: 1024 * 1024}})
	if err != nil {
		t.Fatal(err)
	}
	plan := domain.BatchDeliveryPlan{ID: "batch-http-projection-0001", RunID: fixture.run.ID,
		ProposalID: "child-proposal-http-0001", RootAgentID: "agent-root-http-0001",
		WorkspaceID: fixture.workspace.ID, Status: domain.BatchDeliveryReviewing,
		Spec: spec, BaseCommit: strings.Repeat("a", 40), SourceBranch: "main",
		OperationDigest: strings.Repeat("e", 64), RequestFingerprint: strings.Repeat("f", 64),
		CreatedBy: "http-test", CreatedAt: now, UpdatedAt: now}
	workspace := domain.BatchDeliveryWorkspace{PlanID: plan.ID, Ordinal: 1,
		AgentID: "agent-child-http-0001", Generation: 1,
		Status: domain.BatchWorkspaceReadyForReview, Branch: "codex/http-child",
		WorktreeRoot: `C:\private\batch-worktree`, BaseCommit: plan.BaseCommit,
		HeadCommit: strings.Repeat("b", 40), OwnerTokenDigest: "owner-token-digest-private",
		ToolProfile:            domain.DefaultBatchDeliveryToolProfile(),
		ToolProfileFingerprint: "tool-profile-private", LeaseExpiresAt: now.Add(time.Hour),
		LastHeartbeatAt: now, CreatedAt: now, UpdatedAt: now}
	controller := &batchDeliveryControllerStub{snapshot: application.BatchDeliverySnapshot{
		Plan: plan, Workspaces: []domain.BatchDeliveryWorkspace{workspace},
		Mailbox: map[int][]domain.BatchDeliveryMailboxMessage{1: {{
			ID: "batch-message-http-dispatch", PlanID: plan.ID, Ordinal: 1, Generation: 1,
			Sequence: 1, Kind: domain.BatchMailboxDispatch,
			Actor: plan.RootAgentID, Summary: "child worktree and narrowed tool profile dispatched",
			EvidenceRefs: []string{}, OperationDigest: strings.Repeat("3", 64),
			RequestFingerprint: strings.Repeat("4", 64), CreatedAt: now}, {
			ID: "batch-message-http-0001", PlanID: plan.ID, Ordinal: 1, Generation: 1,
			Sequence: 2, Kind: domain.BatchMailboxReadyForReview,
			Actor: "agent-child-http-0001", Summary: "ready for review",
			EvidenceRefs: []string{"test://http"}, OperationDigest: strings.Repeat("1", 64),
			RequestFingerprint: strings.Repeat("2", 64), CreatedAt: now}}}}}
	fixture.api.batchDeliveryController = controller
	fixture.api.batchDeliveryControlEnabled = true

	list := fixture.get(t, "/api/v1/runs/"+fixture.run.ID+"/batch-deliveries")
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	detail := fixture.get(t, "/api/v1/runs/"+fixture.run.ID+
		"/batch-deliveries/"+plan.ID)
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	t.Run("mailbox_empty_evidence_serializes_as_array", func(t *testing.T) {
		var payload struct {
			Data BatchDeliverySnapshotView `json:"data"`
		}
		if err := json.Unmarshal(detail.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Data.Children) != 1 || len(payload.Data.Children[0].Mailbox) != 2 {
			t.Fatalf("missing mailbox messages: %s", detail.Body.String())
		}
		messages := payload.Data.Children[0].Mailbox
		if messages[0].Kind != "dispatch" || messages[0].EvidenceRefs == nil || len(messages[0].EvidenceRefs) != 0 {
			t.Fatalf("normal dispatch evidence_refs must be []: %s", detail.Body.String())
		}
		if len(messages[1].EvidenceRefs) != 1 || messages[1].EvidenceRefs[0] != "test://http" {
			t.Fatalf("populated evidence refs changed: %#v", messages[1].EvidenceRefs)
		}
	})
	raw := strings.ToLower(detail.Body.String())
	for _, forbidden := range []string{"private\\batch-worktree", "owner-token-digest-private",
		"operation_digest", "request_fingerprint", "tool_profile_fingerprint",
		"worktree_root"} {
		if strings.Contains(raw, strings.ToLower(forbidden)) {
			t.Fatalf("detail exposed %q: %s", forbidden, raw)
		}
	}

	body := `{"version":"batch_delivery_review_control.v1","generation":1,` +
		`"reviewer":"http-independent-reviewer","verdict":"accepted",` +
		`"summary":"full diff and tests independently reviewed",` +
		`"full_diff_reviewed":true,"call_chain_reviewed":true,"tests_reviewed":true}`
	review := performControlMethodPathRequest(t, fixture.api, http.MethodPost,
		"/api/v1/runs/"+fixture.run.ID+"/batch-deliveries/"+plan.ID+
			"/children/1/review", "batch-http-review-operation-0001", strings.NewReader(body))
	if review.Code != http.StatusOK || controller.review.PlanID != plan.ID ||
		controller.review.OperationKey != "batch-http-review-operation-0001" {
		t.Fatalf("review status=%d request=%#v body=%s",
			review.Code, controller.review, review.Body.String())
	}
}
