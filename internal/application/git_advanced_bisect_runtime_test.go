package application

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/gitadvanced"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/runner"
)

func TestGitAdvancedBisectExecutionErrorHidesBackendOutput(t *testing.T) {
	if err := gitBisectExecutionError(errors.New("private recipe output")); strings.Contains(err.Error(), "private") {
		t.Fatal("backend output leaked into the Git receipt error")
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		if !errors.Is(gitBisectExecutionError(errors.Join(cause, errors.New("private recipe output"))), cause) {
			t.Fatal("cancellation classification was lost")
		}
	}
}

func TestGitAdvancedBisectHostEnvironmentIsReplaced(t *testing.T) {
	t.Setenv("GOCACHE", filepath.Join(t.TempDir(), "ambient-cache"))
	resolved := runner.CommandRuntimeResolvedSpec{Environment: []string{"HOME=ambient-home", "GOCACHE=ambient-cache"}}
	if err := gitBisectHostEnvironment(&resolved); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(os.TempDir(), "cyberagent-git-bisect-cache")
	var home, cache string
	for _, value := range resolved.Environment {
		if strings.HasPrefix(value, "HOME=") {
			home = strings.TrimPrefix(value, "HOME=")
		}
		if strings.HasPrefix(value, "GOCACHE=") {
			cache = strings.TrimPrefix(value, "GOCACHE=")
		}
	}
	if home != os.TempDir() || cache != filepath.Join(root, "go-build") || resolved.EnvironmentSHA256 == "" {
		t.Fatalf("host validation environment was inherited: %v", resolved.Environment)
	}
}

func prepareGitBisectRuntime(t *testing.T, f *commandApprovalSandboxFixture) (gitAdvancedApplicationFixture, gitadvanced.Spec) {
	t.Helper()
	executor, err := repository.NewAdvancedExecutor(filepath.Join(t.TempDir(), "managed"), true)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewGitAdvancedService(f.st, executor, f.caps)
	if err != nil {
		t.Fatal(err)
	}
	service.WithBisectCommandRuntime(f.mux)
	lease, _, err := f.st.GetRunExecutionLease(t.Context(), f.turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture := gitAdvancedApplicationFixture{state: f.st, service: service, executor: executor, run: f.turn.Run,
		lease: lease, root: f.root, capabilities: f.caps}
	good := strings.TrimSpace(runFixtureGit(t, "-C", f.root, "rev-parse", "HEAD"))
	for i := 0; i < 4; i++ {
		if err := os.WriteFile(filepath.Join(f.root, "tracked.txt"), []byte(strings.Repeat("candidate\n", i+1)), 0600); err != nil {
			t.Fatal(err)
		}
		runFixtureGit(t, "-C", f.root, "add", "tracked.txt")
		runFixtureGit(t, "-C", f.root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--quiet", "-m", "candidate")
	}
	bad := strings.TrimSpace(runFixtureGit(t, "-C", f.root, "rev-parse", "HEAD"))
	started := executeGitAdvancedFixture(t, fixture, "bisect-runtime-start", gitadvanced.Spec{ProtocolVersion: gitadvanced.ProtocolVersion,
		Operation: gitadvanced.BisectStart, GoodCommit: good, BadCommit: bad})
	return fixture, gitadvanced.Spec{ProtocolVersion: gitadvanced.ProtocolVersion, Operation: gitadvanced.BisectRun,
		SequenceID: started.Sequence.ID, ExpectedCurrent: started.Sequence.CurrentHead,
		Recipe: &gitadvanced.BisectRecipe{Name: gitadvanced.RecipeGoTest, MaxSteps: 8, TimeoutSeconds: 120}}
}

func TestGitAdvancedBisectSelectedSandboxReceipts(t *testing.T) {
	for _, profile := range []domain.RunExecutionProfile{domain.RunExecutionProfileDocker, domain.RunExecutionProfileSBX} {
		t.Run(string(profile), func(t *testing.T) {
			f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionAsk, profile, true, true)
			var sbx *batchSBXBackend
			var docker *batchDockerTargetTransport
			backend := CommandRuntimeDockerSandboxBackend
			if profile == domain.RunExecutionProfileSBX {
				sbx = installBatchSBX(t, f)
				backend = CommandRuntimeSBXBackend
			} else {
				executor := f.service.sandbox.(*DockerSandboxCommandRuntimeExecutor)
				docker = &batchDockerTargetTransport{dockerLifecycleSupervisorTestTransport: executor.service.docker.lifecycleTransport.(*dockerLifecycleSupervisorTestTransport)}
				executor.service.docker.lifecycleTransport = docker
			}
			fixture, spec := prepareGitBisectRuntime(t, f)
			result := executeGitAdvancedFixture(t, fixture, "bisect-runtime-recipe", spec)
			if len(result.Receipt.BisectExecutions) < 2 {
				t.Fatalf("missing revision receipts: %+v", result.Receipt)
			}
			seen := map[string]bool{}
			for ordinal, step := range result.Receipt.BisectExecutions {
				execution := step.Execution
				if step.Ordinal != ordinal || step.Commit == "" || seen[step.Commit] || execution.Backend != backend || execution.JobID == "" || !execution.TreeReaped || !execution.StdinClosed {
					t.Fatalf("invalid revision evidence: %+v", step)
				}
				seen[step.Commit] = true
				job, err := f.st.GetCommandRuntimeJob(t.Context(), execution.JobID)
				if err != nil || job.Stdout != "" || job.Stderr != "" || job.OutputFramesJSON != "[]" || !strings.Contains(job.IntentJSON, "-count=1") {
					t.Fatalf("recipe output or fixed argv changed: %+v %v", job, err)
				}
			}
			if sbx != nil && (len(sbx.requests) != len(seen) || sbx.requests[0].DrydockRoot != f.root) {
				t.Fatal("SBX did not execute source revisions")
			}
			if docker != nil && (docker.mountedRoot != f.root || docker.starts != len(seen)) {
				t.Fatal("Docker did not execute source revisions")
			}
			if f.hostTripwire.attempts.Load() != 0 {
				t.Fatal("sandbox recipe fell back to host")
			}
			owned, _, err := f.st.GetDrydockByRun(t.Context(), f.turn.Run.ID)
			if err != nil || owned.Generation != f.owned.Generation {
				t.Fatal("source recipe advanced unrelated Drydock")
			}
		})
	}
}

func TestGitAdvancedBisectRevocationDoesNotReplay(t *testing.T) {
	f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionFull, domain.RunExecutionProfileSBX, true, true)
	backend := installBatchSBX(t, f)
	fixture, spec := prepareGitBisectRuntime(t, f)
	review, err := fixture.service.Review(t.Context(), GitAdvancedReviewRequest{ProtocolVersion: GitAdvancedAPIProtocolVersion,
		RunID: fixture.run.ID, OperationKey: "bisect-runtime-revoke", RequestedBy: "test_operator", Scope: fixture.scope(), Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := f.st.DecideApproval(t.Context(), approval.DecisionRequest{ProposalID: review.Operation.ID,
		IdempotencyKey: "approve-runtime-revoke", Action: approval.ActionApprove, ReviewedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	backend.waitForCancel = true
	backend.before = func() error { f.caps.RuntimeAuthority.RevokeRun(f.turn.Run.ID); return nil }
	request := GitAdvancedExecuteRequest{ProtocolVersion: GitAdvancedAPIProtocolVersion, RunID: fixture.run.ID,
		OperationID: review.Operation.ID, ApprovalID: decision.Approval.ID, RequestedBy: "test_operator", Scope: fixture.scope()}
	result, err := fixture.service.Execute(t.Context(), request)
	if err == nil || len(backend.requests) != 1 || len(result.Receipt.BisectExecutions) != 1 {
		t.Fatalf("revocation did not stop recipe: %+v %v", result, err)
	}
	if head := strings.TrimSpace(runFixtureGit(t, "-C", f.root, "rev-parse", "HEAD")); head != spec.ExpectedCurrent {
		t.Fatal("revoked revision was classified")
	}
	replayed, err := fixture.service.Execute(context.Background(), request)
	if err != nil || !replayed.Replayed || len(backend.requests) != 1 {
		t.Fatalf("interrupted recipe replayed: %+v %v", replayed, err)
	}
}
