package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/session"
)

type agentRootActionEvaluation struct {
	action     domain.RootAction
	recovery   rootActionTrailingCommentaryRecovery
	repairable bool
	err        error
}

func (s *AgentRunner) evaluateAgentRootAction(ctx context.Context, turn domain.SupervisorTurn,
	response *llm.ChatResponse, prepared *agentTurnPreparation, protocolRepair int, repairReason string,
) agentRootActionEvaluation {
	var action domain.RootAction
	var parseErr error
	var rootActionRecovery rootActionTrailingCommentaryRecovery
	var repairableRootResponse bool
	action, parseErr = parseRootActionForTurn(response.Text, prepared.context.threadEndTurn)
	structuredReply := parseErr == nil
	if parseErr != nil && prepared.context.threadEndTurn && supervisorNativeToolRounds(prepared.toolRounds) < domain.MaxSupervisorToolRounds &&
		domain.RootActionHasTrailingToolCalls(response.Text) {
		reason, _ := domain.NewSupervisorTextToolRepairReason(len(prepared.toolRounds))
		parseErr = errors.New(reason)
	}
	if parseErr != nil {
		if recoveredAction, recovery, recovered :=
			recoverRootActionWithTrailingCommentary(response.Text); recovered {
			action = recoveredAction
			rootActionRecovery = recovery
			parseErr = nil
		}
	}
	if parseErr != nil && turn.OperatorSteering && protocolRepair == 0 {
		if publicAction, ok := publicReplyRootAction(response.Text); ok {
			action = publicAction
			parseErr = nil
		}
	}
	// A nonempty root answer can use the existing bounded repair.
	// Empty answers have no content to reformat. Tool-request repair
	// instead retains the offered tools and the no-execution diagnostic.
	repairableRootResponse = strings.TrimSpace(response.Text) != ""
	validationAction := supervisorValidationAction(action, prepared.context.threadEndTurn)
	if parseErr == nil {
		parseErr = validateRootActionAgainstWorkBoard(validationAction, prepared.context.workItems, turn.Mode.Phase)
	}
	prepared.deferCompletionValidation = false
	if parseErr == nil && action.Kind == domain.RootActionFinish && s.projectHooks != nil {
		selection, err := s.projectHookSelection(ctx, turn.Run.ID)
		if err != nil {
			parseErr = err
		} else {
			prepared.deferCompletionValidation = selection.AfterEditFormat || selection.BeforeFinishTest
		}
	}
	if parseErr == nil && prepared.tools.standardCode != nil && !prepared.deferCompletionValidation {
		parseErr = prepared.tools.standardCode.ValidateAction(ctx, validationAction)
		if parseErr == nil && validationAction.Kind == domain.RootActionFinish {
			action = prepared.tools.standardCode.ProjectDeliveryAction(action)
		}
	}
	if rejectedRound, toolRepair := domain.SupervisorToolRequestRepairRound(repairReason); protocolRepair == 1 && toolRepair && len(prepared.toolRounds) <= rejectedRound {
		// A text-only replacement must not turn an unexecuted request
		// into a successful task reply. The persisted round boundary
		// also enforces this after reopening the same checkpoint.
		parseErr = errors.New("tool-request correction did not provide a valid corrected tool batch; the rejected batch was not executed")
	}
	_, textToolRepair := domain.SupervisorTextToolRepairRound(repairReason)
	if parseErr == nil && prepared.context.threadEndTurn && action.Kind == domain.RootActionContinue &&
		((structuredReply && len(prepared.toolRounds) > 0 && supervisorNativeToolRounds(prepared.toolRounds) < domain.MaxSupervisorToolRounds) ||
			(protocolRepair == 1 && textToolRepair && supervisorNativeToolRounds(prepared.toolRounds) < domain.MaxSupervisorToolRounds)) {
		// The model explicitly selected continue after actual tool work.
		// Do not consume the input as if it had selected an end-of-reply.
		// Reuse the one durable repair; finish/wait remain valid exits.
		reason, _ := domain.NewSupervisorThreadContinueRepairReason(len(prepared.toolRounds))
		if textToolRepair {
			reason = repairReason
		}
		parseErr = errors.New(reason)
	}
	return agentRootActionEvaluation{action: action, recovery: rootActionRecovery,
		repairable: repairableRootResponse, err: parseErr}
}

// commitAgentRootAction records and settles the model output before applying
// response policy and committing the lifecycle transition. retry is only the
// existing steering-conflict path; the caller retains the updated checkpoint.
func (s *AgentRunner) commitAgentRootAction(ctx context.Context, turn *domain.SupervisorTurn,
	result LifecycleResult, modelCall modelCallResult, action domain.RootAction,
	rootActionRecovery rootActionTrailingCommentaryRecovery, prepared *agentTurnPreparation,
) (completed LifecycleResult, retry bool, returnErr error) {
	response := modelCall.Response
	modelCall.Attempt.Outcome = llm.OutcomeSuccess
	eventCtx, eventCancel := supervisorModelEventContext(ctx)
	updated, err := s.recordRootActionModelCompleted(eventCtx, turn.Checkpoint,
		modelCall.Attempt, *response, rootActionRecovery)
	eventCancel()
	if err != nil {
		failure := s.failReceivedModelOutput(ctx, &result, turn, modelCall.Attempt, *response, err)
		return result, false, failure
	}
	turn.Checkpoint = updated
	result.Checkpoint = updated
	if s.monetary != nil {
		if settleErr := s.settleModelAccounting(ctx, turn.Checkpoint.RunID,
			modelCall.Attempt, response.Usage, len(response.ToolCalls)); settleErr != nil {
			failure := s.recordFailure(ctx, &result, settleErr, 0)
			return result, false, failure
		}
	}
	result.ModelOutcome = llm.OutcomeSuccess
	decision := s.checker.CheckText("supervisor_assistant_response", rootActionPolicyText(action))
	if !decision.Allowed {
		err := apperror.New(apperror.CodePolicyDenied, "policy denied supervisor response: "+decision.Reason)
		failure := s.recordFailure(ctx, &result, err, 0)
		return result, false, failure
	}
	safeAction := redactRootAction(action)
	result.RequestedAction = safeAction.Kind
	safeResponse := *response
	if !safeResponse.Replay.RequiresPrivateAssistantHistory() {
		safeResponse.Text = safeAction.Message
	}
	if safeAction.Kind == domain.RootActionFinish || (prepared.context.threadEndTurn && safeAction.Kind == domain.RootActionContinue && supervisorNativeToolRounds(prepared.toolRounds) < domain.MaxSupervisorToolRounds) {
		rounds, added, waiting, hookErr := s.beforeProjectHookFinish(ctx, *turn, prepared.toolRounds, prepared.tools.standardCode)
		prepared.toolRounds = rounds
		if hookErr != nil {
			return result, false, s.recordFailure(ctx, &result, hookErr, 0)
		}
		if waiting {
			return supervisorApprovalWaitingResult(result, rounds), false, nil
		}
		if added {
			return result, true, nil
		}
	}
	if prepared.deferCompletionValidation && prepared.tools.standardCode != nil {
		if err := prepared.tools.standardCode.ValidateAction(ctx, supervisorValidationAction(safeAction, prepared.context.threadEndTurn)); err != nil {
			return result, false, s.recordFailure(ctx, &result, err, 0)
		}
		safeAction = prepared.tools.standardCode.ProjectDeliveryAction(safeAction)
		if !safeResponse.Replay.RequiresPrivateAssistantHistory() {
			safeResponse.Text = safeAction.Message
		}
	}
	toolRoundCount := supervisorNativeToolRounds(prepared.toolRounds)
	var updatedRun domain.Run
	var checkpoint domain.SupervisorCheckpoint
	var messages session.TurnMessages
	if boundaryStore, ok := s.store.(supervisorToolBoundaryStore); ok && (turn.OperatorSteering || turn.ApprovalContinuation) &&
		toolRoundCount == domain.MaxSupervisorToolRounds && safeAction.Kind == domain.RootActionContinue {
		updatedRun, checkpoint, messages, err = boundaryStore.CompleteSupervisorToolBoundary(ctx, turn.Checkpoint, safeResponse, safeAction, decision, 0)
	} else {
		updatedRun, checkpoint, messages, err = s.store.CompleteSupervisorTurn(ctx, turn.Checkpoint, safeResponse, safeAction, decision, 0)
	}
	if err != nil {
		if apperror.CodeOf(apperror.Normalize(err)) == apperror.CodeConflict {
			if current, readErr := supervisorCurrentSteeringSequence(ctx, s.store, turn.Checkpoint); readErr == nil && current != modelCall.Attempt.SteeringSequence {
				return result, true, nil
			}
		}
		failure := s.recordFailure(ctx, &result, err, 0)
		return result, false, failure
	}
	if (safeAction.Kind == domain.RootActionFinish || safeAction.Kind == domain.RootActionWait) &&
		updatedRun.Status == domain.RunRunning && checkpoint.Phase == domain.SupervisorIdle {
		safeAction.Kind = domain.RootActionContinue
		safeAction.Summary = ""
		safeAction.Reason = ""
	}
	if safeAction.Kind == domain.RootActionContinue && updatedRun.Status == domain.RunPaused &&
		checkpoint.Phase == domain.SupervisorWaiting {
		guard, found, guardErr := s.store.GetRunProgressGuard(ctx, updatedRun.ID)
		if guardErr != nil {
			return result, false, apperror.Normalize(guardErr)
		}
		if !found || guard.Status != domain.RunProgressDetected ||
			guard.LastTurn != turn.Checkpoint.NextTurn {
			return result, false, apperror.New(apperror.CodeFailedPrecondition,
				"Run paused during a continue action without a matching progress guard")
		}
		safeAction.Kind = domain.RootActionWait
		safeAction.Reason = guard.WaitReason()
	}
	result.Status = LifecycleTurnCompleted
	result.Text = safeAction.Message
	result.Provider = response.Provider
	result.Model = response.Model
	result.Usage = response.Usage
	result.Action = safeAction
	result.RunStatus = updatedRun.Status
	result.UserMessage = messages.User
	result.ReplyMessage = messages.Assistant
	result.Checkpoint = checkpoint
	result.ToolBoundary = safeAction.Kind == domain.RootActionContinue && checkpoint.Phase == domain.SupervisorTurnStarted &&
		checkpoint.NextTurn == turn.Checkpoint.NextTurn+1 && checkpoint.AttemptID != turn.Checkpoint.AttemptID
	return result, false, nil
}

func validateRootActionAgainstWorkBoard(action domain.RootAction, workItems []domain.WorkItem,
	phase domain.ExecutionPhase,
) error {
	if action.Kind != domain.RootActionFinish {
		return nil
	}
	if phase == domain.ExecutionPhasePlan {
		return errors.New("root lifecycle finish is forbidden in plan phase; use wait for operator review")
	}
	active := 0
	for _, item := range workItems {
		if item.Status == domain.WorkItemPending || item.Status == domain.WorkItemInProgress || item.Status == domain.WorkItemBlocked {
			active++
		}
	}
	if active > 0 {
		return fmt.Errorf("root lifecycle finish conflicts with %d active work item(s)", active)
	}
	return nil
}
