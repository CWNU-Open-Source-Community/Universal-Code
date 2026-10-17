package httpapi

import (
	"context"
	"net/http"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

const ThreadContextPinsPathTemplate = "/api/v1/threads/{thread_id}/context/pins"
const ThreadContextPinResolvePathTemplate = "/api/v1/threads/{thread_id}/context/pin-proposals/{proposal_id}/resolve"

type ThreadContextPinController interface {
	InspectContextPins(context.Context, string) (domain.ThreadContextPins, error)
	ControlContextPin(context.Context, domain.ThreadContextPinControl) (domain.ThreadContextPinResult, error)
	ResolveContextPinProposal(context.Context, domain.ResolveContextPinProposal) (domain.ThreadContextPinResult, error)
}

func matchThreadContextPinPath(path string) (threadID, proposalID string, matched bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 6 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "threads" || parts[4] != "context" {
		return "", "", false
	}
	if len(parts) == 6 && parts[5] == "pins" {
		return parts[3], "", true
	}
	if len(parts) == 8 && parts[5] == "pin-proposals" && parts[7] == "resolve" {
		return parts[3], parts[6], true
	}
	return "", "", false
}

func (a *API) serveThreadContextPins(writer http.ResponseWriter, request *http.Request, requestID, threadID, proposalID string) {
	controller, ok := a.threadTurnController.(ThreadContextPinController)
	if !ok {
		a.writeError(writer, requestID, apperror.New(apperror.CodeNotFound, "Thread context pins are unavailable"), 0)
		return
	}
	if err := validatePathIdentity(threadID); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if err := rejectQuery(request.URL.Query()); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if request.Method == http.MethodGet && proposalID == "" {
		state, err := controller.InspectContextPins(request.Context(), threadID)
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		a.writeSuccessStatus(writer, requestID, state, nil, http.StatusOK)
		return
	}
	if !a.sessionMessageEnabled || !a.authorizeThreadControl(writer, request, requestID) {
		if !a.sessionMessageEnabled {
			a.writeError(writer, requestID, apperror.New(apperror.CodeNotFound, "Thread context pin control is unavailable"), 0)
		}
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", "GET, POST")
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "context pins accept GET or POST"), http.StatusMethodNotAllowed)
		return
	}
	key, err := sessionControlIdempotencyKey(request.Header, "Thread context pins")
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	var result domain.ThreadContextPinResult
	if proposalID == "" {
		var input domain.ThreadContextPinControl
		if !a.decodeThreadControlBody(writer, request, requestID, "Thread context pin control", &input) {
			return
		}
		input.ThreadID = threadID
		input.OperationKey = key
		input.RequestedBy = "http_thread_operator"
		result, err = controller.ControlContextPin(request.Context(), input)
	} else {
		if err := validatePathIdentity(proposalID); err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		var input domain.ResolveContextPinProposal
		if !a.decodeThreadControlBody(writer, request, requestID, "Thread context pin proposal resolution", &input) {
			return
		}
		input.ThreadID = threadID
		input.ProposalID = proposalID
		input.OperationKey = key
		input.RequestedBy = "http_thread_operator"
		result, err = controller.ResolveContextPinProposal(request.Context(), input)
	}
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	a.writeSuccessStatus(writer, requestID, result, nil, http.StatusOK)
}
