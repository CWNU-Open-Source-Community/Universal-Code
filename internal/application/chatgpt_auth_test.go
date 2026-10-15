package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/chatgptauth"
	"cyberagent-workbench/internal/credential"
)

const chatGPTTestStart = "http://127.0.0.1:45555/auth/start/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type chatGPTClientStub struct {
	login func(context.Context, func(string)) (*chatgptauth.Auth, error)
}

func (c chatGPTClientStub) Login(ctx context.Context, _ chatgptauth.Store, _ string, _ bool, ready func(string)) (*chatgptauth.Auth, error) {
	return c.login(ctx, ready)
}
func (chatGPTClientStub) Credentials(context.Context, chatgptauth.Store, string) (*chatgptauth.Auth, error) {
	panic("unexpected credentials call")
}
func (chatGPTClientStub) Models(context.Context, *chatgptauth.Auth) ([]chatgptauth.Model, error) {
	panic("unexpected models call")
}
func (chatGPTClientStub) Logout(context.Context, chatgptauth.Store, string) error {
	panic("unexpected logout call")
}

func chatGPTRequest() ChatGPTAuthRequest {
	return ChatGPTAuthRequest{Version: ChatGPTAuthProtocolVersion, Confirm: true}
}

func TestChatGPTLoginSurvivesRequestAndBrowserLifetime(t *testing.T) {
	browserStarted, browserExit := make(chan struct{}), make(chan struct{})
	defer close(browserExit)
	cancelled := make(chan struct{})
	client := chatGPTClientStub{login: func(ctx context.Context, ready func(string)) (*chatgptauth.Auth, error) {
		ready(chatGPTTestStart)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	}}
	service := NewChatGPTAuthService(client, chatgptauth.Store{Dir: t.TempDir(), KeyStore: credential.NewMemoryStore()}, nil, nil,
		func(target string) error {
			if target != chatGPTTestStart {
				t.Errorf("unexpected browser target %q", target)
			}
			close(browserStarted)
			<-browserExit
			return nil
		})
	t.Cleanup(service.Close)
	requestCtx, endRequest := context.WithCancel(t.Context())
	status, err := service.Change(requestCtx, "login", chatGPTRequest())
	if err != nil || status.Login == nil || status.Login.State != "waiting" || status.Login.StartURL != chatGPTTestStart {
		t.Fatalf("start status=%#v err=%v", status, err)
	}
	endRequest()
	select {
	case <-browserStarted:
	case <-time.After(time.Second):
		t.Fatal("browser was not launched")
	}
	select {
	case <-cancelled:
		t.Fatal("ending the request cancelled the callback listener")
	default:
	}
	if _, err := service.Change(t.Context(), "login", chatGPTRequest()); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("duplicate login was accepted: %v", err)
	}
	cancel := chatGPTRequest()
	cancel.AttemptID = "stale"
	if _, err := service.Change(t.Context(), "cancel", cancel); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale cancellation was accepted: %v", err)
	}
	cancel.AttemptID = status.Login.ID
	cancelCtx, endCancel := context.WithTimeout(t.Context(), time.Second)
	defer endCancel()
	status, err = service.Change(cancelCtx, "cancel", cancel)
	if err != nil || status.Login.State != "cancelled" || status.Login.StartURL != "" {
		t.Fatalf("cancel waited for browser or retained URL: %#v err=%v", status, err)
	}
}

func TestChatGPTLoginFailureNeverExposesRawError(t *testing.T) {
	const secret = "synthetic-refresh-token-never-return"
	client := chatGPTClientStub{login: func(context.Context, func(string)) (*chatgptauth.Auth, error) {
		return nil, errors.New("issuer echoed " + secret)
	}}
	service := NewChatGPTAuthService(client, chatgptauth.Store{Dir: t.TempDir(), KeyStore: credential.NewMemoryStore()}, nil, nil, nil)
	t.Cleanup(service.Close)
	status, err := service.Change(t.Context(), "login", chatGPTRequest())
	if err != nil || status.Login.State != "failed" || status.Login.Message == "" {
		t.Fatalf("failure status=%#v err=%v", status, err)
	}
	raw, _ := json.Marshal(status)
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "access_token") {
		t.Fatalf("status leaked authentication response: %s", raw)
	}
}

func TestChatGPTCloseCancelsCallbackAndRejectsNewLogin(t *testing.T) {
	client := chatGPTClientStub{login: func(ctx context.Context, ready func(string)) (*chatgptauth.Auth, error) {
		ready(chatGPTTestStart)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	service := NewChatGPTAuthService(client, chatgptauth.Store{Dir: t.TempDir(), KeyStore: credential.NewMemoryStore()}, nil, nil, func(string) error { return nil })
	if _, err := service.Change(t.Context(), "login", chatGPTRequest()); err != nil {
		t.Fatal(err)
	}
	service.Close()
	if _, err := service.Change(t.Context(), "login", chatGPTRequest()); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("closed service accepted new login: %v", err)
	}
}

func TestChatGPTUsageAndOperationValidation(t *testing.T) {
	var target string
	service := NewChatGPTAuthService(chatGPTClientStub{}, chatgptauth.Store{Dir: t.TempDir(), KeyStore: credential.NewMemoryStore()}, nil, nil,
		func(value string) error { target = value; return nil })
	for _, test := range []struct {
		action  string
		request ChatGPTAuthRequest
	}{
		{"usage", ChatGPTAuthRequest{Version: ChatGPTAuthProtocolVersion}},
		{"usage", ChatGPTAuthRequest{Version: "bad", Confirm: true}},
		{"usage", ChatGPTAuthRequest{Version: ChatGPTAuthProtocolVersion, Confirm: true, AccountID: "url"}},
		{"login", ChatGPTAuthRequest{Version: ChatGPTAuthProtocolVersion, Confirm: true, AttemptID: "old"}},
		{"logout", chatGPTRequest()},
		{"models", chatGPTRequest()},
	} {
		if _, err := service.Change(t.Context(), test.action, test.request); err == nil {
			t.Fatalf("invalid %s request accepted", test.action)
		}
	}
	if target != "" {
		t.Fatal("invalid request opened a browser")
	}
	if _, err := service.Change(t.Context(), "usage", chatGPTRequest()); err != nil || target != chatgptauth.UsageURL {
		t.Fatalf("usage target=%q err=%v", target, err)
	}
}

func TestValidChatGPTStartURL(t *testing.T) {
	if !ValidChatGPTStartURL(chatGPTTestStart) {
		t.Fatal("valid loopback entry rejected")
	}
	for _, target := range []string{
		strings.Replace(chatGPTTestStart, "127.0.0.1", "localhost", 1),
		strings.Replace(chatGPTTestStart, "http:", "https:", 1),
		strings.Replace(chatGPTTestStart, ":45555", ":70000", 1),
		strings.Replace(chatGPTTestStart, ":45555", "", 1),
		strings.Replace(chatGPTTestStart, "127.0.0.1", "x@127.0.0.1", 1),
		chatGPTTestStart + "?state=secret", chatGPTTestStart + "#fragment",
		chatGPTTestStart + "/", "https://auth.openai.com/api/accounts/authorize?state=secret",
	} {
		if ValidChatGPTStartURL(target) {
			t.Errorf("unsafe browser target accepted: %q", target)
		}
	}
}
