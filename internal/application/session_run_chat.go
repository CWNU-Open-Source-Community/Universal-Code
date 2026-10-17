package application

import (
	"context"
	"fmt"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/codeintel"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/session"
)

type SessionRunStore interface {
	RunStore
	AgentRunnerStore
	OperatorSteeringStore
	GetRunBySession(ctx context.Context, sessionID string) (domain.Run, bool, error)
}

type SessionRunChatExecutor struct {
	store      SessionRunStore
	runs       *RunService
	supervisor *AgentRunner
}

func NewSessionRunChatExecutor(store SessionRunStore, router *llm.Router, checker policy.Checker) *SessionRunChatExecutor {
	return &SessionRunChatExecutor{
		store: store, runs: NewRunService(store), supervisor: NewAgentRunner(store, router, checker),
	}
}

func (e *SessionRunChatExecutor) WithActiveCalls(registry *ActiveCallRegistry) *SessionRunChatExecutor {
	if e != nil && e.supervisor != nil {
		e.supervisor.WithActiveCalls(registry)
	}
	return e
}

func (e *SessionRunChatExecutor) WithCodeIntel(manager *codeintel.Manager) *SessionRunChatExecutor {
	if e != nil && e.supervisor != nil {
		e.supervisor.WithCodeIntel(manager)
	}
	return e
}

func (e *SessionRunChatExecutor) WithLifecycleHooks(engine *hooks.Engine) *SessionRunChatExecutor {
	if e != nil {
		if e.runs != nil {
			e.runs.WithLifecycleHooks(engine)
		}
		if e.supervisor != nil {
			e.supervisor.WithLifecycleHooks(engine)
		}
	}
	return e
}

func (e *SessionRunChatExecutor) ExecuteSessionTurn(ctx context.Context, sess session.Session, input string) (session.RunChatResult, bool, error) {
	return e.ExecuteSessionTurnWithOptions(ctx, sess, input, session.RunChatOptions{})
}

func (e *SessionRunChatExecutor) PrepareSessionCommand(ctx context.Context, sess session.Session, input string) (session.Session, error) {
	fields := strings.Fields(input)
	if len(fields) == 0 || (fields[0] != "/compact" && fields[0] != "/model") {
		return sess, nil
	}
	run, attached, err := e.store.GetRunBySession(ctx, sess.ID)
	if err != nil || !attached {
		return sess, apperror.Normalize(err)
	}
	if run.SessionID != sess.ID {
		return session.Session{}, apperror.New(apperror.CodeConflict, "run and session binding changed")
	}
	if fields[0] == "/model" && len(fields) > 1 {
		return session.Session{}, apperror.New(apperror.CodeFailedPrecondition,
			"This Session is bound to its Run; select a model through Thread model control and continue the Thread to use it")
	}
	if fields[0] == "/compact" {
		return session.Session{}, apperror.New(apperror.CodeFailedPrecondition, "This Session is bound to its Run; compact the idle current Thread through thread compact <thread-id> or the Thread context control")
	}
	// The Run is the executor's route authority, even when an older Session
	// record contains a route written by the former slash-command path.
	sess.Route = run.Config.ModelRoute
	return sess, nil
}

func (e *SessionRunChatExecutor) ExecuteSessionTurnWithOptions(ctx context.Context,
	sess session.Session, input string, options session.RunChatOptions,
) (session.RunChatResult, bool, error) {
	if e == nil || e.store == nil || e.runs == nil || e.supervisor == nil {
		return session.RunChatResult{}, false, apperror.New(apperror.CodeFailedPrecondition, "session run chat dependencies are required")
	}
	run, attached, err := e.store.GetRunBySession(ctx, strings.TrimSpace(sess.ID))
	if err != nil || !attached {
		return session.RunChatResult{}, attached, apperror.Normalize(err)
	}
	if run.SessionID != sess.ID {
		return session.RunChatResult{}, true, apperror.New(apperror.CodeConflict, "run and session binding changed")
	}
	options.OperationKey = strings.TrimSpace(options.OperationKey)
	if options.OperationKey != "" {
		queued, queueErr := e.store.EnqueueOperatorSteering(ctx,
			domain.EnqueueOperatorSteeringRequest{
				RunID: run.ID, SessionID: sess.ID, Content: input,
				OperationKey: options.OperationKey, RequestedBy: "session_operator",
			})
		if queueErr != nil {
			return session.RunChatResult{}, true, apperror.Normalize(queueErr)
		}
		return queuedSessionRunChatResult(run, queued.Message, queued.Replayed), true, nil
	}
	steeringRequest := domain.EnqueueOperatorSteeringRequest{
		RunID: run.ID, SessionID: sess.ID, Content: input,
		OperationKey: idgen.New("session-steering"), RequestedBy: "session_operator",
	}
	queueIfBusy := func(current domain.Run) (session.RunChatResult, bool, error) {
		queued, busy, queueErr := e.store.EnqueueOperatorSteeringIfBusy(ctx, steeringRequest)
		if queueErr != nil || !busy {
			return session.RunChatResult{}, busy, apperror.Normalize(queueErr)
		}
		return queuedSessionRunChatResult(current, queued.Message, queued.Replayed), true, nil
	}
	switch run.Status {
	case domain.RunCreated, domain.RunPreparing:
		run, err = e.runs.Start(ctx, run.ID)
	case domain.RunPaused:
		if queued, busy, queueErr := queueIfBusy(run); queueErr != nil || busy {
			return queued, true, queueErr
		}
		run, err = e.runs.Resume(ctx, run.ID)
	case domain.RunRunning:
		if queued, busy, queueErr := queueIfBusy(run); queueErr != nil || busy {
			return queued, true, queueErr
		}
	case domain.RunWaitingApproval:
		return session.RunChatResult{}, true, apperror.New(apperror.CodeFailedPrecondition, "run is waiting for approval")
	default:
		return session.RunChatResult{}, true, apperror.New(apperror.CodeFailedPrecondition,
			fmt.Sprintf("run %s is terminal as %s", run.ID, run.Status))
	}
	if err != nil {
		return session.RunChatResult{}, true, apperror.Normalize(err)
	}
	result, err := e.supervisor.StepWithInput(ctx, run.ID, input)
	if err != nil {
		if apperror.CodeOf(err) == apperror.CodeConflict {
			if queued, busy, queueErr := queueIfBusy(run); queueErr != nil || busy {
				return queued, true, queueErr
			}
		}
		return session.RunChatResult{}, true, err
	}
	return session.RunChatResult{
		RunID: run.ID, UserMessage: result.UserMessage, ReplyMessage: result.ReplyMessage,
		ContextManaged:   true,
		ContextCompacted: result.ContextCompacted, ContextSummaryID: result.ContextSummaryID,
		Text: result.Text, Action: string(result.Action.Kind), RunStatus: string(result.RunStatus),
	}, true, nil
}

func queuedSessionRunChatResult(run domain.Run,
	message domain.OperatorSteeringMessage, replayed bool,
) session.RunChatResult {
	text := "Operator guidance queued for the next safe root-turn boundary."
	if replayed {
		text = "Operator guidance already has a durable steering record."
	}
	return session.RunChatResult{
		RunID: run.ID, Text: text,
		Action: "queued", RunStatus: string(run.Status), Queued: true,
		SteeringID: message.ID, SteeringSequence: message.Sequence,
		SteeringStatus: string(message.Status), SteeringReplayed: replayed,
	}
}
