package httpapi

import (
	"context"
	"net/http"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

type ThreadNameRequestView struct {
	Title string `json:"title"`
}

type ThreadPinRequestView struct {
	Pinned *bool `json:"pinned"`
}

type threadMetadataStore interface {
	RenameThread(context.Context, string, string) (domain.Thread, error)
	PinThread(context.Context, string, bool) (domain.Thread, error)
}

func matchThreadMetadataPath(path string) (string, string, bool) {
	if !strings.HasPrefix(path, ThreadCollectionPath+"/") {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(path, ThreadCollectionPath+"/"), "/")
	if len(parts) != 2 || !domain.ValidAgentID(parts[0]) || (parts[1] != "name" && parts[1] != "pin") {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func (a *API) serveThreadMetadata(w http.ResponseWriter, r *http.Request, requestID, id, action string) {
	if !a.authorizeThreadControl(w, r, requestID) {
		return
	}
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", http.MethodPut)
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "Conversation metadata only supports PUT"), http.StatusMethodNotAllowed)
		return
	}
	if err := rejectQuery(r.URL.Query()); err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	st, ok := a.store.(threadMetadataStore)
	if !ok {
		a.writeError(w, requestID, apperror.New(apperror.CodeUnavailable, "Conversation metadata is unavailable"), 0)
		return
	}
	var thread domain.Thread
	var err error
	if action == "name" {
		var body ThreadNameRequestView
		if !a.decodeThreadControlBody(w, r, requestID, "conversation title", &body) {
			return
		}
		thread, err = st.RenameThread(r.Context(), id, body.Title)
	} else {
		var body ThreadPinRequestView
		if !a.decodeThreadControlBody(w, r, requestID, "conversation pin", &body) {
			return
		}
		if body.Pinned == nil {
			a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "pinned must be true or false"), 0)
			return
		}
		thread, err = st.PinThread(r.Context(), id, *body.Pinned)
	}
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	// Use the same live/durable projection as list reads.
	views := a.threadListViews(r.Context(), []domain.Thread{thread})
	a.writeSuccess(w, requestID, views[0], nil)
}
