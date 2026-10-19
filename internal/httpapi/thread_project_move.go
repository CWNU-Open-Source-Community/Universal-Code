package httpapi

import (
	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"net/http"
)

type ThreadProjectMoveRequestView struct {
	Version            string `json:"version"`
	ProjectWorkspaceID string `json:"project_workspace_id"`
	ExpectedVersion    int64  `json:"expected_version"`
}
type ThreadProjectMoveView struct {
	Version  string     `json:"version"`
	Thread   ThreadView `json:"thread"`
	Replayed bool       `json:"replayed"`
}

func (a *API) serveThreadProjectMove(w http.ResponseWriter, r *http.Request, requestID, threadID, key string) {
	var body ThreadProjectMoveRequestView
	if !a.decodeThreadControlBody(w, r, requestID, "Thread project move", &body) {
		return
	}
	st, ok := any(a.store).(application.ThreadProjectMoveStore)
	if !ok {
		a.writeError(w, requestID, apperror.New(apperror.CodeFailedPrecondition, "Thread project move storage is unavailable"), 0)
		return
	}
	result, err := application.NewThreadProjectMoveService(st, a.executionPermissionCapabilities.RuntimeAuthority).Move(r.Context(), domain.ThreadProjectMoveRequest{Version: body.Version, ThreadID: threadID, ProjectWorkspaceID: body.ProjectWorkspaceID, ExpectedVersion: body.ExpectedVersion, OperationKey: key, RequestedBy: "http_thread_operator"})
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	a.writeSuccess(w, requestID, ThreadProjectMoveView{Version: domain.ThreadProjectMoveVersion, Thread: threadView(result.Thread), Replayed: result.Replayed}, nil)
}
