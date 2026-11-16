package application

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/gitadvanced"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

type gitAdvancedApplicationFixture struct {
	state        *store.SQLiteStore
	database     string
	service      *GitAdvancedService
	executor     *repository.AdvancedExecutor
	run          domain.Run
	lease        domain.RunExecutionLease
	root         string
	capabilities domain.ExecutionPermissionRuntimeCapabilities
}

type gitAdvancedStartBarrierStore struct {
	*store.SQLiteStore
	entered chan struct{}
	release <-chan struct{}
}

type gitAdvancedApprovalReviewer struct{ calls int }

func (r *gitAdvancedApprovalReviewer) Review(context.Context,
	toolgateway.ReviewRequest,
) (toolgateway.Outcome, error) {
	r.calls++
	return toolgateway.Outcome{}, nil
}

func (s *gitAdvancedStartBarrierStore) StartGitAdvancedOperation(ctx context.Context,
	id, approvalID, approvalFingerprint string, startedAt time.Time,
) (gitadvanced.OperationRecord, bool, error) {
	s.entered <- struct{}{}
	select {
	case <-s.release:
	case <-ctx.Done():
		return gitadvanced.OperationRecord{}, false, ctx.Err()
	}
	return s.SQLiteStore.StartGitAdvancedOperation(ctx, id, approvalID,
		approvalFingerprint, startedAt)
}

func newGitAdvancedApplicationFixture(t *testing.T, modes ...domain.RunExecutionPermissionMode) gitAdvancedApplicationFixture {
	t.Helper()
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "git-advanced.db")
	state, err := store.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	root := t.TempDir()
	runFixtureGit(t, "init", "--quiet", root)
	runFixtureGit(t, "-C", root, "config", "user.email", "test@example.com")
	runFixtureGit(t, "-C", root, "config", "user.name", "git-advanced-test")
	runFixtureGit(t, "-C", root, "config", "core.autocrlf", "false")
	content := "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\neleven\ntwelve\nthirteen\nfourteen\nfifteen\nsixteen\nseventeen\neighteen\nnineteen\ntwenty\n"
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, "-C", root, "add", "base.txt")
	runFixtureGit(t, "-C", root, "commit", "--quiet", "-m", "baseline")
	workspace := store.WorkspaceRecord{ID: "workspace-git-advanced", Name: "git-advanced",
		RootPath: root, CreatedAt: time.Now().UTC()}
	if err := state.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	runs := NewRunService(state)
	_, run, err := runs.Create(ctx, CreateRunRequest{Goal: "advanced Git fixture",
		Profile: "code", WorkspaceID: workspace.ID,
		Budget: domain.Budget{MaxTurns: 8, MaxTokens: 4000, MaxToolCalls: 20}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunExecutionProfileService(state).Change(ctx,
		ChangeRunExecutionProfileRequest{RunID: run.ID, Profile: "local",
			OperationKey: "git-advanced-profile", RequestedBy: "test_operator",
			Reason: "exercise typed Git mutations"}); err != nil {
		t.Fatal(err)
	}
	selected := domain.RunExecutionPermissionAsk
	if len(modes) != 0 {
		selected = modes[0]
	}
	capabilities := domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true,
		DangerFullAccessEnabled: true, RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
	if selected != domain.RunExecutionPermissionAsk {
		if _, err := NewRunExecutionPermissionService(state, capabilities).Change(ctx,
			ChangeRunExecutionPermissionRequest{RunID: run.ID,
				Mode: string(selected), OperationKey: "git-advanced-permission", RequestedBy: "test_operator",
				Reason: "require exact Git preview approval", ConfirmFull: selected == domain.RunExecutionPermissionFull}); err != nil {
			t.Fatal(err)
		}
	}
	run, err = runs.Start(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	leaseResult, err := state.AcquireRunExecutionLease(ctx,
		domain.AcquireRunExecutionLeaseRequest{RunID: run.ID,
			OwnerID: "git-advanced-test-worker", TTL: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := repository.NewAdvancedExecutor(filepath.Join(t.TempDir(), "managed"), true)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewGitAdvancedService(state, executor, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	return gitAdvancedApplicationFixture{state: state, database: database, service: service,
		executor: executor, run: run, lease: leaseResult.Lease,
		root: root, capabilities: capabilities}
}

func (f gitAdvancedApplicationFixture) scope() GitAdvancedScope {
	return GitAdvancedScope{CapabilityGeneration: f.executor.Capability().Generation,
		LeaseID: f.lease.LeaseID, LeaseGeneration: f.lease.Generation}
}

func executeGitAdvancedFixture(t *testing.T, fixture gitAdvancedApplicationFixture,
	operationKey string, spec gitadvanced.Spec,
) GitAdvancedExecuteResult {
	t.Helper()
	review, err := fixture.service.Review(t.Context(), GitAdvancedReviewRequest{
		ProtocolVersion: GitAdvancedAPIProtocolVersion, RunID: fixture.run.ID,
		OperationKey: operationKey, RequestedBy: "test_operator",
		Scope: fixture.scope(), Spec: spec})
	if err != nil || review.Operation == nil || review.Approval == nil ||
		!review.Preview.Executable() {
		t.Fatalf("review %s: %v %#v", spec.Operation, err, review)
	}
	decision, err := fixture.state.DecideApproval(t.Context(), approval.DecisionRequest{
		ProposalID: review.Operation.ID, IdempotencyKey: "approve-" + operationKey,
		Action: approval.ActionApprove, ReviewedBy: "test_operator"})
	if err != nil {
		t.Fatalf("approve %s: %v", spec.Operation, err)
	}
	result, err := fixture.service.Execute(t.Context(), GitAdvancedExecuteRequest{
		ProtocolVersion: GitAdvancedAPIProtocolVersion, RunID: fixture.run.ID,
		OperationID: review.Operation.ID, ApprovalID: decision.Approval.ID,
		RequestedBy: "test_operator", Scope: fixture.scope()})
	if err != nil || result.Receipt.Status != gitadvanced.ReceiptSucceeded {
		t.Fatalf("execute %s: %v %#v", spec.Operation, err, result)
	}
	return result
}

func TestGitAdvancedServiceRequiresApprovalCheckpointAndExactHunk(t *testing.T) {
	fixture := newGitAdvancedApplicationFixture(t)
	const relative = " base.txt"
	path := filepath.Join(fixture.root, relative)
	neighbor := filepath.Join(fixture.root, "base.txt")
	value, err := os.ReadFile(neighbor)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, value, 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, "-C", fixture.root, "--literal-pathspecs", "add", "--", relative)
	runFixtureGit(t, "-C", fixture.root, "commit", "--quiet", "-m", "exact path fixture")
	changed := strings.Replace(string(value), "two\n", "TWO\n", 1)
	changed = strings.Replace(changed, "nineteen\n", "NINETEEN\n", 1)
	if err := os.WriteFile(path, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	discoverySpec := gitadvanced.Spec{ProtocolVersion: gitadvanced.ProtocolVersion,
		Operation: gitadvanced.HunkStage, Paths: []string{relative}}
	discovery, err := fixture.service.DiscoverHunks(t.Context(), fixture.run.ID,
		discoverySpec)
	if err != nil || len(discovery.Preview.Hunks) != 2 || discovery.Operation != nil ||
		discovery.Approval != nil || discovery.Preview.Hunks[0].Path != relative {
		t.Fatalf("hunk discovery created mutation authority: %v %#v", err, discovery)
	}
	spec := discoverySpec
	spec.HunkIDs = []string{discovery.Preview.Hunks[0].ID}
	review, err := fixture.service.Review(t.Context(), GitAdvancedReviewRequest{
		ProtocolVersion: GitAdvancedAPIProtocolVersion, RunID: fixture.run.ID,
		OperationKey: "hunk-stage-review-1", RequestedBy: "test_operator",
		Scope: fixture.scope(), Spec: spec})
	if err != nil || review.Operation == nil || review.Approval == nil ||
		review.Approval.Status != approval.StatusPending ||
		!review.Preview.Recovery.Required {
		t.Fatalf("Git advanced review is incomplete: %v %#v", err, review)
	}
	if _, err := fixture.service.Execute(t.Context(), GitAdvancedExecuteRequest{
		ProtocolVersion: GitAdvancedAPIProtocolVersion, RunID: fixture.run.ID,
		OperationID: review.Operation.ID, ApprovalID: review.Approval.ID,
		RequestedBy: "test_operator", Scope: fixture.scope()}); err == nil {
		t.Fatal("pending Git advanced approval executed")
	}
	decision, err := fixture.state.DecideApproval(t.Context(), approval.DecisionRequest{
		ProposalID: review.Operation.ID, IdempotencyKey: "approve-hunk-stage-1",
		Action: approval.ActionApprove, ReviewedBy: "test_operator"})
	if err != nil || decision.Approval.Status != approval.StatusApproved {
		t.Fatalf("approve Git advanced preview: %v %#v", err, decision)
	}
	result, err := fixture.service.Execute(t.Context(), GitAdvancedExecuteRequest{
		ProtocolVersion: GitAdvancedAPIProtocolVersion, RunID: fixture.run.ID,
		OperationID: review.Operation.ID, ApprovalID: decision.Approval.ID,
		RequestedBy: "test_operator", Scope: fixture.scope()})
	if err != nil || result.Receipt.Status != gitadvanced.ReceiptSucceeded ||
		result.Receipt.CheckpointID == "" || result.Boundary.After == nil ||
		result.Operation.Status != gitadvanced.OperationSucceeded {
		t.Fatalf("approved Git advanced execution: %v %#v", err, result)
	}
	staged := runFixtureGit(t, "-C", fixture.root, "diff", "--cached")
	if !strings.Contains(staged, "TWO") || strings.Contains(staged, "NINETEEN") {
		t.Fatalf("approved exact hunk was not preserved: %s", staged)
	}
	names, err := exec.Command("git", "-C", fixture.root, "diff", "--cached", "--name-only", "-z").Output()
	if err != nil || string(names) != relative+"\x00" {
		t.Fatalf("approval staged a different path: %q (%v)", names, err)
	}
	replayed, err := fixture.service.Execute(t.Context(), GitAdvancedExecuteRequest{
		ProtocolVersion: GitAdvancedAPIProtocolVersion, RunID: fixture.run.ID,
		OperationID: review.Operation.ID, ApprovalID: decision.Approval.ID,
		RequestedBy: "test_operator", Scope: fixture.scope()})
	if err != nil || !replayed.Replayed || replayed.Receipt.ID != result.Receipt.ID {
		t.Fatalf("terminal Git advanced replay: %v %#v", err, replayed)
	}
}

func TestGitAdvancedApprovalControlApprovesExactStoredPreview(t *testing.T) {
	fixture := newGitAdvancedApplicationFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.root, "base.txt"),
		[]byte("approval control stash\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	review, err := fixture.service.Review(t.Context(), GitAdvancedReviewRequest{
		ProtocolVersion: GitAdvancedAPIProtocolVersion, RunID: fixture.run.ID,
		OperationKey: "approval-control-git-advanced-review", RequestedBy: "test_operator",
		Scope: fixture.scope(), Spec: gitadvanced.Spec{
			ProtocolVersion: gitadvanced.ProtocolVersion,
			Operation:       gitadvanced.StashCreate,
			Message:         "approval control exact preview",
		},
	})
	if err != nil || review.Operation == nil || review.Approval == nil {
		t.Fatalf("review Git advanced approval fixture: %v %#v", err, review)
	}
	reviewer := &gitAdvancedApprovalReviewer{}
	control := NewApprovalControlService(fixture.state, reviewer, policy.NewDefaultChecker())
	approved, err := control.Decide(t.Context(), DecideApprovalControlRequest{
		Version: ApprovalControlProtocolVersion, RunID: fixture.run.ID,
		ApprovalID: review.Approval.ID, Action: ApprovalControlApproveOnce,
		OperationKey: "approval-control-git-advanced-decision",
		ReviewedBy:   "desktop_operator",
	})
	if err != nil || approved.Approval.Status != approval.StatusApproved ||
		approved.Approval.RequestFingerprint != review.Operation.ApprovalFingerprint ||
		reviewer.calls != 0 {
		t.Fatalf("Git advanced approval control widened or rerouted authority: %v %#v calls=%d",
			err, approved, reviewer.calls)
	}
	result, err := fixture.service.Execute(t.Context(), GitAdvancedExecuteRequest{
		ProtocolVersion: GitAdvancedAPIProtocolVersion, RunID: fixture.run.ID,
		OperationID: review.Operation.ID, ApprovalID: approved.Approval.ID,
		RequestedBy: "test_operator", Scope: fixture.scope()})
	if err != nil || result.Receipt.Status != gitadvanced.ReceiptSucceeded ||
		result.Receipt.CheckpointID == "" {
		t.Fatalf("approval-control authorized Git execution: %v %#v", err, result)
	}
}

func TestGitAdvancedServiceRejectsLeaseAndRepositoryDriftAfterApproval(t *testing.T) {
	fixture := newGitAdvancedApplicationFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.root, "base.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := gitadvanced.Spec{ProtocolVersion: gitadvanced.ProtocolVersion,
		Operation: gitadvanced.StashCreate, Message: "drift fixture"}
	review, err := fixture.service.Review(t.Context(), GitAdvancedReviewRequest{
		ProtocolVersion: GitAdvancedAPIProtocolVersion, RunID: fixture.run.ID,
		OperationKey: "stash-drift-review", RequestedBy: "test_operator",
		Scope: fixture.scope(), Spec: spec})
	if err != nil || review.Operation == nil || review.Approval == nil {
		t.Fatalf("review stash: %v %#v", err, review)
	}
	decision, err := fixture.state.DecideApproval(t.Context(), approval.DecisionRequest{
		ProposalID: review.Operation.ID, IdempotencyKey: "approve-stash-drift",
		Action: approval.ActionApprove, ReviewedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.state.ReleaseRunExecutionLease(t.Context(), fixture.lease); err != nil {
		t.Fatal(err)
	}
	newLease, err := fixture.state.AcquireRunExecutionLease(t.Context(),
		domain.AcquireRunExecutionLeaseRequest{RunID: fixture.run.ID,
			OwnerID: "replacement-worker", TTL: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	newScope := GitAdvancedScope{CapabilityGeneration: fixture.executor.Capability().Generation,
		LeaseID: newLease.Lease.LeaseID, LeaseGeneration: newLease.Lease.Generation}
	if _, err := fixture.service.Execute(t.Context(), GitAdvancedExecuteRequest{
		ProtocolVersion: GitAdvancedAPIProtocolVersion, RunID: fixture.run.ID,
		OperationID: review.Operation.ID, ApprovalID: decision.Approval.ID,
		RequestedBy: "test_operator", Scope: newScope}); err == nil {
		t.Fatal("replacement lease executed a preview bound to an old generation")
	}
	if stashes := runFixtureGit(t, "-C", fixture.root, "stash", "list"); stashes != "" {
		t.Fatalf("stale lease mutated stash state: %s", stashes)
	}
}

func TestGitAdvancedServiceLetsOnlyStartCASWinnerInvokeGit(t *testing.T) {
	fixture := newGitAdvancedApplicationFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.root, "base.txt"),
		[]byte("concurrent stash\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	review, err := fixture.service.Review(t.Context(), GitAdvancedReviewRequest{
		ProtocolVersion: GitAdvancedAPIProtocolVersion, RunID: fixture.run.ID,
		OperationKey: "concurrent-stash-review", RequestedBy: "test_operator",
		Scope: fixture.scope(), Spec: gitadvanced.Spec{
			ProtocolVersion: gitadvanced.ProtocolVersion,
			Operation:       gitadvanced.StashCreate, Message: "concurrent start fence",
		},
	})
	if err != nil || review.Operation == nil || review.Approval == nil {
		t.Fatalf("review concurrent stash: %v %#v", err, review)
	}
	decision, err := fixture.state.DecideApproval(t.Context(), approval.DecisionRequest{
		ProposalID: review.Operation.ID, IdempotencyKey: "approve-concurrent-stash",
		Action: approval.ActionApprove, ReviewedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	barrier := &gitAdvancedStartBarrierStore{SQLiteStore: fixture.state,
		entered: make(chan struct{}, 2), release: release}
	service, err := NewGitAdvancedService(barrier, fixture.executor,
		fixture.capabilities, fixture.service.checkpoints)
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result GitAdvancedExecuteResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	request := GitAdvancedExecuteRequest{ProtocolVersion: GitAdvancedAPIProtocolVersion,
		RunID: fixture.run.ID, OperationID: review.Operation.ID,
		ApprovalID: decision.Approval.ID, RequestedBy: "test_operator", Scope: fixture.scope()}
	for index := 0; index < 2; index++ {
		go func() {
			result, executeErr := service.Execute(context.Background(), request)
			outcomes <- outcome{result: result, err: executeErr}
		}()
	}
	for index := 0; index < 2; index++ {
		select {
		case <-barrier.entered:
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent executions did not both reach the start CAS")
		}
	}
	close(release)
	results := []outcome{<-outcomes, <-outcomes}
	mutatingWinners, replays := 0, 0
	for _, result := range results {
		if result.result.Replayed {
			replays++
			if result.err != nil && !strings.Contains(result.err.Error(), "already began") {
				t.Fatalf("start loser returned an unrelated failure: %v", result.err)
			}
			continue
		}
		if result.err != nil || result.result.Receipt.Status != gitadvanced.ReceiptSucceeded {
			t.Fatalf("start winner failed: %v %#v", result.err, result.result)
		}
		mutatingWinners++
	}
	if mutatingWinners != 1 || replays != 1 {
		t.Fatalf("start CAS did not fence duplicate mutation: winners=%d replays=%d %#v",
			mutatingWinners, replays, results)
	}
	if stashes := strings.Fields(runFixtureGit(t, "-C", fixture.root,
		"stash", "list", "--format=%H")); len(stashes) != 1 {
		t.Fatalf("concurrent execution created %d stash mutations: %#v", len(stashes), stashes)
	}
}

func TestGitAdvancedBisectCanResetProtectedOriginalBranchAndRejectsExternalDrift(t *testing.T) {
	fixture := newGitAdvancedApplicationFixture(t)
	good := strings.TrimSpace(runFixtureGit(t, "-C", fixture.root, "rev-parse", "HEAD"))
	for index := 0; index < 4; index++ {
		if err := os.WriteFile(filepath.Join(fixture.root, "base.txt"),
			[]byte(strings.Repeat("candidate\n", index+1)), 0o600); err != nil {
			t.Fatal(err)
		}
		runFixtureGit(t, "-C", fixture.root, "add", "base.txt")
		runFixtureGit(t, "-C", fixture.root, "commit", "--quiet", "-m", "candidate")
	}
	bad := strings.TrimSpace(runFixtureGit(t, "-C", fixture.root, "rev-parse", "HEAD"))
	started := executeGitAdvancedFixture(t, fixture, "bisect-protected-start",
		gitadvanced.Spec{ProtocolVersion: gitadvanced.ProtocolVersion,
			Operation: gitadvanced.BisectStart, GoodCommit: good, BadCommit: bad})
	if started.Sequence == nil || started.Sequence.ID == "" {
		t.Fatalf("bisect start produced no durable sequence: %#v", started)
	}
	reset := gitadvanced.Spec{ProtocolVersion: gitadvanced.ProtocolVersion,
		Operation: gitadvanced.BisectReset, SequenceID: started.Sequence.ID}
	if preview, err := fixture.service.Preview(t.Context(), fixture.run.ID, reset); err != nil ||
		!preview.Preview.Executable() {
		t.Fatalf("protected original branch could not be reset: %v %#v", err,
			preview.Preview.BlockedReasons)
	}
	executeGitAdvancedFixture(t, fixture, "bisect-protected-reset", reset)
	if head := strings.TrimSpace(runFixtureGit(t, "-C", fixture.root,
		"rev-parse", "HEAD")); head != bad {
		t.Fatalf("bisect reset restored %s, want %s", head, bad)
	}

	started = executeGitAdvancedFixture(t, fixture, "bisect-drift-start",
		gitadvanced.Spec{ProtocolVersion: gitadvanced.ProtocolVersion,
			Operation: gitadvanced.BisectStart, GoodCommit: good, BadCommit: bad})
	runFixtureGit(t, "-C", fixture.root, "bisect", "good")
	reset.SequenceID = started.Sequence.ID
	if _, err := fixture.service.Preview(t.Context(), fixture.run.ID, reset); err == nil ||
		!strings.Contains(err.Error(), "durable Run binding") {
		t.Fatalf("externally advanced bisect state was adopted: %v", err)
	}
	runFixtureGit(t, "-C", fixture.root, "bisect", "reset")
}

func TestGitAdvancedManagedWorktreeRejectsCleanCommittedHeadDrift(t *testing.T) {
	fixture := newGitAdvancedApplicationFixture(t)
	head := strings.TrimSpace(runFixtureGit(t, "-C", fixture.root, "rev-parse", "HEAD"))
	create := gitadvanced.Spec{ProtocolVersion: gitadvanced.ProtocolVersion,
		Operation: gitadvanced.WorktreeCreate, WorktreeName: "head-drift",
		Branch: "codex/head-drift", Commit: head}
	created := executeGitAdvancedFixture(t, fixture, "worktree-head-drift-create", create)
	if created.Worktree == nil || created.Worktree.ID == "" {
		t.Fatalf("managed worktree was not registered: %#v", created)
	}
	path, err := fixture.executor.ManagedWorktreePath(created.Receipt.PostBinding,
		create.WorktreeName)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "external.txt"),
		[]byte("clean but unreviewed commit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, "-C", path, "add", "external.txt")
	runFixtureGit(t, "-C", path, "commit", "--quiet", "-m", "external head drift")
	remove := gitadvanced.Spec{ProtocolVersion: gitadvanced.ProtocolVersion,
		Operation: gitadvanced.WorktreeRemove, WorktreeID: created.Worktree.ID,
		WorktreeName: create.WorktreeName}
	if _, err := fixture.service.Preview(t.Context(), fixture.run.ID, remove); err == nil ||
		!strings.Contains(err.Error(), "registry binding changed") {
		t.Fatalf("clean committed worktree drift was accepted for removal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, "external.txt")); err != nil {
		t.Fatalf("unreviewed committed worktree content disappeared: %v", err)
	}
	runFixtureGit(t, "-C", fixture.root, "worktree", "remove", path)
}

func TestGitAdvancedProjectionRedactsManagedPathsAndShowsRecoveryState(t *testing.T) {
	fixture := newGitAdvancedApplicationFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.root, "base.txt"),
		[]byte("projection change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Review(t.Context(), GitAdvancedReviewRequest{
		ProtocolVersion: GitAdvancedAPIProtocolVersion, RunID: fixture.run.ID,
		OperationKey: "projection-audit-review", RequestedBy: "test_operator",
		Scope: fixture.scope(), Spec: gitadvanced.Spec{
			ProtocolVersion: gitadvanced.ProtocolVersion, Operation: gitadvanced.StashCreate,
			Message: "projection audit fixture",
		}}); err != nil {
		t.Fatal(err)
	}
	projection, err := fixture.service.Projection(t.Context(), fixture.run.ID, 50)
	if err != nil || projection.ProtocolVersion != GitAdvancedAPIProtocolVersion ||
		projection.Capability.Generation != fixture.scope().CapabilityGeneration ||
		projection.Authority.ProtocolVersion != "git-advanced-authority.v1" ||
		projection.Authority.Scope.CapabilityGeneration != fixture.scope().CapabilityGeneration ||
		projection.Authority.Scope.LeaseGeneration != fixture.lease.Generation ||
		!projection.Authority.LeaseActive || !projection.Authority.Executable ||
		projection.Binding.RepositorySHA256 == "" || projection.Worktrees == nil ||
		len(projection.Operations) != 1 || projection.Operations[0].Preview.ID == "" ||
		projection.Operations[0].Receipt != nil || projection.Conflict.Files == nil {
		t.Fatalf("Git advanced projection is incomplete: %v %#v", err, projection)
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	public := string(encoded)
	for _, secret := range []string{`"lease_id"`, `"spec_json"`, `"preview_json"`,
		`"receipt_json"`, filepath.ToSlash(fixture.root), fixture.lease.LeaseID} {
		if strings.Contains(public, secret) {
			t.Fatalf("Git advanced projection leaked %q: %s", secret, public)
		}
	}
}

func TestGitAdvancedStartupReconciliationPersistsInterruptedConflictWithoutReplay(t *testing.T) {
	fixture := newGitAdvancedApplicationFixture(t)
	originalBranch := strings.TrimSpace(runFixtureGit(t, "-C", fixture.root,
		"branch", "--show-current"))
	baseHead := strings.TrimSpace(runFixtureGit(t, "-C", fixture.root,
		"rev-parse", "HEAD"))
	runFixtureGit(t, "-C", fixture.root, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(fixture.root, "base.txt"),
		[]byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, "-C", fixture.root, "commit", "-qam", "feature change")
	featureHead := strings.TrimSpace(runFixtureGit(t, "-C", fixture.root,
		"rev-parse", "HEAD"))
	runFixtureGit(t, "-C", fixture.root, "checkout", "-q", originalBranch)
	if err := os.WriteFile(filepath.Join(fixture.root, "base.txt"),
		[]byte("main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, "-C", fixture.root, "commit", "-qam", "main change")
	ontoHead := strings.TrimSpace(runFixtureGit(t, "-C", fixture.root,
		"rev-parse", "HEAD"))
	runFixtureGit(t, "-C", fixture.root, "checkout", "-q", "feature")

	review, err := fixture.service.Review(t.Context(), GitAdvancedReviewRequest{
		ProtocolVersion: GitAdvancedAPIProtocolVersion, RunID: fixture.run.ID,
		OperationKey: "rebase-crash-review", RequestedBy: "test_operator",
		Scope: fixture.scope(), Spec: gitadvanced.Spec{
			ProtocolVersion: gitadvanced.ProtocolVersion, Operation: gitadvanced.RebaseStart,
			UpstreamOID: baseHead, OntoOID: ontoHead,
		},
	})
	if err != nil || review.Operation == nil || review.Approval == nil ||
		!review.Preview.Executable() {
		t.Fatalf("review rebase crash fixture: %v %#v", err, review)
	}
	decision, err := fixture.state.DecideApproval(t.Context(), approval.DecisionRequest{
		ProposalID: review.Operation.ID, IdempotencyKey: "approve-rebase-crash",
		Action: approval.ActionApprove, ReviewedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	boundaryRequest := WorkspaceMutationBoundaryRequest{RunID: fixture.run.ID,
		Kind: "git_mutation", OperationKey: review.Operation.OperationKeySHA256,
		TriggerReceiptID:     review.Operation.ID,
		CapabilityGeneration: review.Operation.CapabilityGeneration,
		LeaseID:              review.Operation.LeaseID, LeaseGeneration: review.Operation.LeaseGeneration,
		IncompleteReasons: advancedCheckpointLimitations(review.Operation.Operation)}
	if _, err := fixture.service.checkpoints.BeginBoundary(t.Context(), boundaryRequest); err != nil {
		t.Fatal(err)
	}
	running, _, err := fixture.state.StartGitAdvancedOperation(t.Context(),
		review.Operation.ID, decision.Approval.ID, review.Operation.ApprovalFingerprint,
		time.Now().UTC())
	if err != nil || running.Status != gitadvanced.OperationRunning {
		t.Fatalf("mark rebase running: %v %#v", err, running)
	}
	receipt, err := fixture.executor.ExecuteAdvanced(t.Context(), fixture.root,
		review.Preview)
	if err != nil || receipt.Status != gitadvanced.ReceiptConflicted ||
		receipt.SequenceID == "" {
		t.Fatalf("simulate crash after conflicted rebase: %v %#v", err, receipt)
	}

	// Retained legacy preferences remain readable during crash recovery even
	// though they cannot authorize another mutation.
	seedRetainedNativePermission(t, fixture.database, fixture.state, fixture.run.ID,
		domain.RunExecutionPermissionApproval)
	// A fresh process has a different capability generation. Reconciliation
	// observes state and terminalizes the old operation without invoking Git.
	restartedExecutor, err := repository.NewAdvancedExecutor(
		fixture.executor.ManagedRoot(), true)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewGitAdvancedService(fixture.state, restartedExecutor,
		fixture.capabilities, fixture.service.checkpoints)
	if err != nil {
		t.Fatal(err)
	}
	reconciled, err := restarted.ReconcileStartup(t.Context(), 100)
	if err != nil || reconciled.Examined != 1 || reconciled.Recovered != 1 ||
		reconciled.Conflicted != 1 {
		t.Fatalf("reconcile interrupted rebase: %v %#v", err, reconciled)
	}
	stored, found, err := fixture.state.GetGitAdvancedOperation(t.Context(), running.ID)
	if err != nil || !found || stored.Status != gitadvanced.OperationConflicted {
		t.Fatalf("interrupted operation not terminal: found=%t err=%v %#v", found, err, stored)
	}
	sequence, found, err := fixture.state.GetActiveGitAdvancedSequence(t.Context(),
		review.Preview.Binding.RepositorySHA256)
	if err != nil || !found || sequence.ID != receipt.SequenceID ||
		sequence.Status != gitadvanced.SequenceConflicted ||
		sequence.OriginalHead != featureHead {
		t.Fatalf("interrupted sequence not durable: found=%t err=%v %#v", found, err, sequence)
	}
	second, err := restarted.ReconcileStartup(t.Context(), 100)
	if err != nil || second.Examined != 0 || second.Recovered != 0 {
		t.Fatalf("reconciliation replayed terminal operation: %v %#v", err, second)
	}
}

func TestGitAdvancedStartupReconciliationTerminalizesCompletedSequenceWithoutClaimingSuccess(t *testing.T) {
	fixture := newGitAdvancedApplicationFixture(t)
	baseBranch := strings.TrimSpace(runFixtureGit(t, "-C", fixture.root,
		"branch", "--show-current"))
	runFixtureGit(t, "-C", fixture.root, "switch", "--quiet", "-c", "recovery-source")
	if err := os.WriteFile(filepath.Join(fixture.root, "picked.txt"),
		[]byte("picked before crash\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, "-C", fixture.root, "add", "picked.txt")
	runFixtureGit(t, "-C", fixture.root, "commit", "--quiet", "-m", "picked before crash")
	picked := strings.TrimSpace(runFixtureGit(t, "-C", fixture.root, "rev-parse", "HEAD"))
	runFixtureGit(t, "-C", fixture.root, "switch", "--quiet", baseBranch)
	runFixtureGit(t, "-C", fixture.root, "switch", "--quiet", "-c", "recovery-destination")

	spec := gitadvanced.Spec{ProtocolVersion: gitadvanced.ProtocolVersion,
		Operation: gitadvanced.CherryPickStart, Commits: []string{picked}}
	review, err := fixture.service.Review(t.Context(), GitAdvancedReviewRequest{
		ProtocolVersion: GitAdvancedAPIProtocolVersion, RunID: fixture.run.ID,
		OperationKey: "completed-sequence-crash-review", RequestedBy: "test_operator",
		Scope: fixture.scope(), Spec: spec})
	if err != nil || review.Operation == nil || review.Approval == nil ||
		!review.Preview.Executable() {
		t.Fatalf("review completed sequence crash fixture: %v %#v", err, review)
	}
	decision, err := fixture.state.DecideApproval(t.Context(), approval.DecisionRequest{
		ProposalID: review.Operation.ID, IdempotencyKey: "approve-completed-sequence-crash",
		Action: approval.ActionApprove, ReviewedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	boundaryRequest := WorkspaceMutationBoundaryRequest{RunID: fixture.run.ID,
		Kind:                 workspacecheckpoint.TransactionGitMutation,
		OperationKey:         review.Operation.OperationKeySHA256,
		TriggerReceiptID:     review.Operation.ID,
		CapabilityGeneration: review.Operation.CapabilityGeneration,
		LeaseID:              review.Operation.LeaseID,
		LeaseGeneration:      review.Operation.LeaseGeneration,
		IncompleteReasons:    advancedCheckpointLimitations(review.Operation.Operation)}
	if _, err := fixture.service.checkpoints.BeginBoundary(t.Context(), boundaryRequest); err != nil {
		t.Fatal(err)
	}
	running, _, err := fixture.state.StartGitAdvancedOperation(t.Context(),
		review.Operation.ID, decision.Approval.ID, review.Operation.ApprovalFingerprint,
		time.Now().UTC())
	if err != nil || running.Status != gitadvanced.OperationRunning {
		t.Fatalf("mark cherry-pick running: %v %#v", err, running)
	}
	executed, err := fixture.executor.ExecuteAdvanced(t.Context(), fixture.root,
		review.Preview)
	if err != nil || executed.Status != gitadvanced.ReceiptSucceeded ||
		executed.SequenceID == "" {
		t.Fatalf("simulate crash after completed cherry-pick: %v %#v", err, executed)
	}

	restartedExecutor, err := repository.NewAdvancedExecutor(
		fixture.executor.ManagedRoot(), true)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewGitAdvancedService(fixture.state, restartedExecutor,
		fixture.capabilities, fixture.service.checkpoints)
	if err != nil {
		t.Fatal(err)
	}
	reconciled, err := restarted.ReconcileStartup(t.Context(), 100)
	if err != nil || reconciled.Examined != 1 || reconciled.Recovered != 1 ||
		reconciled.Failed != 1 || reconciled.Conflicted != 0 {
		t.Fatalf("reconcile completed sequence crash: %v %#v", err, reconciled)
	}
	sequence, found, err := fixture.state.GetGitAdvancedSequence(t.Context(),
		executed.SequenceID)
	if err != nil || !found || sequence.Status != gitadvanced.SequenceFailed ||
		sequence.CompletedAt == nil || sequence.CurrentHead == review.Preview.Binding.Head {
		t.Fatalf("completed but uncommitted sequence did not fail closed: found=%t err=%v %#v",
			found, err, sequence)
	}
	if _, found, err := fixture.state.GetActiveGitAdvancedSequence(t.Context(),
		review.Preview.Binding.RepositorySHA256); err != nil || found {
		t.Fatalf("interrupted terminal sequence retained an active fence: found=%t err=%v",
			found, err)
	}
	stored, found, err := fixture.state.GetGitAdvancedOperation(t.Context(), running.ID)
	if err != nil || !found || stored.Status != gitadvanced.OperationFailed {
		t.Fatalf("interrupted operation claimed success: found=%t err=%v %#v", found, err, stored)
	}
}

func TestGitAdvancedStartupReconciliationAcceptsAlreadyPersistedTerminalSequence(t *testing.T) {
	fixture := newGitAdvancedApplicationFixture(t)
	originalBranch := strings.TrimSpace(runFixtureGit(t, "-C", fixture.root,
		"branch", "--show-current"))
	base := strings.TrimSpace(runFixtureGit(t, "-C", fixture.root, "rev-parse", "HEAD"))
	runFixtureGit(t, "-C", fixture.root, "switch", "--quiet", "-c", "persisted-sequence-feature")
	if err := os.WriteFile(filepath.Join(fixture.root, "base.txt"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, "-C", fixture.root, "commit", "-qam", "feature conflict")
	runFixtureGit(t, "-C", fixture.root, "switch", "--quiet", originalBranch)
	if err := os.WriteFile(filepath.Join(fixture.root, "base.txt"), []byte("main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, "-C", fixture.root, "commit", "-qam", "main conflict")
	onto := strings.TrimSpace(runFixtureGit(t, "-C", fixture.root, "rev-parse", "HEAD"))
	runFixtureGit(t, "-C", fixture.root, "switch", "--quiet", "persisted-sequence-feature")

	startReview, err := fixture.service.Review(t.Context(), GitAdvancedReviewRequest{
		ProtocolVersion: GitAdvancedAPIProtocolVersion, RunID: fixture.run.ID,
		OperationKey: "persisted-sequence-start", RequestedBy: "test_operator",
		Scope: fixture.scope(), Spec: gitadvanced.Spec{ProtocolVersion: gitadvanced.ProtocolVersion,
			Operation: gitadvanced.RebaseStart, UpstreamOID: base, OntoOID: onto}})
	if err != nil || startReview.Operation == nil || startReview.Approval == nil {
		t.Fatalf("review persisted sequence start: %v %#v", err, startReview)
	}
	startDecision, err := fixture.state.DecideApproval(t.Context(), approval.DecisionRequest{
		ProposalID: startReview.Operation.ID, IdempotencyKey: "approve-persisted-sequence-start",
		Action: approval.ActionApprove, ReviewedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := fixture.service.Execute(t.Context(), GitAdvancedExecuteRequest{
		ProtocolVersion: GitAdvancedAPIProtocolVersion, RunID: fixture.run.ID,
		OperationID: startReview.Operation.ID, ApprovalID: startDecision.Approval.ID,
		RequestedBy: "test_operator", Scope: fixture.scope()})
	if err != nil || started.Receipt.Status != gitadvanced.ReceiptConflicted ||
		started.Sequence == nil {
		t.Fatalf("start persisted sequence fixture: %v %#v", err, started)
	}

	abortReview, err := fixture.service.Review(t.Context(), GitAdvancedReviewRequest{
		ProtocolVersion: GitAdvancedAPIProtocolVersion, RunID: fixture.run.ID,
		OperationKey: "persisted-sequence-abort", RequestedBy: "test_operator",
		Scope: fixture.scope(), Spec: gitadvanced.Spec{ProtocolVersion: gitadvanced.ProtocolVersion,
			Operation: gitadvanced.RebaseAbort, SequenceID: started.Sequence.ID}})
	if err != nil || abortReview.Operation == nil || abortReview.Approval == nil {
		t.Fatalf("review persisted sequence abort: %v %#v", err, abortReview)
	}
	abortDecision, err := fixture.state.DecideApproval(t.Context(), approval.DecisionRequest{
		ProposalID: abortReview.Operation.ID, IdempotencyKey: "approve-persisted-sequence-abort",
		Action: approval.ActionApprove, ReviewedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	boundaryRequest := WorkspaceMutationBoundaryRequest{RunID: fixture.run.ID,
		Kind:                 workspacecheckpoint.TransactionGitMutation,
		OperationKey:         abortReview.Operation.OperationKeySHA256,
		TriggerReceiptID:     abortReview.Operation.ID,
		CapabilityGeneration: abortReview.Operation.CapabilityGeneration,
		LeaseID:              abortReview.Operation.LeaseID, LeaseGeneration: abortReview.Operation.LeaseGeneration,
		IncompleteReasons: advancedCheckpointLimitations(abortReview.Operation.Operation)}
	if _, err := fixture.service.checkpoints.BeginBoundary(t.Context(), boundaryRequest); err != nil {
		t.Fatal(err)
	}
	running, _, err := fixture.state.StartGitAdvancedOperation(t.Context(),
		abortReview.Operation.ID, abortDecision.Approval.ID,
		abortReview.Operation.ApprovalFingerprint, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := fixture.executor.ExecuteAdvanced(t.Context(), fixture.root,
		abortReview.Preview)
	if err != nil || receipt.Status != gitadvanced.ReceiptSucceeded {
		t.Fatalf("execute persisted sequence abort: %v %#v", err, receipt)
	}
	authority, err := fixture.service.loadMutationAuthority(t.Context(), fixture.run.ID,
		fixture.scope())
	if err != nil {
		t.Fatal(err)
	}
	sequence, _, err := fixture.service.persistGitAdvancedState(t.Context(), authority,
		running, abortReview.Preview, receipt)
	if err != nil || sequence == nil || sequence.Status != gitadvanced.SequenceAborted {
		t.Fatalf("persist terminal sequence before simulated crash: %v %#v", err, sequence)
	}
	generation := sequence.Generation

	restartedExecutor, err := repository.NewAdvancedExecutor(fixture.executor.ManagedRoot(), true)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewGitAdvancedService(fixture.state, restartedExecutor,
		fixture.capabilities, fixture.service.checkpoints)
	if err != nil {
		t.Fatal(err)
	}
	reconciled, err := restarted.ReconcileStartup(t.Context(), 100)
	if err != nil || reconciled.Recovered != 1 || reconciled.Failed != 1 {
		t.Fatalf("reconcile already-persisted terminal sequence: %v %#v", err, reconciled)
	}
	storedSequence, found, err := fixture.state.GetGitAdvancedSequence(t.Context(), sequence.ID)
	if err != nil || !found || storedSequence.Status != gitadvanced.SequenceAborted ||
		storedSequence.Generation != generation {
		t.Fatalf("reconciliation rewrote already-persisted terminal state: found=%t err=%v %#v",
			found, err, storedSequence)
	}
	storedOperation, found, err := fixture.state.GetGitAdvancedOperation(t.Context(), running.ID)
	if err != nil || !found || storedOperation.Status != gitadvanced.OperationFailed {
		t.Fatalf("interrupted operation claimed success: found=%t err=%v %#v",
			found, err, storedOperation)
	}
}

func TestGitAdvancedStartupReconciliationAdoptsExactCreatedWorktreeWithoutClaimingSuccess(t *testing.T) {
	fixture := newGitAdvancedApplicationFixture(t)
	head := strings.TrimSpace(runFixtureGit(t, "-C", fixture.root, "rev-parse", "HEAD"))
	spec := gitadvanced.Spec{ProtocolVersion: gitadvanced.ProtocolVersion,
		Operation: gitadvanced.WorktreeCreate, WorktreeName: "crash-recovery",
		Branch: "codex/crash-recovery", Commit: head}
	review, err := fixture.service.Review(t.Context(), GitAdvancedReviewRequest{
		ProtocolVersion: GitAdvancedAPIProtocolVersion, RunID: fixture.run.ID,
		OperationKey: "worktree-crash-review", RequestedBy: "test_operator",
		Scope: fixture.scope(), Spec: spec})
	if err != nil || review.Operation == nil || review.Approval == nil ||
		!review.Preview.Executable() {
		t.Fatalf("review worktree crash fixture: %v %#v", err, review)
	}
	decision, err := fixture.state.DecideApproval(t.Context(), approval.DecisionRequest{
		ProposalID: review.Operation.ID, IdempotencyKey: "approve-worktree-crash",
		Action: approval.ActionApprove, ReviewedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	boundaryRequest := WorkspaceMutationBoundaryRequest{RunID: fixture.run.ID,
		Kind:                 workspacecheckpoint.TransactionGitMutation,
		OperationKey:         review.Operation.OperationKeySHA256,
		TriggerReceiptID:     review.Operation.ID,
		CapabilityGeneration: review.Operation.CapabilityGeneration,
		LeaseID:              review.Operation.LeaseID,
		LeaseGeneration:      review.Operation.LeaseGeneration,
		IncompleteReasons:    advancedCheckpointLimitations(review.Operation.Operation)}
	if _, err := fixture.service.checkpoints.BeginBoundary(t.Context(), boundaryRequest); err != nil {
		t.Fatal(err)
	}
	running, _, err := fixture.state.StartGitAdvancedOperation(t.Context(),
		review.Operation.ID, decision.Approval.ID, review.Operation.ApprovalFingerprint,
		time.Now().UTC())
	if err != nil || running.Status != gitadvanced.OperationRunning {
		t.Fatalf("mark worktree create running: %v %#v", err, running)
	}
	executed, err := fixture.executor.ExecuteAdvanced(t.Context(), fixture.root,
		review.Preview)
	if err != nil || executed.Status != gitadvanced.ReceiptSucceeded ||
		executed.WorktreeID == "" {
		t.Fatalf("simulate crash after worktree create: %v %#v", err, executed)
	}

	restartedExecutor, err := repository.NewAdvancedExecutor(
		fixture.executor.ManagedRoot(), true)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewGitAdvancedService(fixture.state, restartedExecutor,
		fixture.capabilities, fixture.service.checkpoints)
	if err != nil {
		t.Fatal(err)
	}
	reconciled, err := restarted.ReconcileStartup(t.Context(), 100)
	if err != nil || reconciled.Examined != 1 || reconciled.Recovered != 1 ||
		reconciled.Failed != 1 {
		t.Fatalf("reconcile interrupted worktree create: %v %#v", err, reconciled)
	}
	registered, found, err := fixture.state.GetManagedGitWorktreeByName(t.Context(),
		review.Preview.Binding.CommonDirSHA256, spec.WorktreeName)
	if err != nil || !found || !registered.Present || registered.Head != spec.Commit ||
		registered.Branch != spec.Branch || registered.CreatedOperationID != running.ID {
		t.Fatalf("exact created worktree was not recovered: found=%t err=%v %#v",
			found, err, registered)
	}
	stored, found, err := fixture.state.GetGitAdvancedOperation(t.Context(), running.ID)
	if err != nil || !found || stored.Status != gitadvanced.OperationFailed {
		t.Fatalf("interrupted worktree operation did not fail closed: found=%t err=%v %#v",
			found, err, stored)
	}
	var receipt gitadvanced.Receipt
	if err := json.Unmarshal([]byte(stored.ReceiptJSON), &receipt); err != nil ||
		receipt.WorktreeID != registered.ID ||
		receipt.ErrorCode != gitadvanced.FailureInterrupted ||
		!strings.Contains(receipt.ErrorSummary, "recovered into the durable registry") {
		t.Fatalf("recovered worktree receipt claimed the wrong outcome: %v %#v", err, receipt)
	}
}

func TestGitAdvancedPruneRejectsUnregisteredManagedRootEntry(t *testing.T) {
	fixture := newGitAdvancedApplicationFixture(t)
	binding, err := fixture.executor.CaptureAdvancedBinding(t.Context(), fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fixture.executor.ManagedRoot(),
		binding.CommonDirSHA256[:16], "unregistered")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, "-C", fixture.root, "worktree", "add", "-q", "-b",
		"codex/unregistered-prune", path, binding.Head)
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	_, err = fixture.service.Review(t.Context(), GitAdvancedReviewRequest{
		ProtocolVersion: GitAdvancedAPIProtocolVersion, RunID: fixture.run.ID,
		OperationKey: "unregistered-prune-review", RequestedBy: "test_operator",
		Scope: fixture.scope(), Spec: gitadvanced.Spec{
			ProtocolVersion: gitadvanced.ProtocolVersion,
			Operation:       gitadvanced.WorktreePrune,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "product registry") {
		t.Fatalf("unregistered stale worktree was accepted: %v", err)
	}
}
