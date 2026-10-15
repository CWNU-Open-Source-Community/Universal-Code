package application

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
)

const (
	threadTurnHandoffBatchSize = domain.MaxRunExecutionHandoffSteps
	threadTurnMaxBatches       = (domain.MaxPendingOperatorSteering +
		threadTurnHandoffBatchSize - 1) / threadTurnHandoffBatchSize
)

// ThreadTurnService is the product-facing execution facade. It owns the Run
// lifecycle and Supervisor handoff details so a client only submits one Thread
// turn. It composes the existing authority and event boundaries; the Thread
// message intent binds file preparation before a message becomes consumable.
type ThreadTurnService struct {
	threads          *ThreadService
	lifecycle        *RunLifecycleControlService
	execution        *RunExecutionHandoffService
	recovery         *ThreadRunRecoveryService
	lifecycleHooks   *hooks.Engine
	runtimeAuthority *domain.ExecutionPermissionRuntimeAuthority
	evidence         *EvidenceAttachmentService
	turnMu           sync.Mutex
	activeTurns      map[string]*activeThreadTurn
}

type threadEpochTransitionStore interface {
	GetThreadExecutionPermission(context.Context, string) (
		domain.ThreadExecutionPermissionSnapshot, error)
	GetRunExecutionPermission(context.Context, string) (
		domain.RunExecutionPermissionSnapshot, error)
	AdvanceThreadRunForPendingConfiguration(context.Context, string, string, string, string) (
		domain.Thread, domain.Run, bool, error)
}

type ExecuteThreadTurnRequest struct {
	plan          *threadPlanCommitBinding
	Version       string
	ThreadID      string
	Content       string
	OperationKey  string
	RequestedBy   string
	Files         []domain.WorkspaceFileReference
	Images        []domain.ImageReference
	Attachments   []domain.FileAttachmentReference
	MCPReferences []domain.MCPReference
}

type ExecuteThreadTurnResult struct {
	Submission       SubmitThreadMessageResult
	Execution        *ExecuteRunHandoffResult
	Replayed         bool
	ExecutionStarted bool
	ModelCalled      bool
	ToolCalled       bool
}

func NewThreadTurnService(store ThreadStore, lifecycle *RunLifecycleControlService,
	execution *RunExecutionHandoffService,
) *ThreadTurnService {
	service := &ThreadTurnService{threads: NewThreadService(store), lifecycle: lifecycle,
		execution: execution}
	if execution != nil && execution.supervisor != nil {
		service.threads.WithRunWorktree(execution.supervisor.drydocks)
	}
	if evidenceStore, ok := store.(EvidenceAttachmentStore); ok {
		service.evidence = NewEvidenceAttachmentService(evidenceStore)
	}
	if recoveryStore, ok := store.(ThreadRunRecoveryStore); ok {
		service.recovery = NewThreadRunRecoveryService(recoveryStore)
	}
	return service
}

func NewThreadTurnServiceWithExecutionCapabilities(store ThreadStore,
	lifecycle *RunLifecycleControlService, execution *RunExecutionHandoffService,
	capabilities domain.ExecutionPermissionRuntimeCapabilities,
) *ThreadTurnService {
	service := &ThreadTurnService{
		threads:   NewThreadServiceWithExecutionCapabilities(store, capabilities),
		lifecycle: lifecycle, execution: execution,
		runtimeAuthority: capabilities.RuntimeAuthority,
	}
	if execution != nil && execution.supervisor != nil {
		service.threads.WithRunWorktree(execution.supervisor.drydocks)
	}
	if evidenceStore, ok := store.(EvidenceAttachmentStore); ok {
		service.evidence = NewEvidenceAttachmentService(evidenceStore)
	}
	if recoveryStore, ok := store.(ThreadRunRecoveryStore); ok {
		service.recovery = NewThreadRunRecoveryService(recoveryStore).
			WithExecutionPermissionRuntimeAuthority(capabilities.RuntimeAuthority)
	}
	return service
}

func (s *ThreadTurnService) WithLifecycleHooks(engine *hooks.Engine) *ThreadTurnService {
	if s != nil {
		s.lifecycleHooks = engine
		if s.recovery != nil {
			s.recovery.WithLifecycleHooks(engine)
		}
	}
	return s
}

func (s *ThreadTurnService) WithExecutionPermissionRuntimeAuthority(
	authority *domain.ExecutionPermissionRuntimeAuthority,
) *ThreadTurnService {
	if s != nil {
		s.runtimeAuthority = authority
		if s.recovery != nil {
			s.recovery.WithExecutionPermissionRuntimeAuthority(authority)
		}
	}
	return s
}

func (s *ThreadTurnService) WithModelRouteRegistry(
	registry ThreadModelRouteRegistry,
) *ThreadTurnService {
	if s != nil && s.threads != nil {
		s.threads.WithModelRouteRegistry(registry)
	}
	return s
}

func (s *ThreadTurnService) execute(ctx context.Context,
	request ExecuteThreadTurnRequest,
) (ExecuteThreadTurnResult, error) {
	if s == nil || s.threads == nil || s.threads.store == nil || s.lifecycle == nil ||
		s.execution == nil || s.execution.store == nil || s.execution.supervisor == nil {
		return ExecuteThreadTurnResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"Thread turn dependencies are required")
	}
	normalized, err := normalizeSubmitThreadMessageRequest(SubmitThreadMessageRequest{
		Version: request.Version, ThreadID: request.ThreadID, Content: request.Content,
		OperationKey: request.OperationKey, RequestedBy: request.RequestedBy,
		Files: request.Files, Images: request.Images, Attachments: request.Attachments, MCPReferences: request.MCPReferences, plan: request.plan,
	})
	if err != nil {
		return ExecuteThreadTurnResult{}, err
	}
	intent, err := s.threads.reserveMessage(ctx, normalized)
	if err != nil {
		return ExecuteThreadTurnResult{}, err
	}
	if replay, found, err := s.findCompletedReplay(ctx, normalized); err != nil || found {
		return replay, err
	}
	if err := s.advancePastFailedTurn(ctx, normalized); err != nil {
		return ExecuteThreadTurnResult{}, err
	}
	if err := s.advancePastExhaustedBudget(ctx, normalized); err != nil {
		return ExecuteThreadTurnResult{}, err
	}
	if err := s.advanceForPendingConfiguration(ctx, normalized); err != nil {
		return ExecuteThreadTurnResult{}, err
	}

	var submission SubmitThreadMessageResult
	if len(normalized.Files) == 0 && len(normalized.Images) == 0 && len(normalized.Attachments) == 0 && len(normalized.MCPReferences) == 0 {
		submission, err = s.threads.Submit(ctx, normalized)
	} else {
		if len(normalized.Files) > 0 && s.evidence == nil {
			return ExecuteThreadTurnResult{}, apperror.New(apperror.CodeFailedPrecondition, "Thread file reference preparation is unavailable")
		}
		submission, intent, err = s.threads.prepareMessage(ctx, normalized, intent)
		if err != nil {
			return ExecuteThreadTurnResult{}, err
		}
		if len(normalized.Images) > 0 && submission.Message.ID == "" {
			ref, refErr := supervisorModelRef(s.execution.supervisor.router, submission.Run.Config.ModelRoute)
			if refErr != nil {
				return ExecuteThreadTurnResult{}, refErr
			}
			capability := s.execution.supervisor.router.DescribeVision(ref)
			if capability.State != llm.VisionSupported {
				return ExecuteThreadTurnResult{}, apperror.New(apperror.CodeFailedPrecondition, "Selected model image capability is "+string(capability.State)+"; select a confirmed vision model before sending images")
			}
		}
		if submission.Message.ID == "" {
			prepared := make([]session.PreparedEvidenceAttachment, 0, len(normalized.Files))
			for index, file := range normalized.Files {
				item, prepareErr := s.evidence.PrepareForThread(ctx, AttachEvidenceRequest{
					Version: session.EvidenceAttachmentProtocolVersion, RunID: submission.Run.ID,
					SourceKind: file.SourceKind, SourceRef: file.Path, ContentSHA256: file.ExpectedSHA256,
					OperationKey: domain.ThreadMessageFileOperationKey(intent.OperationKeyDigest, index), AttachedBy: normalized.RequestedBy})
				if prepareErr != nil {
					return ExecuteThreadTurnResult{}, apperror.Wrap(apperror.CodeOf(apperror.Normalize(prepareErr)), "Thread file references could not be verified; refresh the selected files and retry", prepareErr)
				}
				prepared = append(prepared, item)
			}
			// No lifecycle action or model-visible persistence occurs until all
			// selected snapshots have been validated against their expected hash.
			ready, readyErr := s.prepareSubmissionLifecycle(ctx, normalized, ExecuteThreadTurnResult{Submission: submission})
			if readyErr != nil {
				return ExecuteThreadTurnResult{}, readyErr
			}
			submission, err = s.threads.commitMessage(ctx, normalized, ready.Submission, prepared)
		}
	}
	if err != nil {
		return ExecuteThreadTurnResult{}, err
	}
	result := ExecuteThreadTurnResult{Submission: submission, Replayed: submission.Replayed}
	// Ordinary steering may queue only after the confirmation message itself
	// has passed its atomic plan/input binding and entered the existing journal.
	s.turnMu.Lock()
	if active := s.activeTurns[normalized.ThreadID]; active != nil && active.operationKey == normalized.OperationKey {
		active.preparingPlan = false
	}
	s.turnMu.Unlock()
	if submission.Replayed && submission.Message.Status != domain.OperatorSteeringPending {
		if replay, found, err := s.findCompletedReplay(ctx, normalized); err != nil || found {
			return replay, err
		}
		if submission.Run.Terminal() {
			return s.refresh(ctx, result)
		}
	}
	return s.executeSubmission(ctx, normalized, result)
}

func (s *ThreadTurnService) executeSubmission(ctx context.Context,
	normalized SubmitThreadMessageRequest, result ExecuteThreadTurnResult,
) (ExecuteThreadTurnResult, error) {
	submission := result.Submission
	if submission.Message.Status == domain.OperatorSteeringCancelled ||
		submission.Run.Status == domain.RunWaitingApproval {
		return s.refresh(ctx, result)
	}

	var err error
	result, err = s.prepareSubmissionLifecycle(ctx, normalized, result)
	if err != nil {
		return result, err
	}

	for batch := 1; batch <= threadTurnMaxBatches; batch++ {
		// A product Thread turn is driven by the operator message selected into
		// this handoff. The Supervisor may perform its complete model/tool loop
		// while committing that message, but a requested/effective `continue`
		// action must not manufacture another mission-driven Supervisor turn.
		// The next explicit operator message is the next product turn.
		executed, executeErr := s.execution.Execute(ctx,
			threadTurnExecutionRequest(normalized, result.Submission.Run.ID, batch))
		result.Execution = &executed
		result.Replayed = result.Replayed || executed.Replayed
		mergeThreadTurnExecution(&result, executed)
		if err := storedThreadTurnExecutionError(executed.Handoff); err != nil {
			if failure, closed, closeErr := s.closeFailedProductTurn(ctx, normalized.ThreadID, executed.Handoff); closeErr != nil {
				return result, closeErr
			} else if closed {
				if result.Submission.Message.ID == failure.MessageID {
					return result, failedProductTurnError(failure)
				}
				// An earlier accepted input failed before this explicit message.
				// Its failure is sealed; the next batch may now reach this message.
				if ctx.Err() == nil {
					continue
				}
			}
			return result, err
		}
		if executeErr != nil {
			return result, executeErr
		}
		message, err := s.execution.store.GetOperatorSteering(ctx,
			result.Submission.Message.ID)
		if err != nil {
			return result, apperror.Normalize(err)
		}
		result.Submission.Message = message
		if message.Status != domain.OperatorSteeringPending {
			return s.refresh(ctx, result)
		}
		if executed.Handoff.Result != nil && executed.Handoff.Result.StopReason == "root_wait" {
			// A tool approval keeps the Run running and the exact input prepared.
			// Respect the handoff's wait boundary until explicit continuation;
			// starting another batch cannot consume that pending approval.
			return s.refresh(ctx, result)
		}
		current, err := s.threads.store.GetRun(ctx, result.Submission.Run.ID)
		if err != nil {
			return result, apperror.Normalize(err)
		}
		result.Submission.Run = current
		if current.Status != domain.RunRunning {
			return s.refresh(ctx, result)
		}
	}
	return result, apperror.New(apperror.CodeResourceExhausted,
		"Thread turn could not reach its queued message within the bounded handoff batches")
}

func (s *ThreadTurnService) prepareSubmissionLifecycle(ctx context.Context,
	normalized SubmitThreadMessageRequest, result ExecuteThreadTurnResult,
) (ExecuteThreadTurnResult, error) {
	submission := result.Submission
	switch submission.Run.Status {
	case domain.RunCreated:
		controlled, err := s.lifecycle.Apply(ctx, ControlRunLifecycleRequest{
			Version: domain.RunLifecycleControlProtocolVersion, RunID: submission.Run.ID,
			Action: domain.RunLifecycleStart,
			OperationKey: threadTurnLifecycleOperationKey(normalized, submission.Run.ID,
				domain.RunLifecycleStart),
			RequestedBy: normalized.RequestedBy,
		})
		if err != nil {
			return result, err
		}
		result.Submission.Run = controlled.Run
		result.Replayed = result.Replayed || controlled.Replayed
	case domain.RunPaused:
		controlled, err := s.lifecycle.Apply(ctx, ControlRunLifecycleRequest{
			Version: domain.RunLifecycleControlProtocolVersion, RunID: submission.Run.ID,
			Action: domain.RunLifecycleResume,
			OperationKey: threadTurnLifecycleOperationKey(normalized, submission.Run.ID,
				domain.RunLifecycleResume),
			RequestedBy: normalized.RequestedBy,
		})
		if err != nil {
			return result, err
		}
		result.Submission.Run = controlled.Run
		result.Replayed = result.Replayed || controlled.Replayed
	case domain.RunRunning:
		// The exact running Run is ready for the durable handoff below.
	default:
		return result, apperror.New(apperror.CodeFailedPrecondition,
			fmt.Sprintf("Thread turn Run %s is %s", submission.Run.ID,
				submission.Run.Status))
	}

	return result, nil
}

func (s *ThreadTurnService) advanceForPendingConfiguration(ctx context.Context,
	request SubmitThreadMessageRequest,
) error {
	store, ok := s.threads.store.(threadEpochTransitionStore)
	if !ok {
		return nil
	}
	threadRecord, err := s.threads.store.GetThread(ctx, request.ThreadID)
	if err != nil {
		return apperror.Normalize(err)
	}
	if threadRecord.ActiveRunID == "" {
		return nil
	}
	active, err := s.threads.store.GetRun(ctx, threadRecord.ActiveRunID)
	if err != nil {
		return apperror.Normalize(err)
	}
	pending, err := s.pendingEpochConfiguration(ctx, store, threadRecord, active)
	if err != nil || !pending {
		return err
	}
	_, superseded, _, err := store.AdvanceThreadRunForPendingConfiguration(ctx,
		threadRecord.ID, active.ID, request.RequestedBy,
		"thread-turn-epoch-transition-"+runmutation.Fingerprint(
			"thread_turn_epoch_transition_operation.v1", request.ThreadID,
			active.ID, request.OperationKey))
	if err != nil {
		if apperror.CodeOf(err) == apperror.CodeConflict {
			current, currentErr := s.threads.store.GetThread(ctx, request.ThreadID)
			if currentErr == nil && current.ActiveRunID != active.ID {
				return nil
			}
		}
		return apperror.Normalize(err)
	}
	if s.runtimeAuthority != nil {
		s.runtimeAuthority.RevokeRun(superseded.ID)
	}
	mission, missionErr := s.threads.store.GetMission(ctx, superseded.MissionID)
	if missionErr == nil {
		_ = executeLifecycleBoundary(ctx, s.lifecycleHooks, hooks.RunCompleted,
			superseded.ID, mission.WorkspaceID, map[string]any{
				"session_id": superseded.SessionID, "from": active.Status,
				"to": domain.RunCancelled, "source": "thread_epoch_transition",
			})
	}
	if releaser, ok := s.threads.store.(threadRunRecoveryMonetaryReleaser); ok {
		_, _ = releaser.ReleaseOpenMonetaryReservations(ctx, superseded.ID)
	}
	if reconciler, ok := s.threads.store.(threadRunRecoveryDependencyReconciler); ok {
		_, _ = reconciler.ReconcileDependencyEdges(ctx, superseded.ID)
	}
	return nil
}

func (s *ThreadTurnService) advancePastExhaustedBudget(ctx context.Context, request SubmitThreadMessageRequest) error {
	store, ok := s.threads.store.(interface {
		AdvanceThreadRunForExhaustedBudget(context.Context, string, string, string) (domain.Run, bool, error)
	})
	if !ok {
		return nil
	}
	thread, err := s.threads.store.GetThread(ctx, request.ThreadID)
	if err != nil {
		return apperror.Normalize(err)
	}
	if thread.ActiveRunID == "" {
		return nil
	}
	retired, changed, err := store.AdvanceThreadRunForExhaustedBudget(ctx, thread.ID, thread.ActiveRunID, request.RequestedBy)
	if err != nil {
		return apperror.Normalize(err)
	}
	if changed {
		if s.runtimeAuthority != nil {
			s.runtimeAuthority.RevokeRun(retired.ID)
		}
		if releaser, ok := s.threads.store.(threadRunRecoveryMonetaryReleaser); ok {
			_, _ = releaser.ReleaseOpenMonetaryReservations(ctx, retired.ID)
		}
		if reconciler, ok := s.threads.store.(threadRunRecoveryDependencyReconciler); ok {
			_, _ = reconciler.ReconcileDependencyEdges(ctx, retired.ID)
		}
	}
	return nil
}

func (s *ThreadTurnService) pendingEpochConfiguration(ctx context.Context,
	store threadEpochTransitionStore, threadRecord domain.Thread, active domain.Run,
) (bool, error) {
	if reader, ok := s.threads.store.(threadModelRoutePreferenceReader); ok {
		preference, found, err := reader.GetThreadModelRoutePreference(ctx, threadRecord.ID)
		if err != nil {
			return false, apperror.Normalize(err)
		}
		if found {
			desiredRoute := ""
			if preference.Selected {
				desiredRoute = preference.Provider + "/" + preference.Model
			} else {
				mission, missionErr := s.threads.store.GetMission(ctx, threadRecord.MissionID)
				if missionErr != nil {
					return false, apperror.Normalize(missionErr)
				}
				desiredRoute = string(mission.Profile)
			}
			if s.threads.modelRoutes == nil {
				return false, apperror.New(apperror.CodeFailedPrecondition,
					"Thread model route Registry is required")
			}
			activeRef, activeErr := resolveConfiguredModelRef(
				s.threads.modelRoutes.Router(), active.Config.ModelRoute)
			desiredRef, desiredErr := resolveConfiguredModelRef(
				s.threads.modelRoutes.Router(), desiredRoute)
			if activeErr != nil || desiredErr != nil {
				return false, apperror.New(apperror.CodeFailedPrecondition,
					"Thread model route is no longer resolvable")
			}
			if activeRef != desiredRef {
				// Resolve alone only parses a model reference. Reuse the same
				// eligibility projection as successor preparation before ending
				// the current Run; the successor still validates it again.
				catalog, catalogErr := NewThreadModelRouteService(nil, s.threads.modelRoutes).Catalog(ctx)
				if catalogErr != nil {
					return false, catalogErr
				}
				if !slices.ContainsFunc(catalog.Routes, func(route ModelRouteCatalogItem) bool {
					return route.ProviderID == desiredRef.Provider && route.Model == desiredRef.Model && route.Selectable
				}) {
					return false, apperror.New(apperror.CodeFailedPrecondition,
						"selected Thread model route is no longer eligible")
				}
				return true, nil
			}
		}
	}
	threadPermission, err := store.GetThreadExecutionPermission(ctx, threadRecord.ID)
	if err != nil {
		return false, apperror.Normalize(err)
	}
	runPermission, err := store.GetRunExecutionPermission(ctx, active.ID)
	if err != nil {
		return false, apperror.Normalize(err)
	}
	return threadPermission.Mode != runPermission.Mode, nil
}

func (s *ThreadTurnService) advancePastFailedTurn(ctx context.Context,
	request SubmitThreadMessageRequest,
) error {
	if s.recovery == nil || s.recovery.store == nil {
		return nil
	}
	recovery, found, err := s.recovery.store.GetThreadRunRecovery(ctx, request.ThreadID)
	if err != nil {
		return apperror.Normalize(err)
	}
	if !found {
		return s.execution.closeHistoricalWebFetchThreadFailure(ctx, request.ThreadID)
	}
	if !recovery.Quiescent {
		return apperror.New(apperror.CodeUnavailable,
			"The previous Thread turn is still stopping; retry this message shortly")
	}
	if store, ok := s.threads.store.(threadTurnFailureStore); ok {
		_, closed, closeErr := store.EndFailedThreadTurn(ctx, request.ThreadID, recovery.RunID, recovery.HandoffOperationID)
		if closeErr != nil {
			return apperror.Normalize(closeErr)
		}
		if closed {
			return nil
		}
		if reader, ok := s.threads.store.(interface {
			GetSupervisorCheckpoint(context.Context, string) (domain.SupervisorCheckpoint, bool, error)
		}); ok {
			checkpoint, found, err := reader.GetSupervisorCheckpoint(ctx, recovery.RunID)
			if err != nil {
				return apperror.Normalize(err)
			}
			// A handoff may fail before preparing any input (for example an
			// exhausted budget). No old attempt can be replayed in this case.
			if !found || (checkpoint.AttemptID == "" && checkpoint.PendingInput == "") {
				return nil
			}
		}
		return apperror.New(apperror.CodeFailedPrecondition,
			"The earlier execution has no safely closable prepared input; inspect its recorded outcome before continuing")
	}
	return apperror.New(apperror.CodeFailedPrecondition,
		"Thread failed-turn persistence is unavailable; the previous input has been retained")
}

func (s *ThreadTurnService) findCompletedReplay(ctx context.Context,
	request SubmitThreadMessageRequest,
) (ExecuteThreadTurnResult, bool, error) {
	threadRecord, err := s.threads.store.GetThread(ctx, request.ThreadID)
	if err != nil {
		return ExecuteThreadTurnResult{}, false, apperror.Normalize(err)
	}
	bindings, err := s.threads.store.ListThreadRuns(ctx, request.ThreadID)
	if err != nil {
		return ExecuteThreadTurnResult{}, false, apperror.Normalize(err)
	}
	expectedContent, err := domain.NormalizeThreadMessageContent(
		redact.String(request.Content), request.Images, request.Attachments)
	if err != nil {
		return ExecuteThreadTurnResult{}, false, apperror.Normalize(err)
	}
	expectedDigest := domain.OperatorSteeringContentSHA256(expectedContent)
	for bindingIndex := len(bindings) - 1; bindingIndex >= 0; bindingIndex-- {
		binding := bindings[bindingIndex]
		run, err := s.threads.store.GetRun(ctx, binding.RunID)
		if err != nil {
			return ExecuteThreadTurnResult{}, false, apperror.Normalize(err)
		}
		for batch := threadTurnMaxBatches; batch >= 1; batch-- {
			executionRequest := threadTurnExecutionRequest(request, run.ID, batch)
			normalizedExecution, err := normalizeRunExecutionHandoffRequest(executionRequest)
			if err != nil {
				return ExecuteThreadTurnResult{}, false, err
			}
			keyDigest := runmutation.RunExecutionHandoffOperationDigest(run.ID,
				normalizedExecution.OperationKey)
			handoff, found, err := s.execution.store.GetRunExecutionHandoff(ctx, keyDigest)
			if err != nil {
				return ExecuteThreadTurnResult{}, false, apperror.Normalize(err)
			}
			if !found || handoff.Result == nil {
				continue
			}
			fingerprint := runmutation.RunExecutionHandoffRequestFingerprint(run.ID,
				normalizedExecution.RequestedBy, normalizedExecution.MaxSteps)
			if err := validateRunExecutionHandoffReplay(handoff, normalizedExecution,
				keyDigest, fingerprint); err != nil {
				return ExecuteThreadTurnResult{}, false, err
			}
			for _, item := range handoff.Items {
				message, err := s.execution.store.GetOperatorSteering(ctx, item.MessageID)
				if err != nil {
					return ExecuteThreadTurnResult{}, false, apperror.Normalize(err)
				}
				if message.RequestedBy != request.RequestedBy ||
					message.OriginalContentSHA256 != expectedDigest {
					continue
				}
				queued, err := s.threads.store.EnqueueOperatorSteering(ctx,
					domain.EnqueueOperatorSteeringRequest{RunID: run.ID,
						SessionID: run.SessionID, Content: request.Content,
						Images: request.Images, Attachments: request.Attachments,
						OperationKey: request.OperationKey, RequestedBy: request.RequestedBy})
				if err != nil {
					return ExecuteThreadTurnResult{}, false, apperror.Normalize(err)
				}
				if !queued.Replayed || queued.Message.ID != message.ID {
					return ExecuteThreadTurnResult{}, false, apperror.New(
						apperror.CodeConflict,
						"Thread turn replay does not match its durable operator message")
				}
				continuation := SubmitThreadMessageResult{Run: run, Message: queued.Message}
				if err := s.threads.projectMessageContinuation(ctx, request, &continuation); err != nil {
					return ExecuteThreadTurnResult{}, false, apperror.Normalize(err)
				}
				if failureStore, ok := s.threads.store.(threadTurnFailureStore); ok {
					failure, closed, failureErr := failureStore.GetThreadTurnFailure(ctx, run.ID, message.ID)
					if failureErr != nil {
						return ExecuteThreadTurnResult{}, false, apperror.Normalize(failureErr)
					}
					if closed {
						linkedSession, sessionErr := s.threads.store.GetSession(ctx, run.SessionID)
						if sessionErr != nil {
							return ExecuteThreadTurnResult{}, false, apperror.Normalize(sessionErr)
						}
						executed := ExecuteRunHandoffResult{Handoff: handoff, Execution: executionResultFromHandoff(handoff), Replayed: true}
						result := ExecuteThreadTurnResult{Submission: SubmitThreadMessageResult{Thread: threadRecord, Run: run, Session: linkedSession, Message: queued.Message, PredecessorRunID: continuation.PredecessorRunID, SuccessorCreated: continuation.SuccessorCreated, Replayed: true}, Execution: &executed, Replayed: true}
						mergeThreadTurnExecution(&result, executed)
						return result, true, failedProductTurnError(failure)
					}
				}
				if queued.Message.Status == domain.OperatorSteeringPending {
					if handoff.Result.Status == domain.RunExecutionHandoffFailed {
						if failure, closed, closeErr := s.closeFailedProductTurn(ctx, request.ThreadID, handoff); closeErr != nil {
							return ExecuteThreadTurnResult{}, true, closeErr
						} else if closed {
							if failure.MessageID == message.ID {
								return ExecuteThreadTurnResult{Replayed: true}, true, failedProductTurnError(failure)
							}
							continue
						}
						linkedSession, sessionErr := s.threads.store.GetSession(ctx,
							run.SessionID)
						if sessionErr != nil {
							return ExecuteThreadTurnResult{}, false,
								apperror.Normalize(sessionErr)
						}
						executed := ExecuteRunHandoffResult{Handoff: handoff,
							Execution: executionResultFromHandoff(handoff), Replayed: true}
						result := ExecuteThreadTurnResult{
							Submission: SubmitThreadMessageResult{Thread: threadRecord,
								Run: run, Session: linkedSession, Message: queued.Message,
								PredecessorRunID: continuation.PredecessorRunID, SuccessorCreated: continuation.SuccessorCreated, Replayed: true},
							Execution: &executed, Replayed: true,
						}
						mergeThreadTurnExecution(&result, executed)
						return result, true, storedThreadTurnExecutionError(handoff)
					}
					// The previous batch reached approval, wait, or another safe boundary
					// before this exact message. A later retry may resume the same Run and
					// advance to the next deterministic batch without duplicating input.
					continue
				}
				linkedSession, err := s.threads.store.GetSession(ctx, run.SessionID)
				if err != nil {
					return ExecuteThreadTurnResult{}, false, apperror.Normalize(err)
				}
				executed := ExecuteRunHandoffResult{Handoff: handoff,
					Execution: executionResultFromHandoff(handoff), Replayed: true}
				result := ExecuteThreadTurnResult{
					Submission: SubmitThreadMessageResult{Thread: threadRecord, Run: run,
						Session: linkedSession, Message: queued.Message,
						PredecessorRunID: continuation.PredecessorRunID,
						SuccessorCreated: continuation.SuccessorCreated,
						Replayed:         true},
					Execution: &executed, Replayed: true,
				}
				mergeThreadTurnExecution(&result, executed)
				if err := storedThreadTurnExecutionError(handoff); err != nil {
					if queued.Message.Status != domain.OperatorSteeringCommitted {
						return result, true, err
					}
					// A separate, explicitly requested Run handoff may have recovered
					// this exact message. Confirm its committed delivery/event binding
					// without rewriting or presenting the original failed handoff as
					// successful. A pending/cancelled message retains the old error.
					if _, _, commitErr := s.execution.store.GetCommittedOperatorSteeringLifecycleActions(ctx,
						queued.Message.ID); commitErr != nil {
						return result, true, apperror.Normalize(commitErr)
					}
				}
				return result, true, nil
			}
		}
	}
	return ExecuteThreadTurnResult{}, false, nil
}

func (s *ThreadTurnService) refresh(ctx context.Context,
	result ExecuteThreadTurnResult,
) (ExecuteThreadTurnResult, error) {
	run, err := s.threads.store.GetRun(ctx, result.Submission.Run.ID)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	threadRecord, err := s.threads.store.GetThread(ctx, result.Submission.Thread.ID)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	result.Submission.Run = run
	result.Submission.Thread = threadRecord
	return result, nil
}

func threadTurnLifecycleOperationKey(request SubmitThreadMessageRequest, runID string,
	action domain.RunLifecycleAction,
) string {
	return "thread-turn-lifecycle-" + runmutation.Fingerprint(
		"thread_turn_lifecycle_operation.v1", request.ThreadID, runID,
		request.OperationKey, string(action))
}

func threadTurnExecutionRequest(request SubmitThreadMessageRequest, runID string,
	batch int,
) ExecuteRunHandoffRequest {
	return ExecuteRunHandoffRequest{Version: domain.RunExecutionHandoffProtocolVersion,
		RunID: runID, MaxSteps: threadTurnHandoffBatchSize,
		OperationKey: "thread-turn-handoff-" + runmutation.Fingerprint(
			"thread_turn_handoff_operation.v1", request.ThreadID, runID,
			request.OperationKey, fmt.Sprint(batch)),
		RequestedBy: request.RequestedBy}
}

func mergeThreadTurnExecution(result *ExecuteThreadTurnResult,
	executed ExecuteRunHandoffResult,
) {
	if result == nil || executed.Handoff.Result == nil {
		return
	}
	completed := executed.Handoff.Result
	result.ExecutionStarted = result.ExecutionStarted || completed.LeaseID != ""
	result.ModelCalled = result.ModelCalled || completed.ModelCalled
	result.ToolCalled = result.ToolCalled || completed.ToolCalled
}

func storedThreadTurnExecutionError(handoff domain.RunExecutionHandoff) error {
	if handoff.Result == nil || handoff.Result.Status != domain.RunExecutionHandoffFailed {
		return nil
	}
	code := apperror.Code(strings.ToUpper(strings.TrimSpace(handoff.Result.ErrorCode)))
	if code == "" {
		code = apperror.CodeInternal
	}
	message := "This Thread turn stopped at a durable failure boundary; send the next message to continue in a fresh execution context"
	switch code {
	case apperror.CodeFailedPrecondition:
		message = "The current Thread turn could not continue because an execution precondition was not met; send another message to retry this Thread"
	case apperror.CodeUnavailable:
		message = "The model service is unavailable; retry or send another message to continue this Thread"
	case apperror.CodeDeadlineExceeded:
		message = "The current turn timed out; send the next message to continue this Thread"
	case apperror.CodeResourceExhausted:
		message = "The current execution reached a resource limit; adjust the settings and send the next message to continue"
	}
	return apperror.New(code, message)
}
