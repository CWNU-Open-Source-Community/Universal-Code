package application

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

type batchDeliveryApplicationFixture struct {
	store      *store.SQLiteStore
	service    *BatchDeliveryService
	database   string
	repository string
	run        domain.Run
	root       domain.AgentNode
	proposal   domain.ChildTaskProposal
	spec       domain.BatchDeliverySpec
	caps       domain.ExecutionPermissionRuntimeCapabilities
}

func TestBatchDeliveryRealGitWorktreesReviewMergeAndReplay(t *testing.T) {
	fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionAsk)
	ctx := t.Context()
	worktreeParent := t.TempDir()
	prepared, err := fixture.service.Prepare(ctx, PrepareBatchDeliveryRequest{
		RunID: fixture.run.ID, ProposalID: fixture.proposal.ID, Spec: fixture.spec,
		OperationKey: "batch-prepare-real-git-0001", RequestedBy: fixture.root.ID,
		WorktreeParent: worktreeParent, Confirm: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Replayed || len(prepared.Workspaces) != 2 || len(prepared.Authorities) != 2 {
		t.Fatalf("prepare=%#v", prepared)
	}
	if prepared.Workspaces[0].Branch == prepared.Workspaces[1].Branch ||
		prepared.Workspaces[0].WorktreeRoot == prepared.Workspaces[1].WorktreeRoot {
		t.Fatal("batch children did not receive isolated Git identities")
	}
	for index, workspace := range prepared.Workspaces {
		if workspace.Status != domain.BatchWorkspaceDispatched ||
			workspace.ToolProfile != domain.DefaultBatchDeliveryToolProfile() ||
			workspace.ToolProfile.Network || workspace.ToolProfile.Credentials ||
			workspace.ToolProfile.DebugTerminal || workspace.ToolProfile.SpawnChildren ||
			workspace.ToolProfile.WorkspaceDelete {
			t.Fatalf("workspace %d authority=%#v", index+1, workspace)
		}
		branch := fixtureGit(t, "-C", workspace.WorktreeRoot, "branch", "--show-current")
		if branch != workspace.Branch {
			t.Fatalf("workspace %d branch=%q want=%q", index+1, branch, workspace.Branch)
		}
		_, _, _, err := fixture.service.SendMessage(ctx, SendBatchDeliveryMessageRequest{
			PlanID: prepared.Plan.ID, Ordinal: index + 1, Generation: 1,
			OwnerToken: prepared.Authorities[index].OwnerToken, Kind: domain.BatchMailboxAck,
			Summary:      "child accepted isolated assignment",
			OperationKey: "batch-real-git-ack-000" + string(rune('1'+index)),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	for index, relative := range []string{"internal/one/delivery.txt", "internal/two/delivery.txt"} {
		workspace := prepared.Workspaces[index]
		path := filepath.Join(workspace.WorktreeRoot, filepath.FromSlash(relative))
		if err := os.WriteFile(path, []byte("delivery "+string(rune('1'+index))+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		fixtureGit(t, "-C", workspace.WorktreeRoot, "add", "--", relative)
		fixtureGit(t, "-C", workspace.WorktreeRoot, "-c", "user.name=batch-child",
			"-c", "user.email=child@example.invalid", "commit", "--quiet", "-m",
			"deliver task "+string(rune('1'+index)))
	}

	receipts := make([]domain.BatchDeliveryReceipt, 2)
	for index := range prepared.Workspaces {
		receipt, replayed, err := fixture.service.Submit(ctx, SubmitBatchDeliveryRequest{
			PlanID: prepared.Plan.ID, Ordinal: index + 1, Generation: 1,
			OwnerToken:   prepared.Authorities[index].OwnerToken,
			EvidenceRefs: []string{"test://task/" + string(rune('1'+index))},
			Limitations:  []string{"no known limitations"},
			OperationKey: "batch-real-git-submit-000" + string(rune('1'+index)),
		})
		if err != nil || replayed {
			t.Fatalf("submit %d replayed=%t err=%v", index+1, replayed, err)
		}
		receipts[index] = receipt
		want := "internal/" + []string{"one", "two"}[index] + "/delivery.txt"
		if len(receipt.ChangedFiles) != 1 || receipt.ChangedFiles[0] != want ||
			receipt.BaseCommit != prepared.Plan.BaseCommit || receipt.HeadCommit == receipt.BaseCommit ||
			receipt.DiffSHA256 == "" || receipt.CallChainSHA256 == "" ||
			len(receipt.TestReceipts) != 1 {
			t.Fatalf("receipt %d=%#v", index+1, receipt)
		}
		review, reviewReplayed, err := fixture.service.Review(ctx,
			ReviewBatchDeliveryRequest{PlanID: prepared.Plan.ID, Ordinal: index + 1,
				Generation: 1, Reviewer: fixture.root.ID,
				Verdict: domain.BatchReviewAccepted, Summary: "full diff and checks accepted",
				FullDiffReviewed: true, CallChainReviewed: true, TestsReviewed: true,
				OperationKey: "batch-real-git-review-000" + string(rune('1'+index))})
		if err != nil || reviewReplayed || review.ReceiptID != receipt.ID {
			t.Fatalf("review %d=%#v replayed=%t err=%v", index+1, review, reviewReplayed, err)
		}
	}

	sourceHead := fixtureGit(t, "-C", fixture.repository, "rev-parse", "HEAD")
	merged, err := fixture.service.Merge(ctx, MergeBatchDeliveryRequest{
		PlanID: prepared.Plan.ID, OrderedOrdinals: []int{1, 2},
		OperationKey: "batch-real-git-merge-0001", RequestedBy: fixture.root.ID,
		WorktreeParent: t.TempDir(), Confirm: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if merged.Queue.Status != domain.BatchMergeQueueCompleted || len(merged.Steps) != 2 ||
		merged.Queue.NextIndex != 2 {
		t.Fatalf("merge=%#v", merged)
	}
	for _, relative := range []string{"internal/one/delivery.txt", "internal/two/delivery.txt"} {
		if _, err := os.Stat(filepath.Join(merged.Queue.IntegrationRoot,
			filepath.FromSlash(relative))); err != nil {
			t.Fatalf("merged path %s: %v", relative, err)
		}
		if _, err := os.Stat(filepath.Join(fixture.repository,
			filepath.FromSlash(relative))); !os.IsNotExist(err) {
			t.Fatalf("source workspace was polluted at %s: %v", relative, err)
		}
	}
	if got := fixtureGit(t, "-C", fixture.repository, "rev-parse", "HEAD"); got != sourceHead {
		t.Fatalf("source HEAD changed: got=%s want=%s", got, sourceHead)
	}
	for index, workspace := range prepared.Workspaces {
		if got := fixtureGit(t, "-C", workspace.WorktreeRoot, "rev-parse", "HEAD"); got != receipts[index].HeadCommit {
			t.Fatalf("child %d was polluted: got=%s want=%s", index+1, got, receipts[index].HeadCommit)
		}
	}

	replayed, err := fixture.service.Merge(ctx, MergeBatchDeliveryRequest{
		PlanID: prepared.Plan.ID, OrderedOrdinals: []int{1, 2},
		OperationKey: "batch-real-git-merge-0001", RequestedBy: fixture.root.ID,
		Confirm: true,
	})
	if err != nil || !replayed.Replayed || replayed.Queue.Status != domain.BatchMergeQueueCompleted {
		t.Fatalf("merge replay=%#v err=%v", replayed, err)
	}
	review, reviewReplayed, err := fixture.service.Review(ctx,
		ReviewBatchDeliveryRequest{PlanID: prepared.Plan.ID, Ordinal: 1,
			Generation: 1, Reviewer: fixture.root.ID, Verdict: domain.BatchReviewAccepted,
			Summary: "full diff and checks accepted", FullDiffReviewed: true,
			CallChainReviewed: true, TestsReviewed: true,
			OperationKey: "batch-real-git-review-0001"})
	if err != nil || !reviewReplayed || review.ReceiptID != receipts[0].ID {
		t.Fatalf("review replay=%#v replayed=%t err=%v", review, reviewReplayed, err)
	}
}

func TestBatchDeliveryDirtySubmissionRejectedAndCancellationPreservesIt(t *testing.T) {
	fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionAsk)
	prepared, err := fixture.service.Prepare(t.Context(), PrepareBatchDeliveryRequest{
		RunID: fixture.run.ID, ProposalID: fixture.proposal.ID, Spec: fixture.spec,
		OperationKey: "batch-dirty-prepare-0001", RequestedBy: fixture.root.ID,
		WorktreeParent: t.TempDir(), Confirm: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.Workspaces[0].WorktreeRoot,
		"internal", "one", "dirty.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = fixture.service.Submit(t.Context(), SubmitBatchDeliveryRequest{
		PlanID: prepared.Plan.ID, Ordinal: 1, Generation: 1,
		OwnerToken:   prepared.Authorities[0].OwnerToken,
		OperationKey: "batch-dirty-submit-0001",
	})
	if apperror.CodeOf(err) != apperror.CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "clean") {
		t.Fatalf("dirty submission error=%v", err)
	}
	cancelled, err := fixture.service.Cancel(t.Context(), CancelBatchDeliveryRequest{
		PlanID: prepared.Plan.ID, OperationKey: "batch-dirty-cancel-0001",
		RequestedBy: fixture.root.ID, Reason: "operator cancelled dirty batch", Confirm: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Snapshot.Plan.Status != domain.BatchDeliveryAborted ||
		len(cancelled.PreservedOrdinals) != 1 || cancelled.PreservedOrdinals[0] != 1 ||
		cancelled.Snapshot.Workspaces[0].Status != domain.BatchWorkspaceOrphaned ||
		cancelled.Snapshot.Workspaces[1].Status != domain.BatchWorkspaceCancelled {
		t.Fatalf("cancelled=%#v", cancelled)
	}
	if _, err := os.Stat(filepath.Join(prepared.Workspaces[0].WorktreeRoot,
		"internal", "one", "dirty.txt")); err != nil {
		t.Fatalf("dirty recovery evidence was removed: %v", err)
	}
	if _, err := os.Stat(prepared.Workspaces[1].WorktreeRoot); !os.IsNotExist(err) {
		t.Fatalf("clean sibling worktree was not cleaned: %v", err)
	}
}

func TestBatchDeliveryNarrowedWorkspaceToolsEnforceOwnershipAndCommit(t *testing.T) {
	fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionAsk)
	prepared, err := fixture.service.Prepare(t.Context(), PrepareBatchDeliveryRequest{
		RunID: fixture.run.ID, ProposalID: fixture.proposal.ID, Spec: fixture.spec,
		OperationKey: "batch-tools-prepare-0001", RequestedBy: fixture.root.ID,
		WorktreeParent: t.TempDir(), Confirm: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	authority := BatchDeliveryToolAuthority{PlanID: prepared.Plan.ID, Ordinal: 1,
		Generation: 1, OwnerToken: prepared.Authorities[0].OwnerToken}
	if _, _, _, err := fixture.service.SendMessage(t.Context(),
		SendBatchDeliveryMessageRequest{PlanID: authority.PlanID, Ordinal: authority.Ordinal,
			Generation: authority.Generation, OwnerToken: authority.OwnerToken,
			Kind: domain.BatchMailboxAck, Summary: "tool child acknowledged",
			OperationKey: "batch-tools-ack-000001"}); err != nil {
		t.Fatal(err)
	}
	authority.OperationKey = "batch-tools-read-owned-0001"
	read, err := fixture.service.BatchRead(t.Context(), BatchDeliveryReadRequest{
		Authority: authority, Path: "internal/one/base.txt", StartLine: 1, EndLine: 10})
	if err != nil || read.Value.Content != "base" || read.Value.ContentSHA256 == "" {
		t.Fatalf("owned read=%#v err=%v", read, err)
	}
	authority.OperationKey = "batch-tools-read-denied-0001"
	if _, err := fixture.service.BatchRead(t.Context(), BatchDeliveryReadRequest{
		Authority: authority, Path: "internal/two/base.txt", StartLine: 1, EndLine: 10}); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("outside read error=%v", err)
	}
	authority.OperationKey = "batch-tools-search-owned-001"
	glob, err := fixture.service.BatchGlob(t.Context(), BatchDeliveryGlobRequest{
		Authority: authority, Pattern: "**", Limit: 20})
	if err != nil || len(glob.Value.Paths) == 0 {
		t.Fatalf("scoped glob=%#v err=%v", glob, err)
	}
	for _, path := range glob.Value.Paths {
		if !strings.HasPrefix(path, "internal/one/") {
			t.Fatalf("scoped glob leaked %q", path)
		}
	}
	authority.OperationKey = "batch-tools-grep-owned-0001"
	grep, err := fixture.service.BatchGrep(t.Context(), BatchDeliveryGrepRequest{
		Authority: authority, Query: "base", Pattern: "**", Limit: 20})
	if err != nil || len(grep.Value.Matches) == 0 {
		t.Fatalf("scoped grep=%#v err=%v", grep, err)
	}
	for _, match := range grep.Value.Matches {
		if !strings.HasPrefix(match.Path, "internal/one/") {
			t.Fatalf("scoped grep leaked %q", match.Path)
		}
	}

	authority.OperationKey = "batch-tools-change-owned-001"
	change, err := fixture.service.BatchProposeChange(t.Context(), BatchDeliveryChangeRequest{
		Authority: authority, Action: "patch", Path: "internal/one/base.txt",
		ExpectedSHA256: read.Value.ContentSHA256,
		Replacements: []toolgateway.WorkspaceReplacement{{OldText: "base",
			NewText: "changed", ExpectedOccurrences: 1}},
	})
	if err != nil || change.Value.Status != "proposed" {
		t.Fatalf("change=%#v err=%v", change, err)
	}
	authority.OperationKey = "batch-tools-apply-owned-0001"
	applied, err := fixture.service.BatchApplyChange(t.Context(), BatchDeliveryApplyRequest{
		Authority: authority, EditID: change.Value.ID,
		ExpectedOriginalSHA256: change.Value.OriginalHash,
		ExpectedProposedSHA256: change.Value.ProposedHash})
	if err != nil || applied.Value.Status != "applied" {
		t.Fatalf("apply=%#v err=%v", applied, err)
	}
	if data, err := os.ReadFile(filepath.Join(prepared.Workspaces[0].WorktreeRoot,
		"internal", "one", "base.txt")); err != nil || string(data) != "changed\n" {
		t.Fatalf("applied bytes=%q err=%v", data, err)
	}
	authority.OperationKey = "batch-tools-commit-owned-001"
	committed, err := fixture.service.BatchGitCommit(t.Context(), BatchDeliveryGitRequest{
		Authority: authority, Message: "update owned file"})
	if err != nil || committed.Value.HeadCommit == prepared.Plan.BaseCommit ||
		committed.Workspace.HeadCommit != committed.Value.HeadCommit {
		t.Fatalf("commit=%#v err=%v", committed, err)
	}
	replayed, err := fixture.service.BatchGitCommit(t.Context(), BatchDeliveryGitRequest{
		Authority: authority, Message: "update owned file"})
	if err != nil || !replayed.Replayed ||
		replayed.Value.HeadCommit != committed.Value.HeadCommit {
		t.Fatalf("commit replay=%#v err=%v", replayed, err)
	}

	if err := os.WriteFile(filepath.Join(prepared.Workspaces[0].WorktreeRoot,
		"internal", "two", "outside.txt"), []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	authority.OperationKey = "batch-tools-commit-denied-001"
	if _, err := fixture.service.BatchGitCommit(t.Context(), BatchDeliveryGitRequest{
		Authority: authority, Message: "try outside change"}); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("outside commit error=%v", err)
	}
}

func TestBatchDeliveryGitCommitRecoversDurableIntentAfterProcessCrash(t *testing.T) {
	fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionAsk)
	prepared, err := fixture.service.Prepare(t.Context(), PrepareBatchDeliveryRequest{
		RunID: fixture.run.ID, ProposalID: fixture.proposal.ID, Spec: fixture.spec,
		OperationKey: "batch-commit-crash-prepare-01", RequestedBy: fixture.root.ID,
		WorktreeParent: t.TempDir(), Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	child := prepared.Workspaces[0]
	authority := BatchDeliveryToolAuthority{PlanID: prepared.Plan.ID, Ordinal: 1,
		Generation: 1, OwnerToken: prepared.Authorities[0].OwnerToken,
		OperationKey: "batch-commit-crash-operation-01"}
	if _, _, _, err := fixture.service.SendMessage(t.Context(),
		SendBatchDeliveryMessageRequest{PlanID: prepared.Plan.ID, Ordinal: 1,
			Generation: 1, OwnerToken: authority.OwnerToken, Kind: domain.BatchMailboxAck,
			Summary: "ack crash recovery task", OperationKey: "batch-commit-crash-ack-0001"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(child.WorktreeRoot, "internal", "one", "base.txt")
	if err := os.WriteFile(path, []byte("crash-safe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	messageText := "commit with durable intent"
	messageHash := sha256.Sum256([]byte(messageText))
	tokenDigest, err := batchDeliveryOwnerTokenDigest(authority.OwnerToken)
	if err != nil {
		t.Fatal(err)
	}
	intent := batchDeliveryMessage(prepared.Plan.ID, 1, 1, domain.BatchMailboxEvidence,
		child.AgentID, "narrowed child tool intent: git_commit",
		[]string{"message-sha256:" + hex.EncodeToString(messageHash[:]),
			"prior-head:" + child.BaseCommit}, authority.OperationKey+"-git_commit_intent",
		time.Now().UTC())
	if _, _, _, err := fixture.store.AppendBatchDeliveryMailbox(t.Context(), intent,
		tokenDigest, child.LeaseExpiresAt); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", child.WorktreeRoot, "add", "--", "internal/one/base.txt")
	fixtureGit(t, "-C", child.WorktreeRoot,
		"-c", "user.name=CyberAgent Delivery Child",
		"-c", "user.email=delivery-child@cyberagent.invalid",
		"commit", "--quiet", "--no-gpg-sign", "-m", messageText)

	recovered, err := fixture.service.BatchGitCommit(t.Context(), BatchDeliveryGitRequest{
		Authority: authority, Message: messageText})
	if err != nil || !recovered.Replayed || !recovered.Value.Clean ||
		recovered.Value.HeadCommit == child.BaseCommit ||
		recovered.Workspace.HeadCommit != recovered.Value.HeadCommit {
		t.Fatalf("recovered=%#v err=%v", recovered, err)
	}
	replayed, err := fixture.service.BatchGitCommit(t.Context(), BatchDeliveryGitRequest{
		Authority: authority, Message: messageText})
	if err != nil || !replayed.Replayed ||
		replayed.Value.HeadCommit != recovered.Value.HeadCommit {
		t.Fatalf("replay=%#v err=%v", replayed, err)
	}
	if _, err := fixture.service.BatchGitCommit(t.Context(), BatchDeliveryGitRequest{
		Authority: authority, Message: "different reused message"}); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("different replay error=%v", err)
	}
}

func TestBatchDeliveryGitCommitRecoveryRejectsMultipleExternalCommits(t *testing.T) {
	fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionAsk)
	prepared, err := fixture.service.Prepare(t.Context(), PrepareBatchDeliveryRequest{
		RunID: fixture.run.ID, ProposalID: fixture.proposal.ID, Spec: fixture.spec,
		OperationKey: "batch-commit-multiple-prepare-01", RequestedBy: fixture.root.ID,
		WorktreeParent: t.TempDir(), Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	child := prepared.Workspaces[0]
	authority := BatchDeliveryToolAuthority{PlanID: prepared.Plan.ID, Ordinal: 1,
		Generation: 1, OwnerToken: prepared.Authorities[0].OwnerToken,
		OperationKey: "batch-commit-multiple-operation-01"}
	if _, _, _, err := fixture.service.SendMessage(t.Context(),
		SendBatchDeliveryMessageRequest{PlanID: prepared.Plan.ID, Ordinal: 1,
			Generation: 1, OwnerToken: authority.OwnerToken, Kind: domain.BatchMailboxAck,
			Summary:      "ack multiple commit recovery task",
			OperationKey: "batch-commit-multiple-ack-0001"}); err != nil {
		t.Fatal(err)
	}
	messageText := "commit with durable intent"
	messageHash := sha256.Sum256([]byte(messageText))
	tokenDigest, err := batchDeliveryOwnerTokenDigest(authority.OwnerToken)
	if err != nil {
		t.Fatal(err)
	}
	intent := batchDeliveryMessage(prepared.Plan.ID, 1, 1, domain.BatchMailboxEvidence,
		child.AgentID, "narrowed child tool intent: git_commit",
		[]string{"message-sha256:" + hex.EncodeToString(messageHash[:]),
			"prior-head:" + child.BaseCommit}, authority.OperationKey+"-git_commit_intent",
		time.Now().UTC())
	if _, _, _, err := fixture.store.AppendBatchDeliveryMailbox(t.Context(), intent,
		tokenDigest, child.LeaseExpiresAt); err != nil {
		t.Fatal(err)
	}
	for index, content := range []string{"first\n", "second\n"} {
		path := filepath.Join(child.WorktreeRoot, "internal", "one",
			fmt.Sprintf("external-%d.txt", index+1))
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		fixtureGit(t, "-C", child.WorktreeRoot, "add", "--",
			filepath.ToSlash(filepath.Join("internal", "one", filepath.Base(path))))
		fixtureGit(t, "-C", child.WorktreeRoot,
			"-c", "user.name=CyberAgent Delivery Child",
			"-c", "user.email=delivery-child@cyberagent.invalid",
			"commit", "--quiet", "--no-gpg-sign", "-m", messageText)
	}
	if _, err := fixture.service.BatchGitCommit(t.Context(), BatchDeliveryGitRequest{
		Authority: authority, Message: messageText}); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("multiple external commits recovery error=%v", err)
	}
}

func TestBatchDeliveryRestartRestoresMissingWorktreeWithoutDuplicateDispatch(t *testing.T) {
	fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionAsk)
	request := PrepareBatchDeliveryRequest{RunID: fixture.run.ID,
		ProposalID: fixture.proposal.ID, Spec: fixture.spec,
		OperationKey: "batch-restart-prepare-0001", RequestedBy: fixture.root.ID,
		WorktreeParent: t.TempDir(), Confirm: true}
	prepared, err := fixture.service.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fixture.service.SendMessage(t.Context(),
		SendBatchDeliveryMessageRequest{PlanID: prepared.Plan.ID, Ordinal: 1,
			Generation: 1, OwnerToken: prepared.Authorities[0].OwnerToken,
			Kind: domain.BatchMailboxAck, Summary: "child owns restart token",
			OperationKey: "batch-restart-ack-000001"}); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", fixture.repository, "worktree", "remove",
		prepared.Workspaces[0].WorktreeRoot)
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(fixture.database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	service := NewBatchDeliveryService(reopened)
	reconciled, err := service.Reconcile(t.Context(), prepared.Plan.ID)
	if err != nil || reconciled.RecoveredWorktrees != 1 ||
		reconciled.NeedsOperatorAttention {
		cause := err
		chain := ""
		for cause != nil {
			chain += fmt.Sprintf("[%T] %v | ", cause, cause)
			cause = errors.Unwrap(cause)
		}
		t.Fatalf("reconcile=%#v err=%#v chain=%s", reconciled, err, chain)
	}
	if branch := fixtureGit(t, "-C", prepared.Workspaces[0].WorktreeRoot,
		"branch", "--show-current"); branch != prepared.Workspaces[0].Branch {
		t.Fatalf("restored branch=%q want=%q", branch, prepared.Workspaces[0].Branch)
	}
	messages, err := reopened.ListBatchDeliveryMailbox(t.Context(), prepared.Plan.ID, 1, 20)
	if err != nil || len(messages) != 2 || messages[0].Kind != domain.BatchMailboxDispatch ||
		messages[1].Kind != domain.BatchMailboxAck {
		t.Fatalf("mailbox after restart=%#v err=%v", messages, err)
	}
	if err := os.WriteFile(filepath.Join(fixture.repository, "source-advanced.txt"),
		[]byte("new source commit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", fixture.repository, "add", "source-advanced.txt")
	fixtureGit(t, "-C", fixture.repository, "commit", "--quiet", "-m", "advance source")
	request.WorktreeParent = t.TempDir()
	replayed, err := service.Prepare(t.Context(), request)
	if err != nil || !replayed.Replayed || replayed.Plan.BaseCommit != prepared.Plan.BaseCommit ||
		len(replayed.Authorities) != 0 {
		t.Fatalf("prepare replay=%#v err=%v", replayed, err)
	}
	if _, _, _, err := service.SendMessage(t.Context(), SendBatchDeliveryMessageRequest{
		PlanID: prepared.Plan.ID, Ordinal: 1, Generation: 1,
		OwnerToken: prepared.Authorities[0].OwnerToken, Kind: domain.BatchMailboxProgress,
		Summary:      "continued after process restart",
		OperationKey: "batch-restart-progress-001"}); err != nil {
		t.Fatalf("durable owner token failed after restart: %v", err)
	}
}

func TestBatchDeliveryBaseDriftRequiresConfirmationAndTextConflictRollsBack(t *testing.T) {
	fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionAsk)
	prepared, receipts := prepareAcceptedBatchTextDeliveries(t, fixture,
		"batch-conflict")
	conflictPath := filepath.Join(fixture.repository, "internal", "one", "delivery.txt")
	if err := os.WriteFile(conflictPath, []byte("source version\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", fixture.repository, "add", "internal/one/delivery.txt")
	fixtureGit(t, "-C", fixture.repository, "commit", "--quiet", "-m", "advance source conflict")
	latestBase := fixtureGit(t, "-C", fixture.repository, "rev-parse", "HEAD")
	if _, err := fixture.service.Merge(t.Context(), MergeBatchDeliveryRequest{
		PlanID: prepared.Plan.ID, OperationKey: "batch-conflict-unconfirmed-merge",
		RequestedBy: fixture.root.ID, WorktreeParent: t.TempDir(), Confirm: true}); apperror.CodeOf(err) != apperror.CodeConflict ||
		!strings.Contains(err.Error(), "confirm") {
		t.Fatalf("unconfirmed base drift error=%v", err)
	}
	blocked, err := fixture.service.Merge(t.Context(), MergeBatchDeliveryRequest{
		PlanID: prepared.Plan.ID, OperationKey: "batch-conflict-confirmed-merge01",
		RequestedBy: fixture.root.ID, WorktreeParent: t.TempDir(), Confirm: true,
		ConfirmReplay: true})
	if apperror.CodeOf(err) != apperror.CodeConflict ||
		blocked.Queue.Status != domain.BatchMergeQueueBlocked ||
		blocked.Queue.FailureCode != "text_conflict" || len(blocked.Steps) != 1 ||
		blocked.Steps[0].PostMergeHead != latestBase {
		t.Fatalf("conflicted merge=%#v err=%v", blocked, err)
	}
	if got := fixtureGit(t, "-C", blocked.Queue.IntegrationRoot, "rev-parse", "HEAD"); got != latestBase {
		t.Fatalf("integration rollback head=%s want=%s", got, latestBase)
	}
	if got := fixtureGit(t, "-C", fixture.repository, "rev-parse", "HEAD"); got != latestBase {
		t.Fatalf("source changed during conflict: %s", got)
	}
	for index, child := range prepared.Workspaces {
		if got := fixtureGit(t, "-C", child.WorktreeRoot, "rev-parse", "HEAD"); got != receipts[index].HeadCommit {
			t.Fatalf("child %d polluted after conflict", index+1)
		}
	}
}

func TestBatchDeliveryHostValidationRequiresExplicitProcessCapability(t *testing.T) {
	fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionFull)
	spec := fixture.spec
	spec.Tasks[0].Validations = append(spec.Tasks[0].Validations,
		domain.BatchDeliveryValidationRequirement{ID: "go-owned",
			Kind: domain.BatchValidationGoTest, Scope: "internal/one"})
	spec, err := domain.NormalizeBatchDeliverySpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewBatchDeliveryService(fixture.store).Prepare(t.Context(),
		PrepareBatchDeliveryRequest{RunID: fixture.run.ID, ProposalID: fixture.proposal.ID,
			Spec: spec, OperationKey: "batch-host-validation-disabled-01",
			RequestedBy: fixture.root.ID, WorktreeParent: t.TempDir(), Confirm: true})
	if apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("host validation without process capability error=%v", err)
	}
}

func TestBatchDeliveryHostValidationRechecksCurrentRunPermission(t *testing.T) {
	fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionFull)
	fixture.spec.Tasks[0].Validations = append(fixture.spec.Tasks[0].Validations,
		domain.BatchDeliveryValidationRequirement{ID: "go-current-authority",
			Kind: domain.BatchValidationGoTest, Scope: "."})
	var err error
	fixture.spec, err = domain.NormalizeBatchDeliverySpec(fixture.spec)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := fixture.service.Prepare(t.Context(), PrepareBatchDeliveryRequest{
		RunID: fixture.run.ID, ProposalID: fixture.proposal.ID, Spec: fixture.spec,
		OperationKey: "batch-current-authority-prepare-01", RequestedBy: fixture.root.ID,
		WorktreeParent: t.TempDir(), Confirm: true})
	if err != nil {
		t.Fatalf("prepare host validation: %v (chain: %s)", err, testErrorChain(err))
	}
	if _, _, _, err := fixture.service.SendMessage(t.Context(),
		SendBatchDeliveryMessageRequest{PlanID: prepared.Plan.ID, Ordinal: 1,
			Generation: 1, OwnerToken: prepared.Authorities[0].OwnerToken,
			Kind: domain.BatchMailboxAck, Summary: "authority fixture acknowledged",
			OperationKey: "batch-current-authority-ack-0001"}); err != nil {
		t.Fatal(err)
	}
	child := prepared.Workspaces[0]
	path := filepath.Join(child.WorktreeRoot, "internal", "one", "authority.txt")
	if err := os.WriteFile(path, []byte("delivery\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", child.WorktreeRoot, "add", "internal/one/authority.txt")
	fixtureGit(t, "-C", child.WorktreeRoot, "commit", "--quiet", "-m", "authority delivery")

	runs := NewRunService(fixture.store)
	if _, err := runs.Pause(t.Context(), fixture.run.ID); err != nil {
		t.Fatal(err)
	}
	granted, err := fixture.store.GetRunExecutionPermission(t.Context(), fixture.run.ID)
	if err != nil || !fixture.caps.AllowsSnapshot(granted) {
		t.Fatalf("host validation grant before downgrade=%#v err=%v", granted, err)
	}
	downgraded, err := NewRunExecutionPermissionService(fixture.store, fixture.caps).Change(
		t.Context(), ChangeRunExecutionPermissionRequest{RunID: fixture.run.ID,
			Mode:         string(domain.RunExecutionPermissionAsk),
			OperationKey: "batch-current-authority-downgrade-01", RequestedBy: "operator",
			Reason: "remove host validation authority before child submission"})
	if err != nil {
		t.Fatal(err)
	}
	if downgraded.Permission.Mode != domain.RunExecutionPermissionAsk ||
		fixture.caps.AllowsSnapshot(granted) {
		t.Fatal("Ask downgrade retained the prior Full runtime fence")
	}
	if _, err := runs.Start(t.Context(), fixture.run.ID); err != nil {
		t.Fatal(err)
	}
	_, _, err = fixture.service.Submit(t.Context(), SubmitBatchDeliveryRequest{
		PlanID: prepared.Plan.ID, Ordinal: 1, Generation: 1,
		OwnerToken:   prepared.Authorities[0].OwnerToken,
		OperationKey: "batch-current-authority-submit-01"})
	if apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("downgraded Run retained host validation authority: %v", err)
	}
}

func TestBatchDeliveryReconcileDoesNotReviveInactiveRun(t *testing.T) {
	fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionAsk)
	prepared, err := fixture.service.Prepare(t.Context(), PrepareBatchDeliveryRequest{
		RunID: fixture.run.ID, ProposalID: fixture.proposal.ID, Spec: fixture.spec,
		OperationKey: "batch-inactive-reconcile-prepare-01", RequestedBy: fixture.root.ID,
		WorktreeParent: t.TempDir(), Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	child := prepared.Workspaces[0]
	if err := repository.RemoveWorktreeKeepBranch(t.Context(), fixture.repository,
		child.WorktreeRoot, child.Branch, child.BaseCommit); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunService(fixture.store).Pause(t.Context(), fixture.run.ID); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.service.Reconcile(t.Context(), prepared.Plan.ID)
	if err != nil || !result.NeedsOperatorAttention || result.RecoveredWorktrees != 0 ||
		result.MergeResumed {
		t.Fatalf("inactive reconciliation result=%#v err=%v", result, err)
	}
	if _, statErr := os.Stat(child.WorktreeRoot); !os.IsNotExist(statErr) {
		t.Fatalf("inactive Run rematerialized child worktree: %v", statErr)
	}
}

func TestBatchDeliveryReviewRejectsValidationStateDrift(t *testing.T) {
	fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionFull)
	fixture.spec.Tasks[0].Validations = append(fixture.spec.Tasks[0].Validations,
		domain.BatchDeliveryValidationRequirement{ID: "go-review-state",
			Kind: domain.BatchValidationGoTest, Scope: "."})
	var err error
	fixture.spec, err = domain.NormalizeBatchDeliverySpec(fixture.spec)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := fixture.service.Prepare(t.Context(), PrepareBatchDeliveryRequest{
		RunID: fixture.run.ID, ProposalID: fixture.proposal.ID, Spec: fixture.spec,
		OperationKey: "batch-review-state-prepare-0001", RequestedBy: fixture.root.ID,
		WorktreeParent: t.TempDir(), Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fixture.service.SendMessage(t.Context(),
		SendBatchDeliveryMessageRequest{PlanID: prepared.Plan.ID, Ordinal: 1,
			Generation: 1, OwnerToken: prepared.Authorities[0].OwnerToken,
			Kind: domain.BatchMailboxAck, Summary: "state drift fixture acknowledged",
			OperationKey: "batch-review-state-ack-000001"}); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "first-validation-complete")
	testSource := fmt.Sprintf(`package one

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidationStateIsStable(t *testing.T) {
	const marker = %q
	if _, err := os.Stat(marker); os.IsNotExist(err) {
		if err := os.WriteFile(marker, []byte("first"), 0600); err != nil { t.Fatal(err) }
		return
	}
	if err := os.WriteFile(filepath.Join("..", "..", "review-validation-drift.txt"),
		[]byte("drift"), 0600); err != nil { t.Fatal(err) }
}
`, marker)
	child := prepared.Workspaces[0]
	testPath := filepath.Join(child.WorktreeRoot, "internal", "one", "state_drift_test.go")
	if err := os.WriteFile(testPath, []byte(testSource), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", child.WorktreeRoot, "add", "internal/one/state_drift_test.go")
	fixtureGit(t, "-C", child.WorktreeRoot, "commit", "--quiet", "-m", "add state validation")
	receipt, _, err := fixture.service.Submit(t.Context(), SubmitBatchDeliveryRequest{
		PlanID: prepared.Plan.ID, Ordinal: 1, Generation: 1,
		OwnerToken:   prepared.Authorities[0].OwnerToken,
		EvidenceRefs: []string{"test://review-state"}, Limitations: []string{"none"},
		OperationKey: "batch-review-state-submit-0001"})
	if err != nil || len(receipt.TestReceipts) != 2 {
		t.Fatalf("stable submission receipt=%#v err=%v", receipt, err)
	}
	_, _, err = fixture.service.Review(t.Context(), ReviewBatchDeliveryRequest{
		PlanID: prepared.Plan.ID, Ordinal: 1, Generation: 1,
		Reviewer: fixture.root.ID, Verdict: domain.BatchReviewAccepted,
		Summary: "review should detect validation drift", FullDiffReviewed: true,
		CallChainReviewed: true, TestsReviewed: true,
		OperationKey: "batch-review-state-review-0001"})
	if apperror.CodeOf(err) != apperror.CodeConflict ||
		!strings.Contains(err.Error(), "validation changed") {
		t.Fatalf("review validation drift error=%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(child.WorktreeRoot,
		"review-validation-drift.txt")); statErr != nil {
		t.Fatalf("validation drift fixture did not mutate the worktree: %v", statErr)
	}
}

func TestBatchDeliverySemanticValidationFailureRollsBackOnlyMergeStep(t *testing.T) {
	fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionFull)
	for index := range fixture.spec.Tasks {
		fixture.spec.Tasks[index].Validations = append(fixture.spec.Tasks[index].Validations,
			domain.BatchDeliveryValidationRequirement{ID: "go-all-" + string(rune('1'+index)),
				Kind: domain.BatchValidationGoTest, Scope: "."})
	}
	var err error
	fixture.spec, err = domain.NormalizeBatchDeliverySpec(fixture.spec)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := fixture.service.Prepare(t.Context(), PrepareBatchDeliveryRequest{
		RunID: fixture.run.ID, ProposalID: fixture.proposal.ID, Spec: fixture.spec,
		OperationKey: "batch-semantic-prepare-0001", RequestedBy: fixture.root.ID,
		WorktreeParent: t.TempDir(), Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	for index := range prepared.Workspaces {
		if _, _, _, err := fixture.service.SendMessage(t.Context(),
			SendBatchDeliveryMessageRequest{PlanID: prepared.Plan.ID, Ordinal: index + 1,
				Generation: 1, OwnerToken: prepared.Authorities[index].OwnerToken,
				Kind: domain.BatchMailboxAck, Summary: "semantic fixture acknowledged",
				OperationKey: "batch-semantic-ack-000" + string(rune('1'+index))}); err != nil {
			t.Fatal(err)
		}
	}
	oneRoot := prepared.Workspaces[0].WorktreeRoot
	valuePath := filepath.Join(oneRoot, "internal", "one", "value.go")
	valueData, err := os.ReadFile(valuePath)
	if err != nil {
		t.Fatal(err)
	}
	valueData = []byte(strings.Replace(string(valueData), `Value = "base"`,
		`Value = "changed"`, 1))
	if err := os.WriteFile(valuePath, valueData, 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", oneRoot, "add", "internal/one/value.go")
	fixtureGit(t, "-C", oneRoot, "commit", "--quiet", "-m", "change shared value")
	twoRoot := prepared.Workspaces[1].WorktreeRoot
	testSource := `package two_test

import (
	"testing"
	"example.invalid/batchfixture/internal/one"
)

func TestBaseValue(t *testing.T) {
	if one.Value != "base" { t.Fatalf("value=%s", one.Value) }
}
`
	if err := os.WriteFile(filepath.Join(twoRoot, "internal", "two", "value_test.go"),
		[]byte(testSource), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", twoRoot, "add", "internal/two/value_test.go")
	fixtureGit(t, "-C", twoRoot, "commit", "--quiet", "-m", "assert base value")
	for index := range prepared.Workspaces {
		receipt, _, err := fixture.service.Submit(t.Context(), SubmitBatchDeliveryRequest{
			PlanID: prepared.Plan.ID, Ordinal: index + 1, Generation: 1,
			OwnerToken:   prepared.Authorities[index].OwnerToken,
			EvidenceRefs: []string{"test://semantic/" + string(rune('1'+index))},
			Limitations:  []string{"cross-delivery interaction requires merge validation"},
			OperationKey: "batch-semantic-submit-000" + string(rune('1'+index))})
		if err != nil || len(receipt.TestReceipts) != 2 {
			t.Fatalf("submit %d receipt=%#v err=%v chain=%s", index+1, receipt, err,
				testErrorChain(err))
		}
		if _, _, err := fixture.service.Review(t.Context(), ReviewBatchDeliveryRequest{
			PlanID: prepared.Plan.ID, Ordinal: index + 1, Generation: 1,
			Reviewer: fixture.root.ID, Verdict: domain.BatchReviewAccepted,
			Summary: "individual delivery passes", FullDiffReviewed: true,
			CallChainReviewed: true, TestsReviewed: true,
			OperationKey: "batch-semantic-review-000" + string(rune('1'+index))}); err != nil {
			t.Fatal(err)
		}
	}
	merged, err := fixture.service.Merge(t.Context(), MergeBatchDeliveryRequest{
		PlanID: prepared.Plan.ID, OrderedOrdinals: []int{1, 2},
		OperationKey: "batch-semantic-merge-0001", RequestedBy: fixture.root.ID,
		WorktreeParent: t.TempDir(), Confirm: true})
	if apperror.CodeOf(err) != apperror.CodeConflict ||
		merged.Queue.Status != domain.BatchMergeQueueBlocked ||
		merged.Queue.FailureCode != "semantic_validation_failed" || len(merged.Steps) != 2 ||
		merged.Steps[0].Status != domain.BatchMergeQueueCompleted ||
		merged.Steps[1].Status != domain.BatchMergeQueueBlocked ||
		merged.Queue.IntegrationHead != merged.Steps[0].PostMergeHead {
		t.Fatalf("semantic merge=%#v err=%v", merged, err)
	}
	if _, err := os.Stat(filepath.Join(merged.Queue.IntegrationRoot,
		"internal", "two", "value_test.go")); !os.IsNotExist(err) {
		t.Fatalf("failed merge step was not rolled back: %v", err)
	}
	mergedValue, err := os.ReadFile(filepath.Join(merged.Queue.IntegrationRoot,
		"internal", "one", "value.go"))
	if err != nil || !strings.Contains(string(mergedValue), `Value = "changed"`) {
		t.Fatalf("successful prior step was lost: %q err=%v", mergedValue, err)
	}
}

func TestBatchDeliveryLaterMergeRerunsEarlierValidationContract(t *testing.T) {
	fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionFull)
	fixture.spec.Tasks[0].Validations = append(fixture.spec.Tasks[0].Validations,
		domain.BatchDeliveryValidationRequirement{ID: "go-earlier-contract",
			Kind: domain.BatchValidationGoTest, Scope: "."})
	var err error
	fixture.spec, err = domain.NormalizeBatchDeliverySpec(fixture.spec)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := fixture.service.Prepare(t.Context(), PrepareBatchDeliveryRequest{
		RunID: fixture.run.ID, ProposalID: fixture.proposal.ID, Spec: fixture.spec,
		OperationKey: "batch-cumulative-prepare-0001", RequestedBy: fixture.root.ID,
		WorktreeParent: t.TempDir(), Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	for index := range prepared.Workspaces {
		if _, _, _, err := fixture.service.SendMessage(t.Context(),
			SendBatchDeliveryMessageRequest{PlanID: prepared.Plan.ID, Ordinal: index + 1,
				Generation: 1, OwnerToken: prepared.Authorities[index].OwnerToken,
				Kind: domain.BatchMailboxAck, Summary: "cumulative fixture acknowledged",
				OperationKey: "batch-cumulative-ack-000" + string(rune('1'+index))}); err != nil {
			t.Fatal(err)
		}
	}
	oneRoot := prepared.Workspaces[0].WorktreeRoot
	testSource := `package one_test

import (
	"testing"
	"example.invalid/batchfixture/internal/two"
)

func TestTaskOneContract(t *testing.T) {
	if two.Value != "base" { t.Fatalf("two.Value=%s", two.Value) }
}
`
	if err := os.WriteFile(filepath.Join(oneRoot, "internal", "one", "contract_test.go"),
		[]byte(testSource), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", oneRoot, "add", "internal/one/contract_test.go")
	fixtureGit(t, "-C", oneRoot, "commit", "--quiet", "-m", "add task one contract")

	twoRoot := prepared.Workspaces[1].WorktreeRoot
	twoPath := filepath.Join(twoRoot, "internal", "two", "doc.go")
	twoSource, err := os.ReadFile(twoPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(twoPath, []byte(strings.Replace(string(twoSource),
		`Value = "base"`, `Value = "changed"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", twoRoot, "add", "internal/two/doc.go")
	fixtureGit(t, "-C", twoRoot, "commit", "--quiet", "-m", "change task two value")

	for index := range prepared.Workspaces {
		receipt, _, err := fixture.service.Submit(t.Context(), SubmitBatchDeliveryRequest{
			PlanID: prepared.Plan.ID, Ordinal: index + 1, Generation: 1,
			OwnerToken:   prepared.Authorities[index].OwnerToken,
			EvidenceRefs: []string{"test://cumulative/" + string(rune('1'+index))},
			Limitations:  []string{"cross-delivery contract requires cumulative replay"},
			OperationKey: "batch-cumulative-submit-000" + string(rune('1'+index))})
		if err != nil || len(receipt.TestReceipts) != 2-index {
			t.Fatalf("submit %d receipt=%#v err=%v", index+1, receipt, err)
		}
		if _, _, err := fixture.service.Review(t.Context(), ReviewBatchDeliveryRequest{
			PlanID: prepared.Plan.ID, Ordinal: index + 1, Generation: 1,
			Reviewer: fixture.root.ID, Verdict: domain.BatchReviewAccepted,
			Summary:          "individual delivery passes its declared checks",
			FullDiffReviewed: true, CallChainReviewed: true, TestsReviewed: true,
			OperationKey: "batch-cumulative-review-000" + string(rune('1'+index))}); err != nil {
			t.Fatal(err)
		}
	}
	merged, err := fixture.service.Merge(t.Context(), MergeBatchDeliveryRequest{
		PlanID: prepared.Plan.ID, OrderedOrdinals: []int{1, 2},
		OperationKey: "batch-cumulative-merge-0001", RequestedBy: fixture.root.ID,
		WorktreeParent: t.TempDir(), Confirm: true})
	if apperror.CodeOf(err) != apperror.CodeConflict ||
		merged.Queue.Status != domain.BatchMergeQueueBlocked ||
		merged.Queue.FailureCode != "semantic_validation_failed" ||
		len(merged.Steps) != 2 ||
		merged.Steps[0].Status != domain.BatchMergeQueueCompleted ||
		merged.Steps[1].Status != domain.BatchMergeQueueBlocked ||
		merged.Queue.IntegrationHead != merged.Steps[0].PostMergeHead {
		t.Fatalf("cumulative merge=%#v err=%v", merged, err)
	}
	if _, statErr := os.Stat(filepath.Join(merged.Queue.IntegrationRoot,
		"internal", "two", "doc.go")); statErr != nil {
		t.Fatalf("integration root disappeared after rollback: %v", statErr)
	}
	got := fixtureGit(t, "-C", merged.Queue.IntegrationRoot, "show",
		"HEAD:internal/two/doc.go")
	if strings.Contains(got, `Value = "changed"`) {
		t.Fatalf("later delivery was not rolled back: %s", got)
	}
}

func TestBatchDeliveryMergePreservesValidationStateDriftForRecovery(t *testing.T) {
	fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionFull)
	fixture.spec.Tasks[0].Validations = append(fixture.spec.Tasks[0].Validations,
		domain.BatchDeliveryValidationRequirement{ID: "go-integration-state",
			Kind: domain.BatchValidationGoTest, Scope: "."})
	var err error
	fixture.spec, err = domain.NormalizeBatchDeliverySpec(fixture.spec)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := fixture.service.Prepare(t.Context(), PrepareBatchDeliveryRequest{
		RunID: fixture.run.ID, ProposalID: fixture.proposal.ID, Spec: fixture.spec,
		OperationKey: "batch-integration-state-prepare-01", RequestedBy: fixture.root.ID,
		WorktreeParent: t.TempDir(), Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	for index, child := range prepared.Workspaces {
		if _, _, _, err := fixture.service.SendMessage(t.Context(),
			SendBatchDeliveryMessageRequest{PlanID: prepared.Plan.ID, Ordinal: index + 1,
				Generation: 1, OwnerToken: prepared.Authorities[index].OwnerToken,
				Kind: domain.BatchMailboxAck, Summary: "integration state fixture acknowledged",
				OperationKey: "batch-integration-state-ack-00" + string(rune('1'+index))}); err != nil {
			t.Fatal(err)
		}
		relative := "internal/" + []string{"one", "two"}[index] + "/delivery.txt"
		if err := os.WriteFile(filepath.Join(child.WorktreeRoot, filepath.FromSlash(relative)),
			[]byte("delivery\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			testSource := `package one

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrationState(t *testing.T) {
	output, err := exec.Command("git", "branch", "--show-current").Output()
	if err != nil { t.Fatal(err) }
	if strings.Contains(strings.TrimSpace(string(output)), "/merge-") {
		if err := os.WriteFile(filepath.Join("..", "..", "merge-validation-drift.txt"),
			[]byte("preserve me"), 0600); err != nil { t.Fatal(err) }
	}
}
`
			if err := os.WriteFile(filepath.Join(child.WorktreeRoot,
				"internal", "one", "integration_state_test.go"), []byte(testSource), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		fixtureGit(t, "-C", child.WorktreeRoot, "add", "--", "internal")
		fixtureGit(t, "-C", child.WorktreeRoot, "commit", "--quiet", "-m",
			"integration state child "+string(rune('1'+index)))
		receipt, _, err := fixture.service.Submit(t.Context(), SubmitBatchDeliveryRequest{
			PlanID: prepared.Plan.ID, Ordinal: index + 1, Generation: 1,
			OwnerToken:   prepared.Authorities[index].OwnerToken,
			EvidenceRefs: []string{"test://integration-state/" + string(rune('1'+index))},
			Limitations:  []string{"validation state is independently attested"},
			OperationKey: "batch-integration-state-submit-00" + string(rune('1'+index))})
		if err != nil || len(receipt.TestReceipts) != 2-index {
			t.Fatalf("submit %d receipt=%#v err=%v", index+1, receipt, err)
		}
		if _, _, err := fixture.service.Review(t.Context(), ReviewBatchDeliveryRequest{
			PlanID: prepared.Plan.ID, Ordinal: index + 1, Generation: 1,
			Reviewer: fixture.root.ID, Verdict: domain.BatchReviewAccepted,
			Summary: "state-aware validation reviewed", FullDiffReviewed: true,
			CallChainReviewed: true, TestsReviewed: true,
			OperationKey: "batch-integration-state-review-00" + string(rune('1'+index))}); err != nil {
			t.Fatal(err)
		}
	}
	merged, err := fixture.service.Merge(t.Context(), MergeBatchDeliveryRequest{
		PlanID: prepared.Plan.ID, OrderedOrdinals: []int{1, 2},
		OperationKey: "batch-integration-state-merge-01", RequestedBy: fixture.root.ID,
		WorktreeParent: t.TempDir(), Confirm: true})
	if apperror.CodeOf(err) != apperror.CodeConflict ||
		merged.Queue.Status != domain.BatchMergeQueueBlocked ||
		merged.Queue.FailureCode != "validation_state_drift" || len(merged.Steps) != 0 {
		t.Fatalf("integration state drift merge=%#v err=%v", merged, err)
	}
	if _, statErr := os.Stat(filepath.Join(merged.Queue.IntegrationRoot,
		"merge-validation-drift.txt")); statErr != nil {
		t.Fatalf("uncertain validation state was not preserved: %v", statErr)
	}
	if head := fixtureGit(t, "-C", merged.Queue.IntegrationRoot, "rev-parse", "HEAD"); head == prepared.Plan.BaseCommit {
		t.Fatal("uncertain merged commit was silently rolled back")
	}
}

func prepareAcceptedBatchTextDeliveries(t *testing.T,
	fixture batchDeliveryApplicationFixture, prefix string,
) (PrepareBatchDeliveryResult, []domain.BatchDeliveryReceipt) {
	t.Helper()
	prepared, err := fixture.service.Prepare(t.Context(), PrepareBatchDeliveryRequest{
		RunID: fixture.run.ID, ProposalID: fixture.proposal.ID, Spec: fixture.spec,
		OperationKey: prefix + "-prepare-operation", RequestedBy: fixture.root.ID,
		WorktreeParent: t.TempDir(), Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	receipts := make([]domain.BatchDeliveryReceipt, len(prepared.Workspaces))
	for index, child := range prepared.Workspaces {
		ordinal := string(rune('1' + index))
		if _, _, _, err := fixture.service.SendMessage(t.Context(),
			SendBatchDeliveryMessageRequest{PlanID: prepared.Plan.ID, Ordinal: index + 1,
				Generation: 1, OwnerToken: prepared.Authorities[index].OwnerToken,
				Kind: domain.BatchMailboxAck, Summary: "accepted conflict fixture",
				OperationKey: prefix + "-ack-operation-" + ordinal}); err != nil {
			t.Fatal(err)
		}
		relative := "internal/" + []string{"one", "two"}[index] + "/delivery.txt"
		if err := os.WriteFile(filepath.Join(child.WorktreeRoot, filepath.FromSlash(relative)),
			[]byte("child "+ordinal+" version\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		fixtureGit(t, "-C", child.WorktreeRoot, "add", "--", relative)
		fixtureGit(t, "-C", child.WorktreeRoot, "-c", "user.name=batch-child",
			"-c", "user.email=child@example.invalid", "commit", "--quiet", "-m",
			"fixture child "+ordinal)
		receipt, _, err := fixture.service.Submit(t.Context(), SubmitBatchDeliveryRequest{
			PlanID: prepared.Plan.ID, Ordinal: index + 1, Generation: 1,
			OwnerToken:   prepared.Authorities[index].OwnerToken,
			EvidenceRefs: []string{"test://" + prefix + "/" + ordinal},
			Limitations:  []string{"none known"},
			OperationKey: prefix + "-submit-operation-" + ordinal})
		if err != nil {
			t.Fatal(err)
		}
		receipts[index] = receipt
		if _, _, err := fixture.service.Review(t.Context(), ReviewBatchDeliveryRequest{
			PlanID: prepared.Plan.ID, Ordinal: index + 1, Generation: 1,
			Reviewer: fixture.root.ID, Verdict: domain.BatchReviewAccepted,
			Summary: "accepted full diff", FullDiffReviewed: true,
			CallChainReviewed: true, TestsReviewed: true,
			OperationKey: prefix + "-review-operation-" + ordinal}); err != nil {
			t.Fatal(err)
		}
	}
	return prepared, receipts
}

func newBatchDeliveryApplicationFixture(t *testing.T,
	withDependency bool, mode domain.RunExecutionPermissionMode, projects ...*projectconfig.Effective,
) batchDeliveryApplicationFixture {
	return newBatchDeliveryApplicationFixtureWithWorker(t, withDependency, mode, nil, projects...)
}

func newBatchDeliveryApplicationFixtureWithWorker(t *testing.T,
	withDependency bool, mode domain.RunExecutionPermissionMode, worker *domain.WorkerProfile, projects ...*projectconfig.Effective,
) batchDeliveryApplicationFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	ctx := t.Context()
	repositoryRoot := t.TempDir()
	fixtureGit(t, "init", "--quiet", repositoryRoot)
	fixtureGit(t, "-C", repositoryRoot, "config", "user.name", "batch-fixture")
	fixtureGit(t, "-C", repositoryRoot, "config", "user.email", "batch@example.invalid")
	for _, directory := range []string{"internal/one", "internal/two"} {
		if err := os.MkdirAll(filepath.Join(repositoryRoot, filepath.FromSlash(directory)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repositoryRoot, filepath.FromSlash(directory),
			"base.txt"), []byte("base\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repositoryRoot, "go.mod"),
		[]byte("module example.invalid/batchfixture\n\ngo 1.25\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repositoryRoot, "internal", "one", "value.go"),
		[]byte("package one\n\nconst Value = \"base\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repositoryRoot, "internal", "two", "doc.go"),
		[]byte("package two\n\nconst Value = \"base\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", repositoryRoot, "add", "internal", "go.mod")
	fixtureGit(t, "-C", repositoryRoot, "commit", "--quiet", "-m", "base")
	database := filepath.Join(t.TempDir(), "batch-application.db")
	state, err := store.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	workspaceID := "batch-application-workspace"
	if err := state.SaveWorkspace(ctx, store.WorkspaceRecord{ID: workspaceID,
		Name: "batch application", RootPath: repositoryRoot, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	var project *projectconfig.Effective
	if len(projects) != 0 {
		project = projects[0]
	}
	_, run, err := NewRunService(state).Create(ctx, CreateRunRequest{
		ProjectConfig: project,
		Goal:          "deliver two isolated changes", Profile: "code", Surface: "code",
		Phase: "deliver", WorkspaceID: workspaceID,
		Budget: domain.Budget{MaxTurns: 20, MaxTokens: 100000, MaxToolCalls: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	hostCapabilities := domain.ExecutionPermissionRuntimeCapabilities{
		OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
		RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority(),
	}
	if _, err := NewRunExecutionProfileService(state).Change(ctx, ChangeRunExecutionProfileRequest{
		RunID: run.ID, Profile: string(domain.RunExecutionProfileLocal), OperationKey: "batch-application-local-profile", RequestedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	// Git-only batches retain the real initial Ask preference. Host Go/npm
	// validation opts into Full through the current writer and its live fence.
	if mode != domain.RunExecutionPermissionAsk {
		if _, err := NewRunExecutionPermissionService(state, hostCapabilities).Change(ctx,
			ChangeRunExecutionPermissionRequest{RunID: run.ID, Mode: string(mode),
				OperationKey: "batch-application-permission-0001", RequestedBy: "operator",
				Reason:      "exercise explicitly enabled host batch validation",
				ConfirmFull: mode == domain.RunExecutionPermissionFull}); err != nil {
			t.Fatal(err)
		}
	}
	permission, err := state.GetRunExecutionPermission(ctx, run.ID)
	if err != nil || permission.Mode != mode ||
		permission.ProtocolVersion != domain.RunApprovalPermissionProtocolVersion ||
		permission.ProcessEnabled || permission.ExecutionAuthorized || permission.CapabilityGrant {
		t.Fatalf("current batch permission=%#v err=%v", permission, err)
	}
	if mode == domain.RunExecutionPermissionFull {
		if generation, allowed := hostCapabilities.FullAccessGeneration(permission); !allowed || generation == 0 {
			t.Fatal("host validation fixture lacks a live Full runtime fence")
		}
	}
	run, err = NewRunService(state).Start(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	root, _, err := state.RegisterRootAgent(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	dependencies := []int(nil)
	if withDependency {
		dependencies = []int{1}
	}
	childSpec, err := domain.NormalizeChildTaskProposalSpec(domain.ChildTaskProposalSpec{
		Version: domain.ChildTaskProposalVersion, Tasks: []domain.ChildTask{
			{Title: "task one", Goal: "change internal one", Skills: []string{"model.chat"}, Worker: worker,
				SurfaceHint: domain.ChildTaskSurfaceHintCore, TurnLimit: 3, TokenLimit: 1024,
				TimeoutMillis:     120000,
				ExpectedArtifacts: []domain.ChildTaskExpectedArtifact{{PathHint: "internal/one", Kind: "code"}}},
			{Title: "task two", Goal: "change internal two", Skills: []string{"model.chat"}, Worker: worker,
				SurfaceHint:        domain.ChildTaskSurfaceHintCore,
				DependencyOrdinals: dependencies, TurnLimit: 3, TokenLimit: 1024,
				TimeoutMillis:     120000,
				ExpectedArtifacts: []domain.ChildTaskExpectedArtifact{{PathHint: "internal/two", Kind: "code"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	proposal := domain.ChildTaskProposal{ID: idgen.New("childtask"), RunID: run.ID,
		RootAgentID: root.ID, SessionID: run.SessionID, WorkspaceID: workspaceID,
		Status: domain.ChildTaskProposalProposed, Spec: childSpec,
		Surface: domain.ChildTaskSurfaceCore, RequestedBy: "run_supervisor", Version: 1,
		CreatedAt: now}
	operation := domain.ChildTaskOperation{
		KeyDigest: runmutation.OperationKeyDigest("child_task_propose", run.ID,
			"batch-application-proposal-0001"),
		RequestFingerprint: runmutation.Fingerprint("child_task_request.v1", run.ID,
			childSpec.SpecJSONFingerprint()), ProposalID: proposal.ID, RunID: run.ID,
		SessionID: run.SessionID, WorkspaceID: workspaceID, RootAgentID: root.ID,
		LeaseID: idgen.New("lease"), LeaseGeneration: 1, RequestedBy: "run_supervisor",
		CreatedAt: now,
	}
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
		FanoutTier: domain.ReadOnlyFanoutTwo}, "batch-application-review-0001"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := state.AdmitChildTaskProposal(ctx, proposal.ID,
		"batch-application-admit-0001"); err != nil {
		t.Fatal(err)
	}
	spec, err := domain.NormalizeBatchDeliverySpec(domain.BatchDeliverySpec{
		Version: domain.BatchDeliveryProtocolVersion,
		Tasks: []domain.BatchDeliveryTaskSpec{
			{Ordinal: 1, OwnershipHints: []domain.BatchDeliveryOwnershipHint{{Path: "internal/one", Kind: domain.BatchDeliveryOwnershipDirectory}},
				Budget:            domain.BatchDeliveryBudget{TurnLimit: 3, TokenLimit: 1024, TimeoutMillis: 120000},
				Validations:       []domain.BatchDeliveryValidationRequirement{{ID: "diff-one", Kind: domain.BatchValidationGitDiffCheck, Scope: "."}},
				ExpectedArtifacts: childSpec.Tasks[0].ExpectedArtifacts},
			{Ordinal: 2, OwnershipHints: []domain.BatchDeliveryOwnershipHint{{Path: "internal/two", Kind: domain.BatchDeliveryOwnershipDirectory}},
				DependencyOrdinals: dependencies,
				Budget:             domain.BatchDeliveryBudget{TurnLimit: 3, TokenLimit: 1024, TimeoutMillis: 120000},
				Validations:        []domain.BatchDeliveryValidationRequirement{{ID: "diff-two", Kind: domain.BatchValidationGitDiffCheck, Scope: "."}},
				ExpectedArtifacts:  childSpec.Tasks[1].ExpectedArtifacts},
		},
		Contract: domain.BatchDeliveryContract{RequireClean: true,
			RequireIndependentReview: true, RequireAllValidations: true,
			MaxChangedFiles: 32, MaxDiffBytes: 1024 * 1024},
	})
	if err != nil {
		t.Fatal(err)
	}
	return batchDeliveryApplicationFixture{store: state,
		service: NewBatchDeliveryService(state).WithHostValidationExecution(true,
			hostCapabilities),
		database: database, repository: repositoryRoot, caps: hostCapabilities,
		run: run, root: root, proposal: proposal, spec: spec}
}

func fixtureGit(t *testing.T, args ...string) string {
	t.Helper()
	output, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output))
}

func testErrorChain(err error) string {
	out := ""
	for err != nil {
		out += fmt.Sprintf("[%T] %v | ", err, err)
		err = errors.Unwrap(err)
	}
	return out
}
