package httpapi

import (
	"net/http"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
)

const ThreadChangesPathTemplate = "/api/v1/threads/{thread_id}/changes"

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
	return result, nil, err
}
