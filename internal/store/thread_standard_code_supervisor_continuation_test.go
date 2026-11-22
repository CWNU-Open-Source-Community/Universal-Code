package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

func TestGetStandardCodeContinuationDirectApplyBackedByToolApprovals(t *testing.T) {
	ctx := context.Background()
	state, run1, mission, _ := newWorkspaceCheckpointStoreFixture(t)
	defer state.Close()

	executor, err := repository.NewRunWorktreeExecutor(filepath.Join(t.TempDir(), "managed"))
	if err != nil {
		t.Fatal(err)
	}
	drydocks, err := application.NewRunWorktreeService(state, executor)
	if err != nil {
		t.Fatal(err)
	}
	caps := domain.ExecutionPermissionRuntimeCapabilities{
		WorkspaceSandboxEnabled: true,
		OperatorApprovalEnabled: true,
		RuntimeAuthority:        domain.NewExecutionPermissionRuntimeAuthority(),
	}
	checkpoints, err := application.NewWorkspaceCheckpointService(state, caps)
	if err != nil {
		t.Fatal(err)
	}
	drydocks.WithCheckpointService(checkpoints)

	runtime := application.CapabilityReadinessRuntime{
		RunControlEnabled:                 true,
		RunExecutionEnabled:               true,
		ExecutionPermissionControlEnabled: true,
		StandardCodePresetEnabled:         true,
		ExecutionPermissionCapabilities:   caps,
		LocalSandboxInstalled:             true,
		LocalSandboxProven:                true,
		LocalBackendReady:                 true,
		CommandRuntimeAdapters: []commandruntimeadapter.Identity{
			commandruntimeadapter.SandboxedWorkspace(
				application.CommandRuntimeLocalSandboxBackend, "standard-code-continuation",
				"standard-code-continuation-test"),
		},
	}
	presets, err := application.NewStandardCodePresetService(state, drydocks, runtime)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := presets.Configure(ctx, application.ConfigureStandardCodeRequest{
		Version: domain.StandardCodePresetProtocolVersion, RunID: run1.ID,
		Action: "configure", BackendIntent: "local",
		OperationKey: "sc-continuation-preset-0001", RequestedBy: "operator",
	})
	if err != nil || !preview.TrustRequired {
		t.Fatalf("preset preview=%+v err=%v", preview, err)
	}
	configured, err := presets.Configure(ctx, application.ConfigureStandardCodeRequest{
		Version: domain.StandardCodePresetProtocolVersion, RunID: run1.ID,
		Action: "configure", BackendIntent: "local",
		OperationKey: "sc-continuation-preset-0001", RequestedBy: "operator",
		ConfirmWorkspaceTrust: true, ExpectedTrustDigest: preview.TrustDigest,
	})
	if err != nil || configured.Status != application.StandardCodeResultConfigured {
		t.Fatalf("configured=%+v err=%v", configured, err)
	}
	presetOp, found, err := state.GetConfiguredStandardCodePresetOperation(ctx, run1.ID)
	if err != nil || !found {
		t.Fatalf("presetOp found=%t err=%v", found, err)
	}
	owned, found, err := state.GetDrydockByRun(ctx, run1.ID)
	if err != nil || !found {
		t.Fatalf("drydock found=%t err=%v", found, err)
	}
	if _, err := application.NewRunService(state).Start(ctx, run1.ID); err != nil {
		t.Fatal(err)
	}
	lease := acquireTestRunExecutionLease(t, ctx, state, run1.ID)
	turn, err := state.BeginSupervisorTurn(ctx, lease, "Apply approved edit")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.RecordSupervisorModelStarted(ctx, turn.Checkpoint, llm.ModelAttempt{
		Number: 1, TransportAttempt: 1, MaxAttempts: 1, Provider: "test", Model: "model",
	}); err != nil {
		t.Fatal(err)
	}

	// Prepare an interactive proposal -> creates file_edits + tool_approvals.
	prepared, err := fileedit.NewManager(state).PrepareProposal(ctx, fileedit.Proposal{
		ID:            idgen.New("edit"),
		SessionID:     run1.SessionID,
		WorkspaceID:   owned.WorkspaceID,
		WorkspaceRoot: owned.Path,
		Path:          "file.txt",
		Operation:     fileedit.OperationReplace,
		ProposedText:  "direct-apply-content\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	edit, replayed, err := state.CreateFileEditIfAbsent(ctx, prepared)
	if err != nil || replayed {
		t.Fatalf("CreateFileEditIfAbsent replayed=%t err=%v", replayed, err)
	}
	if _, err := application.NewFileEditReviewService(state).WithRunWorktree(drydocks).Review(ctx, application.ReviewFileEditRequest{
		Version: application.FileEditReviewProtocolVersion, RunID: run1.ID,
		EditID: edit.ID, Action: application.FileEditApproveIntent,
	}); err != nil {
		t.Fatal(err)
	}

	// Direct Go apply -> seals WorkspaceCheckpoint + Transaction with trigger_receipt_id=edit.ID.
	applyService := application.NewFileEditApplyService(state, policy.NewDefaultChecker(), checkpoints).
		WithRunWorktree(drydocks).
		WithExecutionPermissionCapabilities(caps)
	applied, err := applyService.Apply(ctx, application.ApplyFileEditRequest{
		Version:         fileedit.FileEditApplyProtocolVersion,
		RunID:           run1.ID,
		EditID:          edit.ID,
		AttemptID:       turn.Checkpoint.AttemptID,
		LeaseID:         lease.LeaseID,
		LeaseGeneration: lease.Generation,
		OperationKey:    "direct-apply-op-0001",
		AppliedBy:       "approval_continuation",
	})
	if err != nil || !applied.FileWritten || applied.Edit.Status != fileedit.StatusApplied {
		t.Fatalf("direct apply=%+v err=%v", applied, err)
	}
	txns, err := state.ListWorkspaceCheckpointTransactions(ctx, run1.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	var txn workspacecheckpoint.Transaction
	for _, candidate := range txns {
		if candidate.TriggerReceiptID == edit.ID && candidate.Kind == workspacecheckpoint.TransactionFileTool && candidate.Status == workspacecheckpoint.TransactionCompleted {
			txn = candidate
			break
		}
	}
	if txn.ID == "" || txn.AfterCheckpointID == "" {
		t.Fatalf("missing completed file_tool transaction for %s: %+v", edit.ID, txns)
	}
	afterCP, err := state.GetWorkspaceCheckpoint(ctx, txn.AfterCheckpointID)
	if err != nil {
		t.Fatal(err)
	}

	// Record StandardCodeSupervisorLedgerEntry with ToolAction="direct_apply:"+edit.ID.
	mode, err := state.GetRunMode(ctx, run1.ID)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := state.GetRunExecutionProfile(ctx, run1.ID)
	if err != nil {
		t.Fatal(err)
	}
	interaction, err := state.GetRunExecutionInteraction(ctx, run1.ID)
	if err != nil {
		t.Fatal(err)
	}
	permission, err := state.GetRunExecutionPermission(ctx, run1.ID)
	if err != nil {
		t.Fatal(err)
	}
	browserCDP, err := state.GetRunBrowserCDPPermission(ctx, run1.ID)
	if err != nil {
		t.Fatal(err)
	}
	rootAgent, found, err := state.GetRootAgent(ctx, run1.ID)
	if err != nil || !found {
		t.Fatalf("root agent found=%t err=%v", found, err)
	}
	expectedCapGen := runmutation.Fingerprint("test-cap-gen", afterCP.RootFingerprint)
	evidence := runmutation.Fingerprint("standard_code_mutation_checkpoint.v1",
		edit.ID, txn.ID, txn.BeforeCheckpointID, txn.AfterCheckpointID, afterCP.RootFingerprint, expectedCapGen)
	now := time.Now().UTC()
	snap := domain.StandardCodeSupervisorSnapshot{
		ProtocolVersion:              domain.StandardCodeSupervisorProtocolVersion,
		RunID:                        run1.ID,
		MissionID:                    mission.ID,
		WorkspaceID:                  mission.WorkspaceID,
		RootAgentID:                  rootAgent.ID,
		PresetOperationKeyDigest:     presetOp.KeyDigest,
		ModeSnapshotID:               mode.ID,
		ModeRevision:                 mode.Revision,
		ProfileSnapshotID:            profile.ID,
		ProfileRevision:              profile.Revision,
		InteractionSnapshotID:        interaction.ID,
		InteractionRevision:          interaction.Revision,
		PermissionSnapshotID:         permission.ID,
		PermissionRevision:           permission.Revision,
		BrowserCDPSnapshotID:         browserCDP.ID,
		BrowserCDPRevision:           browserCDP.Revision,
		Turn:                         turn.Checkpoint.NextTurn,
		AttemptID:                    turn.Checkpoint.AttemptID,
		Version:                      1,
		State:                        domain.StandardCodeSupervisorExecute,
		MutationEpoch:                1,
		WorkspaceRootFingerprint:     afterCP.RootFingerprint,
		CapabilityGeneration:         expectedCapGen,
		ExpectedCapabilityGeneration: expectedCapGen,
		LastEvidenceFingerprint:      evidence,
		RunTokenLimit:                run1.Budget.MaxTokens,
		RunTimeoutMillis:             int64(run1.Budget.TimeoutSeconds) * 1000,
		RunToolCallLimit:             run1.Budget.MaxToolCalls,
		Limits:                       domain.DefaultStandardCodeSupervisorLimits(),
		CreatedAt:                    now,
		UpdatedAt:                    now,
	}
	entry := domain.StandardCodeSupervisorLedgerEntry{
		ID:                  idgen.New("sc-ledger"),
		OperationKeyDigest:  runmutation.Fingerprint("sc-ledger-op", run1.ID, edit.ID),
		RequestFingerprint:  runmutation.Fingerprint("sc-ledger-req", run1.ID, edit.ID),
		Kind:                domain.StandardCodeSupervisorPhaseAdvanced,
		Decision:            domain.StandardCodeSupervisorObserved,
		ToolAction:          "direct_apply:" + edit.ID,
		ToolKind:            domain.StandardCodeToolWorkspaceMutation,
		EvidenceFingerprint: evidence,
		FromState:           domain.StandardCodeSupervisorExecute,
		ToState:             domain.StandardCodeSupervisorExecute,
		ReasonCode:          "workspace_mutation_checkpoint_verified",
		Snapshot:            snap,
		CreatedAt:           now,
		LeaseID:             lease.LeaseID,
		LeaseGeneration:     lease.Generation,
	}
	if _, replayed, err := state.AppendStandardCodeSupervisorLedger(ctx, 0, entry); err != nil || replayed {
		t.Fatalf("AppendStandardCodeSupervisorLedger replayed=%t err=%v", replayed, err)
	}
	if _, _, err := state.ReleaseRunExecutionLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunService(state).Cancel(ctx, run1.ID); err != nil {
		t.Fatal(err)
	}

	// Submit successor Run 2 on the same Thread and verify GetStandardCodeContinuation succeeds.
	thread, err := state.GetThreadByRun(ctx, run1.ID)
	if err != nil {
		t.Fatal(err)
	}
	next, err := application.NewThreadService(state).WithRunWorktree(drydocks).Submit(ctx, application.SubmitThreadMessageRequest{
		Version: domain.ThreadMessageProtocolVersion, ThreadID: thread.ID,
		Content: "Continue after direct apply", OperationKey: "sc-continuation-next-0001", RequestedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	cont, err := state.GetStandardCodeContinuation(ctx, next.Run.ID)
	if err != nil {
		t.Fatalf("GetStandardCodeContinuation failed on direct_apply backed by tool_approvals: %v", err)
	}
	if cont == nil || cont.PredecessorRunID != run1.ID || cont.DrydockID != owned.ID ||
		cont.WorkspaceID != owned.WorkspaceID || cont.MutationEpoch != 1 ||
		cont.MutationRunID != run1.ID || cont.MutationSnapshotVersion != 1 ||
		cont.MutationCheckpointID != txn.AfterCheckpointID {
		t.Fatalf("unexpected StandardCodeContinuation: %+v", cont)
	}

	// Verify tool_approvals scoping fails closed if approval status is not approved.
	if _, err := state.db.ExecContext(ctx, `UPDATE tool_approvals SET status='denied' WHERE proposal_id=?`, edit.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := state.GetStandardCodeContinuation(ctx, next.Run.ID); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("expected FailedPrecondition when tool_approvals is not approved, got %v", err)
	}
}
