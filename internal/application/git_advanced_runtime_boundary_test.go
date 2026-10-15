package application

import (
	"context"
	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/runworktree"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/standardcode"
	"cyberagent-workbench/internal/workspace"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGitBisectDockerColdRecoveryReconstructsCleanupOnlySource(t *testing.T) {
	f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionFull, domain.RunExecutionProfileDocker, true, true)
	executor := f.service.sandbox.(*DockerSandboxCommandRuntimeExecutor)
	old := executor.service.docker
	transport := &batchDockerTargetTransport{dockerLifecycleSupervisorTestTransport: old.lifecycleTransport.(*dockerLifecycleSupervisorTestTransport)}
	old.lifecycleTransport = transport
	fixture, spec := prepareGitBisectRuntime(t, f)

	review, err := fixture.service.Review(t.Context(), GitAdvancedReviewRequest{ProtocolVersion: GitAdvancedAPIProtocolVersion,
		RunID: fixture.run.ID, OperationKey: "cold-git-review", RequestedBy: "test_operator", Scope: fixture.scope(), Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := workspace.AgentCodeRootFingerprint(f.root)
	if err != nil {
		t.Fatal(err)
	}
	proof := &gitBisectRuntimeProof{runID: fixture.run.ID, root: f.root, rootIdentity: identity, operationID: review.Operation.ID,
		check: func(context.Context) error { return nil }}
	transport.waitErr = errors.New("controlled daemon connection lost")
	command := standardcode.Command{ProtocolVersion: standardcode.CommandProtocolVersion, Toolchain: sandbox.DockerStandardCodeToolchainGo,
		Arguments: []string{"test", "-count=1", "./..."}, WorkingDirectory: ".", TimeoutSeconds: 30,
		Purpose: "Git bisect cold recovery"}
	commandSHA, err := command.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	proof.commandSHA256 = commandSHA
	liveCtx := context.WithValue(t.Context(), gitBisectRuntimeKey{}, proof)
	requester := gitBisectDockerRequester(proof)
	preparedCommand, err := executor.service.Prepare(liveCtx, StandardCodeDockerPrepareRequest{RunID: fixture.run.ID,
		ExpectedGeneration: f.owned.Generation, ExpectedCheckpoint: f.owned.LastCheckpointID, Command: command, RequestedBy: requester, OperationKey: "cold-git-prepare"})
	if err != nil || preparedCommand.Preparation == nil || preparedCommand.Approval == nil {
		t.Fatalf("cold fixture prepare=%+v error=%v", preparedCommand, err)
	}
	if _, err := executor.service.manifests.ReviewApproval(liveCtx, preparedCommand.Preparation.Preparation.ID, approval.ActionApprove, "cold-git-approve", "operator", ""); err != nil {
		t.Fatal(err)
	}
	_, err = executor.service.executeCommandRuntime(liveCtx, StandardCodeDockerExecuteRequest{RunID: fixture.run.ID, ExpectedGeneration: f.owned.Generation,
		ExpectedCheckpoint: f.owned.LastCheckpointID, PreparationID: preparedCommand.Preparation.Preparation.ID, ApprovalID: preparedCommand.Approval.ID,
		Command: command, RequestedBy: requester, OperationKey: "cold-git-execute"}, fixture.lease, sandbox.DockerStandardCodeStdinClosed, nil)
	if err == nil {
		t.Fatal("lost running container response was accepted")
	}
	executeErr := err
	records, err := f.st.ListRecoverableDockerSandboxes(t.Context(), 10)
	if err != nil || len(records) != 1 || records[0].Launch == nil || transport.state != sandbox.DockerContainerLifecycleStateRunning {
		t.Fatalf("running lifecycle was not persisted: %+v state=%s error=%v execute=%v cause=%v", records, transport.state, err, executeErr, errors.Unwrap(executeErr))
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
	if _, err := restarted.Cancel(t.Context(), StandardCodeDockerCancelRequest{AdmissionID: record.Admission.ID, RequestedBy: requester, OperationKey: "public-git-cancel"}); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("public Standard Code cancel accepted Git bisect: %v", err)
	}
	if _, found, err := f.st.GetDockerSandboxCancellation(t.Context(), record.Admission.ID); err != nil || found {
		t.Fatal("rejected public cancel had a durable side effect")
	}
	replayed, err := restarted.executeCommandRuntime(t.Context(), StandardCodeDockerExecuteRequest{RunID: fixture.run.ID, ExpectedGeneration: f.owned.Generation,
		ExpectedCheckpoint: f.owned.LastCheckpointID, PreparationID: preparedCommand.Preparation.Preparation.ID, ApprovalID: preparedCommand.Approval.ID,
		Command: command, RequestedBy: requester, OperationKey: "cold-git-execute"}, fixture.lease, sandbox.DockerStandardCodeStdinClosed, nil)
	if err != nil || replayed.batchRecord == nil || !replayed.batchRecord.Replayed || replayed.Result != nil || transport.starts != 1 {
		t.Fatalf("Git bisect terminal replay: %+v error=%v", replayed, err)
	}
	manifest := manifestFromAdmission(record.Admission)
	manifestBinding, _ := sandbox.ParseDockerStandardCodeManifest(manifest)
	if _, err := f.drydockFixture.service.ResolveDrydockExecutionBinding(t.Context(), manifestBinding, true); err == nil {
		t.Fatal("ordinary execution accepted a bisect source without native proof")
	}
	if _, err := restarted.finalize(t.Context(), record, executionContextFromDockerBinding(manifestBinding), requester); err == nil {
		t.Fatal("Git bisect record reached Drydock checkpoint finalizer")
	}
	owned, _, err := f.st.GetDrydockByRun(t.Context(), f.turn.Run.ID)
	if err != nil || owned.Generation != f.owned.Generation {
		t.Fatal("Git bisect cleanup changed unrelated Run Drydock")
	}
}

func TestGitBisectDockerCleanupRequesterRejectsNoncanonicalReferences(t *testing.T) {
	proof := &gitBisectRuntimeProof{operationID: "git-operation-1", rootIdentity: strings.Repeat("a", 64)}
	valid := gitBisectDockerRequester(proof)
	if operation, identity, err := parseGitBisectDockerRequester(valid); err != nil || operation != proof.operationID || identity != proof.rootIdentity {
		t.Fatal("canonical reference rejected", err)
	}
	for _, invalid := range []string{gitBisectDockerActor, valid + "/extra", strings.Replace(valid, "git-operation-1", "../root", 1), strings.Replace(valid, proof.rootIdentity, strings.Repeat("A", 64), 1), strings.Replace(valid, "git-operation-1", strings.Repeat("x", 97), 1)} {
		if _, _, err := parseGitBisectDockerRequester(invalid); err == nil {
			t.Fatalf("invalid reference accepted: %q", invalid)
		}
	}
}

func TestGitBisectSourceOverrideRequiresExactNativeProof(t *testing.T) {
	owned := runworktree.Workspace{Path: "drydock"}
	scope := runner.CommandRuntimeScope{RunID: "run"}
	spec := runner.CommandRuntimeResolvedSpec{WorkspaceRoot: "source"}
	proof := &gitBisectRuntimeProof{runID: "run", root: "source", rootIdentity: strings.Repeat("a", 64), operationID: "operation", specFingerprint: runner.CommandRuntimeSpecFingerprint(spec), check: func(context.Context) error { return nil }}
	ctx := context.WithValue(t.Context(), gitBisectRuntimeKey{}, proof)
	actual, err := gitBisectExecutionWorkspace(ctx, owned, scope, spec)
	if err != nil || actual.Path != "source" {
		t.Fatal("native source refused", err)
	}
	changed := spec
	changed.WorkspaceRoot = "other"
	if _, err := gitBisectExecutionWorkspace(ctx, owned, scope, changed); err == nil {
		t.Fatal("changed source accepted")
	}
	if _, err := gitBisectExecutionWorkspace(ctx, owned, runner.CommandRuntimeScope{RunID: "other"}, spec); err == nil {
		t.Fatal("foreign run accepted")
	}
	mixed := context.WithValue(ctx, batchValidationRuntimeKey{}, &batchValidationRuntimeProof{})
	if _, err := gitBisectExecutionWorkspace(mixed, owned, scope, spec); err == nil {
		t.Fatal("mixed Batch/Git proof accepted")
	}
	proof.check = func(context.Context) error { return errors.New("revoked") }
	if _, err := gitBisectExecutionWorkspace(ctx, owned, scope, spec); err == nil {
		t.Fatal("revoked authority accepted")
	}
}

func TestGitBisectDockerProcessFailureIsNotInfrastructureExit(t *testing.T) {
	for _, code := range []int{1, 125} {
		receipt := &domain.DockerSandboxReceipt{Outcome: domain.DockerSandboxOutcomeFailed, ReasonCode: domain.DockerSandboxReasonProcessFailed, ExitCode: &code, CleanupComplete: true}
		if !gitBisectDockerProcessExit(receipt, nil) {
			t.Fatalf("natural exit %d was not classifiable", code)
		}
		if gitBisectDockerProcessExit(receipt, errors.New("daemon disconnected")) {
			t.Fatal("transport error became recipe status")
		}
		for _, reason := range []string{domain.DockerSandboxReasonDaemonDisconnected, domain.DockerSandboxReasonCleanupFailed, domain.DockerSandboxReasonAuthorityChanged, domain.DockerSandboxReasonIOFailed} {
			receipt.ReasonCode = reason
			if gitBisectDockerProcessExit(receipt, nil) {
				t.Fatalf("infrastructure reason %s became recipe status", reason)
			}
		}
		receipt.ReasonCode = domain.DockerSandboxReasonProcessFailed
		receipt.CleanupComplete = false
		if gitBisectDockerProcessExit(receipt, nil) {
			t.Fatal("unreaped process was classifiable")
		}
	}
}
