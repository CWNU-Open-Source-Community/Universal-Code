package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/runworktree"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

type CommandRuntimeStore interface {
	runner.CommandRuntimeStore
	GetRun(context.Context, string) (domain.Run, error)
	GetMission(context.Context, string) (domain.Mission, error)
	GetWorkspaceByID(context.Context, string) (session.WorkspaceRecord, error)
	GetRootAgent(context.Context, string) (domain.AgentNode, bool, error)
	GetRunMode(context.Context, string) (domain.RunModeSnapshot, error)
	GetRunExecutionProfile(context.Context, string) (
		domain.RunExecutionProfileSnapshot, error)
	GetRunExecutionPermission(context.Context, string) (
		domain.RunExecutionPermissionSnapshot, error)
	GetRunExecutionLease(context.Context, string) (
		domain.RunExecutionLease, bool, error)
	GetDrydockByRun(context.Context, string) (runworktree.Workspace, bool, error)
}

type CommandRuntimeService struct {
	store               CommandRuntimeStore
	manager             *runner.CommandRuntimeManager
	adapter             commandruntimeadapter.Identity
	capabilities        domain.ExecutionPermissionRuntimeCapabilities
	checkpoints         *WorkspaceCheckpointService
	drydocks            *RunWorktreeService
	sandbox             runner.CommandRuntimeSandboxExecutor
	checker             policy.Checker
	policyMu            sync.RWMutex
	batchValidationJobs sync.Map
}

type commandRuntimeSandboxReadiness interface {
	Ready(context.Context, string) (bool, error)
}

type commandRuntimeSandboxCheckpointOwner interface {
	OwnsWorkspaceCheckpoint() bool
}

type commandRuntimeBindings struct {
	run        domain.Run
	mission    domain.Mission
	workspace  session.WorkspaceRecord
	root       domain.AgentNode
	mode       domain.RunModeSnapshot
	profile    domain.RunExecutionProfileSnapshot
	permission domain.RunExecutionPermissionSnapshot
	lease      domain.RunExecutionLease
	drydock    runworktree.Workspace
	rootPath   string
	rootSHA256 string
	rootFound  bool
	leaseFound bool
}

func NewCommandRuntimeService(store CommandRuntimeStore,
	manager *runner.CommandRuntimeManager,
	capabilities domain.ExecutionPermissionRuntimeCapabilities,
) (*CommandRuntimeService, error) {
	_, fixed := manager.FixedCommandPlan()
	if store == nil || manager == nil || !manager.Available() ||
		capabilities.Validate() != nil ||
		(!capabilities.DangerFullAccessEnabled && !fixed) {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"command runtime requires the danger-full-access startup gate")
	}
	adapter, installed := manager.AdapterIdentity()
	if !installed || adapter.Kind != commandruntimeadapter.KindHostUnsandboxed ||
		!adapter.AllowsPermission(domain.RunExecutionPermissionFull) {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"command runtime host adapter identity is invalid")
	}
	return &CommandRuntimeService{store: store, manager: manager, adapter: adapter,
		capabilities: capabilities, checker: policy.NewDefaultChecker(),
		checkpoints: embeddedWorkspaceCheckpointService(store, capabilities)}, nil
}

// NewSandboxedCommandRuntimeService binds the shared Job protocol to one
// already-isolated Local or Docker backend. Drydock owns the workspace and the
// checkpoint projection; the source Workspace is never used as the executable
// root for this adapter.
func NewSandboxedCommandRuntimeService(store CommandRuntimeStore,
	manager *runner.CommandRuntimeManager,
	sandboxExecutor runner.CommandRuntimeSandboxExecutor,
	capabilities domain.ExecutionPermissionRuntimeCapabilities,
	drydocks *RunWorktreeService,
) (*CommandRuntimeService, error) {
	if store == nil || manager == nil || !manager.Available() || drydocks == nil ||
		sandboxExecutor == nil || !sandboxExecutor.Available() ||
		capabilities.Validate() != nil ||
		!capabilities.WorkspaceSandboxEnabled {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"sandboxed command runtime requires a proven Workspace Sandbox and Drydock")
	}
	adapter, installed := manager.AdapterIdentity()
	if !installed || adapter.Kind != commandruntimeadapter.KindSandboxedWorkspace ||
		!adapter.SameBackend(sandboxExecutor.Identity()) ||
		!adapter.AllowsPermission(domain.RunExecutionPermissionAsk) ||
		commandRuntimeExecutionProfile(adapter) == "" {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"sandboxed command runtime adapter identity is invalid")
	}
	checkpoints := embeddedWorkspaceCheckpointService(store, capabilities)
	if checkpoints == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"sandboxed command runtime requires Workspace Checkpoint storage")
	}
	drydocks.WithCheckpointService(checkpoints)
	if drydocks.checkpoints == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"sandboxed command runtime Drydock checkpoint projection is unavailable")
	}
	checkpointService := drydocks.checkpoints
	if owner, ok := sandboxExecutor.(commandRuntimeSandboxCheckpointOwner); ok &&
		owner.OwnsWorkspaceCheckpoint() {
		checkpointService = nil
	}
	return &CommandRuntimeService{store: store, manager: manager, adapter: adapter,
		capabilities: capabilities, checker: policy.NewDefaultChecker(), checkpoints: checkpointService,
		drydocks: drydocks, sandbox: sandboxExecutor}, nil
}

func (s *CommandRuntimeService) InstalledCommandRuntimeAdapter() (
	commandruntimeadapter.Identity, bool,
) {
	if s == nil {
		return commandruntimeadapter.Identity{}, false
	}
	if !commandRuntimeStartupAvailable(s.adapter, s.capabilities) {
		return commandruntimeadapter.Identity{}, false
	}
	adapter := s.adapter
	if adapter.Kind == commandruntimeadapter.KindSandboxedWorkspace &&
		(s.sandbox == nil || !s.sandbox.Available() ||
			!adapter.SameBackend(s.sandbox.Identity())) {
		return commandruntimeadapter.Identity{}, false
	}
	if adapter.Validate() != nil || !adapter.Executable() ||
		!adapter.SameBackend(s.adapter) {
		return commandruntimeadapter.Identity{}, false
	}
	return adapter, true
}

func (s *CommandRuntimeService) AdvertisedCommandRuntimeAdapter(ctx context.Context,
	runID string, permission domain.RunExecutionPermissionMode,
) (commandruntimeadapter.Identity, bool, error) {
	if s != nil && s.adapter.BackendIdentity == runner.RestrictedFixedCommandBackend {
		return commandruntimeadapter.Identity{}, false, nil
	}
	if s == nil || !domain.ValidAgentID(runID) || !s.adapter.Executable() ||
		!s.adapter.AllowsPermission(permission) ||
		!s.capabilities.Allows(permission) {
		return commandruntimeadapter.Identity{}, false, nil
	}
	if ctx == nil {
		return commandruntimeadapter.Identity{}, false, apperror.New(
			apperror.CodeInvalidArgument, "command runtime advertisement context is invalid")
	}
	if ctx.Err() != nil {
		return commandruntimeadapter.Identity{}, false, ctx.Err()
	}
	if permission.IsApprovalMode() && s.adapter.Kind == commandruntimeadapter.KindHostUnsandboxed {
		// An owned execution workspace selects its sandbox independently of the
		// approval preference. Missing sandbox readiness never falls back to host.
		_, owned, err := readRunFileDrydock(ctx, s.store, runID)
		if err != nil || owned {
			return commandruntimeadapter.Identity{}, false, err
		}
	}
	runRecord, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return commandruntimeadapter.Identity{}, false, apperror.Normalize(err)
	}
	mode, err := s.store.GetRunMode(ctx, runID)
	if err != nil {
		return commandruntimeadapter.Identity{}, false, apperror.Normalize(err)
	}
	profile, err := s.store.GetRunExecutionProfile(ctx, runID)
	if err != nil {
		return commandruntimeadapter.Identity{}, false, apperror.Normalize(err)
	}
	currentPermission, err := s.store.GetRunExecutionPermission(ctx, runID)
	if err != nil {
		return commandruntimeadapter.Identity{}, false, apperror.Normalize(err)
	}
	lease, leaseFound, err := s.store.GetRunExecutionLease(ctx, runID)
	if err != nil {
		return commandruntimeadapter.Identity{}, false, apperror.Normalize(err)
	}
	if runRecord.ID != runID || runRecord.Status != domain.RunRunning ||
		runRecord.Terminal() || mode.RunID != runID ||
		mode.MissionID != runRecord.MissionID ||
		mode.Surface != domain.ExecutionSurfaceCode ||
		mode.Phase != domain.ExecutionPhaseDeliver ||
		profile.RunID != runID || profile.MissionID != runRecord.MissionID ||
		profile.Profile != commandRuntimeExecutionProfile(s.adapter) ||
		currentPermission.RunID != runID ||
		currentPermission.MissionID != runRecord.MissionID ||
		currentPermission.Mode != permission ||
		!s.capabilities.AllowsSnapshot(currentPermission) || !leaseFound ||
		lease.RunID != runID || !lease.ActiveAt(time.Now().UTC()) {
		return commandruntimeadapter.Identity{}, false, nil
	}
	if s.adapter.Kind == commandruntimeadapter.KindSandboxedWorkspace {
		if s.sandbox == nil || !s.sandbox.Available() ||
			!s.adapter.SameBackend(s.sandbox.Identity()) {
			return commandruntimeadapter.Identity{}, false, nil
		}
		if readiness, ok := s.sandbox.(commandRuntimeSandboxReadiness); ok {
			ready, err := readiness.Ready(ctx, runID)
			if err != nil {
				return commandruntimeadapter.Identity{}, false, apperror.Normalize(err)
			}
			if !ready {
				return commandruntimeadapter.Identity{}, false, nil
			}
		}
		workspace, found, err := readRunFileDrydock(ctx, s.store, runID)
		if err != nil {
			return commandruntimeadapter.Identity{}, false, apperror.Normalize(err)
		}
		if !found || (workspace.State != runworktree.StateReady &&
			workspace.State != runworktree.StateDelivered) {
			return commandruntimeadapter.Identity{}, false, nil
		}
	}
	return s.adapter, true, nil
}

func (s *CommandRuntimeService) ExecuteCommandRuntime(ctx context.Context,
	scope toolgateway.CommandRuntimeContext, input toolgateway.CommandRuntimeInput,
) (toolgateway.CommandRuntimeExecutionResult, error) {
	if s == nil || s.store == nil || s.manager == nil || ctx == nil ||
		ctx.Err() != nil || scope.Validate() != nil || input.Validate() != nil ||
		scope.Adapter.Validate() != nil {
		return toolgateway.CommandRuntimeExecutionResult{}, apperror.New(
			apperror.CodeInvalidArgument, "command runtime request is invalid")
	}
	if !scope.Adapter.SameBackend(s.adapter) ||
		scope.CapabilityGeneration != s.adapter.Generation ||
		!s.commandRuntimeAdapterCurrent() {
		return toolgateway.CommandRuntimeExecutionResult{}, apperror.New(
			apperror.CodeConflict, "command runtime adapter authority is stale")
	}
	networkRequested := false
	for _, command := range input.Commands {
		if command.Network == runner.CommandRuntimeNetworkHost {
			if s.adapter.Kind != commandruntimeadapter.KindHostUnsandboxed ||
				!s.adapter.AllowsPermission(scope.PermissionMode) {
				return toolgateway.CommandRuntimeExecutionResult{}, apperror.New(
					apperror.CodePolicyDenied,
					"host network requires an installed host command adapter and operation authorization")
			}
			networkRequested = true
		}
	}
	bindings, err := s.loadAuthorizedBindings(ctx, scope, networkRequested)
	if err != nil {
		return toolgateway.CommandRuntimeExecutionResult{}, err
	}
	if err := s.checkPreparedCommandCall(ctx, scope, bindings, input); err != nil {
		return toolgateway.CommandRuntimeExecutionResult{}, err
	}
	adapter := s.adapter
	result := toolgateway.CommandRuntimeExecutionResult{
		Backend: adapter.Backend, Adapter: adapter, Action: input.Action,
		Jobs:              []runner.CommandRuntimeJobSnapshot{},
		Pages:             []runner.CommandRuntimeOutputPage{},
		Artifacts:         []toolgateway.CommandRuntimeArtifactOutput{},
		IncompleteReasons: commandRuntimeIncompleteReasons(adapter, networkRequested),
	}
	switch input.Action {
	case toolgateway.CommandRuntimeActionRun:
		boundaryRequest := s.commandRuntimeBoundaryRequest(scope,
			commandRuntimeWorkspaceBoundaryKey("foreground", scope.OperationKey),
			scope.InvocationID)
		if s.checkpoints != nil {
			if _, err := s.checkpoints.BeginBoundary(ctx, boundaryRequest); err != nil {
				return result, err
			}
		}
		value, runErr := s.runForeground(ctx, scope, input, bindings, result)
		boundaryCause := runErr
		if boundaryCause == nil {
			boundaryCause = commandRuntimeMutationResultError(value)
		}
		boundaryErr := s.completeCommandRuntimeBoundary(ctx, boundaryRequest,
			boundaryCause)
		return value, errors.Join(runErr, boundaryErr)
	case toolgateway.CommandRuntimeActionStart:
		if err := s.completeTerminalCommandRuntimeBoundaries(ctx, scope.RunID); err != nil {
			return result, err
		}
		resolved, err := s.normalizeCommandRuntimeSpec(input.Commands[0],
			bindings.rootPath)
		if err != nil {
			return result, commandRuntimeError(err)
		}
		resolved, err = s.bindOriginalAttachmentInputs(ctx, bindings, resolved)
		if err != nil {
			return result, err
		}
		operationDigest, jobID := runner.CommandRuntimeOperationIdentity(scope.RunID,
			scope.OperationKey)
		boundaryRequest := s.commandRuntimeBoundaryRequest(scope, operationDigest, jobID)
		if s.checkpoints != nil {
			if _, err := s.checkpoints.BeginBoundary(ctx, boundaryRequest); err != nil {
				return result, err
			}
		}
		start, err := s.authorizedCommandStart(scope, bindings, scope.OperationKey, resolved)
		if err != nil {
			return result, errors.Join(err, s.completeCommandRuntimeBoundary(ctx, boundaryRequest, err))
		}
		job, replayed, err := s.manager.Start(ctx, start)
		if err != nil {
			operationErr := commandRuntimeError(err)
			return result, errors.Join(operationErr,
				s.completeCommandRuntimeBoundary(ctx, boundaryRequest, operationErr))
		}
		result.Jobs = append(result.Jobs, job)
		result.Replayed = replayed
		// Foreground operators finish Go-owned preparation hooks before sealing
		// the after checkpoint, even when the native Job exits during Start.
		if job.State.Terminal() && !s.operatorCompletesCommandBoundary(ctx, scope) {
			if boundaryErr := s.completeCommandRuntimeBoundary(ctx, boundaryRequest,
				commandRuntimeJobStateError(job.State)); boundaryErr != nil {
				return result, boundaryErr
			}
		}
		return result, nil
	case toolgateway.CommandRuntimeActionList:
		jobs, err := s.manager.List(ctx, runner.CommandRuntimeListFilter{
			RunID: scope.RunID, Limit: toolgateway.MaxCommandRuntimeResultJobs})
		result.Jobs = jobs
		return result, commandRuntimeError(err)
	case toolgateway.CommandRuntimeActionRead, toolgateway.CommandRuntimeActionWait:
		record, err := s.authorizeReadableJob(ctx, input.JobID, bindings)
		if err != nil {
			return result, err
		}
		job, page, err := s.manager.Wait(ctx, input.JobID,
			time.Duration(*input.WaitMilliseconds)*time.Millisecond,
			*input.Cursor, *input.MaxBytes)
		if err != nil {
			return result, commandRuntimeError(err)
		}
		result.Jobs = append(result.Jobs, job)
		result.Pages = append(result.Pages, page)
		if job.State.Terminal() {
			record, err = s.store.GetCommandRuntimeJob(ctx, input.JobID)
			if err != nil {
				return result, commandRuntimeError(err)
			}
			appendCommandRuntimeArtifact(&result, record)
			if err := s.completeCommandRuntimeJobBoundary(ctx, record); err != nil {
				return result, err
			}
		}
		return result, nil
	case toolgateway.CommandRuntimeActionWriteStdin:
		if _, err := s.authorizeActiveJob(ctx, input.JobID, bindings); err != nil {
			return result, err
		}
		guard, err := s.commandStdinDispatchCheck(scope, bindings, input)
		if err != nil {
			return result, err
		}
		job, _, replayed, err := s.manager.WriteStdinGuarded(ctx, input.JobID,
			scope.OperationKey, []byte(*input.Stdin), *input.CloseStdin, guard)
		if err != nil {
			return result, commandRuntimeError(err)
		}
		result.Jobs = append(result.Jobs, job)
		result.Replayed = replayed
		if job.State.Terminal() {
			record, getErr := s.store.GetCommandRuntimeJob(ctx, input.JobID)
			if getErr != nil {
				return result, commandRuntimeError(getErr)
			}
			if err := s.completeCommandRuntimeJobBoundary(ctx, record); err != nil {
				return result, err
			}
		}
		return result, nil
	case toolgateway.CommandRuntimeActionCancel, toolgateway.CommandRuntimeActionKill:
		record, err := s.authorizeJob(ctx, input.JobID, bindings)
		if err != nil {
			return result, err
		}
		if record.State.Terminal() {
			result.Jobs = append(result.Jobs, runner.ProjectCommandRuntimeJob(record))
			result.Replayed = true
			appendCommandRuntimeArtifact(&result, record)
			if err := s.completeCommandRuntimeJobBoundary(ctx, record); err != nil {
				return result, err
			}
			return result, nil
		}
		if _, err := s.authorizeActiveJob(ctx, input.JobID, bindings); err != nil {
			return result, err
		}
		kill := input.Action == toolgateway.CommandRuntimeActionKill
		grace := time.Duration(0)
		if input.WaitMilliseconds != nil {
			grace = time.Duration(*input.WaitMilliseconds) * time.Millisecond
		}
		job, err := s.manager.Stop(ctx, input.JobID, kill, grace)
		if err != nil {
			return result, commandRuntimeError(err)
		}
		result.Jobs = append(result.Jobs, job)
		if job.State.Terminal() {
			record, getErr := s.store.GetCommandRuntimeJob(ctx, input.JobID)
			if getErr != nil {
				return result, commandRuntimeError(getErr)
			}
			if err := s.completeCommandRuntimeJobBoundary(ctx, record); err != nil {
				return result, err
			}
		}
		return result, nil
	default:
		return result, apperror.New(apperror.CodeInvalidArgument,
			"command runtime action is unsupported")
	}
}

// cleanupUIEvidenceJob is a cleanup-only capability for a Job that this
// process started for one sealed UI-evidence Attempt. It intentionally does
// not consult the current Run lease: expiry, cancellation, and revocation are
// precisely the states in which the Attempt must still be able to reap its own
// process tree. The full durable identity is checked before Stop, so this path
// cannot start, adopt, read, write to, or stop any other Job.
func (s *CommandRuntimeService) cleanupUIEvidenceJob(ctx context.Context,
	binding uiEvidenceCommandCleanupBinding,
) (runner.CommandRuntimeJobSnapshot, error) {
	if s == nil || s.store == nil || s.manager == nil || ctx == nil ||
		ctx.Err() != nil || binding.Validate() != nil {
		return runner.CommandRuntimeJobSnapshot{}, apperror.New(
			apperror.CodeInvalidArgument, "UI evidence command cleanup binding is invalid")
	}
	record, err := s.store.GetCommandRuntimeJob(ctx, binding.JobID)
	if err != nil {
		return runner.CommandRuntimeJobSnapshot{}, commandRuntimeError(err)
	}
	if !uiEvidenceCommandCleanupMatches(record, binding) {
		return runner.CommandRuntimeJobSnapshot{}, apperror.New(
			apperror.CodeConflict, "UI evidence command cleanup binding is stale")
	}
	if record.State.Terminal() {
		if !record.TreeReaped {
			return runner.ProjectCommandRuntimeJob(record), apperror.New(
				apperror.CodeConflict, "UI evidence command process tree is not reaped")
		}
		if err := s.completeCommandRuntimeJobBoundary(ctx, record); err != nil {
			return runner.ProjectCommandRuntimeJob(record), err
		}
		return runner.ProjectCommandRuntimeJob(record), nil
	}
	if !s.manager.OwnsActiveJob(record) {
		return runner.ProjectCommandRuntimeJob(record), apperror.New(
			apperror.CodeConflict, "UI evidence command ownership is stale")
	}
	_, stopErr := s.manager.Stop(ctx, record.ID, true, 0)
	for {
		job, _, waitErr := s.manager.Wait(ctx, record.ID, 100*time.Millisecond,
			math.MaxUint64, runner.MinCommandRuntimeOutputRead)
		if waitErr != nil {
			return job, errors.Join(commandRuntimeError(stopErr),
				commandRuntimeError(waitErr))
		}
		if !job.State.Terminal() {
			continue
		}
		if !job.TreeReaped {
			return job, apperror.New(apperror.CodeConflict,
				"UI evidence command process tree is not reaped")
		}
		record, err = s.store.GetCommandRuntimeJob(ctx, binding.JobID)
		if err != nil {
			return job, commandRuntimeError(err)
		}
		if !uiEvidenceCommandCleanupMatches(record, binding) ||
			!record.State.Terminal() || !record.TreeReaped {
			return job, apperror.New(apperror.CodeConflict,
				"UI evidence command cleanup proof is stale")
		}
		if err := s.completeCommandRuntimeJobBoundary(ctx, record); err != nil {
			return runner.ProjectCommandRuntimeJob(record), err
		}
		return runner.ProjectCommandRuntimeJob(record), nil
	}
}

func uiEvidenceCommandCleanupMatches(job runner.CommandRuntimeJob,
	binding uiEvidenceCommandCleanupBinding,
) bool {
	expectedDigest, expectedID := runner.CommandRuntimeOperationIdentity(
		binding.RunID, binding.OperationKey)
	return binding.JobID == expectedID && job.ID == expectedID &&
		job.OperationDigest == expectedDigest &&
		job.InvocationID == binding.InvocationID && job.RunID == binding.RunID &&
		job.MissionID == binding.MissionID && job.SessionID == binding.SessionID &&
		job.WorkspaceID == binding.WorkspaceID && job.RootAgentID == binding.RootAgentID &&
		job.LeaseID == binding.LeaseID && job.LeaseGeneration == binding.LeaseGeneration
}

func (s *CommandRuntimeService) runForeground(ctx context.Context,
	scope toolgateway.CommandRuntimeContext, input toolgateway.CommandRuntimeInput,
	bindings commandRuntimeBindings, result toolgateway.CommandRuntimeExecutionResult,
) (toolgateway.CommandRuntimeExecutionResult, error) {
	resolvedCommands := make([]runner.CommandRuntimeResolvedSpec, len(input.Commands))
	for index, command := range input.Commands {
		resolved, err := s.normalizeCommandRuntimeSpec(command,
			bindings.rootPath)
		if err != nil {
			return result, commandRuntimeError(err)
		}
		resolved, err = s.bindOriginalAttachmentInputs(ctx, bindings, resolved)
		if err != nil {
			return result, err
		}
		resolvedCommands[index] = resolved
	}
	for index, resolved := range resolvedCommands {
		operationKey := commandRuntimeBatchOperationKey(scope.OperationKey, index)
		start, err := s.authorizedCommandStart(scope, bindings, operationKey, resolved)
		if err != nil {
			return result, err
		}
		job, replayed, err := s.manager.Start(ctx, start)
		if err != nil {
			return result, commandRuntimeError(err)
		}
		result.Replayed = result.Replayed || replayed
		jobID := job.ID
		job, err = s.waitForTerminal(ctx, jobID)
		if err != nil {
			return result, errors.Join(err, commandRuntimeError(
				s.cancelForegroundJob(jobID)))
		}
		job, page, err := s.manager.Wait(ctx, job.ID, 0, 0, *input.MaxBytes)
		if err != nil {
			return result, commandRuntimeError(err)
		}
		result.Jobs = append(result.Jobs, job)
		result.Pages = append(result.Pages, page)
		record, err := s.store.GetCommandRuntimeJob(ctx, job.ID)
		if err != nil {
			return result, commandRuntimeError(err)
		}
		appendCommandRuntimeArtifact(&result, record)
		if job.State != runner.CommandRuntimeJobCompleted &&
			input.FailurePolicy == toolgateway.CommandRuntimeFailFast {
			break
		}
	}
	return result, nil
}

func (s *CommandRuntimeService) commandRuntimeBoundaryRequest(
	scope toolgateway.CommandRuntimeContext, operationKey, receiptID string,
) WorkspaceMutationBoundaryRequest {
	return WorkspaceMutationBoundaryRequest{RunID: scope.RunID,
		Kind: workspacecheckpoint.TransactionCommandBatch, OperationKey: operationKey,
		TriggerReceiptID: receiptID, InvocationID: scope.InvocationID,
		CapabilityGeneration: scope.CapabilityGeneration, LeaseID: scope.LeaseID,
		LeaseGeneration: scope.LeaseGeneration,
		IncompleteReasons: []string{
			"filesystem watcher attribution is unavailable; shell writes are inferred from bounded manifests and Git state",
		}}
}

func (s *CommandRuntimeService) completeCommandRuntimeBoundary(ctx context.Context,
	request WorkspaceMutationBoundaryRequest, cause error,
) error {
	if s == nil || s.checkpoints == nil {
		return nil
	}
	completionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx),
		30*time.Second)
	defer cancel()
	_, err := s.checkpoints.CompleteBoundary(completionCtx, request, cause)
	return err
}

func (s *CommandRuntimeService) completeCommandRuntimeJobBoundary(ctx context.Context,
	job runner.CommandRuntimeJob,
) error {
	if s == nil || s.checkpoints == nil || !job.State.Terminal() {
		return nil
	}
	operationDigest := workspaceBoundaryOperationDigest(job.RunID,
		workspacecheckpoint.TransactionCommandBatch, job.OperationDigest)
	if _, found, err := s.checkpoints.store.GetWorkspaceCheckpointTransactionByOperation(ctx,
		operationDigest); err != nil {
		return apperror.Normalize(err)
	} else if !found {
		// Foreground batch Jobs are covered by one batch-level boundary. Only
		// background Jobs have a per-Job boundary keyed by OperationDigest.
		return nil
	}
	return s.completeCommandRuntimeBoundary(ctx, WorkspaceMutationBoundaryRequest{
		RunID: job.RunID, Kind: workspacecheckpoint.TransactionCommandBatch,
		OperationKey: job.OperationDigest, TriggerReceiptID: job.ID,
		InvocationID: job.InvocationID}, commandRuntimeJobStateError(job.State))
}

func (s *CommandRuntimeService) completeTerminalCommandRuntimeBoundaries(ctx context.Context,
	runID string,
) error {
	if s == nil || s.checkpoints == nil {
		return nil
	}
	jobs, err := s.store.ListCommandRuntimeJobs(ctx,
		runner.CommandRuntimeListFilter{RunID: runID, Limit: 500})
	if err != nil {
		return commandRuntimeError(err)
	}
	for _, job := range jobs {
		if !job.Adapter.SameBackend(s.adapter) {
			continue
		}
		if job.State.Terminal() {
			if err := s.completeCommandRuntimeJobBoundary(ctx, job); err != nil {
				return err
			}
		}
	}
	return nil
}

func commandRuntimeMutationResultError(
	result toolgateway.CommandRuntimeExecutionResult,
) error {
	for _, job := range result.Jobs {
		if err := commandRuntimeJobStateError(job.State); err != nil {
			return err
		}
	}
	return nil
}

func commandRuntimeJobStateError(state runner.CommandRuntimeJobState) error {
	if state == runner.CommandRuntimeJobCompleted {
		return nil
	}
	return fmt.Errorf("command runtime workspace mutation ended in state %s", state)
}

func (s *CommandRuntimeService) cancelForegroundJob(jobID string) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(),
		runner.MaxCommandRuntimeCancelGrace+2*time.Second)
	defer cancel()
	if _, err := s.manager.Stop(cleanupCtx, jobID, false, 0); err != nil {
		return err
	}
	for {
		job, _, err := s.manager.Wait(cleanupCtx, jobID, runner.MaxCommandRuntimeWait,
			math.MaxUint64, runner.MinCommandRuntimeOutputRead)
		if err != nil {
			return err
		}
		if job.State.Terminal() {
			return nil
		}
	}
}

func (s *CommandRuntimeService) waitForTerminal(ctx context.Context,
	jobID string,
) (runner.CommandRuntimeJobSnapshot, error) {
	for {
		job, _, err := s.manager.Wait(ctx, jobID, runner.MaxCommandRuntimeWait,
			math.MaxUint64, runner.MinCommandRuntimeOutputRead)
		if err != nil {
			return runner.CommandRuntimeJobSnapshot{}, commandRuntimeError(err)
		}
		if job.State.Terminal() {
			return job, nil
		}
	}
}

func (s *CommandRuntimeService) loadAuthorizedBindings(ctx context.Context,
	scope toolgateway.CommandRuntimeContext, networkRequested bool,
) (commandRuntimeBindings, error) {
	value, err := s.loadCommandRuntimeBindings(ctx, scope.RunID)
	if err != nil {
		return value, err
	}
	return s.validateAuthorizedBindings(ctx, scope, networkRequested, value)
}

func (s *CommandRuntimeService) loadCommandRuntimeBindings(ctx context.Context,
	runID string,
) (commandRuntimeBindings, error) {
	var value commandRuntimeBindings
	expectedProfile := commandRuntimeExecutionProfile(s.adapter)
	if err := s.capabilities.Validate(); err != nil || !s.adapter.Executable() ||
		expectedProfile == "" ||
		!commandRuntimeStartupAvailable(s.adapter, s.capabilities) {
		return value, apperror.New(apperror.CodePolicyDenied,
			"command runtime startup capability is disabled")
	}
	var err error
	if value.run, err = s.store.GetRun(ctx, runID); err != nil {
		return value, apperror.Normalize(err)
	}
	if value.mission, err = s.store.GetMission(ctx, value.run.MissionID); err != nil {
		return value, apperror.Normalize(err)
	}
	if value.workspace, err = s.store.GetWorkspaceByID(ctx,
		value.mission.WorkspaceID); err != nil {
		return value, apperror.Normalize(err)
	}
	if value.root, value.rootFound, err = s.store.GetRootAgent(ctx, value.run.ID); err != nil {
		return value, apperror.Normalize(err)
	}
	if value.mode, err = s.store.GetRunMode(ctx, value.run.ID); err != nil {
		return value, apperror.Normalize(err)
	}
	if value.profile, err = s.store.GetRunExecutionProfile(ctx,
		value.run.ID); err != nil {
		return value, apperror.Normalize(err)
	}
	if value.permission, err = s.store.GetRunExecutionPermission(ctx,
		value.run.ID); err != nil {
		return value, apperror.Normalize(err)
	}
	if value.lease, value.leaseFound, err = s.store.GetRunExecutionLease(ctx,
		value.run.ID); err != nil {
		return value, apperror.Normalize(err)
	}
	return value, nil
}

func (s *CommandRuntimeService) validateAuthorizedBindings(ctx context.Context,
	scope toolgateway.CommandRuntimeContext, networkRequested bool,
	value commandRuntimeBindings,
) (commandRuntimeBindings, error) {
	expectedProfile := commandRuntimeExecutionProfile(s.adapter)
	if fixed, ok := s.manager.FixedCommandPlan(); ok {
		reader, ok := s.store.(interface {
			GetRunExecutionInteraction(context.Context, string) (domain.RunExecutionInteractionSnapshot, error)
		})
		if !ok {
			return value, errors.New("fixed command interaction reader is unavailable")
		}
		interaction, err := reader.GetRunExecutionInteraction(ctx, value.run.ID)
		if err != nil {
			return value, err
		}
		current, err := runner.PlanControlledCommand(runner.ControlledCommandPlanRequest{ID: fixed.ID,
			WorkspaceID: value.workspace.ID, WorkspaceRoot: value.workspace.RootPath,
			Interaction: interaction, CurrentProfile: value.profile, CurrentSurface: value.mode.Surface,
			Kind: fixed.Kind, RelativePath: fixed.RelativePath, Timeout: time.Duration(fixed.TimeoutMilliseconds) * time.Millisecond})
		if err != nil || current.Fingerprint != fixed.Fingerprint || scope.RequestedBy != toolgateway.CommandRuntimeRequestedByOperator {
			return value, apperror.New(apperror.CodeConflict, "fixed command plan or operator binding changed")
		}
	}
	var err error
	value.rootPath = value.workspace.RootPath
	if value.permission.Mode.IsApprovalMode() && s.adapter.Kind == commandruntimeadapter.KindHostUnsandboxed {
		_, owned, err := readRunFileDrydock(ctx, s.store, value.run.ID)
		if err != nil {
			return value, err
		}
		if owned {
			return value, apperror.New(apperror.CodeConflict, "owned command workspace requires its sandbox adapter")
		}
	}
	if s.adapter.Kind == commandruntimeadapter.KindSandboxedWorkspace {
		var drydockFound bool
		if value.drydock, drydockFound, err = readRunFileDrydock(ctx, s.store,
			value.run.ID); err != nil {
			return value, apperror.Normalize(err)
		}
		bound, bindErr := commandRuntimeDrydockBound(ctx, s.store, value.drydock, value.run.ID, value.mission.ID, value.run.SessionID, value.workspace.ID)
		if bindErr != nil {
			return value, apperror.Normalize(bindErr)
		}
		if !drydockFound || !bound {
			return value, apperror.New(apperror.CodeConflict, "command runtime Drydock binding is stale")
		}
		value.rootPath = value.drydock.Path
	}
	if value.rootSHA256, err = runner.CommandRuntimeWorkspaceRootSHA256(
		value.rootPath); err != nil {
		return value, commandRuntimeError(err)
	}
	operatorStopped := false
	if proof, ok := ctx.Value(operatorCommandApprovalKey{}).(operatorCommandApproval); ok {
		if proof.host != s || proof.runStatus != value.run.Status {
			return value, apperror.New(apperror.CodeConflict, "operator command Run state changed")
		}
		operatorStopped = proof.host == s && scope.RequestedBy == toolgateway.CommandRuntimeRequestedByOperator &&
			proof.invocationID == scope.InvocationID && proof.operationKey == scope.OperationKey &&
			operatorCommandOwnsStoppedRun(ctx, value.run, value.lease)
	}
	if !value.leaseFound || !value.rootFound || value.run.Terminal() ||
		(value.run.Status != domain.RunRunning && !operatorStopped) ||
		value.run.ID != scope.RunID || value.run.MissionID != scope.MissionID ||
		value.run.SessionID != scope.SessionID ||
		value.run.MissionID != value.mission.ID ||
		value.mission.WorkspaceID != scope.WorkspaceID ||
		value.workspace.ID != scope.WorkspaceID || value.root.ID != scope.RootAgentID ||
		value.root.ParentID != "" || value.root.Role != domain.AgentRoleRoot ||
		value.mode.RunID != value.run.ID || value.mode.MissionID != value.mission.ID ||
		value.mode.Surface != domain.ExecutionSurfaceCode ||
		value.mode.Phase != domain.ExecutionPhaseDeliver ||
		value.profile.RunID != value.run.ID ||
		value.profile.MissionID != value.mission.ID ||
		value.profile.Profile != expectedProfile ||
		value.profile.NetworkScope != domain.ExecutionNetworkDisabled ||
		value.permission.RunID != value.run.ID ||
		value.permission.MissionID != value.mission.ID ||
		!s.adapter.AllowsPermission(value.permission.Mode) ||
		value.lease.LeaseID != scope.LeaseID ||
		value.lease.Generation != scope.LeaseGeneration ||
		value.lease.Status != domain.RunExecutionLeaseActive ||
		!value.lease.ExpiresAt.After(time.Now().UTC()) {
		return value, apperror.New(apperror.CodeConflict,
			"command runtime durable binding is stale")
	}
	if scope.ModeRevision != 0 && (value.mode.Surface != scope.Surface ||
		value.mode.Phase != scope.Phase || value.mode.Profile != scope.Profile ||
		value.mode.Revision != scope.ModeRevision || value.root.Role != scope.Role ||
		value.permission.Mode != scope.PermissionMode ||
		value.permission.Revision != scope.PermissionRevision) {
		return value, apperror.New(apperror.CodeConflict,
			"command runtime supplied authority snapshot is stale")
	}
	if !commandRuntimeLivePermissionMatches(s.capabilities, value.permission, scope) {
		return value, apperror.New(apperror.CodePolicyDenied,
			"command runtime runtime binding is stale")
	}
	if !s.capabilities.AllowsSnapshot(value.permission) || (networkRequested && s.adapter.Kind != commandruntimeadapter.KindHostUnsandboxed) {
		return value, apperror.New(apperror.CodePolicyDenied, "command runtime is unavailable for the current snapshot")
	}
	return value, nil
}

func commandRuntimeLivePermissionMatches(
	capabilities domain.ExecutionPermissionRuntimeCapabilities,
	permission domain.RunExecutionPermissionSnapshot,
	scope toolgateway.CommandRuntimeContext,
) bool {
	return agentCodeRuntimeCurrent(capabilities, permission, scope.PermissionSnapshotID, scope.PermissionGeneration, scope.PermissionRuntimeEpoch, scope.RunAuthorizationFence)
}

func (s *CommandRuntimeService) runnerScope(scope toolgateway.CommandRuntimeContext,
	bindings commandRuntimeBindings, operationKey string,
) runner.CommandRuntimeScope {
	return runner.CommandRuntimeScope{InvocationID: scope.InvocationID,
		OperationKey: operationKey, RunID: bindings.run.ID,
		MissionID: bindings.mission.ID, RootAgentID: bindings.root.ID,
		AgentID: scope.AgentID, AgentAttemptID: scope.AgentAttemptID,
		AttributionSource: scope.Attribution().Source,
		SessionID:         bindings.run.SessionID, WorkspaceID: bindings.workspace.ID,
		WorkspaceRootSHA256: bindings.rootSHA256,
		ModeSnapshotID:      bindings.mode.ID, ModeRevision: bindings.mode.Revision,
		ProfileSnapshotID:      bindings.profile.ID,
		ProfileRevision:        bindings.profile.Revision,
		PermissionSnapshotID:   bindings.permission.ID,
		PermissionRevision:     bindings.permission.Revision,
		PermissionGeneration:   scope.PermissionGeneration,
		PermissionRuntimeEpoch: scope.PermissionRuntimeEpoch,
		RunAuthorizationFence:  scope.RunAuthorizationFence,
		PermissionMode:         bindings.permission.Mode, LeaseID: bindings.lease.LeaseID,
		LeaseGeneration: bindings.lease.Generation,
		LeaseOwnerID:    bindings.lease.OwnerID, Adapter: s.adapter}
}

func (s *CommandRuntimeService) authorizeJob(ctx context.Context, jobID string,
	bindings commandRuntimeBindings,
) (runner.CommandRuntimeJob, error) {
	job, err := s.store.GetCommandRuntimeJob(ctx, jobID)
	if err != nil {
		return runner.CommandRuntimeJob{}, commandRuntimeError(err)
	}
	if job.RunID != bindings.run.ID || job.MissionID != bindings.mission.ID ||
		job.SessionID != bindings.run.SessionID ||
		job.WorkspaceID != bindings.workspace.ID || job.RootAgentID != bindings.root.ID {
		return runner.CommandRuntimeJob{}, apperror.New(apperror.CodeNotFound,
			"command runtime job was not found in this Run")
	}
	return job, nil
}

func (s *CommandRuntimeService) authorizeActiveJob(ctx context.Context, jobID string,
	bindings commandRuntimeBindings,
) (runner.CommandRuntimeJob, error) {
	job, err := s.authorizeJob(ctx, jobID, bindings)
	if err != nil {
		return runner.CommandRuntimeJob{}, err
	}
	if job.State.Terminal() || job.WorkspaceRootSHA256 != bindings.rootSHA256 ||
		!job.Adapter.SameBackend(s.adapter) ||
		job.ModeSnapshotID != bindings.mode.ID || job.ModeRevision != bindings.mode.Revision ||
		job.ProfileSnapshotID != bindings.profile.ID ||
		job.ProfileRevision != bindings.profile.Revision ||
		job.PermissionSnapshotID != bindings.permission.ID ||
		job.PermissionRevision != bindings.permission.Revision ||
		job.PermissionMode != bindings.permission.Mode ||
		!commandRuntimeJobGrantMatches(s.capabilities, bindings.permission, job) ||
		!s.manager.OwnsActiveJob(job) {
		return runner.CommandRuntimeJob{}, apperror.New(apperror.CodeConflict,
			"command runtime job ownership is stale")
	}
	return job, nil
}

// authorizeReadableJob closes the race between observing an active durable Job
// and verifying its process-local owner. A short-lived command may complete in
// that interval; terminal output remains readable after its Run identity is
// authorized, while a still-active Job must retain exact process ownership.
func (s *CommandRuntimeService) authorizeReadableJob(ctx context.Context, jobID string,
	bindings commandRuntimeBindings,
) (runner.CommandRuntimeJob, error) {
	job, err := s.authorizeJob(ctx, jobID, bindings)
	if err != nil || job.State.Terminal() {
		return job, err
	}
	if job.Adapter.Kind == commandruntimeadapter.KindLegacyUnbound {
		return job, nil
	}
	active, activeErr := s.authorizeActiveJob(ctx, jobID, bindings)
	if activeErr == nil {
		return active, nil
	}
	refreshed, refreshErr := s.authorizeJob(ctx, jobID, bindings)
	if refreshErr == nil && refreshed.State.Terminal() {
		return refreshed, nil
	}
	return runner.CommandRuntimeJob{}, activeErr
}

func (s *CommandRuntimeService) Reconcile(ctx context.Context) (int, error) {
	if s == nil || s.store == nil || s.manager == nil {
		return 0, apperror.New(apperror.CodeFailedPrecondition,
			"command runtime service is unavailable")
	}
	reconciled, err := s.manager.ReconcileStartup(ctx)
	if err != nil {
		return 0, commandRuntimeError(err)
	}
	jobs, err := s.store.ListCommandRuntimeJobs(ctx,
		runner.CommandRuntimeListFilter{Limit: 500})
	if err != nil {
		return 0, commandRuntimeError(err)
	}
	stopped := reconciled
	for _, job := range jobs {
		if !job.Adapter.SameBackend(s.adapter) {
			continue
		}
		if job.State.Terminal() {
			if s.checkpoints != nil {
				lease, found, err := s.store.GetRunExecutionLease(ctx, job.RunID)
				if err != nil {
					return stopped, apperror.Normalize(err)
				}
				if found && lease.ActiveAt(time.Now().UTC()) {
					// Native exit can precede operator finalization. The Run lease
					// still owns those writes and their checkpoint; recovery resumes
					// only after the foreground owner releases or loses the lease.
					continue
				}
			}
			if completeErr := s.completeCommandRuntimeJobBoundary(ctx, job); completeErr != nil {
				return stopped, completeErr
			}
			continue
		}
		if !s.manager.OwnsActiveJob(job) {
			continue
		}
		current, bindErr := s.commandRuntimeJobBindingsCurrent(ctx, job)
		if bindErr != nil {
			return stopped, bindErr
		}
		if !current {
			stoppedJob, stopErr := s.manager.Stop(context.WithoutCancel(ctx), job.ID,
				true, 0)
			if stopErr == nil {
				stopped++
				if stoppedJob.State.Terminal() {
					record, getErr := s.store.GetCommandRuntimeJob(ctx, job.ID)
					if getErr != nil {
						return stopped, commandRuntimeError(getErr)
					}
					if completeErr := s.completeCommandRuntimeJobBoundary(ctx,
						record); completeErr != nil {
						return stopped, completeErr
					}
				}
			}
		}
	}
	return stopped, nil
}

func (s *CommandRuntimeService) commandRuntimeJobBindingsCurrent(ctx context.Context,
	job runner.CommandRuntimeJob,
) (bool, error) {
	expectedProfile := commandRuntimeExecutionProfile(s.adapter)
	if err := s.capabilities.Validate(); err != nil || !s.commandRuntimeAdapterCurrent() ||
		!job.Adapter.SameBackend(s.adapter) ||
		expectedProfile == "" ||
		!commandRuntimeStartupAvailable(s.adapter, s.capabilities) {
		return false, nil
	}
	runRecord, err := s.store.GetRun(ctx, job.RunID)
	if err != nil {
		return false, apperror.Normalize(err)
	}
	mission, err := s.store.GetMission(ctx, runRecord.MissionID)
	if err != nil {
		return false, apperror.Normalize(err)
	}
	workspace, err := s.store.GetWorkspaceByID(ctx, mission.WorkspaceID)
	if err != nil {
		return false, apperror.Normalize(err)
	}
	root, found, err := s.store.GetRootAgent(ctx, runRecord.ID)
	if err != nil {
		return false, apperror.Normalize(err)
	}
	mode, err := s.store.GetRunMode(ctx, runRecord.ID)
	if err != nil {
		return false, apperror.Normalize(err)
	}
	profile, err := s.store.GetRunExecutionProfile(ctx, runRecord.ID)
	if err != nil {
		return false, apperror.Normalize(err)
	}
	permission, err := s.store.GetRunExecutionPermission(ctx, runRecord.ID)
	if err != nil {
		return false, apperror.Normalize(err)
	}
	if !s.capabilities.AllowsSnapshot(permission) {
		return false, nil
	}
	if !commandRuntimeJobGrantMatches(s.capabilities, permission, job) {
		return false, nil
	}
	rootPath := workspace.RootPath
	if s.adapter.Kind == commandruntimeadapter.KindSandboxedWorkspace {
		owned, drydockFound, drydockErr := readRunFileDrydock(ctx, s.store, runRecord.ID)
		if drydockErr != nil {
			return false, apperror.Normalize(drydockErr)
		}
		bound, bindErr := commandRuntimeDrydockBound(ctx, s.store, owned, runRecord.ID, mission.ID, runRecord.SessionID, workspace.ID)
		if bindErr != nil {
			return false, apperror.Normalize(bindErr)
		}
		if !drydockFound || !bound {
			return false, nil
		}
		rootPath = owned.Path
	}
	if native, ok := s.batchValidationJobs.Load(job.ID); ok {
		binding := native.(*batchValidationJobBinding)
		if binding.operationDigest != job.OperationDigest || binding.specFingerprint != job.SpecFingerprint || binding.proof.check(ctx) != nil {
			return false, nil
		}
		rootPath = binding.proof.target.root
	}
	rootSHA256, err := runner.CommandRuntimeWorkspaceRootSHA256(rootPath)
	if err != nil {
		return false, nil
	}
	return found && !runRecord.Terminal() && (runRecord.Status == domain.RunRunning || s.manager.OwnsActiveOperatorJob(job)) &&
		runRecord.MissionID == job.MissionID && runRecord.SessionID == job.SessionID &&
		mission.ID == job.MissionID && mission.WorkspaceID == job.WorkspaceID &&
		workspace.ID == job.WorkspaceID && root.ID == job.RootAgentID &&
		root.ParentID == "" && root.Role == domain.AgentRoleRoot &&
		rootSHA256 == job.WorkspaceRootSHA256 &&
		mode.ID == job.ModeSnapshotID && mode.Revision == job.ModeRevision &&
		mode.Surface == domain.ExecutionSurfaceCode &&
		mode.Phase == domain.ExecutionPhaseDeliver &&
		profile.ID == job.ProfileSnapshotID && profile.Revision == job.ProfileRevision &&
		profile.Profile == expectedProfile &&
		profile.NetworkScope == domain.ExecutionNetworkDisabled &&
		permission.ID == job.PermissionSnapshotID &&
		permission.Revision == job.PermissionRevision &&
		permission.Mode == job.PermissionMode &&
		s.adapter.AllowsPermission(permission.Mode), nil
}

func commandRuntimeJobGrantMatches(
	capabilities domain.ExecutionPermissionRuntimeCapabilities,
	permission domain.RunExecutionPermissionSnapshot,
	job runner.CommandRuntimeJob,
) bool {
	return agentCodeRuntimeCurrent(capabilities, permission, job.PermissionSnapshotID, job.PermissionGeneration, job.PermissionRuntimeEpoch, job.RunAuthorizationFence)
}

func (s *CommandRuntimeService) commandRuntimeAdapterCurrent() bool {
	if s == nil || s.manager == nil || !s.manager.Available() ||
		!s.adapter.Executable() {
		return false
	}
	managerAdapter, installed := s.manager.AdapterIdentity()
	if !installed || !managerAdapter.SameBackend(s.adapter) {
		return false
	}
	if s.adapter.Kind == commandruntimeadapter.KindSandboxedWorkspace {
		return s.sandbox != nil && s.sandbox.Available() &&
			s.sandbox.Identity().SameBackend(s.adapter)
	}
	return s.adapter.Kind == commandruntimeadapter.KindHostUnsandboxed
}

func (s *CommandRuntimeService) Shutdown(ctx context.Context) error {
	if s == nil || s.manager == nil {
		return nil
	}
	return s.manager.Shutdown(ctx)
}

func (s *CommandRuntimeService) RunReconciler(ctx context.Context,
	interval time.Duration,
) error {
	if s == nil || s.manager == nil || interval < 100*time.Millisecond ||
		interval > time.Minute || ctx == nil {
		return apperror.New(apperror.CodeInvalidArgument,
			"command runtime reconciliation interval is invalid")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := s.Reconcile(ctx); err != nil && ctx.Err() == nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(),
				runner.MaxCommandRuntimeCancelGrace+time.Second)
			_ = s.manager.Shutdown(shutdownCtx)
			cancel()
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func appendCommandRuntimeArtifact(result *toolgateway.CommandRuntimeExecutionResult,
	job runner.CommandRuntimeJob,
) {
	if result == nil || !job.State.Terminal() ||
		(job.Stdout == "" && job.Stderr == "") {
		return
	}
	result.Artifacts = append(result.Artifacts,
		toolgateway.CommandRuntimeArtifactOutput{JobID: job.ID,
			Stdout: job.Stdout, Stderr: job.Stderr})
}

func commandRuntimeStartupAvailable(adapter commandruntimeadapter.Identity,
	capabilities domain.ExecutionPermissionRuntimeCapabilities,
) bool {
	if adapter.Validate() != nil || capabilities.Validate() != nil {
		return false
	}
	switch adapter.Kind {
	case commandruntimeadapter.KindHostUnsandboxed:
		// Installation is a process ceiling. Ask/Auto/Full choose operation
		// review policy only after this native capability exists.
		return capabilities.DangerFullAccessEnabled || adapter.BackendIdentity == runner.RestrictedFixedCommandBackend
	case commandruntimeadapter.KindSandboxedWorkspace:
		return capabilities.WorkspaceSandboxEnabled
	default:
		return false
	}
}

func commandRuntimeExecutionProfile(adapter commandruntimeadapter.Identity) domain.RunExecutionProfile {
	switch adapter.Kind {
	case commandruntimeadapter.KindHostUnsandboxed:
		return domain.RunExecutionProfileLocal
	case commandruntimeadapter.KindSandboxedWorkspace:
		switch adapter.Backend {
		case "local_windows_lpac":
			return domain.RunExecutionProfileLocal
		case "docker_standard_code":
			return domain.RunExecutionProfileDocker
		case "docker_sandboxes":
			return domain.RunExecutionProfileSBX
		}
	}
	return ""
}

func commandRuntimeIncompleteReasons(adapter commandruntimeadapter.Identity,
	networkRequested bool,
) []string {
	switch adapter.Kind {
	case commandruntimeadapter.KindHostUnsandboxed:
		reasons := []string{
			"host_unsandboxed cannot prove host credentials are unavailable to the child process",
		}
		if !networkRequested {
			reasons = append(reasons,
				"host_unsandboxed cannot enforce the command network=disabled intent")
		}
		return reasons
	case commandruntimeadapter.KindSandboxedWorkspace:
		return []string{}
	default:
		return []string{"command runtime adapter evidence is incomplete"}
	}
}

func commandRuntimeBatchOperationKey(operationKey string, index int) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("command-runtime-batch.v2:%d:%s",
		index, operationKey)))
	return "command-runtime-" + hex.EncodeToString(digest[:])
}

func commandRuntimeWorkspaceBoundaryKey(action, operationKey string) string {
	digest := sha256.Sum256([]byte("command-runtime-workspace-boundary.v1\x00" +
		action + "\x00" + operationKey))
	return hex.EncodeToString(digest[:])
}

func (s *CommandRuntimeService) normalizeCommandRuntimeSpec(spec runner.CommandRuntimeSpec, root string) (runner.CommandRuntimeResolvedSpec, error) {
	if normalizer, ok := s.sandbox.(interface {
		NormalizeCommandRuntimeSpec(runner.CommandRuntimeSpec, string) (runner.CommandRuntimeResolvedSpec, error)
	}); ok {
		return normalizer.NormalizeCommandRuntimeSpec(spec, root)
	}
	if s.adapter.Backend == CommandRuntimeLocalSandboxBackend {
		return runner.NormalizeLocalSandboxCommandRuntimeSpec(spec, root)
	}
	return s.manager.NormalizeCommandRuntimeSpec(spec, root)
}

func commandRuntimeError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, runner.ErrCommandRuntimeLocalPowerShell):
		return apperror.Wrap(apperror.CodeFailedPrecondition,
			"Windows 隔离工作区需要 PowerShell 7。请在启动服务的环境中将 CYBERAGENT_POWERSHELL_PATH 配置为已安装的 pwsh.exe 完整路径，然后重启服务；当前命令尚未启动。", err)
	case errors.Is(err, runner.ErrCommandRuntimeBoundary):
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"command runtime boundary rejected the request", err)
	case errors.Is(err, runner.ErrCommandRuntimeJobNotFound):
		return apperror.Wrap(apperror.CodeNotFound,
			"command runtime job was not found", err)
	case errors.Is(err, runner.ErrCommandRuntimeJobClosed),
		errors.Is(err, runner.ErrCommandRuntimeUnavailable):
		return apperror.Wrap(apperror.CodeFailedPrecondition,
			"command runtime is unavailable for this operation", err)
	case errors.Is(err, runner.ErrCommandRuntimeUncertain):
		return apperror.Wrap(apperror.CodeConflict,
			"command runtime replay is uncertain", err)
	default:
		return apperror.Normalize(err)
	}
}

var _ toolgateway.CommandRuntimeExecutor = (*CommandRuntimeService)(nil)
var _ toolgateway.CommandRuntimeAdvertiser = (*CommandRuntimeService)(nil)
