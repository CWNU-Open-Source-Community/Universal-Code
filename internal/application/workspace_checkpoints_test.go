package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

func TestWorkspaceCheckpointBoundaryConcurrentCompletionReplays(t *testing.T) {
	fixture := newWorkspaceCheckpointApplicationFixture(t)
	defer fixture.state.Close()
	request := application.WorkspaceMutationBoundaryRequest{RunID: fixture.run.ID,
		Kind: workspacecheckpoint.TransactionCommandBatch, OperationKey: "concurrent-command-completion",
		TriggerReceiptID: "command-job-concurrent", LeaseID: fixture.lease.LeaseID, LeaseGeneration: fixture.lease.Generation}
	if _, err := fixture.service.BeginBoundary(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	mustWorkspaceCheckpointApplicationWrite(t, filepath.Join(fixture.root, "file.txt"), []byte("completed command\n"))
	const callers = 8
	results := make([]application.WorkspaceMutationBoundary, callers)
	errors := make([]error, callers)
	start := make(chan struct{})
	var group sync.WaitGroup
	for index := range callers {
		group.Go(func() {
			<-start
			results[index], errors[index] = fixture.service.CompleteBoundary(t.Context(), request, nil)
		})
	}
	close(start)
	group.Wait()
	after := ""
	for index := range callers {
		if errors[index] != nil || results[index].After == nil || results[index].Transaction.Status != workspacecheckpoint.TransactionCompleted {
			t.Fatalf("completion %d: %+v %v", index, results[index], errors[index])
		}
		if after == "" {
			after = results[index].After.ID
		} else if results[index].After.ID != after {
			t.Fatal("duplicate completion changed checkpoint identity")
		}
	}
}

func TestWorkspaceCheckpointServiceBoundaryUndoRedoAndTimeline(t *testing.T) {
	fixture := newWorkspaceCheckpointApplicationFixture(t)
	defer fixture.state.Close()
	ctx := context.Background()
	request := application.WorkspaceMutationBoundaryRequest{RunID: fixture.run.ID,
		Kind:         workspacecheckpoint.TransactionFileTool,
		OperationKey: "workspace-boundary-file-0001", TriggerReceiptID: "file-edit-receipt-1",
		AttemptID: "attempt-workspace-1", CapabilityGeneration: strings.Repeat("a", 64),
		LeaseID: fixture.lease.LeaseID, LeaseGeneration: fixture.lease.Generation}
	boundary, err := fixture.service.BeginBoundary(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if boundary.Before.AttemptID != request.AttemptID ||
		boundary.Before.CapabilityGeneration != request.CapabilityGeneration ||
		boundary.Transaction.Status != workspacecheckpoint.TransactionPrepared {
		t.Fatalf("unexpected prepared boundary: %+v", boundary)
	}
	if _, _, err = fixture.service.Capture(ctx,
		application.WorkspaceCheckpointCaptureRequest{RunID: fixture.run.ID,
			OperationKey: "manual-during-open-boundary", RequestedBy: "cli_operator"}); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("manual capture during open boundary error=%v", err)
	}
	mustWorkspaceCheckpointApplicationWrite(t, filepath.Join(fixture.root, "file.txt"),
		[]byte("after\r\n"))
	mustWorkspaceCheckpointApplicationWrite(t, filepath.Join(fixture.root, "binary.dat"),
		[]byte{0, 1, 2, 3})
	completed, err := fixture.service.CompleteBoundary(ctx, request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if completed.After == nil ||
		completed.Transaction.Status != workspacecheckpoint.TransactionCompleted {
		t.Fatalf("unexpected completed boundary: %+v", completed)
	}

	fixture.pause(t)
	timeline, err := fixture.service.Timeline(ctx, fixture.run.ID, 100)
	if err != nil || timeline.Current == nil ||
		timeline.Current.CurrentCheckpointID != completed.After.ID ||
		len(timeline.Checkpoints) < 2 || len(timeline.Transactions) != 1 {
		t.Fatalf("timeline=%+v err=%v", timeline, err)
	}
	preview, err := fixture.service.Undo(ctx, fixture.run.ID,
		timeline.Current.CurrentCheckpointID, "workspace-undo-preview", "desktop_operator", false)
	if err != nil || preview.Confirmed || len(preview.Preview.Conflicts) != 0 {
		t.Fatalf("undo preview=%+v err=%v", preview, err)
	}
	undo, err := fixture.service.Undo(ctx, fixture.run.ID,
		timeline.Current.CurrentCheckpointID, "workspace-undo-0001", "desktop_operator", true)
	if err != nil || undo.After == nil || !undo.Confirmed {
		t.Fatalf("undo=%+v err=%v", undo, err)
	}
	data, err := os.ReadFile(filepath.Join(fixture.root, "file.txt"))
	if err != nil || string(data) != "before\n" {
		t.Fatalf("undo file=%q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, "binary.dat")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("undo retained untracked binary: %v", err)
	}

	current, found, err := fixture.state.GetWorkspaceCheckpointRunState(ctx, fixture.run.ID)
	if err != nil || !found {
		t.Fatalf("undo cursor found=%t err=%v", found, err)
	}
	redo, err := fixture.service.Redo(ctx, fixture.run.ID, current.CurrentCheckpointID,
		"workspace-redo-0001", "desktop_operator", true)
	if err != nil || redo.After == nil {
		t.Fatalf("redo=%+v err=%v", redo, err)
	}
	data, err = os.ReadFile(filepath.Join(fixture.root, "file.txt"))
	if err != nil || string(data) != "after\r\n" {
		t.Fatalf("redo file=%q err=%v", data, err)
	}
	if binary, readErr := os.ReadFile(filepath.Join(fixture.root, "binary.dat")); readErr != nil || string(binary) != string([]byte{0, 1, 2, 3}) {
		t.Fatalf("redo binary=%v err=%v", binary, readErr)
	}
	replay, err := fixture.service.Redo(ctx, fixture.run.ID, current.CurrentCheckpointID,
		"workspace-redo-0001", "desktop_operator", true)
	if err != nil || !replay.Replayed || replay.Transaction == nil ||
		replay.Transaction.ID != redo.Transaction.ID {
		t.Fatalf("redo replay=%+v err=%v", replay, err)
	}
}

func TestWorkspaceCheckpointRestoreRequiresProcessGateForCurrentPreferences(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto} {
		t.Run(string(mode), func(t *testing.T) {
			fixture := newWorkspaceCheckpointApplicationFixture(t)
			defer fixture.state.Close()
			initial, _, err := fixture.service.Capture(t.Context(),
				application.WorkspaceCheckpointCaptureRequest{RunID: fixture.run.ID,
					OperationKey: "gate-initial", RequestedBy: "cli_operator"})
			if err != nil {
				t.Fatal(err)
			}
			mustWorkspaceCheckpointApplicationWrite(t, filepath.Join(fixture.root, "file.txt"), []byte("changed\n"))
			current, _, err := fixture.service.Capture(t.Context(),
				application.WorkspaceCheckpointCaptureRequest{RunID: fixture.run.ID,
					OperationKey: "gate-current", RequestedBy: "cli_operator"})
			if err != nil {
				t.Fatal(err)
			}
			fixture.pause(t)
			if mode != domain.RunExecutionPermissionAsk {
				if _, err := application.NewRunExecutionPermissionService(fixture.state,
					domain.ExecutionPermissionRuntimeCapabilities{}).Change(t.Context(),
					application.ChangeRunExecutionPermissionRequest{RunID: fixture.run.ID,
						Mode: string(mode), OperationKey: "checkpoint-gate-preference-0001", RequestedBy: "cli_operator"}); err != nil {
					t.Fatal(err)
				}
			}
			closed, err := application.NewWorkspaceCheckpointService(fixture.state,
				domain.ExecutionPermissionRuntimeCapabilities{})
			if err != nil {
				t.Fatal(err)
			}
			before, err := closed.Timeline(t.Context(), fixture.run.ID, 100)
			if err != nil {
				t.Fatal(err)
			}
			request := application.WorkspaceRestoreRequest{RunID: fixture.run.ID,
				TargetCheckpointID: initial.ID, ExpectedCurrentCheckpointID: current.ID,
				OperationKey: "gate-restore", RequestedBy: "cli_operator",
				Kind: workspacecheckpoint.TransactionRewind, TriggerReceiptID: "gate-restore-trigger"}
			preview, err := closed.Restore(t.Context(), request)
			if err != nil || preview.Confirmed || preview.Transaction != nil {
				t.Fatalf("closed-gate preview=%+v err=%v", preview, err)
			}
			request.Confirm = true
			denied, err := closed.Restore(t.Context(), request)
			if apperror.CodeOf(err) != apperror.CodePolicyDenied || denied.Transaction != nil || denied.After != nil {
				t.Fatalf("closed-gate restore=%+v err=%v", denied, err)
			}
			after, err := closed.Timeline(t.Context(), fixture.run.ID, 100)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("closed gate changed checkpoint ledger: before=%+v after=%+v err=%v", before, after, err)
			}
			data, err := os.ReadFile(filepath.Join(fixture.root, "file.txt"))
			if err != nil || string(data) != "changed\n" {
				t.Fatalf("closed gate changed workspace: %q err=%v", data, err)
			}
			restored, err := fixture.service.Restore(t.Context(), request)
			if err != nil || restored.After == nil || !restored.Confirmed {
				t.Fatalf("enabled restore=%+v err=%v", restored, err)
			}
			data, err = os.ReadFile(filepath.Join(fixture.root, "file.txt"))
			if err != nil || string(data) != "before\n" {
				t.Fatalf("enabled restore file=%q err=%v", data, err)
			}
			replay, err := closed.Restore(t.Context(), request)
			if err != nil || !replay.Replayed || replay.Transaction == nil ||
				replay.Transaction.ID != restored.Transaction.ID {
				t.Fatalf("closed-gate terminal replay=%+v err=%v", replay, err)
			}
		})
	}
}

func TestWorkspaceCheckpointServiceRestoreFailsClosedOnExternalChange(t *testing.T) {
	fixture := newWorkspaceCheckpointApplicationFixture(t)
	defer fixture.state.Close()
	request := application.WorkspaceMutationBoundaryRequest{RunID: fixture.run.ID,
		Kind:         workspacecheckpoint.TransactionCommandBatch,
		OperationKey: "workspace-command-boundary-0001", TriggerReceiptID: "command-receipt-1",
		AttemptID: "attempt-command-1", CapabilityGeneration: strings.Repeat("b", 64),
		LeaseID: fixture.lease.LeaseID, LeaseGeneration: fixture.lease.Generation}
	before, err := fixture.service.BeginBoundary(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	mustWorkspaceCheckpointApplicationWrite(t, filepath.Join(fixture.root, "file.txt"),
		[]byte("command change\n"))
	after, err := fixture.service.CompleteBoundary(t.Context(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	fixture.pause(t)
	mustWorkspaceCheckpointApplicationWrite(t, filepath.Join(fixture.root, "file.txt"),
		[]byte("external change\n"))
	result, err := fixture.service.Restore(t.Context(), application.WorkspaceRestoreRequest{
		RunID: fixture.run.ID, TargetCheckpointID: before.Before.ID,
		ExpectedCurrentCheckpointID: after.After.ID,
		OperationKey:                "workspace-rewind-conflict-0001", RequestedBy: "desktop_operator",
		Kind:             workspacecheckpoint.TransactionRewind,
		TriggerReceiptID: "operator-rewind-1", Confirm: true})
	var conflict *workspacecheckpoint.ConflictError
	if !errors.As(err, &conflict) || result.Transaction == nil ||
		result.Transaction.Status != workspacecheckpoint.TransactionFailed ||
		len(result.Preview.Conflicts) == 0 {
		t.Fatalf("conflicting restore=%+v err=%v", result, err)
	}
	data, readErr := os.ReadFile(filepath.Join(fixture.root, "file.txt"))
	if readErr != nil || string(data) != "external change\n" {
		t.Fatalf("external file was overwritten: %q err=%v", data, readErr)
	}
}

func TestWorkspaceCheckpointServiceReconcilesInterruptedMutation(t *testing.T) {
	fixture := newWorkspaceCheckpointApplicationFixture(t)
	defer fixture.state.Close()
	initial, _, err := fixture.service.Capture(t.Context(),
		application.WorkspaceCheckpointCaptureRequest{RunID: fixture.run.ID,
			OperationKey: "workspace-reconciliation-initial-0001",
			RequestedBy:  "cli_operator"})
	if err != nil {
		t.Fatal(err)
	}
	request := application.WorkspaceMutationBoundaryRequest{RunID: fixture.run.ID,
		Kind:             workspacecheckpoint.TransactionAgentMerge,
		OperationKey:     "workspace-agent-merge-boundary-0001",
		TriggerReceiptID: "agent-merge-receipt-1", AttemptID: "attempt-agent-merge-1",
		CapabilityGeneration: strings.Repeat("c", 64), LeaseID: fixture.lease.LeaseID,
		LeaseGeneration: fixture.lease.Generation}
	boundary, err := fixture.service.BeginBoundary(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = fixture.state.AdvanceWorkspaceCheckpointRunState(t.Context(),
		workspacecheckpoint.RunState{RunID: fixture.run.ID,
			WorkspaceID: initial.WorkspaceID, CurrentCheckpointID: initial.ID,
			UpdatedAt: time.Now().UTC()}, boundary.Before.ID); err != nil {
		t.Fatal(err)
	}
	mustWorkspaceCheckpointApplicationWrite(t, filepath.Join(fixture.root, "partial.txt"),
		[]byte("partial merge\n"))
	reconciled, err := fixture.service.Reconcile(t.Context())
	if reconciled != 0 || apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("live-lease reconciliation count=%d err=%v", reconciled, err)
	}
	if _, _, err = fixture.state.ReleaseRunExecutionLease(t.Context(), fixture.lease); err != nil {
		t.Fatal(err)
	}
	reconciled, err = fixture.service.Reconcile(t.Context())
	if err != nil || reconciled != 1 {
		t.Fatalf("reconciled=%d err=%v", reconciled, err)
	}
	transaction, found, err := fixture.state.GetWorkspaceCheckpointTransaction(t.Context(),
		boundary.Transaction.ID)
	if err != nil || !found || transaction.Status != workspacecheckpoint.TransactionInterrupted ||
		transaction.AfterCheckpointID == "" || transaction.ErrorCode != "process_restart_reconciliation" {
		t.Fatalf("reconciled transaction=%+v found=%t err=%v", transaction, found, err)
	}
	after, err := fixture.state.GetWorkspaceCheckpointSnapshot(t.Context(),
		transaction.AfterCheckpointID)
	if err != nil || after.Checkpoint.RecoveryLevel != workspacecheckpoint.RecoveryPartial ||
		len(after.Checkpoint.IncompleteReasons) == 0 {
		t.Fatalf("reconciled checkpoint=%+v err=%v", after.Checkpoint, err)
	}
}

func TestWorkspaceCheckpointServiceReconcilesTerminalCursorCommitWindow(t *testing.T) {
	fixture := newWorkspaceCheckpointApplicationFixture(t)
	defer fixture.state.Close()
	request := application.WorkspaceMutationBoundaryRequest{RunID: fixture.run.ID,
		Kind:             workspacecheckpoint.TransactionFileTool,
		OperationKey:     "workspace-terminal-cursor-window-0001",
		TriggerReceiptID: "terminal-cursor-edit-1", AttemptID: "attempt-terminal-cursor-1",
		CapabilityGeneration: strings.Repeat("2", 64), LeaseID: fixture.lease.LeaseID,
		LeaseGeneration: fixture.lease.Generation}
	boundary, err := fixture.service.BeginBoundary(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	mustWorkspaceCheckpointApplicationWrite(t, filepath.Join(fixture.root, "file.txt"),
		[]byte("terminal cursor state\n"))
	completed, err := fixture.service.CompleteBoundary(t.Context(), request, nil)
	if err != nil || completed.After == nil {
		t.Fatalf("completed boundary=%+v err=%v", completed, err)
	}
	if _, _, err = fixture.state.AdvanceWorkspaceCheckpointRunState(t.Context(),
		workspacecheckpoint.RunState{RunID: fixture.run.ID,
			WorkspaceID:         boundary.Before.WorkspaceID,
			CurrentCheckpointID: boundary.Before.ID, LastTransactionID: "",
			UpdatedAt: time.Now().UTC()}, completed.After.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = fixture.state.ReleaseRunExecutionLease(t.Context(), fixture.lease); err != nil {
		t.Fatal(err)
	}
	reconciled, err := fixture.service.Reconcile(t.Context())
	if err != nil || reconciled != 1 {
		t.Fatalf("terminal cursor reconciliation count=%d err=%v", reconciled, err)
	}
	state, found, err := fixture.state.GetWorkspaceCheckpointRunState(t.Context(),
		fixture.run.ID)
	if err != nil || !found || state.CurrentCheckpointID != completed.After.ID ||
		state.LastTransactionID != completed.Transaction.ID {
		t.Fatalf("terminal cursor state=%+v found=%t err=%v", state, found, err)
	}
}

func TestWorkspaceCheckpointServiceForkCreatesIndependentWorktreeAndRun(t *testing.T) {
	fixture := newWorkspaceCheckpointApplicationFixture(t)
	defer fixture.state.Close()
	request := application.WorkspaceMutationBoundaryRequest{RunID: fixture.run.ID,
		Kind:         workspacecheckpoint.TransactionFileTool,
		OperationKey: "workspace-fork-source-change-0001", TriggerReceiptID: "fork-edit-1",
		AttemptID: "attempt-fork-1", CapabilityGeneration: strings.Repeat("d", 64),
		LeaseID: fixture.lease.LeaseID, LeaseGeneration: fixture.lease.Generation}
	if _, err := fixture.service.BeginBoundary(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	mustWorkspaceCheckpointApplicationWrite(t, filepath.Join(fixture.root, "file.txt"),
		[]byte("forked content\n"))
	mustWorkspaceCheckpointApplicationWrite(t, filepath.Join(fixture.root, "fork-only.bin"),
		[]byte{0, 9, 8, 7})
	boundary, err := fixture.service.CompleteBoundary(t.Context(), request, nil)
	if err != nil || boundary.After == nil {
		t.Fatalf("fork source boundary=%+v err=%v", boundary, err)
	}
	fixture.pause(t)
	forkRequest := application.WorkspaceForkRequest{RunID: fixture.run.ID,
		TargetCheckpointID:          boundary.After.ID,
		ExpectedCurrentCheckpointID: boundary.After.ID,
		OperationKey:                "workspace-fork-operation-0001", RequestedBy: "desktop_operator",
		WorkspaceName: "workspace-checkpoint-fork",
		Branch:        "checkpoint/fork-test", Goal: "continue independently from checkpoint",
		Confirm: true}
	unconfirmed := forkRequest
	unconfirmed.Confirm = false
	if _, err := fixture.service.Fork(t.Context(), unconfirmed); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("unconfirmed application fork error=%v", err)
	}
	result, err := fixture.service.Fork(t.Context(), forkRequest)
	if err != nil {
		t.Fatal(err)
	}
	destination := result.Workspace.RootPath
	defer func() {
		if err := repository.RemoveCreatedWorktree(context.Background(), fixture.root,
			destination, forkRequest.Branch); err != nil {
			t.Errorf("cleanup fork worktree: %v", err)
		}
	}()
	if result.Run.ID == fixture.run.ID || result.Workspace.ID == "workspace-checkpoint" ||
		filepath.Dir(destination) != filepath.Dir(fixture.root) ||
		!strings.HasPrefix(filepath.Base(destination), "prayu-fork-") ||
		result.Run.Status != domain.RunCreated || result.Checkpoint.RunID != result.Run.ID ||
		result.Checkpoint.ManifestSHA256 != boundary.After.ManifestSHA256 ||
		result.Transaction.Status != workspacecheckpoint.TransactionCompleted ||
		result.Replayed || len(result.NotInherited) == 0 {
		t.Fatalf("unexpected fork result: %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(destination, "file.txt"))
	if err != nil || string(data) != "forked content\n" {
		t.Fatalf("forked text=%q err=%v", data, err)
	}
	binary, err := os.ReadFile(filepath.Join(destination, "fork-only.bin"))
	if err != nil || string(binary) != string([]byte{0, 9, 8, 7}) {
		t.Fatalf("forked binary=%v err=%v", binary, err)
	}
	branchOutput := exec.Command("git", "-C", destination, "branch", "--show-current")
	branch, err := branchOutput.Output()
	if err != nil || strings.TrimSpace(string(branch)) != forkRequest.Branch {
		t.Fatalf("fork branch=%q err=%v", branch, err)
	}
	replayed, err := fixture.service.Fork(t.Context(), forkRequest)
	if err != nil || !replayed.Replayed || replayed.Run.ID != result.Run.ID ||
		replayed.Checkpoint.ID != result.Checkpoint.ID {
		t.Fatalf("fork replay=%+v err=%v", replayed, err)
	}
}

func TestWorkspaceCheckpointServiceForkReplaysAfterRegistrationCaptureFailure(t *testing.T) {
	fixture := newWorkspaceCheckpointApplicationFixture(t)
	defer fixture.state.Close()
	request := application.WorkspaceMutationBoundaryRequest{RunID: fixture.run.ID,
		Kind:             workspacecheckpoint.TransactionFileTool,
		OperationKey:     "workspace-fork-recovery-source-0001",
		TriggerReceiptID: "fork-recovery-edit-1", AttemptID: "attempt-fork-recovery-1",
		CapabilityGeneration: strings.Repeat("e", 64), LeaseID: fixture.lease.LeaseID,
		LeaseGeneration: fixture.lease.Generation}
	if _, err := fixture.service.BeginBoundary(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	mustWorkspaceCheckpointApplicationWrite(t, filepath.Join(fixture.root, "file.txt"),
		[]byte("recoverable fork content\n"))
	boundary, err := fixture.service.CompleteBoundary(t.Context(), request, nil)
	if err != nil || boundary.After == nil {
		t.Fatalf("fork recovery source=%+v err=%v", boundary, err)
	}
	fixture.pause(t)
	destination := filepath.Join(t.TempDir(), "fork-recovery-worktree")
	forkRequest := application.WorkspaceForkRequest{RunID: fixture.run.ID,
		TargetCheckpointID: boundary.After.ID, ExpectedCurrentCheckpointID: boundary.After.ID,
		OperationKey: "workspace-fork-recovery-0001", RequestedBy: "desktop_operator",
		WorkspaceName: "workspace-checkpoint-fork-recovery", WorkspaceRoot: destination,
		Branch: "checkpoint/fork-recovery", Goal: "resume a partially registered fork",
		Confirm: true}
	failingStore := &failOnceForkCheckpointStore{SQLiteStore: fixture.state,
		sourceRunID: fixture.run.ID}
	service, err := application.NewWorkspaceCheckpointService(failingStore,
		domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Fork(t.Context(), forkRequest); err == nil || !failingStore.failed {
		t.Fatalf("fork capture fault was not observed: failed=%t err=%v",
			failingStore.failed, err)
	}
	open, err := fixture.state.ListOpenWorkspaceCheckpointTransactions(t.Context(), 10)
	if err != nil || len(open) != 1 || open[0].Kind != workspacecheckpoint.TransactionFork {
		t.Fatalf("replayable fork transaction=%+v err=%v", open, err)
	}
	restarted, err := application.NewWorkspaceCheckpointService(fixture.state,
		domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if reconciled, reconcileErr := restarted.Reconcile(t.Context()); reconcileErr != nil ||
		reconciled != 1 {
		t.Fatalf("fork reconciliation count=%d err=%v", reconciled, reconcileErr)
	}
	result, err := restarted.Fork(t.Context(), forkRequest)
	if err != nil || result.Run.ID == "" || result.Checkpoint.RunID != result.Run.ID ||
		result.Transaction.Status != workspacecheckpoint.TransactionCompleted || !result.Replayed {
		t.Fatalf("resumed fork=%+v err=%v", result, err)
	}
	defer func() {
		if err := repository.RemoveCreatedWorktree(context.Background(), fixture.root,
			destination, forkRequest.Branch); err != nil {
			t.Errorf("cleanup recovered fork worktree: %v", err)
		}
	}()
	data, err := os.ReadFile(filepath.Join(destination, "file.txt"))
	if err != nil || string(data) != "recoverable fork content\n" {
		t.Fatalf("recovered fork text=%q err=%v", data, err)
	}
}

func TestWorkspaceCheckpointServiceReconcilesForkBeforeRunRegistration(t *testing.T) {
	fixture := newWorkspaceCheckpointApplicationFixture(t)
	defer fixture.state.Close()
	request := application.WorkspaceMutationBoundaryRequest{RunID: fixture.run.ID,
		Kind:             workspacecheckpoint.TransactionFileTool,
		OperationKey:     "workspace-fork-crash-source-0001",
		TriggerReceiptID: "fork-crash-source-edit-1", AttemptID: "attempt-fork-crash-1",
		CapabilityGeneration: strings.Repeat("f", 64), LeaseID: fixture.lease.LeaseID,
		LeaseGeneration: fixture.lease.Generation}
	if _, err := fixture.service.BeginBoundary(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	mustWorkspaceCheckpointApplicationWrite(t, filepath.Join(fixture.root, "file.txt"),
		[]byte("fork crash recovery content\n"))
	boundary, err := fixture.service.CompleteBoundary(t.Context(), request, nil)
	if err != nil || boundary.After == nil {
		t.Fatalf("fork crash source=%+v err=%v", boundary, err)
	}
	fixture.pause(t)

	operationKey := "workspace-fork-crash-operation-0001"
	digest := runmutation.OperationKeyDigest("workspace_fork_operation.v1",
		fixture.run.ID, operationKey)
	destination := filepath.Join(t.TempDir(), "fork-crash-worktree")
	destination, err = repository.NormalizeWorktreeDestination(fixture.root, destination)
	if err != nil {
		t.Fatal(err)
	}
	branch := "checkpoint/fork-crash-recovery"
	if _, err = repository.CreateWorktree(t.Context(), fixture.root, destination, branch,
		boundary.After.BaseCommit); err != nil {
		t.Fatal(err)
	}
	mustWorkspaceCheckpointApplicationWrite(t, filepath.Join(destination, "file.txt"),
		[]byte("fork crash recovery content\n"))
	copyWorkspaceCheckpointApplicationIndex(t, fixture.root, destination)
	t.Cleanup(func() {
		_ = repository.CleanupInterruptedWorktree(context.Background(), fixture.root,
			destination, branch, boundary.After.BaseCommit)
	})
	wrongCommit := strings.Repeat("0", len(boundary.After.BaseCommit))
	if err = repository.CleanupInterruptedWorktree(t.Context(), fixture.root, destination,
		branch, wrongCommit); err == nil {
		t.Fatal("fork cleanup accepted a drifted commit identity")
	}
	if _, err = os.Stat(destination); err != nil {
		t.Fatalf("failed-closed fork cleanup removed the worktree: %v", err)
	}
	now := time.Now().UTC()
	transaction := workspacecheckpoint.Transaction{
		ID:              "workspace-fork-crash-" + digest[:20],
		ProtocolVersion: workspacecheckpoint.ProtocolVersion, OperationKeyDigest: digest,
		RequestFingerprint: runmutation.Fingerprint("workspace-fork-crash-test.v1", digest),
		RunID:              fixture.run.ID, WorkspaceID: boundary.After.WorkspaceID,
		Kind:                        workspacecheckpoint.TransactionFork,
		TriggerReceiptID:            "missing-fork-run-" + digest[:20],
		BeforeCheckpointID:          boundary.After.ID,
		ExpectedCurrentCheckpointID: boundary.After.ID,
		TargetCheckpointID:          boundary.After.ID, ForkWorkspaceRoot: destination,
		ForkBranch: branch, Status: workspacecheckpoint.TransactionPrepared,
		RecoveryLevel: boundary.After.RecoveryLevel, ConflictJSON: "[]",
		CreatedAt: now, UpdatedAt: now,
	}
	transaction, replayed, err := fixture.state.CreateWorkspaceCheckpointTransaction(
		t.Context(), transaction)
	if err != nil || replayed {
		t.Fatalf("prepare interrupted fork transaction replayed=%t err=%v", replayed, err)
	}
	encoded, err := json.Marshal(transaction)
	if err != nil || strings.Contains(string(encoded), destination) ||
		strings.Contains(string(encoded), branch) ||
		strings.Contains(string(encoded), "fork_workspace_root") {
		t.Fatalf("private fork recovery metadata leaked: %s err=%v", encoded, err)
	}

	reconciled, err := fixture.service.Reconcile(t.Context())
	if err != nil || reconciled != 1 {
		t.Fatalf("fork crash reconciliation count=%d err=%v", reconciled, err)
	}
	if _, err = os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted fork worktree was not removed: %v", err)
	}
	if err = exec.Command("git", "-C", fixture.root, "show-ref", "--verify",
		"refs/heads/"+branch).Run(); err == nil {
		t.Fatal("interrupted fork branch was not removed")
	}
	stored, found, err := fixture.state.GetWorkspaceCheckpointTransaction(t.Context(),
		transaction.ID)
	if err != nil || !found || stored.Status != workspacecheckpoint.TransactionInterrupted ||
		stored.ErrorCode != "process_restart_reconciliation" ||
		stored.ForkWorkspaceRoot != destination || stored.ForkBranch != branch {
		t.Fatalf("reconciled fork transaction=%+v found=%t err=%v", stored, found, err)
	}
}

func TestWorkspaceCheckpointServiceForkReconciliationPreservesExternalDrift(t *testing.T) {
	fixture := newWorkspaceCheckpointApplicationFixture(t)
	defer fixture.state.Close()
	request := application.WorkspaceMutationBoundaryRequest{RunID: fixture.run.ID,
		Kind:             workspacecheckpoint.TransactionFileTool,
		OperationKey:     "workspace-fork-drift-source-0001",
		TriggerReceiptID: "fork-drift-source-edit-1", AttemptID: "attempt-fork-drift-1",
		CapabilityGeneration: strings.Repeat("1", 64), LeaseID: fixture.lease.LeaseID,
		LeaseGeneration: fixture.lease.Generation}
	if _, err := fixture.service.BeginBoundary(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	mustWorkspaceCheckpointApplicationWrite(t, filepath.Join(fixture.root, "file.txt"),
		[]byte("fork drift target\n"))
	boundary, err := fixture.service.CompleteBoundary(t.Context(), request, nil)
	if err != nil || boundary.After == nil {
		t.Fatalf("fork drift source=%+v err=%v", boundary, err)
	}
	fixture.pause(t)
	operationKey := "workspace-fork-drift-operation-0001"
	digest := runmutation.OperationKeyDigest("workspace_fork_operation.v1",
		fixture.run.ID, operationKey)
	destination := filepath.Join(t.TempDir(), "fork-drift-worktree")
	destination, err = repository.NormalizeWorktreeDestination(fixture.root, destination)
	if err != nil {
		t.Fatal(err)
	}
	branch := "checkpoint/fork-drift-recovery"
	if _, err = repository.CreateWorktree(t.Context(), fixture.root, destination, branch,
		boundary.After.BaseCommit); err != nil {
		t.Fatal(err)
	}
	mustWorkspaceCheckpointApplicationWrite(t, filepath.Join(destination, "file.txt"),
		[]byte("fork drift target\n"))
	copyWorkspaceCheckpointApplicationIndex(t, fixture.root, destination)
	t.Cleanup(func() {
		_ = os.Remove(filepath.Join(destination, "external.txt"))
		_ = repository.CleanupInterruptedWorktree(context.Background(), fixture.root,
			destination, branch, boundary.After.BaseCommit)
	})
	now := time.Now().UTC()
	transaction := workspacecheckpoint.Transaction{
		ID:              "workspace-fork-drift-" + digest[:20],
		ProtocolVersion: workspacecheckpoint.ProtocolVersion, OperationKeyDigest: digest,
		RequestFingerprint: runmutation.Fingerprint("workspace-fork-drift-test.v1", digest),
		RunID:              fixture.run.ID, WorkspaceID: boundary.After.WorkspaceID,
		Kind:                        workspacecheckpoint.TransactionFork,
		TriggerReceiptID:            "missing-drift-run-" + digest[:20],
		BeforeCheckpointID:          boundary.After.ID,
		ExpectedCurrentCheckpointID: boundary.After.ID,
		TargetCheckpointID:          boundary.After.ID, ForkWorkspaceRoot: destination,
		ForkBranch: branch, Status: workspacecheckpoint.TransactionPrepared,
		RecoveryLevel: boundary.After.RecoveryLevel, ConflictJSON: "[]",
		CreatedAt: now, UpdatedAt: now,
	}
	transaction, _, err = fixture.state.CreateWorkspaceCheckpointTransaction(t.Context(),
		transaction)
	if err != nil {
		t.Fatal(err)
	}
	mustWorkspaceCheckpointApplicationWrite(t, filepath.Join(destination, "external.txt"),
		[]byte("do not delete me\n"))
	reconciled, err := fixture.service.Reconcile(t.Context())
	if reconciled != 0 || err == nil {
		t.Fatalf("drifted fork reconciliation count=%d err=%v", reconciled, err)
	}
	if data, readErr := os.ReadFile(filepath.Join(destination, "external.txt")); readErr != nil || string(data) != "do not delete me\n" {
		t.Fatalf("external fork content changed: %q err=%v", data, readErr)
	}
	stored, found, err := fixture.state.GetWorkspaceCheckpointTransaction(t.Context(),
		transaction.ID)
	if err != nil || !found || stored.Status != workspacecheckpoint.TransactionPrepared {
		t.Fatalf("drifted fork transaction=%+v found=%t err=%v", stored, found, err)
	}
	if err = os.Remove(filepath.Join(destination, "external.txt")); err != nil {
		t.Fatal(err)
	}
	reconciled, err = fixture.service.Reconcile(t.Context())
	if err != nil || reconciled != 1 {
		t.Fatalf("fork drift retry reconciliation count=%d err=%v", reconciled, err)
	}
	if _, err = os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("verified fork worktree was not removed: %v", err)
	}
}

func TestWorkspaceCheckpointServiceFailsClosedAtOpenTransactionScanLimit(t *testing.T) {
	fixture := newWorkspaceCheckpointApplicationFixture(t)
	defer fixture.state.Close()
	service, err := application.NewWorkspaceCheckpointService(
		&saturatedWorkspaceCheckpointStore{SQLiteStore: fixture.state},
		domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = service.Capture(t.Context(), application.WorkspaceCheckpointCaptureRequest{
		RunID: fixture.run.ID, OperationKey: "checkpoint-backlog-limit-0001",
		RequestedBy: "cli_operator"})
	if apperror.CodeOf(err) != apperror.CodeResourceExhausted {
		t.Fatalf("open transaction scan limit error=%v", err)
	}
}

type saturatedWorkspaceCheckpointStore struct{ *store.SQLiteStore }

func (s *saturatedWorkspaceCheckpointStore) ListOpenWorkspaceCheckpointTransactions(
	context.Context, int,
) ([]workspacecheckpoint.Transaction, error) {
	return make([]workspacecheckpoint.Transaction, 2_000), nil
}

type failOnceForkCheckpointStore struct {
	*store.SQLiteStore
	sourceRunID string
	failed      bool
}

func (s *failOnceForkCheckpointStore) CreateWorkspaceCheckpoint(ctx context.Context,
	snapshot workspacecheckpoint.Snapshot,
) (workspacecheckpoint.Checkpoint, bool, error) {
	if !s.failed && snapshot.Checkpoint.Trigger == workspacecheckpoint.TriggerFork &&
		snapshot.Checkpoint.RunID != s.sourceRunID {
		s.failed = true
		return workspacecheckpoint.Checkpoint{}, false, errors.New(
			"injected fork checkpoint persistence failure")
	}
	return s.SQLiteStore.CreateWorkspaceCheckpoint(ctx, snapshot)
}

type workspaceCheckpointApplicationFixture struct {
	state   *store.SQLiteStore
	service *application.WorkspaceCheckpointService
	run     domain.Run
	lease   domain.RunExecutionLease
	root    string
}

func newWorkspaceCheckpointApplicationFixture(t *testing.T) workspaceCheckpointApplicationFixture {
	t.Helper()
	ctx := context.Background()
	root := newWorkspaceCheckpointApplicationRepository(t)
	state, err := store.Open(filepath.Join(t.TempDir(), "workspace-checkpoint-application.db"))
	if err != nil {
		t.Fatal(err)
	}
	workspace := store.WorkspaceRecord{ID: "workspace-checkpoint-application",
		Name: "workspace-checkpoint-application", RootPath: root}
	if err := state.SaveWorkspace(ctx, workspace); err != nil {
		state.Close()
		t.Fatal(err)
	}
	runs := application.NewRunService(state)
	_, created, err := runs.Create(ctx, application.CreateRunRequest{
		Goal: "test workspace restore", Profile: "code", Surface: "code", Phase: "deliver",
		WorkspaceID: workspace.ID,
		Budget:      domain.Budget{MaxTurns: 8, MaxTokens: 2000, MaxToolCalls: 16}})
	if err != nil {
		state.Close()
		t.Fatal(err)
	}
	capabilities := domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true}
	// A new Run starts in Ask; restore and fork require their own exact operator
	// confirmation after pausing and releasing the execution lease.
	permission, err := state.GetRunExecutionPermission(ctx, created.ID)
	if err != nil || permission.Mode != domain.RunExecutionPermissionAsk {
		state.Close()
		t.Fatalf("initial checkpoint permission=%+v err=%v", permission, err)
	}
	runRecord, err := runs.Start(ctx, created.ID)
	if err != nil {
		state.Close()
		t.Fatal(err)
	}
	leaseResult, err := state.AcquireRunExecutionLease(ctx,
		domain.AcquireRunExecutionLeaseRequest{RunID: runRecord.ID,
			OwnerID: "workspace-checkpoint-test", TTL: time.Minute})
	if err != nil {
		state.Close()
		t.Fatal(err)
	}
	service, err := application.NewWorkspaceCheckpointService(state, capabilities)
	if err != nil {
		state.Close()
		t.Fatal(err)
	}
	return workspaceCheckpointApplicationFixture{state: state, service: service,
		run: runRecord, lease: leaseResult.Lease, root: root}
}

func (f *workspaceCheckpointApplicationFixture) pause(t *testing.T) {
	t.Helper()
	if _, _, err := f.state.ReleaseRunExecutionLease(t.Context(), f.lease); err != nil {
		t.Fatal(err)
	}
	runRecord, err := application.NewRunService(f.state).Pause(t.Context(), f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.run = runRecord
}

func newWorkspaceCheckpointApplicationRepository(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	runWorkspaceCheckpointApplicationGit(t, root, "init", "-q")
	runWorkspaceCheckpointApplicationGit(t, root, "config", "user.email",
		"checkpoint@example.invalid")
	runWorkspaceCheckpointApplicationGit(t, root, "config", "user.name", "Checkpoint Test")
	mustWorkspaceCheckpointApplicationWrite(t, filepath.Join(root, "file.txt"), []byte("before\n"))
	runWorkspaceCheckpointApplicationGit(t, root, "add", ".")
	runWorkspaceCheckpointApplicationGit(t, root, "commit", "-m", "baseline")
	return root
}

func runWorkspaceCheckpointApplicationGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func mustWorkspaceCheckpointApplicationWrite(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func copyWorkspaceCheckpointApplicationIndex(t *testing.T, sourceRoot, targetRoot string) {
	t.Helper()
	indexPath := func(root string) string {
		command := exec.Command("git", "-C", root, "rev-parse", "--git-path", "index")
		command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("resolve Git index for %s: %v: %s", root, err, output)
		}
		value := strings.TrimSpace(string(output))
		if !filepath.IsAbs(value) {
			value = filepath.Join(root, value)
		}
		return filepath.Clean(value)
	}
	content, err := os.ReadFile(indexPath(sourceRoot))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath(targetRoot), content, 0o644); err != nil {
		t.Fatal(err)
	}
}
