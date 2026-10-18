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
	ListOperations(context.Context, string) (domain.ThreadContextCompactionOperations, error)
	Recover(context.Context, domain.ThreadContextCompactionRecoveryRequest) (domain.ThreadContextCompactionOperationView, error)
}

const ThreadContextCompactionsPathTemplate = "/api/v1/threads/{thread_id}/context/compactions"
const ThreadContextCompactionRecoveryPathTemplate = "/api/v1/threads/{thread_id}/context/compactions/{operation_id}/recover"

func matchThreadContextCompactionRecoveryPath(path string) (threadID, operationID string, matched bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/threads/"), "/")
	if !strings.HasPrefix(path, "/api/v1/threads/") || len(parts) < 3 || parts[1] != "context" || parts[2] != "compactions" || parts[0] == "" {
		return "", "", false
	}
	if len(parts) == 3 {
		return parts[0], "", true
	}
	if len(parts) == 5 && parts[3] != "" && parts[4] == "recover" {
		return parts[0], parts[3], true
	}
	return "", "", false
}

func (a *API) serveThreadContextCompactionRecovery(w http.ResponseWriter, r *http.Request, requestID, threadID, operationID string) {
	if a.threadContextCompactionController == nil || !a.runExecutionEnabled {
		a.writeError(w, requestID, apperror.New(apperror.CodeNotFound, "Thread context compaction is unavailable"), http.StatusNotFound)
		return
	}
	if err := validatePathIdentity(threadID); err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	if err := rejectQuery(r.URL.Query()); err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	if operationID == "" {
		if !a.authorized(r, a.tokenHash) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="CyberAgent API"`)
			a.writeError(w, requestID, apperror.New(apperror.CodePolicyDenied, "valid bearer authorization is required"), http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "Compaction operations require GET"), http.StatusMethodNotAllowed)
			return
		}
		if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "read-only HTTP API requests cannot contain a body"), 0)
			return
		}
		view, err := a.threadContextCompactionController.ListOperations(r.Context(), threadID)
		if err != nil {
			a.writeError(w, requestID, err, 0)
			return
		}
		a.writeSuccess(w, requestID, view, nil)
		return
	}
	if !a.authorizeThreadControl(w, r, requestID) {
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "Compaction recovery requires POST"), http.StatusMethodNotAllowed)
		return
	}
	if err := validatePathIdentity(operationID); err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	var input domain.ThreadContextCompactionRecoveryRequest
	if !a.decodeThreadControlBody(w, r, requestID, "Thread context compaction recovery", &input) {
		return
	}
	input.ThreadID, input.OperationID, input.RequestedBy = threadID, operationID, "http_thread_operator"
	view, err := a.threadContextCompactionController.Recover(r.Context(), input)
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	// The immutable operation ID is the retry identity; recovery never resends
	// a provider request, even when the response to this control request is lost.
	a.writeSuccess(w, requestID, view, nil)
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
