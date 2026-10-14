package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/gitadvanced"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/store"
)

type gitAdvancedLostReceiptStore struct {
	*store.SQLiteStore
	lost bool
}

func (s *gitAdvancedLostReceiptStore) CompleteGitAdvancedOperation(ctx context.Context,
	id string, receipt gitadvanced.Receipt, now time.Time,
) (gitadvanced.OperationRecord, bool, error) {
	if receipt.Operation == gitadvanced.WorktreeRemove && !s.lost {
		s.lost = true
		return gitadvanced.OperationRecord{}, false, errors.New("injected worktree removal receipt write failure")
	}
	return s.SQLiteStore.CompleteGitAdvancedOperation(ctx, id, receipt, now)
}

func TestGitAdvancedStartupRecoversPersistedWorktreeRemoval(t *testing.T) {
	fixture := newGitAdvancedApplicationFixture(t)
	ctx := t.Context()
	head := fixtureGit(t, "-C", fixture.root, "rev-parse", "HEAD")
	created := executeGitAdvancedFixture(t, fixture, "remove-recovery-create",
		gitadvanced.Spec{ProtocolVersion: gitadvanced.ProtocolVersion,
			Operation: gitadvanced.WorktreeCreate, WorktreeName: "removed-recovery",
			Branch: "codex/removed-recovery", Commit: head})
	if created.Worktree == nil {
		t.Fatal("created worktree was not persisted")
	}
	fixture.service.store = &gitAdvancedLostReceiptStore{SQLiteStore: fixture.state}
	review, err := fixture.service.Review(ctx, GitAdvancedReviewRequest{
		ProtocolVersion: GitAdvancedAPIProtocolVersion, RunID: fixture.run.ID,
		OperationKey: "remove-recovery-delete", RequestedBy: "test_operator", Scope: fixture.scope(),
		Spec: gitadvanced.Spec{ProtocolVersion: gitadvanced.ProtocolVersion,
			Operation: gitadvanced.WorktreeRemove, WorktreeID: created.Worktree.ID,
			WorktreeName: created.Worktree.Name}})
	if err != nil || review.Operation == nil || review.Approval == nil || !review.Preview.Executable() {
		t.Fatalf("review removal=%+v err=%v", review, err)
	}
	decision, err := fixture.state.DecideApproval(ctx, approval.DecisionRequest{
		ProposalID: review.Operation.ID, IdempotencyKey: "approve-remove-recovery",
		Action: approval.ActionApprove, ReviewedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	request := GitAdvancedExecuteRequest{ProtocolVersion: GitAdvancedAPIProtocolVersion,
		RunID: fixture.run.ID, OperationID: review.Operation.ID, ApprovalID: decision.Approval.ID,
		RequestedBy: "test_operator", Scope: fixture.scope()}
	if _, err := fixture.service.Execute(ctx, request); err == nil {
		t.Fatal("removal receipt failure was not injected")
	}
	removed, found, err := fixture.state.GetManagedGitWorktree(ctx, created.Worktree.ID)
	if err != nil || !found || removed.Present || removed.LastOperationID != review.Operation.ID || removed.RemovedAt == nil {
		t.Fatalf("removal was not persisted: found=%t worktree=%+v err=%v", found, removed, err)
	}
	running, found, err := fixture.state.GetGitAdvancedOperation(ctx, review.Operation.ID)
	if err != nil || !found || running.Status != gitadvanced.OperationRunning {
		t.Fatalf("operation did not remain running: found=%t operation=%+v err=%v", found, running, err)
	}
	reopened, err := store.Open(fixture.database)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	executor, err := repository.NewAdvancedExecutor(fixture.executor.ManagedRoot(), true)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewGitAdvancedService(reopened, executor, fixture.capabilities)
	if err != nil {
		t.Fatal(err)
	}
	result, err := restarted.ReconcileStartup(ctx, 100)
	if err != nil || result.Examined != 1 || result.Recovered != 1 || result.Failed != 1 {
		t.Fatalf("reconcile persisted removal=%+v err=%v", result, err)
	}
	stored, found, err := reopened.GetGitAdvancedOperation(ctx, running.ID)
	if err != nil || !found || stored.Status != gitadvanced.OperationFailed {
		t.Fatalf("recovered operation=%+v found=%t err=%v", stored, found, err)
	}
	var receipt gitadvanced.Receipt
	if err := json.Unmarshal([]byte(stored.ReceiptJSON), &receipt); err != nil ||
		receipt.WorktreeID != removed.ID || receipt.ErrorCode != gitadvanced.FailureInterrupted {
		t.Fatalf("recovered receipt=%+v err=%v", receipt, err)
	}
	after, _, err := reopened.GetManagedGitWorktree(ctx, removed.ID)
	if err != nil || after.Generation != removed.Generation || after.LastOperationID != removed.LastOperationID || after.Present {
		t.Fatalf("recovery repeated removal persistence: worktree=%+v err=%v", after, err)
	}
	if again, err := restarted.ReconcileStartup(ctx, 100); err != nil || again.Examined != 0 {
		t.Fatalf("second reconciliation=%+v err=%v", again, err)
	}
	if replay, err := restarted.Execute(ctx, request); err != nil || !replay.Replayed || replay.Receipt.ID != receipt.ID {
		t.Fatalf("recovered receipt replay=%+v err=%v", replay, err)
	}
}

type removedWorktreeBindingStore struct {
	GitAdvancedStore
	value gitadvanced.ManagedWorktree
}

func (s removedWorktreeBindingStore) GetManagedGitWorktree(context.Context, string) (gitadvanced.ManagedWorktree, bool, error) {
	return s.value, true, nil
}

func TestGitAdvancedRemovedWorktreeRecoveryRejectsForeignState(t *testing.T) {
	record := gitadvanced.OperationRecord{ID: "remove-operation", RunID: "run", WorkspaceID: "workspace",
		Operation: gitadvanced.WorktreeRemove, RepositorySHA256: "repository"}
	preview := gitadvanced.Preview{Spec: gitadvanced.Spec{WorktreeID: "worktree", WorktreeName: "owned"},
		Binding: gitadvanced.RepositoryBinding{CommonDirSHA256: "common"}}
	value := gitadvanced.ManagedWorktree{ID: "worktree", RunID: record.RunID, WorkspaceID: record.WorkspaceID,
		RepositorySHA256: record.RepositorySHA256, CommonDirSHA256: preview.Binding.CommonDirSHA256,
		Name: preview.Spec.WorktreeName, Present: false, LastOperationID: record.ID}
	for _, change := range []struct {
		name   string
		mutate func(*gitadvanced.ManagedWorktree)
	}{
		{"different operation", func(v *gitadvanced.ManagedWorktree) { v.LastOperationID = "other-operation" }},
		{"different target", func(v *gitadvanced.ManagedWorktree) { v.ID = "other-worktree" }},
		{"different Run", func(v *gitadvanced.ManagedWorktree) { v.RunID = "other-run" }},
		{"different repository", func(v *gitadvanced.ManagedWorktree) { v.CommonDirSHA256 = "other-common" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			foreign := value
			change.mutate(&foreign)
			service := &GitAdvancedService{store: removedWorktreeBindingStore{value: foreign}, now: time.Now}
			var receipt gitadvanced.Receipt
			if err := service.persistInterruptedManagedGitWorktree(t.Context(), "", record, preview, &receipt); err == nil || receipt.WorktreeID != "" {
				t.Fatalf("foreign removed state accepted: receipt=%+v err=%v", receipt, err)
			}
		})
	}
}
