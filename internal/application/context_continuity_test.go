package application_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/store"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestContinuityCheckpointForkAndRestartPreserveContextButNotAuthority(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("continuity fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Commit("initial", &git.CommitOptions{Author: &object.Signature{
		Name: "Continuity Test", Email: "continuity@example.invalid", When: time.Now().UTC(),
	}}); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(dir, "state.db")
	state, err := store.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	workspace := store.WorkspaceRecord{ID: "workspace-continuity", Name: "continuity",
		RootPath: dir, CreatedAt: time.Now().UTC()}
	if err := state.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	memoryService := application.NewContextMemoryService(state)
	memory, err := memoryService.Create(ctx, contextmgr.CreateMemoryRequest{
		Scope: contextmgr.MemoryScopeProject, ScopeID: workspace.ID,
		Title: "Verification preference", Content: "Run focused tests before the full suite.",
		RequestedBy: "desktop_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	instructions, err := projectconfig.DiscoverInstructions(ctx, dir, ".")
	if err != nil {
		t.Fatal(err)
	}
	_, run, err := application.NewRunService(state).Create(ctx, application.CreateRunRequest{
		Goal: "preserve bounded context", Profile: "code", WorkspaceID: workspace.ID, WebAccessMode: "full",
		Budget: domain.DefaultBudget(), RequestedBy: "desktop_operator",
		ProjectInstructions: &instructions,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.SaveSessionMessage(ctx, session.NewMessage(run.SessionID, "user",
		"Keep the public API stable.")); err != nil {
		t.Fatal(err)
	}
	if _, err := state.SaveSessionMessage(ctx, session.NewMessage(run.SessionID, "assistant",
		"I will verify compatibility.")); err != nil {
		t.Fatal(err)
	}
	service := application.NewContextContinuityService(state)
	checkpoint, err := service.Checkpoint(ctx, application.CreateContinuityCheckpointRequest{
		RunID: run.ID, Title: "Before API work", Summary: "Decision: preserve compatibility.",
		RequestedBy: "desktop_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.Snapshot.Authority != (contextmgr.ContinuityAuthority{}) ||
		len(checkpoint.Snapshot.Memories) != 1 || len(checkpoint.Snapshot.RecentMessages) != 2 {
		t.Fatalf("unexpected checkpoint snapshot: %#v", checkpoint.Snapshot)
	}
	branch, err := service.Branch(ctx, application.BranchContinuityRequest{
		SourceNodeID: checkpoint.ID, Kind: contextmgr.ContinuityNodeFork,
		Goal: "continue API work", RequestedBy: "desktop_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if branch.Run.ID == run.ID || branch.Run.SessionID == run.SessionID ||
		branch.Run.Config.ContinuityContextFingerprint != checkpoint.ContextSHA256 ||
		branch.CapabilityGrant || len(branch.NotInherited) < 8 || branch.Mission.Scope.WebAccessMode != domain.WebAccessFull || branch.Mission.Scope.NetworkMode != "disabled" {
		t.Fatalf("unsafe or incomplete branch result: %#v", branch)
	}
	tree, err := service.Tree(ctx, branch.Run.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if tree.CapabilityGrant || len(tree.Nodes) < 4 {
		t.Fatalf("branch-aware tree is incomplete: %#v", tree)
	}
	projected := false
	for _, node := range tree.Nodes {
		if node.ID == checkpoint.ID {
			projected = node.ProjectInstructionsFingerprint == instructions.Fingerprint &&
				node.GitBranch != "" && len(node.GitHead) == 40
		}
	}
	if !projected {
		t.Fatalf("checkpoint Git/config identity was not projected: %#v", tree.Nodes)
	}
	disabled := contextmgr.MemoryStatusDisabled
	if _, err := memoryService.Update(ctx, memory.ID, contextmgr.UpdateMemoryRequest{
		Status: &disabled, ExpectedVersion: memory.Version, RequestedBy: "desktop_operator",
	}); err != nil {
		t.Fatal(err)
	}
	tree, err = service.Tree(ctx, run.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	foundExpired := false
	for _, node := range tree.Nodes {
		foundExpired = foundExpired || node.Status == "memory_expired"
	}
	if !foundExpired {
		t.Fatalf("tree did not explain disabled memory: %#v", tree.Nodes)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = store.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	loaded, err := state.GetSessionContinuityNode(ctx, checkpoint.ID)
	if err != nil || loaded.ContextSHA256 != checkpoint.ContextSHA256 {
		t.Fatalf("checkpoint did not survive restart: %#v err=%v", loaded, err)
	}
}
