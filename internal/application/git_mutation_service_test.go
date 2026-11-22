package application

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/store"
)

func runFixtureGit(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	hideSubprocessWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output))
}

func newGitMutationFixture(t *testing.T) (*store.SQLiteStore, string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "git-mutation.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	repoRoot := t.TempDir()
	runFixtureGit(t, "init", "--quiet", repoRoot)
	runFixtureGit(t, "-C", repoRoot, "config", "user.email", "test@example.com")
	runFixtureGit(t, "-C", repoRoot, "config", "user.name", "mutation-test")
	if err := os.WriteFile(filepath.Join(repoRoot, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, "-C", repoRoot, "add", "base.txt")
	runFixtureGit(t, "-C", repoRoot, "commit", "--quiet", "-m", "baseline")
	if err := st.SaveWorkspace(context.Background(), store.WorkspaceRecord{
		ID: "ws-1", Name: "git", RootPath: repoRoot, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	_, run, err := NewRunService(st).Create(context.Background(), CreateRunRequest{
		Goal: "git mutation", Profile: "code", Surface: "code", Phase: "plan",
		WorkspaceID: "ws-1", Budget: domain.Budget{MaxTurns: 5, MaxToolCalls: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	return st, run.ID, repoRoot
}

func TestGitMutationServiceCommitEndToEnd(t *testing.T) {
	ctx := context.Background()
	st, runID, repoRoot := newGitMutationFixture(t)
	executor, err := repository.NewMutationExecutor()
	if err != nil {
		t.Fatal(err)
	}
	service := NewGitMutationService(st, executor)
	if err := os.WriteFile(filepath.Join(repoRoot, "new.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stageSpec := repository.MutationSpec{ProtocolVersion: repository.MutationProtocolVersion,
		Operation: repository.MutationStage, Paths: []string{"new.txt"}}
	stageReview, err := service.Review(ctx, runID, stageSpec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Execute(ctx, GitMutationRequest{RunID: runID, OperationKey: "git-op-key-0001",
		Spec: stageSpec}, stageReview.Review.Binding); err != nil {
		t.Fatal(err)
	}
	commitSpec := repository.MutationSpec{ProtocolVersion: repository.MutationProtocolVersion,
		Operation: repository.MutationCommit, Paths: []string{"new.txt"}, Message: "add new"}
	commitReview, err := service.Review(ctx, runID, commitSpec)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Execute(ctx, GitMutationRequest{RunID: runID, OperationKey: "git-op-key-0002",
		Spec: commitSpec}, commitReview.Review.Binding)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if result.Record.CommitID == "" || result.Record.CommitID == result.Record.PreHead ||
		result.Record.Clean == false {
		t.Fatalf("commit record invalid: %#v", result.Record)
	}
	// Idempotent replay of the same operation key returns the stored record.
	replayed, err := service.Execute(ctx, GitMutationRequest{RunID: runID, OperationKey: "git-op-key-0002",
		Spec: commitSpec}, commitReview.Review.Binding)
	if err != nil || !replayed.Replayed || replayed.Record.ID != result.Record.ID {
		t.Fatalf("replay failed: %#v err=%v", replayed, err)
	}
	// Audit event distinguishes Go-verified facts.
	timeline, err := st.ListRunEvents(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range timeline {
		if event.Type == "git.mutation_completed" && event.Source == "git_mutation_runner" {
			found = true
		}
	}
	if !found {
		t.Fatal("git.mutation_completed audit event missing")
	}
}

func TestGitMutationServiceStaleReviewRejectedEndToEnd(t *testing.T) {
	ctx := context.Background()
	st, runID, repoRoot := newGitMutationFixture(t)
	executor, err := repository.NewMutationExecutor()
	if err != nil {
		t.Fatal(err)
	}
	service := NewGitMutationService(st, executor)
	if err := os.WriteFile(filepath.Join(repoRoot, "x.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := repository.MutationSpec{ProtocolVersion: repository.MutationProtocolVersion,
		Operation: repository.MutationStage, Paths: []string{"x.txt"}}
	review, err := service.Review(ctx, runID, spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "x.txt"), []byte("drifted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = service.Execute(ctx, GitMutationRequest{RunID: runID, OperationKey: "git-op-key-0003",
		Spec: spec}, review.Review.Binding)
	if err == nil || !strings.Contains(err.Error(), "re-review") {
		t.Fatalf("stale review executed end-to-end: %v", err)
	}
	replayed, err := service.Execute(ctx, GitMutationRequest{RunID: runID,
		OperationKey: "git-op-key-0003", Spec: spec}, review.Review.Binding)
	if err == nil || apperror.CodeOf(err) != apperror.CodeFailedPrecondition ||
		!replayed.Replayed || replayed.Record.CompletedAt != nil ||
		!strings.Contains(err.Error(), "reconcile repository state") {
		t.Fatalf("pending mutation replay reported success: result=%#v err=%v", replayed, err)
	}
}
