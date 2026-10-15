package httpapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
)

type chatGPTAuthControllerStub struct {
	actions []string
}

func (*chatGPTAuthControllerStub) Status(context.Context) (application.ChatGPTAuthStatus, error) {
	return application.ChatGPTAuthStatus{ProtocolVersion: application.ChatGPTAuthProtocolVersion,
		Available: true, StoreKind: "test", Accounts: []application.ChatGPTAccountStatus{},
		UsageURL: "https://chatgpt.com/settings/usage"}, nil
}
func (s *chatGPTAuthControllerStub) Change(ctx context.Context, action string, _ application.ChatGPTAuthRequest) (application.ChatGPTAuthStatus, error) {
	s.actions = append(s.actions, action)
	return s.Status(ctx)
}

func TestChatGPTHTTPAuthAndStrictControlBoundary(t *testing.T) {
	fixture := newAPIFixture(t)
	controller := &chatGPTAuthControllerStub{}
	api, err := New(fixture.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken,
		AppVersion: "chatgpt-test", ChatGPTAuthEnabled: true, ChatGPTAuthController: controller})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{ChatGPTAuthPath, ChatGPTAuthPath + "/login"} {
		method := http.MethodGet
		token := testAccessToken
		var body io.Reader
		if strings.HasSuffix(path, "/login") {
			method = http.MethodPost
			token = testControlToken
			body = strings.NewReader(`{}`)
		}
		response := performSessionMessageRequest(t, fixture.api, method, path,
			token, "", "application/json", body)
		assertAPIError(t, response, http.StatusNotFound, "NOT_FOUND")
	}
	const body = `{"version":"chatgpt_auth.v1","confirm":true}`
	for _, action := range []string{"login", "cancel", "logout", "models", "usage"} {
		path := ChatGPTAuthPath + "/" + action
		for _, token := range []string{"", testAccessToken} {
			response := performSessionMessageRequest(t, api, http.MethodPost, path, token, "", "application/json", strings.NewReader(body))
			assertAPIError(t, response, http.StatusUnauthorized, "POLICY_DENIED")
		}
		for _, invalid := range []string{
			`{"version":"chatgpt_auth.v1","confirm":true,"url":"https://evil.example"}`,
			`{"version":"chatgpt_auth.v1","confirm":true,"confirm":false}`,
			`{"version":"chatgpt_auth.v1","confirm":true} {}`,
		} {
			response := performSessionMessageRequest(t, api, http.MethodPost, path, testControlToken, "", "application/json", strings.NewReader(invalid))
			assertAPIError(t, response, http.StatusBadRequest, "INVALID_ARGUMENT")
		}
		var status application.ChatGPTAuthStatus
		decodeDataStatus(t, performSessionMessageRequest(t, api, http.MethodPost, path,
			testControlToken, "", "application/json", strings.NewReader(body)), http.StatusAccepted, &status)
	}
	if strings.Join(controller.actions, ",") != "login,cancel,logout,models,usage" {
		t.Fatalf("rejected requests reached controller: %v", controller.actions)
	}
	var status application.ChatGPTAuthStatus
	decodeDataStatus(t, performSessionMessageRequest(t, api, http.MethodGet, ChatGPTAuthPath,
		testAccessToken, "", "", nil), http.StatusOK, &status)
	if !status.Available || status.ProtocolVersion != application.ChatGPTAuthProtocolVersion {
		t.Fatal("status unavailable")
	}
	assertAPIError(t, performSessionMessageRequest(t, api, http.MethodGet, ChatGPTAuthPath+"?account_id=x",
		testAccessToken, "", "", nil), http.StatusBadRequest, "INVALID_ARGUMENT")
}

func TestChatGPTHTTPRequiresControlTokenAndController(t *testing.T) {
	fixture := newAPIFixture(t)
	for _, config := range []Config{
		{AccessToken: testAccessToken, ChatGPTAuthEnabled: true, ChatGPTAuthController: &chatGPTAuthControllerStub{}},
		{AccessToken: testAccessToken, ControlToken: testControlToken, ChatGPTAuthEnabled: true},
	} {
		if _, err := New(fixture.store, config); err == nil {
			t.Fatal("incomplete ChatGPT control configuration accepted")
		}
	}
}
