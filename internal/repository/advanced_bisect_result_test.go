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
