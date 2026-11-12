package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/codeintel"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/webevidence"
)

type RunExecutionHandoffStore interface {
	SessionRunStore
	GetRunExecutionHandoff(context.Context, string) (domain.RunExecutionHandoff, bool, error)
	GetCommittedOperatorSteeringLifecycleActions(context.Context, string) (
		domain.RootActionKind, domain.RootActionKind, error)
	PrepareRunExecutionHandoff(context.Context, domain.RunExecutionHandoffOperation) (
		domain.RunExecutionHandoff, bool, error)
	CompleteRunExecutionHandoff(context.Context, string, domain.RunExecutionLease,
		domain.RunExecutionHandoffStatus, string, string, int, bool, bool) (
		domain.RunExecutionHandoffResult, bool, error)
}

type RunExecutionHandoffService struct {
	store          RunExecutionHandoffStore
	supervisor     *AgentRunner
	lifecycleHooks *hooks.Engine
}

type ExecuteRunHandoffRequest struct {
	Version      string
	RunID        string
	MaxSteps     int
	OperationKey string
	RequestedBy  string
}

type ExecuteRunHandoffResult struct {
	Handoff   domain.RunExecutionHandoff
	Execution ExecutionResult
	Replayed  bool
}

func NewRunExecutionHandoffService(store RunExecutionHandoffStore,
	router *llm.Router, checker policy.Checker,
) *RunExecutionHandoffService {
	return &RunExecutionHandoffService{
		store: store, supervisor: NewAgentRunner(store, router, checker),
	}
}

func (s *RunExecutionHandoffService) WithRunWorktree(drydocks *RunWorktreeService) *RunExecutionHandoffService {
	if s != nil && s.supervisor != nil {
		s.supervisor.WithRunWorktree(drydocks)
	}
	return s
}

// WithGeneratedContextCompaction selects the existing extractive fallback for
// embedders that explicitly disable auxiliary model calls. Threads default on.
func (s *RunExecutionHandoffService) WithGeneratedContextCompaction(enabled bool) *RunExecutionHandoffService {
	if s != nil && s.supervisor != nil {
		s.supervisor.WithGeneratedContextCompaction(enabled)
	}
	return s
}

func (s *RunExecutionHandoffService) WithActiveCalls(
	registry *ActiveCallRegistry,
) *RunExecutionHandoffService {
	if s != nil && s.supervisor != nil {
		s.supervisor.WithActiveCalls(registry)
	}
	return s
}

func (s *RunExecutionHandoffService) WithDockerSandboxProposalExecutor(
	executor toolgateway.DockerSandboxProposalExecutor,
) *RunExecutionHandoffService {
	if s != nil && s.supervisor != nil {
		s.supervisor.WithDockerSandboxProposalExecutor(executor)
	}
	return s
}

func (s *RunExecutionHandoffService) WithDebugTerminalAgentInput(
	controller DebugTerminalAgentInputController,
) *RunExecutionHandoffService {
	if s != nil && s.supervisor != nil {
		s.supervisor.WithDebugTerminalAgentInput(controller)
	}
	return s
}

func (s *RunExecutionHandoffService) WithCommandRuntime(
	executor toolgateway.CommandRuntimeExecutor,
) *RunExecutionHandoffService {
	if s != nil && s.supervisor != nil {
		s.supervisor.WithCommandRuntime(executor)
	}
	return s
}

func (s *RunExecutionHandoffService) WithStandardCodeDelivery(
	delivery *StandardCodeDeliveryService,
) *RunExecutionHandoffService {
	if s != nil && s.supervisor != nil {
		s.supervisor.WithStandardCodeDelivery(delivery)
	}
	return s
}

func (s *RunExecutionHandoffService) WithMCPClient(
	manager *mcp.Manager,
) *RunExecutionHandoffService {
	if s != nil && s.supervisor != nil {
		s.supervisor.WithMCPClient(manager)
	}
	return s
}

func (s *RunExecutionHandoffService) WithExecutionPermissionCapabilities(
	capabilities domain.ExecutionPermissionRuntimeCapabilities,
) *RunExecutionHandoffService {
	if s != nil && s.supervisor != nil {
		s.supervisor.WithExecutionPermissionCapabilities(capabilities)
	}
	return s
}

// WithWebEvidence installs the same production Web Evidence service used by
// the CLI into the Desktop-owned Supervisor. Keeping the service injectable
// prevents Desktop construction from silently falling back to a providerless
// web_search while preserving direct, allowlisted web_fetch support.
func (s *RunExecutionHandoffService) WithWebEvidence(
	service *webevidence.Service,
) *RunExecutionHandoffService {
	if s != nil && s.supervisor != nil && service != nil {
		s.supervisor.WithWebEvidence(service)
	}
	return s
}

// WithWebFetchAuthorizationScheduler keeps Supervisor capability projection
// aligned with the lifecycle-managed reconciler installed by the host process.
func (s *RunExecutionHandoffService) WithWebFetchAuthorizationScheduler(
	enabled bool,
) *RunExecutionHandoffService {
	if s != nil && s.supervisor != nil {
		s.supervisor.WithWebFetchAuthorizationScheduler(enabled)
	}
	return s
}

// WithBrowserActions gives the Supervisor access only to an already-opened
// ready Full CDP session. Open/Close remain operator control-plane operations.
func (s *RunExecutionHandoffService) WithBrowserActions(
	service *FullCDPProductionService,
) *RunExecutionHandoffService {
	if s != nil && s.supervisor != nil && service != nil {
		s.supervisor.WithBrowserActions(service)
	}
	return s
}

func (s *RunExecutionHandoffService) WithCodeIntel(
	manager *codeintel.Manager,
) *RunExecutionHandoffService {
	if s != nil && s.supervisor != nil {
		s.supervisor.WithCodeIntel(manager)
	}
	return s
}

func (s *RunExecutionHandoffService) WithLifecycleHooks(
	engine *hooks.Engine,
) *RunExecutionHandoffService {
	if s != nil {
		s.lifecycleHooks = engine
		if s.supervisor != nil {
			s.supervisor.WithLifecycleHooks(engine)
		}
	}
	return s
}

func (s *RunExecutionHandoffService) PublicModelStream(
	runID string,
) (PublicModelStreamSnapshot, bool) {
	if s == nil || s.supervisor == nil {
		return PublicModelStreamSnapshot{}, false
	}
	return s.supervisor.PublicModelStream(runID)
}

func (s *RunExecutionHandoffService) Execute(ctx context.Context,
	request ExecuteRunHandoffRequest,
) (ExecuteRunHandoffResult, error) {
	return s.execute(ctx, request, 0)
}

// executeToNaturalBoundary keeps the public Run handoff contract unchanged while
// allowing the product-level Thread facade to continue an accepted operator turn
// until the Supervisor reaches a durable boundary. The continuation limit is an
// internal safety fence; it is deliberately not supplied by the UI.
func (s *RunExecutionHandoffService) executeToNaturalBoundary(ctx context.Context,
	request ExecuteRunHandoffRequest, continuationLimit int,
) (ExecuteRunHandoffResult, error) {
	if continuationLimit <= 0 {
		return ExecuteRunHandoffResult{}, apperror.New(apperror.CodeInvalidArgument,
			"Thread turn continuation limit must be positive")
	}
	return s.execute(ctx, request, continuationLimit)
}

func (s *RunExecutionHandoffService) execute(ctx context.Context,
	request ExecuteRunHandoffRequest, continuationLimit int,
) (ExecuteRunHandoffResult, error) {
	if s == nil || s.store == nil || s.supervisor == nil {
		return ExecuteRunHandoffResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"Run execution handoff dependencies are required")
	}
	normalized, err := normalizeRunExecutionHandoffRequest(request)
	if err != nil {
		return ExecuteRunHandoffResult{}, err
	}
	keyDigest := runmutation.RunExecutionHandoffOperationDigest(normalized.RunID,
		normalized.OperationKey)
	requestFingerprint := runmutation.RunExecutionHandoffRequestFingerprint(
		normalized.RunID, normalized.RequestedBy, normalized.MaxSteps)
	handoff, found, err := s.store.GetRunExecutionHandoff(ctx, keyDigest)
	if err != nil {
		return ExecuteRunHandoffResult{}, apperror.Normalize(err)
	}
	replayedOperation := found
	if found {
		if err := validateRunExecutionHandoffReplay(handoff, normalized,
			keyDigest, requestFingerprint); err != nil {
			return ExecuteRunHandoffResult{}, err
		}
		if handoff.Result != nil {
			return ExecuteRunHandoffResult{Handoff: handoff,
				Execution: executionResultFromHandoff(handoff), Replayed: true}, nil
		}
	} else {
		run, err := s.store.GetRun(ctx, normalized.RunID)
		if err != nil {
			return ExecuteRunHandoffResult{}, apperror.Normalize(err)
		}
		operation := domain.RunExecutionHandoffOperation{
			ID:              idgen.New("run-handoff"),
			ProtocolVersion: domain.RunExecutionHandoffProtocolVersion,
			KeyDigest:       keyDigest, RequestFingerprint: requestFingerprint,
			RunID: run.ID, SessionID: run.SessionID, RequestedBy: normalized.RequestedBy,
			MaxSteps: normalized.MaxSteps, CreatedAt: time.Now().UTC(),
		}
		handoff, _, err = s.store.PrepareRunExecutionHandoff(ctx, operation)
		if err != nil {
			return ExecuteRunHandoffResult{}, apperror.Normalize(err)
		}
		if handoff.Result != nil {
			return ExecuteRunHandoffResult{Handoff: handoff,
				Execution: executionResultFromHandoff(handoff)}, nil
		}
	}

	workspaceID := ""
	if s.lifecycleHooks != nil {
		run, lookupErr := s.store.GetRun(ctx, normalized.RunID)
		if lookupErr != nil {
			return ExecuteRunHandoffResult{Handoff: handoff}, apperror.Normalize(lookupErr)
		}
		mission, lookupErr := s.store.GetMission(ctx, run.MissionID)
		if lookupErr != nil {
			return ExecuteRunHandoffResult{Handoff: handoff}, apperror.Normalize(lookupErr)
		}
		workspaceID = mission.WorkspaceID
	}
	execution := ExecutionResult{RunID: handoff.Operation.RunID,
		Steps: make([]LifecycleResult, 0), RunStatus: domain.RunRunning}
	err = s.supervisor.withRunExecutionLease(ctx, handoff.Operation.RunID,
		func(leaseCtx context.Context, lease domain.RunExecutionLease) error {
			return s.executeSelectionWithLease(leaseCtx, lease, &handoff, &execution,
				continuationLimit)
		})
	if err != nil {
		return ExecuteRunHandoffResult{Handoff: handoff, Execution: execution,
			Replayed: replayedOperation}, apperror.Normalize(err)
	}
	stored, storedFound, err := s.store.GetRunExecutionHandoff(ctx, keyDigest)
	if err != nil || !storedFound || stored.Result == nil {
		if err == nil {
			err = apperror.New(apperror.CodeInternal,
				"Run execution handoff completion was not persisted")
		}
		return ExecuteRunHandoffResult{Handoff: handoff, Execution: execution,
			Replayed: replayedOperation}, apperror.Normalize(err)
	}
	// The turn_end boundary fires when the Supervisor turn has completed and
	// the handoff result is durably persisted. Observation only.
	if stored.Result != nil {
		_ = executeLifecycleBoundary(ctx, s.lifecycleHooks, hooks.TurnEnd,
			normalized.RunID, workspaceID, map[string]any{
				"run_id":          normalized.RunID,
				"stop_reason":     stored.Result.StopReason,
				"run_status":      string(stored.Result.RunStatus),
				"steps_completed": stored.Result.StepsCompleted})
	}
	return ExecuteRunHandoffResult{Handoff: stored, Execution: execution,
		Replayed: replayedOperation}, nil
}

func (s *RunExecutionHandoffService) executeSelectionWithLease(ctx context.Context,
	lease domain.RunExecutionLease, handoff *domain.RunExecutionHandoff,
	execution *ExecutionResult, continuationLimit int,
) error {
	var executionErr error
	handoffSteps := 0
	var lastRequestedAction domain.RootActionKind
	var lastEffectiveAction domain.RootActionKind
	for _, item := range handoff.Items {
		message, err := s.store.GetOperatorSteering(ctx, item.MessageID)
		if err != nil {
			executionErr = apperror.Normalize(err)
			break
		}
		if message.RunID != handoff.Operation.RunID ||
			message.SessionID != handoff.Operation.SessionID {
			executionErr = apperror.New(apperror.CodeConflict,
				"Run execution handoff message binding changed")
			break
		}
		if message.Status != domain.OperatorSteeringPending {
			if message.Status == domain.OperatorSteeringCommitted {
				lastRequestedAction, lastEffectiveAction, err =
					s.store.GetCommittedOperatorSteeringLifecycleActions(ctx, message.ID)
				if err != nil {
					executionErr = apperror.Normalize(err)
					break
				}
			}
			continue
		}
		step, err := s.supervisor.stepSteeringMessageWithLease(ctx, lease,
			message.ID)
		if step.Turn > 0 {
			execution.Steps = append(execution.Steps, step)
			execution.RunStatus = step.RunStatus
			lastRequestedAction = step.RequestedAction
			lastEffectiveAction = step.Action.Kind
			handoffSteps++
		}
		if err != nil {
			latest, lookupErr := s.store.GetOperatorSteering(ctx, item.MessageID)
			if lookupErr == nil && latest.Status == domain.OperatorSteeringCancelled &&
				apperror.CodeOf(apperror.Normalize(err)) == apperror.CodeFailedPrecondition {
				continue
			}
			executionErr = apperror.Normalize(err)
			break
		}
		if continuationLimit > 0 && step.RunStatus == domain.RunWaitingApproval {
			execution.StopReason = "waiting_approval"
			break
		}
		if step.Action.Kind == domain.RootActionFinish {
			execution.StopReason = "root_finish"
			break
		}
		if step.Action.Kind == domain.RootActionWait {
			execution.StopReason = "root_wait"
			break
		}
	}
	if executionErr == nil && execution.StopReason == "" {
		if lastRequestedAction == domain.RootActionFinish &&
			lastEffectiveAction == domain.RootActionContinue {
			// Interactive operator turns deliberately keep their Run resumable by
			// projecting a requested finish to an effective continue. That Run-level
			// policy must not make the Thread facade manufacture another Supervisor
			// turn with the Mission goal as synthetic input. A requested finish is
			// therefore still the natural boundary of this product turn.
			execution.StopReason = "turn_finish"
		} else if continuationLimit > 0 {
			executionErr = s.continueToNaturalBoundaryWithLease(ctx, lease, execution,
				continuationLimit)
		}
	}
	run, runErr := s.store.GetRun(ctx, handoff.Operation.RunID)
	if runErr == nil {
		execution.RunStatus = run.Status
	} else if executionErr == nil {
		executionErr = apperror.Normalize(runErr)
	}
	status := domain.RunExecutionHandoffCompleted
	errorCode := ""
	if executionErr != nil {
		status = domain.RunExecutionHandoffFailed
		errorCode = strings.ToLower(string(apperror.CodeOf(executionErr)))
		if errors.Is(executionErr, errSupervisorContextWindow) {
			execution.StopReason = domain.ThreadFailureContextWindowExceeded
		}
	}
	if execution.StopReason == "" {
		if executionErr != nil {
			execution.StopReason = errorCode
		} else {
			execution.StopReason = "selection_drained"
		}
	}
	completeCtx, cancelComplete := context.WithTimeout(context.WithoutCancel(ctx),
		2*time.Second)
	defer cancelComplete()
	modelCalled := false
	toolCalled := false
	for _, step := range execution.Steps {
		modelCalled = modelCalled || step.ModelAttempts > 0
		toolCalled = toolCalled || step.ToolCalls > 0
	}
	result, _, completeErr := s.store.CompleteRunExecutionHandoff(completeCtx,
		handoff.Operation.ID, lease, status, execution.StopReason, errorCode,
		handoffSteps, modelCalled, toolCalled)
	if completeErr != nil {
		return apperror.Normalize(completeErr)
	}
	handoff.Result = &result
	return nil
}

func (s *RunExecutionHandoffService) continueToNaturalBoundaryWithLease(
	ctx context.Context, lease domain.RunExecutionLease, execution *ExecutionResult,
	continuationLimit int,
) error {
	for range continuationLimit {
		run, err := s.store.GetRun(ctx, execution.RunID)
		if err != nil {
			return apperror.Normalize(err)
		}
		execution.RunStatus = run.Status
		switch {
		case run.Terminal():
			execution.StopReason = "run_terminal"
			return nil
		case run.Status == domain.RunPaused:
			execution.StopReason = "run_paused"
			return nil
		case run.Status == domain.RunWaitingApproval:
			execution.StopReason = "waiting_approval"
			return nil
		}
		queue, err := s.store.GetOperatorSteeringQueueSummary(ctx, execution.RunID)
		if err != nil {
			return apperror.Normalize(err)
		}
		if queue.Pending+queue.Prepared > 0 {
			// Messages outside this durable handoff belong to a later product turn.
			// Do not absorb concurrently queued operator intent into this operation.
			execution.StopReason = "steering_pending"
			return nil
		}
		step, err := s.supervisor.stepWithLease(ctx, lease, "")
		if step.Turn > 0 {
			execution.Steps = append(execution.Steps, step)
			execution.RunStatus = step.RunStatus
		}
		if err != nil {
			return apperror.Normalize(err)
		}
		if step.RunStatus == domain.RunWaitingApproval {
			execution.StopReason = "waiting_approval"
			return nil
		}
		switch step.Action.Kind {
		case domain.RootActionFinish:
			execution.StopReason = "root_finish"
			return nil
		case domain.RootActionWait:
			execution.StopReason = supervisorWaitStopReason(step.Action)
			return nil
		}
	}
	run, err := s.store.GetRun(ctx, execution.RunID)
	if err != nil {
		return apperror.Normalize(err)
	}
	execution.RunStatus = run.Status
	execution.StopReason = "turn_safety_limit"
	return nil
}

func normalizeRunExecutionHandoffRequest(request ExecuteRunHandoffRequest) (
	ExecuteRunHandoffRequest, error,
) {
	if request.Version != domain.RunExecutionHandoffProtocolVersion {
		return ExecuteRunHandoffRequest{}, apperror.New(apperror.CodeInvalidArgument,
			"unsupported Run execution handoff version")
	}
	if request.RunID != strings.TrimSpace(request.RunID) ||
		!domain.ValidAgentID(request.RunID) || strings.ContainsRune(request.RunID, 0) {
		return ExecuteRunHandoffRequest{}, apperror.New(apperror.CodeInvalidArgument,
			"Run execution handoff Run id is invalid")
	}
	if request.MaxSteps <= 0 || request.MaxSteps > domain.MaxRunExecutionHandoffSteps {
		return ExecuteRunHandoffRequest{}, apperror.New(apperror.CodeInvalidArgument,
			"Run execution handoff step limit is invalid")
	}
	operationKey, err := domain.NormalizeAgentOperationKey(request.OperationKey)
	if err != nil || operationKey != request.OperationKey || containsSpaceOrControl(operationKey) {
		return ExecuteRunHandoffRequest{}, apperror.New(apperror.CodeInvalidArgument,
			"Run execution handoff idempotency key is invalid")
	}
	requestedBy := strings.TrimSpace(request.RequestedBy)
	if requestedBy != request.RequestedBy || !domain.ValidAgentID(requestedBy) ||
		strings.ContainsRune(requestedBy, 0) {
		return ExecuteRunHandoffRequest{}, apperror.New(apperror.CodeInvalidArgument,
			"Run execution handoff requester is invalid")
	}
	request.OperationKey = operationKey
	request.RequestedBy = requestedBy
	return request, nil
}

func validateRunExecutionHandoffReplay(handoff domain.RunExecutionHandoff,
	request ExecuteRunHandoffRequest, keyDigest string, requestFingerprint string,
) error {
	operation := handoff.Operation
	if operation.ProtocolVersion != domain.RunExecutionHandoffProtocolVersion ||
		operation.KeyDigest != keyDigest ||
		operation.RequestFingerprint != requestFingerprint ||
		operation.RunID != request.RunID || operation.RequestedBy != request.RequestedBy ||
		operation.MaxSteps != request.MaxSteps {
		return apperror.New(apperror.CodeConflict,
			"Run execution handoff key was already used for different intent")
	}
	return nil
}

func executionResultFromHandoff(handoff domain.RunExecutionHandoff) ExecutionResult {
	result := ExecutionResult{RunID: handoff.Operation.RunID,
		Steps: make([]LifecycleResult, 0)}
	if handoff.Result != nil {
		result.RunStatus = handoff.Result.RunStatus
		result.StopReason = handoff.Result.StopReason
	}
	return result
}
