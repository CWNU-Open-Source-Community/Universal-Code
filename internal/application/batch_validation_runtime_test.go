package application

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/standardcode"
)

func prepareRuntimeBatch(t *testing.T, f *commandApprovalSandboxFixture, kind domain.BatchDeliveryValidationKind) (*BatchDeliveryService, PrepareBatchDeliveryResult) {
	t.Helper()
	ctx, run, root := t.Context(), f.turn.Run, f.turn.Agent
	artifact := []domain.ChildTaskExpectedArtifact{{PathHint: "tracked.txt", Kind: "code"}}
	spec, err := domain.NormalizeChildTaskProposalSpec(domain.ChildTaskProposalSpec{Version: domain.ChildTaskProposalVersion,
		Tasks: []domain.ChildTask{{Title: "verify isolated delivery", Goal: "verify the recorded worktree", Skills: []string{"model.chat"},
			SurfaceHint: domain.ChildTaskSurfaceHintCore, TurnLimit: 1, TokenLimit: 128, TimeoutMillis: 120000, ExpectedArtifacts: artifact}}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	proposal := domain.ChildTaskProposal{ID: idgen.New("batch-runtime-proposal"), RunID: run.ID, RootAgentID: root.ID, SessionID: run.SessionID,
		WorkspaceID: f.owned.SourceWorkspaceID, Status: domain.ChildTaskProposalProposed, Spec: spec, Surface: domain.ChildTaskSurfaceCore,
		RequestedBy: "run_supervisor", Version: 1, CreatedAt: now}
	op := domain.ChildTaskOperation{KeyDigest: runmutation.OperationKeyDigest("child_task_propose", run.ID, "batch-runtime-proposal"),
		RequestFingerprint: runmutation.Fingerprint("child_task_request.v1", run.ID, spec.SpecJSONFingerprint()), ProposalID: proposal.ID,
		RunID: run.ID, SessionID: run.SessionID, WorkspaceID: proposal.WorkspaceID, RootAgentID: root.ID,
		LeaseID: idgen.New("lease"), LeaseGeneration: 1, RequestedBy: "run_supervisor", CreatedAt: now}
	policyEvent, _ := events.New(run.ID, run.MissionID, events.PolicyDecisionEvent, "policy", idgen.New("inv"), map[string]any{"allowed": true})
	proposalEvent, _ := events.New(run.ID, run.MissionID, events.ChildTaskProposedEvent, "agent_coordinator", proposal.ID, map[string]any{"status": "proposed"})
	toolEvent, _ := events.New(run.ID, run.MissionID, events.ToolCompletedEvent, "agent_proposal_tool", idgen.New("inv"), map[string]any{"tool_name": "child_task_propose"})
	if _, _, err := f.st.CreateChildTaskProposal(ctx, op, proposal, policyEvent, proposalEvent, toolEvent); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.st.ReviewChildTaskProposal(ctx, domain.ChildTaskReview{ProposalID: proposal.ID, Action: "approve", Reviewer: "operator", FanoutTier: domain.ReadOnlyFanoutTwo}, "batch-runtime-review"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.st.AdmitChildTaskProposal(ctx, proposal.ID, "batch-runtime-admit"); err != nil {
		t.Fatal(err)
	}
	batchSpec, err := domain.NormalizeBatchDeliverySpec(domain.BatchDeliverySpec{Version: domain.BatchDeliveryProtocolVersion,
		Tasks: []domain.BatchDeliveryTaskSpec{{Ordinal: 1, OwnershipHints: []domain.BatchDeliveryOwnershipHint{{Path: "tracked.txt", Kind: domain.BatchDeliveryOwnershipFile}},
			Budget:      domain.BatchDeliveryBudget{TurnLimit: 1, TokenLimit: 128, TimeoutMillis: 120000},
			Validations: []domain.BatchDeliveryValidationRequirement{{ID: "diff-check", Kind: domain.BatchValidationGitDiffCheck, Scope: "."}, {ID: "recorded-test", Kind: kind, Scope: "."}}, ExpectedArtifacts: artifact}},
		Contract: domain.BatchDeliveryContract{RequireClean: true, RequireIndependentReview: true, RequireAllValidations: true, MaxChangedFiles: 32, MaxDiffBytes: 1024 * 1024}})
	if err != nil {
		t.Fatal(err)
	}
	service := NewBatchDeliveryService(f.st).WithHostValidationExecution(true, f.caps).WithValidationCommandRuntime(f.mux)
	prepared, err := service.Prepare(ctx, PrepareBatchDeliveryRequest{RunID: run.ID, ProposalID: proposal.ID, Spec: batchSpec,
		OperationKey: "batch-runtime-prepare", RequestedBy: root.ID, WorktreeParent: t.TempDir(), Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	writeDrydockTestFile(t, filepath.Join(prepared.Workspaces[0].WorktreeRoot, "tracked.txt"), "batch delivery change\n")
	fixtureGit(t, "-C", prepared.Workspaces[0].WorktreeRoot, "add", "tracked.txt")
	fixtureGit(t, "-C", prepared.Workspaces[0].WorktreeRoot, "-c", "user.name=Batch Test", "-c", "user.email=batch@example.invalid", "commit", "-m", "Batch delivery")
	if _, _, _, err := service.SendMessage(ctx, SendBatchDeliveryMessageRequest{PlanID: prepared.Plan.ID, Ordinal: 1, Generation: prepared.Workspaces[0].Generation,
		OwnerToken: prepared.Authorities[0].OwnerToken, Kind: domain.BatchMailboxAck, Summary: "validation fixture accepted assignment", OperationKey: "runtime-batch-acknowledge"}); err != nil {
		t.Fatal(err)
	}
	if lease, found, err := f.st.GetRunExecutionLease(ctx, run.ID); err != nil {
		t.Fatal(err)
	} else if found {
		if _, _, err := f.st.ReleaseRunExecutionLease(ctx, lease); err != nil {
			t.Fatal(err)
		}
	}
	return service, prepared
}

type batchDockerTargetTransport struct {
	*dockerLifecycleSupervisorTestTransport
	mountedRoot     string
	started         chan struct{}
	cleanupHadMount bool
}

func (b *batchDockerTargetTransport) StageOwned(ctx context.Context, request sandbox.DockerContainerWriteRequest,
	owner sandbox.DockerContainerLifecycleOwnership, fence sandbox.DockerContainerLifecycleFence) (sandbox.DockerContainerStageResult, error) {
	b.mountedRoot = request.HostMounts[0].Source
	return b.dockerLifecycleSupervisorTestTransport.StageOwned(ctx, request, owner, fence)
}

func (b *batchDockerTargetTransport) Wait(ctx context.Context, request sandbox.DockerContainerLifecycleRequest, fence sandbox.DockerContainerLifecycleFence) (sandbox.DockerContainerLifecycleObservation, error) {
	if b.started == nil {
		return b.dockerLifecycleSupervisorTestTransport.Wait(ctx, request, fence)
	}
	close(b.started)
	<-ctx.Done()
	return sandbox.DockerContainerLifecycleObservation{}, ctx.Err()
}

func (b *batchDockerTargetTransport) Cleanup(ctx context.Context, request sandbox.DockerContainerLifecycleRequest, fence sandbox.DockerContainerLifecycleFence) (sandbox.DockerContainerLifecycleCleanupResult, error) {
	_, err := os.Stat(b.mountedRoot)
	b.cleanupHadMount = err == nil
	return b.dockerLifecycleSupervisorTestTransport.Cleanup(ctx, request, fence)
}

func TestBatchRuntimeDockerValidatesChildAndRetainsDigestOnlyReceipt(t *testing.T) {
	for _, kind := range []domain.BatchDeliveryValidationKind{domain.BatchValidationGoTest, domain.BatchValidationNPMTest} {
		t.Run(string(kind), func(t *testing.T) {
			f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionFull, domain.RunExecutionProfileDocker, true, true)
			executor := f.service.sandbox.(*DockerSandboxCommandRuntimeExecutor)
			transport := &batchDockerTargetTransport{dockerLifecycleSupervisorTestTransport: executor.service.docker.lifecycleTransport.(*dockerLifecycleSupervisorTestTransport)}
			executor.service.docker.lifecycleTransport = transport
			service, prepared := prepareRuntimeBatch(t, f, kind)
			child := prepared.Workspaces[0]
			receipt, _, err := service.Submit(t.Context(), SubmitBatchDeliveryRequest{PlanID: prepared.Plan.ID, Ordinal: 1, Generation: child.Generation,
				OwnerToken: prepared.Authorities[0].OwnerToken, EvidenceRefs: []string{"test://batch-runtime"}, Limitations: []string{"controlled transports"}, OperationKey: "same-batch-phase-key"})
			if err != nil {
				t.Fatalf("validation failed: %v; cause: %v; nested: %v", err, errors.Unwrap(err), errors.Unwrap(errors.Unwrap(err)))
			}
			if transport.mountedRoot != child.WorktreeRoot || transport.mountedRoot == f.owned.Path || f.hostTripwire.attempts.Load() != 0 {
				t.Fatalf("validation target escaped child: mount=%q child=%q host=%d", transport.mountedRoot, child.WorktreeRoot, f.hostTripwire.attempts.Load())
			}
			execution := receipt.TestReceipts[1].Execution
			if execution == nil || execution.Backend != CommandRuntimeDockerSandboxBackend || execution.NetworkPolicy != "denied" || execution.Readiness != "ready" || execution.ImageDigest != executor.service.imageDigest || execution.JobID == "" {
				t.Fatalf("actual execution receipt missing: %+v", execution)
			}
			job, err := f.st.GetCommandRuntimeJob(t.Context(), execution.JobID)
			if err != nil || job.Stdout != "" || job.Stderr != "" || job.OutputFramesJSON != "[]" || job.StdoutObservedBytes == 0 {
				t.Fatalf("Batch retained output text or lost counts: job=%+v error=%v", job, err)
			}
			owned, _, err := f.st.GetDrydockByRun(t.Context(), f.turn.Run.ID)
			if err != nil || owned.Generation != f.owned.Generation {
				t.Fatalf("Batch changed Run Drydock: %+v error=%v", owned, err)
			}
			// Using the same public operation key for Review must still execute
			// an independent Job rather than replay the author's verification.
			_, _, err = service.Review(t.Context(), ReviewBatchDeliveryRequest{PlanID: prepared.Plan.ID, Ordinal: 1, Generation: child.Generation,
				Reviewer: f.turn.Agent.ID, Verdict: domain.BatchReviewAccepted, Summary: "independent check", FullDiffReviewed: true,
				CallChainReviewed: true, TestsReviewed: true, OperationKey: "same-batch-phase-key"})
			if err != nil {
				t.Fatal(err)
			}
			if transport.starts != 2 {
				t.Fatalf("review did not independently execute: %d starts", transport.starts)
			}
			if kind == domain.BatchValidationGoTest {
				merged, err := service.Merge(t.Context(), MergeBatchDeliveryRequest{PlanID: prepared.Plan.ID, OrderedOrdinals: []int{1},
					OperationKey: "batch-runtime-merge", RequestedBy: f.turn.Agent.ID, WorktreeParent: t.TempDir(), Confirm: true})
				if err != nil || merged.Queue.Status != domain.BatchMergeQueueCompleted || len(merged.Steps) != 1 || transport.starts != 3 || transport.mountedRoot != merged.Queue.IntegrationRoot {
					t.Fatalf("integration validation used wrong target: queue=%+v starts=%d mount=%q error=%v cause=%v", merged.Queue, transport.starts, transport.mountedRoot, err, errors.Unwrap(err))
				}
				var validation struct {
					Receipts []domain.BatchDeliveryTestReceipt `json:"receipts"`
				}
				if json.Unmarshal([]byte(merged.Steps[0].ValidationJSON), &validation) != nil || len(validation.Receipts) != 2 || validation.Receipts[1].Execution == nil || validation.Receipts[1].Execution.Backend != CommandRuntimeDockerSandboxBackend {
					t.Fatalf("integration receipt missing: %s", merged.Steps[0].ValidationJSON)
				}
			}
		})
	}
}

type batchSBXBackend struct {
	SBXCommandRuntimeBackend
	requests       []sandbox.SBXRunRequest
	before         func() error
	blocked        bool
	waitForCancel  bool
	releaseCleanup <-chan struct{}
}

func (b *batchSBXBackend) Readiness(ctx context.Context) (sandbox.SBXReadiness, error) {
	if b.blocked {
		return sandbox.SBXReadiness{}, errors.New("controlled SBX readiness failure")
	}
	return b.SBXCommandRuntimeBackend.Readiness(ctx)
}

func (b *batchSBXBackend) Run(ctx context.Context, request sandbox.SBXRunRequest, input io.Reader) (sandbox.SBXExecutionResult, error) {
	b.requests = append(b.requests, request)
	if b.before != nil {
		if err := b.before(); err != nil {
			return sandbox.SBXExecutionResult{ExitCode: 125, TreeReaped: true}, err
		}
	}
	if b.waitForCancel {
		<-ctx.Done()
		if b.releaseCleanup != nil {
			<-b.releaseCleanup
		}
		return sandbox.SBXExecutionResult{ExitCode: 125, TreeReaped: true}, ctx.Err()
	}
	return b.SBXCommandRuntimeBackend.Run(ctx, request, input)
}

func installBatchSBX(t *testing.T, f *commandApprovalSandboxFixture) *batchSBXBackend {
	t.Helper()
	base, _, _, _, _ := sbxApplicationFixture(t)
	backend := &batchSBXBackend{SBXCommandRuntimeBackend: base.backend}
	executor, err := NewSBXCommandRuntimeExecutor(f.st, backend)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := runner.NewSandboxCommandRuntimeManager(f.st, executor, "batch-sbx-manager")
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewSandboxedCommandRuntimeService(f.st, manager, executor, f.caps, f.drydockFixture.service)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.manager.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.service, f.manager = service, manager
	f.mux, err = NewCommandRuntimeMultiplexer(f.host, service)
	if err != nil {
		t.Fatal(err)
	}
	return backend
}

func TestBatchRuntimeSBXUsesChildAndFailsClosedOnLostAuthority(t *testing.T) {
	for _, change := range []string{"none", "readiness", "missing-runtime", "permission", "lease", "target", "running-revoke"} {
		t.Run(change, func(t *testing.T) {
			f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionFull, domain.RunExecutionProfileSBX, true, true)
			backend := installBatchSBX(t, f)
			service, prepared := prepareRuntimeBatch(t, f, domain.BatchValidationNPMTest)
			child := prepared.Workspaces[0]
			var testClock atomic.Int64
			testClock.Store(time.Now().UTC().UnixNano())
			if change == "target" {
				service.now = func() time.Time { return time.Unix(0, testClock.Load()).UTC() }
			}
			backend.blocked = change == "readiness"
			backend.waitForCancel = change == "running-revoke"
			if change == "missing-runtime" {
				service.validationRuntime = nil
			}
			backend.before = func() error {
				switch change {
				case "permission", "running-revoke":
					f.caps.RuntimeAuthority.RevokeRun(f.turn.Run.ID)
				case "lease":
					lease, _, err := f.st.GetRunExecutionLease(t.Context(), f.turn.Run.ID)
					if err != nil {
						return err
					}
					if _, _, err := f.st.ReleaseRunExecutionLease(t.Context(), lease); err != nil {
						return err
					}
				case "target":
					testClock.Store(child.LeaseExpiresAt.Add(time.Second).UnixNano())
				}
				return nil
			}
			receipts, err := service.runBatchDeliveryValidations(t.Context(), prepared.Plan.RunID, child.WorktreeRoot, child.BaseCommit, prepared.Plan.Spec.Tasks[0].Validations,
				batchValidationTargetRef{PlanID: prepared.Plan.ID, Ordinal: 1, Generation: child.Generation, OperationKey: "sbx-verification"})
			if change == "none" {
				if err != nil || len(receipts) != 2 || receipts[1].Execution == nil || receipts[1].Execution.Backend != CommandRuntimeSBXBackend || len(backend.requests) != 1 || backend.requests[0].DrydockRoot != child.WorktreeRoot {
					t.Fatalf("SBX validation: receipts=%+v requests=%+v error=%v", receipts, backend.requests, err)
				}
			} else if err == nil {
				t.Fatalf("lost %s authority was accepted", change)
			}
			if change == "readiness" && len(backend.requests) != 0 {
				t.Fatal("unready SBX dispatched")
			}
			if change == "missing-runtime" && len(backend.requests) != 0 {
				t.Fatal("missing SBX runtime dispatched")
			}
			if change == "running-revoke" {
				permission, err := f.st.GetRunExecutionPermission(t.Context(), f.turn.Run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.caps.RuntimeAuthority.ActivateRunFullAccess(permission); err != nil {
					t.Fatal(err)
				}
				if _, err := service.runBatchDeliveryValidations(t.Context(), prepared.Plan.RunID, child.WorktreeRoot, child.BaseCommit, prepared.Plan.Spec.Tasks[0].Validations,
					batchValidationTargetRef{PlanID: prepared.Plan.ID, Ordinal: 1, Generation: child.Generation, OperationKey: "sbx-verification"}); err == nil || len(backend.requests) != 1 {
					t.Fatal("interrupted Batch validation was replayed after reactivation")
				}
			}
			if f.hostTripwire.attempts.Load() != 0 {
				t.Fatal("SBX failed over to host")
			}
		})
	}
}

type batchVerificationResultStore struct {
	BatchDeliveryStore
	results []string
}

func (s batchVerificationResultStore) ListBatchSpecialistVerificationResults(context.Context, string, int, int64, string) ([]string, error) {
	return s.results, nil
}

func TestBatchRuntimeRejectsLegacyOrDifferentBackendVerificationReuse(t *testing.T) {
	f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionFull, domain.RunExecutionProfileDocker, true, true)
	service, prepared := prepareRuntimeBatch(t, f, domain.BatchValidationGoTest)
	child := prepared.Workspaces[0]
	for _, kind := range []string{"legacy", "host", "docker"} {
		t.Run(kind, func(t *testing.T) {
			receipt := domain.BatchDeliveryTestReceipt{RequirementID: "recorded-test", Kind: domain.BatchValidationGoTest, Scope: ".", OutputSHA256: strings.Repeat("a", 64), CompletedAt: time.Now().UTC()}
			if kind != "legacy" {
				adapter := f.host.adapter
				if kind == "docker" {
					adapter = f.service.adapter
				}
				rootSHA, err := runner.CommandRuntimeWorkspaceRootSHA256(child.WorktreeRoot)
				if err != nil {
					t.Fatal(err)
				}
				receipt.Execution = batchValidationExecutionReceipt(adapter, rootSHA)
				receipt.Execution.JobID, receipt.Execution.TreeReaped, receipt.Execution.StdinClosed = "saved-job", true, true
			}
			diff := domain.BatchDeliveryTestReceipt{RequirementID: "diff-check", Kind: domain.BatchValidationGitDiffCheck, Scope: ".", OutputSHA256: strings.Repeat("b", 64), CompletedAt: time.Now().UTC()}
			raw, _ := json.Marshal(map[string]any{"result": map[string]any{"passed": true, "head_commit": child.BaseCommit, "receipts": []domain.BatchDeliveryTestReceipt{diff, receipt}}})
			service.store = batchVerificationResultStore{BatchDeliveryStore: f.st, results: []string{string(raw)}}
			_, reused, err := service.reuseSpecialistBatchVerification(t.Context(), prepared.Plan, child, prepared.Plan.Spec.Tasks[0], child.BaseCommit)
			if err != nil || reused != (kind == "docker") {
				t.Fatalf("reuse %s: reused=%v error=%v", kind, reused, err)
			}
		})
	}
}

func TestBatchRuntimeLocalHostRunsScopedGoCheckInRecordedChild(t *testing.T) {
	f := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionFull)
	manager, err := runner.NewPlatformCommandRuntimeManager(f.store, "batch-host-validation")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewCommandRuntimeService(f.store, manager, f.caps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Shutdown(context.Background()) })
	f.service.WithValidationCommandRuntime(runtime)
	f.spec.Tasks[0].Validations = append(f.spec.Tasks[0].Validations, domain.BatchDeliveryValidationRequirement{ID: "go-child", Kind: domain.BatchValidationGoTest, Scope: "internal/one"})
	prepared, err := f.service.Prepare(t.Context(), PrepareBatchDeliveryRequest{RunID: f.run.ID, ProposalID: f.proposal.ID, Spec: f.spec,
		OperationKey: "batch-host-prepare", RequestedBy: f.root.ID, WorktreeParent: t.TempDir(), Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	child := prepared.Workspaces[0]
	writeDrydockTestFile(t, filepath.Join(child.WorktreeRoot, "internal", "one", "delivery.txt"), "batch local delivery\n")
	fixtureGit(t, "-C", child.WorktreeRoot, "add", "internal/one/delivery.txt")
	fixtureGit(t, "-C", child.WorktreeRoot, "-c", "user.name=Batch Test", "-c", "user.email=batch@example.invalid", "commit", "-m", "Batch local delivery")
	// A broken source test proves the validation executes the child copy.
	if err := os.WriteFile(filepath.Join(f.repository, "internal", "one", "source_only_test.go"), []byte("invalid source test"), 0600); err != nil {
		t.Fatal(err)
	}
	receipts, err := f.service.runBatchDeliveryValidations(t.Context(), f.run.ID, child.WorktreeRoot, child.BaseCommit, prepared.Plan.Spec.Tasks[0].Validations,
		batchValidationTargetRef{PlanID: prepared.Plan.ID, Ordinal: 1, Generation: child.Generation, OperationKey: "native-local-validation"})
	if err != nil || len(receipts) != 2 || receipts[1].Execution == nil || receipts[1].Execution.IsolationGrade != "host_unsandboxed" || receipts[1].Execution.Readiness != "explicit_host_execution" {
		t.Fatalf("Local host validation: receipts=%+v error=%v", receipts, err)
	}
	job, err := f.store.GetCommandRuntimeJob(t.Context(), receipts[1].Execution.JobID)
	if err != nil || job.Stdout != "" || job.Stderr != "" || job.OutputFramesJSON != "[]" || job.WorkingDirectory != "." || !strings.Contains(job.IntentJSON, "./internal/one/...") {
		t.Fatalf("scoped Go Job: %+v error=%v", job, err)
	}
}

func TestBatchRuntimeReconcilePreservesLiveTargetAndRejectsOtherLeaseBorrowers(t *testing.T) {
	f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionFull, domain.RunExecutionProfileSBX, true, true)
	backend := installBatchSBX(t, f)
	service, prepared := prepareRuntimeBatch(t, f, domain.BatchValidationNPMTest)
	child := prepared.Workspaces[0]
	started := make(chan struct{})
	backend.waitForCancel = true
	backend.before = func() error { close(started); return nil }
	ref := batchValidationTargetRef{PlanID: prepared.Plan.ID, Ordinal: 1, Generation: child.Generation, OperationKey: "reconcile-live-batch"}
	completed := make(chan error, 1)
	go func() {
		_, err := service.runBatchDeliveryValidations(t.Context(), prepared.Plan.RunID, child.WorktreeRoot, child.BaseCommit, prepared.Plan.Spec.Tasks[0].Validations, ref)
		completed <- err
	}()
	select {
	case <-started:
	case err := <-completed:
		t.Fatalf("validation did not reach backend: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("validation start timed out")
	}
	if stopped, err := f.service.Reconcile(t.Context()); err != nil || stopped != 0 {
		t.Fatalf("live Batch target was stopped: count=%d error=%v", stopped, err)
	}
	jobs, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.turn.Run.ID, Limit: 10})
	if err != nil || len(jobs) != 1 || jobs[0].State.Terminal() {
		t.Fatalf("live Batch Job lost: %+v error=%v", jobs, err)
	}
	// A second HTTP/native operation cannot borrow the first validation's short
	// lease. The same applies to a concurrent operator command owner.
	other := ref
	other.OperationKey = "second-concurrent-batch"
	_, err = service.runBatchDeliveryValidations(t.Context(), prepared.Plan.RunID, child.WorktreeRoot, child.BaseCommit, prepared.Plan.Spec.Tasks[0].Validations, other)
	if err == nil || apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("concurrent validation borrowed active lease: %v / %v", err, errors.Unwrap(err))
	}
	if len(backend.requests) != 1 {
		t.Fatal("another validation dispatched under a short-lived lease")
	}
	f.caps.RuntimeAuthority.RevokeRun(f.turn.Run.ID)
	if _, err := f.service.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-completed:
		if err == nil {
			t.Fatal("revoked Batch succeeded")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("revoked Batch was not terminated")
	}
	job, err := f.st.GetCommandRuntimeJob(t.Context(), jobs[0].ID)
	if err != nil || !job.State.Terminal() || !job.TreeReaped {
		t.Fatalf("revoked Job not reaped: %+v error=%v", job, err)
	}
	lease, found, err := f.st.GetRunExecutionLease(t.Context(), f.turn.Run.ID)
	if err != nil || !found || lease.Status == domain.RunExecutionLeaseActive {
		t.Fatalf("validation lease was not released after cleanup: %+v error=%v", lease, err)
	}
}

func TestBatchRuntimeDockerColdRecoveryReconstructsCleanupOnlyTarget(t *testing.T) {
	f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionFull, domain.RunExecutionProfileDocker, true, true)
	executor := f.service.sandbox.(*DockerSandboxCommandRuntimeExecutor)
	old := executor.service.docker
	transport := &batchDockerTargetTransport{dockerLifecycleSupervisorTestTransport: old.lifecycleTransport.(*dockerLifecycleSupervisorTestTransport)}
	old.lifecycleTransport = transport
	service, prepared := prepareRuntimeBatch(t, f, domain.BatchValidationGoTest)
	child := prepared.Workspaces[0]
	// A lost daemon response leaves the exact container running and its durable
	// intent recoverable, as after process loss. It grants no terminal receipt.
	transport.waitErr = errors.New("controlled daemon connection lost")
	target, err := service.bindBatchValidationTarget(t.Context(), prepared.Plan.RunID, child.WorktreeRoot, child.BaseCommit,
		batchValidationTargetRef{PlanID: prepared.Plan.ID, Ordinal: 1, Generation: child.Generation, OperationKey: "cold-docker-batch"})
	if err != nil {
		t.Fatal(err)
	}
	command := standardcode.Command{ProtocolVersion: standardcode.CommandProtocolVersion, Toolchain: sandbox.DockerStandardCodeToolchainGo,
		Arguments: []string{"test", "-count=1", "./..."}, WorkingDirectory: ".", TimeoutSeconds: 30,
		Purpose: "batch-validation:" + runmutation.Fingerprint("batch-validation-target.v1", target.ref.PlanID, "", "1", "1", target.rootIdentity, target.base, target.head)}
	commandSHA, err := command.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	liveCtx := context.WithValue(t.Context(), batchValidationRuntimeKey{}, &batchValidationRuntimeProof{target: target, commandSHA256: commandSHA, check: target.check})
	requester := batchValidationDockerRequester(target)
	preparedCommand, err := executor.service.Prepare(liveCtx, StandardCodeDockerPrepareRequest{RunID: prepared.Plan.RunID,
		ExpectedGeneration: f.owned.Generation, ExpectedCheckpoint: f.owned.LastCheckpointID, Command: command, RequestedBy: requester, OperationKey: "cold-batch-prepare"})
	if err != nil || preparedCommand.Preparation == nil || preparedCommand.Approval == nil {
		t.Fatalf("cold fixture prepare=%+v error=%v", preparedCommand, err)
	}
	if _, err := executor.service.manifests.ReviewApproval(liveCtx, preparedCommand.Preparation.Preparation.ID, approval.ActionApprove, "cold-batch-approve", "operator", ""); err != nil {
		t.Fatal(err)
	}
	_, err = executor.service.Execute(liveCtx, StandardCodeDockerExecuteRequest{RunID: prepared.Plan.RunID, ExpectedGeneration: f.owned.Generation,
		ExpectedCheckpoint: f.owned.LastCheckpointID, PreparationID: preparedCommand.Preparation.Preparation.ID, ApprovalID: preparedCommand.Approval.ID,
		Command: command, RequestedBy: requester, OperationKey: "cold-batch-execute"})
	if err == nil {
		t.Fatal("lost running container response was accepted")
	}
	records, err := f.st.ListRecoverableDockerSandboxes(t.Context(), 10)
	if err != nil || len(records) != 1 || records[0].Launch == nil || transport.state != sandbox.DockerContainerLifecycleStateRunning {
		t.Fatalf("running lifecycle was not persisted: %+v state=%s error=%v", records, transport.state, err)
	}
	lifecycle, err := f.st.GetDockerContainerLifecycle(t.Context(), records[0].Launch.LifecycleIntentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.st.ReleaseDockerContainerLifecycleLease(t.Context(), lifecycle.Lease); err != nil {
		t.Fatal(err)
	}
	f.caps.RuntimeAuthority.RevokeRun(f.turn.Run.ID)
	transport.waitErr = nil
	restartedDocker, err := NewDockerSandboxService(f.st, old.readiness, policy.NewDefaultChecker(), old.dockerCapabilities, f.caps,
		WithDockerSandboxExecution(transport, old.ioService.transport, old.stagingRoot, time.Minute), WithDockerStandardCode(f.drydockFixture.service, executor.service.imageDigest))
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewStandardCodeDockerService(f.st, f.drydockFixture.service, executor.service.manifests, restartedDocker, executor.service.imageDigest)
	if err != nil {
		t.Fatal(err)
	}
	if recovered, err := restarted.RecoverStartup(t.Context()); err != nil || len(recovered) != 0 {
		t.Fatalf("cleanup-only recovery: results=%+v error=%v cause=%v nested=%v", recovered, err, errors.Unwrap(err), errors.Unwrap(errors.Unwrap(err)))
	}
	if transport.starts != 1 || transport.creates != 1 || transport.terms != 1 || transport.deletes != 1 || transport.state != sandbox.DockerContainerLifecycleStateAbsent {
		t.Fatalf("recovery did not reap exact running resource: %+v", transport.dockerLifecycleSupervisorTestTransport)
	}
	record, err := f.st.GetDockerSandboxRecord(t.Context(), records[0].Admission.ID)
	if err != nil || record.Receipt == nil || !record.Receipt.CleanupComplete || record.Receipt.Outcome == domain.DockerSandboxOutcomeSucceeded {
		t.Fatalf("cold recovery restored success/start authority: %+v error=%v", record.Receipt, err)
	}
	if _, err := restartedDocker.loadCurrentDockerSandboxAuthority(t.Context(), record.Admission.PlanID, manifestFromAdmission(record.Admission), record.Admission.RequestedBy); err == nil {
		t.Fatal("persistent cleanup reference restored live authority")
	}
	if _, err := restarted.Cancel(t.Context(), StandardCodeDockerCancelRequest{AdmissionID: record.Admission.ID, RequestedBy: requester, OperationKey: "public-batch-cancel"}); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("public Standard Code cancel accepted Batch: %v", err)
	}
	if _, found, err := f.st.GetDockerSandboxCancellation(t.Context(), record.Admission.ID); err != nil || found {
		t.Fatal("rejected public cancel had a durable side effect")
	}
	replayed, err := restarted.Execute(t.Context(), StandardCodeDockerExecuteRequest{RunID: prepared.Plan.RunID, ExpectedGeneration: f.owned.Generation,
		ExpectedCheckpoint: f.owned.LastCheckpointID, PreparationID: preparedCommand.Preparation.Preparation.ID, ApprovalID: preparedCommand.Approval.ID,
		Command: command, RequestedBy: requester, OperationKey: "cold-batch-execute"})
	if err != nil || replayed.batchRecord == nil || !replayed.batchRecord.Replayed || replayed.Result != nil || transport.starts != 1 {
		t.Fatalf("Batch terminal replay: %+v error=%v", replayed, err)
	}
	manifest := manifestFromAdmission(record.Admission)
	manifestBinding, _ := sandbox.ParseDockerStandardCodeManifest(manifest)
	if _, err := restarted.finalize(t.Context(), record, executionContextFromDockerBinding(manifestBinding), requester); err == nil {
		t.Fatal("Batch record reached Drydock checkpoint finalizer")
	}
	owned, _, err := f.st.GetDrydockByRun(t.Context(), f.turn.Run.ID)
	if err != nil || owned.Generation != f.owned.Generation {
		t.Fatal("Batch cleanup changed unrelated Run Drydock")
	}
}

func TestBatchRuntimeBorrowsOnlyCurrentValidatedParentLease(t *testing.T) {
	f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionFull, domain.RunExecutionProfileSBX, true, true)
	backend := installBatchSBX(t, f)
	service, prepared := prepareRuntimeBatch(t, f, domain.BatchValidationNPMTest)
	child := prepared.Workspaces[0]
	parent, err := f.st.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: f.turn.Run.ID, OwnerID: "parent-specialist-owner", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	ref := batchValidationTargetRef{PlanID: prepared.Plan.ID, Ordinal: 1, Generation: child.Generation, OperationKey: "native-parent-verification",
		parentLeaseID: parent.Lease.LeaseID, parentLeaseGeneration: parent.Lease.Generation}
	if _, err := service.runBatchDeliveryValidations(t.Context(), prepared.Plan.RunID, child.WorktreeRoot, child.BaseCommit, prepared.Plan.Spec.Tasks[0].Validations, ref); err != nil {
		t.Fatalf("current parent lease refused: %v / %v", err, errors.Unwrap(err))
	}
	current, _, err := f.st.GetRunExecutionLease(t.Context(), f.turn.Run.ID)
	if err != nil || !current.ActiveAt(time.Now().UTC()) || current.LeaseID != parent.Lease.LeaseID || current.Generation != parent.Lease.Generation {
		t.Fatalf("validation released its parent lease: %+v error=%v", current, err)
	}
	if _, _, err := f.st.ReleaseRunExecutionLease(t.Context(), parent.Lease); err != nil {
		t.Fatal(err)
	}
	ref.OperationKey = "stale-parent-verification"
	if _, err := service.runBatchDeliveryValidations(t.Context(), prepared.Plan.RunID, child.WorktreeRoot, child.BaseCommit, prepared.Plan.Spec.Tasks[0].Validations, ref); err == nil {
		t.Fatal("released parent was converted into a fresh validation lease")
	}
	if len(backend.requests) != 1 {
		t.Fatal("stale parent dispatched another command")
	}
}

func TestBatchRuntimeDockerCleanupRequesterRejectsNoncanonicalReferences(t *testing.T) {
	target := &batchValidationTarget{ref: batchValidationTargetRef{PlanID: "batch-plan-1", Ordinal: 1, Generation: 2}, rootIdentity: strings.Repeat("a", 64)}
	valid := batchValidationDockerRequester(target)
	if ref, identity, err := parseBatchValidationDockerRequester(valid); err != nil || ref.PlanID != target.ref.PlanID || ref.Generation != 2 || identity != target.rootIdentity {
		t.Fatalf("canonical reference refused: %+v %s %v", ref, identity, err)
	}
	for _, invalid := range []string{batchValidationActor, valid + "/extra", strings.Replace(valid, "/1/2/", "/01/2/", 1),
		strings.Replace(valid, "/1/2/", "/1/0/", 1), strings.Replace(valid, "batch-plan-1", "../root", 1),
		strings.Replace(valid, strings.Repeat("a", 64), strings.Repeat("A", 64), 1), strings.Replace(valid, "batch-plan-1", strings.Repeat("x", 250), 1)} {
		if _, _, err := parseBatchValidationDockerRequester(invalid); err == nil {
			t.Fatalf("invalid cleanup reference accepted: %q", invalid)
		}
	}
}

func TestBatchRuntimeCancelReapsDockerBeforeRemovingMount(t *testing.T) {
	f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionFull, domain.RunExecutionProfileDocker, true, true)
	executor := f.service.sandbox.(*DockerSandboxCommandRuntimeExecutor)
	transport := &batchDockerTargetTransport{dockerLifecycleSupervisorTestTransport: executor.service.docker.lifecycleTransport.(*dockerLifecycleSupervisorTestTransport), started: make(chan struct{})}
	executor.service.docker.lifecycleTransport = transport
	service, prepared := prepareRuntimeBatch(t, f, domain.BatchValidationGoTest)
	child := prepared.Workspaces[0]
	finished := make(chan error, 1)
	ref := batchValidationTargetRef{PlanID: prepared.Plan.ID, Ordinal: 1, Generation: child.Generation, OperationKey: "cancel-running-docker"}
	go func() {
		_, err := service.runBatchDeliveryValidations(t.Context(), prepared.Plan.RunID, child.WorktreeRoot, child.BaseCommit, prepared.Plan.Spec.Tasks[0].Validations, ref)
		finished <- err
	}()
	select {
	case <-transport.started:
	case err := <-finished:
		t.Fatalf("Docker did not start: %v / %v", err, errors.Unwrap(err))
	case <-time.After(45 * time.Second):
		t.Fatal("Docker start timed out")
	}
	request := CancelBatchDeliveryRequest{PlanID: prepared.Plan.ID, OperationKey: "cancel-batch-mount", RequestedBy: "operator", Reason: "cancel running validation", Confirm: true}
	result, err := service.Cancel(t.Context(), request)
	if err != nil || result.Snapshot.Plan.Status != domain.BatchDeliveryAborted || result.Snapshot.Workspaces[0].Status != domain.BatchWorkspaceCancelled || !transport.cleanupHadMount || transport.terms != 1 || transport.deletes != 1 {
		t.Fatalf("unsafe cancellation: result=%+v mount=%v terms=%d deletes=%d error=%v cause=%v", result, transport.cleanupHadMount, transport.terms, transport.deletes, err, errors.Unwrap(err))
	}
	if _, err := os.Stat(child.WorktreeRoot); !os.IsNotExist(err) {
		t.Fatalf("reaped mount was not cleaned: %v", err)
	}
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("cancelled validation succeeded")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("cancelled Job did not finish")
	}
	if _, err := service.runBatchDeliveryValidations(t.Context(), prepared.Plan.RunID, child.WorktreeRoot, child.BaseCommit, prepared.Plan.Spec.Tasks[0].Validations, ref); err == nil || transport.starts != 1 {
		t.Fatal("aborted plan dispatched again")
	}
	if replay, err := service.Cancel(t.Context(), request); err != nil || !replay.Replayed {
		t.Fatalf("cancel replay=%+v error=%v", replay, err)
	}
}

func TestBatchRuntimeCancelWithoutLiveOwnerPreservesMountUntilRetry(t *testing.T) {
	f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionFull, domain.RunExecutionProfileSBX, true, true)
	backend := installBatchSBX(t, f)
	service, prepared := prepareRuntimeBatch(t, f, domain.BatchValidationNPMTest)
	child, started := prepared.Workspaces[0], make(chan struct{})
	release := make(chan struct{})
	backend.waitForCancel, backend.releaseCleanup = true, release
	backend.before = func() error { close(started); return nil }
	finished := make(chan error, 1)
	go func() {
		_, err := service.runBatchDeliveryValidations(t.Context(), prepared.Plan.RunID, child.WorktreeRoot, child.BaseCommit, prepared.Plan.Spec.Tasks[0].Validations,
			batchValidationTargetRef{PlanID: prepared.Plan.ID, Ordinal: 1, Generation: child.Generation, OperationKey: "foreign-owned-batch"})
		finished <- err
	}()
	select {
	case <-started:
	case err := <-finished:
		t.Fatalf("SBX did not start: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("SBX start timed out")
	}
	reconstructed := NewBatchDeliveryService(f.st)
	request := CancelBatchDeliveryRequest{PlanID: prepared.Plan.ID, OperationKey: "cancel-without-owner", RequestedBy: "operator", Reason: "preserve unknown execution", Confirm: true}
	if _, err := reconstructed.Cancel(t.Context(), request); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("unowned live Job cleanup accepted: %v", err)
	}
	if _, err := os.Stat(child.WorktreeRoot); err != nil {
		t.Fatal("unconfirmed cleanup removed mount")
	}
	plan, _, err := f.st.GetBatchDeliveryPlan(t.Context(), prepared.Plan.ID)
	if err != nil || plan.Status != domain.BatchDeliveryAborted {
		t.Fatal("cancel did not persist its dispatch fence")
	}
	close(release)
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("aborted foreign Job succeeded")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("foreign Job did not stop")
	}
	result, err := reconstructed.Cancel(t.Context(), request)
	if err != nil || !result.Replayed || result.Snapshot.Workspaces[0].Status != domain.BatchWorkspaceCancelled {
		t.Fatalf("aborted cleanup could not be retried: %+v error=%v", result, err)
	}
}
