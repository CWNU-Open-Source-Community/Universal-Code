package httpapi

import (
	"context"
	"net/http"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

const ThreadContextCompactionPathTemplate = "/api/v1/threads/{thread_id}/context/compact"

type ThreadContextCompactionController interface {
	Compact(context.Context, domain.ThreadContextCompactionRequest) (domain.ThreadContextCompactionResult, error)
}
type ThreadContextCompactionRequestView struct {
	Version       string `json:"version"`
	ExpectedRunID string `json:"expected_run_id"`
	Focus         string `json:"focus,omitempty"`
}

func matchThreadContextCompactionPath(path string) (string, bool) {
	const prefix = "/api/v1/threads/"
	const suffix = "/context/compact"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	return id, id != "" && !strings.Contains(id, "/")
}
func (a *API) serveThreadContextCompaction(w http.ResponseWriter, r *http.Request, requestID, threadID string) {
	if a.threadContextCompactionController == nil || !a.runExecutionEnabled {
		a.writeError(w, requestID, apperror.New(apperror.CodeNotFound, "Thread context compaction is unavailable"), http.StatusNotFound)
		return
	}
	if !a.authorizeThreadControl(w, r, requestID) {
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "Thread context compaction requires POST"), http.StatusMethodNotAllowed)
		return
	}
	if err := validatePathIdentity(threadID); err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	key, err := sessionControlIdempotencyKey(r.Header, "Thread context compaction")
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	var input ThreadContextCompactionRequestView
	if !a.decodeThreadControlBody(w, r, requestID, "Thread context compaction", &input) {
		return
	}
	result, err := a.threadContextCompactionController.Compact(r.Context(), domain.ThreadContextCompactionRequest{
		Version: input.Version, ThreadID: threadID, ExpectedRunID: input.ExpectedRunID, Focus: input.Focus, OperationKey: key, RequestedBy: "http_thread_operator"})
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	a.writeSuccess(w, requestID, result, nil)
}
