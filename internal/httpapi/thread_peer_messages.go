package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

const ThreadPeerMessagesPathTemplate = "/api/v1/threads/{thread_id}/peer-messages"
const ThreadPeerTargetsPathTemplate = "/api/v1/threads/{thread_id}/peer-targets"

type ThreadPeerMessageRequestView struct {
	Version        string `json:"version"`
	TargetThreadID string `json:"target_thread_id"`
	Content        string `json:"content"`
	ReplyTo        string `json:"reply_to,omitempty"`
}

type ThreadPeerMessagesView struct {
	Version  string                     `json:"version"`
	ThreadID string                     `json:"thread_id"`
	Items    []domain.ThreadPeerMessage `json:"items"`
	HasMore  bool                       `json:"has_more"`
}

func matchThreadPeerPath(path string) (string, string, bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/"), "/")
	if len(parts) == 3 && parts[0] == "threads" && parts[1] != "" && (parts[2] == "peer-messages" || parts[2] == "peer-targets") {
		return parts[1], parts[2], true
	}
	return "", "", false
}

func (a *API) serveThreadPeerMessages(w http.ResponseWriter, r *http.Request, requestID, threadID, action string) {
	if a.threadPeerMessages == nil {
		a.writeError(w, requestID, apperror.New(apperror.CodeNotFound, "Peer messaging is unavailable"), 0)
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
	if r.Method == http.MethodGet {
		if !a.authorized(r, a.tokenHash) {
			a.writeError(w, requestID, apperror.New(apperror.CodePolicyDenied, "valid bearer authorization is required"), http.StatusUnauthorized)
			return
		}
		if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "Peer message reads cannot contain a body"), 0)
			return
		}
		if action == "peer-targets" {
			result, err := a.threadPeerMessages.Targets(r.Context(), threadID)
			if err != nil {
				a.writeError(w, requestID, err, 0)
				return
			}
			a.writeSuccess(w, requestID, result, nil)
			return
		}
		items, err := a.threadPeerMessages.Messages(r.Context(), threadID)
		if err != nil {
			a.writeError(w, requestID, err, 0)
			return
		}
		a.writeSuccess(w, requestID, ThreadPeerMessagesView{Version: domain.ThreadPeerMessageVersion, ThreadID: threadID, Items: items, HasMore: len(items) == 200}, nil)
		return
	}
	if action != "peer-messages" || r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "Peer endpoint method is invalid"), http.StatusMethodNotAllowed)
		return
	}
	if !a.runExecutionEnabled || !a.sessionMessageEnabled {
		a.writeError(w, requestID, apperror.New(apperror.CodeNotFound, "Peer message execution is disabled"), http.StatusNotFound)
		return
	}
	if !a.authorizeThreadControl(w, r, requestID) {
		return
	}
	key, err := sessionControlIdempotencyKey(r.Header, "Thread peer message")
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	if err := validateJSONContentType(r.Header); err != nil {
		a.writeError(w, requestID, err, http.StatusUnsupportedMediaType)
		return
	}
	body, err := readBoundedRequestBody(r, domain.MaxOperatorSteeringContentBytes*6+4096)
	if err == nil && !utf8.Valid(body) {
		err = apperror.New(apperror.CodeInvalidArgument, "Peer message body must be valid UTF-8")
	}
	if err == nil {
		err = rejectDuplicateJSONObjectFields(body, "Thread peer message")
	}
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	var view ThreadPeerMessageRequestView
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&view); err == nil {
		err = ensureJSONEOF(decoder)
	}
	if err != nil || view.Version != domain.ThreadPeerMessageVersion {
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "Peer message requires its version, recipient and plain-text content"), 0)
		return
	}
	result, err := a.threadPeerMessages.Send(r.Context(), domain.SendThreadPeerMessageRequest{SourceThreadID: threadID, TargetThreadID: view.TargetThreadID, Content: view.Content, ReplyTo: view.ReplyTo, OperationKey: key, RequestedBy: "http_thread_operator"})
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	a.writeSuccessStatus(w, requestID, result, nil, http.StatusAccepted)
}
