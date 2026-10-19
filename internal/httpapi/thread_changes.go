package httpapi

import (
	"net/http"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
)

const ThreadChangesPathTemplate = "/api/v1/threads/{thread_id}/changes"
const ThreadChangesUndoPathTemplate = "/api/v1/threads/{thread_id}/changes/undo"

func (a *API) threadChanges(request *http.Request, threadID string) (any, *Page, error) {
	query := request.URL.Query()
	if err := validateSingleQueryValues(query, "source_event_id"); err != nil {
		return nil, nil, err
	}
	if len(query["source_event_id"]) != 1 {
		return nil, nil, apperror.New(apperror.CodeInvalidArgument, "source_event_id must appear exactly once")
	}
	reader, ok := a.store.(application.ThreadChangeStore)
	if !ok {
		return nil, nil, apperror.New(apperror.CodeUnavailable, "Thread change summary is unavailable")
	}
	result, err := application.NewThreadChangeService(reader).Summary(request.Context(), threadID, query.Get("source_event_id"))
	if err == nil {
		if store, ok := a.store.(application.ThreadChangeUndoStore); ok {
			status, statusErr := application.NewThreadChangeUndoService(store, a.executionPermissionCapabilities).Status(request.Context(), result)
			if statusErr != nil {
				return nil, nil, statusErr
			}
			result.Undo = &status
		}
	}
	return result, nil, err
}

func matchThreadChangesUndoPath(path string) (string, bool) {
	parts := strings.Split(strings.TrimPrefix(path, ThreadCollectionPath+"/"), "/")
	if strings.HasPrefix(path, ThreadCollectionPath+"/") && len(parts) == 3 && parts[0] != "" && parts[1] == "changes" && parts[2] == "undo" {
		return parts[0], true
	}
	return "", false
}

func (a *API) serveThreadChangesUndo(writer http.ResponseWriter, request *http.Request, requestID, threadID string) {
	if !a.workspaceCheckpointControlEnabled {
		a.writeError(writer, requestID, apperror.New(apperror.CodeNotFound, "Thread change undo is unavailable"), http.StatusNotFound)
		return
	}
	if !a.authorizeThreadControl(writer, request, requestID) {
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "Thread change undo only supports POST"), http.StatusMethodNotAllowed)
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
	var view application.ThreadChangeUndoRequest
	if !a.decodeThreadControlBody(writer, request, requestID, "Thread change undo", &view) {
		return
	}
	store, ok := a.store.(application.ThreadChangeUndoStore)
	if !ok {
		a.writeError(writer, requestID, apperror.New(apperror.CodeUnavailable, "Thread change undo storage is unavailable"), 0)
		return
	}
	result, err := application.NewThreadChangeUndoService(store, a.executionPermissionCapabilities).WithDrydocks(a.fileWorkspaceDrydocks).Undo(request.Context(), threadID, view)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	a.writeSuccessStatus(writer, requestID, result, nil, http.StatusOK)
}
