package httpapi

import (
	"context"
	"net/http"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
)

const ChatGPTAuthPath = "/api/v1/models/chatgpt"

type ChatGPTAuthController interface {
	Status(context.Context) (application.ChatGPTAuthStatus, error)
	Change(context.Context, string, application.ChatGPTAuthRequest) (application.ChatGPTAuthStatus, error)
}

func matchChatGPTAuthControlPath(path string) (string, bool) {
	action := strings.TrimPrefix(path, ChatGPTAuthPath+"/")
	if action == path {
		return "", false
	}
	switch action {
	case "login", "cancel", "logout", "models", "usage":
		return action, true
	}
	return "", false
}

func (a *API) chatGPTAuthStatus(request *http.Request) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	if !a.chatGPTAuthEnabled || a.chatGPTAuthController == nil {
		return nil, nil, apperror.New(apperror.CodeNotFound, "ChatGPT sign-in is unavailable")
	}
	status, err := a.chatGPTAuthController.Status(request.Context())
	return status, nil, err
}

func (a *API) serveChatGPTAuthControl(writer http.ResponseWriter, request *http.Request, requestID, action string) {
	const label = "ChatGPT sign-in"
	if !a.authorizeRunOperation(writer, request, requestID, a.chatGPTAuthEnabled, label) {
		return
	}
	body, err := readStrictControlBody(request, label)
	if err != nil {
		a.writeError(writer, requestID, err, runOperationErrorStatus(err))
		return
	}
	var value application.ChatGPTAuthRequest
	if err := decodeStrictRunOperation(body, &value, label); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	status, err := a.chatGPTAuthController.Change(request.Context(), action, value)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	a.writeSuccessStatus(writer, requestID, status, nil, http.StatusAccepted)
}
