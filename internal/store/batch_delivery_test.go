package store

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
)

func TestSchemaV118UpgradesV117Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "batch-delivery-v117.db")
	state := openHistoricalTestDatabase(t, path, 117)

	// The immutable historical prefix above is the upgrade input.
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if version, err := upgraded.SchemaVersion(t.Context()); err != nil || version != LatestSchemaVersion {
		t.Fatalf("schema version=%d want=%d err=%v", version, LatestSchemaVersion, err)
	}
	for _, table := range []string{"batch_delivery_plans", "batch_delivery_workspaces",
		"batch_delivery_mailbox", "batch_delivery_receipts", "batch_delivery_reviews",
		"batch_delivery_merge_queues", "batch_delivery_merge_steps"} {
		var count int
		if err := upgraded.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM sqlite_master
			WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("table %s count=%d err=%v", table, count, err)
		}
	}
}

func TestBatchDeliveryPlanMailboxReceiptReviewAndGenerationFence(t *testing.T) {
	state, plan, workspaces := newBatchDeliveryStoreFixture(t)
	ctx := context.Background()
	stored, children, replayed, err := state.CreateBatchDeliveryPlan(ctx, plan, workspaces)
	if err != nil || replayed || stored.ID != plan.ID || len(children) != 2 {
		t.Fatalf("create plan=%+v children=%d replayed=%t err=%v", stored, len(children), replayed, err)
	}
	if _, _, replayed, err := state.CreateBatchDeliveryPlan(ctx, plan, workspaces); err != nil || !replayed {
		t.Fatalf("plan replay=%t err=%v", replayed, err)
	}
	now := plan.CreatedAt.Add(time.Second)
	dispatch := batchMailboxFixture(plan.ID, 1, 1, domain.BatchMailboxDispatch,
		"root", "dispatch task", "dispatch-operation-0001", now)
	workspace, _, replayed, err := state.ActivateBatchDeliveryWorkspace(ctx, dispatch,
		plan.BaseCommit)
	if err != nil || replayed || workspace.Status != domain.BatchWorkspaceDispatched {
		t.Fatalf("dispatch workspace=%+v replayed=%t err=%v", workspace, replayed, err)
	}
	ack := batchMailboxFixture(plan.ID, 1, 1, domain.BatchMailboxAck,
		workspaces[0].AgentID, "ack task", "ack-operation-00000001", now.Add(time.Second))
	workspace, _, _, err = state.AppendBatchDeliveryMailbox(ctx, ack,
		workspaces[0].OwnerTokenDigest, now.Add(time.Hour))
	if err != nil || workspace.Status != domain.BatchWorkspaceAcknowledged {
		t.Fatalf("ack workspace=%+v err=%v", workspace, err)
	}
	stale := ack
	stale.ID = idgen.New("batchmsg")
	stale.OperationDigest = runmutation.OperationKeyDigest("batch-mailbox", plan.ID, "stale-operation-0001")
	stale.RequestFingerprint = runmutation.Fingerprint("batch-mailbox.v1", "stale")
	stale.Generation = 2
	if _, _, _, err := state.AppendBatchDeliveryMailbox(ctx, stale,
		workspaces[0].OwnerTokenDigest, now.Add(time.Hour)); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale generation error=%v", err)
	}
	receiptAt := now.Add(2 * time.Second)
	receipt := batchReceiptFixture(plan, workspaces[0], receiptAt)
	ready := batchMailboxFixture(plan.ID, 1, 1, domain.BatchMailboxReadyForReview,
		workspaces[0].AgentID, "ready", "ready-operation-00001", receiptAt)
	storedReceipt, replayed, err := state.RecordBatchDeliveryReceipt(ctx, receipt,
		workspaces[0].OwnerTokenDigest, ready)
	if err != nil || replayed || storedReceipt.ID != receipt.ID {
		t.Fatalf("receipt=%+v replayed=%t err=%v", storedReceipt, replayed, err)
	}
	reviewAt := receiptAt.Add(time.Second)
	review := domain.BatchDeliveryReview{ID: idgen.New("batchreview"), PlanID: plan.ID,
		Ordinal: 1, Generation: 1, ProtocolVersion: domain.BatchDeliveryReviewVersion,
		ReceiptID: receipt.ID, Reviewer: "independent-reviewer", Verdict: domain.BatchReviewChangesRequested,
		Summary: "add a regression assertion", BaseCommit: receipt.BaseCommit,
		HeadCommit: receipt.HeadCommit, DiffSHA256: receipt.DiffSHA256,
		CallChainSHA256: receipt.CallChainSHA256, FullDiffReviewed: true,
		CallChainReviewed: true, TestsReviewed: true,
		OperationDigest:    runmutation.OperationKeyDigest("batch-review", plan.ID, "review-operation-0001"),
		RequestFingerprint: runmutation.Fingerprint("batch-review.v1", receipt.ID, "changes_requested"),
		CreatedAt:          reviewAt}
	reviewMessage := batchMailboxFixture(plan.ID, 1, 1, domain.BatchMailboxChangesRequested,
		review.Reviewer, review.Summary, "review-message-000001", reviewAt)
	if _, replayed, err := state.RecordBatchDeliveryReview(ctx, review, reviewMessage); err != nil || replayed {
		t.Fatalf("review replayed=%t err=%v", replayed, err)
	}
	rotated := runmutation.Fingerprint("batch-owner-token.v1", "rotated")
	retried, err := state.RetryBatchDeliveryWorkspace(ctx, plan.ID, 1, 1, rotated,
		reviewAt.Add(time.Hour), reviewAt.Add(time.Second))
	if err != nil || retried.Generation != 2 || retried.OwnerTokenDigest != rotated ||
		retried.Status != domain.BatchWorkspaceWorking {
		t.Fatalf("retry=%+v err=%v", retried, err)
	}
	if _, _, _, err := state.AppendBatchDeliveryMailbox(ctx, batchMailboxFixture(plan.ID, 1, 1,
		domain.BatchMailboxProgress, workspaces[0].AgentID, "stale", "stale-token-message-01",
		reviewAt.Add(2*time.Second)), workspaces[0].OwnerTokenDigest,
		reviewAt.Add(time.Hour)); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("old generation survived retry: %v", err)
	}
}

func TestBatchDeliveryMailboxConcurrentDuplicateIsExactlyOnce(t *testing.T) {
	state, plan, workspaces := newBatchDeliveryStoreFixture(t)
	ctx := context.Background()
	if _, _, _, err := state.CreateBatchDeliveryPlan(ctx, plan, workspaces); err != nil {
		t.Fatal(err)
	}
	now := plan.CreatedAt.Add(time.Second)
	dispatch := batchMailboxFixture(plan.ID, 1, 1, domain.BatchMailboxDispatch,
		"root", "dispatch task", "concurrent-dispatch-0001", now)
	if _, _, _, err := state.ActivateBatchDeliveryWorkspace(ctx, dispatch,
		plan.BaseCommit); err != nil {
		t.Fatal(err)
	}
	ack := batchMailboxFixture(plan.ID, 1, 1, domain.BatchMailboxAck,
		workspaces[0].AgentID, "ack task", "concurrent-ack-0000001", now.Add(time.Second))
	if _, _, _, err := state.AppendBatchDeliveryMailbox(ctx, ack,
		workspaces[0].OwnerTokenDigest, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	progress := batchMailboxFixture(plan.ID, 1, 1, domain.BatchMailboxProgress,
		workspaces[0].AgentID, "same durable progress", "concurrent-progress-01",
		now.Add(2*time.Second))
	var first, replayed atomic.Int32
	errorsSeen := make(chan error, 8)
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, _, duplicate, err := state.AppendBatchDeliveryMailbox(ctx, progress,
				workspaces[0].OwnerTokenDigest, now.Add(time.Hour))
			if err != nil {
				errorsSeen <- err
				return
			}
			if duplicate {
				replayed.Add(1)
			} else {
				first.Add(1)
			}
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Fatalf("concurrent append: %v", err)
	}
	if first.Load() != 1 || replayed.Load() != 7 {
		t.Fatalf("first=%d replayed=%d", first.Load(), replayed.Load())
	}
	messages, err := state.ListBatchDeliveryMailbox(ctx, plan.ID, 1, 20)
	if err != nil || len(messages) != 3 || messages[2].Sequence != 3 {
		t.Fatalf("mailbox=%#v err=%v", messages, err)
	}

	conflict := progress
	conflict.ID = idgen.New("batchmsg")
	conflict.Summary = "same key, different content"
	conflict.RequestFingerprint = runmutation.Fingerprint("batch-mailbox.v1", "different")
	if _, _, _, err := state.AppendBatchDeliveryMailbox(ctx, conflict,
		workspaces[0].OwnerTokenDigest, now.Add(time.Hour)); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("conflicting replay error=%v", err)
	}
}

func TestBatchDeliveryReceiptRechecksLeaseAndRunAtCommit(t *testing.T) {
	state, plan, workspaces := newBatchDeliveryStoreFixture(t)
	ctx := t.Context()
	if _, _, _, err := state.CreateBatchDeliveryPlan(ctx, plan, workspaces); err != nil {
		t.Fatal(err)
	}
	dispatchAt := plan.CreatedAt.Add(time.Second)
	dispatch := batchMailboxFixture(plan.ID, 1, 1, domain.BatchMailboxDispatch,
		"root", "dispatch task", "receipt-fence-dispatch-01", dispatchAt)
	if _, _, _, err := state.ActivateBatchDeliveryWorkspace(ctx, dispatch,
		plan.BaseCommit); err != nil {
		t.Fatal(err)
	}
	lease := plan.CreatedAt.Add(5 * time.Second)
	ackAt := plan.CreatedAt.Add(2 * time.Second)
	ack := batchMailboxFixture(plan.ID, 1, 1, domain.BatchMailboxAck,
		workspaces[0].AgentID, "ack task", "receipt-fence-ack-000001", ackAt)
	if _, _, _, err := state.AppendBatchDeliveryMailbox(ctx, ack,
		workspaces[0].OwnerTokenDigest, lease); err != nil {
		t.Fatal(err)
	}

	expiredAt := lease.Add(time.Millisecond)
	expired := batchReceiptFixture(plan, workspaces[0], expiredAt)
	expiredReady := batchMailboxFixture(plan.ID, 1, 1,
		domain.BatchMailboxReadyForReview, workspaces[0].AgentID, "ready",
		"receipt-fence-ready-0001", expiredAt)
	if _, _, err := state.RecordBatchDeliveryReceipt(ctx, expired,
		workspaces[0].OwnerTokenDigest, expiredReady); apperror.CodeOf(err) != apperror.CodeDeadlineExceeded {
		t.Fatalf("expired receipt error=%v", err)
	}

	if _, err := application.NewRunService(state).Pause(ctx, plan.RunID); err != nil {
		t.Fatal(err)
	}
	inactiveAt := plan.CreatedAt.Add(3 * time.Second)
	inactive := batchReceiptFixture(plan, workspaces[0], inactiveAt)
	inactive.ID = idgen.New("batchreceipt")
	inactive.OperationDigest = runmutation.OperationKeyDigest("batch-receipt", plan.ID,
		"inactive-receipt-operation")
	inactive.RequestFingerprint = runmutation.Fingerprint("batch-receipt.v1", plan.ID,
		"inactive")
	inactiveReady := batchMailboxFixture(plan.ID, 1, 1,
		domain.BatchMailboxReadyForReview, workspaces[0].AgentID, "ready",
		"receipt-fence-inactive-ready", inactiveAt)
	if _, _, err := state.RecordBatchDeliveryReceipt(ctx, inactive,
		workspaces[0].OwnerTokenDigest, inactiveReady); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("inactive Run receipt error=%v", err)
	}
}

func newBatchDeliveryStoreFixture(t *testing.T, workers ...*domain.WorkerProfile) (*SQLiteStore, domain.BatchDeliveryPlan,
	[]domain.BatchDeliveryWorkspace,
) {
	t.Helper()
	state, err := Open(filepath.Join(t.TempDir(), "batch-delivery.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	ctx := context.Background()
	workspace := WorkspaceRecord{ID: "batch-workspace", Name: "batch-workspace",
		RootPath: t.TempDir()}
	if err := state.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	_, run, err := application.NewRunService(state).Create(ctx, application.CreateRunRequest{
		Goal: "batch delivery store test", Profile: "code", WorkspaceID: workspace.ID,
		Budget: domain.Budget{MaxTurns: 12, MaxTokens: 100000, MaxToolCalls: 20}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunService(state).Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	root, _, err := state.RegisterRootAgent(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	var worker *domain.WorkerProfile
	if len(workers) > 0 {
		worker = workers[0]
	}
	childSpec, err := domain.NormalizeChildTaskProposalSpec(domain.ChildTaskProposalSpec{
		Version: domain.ChildTaskProposalVersion, Tasks: []domain.ChildTask{
			{Worker: worker, Title: "code one", Goal: "change first directory", Skills: []string{"model.chat"},
				SurfaceHint: domain.ChildTaskSurfaceHintCore,
				TurnLimit:   2, TokenLimit: 256, TimeoutMillis: 60000,
				ExpectedArtifacts: []domain.ChildTaskExpectedArtifact{{PathHint: "internal/one", Kind: "code"}}},
			{Title: "code two", Goal: "change second directory", Skills: []string{"model.chat"},
				SurfaceHint:        domain.ChildTaskSurfaceHintCore,
				DependencyOrdinals: []int{1}, TurnLimit: 2, TokenLimit: 256, TimeoutMillis: 60000,
				ExpectedArtifacts: []domain.ChildTaskExpectedArtifact{{PathHint: "internal/two", Kind: "code"}}},
		}})
	if err != nil {
		t.Fatal(err)
	}
	proposal := domain.ChildTaskProposal{ID: idgen.New("childtask"), RunID: run.ID,
		RootAgentID: root.ID, SessionID: run.SessionID, WorkspaceID: workspace.ID,
		Status: domain.ChildTaskProposalProposed, Spec: childSpec,
		Surface: domain.ChildTaskSurfaceCore, RequestedBy: "run_supervisor", Version: 1,
		CreatedAt: now}
	operationKey := "batch-child-proposal-0001"
	operation := domain.ChildTaskOperation{
		KeyDigest: runmutation.OperationKeyDigest("child_task_propose", run.ID, operationKey),
		RequestFingerprint: runmutation.Fingerprint("child_task_request.v1", run.ID,
			childSpec.SpecJSONFingerprint()), ProposalID: proposal.ID, RunID: run.ID,
		SessionID: run.SessionID, WorkspaceID: workspace.ID, RootAgentID: root.ID,
		LeaseID: idgen.New("lease"), LeaseGeneration: 1, RequestedBy: "run_supervisor",
		CreatedAt: now}
	policyEvent, _ := events.New(run.ID, run.MissionID, events.PolicyDecisionEvent,
		"policy", idgen.New("inv"), map[string]any{"allowed": true})
	proposalEvent, _ := events.New(run.ID, run.MissionID, events.ChildTaskProposedEvent,
		"agent_coordinator", proposal.ID, map[string]any{"status": "proposed"})
	toolEvent, _ := events.New(run.ID, run.MissionID, events.ToolCompletedEvent,
		"agent_proposal_tool", idgen.New("inv"), map[string]any{"tool_name": "child_task_propose"})
	if _, _, err := state.CreateChildTaskProposal(ctx, operation, proposal,
		policyEvent, proposalEvent, toolEvent); err != nil {
		t.Fatal(err)
	}
	if _, _, err := state.ReviewChildTaskProposal(ctx, domain.ChildTaskReview{
		ProposalID: proposal.ID, Action: "approve", Reviewer: "operator",
		FanoutTier: domain.ReadOnlyFanoutTwo}, "batch-child-review-0001"); err != nil {
		t.Fatal(err)
	}
	_, assignments, err := state.AdmitChildTaskProposal(ctx, proposal.ID,
		"batch-child-admit-00001")
	if err != nil {
		t.Fatal(err)
	}
	batchSpec, err := domain.NormalizeBatchDeliverySpec(domain.BatchDeliverySpec{
		Version: domain.BatchDeliveryProtocolVersion,
		Tasks: []domain.BatchDeliveryTaskSpec{
			{Ordinal: 1, OwnershipHints: []domain.BatchDeliveryOwnershipHint{{Path: "internal/one", Kind: domain.BatchDeliveryOwnershipDirectory}},
				Budget:            domain.BatchDeliveryBudget{TurnLimit: 2, TokenLimit: 256, TimeoutMillis: 60000},
				Validations:       []domain.BatchDeliveryValidationRequirement{{ID: "diff-one", Kind: domain.BatchValidationGitDiffCheck, Scope: "."}},
				ExpectedArtifacts: childSpec.Tasks[0].ExpectedArtifacts},
			{Ordinal: 2, OwnershipHints: []domain.BatchDeliveryOwnershipHint{{Path: "internal/two", Kind: domain.BatchDeliveryOwnershipDirectory}},
				DependencyOrdinals: []int{1}, Budget: domain.BatchDeliveryBudget{TurnLimit: 2, TokenLimit: 256, TimeoutMillis: 60000},
				Validations:       []domain.BatchDeliveryValidationRequirement{{ID: "diff-two", Kind: domain.BatchValidationGitDiffCheck, Scope: "."}},
				ExpectedArtifacts: childSpec.Tasks[1].ExpectedArtifacts}},
		Contract: domain.BatchDeliveryContract{RequireClean: true, RequireIndependentReview: true,
			RequireAllValidations: true, MaxChangedFiles: 20, MaxDiffBytes: 1024 * 1024}})
	if err != nil {
		t.Fatal(err)
	}
	base := "0123456789012345678901234567890123456789"
	opDigest := runmutation.OperationKeyDigest("batch-delivery.v1", run.ID, "batch-operation-0001")
	plan := domain.BatchDeliveryPlan{ID: idgen.New("batch"), RunID: run.ID,
		ProposalID: proposal.ID, RootAgentID: root.ID, WorkspaceID: workspace.ID,
		Status: domain.BatchDeliveryPreparing, Spec: batchSpec, BaseCommit: base,
		SourceBranch: "main", OperationDigest: opDigest,
		RequestFingerprint: runmutation.Fingerprint("batch-delivery-request.v1", proposal.ID),
		CreatedBy:          "operator", CreatedAt: now, UpdatedAt: now}
	profile := domain.DefaultBatchDeliveryToolProfile()
	workspaces := make([]domain.BatchDeliveryWorkspace, 2)
	for index := range workspaces {
		workspaces[index] = domain.BatchDeliveryWorkspace{PlanID: plan.ID, Ordinal: index + 1,
			AgentID: assignments[index].AdmittedAgentID, Generation: 1,
			Status:       domain.BatchWorkspacePreparing,
			Branch:       "codex/batch-test/task-" + string(rune('1'+index)),
			WorktreeRoot: filepath.Join(t.TempDir(), "child-"+string(rune('1'+index))),
			BaseCommit:   base, OwnerTokenDigest: runmutation.Fingerprint("batch-owner-token.v1", fmtInt(index)),
			ToolProfile: profile, ToolProfileFingerprint: profile.Fingerprint(),
			LeaseExpiresAt: now.Add(time.Hour), LastHeartbeatAt: now,
			CreatedAt: now, UpdatedAt: now}
	}
	return state, plan, workspaces
}

func batchMailboxFixture(planID string, ordinal int, generation int64,
	kind domain.BatchDeliveryMailboxKind, actor, summary, key string, now time.Time,
) domain.BatchDeliveryMailboxMessage {
	return domain.BatchDeliveryMailboxMessage{ID: idgen.New("batchmsg"), PlanID: planID,
		Ordinal: ordinal, Generation: generation, Kind: kind, Actor: actor, Summary: summary,
		EvidenceRefs: []string{}, OperationDigest: runmutation.OperationKeyDigest("batch-mailbox", planID, key),
		RequestFingerprint: runmutation.Fingerprint("batch-mailbox.v1", planID, fmtInt(ordinal),
			fmtInt64(generation), string(kind), actor, summary), CreatedAt: now}
}

func batchReceiptFixture(plan domain.BatchDeliveryPlan, workspace domain.BatchDeliveryWorkspace,
	now time.Time,
) domain.BatchDeliveryReceipt {
	test := domain.BatchDeliveryTestReceipt{RequirementID: "diff-one",
		Kind: domain.BatchValidationGitDiffCheck, Scope: ".", ExitCode: 0,
		OutputSHA256:   runmutation.Fingerprint("test-output.v1", "ok"),
		DurationMillis: 10, CompletedAt: now}
	return domain.BatchDeliveryReceipt{ID: idgen.New("batchreceipt"), PlanID: plan.ID,
		Ordinal: workspace.Ordinal, Generation: workspace.Generation,
		ProtocolVersion: domain.BatchDeliveryReceiptVersion, BaseCommit: plan.BaseCommit,
		HeadCommit:      "1123456789012345678901234567890123456789",
		DiffSHA256:      runmutation.Fingerprint("diff.v1", "one"),
		CallChainSHA256: runmutation.Fingerprint("call.v1", "one"), DiffBytes: 100,
		DiffStat: "1 file changed, 1 insertion(+)", ChangedFiles: []string{"internal/one/file.go"},
		TestReceipts: []domain.BatchDeliveryTestReceipt{test}, EvidenceRefs: []string{},
		Limitations:        []string{"none known"},
		OperationDigest:    runmutation.OperationKeyDigest("batch-receipt", plan.ID, "receipt-operation-01"),
		RequestFingerprint: runmutation.Fingerprint("batch-receipt.v1", plan.ID, "1"), CreatedAt: now}
}

func fmtInt(value int) string     { return string(rune('0' + value)) }
func fmtInt64(value int64) string { return string(rune('0' + value)) }
