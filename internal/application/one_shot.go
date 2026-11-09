package application

import (
	"context"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/session"
)

// OneShotService drives one non-interactive prompt through the ordinary
// Thread turn machinery: the CLI host executes the agent loop in-process,
// exactly as an interactive session would, and the caller receives the final
// assistant reply plus the run's terminal status. It adds no authority of its
// own — every permission, approval, and budget rule of the underlying run
// applies unchanged.
type OneShotService struct {
	store OneShotStore
	turns *ThreadTurnService
}

func NewOneShotService(store OneShotStore, turns *ThreadTurnService) *OneShotService {
	return &OneShotService{store: store, turns: turns}
}

// OneShotStore is the read surface the one-shot flow needs to recover the
// committed reply after the turn finishes.
type OneShotStore interface {
	ListSessionMessages(ctx context.Context, sessionID string, includeCompacted bool) ([]session.Message, error)
}

// OneShotRequest carries one already-validated prompt for an existing
// Thread. Run creation stays with the caller so workspace, profile, route,
// and budget flags remain explicit operator choices.
type OneShotRequest struct {
	Version      string
	ThreadID     string
	Content      string
	OperationKey string
	RequestedBy  string
}

// OneShotResult reports the turn outcome: the run and thread identities, the
// run status at return, and the last committed assistant reply (empty when
// the run paused before producing one).
type OneShotResult struct {
	ThreadID   string
	RunID      string
	RunStatus  string
	TurnStatus string
	StopReason string
	Reply      string
}

// Run executes one prompt to its turn boundary. A run that ends paused —
// waiting approval, budget exhaustion, or continuation — returns the pause
// status with whatever reply was committed, and the caller decides whether
// that satisfies the invocation.
func (s *OneShotService) Run(ctx context.Context, request OneShotRequest) (OneShotResult, error) {
	if s == nil || s.store == nil || s.turns == nil {
		return OneShotResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"one-shot dependencies are required")
	}
	content := strings.TrimSpace(request.Content)
	if content == "" {
		return OneShotResult{}, apperror.New(apperror.CodeInvalidArgument,
			"one-shot prompt is empty")
	}
	result, turnErr := s.turns.Execute(ctx, ExecuteThreadTurnRequest{
		Version: request.Version, ThreadID: request.ThreadID, Content: content,
		OperationKey: request.OperationKey, RequestedBy: request.RequestedBy,
	})
	// When the turn fails mid-execution, messages committed before the error
	// are still in the session. Extract any partial reply so the caller gets
	// useful output alongside the error, instead of losing the agent's work.
	submission := result.Submission
	out := OneShotResult{ThreadID: submission.Thread.ID, RunID: submission.Run.ID,
		RunStatus: string(submission.Run.Status)}
	if result.Execution != nil && result.Execution.Handoff.Result != nil {
		out.TurnStatus = string(result.Execution.Handoff.Result.Status)
		out.StopReason = result.Execution.Handoff.Result.StopReason
	}
	if submission.Session.ID != "" {
		// Use a detached context for the partial reply lookup: the caller's
		// ctx may already be cancelled (e.g. --timeout fired), and the reply
		// is precisely what the caller needs in that scenario.
		detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		reply, replyErr := s.lastAssistantReply(detached, submission.Session.ID)
		if replyErr == nil && reply != "" {
			out.Reply = reply
		} else if replyErr != nil && turnErr == nil {
			return out, replyErr
		}
	}
	return out, turnErr
}

// lastAssistantReply returns the newest committed assistant message. The turn
// machinery commits replies in order, so the last assistant row is the reply
// for this invocation.
func (s *OneShotService) lastAssistantReply(ctx context.Context,
	sessionID string,
) (string, error) {
	messages, err := s.store.ListSessionMessages(ctx, sessionID, true)
	if err != nil {
		return "", apperror.Normalize(err)
	}
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == "assistant" {
			return messages[index].Content, nil
		}
	}
	return "", nil
}
