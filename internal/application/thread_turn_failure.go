package application

import (
	"context"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

// ThreadTurnFailedError confirms a sealed failed product turn, not a generic
// execution error or an uncertain transport result. New intent uses a new key.
type ThreadTurnFailedError struct {
	Cause   error
	Failure *domain.ThreadTurnFailure
}

func (e *ThreadTurnFailedError) Error() string { return e.Cause.Error() }
func (e *ThreadTurnFailedError) Unwrap() error { return e.Cause }

type threadTurnFailureStore interface {
	EndFailedThreadTurn(context.Context, string, string, string) (domain.ThreadTurnFailure, bool, error)
	GetThreadTurnFailure(context.Context, string, string) (domain.ThreadTurnFailure, bool, error)
}

func failedProductTurnError(failure domain.ThreadTurnFailure) error {
	code := apperror.Code(failure.ErrorCode)
	if code == "" {
		code = apperror.CodeFailedPrecondition
	}
	message := "This turn ended with a recorded failure. Your input and completed work are preserved; send a new message to continue this conversation"
	if code == apperror.CodeCancelled {
		message = "This turn was stopped. Your input and completed work are preserved; send a new message to continue this conversation"
	} else if code == apperror.CodeResourceExhausted && failure.FailureStage == domain.ThreadFailureContextWindowExceeded {
		message = "The task context exceeds this model's input window. History and completed work are preserved; shorten the current input, shorten or remove a fixed requirement, or select a model with a larger context window"
	} else if code == apperror.CodeFailedPrecondition {
		switch failure.FailureStage {
		case domain.ThreadFailureToolRequestRejected:
			message = "The model's tool request was rejected before this batch executed. Earlier completed actions are preserved; send a new message to continue"
		case domain.ThreadFailureEmptyModelResponse:
			message = "The model returned no usable answer. This turn ended; earlier completed actions are preserved and you can send a new message to continue"
		case domain.ThreadFailureInvalidModelResponse:
			message = "The model returned an invalid response. This turn ended; earlier completed actions are preserved and you can send a new message to continue"
		}
	}
	return &ThreadTurnFailedError{Cause: apperror.New(code, message), Failure: &failure}
}

func (s *ThreadTurnService) closeFailedProductTurn(ctx context.Context, threadID string, handoff domain.RunExecutionHandoff) (domain.ThreadTurnFailure, bool, error) {
	store, ok := s.threads.store.(threadTurnFailureStore)
	if !ok {
		return domain.ThreadTurnFailure{}, false, nil
	}
	return closeFailedProductTurn(ctx, store, threadID, handoff)
}

func closeFailedProductTurn(ctx context.Context, store threadTurnFailureStore, threadID string,
	handoff domain.RunExecutionHandoff,
) (domain.ThreadTurnFailure, bool, error) {
	if handoff.Result == nil || handoff.Result.Status != domain.RunExecutionHandoffFailed {
		return domain.ThreadTurnFailure{}, false, nil
	}
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	return store.EndFailedThreadTurn(settleCtx, threadID, handoff.Operation.RunID, handoff.Operation.ID)
}
