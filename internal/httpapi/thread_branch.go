package httpapi

import (
	"net/http"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

type ThreadBranchRequestView struct {
	Version       string `json:"version"`
	SourceEventID string `json:"source_event_id"`
}

type ThreadBranchView struct {
	Version       string      `json:"version"`
	Thread        ThreadView  `json:"thread"`
	Run           RunView     `json:"run"`
	Session       SessionView `json:"session"`
	SourceEventID string      `json:"source_event_id"`
	Replayed      bool        `json:"replayed"`
}

func (a *API) serveThreadBranch(writer http.ResponseWriter, request *http.Request, requestID, threadID, operationKey string) {
	var view ThreadBranchRequestView
	if !a.decodeThreadControlBody(writer, request, requestID, "Thread branch", &view) {
		return
	}
	store, ok := any(a.store).(application.ThreadBranchStore)
	if !ok {
		a.writeError(writer, requestID, apperror.New(apperror.CodeFailedPrecondition, "Thread branch storage is unavailable"), 0)
		return
	}
	result, err := application.NewThreadBranchService(store).WithLifecycleHooks(a.lifecycleHooks).Branch(request.Context(), domain.ThreadBranchRequest{
		Version: view.Version, ThreadID: threadID, SourceEventID: view.SourceEventID, OperationKey: operationKey, RequestedBy: "http_thread_operator",
	})
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	a.writeSuccessStatus(writer, requestID, ThreadBranchView{Version: domain.ThreadBranchProtocolVersion,
		Thread: threadView(result.Thread, result.Run.Status), Run: runView(result.Run), Session: sessionView(result.Session),
		SourceEventID: result.SourceEventID, Replayed: result.Replayed}, nil, http.StatusCreated)
}
