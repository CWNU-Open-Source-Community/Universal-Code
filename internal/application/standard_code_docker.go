package application

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/runworktree"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/standardcode"
)

// StandardCodeDockerStore is the small read surface used by the adapter. All
// execution state remains in the existing sandbox, Docker lifecycle, log, and
// Drydock ledgers owned by the services below.
type StandardCodeDockerStore interface {
	GetRun(context.Context, string) (domain.Run, error)
	GetMission(context.Context, string) (domain.Mission, error)
	GetRunExecutionProfile(context.Context, string) (
		domain.RunExecutionProfileSnapshot, error)
	GetRunExecutionPermission(context.Context, string) (
		domain.RunExecutionPermissionSnapshot, error)
	GetRunExecutionLease(context.Context, string) (domain.RunExecutionLease, bool, error)
	GetDrydockByRun(context.Context, string) (runworktree.Workspace, bool, error)
	GetSandboxExecutionCandidate(context.Context, string) (
		sandbox.ValidatedExecutionCandidate, error)
	GetDockerLogCaptureReceiptByAttempt(context.Context, string) (
		sandbox.DockerLogCaptureReceipt, bool, error)
	GetDockerSandboxAdmissionByOperation(context.Context, string) (
		domain.DockerSandboxAdmission, bool, error)
	GetDockerSandboxRecord(context.Context, string) (domain.DockerSandboxRecord, error)
	ListCompletedStandardCodeDockerSandboxes(context.Context, int) (
		[]domain.DockerSandboxRecord, error)
	GetDrydockReceiptByOperation(context.Context, string) (runworktree.Receipt, bool, error)
}

type StandardCodeDockerService struct {
	store         StandardCodeDockerStore
	drydocks      *RunWorktreeService
	manifests     *SandboxManifestService
	docker        *DockerSandboxService
	imageDigest   string
	authorityPoll time.Duration
}

func NewStandardCodeDockerService(store StandardCodeDockerStore,
	drydocks *RunWorktreeService, manifests *SandboxManifestService,
	docker *DockerSandboxService, imageDigest string,
) (*StandardCodeDockerService, error) {
	imageDigest = strings.TrimSpace(imageDigest)
	if store == nil || drydocks == nil || manifests == nil || docker == nil ||
		!sandbox.ValidOCIImageDigest(imageDigest) ||
		docker.standardCodeDrydock != drydocks ||
		docker.standardCodeImageDigest != imageDigest {
		return nil, errors.New("Standard Code Docker service dependencies are invalid")
	}
	manifests.WithStandardCodeDrydock(drydocks)
	return &StandardCodeDockerService{store: store, drydocks: drydocks,
		manifests: manifests, docker: docker, imageDigest: imageDigest,
		authorityPoll: 250 * time.Millisecond}, nil
}

type StandardCodeDockerPrepareRequest struct {
	RunID              string               `json:"run_id"`
	ExpectedGeneration int64                `json:"expected_generation"`
	ExpectedCheckpoint string               `json:"expected_checkpoint_id"`
	OperationKey       string               `json:"operation_key"`
	RequestedBy        string               `json:"requested_by"`
	Command            standardcode.Command `json:"command"`
}

type StandardCodeDockerPrepareResult struct {
	Readiness   standardcode.BackendReadiness `json:"readiness"`
	Preparation *sandbox.PreparedIntent       `json:"preparation,omitempty"`
	Approval    *approval.Record              `json:"approval,omitempty"`
	Blocked     bool                          `json:"blocked"`
}

type StandardCodeDockerReadinessRequest struct {
	RunID              string               `json:"run_id"`
	ExpectedGeneration int64                `json:"expected_generation"`
	ExpectedCheckpoint string               `json:"expected_checkpoint_id"`
	Command            standardcode.Command `json:"command"`
}

func (s *StandardCodeDockerService) Readiness(ctx context.Context,
	request StandardCodeDockerReadinessRequest,
) (standardcode.BackendReadiness, error) {
	_, manifest, err := s.compileCurrent(ctx, request.RunID,
		request.ExpectedGeneration, request.ExpectedCheckpoint, request.Command,
		sandbox.DockerStandardCodeStdinClosed, true)
	if err != nil {
		return standardcode.BackendReadiness{}, err
	}
	readiness, err := s.docker.StandardCodeReadiness(ctx, manifest)
	if err != nil {
		return standardcode.BackendReadiness{}, err
	}
	return standardcode.DockerReadiness(readiness)
}

func (s *StandardCodeDockerService) Prepare(ctx context.Context,
	request StandardCodeDockerPrepareRequest,
) (StandardCodeDockerPrepareResult, error) {
	return s.prepare(ctx, request, sandbox.DockerStandardCodeStdinClosed)
}

func (s *StandardCodeDockerService) prepareCommandRuntime(ctx context.Context,
	request StandardCodeDockerPrepareRequest, stdinPolicy string,
) (StandardCodeDockerPrepareResult, error) {
	return s.prepare(ctx, request, stdinPolicy)
}

func (s *StandardCodeDockerService) prepare(ctx context.Context,
	request StandardCodeDockerPrepareRequest, stdinPolicy string,
) (StandardCodeDockerPrepareResult, error) {
	request.OperationKey = strings.TrimSpace(request.OperationKey)
	request.RequestedBy = strings.TrimSpace(request.RequestedBy)
	if !validDockerSandboxOperationKey(request.OperationKey) ||
		!domain.ValidAgentID(request.RequestedBy) {
		return StandardCodeDockerPrepareResult{}, apperror.New(
			apperror.CodeInvalidArgument,
			"Standard Code preparation operation or operator is invalid")
	}
	_, manifest, err := s.compileCurrent(ctx, request.RunID,
		request.ExpectedGeneration, request.ExpectedCheckpoint, request.Command,
		stdinPolicy, true)
	if err != nil {
		return StandardCodeDockerPrepareResult{}, err
	}
	readiness, err := s.docker.StandardCodeReadiness(ctx, manifest)
	if err != nil {
		return StandardCodeDockerPrepareResult{}, err
	}
	projection, err := standardcode.DockerReadiness(readiness)
	if err != nil {
		return StandardCodeDockerPrepareResult{}, err
	}
	result := StandardCodeDockerPrepareResult{Readiness: projection,
		Blocked: !readiness.Ready}
	if !readiness.Ready {
		return result, nil
	}
	prepared, err := s.manifests.Prepare(ctx, PrepareSandboxManifestRequest{
		RunID: request.RunID, Manifest: manifest,
		OperationKey: standardCodeStageKey(request.OperationKey, "prepare"),
		RequestedBy:  request.RequestedBy,
	})
	if err != nil {
		return result, err
	}
	if !prepared.Validation.NeedsApproval {
		return result, apperror.New(apperror.CodeFailedPrecondition,
			"Standard Code Docker requires exact per-call approval")
	}
	record, err := s.manifests.RequestApproval(ctx, prepared.Preparation.ID,
		request.RequestedBy)
	if err != nil {
		return result, err
	}
	result.Preparation, result.Approval = &prepared, &record
	return result, nil
}

type StandardCodeDockerExecuteRequest struct {
	RunID              string               `json:"run_id"`
	ExpectedGeneration int64                `json:"expected_generation"`
	ExpectedCheckpoint string               `json:"expected_checkpoint_id"`
	PreparationID      string               `json:"preparation_id"`
	ApprovalID         string               `json:"approval_id"`
	OperationKey       string               `json:"operation_key"`
	RequestedBy        string               `json:"requested_by"`
	Command            standardcode.Command `json:"command"`
}

type StandardCodeDockerExecuteResult struct {
	Readiness   standardcode.BackendReadiness `json:"readiness"`
	AdmissionID string                        `json:"admission_id,omitempty"`
	Result      *standardcode.Result          `json:"result,omitempty"`
	Executed    bool                          `json:"executed"`
	batchRecord *domain.DockerSandboxRecord
}

type StandardCodeDockerCancelRequest struct {
	AdmissionID  string `json:"admission_id"`
	OperationKey string `json:"operation_key"`
	RequestedBy  string `json:"requested_by"`
}

func (s *StandardCodeDockerService) Cancel(ctx context.Context,
	request StandardCodeDockerCancelRequest,
) (*standardcode.Result, error) {
	owned, err := s.docker.Get(ctx, request.AdmissionID)
	if err != nil {
		return nil, err
	}
	if isNativeCommandDockerRequester(owned.Admission.RequestedBy) {
		return nil, apperror.New(apperror.CodePolicyDenied, "cancel native commands through their Command Runtime Job")
	}
	cancelled, err := s.docker.Cancel(ctx, DockerSandboxCancelRequest(request))
	if err != nil || cancelled.Record.Receipt == nil {
		return nil, err
	}
	manifest, err := sandbox.DecodeManifest(
		[]byte(cancelled.Record.Admission.ManifestJSON))
	if err != nil {
		return nil, err
	}
	binding, ok := sandbox.ParseDockerStandardCodeManifest(manifest)
	if !ok {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"Docker admission is not a Standard Code execution")
	}
	result, err := s.finalize(ctx, cancelled.Record,
		executionContextFromDockerBinding(binding),
		cancelled.Record.Admission.RequestedBy)
	return &result, err
}

func (s *StandardCodeDockerService) Execute(ctx context.Context,
	request StandardCodeDockerExecuteRequest,
) (StandardCodeDockerExecuteResult, error) {
	return s.execute(ctx, request, nil, sandbox.DockerStandardCodeStdinClosed, nil)
}

func (s *StandardCodeDockerService) executeCommandRuntime(ctx context.Context,
	request StandardCodeDockerExecuteRequest, lease domain.RunExecutionLease,
	stdinPolicy string, stdin io.ReadCloser,
) (StandardCodeDockerExecuteResult, error) {
	if lease.Validate() != nil || lease.RunID != request.RunID ||
		lease.Status != domain.RunExecutionLeaseActive ||
		(stdinPolicy == sandbox.DockerStandardCodeStdinPipe) != (stdin != nil) ||
		(stdinPolicy != sandbox.DockerStandardCodeStdinClosed &&
			stdinPolicy != sandbox.DockerStandardCodeStdinPipe) {
		return StandardCodeDockerExecuteResult{}, apperror.New(
			apperror.CodeInvalidArgument,
			"Standard Code Command Runtime lease is invalid")
	}
	return s.execute(ctx, request, &lease, stdinPolicy, stdin)
}

func (s *StandardCodeDockerService) execute(ctx context.Context,
	request StandardCodeDockerExecuteRequest, runLease *domain.RunExecutionLease,
	stdinPolicy string, stdin io.ReadCloser,
) (StandardCodeDockerExecuteResult, error) {
	request, err := normalizeStandardCodeDockerExecuteRequest(request)
	if err != nil {
		return StandardCodeDockerExecuteResult{}, err
	}
	if replay, found, replayErr := s.loadTerminalReplay(ctx, request,
		runLease, stdinPolicy); replayErr != nil {
		return StandardCodeDockerExecuteResult{}, replayErr
	} else if found {
		return replay, nil
	}
	scope, manifest, err := s.compileCurrent(ctx, request.RunID,
		request.ExpectedGeneration, request.ExpectedCheckpoint, request.Command,
		stdinPolicy, true)
	if err != nil {
		return StandardCodeDockerExecuteResult{}, err
	}
	readiness, err := s.docker.StandardCodeReadiness(ctx, manifest)
	if err != nil {
		return StandardCodeDockerExecuteResult{}, err
	}
	projection, err := standardcode.DockerReadiness(readiness)
	if err != nil {
		return StandardCodeDockerExecuteResult{}, err
	}
	result := StandardCodeDockerExecuteResult{Readiness: projection}
	if !readiness.Ready {
		return result, nil
	}
	candidateRequest := ValidateSandboxExecutionCandidateRequest{
		PreparationID: request.PreparationID, Manifest: manifest,
		ApprovalID:   request.ApprovalID,
		OperationKey: standardCodeStageKey(request.OperationKey, "candidate"),
		RequestedBy:  request.RequestedBy}
	var candidate sandbox.ValidatedExecutionCandidate
	if runLease == nil {
		candidate, err = s.manifests.ValidateExecutionCandidate(ctx, candidateRequest)
	} else {
		candidate, err = s.manifests.validateLeaseBoundExecutionCandidate(ctx,
			candidateRequest, *runLease)
	}
	if err != nil {
		return result, err
	}
	lifecycle, err := s.manifests.BeginDisabledExecution(ctx,
		BeginSandboxExecutionRequest{CandidateID: candidate.Candidate.ID,
			Manifest:     manifest,
			OperationKey: standardCodeStageKey(request.OperationKey, "begin"),
			RequestedBy:  request.RequestedBy})
	if err != nil {
		return result, err
	}
	preflight, err := s.manifests.PrepareDisabledPreflight(ctx,
		PrepareSandboxPreflightRequest{ExecutionID: lifecycle.Execution.ID,
			Manifest:     manifest,
			OperationKey: standardCodeStageKey(request.OperationKey, "preflight"),
			RequestedBy:  request.RequestedBy})
	if err != nil {
		return result, err
	}
	evidence, err := s.manifests.RecordSimulatedBackendEvidence(ctx,
		RecordSandboxBackendEvidenceRequest{PreflightID: preflight.ID,
			Manifest: manifest, ImageDigest: s.imageDigest,
			OperationKey: standardCodeStageKey(request.OperationKey, "evidence"),
			RequestedBy:  request.RequestedBy})
	if err != nil {
		return result, err
	}
	fixture := sandbox.OutputFixture{ProtocolVersion: sandbox.OutputFixtureProtocolVersion,
		Outputs: []sandbox.OutputFixtureItem{
			{Kind: sandbox.OutputKindStdout,
				FileType: sandbox.OutputFileTypeStream, Content: "standard-code stdout"},
			{Kind: sandbox.OutputKindStderr,
				FileType: sandbox.OutputFileTypeStream, Content: "standard-code stderr"},
		}}
	simulation, err := s.manifests.SimulateOutputTransaction(ctx,
		SimulateSandboxOutputRequest{EvidenceID: evidence.ID, Manifest: manifest,
			Fixture:      fixture,
			OperationKey: standardCodeStageKey(request.OperationKey, "output"),
			RequestedBy:  request.RequestedBy})
	if err != nil {
		return result, err
	}
	observation, err := s.manifests.ObserveDockerBackend(ctx,
		ObserveDockerBackendRequest{EvidenceID: evidence.ID,
			OutputSimulationID: simulation.ID, Manifest: manifest,
			OperationKey: standardCodeStageKey(request.OperationKey, "observe"),
			RequestedBy:  request.RequestedBy})
	if err != nil {
		return result, err
	}
	plan, err := s.manifests.CompileDockerContainerPlan(ctx,
		CompileDockerContainerPlanRequest{ObservationID: observation.ID,
			Manifest:     manifest,
			OperationKey: standardCodeStageKey(request.OperationKey, "plan"),
			RequestedBy:  request.RequestedBy})
	if err != nil {
		return result, err
	}
	admission, err := s.docker.Admit(ctx, DockerSandboxAdmissionRequest{
		PlanID: plan.ID, Manifest: manifest,
		OperationKey: standardCodeStageKey(request.OperationKey, "admit"),
		RequestedBy:  request.RequestedBy})
	if err != nil {
		return result, err
	}
	if !admission.Allowed || admission.Admission == nil {
		result.Readiness, err = standardcode.DockerReadiness(admission.Readiness)
		if err != nil {
			return result, err
		}
		return result, nil
	}
	result.AdmissionID = admission.Admission.ID
	if output := dockerCommandRuntimeOutputFromContext(ctx); output != nil {
		if runLease == nil {
			return result, errors.New("Docker Command Runtime output requires its bound execution lease")
		}
		if err := output.bind(scope.RunID, admission.Admission.ID); err != nil {
			return result, err
		}
	}
	var releaseStdin func()
	if stdinPolicy == sandbox.DockerStandardCodeStdinPipe {
		releaseStdin, err = s.docker.registerCommandRuntimeStdin(
			admission.Admission.ID, stdin)
		if err != nil {
			return result, err
		}
		defer releaseStdin()
	}
	executionContext, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go s.monitorCurrentAuthority(executionContext, done, scope, cancel)
	started, startErr := s.docker.Start(executionContext, DockerSandboxStartRequest{
		AdmissionID:  admission.Admission.ID,
		OperationKey: standardCodeStageKey(request.OperationKey, "start"),
		RequestedBy:  request.RequestedBy})
	close(done)
	cancel()
	if started.Record.Receipt == nil {
		return result, startErr
	}
	if output := dockerCommandRuntimeOutputFromContext(ctx); output != nil {
		if err := output.confirmCleanup(started.Record); err != nil {
			return result, errors.Join(startErr, err)
		}
	}
	if isNativeCommandDockerRequester(request.RequestedBy) {
		if isWorkerCommandDockerRequester(request.RequestedBy) {
			proof := workerCommandRuntimeFromContext(ctx)
			if proof == nil || proof.scope.RunID != scope.RunID || workerCommandDockerRequester(proof) != request.RequestedBy {
				return result, runner.ErrCommandRuntimeBoundary
			}
		} else if isGitBisectDockerRequester(request.RequestedBy) {
			proof := gitBisectRuntimeFromContext(ctx)
			if proof == nil || proof.runID != scope.RunID || gitBisectDockerRequester(proof) != request.RequestedBy {
				return result, runner.ErrCommandRuntimeBoundary
			}
		} else {
			proof := batchValidationRuntimeFromContext(ctx)
			if proof == nil || proof.target.runID != scope.RunID || batchValidationDockerRequester(proof.target) != request.RequestedBy {
				return result, runner.ErrCommandRuntimeBoundary
			}
		}
		// The container wrote only the native command target. Its terminal lifecycle
		// receipt must not advance the unrelated Run Drydock checkpoint.
		result.batchRecord, result.Executed = &started.Record, true
		return result, startErr
	}
	common, finalizeErr := s.finalize(ctx, started.Record, scope,
		request.RequestedBy)
	if finalizeErr != nil {
		return result, errors.Join(startErr, finalizeErr)
	}
	result.Result, result.Executed = &common, true
	return result, startErr
}

func (s *StandardCodeDockerService) RecoverStartup(ctx context.Context) (
	[]standardcode.Result, error,
) {
	records, err := s.docker.RecoverStartup(ctx)
	if err != nil {
		return nil, err
	}
	completed, err := s.store.ListCompletedStandardCodeDockerSandboxes(ctx,
		DockerSandboxRecoveryLimit)
	if err != nil {
		return nil, apperror.Normalize(err)
	}
	seen := make(map[string]struct{}, len(records)+len(completed))
	candidates := make([]domain.DockerSandboxRecord, 0, len(records)+len(completed))
	for _, record := range append(records, completed...) {
		if isNativeCommandDockerRequester(record.Admission.RequestedBy) {
			continue
		}
		if _, duplicate := seen[record.Admission.ID]; duplicate {
			continue
		}
		seen[record.Admission.ID] = struct{}{}
		candidates = append(candidates, record)
	}
	results := make([]standardcode.Result, 0, len(candidates))
	for _, record := range candidates {
		manifest, decodeErr := sandbox.DecodeManifest([]byte(record.Admission.ManifestJSON))
		if decodeErr != nil {
			return results, decodeErr
		}
		binding, standardCode := sandbox.ParseDockerStandardCodeManifest(manifest)
		if !standardCode || record.Launch == nil || record.Receipt == nil {
			continue
		}
		checkpointOperation := standardCodeStageKey(record.Admission.ID, "checkpoint")
		checkpointDigest := drydockOperationDigest(runworktree.OperationCheckpoint,
			record.Admission.RunID, checkpointOperation)
		if _, found, receiptErr := s.store.GetDrydockReceiptByOperation(ctx,
			checkpointDigest); receiptErr != nil {
			return results, apperror.Normalize(receiptErr)
		} else if found {
			continue
		}
		scope := executionContextFromDockerBinding(binding)
		value, finalizeErr := s.finalize(ctx, record, scope,
			record.Admission.RequestedBy)
		if finalizeErr != nil {
			return results, finalizeErr
		}
		results = append(results, value)
	}
	return results, nil
}

func normalizeStandardCodeDockerExecuteRequest(
	request StandardCodeDockerExecuteRequest,
) (StandardCodeDockerExecuteRequest, error) {
	request.RunID = strings.TrimSpace(request.RunID)
	request.ExpectedCheckpoint = strings.TrimSpace(request.ExpectedCheckpoint)
	request.PreparationID = strings.TrimSpace(request.PreparationID)
	request.ApprovalID = strings.TrimSpace(request.ApprovalID)
	request.OperationKey = strings.TrimSpace(request.OperationKey)
	request.RequestedBy = strings.TrimSpace(request.RequestedBy)
	if !domain.ValidAgentID(request.RunID) || request.ExpectedGeneration < 1 ||
		!domain.ValidAgentID(request.ExpectedCheckpoint) ||
		!domain.ValidAgentID(request.PreparationID) ||
		!domain.ValidAgentID(request.ApprovalID) ||
		!validDockerSandboxOperationKey(request.OperationKey) ||
		!domain.ValidAgentID(request.RequestedBy) || request.Command.Validate() != nil {
		return StandardCodeDockerExecuteRequest{}, apperror.New(
			apperror.CodeInvalidArgument,
			"Standard Code execution authority or command is invalid")
	}
	return request, nil
}

func (s *StandardCodeDockerService) loadTerminalReplay(ctx context.Context,
	request StandardCodeDockerExecuteRequest, runLease *domain.RunExecutionLease,
	stdinPolicy string,
) (StandardCodeDockerExecuteResult, bool, error) {
	operationDigest := runmutation.Fingerprint("docker_sandbox_admission_operation.v1",
		request.RunID, standardCodeStageKey(request.OperationKey, "admit"))
	admission, found, err := s.store.GetDockerSandboxAdmissionByOperation(ctx,
		operationDigest)
	if err != nil || !found {
		if err != nil {
			if output := dockerCommandRuntimeOutputFromContext(ctx); output != nil {
				// A failed lookup cannot exclude a previous dispatch of this
				// exact operation. Do not invent an absence receipt.
				output.noteDispatchPossible()
			}
		}
		return StandardCodeDockerExecuteResult{}, false, apperror.Normalize(err)
	}
	record, err := s.store.GetDockerSandboxRecord(ctx, admission.ID)
	if output := dockerCommandRuntimeOutputFromContext(ctx); output != nil &&
		(err != nil || record.Start != nil || record.Launch != nil) {
		output.noteDispatchPossible()
	}
	if err != nil {
		return StandardCodeDockerExecuteResult{}, true, apperror.Normalize(err)
	}
	manifest, err := sandbox.DecodeManifest([]byte(admission.ManifestJSON))
	if err != nil {
		return StandardCodeDockerExecuteResult{}, true, err
	}
	binding, exact := sandbox.ParseDockerStandardCodeManifest(manifest)
	if !exact || admission.RunID != request.RunID ||
		admission.PreparationID != request.PreparationID ||
		admission.ApprovalID != request.ApprovalID ||
		admission.RequestedBy != request.RequestedBy ||
		binding.DrydockGeneration != request.ExpectedGeneration ||
		binding.CheckpointID != request.ExpectedCheckpoint ||
		binding.StdinPolicy != stdinPolicy ||
		!standardCodeCommandMatchesBinding(request.Command, binding) {
		return StandardCodeDockerExecuteResult{}, true, apperror.New(
			apperror.CodeConflict,
			"Standard Code execution operation was used for different intent")
	}
	candidate, err := s.store.GetSandboxExecutionCandidate(ctx, admission.CandidateID)
	if err != nil {
		return StandardCodeDockerExecuteResult{}, true, apperror.Normalize(err)
	}
	if runLease == nil {
		if !candidate.Candidate.LeaseQuiescent {
			return StandardCodeDockerExecuteResult{}, true, apperror.New(
				apperror.CodeConflict,
				"Standard Code replay lease mode changed")
		}
	} else if !executionCandidateMatchesRunLease(candidate.Candidate, *runLease) {
		return StandardCodeDockerExecuteResult{}, true, apperror.New(
			apperror.CodeConflict,
			"Standard Code replay Run lease changed")
	}
	if record.Receipt == nil {
		return StandardCodeDockerExecuteResult{}, false, nil
	}
	if output := dockerCommandRuntimeOutputFromContext(ctx); output != nil {
		if err := output.confirmCleanup(record); err != nil {
			return StandardCodeDockerExecuteResult{}, true, err
		}
	}
	if isNativeCommandDockerRequester(admission.RequestedBy) {
		record.Replayed = true
		return StandardCodeDockerExecuteResult{AdmissionID: admission.ID, batchRecord: &record, Executed: true}, true, nil
	}
	readiness, err := s.docker.StandardCodeReadiness(ctx, manifest)
	if err != nil {
		return StandardCodeDockerExecuteResult{}, true, err
	}
	projection, err := standardcode.DockerReadiness(readiness)
	if err != nil {
		return StandardCodeDockerExecuteResult{}, true, err
	}
	value, err := s.finalize(ctx, record, executionContextFromDockerBinding(binding),
		admission.RequestedBy)
	if err != nil {
		return StandardCodeDockerExecuteResult{}, true, err
	}
	value.Replayed = true
	return StandardCodeDockerExecuteResult{Readiness: projection,
		AdmissionID: admission.ID, Result: &value, Executed: true}, true, nil
}

func standardCodeCommandMatchesBinding(command standardcode.Command,
	binding sandbox.DockerStandardCodeRunnerBinding,
) bool {
	commandSHA256, err := command.Fingerprint()
	if err != nil || commandSHA256 != binding.CommandSHA256 ||
		command.Toolchain != binding.Toolchain ||
		command.WorkingDirectory != binding.WorkingDirectory ||
		command.TimeoutSeconds != binding.TimeoutSeconds ||
		len(command.Arguments) != len(binding.Arguments) {
		return false
	}
	for index := range command.Arguments {
		if command.Arguments[index] != binding.Arguments[index] {
			return false
		}
	}
	return true
}

func executionContextFromDockerBinding(
	binding sandbox.DockerStandardCodeRunnerBinding,
) standardcode.ExecutionContext {
	return standardcode.ExecutionContext{RunID: binding.RunID,
		MissionID: binding.MissionID, SessionID: binding.SessionID,
		WorkspaceID: binding.WorkspaceID, DrydockID: binding.DrydockID,
		DrydockWorkspaceID:   binding.DrydockWorkspaceID,
		DrydockGeneration:    binding.DrydockGeneration,
		CheckpointID:         binding.CheckpointID,
		DrydockBindingSHA256: binding.DrydockBindingSHA256,
		ProfileSnapshotID:    binding.ProfileSnapshotID,
		ProfileRevision:      binding.ProfileRevision,
		PermissionSnapshotID: binding.PermissionSnapshotID,
		PermissionRevision:   binding.PermissionRevision,
		CapabilityGeneration: binding.CapabilityGeneration}
}

func (s *StandardCodeDockerService) compileCurrent(ctx context.Context, runID string,
	expectedGeneration int64, expectedCheckpoint string, command standardcode.Command,
	stdinPolicy string, requireUnchanged bool,
) (standardcode.ExecutionContext, sandbox.Manifest, error) {
	runID, expectedCheckpoint = strings.TrimSpace(runID), strings.TrimSpace(expectedCheckpoint)
	if !domain.ValidAgentID(runID) || expectedGeneration < 1 ||
		!domain.ValidAgentID(expectedCheckpoint) {
		return standardcode.ExecutionContext{}, sandbox.Manifest{}, apperror.New(
			apperror.CodeInvalidArgument, "Standard Code execution binding is invalid")
	}
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return standardcode.ExecutionContext{}, sandbox.Manifest{}, apperror.Normalize(err)
	}
	if run.Terminal() {
		return standardcode.ExecutionContext{}, sandbox.Manifest{}, apperror.New(
			apperror.CodeFailedPrecondition, "terminal Run cannot start Standard Code")
	}
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return standardcode.ExecutionContext{}, sandbox.Manifest{}, apperror.Normalize(err)
	}
	workspace, found, err := readRunFileDrydock(ctx, s.store, run.ID)
	if err != nil || !found {
		if err == nil {
			err = apperror.New(apperror.CodeNotFound,
				"Standard Code Drydock was not found")
		}
		return standardcode.ExecutionContext{}, sandbox.Manifest{}, apperror.Normalize(err)
	}
	profile, err := s.store.GetRunExecutionProfile(ctx, run.ID)
	if err != nil {
		return standardcode.ExecutionContext{}, sandbox.Manifest{}, apperror.Normalize(err)
	}
	permission, err := s.store.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		return standardcode.ExecutionContext{}, sandbox.Manifest{}, apperror.Normalize(err)
	}
	capabilities, _, err := s.docker.RuntimeCapabilities()
	if err != nil {
		return standardcode.ExecutionContext{}, sandbox.Manifest{}, err
	}
	generation, err := s.docker.StandardCodeCapabilityGeneration()
	if err != nil {
		return standardcode.ExecutionContext{}, sandbox.Manifest{}, err
	}
	if mission.ID != workspace.MissionID || requireCurrentRunFileDrydock(ctx, s.store, run.ID, workspace) != nil ||
		mission.WorkspaceID != workspace.SourceWorkspaceID ||
		workspace.Generation != expectedGeneration ||
		workspace.LastCheckpointID != expectedCheckpoint ||
		profile.Profile != domain.RunExecutionProfileDocker ||
		!permission.Mode.IsApprovalMode() ||
		!s.docker.permissionCapabilities.WorkspaceSandboxEnabled ||
		!s.docker.permissionCapabilities.AllowsSnapshot(permission) ||
		capabilities.Validate() != nil {
		return standardcode.ExecutionContext{}, sandbox.Manifest{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"Standard Code Docker profile, permission, or Drydock binding is not ready")
	}
	scope := standardcode.ExecutionContext{RunID: run.ID, MissionID: mission.ID,
		SessionID: run.SessionID, WorkspaceID: mission.WorkspaceID,
		DrydockID: workspace.ID, DrydockWorkspaceID: workspace.WorkspaceID,
		DrydockGeneration:    workspace.Generation,
		CheckpointID:         workspace.LastCheckpointID,
		DrydockBindingSHA256: workspace.ExpectedBindingFingerprint,
		ProfileSnapshotID:    profile.ID, ProfileRevision: profile.Revision,
		PermissionSnapshotID: permission.ID, PermissionRevision: permission.Revision,
		CapabilityGeneration: generation}
	manifest, err := standardcode.CompileDockerManifestWithStdin(scope, command,
		stdinPolicy)
	if err != nil {
		return standardcode.ExecutionContext{}, sandbox.Manifest{}, apperror.Wrap(
			apperror.CodeInvalidArgument, "Standard Code command is invalid", err)
	}
	binding, _ := sandbox.ParseDockerStandardCodeManifest(manifest)
	if _, err := s.drydocks.ResolveDrydockExecutionBinding(ctx, binding,
		requireUnchanged); err != nil {
		return standardcode.ExecutionContext{}, sandbox.Manifest{}, err
	}
	return scope, manifest, nil
}

func (s *StandardCodeDockerService) monitorCurrentAuthority(ctx context.Context,
	done <-chan struct{}, scope standardcode.ExecutionContext,
	cancel context.CancelFunc,
) {
	ticker := time.NewTicker(s.authorityPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			if !s.currentAuthorityMetadata(ctx, scope) {
				cancel()
				return
			}
		}
	}
}

func (s *StandardCodeDockerService) currentAuthorityMetadata(ctx context.Context,
	scope standardcode.ExecutionContext,
) bool {
	run, err := s.store.GetRun(ctx, scope.RunID)
	if err != nil || run.Terminal() || run.MissionID != scope.MissionID ||
		run.SessionID != scope.SessionID {
		return false
	}
	profile, err := s.store.GetRunExecutionProfile(ctx, scope.RunID)
	if err != nil || profile.ID != scope.ProfileSnapshotID ||
		profile.Revision != scope.ProfileRevision ||
		profile.Profile != domain.RunExecutionProfileDocker {
		return false
	}
	permission, err := s.store.GetRunExecutionPermission(ctx, scope.RunID)
	if err != nil || permission.ID != scope.PermissionSnapshotID ||
		permission.Revision != scope.PermissionRevision ||
		!permission.Mode.IsApprovalMode() ||
		!s.docker.permissionCapabilities.AllowsSnapshot(permission) {
		return false
	}
	// The Run lease fences admission and every native create/start, not the
	// lifetime of an already running container. Command Runtime retains its
	// exact process owner across turns; its cancellation still reaches Start.
	workspace, found, err := readRunFileDrydock(ctx, s.store, scope.RunID)
	return err == nil && found && requireCurrentRunFileDrydock(ctx, s.store, scope.RunID, workspace) == nil && workspace.ID == scope.DrydockID &&
		workspace.Generation == scope.DrydockGeneration &&
		workspace.LastCheckpointID == scope.CheckpointID &&
		workspace.State != runworktree.StateCleaned &&
		workspace.State != runworktree.StateRecoveryRequired
}

func executionCandidateMatchesRunLease(candidate sandbox.ExecutionCandidate,
	lease domain.RunExecutionLease,
) bool {
	return !candidate.LeaseQuiescent && lease.Validate() == nil &&
		candidate.RunID == lease.RunID && candidate.RunLeaseID == lease.LeaseID &&
		candidate.RunLeaseGeneration == lease.Generation &&
		candidate.RunLeaseOwnerID == lease.OwnerID
}

func (s *StandardCodeDockerService) finalize(ctx context.Context,
	record domain.DockerSandboxRecord, scope standardcode.ExecutionContext,
	requestedBy string,
) (standardcode.Result, error) {
	if isNativeCommandDockerRequester(record.Admission.RequestedBy) || isNativeCommandDockerRequester(requestedBy) {
		return standardcode.Result{}, apperror.New(apperror.CodePolicyDenied, "native commands cannot checkpoint the Run Drydock")
	}
	if record.Launch == nil || record.Receipt == nil ||
		record.Receipt.Validate() != nil || record.Receipt.RunID != scope.RunID {
		return standardcode.Result{}, errors.New(
			"Standard Code Docker terminal receipt is unavailable")
	}
	checkpoint, err := s.drydocks.Checkpoint(ctx, DrydockCheckpointRequest{
		RunID: scope.RunID, ExpectedGeneration: scope.DrydockGeneration,
		OperationKey: standardCodeStageKey(record.Admission.ID, "checkpoint"),
		RequestedBy:  requestedBy, Title: "Standard Code Docker result",
		ConfirmObservedChanges: true,
	})
	if err != nil {
		return standardcode.Result{}, err
	}
	value := standardcode.Result{ProtocolVersion: standardcode.ResultProtocolVersion,
		Backend:     standardcode.BackendDocker,
		ExecutionID: record.Receipt.LifecycleIntentID, RunID: scope.RunID,
		DrydockID: scope.DrydockID, Status: standardCodeStatus(record.Receipt.Outcome),
		ExitCode: record.Receipt.ExitCode, Network: standardcode.NetworkDisabled,
		Credentials: standardcode.CredentialsNone, StartedAt: record.Launch.CreatedAt,
		CompletedAt: record.Receipt.CompletedAt,
		Checkpoint: standardcode.CheckpointResult{DrydockID: scope.DrydockID,
			GenerationBefore: scope.DrydockGeneration,
			GenerationAfter:  checkpoint.Workspace.Generation,
			BeforeID:         scope.CheckpointID, AfterID: checkpoint.Checkpoint.ID,
			ReceiptID: checkpoint.Receipt.ID},
		Artifacts: []standardcode.ArtifactResult{},
		Replayed:  record.Replayed || checkpoint.Replayed}
	if record.Receipt.LogReceiptID != "" {
		logs, found, logErr := s.store.GetDockerLogCaptureReceiptByAttempt(ctx,
			record.Receipt.AttemptID)
		if logErr != nil || !found || logs.Validate() != nil ||
			logs.ID != record.Receipt.LogReceiptID {
			if logErr == nil {
				logErr = errors.New("Standard Code Docker log receipt is unavailable")
			}
			return standardcode.Result{}, logErr
		}
		value.Artifacts = append(value.Artifacts, standardcode.ArtifactResult{
			ID: logs.ID, Kind: "logs", SHA256: logs.ReceiptFingerprint,
			SizeBytes: logs.TotalBytes, FileCount: logs.StreamCount,
			Redacted: logs.RedactedSegments > 0})
	}
	return value, value.Validate()
}

func standardCodeStatus(outcome string) string {
	switch outcome {
	case domain.DockerSandboxOutcomeSucceeded:
		return standardcode.StatusSucceeded
	case domain.DockerSandboxOutcomeTimedOut:
		return standardcode.StatusTimedOut
	case domain.DockerSandboxOutcomeCancelled:
		return standardcode.StatusCancelled
	default:
		return standardcode.StatusFailed
	}
}

func standardCodeStageKey(base, stage string) string {
	digest := runmutation.Fingerprint("standard_code_docker_operation.v1", base, stage)
	return "standard-code-" + stage + "-" + digest[:24]
}
