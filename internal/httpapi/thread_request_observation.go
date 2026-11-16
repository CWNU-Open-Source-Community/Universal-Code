package httpapi

import (
	"context"
	"net/http"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

const ThreadCreationRequestPath = "/api/v1/threads/creation-request"
const ThreadTurnRequestPathTemplate = "/api/v1/threads/{thread_id}/turn-request"

type ThreadRequestObservationView struct {
	Kind               string                          `json:"kind"`
	State              string                          `json:"state"`
	Settled            bool                            `json:"settled"`
	WorkspaceID        string                          `json:"workspace_id"`
	ThreadID           string                          `json:"thread_id,omitempty"`
	RunID              string                          `json:"run_id,omitempty"`
	SessionID          string                          `json:"session_id,omitempty"`
	MessageID          string                          `json:"message_id,omitempty"`
	MessageStatus      string                          `json:"message_status,omitempty"`
	RequestFingerprint string                          `json:"request_fingerprint,omitempty"`
	TurnFailure        *ThreadTurnFailureReferenceView `json:"turn_failure,omitempty"`
	ErrorCode          string                          `json:"error_code,omitempty"`
	FailureStage       string                          `json:"failure_stage,omitempty"`
}

type threadRequestObserver interface {
	InspectThreadCreationRequest(context.Context, string, string, string) (domain.ThreadRequestObservation, error)
	InspectThreadTurnRequest(context.Context, string, string, string) (domain.ThreadRequestObservation, error)
}

func (a *API) threadRequestObservation(request *http.Request, threadID string) (any, *Page, error) {
	observer, ok := a.store.(threadRequestObserver)
	if !ok {
		return nil, nil, apperror.New(apperror.CodeNotFound, "Thread request observation is unavailable")
	}
	key, err := sessionControlIdempotencyKey(request.Header, "Thread request observation")
	if err != nil {
		return nil, nil, err
	}
	var observation domain.ThreadRequestObservation
	if threadID == "" {
		if err = validateSingleQueryValues(request.URL.Query(), "workspace_id"); err != nil {
			return nil, nil, err
		}
		workspaceID := request.URL.Query().Get("workspace_id")
		if err = validatePathIdentity(workspaceID); err != nil {
			return nil, nil, err
		}
		observation, err = observer.InspectThreadCreationRequest(request.Context(), workspaceID, key, "http_thread_operator")
	} else {
		if err = rejectQuery(request.URL.Query()); err != nil {
			return nil, nil, err
		}
		if err = validatePathIdentity(threadID); err != nil {
			return nil, nil, err
		}
		observation, err = observer.InspectThreadTurnRequest(request.Context(), threadID, key, "http_thread_operator")
	}
	if err != nil {
		return nil, nil, err
	}
	return threadRequestObservationView(observation), nil, nil
}

func threadRequestObservationView(observation domain.ThreadRequestObservation) ThreadRequestObservationView {
	view := ThreadRequestObservationView{Kind: observation.Kind, State: observation.State, Settled: observation.Settled,
		WorkspaceID: observation.WorkspaceID, ThreadID: observation.ThreadID, RunID: observation.RunID,
		SessionID: observation.SessionID, MessageID: observation.MessageID, MessageStatus: string(observation.MessageStatus),
		RequestFingerprint: observation.RequestFingerprint, TurnFailure: threadTurnFailureReference(observation.Failure)}
	if observation.Failure != nil {
		// Fixed allowlists, never expose provider or tool error bodies.
		view.ErrorCode = "turn_failed"
		switch observation.Failure.FailureStage {
		case domain.ThreadFailureToolRequestRejected, domain.ThreadFailureEmptyModelResponse,
			domain.ThreadFailureInvalidModelResponse, domain.ThreadFailureContextWindowExceeded, domain.ThreadFailureFullPermissionNotReady:
			view.FailureStage = observation.Failure.FailureStage
		}
	}
	return view
}
