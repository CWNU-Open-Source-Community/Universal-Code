package application_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspace"
)

func TestAgentCodeExecutorCreatesReviewedFileAndFailsClosedOnCASConflict(t *testing.T) {
	for _, targetPath := range []string{"note.txt", "test/nested/note.txt"} {
		t.Run(targetPath, func(t *testing.T) { testAgentCodeReviewedCreate(t, targetPath) })
	}
}

func TestAgentCodeFullAccessCreatesAndAppliesWithoutPerFileReview(t *testing.T) {
	ctx := context.Background()
	rootPath := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(rootPath, 0o755); err != nil {
		t.Fatal(err)
	}
	state, err := store.Open(filepath.Join(t.TempDir(), "auto-file-edit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	workspaceRecord := store.WorkspaceRecord{ID: "workspace-auto-file-edit",
		Name: "auto-file-edit", RootPath: rootPath}
	if err := state.SaveWorkspace(ctx, workspaceRecord); err != nil {
		t.Fatal(err)
	}
	mission, created, err := application.NewRunService(state).Create(ctx,
		application.CreateRunRequest{Goal: "create an ordinary file in Full Access",
			Profile: "code", WorkspaceID: workspaceRecord.ID,
			Budget: domain.Budget{MaxTurns: 8, MaxToolCalls: 20}})
	if err != nil {
		t.Fatal(err)
	}
	runtimeAuthority := domain.NewExecutionPermissionRuntimeAuthority()
	runtimeCapabilities := domain.ExecutionPermissionRuntimeCapabilities{
		OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
		RuntimeAuthority: runtimeAuthority}
	selected, err := application.NewRunExecutionPermissionService(state, runtimeCapabilities).
		Change(ctx, application.ChangeRunExecutionPermissionRequest{
			RunID: created.ID, Mode: string(domain.RunExecutionPermissionFull),
			OperationKey: "auto-file-edit-full-permission-0001", RequestedBy: "test_operator",
			Reason: "select Full Access for ordinary file edits", ConfirmFull: true})
	if err != nil {
		t.Fatal(err)
	}
	run, err := application.NewRunService(state).Start(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	permission := selected.Permission
	generation, live := runtimeCapabilities.FullAccessGeneration(permission)
	if !live {
		if _, err := runtimeAuthority.ActivateRunFullAccess(permission); err != nil {
			t.Fatal(err)
		}
		generation, live = runtimeCapabilities.FullAccessGeneration(permission)
	}
	if !live || generation == 0 || runtimeAuthority.RuntimeEpoch() == "" {
		t.Fatal("Full Access test fixture has no live activation")
	}
	mode, err := state.GetRunMode(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	rootAgent, found, err := state.GetRootAgent(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("root Agent found=%t err=%v", found, err)
	}
	lease, err := state.AcquireRunExecutionLease(ctx,
		domain.AcquireRunExecutionLeaseRequest{RunID: run.ID,
			OwnerID: "auto-file-edit-test", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	rootFingerprint, err := workspace.AgentCodeRootFingerprint(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := runtimeAuthority.IssueRunAuthorizationFence(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	capabilityContext := toolgateway.AgentCodeCapabilityContext{
		RunID: run.ID, MissionID: mission.ID, RootAgentID: rootAgent.ID,
		WorkspaceID: workspaceRecord.ID, RootFingerprint: rootFingerprint,
		Surface: mode.Surface, Phase: mode.Phase, Role: rootAgent.Role,
		Profile: rootAgent.Profile, PermissionMode: permission.Mode,
		PermissionSnapshotID: permission.ID, PermissionGeneration: generation,
		PermissionRuntimeEpoch: runtimeAuthority.RuntimeEpoch(), RunAuthorizationFence: fence,
		ModeRevision: mode.Revision, PermissionRevision: permission.Revision}
	scope := toolgateway.AgentCodeExecutionScope{
		InvocationID: "auto-file-edit-propose-invocation", OperationKey: "auto-file-edit-create-0001",
		RunID: run.ID, MissionID: mission.ID, RootAgentID: rootAgent.ID,
		SessionID: run.SessionID, WorkspaceID: workspaceRecord.ID,
		WorkspaceRoot: rootPath, RootFingerprint: rootFingerprint,
		Surface: mode.Surface, Phase: mode.Phase, Role: rootAgent.Role,
		Profile: rootAgent.Profile, PermissionMode: permission.Mode,
		PermissionSnapshotID: permission.ID, PermissionGeneration: generation,
		PermissionRuntimeEpoch: runtimeAuthority.RuntimeEpoch(), RunAuthorizationFence: fence,
		ModeRevision: mode.Revision, PermissionRevision: permission.Revision,
		CapabilityGeneration: toolgateway.AgentCodeCapabilities(capabilityContext).Generation,
		LeaseID:              lease.Lease.LeaseID, LeaseGeneration: lease.Lease.Generation,
		RequestedBy: "run_supervisor", PolicyDecision: toolgateway.Decision{
			Allowed: true, Approval: toolgateway.ApprovalAutomatic,
			Risk: "low", Reason: "test allowed"}}
	checker := &fileOperationPolicy{reviewDelete: true}
	executor := application.NewAgentCodeToolExecutor(state, checker).WithExecutionPermissionCapabilities(runtimeCapabilities)
	proposed, err := executor.ExecuteAgentCode(ctx, scope, toolgateway.WorkspaceChangeTool,
		mustAgentCodePayload(t, toolgateway.WorkspaceChangePayload{
			Version: toolgateway.AgentCodeRegistryVersion, Action: "create",
			Path: "normal.txt", ExpectedSHA256: "missing", Content: "normal task\n"}))
	if err != nil {
		t.Fatal(err)
	}
	edit := decodeAgentCodeToolResult(t, proposed.JSON)
	var proposalResult map[string]any
	if err := json.Unmarshal([]byte(proposed.JSON), &proposalResult); err != nil {
		t.Fatal(err)
	}
	if edit.Status != fileedit.StatusApproved || proposalResult["review_required"] != false ||
		proposalResult["apply_authorized"] != true ||
		proposalResult["authorization_source"] != "operation_policy_automatic" {
		t.Fatalf("Full Access proposal was not automatically authorized: %s", proposed.JSON)
	}
	approvalRecord, err := state.GetApprovalByProposal(ctx, edit.EditID)
	if err != nil || approvalRecord.Mode != "automatic" || approvalRecord.Status != "approved" ||
		approvalRecord.ReviewedBy != "automatic_policy" {
		t.Fatalf("automatic approval record=%+v err=%v", approvalRecord, err)
	}
	if _, err := os.Stat(filepath.Join(rootPath, "normal.txt")); !os.IsNotExist(err) {
		t.Fatalf("proposal wrote file before workspace_apply: %v", err)
	}
	applyScope := scope
	applyScope.InvocationID = "auto-file-edit-apply-invocation"
	applyScope.OperationKey = "auto-file-edit-apply-0001"
	applied, err := executor.ExecuteAgentCode(ctx, applyScope, toolgateway.WorkspaceApplyTool,
		mustAgentCodePayload(t, toolgateway.WorkspaceApplyPayload{
			Version: toolgateway.AgentCodeRegistryVersion, EditID: edit.EditID,
			ExpectedAction: "create", ExpectedOriginalSHA256: edit.OriginalSHA256,
			ExpectedProposedSHA256: edit.ProposedSHA256}))
	if err != nil || !strings.Contains(applied.JSON, `"file_written":true`) {
		t.Fatalf("Full Access apply=%s err=%v", applied.JSON, err)
	}
	bytes, err := os.ReadFile(filepath.Join(rootPath, "normal.txt"))
	if err != nil || string(bytes) != "normal task\n" {
		t.Fatalf("written file=%q err=%v", bytes, err)
	}
	replaceScope := scope
	replaceScope.InvocationID = "auto-file-edit-replace-invocation"
	replaceScope.OperationKey = "auto-file-edit-replace-0001"
	replaced, err := executor.ExecuteAgentCode(ctx, replaceScope, toolgateway.WorkspaceChangeTool,
		mustAgentCodePayload(t, toolgateway.WorkspaceChangePayload{
			Version: toolgateway.AgentCodeRegistryVersion, Action: "propose_patch",
			Path: "normal.txt", ExpectedSHA256: edit.ProposedSHA256,
			Replacements: []toolgateway.WorkspaceReplacement{{
				OldText: "normal task\n", NewText: "updated task\n", ExpectedOccurrences: 1}}}))
	if err != nil {
		t.Fatal(err)
	}
	replaceEdit := decodeAgentCodeToolResult(t, replaced.JSON)
	if replaceEdit.Status != fileedit.StatusApproved ||
		replaceEdit.OriginalSHA256 != edit.ProposedSHA256 {
		t.Fatalf("Full Access replace was not automatically authorized: %s", replaced.JSON)
	}
	replaceApplyScope := replaceScope
	replaceApplyScope.InvocationID = "auto-file-edit-replace-apply-invocation"
	replaceApplyScope.OperationKey = "auto-file-edit-replace-apply-0001"
	if _, err := executor.ExecuteAgentCode(ctx, replaceApplyScope, toolgateway.WorkspaceApplyTool,
		mustAgentCodePayload(t, toolgateway.WorkspaceApplyPayload{
			Version: toolgateway.AgentCodeRegistryVersion, EditID: replaceEdit.EditID,
			ExpectedAction: "propose_patch", ExpectedOriginalSHA256: replaceEdit.OriginalSHA256,
			ExpectedProposedSHA256: replaceEdit.ProposedSHA256})); err != nil {
		t.Fatal(err)
	}
	bytes, err = os.ReadFile(filepath.Join(rootPath, "normal.txt"))
	if err != nil || string(bytes) != "updated task\n" {
		t.Fatalf("replaced file=%q err=%v", bytes, err)
	}
	manualRevertKey := "auto-file-edit-existing-pending-revert-0001"
	manualRevert, err := application.NewFileEditProposalService(state,
		policy.NewDefaultChecker()).ProposeRevert(ctx,
		application.CreateFileEditRevertProposalRequest{
			Version: application.FileEditProposalProtocolVersion, RunID: run.ID,
			SourceRunID: run.ID, SourceEditID: replaceEdit.EditID, Path: "normal.txt",
			ExpectedSHA256: replaceEdit.ProposedSHA256, OperationKey: manualRevertKey})
	if err != nil || manualRevert.Edit.Status != fileedit.StatusProposed {
		t.Fatalf("manual revert proposal=%+v err=%v", manualRevert, err)
	}
	manualRevertScope := scope
	manualRevertScope.InvocationID = "auto-file-edit-existing-pending-revert-invocation"
	manualRevertScope.OperationKey = manualRevertKey
	manualReplay, err := executor.ExecuteAgentCode(ctx, manualRevertScope,
		toolgateway.WorkspaceChangeTool, mustAgentCodePayload(t,
			toolgateway.WorkspaceChangePayload{Version: toolgateway.AgentCodeRegistryVersion,
				Action: "propose_revert", SourceRunID: run.ID,
				SourceEditID: replaceEdit.EditID, Path: "normal.txt",
				ExpectedSHA256: replaceEdit.ProposedSHA256}))
	if err != nil {
		t.Fatal(err)
	}
	manualReplayEdit := decodeAgentCodeToolResult(t, manualReplay.JSON)
	if manualReplayEdit.Status != fileedit.StatusProposed {
		t.Fatalf("existing pending revert was upgraded: %s", manualReplay.JSON)
	}
	if _, found, err := state.GetFileEditAutoAuthorization(ctx, manualReplayEdit.EditID); err != nil || found {
		t.Fatalf("existing pending revert gained automatic source: found=%t err=%v", found, err)
	}
	if _, err := application.NewFileEditReviewService(state).Review(ctx,
		application.ReviewFileEditRequest{Version: application.FileEditReviewProtocolVersion,
			RunID: run.ID, EditID: manualReplayEdit.EditID,
			Action: application.FileEditDeny}); err != nil {
		t.Fatal(err)
	}
	deniedReplay, err := executor.ExecuteAgentCode(ctx, manualRevertScope,
		toolgateway.WorkspaceChangeTool, mustAgentCodePayload(t,
			toolgateway.WorkspaceChangePayload{Version: toolgateway.AgentCodeRegistryVersion,
				Action: "propose_revert", SourceRunID: run.ID,
				SourceEditID: replaceEdit.EditID, Path: "normal.txt",
				ExpectedSHA256: replaceEdit.ProposedSHA256}))
	if err != nil {
		t.Fatal(err)
	}
	deniedReplayEdit := decodeAgentCodeToolResult(t, deniedReplay.JSON)
	if deniedReplayEdit.Status != fileedit.StatusDenied {
		t.Fatalf("existing denied revert was upgraded: %s", deniedReplay.JSON)
	}
	if _, found, err := state.GetFileEditAutoAuthorization(ctx, deniedReplayEdit.EditID); err != nil || found {
		t.Fatalf("existing denied revert gained automatic source: found=%t err=%v", found, err)
	}

	revertScope := scope
	revertScope.InvocationID = "auto-file-edit-revert-invocation"
	revertScope.OperationKey = "auto-file-edit-revert-0001"
	reverted, err := executor.ExecuteAgentCode(ctx, revertScope,
		toolgateway.WorkspaceChangeTool, mustAgentCodePayload(t,
			toolgateway.WorkspaceChangePayload{Version: toolgateway.AgentCodeRegistryVersion,
				Action: "propose_revert", SourceRunID: run.ID,
				SourceEditID: replaceEdit.EditID, Path: "normal.txt",
				ExpectedSHA256: replaceEdit.ProposedSHA256}))
	if err != nil {
		t.Fatal(err)
	}
	revertEdit := decodeAgentCodeToolResult(t, reverted.JSON)
	var revertProposal map[string]any
	if err := json.Unmarshal([]byte(reverted.JSON), &revertProposal); err != nil ||
		revertEdit.Status != fileedit.StatusApproved || revertEdit.Operation != fileedit.OperationReplace ||
		revertProposal["review_required"] != false || revertProposal["apply_authorized"] != true ||
		revertProposal["delete_confirmation_required"] != false {
		t.Fatalf("Full Access inverse replace was not automatically authorized: %s err=%v",
			reverted.JSON, err)
	}
	revertApproval, err := state.GetApprovalByProposal(ctx, revertEdit.EditID)
	if err != nil || revertApproval.Mode != "automatic" || revertApproval.Status != "approved" {
		t.Fatalf("automatic revert approval=%+v err=%v", revertApproval, err)
	}
	revertApplyScope := revertScope
	revertApplyScope.InvocationID = "auto-file-edit-revert-apply-invocation"
	revertApplyScope.OperationKey = "auto-file-edit-revert-apply-0001"
	revertApplied, err := executor.ExecuteAgentCode(ctx, revertApplyScope,
		toolgateway.WorkspaceApplyTool, mustAgentCodePayload(t,
			toolgateway.WorkspaceApplyPayload{Version: toolgateway.AgentCodeRegistryVersion,
				EditID: revertEdit.EditID, ExpectedAction: "propose_patch",
				ExpectedOriginalSHA256: revertEdit.OriginalSHA256,
				ExpectedProposedSHA256: revertEdit.ProposedSHA256}))
	if err != nil || !strings.Contains(revertApplied.JSON, `"file_written":true`) {
		t.Fatalf("Full Access inverse replace apply=%s err=%v", revertApplied.JSON, err)
	}
	bytes, err = os.ReadFile(filepath.Join(rootPath, "normal.txt"))
	if err != nil || string(bytes) != "normal task\n" {
		t.Fatalf("inverse replace did not restore exact bytes=%q err=%v", bytes, err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "normal.txt"), []byte("later user edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	revertReplay, err := executor.ExecuteAgentCode(ctx, revertApplyScope,
		toolgateway.WorkspaceApplyTool, mustAgentCodePayload(t,
			toolgateway.WorkspaceApplyPayload{Version: toolgateway.AgentCodeRegistryVersion,
				EditID: revertEdit.EditID, ExpectedAction: "propose_patch",
				ExpectedOriginalSHA256: revertEdit.OriginalSHA256,
				ExpectedProposedSHA256: revertEdit.ProposedSHA256}))
	if err != nil || !strings.Contains(revertReplay.JSON, `"replayed":true`) ||
		!strings.Contains(revertReplay.JSON, `"file_written":false`) {
		t.Fatalf("completed inverse receipt replay=%s err=%v", revertReplay.JSON, err)
	}
	if data, readErr := os.ReadFile(filepath.Join(rootPath, "normal.txt")); readErr != nil || string(data) != "later user edit\n" {
		t.Fatalf("completed inverse replay touched later user content=%q err=%v", data, readErr)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "normal.txt"), []byte("normal task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	collisionSourceScope := scope
	collisionSourceScope.InvocationID = "auto-file-edit-revert-key-collision-source-invocation"
	collisionSourceScope.OperationKey = "auto-file-edit-revert-key-collision-source-0001"
	collisionSourceProposal, err := executor.ExecuteAgentCode(ctx, collisionSourceScope,
		toolgateway.WorkspaceChangeTool, mustAgentCodePayload(t,
			toolgateway.WorkspaceChangePayload{Version: toolgateway.AgentCodeRegistryVersion,
				Action: "propose_patch", Path: "normal.txt",
				ExpectedSHA256: fileedit.HashText("normal task\n"),
				Replacements: []toolgateway.WorkspaceReplacement{{OldText: "normal task\n",
					NewText: "collision source\n", ExpectedOccurrences: 1}}}))
	if err != nil {
		t.Fatal(err)
	}
	collisionSourceEdit := decodeAgentCodeToolResult(t, collisionSourceProposal.JSON)
	collisionApplyScope := collisionSourceScope
	collisionApplyScope.InvocationID = "auto-file-edit-revert-key-collision-source-apply-invocation"
	collisionApplyScope.OperationKey = "auto-file-edit-revert-key-collision-source-apply-0001"
	if _, err := executor.ExecuteAgentCode(ctx, collisionApplyScope,
		toolgateway.WorkspaceApplyTool, mustAgentCodePayload(t,
			toolgateway.WorkspaceApplyPayload{Version: toolgateway.AgentCodeRegistryVersion,
				EditID: collisionSourceEdit.EditID, ExpectedAction: "propose_patch",
				ExpectedOriginalSHA256: collisionSourceEdit.OriginalSHA256,
				ExpectedProposedSHA256: collisionSourceEdit.ProposedSHA256})); err != nil {
		t.Fatal(err)
	}
	collisionRevertScope := revertScope
	collisionRevertScope.InvocationID = "auto-file-edit-revert-key-collision-invocation"
	editsBeforeCollision, err := state.ListFileEdits(ctx,
		fileedit.ListFilter{SessionID: run.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.ExecuteAgentCode(ctx, collisionRevertScope,
		toolgateway.WorkspaceChangeTool, mustAgentCodePayload(t,
			toolgateway.WorkspaceChangePayload{Version: toolgateway.AgentCodeRegistryVersion,
				Action: "propose_revert", SourceRunID: run.ID,
				SourceEditID: collisionSourceEdit.EditID, Path: "normal.txt",
				ExpectedSHA256: collisionSourceEdit.ProposedSHA256})); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeConflict &&
		apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeFailedPrecondition {
		t.Fatalf("same automatic operation key accepted a different revert source: %v", err)
	}
	editsAfterCollision, err := state.ListFileEdits(ctx,
		fileedit.ListFilter{SessionID: run.SessionID})
	if err != nil || len(editsAfterCollision) != len(editsBeforeCollision) {
		t.Fatalf("rejected operation-key reuse persisted an edit: before=%d after=%d err=%v",
			len(editsBeforeCollision), len(editsAfterCollision), err)
	}
	if data, readErr := os.ReadFile(filepath.Join(rootPath, "normal.txt")); readErr != nil || string(data) != "collision source\n" {
		t.Fatalf("rejected operation-key reuse changed file=%q err=%v", data, readErr)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "normal.txt"), []byte("normal task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	moveScope := scope
	moveScope.InvocationID = "auto-file-edit-move-invocation"
	moveScope.OperationKey = "auto-file-edit-move-0001"
	movePayload := toolgateway.WorkspaceChangePayload{
		Version: toolgateway.AgentCodeRegistryVersion, Action: "move",
		Path: "normal.txt", ExpectedSHA256: revertEdit.ProposedSHA256,
		DestinationPath: "moved.txt", DestinationExpectedSHA256: "missing"}
	moveResult, err := executor.ExecuteAgentCode(ctx, moveScope, toolgateway.WorkspaceChangeTool,
		mustAgentCodePayload(t, movePayload))
	if err != nil {
		t.Fatal(err)
	}
	moveEdit := decodeAgentCodeToolResult(t, moveResult.JSON)
	var moveProposal map[string]any
	if err := json.Unmarshal([]byte(moveResult.JSON), &moveProposal); err != nil {
		t.Fatal(err)
	}
	if moveEdit.Status != fileedit.StatusApproved || moveProposal["review_required"] != false ||
		moveProposal["apply_authorized"] != true ||
		moveProposal["authorization_source"] != "operation_policy_automatic" {
		t.Fatalf("Full Access move was not automatically authorized: %s", moveResult.JSON)
	}
	if _, err := os.Stat(filepath.Join(rootPath, "moved.txt")); !os.IsNotExist(err) {
		t.Fatalf("move proposal wrote before workspace_apply: %v", err)
	}
	moveReplay, err := executor.ExecuteAgentCode(ctx, moveScope,
		toolgateway.WorkspaceChangeTool, mustAgentCodePayload(t, movePayload))
	if err != nil || moveReplay.JSON != moveResult.JSON {
		t.Fatalf("exact move proposal replay changed result: replay=%s err=%v",
			moveReplay.JSON, err)
	}
	changedMove := movePayload
	changedMove.DestinationPath = "different.txt"
	if _, err := executor.ExecuteAgentCode(ctx, moveScope, toolgateway.WorkspaceChangeTool,
		mustAgentCodePayload(t, changedMove)); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeConflict {
		t.Fatalf("same move key accepted a changed destination: %v", err)
	}
	moveApplyScope := moveScope
	moveApplyScope.InvocationID = "auto-file-edit-move-apply-invocation"
	moveApplyScope.OperationKey = "auto-file-edit-move-apply-0001"
	moveApplied, err := executor.ExecuteAgentCode(ctx, moveApplyScope,
		toolgateway.WorkspaceApplyTool, mustAgentCodePayload(t, toolgateway.WorkspaceApplyPayload{
			Version: toolgateway.AgentCodeRegistryVersion, EditID: moveEdit.EditID,
			ExpectedAction: "move", ExpectedOriginalSHA256: moveEdit.OriginalSHA256,
			ExpectedProposedSHA256: moveEdit.ProposedSHA256}))
	if err != nil {
		t.Fatal(err)
	}
	var moveApplyResult map[string]any
	if err := json.Unmarshal([]byte(moveApplied.JSON), &moveApplyResult); err != nil ||
		moveApplyResult["file_written"] != true {
		t.Fatalf("Full Access move apply=%s err=%v", moveApplied.JSON, err)
	}
	feedback, ok := moveApplyResult["diagnostics"].(map[string]any)
	if !ok || feedback["path"] != "moved.txt" || feedback["document_sha256"] != fileedit.HashText("normal task\n") || feedback["status"] != "unavailable" {
		t.Fatalf("move feedback must identify its destination and missing backend: %s", moveApplied.JSON)
	}
	if _, err := os.Stat(filepath.Join(rootPath, "normal.txt")); !os.IsNotExist(err) {
		t.Fatalf("move source still exists: %v", err)
	}
	bytes, err = os.ReadFile(filepath.Join(rootPath, "moved.txt"))
	if err != nil || string(bytes) != "normal task\n" {
		t.Fatalf("moved file=%q err=%v", bytes, err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "moved.txt"),
		[]byte("user changed after move\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	moveApplyReplay, err := executor.ExecuteAgentCode(ctx, moveApplyScope,
		toolgateway.WorkspaceApplyTool, mustAgentCodePayload(t, toolgateway.WorkspaceApplyPayload{
			Version: toolgateway.AgentCodeRegistryVersion, EditID: moveEdit.EditID,
			ExpectedAction: "move", ExpectedOriginalSHA256: moveEdit.OriginalSHA256,
			ExpectedProposedSHA256: moveEdit.ProposedSHA256}))
	if err != nil {
		t.Fatal(err)
	}
	var moveReplayResult map[string]any
	if err := json.Unmarshal([]byte(moveApplyReplay.JSON), &moveReplayResult); err != nil ||
		moveReplayResult["replayed"] != true || moveReplayResult["file_written"] != false {
		t.Fatalf("completed move receipt replay=%s err=%v", moveApplyReplay.JSON, err)
	}
	if data, readErr := os.ReadFile(filepath.Join(rootPath, "moved.txt")); readErr != nil || string(data) != "user changed after move\n" {
		t.Fatalf("completed move replay touched later user content=%q err=%v", data, readErr)
	}
	deleteScope := scope
	deleteScope.InvocationID = "auto-file-edit-delete-invocation"
	deleteScope.OperationKey = "auto-file-edit-delete-0001"
	deleteProposal, err := executor.ExecuteAgentCode(ctx, deleteScope,
		toolgateway.WorkspaceDeleteTool, mustAgentCodePayload(t,
			toolgateway.WorkspaceDeletePayload{Version: toolgateway.AgentCodeRegistryVersion,
				Action: "propose", Path: "moved.txt", ConfirmPath: "moved.txt",
				ExpectedSHA256: fileedit.HashText("user changed after move\n")}))
	if err != nil {
		t.Fatal(err)
	}
	deleteEdit := decodeAgentCodeToolResult(t, deleteProposal.JSON)
	var deleteProposalResult map[string]any
	if err := json.Unmarshal([]byte(deleteProposal.JSON), &deleteProposalResult); err != nil ||
		deleteEdit.Status != fileedit.StatusProposed ||
		deleteProposalResult["delete_confirmation_required"] != true ||
		deleteProposalResult["review_required"] != true {
		t.Fatalf("direct delete did not remain review-gated: %s err=%v", deleteProposal.JSON, err)
	}
	if _, err := application.NewFileEditReviewService(state).Review(ctx,
		application.ReviewFileEditRequest{Version: application.FileEditReviewProtocolVersion,
			RunID: run.ID, EditID: deleteEdit.EditID,
			Action: application.FileEditApproveIntent}); err != nil {
		t.Fatal(err)
	}
	deleteApplyScope := deleteScope
	deleteApplyScope.InvocationID = "auto-file-edit-delete-apply-invocation"
	deleteApplyScope.OperationKey = "auto-file-edit-delete-apply-0001"
	if _, err := executor.ExecuteAgentCode(ctx, deleteApplyScope,
		toolgateway.WorkspaceDeleteTool, mustAgentCodePayload(t,
			toolgateway.WorkspaceDeletePayload{Version: toolgateway.AgentCodeRegistryVersion,
				Action: "apply", EditID: deleteEdit.EditID, Path: "moved.txt",
				ConfirmPath: "moved.txt", ExpectedSHA256: deleteEdit.OriginalSHA256})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(rootPath, "moved.txt")); !os.IsNotExist(err) {
		t.Fatalf("reviewed delete left its target: %v", err)
	}
	restoreScope := scope
	restoreScope.InvocationID = "auto-file-edit-restore-delete-invocation"
	restoreScope.OperationKey = "auto-file-edit-restore-delete-0001"
	restoreProposal, err := executor.ExecuteAgentCode(ctx, restoreScope,
		toolgateway.WorkspaceChangeTool, mustAgentCodePayload(t,
			toolgateway.WorkspaceChangePayload{Version: toolgateway.AgentCodeRegistryVersion,
				Action: "propose_revert", SourceRunID: run.ID,
				SourceEditID: deleteEdit.EditID, Path: "moved.txt",
				ExpectedSHA256: deleteEdit.ProposedSHA256}))
	if err != nil {
		t.Fatal(err)
	}
	restoreEdit := decodeAgentCodeToolResult(t, restoreProposal.JSON)
	var restoreProposalResult map[string]any
	if err := json.Unmarshal([]byte(restoreProposal.JSON), &restoreProposalResult); err != nil ||
		restoreEdit.Operation != fileedit.OperationCreate || restoreEdit.Status != fileedit.StatusApproved ||
		restoreProposalResult["review_required"] != false ||
		restoreProposalResult["apply_authorized"] != true ||
		restoreProposalResult["delete_confirmation_required"] != false {
		t.Fatalf("Full Access inverse create was not automatically authorized: %s err=%v",
			restoreProposal.JSON, err)
	}
	restoreApplyScope := restoreScope
	restoreApplyScope.InvocationID = "auto-file-edit-restore-delete-apply-invocation"
	restoreApplyScope.OperationKey = "auto-file-edit-restore-delete-apply-0001"
	if _, err := executor.ExecuteAgentCode(ctx, restoreApplyScope,
		toolgateway.WorkspaceApplyTool, mustAgentCodePayload(t,
			toolgateway.WorkspaceApplyPayload{Version: toolgateway.AgentCodeRegistryVersion,
				EditID: restoreEdit.EditID, ExpectedAction: "create",
				ExpectedOriginalSHA256: restoreEdit.OriginalSHA256,
				ExpectedProposedSHA256: restoreEdit.ProposedSHA256})); err != nil {
		t.Fatal(err)
	}
	if data, readErr := os.ReadFile(filepath.Join(rootPath, "moved.txt")); readErr != nil || string(data) != "user changed after move\n" {
		t.Fatalf("inverse create did not restore deleted bytes=%q err=%v", data, readErr)
	}
	deleteInverseScope := scope
	deleteInverseScope.InvocationID = "auto-file-edit-delete-inverse-invocation"
	deleteInverseScope.OperationKey = "auto-file-edit-delete-inverse-0001"
	deleteInverse, err := executor.ExecuteAgentCode(ctx, deleteInverseScope,
		toolgateway.WorkspaceChangeTool, mustAgentCodePayload(t,
			toolgateway.WorkspaceChangePayload{Version: toolgateway.AgentCodeRegistryVersion,
				Action: "propose_revert", SourceRunID: run.ID,
				SourceEditID: restoreEdit.EditID, Path: "moved.txt",
				ExpectedSHA256: restoreEdit.ProposedSHA256}))
	if err != nil {
		t.Fatal(err)
	}
	deleteInverseEdit := decodeAgentCodeToolResult(t, deleteInverse.JSON)
	var deleteInverseResult map[string]any
	if err := json.Unmarshal([]byte(deleteInverse.JSON), &deleteInverseResult); err != nil ||
		deleteInverseEdit.Operation != fileedit.OperationDelete ||
		deleteInverseEdit.Status != fileedit.StatusProposed ||
		deleteInverseResult["review_required"] != true ||
		deleteInverseResult["apply_authorized"] != false ||
		deleteInverseResult["delete_confirmation_required"] != true {
		t.Fatalf("inverse delete did not remain review-gated: %s err=%v", deleteInverse.JSON, err)
	}
	if _, found, err := state.GetFileEditAutoAuthorization(ctx, deleteInverseEdit.EditID); err != nil || found {
		t.Fatalf("inverse delete gained automatic source: found=%t err=%v", found, err)
	}
	pendingInverseSourceScope := scope
	pendingInverseSourceScope.InvocationID = "auto-file-edit-pending-inverse-source-invocation"
	pendingInverseSourceScope.OperationKey = "auto-file-edit-pending-inverse-source-0001"
	pendingInverseSource, err := executor.ExecuteAgentCode(ctx, pendingInverseSourceScope,
		toolgateway.WorkspaceChangeTool, mustAgentCodePayload(t,
			toolgateway.WorkspaceChangePayload{Version: toolgateway.AgentCodeRegistryVersion,
				Action: "propose_patch", Path: "moved.txt",
				ExpectedSHA256: fileedit.HashText("user changed after move\n"),
				Replacements: []toolgateway.WorkspaceReplacement{{OldText: "user changed after move\n",
					NewText: "pending inverse source\n", ExpectedOccurrences: 1}}}))
	if err != nil {
		t.Fatal(err)
	}
	pendingInverseSourceEdit := decodeAgentCodeToolResult(t, pendingInverseSource.JSON)
	pendingInverseSourceApplyScope := pendingInverseSourceScope
	pendingInverseSourceApplyScope.InvocationID = "auto-file-edit-pending-inverse-source-apply-invocation"
	pendingInverseSourceApplyScope.OperationKey = "auto-file-edit-pending-inverse-source-apply-0001"
	if _, err := executor.ExecuteAgentCode(ctx, pendingInverseSourceApplyScope,
		toolgateway.WorkspaceApplyTool, mustAgentCodePayload(t,
			toolgateway.WorkspaceApplyPayload{Version: toolgateway.AgentCodeRegistryVersion,
				EditID: pendingInverseSourceEdit.EditID, ExpectedAction: "propose_patch",
				ExpectedOriginalSHA256: pendingInverseSourceEdit.OriginalSHA256,
				ExpectedProposedSHA256: pendingInverseSourceEdit.ProposedSHA256})); err != nil {
		t.Fatal(err)
	}
	pendingInverseScope := scope
	pendingInverseScope.InvocationID = "auto-file-edit-pending-inverse-invocation"
	pendingInverseScope.OperationKey = "auto-file-edit-pending-inverse-0001"
	pendingInverse, err := executor.ExecuteAgentCode(ctx, pendingInverseScope,
		toolgateway.WorkspaceChangeTool, mustAgentCodePayload(t,
			toolgateway.WorkspaceChangePayload{Version: toolgateway.AgentCodeRegistryVersion,
				Action: "propose_revert", SourceRunID: run.ID,
				SourceEditID: pendingInverseSourceEdit.EditID, Path: "moved.txt",
				ExpectedSHA256: pendingInverseSourceEdit.ProposedSHA256}))
	if err != nil {
		t.Fatal(err)
	}
	pendingInverseEdit := decodeAgentCodeToolResult(t, pendingInverse.JSON)
	if pendingInverseEdit.Operation != fileedit.OperationReplace ||
		pendingInverseEdit.Status != fileedit.StatusApproved {
		t.Fatalf("pending inverse was not automatically authorized: %s", pendingInverse.JSON)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "pending-move-source.txt"),
		[]byte("must remain\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pendingMoveSourceHash, err := fileedit.CurrentHash(rootPath, "pending-move-source.txt")
	if err != nil {
		t.Fatal(err)
	}
	pendingMoveScope := scope
	pendingMoveScope.InvocationID = "auto-file-edit-pending-move-invocation"
	pendingMoveScope.OperationKey = "auto-file-edit-pending-move-0001"
	pendingMove, err := executor.ExecuteAgentCode(ctx, pendingMoveScope,
		toolgateway.WorkspaceChangeTool, mustAgentCodePayload(t, toolgateway.WorkspaceChangePayload{
			Version: toolgateway.AgentCodeRegistryVersion, Action: "move",
			Path: "pending-move-source.txt", ExpectedSHA256: pendingMoveSourceHash,
			DestinationPath:           "pending-move-destination.txt",
			DestinationExpectedSHA256: "missing"}))
	if err != nil {
		t.Fatal(err)
	}
	pendingMoveEdit := decodeAgentCodeToolResult(t, pendingMove.JSON)
	pendingScope := scope
	pendingScope.InvocationID = "auto-file-edit-pending-invocation"
	pendingScope.OperationKey = "auto-file-edit-pending-0001"
	pending, err := executor.ExecuteAgentCode(ctx, pendingScope, toolgateway.WorkspaceChangeTool,
		mustAgentCodePayload(t, toolgateway.WorkspaceChangePayload{
			Version: toolgateway.AgentCodeRegistryVersion, Action: "create",
			Path: "pending.txt", ExpectedSHA256: "missing", Content: "pending work\n"}))
	if err != nil {
		t.Fatal(err)
	}
	pendingEdit := decodeAgentCodeToolResult(t, pending.JSON)
	runtimeAuthority.RevokeRun(run.ID)
	staleApply := application.ApplyFileEditRequest{
		Version: fileedit.FileEditApplyProtocolVersion, RunID: run.ID,
		EditID: pendingEdit.EditID, OperationKey: "auto-file-edit-pending-apply-0001",
		AppliedBy: rootAgent.ID, InvocationID: "auto-file-edit-pending-apply-invocation",
		CapabilityGeneration: scope.CapabilityGeneration, LeaseID: scope.LeaseID,
		LeaseGeneration: scope.LeaseGeneration, PermissionSnapshotID: permission.ID,
		PermissionGeneration: generation, PermissionRuntimeEpoch: runtimeAuthority.RuntimeEpoch(), RunAuthorizationFence: fence}
	applyService := application.NewFileEditApplyService(state,
		policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(runtimeCapabilities)
	completedReplay, err := applyService.Apply(ctx, application.ApplyFileEditRequest{
		Version: fileedit.FileEditApplyProtocolVersion, RunID: run.ID,
		EditID: edit.EditID, OperationKey: applyScope.OperationKey,
		AppliedBy: rootAgent.ID, InvocationID: applyScope.InvocationID,
		CapabilityGeneration: scope.CapabilityGeneration, LeaseID: scope.LeaseID,
		LeaseGeneration: scope.LeaseGeneration, PermissionSnapshotID: permission.ID,
		PermissionGeneration: generation, PermissionRuntimeEpoch: runtimeAuthority.RuntimeEpoch(), RunAuthorizationFence: fence})
	if err != nil || !completedReplay.Replayed || completedReplay.FileWritten {
		t.Fatalf("completed FileEdit receipt was not read-only after revoke: result=%+v err=%v",
			completedReplay, err)
	}
	if _, err := applyService.Apply(ctx, staleApply); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodePolicyDenied {
		t.Fatalf("revoked automatic FileEdit apply err=%v", err)
	}
	staleInverseApply := staleApply
	staleInverseApply.EditID = pendingInverseEdit.EditID
	staleInverseApply.OperationKey = "auto-file-edit-pending-inverse-apply-0001"
	staleInverseApply.InvocationID = "auto-file-edit-pending-inverse-apply-invocation"
	if _, err := applyService.Apply(ctx, staleInverseApply); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodePolicyDenied {
		t.Fatalf("revoked automatic inverse apply err=%v", err)
	}
	if data, err := os.ReadFile(filepath.Join(rootPath, "moved.txt")); err != nil || string(data) != "pending inverse source\n" {
		t.Fatalf("revoked automatic inverse changed bytes=%q err=%v", data, err)
	}
	staleMoveApply := staleApply
	staleMoveApply.EditID = pendingMoveEdit.EditID
	staleMoveApply.OperationKey = "auto-file-edit-pending-move-apply-0001"
	staleMoveApply.InvocationID = "auto-file-edit-pending-move-apply-invocation"
	if _, err := applyService.Apply(ctx, staleMoveApply); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodePolicyDenied {
		t.Fatalf("revoked automatic move apply err=%v", err)
	}
	if data, err := os.ReadFile(filepath.Join(rootPath, "pending-move-source.txt")); err != nil || string(data) != "must remain\n" {
		t.Fatalf("revoked automatic move changed source=%q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(rootPath, "pending-move-destination.txt")); !os.IsNotExist(err) {
		t.Fatalf("revoked automatic move published destination: %v", err)
	}
	if _, err := runtimeAuthority.ActivateRunFullAccess(permission); err != nil {
		t.Fatal(err)
	}
	if _, err := applyService.Apply(ctx, staleApply); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodePolicyDenied {
		t.Fatalf("old automatic FileEdit source revived after Full reactivation: %v", err)
	}
	freshRuntimeAuthority := domain.NewExecutionPermissionRuntimeAuthority()
	for step := uint64(1); step < generation; step++ {
		freshRuntimeAuthority.RevokeRun(fmt.Sprintf("run-unrelated-%d", step))
	}
	freshGrant, err := freshRuntimeAuthority.ActivateRunFullAccess(permission)
	if err != nil || freshGrant.Generation != generation ||
		freshRuntimeAuthority.RuntimeEpoch() == runtimeAuthority.RuntimeEpoch() {
		t.Fatalf("cross-process collision fixture: grant=%+v err=%v", freshGrant, err)
	}
	freshCapabilities := runtimeCapabilities
	freshCapabilities.RuntimeAuthority = freshRuntimeAuthority
	applyService.WithExecutionPermissionCapabilities(freshCapabilities)
	staleApply.PermissionRuntimeEpoch = freshRuntimeAuthority.RuntimeEpoch()
	if _, err := applyService.Apply(ctx, staleApply); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodePolicyDenied {
		t.Fatalf("old automatic FileEdit source revived in new process with same generation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootPath, "pending.txt")); !os.IsNotExist(err) {
		t.Fatalf("revoked automatic FileEdit wrote bytes: %v", err)
	}
	staleScope := scope
	staleScope.InvocationID = "auto-file-edit-stale-invocation"
	staleScope.OperationKey = "auto-file-edit-stale-0001"
	if _, err := executor.ExecuteAgentCode(ctx, staleScope, toolgateway.WorkspaceChangeTool,
		mustAgentCodePayload(t, toolgateway.WorkspaceChangePayload{
			Version: toolgateway.AgentCodeRegistryVersion, Action: "create",
			Path: "stale.txt", ExpectedSHA256: "missing", Content: "must not write\n"})); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodePolicyDenied {
		t.Fatalf("revoked Full Access create err=%v", err)
	}
}

func testAgentCodeReviewedCreate(t *testing.T, targetPath string) {
	ctx := context.Background()
	home := t.TempDir()
	workspaceRoot := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if targetPath != "note.txt" {
		// A normal npm command can leave this zero-byte file. It must remain
		// representable in the pre-apply checkpoint without blocking the edit.
		cache := filepath.Join(workspaceRoot, ".npm-cache")
		if err := os.Mkdir(cache, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cache, "_update-notifier-last-checked"), []byte{}, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	state, err := store.Open(filepath.Join(home, "agent-code.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	record := store.WorkspaceRecord{ID: "workspace-agent-code", Name: "agent-code",
		RootPath: workspaceRoot}
	if err := state.SaveWorkspace(ctx, record); err != nil {
		t.Fatal(err)
	}
	mission, created, err := application.NewRunService(state).Create(ctx,
		application.CreateRunRequest{Goal: "edit workspace", Profile: "code",
			WorkspaceID: record.ID, Budget: domain.Budget{MaxTurns: 8, MaxToolCalls: 20}})
	if err != nil {
		t.Fatal(err)
	}
	permissionService := application.NewRunExecutionPermissionService(state,
		domain.ExecutionPermissionRuntimeCapabilities{WorkspaceSandboxEnabled: true})
	if _, err := permissionService.Change(ctx,
		application.ChangeRunExecutionPermissionRequest{
			RunID: created.ID, Mode: string(domain.RunExecutionPermissionAuto),
			OperationKey: "agent-code-permission-setup-0001", RequestedBy: "test_operator",
			Reason: "select workspace access before exercising Agent Code",
		}); err != nil {
		t.Fatal(err)
	}
	run, err := application.NewRunService(state).Start(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	mode, err := state.GetRunMode(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	permission, err := state.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	rootAgent, found, err := state.GetRootAgent(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("root Agent found=%t err=%v", found, err)
	}
	leaseResult, err := state.AcquireRunExecutionLease(ctx,
		domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "agent-code-test",
			TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	rootFingerprint, err := workspace.AgentCodeRootFingerprint(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	capabilityContext := toolgateway.AgentCodeCapabilityContext{RunID: run.ID,
		MissionID: mission.ID, RootAgentID: rootAgent.ID, WorkspaceID: record.ID,
		RootFingerprint: rootFingerprint, Surface: mode.Surface, Phase: mode.Phase,
		Role: rootAgent.Role, Profile: rootAgent.Profile, PermissionMode: permission.Mode,
		PermissionSnapshotID: permission.ID, ModeRevision: mode.Revision, PermissionRevision: permission.Revision}
	capabilities := toolgateway.AgentCodeCapabilities(capabilityContext)
	scope := toolgateway.AgentCodeExecutionScope{InvocationID: "invocation-agent-code-1",
		OperationKey: "agent-code-create-operation-0001", RunID: run.ID,
		MissionID: mission.ID, RootAgentID: rootAgent.ID, SessionID: run.SessionID,
		WorkspaceID: record.ID, WorkspaceRoot: workspaceRoot,
		RootFingerprint: rootFingerprint, Surface: mode.Surface, Phase: mode.Phase,
		Role: rootAgent.Role, Profile: rootAgent.Profile, PermissionMode: permission.Mode,
		PermissionSnapshotID: permission.ID, ModeRevision: mode.Revision, PermissionRevision: permission.Revision,
		CapabilityGeneration: capabilities.Generation, LeaseID: leaseResult.Lease.LeaseID,
		LeaseGeneration: leaseResult.Lease.Generation, RequestedBy: "run_supervisor",
		PolicyDecision: toolgateway.Decision{Allowed: true,
			Approval: toolgateway.ApprovalAutomatic, Risk: "low", Reason: "test allowed"}}
	executor := application.NewAgentCodeToolExecutor(state, &fileOperationPolicy{review: true})

	createPayload := mustAgentCodePayload(t, toolgateway.WorkspaceChangePayload{
		Version: toolgateway.AgentCodeRegistryVersion, Action: "create", Path: targetPath,
		ExpectedSHA256: "missing", Content: "first\n"})
	proposed, err := executor.ExecuteAgentCode(ctx, scope, toolgateway.WorkspaceChangeTool,
		createPayload)
	if err != nil {
		t.Fatal(err)
	}
	createResult := decodeAgentCodeToolResult(t, proposed.JSON)
	if createResult.EditID == "" || createResult.Status != fileedit.StatusProposed ||
		createResult.OriginalSHA256 != "missing" {
		t.Fatalf("create proposal=%#v", createResult)
	}
	if targetPath != "note.txt" {
		if _, statErr := os.Stat(filepath.Join(workspaceRoot, "test")); !os.IsNotExist(statErr) {
			t.Fatalf("proposal created its missing parents: %v", statErr)
		}
	}
	if _, err := application.NewFileEditReviewService(state).Review(ctx,
		application.ReviewFileEditRequest{Version: application.FileEditReviewProtocolVersion,
			RunID: run.ID, EditID: createResult.EditID,
			Action: application.FileEditApproveIntent}); err != nil {
		t.Fatal(err)
	}
	applyScope := scope
	applyScope.InvocationID = "invocation-agent-code-2"
	applyScope.OperationKey = "agent-code-create-apply-operation-0001"
	applied, err := executor.ExecuteAgentCode(ctx, applyScope, toolgateway.WorkspaceApplyTool,
		mustAgentCodePayload(t, toolgateway.WorkspaceApplyPayload{
			Version: toolgateway.AgentCodeRegistryVersion, EditID: createResult.EditID,
			ExpectedAction: "create", ExpectedOriginalSHA256: createResult.OriginalSHA256,
			ExpectedProposedSHA256: createResult.ProposedSHA256}))
	if err != nil || !strings.Contains(applied.JSON, `"file_written":true`) {
		t.Fatalf("create apply=%s err=%v", applied.JSON, err)
	}
	written, err := os.ReadFile(filepath.Join(workspaceRoot, targetPath))
	if err != nil || string(written) != "first\n" {
		t.Fatalf("created file=%q err=%v", written, err)
	}

	readScope := scope
	readScope.InvocationID = "invocation-agent-code-3"
	readScope.OperationKey = "agent-code-read-operation-0001"
	readResult, err := executor.ExecuteAgentCode(ctx, readScope, toolgateway.WorkspaceReadTool,
		mustAgentCodePayload(t, toolgateway.WorkspaceReadPayload{
			Version: toolgateway.AgentCodeRegistryVersion, Path: targetPath,
			StartLine: 1, EndLine: 20}))
	if err != nil {
		t.Fatal(err)
	}
	var read workspace.AgentCodeRead
	if err := json.Unmarshal([]byte(readResult.JSON), &read); err != nil {
		t.Fatal(err)
	}
	patchScope := scope
	patchScope.InvocationID = "invocation-agent-code-4"
	patchScope.OperationKey = "agent-code-patch-operation-0001"
	patchResult, err := executor.ExecuteAgentCode(ctx, patchScope,
		toolgateway.WorkspaceChangeTool, mustAgentCodePayload(t,
			toolgateway.WorkspaceChangePayload{Version: toolgateway.AgentCodeRegistryVersion,
				Action: "propose_patch", Path: targetPath, ExpectedSHA256: read.ContentSHA256,
				Replacements: []toolgateway.WorkspaceReplacement{{OldText: "first",
					NewText: "second", ExpectedOccurrences: 1}}}))
	if err != nil {
		t.Fatal(err)
	}
	patch := decodeAgentCodeToolResult(t, patchResult.JSON)
	if _, err := application.NewFileEditReviewService(state).Review(ctx,
		application.ReviewFileEditRequest{Version: application.FileEditReviewProtocolVersion,
			RunID: run.ID, EditID: patch.EditID,
			Action: application.FileEditApproveIntent}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, targetPath), []byte("external\n"),
		0o600); err != nil {
		t.Fatal(err)
	}
	conflictScope := scope
	conflictScope.InvocationID = "invocation-agent-code-5"
	conflictScope.OperationKey = "agent-code-patch-apply-operation-0001"
	_, err = executor.ExecuteAgentCode(ctx, conflictScope, toolgateway.WorkspaceApplyTool,
		mustAgentCodePayload(t, toolgateway.WorkspaceApplyPayload{
			Version: toolgateway.AgentCodeRegistryVersion, EditID: patch.EditID,
			ExpectedAction: "propose_patch", ExpectedOriginalSHA256: patch.OriginalSHA256,
			ExpectedProposedSHA256: patch.ProposedSHA256}))
	if apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeConflict {
		t.Fatalf("CAS conflict code=%s err=%v", apperror.CodeOf(apperror.Normalize(err)), err)
	}
	written, _ = os.ReadFile(filepath.Join(workspaceRoot, targetPath))
	if string(written) != "external\n" {
		t.Fatalf("CAS conflict overwrote file: %q", written)
	}

	moveScope := scope
	moveScope.InvocationID = "invocation-agent-code-6"
	moveScope.OperationKey = "agent-code-move-operation-0001"
	moveProposal, err := executor.ExecuteAgentCode(ctx, moveScope,
		toolgateway.WorkspaceChangeTool, mustAgentCodePayload(t,
			toolgateway.WorkspaceChangePayload{Version: toolgateway.AgentCodeRegistryVersion,
				Action: "move", Path: targetPath, ExpectedSHA256: fileedit.HashText("external\n"),
				DestinationPath: "moved.txt", DestinationExpectedSHA256: "missing"}))
	if err != nil {
		t.Fatal(err)
	}
	move := decodeAgentCodeToolResult(t, moveProposal.JSON)
	if move.Status != fileedit.StatusProposed || move.ProposedSHA256 != "missing" {
		t.Fatalf("move proposal=%#v", move)
	}
	if _, err := application.NewFileEditReviewService(state).Review(ctx,
		application.ReviewFileEditRequest{Version: application.FileEditReviewProtocolVersion,
			RunID: run.ID, EditID: move.EditID,
			Action: application.FileEditApproveIntent}); err != nil {
		t.Fatal(err)
	}
	moveApplyScope := scope
	moveApplyScope.InvocationID = "invocation-agent-code-7"
	moveApplyScope.OperationKey = "agent-code-move-apply-operation-0001"
	if _, err := executor.ExecuteAgentCode(ctx, moveApplyScope,
		toolgateway.WorkspaceApplyTool, mustAgentCodePayload(t,
			toolgateway.WorkspaceApplyPayload{Version: toolgateway.AgentCodeRegistryVersion,
				EditID: move.EditID, ExpectedAction: "move",
				ExpectedOriginalSHA256: move.OriginalSHA256,
				ExpectedProposedSHA256: move.ProposedSHA256})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspaceRoot, targetPath)); !os.IsNotExist(err) {
		t.Fatalf("move source still exists: %v", err)
	}
	written, err = os.ReadFile(filepath.Join(workspaceRoot, "moved.txt"))
	if err != nil || string(written) != "external\n" {
		t.Fatalf("moved file=%q err=%v", written, err)
	}

	deleteScope := scope
	deleteScope.InvocationID = "invocation-agent-code-8"
	deleteScope.OperationKey = "agent-code-delete-operation-0001"
	deleteProposal, err := executor.ExecuteAgentCode(ctx, deleteScope,
		toolgateway.WorkspaceDeleteTool, mustAgentCodePayload(t,
			toolgateway.WorkspaceDeletePayload{Version: toolgateway.AgentCodeRegistryVersion,
				Action: "propose", Path: "moved.txt", ConfirmPath: "moved.txt",
				ExpectedSHA256: fileedit.HashText("external\n")}))
	if err != nil {
		t.Fatal(err)
	}
	deletion := decodeAgentCodeToolResult(t, deleteProposal.JSON)
	if deletion.Status != fileedit.StatusProposed || deletion.ProposedSHA256 != "missing" {
		t.Fatalf("delete proposal=%#v", deletion)
	}
	if _, err := application.NewFileEditReviewService(state).Review(ctx,
		application.ReviewFileEditRequest{Version: application.FileEditReviewProtocolVersion,
			RunID: run.ID, EditID: deletion.EditID,
			Action: application.FileEditApproveIntent}); err != nil {
		t.Fatal(err)
	}
	deleteApplyScope := scope
	deleteApplyScope.InvocationID = "invocation-agent-code-9"
	deleteApplyScope.OperationKey = "agent-code-delete-apply-operation-0001"
	if _, err := executor.ExecuteAgentCode(ctx, deleteApplyScope,
		toolgateway.WorkspaceDeleteTool, mustAgentCodePayload(t,
			toolgateway.WorkspaceDeletePayload{Version: toolgateway.AgentCodeRegistryVersion,
				Action: "apply", Path: "moved.txt", ConfirmPath: "moved.txt",
				ExpectedSHA256: deletion.OriginalSHA256, EditID: deletion.EditID})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspaceRoot, "moved.txt")); !os.IsNotExist(err) {
		t.Fatalf("delete target still exists: %v", err)
	}

	if _, err := application.NewRunService(state).Pause(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := state.ReleaseRunExecutionLease(ctx, leaseResult.Lease); err != nil {
		t.Fatal(err)
	}
	permissionResult, err := permissionService.Change(ctx,
		application.ChangeRunExecutionPermissionRequest{
			RunID: run.ID, Mode: string(domain.RunExecutionPermissionAsk),
			OperationKey: "agent-code-permission-drift-0001", RequestedBy: "test_operator",
			Reason: "verify permission revision fencing through a quiescent downgrade",
		})
	if err != nil {
		t.Fatal(err)
	}
	released, found, err := state.GetRunExecutionLease(ctx, run.ID)
	if err != nil || !found || released.Status != domain.RunExecutionLeaseReleased {
		t.Fatalf("quiescent permission drift setup did not release lease: lease=%+v found=%t err=%v",
			released, found, err)
	}
	if _, err := application.NewRunService(state).Resume(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	newLease, err := state.AcquireRunExecutionLease(ctx,
		domain.AcquireRunExecutionLeaseRequest{RunID: run.ID,
			OwnerID: "agent-code-test-after-drift", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	staleScope := scope
	staleScope.InvocationID = "invocation-agent-code-stale-permission"
	staleScope.OperationKey = "agent-code-stale-permission-0001"
	staleScope.LeaseID = newLease.Lease.LeaseID
	staleScope.LeaseGeneration = newLease.Lease.Generation
	_, err = executor.ExecuteAgentCode(ctx, staleScope, toolgateway.WorkspaceReadTool,
		mustAgentCodePayload(t, toolgateway.WorkspaceReadPayload{
			Version: toolgateway.AgentCodeRegistryVersion, Path: "missing.txt",
			StartLine: 1, EndLine: 1}))
	if apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeFailedPrecondition ||
		permissionResult.Permission.Revision == staleScope.PermissionRevision {
		t.Fatalf("old tool authority survived permission drift: revision=%d stale=%d err=%v",
			permissionResult.Permission.Revision, staleScope.PermissionRevision, err)
	}
}

type agentCodeToolResultFixture struct {
	EditID         string `json:"edit_id"`
	Operation      string `json:"operation"`
	Status         string `json:"status"`
	OriginalSHA256 string `json:"original_sha256"`
	ProposedSHA256 string `json:"proposed_sha256"`
}

func decodeAgentCodeToolResult(t *testing.T, value string) agentCodeToolResultFixture {
	t.Helper()
	var result agentCodeToolResultFixture
	if err := json.Unmarshal([]byte(value), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func mustAgentCodePayload(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
