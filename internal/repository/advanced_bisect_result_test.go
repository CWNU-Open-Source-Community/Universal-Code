package repository

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/gitadvanced"
	"cyberagent-workbench/internal/runner"
)

type bisectRegressionExecutor struct{}

func TestBisectFirstBadCommitCompletionHeader(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for _, tc := range []struct {
		name, output, want string
	}{
		{"git-before-2.55", commit + " is the first bad commit\ncommit " + commit, commit},
		{"git-2.55", commit + " is the first 'bad' commit\ncommit " + commit, commit},
		{"windows-newlines", commit + " is the first 'bad' commit\r\n", commit},
		{"sha256-repository", strings.Repeat("b", 64) + " is the first 'bad' commit\n", strings.Repeat("b", 64)},
		{"progress-subject", "Bisecting: 1 revision left to test\n[" + commit + "] is the first bad commit\n", ""},
		{"commit-body", "Bisecting: 0 revisions left to test\n" + commit + " is the first 'bad' commit\n", ""},
		{"invalid-object", "not-an-oid is the first bad commit\n", ""},
		{"different-term", commit + " is the first 'new' commit\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := bisectFirstBadCommit(tc.output); got != tc.want {
				t.Fatalf("completion header returned %q; want %q", got, tc.want)
			}
		})
	}
}

func (bisectRegressionExecutor) ExecuteBisectRecipe(ctx context.Context, step BisectRecipeStep) (runner.OnceStartResult, gitadvanced.BisectExecutionReceipt, error) {
	result, receipt, err := (bisectFixtureExecutor{}).ExecuteBisectRecipe(ctx, step)
	content, readErr := os.ReadFile(filepath.Join(step.Root, "base.txt"))
	if readErr != nil {
		return result, receipt, readErr
	}
	if strings.HasPrefix(string(content), "bad") {
		result.ExitCode = 1
	}
	return result, receipt, err
}

func TestAdvancedBisectReportsFirstBadWhenHeadRemainsOnLastGood(t *testing.T) {
	root := newMutationRepo(t)
	good := fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	var firstBad, lastGood string
	for i, content := range []string{"good candidate\n", "bad regression\n", "bad later\n", "bad latest\n"} {
		if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		fixtureGit(t, "-C", root, "add", "base.txt")
		fixtureGit(t, "-C", root, "commit", "--quiet", "-m", "candidate")
		if i == 0 {
			lastGood = fixtureGit(t, "-C", root, "rev-parse", "HEAD")
		}
		if i == 1 {
			firstBad = fixtureGit(t, "-C", root, "rev-parse", "HEAD")
		}
	}
	executor := newAdvancedExecutor(t).WithBisectRecipeExecutor(bisectRegressionExecutor{})
	start := advancedSpec(gitadvanced.BisectStart)
	start.GoodCommit, start.BadCommit = good, fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	preview, err := executor.ReviewAdvanced(t.Context(), root, start)
	if err != nil {
		t.Fatal(err)
	}
	started, err := executor.ExecuteAdvanced(t.Context(), root, preview)
	if err != nil {
		t.Fatal(err)
	}
	run := advancedSpec(gitadvanced.BisectRun)
	run.SequenceID, run.ExpectedCurrent = started.SequenceID, fixtureGit(t, "-C", root, "rev-parse", "HEAD")
	run.Recipe = &gitadvanced.BisectRecipe{Name: gitadvanced.RecipeGoTest, MaxSteps: 8, TimeoutSeconds: 30}
	preview, err = executor.ReviewAdvanced(t.Context(), root, run)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := executor.ExecuteAdvanced(t.Context(), root, preview)
	if err != nil || receipt.Status != gitadvanced.ReceiptSucceeded || receipt.TargetOID != firstBad ||
		receipt.PostBinding.Head != lastGood || len(receipt.BisectExecutions) != 2 {
		t.Fatalf("bisect returned checkout instead of proven regression: %+v %v", receipt, err)
	}
}
