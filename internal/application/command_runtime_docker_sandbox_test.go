package application

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
)

func commandDockerProofFixture(t *testing.T) (*commandApprovalSandboxFixture, *DockerSandboxCommandRuntimeExecutor, runner.CommandRuntimeScope, runner.CommandRuntimeResolvedSpec) {
	t.Helper()
	f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionAsk, domain.RunExecutionProfileDocker, true)
	e := f.service.sandbox.(*DockerSandboxCommandRuntimeExecutor)
	input := commandApprovalSandboxInput(t)
	spec, err := runner.NormalizeCommandRuntimeSpec(input.Commands[0], f.owned.Path)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := f.st.GetRunExecutionProfile(t.Context(), f.turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	permission, err := f.st.GetRunExecutionPermission(t.Context(), f.turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	lease, found, err := f.st.GetRunExecutionLease(t.Context(), f.turn.Run.ID)
	if err != nil || !found {
		t.Fatalf("lease found=%t error=%v", found, err)
	}
	scope := runner.CommandRuntimeScope{InvocationID: "docker-proof-invocation", OperationKey: "docker-proof-operation", RunID: f.turn.Run.ID, MissionID: f.turn.Mission.ID,
		RootAgentID: f.turn.Agent.ID, AgentID: f.turn.Agent.ID, AttributionSource: domain.AgentAttributionOperatorRoot, SessionID: f.turn.Run.SessionID,
		WorkspaceID: f.owned.SourceWorkspaceID, WorkspaceRootSHA256: spec.WorkspaceRootSHA256, ModeSnapshotID: f.turn.Mode.ID, ModeRevision: f.turn.Mode.Revision,
		ProfileSnapshotID: profile.ID, ProfileRevision: profile.Revision, PermissionSnapshotID: permission.ID, PermissionRevision: permission.Revision,
		PermissionMode: permission.Mode, LeaseID: lease.LeaseID, LeaseGeneration: lease.Generation, LeaseOwnerID: lease.OwnerID, Adapter: e.Identity()}
	if err := scope.Validate(); err != nil {
		t.Fatal(err)
	}
	return f, e, scope, spec
}

func assertDockerProofNoMutation(t *testing.T, f *commandApprovalSandboxFixture) {
	t.Helper()
	for _, event := range f.recorder.snapshot() {
		if strings.HasPrefix(event, "mutate:") {
			t.Fatalf("pre-dispatch rejection mutated daemon: %s", event)
		}
	}
}

func TestDockerCommandNaturalNonzeroExitIsAProgramResult(t *testing.T) {
	_, executor, scope, spec := commandDockerProofFixture(t)
	transport := executor.service.docker.lifecycleTransport.(*dockerLifecycleSupervisorTestTransport)
	transport.naturalExitCode = 1
	result, err := executor.ExecuteSandboxCommand(t.Context(), scope, spec, nil)
	if err != nil || result.ExitCode != 1 || !result.TreeReaped || transport.deletes != 1 {
		t.Fatalf("natural program failure became an infrastructure failure: result=%+v cleanup=%d error=%v", result, transport.deletes, err)
	}
}

func TestDockerCommandPreDispatchAuthorityRejectionHasNoContainerToReap(t *testing.T) {
	for _, kind := range []string{"permission", "lease"} {
		t.Run(kind, func(t *testing.T) {
			f, e, scope, spec := commandDockerProofFixture(t)
			if kind == "permission" {
				scope.PermissionMode = domain.RunExecutionPermissionConservative
			} else {
				scope.LeaseGeneration++
			}
			result, err := e.ExecuteSandboxCommand(t.Context(), scope, spec, nil)
			if err == nil || !result.TreeReaped || result.ExitCode != 125 {
				t.Fatalf("pre-dispatch rejection became uncertain cleanup: result=%+v error=%v", result, err)
			}
			assertDockerProofNoMutation(t, f)
		})
	}
}

type commandDockerPreflightFailStore struct {
	StandardCodeDockerStore
	reads int
}

func (s *commandDockerPreflightFailStore) GetRun(ctx context.Context, id string) (domain.Run, error) {
	s.reads++
	if s.reads == 2 {
		return domain.Run{}, errors.New("controlled execute preflight lookup failure")
	}
	return s.StandardCodeDockerStore.GetRun(ctx, id)
}

func TestDockerCommandInternalPreflightFailurePreservesKnownNoDispatch(t *testing.T) {
	f, e, scope, spec := commandDockerProofFixture(t)
	e.service.store = &commandDockerPreflightFailStore{StandardCodeDockerStore: e.service.store}
	result, err := e.ExecuteSandboxCommand(t.Context(), scope, spec, nil)
	if err == nil || !result.TreeReaped || result.ExitCode != 125 {
		t.Fatalf("internal preflight failure became uncertain cleanup: result=%+v error=%v", result, err)
	}
	assertDockerProofNoMutation(t, f)
}

type commandDockerCleanupFailTransport struct {
	*dockerLifecycleSupervisorTestTransport
}

func (*commandDockerCleanupFailTransport) Cleanup(context.Context, sandbox.DockerContainerLifecycleRequest, sandbox.DockerContainerLifecycleFence) (sandbox.DockerContainerLifecycleCleanupResult, error) {
	return sandbox.DockerContainerLifecycleCleanupResult{}, errors.New("controlled Docker cleanup failure")
}

func TestDockerCommandCleanupFailureDoesNotClaimReapedContainer(t *testing.T) {
	f, e, scope, spec := commandDockerProofFixture(t)
	base := e.service.docker.lifecycleTransport.(*dockerLifecycleSupervisorTestTransport)
	e.service.docker.lifecycleTransport = &commandDockerCleanupFailTransport{base}
	result, err := e.ExecuteSandboxCommand(t.Context(), scope, spec, nil)
	if err == nil || result.TreeReaped || result.ExitCode != 125 || base.creates != 1 || base.starts != 1 || base.state == sandbox.DockerContainerLifecycleStateAbsent {
		t.Fatalf("cleanup failure became a terminal receipt: result=%+v creates=%d starts=%d state=%s error=%v calls=%v", result, base.creates, base.starts, base.state, err, f.recorder.snapshot())
	}
}

type commandDockerPostCleanupFailStore struct{ StandardCodeDockerStore }

func (*commandDockerPostCleanupFailStore) GetDockerLogCaptureReceiptByAttempt(context.Context, string) (sandbox.DockerLogCaptureReceipt, bool, error) {
	return sandbox.DockerLogCaptureReceipt{}, false, errors.New("controlled post-cleanup log receipt lookup failure")
}

func TestDockerCommandPostCleanupFailureRetainsConfirmedTreeProof(t *testing.T) {
	f, e, scope, spec := commandDockerProofFixture(t)
	e.service.store = &commandDockerPostCleanupFailStore{e.service.store}
	base := e.service.docker.lifecycleTransport.(*dockerLifecycleSupervisorTestTransport)
	result, err := e.ExecuteSandboxCommand(t.Context(), scope, spec, nil)
	if err == nil || !result.TreeReaped || result.ExitCode != 125 || base.creates != 1 || base.starts != 1 || base.state != sandbox.DockerContainerLifecycleStateAbsent {
		t.Fatalf("confirmed cleanup was lost to result finalization: result=%+v creates=%d starts=%d state=%s error=%v calls=%v", result, base.creates, base.starts, base.state, err, f.recorder.snapshot())
	}
}

// This seam wraps the real SQLite store. It can move the clock or alter a host
// file after the actual admission commit, without running any Docker daemon.
type commandDockerStartSeamStore struct {
	DockerSandboxStore
	afterAdmission func(domain.DockerSandboxRecord)
	admissionID    string
	recordFailure  bool
	beginMode      string
}

func (s *commandDockerStartSeamStore) CreateDockerSandboxAdmission(ctx context.Context,
	value domain.DockerSandboxAdmission,
) (domain.DockerSandboxRecord, bool, error) {
	record, replayed, err := s.DockerSandboxStore.CreateDockerSandboxAdmission(ctx, value)
	if err == nil {
		s.admissionID = record.Admission.ID
		if !replayed && s.afterAdmission != nil {
			s.afterAdmission(record)
		}
	}
	return record, replayed, err
}

func (s *commandDockerStartSeamStore) GetDockerSandboxRecord(ctx context.Context,
	admissionID string,
) (domain.DockerSandboxRecord, error) {
	if s.recordFailure && s.admissionID != "" {
		return domain.DockerSandboxRecord{}, errors.New("controlled Start record lookup failure")
	}
	return s.DockerSandboxStore.GetDockerSandboxRecord(ctx, admissionID)
}

func (s *commandDockerStartSeamStore) BeginDockerSandboxStart(ctx context.Context,
	value domain.DockerSandboxStartIntent,
) (domain.DockerSandboxStartIntent, bool, error) {
	stored, replayed, err := s.DockerSandboxStore.BeginDockerSandboxStart(ctx, value)
	if err != nil {
		return stored, replayed, err
	}
	switch s.beginMode {
	case "committed_error":
		return domain.DockerSandboxStartIntent{}, false,
			errors.New("controlled loss of committed Start result")
	case "replay":
		return s.DockerSandboxStore.BeginDockerSandboxStart(ctx, value)
	default:
		return stored, replayed, nil
	}
}

func installDockerStartSeam(t *testing.T, e *DockerSandboxCommandRuntimeExecutor) *commandDockerStartSeamStore {
	t.Helper()
	seam := &commandDockerStartSeamStore{DockerSandboxStore: e.service.docker.store}
	e.service.docker.store = seam
	return seam
}

func alterDockerGitMetadataMask(t *testing.T, service *DockerSandboxService) {
	t.Helper()
	mask := filepath.Join(service.stagingRoot, standardCodeGitMetadataMaskFile)
	if err := os.Chmod(mask, 0o600); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.WriteFile(mask, []byte("changed host mask after admission\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDockerCommandFirstStartReadinessExpiryHasNoContainerToReap(t *testing.T) {
	f, e, scope, spec := commandDockerProofFixture(t)
	seam := installDockerStartSeam(t, e)
	var expiry time.Time
	seam.afterAdmission = func(record domain.DockerSandboxRecord) {
		expiry = record.Admission.ReadinessExpiresAt
		e.service.docker.now = func() time.Time { return expiry }
	}
	result, err := e.ExecuteSandboxCommand(t.Context(), scope, spec, nil)
	if err == nil || !result.TreeReaped || result.ExitCode != 125 || expiry.IsZero() {
		t.Fatalf("first Start expiry became uncertain cleanup: result=%+v error=%v expiry=%v", result, err, expiry)
	}
	record, readErr := f.st.GetDockerSandboxRecord(t.Context(), seam.admissionID)
	if readErr != nil || record.Start != nil || record.Launch != nil || record.Receipt != nil {
		t.Fatalf("expired first admission acquired execution WAL: record=%+v error=%v", record, readErr)
	}
	assertDockerProofNoMutation(t, f)
}

func TestDockerCommandFirstStartDeepValidationRefusalHasNoContainerToReap(t *testing.T) {
	f, e, scope, spec := commandDockerProofFixture(t)
	seam := installDockerStartSeam(t, e)
	seam.afterAdmission = func(domain.DockerSandboxRecord) { alterDockerGitMetadataMask(t, e.service.docker) }
	result, err := e.ExecuteSandboxCommand(t.Context(), scope, spec, nil)
	if err == nil || !result.TreeReaped || result.ExitCode != 125 {
		t.Fatalf("first Start reconstruction refusal became uncertain cleanup: result=%+v error=%v", result, err)
	}
	record, readErr := f.st.GetDockerSandboxRecord(t.Context(), seam.admissionID)
	if readErr != nil || record.Start == nil || record.Launch != nil || record.Receipt != nil {
		t.Fatalf("deep refusal did not remain before lifecycle: record=%+v error=%v", record, readErr)
	}
	assertDockerProofNoMutation(t, f)
}

func TestDockerCommandFirstStartUncertainStoreOutcomeRequiresRecovery(t *testing.T) {
	for _, failure := range []string{"record_read", "committed_error", "replay"} {
		t.Run(failure, func(t *testing.T) {
			f, e, scope, spec := commandDockerProofFixture(t)
			seam := installDockerStartSeam(t, e)
			seam.recordFailure = failure == "record_read"
			seam.beginMode = failure
			if failure == "replay" {
				// An existing Start returned by Begin must remain uncertain even
				// when this call subsequently fails before entering lifecycle.
				seam.afterAdmission = func(domain.DockerSandboxRecord) { alterDockerGitMetadataMask(t, e.service.docker) }
			}
			result, err := e.ExecuteSandboxCommand(t.Context(), scope, spec, nil)
			if err == nil || result.TreeReaped || result.ExitCode != 125 {
				t.Fatalf("uncertain %s was certified without cleanup: result=%+v error=%v", failure, result, err)
			}
			record, readErr := f.st.GetDockerSandboxRecord(t.Context(), seam.admissionID)
			if readErr != nil || (failure == "record_read" && record.Start != nil) ||
				(failure != "record_read" && record.Start == nil) || record.Launch != nil || record.Receipt != nil {
				t.Fatalf("unexpected durable state for %s: record=%+v error=%v", failure, record, readErr)
			}
			assertDockerProofNoMutation(t, f)
		})
	}
}

func TestDockerCommandExistingStartAndLaunchDoNotInventNoDispatchProof(t *testing.T) {
	for _, previous := range []string{"start", "launch"} {
		t.Run(previous, func(t *testing.T) {
			f, e, scope, spec := commandDockerProofFixture(t)
			seam := installDockerStartSeam(t, e)
			if previous == "start" {
				seam.beginMode = "committed_error"
			} else {
				base := e.service.docker.lifecycleTransport.(*dockerLifecycleSupervisorTestTransport)
				e.service.docker.lifecycleTransport = &commandDockerCleanupFailTransport{base}
			}
			first, err := e.ExecuteSandboxCommand(t.Context(), scope, spec, nil)
			if err == nil || first.TreeReaped {
				t.Fatalf("setup did not leave uncertain %s: result=%+v error=%v", previous, first, err)
			}
			record, err := f.st.GetDockerSandboxRecord(t.Context(), seam.admissionID)
			if err != nil || record.Start == nil || (previous == "launch" && record.Launch == nil) || record.Receipt != nil {
				t.Fatalf("missing durable %s setup: record=%+v error=%v", previous, record, err)
			}
			alterDockerGitMetadataMask(t, e.service.docker)
			ctx, output := withDockerCommandRuntimeOutput(t.Context(), scope.RunID, spec.Spec.Output.ArtifactBytes)
			if err := output.bind(scope.RunID, record.Admission.ID); err != nil {
				t.Fatal(err)
			}
			baseKey := "command-runtime-docker-" + runmutation.Fingerprint(
				"command_runtime_docker_adapter_operation.v1", scope.RunID, scope.OperationKey)[:24]
			_, err = e.service.docker.Start(ctx, DockerSandboxStartRequest{
				AdmissionID:  record.Admission.ID,
				OperationKey: standardCodeStageKey(baseKey+"-execute", "start"), RequestedBy: scope.RootAgentID,
			})
			if err == nil || output.noOwnedTree() {
				t.Fatalf("existing %s replay cleared uncertainty on deep refusal: error=%v noOwnedTree=%t", previous, err, output.noOwnedTree())
			}
			if previous == "start" {
				assertDockerProofNoMutation(t, f)
			}
		})
	}
}

type commandDockerGatedStartStore struct {
	DockerSandboxStore
	recordReads atomic.Int32
	entered     chan struct{}
	release     chan struct{}
}

func (s *commandDockerGatedStartStore) GetDockerSandboxRecord(ctx context.Context,
	admissionID string,
) (domain.DockerSandboxRecord, error) {
	if s.recordReads.Add(1) == 1 {
		close(s.entered)
		<-s.release
	}
	return s.DockerSandboxStore.GetDockerSandboxRecord(ctx, admissionID)
}

func TestDockerCommandStartGateCoversFirstReadAndStickyCancellation(t *testing.T) {
	fixture := newDockerSandboxServiceFixture(t, "start-gate-before-read")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	admitted, err := fixture.service.Admit(ctx, DockerSandboxAdmissionRequest{
		PlanID: fixture.plan.ID, Manifest: fixture.manifest,
		OperationKey: "start-gate-admit", RequestedBy: fixture.requestedBy,
	})
	if err != nil || admitted.Admission == nil {
		t.Fatalf("Admit()=%+v error=%v", admitted, err)
	}
	gate := &commandDockerGatedStartStore{DockerSandboxStore: fixture.service.store,
		entered: make(chan struct{}), release: make(chan struct{})}
	defer func() {
		select {
		case <-gate.release:
		default:
			close(gate.release)
		}
	}()
	fixture.service.store = gate
	firstCtx, firstOutput := withDockerCommandRuntimeOutput(ctx, admitted.Admission.RunID, 1024)
	if err := firstOutput.bind(admitted.Admission.RunID, admitted.Admission.ID); err != nil {
		t.Fatal(err)
	}
	request := DockerSandboxStartRequest{AdmissionID: admitted.Admission.ID,
		OperationKey: "start-gate-start", RequestedBy: fixture.requestedBy}
	firstDone := make(chan error, 1)
	go func() {
		_, err := fixture.service.Start(firstCtx, request)
		firstDone <- err
	}()
	select {
	case <-gate.entered:
	case <-ctx.Done():
		t.Fatal("first Start did not enter its gated read")
	}
	secondCtx, secondOutput := withDockerCommandRuntimeOutput(ctx, admitted.Admission.RunID, 1024)
	if err := secondOutput.bind(admitted.Admission.RunID, admitted.Admission.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Start(secondCtx, request); err == nil || secondOutput.noOwnedTree() {
		t.Fatalf("concurrent Start bypassed active gate or invented absence proof: error=%v", err)
	}
	cancelRequest := DockerSandboxCancelRequest{AdmissionID: admitted.Admission.ID,
		OperationKey: "start-gate-cancel", RequestedBy: fixture.requestedBy}
	cancelled, err := fixture.service.Cancel(ctx, cancelRequest)
	if err != nil || cancelled.Cancellation.AdmissionID != admitted.Admission.ID ||
		cancelled.Record.Start != nil || cancelled.Record.Launch != nil || cancelled.Record.Receipt != nil ||
		fixture.lifecycle.creates != 0 || fixture.lifecycle.starts != 0 {
		t.Fatalf("Cancel started a parallel lifecycle while Start held the gate: result=%+v error=%v", cancelled, err)
	}
	differentCancel := cancelRequest
	differentCancel.OperationKey = "start-gate-different-cancel"
	if _, err := fixture.service.Cancel(ctx, differentCancel); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("different cancellation operation changed the sticky request: error=%v", err)
	}
	close(gate.release)
	select {
	case err := <-firstDone:
		if err == nil || firstOutput.noOwnedTree() {
			t.Fatalf("cancelled record read did not retain unknown provenance: error=%v", err)
		}
	case <-ctx.Done():
		t.Fatal("cancelled Start did not release its active gate")
	}
	// The persisted cancellation can be completed explicitly after the first
	// caller exits; it never creates/starts the cancelled admission.
	completed, err := fixture.service.Cancel(ctx, cancelRequest)
	if err != nil || completed.Record.Receipt == nil || !completed.Record.Receipt.CleanupComplete ||
		completed.Record.Receipt.Outcome != domain.DockerSandboxOutcomeCancelled ||
		completed.Cancellation.CancellationFingerprint != cancelled.Cancellation.CancellationFingerprint ||
		!completed.Cancellation.RequestedAt.Equal(cancelled.Cancellation.RequestedAt) ||
		fixture.lifecycle.creates != 0 || fixture.lifecycle.starts != 0 {
		t.Fatalf("sticky cancellation could not settle after the gate released: result=%+v error=%v", completed, err)
	}
	replayed, err := fixture.service.Cancel(ctx, cancelRequest)
	if err != nil || !replayed.Replayed || replayed.Record.Receipt == nil ||
		replayed.Record.Receipt.ID != completed.Record.Receipt.ID ||
		fixture.lifecycle.creates != 0 || fixture.lifecycle.starts != 0 {
		t.Fatalf("terminal Cancel replay changed durable result: result=%+v error=%v", replayed, err)
	}
	if _, err := fixture.service.Cancel(ctx, differentCancel); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("terminal Cancel replay accepted a different operation: error=%v", err)
	}
}

type commandDockerCancelBarrierStore struct {
	DockerSandboxStore
	reads   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (s *commandDockerCancelBarrierStore) GetDockerSandboxCancellation(ctx context.Context,
	admissionID string,
) (domain.DockerSandboxCancellation, bool, error) {
	value, found, err := s.DockerSandboxStore.GetDockerSandboxCancellation(ctx, admissionID)
	if s.reads.Add(1) <= 2 {
		s.entered <- struct{}{}
		select {
		case <-s.release:
		case <-ctx.Done():
			return domain.DockerSandboxCancellation{}, false, ctx.Err()
		}
	}
	return value, found, err
}

func TestDockerCommandConcurrentIdenticalCancelConvergesExactStoredRequest(t *testing.T) {
	fixture := newDockerSandboxServiceFixture(t, "same-cancel-concurrent")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	admitted, err := fixture.service.Admit(ctx, DockerSandboxAdmissionRequest{
		PlanID: fixture.plan.ID, Manifest: fixture.manifest,
		OperationKey: "same-cancel-admit", RequestedBy: fixture.requestedBy,
	})
	if err != nil || admitted.Admission == nil {
		t.Fatalf("Admit()=%+v error=%v", admitted, err)
	}
	barrier := &commandDockerCancelBarrierStore{DockerSandboxStore: fixture.service.store,
		entered: make(chan struct{}, 2), release: make(chan struct{})}
	fixture.service.store = barrier
	defer func() {
		select {
		case <-barrier.release:
		default:
			close(barrier.release)
		}
	}()
	var clockReads atomic.Int32
	fixture.service.now = func() time.Time {
		return admitted.Admission.CreatedAt.Add(time.Duration(clockReads.Add(1)) * time.Nanosecond)
	}
	_, activeCancel, err := fixture.service.registerActive(admitted.Admission.ID, ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.service.unregisterActive(admitted.Admission.ID)
	defer activeCancel()
	request := DockerSandboxCancelRequest{AdmissionID: admitted.Admission.ID,
		OperationKey: "same-cancel-operation", RequestedBy: fixture.requestedBy}
	type result struct {
		value DockerSandboxCancelResult
		err   error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			value, err := fixture.service.Cancel(ctx, request)
			results <- result{value: value, err: err}
		}()
	}
	for range 2 {
		select {
		case <-barrier.entered:
		case <-ctx.Done():
			t.Fatal("both first cancellation requests did not observe absence")
		}
	}
	close(barrier.release)
	var fingerprint string
	for range 2 {
		select {
		case got := <-results:
			if got.err != nil || got.value.Cancellation.Validate() != nil ||
				(fingerprint != "" && fingerprint != got.value.Cancellation.CancellationFingerprint) {
				t.Fatalf("same first cancellation requests did not converge: result=%+v error=%v", got.value, got.err)
			}
			fingerprint = got.value.Cancellation.CancellationFingerprint
		case <-ctx.Done():
			t.Fatal("concurrent cancellation recovery did not finish")
		}
	}
	stored, found, err := fixture.store.GetDockerSandboxCancellation(ctx, admitted.Admission.ID)
	if err != nil || !found || stored.CancellationFingerprint != fingerprint || barrier.reads.Load() != 3 ||
		fixture.lifecycle.creates != 0 || fixture.lifecycle.starts != 0 {
		t.Fatalf("bounded conflict recovery changed identity or dispatched: stored=%+v found=%t reads=%d error=%v", stored, found, barrier.reads.Load(), err)
	}
	foreign := request
	foreign.RequestedBy = "another_operator"
	if _, err := fixture.service.Cancel(ctx, foreign); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("foreign requester borrowed stored cancellation: error=%v", err)
	}
}
