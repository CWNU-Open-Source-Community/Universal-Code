package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/store"
)

type automaticFailureCancelStore struct {
	*store.SQLiteStore
	cancel context.CancelFunc
}

func TestHostOwnedAutomaticCallerDeadlineHandsOrdinaryQueueToHost(t *testing.T) {
	f := newAutomaticQueueFixture(t, automaticQueueOptions{callerTimeout: 5 * time.Second})
	err := awaitTurnResult(t, f.automaticDone)
	var failed *application.ThreadTurnFailedError
	code := apperror.CodeOf(apperror.Normalize(err))
	if !errors.As(err, &failed) || failed.Failure == nil || code != apperror.CodeDeadlineExceeded || failed.Failure.ErrorCode != string(code) {
		t.Fatalf("real caller deadline did not seal its interruption: %T %v", err, err)
	}
	t.Logf("caller deadline returned=%T code=%s sealed_code=%s errors.Is(deadline)=%t errors.Is(cancelled)=%t", err, code, failed.Failure.ErrorCode, errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled))
	f.releaseOrdinary()
	awaitAutomaticQueueIdle(t, f)
	if calls := len(f.provider.Requests()); calls != 3 {
		t.Fatalf("caller deadline stopped accepted ordinary work: model_calls=%d want=3", calls)
	}
	for _, request := range f.queued {
		observation, err := f.state.InspectThreadTurnRequest(t.Context(), request.ThreadID, request.OperationKey, request.RequestedBy)
		if err != nil || observation.State != "completed" || !observation.Settled {
			t.Errorf("caller deadline lost accepted input: %+v err=%v", observation, err)
		}
	}
}

func (s *automaticFailureCancelStore) EndFailedThreadTurn(ctx context.Context, threadID, runID, handoffID string) (domain.ThreadTurnFailure, bool, error) {
	failure, closed, err := s.SQLiteStore.EndFailedThreadTurn(ctx, threadID, runID, handoffID)
	if err == nil && closed && (failure.ErrorCode == string(apperror.CodeFailedPrecondition) || failure.ErrorCode == string(apperror.CodeDeadlineExceeded)) {
		// The provider failure is already durable. Only now cancel the automatic
		// caller, so cancellation cannot be mistaken for the failure's cause.
		s.cancel()
	}
	return failure, closed, err
}

func TestHostOwnedAutomaticSealedFailureKeepsOrdinaryQueueWhenCallerCancels(t *testing.T) {
	for _, failureCode := range []apperror.Code{apperror.CodeFailedPrecondition, apperror.CodeDeadlineExceeded} {
		for _, cancelAtFailure := range []bool{false, true} {
			name := string(failureCode) + "/caller_live"
			if cancelAtFailure {
				name = string(failureCode) + "/caller_cancelled_after_failure_commit"
			}
			t.Run(name, func(t *testing.T) {
				f := newAutomaticQueueFixture(t, automaticQueueOptions{failAutomatic: true, failAutomaticDeadline: failureCode == apperror.CodeDeadlineExceeded, cancelAtFailure: cancelAtFailure})
				f.releaseAutomatic()
				err := awaitTurnResult(t, f.automaticDone)
				var failed *application.ThreadTurnFailedError
				if !errors.As(err, &failed) || failed.Failure == nil || failed.Failure.ErrorCode != string(failureCode) {
					t.Fatalf("automatic failure was not returned as its durable outcome: %T %v", err, err)
				}
				failure := *failed.Failure
				f.releaseOrdinary()
				awaitAutomaticQueueIdle(t, f)
				stored, found, readErr := f.state.GetThreadTurnFailure(t.Context(), failure.RunID, failure.MessageID)
				if readErr != nil || !found || stored.ErrorCode != failure.ErrorCode || stored.AttemptID != failure.AttemptID || stored.EventSequence != failure.EventSequence {
					t.Fatalf("original sealed provider failure changed: before=%+v after=%+v found=%t err=%v", failure, stored, found, readErr)
				}
				if calls := len(f.provider.Requests()); calls != 1 {
					t.Errorf("late caller cancellation bypassed provider failure and ran ordinary inputs: model_calls=%d want=1", calls)
				}
				for i, request := range f.queued {
					observation, readErr := f.state.InspectThreadTurnRequest(t.Context(), request.ThreadID, request.OperationKey, request.RequestedBy)
					if readErr != nil || observation.State != "received" || observation.Settled || observation.MessageID != f.accepted[i].Submission.Message.ID || observation.RunID != f.accepted[i].Submission.Run.ID {
						t.Errorf("failed automatic boundary consumed accepted input: observation=%+v err=%v", observation, readErr)
					}
				}
				t.Logf("failure=%s original_attempt=%s returned=%T model_calls=%d late_caller_cancel=%t", failure.ErrorCode, failure.AttemptID, err, len(f.provider.Requests()), cancelAtFailure)
			})
		}
	}
}
